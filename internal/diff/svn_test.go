// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package diff

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/svncmd"
)

func TestSVNProviderGetDiff(t *testing.T) {
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "src", "app.go"), []byte("package main\nfunc changed() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, "extra"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "extra", "helper.go"), []byte("package extra\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tracked := `Index: src/app.go
===================================================================
diff --git a/project/trunk/src/app.go b/project/trunk/src/app.go
--- a/project/trunk/src/app.go (revision 42)
+++ b/project/trunk/src/app.go (working copy)
@@ -1,2 +1,2 @@
 package main
-func old() {}
+func changed() {}
Index: .
===================================================================
diff --git a/project/trunk b/project/trunk
--- a/project/trunk (revision 42)
+++ b/project/trunk (working copy)
Property changes on: .
___________________________________________________________________
Modified: svn:ignore
`
	status := `<?xml version="1.0"?>
<status><target path=".">
  <entry path="extra"><wc-status item="unversioned" props="none"/></entry>
  <entry path="node_modules"><wc-status item="unversioned" props="none"/></entry>
</target></status>`

	provider := NewSVNWorkspaceProvider(repo)
	var calls [][]string
	provider.run = func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, slices.Clone(args))
		switch args[0] {
		case "diff":
			return tracked, nil
		case "status":
			return status, nil
		default:
			return "", errors.New("unexpected svn command")
		}
	}

	diffs, err := provider.GetDiff(context.Background())
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if len(diffs) != 2 {
		t.Fatalf("len(diffs) = %d, want 2: %+v", len(diffs), diffs)
	}
	if diffs[0].NewPath != "src/app.go" || diffs[0].OldPath != "src/app.go" {
		t.Errorf("tracked paths = %q/%q, want src/app.go", diffs[0].OldPath, diffs[0].NewPath)
	}
	if diffs[0].Insertions != 1 || diffs[0].Deletions != 1 {
		t.Errorf("tracked insertions/deletions = %d/%d, want 1/1", diffs[0].Insertions, diffs[0].Deletions)
	}
	if diffs[0].NewFileContent != "package main\nfunc changed() {}\n" {
		t.Errorf("tracked content = %q", diffs[0].NewFileContent)
	}
	if diffs[1].NewPath != "extra/helper.go" || !diffs[1].IsNew {
		t.Errorf("unversioned diff = %+v, want new extra/helper.go", diffs[1])
	}
	if len(calls) != 2 {
		t.Fatalf("svn calls = %v, want diff and status", calls)
	}
	joined := strings.Join(calls[0], " ")
	for _, option := range []string{"--git", "--internal-diff", "--show-copies-as-adds"} {
		if !strings.Contains(joined, option) {
			t.Errorf("svn diff args %q missing %s", joined, option)
		}
	}
}

func TestNormalizeSVNDiffPreservesHunkContent(t *testing.T) {
	raw := "Index: src/app.go\r\n" +
		"===================================================================\r\n" +
		"diff --git a/trunk/src/app.go b/trunk/src/app.go\r\n" +
		"--- a/trunk/src/app.go (revision 1)\r\n" +
		"+++ b/trunk/src/app.go (working copy)\r\n" +
		"@@ -1 +1 @@\r\n" +
		"-old\r\n" +
		"+++ example\r\n"

	got := normalizeSVNDiff(t.TempDir(), raw)
	if !strings.Contains(got, "diff --git a/src/app.go b/src/app.go") {
		t.Errorf("normalized header missing:\n%s", got)
	}
	if !strings.Contains(got, "\n+++ example\n") {
		t.Errorf("hunk content was rewritten as a file header:\n%s", got)
	}
	if strings.Contains(got, "Index:") || strings.Contains(got, "====") {
		t.Errorf("Subversion separators leaked into parser input:\n%s", got)
	}
}

func TestNormalizeSVNDiffDropsPathOutsideWorkingCopy(t *testing.T) {
	raw := `Index: ../outside.go
===================================================================
diff --git a/trunk/outside.go b/trunk/outside.go
--- a/trunk/outside.go
+++ b/trunk/outside.go
@@ -1 +1 @@
-old
+new
`
	if got := normalizeSVNDiff(t.TempDir(), raw); got != "" {
		t.Fatalf("normalizeSVNDiff() = %q, want invalid section dropped", got)
	}
}

func TestSVNContentDiffSectionsDropsPropertiesFromContentChange(t *testing.T) {
	raw := `diff --git a/src/app.go b/src/app.go
--- a/src/app.go
+++ b/src/app.go
@@ -1 +1 @@
-old
+new
Property changes on: src/app.go
___________________________________________________________________
Added: svn:eol-style
## -0,0 +1 ##
+native
`

	got := svnContentDiffSections(raw)
	if !strings.Contains(got, "@@ -1 +1 @@\n-old\n+new") {
		t.Fatalf("content hunk missing:\n%s", got)
	}
	for _, propertyText := range []string{"Property changes on:", "svn:eol-style", "+native"} {
		if strings.Contains(got, propertyText) {
			t.Errorf("property text %q leaked into content diff:\n%s", propertyText, got)
		}
	}
}

func TestSVNProviderMarksGitBinaryPatch(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "blob.bin"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	provider := NewSVNWorkspaceProvider(repo)
	provider.run = func(_ context.Context, args ...string) (string, error) {
		if args[0] == "status" {
			return `<status><target path="."/></status>`, nil
		}
		return `Index: blob.bin
===================================================================
diff --git a/trunk/blob.bin b/trunk/blob.bin
--- a/trunk/blob.bin (revision 1)
+++ b/trunk/blob.bin (working copy)
GIT binary patch
literal 3
abc
`, nil
	}

	diffs, err := provider.GetDiff(context.Background())
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if len(diffs) != 1 || !diffs[0].IsBinary {
		t.Fatalf("diffs = %+v, want one binary diff", diffs)
	}
}

func TestSVNProviderAddedAndDeletedFiles(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "added.go"), []byte("package added\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := NewSVNWorkspaceProvider(repo)
	provider.run = func(_ context.Context, args ...string) (string, error) {
		if args[0] == "status" {
			return `<status><target path="."/></status>`, nil
		}
		return `Index: added.go
===================================================================
diff --git a/project/trunk/added.go b/project/trunk/added.go
new file mode 100644
--- /dev/null
+++ b/project/trunk/added.go (working copy)
@@ -0,0 +1 @@
+package added
Index: deleted.go
===================================================================
diff --git a/project/trunk/deleted.go b/project/trunk/deleted.go
deleted file mode 100644
--- a/project/trunk/deleted.go (revision 7)
+++ /dev/null
@@ -1 +0,0 @@
-package deleted
`, nil
	}

	diffs, err := provider.GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 2 {
		t.Fatalf("diffs = %+v, want two entries", diffs)
	}
	if !diffs[0].IsNew || diffs[0].NewPath != "added.go" || diffs[0].Insertions != 1 {
		t.Errorf("added diff = %+v", diffs[0])
	}
	if !diffs[1].IsDeleted || diffs[1].OldPath != "deleted.go" || diffs[1].NewPath != "/dev/null" || diffs[1].Deletions != 1 {
		t.Errorf("deleted diff = %+v", diffs[1])
	}
}

func TestSVNProviderUnversionedBinary(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "blob.bin"), []byte{0, 1, 2}, 0o644); err != nil {
		t.Fatal(err)
	}
	provider := NewSVNWorkspaceProvider(repo)
	provider.run = func(_ context.Context, args ...string) (string, error) {
		if args[0] == "status" {
			return `<status><target path="."><entry path="blob.bin"><wc-status item="unversioned"/></entry></target></status>`, nil
		}
		return "", nil
	}

	diffs, err := provider.GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || !diffs[0].IsNew || !diffs[0].IsBinary {
		t.Fatalf("diffs = %+v, want one new binary file", diffs)
	}
}

func TestSVNProviderRemoteIdentity(t *testing.T) {
	provider := NewSVNWorkspaceProvider(t.TempDir())
	provider.info = func(context.Context) (svncmd.WorkingCopyInfo, error) {
		return svncmd.WorkingCopyInfo{
			URL:            "https://user:secret@SVN.EXAMPLE.COM/repos/project/trunk",
			RepositoryRoot: "https://user:secret@SVN.EXAMPLE.COM/repos/project",
		}, nil
	}
	if got := provider.RemoteIdentity(context.Background()); got != "svn.example.com/repos/project" {
		t.Errorf("RemoteIdentity = %q", got)
	}
}

func TestSVNProviderRemoteIdentityFallsBackToWorkingCopyURL(t *testing.T) {
	provider := NewSVNWorkspaceProvider(t.TempDir())
	provider.info = func(context.Context) (svncmd.WorkingCopyInfo, error) {
		return svncmd.WorkingCopyInfo{URL: "svn://SVN.EXAMPLE.COM/project/trunk"}, nil
	}
	if got := provider.RemoteIdentity(context.Background()); got != "svn.example.com/project/trunk" {
		t.Errorf("RemoteIdentity = %q", got)
	}
}

func TestSVNProviderDiffErrorMentionsMinimumVersion(t *testing.T) {
	provider := NewSVNWorkspaceProvider(t.TempDir())
	provider.run = func(context.Context, ...string) (string, error) {
		return "", errors.New("unknown option: --git")
	}
	_, err := provider.GetDiff(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1.7") {
		t.Fatalf("error = %v, want minimum-version guidance", err)
	}
}
