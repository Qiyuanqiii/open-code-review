// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type tuiPluginFixture struct {
	plugins []pluginInfo
	entries []tuiCatalogPlugin
	markets []tuiMarketInfo
	calls   [][]string
	fail    string
}

func newTUIPluginFixture() *tuiPluginFixture {
	return &tuiPluginFixture{
		plugins: []pluginInfo{
			{ID: "first", Name: "First plugin", Version: "1.0", Description: "Review rules", Rules: []string{"rules"}, MCPServers: []string{"checks"}},
			{ID: "second", Name: "Second plugin", Version: "2.0", Enabled: true},
		},
		entries: []tuiCatalogPlugin{
			{ID: "first", Market: "examples", Name: "First plugin", Version: "1.1", SHA256: "verified-digest"},
			{ID: "third", Market: "examples", Name: "Third plugin", Version: "3.0", Source: "https://example.test/package.zip", Description: "Additional rules"},
		},
		markets: []tuiMarketInfo{{Name: "examples", Source: "/catalog"}},
	}
}

func (f *tuiPluginFixture) run(_ context.Context, raw []string) (string, error) {
	f.calls = append(f.calls, append([]string{}, raw...))
	if len(raw) < 2 || raw[0] != "--json" {
		return "", fmt.Errorf("expected JSON management output")
	}
	args := raw[1:]
	if args[0] == f.fail {
		return "", errors.New("permission denied\ncheck package access")
	}
	var response any = map[string]string{"status": "ok"}
	switch args[0] {
	case "list":
		response = f.plugins
	case "show":
		for _, p := range f.plugins {
			if p.ID == args[1] {
				response = p
			}
		}
	case "enable", "disable", "update":
		for i := range f.plugins {
			if f.plugins[i].ID == args[1] {
				if args[0] == "update" {
					f.plugins[i].Version = "new-version"
				} else {
					f.plugins[i].Enabled = args[0] == "enable"
				}
			}
		}
	case "remove":
		for i, p := range f.plugins {
			if p.ID == args[1] {
				f.plugins = append(f.plugins[:i:i], f.plugins[i+1:]...)
				break
			}
		}
	case "install":
		f.plugins = append(f.plugins, pluginInfo{ID: "third", Name: "Third plugin", Version: "3.0", Enabled: len(args) == 3 && args[2] == "--enable"})
	case "market":
		if args[1] == "list" {
			response = f.markets
		} else if args[1] == "search" {
			response = f.entries
		}
	}
	data, err := json.Marshal(response)
	return string(data), err
}

func finishTUIPluginCommand(t *testing.T, m reviewTUIModel, cmd tea.Cmd) reviewTUIModel {
	t.Helper()
	if cmd == nil {
		t.Fatal("plugin operation did not create a worker")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatal("plugin work must keep the UI ticking")
	}
	next, _ := m.Update(batch[0]())
	return next.(reviewTUIModel)
}

func commandTUIPlugins(t *testing.T, m reviewTUIModel, line string) reviewTUIModel {
	t.Helper()
	next, cmd := m.command(line)
	return finishTUIPluginCommand(t, next.(reviewTUIModel), cmd)
}

func TestTUIPluginArgsPreservePathsAndLiteralShellText(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{`install "D:\My Plugins\rules\" --enable`, []string{"install", `D:\My Plugins\rules\`, "--enable"}},
		{`market add example 'D:\Catalog Files\market.json'`, []string{"market", "add", "example", `D:\Catalog Files\market.json`}},
		{`install '$(touch marker); & echo unsafe'`, []string{"install", "$(touch marker); & echo unsafe"}},
		{`show ""`, []string{"show", ""}},
		{" \t ", nil},
		{`install "part one"part-two`, []string{"install", "part onepart-two"}},
	} {
		t.Run(tc.line, func(t *testing.T) {
			got, err := tuiPluginArgs(tc.line)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("arguments = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, line := range []string{`install "unterminated`, "install first\nremove second", "install \x00", "install \x1b[31m"} {
		if _, err := tuiPluginArgs(line); err == nil {
			t.Fatalf("malformed arguments accepted: %q", line)
		}
	}
}

func TestTUIPluginsNavigateWithoutExecutingManagementActions(t *testing.T) {
	f := newTUIPluginFixture()
	m := testReviewTUI()
	m.services.pluginCommand = f.run
	next, cmd := m.command("/plugins")
	m = next.(reviewTUIModel)
	if !m.busy || len(f.calls) != 0 {
		t.Fatal("plugin loading blocked the UI update")
	}
	m = finishTUIPluginCommand(t, m, cmd)
	if m.busy || m.screen != tuiPlugins || len(m.plugins) != 2 || !strings.Contains(m.content(), "Disabled") || !strings.Contains(m.content(), "Enabled") {
		t.Fatal("installed list lost plugin state")
	}
	m, _ = pressTUI(m, tea.KeyDown)
	m, cmd = pressTUI(m, tea.KeyEnter)
	if cmd != nil || m.screen != tuiPluginDetails || m.pluginDetail.ID != "second" || len(f.calls) != 1 {
		t.Fatal("opening details executed an action or selected the wrong plugin")
	}
	m, _ = pressTUI(m, tea.KeyEscape)
	if m.screen != tuiPlugins || m.selected != 1 {
		t.Fatal("details did not return to the selected list item")
	}
	m = commandTUIPlugins(t, m, "/plugin show first")
	if m.pluginDetail.ID != "first" || !strings.Contains(m.content(), "MCP servers: checks") {
		t.Fatal("show command did not display the safe management view")
	}
}

func TestTUIMarketNavigationAndSelectedInstall(t *testing.T) {
	f := newTUIPluginFixture()
	m := testReviewTUI()
	m.services.pluginCommand = f.run
	m = commandTUIPlugins(t, m, "/market")
	if m.screen != tuiMarket || len(m.marketEntries) != 2 || !strings.Contains(m.content(), "first@examples") || !strings.Contains(m.content(), "Not installed") {
		t.Fatal("market list lost entries or installed state")
	}
	m, _ = pressTUI(m, tea.KeyDown)
	m, cmd := pressTUI(m, tea.KeyEnter)
	if cmd != nil || m.screen != tuiMarketDetails || m.marketDetail.ID != "third" || len(f.calls) != 3 {
		t.Fatal("market details triggered installation")
	}
	m = commandTUIPlugins(t, m, "/plugin install --enable")
	if !reflect.DeepEqual(f.calls[3], []string{"--json", "install", "third@examples", "--enable"}) {
		t.Fatalf("selected install lost catalog identity: %v", f.calls[3])
	}
	if m.busy || m.screen != tuiMarketDetails || m.marketDetail.ID != "third" || m.pluginState("third") != "Enabled" || !strings.Contains(m.content(), "Enabled") {
		t.Fatal("installation did not refresh enabled state in the market")
	}
	m, _ = pressTUI(m, tea.KeyEscape)
	if m.screen != tuiMarket || m.selected != 1 {
		t.Fatal("installed market item lost its list selection")
	}
}

func TestTUIPluginActionsRefreshSelectedDetails(t *testing.T) {
	f := newTUIPluginFixture()
	m := testReviewTUI()
	m.services.pluginCommand = f.run
	m = commandTUIPlugins(t, m, "/plugins")
	m, _ = pressTUI(m, tea.KeyEnter)
	for _, action := range []string{"enable", "disable", "update", "remove"} {
		m = commandTUIPlugins(t, m, "/plugin "+action)
		if m.busy || m.isError {
			t.Fatalf("%s did not finish successfully: %s", action, m.pluginError)
		}
		switch action {
		case "enable":
			if !m.pluginDetail.Enabled {
				t.Fatal("enable did not refresh details")
			}
		case "disable":
			if m.pluginDetail.Enabled {
				t.Fatal("disable retained enabled state")
			}
		case "update":
			if m.pluginDetail.Version != "new-version" {
				t.Fatal("update did not reload the package version")
			}
		case "remove":
			if m.screen != tuiPlugins || len(m.plugins) != 1 || m.plugins[0].ID != "second" {
				t.Fatal("remove left a deleted plugin open")
			}
		}
	}
}

func TestTUIPluginCommandValidationAndBusyGuard(t *testing.T) {
	for _, line := range []string{
		"/plugins extra", "/plugin enable", "/plugin install", "/plugin remove first second", "/plugin update --json", "/plugin install source --unknown",
		"/plugin market add name", "/plugin market refresh", "/plugin market remove one two", "/plugin market unknown", "/plugin arbitrary", `/plugin install "unterminated`,
	} {
		t.Run(line, func(t *testing.T) {
			m := testReviewTUI()
			m.services.pluginCommand = func(context.Context, []string) (string, error) {
				t.Fatal("invalid command executed management work")
				return "", nil
			}
			next, cmd := m.command(line)
			m = next.(reviewTUIModel)
			if cmd != nil || m.busy || !m.isError {
				t.Fatal("invalid command did not remain a visible input error")
			}
		})
	}
	m := testReviewTUI()
	ctx, id := m.beginJob("Reviewing")
	m.services.pluginCommand = func(context.Context, []string) (string, error) {
		t.Fatal("management ran during a review")
		return "", nil
	}
	for _, line := range []string{"/plugins", "/market", "/plugin install local", "/plugin enable first", "/plugin market refresh examples"} {
		next, cmd := m.command(line)
		m = next.(reviewTUIModel)
		if cmd != nil || !m.busy || m.job != id || ctx.Err() != nil {
			t.Fatal("busy guard started or cancelled management work")
		}
	}
	m.cancel()
}

func TestTUIPluginFailureKeepsDiagnosticsAndBlocksSelectedActions(t *testing.T) {
	f := newTUIPluginFixture()
	m := testReviewTUI()
	m.services.pluginCommand = f.run
	m = commandTUIPlugins(t, m, "/plugins")
	f.fail = "enable"
	m = commandTUIPlugins(t, m, "/plugin enable")
	if m.busy || !m.isError || !strings.Contains(m.content(), "permission denied\ncheck package access") || strings.Contains(m.content(), "Plugin changes saved") {
		t.Fatal("plugin failure hid the diagnostic or reported success")
	}
	calls := len(f.calls)
	m, cmd := pressTUI(m, tea.KeyEnter)
	if cmd != nil || len(f.calls) != calls {
		t.Fatal("failed list allowed Enter to act on a stale selection")
	}
	next, cmd := m.command("/plugin remove")
	m = next.(reviewTUIModel)
	if cmd != nil || len(f.calls) != calls || !m.isError {
		t.Fatal("failed list supplied an implicit deletion target")
	}
	f.fail = ""
	m = commandTUIPlugins(t, m, "/plugins")
	if m.pluginError != "" || m.isError || m.plugins[0].Enabled {
		t.Fatal("retry did not recover the unmodified state")
	}
}

func TestTUIPluginMalformedOutputAndReloadFailure(t *testing.T) {
	for _, response := range []string{"invalid JSON", "[] trailing"} {
		m := testReviewTUI()
		m.services.pluginCommand = func(context.Context, []string) (string, error) { return response, nil }
		m = commandTUIPlugins(t, m, "/plugins")
		if !m.isError || !strings.Contains(m.content(), "decode plugin response") {
			t.Fatal("malformed management output looked like an empty success")
		}
	}
	f := newTUIPluginFixture()
	m := testReviewTUI()
	m.services.pluginCommand = f.run
	m = commandTUIPlugins(t, m, "/plugins")
	m.services.pluginCommand = func(ctx context.Context, args []string) (string, error) {
		result, err := f.run(ctx, args)
		if args[1] == "enable" {
			f.fail = "list"
		}
		return result, err
	}
	m = commandTUIPlugins(t, m, "/plugin enable first")
	if !f.plugins[0].Enabled || !m.isError || !strings.Contains(m.content(), "changes saved, but reloading") || strings.Contains(m.content(), "Disabled") {
		t.Fatal("refresh failure displayed stale plugin state as current")
	}
}

func TestTUIPluginArgumentsReachOnlyTheInjectedService(t *testing.T) {
	for _, tc := range []struct {
		line string
		want []string
	}{
		{`/plugin install "D:\Plugin Files\pack\"`, []string{"--json", "install", `D:\Plugin Files\pack\`}},
		{`/plugin install '$(touch marker); & echo unsafe'`, []string{"--json", "install", "$(touch marker); & echo unsafe"}},
		{`/plugin market add mine "D:\Market Files\catalog.json"`, []string{"--json", "market", "add", "mine", `D:\Market Files\catalog.json`}},
		{`/plugin market refresh mine`, []string{"--json", "market", "refresh", "mine"}},
		{`/plugin market remove mine`, []string{"--json", "market", "remove", "mine"}},
	} {
		t.Run(tc.line, func(t *testing.T) {
			f := newTUIPluginFixture()
			m := testReviewTUI()
			m.services.pluginCommand = f.run
			m = commandTUIPlugins(t, m, tc.line)
			if m.isError || !reflect.DeepEqual(f.calls[0], tc.want) {
				t.Fatalf("management arguments changed: %q, error %q", f.calls[0], m.pluginError)
			}
		})
	}
	f := newTUIPluginFixture()
	m := testReviewTUI()
	m.services.pluginCommand = f.run
	m = commandTUIPlugins(t, m, `/market "--json"`)
	if m.pluginQuery != "--json" || !reflect.DeepEqual(f.calls[2], []string{"--json", "market", "search", "--", "--json"}) {
		t.Fatal("market query was interpreted as a command flag")
	}
}

func TestTUIPluginCancellationAndStaleCompletion(t *testing.T) {
	m := testReviewTUI()
	var jobContext context.Context
	m.services.pluginCommand = func(ctx context.Context, _ []string) (string, error) {
		jobContext = ctx
		return "[]", nil
	}
	next, cmd := m.command("/plugins")
	m = next.(reviewTUIModel)
	next, _ = m.Update(tuiPluginDoneMsg{id: m.job - 1, screen: tuiMarket})
	m = next.(reviewTUIModel)
	if !m.busy || m.screen != tuiPlugins {
		t.Fatal("stale plugin completion changed the active task")
	}
	m, _ = pressTUI(m, tea.KeyEscape)
	if !m.busy || !m.stopping || !strings.Contains(m.notice, "plugin task") {
		t.Fatal("Escape did not request plugin cancellation")
	}
	m = finishTUIPluginCommand(t, m, cmd)
	if m.busy || !m.isError || !strings.Contains(m.pluginError, "context canceled") || jobContext != nil {
		t.Fatal("cancelled worker started management or lost the cancellation result")
	}
	m = commandTUIPlugins(t, testReviewTUI(), "/plugins")
	if !m.isError || !strings.Contains(m.content(), "plugin management is unavailable") {
		t.Fatal("missing service must produce a diagnostic")
	}
}

func TestTUIPluginViewsStayVisibleAndFitLocalizedTerminals(t *testing.T) {
	f := newTUIPluginFixture()
	for i := 0; i < 30; i++ {
		f.plugins = append(f.plugins, pluginInfo{ID: fmt.Sprintf("plugin-%02d", i), Version: "1.0"})
	}
	for _, language := range tuiLanguageIDs {
		for _, size := range [][2]int{{100, 32}, {40, 18}, {24, 10}} {
			m := testReviewTUI()
			m.preferences.Language = language
			m.services.pluginCommand = f.run
			next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			m = next.(reviewTUIModel)
			m = commandTUIPlugins(t, m, "/plugins")
			for i := 0; i < len(f.plugins)-1; i++ {
				m, _ = pressTUI(m, tea.KeyDown)
			}
			if !strings.Contains(m.viewport.View(), "plugin-29") {
				t.Fatalf("%s %v: selected plugin is outside the viewport", language, size)
			}
			for _, screen := range []tuiScreen{tuiPlugins, tuiPluginDetails, tuiMarket, tuiMarketDetails} {
				m.screen, m.pluginDetail, m.marketDetail, m.marketEntries, m.markets = screen, &f.plugins[0], &f.entries[0], f.entries, f.markets
				m.refresh(false)
				view := m.View().Content
				if len(strings.Split(view, "\n")) > size[1] || view != ansi.Strip(view) {
					t.Fatalf("%s %v: plugin screen exceeds height or contains styles", language, size)
				}
				for _, line := range strings.Split(view, "\n") {
					if ansi.StringWidth(line) > size[0] {
						t.Fatalf("%s %v: plugin screen exceeds width: %q", language, size, line)
					}
				}
			}
		}
	}
}

func TestTUIPluginMetadataCannotStyleOrForgeDisplayRows(t *testing.T) {
	m := testReviewTUI()
	m.screen = tuiPluginDetails
	m.pluginDetail = &pluginInfo{
		ID: "safe-id", Name: "Visible\x1b[31m name", Version: "1.0\nforged version",
		Description: "Description\x1b[2J\nforged description", Source: "source\nforged source", Directory: "package\rforged package",
		Rules: []string{"rules\nforged rule"}, MCPServers: []string{"server\x1b[32m\nforged server"},
	}
	content := m.pluginContent()
	if content != ansi.Strip(content) || strings.Contains(content, "\x1b") || strings.Contains(content, "\nforged") || strings.Contains(content, "[31m") {
		t.Fatal("package metadata can change terminal styling or add forged rows")
	}
	if !strings.Contains(content, "Description forged description") || !strings.Contains(content, "server forged server") {
		t.Fatal("safe metadata was lost while removing terminal controls")
	}
}

func TestTUIPluginManagementUsesNativeCommandStore(t *testing.T) {
	setTestHome(t, t.TempDir())
	source := filepath.Join(t.TempDir(), "Plugin With Spaces")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := `{"schema_version":1,"id":"native-tui","name":"Native TUI","version":"1.0.0","mcp_servers":{"never-start":{"type":"stdio","command":"must-not-be-executed","env":["SECRET=must-not-display"]}}}`
	if err := os.WriteFile(filepath.Join(source, "ocr-plugin.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	m := testReviewTUI()
	m.services.pluginCommand = runPluginCommand
	m = commandTUIPlugins(t, m, `/plugin install "`+source+`"`)
	if m.isError || len(m.plugins) != 1 || m.plugins[0].Enabled {
		t.Fatalf("native install failed or enabled automatically: %s", m.pluginError)
	}
	m, _ = pressTUI(m, tea.KeyEnter)
	if strings.Contains(m.content(), "must-not-display") || !strings.Contains(m.content(), "never-start") {
		t.Fatal("native plugin details exposed credentials or omitted server names")
	}
	m = commandTUIPlugins(t, m, "/plugin enable")
	if m.isError || !m.pluginDetail.Enabled {
		t.Fatalf("native enable did not refresh the selected plugin: %s", m.pluginError)
	}
	catalogPath := filepath.Join(t.TempDir(), "Catalog With Spaces.json")
	catalog, err := json.Marshal(map[string]any{
		"schema_version": 1,
		"name":           "Native TUI catalog",
		"plugins":        []map[string]string{{"id": "native-tui", "name": "Native TUI", "version": "1.0.0", "source": source}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(catalogPath, catalog, 0o600); err != nil {
		t.Fatal(err)
	}
	m = commandTUIPlugins(t, m, `/plugin market add local "`+catalogPath+`"`)
	if m.isError || m.screen != tuiMarket || len(m.marketEntries) != 1 || m.marketEntries[0].Market != "local" {
		t.Fatalf("native market did not load into the TUI: %s", m.pluginError)
	}
	m = commandTUIPlugins(t, m, "/plugin market search native-tui")
	m, _ = pressTUI(m, tea.KeyEnter)
	m = commandTUIPlugins(t, m, "/plugin remove native-tui")
	if m.isError || m.pluginState("native-tui") != "Not installed" || m.screen != tuiMarketDetails {
		t.Fatalf("native removal did not update market state: %s", m.pluginError)
	}
	m = commandTUIPlugins(t, m, "/plugin install")
	if m.isError || m.pluginState("native-tui") != "Disabled" {
		t.Fatalf("selected native market install failed: %s", m.pluginError)
	}
	m = commandTUIPlugins(t, m, "/plugin show native-tui")
	m, _ = pressTUI(m, tea.KeyEscape)
	if m.screen != tuiPlugins || len(m.plugins) != 1 || m.plugins[0].ID != "native-tui" {
		t.Fatal("native show did not return to the installed plugin list")
	}
}
