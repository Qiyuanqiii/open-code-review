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
)

func TestPluginProjectCommandLifecycle(t *testing.T) {
	freshOCRHome(t)
	repo := t.TempDir()
	source := filepath.Join(repo, "plugins", "example")
	writePluginCommandFixture(t, source, "1")
	output := pluginCommandOK(t, "add", source, "--repo", repo, "--json")
	if strings.Contains(output, "secret-token") || strings.Contains(output, "secret-argument") || !strings.Contains(output, "./plugins/example") {
		t.Fatalf("project lock output = %s", output)
	}
	if out := pluginCommandOK(t, "list", "--json"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("project add changed global state: %s", out)
	}
	for _, args := range [][]string{
		{"sync", "--repo", repo},
		{"preflight", "--repo", repo},
	} {
		out, err := runPluginCommand(context.Background(), append(args, "--json"))
		if err == nil || !json.Valid([]byte(out)) || !strings.Contains(out, `"error"`) {
			t.Fatalf("structured project failure = %s, %v", out, err)
		}
	}
	pluginCommandOK(t, "sync", "--frozen", "--offline", "--repo", repo)
	out, err := runPluginCommand(context.Background(), []string{"preflight", "--repo", repo, "--json"})
	if err == nil || !strings.Contains(out, "PLUGIN_EXEC_NOT_AUTHORIZED") {
		t.Fatalf("untrusted project preflight = %s, %v", out, err)
	}
	pluginCommandOK(t, "trust", "--repo", repo, "--json")
	if out := pluginCommandOK(t, "preflight", "--repo", repo); !strings.Contains(out, "test-review 1") {
		t.Fatalf("trusted preflight = %s", out)
	}
	pluginCommandOK(t, "project", "--repo", repo)
	pluginCommandOK(t, "trust", "--revoke", "--repo", repo)
	if _, err := runPluginCommand(context.Background(), []string{"preflight", "--repo", repo}); err == nil {
		t.Fatal("revoked trust was ignored")
	}
	writePluginCommandFixture(t, source, "2")
	pluginCommandOK(t, "lock", "--repo", repo)
	pluginCommandOK(t, "sync", "--frozen", "--offline", "--repo", repo, "--json")
	if data, err := os.ReadFile(filepath.Join(repo, ".opencodereview", "plugins.lock.json")); err != nil || !strings.Contains(string(data), `"version": "2"`) {
		t.Fatalf("relock did not update version: %v", err)
	}
	pluginCommandOK(t, "trust", "--repo", repo)
	pluginCommandOK(t, "preflight", "--repo", repo, "--json")
}
