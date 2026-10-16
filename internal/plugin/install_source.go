// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// SelectionRequiredError reports the plugins available from a multi-plugin
// source. Select a candidate's ID in the next PrepareInstall call.
type SelectionRequiredError struct {
	Candidates []CatalogEntry
}

func (e *SelectionRequiredError) Error() string {
	ids := make([]string, 0, len(e.Candidates))
	for _, candidate := range e.Candidates {
		ids = append(ids, candidate.ID)
	}
	return fmt.Sprintf("source contains multiple plugins (%s); select one with --plugin ID", strings.Join(ids, ", "))
}

type packageArtifact struct {
	Source    string
	Directory string
}

// PreparedInstall holds a validated package for inspection before installation.
// Exported metadata is a copy; changing it never changes the installed package.
// Close and InstallPrepared are serialized. A successful install consumes the
// preparation, and Close is idempotent.
type PreparedInstall struct {
	Manifest            Manifest
	Source              string
	SHA256              string
	ArchiveSHA256       string
	ContentSHA256       string
	ContentDigestFormat string
	Revision            string
	PluginID            string

	mu          sync.Mutex
	storeRoot   string
	item        Installed
	directory   string
	packageDir  string
	packageHash string
	artifact    packageArtifact
	closed      bool
}

func (p *PreparedInstall) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	return os.RemoveAll(p.directory)
}

// PrepareInstall downloads and validates a source without changing the registry.
// A single plugin is selected automatically. pluginID selects an entry from a
// catalog or repository containing several plugins.
func (s *Store) PrepareInstall(ctx context.Context, source, pluginID string) (*PreparedInstall, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, err := s.readState()
	if err != nil {
		return nil, err
	}
	return s.prepareInstall(ctx, source, pluginID, value.Markets)
}

func (s *Store) prepareInstall(ctx context.Context, source, pluginID string, markets map[string]Market) (*PreparedInstall, error) {
	resolved, origin, expected, err := resolveInstallSource(source, markets)
	if err != nil {
		return nil, err
	}
	if pluginID != "" && !validID(pluginID) {
		return nil, fmt.Errorf("invalid plugin selection %q", pluginID)
	}
	storeRoot, err := filepath.Abs(s.root)
	if err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp("", "ocr-plugin-*")
	if err != nil {
		return nil, err
	}
	p := &PreparedInstall{storeRoot: storeRoot, directory: directory, packageDir: directory}
	success := false
	defer func() {
		if !success {
			p.Close()
		}
	}()
	item := Installed{Source: origin}
	p.artifact.Source = resolved
	if expected.ID != "" {
		if pluginID != "" && pluginID != expected.ID {
			return nil, fmt.Errorf("plugin selection %q does not match catalog id %q", pluginID, expected.ID)
		}
		item.SHA256, err = s.preparePackage(ctx, resolved, expected.SHA256, directory)
	} else if strings.HasPrefix(strings.ToLower(resolved), "https:") {
		item.PluginID = pluginID
		p.packageDir, item.SHA256, item.Revision, item.PluginID, err = s.prepareURL(ctx, resolved, pluginID, directory, &p.artifact)
	} else {
		item.SHA256, err = s.preparePackage(ctx, resolved, "", directory)
	}
	if err != nil {
		return nil, err
	}
	if p.artifact.Directory == "" {
		p.artifact.Directory, err = filepath.Rel(directory, p.packageDir)
		if err != nil {
			return nil, err
		}
		p.artifact.Directory = filepath.ToSlash(p.artifact.Directory)
	}
	manifest, err := inspectPackage(ctx, p.packageDir)
	if err != nil {
		return nil, err
	}
	if err := manifest.CheckCompatibility(HostVersion); err != nil {
		return nil, err
	}
	if expected.ID != "" && (manifest.ID != expected.ID || manifest.Version != expected.Version) {
		return nil, fmt.Errorf("package id/version %s@%s does not match catalog %s@%s", manifest.ID, manifest.Version, expected.ID, expected.Version)
	}
	if pluginID != "" && manifest.ID != pluginID {
		return nil, fmt.Errorf("package id %q does not match selected plugin %q", manifest.ID, pluginID)
	}
	if expected.ID == "" && strings.HasPrefix(strings.ToLower(resolved), "https:") {
		item.PluginID = manifest.ID
	}
	p.packageHash, err = packageDigest(ctx, p.packageDir)
	if err != nil {
		return nil, err
	}
	item.ArchiveSHA256 = item.SHA256
	item.ContentSHA256, item.ContentDigestFormat = p.packageHash, "ocr-package-tree-v1"
	if item.SHA256 == "" {
		item.SHA256 = p.packageHash
	}
	// Reparse to keep slices, maps and nested pointers private to the snapshot.
	data, _ := json.Marshal(manifest)
	item.Manifest, _ = ParseManifest(data)
	p.item = item
	p.Manifest, p.Source, p.SHA256 = manifest, item.Source, item.SHA256
	p.ArchiveSHA256, p.ContentSHA256, p.ContentDigestFormat = item.ArchiveSHA256, item.ContentSHA256, item.ContentDigestFormat
	p.Revision, p.PluginID = item.Revision, item.PluginID
	success = true
	return p, nil
}

func (s *Store) prepareURL(ctx context.Context, source, pluginID, directory string, artifact *packageArtifact) (packageDir, digest, revision, selection string, err error) {
	if repository, ok, parseErr := parseGitHubSource(source); ok || parseErr != nil {
		if parseErr != nil {
			return "", "", "", "", parseErr
		}
		return s.prepareGitHub(ctx, repository, pluginID, directory, artifact)
	}
	data, catalogSource, err := s.download(ctx, source, maxArchiveBytes)
	if err != nil {
		return "", "", "", "", err
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		if len(data) > maxCatalogBytes {
			return "", "", "", "", fmt.Errorf("marketplace exceeds %d byte limit", maxCatalogBytes)
		}
		packageDir, digest, selection, err = s.prepareCatalog(ctx, catalogSource, data, pluginID, directory, artifact)
		return packageDir, digest, "", selection, err
	}
	if !bytes.HasPrefix(data, []byte("PK")) && !bytes.HasPrefix(data, []byte{0x1f, 0x8b}) {
		return "", "", "", "", fmt.Errorf("source must contain a plugin tar.gz or ZIP archive, or an OCR marketplace JSON document")
	}
	if err := extractArchive(ctx, data, directory); err != nil {
		return "", "", "", "", err
	}
	packageDir, err = selectPackage(ctx, directory, source, pluginID)
	return packageDir, archiveDigest(data), "", pluginID, err
}

func (s *Store) prepareCatalog(ctx context.Context, source string, data []byte, pluginID, directory string, artifact *packageArtifact) (string, string, string, error) {
	market, err := parseMarket("", source, data, true)
	if err != nil {
		return "", "", "", err
	}
	entry, err := selectCatalogEntry(market.Plugins, pluginID)
	if err != nil {
		return "", "", "", err
	}
	artifact.Source = entry.Source
	digest, err := s.preparePackage(ctx, entry.Source, entry.SHA256, directory)
	if err != nil {
		return "", "", "", err
	}
	manifest, err := inspectPackage(ctx, directory)
	if err != nil {
		return "", "", "", err
	}
	if manifest.ID != entry.ID || manifest.Version != entry.Version {
		return "", "", "", fmt.Errorf("package id/version %s@%s does not match catalog %s@%s", manifest.ID, manifest.Version, entry.ID, entry.Version)
	}
	return directory, digest, entry.ID, nil
}

func selectCatalogEntry(entries []CatalogEntry, pluginID string) (CatalogEntry, error) {
	if len(entries) == 0 {
		return CatalogEntry{}, fmt.Errorf("source contains no plugins")
	}
	if pluginID == "" {
		if len(entries) == 1 {
			return entries[0], nil
		}
		return CatalogEntry{}, &SelectionRequiredError{Candidates: entries}
	}
	for _, entry := range entries {
		if entry.ID == pluginID {
			return entry, nil
		}
	}
	return CatalogEntry{}, fmt.Errorf("plugin %q is absent from source", pluginID)
}

func selectPackage(ctx context.Context, directory, source, pluginID string) (string, error) {
	var entries []CatalogEntry
	paths := make(map[string]string)
	// A root manifest defines the package, including any nested manifests used as
	// data. Otherwise discover packages beneath a repository/archive wrapper.
	rootManifest := filepath.Join(directory, ManifestFilename)
	if _, err := os.Stat(rootManifest); err == nil {
		manifest, err := inspectPackage(ctx, directory)
		if err != nil {
			return "", err
		}
		if pluginID != "" && manifest.ID != pluginID {
			return "", fmt.Errorf("package id %q does not match selected plugin %q", manifest.ID, pluginID)
		}
		return directory, nil
	}
	err := filepath.WalkDir(directory, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if _, err := os.Stat(filepath.Join(name, ManifestFilename)); errors.Is(err, os.ErrNotExist) {
			return nil
		} else if err != nil {
			return err
		}
		manifest, err := inspectPackage(ctx, name)
		if err != nil {
			return err
		}
		if _, exists := paths[manifest.ID]; exists {
			return fmt.Errorf("source contains duplicate plugin id %q", manifest.ID)
		}
		paths[manifest.ID] = name
		entries = append(entries, CatalogEntry{ID: manifest.ID, Name: manifest.Name, Version: manifest.Version, Description: manifest.Description, Source: source})
		return filepath.SkipDir
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
	selected, err := selectCatalogEntry(entries, pluginID)
	return paths[selected.ID], err
}

func packageDigest(ctx context.Context, directory string) (string, error) {
	digest := sha256.New()
	err := filepath.WalkDir(directory, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("prepared package contains a symlink or special file")
		}
		relative, err := filepath.Rel(directory, name)
		if err != nil {
			return err
		}
		data, err := readFileBounded(name, maxPackageBytes)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		fmt.Fprintf(digest, "%d:%s:%d:%d:", len(relative), relative, info.Mode().Perm()&0111, len(data))
		digest.Write(data)
		return nil
	})
	return hex.EncodeToString(digest.Sum(nil)), err
}

// InstallPrepared commits the exact validated package from PrepareInstall.
func (s *Store) InstallPrepared(ctx context.Context, prepared *PreparedInstall, enable bool) (Installed, error) {
	if prepared == nil {
		return Installed{}, fmt.Errorf("prepared plugin is required")
	}
	prepared.mu.Lock()
	defer prepared.mu.Unlock()
	if prepared.closed {
		return Installed{}, fmt.Errorf("prepared plugin is closed")
	}
	storeRoot, err := filepath.Abs(s.root)
	if err != nil {
		return Installed{}, err
	}
	if storeRoot != prepared.storeRoot {
		return Installed{}, fmt.Errorf("prepared plugin belongs to another store")
	}
	unlock, err := s.lock(ctx)
	if err != nil {
		return Installed{}, err
	}
	defer unlock()
	value, err := s.readState()
	if err != nil {
		return Installed{}, err
	}
	return s.installPrepared(ctx, &value, prepared, enable, "")
}
