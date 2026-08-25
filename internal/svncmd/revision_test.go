// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import (
	"strings"
	"testing"
)

func TestValidateRevision(t *testing.T) {
	for _, revision := range []string{"0", "42", "r42", "R42", "HEAD", "head", "{2026-08-25T10:00:00Z}"} {
		if err := ValidateRevision(revision); err != nil {
			t.Errorf("ValidateRevision(%q): %v", revision, err)
		}
	}
	for _, revision := range []string{"", " 42", "42 ", "-r42", "41:42", "BASE", "PREV", "COMMITTED", "{bad{date}}", "feature"} {
		if err := ValidateRevision(revision); err == nil {
			t.Errorf("ValidateRevision(%q) unexpectedly succeeded", revision)
		}
	}
}

func TestPreviousRevision(t *testing.T) {
	if got, err := PreviousRevision("42"); err != nil || got != "41" {
		t.Fatalf("PreviousRevision(42) = %q, %v", got, err)
	}
	if got, err := PreviousRevision("0"); err != nil || got != "" {
		t.Fatalf("PreviousRevision(0) = %q, %v", got, err)
	}
	if _, err := PreviousRevision("HEAD"); err == nil {
		t.Fatal("PreviousRevision accepted an unresolved revision")
	}
	if _, err := PreviousRevision(""); err == nil {
		t.Fatal("PreviousRevision accepted an empty revision")
	}
	if _, err := PreviousRevision(strings.Repeat("9", 40)); err == nil {
		t.Fatal("PreviousRevision accepted an overflowing revision")
	}
}

func TestResolveRevisionRejectsInvalidInputWithoutSVN(t *testing.T) {
	if _, err := ResolveRevision(t.Context(), t.TempDir(), "", "1"); err == nil {
		t.Fatal("ResolveRevision accepted an empty repository root")
	}
	if _, err := ResolveRevision(t.Context(), t.TempDir(), "https://example.com/repo", "BASE"); err == nil {
		t.Fatal("ResolveRevision accepted a working-copy-relative revision")
	}
}

func TestChildTargetEscapesPathAndPinsRevision(t *testing.T) {
	got, err := ChildTarget("https://svn.example.com/repo/trunk", "dir/a b@c.go", "17")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://svn.example.com/repo/trunk/dir/a%20b@c.go@17" && got != "https://svn.example.com/repo/trunk/dir/a%20b%40c.go@17" {
		t.Fatalf("ChildTarget = %q", got)
	}
	for _, path := range []string{"../secret", "/absolute", "."} {
		if _, err := ChildTarget("https://svn.example.com/repo", path, "1"); err == nil {
			t.Errorf("ChildTarget accepted %q", path)
		}
	}
}

func TestValidateRevisionRejectsOversizedInput(t *testing.T) {
	if err := ValidateRevision(strings.Repeat("1", 257)); err == nil {
		t.Fatal("oversized revision was accepted")
	}
}
