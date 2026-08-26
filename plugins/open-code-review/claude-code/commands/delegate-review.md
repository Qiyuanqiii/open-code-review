---
description: Run OCR in delegation mode — OCR handles file selection and rules, the host agent performs the actual review.
---

Invoke OpenCodeReview (OCR) in delegation mode. OCR determines which files to review and provides review rules; you perform the actual code review using your own capabilities.

## Workflow

### Step 1: Preview

```bash
ocr delegate preview [user-args]
```

- Default (no user arguments): workspace mode (Git staged/unstaged/untracked or SVN versioned/unversioned changes).
- If the user provides `--commit` or `-c`: pass through as-is.
- If the user provides `--from` and `--to`: pass through as-is.
- If the user provides either remote SVN target, require and pass through both `--svn-from-target` and `--svn-to-target`.
- (Optional) Provide `--background "context"` or `-b "context"` for business context.
- If `ocr` is not found, install it: `npm i -g @alibaba-group/open-code-review`.
- SVN requests require an `svn` 1.7+ command-line client with non-interactive authentication and certificate trust already configured.

This outputs VCS/mode/ref metadata and the reviewable file list.

### Step 2: Get Rules

Pass all reviewable file paths to get their review checklists:

```bash
ocr delegate rule <path1> <path2> ...
```

### Step 3: Get Diffs and Review

For each reviewable file, get its diff using the VCS/mode metadata from Step 1:
- Git range: `git diff <merge_base>..<to> -- <path>`
- Git commit: `git show <commit> -- <path>`
- Git workspace: `git diff HEAD -- <path>` (or read directly for untracked files)
- SVN workspace: `svn diff --git --internal-diff --show-copies-as-adds -- <path>` (or read directly for unversioned files)
- SVN range/commit: read the selected `<url>` from `svn info --xml --non-interactive --depth empty -- .`, then use the frozen numeric endpoints and explicit URL pegs: `svn diff --non-interactive --git --internal-diff --show-copies-as-adds --old <url>@<resolved_base> --new <url>@<resolved_head>` and `svn cat --non-interactive --revision <resolved_head> -- <url>/<url-escaped-path>@<resolved_head>`
- Remote SVN path range: pass both `--svn-from-target` and `--svn-to-target`; keep the input URLs in runtime memory because preview JSON deliberately omits them, resolve their historical URLs with the reported operative/peg revisions, and compare those URLs with `--notice-ancestry`

Then review focusing on: correctness, security, performance, error handling, concurrency, maintainability. Only comment on changed code (+ lines).

### Step 4: Report and Fix

Classify each issue by severity:

- **High**: Obvious bugs, security issues, data loss, or clear mistakes with precise fix proposals
- **Medium**: Reasonable concerns, performance suggestions, or fixes requiring manual work
- **Low**: Discard silently (likely false positives, nitpicks, or insufficient context)

Automatically fix High and Medium issues that are safe and well-defined.
