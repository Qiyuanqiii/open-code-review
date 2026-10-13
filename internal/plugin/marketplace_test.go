// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package plugin

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func testZIP(t *testing.T, id, version string) []byte {
	t.Helper()
	manifest, err := json.Marshal(testManifest(id, version))
	if err != nil {
		t.Fatal(err)
	}
	return zipEntries(t, map[string]string{ManifestFilename: string(manifest), "rules.json": `{"default_rule":"Check correctness"}`, "docs/review.md": "Review instructions"})
}

func zipEntries(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for name, content := range entries {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func zipDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func TestLocalMarketplaceLifecycle(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewStore(filepath.Join(root, "store"))
	source := testPackage(t, "example", "1")
	catalogDir := t.TempDir()
	archive := testZIP(t, "archived", "1")
	if err := os.WriteFile(filepath.Join(catalogDir, "plugin.zip"), archive, 0600); err != nil {
		t.Fatal(err)
	}
	published := catalog{SchemaVersion: 1, Name: "Example market", Plugins: []catalogPlugin{
		{ID: "example", Name: "Example plugin", Version: "1", Description: "Security rules", Source: source},
		{ID: "archived", Name: "Archived plugin", Version: "1", Source: "plugin.zip", SHA256: zipDigest(archive)},
	}}
	file := filepath.Join(catalogDir, MarketplaceFilename)
	writeTestJSON(t, file, published)
	market, err := store.AddMarket(ctx, "local", catalogDir)
	if err != nil || market.Name != "local" || market.Source != file || market.RefreshedAt.IsZero() || len(market.Plugins) != 2 {
		t.Fatalf("AddMarket = %+v, %v", market, err)
	}
	_, err = store.AddMarket(ctx, "local", file)
	requireError(t, err, "already exists")
	if _, err := store.AddMarket(ctx, "another", file); err != nil {
		t.Fatal(err)
	}
	markets, err := store.ListMarkets()
	if err != nil || len(markets) != 2 || markets[0].Name != "another" || markets[1].Name != "local" {
		t.Fatalf("ListMarkets ordering = %v, %v", markets, err)
	}
	allMarkets, err := store.Search("")
	if err != nil || len(allMarkets) != 4 || allMarkets[0].Market != "another" || allMarkets[1].Market != "local" {
		t.Fatalf("Search market ordering = %v, %v", allMarkets, err)
	}
	if err := store.RemoveMarket("another"); err != nil {
		t.Fatal(err)
	}
	search, err := store.Search("SECURITY")
	if err != nil || len(search) != 1 || search[0].ID != "example" || search[0].Market != "local" {
		t.Fatalf("Search = %v, %v", search, err)
	}
	all, err := store.Search("")
	if err != nil || len(all) != 2 || all[0].ID != "archived" {
		t.Fatalf("Search ordering = %v, %v", all, err)
	}
	item, err := store.Install(ctx, "example@local", false)
	if err != nil || item.Source != "example@local" || item.Enabled {
		t.Fatalf("catalog Install = %+v, %v", item, err)
	}
	archived, err := store.Install(ctx, "archived@local", true)
	if err != nil || !archived.Enabled {
		t.Fatalf("archive Install = %+v, %v", archived, err)
	}
	_, err = store.Install(ctx, "missing@local", false)
	requireError(t, err, "absent")
	_, err = store.Install(ctx, "example@absent", false)
	requireError(t, err, "does not exist")
	writeTestJSON(t, filepath.Join(source, ManifestFilename), testManifest("example", "2"))
	_, err = store.Update(ctx, "example")
	requireError(t, err, "does not match catalog")
	published.Plugins[0].Version = "2"
	writeTestJSON(t, file, published)
	if _, err := store.RefreshMarket(ctx, "local"); err != nil {
		t.Fatal(err)
	}
	updated, err := store.Update(ctx, "example")
	if err != nil || updated.Manifest.Version != "2" || updated.Enabled {
		t.Fatalf("marketplace Update = %+v, %v", updated, err)
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	_, err = store.RefreshMarket(ctx, "local")
	if err == nil {
		t.Fatal("missing catalog refresh succeeded")
	}
	cached, err := store.Search("example")
	if err != nil || len(cached) != 1 || cached[0].Version != "2" {
		t.Fatalf("failed refresh lost cache: %v, %v", cached, err)
	}
	if err := store.RemoveMarket("local"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.List(); err != nil {
		t.Fatalf("removing market damaged installed snapshots: %v", err)
	}
	_, err = store.Update(ctx, "example")
	requireError(t, err, "does not exist")
	requireError(t, store.RemoveMarket("local"), "does not exist")
	_, err = store.RefreshMarket(ctx, "local")
	requireError(t, err, "does not exist")
}

func TestRemoteMarketplaceIntegrityAndCache(t *testing.T) {
	ctx := context.Background()
	archive := testZIP(t, "example", "1")
	var requests atomic.Int32
	var failCatalog atomic.Bool
	var corruptZIP atomic.Bool
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		switch request.URL.Path {
		case "/catalog.json":
			if failCatalog.Load() {
				http.Error(writer, "unavailable", http.StatusServiceUnavailable)
				return
			}
			json.NewEncoder(writer).Encode(catalog{SchemaVersion: 1, Name: "Remote catalog", Plugins: []catalogPlugin{{ID: "example", Name: "Example", Version: "1", Source: "plugin.zip", SHA256: zipDigest(archive)}}})
		case "/plugin.zip":
			if corruptZIP.Load() {
				writer.Write([]byte("tampered"))
				return
			}
			writer.Write(archive)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	store := NewStore(t.TempDir())
	store.client = server.Client()
	market, err := store.AddMarket(ctx, "remote", server.URL+"/catalog.json")
	if err != nil || market.Plugins[0].Source != server.URL+"/plugin.zip" {
		t.Fatalf("AddMarket = %+v, %v", market, err)
	}
	before := requests.Load()
	for i := 0; i < 2; i++ {
		if _, err := store.Search("example"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ListMarkets(); err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != before {
		t.Fatal("reading cached market made a network request")
	}
	corruptZIP.Store(true)
	_, err = store.Install(ctx, "example@remote", true)
	requireError(t, err, "sha256 does not match")
	items, err := store.List()
	if err != nil || len(items) != 0 {
		t.Fatalf("corrupt ZIP changed installed state: %v, %v", items, err)
	}
	corruptZIP.Store(false)
	item, err := store.Install(ctx, "example@remote", true)
	if err != nil || !item.Enabled || item.Source != "example@remote" {
		t.Fatalf("Install = %+v, %v", item, err)
	}
	stateBefore, err := os.ReadFile(filepath.Join(store.root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	failCatalog.Store(true)
	_, err = store.RefreshMarket(ctx, "remote")
	requireError(t, err, "HTTP 503")
	stateAfter, err := os.ReadFile(filepath.Join(store.root, "state.json"))
	if err != nil || string(stateBefore) != string(stateAfter) {
		t.Fatalf("failed refresh changed saved state: %v", err)
	}
	server.Close()
	matches, err := store.Search("")
	if err != nil || len(matches) != 1 {
		t.Fatalf("offline search failed: %v, %v", matches, err)
	}
}

func TestRedirectedCatalogInstallAndUpdate(t *testing.T) {
	archives := map[string][]byte{"1": testZIP(t, "example", "1"), "2": testZIP(t, "example", "2")}
	var release atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/entry":
			http.Redirect(w, r, fmt.Sprintf("/v%d/ocr-marketplace.json", release.Load()), http.StatusFound)
		case "/v1/ocr-marketplace.json", "/v2/ocr-marketplace.json":
			version := strings.TrimPrefix(strings.Split(r.URL.Path, "/")[1], "v")
			json.NewEncoder(w).Encode(catalog{SchemaVersion: 1, Name: "Redirected catalog", Plugins: []catalogPlugin{{
				ID: "example", Name: "Example", Version: version, Source: "plugin.zip", SHA256: zipDigest(archives[version]),
			}}})
		case "/v1/plugin.zip", "/v2/plugin.zip":
			version := strings.TrimPrefix(strings.Split(r.URL.Path, "/")[1], "v")
			w.Write(archives[version])
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	for _, mode := range []string{"direct", "registered"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			release.Store(1)
			store := NewStore(t.TempDir())
			store.client = server.Client()
			origin := server.URL + "/entry"
			source := origin
			if mode == "registered" {
				market, err := store.AddMarket(ctx, "remote", origin)
				if err != nil || market.Source != origin || len(market.Plugins) != 1 || market.Plugins[0].Source != server.URL+"/v1/plugin.zip" {
					t.Fatalf("redirected market = %+v, %v", market, err)
				}
				source = "example@remote"
			}
			installed, err := store.Install(ctx, source, false)
			if err != nil || installed.Manifest.Version != "1" || installed.Source != source {
				t.Fatalf("redirected install = %+v, %v", installed, err)
			}
			release.Store(2)
			if mode == "registered" {
				market, err := store.RefreshMarket(ctx, "remote")
				if err != nil || market.Source != origin || len(market.Plugins) != 1 || market.Plugins[0].Source != server.URL+"/v2/plugin.zip" {
					t.Fatalf("redirected refresh = %+v, %v", market, err)
				}
			}
			updated, err := store.Update(ctx, "example")
			if err != nil || updated.Manifest.Version != "2" || updated.Source != source {
				t.Fatalf("redirected update = %+v, %v", updated, err)
			}
		})
	}
}

func TestMarketplaceValidation(t *testing.T) {
	ctx := context.Background()
	for _, data := range []string{
		`{"schema_version":2,"name":"Market","plugins":[]}`,
		`{"schema_version":1,"name":" ","plugins":[]}`,
		`{"schema_version":1,"name":"Market","unsupported":true}`,
		`{"schema_version":1,"name":"Market","plugins":[{"id":"bad/escape","name":"Example","version":"1","source":"local"}]}`,
		`{"schema_version":1,"name":"Market","plugins":[{"id":"example","name":"","version":"1","source":"local"}]}`,
		`{"schema_version":1,"name":"Market","plugins":[{"id":"example","name":"Example","version":"1","source":"local","sha256":"invalid"}]}`,
		`{"schema_version":1,"name":"Market","plugins":[{"id":"example","name":"Example","version":"1","source":"local"},{"id":"example","name":"Example","version":"2","source":"other"}]}`,
		`{"schema_version":1,"name":"Market","plugins":[{"id":"example","name":"Example","version":"1","source":"https://example.com/plugin.zip"}]}`,
		`{"schema_version":1,"name":"Market","plugins":[{"id":"example","name":"Example","version":"1","source":"http://example.com/plugin.zip"}]}`,
		`{"schema_version":1,"name":"Market","plugins":[{"id":"example","name":"Example","version":"1","source":"https://user@example.com/plugin.zip","sha256":"` + strings.Repeat("a", 64) + `"}]}`,
	} {
		store := NewStore(t.TempDir())
		file := filepath.Join(t.TempDir(), "catalog.json")
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := store.AddMarket(ctx, "local", file); err == nil {
			t.Fatalf("invalid catalog accepted: %s", data)
		}
		items, err := store.ListMarkets()
		if err != nil || len(items) != 0 {
			t.Fatalf("invalid catalog changed state: %v, %v", items, err)
		}
	}
	store := NewStore(t.TempDir())
	_, err := store.AddMarket(ctx, "../escape", "ignored")
	requireError(t, err, "invalid marketplace name")
	for _, source := range []string{"", "http://example.com/catalog.json", "ftp://example.com/catalog.json", "https://user@example.com/catalog.json", "https://example.com/catalog.json#fragment", "https://", "missing-file"} {
		if _, err := store.AddMarket(ctx, "local", source); err == nil {
			t.Fatalf("invalid market source accepted: %q", source)
		}
	}
	resolved, _, _, err := resolveInstallSource("https://example.com/plugin.zip", nil)
	if err != nil || resolved != "https://example.com/plugin.zip" {
		t.Fatalf("direct HTTPS source resolution = %s, %v", resolved, err)
	}
	_, err = store.Install(ctx, "", true)
	requireError(t, err, "source is required")
	_, err = store.Install(ctx, "missing-file", true)
	requireError(t, err, "read plugin source")
}

func TestHTTPSRedirectsAndDownloadBounds(t *testing.T) {
	ctx := context.Background()
	plain := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		t.Error("HTTPS download followed HTTP downgrade")
		writer.Write([]byte("insecure"))
	}))
	defer plain.Close()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/downgrade":
			http.Redirect(writer, request, plain.URL, http.StatusFound)
		case "/redirect":
			http.Redirect(writer, request, "/redirect", http.StatusFound)
		case "/large":
			writer.Header().Set("Content-Length", "100")
			writer.Write([]byte(strings.Repeat("x", 100)))
		case "/chunked":
			writer.(http.Flusher).Flush()
			writer.Write([]byte(strings.Repeat("x", 100)))
		case "/ok":
			writer.Write([]byte("ok"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	store := NewStore(t.TempDir())
	store.client = server.Client()
	for _, test := range []struct{ path, want string }{{"/downgrade", "HTTPS"}, {"/redirect", "too many redirects"}, {"/large", "byte limit"}, {"/chunked", "byte limit"}, {"/missing", "HTTP 404"}} {
		_, _, err := store.download(ctx, server.URL+test.path, 10)
		requireError(t, err, test.want)
	}
	data, _, err := store.download(ctx, server.URL+"/ok", 10)
	if err != nil || string(data) != "ok" {
		t.Fatalf("download = %s, %v", data, err)
	}
	_, _, err = store.download(ctx, plain.URL, 10)
	requireError(t, err, "HTTPS")
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, _, err = store.download(cancelled, server.URL+"/ok", 10)
	requireError(t, err, "context canceled")
}

func TestRemoteCatalogRejectsUntrustedSources(t *testing.T) {
	var published atomic.Value
	published.Store(`{"schema_version":1,"name":"Remote","plugins":[]}`)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(writer, published.Load().(string))
	}))
	defer server.Close()
	store := NewStore(t.TempDir())
	store.client = server.Client()
	for _, source := range []string{"http://example.com/plugin.zip", "file:///etc/plugin.zip", "https://user@example.com/plugin.zip", "%invalid"} {
		published.Store(`{"schema_version":1,"name":"Remote","plugins":[{"id":"example","name":"Example","version":"1","source":"` + source + `","sha256":"` + strings.Repeat("a", 64) + `"}]}`)
		if _, err := store.AddMarket(context.Background(), "remote", server.URL+"/catalog"); err == nil {
			t.Fatalf("untrusted remote source accepted: %s", source)
		}
	}
	// Relative references are always remote HTTPS ZIPs, so they need a digest.
	published.Store(`{"schema_version":1,"name":"Remote","plugins":[{"id":"example","name":"Example","version":"1","source":"plugin.zip"}]}`)
	_, err := store.AddMarket(context.Background(), "remote", server.URL+"/catalog")
	requireError(t, err, "requires sha256")
}

func TestCatalogSourceNeverFallsBackToLocal(t *testing.T) {
	resolved, origin, _, err := resolveInstallSource("example@missing", nil)
	if err == nil || resolved != "" || origin != "" {
		t.Fatalf("unavailable catalog origin changed to local source: %s, %s, %v", resolved, origin, err)
	}
	path := filepath.Join(t.TempDir(), "example@local")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	resolved, origin, _, err = resolveInstallSource(path, nil)
	if err != nil || resolved != path || origin != path {
		t.Fatalf("explicit local path = %s, %s, %v", resolved, origin, err)
	}
}

func TestUppercaseHTTPSCatalogInstall(t *testing.T) {
	archive := testZIP(t, "example", "1")
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/plugin.zip" {
			writer.Write(archive)
			return
		}
		json.NewEncoder(writer).Encode(catalog{SchemaVersion: 1, Name: "Remote", Plugins: []catalogPlugin{{ID: "example", Name: "Example", Version: "1", Source: "HTTPS://" + request.Host + "/plugin.zip", SHA256: zipDigest(archive)}}})
	}))
	defer server.Close()
	store := NewStore(t.TempDir())
	store.client = server.Client()
	if _, err := store.AddMarket(context.Background(), "remote", "HTTPS://"+strings.TrimPrefix(server.URL, "https://")+"/catalog.json"); err != nil {
		t.Fatal(err)
	}
	item, err := store.Install(context.Background(), "example@remote", false)
	if err != nil || item.Manifest.ID != "example" {
		t.Fatalf("uppercase HTTPS catalog install = %+v, %v", item, err)
	}
}

func TestDisplaySource(t *testing.T) {
	for _, test := range []struct{ source, want string }{
		{"https://user:password@example.com/catalog.json?token=secret#private", "https://example.com/catalog.json"},
		{"https://example.com/catalog.json?", "https://example.com/catalog.json"},
		{"HTTPS://example.com/catalog.json?token=secret", "https://example.com/catalog.json"},
		{"http://example.com/plugin.zip?token=secret", "http://example.com/plugin.zip"},
		{`C:\plugins\example`, `C:\plugins\example`},
		{"/home/user/plugins/example", "/home/user/plugins/example"},
		{"./example@local", "./example@local"},
		{"example@market", "example@market"},
	} {
		if got := DisplaySource(test.source); got != test.want {
			t.Errorf("DisplaySource(%q) = %q, want %q", test.source, got, test.want)
		}
	}
}

func TestDownloadErrorsRedactQueries(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/redirect":
			http.Redirect(writer, request, "/redirect?redirect_token=redirect-secret", http.StatusFound)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	store := NewStore(t.TempDir())
	store.client = server.Client()
	for _, path := range []string{"/missing", "/redirect"} {
		_, _, err := store.download(context.Background(), server.URL+path+"?token=request-secret", 100)
		if err == nil {
			t.Fatal("expected download failure")
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token=") || strings.Contains(err.Error(), "?") {
			t.Fatalf("download error exposed query credentials: %v", err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := store.download(cancelled, server.URL+"/catalog?token=request-secret", 100)
	if !errors.Is(err, context.Canceled) || strings.Contains(err.Error(), "request-secret") {
		t.Fatalf("redacted cancellation error = %v", err)
	}
	// Inner url.Error URLs may refer to redirects rather than the source URL.
	original := &url.Error{Op: "Get", URL: "https://user:password@example.com/redirect?token=redirect-secret", Err: context.DeadlineExceeded}
	redacted := redactDownloadError(original, "https://example.com/catalog?token=request-secret")
	if !errors.Is(redacted, context.DeadlineExceeded) || strings.Contains(redacted.Error(), "secret") || strings.Contains(redacted.Error(), "password") {
		t.Fatalf("nested URL error exposed credentials: %v", redacted)
	}
}
