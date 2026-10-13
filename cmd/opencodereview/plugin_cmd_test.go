// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alibaba/open-code-review/internal/plugin"
)

func writePluginCommandFixture(t *testing.T, dir, version string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	manifest := plugin.Manifest{
		SchemaVersion: 1, ID: "test-review", Name: "Test Review", Version: version,
		Description: "Review error handling", Rules: []string{"rules.json"},
		MCPServers: map[string]plugin.MCPServer{"notes": {
			Command: "nonexistent-plugin-test-command", Args: []string{"secret-argument"}, Env: []string{"TOKEN=secret-token"},
		}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{
		"ocr-plugin.json": data,
		"rules.json":      []byte(`{"rules":[{"path":"**/*.go","rule":"Check returned errors."}]}`),
	} {
		if err := os.WriteFile(filepath.Join(dir, path), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func pluginCommandOK(t *testing.T, args ...string) string {
	t.Helper()
	output, err := runPluginCommand(context.Background(), args)
	if err != nil {
		t.Fatalf("plugin %v: %v\n%s", args, err, output)
	}
	return output
}

func TestPluginCommandLifecycleAndCredentialRedaction(t *testing.T) {
	freshOCRHome(t)
	t.Setenv("PATH", "")
	dir := filepath.Join(t.TempDir(), "package with spaces")
	writePluginCommandFixture(t, dir, "1.0.0")
	if out := pluginCommandOK(t, "list", "--json"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("new store = %s", out)
	}
	if out := pluginCommandOK(t, "list"); !strings.Contains(out, "No plugins installed") {
		t.Fatalf("empty guidance missing: %s", out)
	}
	if out := pluginCommandOK(t, "install", dir); !strings.Contains(out, "disabled") || !strings.Contains(out, "ocr plugin enable test-review") {
		t.Fatalf("install did not explain activation: %s", out)
	}
	for _, args := range [][]string{{"show", "test-review"}, {"show", "test-review", "--json"}, {"list", "--json"}} {
		out := pluginCommandOK(t, args...)
		if strings.Contains(out, "secret-token") || strings.Contains(out, "secret-argument") || !strings.Contains(out, "notes") {
			t.Fatalf("management output leaked credentials or omitted capabilities: %s", out)
		}
	}
	pluginCommandOK(t, "enable", "test-review", "--json")
	var infos []pluginInfo
	if err := json.Unmarshal([]byte(pluginCommandOK(t, "list", "--json")), &infos); err != nil || len(infos) != 1 || !infos[0].Enabled {
		t.Fatalf("enabled list = %+v, %v", infos, err)
	}
	if out := pluginCommandOK(t, "list"); !strings.Contains(out, "enabled") || !strings.Contains(out, "1 rule packs, 1 MCP servers") {
		t.Fatalf("list omitted state/capabilities: %s", out)
	}
	writePluginCommandFixture(t, dir, "1.1.0")
	var updated pluginInfo
	if err := json.Unmarshal([]byte(pluginCommandOK(t, "update", "test-review", "--json")), &updated); err != nil || updated.Version != "1.1.0" || !updated.Enabled {
		t.Fatalf("update lost source or enabled state: %+v, %v", updated, err)
	}
	pluginCommandOK(t, "disable", "test-review")
	if out := pluginCommandOK(t, "show", "test-review"); !strings.Contains(out, "disabled") {
		t.Fatalf("disable did not take effect: %s", out)
	}
	pluginCommandOK(t, "remove", "test-review", "--json")
	if _, err := runPluginCommand(context.Background(), []string{"show", "test-review"}); err == nil {
		t.Fatal("removed plugin is still available")
	}
}

func TestPluginMarketplaceCommandLifecycle(t *testing.T) {
	freshOCRHome(t)
	t.Setenv("PATH", "")
	dir := t.TempDir()
	packageDir := filepath.Join(dir, "package")
	writePluginCommandFixture(t, packageDir, "1.0.0")
	catalog := filepath.Join(dir, "ocr-marketplace.json")
	writeCatalog := func(version string) {
		t.Helper()
		data, err := json.Marshal(map[string]any{
			"schema_version": 1, "name": "Examples",
			"plugins": []map[string]string{{"id": "test-review", "name": "Test Review", "version": version, "description": "Review error handling", "source": "package"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(catalog, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeCatalog("1.0.0")
	if out := pluginCommandOK(t, "market", "list"); !strings.Contains(out, "No marketplaces") {
		t.Fatalf("unexpected initial marketplace: %s", out)
	}
	pluginCommandOK(t, "market", "add", "examples", catalog, "--json")
	if out := pluginCommandOK(t, "market", "list"); !strings.Contains(out, "examples") || !strings.Contains(out, "ocr-marketplace.json") {
		t.Fatalf("marketplace missing: %s", out)
	}
	var markets []plugin.Market
	if err := json.Unmarshal([]byte(pluginCommandOK(t, "market", "list", "--json")), &markets); err != nil || len(markets) != 1 {
		t.Fatalf("marketplace JSON: %+v, %v", markets, err)
	}
	if out := pluginCommandOK(t, "market", "search", "error", "handling"); !strings.Contains(out, "test-review@examples") {
		t.Fatalf("search did not expose install identifier: %s", out)
	}
	var entries []plugin.CatalogEntry
	if err := json.Unmarshal([]byte(pluginCommandOK(t, "market", "search", "--json")), &entries); err != nil || len(entries) != 1 || entries[0].Market != "examples" {
		t.Fatalf("catalog JSON: %+v, %v", entries, err)
	}
	if out := pluginCommandOK(t, "market", "search", "no-matching-plugin"); !strings.Contains(out, "No matching plugins") {
		t.Fatalf("search guidance missing: %s", out)
	}
	pluginCommandOK(t, "install", "test-review@examples", "--enable", "--json")
	writePluginCommandFixture(t, packageDir, "1.1.0")
	writeCatalog("1.1.0")
	pluginCommandOK(t, "market", "refresh", "examples")
	if out := pluginCommandOK(t, "update", "test-review"); !strings.Contains(out, "1.1.0 (enabled)") {
		t.Fatalf("market update did not install refreshed version: %s", out)
	}
	pluginCommandOK(t, "market", "remove", "examples", "--json")
	if out := pluginCommandOK(t, "market", "list", "--json"); !strings.Contains(out, "[]") {
		t.Fatalf("removed market remained: %s", out)
	}
	// Removing the catalog does not uninstall a plugin already copied from it.
	pluginCommandOK(t, "show", "test-review")
	pluginCommandOK(t, "remove", "test-review")
}

func TestPluginCommandErrorsAreReturnedToCLIAndTUI(t *testing.T) {
	freshOCRHome(t)
	for _, args := range [][]string{
		{"install"}, {"install", filepath.Join(t.TempDir(), "missing")},
		{"show", "missing"}, {"update", "missing"}, {"enable", "missing"}, {"disable", "missing"}, {"remove", "missing"},
		{"market", "add", "missing-source"}, {"market", "add", "test", filepath.Join(t.TempDir(), "missing.json")},
		{"market", "refresh", "missing"}, {"market", "remove", "missing"}, {"unrecognized"},
	} {
		t.Run(strings.Join(args[:min(2, len(args))], "_"), func(t *testing.T) {
			if _, err := runPluginCommand(context.Background(), args); err == nil {
				t.Fatalf("plugin %v silently succeeded", args)
			}
		})
	}
	for _, args := range [][]string{{"--help"}, {"market", "--help"}} {
		if out := pluginCommandOK(t, args...); !strings.Contains(out, "Available Commands:") {
			t.Fatalf("help omitted commands: %s", out)
		}
	}
}

func TestPluginManagementTextStripsTerminalControl(t *testing.T) {
	if got := pluginText("hello\x1b[31m\n\tworld\x1b[0m"); got != "hello world" {
		t.Fatalf("unsafe plugin description: %q", got)
	}
}

func TestPluginMarketplaceOutputRedactsSourceQueries(t *testing.T) {
	freshOCRHome(t)
	dir := t.TempDir()
	catalog := filepath.Join(dir, plugin.MarketplaceFilename)
	data := `{"schema_version":1,"name":"Private downloads","plugins":[{"id":"test-review","name":"Test Review","version":"1.0.0","source":"https://example.invalid/plugin.zip?token=source-secret","sha256":"` + strings.Repeat("a", 64) + `"}]}`
	if err := os.WriteFile(catalog, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	pluginCommandOK(t, "market", "add", "private", catalog)
	for _, args := range [][]string{{"market", "list", "--json"}, {"market", "search", "--json"}} {
		out := pluginCommandOK(t, args...)
		if strings.Contains(out, "source-secret") || !strings.Contains(out, "https://example.invalid/plugin.zip") {
			t.Fatalf("unsafe or missing public source: %s", out)
		}
	}
	// Display redaction must not change the private download location in storage.
	store, err := plugin.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.Search("")
	if err != nil || len(entries) != 1 || !strings.Contains(entries[0].Source, "source-secret") {
		t.Fatalf("download source was altered: %+v, %v", entries, err)
	}
	info := pluginManagementInfo(plugin.Installed{Source: entries[0].Source})
	if strings.Contains(info.Source, "source-secret") {
		t.Fatalf("installed plugin source leaked query: %s", info.Source)
	}
}
