// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package scan

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/config/template"
	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/session"
)

func TestMaybeRunPlan_CustomFallbackOnEverySkipOrFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	const fallback = "No plan. Review only the selected function using supplied evidence."
	for _, name := range []string{"no plan task", "no-plan flag", "request failure", "empty response", "empty checklist"} {
		t.Run(name, func(t *testing.T) {
			tpl := makeTemplateWithFullScan()
			tpl.NoPlanGuidance = fallback
			tpl.PlanTask = &template.LlmConversation{Messages: []template.ChatMessage{
				{Role: "user", Content: "{{file_content}}"},
			}}
			client := &fakeScanClient{}
			a := newAgentForTest(t, tpl)
			t.Cleanup(func() {
				if err := a.Session().Finalize(); err != nil {
					t.Error(err)
				}
			})
			a.args.LLMClient = client
			switch name {
			case "no plan task":
				a.args.Template.PlanTask = nil
			case "no-plan flag":
				a.args.SkipPlan = true
			case "request failure":
				a.args.LLMClient = &errorScanClient{err: fmt.Errorf("test failure")}
			case "empty checklist":
				text := "{\"summary\":\"\",\"checkpoints\":[]}"
				client.responses = []*llm.ChatResponse{{Choices: []llm.Choice{{Message: llm.ResponseMessage{Content: &text}}}}}
			}
			item := model.ScanItem{Path: "handler.go", Content: "package handler\n"}
			got := a.maybeRunPlan(t.Context(), item, "rules")
			if got != fallback {
				t.Fatalf("fallback = %q, want %q", got, fallback)
			}
			messages := a.renderMessages(item, "rules", got)
			if !strings.Contains(messages[1].ExtractText(), fallback) {
				t.Fatal("custom fallback did not reach MAIN_TASK")
			}
			if (name == "no plan task" || name == "no-plan flag") && client.calls != 0 {
				t.Fatal("disabled planning called the LLM")
			}
		})
	}
}

func TestScanPromptOverride_ResumeUsesMatchingContract(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	repo := initTestRepo(t)
	writeFile(t, repo, "handler.go", []byte("package handler\nfunc Handle() {}\n"))
	gitCommit(t, repo, "init")
	data, err := os.ReadFile(filepath.Join("..", "..", "examples", "scan", "bounded-template.json"))
	if err != nil {
		t.Fatal(err)
	}
	load := func(data []byte) template.ScanTemplate {
		t.Helper()
		path := filepath.Join(t.TempDir(), "prompts.json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		tpl, err := template.LoadScan(path)
		if err != nil {
			t.Fatal(err)
		}
		return *tpl
	}
	doneClient := func() *fakeScanClient {
		return &fakeScanClient{responses: []*llm.ChatResponse{{
			Choices: []llm.Choice{{Message: llm.ResponseMessage{ToolCalls: []llm.ToolCall{{
				ID: "done", Type: "function", Function: llm.FunctionCall{Name: "task_done", Arguments: "{}"},
			}}}}},
		}}}
	}
	newScan := func(tpl template.ScanTemplate, client *fakeScanClient, resume *session.ResumeState) *Agent {
		return NewAgent(Args{
			RepoDir: repo, Template: tpl, LLMClient: client, Resume: resume,
			MaxConcurrency: 1, SkipSummary: true, SkipDedup: true,
			MainToolDefs: []llm.ToolDef{{Type: "function", Function: llm.FunctionDef{Name: "task_done"}}},
		})
	}
	first := newScan(load(data), doneClient(), nil)
	if _, err := first.Run(t.Context()); err != nil {
		t.Fatal(err)
	}
	resume, err := session.LoadResumeState(repo, first.SessionID())
	if err != nil || resume.CompletedCount() != 1 {
		t.Fatalf("load checkpoint: %v", err)
	}
	defaults, err := template.LoadScanDefault()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		tpl       template.ScanTemplate
		wantCalls int
	}{
		{"same prompts at another path", load(data), 0},
		{"changed main", load([]byte(strings.Replace(string(data), "Review only the function", "Audit only the function", 1))), 1},
		{"changed fallback", load([]byte(strings.Replace(string(data), "No pre-scan plan.", "Planning disabled.", 1))), 1},
		{"default contract", *defaults, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := doneClient()
			a := newScan(tt.tpl, client, resume)
			a.args.SkipPlan = true
			if _, err := a.Run(t.Context()); err != nil {
				t.Fatal(err)
			}
			if client.calls != tt.wantCalls {
				t.Fatalf("LLM calls = %d, want %d", client.calls, tt.wantCalls)
			}
			info := a.ResumeInfo()
			if info.RerunFiles != int64(tt.wantCalls) || info.ReusedFiles != int64(1-tt.wantCalls) {
				t.Fatalf("wrong checkpoint reuse: %+v", info)
			}
		})
	}
}
