// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package diff

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/svncmd"
)

const defaultSVNInfoXML = `<?xml version="1.0"?>
<info><entry path="." kind="dir" revision="42">
  <url>https://svn.example.com/repos/project/trunk</url>
  <relative-url>^/project/trunk</relative-url>
  <repository><root>https://svn.example.com/repos</root></repository>
  <wc-info><depth>infinity</depth></wc-info>
</entry></info>`

func stubSVNWorkspace(t *testing.T, provider *SVNProvider, tracked, status string) *[][]string {
	return stubSVNWorkspaceState(t, provider, tracked, status, defaultSVNInfoXML, `<properties/>`)
}

func stubSVNWorkspaceState(t *testing.T, provider *SVNProvider, tracked, status, info, properties string) *[][]string {
	t.Helper()
	var calls [][]string
	provider.run = func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, slices.Clone(args))
		switch args[0] {
		case "--version":
			return "1.14.5-SlikSvn\n", nil
		case "status":
			return status, nil
		case "info":
			return info, nil
		case "propget":
			return properties, nil
		case "diff":
			return tracked, nil
		default:
			return "", fmt.Errorf("unexpected svn command: %v", args)
		}
	}
	return &calls
}

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
	calls := stubSVNWorkspace(t, provider, tracked, status)

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
	if len(*calls) != 5 {
		t.Fatalf("svn calls = %v, want five bounded recursive commands", *calls)
	}
	var diffCall []string
	for _, call := range *calls {
		if call[0] == "diff" {
			diffCall = call
		}
	}
	joined := strings.Join(diffCall, " ")
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
	stubSVNWorkspace(t, provider, `Index: blob.bin
===================================================================
diff --git a/trunk/blob.bin b/trunk/blob.bin
--- a/trunk/blob.bin (revision 1)
+++ b/trunk/blob.bin (working copy)
GIT binary patch
literal 3
abc
`, `<status><target path="."/></status>`)

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
	stubSVNWorkspace(t, provider, `Index: added.go
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
`, `<status><target path="."/></status>`)

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
	stubSVNWorkspace(t, provider, "", `<status><target path="."><entry path="blob.bin"><wc-status item="unversioned"/></entry></target></status>`)

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

func TestSVNProviderRemoteIdentityPrefersRepositoryUUID(t *testing.T) {
	provider := NewSVNWorkspaceProvider(t.TempDir())
	provider.info = func(context.Context) (svncmd.WorkingCopyInfo, error) {
		return svncmd.WorkingCopyInfo{
			URL:            "file:///temporary/location/trunk",
			RepositoryRoot: "file:///temporary/location",
			RepositoryUUID: "A0B1C2D3-E4F5-6789-ABCD-EF0123456789",
		}, nil
	}
	if got := provider.RemoteIdentity(context.Background()); got != "svn:a0b1c2d3-e4f5-6789-abcd-ef0123456789" {
		t.Errorf("RemoteIdentity = %q", got)
	}
}

func TestSVNProviderRejectsUnsupportedVersion(t *testing.T) {
	provider := NewSVNWorkspaceProvider(t.TempDir())
	provider.run = func(_ context.Context, args ...string) (string, error) {
		if args[0] == "--version" {
			return "1.6.17\n", nil
		}
		return "", errors.New("unexpected command after failed capability check")
	}
	_, err := provider.GetDiff(context.Background())
	if err == nil || !strings.Contains(err.Error(), "1.7") {
		t.Fatalf("error = %v, want minimum-version guidance", err)
	}
}

func TestSVNCommitProviderUsesFrozenRevisionContent(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "value.c"), []byte("working copy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	provider := NewSVNCommitProvider(repo, "HEAD")
	provider.info = func(context.Context) (svncmd.WorkingCopyInfo, error) {
		return svncmd.WorkingCopyInfo{
			URL:            "https://svn.example.com/project/trunk",
			RepositoryRoot: "https://svn.example.com/project",
		}, nil
	}
	provider.resolveRevision = func(_ context.Context, root, raw string) (string, error) {
		if root != "https://svn.example.com/project" || raw != "HEAD" {
			t.Fatalf("resolveRevision(%q, %q)", root, raw)
		}
		return "8", nil
	}
	var calls [][]string
	provider.run = func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, slices.Clone(args))
		switch args[0] {
		case "diff":
			return `Index: value.c
===================================================================
diff --git a/value.c b/value.c
--- a/value.c (revision 7)
+++ b/value.c (revision 8)
@@ -1 +1 @@
-old
+immutable
`, nil
		case "cat":
			return "immutable\n", nil
		default:
			return "", errors.New("unexpected svn command")
		}
	}

	diffs, err := provider.GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || diffs[0].NewFileContent != "immutable\n" {
		t.Fatalf("diffs = %+v", diffs)
	}
	resolved := provider.ResolveInput(context.Background())
	if resolved.ResolvedBase != "7" || resolved.ResolvedHead != "8" || resolved.ExactRange != "7:8" {
		t.Fatalf("resolution = %+v", resolved)
	}
	if resolved.RepositoryTarget != "https://svn.example.com/project/trunk" {
		t.Fatalf("repository target = %q", resolved.RepositoryTarget)
	}
	if len(calls) != 2 || calls[0][0] != "diff" || calls[1][0] != "cat" {
		t.Fatalf("svn calls = %v", calls)
	}
	diffArgs := strings.Join(calls[0], " ")
	if !strings.Contains(diffArgs, "trunk@7") || !strings.Contains(diffArgs, "trunk@8") {
		t.Errorf("diff args are not frozen: %s", diffArgs)
	}
	catArgs := strings.Join(calls[1], " ")
	if !strings.Contains(catArgs, "--revision 8") || !strings.Contains(catArgs, "value.c@8") {
		t.Errorf("cat args are not frozen: %s", catArgs)
	}
}

func TestSVNRangeProviderResolvesBothEndpoints(t *testing.T) {
	provider := NewSVNRangeProvider(t.TempDir(), "r3", "HEAD")
	provider.info = func(context.Context) (svncmd.WorkingCopyInfo, error) {
		return svncmd.WorkingCopyInfo{URL: "svn://example.com/trunk", RepositoryRoot: "svn://example.com"}, nil
	}
	provider.resolveRevision = func(_ context.Context, _ string, raw string) (string, error) {
		return map[string]string{"r3": "3", "HEAD": "9"}[raw], nil
	}
	provider.run = func(_ context.Context, args ...string) (string, error) {
		if args[0] != "diff" {
			return "", errors.New("unexpected svn command")
		}
		return "", nil
	}
	if _, err := provider.GetDiff(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := provider.ResolveInput(context.Background())
	if got.ResolvedBase != "3" || got.ResolvedHead != "9" || got.ExactRange != "3:9" {
		t.Fatalf("resolution = %+v", got)
	}
}

func TestSVNRangeProviderResolvesEquivalentHEADOnce(t *testing.T) {
	provider := NewSVNRangeProvider(t.TempDir(), "HEAD", "head")
	provider.info = func(context.Context) (svncmd.WorkingCopyInfo, error) {
		return svncmd.WorkingCopyInfo{URL: "svn://example.com/trunk", RepositoryRoot: "svn://example.com"}, nil
	}
	resolveCalls := 0
	provider.resolveRevision = func(context.Context, string, string) (string, error) {
		resolveCalls++
		return "9", nil
	}
	provider.run = func(context.Context, ...string) (string, error) { return "", nil }
	if _, err := provider.GetDiff(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resolveCalls != 1 {
		t.Fatalf("HEAD resolution calls = %d, want 1", resolveCalls)
	}
	if got := provider.ResolveInput(context.Background()).ExactRange; got != "9:9" {
		t.Fatalf("exact range = %q", got)
	}
}

func TestSVNTargetRangeProviderKeepsOperativeAndPegEndpointsSealed(t *testing.T) {
	provider := NewSVNTargetRangeProvider(
		t.TempDir(),
		"HEAD", "20",
		"https://svn.example.com/repos/app/trunk@HEAD",
		"https://svn.example.com/repos/app/branches/feature@25",
	)
	resolveCalls := 0
	provider.resolveTargets = func(_ context.Context, from, to, fromTarget, toTarget string) (svncmd.ResolvedTargetPair, error) {
		resolveCalls++
		if from != "HEAD" || to != "20" || !strings.Contains(fromTarget, "/trunk@HEAD") || !strings.Contains(toTarget, "/feature@25") {
			t.Fatalf("unexpected target resolution input: %q %q %q %q", from, to, fromTarget, toTarget)
		}
		return svncmd.ResolvedTargetPair{
			Source: svncmd.ResolvedTarget{
				URL: "https://svn.example.com/repos/app/trunk", RepositoryRoot: "https://svn.example.com/repos/app",
				RepositoryUUID: "AABB", OperativeRevision: "10", PegRevision: "12",
			},
			Destination: svncmd.ResolvedTarget{
				URL: "https://svn.example.com/repos/app/branches/feature", RepositoryRoot: "https://svn.example.com/repos/app",
				RepositoryUUID: "AABB", OperativeRevision: "20", PegRevision: "25",
			},
		}, nil
	}
	var calls [][]string
	provider.run = func(_ context.Context, args ...string) (string, error) {
		calls = append(calls, slices.Clone(args))
		switch args[0] {
		case "diff":
			return `Index: src/value.go
===================================================================
diff --git a/trunk/src/value.go b/branches/feature/src/value.go
--- a/trunk/src/value.go (revision 10)
+++ b/branches/feature/src/value.go (revision 20)
@@ -1 +1 @@
-old
+new
`, nil
		case "cat":
			return "new\n", nil
		default:
			return "", errors.New("unexpected SVN command")
		}
	}

	diffs, err := provider.GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || diffs[0].NewPath != "src/value.go" || diffs[0].NewFileContent != "new\n" {
		t.Fatalf("diffs = %+v", diffs)
	}
	if resolveCalls != 1 || len(calls) != 2 {
		t.Fatalf("resolve calls = %d, SVN calls = %v", resolveCalls, calls)
	}
	diffArgs := strings.Join(calls[0], " ")
	for _, want := range []string{"--non-interactive", "--notice-ancestry", "/trunk@10", "/branches/feature@20"} {
		if !strings.Contains(diffArgs, want) {
			t.Errorf("diff args %q do not contain %q", diffArgs, want)
		}
	}
	if strings.Contains(diffArgs, "--revision") {
		t.Fatalf("old/new targets already carry their operative revisions: %s", diffArgs)
	}
	catArgs := strings.Join(calls[1], " ")
	if !strings.Contains(catArgs, "--revision 20") || !strings.Contains(catArgs, "/branches/feature/src/value.go@20") {
		t.Fatalf("destination content was not read from the sealed target: %s", catArgs)
	}
	resolved := provider.ResolveInput(context.Background())
	if resolved.RepositorySourceTarget == "" || resolved.SourcePegRevision != "12" || resolved.TargetPegRevision != "25" {
		t.Fatalf("resolution = %+v", resolved)
	}
	identity := provider.RemoteIdentity(context.Background())
	if identity != "svn:aabb" {
		t.Fatalf("remote identity = %q", identity)
	}
}

func TestSVNCommitProviderRevisionZeroIsEmpty(t *testing.T) {
	provider := NewSVNCommitProvider(t.TempDir(), "0")
	provider.info = func(context.Context) (svncmd.WorkingCopyInfo, error) {
		return svncmd.WorkingCopyInfo{URL: "svn://example.com/trunk", RepositoryRoot: "svn://example.com"}, nil
	}
	provider.resolveRevision = func(context.Context, string, string) (string, error) { return "0", nil }
	provider.run = func(context.Context, ...string) (string, error) {
		t.Fatal("revision zero must not spawn svn diff")
		return "", nil
	}
	diffs, err := provider.GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 0 {
		t.Fatalf("diffs = %+v", diffs)
	}
	got := provider.ResolveInput(context.Background())
	if got.ResolvedBase != "" || got.ResolvedHead != "0" || got.ExactRange != "" {
		t.Fatalf("resolution = %+v", got)
	}
}

func TestSVNProviderSealedInputSkipsResolution(t *testing.T) {
	provider := NewSVNRangeProvider(t.TempDir(), "HEAD", "HEAD")
	provider.SealInput(InputResolution{
		ResolvedBase:     "10",
		ResolvedHead:     "11",
		ExactRange:       "10:11",
		RepositoryTarget: "https://svn.example.com/trunk",
		RepositoryRoot:   "https://svn.example.com",
	})
	provider.info = func(context.Context) (svncmd.WorkingCopyInfo, error) {
		t.Fatal("sealed input must not re-read working-copy metadata")
		return svncmd.WorkingCopyInfo{}, nil
	}
	provider.resolveRevision = func(context.Context, string, string) (string, error) {
		t.Fatal("sealed input must not resolve revisions again")
		return "", nil
	}
	provider.run = func(_ context.Context, args ...string) (string, error) {
		if args[0] != "diff" {
			t.Fatalf("unexpected command: %v", args)
		}
		return "", nil
	}
	if _, err := provider.GetDiff(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := provider.RemoteIdentity(context.Background()); got != "svn.example.com" {
		t.Fatalf("RemoteIdentity = %q", got)
	}
}

func TestSVNProviderRejectsRevisionBeforeReadingMetadata(t *testing.T) {
	provider := NewSVNCommitProvider(t.TempDir(), "-r7")
	provider.info = func(context.Context) (svncmd.WorkingCopyInfo, error) {
		t.Fatal("invalid revision must be rejected before spawning svn info")
		return svncmd.WorkingCopyInfo{}, nil
	}
	if _, err := provider.GetDiff(context.Background()); err == nil || !strings.Contains(err.Error(), "must not start") {
		t.Fatalf("error = %v", err)
	}
}

func TestSVNProviderCopyMoveAndReplacementMetadata(t *testing.T) {
	repo := t.TempDir()
	for path, content := range map[string]string{
		"copied.go":  "package copied\n",
		"moved.go":   "package moved\n",
		"replace.go": "package replacement\n",
	} {
		if err := os.WriteFile(filepath.Join(repo, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tracked := `Index: copied.go
===================================================================
diff --git a/project/trunk/copied.go b/project/trunk/copied.go
new file mode 100644
--- /dev/null
+++ b/project/trunk/copied.go
@@ -0,0 +1 @@
+package copied
Index: moved.go
===================================================================
diff --git a/project/trunk/moved.go b/project/trunk/moved.go
new file mode 100644
--- /dev/null
+++ b/project/trunk/moved.go
@@ -0,0 +1 @@
+package moved
Index: old.go
===================================================================
diff --git a/project/trunk/old.go b/project/trunk/old.go
deleted file mode 100644
--- a/project/trunk/old.go
+++ /dev/null
@@ -1 +0,0 @@
-package moved
Index: replace.go
===================================================================
diff --git a/project/trunk/replace.go b/project/trunk/replace.go
deleted file mode 100644
--- a/project/trunk/replace.go
+++ /dev/null
@@ -1 +0,0 @@
-package old
Index: replace.go
===================================================================
diff --git a/project/trunk/replace.go b/project/trunk/replace.go
new file mode 100644
--- /dev/null
+++ b/project/trunk/replace.go
@@ -0,0 +1 @@
+package replacement
`
	status := `<status><target path=".">
  <entry path="copied.go"><wc-status item="added" copied="true"/></entry>
  <entry path="moved.go"><wc-status item="added" copied="true" moved-from="old.go"/></entry>
  <entry path="old.go"><wc-status item="deleted" moved-to="moved.go"/></entry>
  <entry path="replace.go"><wc-status item="replaced"/></entry>
</target></status>`
	info := `<?xml version="1.0"?><info>
  <entry path="." kind="dir" revision="42"><repository><root>https://svn.example.com/repos</root></repository><wc-info><depth>infinity</depth></wc-info></entry>
  <entry path="copied.go" kind="file" revision="42"><repository><root>https://svn.example.com/repos</root></repository><wc-info><copy-from-url>https://svn.example.com/repos/project/trunk/original.go</copy-from-url><copy-from-rev>41</copy-from-rev></wc-info></entry>
  <entry path="moved.go" kind="file" revision="42"><repository><root>https://svn.example.com/repos</root></repository><wc-info><copy-from-url>https://svn.example.com/repos/project/trunk/old.go</copy-from-url><copy-from-rev>42</copy-from-rev><moved-from>old.go</moved-from></wc-info></entry>
</info>`

	provider := NewSVNWorkspaceProvider(repo)
	stubSVNWorkspaceState(t, provider, tracked, status, info, `<properties/>`)
	diffs, err := provider.GetDiff(context.Background())
	if err != nil {
		t.Fatalf("GetDiff: %v", err)
	}
	if len(diffs) != 5 {
		t.Fatalf("len(diffs) = %d, want 5: %+v", len(diffs), diffs)
	}

	copied := findSVNDiff(t, diffs, "copied.go", false)
	if !copied.IsCopied || copied.CopyFromPath != "^/project/trunk/original.go" || copied.CopyFromRevision != "41" {
		t.Errorf("copied metadata = %+v", copied)
	}
	if !strings.Contains(copied.Diff, "copy from ^/project/trunk/original.go@41\ncopy to copied.go") {
		t.Errorf("copy headers missing:\n%s", copied.Diff)
	}

	movedAdd := findSVNDiff(t, diffs, "moved.go", false)
	if !movedAdd.IsCopied || movedAdd.IsRenamed || movedAdd.MovedFromPath != "old.go" {
		t.Errorf("move add metadata = %+v", movedAdd)
	}
	movedDelete := findSVNDiff(t, diffs, "old.go", true)
	if movedDelete.MovedToPath != "moved.go" || !movedDelete.IsDeleted {
		t.Errorf("move delete metadata = %+v", movedDelete)
	}

	replacedDelete := findSVNDiff(t, diffs, "replace.go", true)
	replacedAdd := findSVNDiff(t, diffs, "replace.go", false)
	if !replacedDelete.IsReplaced || !replacedAdd.IsReplaced {
		t.Errorf("replacement entries = delete %+v, add %+v", replacedDelete, replacedAdd)
	}
}

func findSVNDiff(t *testing.T, diffs []model.Diff, path string, deleted bool) model.Diff {
	t.Helper()
	for _, d := range diffs {
		candidate := d.NewPath
		if d.IsDeleted {
			candidate = d.OldPath
		}
		if candidate == path && d.IsDeleted == deleted {
			return d
		}
	}
	t.Fatalf("diff %q deleted=%v not found in %+v", path, deleted, diffs)
	return model.Diff{}
}

func TestAnnotateSVNDirectoryCopyDerivesChildMetadata(t *testing.T) {
	diffs := []model.Diff{
		{
			OldPath: "copied dir/child @.go", NewPath: "copied dir/child @.go", IsNew: true,
			Diff: "diff --git a/copied dir/child @.go b/copied dir/child @.go\nnew file mode 100644",
		},
		{
			OldPath: "copied dir/new.go", NewPath: "copied dir/new.go", IsNew: true,
			Diff: "diff --git a/copied dir/new.go b/copied dir/new.go\nnew file mode 100644",
		},
	}
	inspection := svnInspection{
		status: []svncmd.StatusEntry{
			{Path: filepath.FromSlash("copied dir/child @.go"), Item: "normal", Copied: true},
			{Path: filepath.FromSlash("copied dir/new.go"), Item: "added", Copied: false},
		},
		info: []svncmd.WorkingCopyEntry{{
			Path: "copied dir", RepositoryRoot: "https://svn.example.com/repos/project",
			CopyFromURL: "https://svn.example.com/repos/project/trunk/source%20dir", CopyFromRevision: "77",
		}},
	}

	if err := annotateSVNHistory(t.TempDir(), diffs, inspection); err != nil {
		t.Fatalf("annotateSVNHistory: %v", err)
	}
	got := diffs[0]
	if !got.IsCopied || got.CopyFromPath != "^/trunk/source dir/child @.go" || got.CopyFromRevision != "77" {
		t.Fatalf("derived copy metadata = %+v", got)
	}
	if diffs[1].IsCopied || diffs[1].CopyFromPath != "" {
		t.Fatalf("new child under copied directory was misclassified: %+v", diffs[1])
	}
}

func TestMatchingMoveRootsPreferMostSpecificPath(t *testing.T) {
	roots := []svnMoveRoot{
		{from: "old", to: "new"},
		{from: "old/nested", to: "new/nested"},
	}
	if got, ok := matchingMoveSource(roots, "old/nested/file.go"); !ok || got.from != "old/nested" {
		t.Fatalf("source match = %+v, %v", got, ok)
	}
	if got, ok := matchingMoveDestination(roots, "new/nested/file.go"); !ok || got.to != "new/nested" {
		t.Fatalf("destination match = %+v, %v", got, ok)
	}
}

func TestValidateSVNStatusRejectsUnsafeStates(t *testing.T) {
	tests := []struct {
		name  string
		entry svncmd.StatusEntry
		want  string
	}{
		{name: "switched", entry: svncmd.StatusEntry{Path: "src", Item: "normal", Switched: true}, want: "switched"},
		{name: "external", entry: svncmd.StatusEntry{Path: "vendor", Item: "external"}, want: "external"},
		{name: "obstructed", entry: svncmd.StatusEntry{Path: "src/app.go", Item: "obstructed"}, want: "obstructed"},
		{name: "incomplete", entry: svncmd.StatusEntry{Path: "src", Item: "incomplete"}, want: "incomplete"},
		{name: "text conflict", entry: svncmd.StatusEntry{Path: "src/app.go", Item: "conflicted"}, want: "conflicted"},
		{name: "property conflict", entry: svncmd.StatusEntry{Path: "src/app.go", Item: "modified", Properties: "conflicted"}, want: "conflicted"},
		{name: "tree conflict", entry: svncmd.StatusEntry{Path: "src", Item: "modified", TreeConflicted: true}, want: "conflicted"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateSVNStatus(t.TempDir(), []svncmd.StatusEntry{test.entry})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSVNInspectionRejectsSparseAndExternalWorkingCopies(t *testing.T) {
	t.Run("sparse", func(t *testing.T) {
		entries := []svncmd.WorkingCopyEntry{{Path: ".", Kind: "dir", Depth: "immediates"}}
		err := validateSVNDepths(t.TempDir(), entries)
		if err == nil || !strings.Contains(err.Error(), "sparse") || !strings.Contains(err.Error(), "immediates") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("externals", func(t *testing.T) {
		provider := NewSVNWorkspaceProvider(t.TempDir())
		calls := stubSVNWorkspaceState(t, provider, "", `<status/>`, defaultSVNInfoXML,
			`<properties><target path="."><property name="svn:externals">^/vendor vendor</property></target></properties>`)
		_, err := provider.GetDiff(context.Background())
		if err == nil || !strings.Contains(err.Error(), "svn:externals") {
			t.Fatalf("error = %v", err)
		}
		if len(*calls) != 4 {
			t.Fatalf("calls = %v, diff must not run after external rejection", *calls)
		}
	})
}

func TestMixedSVNRevisionsAreDetectedAndSupported(t *testing.T) {
	entries := []svncmd.WorkingCopyEntry{{Revision: "10"}, {Revision: "11"}, {Revision: "Resource is not under version control."}}
	if !hasMixedSVNRevisions(entries) {
		t.Fatal("mixed BASE revisions were not detected")
	}
	if hasMixedSVNRevisions([]svncmd.WorkingCopyEntry{{Revision: "10"}, {Revision: "10"}}) {
		t.Fatal("uniform BASE revisions reported as mixed")
	}

	provider := NewSVNWorkspaceProvider(t.TempDir())
	info := `<info>
  <entry path="." kind="dir" revision="10"><wc-info><depth>infinity</depth></wc-info></entry>
  <entry path="file.go" kind="file" revision="11"><wc-info><depth>infinity</depth></wc-info></entry>
</info>`
	stubSVNWorkspaceState(t, provider, "", `<status/>`, info, `<properties/>`)
	inspection, err := provider.inspectWorkingCopy(context.Background())
	if err != nil {
		t.Fatalf("mixed-revision working copy rejected: %v", err)
	}
	if !inspection.mixedRevision {
		t.Fatal("inspection did not record mixed revisions")
	}
}

func TestSVNContentDiffSectionsPropertyBehavior(t *testing.T) {
	propertyOnly := `diff --git a/script.sh b/script.sh
old mode 100644
new mode 100755
--- a/script.sh
+++ b/script.sh
Property changes on: script.sh
Added: svn:executable
+*
diff --git a/eol.txt b/eol.txt
--- a/eol.txt
+++ b/eol.txt
Property changes on: eol.txt
Modified: svn:eol-style
-native
+LF
`
	if got := svnContentDiffSections(propertyOnly); got != "" {
		t.Fatalf("property-only changes = %q, want omitted", got)
	}

	binaryWithMIME := `diff --git a/blob.dat b/blob.dat
GIT binary patch
literal 3
abc
Property changes on: blob.dat
Added: svn:mime-type
+application/octet-stream
`
	got := svnContentDiffSections(binaryWithMIME)
	if !strings.Contains(got, "GIT binary patch") || strings.Contains(got, "svn:mime-type") {
		t.Fatalf("binary/MIME output = %q", got)
	}
}

func TestNormalizeSVNPathUnicodeSpacesPegAndPlatformPaths(t *testing.T) {
	repo := t.TempDir()
	name := "src/\u6d4b\u8bd5 space @ file.go"
	if got := normalizeSVNPath(repo, filepath.FromSlash(name)); got != name {
		t.Fatalf("normalizeSVNPath = %q, want %q", got, name)
	}
	abs := filepath.Join(repo, "src", "windows.go")
	if got := normalizeSVNPath(repo, abs); got != "src/windows.go" {
		t.Fatalf("absolute path = %q, want src/windows.go", got)
	}
	if runtime.GOOS == "windows" {
		backslash := filepath.Join("src", "nested", "file.go")
		if got := normalizeSVNPath(repo, backslash); got != "src/nested/file.go" {
			t.Fatalf("Windows path = %q", got)
		}
	}
}

func TestNormalizeSVNDiffRecoversUnicodePathFromXMLCandidate(t *testing.T) {
	path := "unicode-\u6d4b\u8bd5 space @.go"
	raw := `Index: unicode-?? space @.go
===================================================================
diff --git a/trunk/unicode-?? space @.go b/trunk/unicode-?? space @.go
--- a/trunk/unicode-?? space @.go
+++ b/trunk/unicode-?? space @.go
@@ -1 +1 @@
-before
+after
`
	got, err := normalizeSVNDiffWithCandidates(t.TempDir(), raw, []string{path})
	if err != nil {
		t.Fatalf("normalizeSVNDiffWithCandidates: %v", err)
	}
	if !strings.Contains(got, "diff --git a/"+path+" b/"+path) || !strings.Contains(got, "+++ b/"+path) {
		t.Fatalf("Unicode path was not recovered:\n%s", got)
	}
}

func TestResolveSVNDiffPathRejectsAmbiguousConsoleEncoding(t *testing.T) {
	_, err := resolveSVNDiffPath(t.TempDir(), "src/??.go", []string{"src/\u6d4b\u8bd5.go", "src/\u6d4b\u9a8c.go"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("error = %v", err)
	}
}

func TestResolveSVNDiffPathRejectsUnmatchedConsoleEncoding(t *testing.T) {
	_, err := resolveSVNDiffPath(t.TempDir(), "src/??.go", []string{"src/ordinary.go"})
	if err == nil || !strings.Contains(err.Error(), "could not be reconciled") {
		t.Fatalf("error = %v", err)
	}
}

func TestRepositoryRelativeSVNPath(t *testing.T) {
	got, err := repositoryRelativeSVNPath(
		"https://user:secret@SVN.EXAMPLE.COM/repos/project",
		"https://svn.example.com/repos/project/trunk/source%20@.go",
	)
	if err != nil {
		t.Fatalf("repositoryRelativeSVNPath: %v", err)
	}
	if got != "^/trunk/source @.go" {
		t.Fatalf("path = %q", got)
	}
	got, err = repositoryRelativeSVNPath("https://svn.example.com", "https://svn.example.com/trunk/file.go")
	if err != nil || got != "^/trunk/file.go" {
		t.Fatalf("root repository path = %q, error = %v", got, err)
	}

	for _, candidate := range []string{
		"https://other.example.com/repos/project/trunk/file.go",
		"https://svn.example.com/repos/project-other/trunk/file.go",
	} {
		if _, outsideErr := repositoryRelativeSVNPath("https://svn.example.com/repos/project", candidate); outsideErr == nil {
			t.Errorf("outside candidate %q was accepted", candidate)
		}
	}
}

func TestCollectUnversionedPathRejectsNestedWorkingCopy(t *testing.T) {
	repo := t.TempDir()
	nested := filepath.Join(repo, "unversioned", "vendor", "nested")
	if err := os.MkdirAll(filepath.Join(nested, ".svn"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := collectUnversionedPath(context.Background(), repo, "unversioned", nil, make(map[string]struct{}))
	if err == nil || !strings.Contains(err.Error(), "nested Subversion working copy") {
		t.Fatalf("error = %v", err)
	}
}

func TestSVNProviderPropagatesCancellation(t *testing.T) {
	provider := NewSVNWorkspaceProvider(t.TempDir())
	provider.run = func(ctx context.Context, _ ...string) (string, error) {
		return "", ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := provider.GetDiff(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
