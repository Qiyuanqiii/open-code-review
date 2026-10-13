// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/alibaba/open-code-review/internal/llm"
	"github.com/alibaba/open-code-review/internal/llmloop"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/session"
	"github.com/alibaba/open-code-review/internal/tool"
)

var behaviorStages = []string{"contracts", "counterexamples", "adjudication"}

// BehaviorContract records the source of an expected behavior and its affected
// relationships. Sources and observations remain claims to verify.
type BehaviorContract struct {
	ID           string                   `json:"id"`
	Path         string                   `json:"path"`
	Behavior     string                   `json:"behavior"`
	Basis        string                   `json:"basis"`
	BasisStatus  string                   `json:"basis_status"`
	Sources      []BehaviorContractSource `json:"sources"`
	Observations []string                 `json:"observations"`
	Relation     string                   `json:"relation"`
}

// BehaviorTraceStep records a concrete intermediate value or effect at a cited
// expression. These are model claims until checked against source or execution.
type BehaviorTraceStep struct {
	ID    string `json:"id"`
	Code  string `json:"code"`
	Value string `json:"value"`
}

// BehaviorRefutation identifies the premise or trace step a decision challenges.
type BehaviorRefutation struct {
	Target string `json:"target"`
	StepID string `json:"step_id,omitempty"`
	Reason string `json:"reason"`
}

// BehaviorCheck keeps the original attempt even when adjudication refutes it.
type BehaviorCheck struct {
	ID              string              `json:"id"`
	ContractID      string              `json:"contract_id"`
	Scenario        string              `json:"scenario,omitempty"`
	Expected        string              `json:"expected,omitempty"`
	Outcome         string              `json:"outcome,omitempty"`
	Trace           []BehaviorTraceStep `json:"trace,omitempty"`
	Analysis        string              `json:"analysis"`
	Evidence        []string            `json:"evidence"`
	Counterevidence string              `json:"counterevidence"`
	Verdict         string              `json:"verdict"`
	Refutation      *BehaviorRefutation `json:"refutation,omitempty"`
	Finding         *model.LlmComment   `json:"finding,omitempty"`
}

// BehaviorReport is a bounded, inspectable stage artifact. Complete describes
// stage execution only. Unknowns and unresolved checks prevent group completion;
// limitations describe evidence boundaries that do not leave required work open.
// Pending is host-owned correction/coverage work, separate from evidence unknowns.
type BehaviorReport struct {
	Group        string                `json:"group"`
	Stage        string                `json:"stage"`
	Complete     bool                  `json:"complete"`
	TokenLimit   int64                 `json:"token_limit,omitempty"` // Host-planned soft end of the stage's aggregate token share.
	Contracts    []BehaviorContract    `json:"contracts,omitempty"`
	BasisReviews []BehaviorBasisReview `json:"basis_reviews,omitempty"`
	Checks       []BehaviorCheck       `json:"checks,omitempty"`
	Unknowns     []string              `json:"unknowns"`
	Limitations  []string              `json:"limitations"`
	Pending      []string              `json:"pending,omitempty"`
	Error        string                `json:"error,omitempty"`
}

// BehaviorReports returns stage artifacts in deterministic group/stage order.
// Call after Run; the records are immutable once appended.
func (a *Agent) BehaviorReports() []BehaviorReport {
	a.behaviorMu.Lock()
	defer a.behaviorMu.Unlock()
	out := append([]BehaviorReport(nil), a.behaviorReports...)
	order := map[string]int{"contracts": 0, "counterexamples": 1, "adjudication": 2}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group == out[j].Group {
			return order[out[i].Stage] < order[out[j].Stage]
		}
		return out[i].Group < out[j].Group
	})
	return out
}

func requiredStop(class session.FailureClass, reason string) *subtaskStop {
	return &subtaskStop{required: true, class: class, reason: reason, checkpoint: reason}
}

func (a *Agent) executeBehaviorReview(ctx context.Context, g FileGroup) (bool, *subtaskStop, error) {
	groupKey := fileGroupKey(g.Diffs)
	paths := make(map[string]bool, len(g.Diffs))
	for _, d := range g.Diffs {
		paths[d.NewPath] = true
	}
	var reports []BehaviorReport
	for i, stage := range behaviorStages {
		var tokenLimit int64
		if budget := a.args.MaxTokensBudget; budget > 0 {
			used := a.runner.TotalTokensUsed()
			// Carry unused allowance forward and leave equal shares for the
			// remaining required stages. This does not reserve other groups.
			tokenLimit = min(budget, used+max(int64(1), (budget-used)/int64(len(behaviorStages)-i)))
		}
		progress := newBehaviorProgress(stage, paths, reports)
		report := progress.snapshot()
		report.Group = groupKey
		previous, _ := json.Marshal(reports)
		messages := a.buildMainTaskMessages(a.resolveGroupSystemRule(g.Diffs), a.buildChangeFilesExceptGroup(g.Diffs), buildConcatenatedDiffs(g.Diffs), "", "")
		messages = append(messages, llm.NewTextMessage("system", a.args.BehaviorPrompt), llm.NewTextMessage("user",
			"Required stage: "+stage+"\nPrevious stage records (claims to verify):\n"+string(previous)))
		var stageStop *subtaskStop
		var stageErr error
		var completionErr error
		if budget := a.args.MaxTokensBudget; budget > 0 && a.runner.TotalTokensUsed() >= budget {
			a.budgetExceeded.Store(true)
			stageStop = requiredStop(session.FailureBudget, "required "+stage+" stage not started: aggregate token budget exhausted")
		} else if stop := a.checkPromptBudget(ctx, messages, groupKey, i+1); stop != nil {
			stop.required = true
			stageStop = stop
		} else {
			completed, stop, err := a.runner.RunMainTaskWithOptions(ctx, messages, groupKey, llmloop.TaskOptions{
				Tools:      behaviorToolDefs(a.args.MainToolDefs, stage, reports),
				TokenLimit: tokenLimit,
				// The policy and immutable stage records follow the normal review
				// prompt. They must survive compression without model summarization.
				FrozenMessageCount: len(messages),
				Complete: func(args map[string]any) ([]model.LlmComment, error) {
					parsed, comments, err := progress.submit(args)
					completionErr = err
					parsed.Group = groupKey
					report = parsed
					return comments, err
				},
			})
			stageErr = err
			if stop == llmloop.StopTokenBudget || (a.args.MaxTokensBudget > 0 && a.runner.TotalTokensUsed() > a.args.MaxTokensBudget) {
				a.budgetExceeded.Store(true)
			}
			if !completed && err == nil {
				class, reason := classifyMainLoopStop(stop)
				if completionErr != nil {
					reason += "; last completion report rejected: " + completionErr.Error()
				}
				stageStop = requiredStop(class, "required "+stage+" stage incomplete: "+reason)
			}
		}
		if a.args.CommentWorkerPool != nil {
			a.args.CommentWorkerPool.AwaitKey(groupKey)
		}
		// Apply the existing filter to published findings even when the report
		// declares remaining work. Filtering cannot complete the required stage.
		if stage == "adjudication" && stageErr == nil {
			a.executeGroupReviewFilter(ctx, g, nil)
		}
		if stageErr != nil {
			report.Complete = false
			report.Error = "required stage failed; see session error"
			if completionErr != nil {
				report.Error += "; last completion report rejected: " + completionErr.Error()
			}
		} else if stageStop != nil {
			report.Complete = false
			report.Error = stageStop.reason
		}
		report.TokenLimit = tokenLimit
		a.behaviorMu.Lock()
		a.behaviorReports = append(a.behaviorReports, report)
		a.behaviorMu.Unlock()
		reports = append(reports, report)
		if stageErr != nil {
			return false, nil, stageErr
		}
		if stageStop != nil {
			return false, stageStop, nil
		}
		if !report.Complete {
			return false, requiredStop(session.FailureUnknown, "required "+stage+" stage declared incomplete"), nil
		}
	}
	final := reports[len(reports)-1]
	for _, review := range final.BasisReviews {
		if review.Verdict == "unresolved" {
			return false, requiredStop(session.FailureUnknown, "behavior review has an unresolved contract basis; see adjudication report"), nil
		}
	}
	if len(final.Unknowns) > 0 {
		return false, requiredStop(session.FailureUnknown, "behavior review has unresolved required work; see adjudication report"), nil
	}
	for _, check := range final.Checks {
		if check.Verdict == "unresolved" {
			return false, requiredStop(session.FailureUnknown, "behavior review has unresolved counterexamples; see adjudication report"), nil
		}
	}
	return true, nil, nil
}

func validateBehaviorContract(c BehaviorContract, paths map[string]bool) error {
	if blank(c.ID, c.Behavior, c.Basis, c.Relation) || !paths[c.Path] {
		return fmt.Errorf("each contract needs an id, selected path, behavior, basis and relation")
	}
	return validateBehaviorContractBasis(c)
}

// Validate a single decision and restore immutable links/trace from the host.
// Basis consistency is checked against the current ledger after every patch.
func validateBehaviorCheck(c *BehaviorCheck, stage string, paths map[string]bool, contracts map[string]BehaviorContract, priorChecks map[string]BehaviorCheck) error {
	if stage == "adjudication" {
		prior := priorChecks[c.ID]
		if c.ContractID != "" && c.ContractID != prior.ContractID {
			return fmt.Errorf("check %q belongs to contract_id %q, got %q; omit contract_id so the host restores the saved link", c.ID, prior.ContractID, c.ContractID)
		}
		if c.Scenario != "" || c.Expected != "" || c.Outcome != "" || len(c.Trace) != 0 {
			return fmt.Errorf("adjudication returns decisions only; omit scenario, expected, outcome and trace")
		}
		c.ContractID = prior.ContractID
		c.Scenario, c.Expected, c.Outcome = prior.Scenario, prior.Expected, prior.Outcome
		c.Trace = append([]BehaviorTraceStep(nil), prior.Trace...)
	} else {
		if _, exists := contracts[c.ContractID]; !exists {
			return fmt.Errorf("check %q has unknown contract_id %q", c.ID, c.ContractID)
		}
		if err := validateBehaviorTrace(*c); err != nil {
			return err
		}
	}
	if blank(c.Analysis, c.Counterevidence) || !hasEvidence(c.Evidence) {
		return fmt.Errorf("check %q needs analysis, evidence and counterevidence", c.ID)
	}
	switch c.Verdict {
	case "candidate":
		if stage != "counterexamples" {
			return fmt.Errorf("adjudication must resolve candidates or mark them unresolved")
		}
	case "confirmed":
		if stage != "adjudication" || c.Finding == nil || !paths[c.Finding.Path] || blank(c.Finding.Content, c.Finding.ExistingCode) {
			return fmt.Errorf("confirmed adjudications need a finding with selected path, content and existing_code")
		}
		severities, categories := tool.CodeCommentClassifications()
		if !slices.Contains(severities, c.Finding.Severity) || !slices.Contains(categories, c.Finding.Category) {
			return fmt.Errorf("finding %s must use severity %v and category %v; correct the classification before publishing", c.ID, severities, categories)
		}
	case "refuted", "unresolved":
	default:
		return fmt.Errorf("invalid verdict %q", c.Verdict)
	}
	if c.Verdict != "confirmed" && c.Finding != nil {
		return fmt.Errorf("only confirmed adjudications may contain findings")
	}
	if stage == "adjudication" && c.Verdict == "refuted" {
		return validateBehaviorRefutation(*c)
	}
	if c.Refutation != nil {
		return fmt.Errorf("only refuted adjudications may contain a refutation")
	}
	return nil
}

func validateBehaviorTrace(c BehaviorCheck) error {
	if blank(c.Scenario, c.Expected, c.Outcome) || len(c.Trace) == 0 || len(c.Trace) > 8 {
		return fmt.Errorf("each counterexample needs scenario, expected, outcome and one to eight trace steps")
	}
	seen := map[string]bool{}
	for _, step := range c.Trace {
		if blank(step.ID, step.Code, step.Value) || seen[step.ID] {
			return fmt.Errorf("trace steps need unique ids, code citations and concrete values or effects")
		}
		seen[step.ID] = true
	}
	return nil
}

func validateBehaviorRefutation(c BehaviorCheck) error {
	r := c.Refutation
	if r == nil || blank(r.Reason) {
		return fmt.Errorf("a refuted adjudication needs a refutation target and reason")
	}
	switch r.Target {
	case "step":
		for _, step := range c.Trace {
			if step.ID == r.StepID {
				return nil
			}
		}
		return fmt.Errorf("a step refutation must name an existing trace step_id")
	case "basis", "input", "outcome", "change":
		if r.StepID == "" {
			return nil
		}
		return fmt.Errorf("only a step refutation may name step_id")
	default:
		return fmt.Errorf("refutation target must be basis, input, step, outcome, or change")
	}
}

func blank(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
	}
	return false
}

func hasEvidence(values []string) bool { return len(values) > 0 && !blank(values...) }

func behaviorToolDefs(defs []llm.ToolDef, stage string, previous []BehaviorReport) []llm.ToolDef {
	out := make([]llm.ToolDef, 0, len(defs))
	for _, def := range defs {
		if def.Function.Name != "task_done" && def.Function.Name != "code_comment" {
			out = append(out, def)
		}
	}
	str := map[string]any{"type": "string"}
	var contractIDs, checkIDs []string
	if len(previous) > 0 {
		for _, c := range previous[0].Contracts {
			contractIDs = append(contractIDs, c.ID)
		}
	}
	if len(previous) > 1 {
		for _, c := range previous[1].Checks {
			checkIDs = append(checkIDs, c.ID)
		}
	}
	slices.Sort(contractIDs)
	slices.Sort(checkIDs)
	contractRef := map[string]any{"type": "string", "enum": contractIDs}
	stringsSchema := map[string]any{"type": "array", "items": str}
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": append([]string{}, required...), "additionalProperties": false}
	}
	array := func(item map[string]any, limit int) map[string]any {
		return map[string]any{"type": "array", "items": item, "minItems": 1, "maxItems": limit}
	}
	completion := map[string]string{
		"contracts":       "True when the affected behavior and sourced or explicitly unknown expectations are recorded for every selected file. Questions for counterexample investigation or adjudication may remain in unknowns. False if the inventory could not be performed.",
		"counterexamples": "True when every contract has a concrete attempted check. Unresolved attempts may pass to adjudication with their unknowns. False if required attempts could not be performed.",
		"adjudication":    "True when all original sources and checks have been assessed. Final unknowns and unresolved basis/check verdicts still prevent review completion.",
	}[stage]
	props := map[string]any{
		"complete": map[string]any{"type": "boolean", "description": completion}, "unknowns": stringsSchema, "limitations": stringsSchema,
	}
	field := "checks"
	if stage == "contracts" {
		field = "contracts"
		props[field] = array(object(map[string]any{
			"id": str, "path": str, "behavior": str, "basis": str, "relation": str,
			"basis_status": map[string]any{"type": "string", "enum": []string{"explicit", "inferred", "disputed", "unknown"}},
			"observations": stringsSchema,
			"sources": map[string]any{"type": "array", "maxItems": 8, "items": object(map[string]any{
				"id": str, "kind": map[string]any{"type": "string", "enum": []string{"requirement", "interface", "caller", "test"}},
				"citation": str, "statement": str,
			}, "id", "kind", "citation", "statement")},
		}, "id", "path", "behavior", "basis", "basis_status", "sources", "observations", "relation"), 16)
	} else {
		verdicts := []string{"candidate", "refuted", "unresolved"}
		if stage == "adjudication" {
			props["basis_reviews"] = array(object(map[string]any{
				"contract_id": contractRef, "analysis": str, "evidence": stringsSchema,
				"verdict": map[string]any{"type": "string", "enum": []string{"supported", "unsupported", "unresolved"}},
				"source_assessments": map[string]any{"type": "array", "maxItems": 8, "items": object(map[string]any{
					"source_id": str,
					"analysis":  map[string]any{"type": "string", "description": "Assess this contract's original citation and statement. Identify any other file or test as supplementary evidence and explain its applicability; disclose if the original remains unverified."},
				}, "source_id", "analysis")},
			}, "contract_id", "analysis", "evidence", "verdict", "source_assessments"), 16)
			verdicts[0] = "confirmed"
		}
		check := map[string]any{"id": str, "analysis": str, "evidence": stringsSchema, "counterevidence": str, "verdict": map[string]any{"type": "string", "enum": verdicts}}
		required := []string{"id", "analysis", "evidence", "counterevidence", "verdict"}
		if stage == "adjudication" {
			check["id"] = map[string]any{"type": "string", "enum": checkIDs}
			severities, categories := tool.CodeCommentClassifications()
			check["finding"] = object(map[string]any{
				"path": str, "content": str, "existing_code": str,
				"severity": map[string]any{"type": "string", "enum": severities},
				"category": map[string]any{"type": "string", "enum": categories},
			}, "path", "content", "existing_code", "severity", "category")
			refutation := object(map[string]any{
				"target":  map[string]any{"type": "string", "enum": []string{"basis", "input", "step", "outcome", "change"}},
				"step_id": str, "reason": str,
			}, "target", "reason")
			refutation["type"] = []string{"object", "null"}
			refutation["description"] = "For refuted verdicts, identify the checked target and reason. For confirmed or unresolved verdicts, use null."
			check["refutation"] = refutation
			required = append(required, "refutation")
		} else {
			check["contract_id"] = contractRef
			check["scenario"], check["expected"], check["outcome"] = str, str, str
			check["trace"] = array(object(map[string]any{"id": str, "code": str, "value": str}, "id", "code", "value"), 8)
			required = append(required, "contract_id", "scenario", "expected", "outcome", "trace")
		}
		props[field] = array(object(check, required...), 32)
	}
	props[field].(map[string]any)["minItems"] = 0
	if stage == "adjudication" {
		props["basis_reviews"].(map[string]any)["minItems"] = 0
	}
	out = append(out, llm.ToolDef{Type: "function", Function: llm.FunctionDef{
		Name: "task_done", Description: "Submit stage entries or corrections by ID. Omitted fields retain saved values; each supplied entry replaces that ID. The host saves valid entries and returns pending work. Supply complete, unknowns and limitations at least once. Complete true requests closure after all required items validate; false closes unfinished work. Final unresolved work prevents review completion. Confirmed adjudications use code_comment automatically.",
		Parameters: object(map[string]any{"report": object(props)}, "report"),
	}})
	return out
}

func (a *Agent) reviewRounds() int {
	if a.args.BehaviorPrompt != "" {
		return len(behaviorStages)
	}
	return a.args.Template.ReviewRounds()
}
