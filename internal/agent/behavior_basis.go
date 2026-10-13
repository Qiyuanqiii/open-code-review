// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package agent

import (
	"fmt"
	"slices"
	"strings"
)

// BehaviorContractSource identifies wording or a caller/test dependency that
// supports or challenges an obligation. Implementation facts live in observations.
type BehaviorContractSource struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Citation  string `json:"citation"`
	Statement string `json:"statement"`
}

// BehaviorSourceAssessment responds to an immutable contract source.
type BehaviorSourceAssessment struct {
	SourceID string `json:"source_id"`
	Analysis string `json:"analysis"`
}

// BehaviorBasisReview evaluates an obligation once for all its checks. Evidence
// may resolve a previous uncertainty without overwriting the original sources.
type BehaviorBasisReview struct {
	ContractID        string                     `json:"contract_id"`
	SourceAssessments []BehaviorSourceAssessment `json:"source_assessments"`
	Verdict           string                     `json:"verdict"`
	Analysis          string                     `json:"analysis"`
	Evidence          []string                   `json:"evidence"`
}

func validateBehaviorContractBasis(c BehaviorContract) error {
	if c.Sources == nil || len(c.Sources) > 8 || c.Observations == nil || blank(c.Observations...) {
		return fmt.Errorf("contract %s needs sources (at most eight) and nonblank observations; use [] when none", c.ID)
	}
	switch c.BasisStatus {
	case "explicit", "inferred", "disputed":
		if len(c.Sources) == 0 {
			return fmt.Errorf("contract %s needs a source for its %s basis; use unknown when no source is available", c.ID, c.BasisStatus)
		}
	case "unknown":
	default:
		return fmt.Errorf("contract %s basis_status must be explicit, inferred, disputed or unknown", c.ID)
	}
	seen := map[string]bool{}
	for _, source := range c.Sources {
		if blank(source.ID, source.Citation, source.Statement) || seen[source.ID] {
			return fmt.Errorf("contract %s sources need unique ids, citations and source statements", c.ID)
		}
		switch source.Kind {
		case "requirement", "interface", "caller", "test":
		default:
			return fmt.Errorf("source %s kind must be requirement, interface, caller or test; put implementation facts in observations", source.ID)
		}
		seen[source.ID] = true
	}
	return nil
}

func validateBehaviorBasisReviews(reviews []BehaviorBasisReview, contracts map[string]BehaviorContract) (map[string]string, error) {
	verdicts := map[string]string{}
	var omissions []string
	for _, review := range reviews {
		contract, exists := contracts[review.ContractID]
		if !exists || verdicts[review.ContractID] != "" || blank(review.Analysis) || review.SourceAssessments == nil {
			return nil, fmt.Errorf("each basis review needs a unique existing contract_id, analysis and source_assessments")
		}
		if review.Evidence == nil || blank(review.Evidence...) {
			return nil, fmt.Errorf("basis review %s needs evidence; use [] only when unresolved", contract.ID)
		}
		switch review.Verdict {
		case "supported", "unsupported":
			if !hasEvidence(review.Evidence) {
				return nil, fmt.Errorf("basis review %s needs checked evidence for %s", contract.ID, review.Verdict)
			}
		case "unresolved":
		default:
			return nil, fmt.Errorf("basis verdict must be supported, unsupported or unresolved")
		}
		sources := map[string]bool{}
		var sourceIDs []string
		for _, source := range contract.Sources {
			sources[source.ID] = true
			sourceIDs = append(sourceIDs, source.ID)
		}
		slices.Sort(sourceIDs)
		for _, assessment := range review.SourceAssessments {
			if !sources[assessment.SourceID] || blank(assessment.Analysis) {
				return nil, fmt.Errorf("basis review %s has an invalid, repeated or empty assessment for source_id %q; original source_ids: %v; assess each once with an analysis", contract.ID, assessment.SourceID, sourceIDs)
			}
			delete(sources, assessment.SourceID)
		}
		if len(sources) != 0 {
			missing := make([]string, 0, len(sources))
			for id := range sources {
				missing = append(missing, id)
			}
			slices.Sort(missing)
			omissions = append(omissions, fmt.Sprintf("basis review %s omits original source_ids %v", contract.ID, missing))
		}
		verdicts[contract.ID] = review.Verdict
	}
	if len(verdicts) != len(contracts) {
		var missing []string
		for id := range contracts {
			if verdicts[id] == "" {
				missing = append(missing, id)
			}
		}
		slices.Sort(missing)
		omissions = append(omissions, fmt.Sprintf("adjudication omits basis reviews for contract_ids %v", missing))
	}
	if len(omissions) > 0 {
		// A final correction may be the only request left. Report all coverage
		// gaps together while keeping incomplete adjudications unpublished.
		return nil, fmt.Errorf("%s; assess each missing source or contract and resubmit the affected basis_reviews entries", strings.Join(omissions, "; "))
	}
	return verdicts, nil
}

func validateBehaviorBasisVerdict(check BehaviorCheck, basis string) error {
	if check.Verdict == "confirmed" && basis != "supported" {
		return fmt.Errorf("check %s can only confirm a supported obligation", check.ID)
	}
	if check.Verdict == "refuted" && check.Refutation != nil {
		switch check.Refutation.Target {
		case "basis":
			if basis != "unsupported" {
				return fmt.Errorf("check %s needs an unsupported basis review to refute its obligation", check.ID)
			}
		case "outcome":
			if basis != "supported" {
				return fmt.Errorf("check %s needs a supported obligation to refute an outcome mismatch", check.ID)
			}
		}
	}
	return nil
}
