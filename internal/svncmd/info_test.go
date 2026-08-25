// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import "testing"

func TestParseInfo(t *testing.T) {
	data := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<info>
  <entry kind="dir" path="." revision="42">
    <url>https://svn.example.com/repos/project/trunk</url>
    <relative-url>^/project/trunk</relative-url>
    <repository><root>https://svn.example.com/repos</root><uuid>repo-uuid</uuid></repository>
    <wc-info><wcroot-abspath>C:/work/project</wcroot-abspath></wc-info>
  </entry>
</info>`)

	got, err := ParseInfo(data)
	if err != nil {
		t.Fatalf("ParseInfo: %v", err)
	}
	if got.Root != "C:/work/project" {
		t.Errorf("Root = %q, want C:/work/project", got.Root)
	}
	if got.URL != "https://svn.example.com/repos/project/trunk" {
		t.Errorf("URL = %q", got.URL)
	}
	if got.RepositoryRoot != "https://svn.example.com/repos" {
		t.Errorf("RepositoryRoot = %q", got.RepositoryRoot)
	}
	if got.RelativeURL != "^/project/trunk" {
		t.Errorf("RelativeURL = %q", got.RelativeURL)
	}
	if got.RepositoryUUID != "repo-uuid" || got.Revision != "42" {
		t.Errorf("repository UUID/revision = %q/%q", got.RepositoryUUID, got.Revision)
	}
}

func TestParseInfoRejectsMissingRoot(t *testing.T) {
	_, err := ParseInfo([]byte(`<info><entry><url>https://example.com/repo</url></entry></info>`))
	if err == nil {
		t.Fatal("expected missing working-copy root error")
	}
}

func TestParseInfoRejectsInvalidOrEmptyXML(t *testing.T) {
	for _, data := range []string{"<info>", "<info></info>"} {
		if _, err := ParseInfo([]byte(data)); err == nil {
			t.Errorf("ParseInfo(%q) unexpectedly succeeded", data)
		}
	}
}

func TestSafeCommandTextRedactsURLUserinfo(t *testing.T) {
	got := safeCommandText("svn cat https://alice:secret@svn.example.com/repo/file@7")
	if got != "svn cat https://<redacted>@svn.example.com/repo/file@7" {
		t.Fatalf("safeCommandText = %q", got)
	}
}
