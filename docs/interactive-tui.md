# Interactive review workspace

Run `ocr tui` from a Git repository, or select a repository explicitly:

```sh
ocr tui --repo /path/to/repository
ocr tui --from main --to feature
ocr tui --commit HEAD
```

The workspace requires an interactive terminal. Provider and model settings use
the same configuration as `ocr review`. Use `/provider` or `/model` to open the
configuration screen. Credential commands used by reviews must work without
interactive stdin.

Use `/login` to open the existing ChatGPT browser sign-in flow. When it finishes,
use `/provider` to configure ChatGPT and a model available to your account.
Once configured, `/provider chatgpt` selects it for the next review. Login keeps
the current provider, model, and review scope. If plan usage is disabled, the workspace shows
the CLI command for granting permission; `/login` does not request that permission
automatically.

## Review changes

Type a description to set the review background, then use these commands:

| Command | Action |
| --- | --- |
| `/scope` | Choose the working tree, a revision range, or a commit |
| `/preview` | Inspect selected and excluded files |
| `/review` | Start a review of the current scope |
| `/logs` | Show recent review progress |
| `/sessions` | Browse saved runs and their findings |
| `/login` | Sign in with ChatGPT |
| `/settings` | Change and save the interface language |
| `/help` | Show available commands |
| `/quit` | Close the workspace |

Use the arrow keys and Enter to open a finding or saved session. Esc returns to
the previous screen. During a review, Esc requests cancellation and waits for
the child process to save its available results. PgUp and PgDn scroll the view.

The interface supports English, Simplified Chinese, Japanese, Korean, and
Russian. This setting controls interface text independently of review output.

## Plugins and marketplaces

The workspace uses the same [plugin store](native-plugins.md) as the CLI.

### Install from a link

1. Open `/plugins`, select **Install from link**, and press Enter.
2. Paste an HTTPS ZIP, public GitHub plugin repository, or marketplace JSON URL.
3. Press Enter to download and preview the package. If the source contains
   multiple plugins, choose one to preview.
4. Inspect the name, version, source, digest, and contributed capabilities.
   Choose **Install** to keep it disabled, or **Install and enable** to activate
   its rules and MCP tools for subsequent reviews.

For example, paste `https://github.com/Qiyuanqiii/ocr-behavior-review`.
You can also open its preview directly:

```text
/plugin install https://github.com/Qiyuanqiii/ocr-behavior-review
```

No marketplace registration is required. Confirmation installs the package
already downloaded for the preview. Esc cancels a download or leaves a preview
without installing it; temporary preview files are removed. PgUp and PgDn scroll
long package details.

Use `/plugin update behavior-review` to fetch the original source again while
preserving the selected plugin and its enabled state. If the plugin is already
installed, use update instead of installing it a second time.

### Browse a marketplace

Add a marketplace when you want to browse and search its catalog:

```text
/plugin market add examples ./examples/ocr-plugins
/market
/plugin install review-checklist@examples --enable
/plugins
```

Enter opens the selected package's details. `/plugin install` installs the
selected marketplace entry; `/plugin enable` and `/plugin disable` operate on
the selected installed package. Explicit package IDs also work.

Use `/preview PACKAGE_ID` or `/review PACKAGE_ID` to select an installed
[review workflow](review-workflow-plugins.md) for that invocation. The following
plain `/review` uses the ordinary workflow. Workflow plugins require a fresh run.
For Behavior Review, use `/preview behavior-review` to inspect the scope, then
`/review behavior-review` to run its workflow. Installing or enabling a package
does not automatically select its review workflow.
