// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/llmloop"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/session"
)

func configureCompletionAgent(a *Agent, rounds int) {
	a.args.BehaviorPrompt = behaviorPrompt
	a.args.Template.MaxReviewRounds = 3
	a.args.Template.MaxToolRequestTimes = rounds
	// Agent.New copies the template into the runner. Configure both test layers
	// before dispatch so the fixture actually reaches the requested boundary.
	a.runner = llmloop.NewRunner(llmloop.Deps{
		LLMClient: a.args.LLMClient, Model: a.args.Model, Template: a.args.Template,
		Tools: a.args.Tools, MainToolDefs: a.args.MainToolDefs,
		CommentCollector: a.args.CommentCollector, Session: a.session,
		DiffLookup: a.findDiff, AllDiffs: a.allDiffs, NewRequestMeta: a.newRequestMeta,
		MaxTokensBudget: a.args.MaxTokensBudget,
	})
}

func TestMissingSourceCompletionFeedback(t *testing.T) {
	reports := behaviorFixtures()
	reports[0].Contracts[0].Sources = append(reports[0].Contracts[0].Sources,
		BehaviorContractSource{ID: "test-1", Kind: "test", Citation: "good_test.go:1", Statement: "Get returns the stored value."})
	_, comments, err := parseBehaviorReport(reportArgs(reports[2]), "adjudication", map[string]bool{"good.go": true}, reports[:2])
	if err == nil || !strings.Contains(err.Error(), "test-1") || len(comments) != 0 {
		t.Fatalf("missing source feedback=%v comments=%v", err, comments)
	}

	client := &behaviorClient{reports: append(reports, reports[2])}
	a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}, nil, client)
	configureCompletionAgent(a, 1)
	_, _ = a.dispatchSubtasks(context.Background())
	manifest := finishManifestFlow(t, a)
	if manifest.TerminalState != session.StateFailed || !strings.Contains(a.BehaviorReports()[2].Error, "test-1") || client.calls != 4 {
		t.Fatalf("failure detail lost: manifest=%+v reports=%+v calls=%d", manifest, a.BehaviorReports(), client.calls)
	}
}

func TestBasisFeedbackListsOriginalSourceChoices(t *testing.T) {
	for _, assessments := range [][]BehaviorSourceAssessment{
		{{SourceID: "good.go", Analysis: "A citation is not the original ID."}},
		{{SourceID: "src1", Analysis: "checked"}, {SourceID: "src1", Analysis: "repeated"}},
		{{SourceID: "src1", Analysis: ""}},
	} {
		reports := behaviorFixtures()
		reports[0].Contracts[0].Sources = append(reports[0].Contracts[0].Sources,
			BehaviorContractSource{ID: "test-1", Kind: "test", Citation: "good_test.go:1", Statement: "Get preserves its stored value."})
		reports[2].BasisReviews[0].SourceAssessments = assessments
		_, _, err := parseBehaviorReport(reportArgs(reports[2]), "adjudication", map[string]bool{"good.go": true}, reports[:2])
		if err == nil || !strings.Contains(err.Error(), "original source_ids: [src1 test-1]") || !strings.Contains(err.Error(), "source_id \""+assessments[0].SourceID+"\"") {
			t.Fatalf("source correction lacks the bad ID or original choices: %v", err)
		}
	}
}

func TestBasisFeedbackCollectsAllOmissions(t *testing.T) {
	reports := behaviorFixtures()
	contracts := map[string]BehaviorContract{}
	var reviews []BehaviorBasisReview
	for _, id := range []string{"c2", "c1", "c4", "c3"} {
		contract := reports[0].Contracts[0]
		contract.ID = id
		contract.Sources = append([]BehaviorContractSource(nil), contract.Sources...)
		contract.Sources = append(contract.Sources,
			BehaviorContractSource{ID: id + "-test", Kind: "test", Citation: "good_test.go:1", Statement: "Get preserves its stored value."})
		contracts[id] = contract
		if id == "c1" || id == "c2" {
			review := reports[2].BasisReviews[0]
			review.ContractID = id
			reviews = append(reviews, review)
		}
	}
	verdicts, err := validateBehaviorBasisReviews(reviews, contracts)
	if err == nil || verdicts != nil {
		t.Fatalf("incomplete basis accepted: verdicts=%v error=%v", verdicts, err)
	}
	for _, missing := range []string{"c1-test", "c2-test", "c3", "c4"} {
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("correction feedback omits %s: %v", missing, err)
		}
	}
	if !strings.Contains(err.Error(), "contract_ids [c3 c4]") {
		t.Errorf("missing contracts need stable IDs in feedback: %v", err)
	}
}

type completionFailureClient struct {
	behaviorClient
	failure error
}

func (c *completionFailureClient) CompletionsWithCtx(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	response, err := c.behaviorClient.CompletionsWithCtx(ctx, req)
	if err != nil {
		return nil, c.failure
	}
	return response, nil
}

func TestBehaviorCorrectionFailureKeepsEvidenceAndClassification(t *testing.T) {
	for _, tc := range []struct {
		name    string
		failure error
		class   session.FailureClass
	}{
		{"provider", errors.New("private-provider-payload"), session.FailureProvider},
		{"timeout", context.DeadlineExceeded, session.FailureTimeout},
		{"cancelled", context.Canceled, session.FailureCancelled},
	} {
		for _, rounds := range []int{1, 2} {
			t.Run(tc.name+map[int]string{1: "/grace", 2: "/ordinary"}[rounds], func(t *testing.T) {
				reports := behaviorFixtures()
				reports[0].Contracts[0].Sources = append(reports[0].Contracts[0].Sources,
					BehaviorContractSource{ID: "test-1", Kind: "test", Citation: "good_test.go:1", Statement: "Get preserves its stored value."})
				client := &completionFailureClient{behaviorClient: behaviorClient{reports: reports, failAt: 4}, failure: tc.failure}
				a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}, nil, client)
				configureCompletionAgent(a, rounds)
				comments, _ := a.dispatchSubtasks(context.Background())
				manifest := finishManifestFlow(t, a)
				if manifest.TerminalState != session.StateFailed || len(manifest.Coverage.Failed) != 1 || len(comments) != 0 || client.calls != 4 {
					t.Fatalf("failed correction state lost: manifest=%+v comments=%v requests=%d", manifest, comments, client.calls)
				}
				if got := manifest.Coverage.Failed[0].Classification; got != tc.class {
					t.Errorf("failure classification=%s, want %s", got, tc.class)
				}
				detail := a.BehaviorReports()[2].Error
				if !strings.Contains(detail, "test-1") || strings.Contains(detail, "private-provider-payload") {
					t.Errorf("public report lost validation evidence or exposed provider details: %s", detail)
				}
			})
		}
	}
}

func TestBehaviorAcceptsValidGraceReports(t *testing.T) {
	client := &behaviorClient{reports: behaviorFixtures()}
	a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}, nil, client)
	configureCompletionAgent(a, 0)
	_, _ = a.dispatchSubtasks(context.Background())
	manifest := finishManifestFlow(t, a)
	if manifest.TerminalState != session.StateComplete || client.calls != 3 {
		t.Fatalf("valid grace reports discarded: manifest=%+v calls=%d", manifest, client.calls)
	}
}

type behaviorBudgetClient struct{ behaviorClient }

func (c *behaviorBudgetClient) CompletionsWithCtx(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	response, err := c.behaviorClient.CompletionsWithCtx(ctx, req)
	if response != nil {
		response.Usage = &llm.UsageInfo{PromptTokens: 50, CompletionTokens: 10}
	}
	return response, err
}

func TestBehaviorBudgetStopsUnstartedStages(t *testing.T) {
	client := &behaviorBudgetClient{behaviorClient{reports: behaviorFixtures()}}
	diffs := []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}
	a := newManifestFlowAgentWithClient(t, diffs, nil, client)
	a.args.MaxTokensBudget = 50
	configureCompletionAgent(a, 5)
	completed, stop, err := a.executeBehaviorReview(context.Background(), FileGroup{Diffs: diffs})
	if completed || err != nil || stop == nil || stop.class != session.FailureBudget || client.calls != 1 || !a.budgetExceeded.Load() {
		t.Fatalf("completion=%v stop=%+v error=%v calls=%d", completed, stop, err, client.calls)
	}
	reports := a.BehaviorReports()
	if len(reports) != 2 || !reports[0].Complete || reports[1].Complete || reports[1].Stage != "counterexamples" {
		t.Fatalf("stage completion was lost or invented: %+v", reports)
	}
}

// The optional fixture holds public reports from the sealed 2026-09-22 run.
// Its repair is authored for deterministic protocol testing; it is not a new
// model judgment or evidence of discovery quality.
