// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package vcs_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/vcs"
)

func TestRequestsCannotSilentlySelectAnotherComparison(t *testing.T) {
	// Supplying a base for commit/workspace, or omitting a range endpoint,
	// must never silently change which revisions a provider compares.
	for _, spec := range []vcs.ReviewSpec{
		{Mode: vcs.Workspace, Base: "r1"}, {Mode: vcs.Workspace, Head: "r2"},
		{Mode: vcs.Commit}, {Mode: vcs.Commit, Base: "r1", Head: "r2"},
		{Mode: vcs.MergeBase, Base: "r1"}, {Mode: vcs.MergeBase, Head: "r2"},
		{Mode: vcs.TwoRevisions, Base: "r1"}, {Mode: vcs.TwoRevisions, Head: "r2"},
	} {
		if err := spec.Validate(); err == nil {
			t.Fatalf("accepted ambiguous request %+v", spec)
		}
	}
	for _, spec := range []vcs.ReviewSpec{
		{Mode: vcs.Workspace}, {Mode: vcs.Commit, Head: "r2"},
		{Mode: vcs.MergeBase, Base: "trunk", Head: "branch"},
		{Mode: vcs.TwoRevisions, Base: "r1", Head: "r2"},
	} {
		if err := spec.Validate(); err != nil {
			t.Fatalf("rejected %+v: %v", spec, err)
		}
	}
	if err := (vcs.ReviewSpec{Mode: "index-only"}).Validate(); !errors.Is(err, vcs.ErrUnsupported) {
		t.Fatalf("unsupported mode: %v", err)
	}
	if !(vcs.ContentRef{}).IsWorkspace() || (vcs.ContentRef{Revision: "r2"}).IsWorkspace() {
		t.Fatal("workspace and revision content were confused")
	}
}

func TestChangesPreservesProviderOrder(t *testing.T) {
	changes := vcs.Changes{
		Included: []model.Diff{{NewPath: "a.go"}, {NewPath: "c.go"}, {NewPath: "e.go"}},
		Excluded: []model.Diff{{NewPath: "b.go"}, {NewPath: "d.go"}, {NewPath: "f.go"}},
	}
	for _, test := range []struct {
		name      string
		positions []int
		want      []string
	}{
		{name: "unspecified", want: []string{"excluded:b.go", "excluded:d.go", "excluded:f.go", "included:a.go", "included:c.go", "included:e.go"}},
		{name: "interleaved", positions: []int{1, 3, 5}, want: []string{"included:a.go", "excluded:b.go", "included:c.go", "excluded:d.go", "included:e.go", "excluded:f.go"}},
		{name: "excluded first", positions: []int{0, 1, 2}, want: []string{"excluded:b.go", "excluded:d.go", "excluded:f.go", "included:a.go", "included:c.go", "included:e.go"}},
		{name: "excluded last", positions: []int{3, 4, 5}, want: []string{"included:a.go", "included:c.go", "included:e.go", "excluded:b.go", "excluded:d.go", "excluded:f.go"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			changes.ExcludedAt = test.positions
			var got []string
			changes.ForEachInOrder(func(change model.Diff, excluded bool) {
				kind := "included:"
				if excluded {
					kind = "excluded:"
				}
				got = append(got, kind+change.NewPath)
			})
			if !slices.Equal(got, test.want) {
				t.Fatalf("changeset order = %v, want %v", got, test.want)
			}
		})
	}
}
