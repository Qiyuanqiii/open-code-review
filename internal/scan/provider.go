// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

// Package scan implements `ocr scan` — full-file code review. It owns the
// file-enumeration provider, the per-file orchestrator, and the FULL_SCAN
// prompt-template plumbing. Shared LLM tool-use loop / memory compression
// lives in internal/llmloop; this package only handles scan-specific
// concerns (enumeration, FULL_SCAN_TASK rendering, scan-specific filter).
package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/alibaba/open-code-review/internal/diff"
	"github.com/alibaba/open-code-review/internal/gitcmd"
	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/vcs"
	vcsGit "github.com/alibaba/open-code-review/internal/vcs/git"
)

// binarySniffWindow is the number of leading bytes inspected to decide
// whether a file is binary. Matches common heuristics (git, less).
const binarySniffWindow = 8000

// DefaultMaxFileSizeBytes is the default hard cap on how large a single
// file may be before the scanner skips it. The real review-feasibility
// limit is the per-file token budget (selectScanItems, ~188 KB at
// MaxTokens=58888) — this byte cap exists only to stop us from reading
// multi-MB dumps into memory. Callers can override via NewProvider.
const DefaultMaxFileSizeBytes int64 = 2 << 20 // 2 MiB

// Provider enumerates source files in a repository for full-file review.
// Unlike diff.Provider it produces no unified diffs — each ScanItem carries
// the full file content via Content, and binaries are emitted as placeholder
// entries (Content empty, IsBinary=true) so callers can still surface them
// in previews without spending memory on their bytes.
type Provider struct {
	repoDir          string
	paths            []string // empty = whole repo
	files            vcs.FileSource
	maxFileSizeBytes int64
}

// NewProvider creates a Provider that enumerates the repository at repoDir.
// If paths is non-empty each element must be a repo-relative path (file or
// directory); only matching files are returned. maxFileSizeBytes <= 0 falls
// back to DefaultMaxFileSizeBytes.
func NewProvider(repoDir string, paths []string, runner *gitcmd.Runner, maxFileSizeBytes int64) *Provider {
	return NewProviderWithSource(repoDir, paths, vcsGit.New(repoDir, runner), maxFileSizeBytes)
}

// NewProviderWithSource enumerates workspace files through source. Local file
// metadata still determines whether an entry is regular and exceeds the cap.
func NewProviderWithSource(repoDir string, paths []string, source vcs.FileSource, maxFileSizeBytes int64) *Provider {
	if source == nil {
		source = vcsGit.New(repoDir, nil)
	}
	cleaned := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Normalize: strip leading "./" and trailing "/" so prefix matching
		// against repository-relative source paths works.
		p = strings.TrimPrefix(p, "./")
		p = strings.TrimSuffix(p, "/")
		cleaned = append(cleaned, filepath.ToSlash(p))
	}
	if maxFileSizeBytes <= 0 {
		maxFileSizeBytes = DefaultMaxFileSizeBytes
	}
	return &Provider{
		repoDir:          repoDir,
		paths:            cleaned,
		files:            source,
		maxFileSizeBytes: maxFileSizeBytes,
	}
}

// Enumerate returns one ScanItem per reviewable file. Binaries are emitted
// with empty Content + IsBinary=true so previews can show them as excluded.
func (p *Provider) Enumerate(ctx context.Context) ([]model.ScanItem, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	files, err := p.files.ListFiles(ctx, vcs.ContentRef{})
	if err != nil {
		return nil, err
	}

	if len(p.paths) > 0 {
		files = filterByPaths(files, p.paths)
	}

	gitignorePatterns := diff.LoadGitignorePatterns(p.repoDir)

	var out []model.ScanItem
	for _, rel := range files {
		// Per-iteration cancellation check: a large repo with thousands of
		// files may take seconds to walk, and downstream Lstat / ReadFile
		// each cost a syscall — abort early when ctx is cancelled.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if rel == "" {
			continue
		}
		if diff.IsPathExcluded(p.repoDir, rel, gitignorePatterns) {
			continue
		}
		full := filepath.Join(p.repoDir, rel)
		info, err := os.Lstat(full)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[ocr] WARNING: cannot stat %s: %v\n", rel, err)
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Size() > p.maxFileSizeBytes {
			fmt.Fprintf(os.Stderr, "[ocr] WARNING: skipping %s (%d bytes exceeds %d-byte scan limit; raise MaxTokens if the real concern is token budget, not memory)\n",
				rel, info.Size(), p.maxFileSizeBytes)
			continue
		}
		var content []byte
		var binary bool
		err = p.files.ReadFile(ctx, vcs.ContentRef{}, rel, func(r io.Reader) error {
			var readErr error
			content, binary, readErr = readScanContent(r, p.maxFileSizeBytes)
			return readErr
		})
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "[ocr] WARNING: cannot read %s: %v\n", rel, err)
			continue
		}
		if binary {
			// Emit placeholder so preview can display [B], but do not
			// read the file body — saves memory on large binaries.
			out = append(out, model.ScanItem{
				Path:     rel,
				IsBinary: true,
			})
			continue
		}
		out = append(out, model.ScanItem{
			Path:      rel,
			Content:   string(content),
			IsBinary:  false,
			LineCount: countLines(content),
		})
	}
	return out, nil
}

var errScanFileTooLarge = errors.New("file exceeds scan size limit")

// readScanContent stops after the binary sniff for binary placeholders and
// bounds text reads even if a workspace file grows after its metadata check.
func readScanContent(r io.Reader, maxBytes int64) ([]byte, bool, error) {
	limited := &io.LimitedReader{R: r, N: maxBytes}
	prefix := make([]byte, binarySniffWindow)
	n, err := io.ReadFull(limited, prefix)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, false, err
	}
	prefix = prefix[:n]
	if bytes.IndexByte(prefix, 0) >= 0 {
		return nil, true, nil
	}
	rest, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if limited.N == 0 {
		var extra [1]byte
		n, err := io.ReadFull(r, extra[:])
		if n != 0 {
			return nil, false, errScanFileTooLarge
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, false, err
		}
	}
	return append(prefix, rest...), false, nil
}

// filterByPaths keeps only entries whose path equals a user-supplied path
// (for exact files) or lies under it (for directories).
func filterByPaths(all []string, paths []string) []string {
	var out []string
	for _, f := range all {
		for _, want := range paths {
			if f == want || strings.HasPrefix(f, want+"/") {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// countLines returns the number of lines in content. A file without a
// trailing newline still counts its final line. Empty input → 0.
func countLines(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	n := bytes.Count(content, []byte{'\n'})
	if content[len(content)-1] != '\n' {
		n++
	}
	return n
}

// isBinaryFile reads up to binarySniffWindow bytes from path and reports
// whether they contain a NUL byte (git's "binary" heuristic).
func isBinaryFile(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	buf := make([]byte, binarySniffWindow)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false, err
	}
	return bytes.IndexByte(buf[:n], 0) >= 0, nil
}
