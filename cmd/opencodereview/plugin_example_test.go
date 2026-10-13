// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alibaba/open-code-review/internal/config/rules"
	"github.com/alibaba/open-code-review/internal/tool"
)

func TestNativePluginExampleMarketThroughRuntime(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("Node.js is required to exercise the bundled MCP example")
	}
	freshOCRHome(t)
	marketDir, err := filepath.Abs(filepath.Join("..", "..", "examples", "ocr-plugins"))
	if err != nil {
		t.Fatal(err)
	}
	pluginCommandOK(t, "market", "add", "examples", marketDir)
	pluginCommandOK(t, "install", "review-checklist@examples", "--enable")
	pluginCommandOK(t, "install", "project-notes@examples")
	repo := t.TempDir()
	resolver, _, err := rules.NewResolver(repo, "", rules.ResolverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := resolver.Resolve("main.go"); !strings.Contains(got, "resource ownership mistakes") || strings.Contains(got, "project_review_notes") {
		t.Fatalf("enabled/disabled example rules not respected: %s", got)
	}
	pluginCommandOK(t, "enable", "project-notes")
	resolver, _, err = rules.NewResolver(repo, "", rules.ResolverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	reg := tool.NewRegistry()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	clients, err := initRuntimeMCPClients(ctx, nil, resolver, reg, repo, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeReviewMCPClients(clients) })
	if len(clients) != 1 {
		t.Fatalf("example plugin did not launch exactly one tool server: %d", len(clients))
	}
	provider, ok := reg.Get("project_review_notes")
	if !ok {
		t.Fatal("example MCP tool was not discovered")
	}
	if out, err := provider.Execute(ctx, map[string]any{}); err != nil || !strings.Contains(out, "No REVIEW_NOTES.md") {
		t.Fatalf("missing notes: %q, %v", out, err)
	}
	path := filepath.Join(repo, "REVIEW_NOTES.md")
	if err := os.WriteFile(path, []byte("Check that retry failures preserve the original cause.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := provider.Execute(ctx, map[string]any{}); err != nil || out != "Check that retry failures preserve the original cause.\n" {
		t.Fatalf("plugin did not read reviewed repository: %q, %v", out, err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 65537)), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := provider.Execute(ctx, map[string]any{}); err != nil || !strings.Contains(out, "exceeds 64 KiB") {
		t.Fatalf("example lost bounded file read: %q, %v", out, err)
	}
	pluginCommandOK(t, "disable", "project-notes")
	nextResolver, _, err := rules.NewResolver(repo, "", rules.ResolverOptions{})
	if err != nil {
		t.Fatal(err)
	}
	nextClients, err := initRuntimeMCPClients(ctx, nil, nextResolver, tool.NewRegistry(), repo, "test")
	defer closeReviewMCPClients(nextClients)
	if err != nil || len(nextClients) != 0 || strings.Contains(nextResolver.Resolve("main.go"), "project_review_notes") {
		t.Fatalf("disabled example still contributes: clients=%d, error=%v", len(nextClients), err)
	}
}
