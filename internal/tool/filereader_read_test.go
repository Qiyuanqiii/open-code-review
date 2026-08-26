// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package tool

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/gitcmd"
	"github.com/alibaba/open-code-review/internal/vcs"
)

func TestFileReader_Read_Workspace(t *testing.T) {
	dir := t.TempDir()
	content := "line1\nline2\nline3\n"
	if err := os.WriteFile(filepath.Join(dir, "test.go"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	fr := &FileReader{RepoDir: dir, Mode: ModeWorkspace}
	got, err := fr.Read(context.Background(), "test.go")
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	if got != content {
		t.Errorf("Read() = %q, want %q", got, content)
	}
}

func TestFileReader_Read_WorkspaceNotFound(t *testing.T) {
	dir := t.TempDir()
	fr := &FileReader{RepoDir: dir, Mode: ModeWorkspace}
	_, err := fr.Read(context.Background(), "missing.go")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestFileReader_Read_PathTraversal(t *testing.T) {
	dir := t.TempDir()
	fr := &FileReader{RepoDir: dir, Mode: ModeWorkspace}

	_, err := fr.Read(context.Background(), "../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal")
	}
}

func TestFileReader_Read_SymlinkOutsideRepo(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	secretFile := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secretFile, []byte("sensitive"), 0644); err != nil {
		t.Fatal(err)
	}

	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(secretFile, link); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	fr := &FileReader{RepoDir: dir, Mode: ModeWorkspace}
	_, err := fr.Read(context.Background(), "link.txt")
	if err == nil {
		t.Error("expected error for symlink pointing outside repo")
	}
}

func TestFileReader_ReadLines_Workspace(t *testing.T) {
	dir := t.TempDir()
	content := "aaa\nbbb\nccc\nddd\n"
	if err := os.WriteFile(filepath.Join(dir, "lines.txt"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	fr := &FileReader{RepoDir: dir, Mode: ModeWorkspace}

	t.Run("all lines", func(t *testing.T) {
		lines, total, err := fr.ReadLines(context.Background(), "lines.txt", 1, 100)
		if err != nil {
			t.Fatal(err)
		}
		if total != 5 {
			t.Errorf("total = %d, want 5", total)
		}
		if len(lines) != 5 {
			t.Errorf("lines count = %d, want 5", len(lines))
		}
	})

	t.Run("start from line 2 with limit", func(t *testing.T) {
		lines, total, err := fr.ReadLines(context.Background(), "lines.txt", 2, 2)
		if err != nil {
			t.Fatal(err)
		}
		if total != 5 {
			t.Errorf("total = %d, want 5", total)
		}
		if len(lines) != 2 {
			t.Fatalf("lines count = %d, want 2", len(lines))
		}
		if lines[0] != "bbb" || lines[1] != "ccc" {
			t.Errorf("lines = %v, want [bbb ccc]", lines)
		}
	})

	t.Run("path traversal rejected", func(t *testing.T) {
		_, _, err := fr.ReadLines(context.Background(), "../../etc/passwd", 1, 10)
		if err == nil {
			t.Error("expected error for path traversal")
		}
	})
}

func TestFileReader_Read_CommitMode(t *testing.T) {
	dir := setupTestRepo(t)
	commit := getHeadCommit(t, dir)

	fr := &FileReader{RepoDir: dir, Mode: ModeCommit, Ref: commit}
	got, err := fr.Read(context.Background(), "hello.go")
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	if !strings.Contains(got, "package main") {
		t.Errorf("Read() = %q, want containing 'package main'", got)
	}
	if !strings.Contains(got, "func Hello()") {
		t.Errorf("Read() = %q, want containing 'func Hello()'", got)
	}
}

func TestFileReaderReadSubversionRevision(t *testing.T) {
	var calls [][]string
	fr := &FileReader{
		RepoDir:        t.TempDir(),
		RepositoryKind: vcs.Subversion,
		Mode:           ModeCommit,
		Ref:            "17",
		SVNTarget:      "https://svn.example.com/project/trunk",
		SVNOutput: func(_ context.Context, args ...string) ([]byte, error) {
			calls = append(calls, append([]string(nil), args...))
			return []byte("first\nsecond\nthird\n"), nil
		},
	}
	content, err := fr.Read(context.Background(), "src/a b.go")
	if err != nil {
		t.Fatal(err)
	}
	if content != "first\nsecond\nthird\n" {
		t.Fatalf("Read = %q", content)
	}
	lines, total, err := fr.ReadLines(context.Background(), "src/a b.go", 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(lines) != 1 || lines[0] != "second" {
		t.Fatalf("ReadLines = %v, %d", lines, total)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %v", calls)
	}
	for _, call := range calls {
		joined := strings.Join(call, " ")
		if !strings.Contains(joined, "cat --non-interactive --revision 17") || !strings.Contains(joined, "a%20b.go@17") {
			t.Errorf("svn call is not pinned: %s", joined)
		}
	}
}

func TestFileReaderReadSubversionUsesDistinctPegRevision(t *testing.T) {
	var call []string
	fr := &FileReader{
		RepoDir:        t.TempDir(),
		RepositoryKind: vcs.Subversion,
		Mode:           ModeRange,
		Ref:            "21",
		SVNTarget:      "https://svn.example.com/project/branches/feature",
		SVNPegRevision: "25",
		SVNOutput: func(_ context.Context, args ...string) ([]byte, error) {
			call = append([]string(nil), args...)
			return []byte("sealed\n"), nil
		},
	}
	if _, err := fr.Read(context.Background(), "src/value.go"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(call, " ")
	if !strings.Contains(joined, "--revision 21") || !strings.Contains(joined, "value.go@25") {
		t.Fatalf("SVN read did not keep operative and peg revisions distinct: %s", joined)
	}
}

func TestFileReaderSubversionRevisionRequiresTarget(t *testing.T) {
	fr := &FileReader{RepositoryKind: vcs.Subversion, Mode: ModeCommit, Ref: "17"}
	if _, err := fr.Read(context.Background(), "main.go"); err == nil {
		t.Fatal("missing SVN target was accepted")
	}
}

func TestFileReader_Read_CommitMode_MissingFile(t *testing.T) {
	dir := setupTestRepo(t)
	commit := getHeadCommit(t, dir)

	fr := &FileReader{RepoDir: dir, Mode: ModeCommit, Ref: commit}
	_, err := fr.Read(context.Background(), "nonexistent.go")
	if err == nil {
		t.Error("expected error for missing file in commit mode")
	}
}

func TestFileReader_Read_CommitMode_WithRunner(t *testing.T) {
	dir := setupTestRepo(t)
	commit := getHeadCommit(t, dir)
	runner := gitcmd.New(4)

	fr := &FileReader{RepoDir: dir, Mode: ModeCommit, Ref: commit, Runner: runner}
	got, err := fr.Read(context.Background(), "hello.go")
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	if !strings.Contains(got, "package main") {
		t.Errorf("Read() = %q, want containing 'package main'", got)
	}
}

func TestFileReader_Read_CommitMode_WithRunner_MissingFile(t *testing.T) {
	dir := setupTestRepo(t)
	commit := getHeadCommit(t, dir)
	runner := gitcmd.New(4)

	fr := &FileReader{RepoDir: dir, Mode: ModeCommit, Ref: commit, Runner: runner}
	_, err := fr.Read(context.Background(), "nonexistent.go")
	if err == nil {
		t.Error("expected error for missing file in commit mode with runner")
	}
}

func TestFileReader_ReadLines_CommitMode_WithRunner(t *testing.T) {
	dir := setupTestRepo(t)
	commit := getHeadCommit(t, dir)
	runner := gitcmd.New(4)

	fr := &FileReader{RepoDir: dir, Mode: ModeCommit, Ref: commit, Runner: runner}
	lines, total, err := fr.ReadLines(context.Background(), "hello.go", 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Errorf("totalLines = %d, want 4", total)
	}
	if len(lines) < 1 || lines[0] != "package main" {
		t.Errorf("first line = %q, want %q", lines[0], "package main")
	}
}

func TestFileReader_ReadLines_CommitMode_MissingFile(t *testing.T) {
	dir := setupTestRepo(t)
	commit := getHeadCommit(t, dir)

	fr := &FileReader{RepoDir: dir, Mode: ModeCommit, Ref: commit}
	_, _, err := fr.ReadLines(context.Background(), "nonexistent.go", 1, 100)
	if err == nil {
		t.Error("expected error for missing file in commit mode")
	}
}

func TestFileReader_Read_SubdirectoryFile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "src", "pkg")
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "main.go"), []byte("package main"), 0644); err != nil {
		t.Fatal(err)
	}

	fr := &FileReader{RepoDir: dir, Mode: ModeWorkspace}
	got, err := fr.Read(context.Background(), "src/pkg/main.go")
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	if got != "package main" {
		t.Errorf("Read() = %q, want %q", got, "package main")
	}
}

// TestFileReader_Read_CommitMode_MonorepoSubdirPath reproduces #287 at the
// git-show layer: in a monorepo, git reports paths relative to the repo root
// (e.g. "subproject1/src/models/request_meta.py"). With RepoDir anchored at the
// git top-level (the fix), `git show HEAD:<root-relative-path>` must resolve —
// this is the exact command that failed in the issue.
func TestFileReader_Read_CommitMode_MonorepoSubdirPath(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init")
	git("config", "user.email", "t@t.co")
	git("config", "user.name", "t")

	rel := filepath.Join("subproject1", "src", "models", "request_meta.py")
	if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "class RequestMeta:\n    id = 1\n"
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", ".")
	git("commit", "-m", "init")
	commit := getHeadCommit(t, dir)

	// Use the git-style forward-slash path the diff/LLM would supply.
	gitPath := "subproject1/src/models/request_meta.py"

	// git-show (commit mode): the exact path from the issue error.
	frShow := &FileReader{RepoDir: dir, Mode: ModeCommit, Ref: commit}
	got, err := frShow.Read(context.Background(), gitPath)
	if err != nil {
		t.Fatalf("commit-mode Read(%q) error: %v", gitPath, err)
	}
	if got != content {
		t.Errorf("commit-mode Read = %q, want %q", got, content)
	}

	// disk (workspace mode): same root-relative path resolves too.
	frDisk := &FileReader{RepoDir: dir, Mode: ModeWorkspace}
	gotDisk, err := frDisk.Read(context.Background(), gitPath)
	if err != nil {
		t.Fatalf("workspace-mode Read(%q) error: %v", gitPath, err)
	}
	if gotDisk != content {
		t.Errorf("workspace-mode Read = %q, want %q", gotDisk, content)
	}
}
