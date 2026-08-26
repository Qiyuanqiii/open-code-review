// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import (
	"strings"
	"testing"
)

func TestParseTargetSpec(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantTarget string
		wantPeg    string
	}{
		{name: "https numeric peg", raw: "https://svn.example.com/repos/app/trunk@17", wantTarget: "https://svn.example.com/repos/app/trunk", wantPeg: "17"},
		{name: "moving peg", raw: "svn+ssh://svn.example.com/repos/app/branches/feature@HEAD", wantTarget: "svn+ssh://svn.example.com/repos/app/branches/feature", wantPeg: "HEAD"},
		{name: "date peg", raw: "svn://svn.example.com/app/trunk@{2026-08-26 12:00:00 +0800}", wantTarget: "svn://svn.example.com/app/trunk", wantPeg: "{2026-08-26 12:00:00 +0800}"},
		{name: "literal at escape", raw: "https://svn.example.com/repos/app/branch@name@", wantTarget: "https://svn.example.com/repos/app/branch@name"},
		{name: "repository relative", raw: "^/branches/feature@9", wantTarget: "^/branches/feature", wantPeg: "9"},
		{name: "repository root", raw: "^/@9", wantTarget: "^/", wantPeg: "9"},
		{name: "normalized path", raw: "https://SVN.EXAMPLE.COM/repos/app/branches/../trunk/@9", wantTarget: "https://svn.example.com/repos/app/trunk", wantPeg: "9"},
		{name: "file URL", raw: "file:///tmp/repository/trunk@3", wantTarget: "file:///tmp/repository/trunk", wantPeg: "3"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseTargetSpec(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got.Target != test.wantTarget || got.PegRevision != test.wantPeg {
				t.Fatalf("ParseTargetSpec = %+v, want target %q peg %q", got, test.wantTarget, test.wantPeg)
			}
		})
	}
}

func TestParseTargetSpecRejectsUnsafeValuesWithoutEchoingSecrets(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "userinfo", raw: "https://alice:secret@svn.example.com/repo/trunk@7"},
		{name: "query", raw: "https://svn.example.com/repo/trunk?token=secret@7"},
		{name: "fragment", raw: "https://svn.example.com/repo/trunk#secret@7"},
		{name: "option", raw: "--config-option=servers:global:http-proxy-password=secret"},
		{name: "whitespace", raw: " https://svn.example.com/repo/trunk@7"},
		{name: "ambiguous peg", raw: "https://svn.example.com/repo/trunk@BASE"},
		{name: "relative query", raw: "^/trunk?token=secret@7"},
		{name: "relative traversal", raw: "^/../secret@7"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseTargetSpec(test.raw)
			if err == nil {
				t.Fatal("unsafe target was accepted")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "alice") || strings.Contains(err.Error(), test.raw) {
				t.Fatalf("error disclosed target data: %v", err)
			}
		})
	}
}

func TestSameRepositoryPrefersUUIDAndFallsBackToCanonicalRoot(t *testing.T) {
	if !sameRepository(
		ResolvedTarget{RepositoryUUID: "AABB", RepositoryRoot: "https://one.example/repo"},
		ResolvedTarget{RepositoryUUID: "aabb", RepositoryRoot: "https://two.example/repo"},
	) {
		t.Fatal("matching UUIDs were rejected")
	}
	if sameRepository(
		ResolvedTarget{RepositoryUUID: "aabb", RepositoryRoot: "https://svn.example/repo"},
		ResolvedTarget{RepositoryUUID: "ccdd", RepositoryRoot: "https://svn.example/repo"},
	) {
		t.Fatal("different UUIDs were accepted")
	}
	if !sameRepository(
		ResolvedTarget{RepositoryRoot: "HTTPS://SVN.EXAMPLE/repo/"},
		ResolvedTarget{RepositoryRoot: "https://svn.example/repo"},
	) {
		t.Fatal("equivalent credential-free roots were rejected")
	}
}
