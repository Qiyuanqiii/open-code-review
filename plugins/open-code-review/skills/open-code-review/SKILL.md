---
name: open-code-review
description: >
  AI-powered code review using the `ocr` CLI. Reviews workspace changes,
  commits, or branch comparisons. Detects bugs, security issues,
  performance problems, and code quality concerns. Auto-detects whether
  to use OCR's built-in LLM (Mode A) or delegate to the host agent (Mode B).
license: Apache-2.0
compatibility: >
  Requires `ocr` CLI (npm i -g @alibaba-group/open-code-review).
metadata:
  author: alibaba
  homepage: https://github.com/alibaba/open-code-review
  version: "2.0.0"
---

# Open Code Review

## Prerequisites

```bash
which ocr || echo "NOT INSTALLED"
```

If missing: `npm install -g @alibaba-group/open-code-review`

## Mode Selection

```bash
ocr llm test
```

- **Success → Mode A** (OCR-owned LLM)
- **Failure → Mode B** (delegate to host agent, zero API key required)

---

## Mode A: OCR-Owned LLM

Run the review with OCR's configured LLM:

```bash
ocr review --audience agent --background "<business context>" [flags]
```

| User intent | Flags |
|---|---|
| Review working copy | _(none)_ |
| Review a commit | `--commit <ref>` |
| Compare branches | `--from <base> --to <head>` |
| Dry-run | `--preview` |

Parse the output, classify by priority (high/medium/low), report findings to the user, and optionally apply fixes.

---

## Mode B: Delegate to Host Agent

When no LLM is configured, OCR prepares a deterministic evidence bundle and returns a Markdown workflow for the host agent to follow.

### Step 1: Prepare

```bash
ocr delegate [--from <base> --to <head> | --commit <ref>] [--exclude PATTERNS]
```

This outputs a Markdown workflow containing:
- The bundle file path and bundle_id
- The review contract (allowed priorities, categories, schema)
- Instructions for producing, validating, and rendering the review

### Step 2: Follow the Workflow

The output is self-contained. Read it and follow its steps:

1. Read the bundle JSON file
2. Produce review comments conforming to `review-comments/v1`
3. Save comments to a file
4. Run `ocr delegate validate --bundle <path> --comments <path>`
5. If invalid, fix and re-validate
6. Run `ocr delegate report --bundle <path> --comments <path>`

The report renders as Markdown/text/JSON. Present findings to the user.

---

## Gotchas

- Always use `--audience agent` in Mode A to suppress progress UI
- Working directory matters — use `--repo <path>` to target another repo
- Large diffs may hit token limits; use `--preview` to check scope first
- In Mode B, the bundle is immutable — comments must reference paths and lines that exist in the bundle

## References

- Docs: https://github.com/alibaba/open-code-review
- NPM: https://www.npmjs.com/package/@alibaba-group/open-code-review
