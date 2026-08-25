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

type svnReviewMode uint8

const (
	svnModeWorkspace svnReviewMode = iota
	svnModeRange
	svnModeCommit
)

// SVNProvider retrieves workspace or immutable repository changes for the URL
// selected by a Subversion working copy.
type SVNProvider struct {
	repoDir         string
	mode            svnReviewMode
	from            string
	to              string
	commit          string
	input           InputResolution
	inputResolved   bool
	run             func(context.Context, ...string) (string, error)
	info            func(context.Context) (svncmd.WorkingCopyInfo, error)
	resolveRevision func(context.Context, string, string) (string, error)
}

// NewSVNWorkspaceProvider creates a provider for local Subversion changes.
func NewSVNWorkspaceProvider(repoDir string) *SVNProvider {
	return newSVNProvider(repoDir, svnModeWorkspace, "", "", "")
}

// NewSVNRangeProvider creates a provider comparing two SVN revisions.
func NewSVNRangeProvider(repoDir, from, to string) *SVNProvider {
	return newSVNProvider(repoDir, svnModeRange, from, to, "")
}

// NewSVNCommitProvider creates a provider for the change introduced by one SVN
// revision.
func NewSVNCommitProvider(repoDir, revision string) *SVNProvider {
	return newSVNProvider(repoDir, svnModeCommit, "", "", revision)
}

func newSVNProvider(repoDir string, mode svnReviewMode, from, to, commit string) *SVNProvider {
	p := &SVNProvider{repoDir: repoDir, mode: mode, from: from, to: to, commit: commit}
	p.run = p.runSVN
	p.info = func(ctx context.Context) (svncmd.WorkingCopyInfo, error) {
		return svncmd.Info(ctx, repoDir)
	}
	p.resolveRevision = func(ctx context.Context, repositoryRoot, raw string) (string, error) {
		return svncmd.ResolveRevision(ctx, repoDir, repositoryRoot, raw)
	}
	return p
}

// ResolveSVNInput freezes user-provided SVN revision spellings to numeric
// repository endpoints and captures the selected repository URL for immutable
// file reads.
func ResolveSVNInput(ctx context.Context, repoDir, from, to, commit string) (InputResolution, error) {
	var provider *SVNProvider
	switch {
	case commit != "":
		provider = NewSVNCommitProvider(repoDir, commit)
	case from != "" && to != "":
		provider = NewSVNRangeProvider(repoDir, from, to)
	default:
		return InputResolution{}, fmt.Errorf("Subversion input resolution requires --commit or --from/--to")
	}
	if err := provider.ensureInputResolved(ctx); err != nil {
		return InputResolution{}, err
	}
	return provider.input, nil
}

// SealInput makes the provider reuse endpoints resolved by pre-flight admission
// instead of resolving moving inputs such as HEAD again.
func (p *SVNProvider) SealInput(input InputResolution) {
	p.input = input
	p.inputResolved = true
}

// GetDiff returns workspace or immutable revision changes.
func (p *SVNProvider) GetDiff(ctx context.Context) ([]model.Diff, error) {
	if p.mode != svnModeWorkspace {
		return p.getImmutableDiff(ctx)
	}
	return p.getWorkspaceDiff(ctx)
}

func (p *SVNProvider) getWorkspaceDiff(ctx context.Context) ([]model.Diff, error) {
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

func (p *SVNProvider) getImmutableDiff(ctx context.Context) ([]model.Diff, error) {
	if err := p.ensureInputResolved(ctx); err != nil {
		return nil, err
	}
	if p.input.ResolvedHead == "" || p.input.RepositoryTarget == "" {
		return nil, fmt.Errorf("sealed Subversion input is missing its destination revision or repository target")
	}
	if p.mode == svnModeRange && p.input.ResolvedBase == "" {
		return nil, fmt.Errorf("sealed Subversion range input is missing its source revision")
	}
	if p.mode == svnModeCommit && p.input.ResolvedBase == "" {
		// Revision zero is the empty repository and has no preceding change.
		return []model.Diff{}, nil
	}

	oldTarget := svncmd.PegTarget(p.input.RepositoryTarget, p.input.ResolvedBase)
	newTarget := svncmd.PegTarget(p.input.RepositoryTarget, p.input.ResolvedHead)
	tracked, err := p.run(ctx, "diff", "--git", "--internal-diff", "--show-copies-as-adds", "--depth", "infinity", "--old", oldTarget, "--new", newTarget)
	if err != nil {
		return nil, fmt.Errorf("svn revision diff failed (Subversion 1.7 or newer is required): %w", err)
	}

	normalized := svnContentDiffSections(normalizeSVNDiff(p.repoDir, tracked))
	readContent := func(ctx context.Context, path string) ([]byte, error) {
		target, err := svncmd.ChildTarget(p.input.RepositoryTarget, path, p.input.ResolvedHead)
		if err != nil {
			return nil, err
		}
		out, err := p.run(ctx, "cat", "--revision", p.input.ResolvedHead, "--", target)
		return []byte(out), err
	}
	diffs, err := parseDiffTextWithReader(ctx, normalized, readContent)
	if err != nil {
		return nil, err
	}
	diffs = removeSVNPropertyOnlyDiffs(diffs)
	return (&Provider{repoDir: p.repoDir}).filterDiffs(diffs), nil
}

func (p *SVNProvider) ensureInputResolved(ctx context.Context) error {
	if p.mode == svnModeWorkspace || p.inputResolved {
		return nil
	}
	switch p.mode {
	case svnModeRange:
		if err := svncmd.ValidateRevision(p.from); err != nil {
			return fmt.Errorf("validate --from: %w", err)
		}
		if err := svncmd.ValidateRevision(p.to); err != nil {
			return fmt.Errorf("validate --to: %w", err)
		}
	case svnModeCommit:
		if err := svncmd.ValidateRevision(p.commit); err != nil {
			return fmt.Errorf("validate --commit: %w", err)
		}
	default:
		return fmt.Errorf("unknown Subversion review mode")
	}
	info, err := p.info(ctx)
	if err != nil {
		return fmt.Errorf("read Subversion working-copy metadata: %w", err)
	}
	if info.URL == "" || info.RepositoryRoot == "" {
		return fmt.Errorf("Subversion working-copy metadata did not report repository URLs")
	}

	p.input.RepositoryTarget = info.URL
	p.input.RepositoryRoot = info.RepositoryRoot
	p.input.RepositoryUUID = info.RepositoryUUID
	resolvedRevisions := make(map[string]string)
	resolve := func(raw string) (string, error) {
		key := raw
		if strings.EqualFold(raw, "HEAD") {
			key = "HEAD"
		}
		if revision, ok := resolvedRevisions[key]; ok {
			return revision, nil
		}
		revision, err := p.resolveRevision(ctx, info.RepositoryRoot, raw)
		if err == nil {
			resolvedRevisions[key] = revision
		}
		return revision, err
	}
	switch p.mode {
	case svnModeRange:
		base, err := resolve(p.from)
		if err != nil {
			return fmt.Errorf("resolve --from: %w", err)
		}
		head, err := resolve(p.to)
		if err != nil {
			return fmt.Errorf("resolve --to: %w", err)
		}
		p.input.ResolvedBase = base
		p.input.ResolvedHead = head
		p.input.ExactRange = base + ":" + head
	case svnModeCommit:
		head, err := resolve(p.commit)
		if err != nil {
			return fmt.Errorf("resolve --commit: %w", err)
		}
		base, err := svncmd.PreviousRevision(head)
		if err != nil {
			return err
		}
		p.input.ResolvedBase = base
		p.input.ResolvedHead = head
		if base != "" {
			p.input.ExactRange = base + ":" + head
		}
	}
	p.inputResolved = true
	return nil
}

// ResolveInput returns the numeric SVN endpoints used by GetDiff. Resolution
// errors are surfaced by GetDiff; callers invoke this after a successful load.
func (p *SVNProvider) ResolveInput(ctx context.Context) InputResolution {
	_ = p.ensureInputResolved(ctx)
	return p.input
}

// RemoteIdentity returns a stable, credential-free repository identity. SVN's
// repository UUID survives URL moves and also identifies local file repositories;
// older servers that omit it fall back to a canonical credential-free URL.
func (p *SVNProvider) RemoteIdentity(ctx context.Context) string {
	if uuid := strings.TrimSpace(p.input.RepositoryUUID); uuid != "" {
		return "svn:" + strings.ToLower(uuid)
	}
	if p.input.RepositoryRoot != "" {
		return canonicalRemote(p.input.RepositoryRoot)
	}
	info, err := p.info(ctx)
	if err != nil {
		return ""
	}
	if uuid := strings.TrimSpace(info.RepositoryUUID); uuid != "" {
		return "svn:" + strings.ToLower(uuid)
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
