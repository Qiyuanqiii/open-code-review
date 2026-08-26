// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package diff

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/model"
)

func TestSVNRevisionProviderIntegration(t *testing.T) {
	svnIntegrationTools(t)
	wc := createSVNRevisionFixture(t)

	// A dirty working copy must not affect immutable content at r3.
	if err := os.WriteFile(filepath.Join(wc, "modified.txt"), []byte("dirty workspace\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	commitProvider := NewSVNCommitProvider(wc, "3")
	diffs, err := commitProvider.GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byPath := diffsByEffectivePath(diffs)
	if got := byPath["modified.txt"].NewFileContent; got != "committed r3\n" {
		t.Fatalf("modified content = %q, want immutable r3 content", got)
	}
	if d, ok := byPath["deleted.txt"]; !ok || !d.IsDeleted {
		t.Fatalf("deleted diff = %+v, present=%v", d, ok)
	}
	if d, ok := byPath["copied.txt"]; !ok || !d.IsNew || d.NewFileContent != "source r2\n" {
		t.Fatalf("copied diff = %+v, present=%v", d, ok)
	}
	if d, ok := byPath["replaced.txt"]; !ok || d.NewFileContent != "replacement r3\n" {
		t.Fatalf("replacement diff = %+v, present=%v", d, ok)
	}
	if d, ok := byPath["binary.bin"]; !ok || !d.IsBinary {
		t.Fatalf("binary diff = %+v, present=%v", d, ok)
	}
	resolved := commitProvider.ResolveInput(context.Background())
	if resolved.ResolvedBase != "2" || resolved.ResolvedHead != "3" || resolved.ExactRange != "2:3" {
		t.Fatalf("commit resolution = %+v", resolved)
	}
	if resolved.RepositoryUUID == "" || commitProvider.RemoteIdentity(context.Background()) != "svn:"+strings.ToLower(resolved.RepositoryUUID) {
		t.Fatalf("repository identity was not derived from UUID: %+v", resolved)
	}

	rangeProvider := NewSVNRangeProvider(wc, "r2", "3")
	rangeDiffs, err := rangeProvider.GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rangeDiffs) != len(diffs) {
		t.Fatalf("range diff count = %d, commit diff count = %d", len(rangeDiffs), len(diffs))
	}
	if got := rangeProvider.ResolveInput(context.Background()).ExactRange; got != "2:3" {
		t.Fatalf("range exact endpoint = %q", got)
	}

	additionProvider := NewSVNCommitProvider(wc, "2")
	additionDiffs, err := additionProvider.GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := diffsByEffectivePath(additionDiffs)["modified.txt"]; !ok || !d.IsNew || d.NewFileContent != "committed r2\n" {
		t.Fatalf("addition diff = %+v, present=%v", d, ok)
	}

	for _, revision := range []string{"0", "4"} {
		provider := NewSVNCommitProvider(wc, revision)
		empty, err := provider.GetDiff(context.Background())
		if err != nil {
			t.Fatalf("revision %s: %v", revision, err)
		}
		if len(empty) != 0 {
			t.Fatalf("revision %s produced content diffs: %+v", revision, empty)
		}
	}

	sealed, err := ResolveSVNInput(context.Background(), wc, "", "", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if sealed.ResolvedHead != "4" {
		t.Fatalf("HEAD resolved to %q, want 4", sealed.ResolvedHead)
	}
	writeFixtureFile(t, wc, "modified.txt", []byte("committed r5\n"))
	runSVNFixtureCommand(t, wc, "svn", "commit", "-m", "advance head")

	pinned := NewSVNCommitProvider(wc, "HEAD")
	pinned.SealInput(sealed)
	pinnedDiffs, err := pinned.GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pinnedDiffs) != 0 {
		t.Fatalf("sealed HEAD drifted to r5: %+v", pinnedDiffs)
	}
	moving := NewSVNCommitProvider(wc, "HEAD")
	movingDiffs, err := moving.GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := diffsByEffectivePath(movingDiffs)["modified.txt"].NewFileContent; got != "committed r5\n" {
		t.Fatalf("unsealed HEAD content = %q", got)
	}
}

func svnIntegrationTools(t *testing.T) {
	t.Helper()
	for _, name := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is not installed", name)
		}
	}
}

func createSVNRevisionFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wc := filepath.Join(root, "wc")
	runSVNFixtureCommand(t, root, "svnadmin", "create", repo)
	repoURL := localRepositoryURL(repo)
	runSVNFixtureCommand(t, root, "svn", "mkdir", repoURL+"/trunk", "-m", "create trunk")
	runSVNFixtureCommand(t, root, "svn", "checkout", repoURL+"/trunk", wc)

	writeFixtureFile(t, wc, "modified.txt", []byte("committed r2\n"))
	writeFixtureFile(t, wc, "deleted.txt", []byte("delete me\n"))
	writeFixtureFile(t, wc, "source.txt", []byte("source r2\n"))
	writeFixtureFile(t, wc, "replaced.txt", []byte("original r2\n"))
	writeFixtureFile(t, wc, "binary.bin", []byte{0, 1, 2})
	runSVNFixtureCommand(t, wc, "svn", "add", "--force", ".")
	runSVNFixtureCommand(t, wc, "svn", "propset", "svn:mime-type", "application/octet-stream", "binary.bin")
	runSVNFixtureCommand(t, wc, "svn", "commit", "-m", "initial content")

	writeFixtureFile(t, wc, "modified.txt", []byte("committed r3\n"))
	runSVNFixtureCommand(t, wc, "svn", "delete", "deleted.txt")
	runSVNFixtureCommand(t, wc, "svn", "copy", "source.txt", "copied.txt")
	runSVNFixtureCommand(t, wc, "svn", "delete", "replaced.txt")
	runSVNFixtureCommand(t, wc, "svn", "copy", "source.txt", "replaced.txt")
	writeFixtureFile(t, wc, "replaced.txt", []byte("replacement r3\n"))
	writeFixtureFile(t, wc, "binary.bin", []byte{0, 3, 4})
	runSVNFixtureCommand(t, wc, "svn", "commit", "-m", "mixed revision changes")

	runSVNFixtureCommand(t, wc, "svn", "update")
	runSVNFixtureCommand(t, wc, "svn", "propset", "custom:marker", "yes", ".")
	runSVNFixtureCommand(t, wc, "svn", "commit", "-m", "property only")
	return wc
}

func localRepositoryURL(path string) string {
	slashPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

func runSVNFixtureCommand(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}

func writeFixtureFile(t *testing.T, root, rel string, content []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func diffsByEffectivePath(diffs []model.Diff) map[string]model.Diff {
	result := make(map[string]model.Diff, len(diffs))
	for _, d := range diffs {
		path := d.NewPath
		if path == "/dev/null" {
			path = d.OldPath
		}
		result[path] = d
	}
	return result
}

func TestSVNProviderIntegrationEdgeCases(t *testing.T) {
	svnPath, err := exec.LookPath("svn")
	if err != nil {
		t.Skip("svn executable is not installed")
	}
	svnAdminPath, err := exec.LookPath("svnadmin")
	if err != nil {
		t.Skip("svnadmin executable is not installed")
	}
	t.Setenv("PATH", filepath.Dir(svnPath)+string(os.PathListSeparator)+os.Getenv("PATH"))

	fixture := t.TempDir()
	repository := filepath.Join(fixture, "repository")
	workingCopy := filepath.Join(fixture, "working copy")
	runSVNFixtureCommand(t, fixture, svnAdminPath, "create", repository)
	repositoryURL := localSVNRepositoryURL(t, repository)
	runSVNFixtureCommand(t, fixture, svnPath, "mkdir", repositoryURL+"/trunk", "-m", "initialize trunk")
	runSVNFixtureCommand(t, fixture, svnPath, "checkout", repositoryURL+"/trunk", workingCopy)

	unicodePath := "unicode-\u6d4b\u8bd5 space @.go"
	writeSVNFixtureFile(t, workingCopy, "original.go", []byte("package original\n"))
	writeSVNFixtureFile(t, workingCopy, "move.go", []byte("package moved\n"))
	writeSVNFixtureFile(t, workingCopy, "replace.go", []byte("package old\n"))
	writeSVNFixtureFile(t, workingCopy, "property.txt", []byte("property only\n"))
	writeSVNFixtureFile(t, workingCopy, "binary.dat", []byte{0, 1, 2, 3})
	writeSVNFixtureFile(t, workingCopy, unicodePath, []byte("package before\n"))
	runSVNFixtureCommand(t, workingCopy, svnPath, "add", "--force", "--", ".")
	runSVNFixtureCommand(t, workingCopy, svnPath, "commit", "-m", "add baseline")

	runSVNFixtureCommand(t, workingCopy, svnPath, "copy", "original.go", "copied.go")
	runSVNFixtureCommand(t, workingCopy, svnPath, "move", "move.go", "moved.go")
	runSVNFixtureCommand(t, workingCopy, svnPath, "delete", "replace.go")
	writeSVNFixtureFile(t, workingCopy, "replace.go", []byte("package replacement\n"))
	runSVNFixtureCommand(t, workingCopy, svnPath, "add", "replace.go")
	runSVNFixtureCommand(t, workingCopy, svnPath, "propset", "svn:executable", "yes", "--", "property.txt")
	runSVNFixtureCommand(t, workingCopy, svnPath, "propset", "svn:eol-style", "LF", "--", "property.txt")
	runSVNFixtureCommand(t, workingCopy, svnPath, "propset", "svn:mime-type", "application/octet-stream", "--", "binary.dat")
	writeSVNFixtureFile(t, workingCopy, "binary.dat", []byte{0, 9, 8, 7})
	writeSVNFixtureFile(t, workingCopy, unicodePath, []byte("package after\n"))

	provider := NewSVNWorkspaceProvider(workingCopy)
	diffs, err := provider.GetDiff(context.Background())
	if err != nil {
		t.Fatalf("GetDiff against real SVN working copy: %v", err)
	}

	copied := findSVNDiff(t, diffs, "copied.go", false)
	if !copied.IsCopied || copied.CopyFromPath != "^/trunk/original.go" || copied.CopyFromRevision == "" {
		t.Errorf("copy metadata = %+v", copied)
	}
	movedAdd := findSVNDiff(t, diffs, "moved.go", false)
	movedDelete := findSVNDiff(t, diffs, "move.go", true)
	if !movedAdd.IsCopied || movedAdd.MovedFromPath != "move.go" || movedDelete.MovedToPath != "moved.go" {
		t.Errorf("move metadata = add %+v, delete %+v", movedAdd, movedDelete)
	}
	if !findSVNDiff(t, diffs, "replace.go", false).IsReplaced || !findSVNDiff(t, diffs, "replace.go", true).IsReplaced {
		t.Error("replacement was not reported as delete plus add")
	}
	if !findSVNDiff(t, diffs, "binary.dat", false).IsBinary {
		t.Error("MIME-marked binary content change was not detected")
	}
	if got := findSVNDiff(t, diffs, unicodePath, false); got.Insertions != 1 || got.Deletions != 1 {
		t.Errorf("Unicode/space/@ diff = %+v", got)
	}
	for _, d := range diffs {
		if d.NewPath == "property.txt" || d.OldPath == "property.txt" {
			t.Errorf("property-only executable/EOL change was not omitted: %+v", d)
		}
	}

	t.Run("sparse working copy", func(t *testing.T) {
		sparse := filepath.Join(fixture, "sparse")
		runSVNFixtureCommand(t, fixture, svnPath, "checkout", "--depth", "empty", repositoryURL+"/trunk", sparse)
		_, sparseErr := NewSVNWorkspaceProvider(sparse).GetDiff(context.Background())
		if sparseErr == nil || !strings.Contains(sparseErr.Error(), "sparse") {
			t.Fatalf("sparse error = %v", sparseErr)
		}
	})

	t.Run("external definition", func(t *testing.T) {
		runSVNFixtureCommand(t, workingCopy, svnPath, "propset", "svn:externals", "^/trunk nested-external", "--", ".")
		_, externalErr := NewSVNWorkspaceProvider(workingCopy).GetDiff(context.Background())
		if externalErr == nil || !strings.Contains(externalErr.Error(), "svn:externals") {
			t.Fatalf("external error = %v", externalErr)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, cancelErr := NewSVNWorkspaceProvider(workingCopy).GetDiff(ctx)
		if !errors.Is(cancelErr, context.Canceled) {
			t.Fatalf("cancellation error = %v", cancelErr)
		}
	})
}

func localSVNRepositoryURL(t *testing.T, repository string) string {
	t.Helper()
	path := filepath.ToSlash(repository)
	if runtime.GOOS == "windows" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func writeSVNFixtureFile(t *testing.T, root, relative string, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(relative)), content, 0o644); err != nil {
		t.Fatalf("write %q: %v", relative, err)
	}
}
