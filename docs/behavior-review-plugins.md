# Review policy plugins

A review contribution combines a package policy with the host's `behavior-v1`
engine. Select it for a single diff review:

```sh
ocr plugin install ./plugin-package
ocr review --review-plugin behavior-review --preview
ocr review --review-plugin behavior-review --format json --output review.json
```

In the TUI, `/review behavior-review` starts the selected scope with that policy.
`/preview behavior-review` validates the selection and previews files without
calling a model. A subsequent `/review` uses the ordinary flow. Installation and
`plugin enable` do not select a review policy. The selected package's rules and
MCP contributions, if present, join the enabled packages for that invocation.

## Manifest

```json
{
  "schema_version": 1,
  "id": "behavior-review",
  "name": "Behavior Review",
  "version": "0.1.0",
  "review": {
    "engine": "behavior-v1",
    "prompt": "behavior-review.md"
  }
}
```

`review` counts as a contribution alongside `rules` and `mcp_servers`. Only
`behavior-v1` is supported. `prompt` must be a relative Markdown file inside the
package containing nonempty UTF-8 text, at most 64 KiB. Installation validates the
file without running code. The captured policy content and selection participate
in the run's rule identity. Updating a package leaves an active run's snapshot
unchanged. Hosts without this extension reject the unknown `review` field.

## Execution and reports

The engine performs three conversations per file group: `contracts`,
`counterexamples`, and `adjudication`. It reuses OCR's diff/rule selection, context
tools, LLM runner, usage accounting, compression, sessions, comment publication
and filtering. It runs all required stages even when no candidates are found.
Effort does not reduce these three stages or change model reasoning settings.

The package policy describes judgment principles and the stage protocol. The
host owns the stage, accepted entries, saved references and coverage checks.
`task_done.report` accepts entries by ID; correction responses list saved IDs and
pending work. Later patches retain omitted fields. Missing `complete` is a
protocol error; explicit `false` records unfinished work. During adjudication the
host restores the original attempts and links so the model only submits decisions.
Stage inputs survive compression unchanged.

JSON output includes `behavior_review`, containing reports with contracts,
source assessments, checks, unknowns, limitations, pending corrections and errors.
Only valid confirmed decisions enter normal comment publication and filtering.
Required unfinished work marks coverage incomplete even if findings were already
published. Plugin reviews return nonzero for partial or failed coverage. Ordinary
review exit behavior is unchanged.

Existing tool-round, context and timeout settings apply. The group timeout covers
three stages. No token cap is added; when the user supplies an aggregate budget,
unused allowance carries forward and remaining stages share it. Submission and
correction use the existing request allowance and final grace request.

Plugin reviews currently require a fresh run; `--resume` is rejected. Session
records and JSON output preserve analysis for inspection, but the current resume
index does not reconstruct the structured stage ledger. Complete indicates that
assigned work finished; it does not establish absence of defects.

The maintained policy package is
[ocr-behavior-review](https://github.com/Qiyuanqiii/ocr-behavior-review).
