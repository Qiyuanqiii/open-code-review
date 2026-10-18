# 审查工作流插件（Review Workflow Plugins）

关于包安装、执行授权以及运行时适配器之间的关系，请参阅[原生插件架构设计](plugin-architecture.md)。

当代码审查需要遵循特定的执行阶段顺序和完成标准时（例如：先检查改动，再在后续阶段对候选问题进行二次验证确认，最后生成结构化结论报告），可以使用**审查工作流插件**。插件包声明各个阶段并负责校验模型的提交结果；OCR 宿主则提供 Diff 内容、审查规则、上下文工具以及 Token 预算控制。

## 运行 single-pass 示例

在此代码检出中，确保系统可用 `python`（Python 3），安装示例插件并对工作区未提交的修改执行预览：

```sh
ocr plugin install ./examples/ocr-plugins/single-pass
ocr review --review-plugin single-pass --preview
```

在已配置好模型 Provider 并有代码修改的情况下，运行：

```sh
ocr review --review-plugin single-pass --format json --output review.json
```

该示例包含一个阶段。它要求模型提交发现的问题，并在 OCR 发布评论前由插件验证提交内容。可在 `review.json` 中的 `review_plugin` 字段查看该阶段的完成状态与报告。仅安装插件不会运行包代码；预览（`--preview`）亦不会启动 Python 进程或调用大模型。

上述命令使用的是用户全局安装的插件。如果被审查的仓库声明了项目级插件，请将示例添加到项目声明中，并按下方项目工作流进行授权。显式指定全局插件不会覆盖项目锁定的插件集。

## 项目选择与执行授权

对于受仓库版本控制的工作流插件，使用 `ocr plugin add <source>` 声明依赖，提交 `.opencodereview/plugins.json` 与 `plugins.lock.json`，然后使用 `ocr plugin sync --frozen` 完成同步安装。在通过 `--review-plugin <id>` 调用前，请检查锁定的包并运行 `ocr plugin trust` 授予本地执行信任。运行 `ocr plugin preflight --json` 可在不启动工作流或请求模型的情况下，预检安装内容、版本兼容性以及本地授权状态。如果项目存在插件声明，工作流仅能从锁定的插件中选取；全局安装的工作流无法绕过项目锁定。详见[原生 OCR 插件指南](native-plugins.zh-CN.md#项目级插件管理)。

审查工作流插件拥有其阶段计划、提示词指令、完成 Schema、结果校验逻辑以及跨阶段状态。OCR 提供所选文件的 Diff、规则注入、模型与工具执行、会话持久化、Token 计量、评论处理和结果输出。OCR 宿主不包含任何特定插件的硬编码阶段名称或报告字段。

单次审查调用工作流：

```sh
ocr review --review-plugin PACKAGE_ID --preview
ocr review --review-plugin PACKAGE_ID --format json --output review.json
```

仅安装或启用插件**不会**自动调用其审查工作流。`--preview` 仅加载清单，不启动可执行程序或请求模型。正式运行工作流时，插件可执行程序以当前用户的本地权限运行（类似于本地 MCP Server）。显式通过 `--review-plugin` 选取时，即使该插件在全局处于禁用状态，也会在本次调用中加载其规则与 MCP Server；原有的持久化启用状态保持不变。

## 清单配置（Manifest）

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

`command` 为可执行文件路径，不经过 Shell 解析。命令及参数支持 `${plugin_dir}`、`${os}`、`${arch}` 以及 `${exe}`（Windows 下为 `.exe`，其余平台为空）。直接使用 PATH 上的运行时（如 `python`）同样支持。自带的 Python 示例默认使用 `python` 命令；如果系统仅有 `python3`，请在打包或安装前将清单命令调整为 `python3`。OCR 不会自动推导解释器或静默安装运行环境。

插件包可自由组合审查工作流、规则包和 MCP 工具。阶段 ID 为唯一的标识符；清单可声明 1 到 64 个有序执行阶段。不同插件可使用不同的阶段数量、名称、Schema 和判定逻辑。

已安装的快照为当前运行提供规则与工作流。更新已安装的包不会影响正在运行的会话快照。历史版本使用 `engine`/`prompt` 的插件已被废弃，调用时会提示需要更新。

## 兼容性说明

工作流插件可在清单中声明可选的 `requires.ocr_min_version` 与 `requires.features`（例如 `["rules", "mcp", "stdio-v1"]`）。未满足的要求将在启动进程前报错拦截。`stdio-v1` 协议继续使用 `begin`/`submit` 消息，无强制 handshake 或 JSON-RPC 迁移。不兼容的协议改动必须升级版本号，而非对 v1 规范产生歧义。

## stdio-v1 协议规范

OCR 为每个文件组启动一个工作流进程，以被审查的代码仓库为工作目录。进程会在该文件组的所有阶段中保持存活，并保留跨阶段状态。请求与响应均为单行 UTF-8 JSON 对象，每行最大 4 MiB。每个请求必须对应且仅对应一个响应；调试诊断信息请输出至 stderr。每个响应必须包含 `"version": 1`。返回 `error` 字符串表示插件无法处理该请求，将终止当前文件组的审查。

### 阶段开始（`begin`）

在每个阶段开始前，OCR 发送：

```json
{"version":1,"method":"begin","stage":"inspect","paths":["main.go"],"severities":["critical","high","medium","low"],"categories":["bug","security","performance","maintainability","test","style","documentation","other"]}
```

插件返回任务定义以及可选的初始报告：

```json
{"version":1,"task":{"instructions":"检查选定的代码。","input":"检查公共 API 的返回值约定。","completion":{"description":"提交检查结果。","parameters":{"type":"object","properties":{"checked":{"type":"boolean"}},"required":["checked"]}}},"report":{"pending":["inspection"]}}
```

OCR 将 `instructions` 与 `input` 追加到普通的 Diff/规则上下文中，并在上下文压缩时保留。`instructions` 与 `input` 必须包含非空白文本。模型可使用 OCR 内置的上下文检索工具，而模型的 `task_done` 工具定义来自插件返回的 `completion`；直接的 `code_comment` 工具在此模式下会被隐藏，所有评论发布必须经过插件验证器。请求中不会传输模型凭证，可执行程序继承宿主进程环境变量。

### 模型提交（`submit`）

当模型调用 `task_done` 时，OCR 原样转发参数：

```json
{"version":1,"method":"submit","stage":"inspect","arguments":{"checked":true}}
```

接受提交的响应要求显式指定 `accepted` 和 `complete` 布尔值：

```json
{"version":1,"accepted":true,"complete":true,"report":{"checked":true},"comments":[]}
```

插件完全掌握报告结构与阶段完成标准。`complete:false` 会将当前文件组标记为未完成；`feedback` 用于向模型解释剩余工作。即使工作未完成，已被接受的 comments 依然会进入 OCR 正常的发布和去重管道。评论必须对应当前选中的文件路径、包含有效内容，并使用 `begin` 时提供的合法级别（severity）和类别（category）。

如果模型的提交有误但可修复，插件可返回 `accepted:false`、非空的 `feedback`、最新保存的 `report` 且无 comments。OCR 会在剩余请求预算内将反馈回传给模型。插件应自行维护有效的部分状态。进程崩溃、畸形响应或缺失必须的布尔字段将直接中止执行，不会被视作模型错误重试。

## 完成状态与输出

声明的每个阶段都必须执行完成，即使前序阶段未发现任何代码缺陷。`--effort` 不会裁剪插件的阶段计划。组超时时间覆盖所有阶段的总时长。当用户指定 Token 预算时，未使用的额度会自动结转供剩余阶段共享。

JSON 输出中的 `review_plugin` 数组包含每个阶段的记录：`plugin`、`group`、`stage`、`complete`、可选的 `token_limit`、不透明的 `report` 以及任何执行 `error`。运行结束后，可通过 `ocr session show SESSION_ID --json` 检查会话中持久化保存的阶段报告。若阶段未完成，将计入覆盖率报告并导致 CLI 以非零状态码退出。

工作流审查要求全新调用；`--resume` 目前不支持恢复任意进程内部状态。
