// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/session"
)

func TestBehaviorCompletionDeclaration(t *testing.T) {
	for stageIndex, stage := range behaviorStages {
		for _, value := range []any{nil, "missing", "true", 1, false, true} {
			reports := behaviorFixtures()
			args := reportArgs(reports[stageIndex])
			submitted := args["report"].(map[string]any)
			delete(submitted, "stage")
			submitted["complete"] = value
			if value == "missing" {
				delete(submitted, "complete")
			}
			got, comments, err := parseBehaviorReport(args, stage, map[string]bool{"good.go": true}, reports[:stageIndex])
			complete, valid := value.(bool)
			if !valid {
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), "complete") || len(comments) != 0 {
					t.Fatalf("%s complete=%v accepted or unnamed: %v, %+v", stage, value, err, comments)
				}
			} else if err != nil || got.Complete != complete || got.Stage != stage {
				t.Fatalf("%s complete=%v: report=%+v error=%v", stage, value, got, err)
			}
		}
	}
}

func TestBehaviorReferenceFeedback(t *testing.T) {
	reports := behaviorFixtures()
	second := reports[1].Checks[0]
	second.ID, second.ContractID = "t2", "renamed-two"
	reports[1].Checks[0].ContractID = "renamed-one"
	reports[1].Checks = append(reports[1].Checks, second)
	_, comments, err := parseBehaviorReport(reportArgs(reports[1]), "counterexamples", map[string]bool{"good.go": true}, reports[:1])
	if err == nil || len(comments) != 0 {
		t.Fatalf("invented references accepted: %v %+v", err, comments)
	}
	for _, detail := range []string{"t1", "renamed-one", "t2", "renamed-two", "valid contract_ids: [c1]"} {
		if !strings.Contains(err.Error(), detail) {
			t.Errorf("repair feedback omits %q: %v", detail, err)
		}
	}
	reports = behaviorFixtures()
	reports[2].Checks = nil
	_, _, err = parseBehaviorReport(reportArgs(reports[2]), "adjudication", map[string]bool{"good.go": true}, reports[:2])
	if err == nil || !strings.Contains(err.Error(), "contract_ids [c1] and prior check ids [t1]") {
		t.Fatalf("coverage repair needs the missing IDs: %v", err)
	}
}

func TestBehaviorHostRestoresDecisionLinks(t *testing.T) {
	reports := behaviorFixtures()
	contract := reports[0].Contracts[0]
	contract.ID = "c2"
	reports[0].Contracts = append(reports[0].Contracts, contract)
	check := reports[1].Checks[0]
	check.ID, check.ContractID, check.Scenario = "t2", "c2", "a different stored value"
	reports[1].Checks = append(reports[1].Checks, check)
	basis := reports[2].BasisReviews[0]
	basis.ContractID = "c2"
	reports[2].BasisReviews = append(reports[2].BasisReviews, basis)
	decision := reports[2].Checks[0]
	decision.ID = "t2"
	// Reorder the decisions: their positions cannot establish their identity.
	reports[2].Checks = append([]BehaviorCheck{decision}, reports[2].Checks...)
	args := reportArgs(reports[2])
	submitted := args["report"].(map[string]any)
	delete(submitted, "stage")
	for _, value := range submitted["checks"].([]any) {
		delete(value.(map[string]any), "contract_id")
	}
	before, _ := json.Marshal(reports[:2])
	got, _, err := parseBehaviorReport(args, "adjudication", map[string]bool{"good.go": true}, reports[:2])
	if err != nil || got.Checks[0].ContractID != "c2" || got.Checks[0].Scenario != check.Scenario || got.Checks[1].ContractID != "c1" {
		t.Fatalf("saved links were not restored by ID: report=%+v error=%v", got, err)
	}
	after, _ := json.Marshal(reports[:2])
	if string(before) != string(after) {
		t.Fatal("parsing changed the saved records")
	}
	// A supplied conflicting link must be rejected even when it names a real contract.
	submitted["checks"].([]any)[0].(map[string]any)["contract_id"] = "c1"
	if _, comments, err := parseBehaviorReport(args, "adjudication", map[string]bool{"good.go": true}, reports[:2]); err == nil || !strings.Contains(err.Error(), `belongs to contract_id "c2"`) || len(comments) != 0 {
		t.Fatalf("rebound decision accepted: %v %+v", err, comments)
	}
}

func TestBehaviorSchemaUsesSavedReferences(t *testing.T) {
	reports := behaviorFixtures()
	for i, stage := range behaviorStages {
		defs := behaviorToolDefs(nil, stage, reports[:i])
		reportSchema := defs[0].Function.Parameters["properties"].(map[string]any)["report"].(map[string]any)
		if required := reportSchema["required"].([]string); required == nil || len(required) != 0 {
			t.Fatal("the report schema must allow entry-only or metadata-only patches")
		}
		props := reportSchema["properties"].(map[string]any)
		if _, present := props["pending"]; present {
			t.Fatal("the model cannot set host-owned pending work")
		}
		if _, present := props["stage"]; present {
			t.Fatal("the model is asked to repeat the host-owned stage")
		}
		if i == 0 {
			continue
		}
		checkSchema := props["checks"].(map[string]any)["items"].(map[string]any)
		check := checkSchema["properties"].(map[string]any)
		if stage == "counterexamples" {
			if !reflect.DeepEqual(check["contract_id"].(map[string]any)["enum"], []string{"c1"}) {
				t.Fatal("counterexample contract choices do not match saved records")
			}
		} else {
			if !slices.Contains(checkSchema["required"].([]string), "refutation") || !reflect.DeepEqual(check["refutation"].(map[string]any)["type"], []string{"object", "null"}) {
				t.Fatal("adjudication must request a refutation explicitly, with null for other verdicts")
			}
			if _, present := check["contract_id"]; present {
				t.Fatal("adjudication is asked to repeat the saved contract link")
			}
			if !reflect.DeepEqual(check["id"].(map[string]any)["enum"], []string{"t1"}) {
				t.Fatal("decision choices do not match saved checks")
			}
			basis := props["basis_reviews"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
			if !reflect.DeepEqual(basis["contract_id"].(map[string]any)["enum"], []string{"c1"}) {
				t.Fatal("basis choices do not match saved contracts")
			}
		}
	}
}

// Keep raw maps at the provider boundary: decoding into BehaviorReport here
// would turn an omitted complete field into an explicit false before the test.
type behaviorSubmissionClient struct {
	behaviorClient
	submissions []map[string]any
}

func (c *behaviorSubmissionClient) CompletionsWithCtx(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.requests = append(c.requests, req)
	c.calls++
	args := c.submissions[min(c.calls-1, len(c.submissions)-1)]
	data, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	resp := agentTaskDoneResponse()
	resp.Choices[0].Message.ToolCalls[0].Function.Arguments = string(data)
	return resp, nil
}

func TestBehaviorMissingCompletionRecovery(t *testing.T) {
	for _, outcome := range []string{"repaired", "exhausted", "explicit false"} {
		t.Run(outcome, func(t *testing.T) {
			reports := behaviorFixtures()
			submitted := reportArgs(reports[2])
			delete(submitted["report"].(map[string]any), "complete")
			client := &behaviorSubmissionClient{submissions: []map[string]any{reportArgs(reports[0]), reportArgs(reports[1]), submitted}}
			if outcome == "repaired" {
				client.submissions = append(client.submissions, behaviorPatch(map[string]any{"complete": true}))
			} else if outcome == "explicit false" {
				submitted["report"].(map[string]any)["complete"] = false
			}
			a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}, nil, client)
			configureCompletionAgent(a, 1)
			_, _ = a.dispatchSubtasks(context.Background())
			manifest := finishManifestFlow(t, a)
			if outcome == "repaired" {
				if manifest.TerminalState != session.StateComplete || client.calls != 4 {
					t.Fatalf("grace repair did not complete: %+v calls=%d", manifest, client.calls)
				}
				return
			}
			if manifest.TerminalState != session.StateFailed || len(manifest.Coverage.Failed) != 1 {
				t.Fatalf("unfinished work passed the gate: %+v", manifest)
			}
			failure := manifest.Coverage.Failed[0]
			if outcome == "exhausted" {
				if client.calls != 4 || failure.Classification != session.FailureBudget || !strings.Contains(a.BehaviorReports()[2].Error, "report.complete") {
					t.Fatalf("protocol failure lost its cause: %+v calls=%d reports=%+v", failure, client.calls, a.BehaviorReports())
				}
			} else if client.calls != 3 || failure.Classification != session.FailureUnknown || a.BehaviorReports()[2].Error != "" {
				t.Fatalf("explicit incomplete declaration treated as protocol failure: %+v calls=%d", failure, client.calls)
			}
		})
	}
}

func TestBehaviorReferenceRecovery(t *testing.T) {
	reports := behaviorFixtures()
	broken := reportArgs(reports[1])
	broken["report"].(map[string]any)["checks"].([]any)[0].(map[string]any)["contract_id"] = "renamed"
	client := &behaviorSubmissionClient{submissions: []map[string]any{
		reportArgs(reports[0]), broken, reportArgs(reports[1]), reportArgs(reports[2]),
	}}
	a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}, nil, client)
	configureCompletionAgent(a, 1)
	_, _ = a.dispatchSubtasks(context.Background())
	manifest := finishManifestFlow(t, a)
	if manifest.TerminalState != session.StateComplete || client.calls != 4 {
		t.Fatalf("saved-reference repair failed: state=%s calls=%d", manifest.TerminalState, client.calls)
	}
	request, _ := json.Marshal(client.requests[2])
	for _, detail := range []string{"renamed", "valid contract_ids: [c1]"} {
		if !strings.Contains(string(request), detail) {
			t.Fatalf("correction request lacks %q", detail)
		}
	}
}

// The external fixture contains unchanged public submissions from the sealed
// V9 comparison. Any repaired field is explicitly host-authored, not a new model
// decision. Keeping it external avoids embedding known bug answers in the repo.
