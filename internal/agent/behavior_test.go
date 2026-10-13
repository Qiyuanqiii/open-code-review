// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/config/template"
	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/session"
	"github.com/alibaba/open-code-review/internal/tool"
)

func behaviorFixtures() []BehaviorReport {
	contract := BehaviorContract{
		ID: "c1", Path: "good.go", Behavior: "return a value", Basis: "caller expects a value", BasisStatus: "explicit", Relation: "caller -> value",
		Sources:      []BehaviorContractSource{{ID: "src1", Kind: "interface", Citation: "good.go:1 Get", Statement: "Get returns the stored value."}},
		Observations: []string{"good.go:1 return value"},
	}
	basis := BehaviorBasisReview{
		ContractID: "c1", Verdict: "supported", Analysis: "The interface requires preserving the stored value, including zero.",
		SourceAssessments: []BehaviorSourceAssessment{{SourceID: "src1", Analysis: "Get's return contract applies to the selected function."}},
		Evidence:          []string{"good.go:1 Get returns the stored value"},
	}
	check := BehaviorCheck{ID: "t1", ContractID: "c1", Scenario: "value is zero", Expected: "return zero to caller", Outcome: "caller receives zero", Trace: []BehaviorTraceStep{{ID: "s1", Code: "good.go:1 return value", Value: "return 0"}}, Analysis: "caller accepts zero", Evidence: []string{"good.go:1 return value"}, Counterevidence: "zero is explicitly accepted", Verdict: "refuted"}
	decision := check
	decision.Scenario, decision.Expected, decision.Outcome, decision.Trace = "", "", "", nil
	decision.Refutation = &BehaviorRefutation{Target: "outcome", Reason: "returned zero satisfies the caller's expectation"}
	return []BehaviorReport{
		{Stage: "contracts", Complete: true, Contracts: []BehaviorContract{contract}, Unknowns: []string{}, Limitations: []string{}},
		{Stage: "counterexamples", Complete: true, Checks: []BehaviorCheck{check}, Unknowns: []string{}, Limitations: []string{}},
		{Stage: "adjudication", Complete: true, BasisReviews: []BehaviorBasisReview{basis}, Checks: []BehaviorCheck{decision}, Unknowns: []string{}, Limitations: []string{}},
	}
}

func reportArgs(report BehaviorReport) map[string]any {
	data, _ := json.Marshal(report)
	var value any
	_ = json.Unmarshal(data, &value)
	return map[string]any{"report": value}
}

func TestBehaviorReportCoverage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stage int
		edit  func(*BehaviorReport)
	}{
		{"missing path", 0, func(r *BehaviorReport) { r.Contracts = nil }},
		{"invented path", 0, func(r *BehaviorReport) { r.Contracts[0].Path = "other.go" }},
		{"missing basis", 0, func(r *BehaviorReport) { r.Contracts[0].Basis = " " }},
		{"empty observation", 0, func(r *BehaviorReport) { r.Contracts[0].Observations = []string{" "} }},
		{"duplicate contract", 0, func(r *BehaviorReport) { r.Contracts = append(r.Contracts, r.Contracts[0]) }},
		{"missing unknowns", 0, func(r *BehaviorReport) { r.Unknowns = nil }},
		{"missing limitations", 0, func(r *BehaviorReport) { r.Limitations = nil }},
		{"wrong stage", 1, func(r *BehaviorReport) { r.Stage = "contracts" }},
		{"missing attempt", 1, func(r *BehaviorReport) { r.Checks = nil }},
		{"unknown contract", 1, func(r *BehaviorReport) { r.Checks[0].ContractID = "missing" }},
		{"no challenge", 1, func(r *BehaviorReport) { r.Checks[0].Counterevidence = "" }},
		{"no expectation", 1, func(r *BehaviorReport) { r.Checks[0].Expected = "" }},
		{"no outcome", 1, func(r *BehaviorReport) { r.Checks[0].Outcome = "" }},
		{"no trace", 1, func(r *BehaviorReport) { r.Checks[0].Trace = nil }},
		{"uncited trace", 1, func(r *BehaviorReport) { r.Checks[0].Trace[0].Code = "" }},
		{"duplicate step", 1, func(r *BehaviorReport) { r.Checks[0].Trace = append(r.Checks[0].Trace, r.Checks[0].Trace[0]) }},
		{"oversized trace", 1, func(r *BehaviorReport) { r.Checks[0].Trace = make([]BehaviorTraceStep, 9) }},
		{"duplicate check", 1, func(r *BehaviorReport) { r.Checks = append(r.Checks, r.Checks[0]) }},
		{"premature confirmation", 1, func(r *BehaviorReport) { r.Checks[0].Verdict = "confirmed" }},
		{"dropped attempt", 2, func(r *BehaviorReport) { r.Checks[0].ID = "replacement" }},
		{"rewritten scenario", 2, func(r *BehaviorReport) { r.Checks[0].Scenario = "value is one" }},
		{"rewritten expectation", 2, func(r *BehaviorReport) { r.Checks[0].Expected = "return one" }},
		{"rewritten outcome", 2, func(r *BehaviorReport) { r.Checks[0].Outcome = "return one" }},
		{"rewritten trace", 2, func(r *BehaviorReport) {
			r.Checks[0].Trace = []BehaviorTraceStep{{ID: "s1", Code: "other code", Value: "1"}}
		}},
		{"unbound refutation", 2, func(r *BehaviorReport) { r.Checks[0].Refutation = nil }},
		{"unsupported target", 2, func(r *BehaviorReport) { r.Checks[0].Refutation.Target = "severity" }},
		{"no rebuttal", 2, func(r *BehaviorReport) { r.Checks[0].Refutation.Reason = " " }},
		{"unknown step", 2, func(r *BehaviorReport) {
			r.Checks[0].Refutation = &BehaviorRefutation{Target: "step", StepID: "s2", Reason: "different computation"}
		}},
		{"step on other target", 2, func(r *BehaviorReport) { r.Checks[0].Refutation.StepID = "s1" }},
		{"unresolved with refutation", 2, func(r *BehaviorReport) { r.Checks[0].Verdict = "unresolved" }},
		{"undecided candidate", 2, func(r *BehaviorReport) { r.Checks[0].Verdict = "candidate" }},
		{"missing finding", 2, func(r *BehaviorReport) { r.Checks[0].Verdict = "confirmed" }},
		{"refuted finding", 2, func(r *BehaviorReport) { r.Checks[0].Finding = &model.LlmComment{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reports := behaviorFixtures()
			report := reports[tc.stage]
			tc.edit(&report)
			if _, _, err := parseBehaviorReport(reportArgs(report), behaviorStages[tc.stage], map[string]bool{"good.go": true}, reports[:tc.stage]); err == nil {
				t.Fatal("invalid coverage accepted")
			}
		})
	}
}

func TestAdjudicationPreservesAttempt(t *testing.T) {
	for _, target := range []string{"basis", "input", "step", "outcome", "change"} {
		t.Run(target, func(t *testing.T) {
			reports := behaviorFixtures()
			reports[2].Checks[0].Refutation.Target = target
			if target == "basis" {
				reports[2].BasisReviews[0].Verdict = "unsupported"
			}
			if target == "step" {
				reports[2].Checks[0].Refutation.StepID = "s1"
			}
			got, _, err := parseBehaviorReport(reportArgs(reports[2]), "adjudication", map[string]bool{"good.go": true}, reports[:2])
			if err != nil {
				t.Fatal(err)
			}
			before, after := reports[1].Checks[0], got.Checks[0]
			if after.Scenario != before.Scenario || after.Expected != before.Expected || after.Outcome != before.Outcome || !reflect.DeepEqual(after.Trace, before.Trace) {
				t.Fatalf("attempt changed during adjudication: before=%+v after=%+v", before, after)
			}
			got.Checks[0].Trace[0].Value = "edited"
			if reports[1].Checks[0].Trace[0].Value != "return 0" {
				t.Fatal("adjudication aliases the preceding trace")
			}
		})
	}
}

type behaviorClient struct {
	reports  []BehaviorReport
	requests []llm.ChatRequest
	calls    int
	failAt   int
	classic  bool
}

func (c *behaviorClient) CompletionsWithCtx(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.requests = append(c.requests, req)
	c.calls++
	if c.calls == c.failAt {
		return nil, errors.New("provider unavailable")
	}
	if c.classic {
		return agentTaskDoneResponse(), nil
	}
	if c.calls > len(c.reports) {
		return agentTaskDoneResponse(), nil
	}
	data, _ := json.Marshal(reportArgs(c.reports[c.calls-1]))
	resp := agentTaskDoneResponse()
	resp.Choices[0].Message.ToolCalls[0].Function.Arguments = string(data)
	return resp, nil
}

func TestBehaviorStagesAndManifest(t *testing.T) {
	for _, scenario := range []string{"zero findings", "limitations", "unknown", "unresolved", "incomplete", "provider failure", "missing reports", "finding with unknown"} {
		t.Run(scenario, func(t *testing.T) {
			client := &behaviorClient{reports: behaviorFixtures()}
			if scenario != "zero findings" {
				client.reports[2].Limitations = []string{"Static trace only; no test execution tool is available."}
			}
			switch scenario {
			case "unknown", "finding with unknown":
				client.reports[2].Unknowns = []string{"unavailable caller"}
			case "unresolved":
				client.reports[2].Checks[0].Verdict = "unresolved"
				client.reports[2].Checks[0].Refutation = nil
			case "incomplete":
				client.reports[1].Complete = false
			case "provider failure":
				client.failAt = 2
			case "missing reports":
				client.classic = true
			}
			if scenario == "finding with unknown" {
				client.reports[2].Checks[0].Verdict = "confirmed"
				client.reports[2].Checks[0].Refutation = nil
				client.reports[2].Checks[0].Finding = &model.LlmComment{Path: "good.go", Content: "value is lost", ExistingCode: "value", Severity: "medium", Category: "bug"}
			}
			a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}, nil, client)
			a.args.BehaviorPrompt = behaviorPrompt
			a.args.Template.MaxReviewRounds = 3
			a.args.Tools.Register(&tool.CodeCommentProvider{Collector: a.args.CommentCollector})
			comments, _ := a.dispatchSubtasks(context.Background())
			manifest := finishManifestFlow(t, a)
			if scenario == "zero findings" || scenario == "limitations" {
				if manifest.TerminalState != session.StateComplete || client.calls != 3 || len(a.BehaviorReports()) != 3 || len(comments) != 0 {
					t.Fatalf("zero-candidate stage work skipped: calls=%d manifest=%+v", client.calls, manifest)
				}
				if scenario == "limitations" && len(a.BehaviorReports()[2].Limitations) != 1 {
					t.Fatal("evidence boundary was discarded")
				}
			} else if manifest.TerminalState != session.StateFailed || len(manifest.Coverage.Completed) != 0 || len(manifest.Coverage.Failed) != 1 {
				t.Fatalf("incomplete work reported as complete: %+v", manifest)
			}
			if scenario == "finding with unknown" && len(comments) != 1 {
				t.Fatalf("confirmed finding discarded: %+v", comments)
			}
		})
	}
}

func TestBehaviorStageHandoff(t *testing.T) {
	for _, scenario := range []string{"resolved later", "still unresolved", "incomplete inventory"} {
		t.Run(scenario, func(t *testing.T) {
			reports := behaviorFixtures()
			reports[0].Contracts[0].BasisStatus = "disputed"
			reports[0].Unknowns = []string{"c1: determine whether the return contract covers zero in counterexamples and adjudication."}
			reports[1].Checks[0].Verdict = "unresolved"
			reports[1].Unknowns = []string{"c1/t1: adjudication must establish whether the caller accepts zero."}
			if scenario == "incomplete inventory" {
				reports[0].Complete = false
			}
			if scenario == "still unresolved" {
				reports[2].Unknowns = reports[1].Unknowns
				reports[2].BasisReviews[0].Verdict = "unresolved"
				reports[2].Checks[0].Verdict = "unresolved"
				reports[2].Checks[0].Refutation = nil
			}
			client := &behaviorClient{reports: reports}
			a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}, nil, client)
			configureCompletionAgent(a, 2)
			_, _ = a.dispatchSubtasks(context.Background())
			manifest := finishManifestFlow(t, a)
			wantCalls, wantState := 3, session.StateFailed
			if scenario == "incomplete inventory" {
				wantCalls = 1
			} else if scenario == "resolved later" {
				wantState = session.StateComplete
			}
			if client.calls != wantCalls || manifest.TerminalState != wantState {
				t.Fatalf("stage handoff: requests=%d state=%s, want %d/%s", client.calls, manifest.TerminalState, wantCalls, wantState)
			}
			stored := a.BehaviorReports()
			for i := 0; i < wantCalls; i++ {
				if !reflect.DeepEqual(stored[i].Unknowns, reports[i].Unknowns) {
					t.Fatalf("stage %s lost its unknowns: %+v", reports[i].Stage, stored[i])
				}
				if i == 0 {
					continue
				}
				var previous []BehaviorReport
				for _, message := range client.requests[i].Messages {
					text, _ := message.Content.(string)
					if _, payload, ok := strings.Cut(text, "Previous stage records (claims to verify):\n"); ok {
						if err := json.Unmarshal([]byte(payload), &previous); err != nil {
							t.Fatal(err)
						}
					}
				}
				if !reflect.DeepEqual(previous, stored[:i]) {
					t.Fatalf("stage %s lost the preceding questions or original sources", reports[i].Stage)
				}
			}
		})
	}
}

type behaviorFilterClient struct {
	behaviorClient
	filterCalls int
}

func (c *behaviorFilterClient) CompletionsWithCtx(ctx context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	for _, def := range req.Tools {
		if def.Function.Name == "report_incorrect_comments" {
			c.filterCalls++
			resp := agentTaskDoneResponse()
			resp.Choices[0].Message.ToolCalls[0].Function.Name = "report_incorrect_comments"
			resp.Choices[0].Message.ToolCalls[0].Function.Arguments = `{"comment_ids":["c-0"]}`
			return resp, nil
		}
	}
	return c.behaviorClient.CompletionsWithCtx(ctx, req)
}

func TestIncompleteAdjudicationStillFiltersFindings(t *testing.T) {
	client := &behaviorFilterClient{behaviorClient: behaviorClient{reports: behaviorFixtures()}}
	client.reports[2].Complete = false
	client.reports[2].Unknowns = []string{"a required caller is unavailable"}
	client.reports[2].Checks[0].Verdict = "confirmed"
	client.reports[2].Checks[0].Refutation = nil
	client.reports[2].Checks[0].Finding = &model.LlmComment{Path: "good.go", Content: "value is lost", ExistingCode: "value", Severity: "medium", Category: "bug"}
	a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}, nil, client)
	a.args.BehaviorPrompt = behaviorPrompt
	a.args.Template.MaxReviewRounds = 3
	a.args.Template.ReviewFilterTask = &template.LlmConversation{Messages: []template.ChatMessage{{Role: "user", Content: "{{comments}}"}}}
	a.args.Tools.Register(&tool.CodeCommentProvider{Collector: a.args.CommentCollector})
	comments, _ := a.dispatchSubtasks(context.Background())
	manifest := finishManifestFlow(t, a)
	if client.filterCalls != 1 || len(comments) != 0 {
		t.Fatalf("incomplete adjudication bypassed filtering: calls=%d comments=%+v", client.filterCalls, comments)
	}
	if manifest.TerminalState != session.StateFailed || a.BehaviorReports()[2].Complete {
		t.Fatalf("filtering completed unfinished work: %+v", manifest)
	}
}

// Parse one submission without any prior progress. Stateful patch behavior is
// exercised separately through the same accumulator and the real runner.
func parseBehaviorReport(args map[string]any, stage string, paths map[string]bool, previous []BehaviorReport) (BehaviorReport, []model.LlmComment, error) {
	return newBehaviorProgress(stage, paths, previous).submit(args)
}

const behaviorPrompt = "Apply the selected behavior review policy to the assigned stage."
