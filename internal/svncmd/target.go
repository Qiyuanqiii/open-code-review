// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package svncmd

import (
	"context"
	"fmt"
	"net/url"
	pathpkg "path"
	"strings"
	"unicode"
)

const maximumTargetLength = 4096

// TargetSpec is a validated remote Subversion target and its optional peg
// revision. Target never contains userinfo, a query, a fragment, or peg syntax.
type TargetSpec struct {
	Target      string
	PegRevision string
}

// ResolvedTarget is one immutable side of a repository-to-repository
// comparison. URL and repository routing data are runtime-only.
type ResolvedTarget struct {
	URL               string
	RepositoryRoot    string
	RepositoryUUID    string
	OperativeRevision string
	PegRevision       string
}

// ResolvedTargetPair contains the sealed source and destination targets used by
// an exact remote Subversion comparison.
type ResolvedTargetPair struct {
	Source      ResolvedTarget
	Destination ResolvedTarget
}

// ParseTargetSpec validates a repository URL (or ^/ working-copy-relative URL)
// with an optional @PEG suffix. A trailing @ escapes a literal @ in the target
// and causes the operative revision to be used as the peg revision.
func ParseTargetSpec(raw string) (TargetSpec, error) {
	if raw == "" {
		return TargetSpec{}, fmt.Errorf("Subversion target must not be empty")
	}
	if raw != strings.TrimSpace(raw) {
		return TargetSpec{}, fmt.Errorf("Subversion target must not have surrounding whitespace")
	}
	if len(raw) > maximumTargetLength {
		return TargetSpec{}, fmt.Errorf("Subversion target is too long")
	}
	if strings.HasPrefix(raw, "-") {
		return TargetSpec{}, fmt.Errorf("Subversion target must not start with '-'")
	}
	for _, r := range raw {
		if unicode.IsControl(r) {
			return TargetSpec{}, fmt.Errorf("Subversion target must not contain control characters")
		}
	}

	target := raw
	peg := ""
	if strings.HasSuffix(target, "@") {
		target = strings.TrimSuffix(target, "@")
	} else if separator := strings.LastIndexByte(target, '@'); separator >= 0 {
		candidate := target[separator+1:]
		if err := ValidateRevision(candidate); err != nil {
			return TargetSpec{}, fmt.Errorf("Subversion target has an invalid peg revision; use @<number>, @HEAD, or @{date}, or append a trailing @ to escape a literal @")
		}
		peg = candidate
		target = target[:separator]
	}
	if target == "" {
		return TargetSpec{}, fmt.Errorf("Subversion target URL must not be empty")
	}
	for _, r := range target {
		if unicode.IsSpace(r) {
			return TargetSpec{}, fmt.Errorf("Subversion target URL must not contain whitespace")
		}
	}
	if strings.HasPrefix(target, "^/") {
		if strings.ContainsAny(target, "?#") {
			return TargetSpec{}, fmt.Errorf("repository-relative Subversion target must not contain a query string or fragment")
		}
		relativePath := pathpkg.Clean(strings.TrimPrefix(target, "^/"))
		if relativePath == "." {
			return TargetSpec{Target: "^/", PegRevision: peg}, nil
		}
		if relativePath == ".." || strings.HasPrefix(relativePath, "../") || strings.HasPrefix(relativePath, "/") {
			return TargetSpec{}, fmt.Errorf("repository-relative Subversion target must stay within the repository root")
		}
		return TargetSpec{Target: "^/" + relativePath, PegRevision: peg}, nil
	}

	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme == "" {
		return TargetSpec{}, fmt.Errorf("Subversion target must be an absolute repository URL or start with ^/")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" && scheme != "file" && scheme != "svn" && !(strings.HasPrefix(scheme, "svn+") && len(scheme) > len("svn+")) {
		return TargetSpec{}, fmt.Errorf("Subversion target uses an unsupported URL scheme")
	}
	if parsed.User != nil {
		return TargetSpec{}, fmt.Errorf("Subversion target must not contain username or password userinfo; configure SVN authentication separately")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return TargetSpec{}, fmt.Errorf("Subversion target must not contain a query string")
	}
	if parsed.Fragment != "" {
		return TargetSpec{}, fmt.Errorf("Subversion target must not contain a fragment")
	}
	if scheme != "file" && parsed.Host == "" {
		return TargetSpec{}, fmt.Errorf("Subversion target URL must include a host")
	}
	if scheme == "file" && parsed.Path == "" {
		return TargetSpec{}, fmt.Errorf("file Subversion target URL must include a path")
	}
	parsed.Scheme = scheme
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.Path = normalizeTargetPath(parsed.Path)
	parsed.RawPath = ""
	return TargetSpec{Target: parsed.String(), PegRevision: peg}, nil
}

// ResolveTargetPair validates that both targets are directories in the same
// repository, freezes all revision spellings once, and re-inspects the targets
// at the resulting numeric operative and peg revisions.
func ResolveTargetPair(ctx context.Context, dir, fromRevision, toRevision, fromTarget, toTarget string) (ResolvedTargetPair, error) {
	if err := ValidateRevision(fromRevision); err != nil {
		return ResolvedTargetPair{}, fmt.Errorf("validate source operative revision: %w", err)
	}
	if err := ValidateRevision(toRevision); err != nil {
		return ResolvedTargetPair{}, fmt.Errorf("validate destination operative revision: %w", err)
	}
	sourceSpec, err := ParseTargetSpec(fromTarget)
	if err != nil {
		return ResolvedTargetPair{}, fmt.Errorf("validate source target: %w", err)
	}
	destinationSpec, err := ParseTargetSpec(toTarget)
	if err != nil {
		return ResolvedTargetPair{}, fmt.Errorf("validate destination target: %w", err)
	}
	if strings.HasPrefix(sourceSpec.Target, "^/") || strings.HasPrefix(destinationSpec.Target, "^/") {
		if _, infoErr := Info(ctx, dir); infoErr != nil {
			if ctx.Err() != nil {
				return ResolvedTargetPair{}, ctx.Err()
			}
			return ResolvedTargetPair{}, fmt.Errorf("repository-relative SVN targets require --repo to name a Subversion working copy")
		}
	}

	sourcePegRaw := sourceSpec.PegRevision
	if sourcePegRaw == "" {
		sourcePegRaw = fromRevision
	}
	destinationPegRaw := destinationSpec.PegRevision
	if destinationPegRaw == "" {
		destinationPegRaw = toRevision
	}

	sourceInitial, err := inspectTarget(ctx, dir, sourceSpec.Target, fromRevision, sourcePegRaw)
	if err != nil {
		return ResolvedTargetPair{}, fmt.Errorf("inspect SVN source target: %w", err)
	}
	destinationInitial, err := inspectTarget(ctx, dir, destinationSpec.Target, toRevision, destinationPegRaw)
	if err != nil {
		return ResolvedTargetPair{}, fmt.Errorf("inspect SVN destination target: %w", err)
	}
	if !sameRepository(sourceInitial, destinationInitial) {
		return ResolvedTargetPair{}, fmt.Errorf("source and destination SVN targets must belong to the same repository")
	}

	repositoryRoot := sourceInitial.RepositoryRoot
	if repositoryRoot == "" {
		return ResolvedTargetPair{}, fmt.Errorf("SVN target metadata did not report a repository root")
	}
	resolved := make(map[string]string)
	freeze := func(raw string) (string, error) {
		key := revisionCacheKey(raw)
		if value, ok := resolved[key]; ok {
			return value, nil
		}
		value, err := resolveRevision(ctx, dir, repositoryRoot, raw, true)
		if err == nil {
			resolved[key] = value
		}
		return value, err
	}
	sourceRevision, err := freeze(fromRevision)
	if err != nil {
		return ResolvedTargetPair{}, fmt.Errorf("resolve source operative revision: %w", err)
	}
	destinationRevision, err := freeze(toRevision)
	if err != nil {
		return ResolvedTargetPair{}, fmt.Errorf("resolve destination operative revision: %w", err)
	}
	sourcePeg, err := freeze(sourcePegRaw)
	if err != nil {
		return ResolvedTargetPair{}, fmt.Errorf("resolve source peg revision: %w", err)
	}
	destinationPeg, err := freeze(destinationPegRaw)
	if err != nil {
		return ResolvedTargetPair{}, fmt.Errorf("resolve destination peg revision: %w", err)
	}

	sourceFinal, err := inspectTarget(ctx, dir, sourceSpec.Target, sourceRevision, sourcePeg)
	if err != nil {
		return ResolvedTargetPair{}, fmt.Errorf("inspect sealed SVN source target: %w", err)
	}
	destinationFinal, err := inspectTarget(ctx, dir, destinationSpec.Target, destinationRevision, destinationPeg)
	if err != nil {
		return ResolvedTargetPair{}, fmt.Errorf("inspect sealed SVN destination target: %w", err)
	}
	if !sameRepository(sourceFinal, destinationFinal) || !sameRepository(sourceInitial, sourceFinal) || !sameRepository(destinationInitial, destinationFinal) {
		return ResolvedTargetPair{}, fmt.Errorf("SVN repository identity changed while targets were being sealed")
	}
	// svn info resolves each peg target to the node's URL at the frozen
	// operative revision. Downstream commands use that historical URL together
	// with the same numeric operative revision, so they no longer depend on a
	// moving peg or on a path that did not exist at the operative endpoint.
	sourceFinal.OperativeRevision = sourceRevision
	sourceFinal.PegRevision = sourcePeg
	destinationFinal.OperativeRevision = destinationRevision
	destinationFinal.PegRevision = destinationPeg
	return ResolvedTargetPair{Source: sourceFinal, Destination: destinationFinal}, nil
}

func inspectTarget(ctx context.Context, dir, target, operativeRevision, pegRevision string) (ResolvedTarget, error) {
	args := []string{"info", "--xml", "--non-interactive", "--depth", "empty", "--revision", revisionArgument(operativeRevision), "--", PegTarget(target, revisionArgument(pegRevision))}
	out, err := Output(ctx, dir, args...)
	if err != nil {
		return ResolvedTarget{}, err
	}
	entries, err := ParseInfoEntries(out)
	if err != nil {
		return ResolvedTarget{}, err
	}
	entry := entries[0]
	if entry.Kind != "dir" {
		return ResolvedTarget{}, fmt.Errorf("SVN comparison target must be a directory")
	}
	targetURL, err := credentialFreeURL(entry.URL)
	if err != nil {
		return ResolvedTarget{}, fmt.Errorf("SVN target metadata returned an invalid URL")
	}
	repositoryRoot, err := credentialFreeURL(entry.RepositoryRoot)
	if err != nil {
		return ResolvedTarget{}, fmt.Errorf("SVN target metadata returned an invalid repository root")
	}
	return ResolvedTarget{
		URL:            targetURL,
		RepositoryRoot: repositoryRoot,
		RepositoryUUID: strings.TrimSpace(entry.RepositoryUUID),
	}, nil
}

func sameRepository(left, right ResolvedTarget) bool {
	leftUUID := strings.ToLower(strings.TrimSpace(left.RepositoryUUID))
	rightUUID := strings.ToLower(strings.TrimSpace(right.RepositoryUUID))
	if leftUUID != "" && rightUUID != "" {
		return leftUUID == rightUUID
	}
	return left.RepositoryRoot != "" && canonicalTargetIdentity(left.RepositoryRoot) == canonicalTargetIdentity(right.RepositoryRoot)
}

func credentialFreeURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" {
		return "", fmt.Errorf("invalid URL")
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.Path = normalizeTargetPath(parsed.Path)
	parsed.RawPath = ""
	return parsed.String(), nil
}

func canonicalTargetIdentity(raw string) string {
	clean, err := credentialFreeURL(raw)
	if err != nil {
		return ""
	}
	return clean
}

func normalizeTargetPath(targetPath string) string {
	if targetPath == "" {
		return targetPath
	}
	clean := pathpkg.Clean(targetPath)
	if strings.HasPrefix(targetPath, "/") && !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	if clean == "." {
		return ""
	}
	return clean
}

func revisionCacheKey(raw string) string {
	if strings.EqualFold(raw, "HEAD") {
		return "HEAD"
	}
	if len(raw) > 1 && (raw[0] == 'r' || raw[0] == 'R') {
		return raw[1:]
	}
	return raw
}
