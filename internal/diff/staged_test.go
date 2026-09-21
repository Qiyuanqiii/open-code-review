// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package diff

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/gitcmd"
	"github.com/alibaba/open-code-review/internal/model"
)

func stagedGitOutput(t *testing.T, repo string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeStagedFile(t *testing.T, repo, path, content string) {
	t.Helper()
	full := filepath.Join(repo, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func captureStagedTest(t *testing.T, repo string) *StagedSnapshot {
	t.Helper()
	snapshot, err := CaptureStagedSnapshot(context.Background(), repo, gitcmd.New(2))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestStagedSnapshotFreezesIndexAndFullContext(t *testing.T) {
	repo := initRepoWithChange(t)
	writeStagedFile(t, repo, "sample.txt", "staged version\n")
	writeStagedFile(t, repo, "context.txt", "staged context\n")
	runGitTest(t, repo, "add", ".")
	writeStagedFile(t, repo, "sample.txt", "unstaged version\n")
	writeStagedFile(t, repo, "context.txt", "unstaged context\n")
	writeStagedFile(t, repo, "untracked.txt", "must not appear\n")
	indexPath := filepath.Join(repo, ".git", "index")
	before, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := captureStagedTest(t, repo)
	after, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("capture modified original index: %v", err)
	}
	provider := NewStagedProvider(repo, snapshot, gitcmd.New(2))
	resolution := provider.ResolveInput(context.Background())
	if resolution.ResolvedBase != snapshot.BaseCommit || resolution.SnapshotTree != snapshot.Tree || resolution.ResolvedHead != "" || resolution.ExactRange != "" {
		t.Fatalf("incorrect staged identity: %+v", resolution)
	}
	// Both live inputs move after capture; the provider must still report the
	// staged versions, including full file content loaded by the parser.
	runGitTest(t, repo, "add", ".")
	runGitTest(t, repo, "commit", "-q", "-m", "move HEAD and index")
	set, err := provider.GetDiffSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Included) != 2 {
		t.Fatalf("snapshot changes: %+v", set.Included)
	}
	for _, d := range set.Included {
		if !strings.HasPrefix(d.NewFileContent, "staged ") || strings.Contains(d.Diff, "unstaged") || d.NewPath == "untracked.txt" {
			t.Fatalf("live working tree leaked into snapshot: %+v", d)
		}
	}
	if got := stagedGitOutput(t, repo, "show", snapshot.Tree+":context.txt"); got != "staged context" {
		t.Fatalf("snapshot context = %q", got)
	}
	// The provider owns a copy rather than a caller-mutable snapshot pointer.
	snapshot.Tree = "invalid"
	if _, err := provider.GetDiff(context.Background()); err != nil {
		t.Fatalf("caller mutation changed captured provider: %v", err)
	}
}

func TestStagedSnapshotUnbornAndEmpty(t *testing.T) {
	for _, stagedFile := range []bool{false, true} {
		t.Run(map[bool]string{false: "empty", true: "first file"}[stagedFile], func(t *testing.T) {
			repo := t.TempDir()
			runGitTest(t, repo, "init", "-q")
			if stagedFile {
				writeStagedFile(t, repo, "first.txt", "first staged content\n")
				runGitTest(t, repo, "add", "first.txt")
			}
			snapshot, err := CaptureStagedSnapshot(context.Background(), repo, nil)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.BaseCommit != "" || snapshot.BaseTree == "" || snapshot.Tree == "" {
				t.Fatalf("unborn snapshot = %+v", snapshot)
			}
			diffs, err := NewStagedProvider(repo, snapshot, nil).GetDiff(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if stagedFile && (len(diffs) != 1 || !diffs[0].IsNew || diffs[0].NewFileContent != "first staged content\n") {
				t.Fatalf("first staged diff = %+v", diffs)
			}
			if !stagedFile {
				if len(diffs) != 0 || snapshot.Tree != snapshot.BaseTree {
					t.Fatalf("empty snapshot = %+v, diffs=%+v", snapshot, diffs)
				}
				if _, err := os.Stat(filepath.Join(repo, ".git", "index")); !os.IsNotExist(err) {
					t.Fatalf("capture created user index: %v", err)
				}
			}
		})
	}
}

func TestStagedSnapshotTrackedFilesIgnoreLiveGitignore(t *testing.T) {
	repo := initRepoWithChange(t)
	runGitTest(t, repo, "add", "sample.txt")
	writeStagedFile(t, repo, ".gitignore", "sample.txt\nnew.txt\n")
	writeStagedFile(t, repo, "new.txt", "explicitly staged despite ignore\n")
	writeStagedFile(t, repo, "vendor/excluded.txt", "provider exclusion\n")
	runGitTest(t, repo, "add", ".gitignore", "vendor/excluded.txt")
	runGitTest(t, repo, "add", "-f", "new.txt")
	snapshot := captureStagedTest(t, repo)
	writeStagedFile(t, repo, ".gitignore", "*\n")
	set, err := NewStagedProvider(repo, snapshot, nil).GetDiffSet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Included) != 3 || len(set.Excluded) != 1 || set.Excluded[0].NewPath != "vendor/excluded.txt" {
		t.Fatalf("staged filtering lost tracked changes or provider exclusions: %+v", set)
	}
	count := 0
	set.ForEachInOrder(func(_ model.Diff, _ bool) { count++ })
	if count != 4 {
		t.Fatalf("preview visits = %d", count)
	}
}

func TestStagedSnapshotUsesFrozenAttributes(t *testing.T) {
	repo := initRepoWithChange(t)
	writeStagedFile(t, repo, ".gitattributes", "sample.txt diff\n")
	runGitTest(t, repo, "add", ".")
	snapshot := captureStagedTest(t, repo)
	// The versioned snapshot requires a text diff. Neither a working-tree nor
	// an index change may later turn that diff into a skipped binary file.
	writeStagedFile(t, repo, ".gitattributes", "sample.txt -diff\n")
	runGitTest(t, repo, "add", ".gitattributes")
	diffs, err := NewStagedProvider(repo, snapshot, nil).GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range diffs {
		if d.NewPath == "sample.txt" {
			if d.IsBinary || !strings.Contains(d.Diff, "+CHANGED") {
				t.Fatalf("live attributes changed frozen text diff: %+v", d)
			}
			return
		}
	}
	t.Fatal("staged text file missing")
}

func TestStagedSnapshotRejectsIntentToAdd(t *testing.T) {
	for _, content := range []string{"", "not staged\n"} {
		t.Run(map[bool]string{true: "empty file", false: "nonempty file"}[content == ""], func(t *testing.T) {
			repo := initRepoWithChange(t)
			writeStagedFile(t, repo, "intent.txt", content)
			runGitTest(t, repo, "add", "-N", "intent.txt")
			_, err := CaptureStagedSnapshot(context.Background(), repo, nil)
			if err == nil || !strings.Contains(err.Error(), "intent-to-add") {
				t.Fatalf("expected intent-to-add error, got %v", err)
			}
		})
	}
}

func TestStagedSnapshotRejectsUnmergedIndex(t *testing.T) {
	repo := initRepoWithChange(t)
	blob := stagedGitOutput(t, repo, "rev-parse", "HEAD:sample.txt")
	cmd := exec.Command("git", "update-index", "--index-info")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader("0 0000000000000000000000000000000000000000\tsample.txt\n100644 " + blob + " 1\tsample.txt\n100644 " + blob + " 2\tsample.txt\n100644 " + blob + " 3\tsample.txt\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create conflict stages: %v\n%s", err, out)
	}
	_, err := CaptureStagedSnapshot(context.Background(), repo, nil)
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("expected unmerged-index error, got %v", err)
	}
}

func TestStagedSnapshotRejectsSplitIndex(t *testing.T) {
	repo := initRepoWithChange(t)
	runGitTest(t, repo, "update-index", "--split-index")
	_, err := CaptureStagedSnapshot(context.Background(), repo, nil)
	if err == nil || !strings.Contains(err.Error(), "split index") {
		t.Fatalf("expected split-index error, got %v", err)
	}
}

func TestStagedSnapshotRejectsSparseIndex(t *testing.T) {
	repo := initRepoWithChange(t)
	writeStagedFile(t, repo, "included/file.txt", "included\n")
	writeStagedFile(t, repo, "excluded/file.txt", "excluded\n")
	runGitTest(t, repo, "add", ".")
	runGitTest(t, repo, "commit", "-q", "-m", "add directories")
	runGitTest(t, repo, "sparse-checkout", "set", "--cone", "--sparse-index", "included")
	_, err := CaptureStagedSnapshot(context.Background(), repo, nil)
	if err == nil || !strings.Contains(err.Error(), "sparse index") {
		t.Fatalf("expected sparse-index error, got %v", err)
	}
}

func TestStagedSnapshotLinkedWorktree(t *testing.T) {
	repo := initRepoWithChange(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGitTest(t, repo, "worktree", "add", "--detach", linked, "HEAD")
	writeStagedFile(t, linked, "sample.txt", "linked staged\n")
	runGitTest(t, linked, "add", "sample.txt")
	snapshot := captureStagedTest(t, linked)
	diffs, err := NewStagedProvider(linked, snapshot, nil).GetDiff(context.Background())
	if err != nil || len(diffs) != 1 || diffs[0].NewFileContent != "linked staged\n" {
		t.Fatalf("linked worktree snapshot = %+v, err=%v", diffs, err)
	}
}

func TestStagedSnapshotHonorsAlternateIndex(t *testing.T) {
	repo := initRepoWithChange(t)
	originalPath := filepath.Join(repo, ".git", "index")
	original, err := os.ReadFile(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	alternate := filepath.Join(t.TempDir(), "alternate-index")
	if err := os.WriteFile(alternate, original, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_INDEX_FILE", alternate)
	runGitTest(t, repo, "add", "sample.txt")
	snapshot := captureStagedTest(t, repo)
	if got := stagedGitOutput(t, repo, "show", snapshot.Tree+":sample.txt"); !strings.Contains(got, "CHANGED") {
		t.Fatalf("snapshot ignored alternate index: %q", got)
	}
	after, err := os.ReadFile(originalPath)
	if err != nil || !bytes.Equal(original, after) {
		t.Fatalf("capture changed default index: %v", err)
	}
}

func TestStagedSnapshotSHA256Repository(t *testing.T) {
	repo := t.TempDir()
	runGitTest(t, repo, "init", "-q", "--object-format=sha256")
	writeStagedFile(t, repo, "first.txt", "sha256 staged\n")
	runGitTest(t, repo, "add", "first.txt")
	snapshot := captureStagedTest(t, repo)
	if len(snapshot.BaseTree) != 64 || len(snapshot.Tree) != 64 {
		t.Fatalf("incorrect object format: %+v", snapshot)
	}
	diffs, err := NewStagedProvider(repo, snapshot, nil).GetDiff(context.Background())
	if err != nil || len(diffs) != 1 || diffs[0].NewFileContent != "sha256 staged\n" {
		t.Fatalf("SHA256 staged diff: %+v, %v", diffs, err)
	}
}

func TestStagedSnapshotRejectsChangesDuringCapture(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shim relies on a shebang script")
	}
	for _, change := range []string{"index", "HEAD"} {
		t.Run(change, func(t *testing.T) {
			repo := initRepoWithChange(t)
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			base := stagedGitOutput(t, repo, "rev-parse", "HEAD")
			runGitTest(t, repo, "add", "sample.txt")
			runGitTest(t, repo, "commit", "-q", "-m", "second commit")
			second := stagedGitOutput(t, repo, "rev-parse", "HEAD")
			indexPath := filepath.Join(repo, ".git", "index")
			changedIndex, err := os.ReadFile(indexPath)
			if err != nil {
				t.Fatal(err)
			}
			replacement := filepath.Join(t.TempDir(), "replacement-index")
			if err := os.WriteFile(replacement, changedIndex, 0o600); err != nil {
				t.Fatal(err)
			}
			runGitTest(t, repo, "reset", "--mixed", base)
			t.Setenv("OCR_TEST_REAL_GIT", realGit)
			t.Setenv("OCR_TEST_LIVE_INDEX", indexPath)
			t.Setenv("OCR_TEST_REPLACEMENT_INDEX", replacement)
			t.Setenv("OCR_TEST_SECOND_COMMIT", second)
			mutation := "cp \"$OCR_TEST_REPLACEMENT_INDEX\" \"$OCR_TEST_LIVE_INDEX\""
			if change == "HEAD" {
				mutation = "\"$OCR_TEST_REAL_GIT\" update-ref HEAD \"$OCR_TEST_SECOND_COMMIT\""
			}
			// Mutate exactly after write-tree, inside the capture window, so
			// this tests the race guard without scheduler or timing assumptions.
			shimGit(t, "case \"$*\" in\n*write-tree*)\n  \"$OCR_TEST_REAL_GIT\" \"$@\" || exit $?\n  "+mutation+"\n  exit $?;;\nesac\nexec \"$OCR_TEST_REAL_GIT\" \"$@\"\n")
			_, err = CaptureStagedSnapshot(context.Background(), repo, nil)
			if err == nil || !strings.Contains(err.Error(), "changed while capturing") {
				t.Fatalf("expected concurrent %s change rejection, got %v", change, err)
			}
		})
	}
}

func TestStagedSnapshotRenameDeleteAndSymlink(t *testing.T) {
	repo := initRepoWithChange(t)
	writeStagedFile(t, repo, "delete.txt", "delete me\n")
	runGitTest(t, repo, "add", ".")
	runGitTest(t, repo, "commit", "-q", "-m", "prepare paths")
	runGitTest(t, repo, "mv", "sample.txt", "renamed.txt")
	runGitTest(t, repo, "rm", "delete.txt")
	cmd := exec.Command("git", "hash-object", "-w", "--stdin")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader("renamed.txt")
	blob, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	// Build a symlink entry directly so this works without Windows' optional
	// filesystem symlink privileges and proves no working-tree link is followed.
	runGitTest(t, repo, "update-index", "--add", "--cacheinfo", "120000,"+strings.TrimSpace(string(blob))+",link.txt")
	snapshot := captureStagedTest(t, repo)
	diffs, err := NewStagedProvider(repo, snapshot, nil).GetDiff(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 3 {
		t.Fatalf("diffs = %+v", diffs)
	}
	var deleted, renamed, symlink bool
	for _, d := range diffs {
		deleted = deleted || d.IsDeleted && d.OldPath == "delete.txt"
		renamed = renamed || d.IsRenamed && d.NewPath == "renamed.txt"
		symlink = symlink || d.NewPath == "link.txt" && d.NewFileContent == "renamed.txt"
	}
	if !deleted || !renamed || !symlink {
		t.Fatalf("missing staged path semantics: %+v", diffs)
	}
}

func TestStagedSnapshotPreservesBinaryAndModeOnlyChanges(t *testing.T) {
	repo := initRepoWithChange(t)
	runGitTest(t, repo, "checkout", "--", "sample.txt")
	runGitTest(t, repo, "update-index", "--chmod=+x", "sample.txt")
	writeStagedFile(t, repo, "binary.dat", "\x00\x01\x02")
	runGitTest(t, repo, "add", "binary.dat")
	snapshot := captureStagedTest(t, repo)
	diffs, err := NewStagedProvider(repo, snapshot, nil).GetDiff(context.Background())
	if err != nil || len(diffs) != 2 {
		t.Fatalf("staged binary/mode changes = %+v, %v", diffs, err)
	}
	for _, d := range diffs {
		switch d.NewPath {
		case "binary.dat":
			if !d.IsBinary || d.NewFileContent != "" {
				t.Fatalf("binary treated as source text: %+v", d)
			}
		case "sample.txt":
			if d.IsBinary || d.Insertions != 0 || d.Deletions != 0 || !strings.Contains(d.Diff, "new mode 100755") {
				t.Fatalf("mode-only metadata lost: %+v", d)
			}
		}
	}
}

func TestStagedSnapshotRejectsGitlinkChanges(t *testing.T) {
	for _, change := range []string{"add", "update", "delete", "file to gitlink", "gitlink to file"} {
		t.Run(change, func(t *testing.T) {
			repo := initRepoWithChange(t)
			runGitTest(t, repo, "checkout", "--", "sample.txt")
			head := stagedGitOutput(t, repo, "rev-parse", "HEAD")
			if change == "update" || change == "delete" || change == "gitlink to file" {
				runGitTest(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
				runGitTest(t, repo, "commit", "-q", "-m", "add submodule entry")
			}
			switch change {
			case "add":
				runGitTest(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
			case "update":
				newHead := stagedGitOutput(t, repo, "rev-parse", "HEAD")
				runGitTest(t, repo, "update-index", "--cacheinfo", "160000,"+newHead+",module")
			case "delete":
				runGitTest(t, repo, "update-index", "--force-remove", "module")
			case "file to gitlink":
				runGitTest(t, repo, "update-index", "--cacheinfo", "160000,"+head+",sample.txt")
			case "gitlink to file":
				blob := stagedGitOutput(t, repo, "rev-parse", "HEAD:sample.txt")
				runGitTest(t, repo, "update-index", "--cacheinfo", "100644,"+blob+",module")
			}
			snapshot := captureStagedTest(t, repo)
			_, err := NewStagedProvider(repo, snapshot, nil).GetDiff(context.Background())
			if err == nil || !strings.Contains(err.Error(), "gitlink") {
				t.Fatalf("expected explicit gitlink rejection, got %v", err)
			}
		})
	}
}

func TestStagedSnapshotUnchangedGitlinkAllowsSourceChanges(t *testing.T) {
	repo := initRepoWithChange(t)
	head := stagedGitOutput(t, repo, "rev-parse", "HEAD")
	runGitTest(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
	runGitTest(t, repo, "commit", "-q", "-m", "add submodule entry")
	runGitTest(t, repo, "add", "sample.txt")
	snapshot := captureStagedTest(t, repo)
	diffs, err := NewStagedProvider(repo, snapshot, nil).GetDiff(context.Background())
	if err != nil || len(diffs) != 1 || diffs[0].NewPath != "sample.txt" {
		t.Fatalf("unchanged gitlink blocked source review: %+v, %v", diffs, err)
	}
}

func TestStagedSnapshotGitlinkCannotBeHiddenByDiffConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config [][2]string
	}{
		{name: "log format", config: [][2]string{{"diff.submodule", "log"}}},
		{name: "inline format", config: [][2]string{{"diff.submodule", "diff"}}},
		{name: "ignore all", config: [][2]string{{"diff.ignoreSubmodules", "all"}}},
		{name: "log and ignore", config: [][2]string{{"diff.submodule", "log"}, {"diff.ignoreSubmodules", "all"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := initRepoWithChange(t)
			head := stagedGitOutput(t, repo, "rev-parse", "HEAD")
			runGitTest(t, repo, "update-index", "--add", "--cacheinfo", "160000,"+head+",module")
			runGitTest(t, repo, "commit", "-q", "-m", "add submodule entry")
			newHead := stagedGitOutput(t, repo, "rev-parse", "HEAD")
			runGitTest(t, repo, "update-index", "--cacheinfo", "160000,"+newHead+",module")
			for _, setting := range tc.config {
				runGitTest(t, repo, "config", setting[0], setting[1])
			}
			snapshot := captureStagedTest(t, repo)
			_, err := NewStagedProvider(repo, snapshot, nil).GetDiff(context.Background())
			if err == nil || !strings.Contains(err.Error(), "gitlink") {
				t.Fatalf("Git configuration hid an unsupported gitlink change: %v", err)
			}
		})
	}
}

func TestStagedSnapshotInvalidInputs(t *testing.T) {
	if _, err := CaptureStagedSnapshot(context.Background(), t.TempDir(), nil); err == nil {
		t.Fatal("non-repository accepted")
	}
	repo := initRepoWithChange(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := CaptureStagedSnapshot(ctx, repo, nil); err == nil {
		t.Fatal("cancelled capture accepted")
	}
	provider := NewStagedProvider(repo, nil, nil)
	if _, err := provider.GetDiff(context.Background()); err == nil {
		t.Fatal("missing snapshot accepted")
	}
	if got := provider.ResolveInput(context.Background()); got != (InputResolution{}) {
		t.Fatalf("missing snapshot invented identity: %+v", got)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git", "index"), []byte("corrupt index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureStagedSnapshot(context.Background(), repo, nil); err == nil {
		t.Fatal("corrupt index accepted")
	}
}

func TestStagedBaseCommitPreservesProbeFailure(t *testing.T) {
	for _, command := range []string{"symbolic-ref", "show-ref"} {
		for _, cancelled := range []bool{false, true} {
			name := command + map[bool]string{false: " diagnostic", true: " cancellation"}[cancelled]
			t.Run(name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				probeErr := errors.New("probe failed")
				_, err := resolveStagedBaseCommit(ctx, func(args ...string) (string, string, error) {
					if args[0] == command {
						if cancelled {
							cancel()
						}
						return "", "specific probe diagnostic", probeErr
					}
					if args[0] == "rev-parse" {
						return "", "", errors.New("quiet initial rev-parse failure")
					}
					return "refs/heads/main\n", "", nil
				})
				if cancelled {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("lost cancellation during %s: %v", command, err)
					}
				} else if !errors.Is(err, probeErr) || !strings.Contains(err.Error(), "specific probe diagnostic") {
					t.Fatalf("lost %s failure and diagnostic: %v", command, err)
				}
			})
		}
	}
}

func TestStagedBaseCommitReportsCorruptRef(t *testing.T) {
	repo := initRepoWithChange(t)
	ref := stagedGitOutput(t, repo, "symbolic-ref", "HEAD")
	missingObject := strings.Repeat("f", 40)
	if err := os.WriteFile(filepath.Join(repo, ".git", filepath.FromSlash(ref)), []byte(missingObject+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := stagedBaseCommit(context.Background(), repo, gitcmd.New(1))
	if err == nil || !strings.Contains(err.Error(), "git show-ref") || !strings.Contains(err.Error(), missingObject) {
		t.Fatalf("lost corrupt-ref diagnostic: %v", err)
	}
}
