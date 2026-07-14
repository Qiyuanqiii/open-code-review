package reviewbundle

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/open-code-review/open-code-review/internal/config/rules"
	"github.com/open-code-review/open-code-review/internal/gitcmd"
)

func TestPrepareScanBuildsDeterministicGroupedManifest(t *testing.T) {
	repository := initPrepareRepository(t)
	writeTargetFile(t, repository, "cmd/main.go", "package main\n\nfunc main() {}\n")
	writeTargetFile(t, repository, "pkg/util.go", "package pkg\n\nfunc Util() {}\n")
	writeTargetFile(t, repository, "web/app.ts", "export const app = true\n")
	writeTargetFile(t, repository, "asset.bin", "\x00binary")

	options := ScanOptions{
		RepoDir:       repository,
		Paths:         []string{"cmd", "pkg", "web", "asset.bin"},
		Resolver:      detailResolverStub{},
		FileFilter:    &rules.FileFilter{},
		GitRunner:     gitcmd.New(2),
		BatchStrategy: "by-language",
		BatchSize:     10,
		MaxBundleSize: DefaultMaxBundleBytes,
	}
	first, firstJSON, err := PrepareScan(context.Background(), options)
	if err != nil {
		t.Fatalf("PrepareScan() error = %v", err)
	}
	second, secondJSON, err := PrepareScan(context.Background(), options)
	if err != nil {
		t.Fatalf("second PrepareScan() error = %v", err)
	}
	if first.ManifestID == "" || first.ManifestID != second.ManifestID ||
		string(firstJSON) != string(secondJSON) {
		t.Fatal("scan manifest is not deterministic")
	}
	if first.Summary.TotalFiles != 4 || first.Summary.ReviewableFiles != 3 ||
		first.Summary.ExcludedFiles != 1 {
		t.Fatalf("summary = %+v", first.Summary)
	}
	if len(first.Bundles) != 2 {
		t.Fatalf("bundles = %d, want one Go and one TypeScript batch", len(first.Bundles))
	}
	assertManifestFileUniqueness(t, first)
	foundBinarySkip := false
	for _, skipped := range first.SkippedFiles {
		if skipped.Path == "asset.bin" && skipped.Reason == "binary" {
			foundBinarySkip = true
		}
	}
	if !foundBinarySkip {
		t.Fatalf("skipped files = %+v, want binary reason", first.SkippedFiles)
	}
	for _, bundle := range first.Bundles {
		if bundle.Target.Mode != TargetScan {
			t.Fatalf("target mode = %q, want scan", bundle.Target.Mode)
		}
		for _, file := range bundle.Files {
			if file.Content == "" || file.Patch != "" {
				t.Fatalf("scan file evidence = %+v", file)
			}
		}
	}
}

func TestPrepareScanSupportsNonGitDirectoryAndHardBudget(t *testing.T) {
	directory := t.TempDir()
	for index, name := range []string{"a.go", "b.go", "c.go"} {
		content := "package sample\n\nvar Value = \"" + string(rune('a'+index)) + "\"\n"
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	manifest, _, err := PrepareScan(context.Background(), ScanOptions{
		RepoDir:        directory,
		Resolver:       detailResolverStub{},
		GitRunner:      gitcmd.New(2),
		BatchStrategy:  "none",
		MaxTokenBudget: 1,
		MaxBundleSize:  DefaultMaxBundleBytes,
	})
	if err != nil {
		t.Fatalf("PrepareScan() error = %v", err)
	}
	if !manifest.Partial || len(manifest.Bundles) != 0 ||
		len(manifest.SkippedFiles) != 3 {
		t.Fatalf("budget manifest = %+v", manifest)
	}
	for _, skipped := range manifest.SkippedFiles {
		if skipped.Reason != "token_budget" {
			t.Fatalf("skipped = %+v, want token_budget", skipped)
		}
	}
}

func TestPrepareScanReportsOversizedFiles(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(directory, "large.go"),
		[]byte("package sample\nvar Large = true\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := PrepareScan(context.Background(), ScanOptions{
		RepoDir:          directory,
		Resolver:         detailResolverStub{},
		GitRunner:        gitcmd.New(1),
		MaxFileSizeBytes: 8,
		MaxBundleSize:    DefaultMaxBundleBytes,
	})
	if err != nil {
		t.Fatalf("PrepareScan() error = %v", err)
	}
	if manifest.Summary.TotalFiles != 1 || len(manifest.SkippedFiles) != 1 ||
		manifest.SkippedFiles[0].Path != "large.go" ||
		manifest.SkippedFiles[0].Reason != "file_size" {
		t.Fatalf("manifest = %+v", manifest)
	}
}

func assertManifestFileUniqueness(t *testing.T, manifest *ScanManifest) {
	t.Helper()
	seen := make(map[string]bool)
	for _, bundle := range manifest.Bundles {
		for _, file := range bundle.Files {
			if seen[file.Path] {
				t.Errorf("duplicate manifest file %s", file.Path)
			}
			seen[file.Path] = true
		}
	}
}
