// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package diff

import (
	"context"
	"fmt"
	"strconv"

	"github.com/alibaba/open-code-review/internal/svncmd"
)

type svnInspection struct {
	status        []svncmd.StatusEntry
	info          []svncmd.WorkingCopyEntry
	mixedRevision bool
}

// inspectWorkingCopy uses a fixed number of recursive commands. Copy metadata
// therefore does not create one subprocess per changed file, and every command
// retains exec.CommandContext cancellation through svncmd.Output.
func (p *SVNProvider) inspectWorkingCopy(ctx context.Context) (svnInspection, error) {
	version, err := p.run(ctx, "--version", "--quiet")
	if err != nil {
		return svnInspection{}, fmt.Errorf("check Subversion capabilities: %w", err)
	}
	if err := svncmd.ValidateVersion(version); err != nil {
		return svnInspection{}, err
	}

	statusXML, err := p.run(ctx, "status", "--xml", "--verbose", "--depth", "infinity", "--", ".")
	if err != nil {
		return svnInspection{}, fmt.Errorf("inspect Subversion working-copy status: %w", err)
	}
	status, err := svncmd.ParseStatus([]byte(statusXML))
	if err != nil {
		return svnInspection{}, err
	}
	if err := validateSVNStatus(p.repoDir, status); err != nil {
		return svnInspection{}, err
	}

	infoXML, err := p.run(ctx, "info", "--xml", "--depth", "infinity", "--", ".")
	if err != nil {
		return svnInspection{}, fmt.Errorf("inspect Subversion working-copy metadata: %w", err)
	}
	info, err := svncmd.ParseInfoEntries([]byte(infoXML))
	if err != nil {
		return svnInspection{}, err
	}
	if err := validateSVNDepths(p.repoDir, info); err != nil {
		return svnInspection{}, err
	}

	externalsXML, err := p.run(ctx, "propget", "svn:externals", "--xml", "--recursive", "--", ".")
	if err != nil {
		return svnInspection{}, fmt.Errorf("inspect svn:externals definitions: %w", err)
	}
	externals, err := svncmd.ParseExternalTargets([]byte(externalsXML))
	if err != nil {
		return svnInspection{}, err
	}
	if len(externals) > 0 {
		path := normalizeSVNPath(p.repoDir, externals[0])
		if path == "" {
			path = externals[0]
		}
		return svnInspection{}, fmt.Errorf("unsupported Subversion working copy: svn:externals is defined at %q; review the external as a separate working copy", path)
	}

	return svnInspection{
		status:        status,
		info:          info,
		mixedRevision: hasMixedSVNRevisions(info),
	}, nil
}

func validateSVNStatus(repoDir string, entries []svncmd.StatusEntry) error {
	for _, entry := range entries {
		path := normalizeSVNPath(repoDir, entry.Path)
		if path == "" {
			return fmt.Errorf("Subversion status path %q is outside working copy", entry.Path)
		}
		switch {
		case entry.Switched:
			return fmt.Errorf("unsupported Subversion working copy: switched path %q", path)
		case entry.Item == "external":
			return fmt.Errorf("unsupported Subversion working copy: external path %q; review the external separately", path)
		case entry.Item == "obstructed" || entry.Item == "incomplete":
			return fmt.Errorf("unsupported Subversion working copy: %s path %q", entry.Item, path)
		case entry.Item == "conflicted" || entry.Properties == "conflicted" || entry.TreeConflicted:
			return fmt.Errorf("unsupported Subversion working copy: conflicted path %q", path)
		}
	}
	return nil
}

func validateSVNDepths(repoDir string, entries []svncmd.WorkingCopyEntry) error {
	for _, entry := range entries {
		if entry.Kind != "dir" || entry.Depth == "" || entry.Depth == "infinity" {
			continue
		}
		path := normalizeSVNPath(repoDir, entry.Path)
		if path == "" {
			path = entry.Path
		}
		return fmt.Errorf("unsupported sparse Subversion working copy: path %q has depth %q; use svn update --set-depth infinity", path, entry.Depth)
	}
	return nil
}

// hasMixedSVNRevisions detects distinct numeric BASE revisions. Mixed-revision
// working copies remain supported: workspace diff compares each node to its own
// BASE rather than pretending the entire tree has one revision.
func hasMixedSVNRevisions(entries []svncmd.WorkingCopyEntry) bool {
	revisions := make(map[uint64]struct{})
	for _, entry := range entries {
		revision, err := strconv.ParseUint(entry.Revision, 10, 64)
		if err != nil {
			continue
		}
		revisions[revision] = struct{}{}
		if len(revisions) > 1 {
			return true
		}
	}
	return false
}
