// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
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

	"github.com/alibaba/open-code-review/internal/agent"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/plugin"
	"github.com/alibaba/open-code-review/internal/session"
)

func TestReviewPluginFlagsAndTUI(t *testing.T) {
	opts, err := parseReviewFlags([]string{"--review-plugin", "behavior-review"})
	if err != nil || opts.reviewPlugin != "behavior-review" {
		t.Fatalf("flags: %+v %v", opts, err)
	}
	args := tuiReviewArgs(opts, "localhost:1")
	i := slices.Index(args, "--review-plugin")
	if i < 0 || args[i+1] != "behavior-review" {
		t.Fatalf("subprocess dropped selection: %v", args)
	}
	if _, err := parseReviewFlags([]string{"--review-plugin", "behavior-review", "--resume", "old"}); err == nil {
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
	for _, command := range []string{"/review behavior-review", "/review", "/preview behavior-review", "/review"} {
		next, cmd := m.command(command)
		m = finishTUIReviewFlow(t, next.(reviewTUIModel), cmd)
		if m.opts.reviewPlugin != "" {
			t.Fatal("selection leaked to later commands")
		}
		if m.opts.resume != "remembered-session" {
			t.Fatal("one-off plugin command cleared the saved resume selection")
		}
	}
	if !reflect.DeepEqual(selections, []string{"behavior-review", "", "behavior-review", ""}) {
		t.Fatalf("selections: %v", selections)
	}
	if !reflect.DeepEqual(resumes, []string{"", "remembered-session", "", "remembered-session"}) {
		t.Fatalf("resume selections: %v", resumes)
	}
}

func TestPluginReviewPartialExit(t *testing.T) {
	manifest := &session.RunManifest{TerminalState: session.StatePartial}
	if pluginReviewResultError(nil, manifest, "behavior-review") == nil {
		t.Fatal("incomplete required work exited successfully")
	}
	if err := pluginReviewResultError(nil, manifest, ""); err != nil {
		t.Fatalf("ordinary partial exit changed: %v", err)
	}
	manifest.TerminalState = session.StateComplete
	if err := pluginReviewResultError(nil, manifest, "behavior-review"); err != nil {
		t.Fatal(err)
	}
}

func TestReviewPluginFullFlow(t *testing.T) {
	for _, unresolved := range []bool{false, true} {
		t.Run(fmt.Sprint("unresolved=", unresolved), func(t *testing.T) {
			store := pluginRuntimeHome(t)
			source := t.TempDir()
			for file, text := range map[string]string{
				plugin.ManifestFilename: `{"schema_version":1,"id":"behavior-review","name":"Behavior Review","version":"1","review":{"engine":"behavior-v1","prompt":"policy.md"}}`,
				"policy.md":             "Test invocation policy: check behavior through the required stages.",
			} {
				if err := os.WriteFile(filepath.Join(source, file), []byte(text), 0600); err != nil {
					t.Fatal(err)
				}
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
			var stages []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				var request struct {
					Messages []struct {
						Content json.RawMessage `json:"content"`
					} `json:"messages"`
				}
				if err := json.Unmarshal(body, &request); err != nil {
					t.Error(err)
					http.Error(w, "bad request", 400)
					return
				}
				stage := ""
				for _, message := range request.Messages {
					for _, name := range []string{"contracts", "counterexamples", "adjudication"} {
						if strings.Contains(string(message.Content), "Required stage: "+name) {
							stage = name
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				if stage == "" {
					fmt.Fprint(w, `{"id":"msg","type":"message","role":"assistant","model":"test","content":[{"type":"text","text":"Review main.go"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
					return
				}
				if !strings.Contains(string(body), "Test invocation policy") {
					t.Error("selected policy missing from request")
				}
				mu.Lock()
				stages = append(stages, stage)
				mu.Unlock()
				report := pluginFlowReport(stage, unresolved)
				input, _ := json.Marshal(map[string]any{"report": report})
				fmt.Fprintf(w, `{"id":"msg","type":"message","role":"assistant","model":"test","content":[{"type":"tool_use","id":"done","name":"task_done","input":%s}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":10}}`, input)
			}))
			defer server.Close()
			t.Setenv("OCR_LLM_URL", server.URL+"/v1/messages")
			t.Setenv("OCR_LLM_TOKEN", "test-token")
			t.Setenv("OCR_LLM_MODEL", "test")
			t.Setenv("OCR_LLM_PROTOCOL", "anthropic")
			t.Setenv("OCR_LLM_AUTH_HEADER", "x-api-key")
			var runErr error
			output := captureDelegateStdout(t, func() {
				runErr = executeReview(reviewOptions{repoDir: repo, reviewPlugin: "behavior-review", outputFormat: "json", audience: "agent", concurrency: 1, maxGitProcs: 2, effort: "low", noFilter: true})
			})
			var result jsonOutput
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatalf("output: %v\n%s", err, output)
			}
			if (runErr != nil) != unresolved {
				t.Fatalf("exit did not reflect required work: %v", runErr)
			}
			wantState := session.StateComplete
			if unresolved {
				wantState = session.StateFailed
			}
			if result.Manifest == nil || result.Manifest.TerminalState != wantState || len(result.BehaviorReview) != 3 {
				t.Fatalf("result lost coverage or stages: %+v", result)
			}
			mu.Lock()
			gotStages := append([]string(nil), stages...)
			mu.Unlock()
			if !reflect.DeepEqual(gotStages, []string{"contracts", "counterexamples", "adjudication"}) {
				t.Fatalf("stage order: %v", gotStages)
			}
			items, err := store.List()
			if err != nil || len(items) != 1 || items[0].Enabled {
				t.Fatalf("run changed installation: %+v %v", items, err)
			}
		})
	}
}

func pluginFlowReport(stage string, unresolved bool) agent.BehaviorReport {
	r := agent.BehaviorReport{Stage: stage, Complete: true, Unknowns: []string{}, Limitations: []string{}}
	switch stage {
	case "contracts":
		r.Contracts = []agent.BehaviorContract{{ID: "c1", Path: "main.go", Behavior: "return the configured value", Basis: "caller contract", BasisStatus: "explicit", Sources: []agent.BehaviorContractSource{{ID: "s1", Kind: "interface", Citation: "main.go:value", Statement: "Return one."}}, Observations: []string{"returns one"}, Relation: "caller -> value"}}
	case "counterexamples":
		r.Checks = []agent.BehaviorCheck{{ID: "t1", ContractID: "c1", Scenario: "call value", Expected: "one", Outcome: "one", Trace: []agent.BehaviorTraceStep{{ID: "step1", Code: "main.go:value", Value: "return 1"}}, Analysis: "value is one", Evidence: []string{"main.go:value"}, Counterevidence: "the result satisfies the contract", Verdict: "refuted"}}
	case "adjudication":
		r.BasisReviews = []agent.BehaviorBasisReview{{ContractID: "c1", SourceAssessments: []agent.BehaviorSourceAssessment{{SourceID: "s1", Analysis: "the interface applies"}}, Verdict: "supported", Analysis: "the required result is one", Evidence: []string{"main.go:value"}}}
		r.Checks = []agent.BehaviorCheck{{ID: "t1", Analysis: "one matches the requirement", Evidence: []string{"main.go:value"}, Counterevidence: "the requirement is satisfied", Verdict: "refuted", Refutation: &agent.BehaviorRefutation{Target: "outcome", Reason: "one is the required result"}}}
		if unresolved {
			r.Unknowns = []string{"Required caller condition still needs verification."}
		}
	}
	return r
}
