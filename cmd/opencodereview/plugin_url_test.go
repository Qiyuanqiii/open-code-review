// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alibaba/open-code-review/internal/plugin"
)

func pluginURLArchive(t *testing.T, id, version string) []byte {
	t.Helper()
	manifest := plugin.Manifest{
		SchemaVersion: 1, ID: id, Name: "URL test plugin", Version: version,
		Rules: []string{"rules.json"},
		MCPServers: map[string]plugin.MCPServer{"notes": {
			Command: "must-not-execute-plugin", Args: []string{"private-argument"}, Env: []string{"TOKEN=private-token"},
		}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	for name, content := range map[string][]byte{
		"ocr-plugin.json": data,
		"rules.json":      []byte(`{"rules":[{"path":"**/*.go","rule":"Check errors."}]}`),
	} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

func usePluginURLServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	return server
}

func TestPluginURLCommandPreviewInstallAndUpdate(t *testing.T) {
	freshOCRHome(t)
	t.Setenv("PATH", "")
	var mu sync.Mutex
	archive := pluginURLArchive(t, "url-review", "1.0")
	server := usePluginURLServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_, _ = w.Write(archive)
	}))
	source := server.URL + "/plugin.zip?token=private-query"
	output := pluginCommandOK(t, "install", source, "--dry-run", "--enable", "--json")
	var preview pluginInstallPreview
	if err := json.Unmarshal([]byte(output), &preview); err != nil || len(preview.Plugins) != 1 || preview.Plugins[0].ID != "url-review" || len(preview.Plugins[0].SHA256) != 64 {
		t.Fatalf("preview = %s, error = %v", output, err)
	}
	if strings.Contains(output, "private-") {
		t.Fatalf("preview exposed credentials: %s", output)
	}
	if output := pluginCommandOK(t, "list", "--json"); strings.TrimSpace(output) != "[]" {
		t.Fatalf("preview installed a plugin: %s", output)
	}
	if output := pluginCommandOK(t, "install", source, "--dry-run"); !strings.Contains(output, "No plugin was installed") || !strings.Contains(output, "url-review") {
		t.Fatalf("missing preview guidance: %s", output)
	}
	var installed pluginInfo
	output = pluginCommandOK(t, "install", source, "--json")
	if err := json.Unmarshal([]byte(output), &installed); err != nil || installed.Enabled || installed.SHA256 != preview.Plugins[0].SHA256 || installed.Source != server.URL+"/plugin.zip" {
		t.Fatalf("install = %s, error = %v", output, err)
	}
	pluginCommandOK(t, "enable", "url-review")
	updated := pluginURLArchive(t, "url-review", "2.0")
	mu.Lock()
	archive = updated
	mu.Unlock()
	output = pluginCommandOK(t, "update", "url-review", "--json")
	if err := json.Unmarshal([]byte(output), &installed); err != nil || !installed.Enabled || installed.Version != "2.0" || installed.SHA256 == preview.Plugins[0].SHA256 {
		t.Fatalf("update = %s, error = %v", output, err)
	}
	if output := pluginCommandOK(t, "market", "list", "--json"); strings.TrimSpace(output) != "[]" {
		t.Fatalf("direct URL installation created a marketplace: %s", output)
	}
}

func TestPluginCatalogURLCommandSelectsWithoutRegistration(t *testing.T) {
	freshOCRHome(t)
	first, second := pluginURLArchive(t, "first", "1"), pluginURLArchive(t, "second", "1")
	digest := func(data []byte) string {
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}
	server := usePluginURLServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first.zip":
			_, _ = w.Write(first)
		case "/second.zip":
			_, _ = w.Write(second)
		default:
			entries := []map[string]string{{"id": "first", "name": "First", "version": "1", "source": "first.zip", "sha256": digest(first)}}
			if r.URL.Path == "/many.json" {
				entries = append(entries, map[string]string{"id": "second", "name": "Second", "version": "1", "source": "second.zip", "sha256": digest(second)})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"schema_version": 1, "name": "Test catalog", "plugins": entries})
		}
	}))
	source := server.URL + "/many.json"
	output := pluginCommandOK(t, "install", source, "--dry-run", "--json")
	var preview pluginInstallPreview
	if err := json.Unmarshal([]byte(output), &preview); err != nil || len(preview.Plugins) != 2 {
		t.Fatalf("catalog preview = %s, error = %v", output, err)
	}
	if _, err := runPluginCommand(context.Background(), []string{"install", source}); err == nil || !strings.Contains(err.Error(), "--plugin") {
		t.Fatalf("ambiguous install did not explain selection: %v", err)
	}
	if out := pluginCommandOK(t, "list", "--json"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("ambiguous install changed plugins: %s", out)
	}
	pluginCommandOK(t, "install", source, "--plugin", "second", "--enable")
	pluginCommandOK(t, "install", server.URL+"/single.json")
	var installed []pluginInfo
	output = pluginCommandOK(t, "list", "--json")
	if err := json.Unmarshal([]byte(output), &installed); err != nil || len(installed) != 2 || installed[0].ID != "first" || installed[0].Enabled || installed[1].ID != "second" || !installed[1].Enabled {
		t.Fatalf("selection/enable state = %s, error = %v", output, err)
	}
	if out := pluginCommandOK(t, "market", "list", "--json"); strings.TrimSpace(out) != "[]" {
		t.Fatalf("catalog URL installation required registration: %s", out)
	}
}
