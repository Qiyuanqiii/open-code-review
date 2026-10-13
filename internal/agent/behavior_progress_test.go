// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/session"
	"github.com/alibaba/open-code-review/internal/tool"
)

func progressFixtures() []BehaviorReport {
	r := behaviorFixtures()
	c := r[0].Contracts[0]
	c.ID, c.Path = "c2", "other.go"
	r[0].Contracts = append(r[0].Contracts, c)
	b := r[2].BasisReviews[0]
	b.ContractID = "c2"
	r[2].BasisReviews = append(r[2].BasisReviews, b)
	for _, i := range []int{1, 2} {
		check := r[i].Checks[0]
		check.ID, check.ContractID = "t2", "c2"
		r[i].Checks = append(r[i].Checks, check)
	}
	return r
}

func behaviorPatch(fields map[string]any) map[string]any { return map[string]any{"report": fields} }

func brokenProgressSubmission(r BehaviorReport) (map[string]any, map[string]any) {
	good := reportArgs(r)["report"].(map[string]any)
	bad := reportArgs(r)["report"].(map[string]any)
	field, attribute := "checks", "analysis"
	if r.Stage == "contracts" {
		field, attribute = "contracts", "behavior"
	}
	// An item-level JSON type error must not discard its valid sibling.
	bad[field].([]any)[1].(map[string]any)[attribute] = 17
	return behaviorPatch(bad), behaviorPatch(map[string]any{field: []any{good[field].([]any)[1]}})
}

func TestBehaviorProgressRepairsOnlyBadEntry(t *testing.T) {
	for i, stage := range behaviorStages {
		t.Run(stage, func(t *testing.T) {
			r := progressFixtures()
			p := newBehaviorProgress(stage, map[string]bool{"good.go": true, "other.go": true}, r[:i])
			bad, patch := brokenProgressSubmission(r[i])
			saved, comments, err := p.submit(bad)
			if err == nil || saved.Complete || len(comments) != 0 || len(saved.Pending) == 0 {
				t.Fatalf("bad sibling passed completion: %+v %v", saved, err)
			}
			if (i == 0 && len(saved.Contracts) != 1) || (i > 0 && len(saved.Checks) != 1) {
				t.Fatalf("valid entry was lost: %+v", saved)
			}
			fixed, _, err := p.submit(patch)
			if err != nil || !fixed.Complete || len(fixed.Pending) != 0 {
				t.Fatalf("single-entry repair did not complete: %+v %v", fixed, err)
			}
			if i == 0 {
				if !reflect.DeepEqual(fixed.Contracts, r[0].Contracts) || !reflect.DeepEqual(saved.Contracts[0], fixed.Contracts[0]) {
					t.Fatal("contract repair lost previously saved work")
				}
			} else if len(fixed.Checks) != 2 || !reflect.DeepEqual(saved.Checks[0], fixed.Checks[0]) {
				t.Fatal("check repair lost previously saved work")
			}
		})
	}
}

func TestBehaviorProgressMetadataOnlyRepair(t *testing.T) {
	for _, field := range []string{"complete", "unknowns", "limitations"} {
		for _, malformed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/malformed=%v", field, malformed), func(t *testing.T) {
				r := behaviorFixtures()
				p := newBehaviorProgress("adjudication", map[string]bool{"good.go": true}, r[:2])
				args := reportArgs(r[2])
				fields := args["report"].(map[string]any)
				value := fields[field]
				delete(fields, field)
				if malformed {
					fields[field] = "wrong type"
				}
				partial, _, err := p.submit(args)
				if err == nil || len(partial.Checks) != 1 || len(partial.BasisReviews) != 1 {
					t.Fatalf("header failure discarded entries: %+v %v", partial, err)
				}
				fixed, _, err := p.submit(behaviorPatch(map[string]any{field: value}))
				if err != nil || !fixed.Complete {
					t.Fatalf("header-only correction failed: %+v %v", fixed, err)
				}
			})
		}
	}
}

func TestBehaviorProgressInvalidReplacementAndDuplicates(t *testing.T) {
	r := behaviorFixtures()
	p := newBehaviorProgress("counterexamples", map[string]bool{"good.go": true}, r[:1])
	args := reportArgs(r[1])
	delete(args["report"].(map[string]any), "complete")
	first, _, _ := p.submit(args)
	// Identical replay is idempotent before completion is requested.
	again, _, _ := p.submit(args)
	if !reflect.DeepEqual(first, again) {
		t.Fatal("an identical submission changed saved progress")
	}
	broken := r[1].Checks[0]
	broken.Analysis = ""
	for _, checks := range [][]BehaviorCheck{{broken}, {r[1].Checks[0], r[1].Checks[0]}} {
		partial, _, err := p.submit(behaviorPatch(map[string]any{"checks": checks, "complete": true}))
		if err == nil || partial.Complete || !reflect.DeepEqual(partial.Checks, first.Checks) {
			t.Fatalf("rejected replacement overwrote accepted work: %+v %v", partial, err)
		}
		if _, _, err := p.submit(behaviorPatch(map[string]any{"checks": []any{}})); err == nil {
			t.Fatal("an empty patch erased a pending correction")
		}
		fixed, _, err := p.submit(behaviorPatch(map[string]any{"checks": r[1].Checks}))
		if err != nil || !fixed.Complete || len(fixed.Checks) != 1 {
			t.Fatalf("replacement repair failed: %+v %v", fixed, err)
		}
	}
}

func TestBehaviorProgressRevalidatesDependentDecision(t *testing.T) {
	r := behaviorFixtures()
	r[2].Checks[0].Verdict, r[2].Checks[0].Refutation = "confirmed", nil
	r[2].Checks[0].Finding = &model.LlmComment{Path: "good.go", Content: "value is lost", ExistingCode: "value", Severity: "medium", Category: "bug"}
	p := newBehaviorProgress("adjudication", map[string]bool{"good.go": true}, r[:2])
	// A locally valid decision can arrive before its basis. It cannot publish.
	partial, comments, err := p.submit(behaviorPatch(map[string]any{"checks": r[2].Checks, "unknowns": []string{}, "limitations": []string{}}))
	if err == nil || len(partial.Checks) != 1 || len(comments) != 0 {
		t.Fatalf("decision awaiting basis was lost or published: %+v %v", partial, err)
	}
	_, _, _ = p.submit(behaviorPatch(map[string]any{"basis_reviews": r[2].BasisReviews}))
	unsupported := r[2].BasisReviews[0]
	unsupported.Verdict = "unsupported"
	partial, comments, err = p.submit(behaviorPatch(map[string]any{"basis_reviews": []BehaviorBasisReview{unsupported}, "complete": true}))
	if err == nil || partial.Complete || len(comments) != 0 || !strings.Contains(err.Error(), "only confirm a supported obligation") {
		t.Fatalf("stale confirmation survived basis change: %+v %v", partial, err)
	}
	// Correcting only the basis revalidates the saved decision.
	fixed, comments, err := p.submit(behaviorPatch(map[string]any{"basis_reviews": r[2].BasisReviews}))
	if err != nil || !fixed.Complete || len(comments) != 1 {
		t.Fatalf("basis-only correction could not use saved decision: %+v %v", fixed, err)
	}
	// A malformed replacement retains the prior basis for inspection, but it
	// cannot authorize publication, even with an explicit incomplete closure.
	unsupported.SourceAssessments = nil
	partial, comments, err = p.submit(behaviorPatch(map[string]any{"basis_reviews": []BehaviorBasisReview{unsupported}, "complete": false}))
	if err != nil || partial.Complete || len(comments) != 0 || len(partial.Pending) == 0 || partial.BasisReviews[0].Verdict != "supported" {
		t.Fatalf("invalid replacement authorized the old confirmation: %+v %v", partial, err)
	}
}

func TestBehaviorProgressEnvelopeRecovery(t *testing.T) {
	r := behaviorFixtures()
	oversized := make([]BehaviorCheck, 33)
	for _, bad := range []map[string]any{
		{"report": "malformed"},
		{"report": make(chan int)},
		behaviorPatch(map[string]any{"checks": nil}),
		behaviorPatch(map[string]any{"checks": "malformed"}),
		behaviorPatch(map[string]any{"checks": oversized}),
		behaviorPatch(map[string]any{"checks": []any{map[string]any{"analysis": "missing ID"}}}),
		behaviorPatch(map[string]any{"stage": "contracts"}),
		behaviorPatch(map[string]any{"contracts": r[0].Contracts}),
		behaviorPatch(map[string]any{"basis_reviews": r[2].BasisReviews}),
	} {
		p := newBehaviorProgress("counterexamples", map[string]bool{"good.go": true}, r[:1])
		first := reportArgs(r[1])
		delete(first["report"].(map[string]any), "complete")
		saved, _, _ := p.submit(first)
		partial, _, err := p.submit(bad)
		if err == nil || !reflect.DeepEqual(partial.Checks, saved.Checks) {
			t.Fatalf("malformed patch discarded progress: %+v %v", partial, err)
		}
		fixed, _, err := p.submit(behaviorPatch(map[string]any{"checks": r[1].Checks, "complete": true}))
		if err != nil || !fixed.Complete || len(fixed.Checks) != 1 {
			t.Fatalf("envelope correction failed: %+v %v", fixed, err)
		}
	}
}

func TestBehaviorProgressAccumulatedCapacity(t *testing.T) {
	r := behaviorFixtures()
	for _, stage := range []string{"contracts", "counterexamples"} {
		limit, field := 32, "checks"
		var previous []BehaviorReport
		entry := func(id string) any {
			c := r[1].Checks[0]
			c.ID = id
			return c
		}
		if stage == "contracts" {
			limit, field = 16, "contracts"
			entry = func(id string) any {
				c := r[0].Contracts[0]
				c.ID = id
				return c
			}
		} else {
			previous = r[:1]
		}
		p := newBehaviorProgress(stage, map[string]bool{"good.go": true}, previous)
		for i := range limit {
			_, _, _ = p.submit(behaviorPatch(map[string]any{field: []any{entry(fmt.Sprintf("id-%d", i))}}))
		}
		full := p.snapshot()
		partial, _, err := p.submit(behaviorPatch(map[string]any{field: []any{entry("overflow")}, "complete": true, "unknowns": []string{}, "limitations": []string{}}))
		if err == nil || partial.Complete || len(partial.Contracts)+len(partial.Checks) != limit || !reflect.DeepEqual(full.Contracts, partial.Contracts) || !reflect.DeepEqual(full.Checks, partial.Checks) {
			t.Fatalf("accumulated limit bypassed or progress lost: %+v %v", partial, err)
		}
		// Capacity exhaustion remains visible on explicit incomplete closure.
		partial, _, err = p.submit(behaviorPatch(map[string]any{"complete": false}))
		if err != nil || partial.Complete || len(partial.Pending) == 0 {
			t.Fatalf("capacity stop changed to success: %+v %v", partial, err)
		}
	}
}

func TestBehaviorProgressNamedErrorRequiresSameID(t *testing.T) {
	r := progressFixtures()
	p := newBehaviorProgress("counterexamples", map[string]bool{"good.go": true, "other.go": true}, r[:1])
	bad, patch := brokenProgressSubmission(r[1])
	_, _, _ = p.submit(bad)
	partial, _, err := p.submit(behaviorPatch(map[string]any{"checks": r[1].Checks[:1]}))
	if err == nil || !strings.Contains(err.Error(), "checks[t2]") || partial.Complete {
		t.Fatalf("an unrelated valid patch erased t2's error: %+v %v", partial, err)
	}
	fixed, _, err := p.submit(patch)
	if err != nil || !fixed.Complete {
		t.Fatalf("same-ID repair failed: %+v %v", fixed, err)
	}
}

func TestBehaviorProgressUnknownIdentityRecovery(t *testing.T) {
	r := behaviorFixtures()
	p := newBehaviorProgress("adjudication", map[string]bool{"good.go": true}, r[:2])
	bad := reportArgs(r[2])
	bad["report"].(map[string]any)["checks"].([]any)[0].(map[string]any)["id"] = "invented"
	partial, _, err := p.submit(bad)
	if err == nil || len(partial.Checks) != 0 || !strings.Contains(err.Error(), "t1") {
		t.Fatalf("invented identity accepted or feedback lost: %+v %v", partial, err)
	}
	if _, _, err := p.submit(behaviorPatch(map[string]any{"complete": true})); err == nil {
		t.Fatal("completion declaration cleared the identity error")
	}
	fixed, _, err := p.submit(behaviorPatch(map[string]any{"checks": r[2].Checks}))
	if err != nil || !fixed.Complete || fixed.Checks[0].ContractID != "c1" {
		t.Fatalf("valid-ID correction failed: %+v %v", fixed, err)
	}
}

type progressStopClient struct {
	behaviorSubmissionClient
	stopAt  int
	failure error
	spendAt int
}

func (c *progressStopClient) CompletionsWithCtx(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if c.calls+1 == c.stopAt {
		c.calls++
		return nil, c.failure
	}
	response, err := c.behaviorSubmissionClient.CompletionsWithCtx(ctx, req)
	if response != nil && c.calls == c.spendAt {
		response.Usage = &llm.UsageInfo{PromptTokens: 2000000, CompletionTokens: 10}
	}
	return response, err
}

func TestBehaviorProgressStopsKeepAcceptedEntries(t *testing.T) {
	for i, stage := range behaviorStages {
		for _, stop := range []string{"provider", "cancelled", "rounds", "tokens", "declared incomplete"} {
			t.Run(stage+"/"+stop, func(t *testing.T) {
				r := progressFixtures()
				bad, _ := brokenProgressSubmission(r[i])
				client := &progressStopClient{}
				for _, prev := range r[:i] {
					client.submissions = append(client.submissions, reportArgs(prev))
				}
				client.submissions = append(client.submissions, bad)
				wantClass, wantCalls := session.FailureBudget, i+2
				switch stop {
				case "provider":
					client.stopAt, client.failure, wantClass = i+2, errors.New("provider unavailable"), session.FailureProvider
				case "cancelled":
					client.stopAt, client.failure, wantClass = i+2, context.Canceled, session.FailureCancelled
				case "tokens":
					client.spendAt = i + 1
				case "declared incomplete":
					client.submissions = append(client.submissions, behaviorPatch(map[string]any{"complete": false}))
					wantClass = session.FailureUnknown
				}
				diffs := []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}, {NewPath: "other.go", Diff: "+value", Insertions: 1}}
				a := newManifestFlowAgentWithClient(t, diffs, nil, client)
				a.args.Template.GroupingMinFiles, a.args.Template.GroupingBundleLineThreshold = 3, 3
				if stop == "tokens" {
					// Allow prompt preflight, then cross the limit on the partial
					// submission so this exercises preservation after execution.
					a.args.MaxTokensBudget = 1000000
				}
				rounds := 1
				if stop == "tokens" {
					rounds = 2 // Reach the between-request budget gate, then existing grace.
				}
				configureCompletionAgent(a, rounds)
				comments, _ := a.dispatchSubtasks(context.Background())
				manifest := finishManifestFlow(t, a)
				if manifest.TerminalState != session.StateFailed || len(manifest.Coverage.Failed) != len(diffs) || client.calls != wantCalls || len(comments) != 0 {
					t.Fatalf("incorrect stop: manifest=%+v requests=%d comments=%d", manifest, client.calls, len(comments))
				}
				for _, failed := range manifest.Coverage.Failed {
					if failed.Classification != wantClass {
						t.Fatalf("failure=%s, want %s", failed.Classification, wantClass)
					}
				}
				stored := a.BehaviorReports()
				if len(stored) != i+1 || stored[i].Complete || len(stored[i].Pending) == 0 || (i == 0 && len(stored[i].Contracts) != 1) || (i > 0 && len(stored[i].Checks) != 1) {
					t.Fatalf("progress did not survive stop: %+v", stored)
				}
			})
		}
	}
}

func TestBehaviorProgressGracePatchesPublishOnce(t *testing.T) {
	r := progressFixtures()
	for i := range r[2].Checks {
		r[2].Checks[i].Verdict, r[2].Checks[i].Refutation = "confirmed", nil
		r[2].Checks[i].Finding = &model.LlmComment{Path: r[0].Contracts[i].Path, Content: "value is lost", ExistingCode: "value", Severity: "medium", Category: "bug"}
	}
	client := &behaviorSubmissionClient{}
	for _, report := range r {
		bad, patch := brokenProgressSubmission(report)
		client.submissions = append(client.submissions, bad, patch)
	}
	a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}, {NewPath: "other.go", Diff: "+value", Insertions: 1}}, nil, client)
	a.args.Template.GroupingMinFiles, a.args.Template.GroupingBundleLineThreshold = 3, 3
	configureCompletionAgent(a, 1)
	a.args.Tools.Register(&tool.CodeCommentProvider{Collector: a.args.CommentCollector})
	comments, _ := a.dispatchSubtasks(context.Background())
	manifest := finishManifestFlow(t, a)
	if manifest.TerminalState != session.StateComplete || client.calls != 6 || len(comments) != 2 {
		t.Fatalf("minimal grace patches failed: manifest=%+v requests=%d comments=%d", manifest, client.calls, len(comments))
	}
	for _, i := range []int{1, 3, 5} {
		request, _ := json.Marshal(client.requests[i])
		if !strings.Contains(string(request), "saved contracts") || !strings.Contains(string(request), "submit only corrected or missing") {
			t.Fatal("the loop did not deliver the saved-progress receipt")
		}
	}
}
