// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/session"
	"github.com/alibaba/open-code-review/internal/tool"
)

type behaviorBudgetRead struct{}

func (behaviorBudgetRead) Tool() tool.Tool { return tool.FileRead }
func (behaviorBudgetRead) Execute(context.Context, map[string]any) (string, error) {
	return "func Get() int { return 0 }", nil
}

// Investigate until the runner closes the stage. Adjudication then emits an
// empty response, a malformed decision and an entry-only correction.
type behaviorAllowanceClient struct {
	reports      []BehaviorReport
	stage, calls int
	closing      [3]int
	finishedAt   [3]int64
	rejectRepair bool
}

func (c *behaviorAllowanceClient) CompletionsWithCtx(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.calls++
	if c.stage >= 3 || c.calls > 40 {
		return nil, fmt.Errorf("unexpected extra request")
	}
	resp := agentTaskDoneResponse()
	resp.Usage = &llm.UsageInfo{PromptTokens: 100000, CompletionTokens: 1000, TotalTokens: 101000}
	if len(req.Tools) > 1 {
		resp.Choices[0].Message.ToolCalls[0].Function = llm.FunctionCall{Name: "file_read", Arguments: `{"path":"good.go"}`}
		return resp, nil
	}
	c.closing[c.stage]++
	if c.stage == 2 && c.closing[2] == 1 {
		resp.Choices[0].Message.ToolCalls = nil
		resp.Choices[0].FinishReason = "length"
		return resp, nil
	}
	args := reportArgs(c.reports[c.stage])
	if c.stage == 2 && (c.closing[2] == 2 || c.rejectRepair) {
		delete(args["report"].(map[string]any)["checks"].([]any)[0].(map[string]any), "refutation")
	} else {
		if c.stage == 2 {
			args = behaviorPatch(map[string]any{"checks": c.reports[2].Checks})
		}
		c.finishedAt[c.stage] = int64(c.calls) * 101000
		c.stage++
	}
	data, err := json.Marshal(args)
	resp.Choices[0].Message.ToolCalls[0].Function.Arguments = string(data)
	return resp, err
}

func TestBehaviorClosingBudgetIsDistinctFromGlobalExhaustion(t *testing.T) {
	client := &behaviorAllowanceClient{reports: behaviorFixtures(), rejectRepair: true}
	a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}, nil, client)
	a.args.MaxTokensBudget = 2000000
	a.args.Template.MaxCompletionTokens = 1000
	a.args.MainToolDefs = append(a.args.MainToolDefs, llm.ToolDef{Type: "function", Function: llm.FunctionDef{Name: "file_read"}})
	a.args.Tools.Register(behaviorBudgetRead{})
	configureCompletionAgent(a, 100)
	_, _ = a.dispatchSubtasks(context.Background())
	manifest := finishManifestFlow(t, a)
	if manifest.TerminalState != session.StateFailed || len(manifest.Coverage.Failed) != 1 || manifest.Coverage.Failed[0].Classification != session.FailureBudget || a.BudgetExceeded() {
		t.Fatalf("closing failure confused with global exhaustion: %+v global=%v", manifest, a.BudgetExceeded())
	}
	report := a.BehaviorReports()[2]
	if report.Complete || len(report.Pending) == 0 || !strings.Contains(report.Error, "scoped completion requests") || client.closing[2] != 3 {
		t.Fatalf("closing failure lost its cause or pending work: %+v calls=%v", report, client.closing)
	}
}

func TestBehaviorStageBudgetPreservesAdjudicationRoom(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprintf("unknown=%v", unknown), func(t *testing.T) {
			client := &behaviorAllowanceClient{reports: behaviorFixtures()}
			if unknown {
				client.reports[2].Unknowns = []string{"Required runtime observation is unavailable."}
			}
			a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}, nil, client)
			a.args.MaxTokensBudget = 2000000
			a.args.Template.MaxCompletionTokens = 1000
			a.args.MainToolDefs = append(a.args.MainToolDefs, llm.ToolDef{Type: "function", Function: llm.FunctionDef{Name: "file_read"}})
			a.args.Tools.Register(behaviorBudgetRead{})
			configureCompletionAgent(a, 100)
			_, _ = a.dispatchSubtasks(context.Background())
			manifest := finishManifestFlow(t, a)
			want := session.StateComplete
			if unknown {
				want = session.StateFailed
				if len(manifest.Coverage.Failed) != 1 || manifest.Coverage.Failed[0].Classification != session.FailureUnknown {
					t.Fatalf("unresolved evidence misclassified: %+v", manifest)
				}
			}
			if manifest.TerminalState != want || client.stage != 3 || client.closing != [3]int{1, 1, 3} {
				t.Fatalf("state=%s stages=%d closing=%v", manifest.TerminalState, client.stage, client.closing)
			}
			if a.BudgetExceeded() || a.runner.TotalTokensUsed() > a.args.MaxTokensBudget || client.finishedAt[0] >= a.args.MaxTokensBudget/2 {
				t.Fatalf("earlier stages starved completion: finished=%v total=%d", client.finishedAt, a.runner.TotalTokensUsed())
			}
			reports := a.BehaviorReports()
			if reports[0].TokenLimit != 666666 || reports[1].TokenLimit <= reports[0].TokenLimit || reports[2].TokenLimit != a.args.MaxTokensBudget {
				t.Fatalf("stage budget boundaries not retained: %+v", reports)
			}
			if len(reports[2].Pending) != 0 || !slices.Equal(reports[2].Unknowns, client.reports[2].Unknowns) {
				t.Fatalf("correction lost completion state or unknowns: %+v", reports[2])
			}
			t.Logf("stage end usage=%v; stage soft limits=%v; closing=%v", client.finishedAt, []int64{reports[0].TokenLimit, reports[1].TokenLimit, reports[2].TokenLimit}, client.closing)
		})
	}
}

// The external fixture contains only public V12 tool arguments and saved
// reports. Its repair is host-authored protocol test data, not a model verdict.
