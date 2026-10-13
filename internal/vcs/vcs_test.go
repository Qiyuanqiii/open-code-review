// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package vcs_test

import (
	"errors"
	"testing"

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
