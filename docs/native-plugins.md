# Native OCR plugins

OCR can host plugins that contribute review rules and MCP tools through its
installer, registry, and runtime. These packages use `ocr-plugin.json` and are
separate from the integrations that install OCR into other coding agents. Packages
may also contribute custom review workflows (see [Review workflow plugins](review-workflow-plugins.md)).

For module boundaries, lifecycle diagrams, and a suggested code-review order,
see [Native plugin architecture](plugin-architecture.md).

## Supported Installation Sources & URL Input

A frequent question when getting started with OCR plugins is: **What sources are supported, and does input support URLs?**

**Yes, URL inputs are fully supported.** Both `ocr plugin install <source>` and `ocr plugin add <source>` accept remote URLs as well as local file paths.

### Supported Sources

OCR supports four primary package source types:

1. **Public GitHub Repositories (URLs)**
   - **Repository root**: `https://github.com/OWNER/REPO`
     Installs the package located at the root of the repository.
   - **Branch or Tag with optional subdirectory**: `https://github.com/OWNER/REPO/tree/REF/PATH`
     For example: `https://github.com/example/monorepo/tree/v1.2.0/plugins/security-rules`
   - **Commit SHA**: `https://github.com/OWNER/REPO/tree/FULL_40_CHAR_COMMIT_SHA/PATH`
     Directly targets an exact immutable revision.
   - *How it works*: When a branch or tag ref is supplied, OCR queries the GitHub API to resolve the ref to an exact 40-character commit SHA. If a 40-character commit SHA is already in the URL, API resolution is bypassed. OCR then streams the repository snapshot tar.gz from `codeload.github.com` into an isolated preparation directory and extracts the target plugin directory.

2. **Remote HTTPS Archives (URLs)**
   - **HTTPS tar.gz archives**: `https://example.com/team-review.tar.gz`
   - **HTTPS ZIP archives**: `https://example.com/team-review.zip`
   - Release assets (e.g. from GitHub Releases or GitLab) containing pre-built binaries or packaged rules: `https://github.com/OWNER/REPO/releases/download/v1.0.0/plugin.tar.gz`
   - *How it works*: OCR downloads the archive via HTTPS, inspects magic bytes (gzip `0x1f 0x8b` or ZIP `PK`) so a file extension is not strictly required, verifies the download against size limits, and extracts the content into an isolated sandbox for validation.

3. **Local Directories (Paths)**
   - Relative or absolute directory containing `ocr-plugin.json` at its root:
     ```text
     ocr plugin install ./examples/ocr-plugins/review-checklist
     ocr plugin install /opt/company-plugins/code-style
     ```
   - OCR validates the manifest and directory contents, then copies a snapshot into the local OCR plugin store.

4. **Local Archives (Paths)**
   - Local `.tar.gz` or `.zip` files:
     ```text
     ocr plugin install ./team-review.tar.gz
     ocr plugin install ./team-review.zip
     ```
   - Unpacks into an isolated directory, validates structure, and stores a snapshot.

### Disallowed & Unsupported Sources

To preserve deterministic execution, reproducible project locks, and system security, the following are **not** supported:

- **Plain HTTP URLs (`http://`)**: Enforced HTTPS only. Insecure connections are rejected. HTTPS redirects that downgrade to HTTP are rejected.
- **URLs with embedded user credentials or fragments**: URLs with userinfo (e.g. `https://user:password@example.com/...`) or URL fragments (`#...`) are rejected.
- **Legacy Marketplace Catalogs & JSON Index URLs**: Catalog search and catalog-based index files (`ID@market`) have been removed in favor of direct package installation.
- **Bare Git SSH URLs (`git@github.com:...`)**: Direct SSH git cloning is not supported. Use public HTTPS URLs or download the archive asset.
- **Symlinks, hard links, device files, and sockets**: Archives or local directories containing symlinks, hard links, or special device files are strictly rejected during extraction and copying to prevent filesystem traversal and privilege escalation.
- **File paths containing `@`**: Bare inputs with `@` are rejected to prevent confusion with legacy marketplace IDs. If a local file has `@` in its name, specify an explicit relative or absolute path (e.g. `./plugin@1.0.tar.gz`).

### Archive & Resource Limits

To safeguard against decompression bombs and corrupted assets:
- **Manifest size limit**: 1 MiB (`ocr-plugin.json`).
- **Archive download limit**: 32 MiB.
- **Expanded content limit**: 64 MiB total unpacked package content.
- **Decompressed stream limit (tar.gz)**: 72 MiB (including gzip headers, tar headers, padding, and metadata).
- **Package entry count limit**: 4,096 files maximum per package.
- **Project configuration limit**: 4 MiB (`plugins.json` and `plugins.lock.json`).

---

## CLI Subcommand Reference

OCR provides a complete set of subcommands under `ocr plugin` for managing both user-global plugins and project-level plugin dependencies, as well as `ocr mcp list` for inspecting tools.

```text
ocr plugin <command> [flags]
```

### Global Plugin Subcommands

Global plugins are installed under `~/.opencodereview/plugins`. When enabled, their review rules and MCP tools automatically apply to every review and scan run across all repositories for the current user (unless a project-specific plugin declaration is present).

#### 1. `ocr plugin install <url|path>`
Downloads and validates a package from a URL or local file path, creates an isolated snapshot, and registers it.

- **Arguments**: `<url|path>` (GitHub URL, HTTPS tar.gz/ZIP URL, local directory, or local archive).
- **Flags**:
  - `--enable`: Automatically enable the plugin immediately after installation.
  - `--dry-run`: Download/inspect and print package details without saving or enabling the package.
  - `--plugin <id>`: Disambiguate and select a specific plugin when an archive or repository contains multiple plugins.
  - `--json`: Output structured machine-readable JSON.
- **Behavior**: Installation only copies files and checks the manifest schema; it **never** executes package code, runs install scripts, builds binaries, or contacts language runtime package managers.
- **Examples**:
  ```sh
  # Dry-run preview of a GitHub plugin
  ocr plugin install https://github.com/example/review-rules --dry-run

  # Install and enable an HTTPS tar.gz plugin
  ocr plugin install https://example.com/team-review.tar.gz --enable

  # Install from a local directory
  ocr plugin install ./examples/ocr-plugins/project-notes
  ```

#### 2. `ocr plugin list`
Lists all globally registered plugins, their installed version, source location, and enabled/disabled state.

- **Flags**:
  - `--json`: Output as a JSON array of plugin records.
- **Example output**:
  ```text
  team-review      1.0.0  [enabled]   https://example.com/team-review.tar.gz
  project-notes    1.2.0  [disabled]  ./examples/ocr-plugins/project-notes
  ```

#### 3. `ocr plugin show <id>`
Inspects an installed plugin's manifest, snapshot directory, contributed rules, MCP servers, and compatibility requirements.

- **Arguments**: `<id>` (Plugin ID).
- **Flags**:
  - `--json`: Output full structured JSON including `mcp_execution` declarations.
- **Security & Redaction**: To prevent credential leakage, command arguments, environment variables, authentication headers, and URL query parameters are redacted from management displays.

#### 4. `ocr plugin update <id>`
Refreshes an installed plugin from its original recorded source, keeping its current enabled/disabled state intact.

- **Arguments**: `<id>` (Plugin ID).
- **Flags**:
  - `--accept-permissions`: Explicitly accept added or modified MCP execution declarations.
  - `--json`: Output JSON result or structured error if permissions changed.
- **Security Gate**: If the updated package introduces new MCP servers or alters existing server commands, arguments, environment variables, or transport configurations, OCR refuses to apply the update and outputs an error (`PLUGIN_EXECUTION_CHANGED`). The user must review the changes and run:
  ```sh
  ocr plugin update <id> --accept-permissions
  ```

#### 5. `ocr plugin enable <id>`
Activates a registered plugin. Its review rules will be evaluated and its declared MCP tools will become available in subsequent `ocr review` and `ocr scan` runs.

#### 6. `ocr plugin disable <id>`
Deactivates a plugin. The plugin remains installed in the store, but its rules and MCP servers will not be loaded during reviews.

#### 7. `ocr plugin remove <id>`
Unregisters and removes a plugin from the registry. Existing review sessions that are currently in progress continue running against their existing snapshots.

---

### Project-Level Plugin Subcommands

When working in a team or CI/CD environment, reproducible reviews require locking exact plugin versions and ensuring transparent execution authorization. Project plugins are declared in `.opencodereview/plugins.json` and locked in `.opencodereview/plugins.lock.json` at the repository root.

When a repository contains project plugin files, OCR runs in **Project Plugin mode**: only the locked project plugins are loaded, completely ignoring global plugins.

#### 1. `ocr plugin add <source>`
Declares a plugin dependency for the current project. Resolves the source, updates `.opencodereview/plugins.json`, and records the exact platform artifact in `.opencodereview/plugins.lock.json`.

- **Flags**:
  - `--plugin <id>`: Select a plugin ID if the source contains multiple plugins.
  - `--repo <path>`: Path to project root (default: current directory).
- **Example**:
  ```sh
  ocr plugin add https://github.com/example/team-review
  git add .opencodereview/plugins.json .opencodereview/plugins.lock.json
  ```

#### 2. `ocr plugin lock`
Resolves all dependencies declared in `.opencodereview/plugins.json` for the current operating system and architecture (`GOOS/GOARCH`), generating or updating entries in `.opencodereview/plugins.lock.json`.

- **Flags**:
  - `--repo <path>`: Project root directory.
- Commit the updated lockfile so that teammates and CI runners use identical artifacts.

#### 3. `ocr plugin sync`
Installs the exact package snapshots specified in `.opencodereview/plugins.lock.json`.

- **Flags**:
  - `--frozen`: **Mandatory**. Ensures the committed declaration and lockfile match exactly without resolving new versions.
  - `--offline`: Use only locally cached snapshots or repository-internal sources without network requests.
  - `--repo <path>`: Project root directory.
- **Safety**: Sync installs snapshots into a project-isolated store. It **never** executes package code or authorizes execution.

#### 4. `ocr plugin trust`
Grants explicit local execution authorization for the current project's locked MCP server executables on this machine.

- **Flags**:
  - `--revoke`: Revokes local execution trust for the current lock.
  - `--repo <path>`: Project root directory.
- **Trust Boundary**: Trust is stored outside the repository in `~/.opencodereview/trust` and is cryptographically bound to the canonical repository path, exact lockfile SHA, and host platform. A repository cannot grant itself trust by committing configuration. Data-only rule plugins do not require execution authorization.

#### 5. `ocr plugin project`
Inspects the project declaration, lockfile contents, platform entries, and local authorization status without downloading packages or modifying trust.

- **Flags**:
  - `--repo <path>`: Project root directory.
  - `--json`: Output as structured JSON.

#### 6. `ocr plugin preflight`
Performs verification before starting a review or in CI workflows. Verifies that all locked packages are installed, checksums match, host version requirements are met, and execution trust is granted.

- **Flags**:
  - `--fingerprint`: Computes and prints a deterministic fingerprint hash of the verified lock and platform. Used for incremental review caching and session reuse.
  - `--repo <path>`: Project root directory.
  - `--json`: Output verification results as JSON.

---

### MCP Inspection Subcommand

#### `ocr mcp list`
Provides a unified, read-only view of all configured MCP servers across user configuration (`~/.opencodereview/config.json`), globally enabled plugins, and project-locked plugins.

```sh
ocr mcp list
ocr mcp list --repo /path/to/project --json
```

- **Output Fields**: Server name (namespaced as `plugin/PLUGIN_ID/SERVER`), source (`user_config`, `global_plugin`, `project_plugin`), transport type (`stdio`, `remote`), configuration state, and authorization status.
- **Safety**: `ocr mcp list` reads only metadata and manifests. It **never** runs setup hooks, starts server processes, dials network endpoints, or accesses model provider keys.

| State | Meaning |
| --- | --- |
| `configured` | Server declaration is selected and authorized; ready for review execution |
| `disabled` | Globally installed plugin is disabled |
| `incompatible` | Plugin's host version requirements are unmet |
| `authorization_required` | Project snapshot is installed, but `ocr plugin trust` has not been granted |
| `server_name_conflict` | Plugin server name collides with a manual server name |

---

## Typical Workflows

### End-to-End Global Plugin Workflow

```sh
# 1. Preview before installing
ocr plugin install https://github.com/example/team-rules --dry-run

# 2. Install and enable
ocr plugin install https://github.com/example/team-rules --enable

# 3. Check installed capabilities
ocr plugin show team-rules
ocr mcp list

# 4. Run review (rules and MCP tools are automatically active)
ocr review

# 5. Later: update plugin with permission review
ocr plugin update team-rules --accept-permissions
```

### End-to-End Project Plugin & CI Workflow

```sh
# 1. Add dependency and commit lockfile
ocr plugin add https://github.com/example/security-pack
git add .opencodereview/
git commit -m "chore: add security-pack plugin"

# 2. Teammate or fresh checkout: sync exact frozen lock
ocr plugin sync --frozen

# 3. Inspect package and grant local execution trust (for MCP tools)
ocr plugin project
ocr plugin trust

# 4. Verify preflight
ocr plugin preflight

# 5. Run review
ocr review
```

---

## Authoring a Package

Place `ocr-plugin.json` at the root of a directory or tar.gz archive. Existing ZIP packages remain supported.

```text
my-plugin/
├── ocr-plugin.json
├── rules.json
├── checks.md
└── server.mjs
```

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
  },
  "review": {
    "protocol": "stdio-v1",
    "command": "python",
    "args": ["${plugin_dir}/review.py"],
    "stages": ["inspect", "verify"]
  },
  "requires": {
    "ocr_min_version": "1.2.0",
    "features": ["rules", "mcp", "stdio-v1"]
  },
  "metadata": {
    "homepage": "https://example.com/team-review"
  }
}
```

### Manifest Specifications

- `schema_version`: Must be `1`. Unknown operational fields fail validation.
- `id`: Unique lowercase identifier containing letters, digits, dots, underscores, or hyphens (up to 64 chars). Windows reserved device names (`CON`, `PRN`, `AUX`, `NUL`, `COM1`, etc.) are rejected.
- `name` & `version`: Required non-empty strings. Use semantic versioning for releases.
- `rules`: Array of relative paths to rule files (`rules.json` or markdown files referenced within rules).
- `mcp_servers`: Map of MCP server configurations.
  - `stdio`: Supports `command`, `args`, `env` (`KEY=VALUE`), and `tools` allowlist.
  - `remote`: Supports `url` (HTTPS), `headers`, and `tools`.
- `review`: Optional review workflow declaration. See [Review workflow plugins](review-workflow-plugins.md).
- Placeholders supported in `command`, `args`, and `env`:
  - `${plugin_dir}`: Absolute path to the installed package snapshot directory.
  - `${os}`: Operating system name (`linux`, `darwin`, `windows`).
  - `${arch}`: Architecture name (`amd64`, `arm64`).
  - `${exe}`: `.exe` on Windows; empty string on Unix.

### Packaging for Distribution

When publishing a plugin containing executable scripts or binaries on Linux/macOS, set executable permissions before archiving (`chmod +x bin/server`). OCR preserves regular file executable bits during tar.gz extraction:

```sh
tar -czf team-review-1.0.0.tar.gz -C my-plugin .
```

---

## Security Model & Integrity Guarantees

1. **No Code Execution on Installation**: `install`, `sync`, `list`, `show`, `dry-run`, `lock`, and `preflight` do not run plugin scripts or package commands.
2. **Deterministic Digests**:
   - `archive_sha256`: SHA-256 of the exact downloaded or local archive file.
   - `content_sha256`: SHA-256 computed using `ocr-package-tree-v1` across all files in lexical order, hashing path length, relative path, executable bit mask, size, and content bytes.
3. **Execution Change Gates**: Updating a global plugin that changes or adds executable MCP declarations requires explicit confirmation via `--accept-permissions`.
4. **Project Trust Isolation**: Project execution authorization is stored outside the repository and cannot be committed. Repositories cannot execute arbitrary code on clean developer clones without explicit `ocr plugin trust`.
5. **CI Base Policy Protection**: In GitHub Actions, OCR loads plugin declarations and locks from the trusted base commit of a pull request, never from an untrusted PR head branch.
