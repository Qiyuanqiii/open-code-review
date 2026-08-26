---
title: Delegation Mode
sidebar:
  order: 5
---

OCR handles deterministic engineering (file selection, rule resolution)
while the host agent performs the actual code review using its own LLM
capabilities. No LLM endpoint is required on the OCR side.

## When to use delegation mode

Delegation mode is designed for subscription-based AI coding agents —
such as Claude Code, Codex, Cursor, Open Code, Qoder, etc. — where you
already have an LLM subscription bundled with the host agent. Instead
of configuring a separate model endpoint for OCR, you reuse the host
agent's existing subscription quota to perform the review.

Use delegation mode when:

1. Your AI coding agent runs on a subscription plan and you want to
   reuse that quota for code review — no extra API key or model
   configuration needed.
2. You want OCR only for its engineering scaffolding — file filtering,
   rule resolution, exclusion logic — while the host agent handles all
   LLM reasoning.
3. You're building a custom agent pipeline that needs structured inputs
   (file list + rules) for its own review step.

## Prerequisites

The `ocr` CLI must be installed:

```bash
which ocr || npm install -g @alibaba-group/open-code-review
```

No LLM configuration (`ocr config set …` or environment variables) is
needed — delegation mode never calls an LLM on the OCR side.

## Install the skill / command

### Claude Code — Command

```bash
mkdir -p .claude/commands
curl -o .claude/commands/delegate-review.md \
  https://raw.githubusercontent.com/alibaba/open-code-review/main/plugins/open-code-review/claude-code/commands/delegate-review.md
```

### Any agent — Skill

```bash
npx skills add alibaba/open-code-review --skill open-code-review-delegate
```

Or copy the manifest manually:

```bash
cp -R /path/to/open-code-review/skills/open-code-review-delegate ~/.claude/skills/
```

## Workflow

### Step 1: Preview — determine what to review

```bash
ocr delegate preview [--from <ref> --to <ref>] [--commit <hash>] [--exclude <patterns>]
```

Outputs:

- **vcs** — git / svn
- **mode** — workspace / range / commit
- **ref metadata** — from, to, commit, merge\_base, and safe resolved SVN operative/peg revisions
- **Reviewable file list** — paths, status, insertions/deletions
- **Excluded files** — with exclusion reason

Common invocations:

| Scenario | Command |
|----------|---------|
| Workspace changes | `ocr delegate preview` |
| Branch comparison | `ocr delegate preview --from main --to feature` |
| Remote SVN paths | `ocr delegate preview --from 120 --to 128 --svn-from-target <old-url@peg> --svn-to-target <new-url@peg>` |
| Single commit | `ocr delegate preview -c abc123` |

### Step 2: Get rules for files

```bash
ocr delegate rule <path1> <path2> ...
```

Pass the reviewable paths from Step 1. Output is grouped by rule
content — files sharing the same rule appear under one group, avoiding
repetition.

### Step 3: Get diffs

Use the VCS and mode/ref info from Step 1:

**Range mode** (merge\_base provided):
```bash
git diff <merge_base>..<to> -- <path>
```

**Commit mode**:
```bash
git show <commit> -- <path>
```

For SVN range or commit mode, first obtain the selected URL with
`svn info --show-item url`, then use the numeric `resolved_base` and
`resolved_head` returned by preview:

```bash
svn diff --git --internal-diff --show-copies-as-adds --old <working-copy-url>@<resolved_base> --new <working-copy-url>@<resolved_head>
svn cat --revision <resolved_head> -- <working-copy-url>/<url-escaped-path>@<resolved_head>
```

The explicit URL pegs keep both the diff and destination content independent
of a dirty, stale, or mixed-revision working copy.

For an explicit repository-path comparison, invoke preview with
`--svn-from-target` and `--svn-to-target`. The JSON deliberately does not echo
either URL; retain them in the host agent's runtime input, separate each URL
from its optional peg suffix, and combine the URL parts with the safe numeric
`resolved_base`, `resolved_head`, `resolved_base_peg`, and `resolved_head_peg`
fields:

```bash
ocr delegate preview --format json \
  --from 120 --to 128 \
  --svn-from-target 'https://svn.example.com/repos/app/trunk@120' \
  --svn-to-target 'https://svn.example.com/repos/app/branches/feature@128'

svn info --xml --non-interactive --revision <resolved_base> -- <source-target-url>@<resolved_base_peg>
svn info --xml --non-interactive --revision <resolved_head> -- <destination-target-url>@<resolved_head_peg>

svn diff --non-interactive --git --internal-diff --show-copies-as-adds \
  --notice-ancestry \
  --old <source-url-from-info>@<resolved_base> \
  --new <destination-url-from-info>@<resolved_head>
```

This compares the exact SVN endpoints; it does not apply Git merge-base
semantics. Resolving the historical URLs first preserves path moves. Destination
reads use the destination URL returned by `svn info` at `resolved_head`.

**Workspace mode**:
```bash
git diff HEAD -- <path>        # Git tracked files
svn diff --git --internal-diff --show-copies-as-adds -- <path>  # SVN versioned files
cat <path>                     # Git untracked / SVN unversioned files
```

### Step 4: Review each file

For each reviewable file:

1. Get its diff (Step 3)
2. Consult the matching Rule Group (Step 2) as the review checklist
3. Conduct a thorough review, using context exploration as needed

### Step 5: Report

Classify each finding by severity:

- **Critical/High** — bugs, security issues, data loss risks. Always report.
- **Medium** — performance concerns, error handling gaps. Report with context.
- **Low** — style nits, minor suggestions. Discard silently unless clearly valuable.

## Sub-commands reference

| Command | Purpose |
|---------|---------|
| `ocr delegate preview` | List reviewable files + VCS/mode/ref metadata |
| `ocr delegate rule <path...>` | Resolve review rules grouped by content |

## Shared flags

| Flag | Description |
|------|-------------|
| `--from <ref>` | Source ref for range mode |
| `--to <ref>` | Target ref for range mode |
| `--svn-from-target <url[@peg]>` | Source SVN directory for an exact remote range |
| `--svn-to-target <url[@peg]>` | Destination SVN directory for an exact remote range |
| `-c, --commit <hash>` | Single commit mode |
| `--repo <path>` | Repository root (default: cwd) |
| `--rule <path>` | Custom rule.json path |
| `--exclude <patterns>` | Comma-separated exclude patterns |
| `-b, --background <text>` | Business context |
| `-B, --background-file <path>` | Business context from Markdown file (takes precedence over `-b`) |

## Comparison with other integration modes

| Mode | Who calls the LLM? | Use case |
|------|-------------------|----------|
| [Agent Skill](../agent-skill/) | OCR | Agent invokes `ocr review`; OCR drives the full review |
| [Command (Claude Code)](../claude-code/) | OCR | Slash command in Claude Code; OCR drives the review |
| **Delegation Mode** | Host agent | OCR provides scaffolding; agent drives the review |

## See Also

- [Agent Skill](../agent-skill/) — OCR drives the full review on behalf of the agent.
- [Command (Claude Code)](../claude-code/) — slash-command flavor with auto-fix.
