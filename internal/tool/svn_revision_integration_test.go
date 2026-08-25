// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package tool

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/vcs"
)

func TestSubversionRevisionFileToolsIntegration(t *testing.T) {
	for _, name := range []string{"svn", "svnadmin"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is not installed", name)
		}
	}
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	wc := filepath.Join(root, "wc")
	runToolSVN(t, root, "svnadmin", "create", repo)
	repoURL := toolLocalRepositoryURL(repo)
	runToolSVN(t, root, "svn", "mkdir", repoURL+"/trunk", "-m", "create trunk")
	runToolSVN(t, root, "svn", "checkout", repoURL+"/trunk", wc)
	if err := os.MkdirAll(filepath.Join(wc, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(wc, "src", "search.go")
	if err := os.WriteFile(file, []byte("package src\nfunc FrozenNeedle() int { return 2 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runToolSVN(t, wc, "svn", "add", "src")
	runToolSVN(t, wc, "svn", "commit", "-m", "add searchable file")
	if err := os.WriteFile(file, []byte("package src\nfunc DirtyNeedle() int { return 99 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	reader := &FileReader{
		RepoDir:        wc,
		RepositoryKind: vcs.Subversion,
		Mode:           ModeCommit,
		Ref:            "2",
		SVNTarget:      repoURL + "/trunk",
	}
	content, err := reader.Read(context.Background(), "src/search.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "FrozenNeedle") || strings.Contains(content, "DirtyNeedle") {
		t.Fatalf("immutable file content = %q", content)
	}

	found, err := NewFileFind(reader).Execute(context.Background(), map[string]any{"query_name": "search"})
	if err != nil {
		t.Fatal(err)
	}
	if found != "src/search.go" {
		t.Fatalf("file_find = %q", found)
	}

	result, err := NewCodeSearch(reader).Execute(context.Background(), map[string]any{
		"search_text":   "FrozenNeedle",
		"file_patterns": []any{"src/*.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "File: src/search.go") || strings.Contains(result, "DirtyNeedle") {
		t.Fatalf("code_search = %q", result)
	}
}

func toolLocalRepositoryURL(path string) string {
	slashPath := filepath.ToSlash(path)
	if runtime.GOOS == "windows" && !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

func runToolSVN(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
}
