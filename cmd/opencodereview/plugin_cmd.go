// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 alibaba/open-code-review Contributors

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/alibaba/open-code-review/internal/plugin"
	"github.com/charmbracelet/x/ansi"
	"github.com/spf13/cobra"
)

// pluginInfo is the public management view. It omits MCP arguments, environment
// variables and authentication headers, which can contain credentials.
type pluginInfo struct {
	ID          string                     `json:"id"`
	Name        string                     `json:"name"`
	Version     string                     `json:"version"`
	Description string                     `json:"description"`
	Enabled     bool                       `json:"enabled"`
	Source      string                     `json:"source"`
	Directory   string                     `json:"directory"`
	Rules       []string                   `json:"rules"`
	MCPServers  []string                   `json:"mcp_servers"`
	Review      *plugin.ReviewContribution `json:"review,omitempty"`
}

func pluginManagementInfo(installed plugin.Installed) pluginInfo {
	m := installed.Manifest
	names := make([]string, 0, len(m.MCPServers))
	for name := range m.MCPServers {
		names = append(names, name)
	}
	sort.Strings(names)
	return pluginInfo{m.ID, m.Name, m.Version, m.Description, installed.Enabled, plugin.DisplaySource(installed.Source), installed.Dir, append([]string{}, m.Rules...), names, m.Review}
}

func pluginText(value string) string {
	return strings.Join(strings.Fields(sanitizeTerminal(ansi.Strip(value))), " ")
}

func pluginJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func newPluginCmd() *cobra.Command {
	var jsonOutput bool
	command := &cobra.Command{
		Use: "plugin", Aliases: []string{"plugins"}, Short: "Manage OCR plugins and marketplaces",
		Long: "Install rule packs and MCP tools for OCR. Installation copies the package without executing it. Enable a plugin to use its rules and tools in subsequent reviews.",
		Args: cobra.NoArgs, SilenceUsage: true, SilenceErrors: true,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	command.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output structured JSON")
	withStore := func(action func(*cobra.Command, []string, *plugin.Store) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			store, err := plugin.DefaultStore()
			if err != nil {
				return err
			}
			return action(cmd, args, store)
		}
	}
	printInstalled := func(cmd *cobra.Command, installed plugin.Installed) error {
		info := pluginManagementInfo(installed)
		if jsonOutput {
			return pluginJSON(cmd.OutOrStdout(), info)
		}
		state := "disabled"
		if info.Enabled {
			state = "enabled"
		}
		_, err := fmt.Fprintf(cmd.OutOrStdout(), "%s %s (%s)\n%s\nSource: %s\nPackage: %s\nRules: %s\nMCP servers: %s\n",
			pluginText(info.ID), pluginText(info.Version), state, pluginText(info.Description), pluginText(info.Source),
			pluginText(info.Directory), pluginText(strings.Join(info.Rules, ", ")), pluginText(strings.Join(info.MCPServers, ", ")))
		if err == nil && info.Review != nil {
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Run once: ocr review --review-plugin %s\n", pluginText(info.ID))
		}
		return err
	}
	command.AddCommand(&cobra.Command{
		Use: "list", Short: "List installed plugins and their enabled state", Args: cobra.NoArgs,
		RunE: withStore(func(cmd *cobra.Command, _ []string, store *plugin.Store) error {
			installed, err := store.List()
			if err != nil {
				return err
			}
			infos := make([]pluginInfo, 0, len(installed))
			for _, item := range installed {
				infos = append(infos, pluginManagementInfo(item))
			}
			if jsonOutput {
				return pluginJSON(cmd.OutOrStdout(), infos)
			}
			if len(infos) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No plugins installed. Use 'ocr plugin install PATH' or browse 'ocr plugin market search'.")
				return err
			}
			out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(out, "ID\tVERSION\tSTATE\tCAPABILITIES")
			for _, info := range infos {
				state := "disabled"
				if info.Enabled {
					state = "enabled"
				}
				capabilities := fmt.Sprintf("%d rule packs, %d MCP servers", len(info.Rules), len(info.MCPServers))
				if info.Review != nil {
					capabilities += ", review policy (explicit selection)"
				}
				fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", pluginText(info.ID), pluginText(info.Version), state, capabilities)
			}
			return out.Flush()
		}),
	})
	command.AddCommand(&cobra.Command{
		Use: "show <id>", Short: "Inspect an installed plugin without exposing tool credentials", Args: exactArgs(1),
		RunE: withStore(func(cmd *cobra.Command, args []string, store *plugin.Store) error {
			installed, err := findInstalledPlugin(store, args[0])
			if err != nil {
				return err
			}
			return printInstalled(cmd, installed)
		}),
	})
	var enable bool
	install := &cobra.Command{
		Use: "install <path|id@market>", Short: "Copy a local package or install from a registered marketplace",
		Example: "  ocr plugin install ./examples/ocr-plugins/review-checklist\n  ocr plugin install review-checklist@examples --enable", Args: exactArgs(1),
		RunE: withStore(func(cmd *cobra.Command, args []string, store *plugin.Store) error {
			installed, err := store.Install(cmd.Context(), args[0], enable)
			if err != nil {
				return err
			}
			if err := printInstalled(cmd, installed); err != nil {
				return err
			}
			if !jsonOutput && !installed.Enabled {
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Enable with: ocr plugin enable %s\n", pluginText(installed.Manifest.ID))
			}
			return err
		}),
	}
	install.Flags().BoolVar(&enable, "enable", false, "activate the package for subsequent reviews; its MCP tools can execute code")
	command.AddCommand(install)
	command.AddCommand(&cobra.Command{
		Use: "update <id>", Short: "Refresh a plugin from its original source, preserving enabled state", Args: exactArgs(1),
		RunE: withStore(func(cmd *cobra.Command, args []string, store *plugin.Store) error {
			installed, err := store.Update(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return printInstalled(cmd, installed)
		}),
	})
	for _, action := range []string{"enable", "disable", "remove"} {
		command.AddCommand(&cobra.Command{
			Use: action + " <id>", Short: strings.ToUpper(action[:1]) + action[1:] + " an installed plugin", Args: exactArgs(1),
			RunE: withStore(func(cmd *cobra.Command, args []string, store *plugin.Store) error {
				var err error
				if action == "remove" {
					err = store.Remove(args[0])
				} else {
					err = store.SetEnabled(args[0], action == "enable")
				}
				if err != nil {
					return err
				}
				if jsonOutput {
					return pluginJSON(cmd.OutOrStdout(), map[string]string{"id": args[0], "action": action, "status": "ok"})
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Plugin %s: %s complete. Changes apply to subsequent reviews.\n", pluginText(args[0]), action)
				return err
			}),
		})
	}
	market := &cobra.Command{Use: "market", Short: "Browse and manage marketplace catalogs", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	market.AddCommand(&cobra.Command{
		Use: "list", Short: "List configured marketplaces", Args: cobra.NoArgs,
		RunE: withStore(func(cmd *cobra.Command, _ []string, store *plugin.Store) error {
			markets, err := store.ListMarkets()
			if err != nil {
				return err
			}
			for i := range markets {
				markets[i].Source = plugin.DisplaySource(markets[i].Source)
				for j := range markets[i].Plugins {
					markets[i].Plugins[j].Source = plugin.DisplaySource(markets[i].Plugins[j].Source)
				}
			}
			if jsonOutput {
				return pluginJSON(cmd.OutOrStdout(), markets)
			}
			if len(markets) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No marketplaces configured. Use 'ocr plugin market add NAME PATH_OR_HTTPS_URL'.")
				return err
			}
			for _, item := range markets {
				if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", pluginText(item.Name), pluginText(item.Source)); err != nil {
					return err
				}
			}
			return nil
		}),
	})
	for _, action := range []string{"add", "refresh", "remove"} {
		use, count := action+" <name>", 1
		if action == "add" {
			use, count = "add <name> <path|https-url>", 2
		}
		market.AddCommand(&cobra.Command{
			Use: use, Short: strings.ToUpper(action[:1]) + action[1:] + " a marketplace", Args: exactArgs(count),
			RunE: withStore(func(cmd *cobra.Command, args []string, store *plugin.Store) error {
				var err error
				switch action {
				case "add":
					_, err = store.AddMarket(cmd.Context(), args[0], args[1])
				case "refresh":
					_, err = store.RefreshMarket(cmd.Context(), args[0])
				case "remove":
					err = store.RemoveMarket(args[0])
				}
				if err != nil {
					return err
				}
				if jsonOutput {
					return pluginJSON(cmd.OutOrStdout(), map[string]string{"market": args[0], "action": action, "status": "ok"})
				}
				_, err = fmt.Fprintf(cmd.OutOrStdout(), "Marketplace %s: %s complete.\n", pluginText(args[0]), action)
				return err
			}),
		})
	}
	market.AddCommand(&cobra.Command{
		Use: "search [query...]", Short: "Search cached catalogs; refresh a marketplace to fetch changes", Args: cobra.ArbitraryArgs,
		RunE: withStore(func(cmd *cobra.Command, args []string, store *plugin.Store) error {
			entries, err := store.Search(strings.Join(args, " "))
			if err != nil {
				return err
			}
			for i := range entries {
				entries[i].Source = plugin.DisplaySource(entries[i].Source)
			}
			if jsonOutput {
				return pluginJSON(cmd.OutOrStdout(), entries)
			}
			if len(entries) == 0 {
				_, err := fmt.Fprintln(cmd.OutOrStdout(), "No matching plugins. Add a marketplace or refresh its catalog.")
				return err
			}
			out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(out, "PLUGIN\tVERSION\tDESCRIPTION")
			for _, item := range entries {
				fmt.Fprintf(out, "%s@%s\t%s\t%s\n", pluginText(item.ID), pluginText(item.Market), pluginText(item.Version), pluginText(item.Description))
			}
			return out.Flush()
		}),
	})
	command.AddCommand(market)
	return command
}

func findInstalledPlugin(store *plugin.Store, id string) (plugin.Installed, error) {
	installed, err := store.List()
	if err != nil {
		return plugin.Installed{}, err
	}
	for _, item := range installed {
		if item.Manifest.ID == id {
			return item, nil
		}
	}
	return plugin.Installed{}, fmt.Errorf("plugin %q is not installed", id)
}

// runPluginCommand shares command parsing and behavior with the TUI without
// executing a shell, using the global Cobra command, or writing to the terminal.
func runPluginCommand(ctx context.Context, args []string) (string, error) {
	var out bytes.Buffer
	command := newPluginCmd()
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(args)
	err := command.ExecuteContext(ctx)
	return out.String(), err
}
