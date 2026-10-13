# Review workflow plugins

A review plugin owns its stage plan, prompts, completion schema, validation and
cross-stage state. OCR provides selected diffs and rules, model/tool execution,
sessions, usage accounting, comment processing and output. The host contains no
plugin-specific stage names or report fields.

Select a package for one invocation:

```sh
ocr review --review-plugin PACKAGE_ID --preview
ocr review --review-plugin PACKAGE_ID --format json --output review.json
```

TUI commands are `/preview PACKAGE_ID` and `/review PACKAGE_ID`. The next plain
`/review` uses the normal flow. Installing or enabling a package does not select
its review workflow. Preview loads the manifest without starting the executable
or calling a model. A real selected review runs the package command with the
user's local permissions, like a local MCP server.

## Manifest

```json
{
  "schema_version": 1,
  "id": "example-review",
  "name": "Example Review",
  "version": "1.0.0",
  "review": {
    "protocol": "stdio-v1",
    "command": "${plugin_dir}/bin/${os}-${arch}/reviewer${exe}",
    "args": ["--policy", "${plugin_dir}/policy.md"],
    "stages": ["inspect", "verify"]
  }
}
```

The command is an executable, without shell parsing. Commands and arguments
expand `${plugin_dir}`, `${os}`, `${arch}` and `${exe}` (the last is `.exe` on
Windows and empty elsewhere). A runtime on PATH, such as `python`, also works.
Packages may combine review, rules and MCP contributions. Stage IDs are unique
portable identifiers; the manifest declares an ordered plan of one to 64 stages.
Other plugins can use different stage counts, names, schemas and decisions.

An installed package snapshot supplies both the rules and workflow for the run.
Updating the package does not change an active run's snapshot. Hosts supporting
only the earlier `engine`/`prompt` experiment require an upgrade for this protocol.
The host reads retired `engine`/`prompt` installation metadata so those packages
can still be listed, removed or updated. Attempting to run one asks for an update.

## stdio-v1

OCR starts one process per file group, with the reviewed repository as its working
directory. The process retains state across that group's stages. Requests and
responses are UTF-8 JSON objects, one line each, at most 4 MiB. Emit exactly one
response per request; send diagnostics to stderr. Each response has `version: 1`.
An `error` string means the plugin cannot execute the request and stops the group.

Before each stage, OCR sends:

```json
{"version":1,"method":"begin","stage":"inspect","paths":["main.go"],"severities":["critical","high","medium","low"],"categories":["bug","security","performance","maintainability","test","style","documentation","other"]}
```

The plugin returns a task and optionally its initial report:

```json
{"version":1,"task":{"instructions":"Inspect the selected code.","input":"Check the public return contract.","completion":{"description":"Submit the result.","parameters":{"type":"object","properties":{"checked":{"type":"boolean"}},"required":["checked"]}}},"report":{"pending":["inspection"]}}
```

OCR appends the instructions and input to its normal diff/rule context and keeps
them through context compression. The model retains OCR's context tools. Its
`task_done` definition comes from `completion`; direct `code_comment` is withheld
so publication passes through the plugin's validator. No model credentials are
sent in protocol requests. The executable inherits the normal process environment.

When the model calls `task_done`, OCR forwards the arguments unchanged:

```json
{"version":1,"method":"submit","stage":"inspect","arguments":{"checked":true}}
```

Accepted submissions require explicit `accepted` and `complete` booleans:

```json
{"version":1,"accepted":true,"complete":true,"report":{"checked":true},"comments":[]}
```

The plugin owns the report structure and its completion criteria. `complete:false`
stops the group as incomplete; `feedback` explains the remaining work. Accepted
comments still use OCR's normal publication and filtering, including on incomplete
work. Comments must reference a selected path, contain content, and use the native
severity/category values supplied at `begin`. Other comment fields are
`existing_code`, `suggestion_code`, `start_line`, `end_line` and `thinking`.

For a repairable model submission, return `accepted:false`, nonempty `feedback`,
the latest saved `report` and no comments. OCR returns the feedback to the model
within the existing request allowance. The plugin must retain valid partial
entries itself. Crashes, malformed protocol responses and missing required
booleans stop execution; they are not retried as model mistakes.

## Completion and output

Every declared stage must finish, even if previous stages found no issues.
`--effort` does not shorten a plugin's plan. Existing context, tool-round and
timeout settings apply. The group timeout covers the declared stage count. When
the user supplies a token budget, unused allowance carries forward and remaining
stages share it; no additional total token limit is imposed.

JSON output includes `review_plugin`: records with `plugin`, `group`, `stage`,
`complete`, optional `token_limit`, opaque `report` and any execution `error`.
The latest returned report is retained if later model or process execution fails.
Unfinished required work enters the existing coverage manifest and causes a
nonzero CLI exit. Ordinary review exit behavior is unchanged.

Plugin reviews require a fresh invocation; `--resume` does not yet restore
arbitrary process state. JSON and session records remain available for inspection.

See [the single-pass example](../examples/ocr-plugins/single-pass) for a Python
implementation and [Behavior Review](https://github.com/Qiyuanqiii/ocr-behavior-review)
for an independent executable with a three-stage method and retained evidence.
