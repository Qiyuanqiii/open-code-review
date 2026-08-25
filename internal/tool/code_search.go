// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package tool

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alibaba/open-code-review/internal/vcs"
	"github.com/bmatcuk/doublestar/v4"
)

const (
	gitGrepMaxCount = 100
	gitGrepTimeout  = 10 * time.Second
)

// CodeSearchProvider performs text search across the repository.
type CodeSearchProvider struct {
	FileReader *FileReader
}

func NewCodeSearch(fr *FileReader) *CodeSearchProvider { return &CodeSearchProvider{FileReader: fr} }

func (p *CodeSearchProvider) Tool() Tool { return CodeSearch }

func (p *CodeSearchProvider) Execute(ctx context.Context, args map[string]any) (string, error) {
	searchText, _ := args["search_text"].(string)
	caseSensitive, _ := args["case_sensitive"].(bool)
	usePerlRegexp, _ := args["use_perl_regexp"].(bool)

	filePatternsIface, _ := args["file_patterns"].([]any)
	var patterns []string
	for _, item := range filePatternsIface {
		if s, ok := item.(string); ok && s != "" {
			if hasTraversalPathComponent(s) {
				return "Error: file_patterns must not contain ..", nil
			}
			patterns = append(patterns, s)
		}
	}

	if strings.TrimSpace(searchText) == "" {
		return "Error: search_text is blank", nil
	}

	result, err := p.gitGrep(ctx, searchText, caseSensitive, usePerlRegexp, patterns)
	if err != nil {
		return "", fmt.Errorf("code_search failed: %w", err)
	}
	return result, nil
}

func (p *CodeSearchProvider) buildGrepArgs(searchText string, caseSensitive bool, usePerlRegexp bool, noIndex bool, pathspec []string) []string {
	cmdArgs := []string{"--no-pager", "grep"}

	if noIndex {
		// Non-git directory: search the working tree directly while still
		// honoring .gitignore and skipping .git (via --exclude-standard).
		cmdArgs = append(cmdArgs, "--no-index", "--exclude-standard")
	} else if p.FileReader.Ref == "" {
		cmdArgs = append(cmdArgs, "--untracked")
	}

	if !caseSensitive {
		cmdArgs = append(cmdArgs, "-i")
	}
	if usePerlRegexp {
		cmdArgs = append(cmdArgs, "-P")
	} else {
		cmdArgs = append(cmdArgs, "-F")
	}

	cmdArgs = append(cmdArgs, "-n", "--no-color")
	cmdArgs = append(cmdArgs, "--max-count", fmt.Sprintf("%d", gitGrepMaxCount))

	cmdArgs = append(cmdArgs, "-e", searchText)

	if ref := p.FileReader.Ref; ref != "" {
		if strings.HasPrefix(ref, "-") {
			// Defense-in-depth: reject option-like refs here even though
			// validateReviewRefs already verifies the ref upstream.
			// NOTE: git grep < 2.45 does not support --end-of-options before
			// the revision, so this is the one git invocation where we can't
			// rely on that separator.
			return nil
		}
		cmdArgs = append(cmdArgs, ref)
	}

	cmdArgs = append(cmdArgs, "--")
	cmdArgs = append(cmdArgs, pathspec...)

	return cmdArgs
}

func hasTraversalPathComponent(pathspec string) bool {
	pathspec, _ = splitSearchPathspecMagic(pathspec)
	for _, part := range strings.Split(filepath.ToSlash(pathspec), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func (p *CodeSearchProvider) runGitGrep(parentCtx context.Context, cmdArgs []string) (string, string, error) {
	ctx, cancel := context.WithTimeout(parentCtx, gitGrepTimeout)
	defer cancel()

	if p.FileReader.Runner != nil {
		stdout, stderr, err := p.FileReader.Runner.RunSplit(ctx, p.FileReader.RepoDir, cmdArgs...)
		if ctx.Err() != nil && err != nil {
			return "", "", ctx.Err()
		}
		return stdout, stderr, err
	}

	cmd := exec.CommandContext(ctx, "git", cmdArgs...)
	cmd.Dir = p.FileReader.RepoDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() != nil && err != nil && cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == -1 {
		return "", "", ctx.Err()
	}
	return stdout.String(), stderr.String(), err
}

func (p *CodeSearchProvider) gitGrep(ctx context.Context, searchText string, caseSensitive bool, usePerlRegexp bool, pathspec []string) (string, error) {
	var outStr, errStr string
	var err error
	if p.FileReader.RepositoryKind == vcs.Subversion && p.FileReader.Ref == "" {
		outStr, err = p.walkGrep(ctx, searchText, caseSensitive, usePerlRegexp, pathspec)
	} else {
		cmdArgs := p.buildGrepArgs(searchText, caseSensitive, usePerlRegexp, false, pathspec)
		if cmdArgs == nil {
			return "Error: ref must not start with '-'", nil
		}

		outStr, errStr, err = p.runGitGrep(ctx, cmdArgs)

		// Non-git directory: `git grep` exits 128 with "not a git repository".
		// `ocr scan` supports plain directories, so retry in --no-index mode, which
		// searches the working tree directly while still honoring .gitignore.
		// Ref-based search needs a real repo, so it is not retried.
		if err != nil && p.FileReader.Ref == "" && isNotGitRepoError(err, errStr) {
			cmdArgs = p.buildGrepArgs(searchText, caseSensitive, usePerlRegexp, true, pathspec)
			outStr, errStr, err = p.runGitGrep(ctx, cmdArgs)
		}
	}

	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return "code_search timed out. Try narrowing file_patterns to a more specific path.", nil
		}
		if errors.Is(err, context.Canceled) {
			return "", err
		}
		if p.FileReader.RepositoryKind == vcs.Subversion {
			return fmt.Sprintf("Error: %s", strings.TrimSpace(err.Error())), nil
		}
		if outStr == "" {
			if errStr == "" {
				return "No matches found", nil
			}
			return fmt.Sprintf("Error: %s", strings.TrimSpace(errStr)), nil
		}
	}
	if outStr == "" {
		return "No matches found", nil
	}

	lines := strings.Split(strings.TrimRight(outStr, "\n"), "\n")
	truncated := len(lines) >= gitGrepMaxCount

	type match struct {
		lineNum int
		content string
	}
	fileMatches := make(map[string][]match)
	var fileOrder []string
	seen := make(map[string]bool)

	hasRef := p.FileReader.Ref != ""
	splitN := 3
	offset := 0
	if hasRef {
		splitN = 4
		offset = 1
	}

	var sb strings.Builder
	if truncated {
		sb.WriteString(fmt.Sprintf("Note: The results have been truncated. Only showing first %d results.\n", gitGrepMaxCount))
	}

	for _, line := range lines {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", splitN)
		if len(parts) < splitN {
			continue
		}
		fname := parts[offset]
		m := match{}
		ln, parseErr := strconv.Atoi(parts[offset+1])
		if parseErr != nil {
			continue
		}
		m.lineNum = ln
		m.content = parts[offset+2]
		if !seen[fname] {
			seen[fname] = true
			fileOrder = append(fileOrder, fname)
		}
		fileMatches[fname] = append(fileMatches[fname], m)
	}

	for _, path := range fileOrder {
		matches := fileMatches[path]
		sb.WriteString(fmt.Sprintf("File: %s\nMatch lines: %d\n", path, len(matches)))
		for _, m := range matches {
			sb.WriteString(fmt.Sprintf("%d|%s\n", m.lineNum, m.content))
		}
		sb.WriteString("\n")
	}

	if err != nil && errStr != "" {
		sb.WriteString(fmt.Sprintf("Warning: %s\n", strings.TrimSpace(errStr)))
	}

	return sb.String(), nil
}

// walkGrep searches an SVN working copy without requiring Git. The output is
// shaped like `git grep -n` so the common result formatter can be reused.
func (p *CodeSearchProvider) walkGrep(parentCtx context.Context, searchText string, caseSensitive bool, useRegexp bool, pathspec []string) (string, error) {
	ctx, cancel := context.WithTimeout(parentCtx, gitGrepTimeout)
	defer cancel()

	var expression *regexp.Regexp
	if useRegexp {
		pattern := searchText
		if !caseSensitive {
			pattern = "(?i:" + pattern + ")"
		}
		var err error
		expression, err = regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("invalid regular expression: %w", err)
		}
	}

	files, err := NewFileFind(p.FileReader).listWalkFiles(ctx)
	if err != nil {
		return "", err
	}

	fixedSearch := searchText
	if !caseSensitive {
		fixedSearch = strings.ToLower(fixedSearch)
	}

	var output strings.Builder
	matchCount := 0
	for _, path := range files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if !matchesSearchPathspec(path, pathspec) {
			continue
		}

		fullPath := filepath.Join(p.FileReader.RepoDir, filepath.FromSlash(path))
		file, openErr := os.Open(fullPath)
		if openErr != nil {
			return "", fmt.Errorf("open %q: %w", path, openErr)
		}

		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64*1024), 1024*1024)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := scanner.Text()
			if strings.IndexByte(line, 0) >= 0 {
				continue
			}

			matched := false
			if expression != nil {
				matched = expression.MatchString(line)
			} else if caseSensitive {
				matched = strings.Contains(line, fixedSearch)
			} else {
				matched = strings.Contains(strings.ToLower(line), fixedSearch)
			}
			if !matched {
				continue
			}

			fmt.Fprintf(&output, "%s:%d:%s\n", path, lineNumber, line)
			matchCount++
			if matchCount >= gitGrepMaxCount {
				break
			}
		}
		scanErr := scanner.Err()
		closeErr := file.Close()
		if scanErr != nil {
			return "", fmt.Errorf("search %q: %w", path, scanErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close %q: %w", path, closeErr)
		}
		if matchCount >= gitGrepMaxCount {
			break
		}
	}

	return output.String(), nil
}

func matchesSearchPathspec(path string, pathspec []string) bool {
	if len(pathspec) == 0 {
		return true
	}

	path = filepath.ToSlash(path)
	hasPositive := false
	matchedPositive := false
	for _, rawPattern := range pathspec {
		pattern, excluded := splitSearchPathspecMagic(rawPattern)
		pattern = strings.TrimPrefix(filepath.ToSlash(pattern), "./")
		if pattern == "" {
			continue
		}
		matched := matchesOneSearchPathspec(path, pattern)
		if excluded && matched {
			return false
		}
		if !excluded {
			hasPositive = true
			matchedPositive = matchedPositive || matched
		}
	}
	return !hasPositive || matchedPositive
}

func splitSearchPathspecMagic(raw string) (string, bool) {
	switch {
	case strings.HasPrefix(raw, ":!") || strings.HasPrefix(raw, ":^"):
		return raw[2:], true
	case strings.HasPrefix(raw, ":("):
		if end := strings.IndexByte(raw, ')'); end >= 0 {
			excluded := false
			for _, magic := range strings.Split(raw[2:end], ",") {
				if magic == "exclude" || magic == "!" || magic == "^" {
					excluded = true
				}
			}
			return raw[end+1:], excluded
		}
	}
	return raw, false
}

func matchesOneSearchPathspec(path, pattern string) bool {
	if strings.HasSuffix(pattern, "/") {
		return strings.HasPrefix(path, pattern)
	}

	matched, matchErr := doublestar.Match(pattern, path)
	if matchErr == nil && matched {
		return true
	}
	if !strings.Contains(pattern, "/") {
		matched, matchErr = doublestar.Match(pattern, filepath.Base(path))
		if matchErr == nil && matched {
			return true
		}
		return path == pattern || strings.HasPrefix(path, pattern+"/")
	}
	return false
}

func isNotGitRepoError(err error, stderr string) bool {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 128 &&
		(strings.Contains(stderr, "not a git repository") || strings.Contains(stderr, ".git")) {
		return true
	}
	return false
}
