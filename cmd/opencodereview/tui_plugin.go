// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The management views contain names and package metadata, never MCP secrets.
type tuiCatalogPlugin struct {
	Market      string `json:"market"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Source      string `json:"source"`
	SHA256      string `json:"sha256"`
}

type tuiMarketInfo struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

type tuiPluginSnapshot struct {
	plugins []pluginInfo
	markets []tuiMarketInfo
	entries []tuiCatalogPlugin
	market  bool
}

type tuiPluginDoneMsg struct {
	id       int
	screen   tuiScreen
	query    string
	pluginID string
	marketID string
	snapshot *tuiPluginSnapshot
	detail   *pluginInfo
	changed  bool
	err      error
}

// tuiPluginArgs only separates quoted arguments. Backslashes stay literal so
// Windows paths work; shell substitutions, operators, and escapes never run.
func tuiPluginArgs(line string) ([]string, error) {
	var args []string
	var token strings.Builder
	var quote rune
	started := false
	for _, r := range line {
		if unicode.IsControl(r) && r != '\t' {
			return nil, fmt.Errorf("control characters are not allowed")
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				token.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote, started = r, true
		} else if unicode.IsSpace(r) {
			if started {
				args = append(args, token.String())
				token.Reset()
				started = false
			}
		} else {
			token.WriteRune(r)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("close the quoted argument")
	}
	if started {
		args = append(args, token.String())
	}
	return args, nil
}

func (m reviewTUIModel) isPluginScreen() bool {
	return m.screen == tuiPlugins || m.screen == tuiPluginDetails || m.screen == tuiMarket || m.screen == tuiMarketDetails
}

func (m reviewTUIModel) selectedInstalledPlugin() string {
	if m.pluginError != "" {
		return ""
	}
	if m.screen == tuiPluginDetails && m.pluginDetail != nil {
		return m.pluginDetail.ID
	}
	if m.screen == tuiPlugins && m.selected >= 0 && m.selected < len(m.plugins) {
		return m.plugins[m.selected].ID
	}
	return ""
}

func (m reviewTUIModel) selectedMarketPlugin() string {
	if m.pluginError != "" {
		return ""
	}
	if m.screen == tuiMarketDetails && m.marketDetail != nil {
		return m.marketDetail.ID + "@" + m.marketDetail.Market
	}
	if m.screen == tuiMarket && m.selected >= 0 && m.selected < len(m.marketEntries) {
		p := m.marketEntries[m.selected]
		return p.ID + "@" + p.Market
	}
	return ""
}

func (m reviewTUIModel) pluginCommand(line string) (tea.Model, tea.Cmd) {
	args, err := tuiPluginArgs(line)
	if err != nil {
		m.notice, m.isError = m.tr("Invalid plugin arguments: %s", m.tr(err.Error())), true
		m.refresh(false)
		return m, nil
	}
	if len(args) == 0 || (len(args) == 1 && args[0] == "list") {
		return m.startPluginView(tuiPlugins, "")
	}
	valid := false
	switch args[0] {
	case "enable", "disable", "update", "remove", "show":
		if len(args) == 1 {
			if id := m.selectedInstalledPlugin(); id != "" {
				args = append(args, id)
			}
		}
		valid = len(args) == 2 && args[1] != "" && !strings.HasPrefix(args[1], "-")
		if valid && args[0] == "show" {
			return m.startPluginShow(args[1])
		}
	case "install":
		if len(args) == 1 || (len(args) == 2 && args[1] == "--enable") {
			if source := m.selectedMarketPlugin(); source != "" {
				args = append([]string{args[0], source}, args[1:]...)
			}
		}
		valid = (len(args) == 2 || (len(args) == 3 && args[2] == "--enable")) && args[1] != "" && !strings.HasPrefix(args[1], "-")
	case "market":
		if len(args) >= 2 {
			switch args[1] {
			case "list":
				if len(args) == 2 {
					return m.startPluginView(tuiMarket, "")
				}
			case "search":
				return m.startPluginView(tuiMarket, strings.Join(args[2:], " "))
			case "add":
				valid = len(args) == 4
			case "refresh", "remove":
				valid = len(args) == 3
			}
			for _, arg := range args[2:] {
				valid = valid && arg != "" && !strings.HasPrefix(arg, "-")
			}
		}
	}
	if !valid {
		m.notice, m.isError = m.tr("Use /plugin install SOURCE [--enable], enable|disable|update|remove|show [ID], or market add|refresh|remove."), true
		m.refresh(false)
		return m, nil
	}
	return m.startPluginAction(args)
}

func (m reviewTUIModel) pluginRequest(ctx context.Context, args []string, value any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.services.pluginCommand == nil {
		return fmt.Errorf("plugin management is unavailable")
	}
	output, err := m.services.pluginCommand(ctx, append([]string{"--json"}, args...))
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if value != nil {
		if err := json.Unmarshal([]byte(output), value); err != nil {
			return fmt.Errorf("decode plugin response: %w", err)
		}
	}
	return nil
}

func (m reviewTUIModel) pluginSnapshot(ctx context.Context, market bool, query string) (*tuiPluginSnapshot, error) {
	snapshot := &tuiPluginSnapshot{market: market}
	if err := m.pluginRequest(ctx, []string{"list"}, &snapshot.plugins); err != nil {
		return nil, err
	}
	if market {
		if err := m.pluginRequest(ctx, []string{"market", "list"}, &snapshot.markets); err != nil {
			return nil, err
		}
		args := []string{"market", "search"}
		if query != "" {
			args = append(args, "--", query)
		}
		if err := m.pluginRequest(ctx, args, &snapshot.entries); err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}

func (m reviewTUIModel) startPluginView(screen tuiScreen, query string) (tea.Model, tea.Cmd) {
	name := "Loading plugins"
	if screen == tuiMarket {
		name = "Loading marketplace"
	}
	ctx, id := m.beginJob(name)
	m.pluginJob, m.screen, m.pluginError, m.selected = true, screen, "", 0
	run := m.work(func() tea.Msg {
		snapshot, err := m.pluginSnapshot(ctx, screen == tuiMarket, query)
		return tuiPluginDoneMsg{id: id, screen: screen, query: query, snapshot: snapshot, err: err}
	})
	m.viewport.GotoTop()
	m.refresh(false)
	return m, tea.Batch(run, tuiTick())
}

func (m reviewTUIModel) startPluginShow(pluginID string) (tea.Model, tea.Cmd) {
	ctx, id := m.beginJob("Loading plugin")
	m.pluginJob, m.pluginError, m.screen = true, "", tuiPluginDetails
	run := m.work(func() tea.Msg {
		var detail pluginInfo
		err := m.pluginRequest(ctx, []string{"show", pluginID}, &detail)
		var snapshot *tuiPluginSnapshot
		if err == nil {
			snapshot, err = m.pluginSnapshot(ctx, false, "")
		}
		return tuiPluginDoneMsg{id: id, screen: tuiPluginDetails, pluginID: pluginID, detail: &detail, snapshot: snapshot, err: err}
	})
	m.viewport.GotoTop()
	m.refresh(false)
	return m, tea.Batch(run, tuiTick())
}

func (m reviewTUIModel) startPluginAction(args []string) (tea.Model, tea.Cmd) {
	screen := m.screen
	if !m.isPluginScreen() {
		screen = tuiPlugins
	}
	if args[0] == "market" {
		screen = tuiMarket
	}
	pluginID, marketID := m.selectedInstalledPlugin(), m.selectedMarketPlugin()
	if len(args) > 1 && args[0] != "market" && args[0] != "install" {
		pluginID = args[1]
	}
	ctx, id := m.beginJob("Managing plugins")
	m.pluginJob, m.pluginError, m.screen = true, "", screen
	query := m.pluginQuery
	run := m.work(func() tea.Msg {
		err := m.pluginRequest(ctx, args, nil)
		var snapshot *tuiPluginSnapshot
		if err == nil {
			snapshot, err = m.pluginSnapshot(ctx, screen == tuiMarket || screen == tuiMarketDetails, query)
			if err != nil {
				err = fmt.Errorf("changes saved, but reloading the plugin list failed: %w", err)
			}
		}
		return tuiPluginDoneMsg{id: id, screen: screen, query: query, pluginID: pluginID, marketID: marketID, snapshot: snapshot, changed: true, err: err}
	})
	m.refresh(false)
	return m, tea.Batch(run, tuiTick())
}

func (m reviewTUIModel) finishPluginJob(msg tuiPluginDoneMsg) (tea.Model, tea.Cmd) {
	if msg.id != m.job {
		return m, nil
	}
	m.finishJob(msg.err)
	m.pluginJob, m.screen, m.pluginError = false, msg.screen, ""
	if msg.err != nil {
		m.pluginError, m.notice = m.tr(sanitizeTerminal(ansi.Strip(msg.err.Error()))), ""
	} else {
		if snapshot := msg.snapshot; snapshot != nil {
			m.plugins, m.selected = snapshot.plugins, 0
			if snapshot.market {
				m.markets, m.marketEntries, m.pluginQuery = snapshot.markets, snapshot.entries, msg.query
			}
			m.pluginDetail, m.marketDetail = nil, nil
			for i, p := range m.plugins {
				if p.ID == msg.pluginID {
					m.pluginDetail = &m.plugins[i]
					if m.screen == tuiPlugins || m.screen == tuiPluginDetails {
						m.selected = i
					}
				}
			}
			for i, p := range m.marketEntries {
				if p.ID+"@"+p.Market == msg.marketID {
					m.marketDetail = &m.marketEntries[i]
					if m.screen == tuiMarket || m.screen == tuiMarketDetails {
						m.selected = i
					}
				}
			}
			if m.screen == tuiPluginDetails && m.pluginDetail == nil {
				m.screen = tuiPlugins
			}
			if m.screen == tuiMarketDetails && m.marketDetail == nil {
				m.screen = tuiMarket
			}
		}
		if msg.detail != nil {
			m.pluginDetail = msg.detail
		}
		if msg.changed {
			m.notice = "Plugin changes saved. Enabled plugins apply to subsequent reviews."
		}
	}
	m.viewport.GotoTop()
	m.refresh(false)
	m.ensureSelectionVisible()
	if m.quit {
		return m, tea.Quit
	}
	return m, nil
}

func (m reviewTUIModel) openSelectedPlugin() (tea.Model, tea.Cmd) {
	if m.busy || m.pluginError != "" {
		return m, nil
	}
	if m.screen == tuiPlugins && m.selected >= 0 && m.selected < len(m.plugins) {
		p := m.plugins[m.selected]
		m.pluginDetail, m.screen = &p, tuiPluginDetails
	}
	if m.screen == tuiMarket && m.selected >= 0 && m.selected < len(m.marketEntries) {
		p := m.marketEntries[m.selected]
		m.marketDetail, m.screen = &p, tuiMarketDetails
	}
	m.viewport.GotoTop()
	m.refresh(false)
	return m, nil
}

func (m reviewTUIModel) pluginState(id string) string {
	for _, p := range m.plugins {
		if p.ID == id {
			if p.Enabled {
				return m.tr("Enabled")
			}
			return m.tr("Disabled")
		}
	}
	return m.tr("Not installed")
}

func (m reviewTUIModel) pluginContent() string {
	market := m.screen == tuiMarket || m.screen == tuiMarketDetails
	title := "Installed plugins"
	if market {
		title = "Plugin marketplace"
	}
	lines := []string{m.tint(m.tr(title), tuiBrandFace), ""}
	if m.pluginError != "" {
		return strings.Join(append(lines, m.tr("Plugin task failed"), m.pluginError, "", m.tr("Use /plugins or /market to reload, then retry the action.")), "\n")
	}
	if m.screen == tuiPluginDetails && m.pluginDetail != nil {
		p := m.pluginDetail
		state := m.tr("Disabled")
		if p.Enabled {
			state = m.tr("Enabled")
		}
		lines = append(lines, pluginText(p.Name)+"  "+pluginText(p.Version)+"  ·  "+state,
			m.tr("Plugin ID: %s", pluginText(p.ID)), "", pluginText(p.Description), "",
			m.tr("Source: %s", pluginText(p.Source)), m.tr("Package: %s", pluginText(p.Directory)),
			m.tr("Rule packs: %s", pluginText(strings.Join(p.Rules, ", "))),
			m.tr("MCP servers: %s", pluginText(strings.Join(p.MCPServers, ", "))), "",
			m.tr("Enable or disable: /plugin enable | /plugin disable"),
			m.tr("Update or remove: /plugin update | /plugin remove"),
			m.tr("Enabled MCP tools can execute code during reviews."))
		if p.Review != nil {
			lines = append(lines, "", m.tr("Run once: /review %s", pluginText(p.ID)))
		}
	} else if m.screen == tuiMarketDetails && m.marketDetail != nil {
		p := m.marketDetail
		lines = append(lines, pluginText(p.Name)+"  "+pluginText(p.Version)+"  ·  "+m.pluginState(p.ID),
			m.tr("Plugin ID: %s", pluginText(p.ID+"@"+p.Market)), "", pluginText(p.Description), "",
			m.tr("Source: %s", pluginText(p.Source)), m.tr("SHA-256: %s", pluginText(p.SHA256)), "",
			m.tr("Install selected plugin: /plugin install"),
			m.tr("Install and enable: /plugin install --enable"),
			m.tr("Installation copies the package. Enabling activates its rules and MCP tools."))
	} else if market {
		if m.pluginQuery != "" {
			lines = append(lines, m.tr("Search: %s", pluginText(m.pluginQuery)))
		}
		if len(m.markets) == 0 {
			lines = append(lines, m.tr("No marketplaces configured."))
		} else {
			names := make([]string, 0, len(m.markets))
			for _, item := range m.markets {
				names = append(names, pluginText(item.Name))
			}
			lines = append(lines, m.tr("Marketplaces: %s", strings.Join(names, ", ")))
		}
		lines = append(lines, m.tr("/market QUERY searches cached catalogs. Enter opens details."), "")
		start, end := tuiVisibleRange(len(m.marketEntries), m.selected, max(1, m.viewport.Height()-len(lines)-4))
		for i := start; i < end; i++ {
			p := m.marketEntries[i]
			lines = append(lines, m.pluginRow(i, p.ID+"@"+p.Market, p.Version, m.pluginState(p.ID)))
		}
		if len(m.marketEntries) == 0 {
			lines = append(lines, m.tr("No matching plugins. Add a marketplace or refresh its catalog."))
		}
		lines = append(lines, "", m.tr("Add marketplace: /plugin market add NAME SOURCE"),
			m.tr("Refresh or remove: /plugin market refresh NAME | /plugin market remove NAME"),
			m.tr("Install: /plugin install [ID@MARKET] [--enable]"))
	} else {
		lines = append(lines, m.tr("Enter opens details. Changes apply to subsequent reviews."), "")
		start, end := tuiVisibleRange(len(m.plugins), m.selected, max(1, m.viewport.Height()-len(lines)-4))
		for i := start; i < end; i++ {
			p := m.plugins[i]
			lines = append(lines, m.pluginRow(i, p.ID, p.Version, m.pluginState(p.ID)))
		}
		if len(m.plugins) == 0 {
			lines = append(lines, m.tr("No plugins installed. /market browses available packages."))
		}
		lines = append(lines, "", m.tr("Enable or disable: /plugin enable [ID] | /plugin disable [ID]"),
			m.tr("Update or remove: /plugin update [ID] | /plugin remove [ID]"),
			m.tr("Install local package: /plugin install \"PATH\" [--enable]"))
	}
	return strings.Join(lines, "\n")
}

func (m reviewTUIModel) pluginRow(index int, id, version, state string) string {
	prefix := "  "
	if index == m.selected {
		prefix = "> "
	}
	line := prefix + pluginText(id) + "  " + pluginText(version) + "  ·  " + state
	return m.selection(ansi.Truncate(line, m.bodyWidth(), "…"), index == m.selected, m.bodyWidth())
}
