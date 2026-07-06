package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/open-code-review/open-code-review/internal/config/rules"
)

func TestApplyCLIExcludes_Empty(t *testing.T) {
	cc := &commonContext{FileFilter: &rules.FileFilter{Exclude: []string{"a"}}}
	applyCLIExcludes(cc, nil)
	if len(cc.FileFilter.Exclude) != 1 {
		t.Errorf("expected 1 exclude, got %d", len(cc.FileFilter.Exclude))
	}
}

func TestApplyCLIExcludes_AppendsPatterns(t *testing.T) {
	cc := &commonContext{FileFilter: &rules.FileFilter{Exclude: []string{"a"}}}
	applyCLIExcludes(cc, []string{"b", "c"})
	if len(cc.FileFilter.Exclude) != 3 {
		t.Errorf("expected 3 excludes, got %d", len(cc.FileFilter.Exclude))
	}
}

func TestApplyCLIExcludes_NilFileFilter(t *testing.T) {
	cc := &commonContext{}
	applyCLIExcludes(cc, []string{"x"})
	if cc.FileFilter == nil {
		t.Fatal("expected FileFilter to be created")
	}
	if len(cc.FileFilter.Exclude) != 1 || cc.FileFilter.Exclude[0] != "x" {
		t.Errorf("expected [x], got %v", cc.FileFilter.Exclude)
	}
}

func TestNewQuietHandle_NoOp(t *testing.T) {
	h := newQuietHandle("text", "developer")
	if h.fn != nil {
		t.Error("expected no-op handle for text/developer")
	}
	h.Restore()
}

func TestNewQuietHandle_JSON(t *testing.T) {
	h := newQuietHandle("json", "developer")
	if h.fn == nil {
		t.Error("expected fn to be set for json format")
	}
	h.Restore()
	if h.fn != nil {
		t.Error("expected fn to be nil after Restore")
	}
}

func TestNewQuietHandle_Agent(t *testing.T) {
	h := newQuietHandle("text", "agent")
	if h.fn == nil {
		t.Error("expected fn to be set for agent audience")
	}
	h.Restore()
}

func TestQuietHandle_NilReceiver(t *testing.T) {
	var h *quietHandle
	h.Restore()
}

func TestQuietHandle_IdempotentRestore(t *testing.T) {
	h := newQuietHandle("json", "developer")
	h.Restore()
	h.Restore()
	if h.fn != nil {
		t.Error("expected nil after double restore")
	}
}

func TestResolveWorkingDir_CurrentDir(t *testing.T) {
	dir := t.TempDir()
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	os.Chdir(dir)

	absPath, isGit, err := resolveWorkingDir("", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if absPath == "" {
		t.Error("expected non-empty absPath")
	}
	if isGit {
		t.Error("temp dir should not be a git repo")
	}
}

func TestResolveWorkingDir_RequireGitFails(t *testing.T) {
	dir := t.TempDir()
	_, _, err := resolveWorkingDir(dir, true)
	if err == nil {
		t.Fatal("expected error for non-git dir with requireGit=true")
	}
}

func TestResolveWorkingDir_NonExistent(t *testing.T) {
	_, _, err := resolveWorkingDir(filepath.Join(t.TempDir(), "no-such-dir"), false)
	if err == nil {
		t.Fatal("expected error for non-existent path")
	}
}

// TestResolveWorkingDir_SubdirResolvesToToplevel locks in the #287 fix: when
// ocr is invoked from a monorepo subdirectory, the resolved repo dir must be
// the repository top level, not the subdir, so diff and file_read agree on
// repo-root-relative paths.
func TestResolveWorkingDir_SubdirResolvesToToplevel(t *testing.T) {
	root := initTestGitRepo(t)
	sub := filepath.Join(root, "subproject", "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, isGit, err := resolveWorkingDir(sub, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isGit {
		t.Fatal("expected isGit=true for a subdir of a git repo")
	}
	wantRoot, _ := filepath.EvalSymlinks(root)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantRoot {
		t.Errorf("resolveWorkingDir(subdir) = %q, want repo root %q", got, wantRoot)
	}
}

// TestResolveWorkingDir_ScanSubdirKeepsScope guards against regressing scan's
// scope: requireGit=false (scan path) must keep the subdirectory, not expand to
// the whole repo, even when the subdir lives inside a git repository.
func TestResolveWorkingDir_ScanSubdirKeepsScope(t *testing.T) {
	root := initTestGitRepo(t)
	sub := filepath.Join(root, "subproject", "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	got, isGit, err := resolveWorkingDir(sub, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !isGit {
		t.Fatal("expected isGit=true for a subdir of a git repo")
	}
	wantSub, _ := filepath.EvalSymlinks(sub)
	gotResolved, _ := filepath.EvalSymlinks(got)
	if gotResolved != wantSub {
		t.Errorf("resolveWorkingDir(subdir, requireGit=false) = %q, want subdir %q", got, wantSub)
	}
}

// TestResolveWorkingDir_BareRepoFailsLoudly ensures a bare repository (where
// `git rev-parse --show-toplevel` yields no work tree) errors on the review
// path instead of silently falling back to the invocation dir and reproducing
// the #287 path mismatch.
func TestResolveWorkingDir_BareRepoFailsLoudly(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}

	_, _, err := resolveWorkingDir(dir, true)
	if err == nil {
		t.Fatal("expected error for bare repo with requireGit=true")
	}
	if !strings.Contains(err.Error(), "not a git repository") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestResolveWorkingDir_GitRepo(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.Mkdir(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}

	absPath, isGit, err := resolveWorkingDir(dir, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if absPath == "" {
		t.Error("expected non-empty absPath")
	}
	_ = isGit
}
