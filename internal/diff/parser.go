// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

// Package diff parses Git-compatible unified diff output into structured Diff
// objects.
package diff

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/alibaba/open-code-review/internal/gitcmd"
	"github.com/alibaba/open-code-review/internal/model"
)

var (
	diffHeaderRe = regexp.MustCompile(`^diff --git a/(.+?) b/(.+)$`)
	// Anchored: git emits the marker at column 0 ("Binary files a/x and b/y
	// differ"). Content lines inside hunks always carry a leading "+", "-"
	// or " " prefix, so an anchored match can never misfire on file content.
	binaryRe = regexp.MustCompile(`^Binary files `)
)

type diffContentReader func(context.Context, string) ([]byte, error)

// ParseDiffText splits the unified diff text into per-file Diff structs.
// ref, if non-empty, is a Git ref used to read new-file content via git show
// instead of reading from the working tree. runner, if non-nil, executes Git
// subprocesses through a shared concurrency limiter.
func ParseDiffText(ctx context.Context, diffText string, repoDir string, ref string, runner *gitcmd.Runner) ([]model.Diff, error) {
	readContent := func(ctx context.Context, path string) ([]byte, error) {
		if ref != "" {
			args := []string{"-c", "core.quotepath=false", "show", "--end-of-options", ref + ":" + path}
			if runner != nil {
				return runner.Output(ctx, repoDir, args...)
			}
			cmd := exec.CommandContext(ctx, "git", args...)
			cmd.Dir = repoDir
			return cmd.Output()
		}
		return readWorkspaceFileForDiff(repoDir, path)
	}
	return parseDiffTextWithReader(ctx, diffText, readContent)
}

// parseDiffTextWithReader lets non-Git providers supply immutable destination
// content while reusing the Git-compatible unified-diff parser.
func parseDiffTextWithReader(ctx context.Context, diffText string, readContent diffContentReader) ([]model.Diff, error) {
	lines := strings.Split(diffText, "\n")
	var diffs []model.Diff
	var current *model.Diff
	var buf strings.Builder
	// inHunk tracks whether the current line sits inside a "@@" hunk of the
	// current file's section. Only hunk content lines carry a leading
	// "+"/"-"/" " marker, so insertion/deletion counting and the binary
	// marker must look at hunk state: outside a hunk, "+++ b/file" and
	// "--- a/file" are headers, not content; inside a hunk, an added line
	// like "++i" renders as "+++i" and still counts as an insertion.
	inHunk := false

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	for _, line := range lines {
		if m := diffHeaderRe.FindStringSubmatch(line); m != nil {
			// Flush previous diff
			if current != nil {
				current.Diff = strings.TrimSuffix(buf.String(), "\n")
				finalizeDiff(ctx, current, readContent)
				diffs = append(diffs, *current)
				buf.Reset()
			}
			current = &model.Diff{
				OldPath: m[1],
				NewPath: m[2],
			}
			inHunk = false
		}
		if current == nil {
			continue
		}

		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		// The object IDs and mode in Git's extended "index" header are not
		// useful review context. Keep index text in hunks, where it is file
		// content and therefore carries a diff prefix.
		case !inHunk && strings.HasPrefix(line, "index "):
			continue
		case !inHunk && binaryRe.MatchString(line):
			current.IsBinary = true
		case !inHunk && (line == "GIT binary patch" ||
			strings.HasPrefix(line, "Cannot display: file marked as a binary type")):
			current.IsBinary = true
		// Extended header lines (unambiguous: content lines always carry a
		// leading "+", "-" or " " prefix, so a bare prefix match is safe).
		case strings.HasPrefix(line, "new file mode "):
			current.IsNew = true
		case strings.HasPrefix(line, "deleted file mode "):
			current.IsDeleted = true
		case strings.HasPrefix(line, "rename from "):
			// Authoritative old path for renames; more reliable than the
			// "diff --git" header when paths contain spaces.
			current.OldPath = strings.TrimPrefix(line, "rename from ")
			current.IsRenamed = true
		case strings.HasPrefix(line, "rename to "):
			current.NewPath = strings.TrimPrefix(line, "rename to ")
			current.IsRenamed = true
		// git emits "--- /dev/null" / "+++ /dev/null" without a/ b/ prefixes.
		// Guarded by inHunk: inside a hunk the same strings can be content
		// (e.g. an added line "++ /dev/null").
		case !inHunk && line == "--- /dev/null":
			current.IsNew = true
		case !inHunk && line == "+++ /dev/null":
			current.IsDeleted = true
		case inHunk && strings.HasPrefix(line, "+"):
			current.Insertions++
		case inHunk && strings.HasPrefix(line, "-"):
			current.Deletions++
		}
		buf.WriteString(line)
		buf.WriteString("\n")
	}

	// Flush last diff
	if current != nil {
		current.Diff = strings.TrimSuffix(buf.String(), "\n")
		finalizeDiff(ctx, current, readContent)
		diffs = append(diffs, *current)
	}

	return diffs, nil
}

// finalizeDiff reads destination content through the active VCS provider.
func finalizeDiff(ctx context.Context, d *model.Diff, readContent diffContentReader) {
	if d.IsDeleted || d.NewPath == "/dev/null" {
		d.NewPath = "/dev/null"
		return
	}
	content, err := readContent(ctx, d.NewPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[ocr] WARNING: cannot read file %s for review: %v\n", d.NewPath, err)
		return
	}
	d.NewFileContent = string(content)
}
