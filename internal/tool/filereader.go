// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package tool

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/alibaba/open-code-review/internal/gitcmd"
	"github.com/alibaba/open-code-review/internal/pathutil"
	"github.com/alibaba/open-code-review/internal/svncmd"
	"github.com/alibaba/open-code-review/internal/vcs"
)

// ReviewMode represents the active review mode.
type ReviewMode int

const (
	// ModeWorkspace reads files from the current working tree.
	ModeWorkspace ReviewMode = iota
	// ModeRange reads files as they exist at the immutable destination endpoint.
	ModeRange
	// ModeCommit reads files as they exist at one immutable revision.
	ModeCommit
)

// ParseReviewMode returns the correct ReviewMode based on provided flag values.
func ParseReviewMode(from, to, commit string) ReviewMode {
	if commit != "" {
		return ModeCommit
	}
	if from != "" && to != "" {
		return ModeRange
	}
	return ModeWorkspace
}

// RefValue returns the requested destination ref or revision for range or
// commit mode. Returns ("", false) for workspace mode.
func (m ReviewMode) RefValue(toRef, commit string) (string, bool) {
	switch m {
	case ModeRange:
		return toRef, true
	case ModeCommit:
		return commit, true
	default:
		return "", false
	}
}

// FileReader resolves file contents according to the active review mode.
type FileReader struct {
	RepoDir string
	// RepositoryKind lets workspace tools avoid a needless Git dependency in a
	// Subversion working copy. The zero value preserves historical behavior.
	RepositoryKind vcs.Kind
	Mode           ReviewMode
	// Ref is the frozen Git ref or numeric SVN revision used for immutable reads.
	// Empty for ModeWorkspace.
	Ref string
	// SVNTarget is the selected working-copy URL. It is runtime-only and may
	// contain repository routing details, so it must never be persisted.
	SVNTarget string
	Runner    *gitcmd.Runner
	// SVNOutput optionally overrides SVN command execution for tests.
	SVNOutput func(context.Context, ...string) ([]byte, error)
}

// Read returns the full content of a file path (relative to RepoDir),
// resolved according to the active review mode.
// - Workspace: reads directly from the filesystem.
// - Range / Commit: reads from the destination Git commit or SVN revision.
func (fr *FileReader) Read(ctx context.Context, path string) (string, error) {
	switch fr.Mode {
	case ModeWorkspace:
		return fr.readFromDisk(path)
	case ModeRange, ModeCommit:
		if fr.RepositoryKind == vcs.Subversion {
			return fr.readFromSVN(ctx, path)
		}
		return fr.readFromGitShow(ctx, path)
	default:
		return fr.readFromDisk(path)
	}
}

func (fr *FileReader) readFromSVN(parentCtx context.Context, path string) (string, error) {
	if fr.Ref == "" || fr.SVNTarget == "" {
		return "", fmt.Errorf("immutable Subversion file read is missing a target or revision")
	}
	target, err := svncmd.ChildTarget(fr.SVNTarget, path, fr.Ref)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
	defer cancel()
	output, err := fr.runSVNOutput(ctx, "cat", "--revision", fr.Ref, "--", target)
	if err != nil {
		return "", fmt.Errorf("svn cat %s at revision %s: %w", path, fr.Ref, err)
	}
	return string(output), nil
}

func (fr *FileReader) runSVNOutput(ctx context.Context, args ...string) ([]byte, error) {
	if fr.SVNOutput != nil {
		return fr.SVNOutput(ctx, args...)
	}
	return svncmd.Output(ctx, fr.RepoDir, args...)
}

func (fr *FileReader) listSVNFiles(ctx context.Context) ([]string, error) {
	if fr.Ref == "" || fr.SVNTarget == "" {
		return nil, fmt.Errorf("immutable Subversion file listing is missing a target or revision")
	}
	out, err := fr.runSVNOutput(ctx, "list", "--xml", "--recursive", "--revision", fr.Ref, "--", svncmd.PegTarget(fr.SVNTarget, fr.Ref))
	if err != nil {
		return nil, err
	}
	return svncmd.ParseList(out)
}

func (fr *FileReader) readFromDisk(path string) (string, error) {
	fullPath, err := fr.resolveWorkspacePath(path)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("read file %q: %w", path, err)
	}
	return string(content), nil
}

func (fr *FileReader) resolveWorkspacePath(path string) (string, error) {
	repoRoot, err := pathutil.CanonicalPath(fr.RepoDir)
	if err != nil {
		return "", fmt.Errorf("resolve repository path %q: %w", fr.RepoDir, err)
	}

	fullPath := filepath.Join(repoRoot, path)
	if !pathutil.WithinBase(repoRoot, fullPath) {
		return "", fmt.Errorf("file path %q is outside repository", path)
	}

	resolvedPath, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fullPath, nil
		}
		return "", fmt.Errorf("resolve file %q: %w", path, err)
	}
	if !pathutil.WithinBase(repoRoot, resolvedPath) {
		return "", fmt.Errorf("file path %q is outside repository", path)
	}
	return resolvedPath, nil
}

func (fr *FileReader) readFromGitShow(parentCtx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(parentCtx, 30*time.Second)
	defer cancel()

	args := []string{"-c", "core.quotepath=false", "show", "--end-of-options", fr.Ref + ":" + path}
	if fr.Runner != nil {
		output, err := fr.Runner.Output(ctx, fr.RepoDir, args...)
		if err != nil {
			return "", fmt.Errorf("git show %s:%s: %w", fr.Ref, path, err)
		}
		return string(output), nil
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = fr.RepoDir
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git show %s:%s: %w", fr.Ref, path, err)
	}
	return string(output), nil
}

// ReadLines returns a window of lines from the file plus the total line count.
// startLine is 1-based; maxLines is the maximum number of lines to collect.
func (fr *FileReader) ReadLines(ctx context.Context, path string, startLine, maxLines int) ([]string, int, error) {
	switch fr.Mode {
	case ModeWorkspace:
		return fr.readLinesFromDisk(path, startLine, maxLines)
	case ModeRange, ModeCommit:
		if fr.RepositoryKind == vcs.Subversion {
			content, err := fr.readFromSVN(ctx, path)
			if err != nil {
				return nil, 0, err
			}
			return scanLines(strings.NewReader(content), startLine, maxLines)
		}
		innerCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return fr.readLinesFromGitShow(innerCtx, path, startLine, maxLines)
	default:
		return fr.readLinesFromDisk(path, startLine, maxLines)
	}
}

// scanLines reads from r line by line, collecting at most maxLines lines
// starting from startLine (1-based), while counting the total number of lines.
// The behavior matches strings.Split(content, "\n") for trailing-newline files.
func scanLines(r io.Reader, startLine, maxLines int) ([]string, int, error) {
	br := bufio.NewReader(r)
	var collected []string
	lineNum := 0
	lastHadNewline := false

	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			lineNum++
			lastHadNewline = line[len(line)-1] == '\n'
			trimmed := strings.TrimSuffix(line, "\n")
			trimmed = strings.TrimSuffix(trimmed, "\r")
			if lineNum >= startLine && len(collected) < maxLines {
				collected = append(collected, trimmed)
			}
		}
		if err != nil {
			if err != io.EOF {
				return nil, 0, err
			}
			break
		}
	}

	if lastHadNewline {
		lineNum++
		if lineNum >= startLine && len(collected) < maxLines {
			collected = append(collected, "")
		}
	}

	return collected, lineNum, nil
}

func (fr *FileReader) readLinesFromDisk(path string, startLine, maxLines int) ([]string, int, error) {
	fullPath, err := fr.resolveWorkspacePath(path)
	if err != nil {
		return nil, 0, err
	}
	f, err := os.Open(fullPath)
	if err != nil {
		return nil, 0, fmt.Errorf("read file %q: %w", path, err)
	}
	defer f.Close()

	return scanLines(f, startLine, maxLines)
}

func (fr *FileReader) readLinesFromGitShow(ctx context.Context, path string, startLine, maxLines int) ([]string, int, error) {
	args := []string{"-c", "core.quotepath=false", "show", "--end-of-options", fr.Ref + ":" + path}

	var collected []string
	var totalLines int

	if fr.Runner != nil {
		err := fr.Runner.Stream(ctx, fr.RepoDir, func(stdout io.Reader) error {
			var scanErr error
			collected, totalLines, scanErr = scanLines(stdout, startLine, maxLines)
			return scanErr
		}, args...)
		if err != nil {
			return nil, 0, fmt.Errorf("git show %s:%s: %w", fr.Ref, path, err)
		}
		return collected, totalLines, nil
	}

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = fr.RepoDir
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, 0, fmt.Errorf("git show %s:%s: %w", fr.Ref, path, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, 0, fmt.Errorf("git show %s:%s: %w", fr.Ref, path, err)
	}

	collected, totalLines, scanErr := scanLines(stdoutPipe, startLine, maxLines)
	if scanErr != nil {
		cmd.Process.Kill()
	}
	waitErr := cmd.Wait()

	if scanErr != nil {
		return nil, 0, fmt.Errorf("git show %s:%s: %w", fr.Ref, path, scanErr)
	}
	if waitErr != nil {
		return nil, 0, fmt.Errorf("git show %s:%s: %w", fr.Ref, path, waitErr)
	}
	return collected, totalLines, nil
}
