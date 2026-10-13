// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/alibaba/open-code-review/internal/model"
)

// Each slot keeps its last valid value and any rejected replacement separately.
// Invalid named entries consume the same bounded capacity as accepted entries.
type behaviorEntries[T any] struct {
	order  []string
	values map[string]T
	errors map[string]string
	batch  string
}

func (e *behaviorEntries[T]) apply(raw json.RawMessage, field string, limit int, idOf func(T) string, allowed []string, validate func(*T) error) {
	if raw == nil {
		return
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil || items == nil || len(items) > limit {
		e.batch = fmt.Sprintf("%s must be an array of at most %d entries", field, limit)
		return
	}
	if len(items) == 0 {
		return // Empty patches do not erase entries or acknowledge rejected items.
	}
	if e.values == nil {
		e.values, e.errors = map[string]T{}, map[string]string{}
	}
	counts := map[string]int{}
	for _, raw := range items {
		var value T
		_ = json.Unmarshal(raw, &value)
		counts[idOf(value)]++
	}
	var batchErrors []string
	for _, raw := range items {
		var value T
		err := json.Unmarshal(raw, &value)
		id := idOf(value)
		if blank(id) {
			batchErrors = append(batchErrors, fmt.Sprintf("%s needs a nonempty entry id", field))
			continue
		}
		if allowed != nil && !slices.Contains(allowed, id) {
			batchErrors = append(batchErrors, fmt.Sprintf("%s id %q is unknown; choose a saved reference", field, id))
			continue
		}
		if !slices.Contains(e.order, id) {
			if len(e.order) == limit {
				batchErrors = append(batchErrors, fmt.Sprintf("%s exceeds %d saved IDs; correct existing entries or declare incomplete work", field, limit))
				continue
			}
			e.order = append(e.order, id)
		}
		if counts[id] > 1 {
			err = fmt.Errorf("duplicate id in patch; submit this entry once")
		} else if err == nil {
			err = validate(&value)
		}
		if err != nil {
			e.errors[id] = fmt.Sprintf("%s[%s]: %v", field, id, err)
			continue
		}
		e.values[id] = value
		delete(e.errors, id)
	}
	// Unnamed/out-of-scope entries cannot become obligations. A replacement
	// batch repairs those errors; named errors persist until that ID is fixed.
	e.batch = strings.Join(batchErrors, "; ")
}

func (e *behaviorEntries[T]) saved() []T {
	var values []T
	for _, id := range e.order {
		if value, ok := e.values[id]; ok {
			values = append(values, value)
		}
	}
	return values
}

func (e *behaviorEntries[T]) pending() []string {
	var pending []string
	if e.batch != "" {
		pending = append(pending, e.batch)
	}
	for _, id := range e.order {
		if err := e.errors[id]; err != "" {
			pending = append(pending, err)
		}
	}
	return pending
}

// One accumulator belongs to one stage invocation. Existing session tool
// results retain receipts; the agent emits this snapshot even on loop failure.
type behaviorProgress struct {
	stage        string
	paths        map[string]bool
	prior        map[string]BehaviorContract
	priorChecks  map[string]BehaviorCheck
	contracts    behaviorEntries[BehaviorContract]
	basis        behaviorEntries[BehaviorBasisReview]
	checks       behaviorEntries[BehaviorCheck]
	complete     *bool
	unknowns     []string
	limitations  []string
	headerErrors map[string]string
}

func newBehaviorProgress(stage string, paths map[string]bool, previous []BehaviorReport) *behaviorProgress {
	p := &behaviorProgress{stage: stage, paths: paths, prior: map[string]BehaviorContract{}, priorChecks: map[string]BehaviorCheck{}, headerErrors: map[string]string{}}
	if len(previous) > 0 {
		for _, c := range previous[0].Contracts {
			p.prior[c.ID] = c
		}
	}
	if len(previous) > 1 {
		for _, c := range previous[1].Checks {
			p.priorChecks[c.ID] = c
		}
	}
	return p
}

func (p *behaviorProgress) submit(args map[string]any) (BehaviorReport, []model.LlmComment, error) {
	data, err := json.Marshal(args["report"])
	var fields map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(data, &fields)
	}
	if err != nil || fields == nil {
		p.headerErrors["report"] = "report must be an object containing stage entries or corrections"
	} else {
		delete(p.headerErrors, "report")
		p.applyHeaders(fields)
		p.contracts.apply(fields["contracts"], "contracts", 16, func(c BehaviorContract) string { return c.ID }, nil, func(c *BehaviorContract) error {
			return validateBehaviorContract(*c, p.paths)
		})
		p.basis.apply(fields["basis_reviews"], "basis_reviews", 16, func(b BehaviorBasisReview) string { return b.ContractID }, append([]string{}, slices.Sorted(maps.Keys(p.prior))...), func(b *BehaviorBasisReview) error {
			_, err := validateBehaviorBasisReviews([]BehaviorBasisReview{*b}, map[string]BehaviorContract{b.ContractID: p.prior[b.ContractID]})
			return err
		})
		var allowed []string
		if p.stage == "adjudication" {
			allowed = slices.Sorted(maps.Keys(p.priorChecks))
			if allowed == nil {
				allowed = []string{}
			}
		}
		p.checks.apply(fields["checks"], "checks", 32, func(c BehaviorCheck) string { return c.ID }, allowed, func(c *BehaviorCheck) error {
			return validateBehaviorCheck(c, p.stage, p.paths, p.prior, p.priorChecks)
		})
	}
	report := p.snapshot()
	// Explicit false closes partial work, preserving pending entries. Missing
	// or malformed headers remain protocol errors, never implicit false.
	closed := p.complete != nil && (!*p.complete || len(report.Pending) == 0) && len(p.metadataPending()) == 0
	if !closed {
		choices := ""
		if p.stage != "contracts" {
			choices = fmt.Sprintf("; valid contract_ids: %v", slices.Sorted(maps.Keys(p.prior)))
		}
		if p.stage == "adjudication" {
			choices += fmt.Sprintf("; valid check ids: %v", slices.Sorted(maps.Keys(p.priorChecks)))
		}
		return report, nil, fmt.Errorf("saved contracts %v (this stage only), basis_reviews %v, checks %v; pending: %s%s; submit only corrected or missing entries/fields (each entry in full)",
			slices.Sorted(maps.Keys(p.contracts.values)), slices.Sorted(maps.Keys(p.basis.values)), slices.Sorted(maps.Keys(p.checks.values)), strings.Join(report.Pending, "; "), choices)
	}
	var comments []model.LlmComment
	for _, c := range report.Checks {
		if c.Verdict == "confirmed" && p.checks.batch == "" && p.basis.batch == "" && p.checks.errors[c.ID] == "" && p.basis.errors[c.ContractID] == "" && p.decisionError(c) == nil {
			comments = append(comments, *c.Finding)
		}
	}
	return report, comments, nil
}

func (p *behaviorProgress) applyHeaders(fields map[string]json.RawMessage) {
	// These envelope errors describe the current patch; omitted forbidden
	// fields need no model-owned state and cannot invalidate saved entries.
	for _, key := range []string{"stage", "contracts", "basis_reviews", "checks"} {
		delete(p.headerErrors, key)
	}
	for _, key := range []string{"complete", "unknowns", "limitations", "stage"} {
		raw, present := fields[key]
		if !present {
			continue
		}
		var err error
		switch key {
		case "complete":
			var value *bool
			if err = json.Unmarshal(raw, &value); err == nil && value != nil {
				p.complete = value
			} else {
				err = fmt.Errorf("must be a boolean")
			}
		case "stage":
			var value string
			if err = json.Unmarshal(raw, &value); err == nil && value != p.stage {
				err = fmt.Errorf("must match assigned stage %s", p.stage)
			}
		default:
			var value []string
			if err = json.Unmarshal(raw, &value); err == nil && value != nil && !blank(value...) {
				if key == "unknowns" {
					p.unknowns = value
				} else {
					p.limitations = value
				}
			} else {
				err = fmt.Errorf("must be an array of nonblank strings; use [] when none")
			}
		}
		if err != nil {
			p.headerErrors[key] = fmt.Sprintf("report.%s %v", key, err)
		} else {
			delete(p.headerErrors, key)
		}
	}
	// Stage-inapplicable collections never enter the stage's accumulator.
	for _, key := range []string{"contracts", "basis_reviews", "checks"} {
		allowed := (key == "contracts" && p.stage == "contracts") || (key == "basis_reviews" && p.stage == "adjudication") || (key == "checks" && p.stage != "contracts")
		if raw, present := fields[key]; present && !allowed {
			var empty []json.RawMessage
			if err := json.Unmarshal(raw, &empty); err != nil || len(empty) != 0 {
				p.headerErrors[key] = fmt.Sprintf("%s stage cannot contain %s", p.stage, key)
			} else {
				delete(p.headerErrors, key)
			}
			delete(fields, key)
		}
	}
}

func (p *behaviorProgress) metadataPending() []string {
	var pending []string
	for _, key := range slices.Sorted(maps.Keys(p.headerErrors)) {
		pending = append(pending, p.headerErrors[key])
	}
	if p.complete == nil {
		pending = append(pending, "report.complete is required and must be a boolean")
	}
	if p.unknowns == nil || p.limitations == nil {
		pending = append(pending, "report must include unknowns and limitations arrays (use [] when none)")
	}
	return pending
}

func (p *behaviorProgress) decisionError(c BehaviorCheck) error {
	if _, present := p.basis.values[c.ContractID]; !present || p.basis.errors[c.ContractID] != "" {
		return fmt.Errorf("check %s needs a valid basis review for contract_id %s", c.ID, c.ContractID)
	}
	return validateBehaviorBasisVerdict(c, p.basis.values[c.ContractID].Verdict)
}

func (p *behaviorProgress) snapshot() BehaviorReport {
	r := BehaviorReport{Stage: p.stage, Contracts: p.contracts.saved(), BasisReviews: p.basis.saved(), Checks: p.checks.saved(), Unknowns: append([]string{}, p.unknowns...), Limitations: append([]string{}, p.limitations...)}
	r.Pending = append(r.Pending, p.metadataPending()...)
	r.Pending = append(r.Pending, p.contracts.pending()...)
	r.Pending = append(r.Pending, p.basis.pending()...)
	r.Pending = append(r.Pending, p.checks.pending()...)
	covered := map[string]bool{}
	if p.stage == "contracts" {
		for _, c := range r.Contracts {
			covered[c.Path] = true
		}
		for _, path := range slices.Sorted(maps.Keys(p.paths)) {
			if !covered[path] {
				r.Pending = append(r.Pending, fmt.Sprintf("contracts omit selected path %s", path))
			}
		}
	} else {
		if len(p.prior) == 0 || (p.stage == "adjudication" && len(p.priorChecks) == 0) {
			r.Pending = append(r.Pending, "checks require the preceding stages")
		}
		for _, c := range r.Checks {
			covered[c.ContractID] = true
			if p.stage == "adjudication" {
				if err := p.decisionError(c); err != nil {
					r.Pending = append(r.Pending, err.Error())
				}
			}
		}
		var missingContracts, missingChecks, missingBasis []string
		for _, id := range slices.Sorted(maps.Keys(p.prior)) {
			if !covered[id] {
				missingContracts = append(missingContracts, id)
			}
			if _, present := p.basis.values[id]; !present && p.stage == "adjudication" {
				missingBasis = append(missingBasis, id)
			}
		}
		for _, id := range slices.Sorted(maps.Keys(p.priorChecks)) {
			if _, present := p.checks.values[id]; !present {
				missingChecks = append(missingChecks, id)
			}
		}
		if len(missingContracts)+len(missingChecks) > 0 {
			r.Pending = append(r.Pending, fmt.Sprintf("checks omit contract_ids %v and prior check ids %v", missingContracts, missingChecks))
		}
		if len(missingBasis) > 0 {
			r.Pending = append(r.Pending, fmt.Sprintf("adjudication omits basis reviews for contract_ids %v", missingBasis))
		}
	}
	r.Complete = p.complete != nil && *p.complete && len(r.Pending) == 0
	return r
}
