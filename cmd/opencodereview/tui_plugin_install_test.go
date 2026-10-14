// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/alibaba/open-code-review/internal/plugin"
)

func tuiInstallManifest(id string) plugin.Manifest {
	return plugin.Manifest{
		SchemaVersion: 1, ID: id, Name: "Link plugin", Version: "1.2.3", Description: "Additional review capabilities",
		Rules: []string{"rules.json"},
		MCPServers: map[string]plugin.MCPServer{
			"checks": {Type: "stdio", Command: "must-not-execute", Env: []string{"SECRET=must-not-display"}},
		},
		Review: &plugin.ReviewContribution{Protocol: "stdio-v1", Command: "must-not-execute", Stages: []string{"inspect", "report"}},
	}
}

func prepareTUIInstallFixture(t *testing.T, store *plugin.Store, id string) *plugin.PreparedInstall {
	t.Helper()
	source := t.TempDir()
	data, err := json.Marshal(tuiInstallManifest(id))
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string][]byte{plugin.ManifestFilename: data, "rules.json": []byte(`{}`)} {
		if err := os.WriteFile(filepath.Join(source, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prepared, err := store.PrepareInstall(context.Background(), source, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = prepared.Close() })
	return prepared
}

func assertTUIPreparedClosed(t *testing.T, store *plugin.Store, prepared *plugin.PreparedInstall) {
	t.Helper()
	if _, err := store.InstallPrepared(context.Background(), prepared, false); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("abandoned prepared package remains installable: %v", err)
	}
}

func TestTUIPluginInstallLinkEntryAndPaste(t *testing.T) {
	f := newTUIPluginFixture()
	m := testReviewTUI()
	m.services.pluginCommand = f.run
	m = commandTUIPlugins(t, m, "/plugins")
	if !strings.Contains(m.content(), "Install from link") {
		t.Fatal("installed plugins do not offer link installation")
	}
	for range len(f.plugins) {
		m, _ = pressTUI(m, tea.KeyDown)
	}
	m, cmd := pressTUI(m, tea.KeyEnter)
	if cmd != nil || m.screen != tuiPluginLink || m.busy || len(f.calls) != 1 {
		t.Fatal("link entry installed a package instead of opening input")
	}
	next, _ := m.Update(tea.PasteMsg{Content: "https://example.test/catalog.json"})
	m = next.(reviewTUIModel)
	if m.input.Value() != "https://example.test/catalog.json" || len(m.commandChoices()) != 0 {
		t.Fatal("pasting a source URL did not reach the link input")
	}
	m.services.preparePlugin = func(_ context.Context, source, id string) (*plugin.PreparedInstall, error) {
		if source != "https://example.test/catalog.json" || id != "" {
			t.Fatalf("source selection changed: %q %q", source, id)
		}
		return nil, &plugin.SelectionRequiredError{Candidates: []plugin.CatalogEntry{{ID: "first"}, {ID: "second"}}}
	}
	m, cmd = pressTUI(m, tea.KeyEnter)
	m = finishTUIPluginCommand(t, m, cmd)
	if m.screen != tuiPluginChoose || m.busy || len(m.pluginCandidates) != 2 {
		t.Fatal("Enter did not open the catalog plugin choice")
	}
	m, _ = pressTUI(m, tea.KeyEscape)
	if m.screen != tuiPluginLink || m.input.Value() != "https://example.test/catalog.json" {
		t.Fatal("Esc did not return to the pasted source")
	}
	m, _ = pressTUI(m, tea.KeyEscape)
	if m.screen != tuiPlugins || m.selected != len(f.plugins) || m.input.Value() != "" {
		t.Fatal("Esc did not cancel source entry")
	}
	for _, line := range []string{"/plugin install", "/plugin install --enable"} {
		next, cmd = m.command(line)
		m = next.(reviewTUIModel)
		if cmd != nil || m.screen != tuiPluginLink || m.busy {
			t.Fatalf("%q did not open link input", line)
		}
	}
}

func TestTUIPluginCatalogChoicePreviewsBeforeInstalling(t *testing.T) {
	setTestHome(t, t.TempDir())
	store, err := plugin.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	prepared := prepareTUIInstallFixture(t, store, "second")
	prepared.Source = "https://user:password@example.test/plugin.zip?token=must-not-display#private"
	m := testReviewTUI()
	m.services.pluginCommand = runPluginCommand
	var selected []string
	m.services.preparePlugin = func(_ context.Context, source, id string) (*plugin.PreparedInstall, error) {
		if source != "https://example.test/market.json" {
			t.Fatalf("selection lost original source: %s", source)
		}
		selected = append(selected, id)
		if id == "" {
			return nil, &plugin.SelectionRequiredError{Candidates: []plugin.CatalogEntry{{ID: "first", Name: "First"}, {ID: "second", Name: "Second"}}}
		}
		return prepared, nil
	}
	installs := 0
	m.services.installPlugin = func(ctx context.Context, packageInfo *plugin.PreparedInstall, enable bool) (plugin.Installed, error) {
		installs++
		if packageInfo != prepared || !enable {
			t.Fatal("confirmation changed package or enable choice")
		}
		return store.InstallPrepared(ctx, packageInfo, enable)
	}
	m = commandTUIPlugins(t, m, "/plugin install https://example.test/market.json")
	m, _ = pressTUI(m, tea.KeyDown)
	m, cmd := pressTUI(m, tea.KeyEnter)
	m = finishTUIPluginCommand(t, m, cmd)
	if m.screen != tuiPluginInstallPreview || installs != 0 || len(selected) != 2 || selected[1] != "second" {
		t.Fatal("catalog choice installed without a package preview")
	}
	content := m.content()
	for _, want := range []string{"Link plugin", "1.2.3", "Rule packs: rules.json", "MCP servers: checks", "Review stages: inspect, report", "Install and enable"} {
		if !strings.Contains(content, want) {
			t.Fatalf("preview omitted %q", want)
		}
	}
	if strings.Contains(content, "must-not-display") || strings.Contains(content, "password") || strings.Contains(content, "#private") || strings.Contains(content, "must-not-execute") || len(m.plugins) != 0 {
		t.Fatal("preview exposed credentials or executed installation")
	}
	if !strings.Contains(content, "Source: https://example.test/plugin.zip") {
		t.Fatal("preview lost safe source identity while redacting URL credentials")
	}
	m, _ = pressTUI(m, tea.KeyDown)
	m, cmd = pressTUI(m, tea.KeyEnter)
	if installs != 0 || !m.busy {
		t.Fatal("confirmation blocked the TUI update")
	}
	m = finishTUIPluginCommand(t, m, cmd)
	if installs != 1 || m.isError || m.screen != tuiPlugins || len(m.plugins) != 1 || !m.plugins[0].Enabled {
		t.Fatalf("confirmation did not install and refresh enabled plugin: %s", m.pluginError)
	}
}

func TestTUIPluginURLDefaultServicesInstallThePreviewedPackage(t *testing.T) {
	setTestHome(t, t.TempDir())
	var archive bytes.Buffer
	w := zip.NewWriter(&archive)
	manifest, err := json.Marshal(tuiInstallManifest("from-link"))
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{plugin.ManifestFilename: manifest, "rules.json": []byte(`{}`)} {
		file, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var downloads atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downloads.Add(1)
		_, _ = w.Write(archive.Bytes())
	}))
	defer server.Close()
	previousTransport := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	defer func() { http.DefaultTransport = previousTransport }()
	m := testReviewTUI()
	m.services.preparePlugin, m.services.installPlugin, m.services.pluginCommand = prepareTUIPlugin, installTUIPlugin, runPluginCommand
	m = commandTUIPlugins(t, m, "/plugin install "+server.URL+"/plugin.zip")
	if m.screen != tuiPluginInstallPreview || m.isError || m.selected != 0 || downloads.Load() != 1 {
		t.Fatalf("default services did not preview ZIP: %s", m.notice)
	}
	packageInfo := m.pluginPrepared.packageInfo
	if packageInfo.SHA256 == "" {
		t.Fatal("ZIP preview did not identify its downloaded package")
	}
	m, cmd := pressTUI(m, tea.KeyEnter)
	m = finishTUIPluginCommand(t, m, cmd)
	if m.isError || len(m.plugins) != 1 || m.plugins[0].Enabled || downloads.Load() != 1 {
		t.Fatalf("default services did not install the same disabled package: %s", m.pluginError)
	}
	store, err := plugin.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	assertTUIPreparedClosed(t, store, packageInfo)
}

func TestTUIPluginPreviewEscapeAndStaleResultClosePackages(t *testing.T) {
	store := plugin.NewStore(filepath.Join(t.TempDir(), "store"))
	for _, candidates := range [][]plugin.CatalogEntry{nil, {{ID: "first"}, {ID: "second"}}} {
		prepared := prepareTUIInstallFixture(t, store, "preview")
		m := testReviewTUI().openPluginLink(false)
		m.pluginSource, m.pluginCandidates = "https://example.test/source", candidates
		m.screen, m.pluginPrepared = tuiPluginInstallPreview, newTUIPreparedPlugin(m.ctx, prepared)
		m, _ = pressTUI(m, tea.KeyEscape)
		want := tuiPluginLink
		if len(candidates) > 1 {
			want = tuiPluginChoose
		}
		if m.screen != want || m.pluginPrepared != nil {
			t.Fatal("Esc did not release preview and return one screen")
		}
		assertTUIPreparedClosed(t, store, prepared)
	}
	prepared := prepareTUIInstallFixture(t, store, "stale")
	m := testReviewTUI()
	ctx, id := m.beginJob("Reviewing")
	defer m.cancel()
	next, _ := m.Update(tuiPluginPreparedMsg{id: id - 1, prepared: newTUIPreparedPlugin(m.ctx, prepared)})
	m = next.(reviewTUIModel)
	if !m.busy || m.screen != tuiHome || ctx.Err() != nil {
		t.Fatal("stale preparation replaced or cancelled the active job")
	}
	assertTUIPreparedClosed(t, store, prepared)
}

func TestTUIPluginPrepareCancellationReleasesLatePackage(t *testing.T) {
	store := plugin.NewStore(filepath.Join(t.TempDir(), "store"))
	for _, beforeWorker := range []bool{true, false} {
		prepared := prepareTUIInstallFixture(t, store, "cancelled")
		m := testReviewTUI().openPluginLink(false)
		calls := 0
		m.services.preparePlugin = func(context.Context, string, string) (*plugin.PreparedInstall, error) {
			calls++
			m.cancel()
			return prepared, nil
		}
		next, cmd := m.startPluginPrepare("https://example.test/plugin.zip", "")
		m = next.(reviewTUIModel)
		if beforeWorker {
			m, _ = pressTUI(m, tea.KeyEscape)
		}
		m = finishTUIPluginCommand(t, m, cmd)
		if m.busy || m.screen != tuiPluginLink || m.pluginPrepared != nil || m.isError || m.input.Value() != "https://example.test/plugin.zip" {
			t.Fatal("cancelled preparation displayed a live preview or lost input")
		}
		if beforeWorker {
			if calls != 0 {
				t.Fatal("cancelled worker started a download")
			}
		} else {
			assertTUIPreparedClosed(t, store, prepared)
		}
	}
}

func TestTUIPluginInstallCancelledBeforeWorkerAndParentCleanup(t *testing.T) {
	store := plugin.NewStore(filepath.Join(t.TempDir(), "store"))
	prepared := prepareTUIInstallFixture(t, store, "cancel-install")
	m := testReviewTUI().openPluginLink(false)
	m.screen, m.pluginPrepared = tuiPluginInstallPreview, newTUIPreparedPlugin(m.ctx, prepared)
	m.services.installPlugin = func(context.Context, *plugin.PreparedInstall, bool) (plugin.Installed, error) {
		t.Fatal("cancelled install worker performed installation")
		return plugin.Installed{}, nil
	}
	m, cmd := pressTUI(m, tea.KeyEnter)
	m, _ = pressTUI(m, tea.KeyEscape)
	m = finishTUIPluginCommand(t, m, cmd)
	if m.busy || !m.isError || !strings.Contains(m.pluginError, "context canceled") {
		t.Fatal("install cancellation hid the operation result")
	}
	assertTUIPreparedClosed(t, store, prepared)
	parentPrepared := prepareTUIInstallFixture(t, store, "parent-cancel")
	ctx, cancel := context.WithCancel(context.Background())
	lease := newTUIPreparedPlugin(ctx, parentPrepared)
	cancel()
	deadline := time.Now().Add(time.Second)
	for {
		_, err := store.InstallPrepared(ctx, parentPrepared, false)
		if err != nil && strings.Contains(err.Error(), "closed") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("parent cancellation did not close preview: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	lease.close()
}

func TestTUIPluginLinkErrorsAndEnablePreference(t *testing.T) {
	m := testReviewTUI().openPluginLink(false)
	m.input.SetValue("file:///plugin.zip")
	m, cmd := pressTUI(m, tea.KeyEnter)
	if cmd != nil || m.busy || !m.isError || m.input.Value() != "file:///plugin.zip" {
		t.Fatal("invalid source must remain an editable input error")
	}
	for _, err := range []error{errors.New("download failed"), nil} {
		m.services.preparePlugin = func(context.Context, string, string) (*plugin.PreparedInstall, error) { return nil, err }
		next, cmd := m.startPluginPrepare("https://example.test/plugin.zip", "")
		m = finishTUIPluginCommand(t, next.(reviewTUIModel), cmd)
		if !m.isError || m.screen != tuiPluginLink || m.busy {
			t.Fatal("preparation error did not remain editable")
		}
	}
	m.services.preparePlugin = nil
	next, cmd := m.startPluginPrepare("https://example.test/plugin.zip", "")
	m = finishTUIPluginCommand(t, next.(reviewTUIModel), cmd)
	if !strings.Contains(m.notice, "plugin management is unavailable") {
		t.Fatal("missing preparation service hid diagnostic")
	}
	store := plugin.NewStore(filepath.Join(t.TempDir(), "store"))
	prepared := prepareTUIInstallFixture(t, store, "enable-preferred")
	m.services.preparePlugin = func(context.Context, string, string) (*plugin.PreparedInstall, error) { return prepared, nil }
	m = commandTUIPlugins(t, m, "/plugin install https://example.test/plugin.zip --enable")
	if m.screen != tuiPluginInstallPreview || m.selected != 1 {
		t.Fatal("explicit enable flag lost its confirmation preference")
	}
	m, cmd = pressTUI(m, tea.KeyEnter)
	m = finishTUIPluginCommand(t, m, cmd)
	if !m.isError || !strings.Contains(m.pluginError, "plugin management is unavailable") {
		t.Fatal("missing installation service hid diagnostic")
	}
	assertTUIPreparedClosed(t, store, prepared)
}

func TestTUIPluginInstallScreensFitLocalizedTerminals(t *testing.T) {
	store := plugin.NewStore(filepath.Join(t.TempDir(), "store"))
	prepared := prepareTUIInstallFixture(t, store, "localized")
	for _, language := range tuiLanguageIDs {
		for _, size := range [][2]int{{100, 32}, {40, 18}, {24, 10}} {
			m := testReviewTUI().openPluginLink(false)
			m.preferences.Language = language
			next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = next.(reviewTUIModel)
			m.pluginPrepared = newTUIPreparedPlugin(m.ctx, prepared)
			m.pluginCandidates = []plugin.CatalogEntry{{ID: "first", Name: "First"}, {ID: "second", Name: "Second"}}
			for _, screen := range []tuiScreen{tuiPluginLink, tuiPluginChoose, tuiPluginInstallPreview} {
				m.screen = screen
				m.refresh(false)
				if screen == tuiPluginInstallPreview {
					m.ensureSelectionVisible()
					if !strings.Contains(m.viewport.View(), "> "+m.tr("Install")) {
						t.Fatalf("%s %v: install choice is outside the viewport", language, size)
					}
				}
				view := m.View().Content
				if len(strings.Split(view, "\n")) > size[1] || view != ansi.Strip(view) {
					t.Fatalf("%s %v: install screen exceeds height or contains styling", language, size)
				}
				for _, line := range strings.Split(view, "\n") {
					if ansi.StringWidth(line) > size[0] {
						t.Fatalf("%s %v: install screen exceeds width: %q", language, size, line)
					}
				}
			}
			// One fixture is shared between views; stop each parent's cleanup hook.
			m.pluginPrepared.stopCleanup()
		}
	}
}
