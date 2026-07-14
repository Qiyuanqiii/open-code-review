---
description: Run OpenCodeReview (OCR) to review code changes and autonomously apply fixes.
---

Invoke the professional code review Agent CLI tool OpenCodeReview (OCR) to review current code changes, and let the Agent autonomously decide whether to apply fixes.

## Mode Detection

```bash
ocr llm test
```

- **Exit 0 → Mode A** (OCR has its own LLM configured)
- **Non-zero → Mode B** (delegate to host agent)

---

## Mode A: OCR-Owned LLM

### Step 1: Run Code Review

```bash
ocr review --audience agent --background "<business context>" [user-args]
```

- Default (no user arguments): reviews staged, unstaged, and untracked changes (workspace mode).
- If the user provides `--commit` or `-c`: pass through as-is.
- If the user provides `--from` and `--to`: pass through as-is.
- (Optional) Provide `--background "requirement context"` to review whether the requirements are correctly implemented.
- Capture full stdout. Set a 5-minute timeout.
- If the `ocr` command is not found, install it by running `npm i -g @alibaba-group/open-code-review`.

### Step 2: Filter and Evaluate

For each comment, assess its validity and quality:

- **High**: Obvious bugs, security issues, clear mistakes, or well-founded suggestions with precise fix proposals
- **Medium**: Reasonable concerns but context-dependent, style/performance suggestions, or fixes that require manual implementation
- **Low**: Likely false positives, lacking sufficient context, nitpicks, or meaningless suggestions

Silently discard low-confidence comments. Display the remaining comments.

### Step 3: Fix

Automatically fix issues and suggestions that are worth adopting.

---

## Mode B: Delegate to Host Agent

When `ocr llm test` fails (no LLM configured), use OCR's delegate mode.

### Step 1: Prepare Bundle

```bash
ocr delegate [--from <base> --to <head> | --commit <ref>] [--exclude PATTERNS]
```

This outputs a self-contained Markdown workflow. Read and follow it:

1. Read the bundle JSON file referenced in the workflow
2. Review the code changes and produce comments conforming to `review-comments/v1`
3. Save comments to the path suggested in the workflow
4. Validate: `ocr delegate validate --bundle <path> --comments <path>`
5. If invalid, fix errors and re-validate
6. Generate report: `ocr delegate report --bundle <path> --comments <path>`

### Step 2: Report and Fix

Present the report findings to the user, then apply fixes for high/medium priority items.
