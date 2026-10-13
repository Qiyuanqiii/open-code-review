// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"testing"

	"github.com/alibaba/open-code-review/internal/config/toolsconfig"
	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/session"
	"github.com/alibaba/open-code-review/internal/tool"
)

func classifiedBehaviorDecision(severity, category string) BehaviorReport {
	r := behaviorFixtures()[2]
	r.Checks[0].Verdict, r.Checks[0].Refutation = "confirmed", nil
	r.Checks[0].Finding = &model.LlmComment{Path: "good.go", Content: "The returned value differs from the stored value.", ExistingCode: "value", Severity: severity, Category: category}
	return r
}

func TestBehaviorClassificationsMatchNativeTool(t *testing.T) {
	readEnum := func(root map[string]any, path ...string) []string {
		t.Helper()
		var value any = root
		for _, key := range path {
			value = value.(map[string]any)[key]
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var values []string
		if err := json.Unmarshal(data, &values); err != nil {
			t.Fatal(err)
		}
		slices.Sort(values)
		return values
	}
	entries, err := toolsconfig.Load("")
	if err != nil {
		t.Fatal(err)
	}
	var native llm.FunctionDef
	for _, entry := range entries {
		if entry.Name == "code_comment" {
			if err := json.Unmarshal(entry.Definition, &native); err != nil {
				t.Fatal(err)
			}
		}
	}
	behavior := behaviorToolDefs(nil, "adjudication", behaviorFixtures()[:2])[0].Function
	for _, field := range []string{"severity", "category"} {
		want := readEnum(native.Parameters, "properties", "comments", "items", "properties", field, "enum")
		got := readEnum(behavior.Parameters, "properties", "report", "properties", "checks", "items", "properties", "finding", "properties", field, "enum")
		if len(want) == 0 || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s classification drift: got %v want %v", field, got, want)
		}
	}
}

func TestBehaviorRejectsLossyClassification(t *testing.T) {
	for _, pair := range [][2]string{{"moderate", "bug"}, {"medium", "correctness"}, {"", "bug"}, {"medium", ""}} {
		t.Run(pair[0]+"/"+pair[1], func(t *testing.T) {
			reports := behaviorFixtures()
			r := classifiedBehaviorDecision(pair[0], pair[1])
			if _, _, err := parseBehaviorReport(reportArgs(r), "adjudication", map[string]bool{"good.go": true}, reports[:2]); err == nil {
				t.Fatal("classification requiring a fallback was accepted")
			}
		})
	}
}

func TestBehaviorClassificationRepairBeforePublication(t *testing.T) {
	for _, repaired := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrepaired", true: "repaired"}[repaired], func(t *testing.T) {
			reports := behaviorFixtures()
			reports[1].Checks[0].Verdict = "candidate"
			reports[1].Checks[0].Outcome = "The caller receives seven for stored value zero."
			reports[1].Checks[0].Trace[0].Value = "return 7"
			reports[2] = classifiedBehaviorDecision("moderate", "correctness")
			if repaired {
				reports = append(reports, classifiedBehaviorDecision("medium", "bug"))
			}
			client := &behaviorClient{reports: reports}
			a := newManifestFlowAgentWithClient(t, []model.Diff{{NewPath: "good.go", Diff: "+value", Insertions: 1}}, nil, client)
			a.args.BehaviorPrompt = behaviorPrompt
			a.args.Template.MaxReviewRounds = 3
			a.args.Tools.Register(&tool.CodeCommentProvider{Collector: a.args.CommentCollector})
			comments, _ := a.dispatchSubtasks(context.Background())
			manifest := finishManifestFlow(t, a)
			if repaired {
				if client.calls != 4 || manifest.TerminalState != session.StateComplete || len(comments) != 1 {
					t.Fatalf("repair/publication failed: calls=%d state=%s comments=%v", client.calls, manifest.TerminalState, comments)
				}
				if comments[0].Severity != "medium" || comments[0].Category != "bug" {
					t.Fatalf("confirmed classification changed in publication: %+v", comments[0])
				}
			} else if manifest.TerminalState != session.StateFailed || len(comments) != 0 {
				t.Fatalf("invalid classification published or completed: state=%s comments=%v", manifest.TerminalState, comments)
			}
		})
	}
}
