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
GitHub branch/tag sources are resolved to a commit before download. If resolution
fails, including API quota errors, installation/update fails without changing the
registered package. Retry later or supply a `/tree/FULL_COMMIT_SHA` URL; a full
40-character commit ID can be downloaded directly without the API.

### Accept execution changes during updates

When an update adds or changes an MCP server configuration or a workflow's
protocol, command, arguments or stages, OCR reports the changed declaration and
field names and leaves the previous installation unchanged. Values such as
arguments, environment variables and authentication headers are omitted from the
change report. Inspect the new package source before accepting:

```text
ocr plugin update team-review
ocr plugin update team-review --accept-permissions
```

This also applies to disabled packages. Removing an execution contribution or
changing only rules/metadata does not require this flag. An unchanged executable
entrypoint can still contain updated code: acceptance checks declared execution
changes, and does not sandbox or establish the trustworthiness of new code.
Installation and update never run plugin commands.

With `--json`, this refusal writes an `error` object with code
`PLUGIN_EXECUTION_CHANGED`, `plugin_id`, and `changes` to stdout and exits nonzero.
Other command errors retain the existing stderr text contract.

### Recorded digests

New installations expose these fields in `show --json` and installation results:

| Field | Meaning |
| --- | --- |
| `archive_sha256` | SHA-256 of the exact downloaded/local archive bytes; absent for directory sources |
| `content_sha256` | SHA-256 of the prepared package's files, also checked after copying into the installed snapshot |
| `content_digest_format` | `ocr-package-tree-v1`, identifying the content-digest algorithm |
| `revision` | GitHub commit used to discover the package or published catalog |

The content digest walks files in lexical directory order, using forward-slash
relative paths. For each file it hashes `PATH_BYTE_LENGTH:PATH:EXEC_BITS:SIZE:`
(all numbers decimal), then the raw content bytes. `EXEC_BITS` is the permission
mask `mode & 0111`; directories do not contribute records. It describes the
prepared filesystem on that platform, not a portable cross-platform lock format.
Windows and Unix can expose different executable bits.

The legacy `sha256` field remains for existing consumers: archive digest for
archives, content digest for directories. Older state records remain readable;
OCR does not guess their digest kind or silently fill new fields. Updating such a
package records the explicit fields. Old OCR builds that reject the new state
fields cannot read the updated state; retain a backup before downgrading.

A direct archive digest records downloaded bytes. Marketplace-provided archive
digests are checked before installation. Neither is a publisher signature, and
these records alone do not select a project's plugin set. The project declaration
and lock described below provide that selection and frozen installation.

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
| `ocr plugin update ID` | Replace from the original source; preserve enabled state; reject changed execution declarations |
| `ocr plugin update ID --accept-permissions` | Explicitly accept added/changed execution declarations |
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

Without a project plugin declaration, enabled rules and MCP contributions apply
to every repository for that user.
`disable` stops automatic loading; an explicit `--review-plugin ID` selects the
workflow and its accompanying rules/MCP servers for that invocation, even when
the package is disabled. It does not change the saved enabled state. There is no
automatic project installation during reviews. See [Project plugins](#project-plugins)
for an explicit declaration and frozen installation workflow.

Marketplace updates are explicit:

```text
ocr plugin market refresh examples
ocr plugin update review-checklist
```

## Project plugins

Declare the plugin set in the repository when a team needs repeatable reviews.
All participants need an OCR build providing these project commands; earlier
builds do not read the project declaration.
Run these commands from the project root (or pass `--repo PROJECT_ROOT`):

```text
ocr plugin add https://github.com/example/team-review
ocr plugin project --json
ocr plugin sync --frozen
ocr plugin preflight --json
```

`add` resolves the package and writes `.opencodereview/plugins.json` and
`.opencodereview/plugins.lock.json`. Commit both files. `--plugin ID` selects a
package from a multi-plugin source. Existing global installation commands remain
available; `add` does not change globally installed or enabled plugins.

A declaration looks like this:

```json
{
  "schema_version": 1,
  "plugins": [
    {"id": "team-review", "source": "https://github.com/example/team-review"},
    {"id": "local-rules", "source": "./plugins/local-rules"}
  ]
}
```

Local sources must be inside the project and are stored as relative paths.
Remote sources use permanent HTTPS URLs without credentials, fragments, or query
parameters. A named marketplace selection is resolved into a source URL/path;
teammates do not need the same marketplace alias. The lock contains artifact
locations and digests, not the manifest's command arguments, environment values,
or authentication headers. Do not put secrets in committed local plugin files.

The lock ties the declaration to an ordered list of package identities, exact
archive URLs or local sources, package directories, and archive/content digests.
GitHub sources are resolved to a full commit's archive; a published catalog is
resolved to its selected release archive. Frozen sync fetches those archives
directly, without consulting a catalog or resolving a branch again. If bytes,
identity, or the declaration do not match, installation fails. It never runs
plugin programs or package managers.

Each `platforms` entry is keyed by OS/architecture, such as `windows/amd64` or
`linux/arm64`. The `ocr-package-tree-v1` digest includes executable bits and can
differ between platforms. Run `ocr plugin lock` on each required platform and
commit the resulting entries. Relocking an unchanged declaration preserves other
platform entries. Changing the declaration invalidates those entries so they
must be resolved again; an absent platform fails with `PLATFORM_UNSUPPORTED`.

### Frozen use and local authorization

When a project declaration exists, review, scan, and rule inspection load only
its locked plugins, in declaration order. An empty `plugins` array selects no
plugins. Missing or invalid project files cause an error instead of falling back
to global plugins. Existing project/user rule files and ordinary MCP configuration
retain their existing behavior; the declaration controls packaged plugins.

Installed project snapshots are kept under the user's plugin store, separately
for each canonical project path, lock, and platform. Review and preflight verify
their contents before use and perform no plugin downloads. To populate a fresh
checkout's snapshots, run `ocr plugin sync --frozen`. With `--offline`, sync uses
already verified snapshots or repository-local sources and makes no network
requests. A missing remote artifact fails with `ARTIFACT_MISSING`.

Data-only rule packages need no execution authorization. Any package declaring
MCP servers (including remote endpoints) or an active review workflow requires a
separate local decision after inspecting the locked package:

```text
ocr plugin sync --frozen
ocr plugin trust
ocr plugin preflight --json
ocr review --review-plugin team-review
ocr plugin trust --revoke
```

`trust` authorizes the current project lock's executable contributions. The record
is stored outside the repository and is bound to the canonical project path,
lock contents, and platform. It is not written by `add`, `lock`, or `sync`, and a
changed lock needs new authorization. Repositories cannot grant themselves trust
by committing an enabled flag. Trust permits native execution with the current
user's permissions; it is not a sandbox. Protect the local trust store and do not
restore it from a shared CI cache.

`ocr plugin preflight` checks locked snapshots, host compatibility, and execution
authorization without obtaining model credentials, starting MCP/workflow
processes, or calling a model. `--json` returns the installed package summaries.
`--fingerprint` instead returns a digest of the verified lock and platform;
with `--json`, the result is a JSON string. It repeats the same content and trust
checks, and excludes machine-local installation paths.
Project command operational errors return an `error` object with a code and
message on stdout and exit nonzero. Codes include `LOCK_MISMATCH`,
`PLATFORM_UNSUPPORTED`, `ARTIFACT_MISSING`, `ARTIFACT_MISMATCH`, and
`PLUGIN_EXEC_NOT_AUTHORIZED`; other failures use `PROJECT_PLUGIN_ERROR`.

### Update or remove a project dependency

Edit `plugins.json` to change a source, change order, or remove a dependency, then
resolve and review the new lock explicitly:

```text
ocr plugin lock
git diff -- .opencodereview/plugins.json .opencodereview/plugins.lock.json
ocr plugin sync --frozen
ocr plugin trust
```

`lock` refreshes all declared sources for the current platform. Use the global
`plugin update --accept-permissions` flow only for global installations; project
execution authorization is bound to the entire lock instead. Snapshots for old
locks remain available to in-progress reviews. A modified cached snapshot is
rejected; remove the project cache directory reported in the error (including
its `state.json` and `packages` directory) and sync again to recover. This cache
is outside the repository; keep the committed declaration and lock files.
No automatic pruning is included.

### GitHub Actions

The Action checks out the PR's base commit for rules and plugin policy. For
comment-triggered runs, it obtains the base SHA from the PR API. A workflow can
override this with `policy_sha`, a full trusted commit SHA. PR head files are
fetched for the diff; they do not supply the active plugin declaration or lock.

Set `project_plugins: 'true'` to install the committed lock with `sync --frozen`
before configuring model credentials. Pin an Action and CLI version containing
project-plugin support; older published CLI versions cannot run these commands.
The declaration, lock, project rules, custom `rule` file, and referenced rule
files must be committed at the policy SHA, inside its checkout, and unchanged
under Git's checkout/clean transformations. Package content digests still require
identical installed bytes; keep repository-local package line endings stable
(for example, use `eol=lf` in `.gitattributes`). The lock must include the runner's
OS/architecture.

Each plugin-enabled run uses a fresh private OCR home. Personal/global packages,
rules, configuration, and old trust records are not inherited. Missing artifacts,
changed content, unsupported packages/platforms, or invalid locks stop the job;
the Action does not replace a locked version with the latest available package.

`allow_plugin_exec` defaults to `'false'`. Set it to `'true'` in the trusted
workflow only after inspecting the locked MCP/workflow declarations. It grants
that lock execution permission for this run. Native plugins have the runner's
permissions and can access the review process's environment, including model
credentials. A lock file by itself never authorizes execution. See the
[Action example](../examples/github_actions/README.md#project-plugins-in-ci).

### Resume and incremental review

Session identity includes enabled plugin manifests, package contents (including
executable bits), rule text, and selected workflow. Changing these rejects a
resume and asks for a new review. Locked project snapshots must still pass
content and execution-authorization checks before a session can resume.

Action checkpoints include the trusted policy SHA, verified plugin lock/content
identity, and execution permission input. Changed policy or plugins force a full
review. A missing verified fingerprint prevents checkpoint reuse or advancement.
The CI plugin identity excludes temporary installation paths, so moving to a
fresh runner does not by itself invalidate a checkpoint.

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
model. ZIPs produced without Unix permission attributes may install scripts or
binaries without executable bits on Unix; prefer tar.gz produced on Unix for such
packages. Symlinks, hard links, sparse files and device files are rejected.

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

- `schema_version` must be `1`. Unknown operational fields and duplicate JSON
  keys fail validation, including executable setup/install hooks. See the
  compatibility rules below for descriptive metadata.
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

### Compatibility and host requirements

The package version is the author's release label. It is separate from manifest
`schema_version: 1` and workflow `stdio-v1` / message `version: 1`.

A manifest can optionally declare:

```json
"requires": {
  "ocr_min_version": "1.2.3",
  "features": ["rules", "mcp", "stdio-v1"]
},
"metadata": {
  "homepage": "https://example.com/team-review",
  "publisher_note": "Descriptive information only"
}
```

`ocr_min_version` is a minimum full semantic version, with optional `v` prefix,
prerelease and build metadata; it is not a version range. The number above is an
illustration, not the release that introduced plugins. Feature names currently
supported are `rules`, `mcp`, and `stdio-v1`. Unknown required features fail before
installation, activation or runtime loading. A `dev` or otherwise non-semantic
host version cannot satisfy a minimum-version declaration; build with an accurate
semantic version or use a versioned release. Manifests without requirements remain
supported. Installed incompatible packages can still be inspected or removed.

Only `metadata` is an open-ended descriptive object. Its contents never authorize
execution or change behavior. Other manifest objects, including `requires`, MCP
and workflow declarations, remain strict. Duplicate keys are rejected everywhere.
Old hosts may reject these new optional fields; omitting them retains the old
manifest shape. Older hosts must not silently ignore requirements they cannot
enforce.

Breaking manifest semantics require a new schema version. Incompatible workflow
messages require a new protocol identifier/message version. The current workflow
protocol remains experimental, with no new mandatory handshake in this release;
its v1 message shape remains unchanged. See the [workflow protocol](review-workflow-plugins.md).

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
