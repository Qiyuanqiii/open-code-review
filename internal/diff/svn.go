// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package diff

import (
	"context"
	"encoding/xml"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/pathutil"
	"github.com/alibaba/open-code-review/internal/svncmd"
)

// SVNProvider retrieves workspace changes from a Subversion working copy.
// Subversion revisions and repository paths do not have Git's commit/range
// semantics, so this provider deliberately implements workspace mode only.
type SVNProvider struct {
	repoDir string
	run     func(context.Context, ...string) (string, error)
	info    func(context.Context) (svncmd.WorkingCopyInfo, error)
}

// NewSVNWorkspaceProvider creates a provider for local Subversion changes.
func NewSVNWorkspaceProvider(repoDir string) *SVNProvider {
	p := &SVNProvider{repoDir: repoDir}
	p.run = p.runSVN
	p.info = func(ctx context.Context) (svncmd.WorkingCopyInfo, error) {
		return svncmd.Info(ctx, repoDir)
	}
	return p
}

// GetDiff returns versioned and unversioned workspace changes.
func (p *SVNProvider) GetDiff(ctx context.Context) ([]model.Diff, error) {
	tracked, err := p.run(ctx, "diff", "--git", "--internal-diff", "--show-copies-as-adds", "--depth", "infinity", ".")
	if err != nil {
		return nil, fmt.Errorf("svn diff failed (Subversion 1.7 or newer is required): %w", err)
	}

	unversioned, err := p.unversionedFiles(ctx)
	if err != nil {
		return nil, fmt.Errorf("list unversioned Subversion files: %w", err)
	}

	var combined strings.Builder
	combined.WriteString(svnContentDiffSections(normalizeSVNDiff(p.repoDir, tracked)))
	for _, section := range workspaceFileDiffs(p.repoDir, unversioned) {
		combined.WriteString(section)
		combined.WriteByte('\n')
	}

	diffs, err := ParseDiffText(ctx, combined.String(), p.repoDir, "", nil)
	if err != nil {
		return nil, err
	}
	diffs = removeSVNPropertyOnlyDiffs(diffs)
	return (&Provider{repoDir: p.repoDir}).filterDiffs(diffs), nil
}

// ResolveInput returns no Git commit endpoints for a mutable Subversion
// working copy.
func (p *SVNProvider) ResolveInput(context.Context) InputResolution {
	return InputResolution{}
}

// RemoteIdentity returns the credential-free repository URL identity when it
// is available from local working-copy metadata.
func (p *SVNProvider) RemoteIdentity(ctx context.Context) string {
	info, err := p.info(ctx)
	if err != nil {
		return ""
	}
	identity := info.RepositoryRoot
	if identity == "" {
		identity = info.URL
	}
	return canonicalRemote(identity)
}

func (p *SVNProvider) runSVN(ctx context.Context, args ...string) (string, error) {
	out, err := svncmd.CombinedOutput(ctx, p.repoDir, args...)
	return string(out), err
}

// normalizeSVNDiff converts Subversion's repository-root-relative Git headers
// to working-copy-relative paths. `svn diff --git` always prefixes each file
// section with `Index:`, whose path is relative to the command target and is
// therefore the authoritative path for local file reads.
func normalizeSVNDiff(repoDir, raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", "\n")
	lines := strings.Split(raw, "\n")
	var out strings.Builder
	var indexPath string
	inHunk := false
	skipSection := true

	for _, line := range lines {
		if path, ok := strings.CutPrefix(line, "Index: "); ok {
			indexPath = normalizeSVNPath(repoDir, path)
			inHunk = false
			skipSection = indexPath == ""
			continue
		}
		if skipSection {
			continue
		}
		if strings.Trim(line, "=") == "" && strings.Contains(line, "=") {
			continue
		}
		if strings.HasPrefix(line, "diff --git ") && indexPath != "" {
			line = fmt.Sprintf("diff --git a/%s b/%s", indexPath, indexPath)
			inHunk = false
		}
		if strings.HasPrefix(line, "@@") {
			inHunk = true
		}
		if indexPath != "" && !inHunk {
			switch {
			case strings.HasPrefix(line, "--- ") && !strings.HasPrefix(line, "--- /dev/null"):
				line = "--- a/" + indexPath
			case strings.HasPrefix(line, "+++ ") && !strings.HasPrefix(line, "+++ /dev/null"):
				line = "+++ b/" + indexPath
			}
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.String()
}

func normalizeSVNPath(repoDir, raw string) string {
	path := filepath.FromSlash(strings.TrimSpace(raw))
	if filepath.IsAbs(path) {
		rel, err := filepath.Rel(repoDir, path)
		if err != nil {
			return ""
		}
		path = rel
	}
	path = filepath.Clean(path)
	if path == "." {
		return path
	}
	if path == ".." || strings.HasPrefix(path, ".."+string(filepath.Separator)) {
		return ""
	}
	return filepath.ToSlash(path)
}

// svnContentDiffSections drops property-only sections before parsing them.
// They have no line-addressable source change for the review agent, and trying
// to finalize a directory section would otherwise produce a misleading file
// read warning.
func svnContentDiffSections(raw string) string {
	lines := strings.Split(raw, "\n")
	var out strings.Builder
	var section []string
	meaningful := false
	inProperties := false

	flush := func() {
		if meaningful {
			out.WriteString(strings.Join(section, "\n"))
			out.WriteByte('\n')
		}
		section = nil
		meaningful = false
		inProperties = false
	}

	for _, line := range lines {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			section = []string{line}
			continue
		}
		if section == nil {
			continue
		}
		if strings.HasPrefix(line, "Property changes on: ") {
			inProperties = true
			continue
		}
		if inProperties {
			continue
		}
		section = append(section, line)
		if strings.HasPrefix(line, "@@") ||
			strings.HasPrefix(line, "new file mode ") ||
			strings.HasPrefix(line, "deleted file mode ") ||
			binaryRe.MatchString(line) || line == "GIT binary patch" ||
			strings.HasPrefix(line, "Cannot display: file marked as a binary type") {
			meaningful = true
		}
	}
	flush()
	return out.String()
}

type svnStatusDocument struct {
	Targets []struct {
		Entries []struct {
			Path   string `xml:"path,attr"`
			Status struct {
				Item string `xml:"item,attr"`
			} `xml:"wc-status"`
		} `xml:"entry"`
	} `xml:"target"`
}

func (p *SVNProvider) unversionedFiles(ctx context.Context) ([]string, error) {
	out, err := p.run(ctx, "status", "--xml", "--depth", "infinity", ".")
	if err != nil {
		return nil, err
	}
	var status svnStatusDocument
	if err := xml.Unmarshal([]byte(out), &status); err != nil {
		return nil, fmt.Errorf("parse svn status XML: %w", err)
	}

	patterns := LoadGitignorePatterns(p.repoDir)
	seen := make(map[string]struct{})
	for _, target := range status.Targets {
		for _, entry := range target.Entries {
			if entry.Status.Item != "unversioned" {
				continue
			}
			rel := normalizeSVNPath(p.repoDir, entry.Path)
			if rel == "" || rel == "." || IsPathExcluded(p.repoDir, rel, patterns) {
				continue
			}
			if err := collectUnversionedPath(ctx, p.repoDir, rel, patterns, seen); err != nil {
				return nil, err
			}
		}
	}

	files := make([]string, 0, len(seen))
	for path := range seen {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func collectUnversionedPath(ctx context.Context, repoDir, rel string, patterns []string, seen map[string]struct{}) error {
	fullPath := filepath.Join(repoDir, filepath.FromSlash(rel))
	if !pathutil.WithinBase(repoDir, fullPath) {
		return fmt.Errorf("Subversion status path %q is outside working copy", rel)
	}
	info, err := os.Lstat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		seen[rel] = struct{}{}
		return nil
	}

	return filepath.WalkDir(fullPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return nil
		}
		child, err := filepath.Rel(repoDir, path)
		if err != nil {
			return nil
		}
		child = filepath.ToSlash(child)
		if entry.IsDir() {
			if path != fullPath && IsPathExcluded(repoDir, child, patterns) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() && entry.Type()&os.ModeSymlink == 0 {
			return nil
		}
		if !IsPathExcluded(repoDir, child, patterns) {
			seen[child] = struct{}{}
		}
		return nil
	})
}

func removeSVNPropertyOnlyDiffs(diffs []model.Diff) []model.Diff {
	result := make([]model.Diff, 0, len(diffs))
	for _, d := range diffs {
		if d.Insertions == 0 && d.Deletions == 0 && !d.IsBinary && !d.IsNew && !d.IsDeleted {
			continue
		}
		result = append(result, d)
	}
	return result
}
