// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package diff

import (
	"context"
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
