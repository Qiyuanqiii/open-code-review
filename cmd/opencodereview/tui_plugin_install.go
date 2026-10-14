// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/alibaba/open-code-review/internal/plugin"
)

type tuiPluginPreparedMsg struct {
	id         int
	prepared   *tuiPreparedPlugin
	candidates []plugin.CatalogEntry
	err        error
}

// The preview owns the downloaded snapshot until confirmation transfers it to
// the install worker. Parent cancellation also cleans up an abandoned preview.
type tuiPreparedPlugin struct {
	packageInfo *plugin.PreparedInstall
	stopCleanup func() bool
	once        sync.Once
}

func newTUIPreparedPlugin(ctx context.Context, prepared *plugin.PreparedInstall) *tuiPreparedPlugin {
	result := &tuiPreparedPlugin{packageInfo: prepared}
	result.stopCleanup = context.AfterFunc(ctx, func() { _ = prepared.Close() })
	return result
}

func (p *tuiPreparedPlugin) close() {
	if p == nil {
		return
	}
	p.once.Do(func() {
		if p.stopCleanup != nil {
			p.stopCleanup()
		}
		_ = p.packageInfo.Close()
	})
}

func prepareTUIPlugin(ctx context.Context, source, id string) (*plugin.PreparedInstall, error) {
	store, err := plugin.DefaultStore()
	if err != nil {
		return nil, err
	}
	return store.PrepareInstall(ctx, source, id)
}

func installTUIPlugin(ctx context.Context, prepared *plugin.PreparedInstall, enable bool) (plugin.Installed, error) {
	store, err := plugin.DefaultStore()
	if err != nil {
		return plugin.Installed{}, err
	}
	return store.InstallPrepared(ctx, prepared, enable)
}

func tuiPluginURL(source string) bool {
	parsed, err := url.Parse(source)
	return err == nil && parsed.Host != "" && parsed.Scheme == "https"
}

func (m reviewTUIModel) isPluginInstallScreen() bool {
	return m.screen == tuiPluginLink || m.screen == tuiPluginChoose || m.screen == tuiPluginInstallPreview
}

func (m reviewTUIModel) openPluginLink(enable bool) reviewTUIModel {
	if !m.isPluginInstallScreen() {
		m.pluginReturn, m.pluginReturnIndex = m.screen, m.selected
	}
	m.pluginPrepared.close()
	m.pluginPrepared, m.pluginCandidates, m.pluginSource = nil, nil, ""
	m.screen, m.selected, m.pluginChoice, m.pluginEnable = tuiPluginLink, 0, 0, enable
	m.notice, m.pluginError, m.isError = "", "", false
	m.input.SetValue("")
	m.menuDismissed = true
	m.viewport.GotoTop()
	m.refresh(false)
	return m
}

func (m reviewTUIModel) startPluginPrepare(source, pluginID string) (tea.Model, tea.Cmd) {
	if m.screen == tuiPluginChoose {
		m.pluginChoice = m.selected
	}
	m.pluginPrepared.close()
	m.pluginPrepared, m.pluginSource = nil, source
	m.input.SetValue("")
	ctx, id := m.beginJob("Preparing plugin")
	m.pluginJob = true
	run := m.work(func() tea.Msg {
		if err := ctx.Err(); err != nil {
			return tuiPluginPreparedMsg{id: id, err: err}
		}
		if m.services.preparePlugin == nil {
			return tuiPluginPreparedMsg{id: id, err: fmt.Errorf("plugin management is unavailable")}
		}
		prepared, err := m.services.preparePlugin(ctx, source, pluginID)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err != nil {
			if prepared != nil {
				_ = prepared.Close()
			}
			var selection *plugin.SelectionRequiredError
			if errors.As(err, &selection) {
				return tuiPluginPreparedMsg{id: id, candidates: selection.Candidates}
			}
			return tuiPluginPreparedMsg{id: id, err: err}
		}
		if prepared == nil {
			return tuiPluginPreparedMsg{id: id, err: fmt.Errorf("plugin preparation produced no package")}
		}
		return tuiPluginPreparedMsg{id: id, prepared: newTUIPreparedPlugin(m.ctx, prepared)}
	})
	m.refresh(false)
	return m, tea.Batch(run, tuiTick())
}

func (m reviewTUIModel) finishPluginPrepare(msg tuiPluginPreparedMsg) (tea.Model, tea.Cmd) {
	if msg.id != m.job {
		msg.prepared.close()
		return m, nil
	}
	cancelled := m.stopping || errors.Is(msg.err, context.Canceled)
	m.finishJob(msg.err)
	m.pluginJob = false
	if m.quit {
		msg.prepared.close()
		return m, tea.Quit
	}
	if msg.err != nil || cancelled {
		msg.prepared.close()
		m.screen = tuiPluginLink
		m.input.SetValue(m.pluginSource)
		m.input.CursorEnd()
		if cancelled {
			m.notice, m.isError = "Plugin task cancelled.", false
		} else {
			m.notice = sanitizeTerminal(ansi.Strip(msg.err.Error()))
		}
	} else if len(msg.candidates) > 0 {
		m.pluginCandidates, m.screen, m.selected = msg.candidates, tuiPluginChoose, 0
	} else if msg.prepared != nil {
		m.pluginPrepared, m.screen, m.selected = msg.prepared, tuiPluginInstallPreview, 0
		if m.pluginEnable {
			m.selected = 1
		}
	}
	m.viewport.GotoTop()
	m.refresh(false)
	m.ensureSelectionVisible()
	return m, nil
}

func (m reviewTUIModel) startPreparedPluginInstall(enable bool) (tea.Model, tea.Cmd) {
	prepared := m.pluginPrepared
	if prepared == nil || m.busy {
		return m, nil
	}
	m.pluginPrepared = nil
	ctx, id := m.beginJob("Installing plugin")
	m.pluginJob = true
	run := m.work(func() tea.Msg {
		defer prepared.close()
		var installed plugin.Installed
		err := ctx.Err()
		if err == nil {
			if m.services.installPlugin == nil {
				err = fmt.Errorf("plugin management is unavailable")
			} else {
				installed, err = m.services.installPlugin(ctx, prepared.packageInfo, enable)
			}
		}
		var snapshot *tuiPluginSnapshot
		if err == nil {
			snapshot, err = m.pluginSnapshot(ctx, false, "")
			if err != nil {
				err = fmt.Errorf("changes saved, but reloading the plugin list failed: %w", err)
			}
		}
		return tuiPluginDoneMsg{id: id, screen: tuiPlugins, pluginID: installed.Manifest.ID, snapshot: snapshot, changed: true, err: err}
	})
	m.refresh(false)
	return m, tea.Batch(run, tuiTick())
}

func (m reviewTUIModel) updatePluginInstall(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.busy {
			m.quit = true
			m.stopJob()
			return m, nil
		}
		if m.input.Value() != "" {
			m.input.SetValue("")
			m.refresh(false)
			return m, nil
		}
		m.pluginPrepared.close()
		m.pluginPrepared = nil
		return m, tea.Quit
	case "esc":
		if m.busy {
			m.stopJob()
			return m, nil
		}
		m.pluginPrepared.close()
		m.pluginPrepared = nil
		switch m.screen {
		case tuiPluginInstallPreview:
			if len(m.pluginCandidates) > 1 {
				m.screen, m.selected = tuiPluginChoose, m.pluginChoice
			} else {
				m.screen = tuiPluginLink
			}
		case tuiPluginChoose:
			m.screen = tuiPluginLink
		default:
			m.screen, m.selected = m.pluginReturn, m.pluginReturnIndex
			m.pluginSource, m.pluginCandidates = "", nil
		}
		m.input.SetValue("")
		if m.screen == tuiPluginLink {
			m.input.SetValue(m.pluginSource)
			m.input.CursorEnd()
		}
		m.notice, m.isError = "", false
		m.viewport.GotoTop()
	case "enter":
		if m.busy {
			return m, nil
		}
		switch m.screen {
		case tuiPluginLink:
			source := strings.TrimSpace(m.input.Value())
			if !tuiPluginURL(source) {
				m.notice, m.isError = "Enter an HTTPS plugin URL.", true
				break
			}
			m.pluginCandidates = nil
			return m.startPluginPrepare(source, "")
		case tuiPluginChoose:
			if m.selected >= 0 && m.selected < len(m.pluginCandidates) {
				return m.startPluginPrepare(m.pluginSource, m.pluginCandidates[m.selected].ID)
			}
		case tuiPluginInstallPreview:
			return m.startPreparedPluginInstall(m.selected == 1)
		}
	case "up", "down":
		if !m.busy && m.screen != tuiPluginLink {
			count, delta := 2, 1
			if m.screen == tuiPluginChoose {
				count = len(m.pluginCandidates)
			}
			if msg.String() == "up" {
				delta = -1
			}
			m.selected = max(0, min(count-1, m.selected+delta))
			m.refresh(false)
			m.ensureSelectionVisible()
			return m, nil
		}
	case "pgup", "pgdown":
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	default:
		if m.screen == tuiPluginLink && !m.busy {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			m.refresh(false)
			return m, cmd
		}
	}
	m.refresh(false)
	m.ensureSelectionVisible()
	return m, nil
}

func (m reviewTUIModel) pluginActionRow(index int, label string) string {
	prefix := "  "
	if index == m.selected {
		prefix = "> "
	}
	return m.selection(ansi.Truncate(prefix+m.tr(label), m.bodyWidth(), "..."), index == m.selected, m.bodyWidth())
}

func (m reviewTUIModel) pluginInstallContent() string {
	lines := []string{m.tint(m.tr("Install from link"), tuiBrandFace), ""}
	switch m.screen {
	case tuiPluginLink:
		lines = append(lines, m.tr("Paste a ZIP, GitHub repository, or marketplace URL."),
			m.tr("Enter previews the plugin before installation."))
	case tuiPluginChoose:
		lines = append(lines, m.tr("Choose a plugin from this source."), "")
		start, end := tuiVisibleRange(len(m.pluginCandidates), m.selected, max(1, m.viewport.Height()-len(lines)))
		for i := start; i < end; i++ {
			p := m.pluginCandidates[i]
			lines = append(lines, m.pluginRow(i, p.Name+" ("+p.ID+")", p.Version, m.pluginState(p.ID)))
		}
	case tuiPluginInstallPreview:
		if m.pluginPrepared == nil {
			return strings.Join(append(lines, m.tr("Installing plugin")), "\n")
		}
		p := m.pluginPrepared.packageInfo
		manifest := p.Manifest
		servers := make([]string, 0, len(manifest.MCPServers))
		for name := range manifest.MCPServers {
			servers = append(servers, name)
		}
		sort.Strings(servers)
		lines = append(lines, pluginText(manifest.Name)+"  "+pluginText(manifest.Version),
			m.tr("Plugin ID: %s", pluginText(manifest.ID)), pluginText(manifest.Description), "",
			m.tr("Source: %s", pluginText(plugin.DisplaySource(p.Source))), m.tr("SHA-256: %s", pluginText(p.SHA256)))
		if p.Revision != "" {
			lines = append(lines, m.tr("Revision: %s", pluginText(p.Revision)))
		}
		lines = append(lines, "", m.tr("Rule packs: %s", pluginText(strings.Join(manifest.Rules, ", "))),
			m.tr("MCP servers: %s", pluginText(strings.Join(servers, ", "))))
		if manifest.Review != nil {
			lines = append(lines, m.tr("Review stages: %s", pluginText(strings.Join(manifest.Review.Stages, ", "))))
		}
		lines = append(lines, "", m.tr("Installation copies the package. Enabling activates its rules and MCP tools."))
		if len(servers) > 0 {
			lines = append(lines, m.tr("Enabled MCP tools can execute code during reviews."))
		}
		lines = append(lines, "", m.pluginActionRow(0, "Install"), m.pluginActionRow(1, "Install and enable"))
	}
	return strings.Join(lines, "\n")
}
