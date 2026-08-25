// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/url"
	pathpkg "path"
	"regexp"
	"strconv"
	"strings"
)

var (
	numericRevisionRE = regexp.MustCompile(`^[rR]?[0-9]+$`)
	dateRevisionRE    = regexp.MustCompile(`^\{[^{}\x00\r\n]+\}$`)
)

// ValidateRevision rejects unsafe or working-copy-relative Subversion
// revision spellings before they are passed to svn. Numeric revisions, HEAD,
// and repository date revisions are unambiguous and can all be frozen to a
// repository-wide numeric revision. BASE, COMMITTED, and PREV depend on mixed
// working-copy state and are deliberately rejected.
func ValidateRevision(raw string) error {
	if raw == "" {
		return fmt.Errorf("revision must not be empty")
	}
	if raw != strings.TrimSpace(raw) {
		return fmt.Errorf("revision %q must not have surrounding whitespace", raw)
	}
	if strings.HasPrefix(raw, "-") {
		return fmt.Errorf("revision %q must not start with '-'", raw)
	}
	if len(raw) > 256 {
		return fmt.Errorf("revision is too long")
	}
	if numericRevisionRE.MatchString(raw) || strings.EqualFold(raw, "HEAD") || dateRevisionRE.MatchString(raw) {
		return nil
	}
	if strings.EqualFold(raw, "BASE") || strings.EqualFold(raw, "COMMITTED") || strings.EqualFold(raw, "PREV") {
		return fmt.Errorf("revision %q depends on mutable working-copy state; use a number, HEAD, or a date revision", raw)
	}
	return fmt.Errorf("revision %q is ambiguous; use a number, HEAD, or a date revision", raw)
}

// ResolveRevision converts a validated SVN revision spelling into its stable
// repository-wide numeric revision. The repository root exists at revision 0,
// so resolving there also handles history from before the selected target was
// created.
func ResolveRevision(ctx context.Context, dir, repositoryRoot, raw string) (string, error) {
	if err := ValidateRevision(raw); err != nil {
		return "", err
	}
	if repositoryRoot == "" {
		return "", fmt.Errorf("Subversion repository root is empty")
	}
	revisionArg := raw
	if numericRevisionRE.MatchString(revisionArg) && (revisionArg[0] == 'r' || revisionArg[0] == 'R') {
		revisionArg = revisionArg[1:]
	} else if strings.EqualFold(revisionArg, "HEAD") {
		revisionArg = "HEAD"
	}
	out, err := Output(ctx, dir, "info", "--xml", "--revision", revisionArg, "--", PegTarget(repositoryRoot, "HEAD"))
	if err != nil {
		return "", fmt.Errorf("resolve Subversion revision %q: %w", raw, err)
	}
	var doc infoDocument
	if err := xml.Unmarshal(out, &doc); err != nil {
		return "", fmt.Errorf("parse resolved Subversion revision %q: %w", raw, err)
	}
	if len(doc.Entries) == 0 {
		return "", fmt.Errorf("resolve Subversion revision %q: svn info returned no entries", raw)
	}
	revision := strings.TrimSpace(doc.Entries[0].Revision)
	if !isResolvedRevision(revision) {
		return "", fmt.Errorf("resolve Subversion revision %q: svn info returned invalid revision %q", raw, revision)
	}
	return revision, nil
}

// PreviousRevision returns the revision immediately before revision. Revision
// zero has no predecessor and therefore returns an empty string.
func PreviousRevision(revision string) (string, error) {
	if !isResolvedRevision(revision) {
		return "", fmt.Errorf("invalid resolved Subversion revision %q", revision)
	}
	n, err := strconv.ParseUint(revision, 10, 64)
	if err != nil {
		return "", fmt.Errorf("parse Subversion revision %q: %w", revision, err)
	}
	if n == 0 {
		return "", nil
	}
	return strconv.FormatUint(n-1, 10), nil
}

// PegTarget appends an explicit numeric or keyword peg revision to an SVN URL.
// The last @ is the peg separator, so an earlier @ in userinfo or a path remains
// part of the URL.
func PegTarget(target, revision string) string {
	return target + "@" + revision
}

// ChildTarget builds a safely escaped child URL at an explicit peg revision.
// rel must be a repository-relative path and cannot escape the selected target.
func ChildTarget(targetURL, rel, revision string) (string, error) {
	rel = strings.ReplaceAll(rel, "\\", "/")
	clean := pathpkg.Clean(rel)
	if clean == "." || clean == "" || clean == ".." || strings.HasPrefix(clean, "../") || strings.HasPrefix(clean, "/") {
		return "", fmt.Errorf("invalid repository-relative path %q", rel)
	}
	parts := strings.Split(clean, "/")
	for i, part := range parts {
		parts[i] = url.PathEscape(part)
	}
	return PegTarget(strings.TrimRight(targetURL, "/")+"/"+strings.Join(parts, "/"), revision), nil
}

func isResolvedRevision(revision string) bool {
	if revision == "" {
		return false
	}
	for _, r := range revision {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
