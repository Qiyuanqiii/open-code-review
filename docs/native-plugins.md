# Native OCR plugins and marketplaces

OCR can host plugins that contribute review rules, MCP tools and an explicitly
selected review workflow through its installer, registry, and runtime. These
packages use `ocr-plugin.json` and are
separate from the integrations that install OCR into other coding agents.

[Review workflow plugins](review-workflow-plugins.md) provide their own stage plan,
prompts and validators through a local process. Start one with
`ocr review --review-plugin ID`. Selection affects that
invocation only.

## Install from a link

Install a public GitHub plugin repository or an HTTPS tar.gz package directly. No
marketplace registration is required:

```text
ocr plugin install https://github.com/Qiyuanqiii/ocr-behavior-review
ocr plugin install https://example.com/team-review.tar.gz --enable
```

For a GitHub repository, OCR discovers its plugin package or published catalog.
A catalog can point to release archives containing executable builds that are not
present in the source tree. Installation downloads and inspects the package; it
does not execute plugin commands or install dependencies.

Preview a source without installing or enabling anything:

```text
ocr plugin install https://example.com/team-review.tar.gz --dry-run
ocr plugin install https://example.com/ocr-marketplace.json --dry-run --json
```

A catalog containing one plugin selects it automatically. If it contains
multiple plugins, the command lists them and asks for an explicit selection:

```text
ocr plugin install https://example.com/ocr-marketplace.json --plugin team-review --enable
```

Direct installations remember their original source and selected plugin.
`ocr plugin update ID` checks that source again and preserves the enabled state.
The stored metadata includes the downloaded archive's SHA-256 and the resolved
GitHub revision when available. A direct archive's recorded digest identifies its
downloaded bytes; marketplace-provided digests are also checked against the
download before installation.

## Browse the bundled marketplace

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

## CLI

| Command | Behavior |
| --- | --- |
| `ocr plugin list` | List installed packages and enabled state |
| `ocr plugin show ID` | Show source, package location, and contributed capabilities |
| `ocr plugin install URL` | Install an HTTPS tar.gz/ZIP, public GitHub plugin repository, or single-plugin catalog |
| `ocr plugin install URL --plugin ID` | Choose a plugin from a source containing multiple packages |
| `ocr plugin install SOURCE --dry-run` | Inspect packages without installing or enabling them |
| `ocr plugin install PATH` | Copy a local directory or tar.gz/ZIP package |
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

Place `ocr-plugin.json` at the root of a directory or tar.gz archive. Existing ZIP
packages remain supported. Archive formats are detected from their contents,
so download URLs do not need a filename extension. GitHub repository sources
use tar.gz archives.

Package the bundled checklist example from the repository root:

```sh
tar -czf review-checklist.tar.gz -C examples/ocr-plugins/review-checklist .
ocr plugin install ./review-checklist.tar.gz --dry-run
ocr plugin install ./review-checklist.tar.gz
```

When publishing a program or script, set its executable bit before packaging
on Linux/macOS (for example, `chmod +x bin/review`). OCR preserves executable
bits from regular files through extraction and installation. Package files
receive owner read/write permissions plus their archived executable bits;
directories remain private (`0700`). Ownership, setuid/setgid bits, and other
archived permissions are not restored. Windows uses its native permission
model. Symlinks, hard links, sparse files and device files are rejected.

Example manifest:

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
tar.gz or ZIP archives. A catalog archive must contain `ocr-plugin.json` directly at its root, rather
than wrapping the package in another directory.

For an HTTPS catalog, each entry must reference an HTTPS tar.gz or ZIP (an HTTPS-relative
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

Limits are 1 MiB for a manifest, 4 MiB for a catalog, 32 MiB for an archive download,
64 MiB expanded package content, and 4096 package entries. For tar.gz, the
decompressed stream also has a 72 MiB limit including headers, padding and
metadata. Gzip checksums are verified before installation. Rule packs and their
referenced text have additional runtime limits. Failed downloads, malformed
packages, or digest mismatches leave the previous registered installation in
place. A store lock serializes modifications from separate CLI processes;
a lock left by a crashed process is reported rather than silently removed.
