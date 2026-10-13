// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/llmloop"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/plugin"
	"github.com/alibaba/open-code-review/internal/session"
	"github.com/alibaba/open-code-review/internal/tool"
)

type pluginReviewClient struct {
	calls    int
	retry    bool
	failAt   int
	requests []llm.ChatRequest
}

func TestPluginStartupCancellationClassification(t *testing.T) {
	diffs := []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}
	a := newManifestFlowAgentWithClient(t, diffs, nil, &pluginReviewClient{})
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	a.args.ReviewPlugin = &plugin.Installed{Manifest: plugin.Manifest{SchemaVersion: 1, ID: "cancelled", Name: "Cancelled", Version: "1", Review: &plugin.ReviewContribution{Protocol: "stdio-v1", Command: executable, Stages: []string{"inspect"}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = a.executePluginReview(ctx, FileGroup{Diffs: diffs})
	class, _ := classifyItemError(err)
	if class != session.FailureCancelled {
		t.Fatalf("cancelled plugin classified as %s", class)
	}
}

func (c *pluginReviewClient) CompletionsWithCtx(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	c.calls++
	c.requests = append(c.requests, req)
	if c.calls == c.failAt {
		return nil, errors.New("provider unavailable")
	}
	res := agentTaskDoneResponse()
	res.Choices[0].Message.ToolCalls[0].Function.Arguments = `{"checked":true}`
	if c.retry && c.calls == 1 {
		res.Choices[0].Message.ToolCalls[0].Function.Arguments = `{}`
	}
	return res, nil
}

func TestPluginReviewRecoveryAndFailures(t *testing.T) {
	for _, mode := range []string{"complete", "retry", "provider-after-rejection", "incomplete", "finding-incomplete", "missing-complete", "crash", "invalid-comment"} {
		t.Run(mode, func(t *testing.T) {
			client := &pluginReviewClient{retry: mode == "retry" || mode == "provider-after-rejection"}
			if mode == "provider-after-rejection" {
				client.failAt = 2
			}
			a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}, nil, client)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			a.args.ReviewPlugin = &plugin.Installed{Manifest: plugin.Manifest{SchemaVersion: 1, ID: "custom", Name: "Custom", Version: "1", Review: &plugin.ReviewContribution{Protocol: "stdio-v1", Command: executable, Args: []string{"-test.run=^TestAgentReviewProcess$", "--", "agent-review-helper", mode}, Stages: []string{"inspect", "verify"}}}}
			a.args.Template.MaxToolRequestTimes = 4
			a.args.Tools.Register(&tool.CodeCommentProvider{Collector: a.args.CommentCollector})
			a.runner = llmloop.NewRunner(llmloop.Deps{LLMClient: client, Model: a.args.Model, Template: a.args.Template, Tools: a.args.Tools, MainToolDefs: a.args.MainToolDefs, CommentCollector: a.args.CommentCollector, Session: a.session, DiffLookup: a.findDiff, AllDiffs: a.allDiffs, NewRequestMeta: a.newRequestMeta})
			comments, _ := a.dispatchSubtasks(context.Background())
			manifest := finishManifestFlow(t, a)
			complete := mode == "complete" || mode == "retry"
			if (manifest.TerminalState == session.StateComplete) != complete {
				t.Fatalf("wrong completion: %+v", manifest)
			}
			reports := a.PluginReviewReports()
			if len(reports) == 0 || len(reports[0].Report) == 0 {
				t.Fatal("plugin state lost")
			}
			if complete && (len(reports) != 2 || !reports[1].Complete) {
				t.Fatalf("stage plan not completed: %+v", reports)
			}
			wantCalls := 1
			if mode == "complete" || mode == "provider-after-rejection" {
				wantCalls = 2
			}
			if mode == "retry" {
				wantCalls = 3
			}
			if client.calls != wantCalls {
				t.Fatalf("calls=%d want %d", client.calls, wantCalls)
			}
			wantComments := 0
			if mode == "finding-incomplete" {
				wantComments = 1
			}
			if len(comments) != wantComments {
				t.Fatalf("comments=%+v", comments)
			}
			if mode == "provider-after-rejection" && (!strings.Contains(reports[0].Error, "checked is required") || manifest.Coverage.Failed[0].Classification != session.FailureProvider) {
				t.Fatalf("last accepted state/feedback lost: %+v %+v", reports, manifest)
			}
			if mode == "crash" || mode == "missing-complete" || mode == "invalid-comment" {
				if manifest.Coverage.Failed[0].Classification != session.FailureUnknown {
					t.Fatalf("plugin fault classified as provider fault: %+v", manifest)
				}
			}
			for _, request := range client.requests {
				for _, def := range request.Tools {
					if def.Function.Name == "code_comment" {
						t.Fatal("model can bypass plugin validator")
					}
				}
			}
		})
	}
}

func TestAgentReviewProcess(t *testing.T) {
	i := slices.Index(os.Args, "agent-review-helper")
	if i < 0 {
		return
	}
	mode := os.Args[i+1]
	lines := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for lines.Scan() {
		var req plugin.ReviewRequest
		if json.Unmarshal(lines.Bytes(), &req) != nil {
			os.Exit(2)
		}
		res := plugin.ReviewResponse{Version: 1, Report: json.RawMessage(`{"saved":"plugin-owned-state"}`)}
		if req.Method == "begin" {
			res.Task = &plugin.ReviewTask{Instructions: "Inspect this change.", Input: "Custom stage: " + req.Stage, Completion: plugin.ReviewCompletion{Description: "Submit checked.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"checked": map[string]any{"type": "boolean"}}}}}
		} else {
			if mode == "crash" {
				os.Exit(3)
			}
			accepted := req.Arguments["checked"] == true
			complete := mode != "incomplete" && mode != "finding-incomplete"
			res.Accepted, res.Complete = &accepted, &complete
			if !accepted {
				res.Complete = nil
				res.Feedback = "checked is required"
			}
			if mode == "missing-complete" {
				res.Complete = nil
			}
			if mode == "finding-incomplete" || mode == "invalid-comment" {
				res.Comments = []model.LlmComment{{Path: "good.go", Content: "The value is lost.", ExistingCode: "value", Severity: "medium", Category: "bug"}}
				if mode == "invalid-comment" {
					res.Comments[0].Path = "outside.go"
				}
			}
		}
		if enc.Encode(res) != nil {
			os.Exit(4)
		}
	}
	os.Exit(0)
}
