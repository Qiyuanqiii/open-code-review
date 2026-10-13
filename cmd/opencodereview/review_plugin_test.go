// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/plugin"
	"github.com/alibaba/open-code-review/internal/session"
)

func TestReviewPluginFlagsAndTUI(t *testing.T) {
	opts, err := parseReviewFlags([]string{"--review-plugin", "example-review"})
	if err != nil || opts.reviewPlugin != "example-review" {
		t.Fatalf("flags: %+v %v", opts, err)
	}
	args := tuiReviewArgs(opts, "localhost:1")
	i := slices.Index(args, "--review-plugin")
	if i < 0 || args[i+1] != "example-review" {
		t.Fatalf("subprocess dropped selection: %v", args)
	}
	if _, err := parseReviewFlags([]string{"--review-plugin", "example-review", "--resume", "old"}); err == nil {
		t.Fatal("unsupported resume accepted")
	}
	m := testReviewTUI()
	m.opts.resume = "remembered-session"
	var selections, resumes []string
	m.services.review = func(_ context.Context, opts reviewOptions, _ func(string)) (jsonOutput, error) {
		selections = append(selections, opts.reviewPlugin)
		resumes = append(resumes, opts.resume)
		return jsonOutput{Status: "complete"}, nil
	}
	m.services.preview = func(_ context.Context, opts reviewOptions) (model.Preview, error) {
		selections = append(selections, opts.reviewPlugin)
		resumes = append(resumes, opts.resume)
		return model.Preview{}, nil
	}
	for _, command := range []string{"/review example-review", "/review", "/preview example-review", "/review"} {
		next, cmd := m.command(command)
		m = finishTUIReviewFlow(t, next.(reviewTUIModel), cmd)
		if m.opts.reviewPlugin != "" {
			t.Fatal("selection leaked to later commands")
		}
		if m.opts.resume != "remembered-session" {
			t.Fatal("one-off plugin command cleared the saved resume selection")
		}
	}
	if !reflect.DeepEqual(selections, []string{"example-review", "", "example-review", ""}) {
		t.Fatalf("selections: %v", selections)
	}
	if !reflect.DeepEqual(resumes, []string{"", "remembered-session", "", "remembered-session"}) {
		t.Fatalf("resume selections: %v", resumes)
	}
}

func TestPluginReviewPartialExit(t *testing.T) {
	manifest := &session.RunManifest{TerminalState: session.StatePartial}
	if pluginReviewResultError(nil, manifest, "example-review") == nil {
		t.Fatal("incomplete required work exited successfully")
	}
	if err := pluginReviewResultError(nil, manifest, ""); err != nil {
		t.Fatalf("ordinary partial exit changed: %v", err)
	}
	manifest.TerminalState = session.StateComplete
	if err := pluginReviewResultError(nil, manifest, "example-review"); err != nil {
		t.Fatal(err)
	}
}

func TestReviewPluginFullFlow(t *testing.T) {
	for _, stages := range [][]string{{"inspect"}, {"inventory", "verify"}} {
		for _, incomplete := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/incomplete=%v", stages, incomplete), func(t *testing.T) {
				store := pluginRuntimeHome(t)
				source := t.TempDir()
				executable, err := os.Executable()
				if err != nil {
					t.Fatal(err)
				}
				m := plugin.Manifest{SchemaVersion: 1, ID: "example-review", Name: "Example", Version: "1", Review: &plugin.ReviewContribution{Protocol: "stdio-v1", Command: executable, Args: []string{"-test.run=^TestCLIReviewProcess$", "--", "cli-review-helper", fmt.Sprint(incomplete)}, Stages: stages}}
				data, _ := json.Marshal(m)
				if err := os.WriteFile(filepath.Join(source, plugin.ManifestFilename), data, 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := store.Install(context.Background(), source, false); err != nil {
					t.Fatal(err)
				}
				repo := initRulesCheckTestRepo(t)
				gitCommitFile(t, repo, "main.go", "package main\nfunc value() int { return 0 }\n", "initial")
				if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main\nfunc value() int { return 1 }\n"), 0600); err != nil {
					t.Fatal(err)
				}
				var mu sync.Mutex
				var visited []string
				var requests int
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, _ := io.ReadAll(r.Body)
					mu.Lock()
					defer mu.Unlock()
					requests++
					stage := ""
					for _, name := range stages {
						if strings.Contains(string(body), "Workflow stage: "+name) {
							stage = name
						}
					}
					w.Header().Set("Content-Type", "application/json")
					if stage == "" {
						fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"Review main.go"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
						return
					}
					visited = append(visited, stage)
					fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","model":"test","content":[{"type":"tool_use","id":"done","name":"task_done","input":{"checked":true}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":10}}`)
				}))
				defer server.Close()
				t.Setenv("OCR_LLM_URL", server.URL+"/v1/messages")
				t.Setenv("OCR_LLM_TOKEN", "test-token")
				t.Setenv("OCR_LLM_MODEL", "test")
				t.Setenv("OCR_LLM_PROTOCOL", "anthropic")
				t.Setenv("OCR_LLM_AUTH_HEADER", "x-api-key")
				captureDelegateStdout(t, func() {
					if err := executeReview(reviewOptions{repoDir: repo, reviewPlugin: "example-review", preview: true, audience: "agent"}); err != nil {
						t.Error(err)
					}
				})
				mu.Lock()
				count := requests
				mu.Unlock()
				if count != 0 {
					t.Fatal("preview called model")
				}
				var runErr error
				output := captureDelegateStdout(t, func() {
					runErr = executeReview(reviewOptions{repoDir: repo, reviewPlugin: "example-review", outputFormat: "json", audience: "agent", concurrency: 1, maxGitProcs: 2, effort: "low", noFilter: true})
				})
				var result jsonOutput
				if err := json.Unmarshal([]byte(output), &result); err != nil {
					t.Fatalf("output: %v\n%s", err, output)
				}
				wantCount := len(stages)
				wantState := session.StateComplete
				if incomplete {
					wantState, wantCount = session.StateFailed, 1
				}
				if (runErr != nil) != incomplete || result.Manifest == nil || result.Manifest.TerminalState != wantState || len(result.ReviewPlugin) != wantCount {
					t.Fatalf("error=%v result=%+v", runErr, result)
				}
				mu.Lock()
				got := slices.Clone(visited)
				mu.Unlock()
				if !reflect.DeepEqual(got, stages[:wantCount]) {
					t.Fatalf("stage plan: %v", got)
				}
				items, err := store.List()
				if err != nil || len(items) != 1 || items[0].Enabled {
					t.Fatalf("saved state changed: %+v %v", items, err)
				}
			})
		}
	}
}

func TestCLIReviewProcess(t *testing.T) {
	i := slices.Index(os.Args, "cli-review-helper")
	if i < 0 {
		return
	}
	incomplete := os.Args[i+1] == "true"
	lines := bufio.NewScanner(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	for lines.Scan() {
		var req plugin.ReviewRequest
		if json.Unmarshal(lines.Bytes(), &req) != nil {
			os.Exit(2)
		}
		res := plugin.ReviewResponse{Version: 1}
		if req.Method == "begin" {
			res.Task = &plugin.ReviewTask{Instructions: "Inspect the selected code.", Input: "Workflow stage: " + req.Stage, Completion: plugin.ReviewCompletion{Description: "Submit the check result.", Parameters: map[string]any{"type": "object", "properties": map[string]any{"checked": map[string]any{"type": "boolean"}}, "required": []string{"checked"}}}}
		} else {
			accepted, complete := true, !incomplete
			res.Accepted, res.Complete = &accepted, &complete
			res.Report = json.RawMessage(`{"custom_check":true}`)
			if incomplete {
				res.Feedback = "Required inspection is unavailable."
			}
		}
		if enc.Encode(res) != nil {
			os.Exit(3)
		}
	}
	os.Exit(0)
}
