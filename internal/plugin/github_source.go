// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package plugin

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type githubSource struct {
	owner     string
	repo      string
	ref       string
	directory string
	source    string
}

func parseGitHubSource(source string) (githubSource, bool, error) {
	var result githubSource
	u, err := url.Parse(source)
	if err != nil || !strings.EqualFold(u.Host, "github.com") {
		return result, false, nil
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) < 2 {
		return result, true, fmt.Errorf("GitHub source requires an owner and repository")
	}
	for index := range parts {
		parts[index], err = url.PathUnescape(parts[index])
		if err != nil {
			return result, true, fmt.Errorf("invalid GitHub source path")
		}
	}
	result.owner, result.repo = parts[0], strings.TrimSuffix(parts[1], ".git")
	for _, name := range []string{result.owner, result.repo} {
		if strings.Contains(name, "/") || safeRelativePath(name) != nil {
			return result, true, fmt.Errorf("invalid GitHub owner or repository")
		}
	}
	result.ref, result.source = "HEAD", source
	if len(parts) > 2 {
		// Release assets and archive URLs are ordinary HTTPS ZIP sources.
		if parts[2] == "releases" || parts[2] == "archive" {
			return githubSource{}, false, nil
		}
		if parts[2] != "tree" || len(parts) < 4 || parts[3] == "" {
			return result, true, fmt.Errorf("GitHub source must be a repository URL or /tree/REF/PATH URL")
		}
		result.ref = parts[3]
		if strings.ContainsAny(result.ref, "\x00\r\n") || len(result.ref) > 256 {
			return result, true, fmt.Errorf("invalid GitHub ref")
		}
		result.directory = strings.Join(parts[4:], "/")
		if result.directory != "" {
			if err := safeRelativePath(result.directory); err != nil {
				return result, true, fmt.Errorf("invalid GitHub plugin directory: %w", err)
			}
		}
	}
	return result, true, nil
}

func (s *Store) prepareGitHub(ctx context.Context, repository githubSource, pluginID, directory string) (string, string, string, string, error) {
	base := url.PathEscape(repository.owner) + "/" + url.PathEscape(repository.repo)
	ref := repository.ref
	revision := ""
	data, _, err := s.download(ctx, "https://api.github.com/repos/"+base+"/commits/"+url.PathEscape(ref), maxManifestBytes)
	if err != nil {
		var status httpStatusError
		// Public API quotas can be exhausted independently of archive downloads.
		// The exact archive bytes remain held and hashed for confirmation.
		if !errors.As(err, &status) || status.status != http.StatusForbidden {
			return "", "", "", "", err
		}
	} else {
		var commit struct {
			SHA string `json:"sha"`
		}
		if err := json.Unmarshal(data, &commit); err != nil {
			return "", "", "", "", fmt.Errorf("parse GitHub revision: %w", err)
		}
		decoded, err := hex.DecodeString(commit.SHA)
		if err != nil || len(decoded) != 20 {
			return "", "", "", "", fmt.Errorf("GitHub returned an invalid commit revision")
		}
		ref, revision = strings.ToLower(commit.SHA), strings.ToLower(commit.SHA)
	}
	archive, _, err := s.download(ctx, "https://codeload.github.com/"+base+"/zip/"+url.PathEscape(ref), maxArchiveBytes)
	if err != nil {
		return "", "", "", "", err
	}
	if err := extractZIP(ctx, archive, directory); err != nil {
		return "", "", "", "", err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return "", "", "", "", err
	}
	if len(entries) != 1 || !entries[0].IsDir() {
		return "", "", "", "", fmt.Errorf("GitHub archive must contain one repository directory")
	}
	repoRoot := filepath.Join(directory, entries[0].Name())
	packageRoot := filepath.Join(repoRoot, filepath.FromSlash(repository.directory))
	if info, err := os.Stat(packageRoot); err != nil || !info.IsDir() {
		return "", "", "", "", fmt.Errorf("GitHub plugin directory %q does not exist", repository.directory)
	}
	if _, err := os.Stat(filepath.Join(packageRoot, ManifestFilename)); errors.Is(err, os.ErrNotExist) {
		// Published packages may include built executables absent from the source
		// tree. Prefer a catalog at the selected root to its nested source package.
		for _, filename := range []string{MarketplaceFilename, "marketplace.json"} {
			catalogData, err := readFileBounded(filepath.Join(packageRoot, filename), maxCatalogBytes)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return "", "", "", "", err
			}
			packageDir, err := os.MkdirTemp(directory, ".package-*")
			if err != nil {
				return "", "", "", "", err
			}
			catalogPath := filename
			if repository.directory != "" {
				catalogPath = repository.directory + "/" + filename
			}
			catalogURL := "https://raw.githubusercontent.com/" + base + "/" + url.PathEscape(ref) + "/" + catalogPath
			prepared, digest, selection, err := s.prepareCatalog(ctx, catalogURL, catalogData, pluginID, packageDir)
			return prepared, digest, revision, selection, err
		}
	}
	selected, err := selectPackage(ctx, packageRoot, repository.source, pluginID)
	if err != nil {
		return "", "", "", "", err
	}
	manifest, err := inspectPackage(ctx, selected)
	if err != nil {
		return "", "", "", "", err
	}
	return selected, archiveDigest(archive), revision, manifest.ID, nil
}
