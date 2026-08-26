// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import (
	"context"
	"errors"
	"strings"
	"testing"
)

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

func TestParseInfoEntriesRejectsInvalidOrEmptyXML(t *testing.T) {
	for _, data := range [][]byte{[]byte(`<info>`), []byte(`<info/>`)} {
		if _, err := ParseInfoEntries(data); err == nil {
			t.Errorf("ParseInfoEntries(%q) succeeded", data)
		}
	}
}

func TestSafeCommandTextRedactsURLUserinfo(t *testing.T) {
	got := safeCommandText("svn cat https://alice:secret@svn.example.com/repo/file?token=value@7; Authentication failed for 'alice'; username: alice")
	if got != "svn cat https://<redacted>@svn.example.com/repo/file?<redacted> Authentication failed for <redacted>; username: <redacted>" {
		t.Fatalf("safeCommandText = %q", got)
	}
}

func TestSafeCommandArgsRedactsCredentialOptions(t *testing.T) {
	got := safeCommandArgs([]string{"cat", "--username", "alice", "--password=secret", "--config-option", "servers:global:http-proxy-password=secret"})
	for _, secret := range []string{"alice", "secret", "http-proxy-password"} {
		if strings.Contains(got, secret) {
			t.Fatalf("safeCommandArgs disclosed %q in %q", secret, got)
		}
	}
}

func TestParseInfoEntriesIncludesCopyAndMoveMetadata(t *testing.T) {
	data := []byte(`<?xml version="1.0"?>
<info>
  <entry kind="dir" path="." revision="9">
    <url>https://svn.example.com/repos/project/trunk</url>
    <relative-url>^/project/trunk</relative-url>
    <repository><root>https://svn.example.com/repos</root></repository>
    <wc-info><wcroot-abspath>C:/work/project</wcroot-abspath><depth>infinity</depth></wc-info>
  </entry>
  <entry kind="file" path="new path/file.txt" revision="8">
    <url>https://svn.example.com/repos/project/trunk/new%20path/file.txt</url>
    <relative-url>^/project/trunk/new%20path/file.txt</relative-url>
    <repository><root>https://svn.example.com/repos</root></repository>
    <wc-info>
      <wcroot-abspath>C:/work/project</wcroot-abspath><schedule>add</schedule><depth>infinity</depth>
      <copy-from-url>https://svn.example.com/repos/project/trunk/old%20path/file.txt</copy-from-url>
      <copy-from-rev>8</copy-from-rev><moved-from>old path/file.txt</moved-from>
    </wc-info>
  </entry>
</info>`)

	entries, err := ParseInfoEntries(data)
	if err != nil {
		t.Fatalf("ParseInfoEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	got := entries[1]
	if got.Path != "new path/file.txt" || got.Kind != "file" || got.Revision != "8" {
		t.Errorf("entry identity = %+v", got)
	}
	if got.CopyFromURL != "https://svn.example.com/repos/project/trunk/old%20path/file.txt" || got.CopyFromRevision != "8" {
		t.Errorf("copy metadata = %+v", got)
	}
	if got.MovedFrom != "old path/file.txt" || got.Schedule != "add" || got.Depth != "infinity" {
		t.Errorf("working-copy metadata = %+v", got)
	}
}

func TestCommandErrorWithDiagnostic(t *testing.T) {
	baseErr := errors.New("exit status 1")
	got := commandErrorWithDiagnostic(context.Background(), []string{"status", "--xml"}, baseErr, "  useful diagnosis  ")
	if !errors.Is(got, baseErr) || !strings.Contains(got.Error(), "useful diagnosis") || !strings.Contains(got.Error(), "status --xml") {
		t.Fatalf("error = %v", got)
	}

	longDiagnostic := strings.Repeat("x", diagnosticLimit) + "\u754cfailure"
	got = commandErrorWithDiagnostic(context.Background(), []string{"diff"}, baseErr, longDiagnostic)
	if !strings.Contains(got.Error(), "...") || !strings.HasSuffix(got.Error(), "failure") {
		t.Fatalf("bounded diagnostic = %v", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got = commandErrorWithDiagnostic(ctx, nil, baseErr, "diagnosis"); !errors.Is(got, context.Canceled) {
		t.Fatalf("canceled error = %v", got)
	}
}

func TestCommandErrorProvidesNonInteractiveAuthAndCertificateGuidance(t *testing.T) {
	baseErr := errors.New("exit status 1")
	auth := commandErrorWithDiagnostic(context.Background(), []string{"info"}, baseErr, "svn: E170001: Authentication failed")
	if !strings.Contains(auth.Error(), "preconfigure credentials") {
		t.Fatalf("authentication guidance = %v", auth)
	}
	certificate := commandErrorWithDiagnostic(context.Background(), []string{"info"}, baseErr, "svn: E230001: Server SSL certificate verification failed")
	if !strings.Contains(certificate.Error(), "pre-trust the server certificate") {
		t.Fatalf("certificate guidance = %v", certificate)
	}
}
