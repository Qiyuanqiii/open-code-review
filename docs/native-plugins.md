# Native OCR plugins and marketplaces

OCR can host plugins that contribute review rules, MCP tools and an explicitly
selected review policy. The CLI and interactive TUI share the same
installer, registry, and runtime. These packages use `ocr-plugin.json` and are
separate from the integrations that install OCR into other coding agents.

[Review policy plugins](behavior-review-plugins.md) run through a host-owned
three-stage engine. Start one with `ocr review --review-plugin ID` or TUI
`/review ID`. Selecting a policy affects that invocation only.

## Try the bundled marketplace

From this checkout:

```text
ocr plugin market add examples ./examples/ocr-plugins
ocr plugin market search
ocr plugin install review-checklist@examples
ocr plugin show review-checklist
ocr plugin enable review-checklist
ocr rules check src/main.go
```

The checklist is appended to matching review rules. The existing project,
user, and built-in rules keep their priority. Enable the tool example with:

```text
ocr plugin install project-notes@examples --enable
```

The Project Notes example requires `node` on PATH. During a review or scan, its
`project_review_notes` tool can read `REVIEW_NOTES.md` from the reviewed
repository. It accepts no arguments, reads at most 64 KiB, and refuses a notes
file resolving outside that repository. The example supports the MCP
2025-11-25 initialization handshake and
[stdio transport](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports).

Installation copies files and validates the manifest; it does not run package
code or install language runtimes. Packages start disabled unless `--enable`
is supplied. Enabling allows the review model to use the declared MCP tools,
including executing their server commands with your account's permissions.

## CLI and TUI

| Command | Behavior |
| --- | --- |
| `ocr plugin list` | List installed packages and enabled state |
| `ocr plugin show ID` | Show source, package location, and contributed capabilities |
| `ocr plugin install PATH` | Copy a local directory or ZIP package |
| `ocr plugin install ID@MARKET` | Install the selected marketplace entry |
| `ocr plugin enable ID` / `disable ID` | Change rule and MCP contributions for subsequent reviews |
| `ocr plugin update ID` | Replace the snapshot from its original source; preserve enabled state |
| `ocr plugin remove ID` | Unregister a package |
| `ocr plugin market add NAME SOURCE` | Register a local or HTTPS catalog and cache it |
| `ocr plugin market list` | List configured catalogs |
| `ocr plugin market search [QUERY...]` | Search cached catalogs without network access |
| `ocr plugin market refresh NAME` | Fetch and validate the catalog again |
| `ocr plugin market remove NAME` | Unregister a catalog; installed plugins remain |

All commands support `--json`. Management output omits MCP environment values,
arguments, authentication headers, and source URL query parameters. Installing a
package whose ID is already registered fails; use `update` to explicitly replace
it.

In `ocr tui`, use `/plugins` for installed packages and `/market` to browse
marketplace entries. Use `/plugin ...` for the corresponding CLI actions, for
example `/plugin market add examples "D:/my project/examples/ocr-plugins"`.
Quoted paths are parsed as arguments and never passed to a shell.

The store lives in `~/.opencodereview/plugins`, independently of model and MCP
configuration in `config.json`. Each installation creates a copied package
snapshot. Editing the source directory takes effect only after `update`.
Older snapshots remain on disk after update/removal so an active review can
finish using its original files. This version has no automatic cache cleanup.

Marketplace updates are explicit:

```text
ocr plugin market refresh examples
ocr plugin update review-checklist
```

## Author a package

Place `ocr-plugin.json` at the root of a directory or ZIP archive:

```json
{
  "schema_version": 1,
  "id": "team-review",
  "name": "Team Review",
  "version": "1.0.0",
  "description": "Team-specific checks and repository context tools.",
  "rules": ["rules.json"],
  "mcp_servers": {
    "context": {
      "type": "stdio",
      "command": "node",
      "args": ["${plugin_dir}/server.mjs"],
      "tools": ["team_repository_context"]
    }
  }
}
```

- `schema_version` must be `1`. Unknown fields and duplicate JSON keys fail
  validation, including executable setup/install hooks.
- `id` is a portable lowercase identifier containing letters, digits, dots,
  underscores, or hyphens, with an alphanumeric first and last character, at
  most 64 characters. Windows reserved filenames are rejected.
- `name` and `version` are required nonempty strings. Change the version when
  publishing a different release. At least one rule pack, MCP server or review
  policy is required.
- `rules` contains relative `.json` paths. All packaged files must be regular
  files or directories. Symlinks, traversal, absolute paths, conflicting names,
  and special files are rejected.
- `mcp_servers` uses named server configurations. `stdio` (the default) supports
  `command`, `args`, `env` as `KEY=value` strings, and a `tools` allowlist. Remote
  servers use `type: "remote"`, `url`, `headers`, and optional `tools`.
- `${plugin_dir}` expands literally in stdio command, arguments, and environment
  values to the copied installation directory. Commands run with the reviewed
  repository as their working directory. OCR does not evaluate a shell.

MCP tool names must be unique across the active tools. User-configured MCP tools
are registered before plugin tools; conflicts with built-in or already
registered tools are skipped with a warning. Server labels are namespaced as
`plugin/PLUGIN_ID/SERVER_NAME`. Startup failures follow the existing MCP policy:
OCR reports the failure and continues without that server's tools.

### Rule packs

```json
{
  "rules": [
    {
      "path": "**/*.go",
      "rule": "Check that failures preserve the original error cause."
    }
  ]
}
```

A rule can also refer to a packaged Markdown file using the existing relative
rule-file notation, such as `"rule": "./checks.md"`. References must stay inside
the installed package. Each pack uses the first matching entry; matching packs
are appended in plugin-ID order and manifest file order. Plugin packs cannot
change file selection through `include` or `exclude`. Use project/user rules
for those filters.

`ocr rules check` and `ocr delegate rule` show the effective augmented rules.
Previews, rule inspection, and delegated review do not start plugin MCP
processes. Normal review and scan capture the enabled plugins with their rule
resolver, then start the same captured tool configuration. Rule text, manifests,
and package snapshot identity participate in the existing resume rule hash,
so incompatible changes reject reuse of an old checkpoint.

Version 1 provides rule and MCP-tool contributions. The VCS interfaces remain
internal Go interfaces; there is no external VCS plugin protocol, arbitrary TUI
component loading, or review lifecycle hook in this manifest version. Additional
contribution types require an explicit host contract before packages can use
them.

## Publish a marketplace

A local directory uses `ocr-marketplace.json`, or pass an explicit JSON path:

```json
{
  "schema_version": 1,
  "name": "Team Plugins",
  "plugins": [
    {
      "id": "team-review",
      "name": "Team Review",
      "version": "1.0.0",
      "description": "Team review rules and context tools.",
      "source": "./team-review"
    }
  ]
}
```

Local sources are resolved relative to the catalog. They can be directories or
ZIP archives. A ZIP must contain `ocr-plugin.json` directly at its root, rather
than wrapping the package in another directory.

For an HTTPS catalog, each entry must reference an HTTPS ZIP (an HTTPS-relative
URL is allowed) and include `sha256`, the 64-character hexadecimal digest of
the archive bytes. OCR verifies the digest and the manifest ID/version before
committing installation. HTTPS redirects must remain HTTPS. Credentials in URL
userinfo and fragments are rejected. A catalog digest detects mismatched
downloads; the catalog source itself must still be trusted.

```text
ocr plugin market add team https://example.com/ocr-marketplace.json
ocr plugin market search
ocr plugin install team-review@team
```

Limits are 1 MiB for a manifest, 4 MiB for a catalog, 32 MiB for a ZIP download,
64 MiB expanded package content, and 4096 package entries. Rule packs and their
referenced text have additional runtime limits. Failed downloads, malformed
packages, or digest mismatches leave the previous registered installation in
place. A store lock serializes modifications from separate CLI/TUI processes;
a lock left by a crashed process is reported rather than silently removed.
