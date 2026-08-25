// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCommandIntegration(t *testing.T) {
	for _, name := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is not installed", name)
		}
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wc := filepath.Join(root, "wc")
	runSVNCommand(t, root, "svnadmin", "create", repo)
	repoURL := commandRepositoryURL(repo)
	runSVNCommand(t, root, "svn", "mkdir", repoURL+"/trunk", "-m", "create trunk")
	runSVNCommand(t, root, "svn", "checkout", repoURL+"/trunk", wc)
	if err := os.WriteFile(filepath.Join(wc, "value.txt"), []byte("revision two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSVNCommand(t, wc, "svn", "add", "value.txt")
	runSVNCommand(t, wc, "svn", "commit", "-m", "add value")

	ctx := context.Background()
	info, err := Info(ctx, wc)
	if err != nil {
		t.Fatal(err)
	}
	if info.URL != repoURL+"/trunk" || info.RepositoryRoot != repoURL || info.RepositoryUUID == "" {
		t.Fatalf("working-copy info = %+v", info)
	}
	for raw, want := range map[string]string{"HEAD": "2", "r1": "1"} {
		got, err := ResolveRevision(ctx, wc, info.RepositoryRoot, raw)
		if err != nil {
			t.Fatalf("ResolveRevision(%q): %v", raw, err)
		}
		if got != want {
			t.Fatalf("ResolveRevision(%q) = %q, want %q", raw, got, want)
		}
	}

	listXML, err := Output(ctx, wc, "list", "--xml", "--recursive", "--revision", "2", "--", PegTarget(info.URL, "2"))
	if err != nil {
		t.Fatal(err)
	}
	files, err := ParseList(listXML)
	if err != nil || len(files) != 1 || files[0] != "value.txt" {
		t.Fatalf("repository files = %v, err = %v", files, err)
	}
	if out, err := CombinedOutput(ctx, wc, "diff", "--git", "."); err != nil || len(out) != 0 {
		t.Fatalf("clean working-copy diff = %q, err = %v", out, err)
	}

	if _, err := Output(ctx, wc, "info", "--xml", "--revision", "9999", "--", PegTarget(info.RepositoryRoot, "HEAD")); err == nil {
		t.Fatal("out-of-range revision unexpectedly succeeded")
	}
	missing, err := ChildTarget(info.URL, "missing.txt", "2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CombinedOutput(ctx, wc, "cat", "--revision", "2", "--", missing); err == nil {
		t.Fatal("missing repository file unexpectedly succeeded")
	}
}

func commandRepositoryURL(path string) string {
	slashPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

func runSVNCommand(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}
