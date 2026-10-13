// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxManifestBytes = 1 << 20
	maxCatalogBytes  = 4 << 20
	maxArchiveBytes  = 32 << 20
	maxPackageBytes  = 64 << 20
	maxPackageFiles  = 4096
)

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("content exceeds %d byte limit", limit)
	}
	return data, nil
}

func readFileBounded(name string, limit int64) ([]byte, error) {
	file, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readBounded(file, limit)
}

// inspectPackage rejects symlinks throughout the package, not only in declared
// contributions. It also bounds the snapshot and prevents ambiguous filenames.
func inspectPackage(ctx context.Context, root string) (Manifest, error) {
	var m Manifest
	info, err := os.Lstat(root)
	if err != nil {
		return m, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return m, fmt.Errorf("plugin source must be a directory, without symlinks")
	}
	count := 0
	var total int64
	seen := make(map[string]bool)
	err = filepath.WalkDir(root, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == root {
			return nil
		}
		rel, err := filepath.Rel(root, name)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if err := safeRelativePath(rel); err != nil {
			return fmt.Errorf("package file %q: %w", rel, err)
		}
		key := strings.ToLower(rel)
		if seen[key] {
			return fmt.Errorf("package contains conflicting filename %q", rel)
		}
		seen[key] = true
		count++
		if count > maxPackageFiles {
			return fmt.Errorf("package exceeds %d entries", maxPackageFiles)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("package file %q is a symlink or special file", rel)
		}
		if info.Mode().IsRegular() {
			total += info.Size()
			if total > maxPackageBytes {
				return fmt.Errorf("package exceeds %d byte limit", maxPackageBytes)
			}
		}
		return nil
	})
	if err != nil {
		return m, err
	}
	data, err := readFileBounded(filepath.Join(root, ManifestFilename), maxManifestBytes)
	if err != nil {
		return m, fmt.Errorf("read %s: %w", ManifestFilename, err)
	}
	m, err = ParseManifest(data)
	if err != nil {
		return m, err
	}
	for _, rule := range m.Rules {
		info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rule)))
		if err != nil || !info.Mode().IsRegular() {
			return m, fmt.Errorf("rule %q must be an existing regular file", rule)
		}
	}
	return m, nil
}

func copyPackage(ctx context.Context, source, destination string) error {
	// Walk again while copying and enforce limits on bytes actually read. A
	// changing source cannot bypass validation by replacing a regular file with a
	// symlink between the directory inspection and the snapshot operation.
	var total int64
	return filepath.WalkDir(source, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == source {
			return nil
		}
		rel, err := filepath.Rel(source, name)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		target := filepath.Join(destination, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("package file %q is a symlink or special file", rel)
		}
		input, err := os.Open(name)
		if err != nil {
			return err
		}
		defer input.Close()
		opened, err := input.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(info, opened) || !opened.Mode().IsRegular() {
			return fmt.Errorf("package source changed while copying %q", rel)
		}
		output, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600|info.Mode().Perm()&0111)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(output, io.LimitReader(input, maxPackageBytes-total+1))
		closeErr := output.Close()
		total += n
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if total > maxPackageBytes {
			return fmt.Errorf("package exceeds %d byte limit", maxPackageBytes)
		}
		return nil
	})
}

func extractZIP(ctx context.Context, data []byte, destination string) error {
	if len(data) > maxArchiveBytes {
		return fmt.Errorf("ZIP exceeds %d byte limit", maxArchiveBytes)
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("read plugin ZIP: %w", err)
	}
	if len(archive.File) > maxPackageFiles {
		return fmt.Errorf("ZIP exceeds %d entries", maxPackageFiles)
	}
	seen := make(map[string]bool)
	var total uint64
	for _, entry := range archive.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.TrimSuffix(entry.Name, "/")
		if err := safeRelativePath(name); err != nil {
			return fmt.Errorf("ZIP entry %q: %w", entry.Name, err)
		}
		key := strings.ToLower(name)
		if seen[key] {
			return fmt.Errorf("ZIP contains duplicate filename %q", entry.Name)
		}
		seen[key] = true
		if !entry.Mode().IsRegular() && !entry.Mode().IsDir() {
			return fmt.Errorf("ZIP entry %q is a symlink or special file", entry.Name)
		}
		if entry.UncompressedSize64 > maxPackageBytes || total+entry.UncompressedSize64 > maxPackageBytes {
			return fmt.Errorf("ZIP exceeds %d uncompressed byte limit", maxPackageBytes)
		}
		total += entry.UncompressedSize64
		target := filepath.Join(destination, filepath.FromSlash(name))
		if entry.Mode().IsDir() {
			if err := os.MkdirAll(target, 0700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		if err := extractFile(entry, target); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(entry *zip.File, target string) error {
	input, err := entry.Open()
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600|entry.Mode().Perm()&0111)
	if err != nil {
		return err
	}
	n, copyErr := io.Copy(output, io.LimitReader(input, int64(entry.UncompressedSize64)+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if uint64(n) != entry.UncompressedSize64 {
		return fmt.Errorf("ZIP entry %q size does not match its header", entry.Name)
	}
	return nil
}
