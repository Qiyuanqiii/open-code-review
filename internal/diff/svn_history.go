// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package diff

import (
	"fmt"
	"net/url"
	pathpkg "path"
	"sort"
	"strings"

	"github.com/alibaba/open-code-review/internal/model"
	"github.com/alibaba/open-code-review/internal/svncmd"
)

type svnCopyRoot struct {
	path     string
	source   string
	revision string
}

type svnMoveRoot struct {
	from string
	to   string
}

func annotateSVNHistory(repoDir string, diffs []model.Diff, inspection svnInspection) error {
	if len(diffs) == 0 {
		return nil
	}
	statusByPath := make(map[string]svncmd.StatusEntry, len(inspection.status))
	var moves []svnMoveRoot
	seenMoves := make(map[string]struct{})
	for _, status := range inspection.status {
		path := normalizeSVNPath(repoDir, status.Path)
		if path == "" {
			continue
		}
		statusByPath[path] = status
		if status.MovedFrom != "" {
			from := normalizeSVNPath(repoDir, status.MovedFrom)
			if from != "" {
				moves = appendSVNMove(moves, seenMoves, from, path)
			}
		} else if status.MovedTo != "" {
			to := normalizeSVNPath(repoDir, status.MovedTo)
			if to != "" {
				moves = appendSVNMove(moves, seenMoves, path, to)
			}
		}
	}

	var copies []svnCopyRoot
	for _, entry := range inspection.info {
		if entry.CopyFromURL == "" {
			continue
		}
		path := normalizeSVNPath(repoDir, entry.Path)
		if path == "" {
			continue
		}
		source, err := repositoryRelativeSVNPath(entry.RepositoryRoot, entry.CopyFromURL)
		if err != nil {
			return fmt.Errorf("resolve copy source for %q: %w", path, err)
		}
		copies = append(copies, svnCopyRoot{path: path, source: source, revision: entry.CopyFromRevision})
	}
	sort.Slice(copies, func(i, j int) bool { return len(copies[i].path) > len(copies[j].path) })

	for i := range diffs {
		d := &diffs[i]
		newPath := d.NewPath
		oldPath := d.OldPath
		if status, ok := statusByPath[newPath]; ok && status.Item == "replaced" {
			d.IsReplaced = true
		}
		if status, ok := statusByPath[oldPath]; ok && status.Item == "replaced" {
			d.IsReplaced = true
		}

		if status, ok := statusByPath[newPath]; d.IsNew && ok && status.Copied {
			if copyRoot, ok := matchingCopyRoot(copies, newPath); ok {
				suffix := strings.TrimPrefix(strings.TrimPrefix(newPath, copyRoot.path), "/")
				d.IsCopied = true
				d.CopyFromPath = appendSVNPath(copyRoot.source, suffix)
				d.CopyFromRevision = copyRoot.revision
			}
			if move, ok := matchingMoveDestination(moves, newPath); ok {
				suffix := strings.TrimPrefix(strings.TrimPrefix(newPath, move.to), "/")
				d.MovedFromPath = appendSVNPath(move.from, suffix)
			}
		}
		if d.IsDeleted {
			if move, ok := matchingMoveSource(moves, oldPath); ok {
				suffix := strings.TrimPrefix(strings.TrimPrefix(oldPath, move.from), "/")
				d.MovedToPath = appendSVNPath(move.to, suffix)
			}
		}
		addSVNHistoryHeaders(d)
	}
	return nil
}

func appendSVNMove(moves []svnMoveRoot, seen map[string]struct{}, from, to string) []svnMoveRoot {
	key := from + "\x00" + to
	if _, ok := seen[key]; ok {
		return moves
	}
	seen[key] = struct{}{}
	return append(moves, svnMoveRoot{from: from, to: to})
}

func matchingCopyRoot(roots []svnCopyRoot, path string) (svnCopyRoot, bool) {
	for _, root := range roots {
		if path == root.path || strings.HasPrefix(path, root.path+"/") {
			return root, true
		}
	}
	return svnCopyRoot{}, false
}

func matchingMoveDestination(roots []svnMoveRoot, path string) (svnMoveRoot, bool) {
	var match svnMoveRoot
	for _, root := range roots {
		if (path == root.to || strings.HasPrefix(path, root.to+"/")) && len(root.to) > len(match.to) {
			match = root
		}
	}
	return match, match.to != ""
}

func matchingMoveSource(roots []svnMoveRoot, path string) (svnMoveRoot, bool) {
	var match svnMoveRoot
	for _, root := range roots {
		if (path == root.from || strings.HasPrefix(path, root.from+"/")) && len(root.from) > len(match.from) {
			match = root
		}
	}
	return match, match.from != ""
}

func appendSVNPath(root, suffix string) string {
	if suffix == "" {
		return root
	}
	return strings.TrimSuffix(root, "/") + "/" + suffix
}

func addSVNHistoryHeaders(d *model.Diff) {
	var headers []string
	if d.IsCopied {
		source := d.CopyFromPath
		if d.CopyFromRevision != "" {
			source += "@" + d.CopyFromRevision
		}
		headers = append(headers, "copy from "+source, "copy to "+d.NewPath)
	}
	if d.MovedFromPath != "" {
		headers = append(headers, "svn move from "+d.MovedFromPath)
	}
	if d.MovedToPath != "" {
		headers = append(headers, "svn move to "+d.MovedToPath)
	}
	if d.IsReplaced {
		headers = append(headers, "svn replacement")
	}
	if len(headers) == 0 {
		return
	}
	parts := strings.SplitN(d.Diff, "\n", 2)
	if len(parts) == 1 {
		d.Diff += "\n" + strings.Join(headers, "\n")
		return
	}
	d.Diff = parts[0] + "\n" + strings.Join(headers, "\n") + "\n" + parts[1]
}

func repositoryRelativeSVNPath(repositoryRoot, candidate string) (string, error) {
	root, err := url.Parse(repositoryRoot)
	if err != nil || root.Scheme == "" {
		return "", fmt.Errorf("invalid repository root URL")
	}
	source, err := url.Parse(candidate)
	if err != nil || source.Scheme == "" {
		return "", fmt.Errorf("invalid copy-from URL")
	}
	if !strings.EqualFold(root.Scheme, source.Scheme) || !strings.EqualFold(root.Host, source.Host) {
		return "", fmt.Errorf("copy-from URL is outside repository root")
	}
	rootPath := cleanSVNURLPath(root.Path)
	sourcePath := cleanSVNURLPath(source.Path)
	if rootPath != "/" && sourcePath != rootPath && !strings.HasPrefix(sourcePath, rootPath+"/") {
		return "", fmt.Errorf("copy-from URL is outside repository root")
	}
	relative := strings.TrimPrefix(sourcePath, "/")
	if rootPath != "/" {
		relative = strings.TrimPrefix(strings.TrimPrefix(sourcePath, rootPath), "/")
	}
	if strings.ContainsAny(relative, "\x00\r\n") {
		return "", fmt.Errorf("copy-from URL contains an unsupported control character")
	}
	if relative == "" {
		return "^/", nil
	}
	return "^/" + relative, nil
}

func cleanSVNURLPath(value string) string {
	return pathpkg.Clean("/" + strings.TrimPrefix(value, "/"))
}
