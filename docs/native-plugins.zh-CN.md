# 原生 OCR 插件

OCR 支持通过其安装器、注册中心和运行时加载原生插件，用以提供审查规则（Review Rules）和 MCP 上下文工具。这些插件包以 `ocr-plugin.json` 为清单，与将 OCR 集成到其他编码 Agent 中的集成方案相互独立。

有关模块边界、生命周期图解和代码评审顺序，请参阅[原生插件架构设计](plugin-architecture.md)。

## 支持的安装来源与 URL 输入

使用 OCR 插件时最常见的疑问是：**目前支持哪些来源？输入支持 URL 吗？**

**答案是：完全支持 URL 输入。** 无论是 `ocr plugin install <source>` 还是 `ocr plugin add <source>`，均原生支持远程网络 URL 以及本地文件路径。

### 支持的来源类型

OCR 支持四类主要的插件安装来源：

1. **公开 GitHub 仓库（URL）**
   - **仓库根目录**：`https://github.com/OWNER/REPO`
     直接安装位于仓库根目录的插件。
   - **分支或 Tag（支持子目录）**：`https://github.com/OWNER/REPO/tree/REF/PATH`
     例如：`https://github.com/example/monorepo/tree/v1.2.0/plugins/security-rules`
   - **Commit SHA 确定版本**：`https://github.com/OWNER/REPO/tree/完整40位COMMIT_SHA/PATH`
     直接指向不可变的确定 Commit。
   - *工作原理*：当提供分支名或 Tag 时，OCR 会通过 GitHub API 将其解析为确定的 40 位 Commit SHA（若 URL 中已是 40 位 SHA 则跳过 API 解析），随后从 `codeload.github.com` 流式下载对应 Commit 的 tar.gz 归档至隔离的临时目录，并解压目标插件目录。

2. **远程 HTTPS 归档（URL）**
   - **HTTPS tar.gz 归档**：`https://example.com/team-review.tar.gz`
   - **HTTPS ZIP 归档**：`https://example.com/team-review.zip`
   - **Releases 资产包**（如 GitHub Releases 或 GitLab 发布资产）：`https://github.com/OWNER/REPO/releases/download/v1.0.0/plugin.tar.gz`
   - *工作原理*：OCR 通过 HTTPS 下载文件，自动检查文件头魔数识别格式（gzip 为 `0x1f 0x8b`，ZIP 为 `PK`，因此 URL 不强制要求特定扩展名），并在大小安全限制内解压至隔离沙箱进行校验。

3. **本地目录路径（Path）**
   - 包含 `ocr-plugin.json` 的本地相对或绝对路径：
     ```text
     ocr plugin install ./examples/ocr-plugins/review-checklist
     ocr plugin install /opt/company-plugins/code-style
     ```
   - OCR 校验清单格式与文件内容后，将完整快照复制到本地插件存储库。

4. **本地归档文件（Path）**
   - 本地 `.tar.gz` 或 `.zip` 压缩包：
     ```text
     ocr plugin install ./team-review.tar.gz
     ocr plugin install ./team-review.zip
     ```
   - 解压至隔离目录，通过完整性校验后保存快照。

### 不支持与禁止的来源

为保证执行的确定性、项目锁文件的可复现性以及系统安全，以下来源**不被支持**：

- **明文 HTTP 链接（`http://`）**：强制要求 HTTPS，任何不安全的 HTTP 链接均会被拒绝；发生向 HTTP 降级的重定向亦会被拒绝。
- **带有内嵌凭证或锚点的 URL**：带有 userinfo（如 `https://user:password@...`）或 URL Fragment（`#...`）的链接会被拒绝。
- **旧版 Marketplace Catalog 索引 / JSON 目录**：为了精简与提高安全性，已移除全局插件市场的中心化目录搜索功能（`ID@market` 语法），转为直接包路径与 URL 安装。
- **Git SSH 克隆链接（`git@github.com:...`）**：不支持裸 SSH Git 协议克隆，请使用公开的 HTTPS 链接或直接下载归档。
- **软链接（Symlinks）、硬链接、设备文件与 Socket**：压缩包或目录中包含软链接、硬链接或特殊设备文件均会被拒绝解压与拷贝，以防止路径穿越和提权攻击。
- **包含 `@` 符号的文件名**：禁止直接使用裸 `@` 文件名，防止与旧版市场 ID 混淆。如果本地文件名含有 `@`，请使用显式路径（如 `./plugin@1.0.tar.gz`）。

### 归档与资源安全限制

为防止解压炸弹和异常资源耗尽：
- **清单大小上限**：1 MiB（`ocr-plugin.json`）。
- **归档下载上限**：32 MiB。
- **解压内容上限**：64 MiB（展开后的总文件大小）。
- **tar.gz 解压流上限**：72 MiB（包含 tar 头部、对齐与元数据）。
- **单插件文件数量上限**：最多 4,096 个文件。
- **项目配置文件上限**：4 MiB（`plugins.json` 与 `plugins.lock.json`）。

---

## 完整 CLI 子命令参考

OCR 在 `ocr plugin` 下提供了一整套管理命令，分为**用户全局插件管理**与**项目级插件管理**，并辅以 `ocr mcp list` 用于检查工具配置。

```text
ocr plugin <command> [flags]
```

### 全局插件管理命令

全局插件存储于 `~/.opencodereview/plugins` 目录下。启用后，其提供的规则和 MCP 工具会自动应用于当前用户在所有仓库中运行的代码审查与扫描（除非当前项目声明了项目级插件）。

#### 1. `ocr plugin install <url|path>`
从 URL 或本地路径安装并注册插件，校验清单并保存不可变的快照。

- **参数**：`<url|path>`（GitHub 链接、HTTPS 归档链接、本地目录或本地归档）。
- **常用选项**：
  - `--enable`：安装完成后立即启用该插件。
  - `--dry-run`：仅下载/检查插件内容并输出预览，不保存快照也不启用。
  - `--plugin <id>`：当源文件包含多个插件时，显式指定要安装的插件 ID。
  - `--json`：以结构化 JSON 格式输出结果。
- **核心原则**：安装过程仅复制文件并校验清单，**绝对不会执行插件代码、运行安装脚本或通过包管理器下载依赖**。
- **命令示例**：
  ```sh
  # 预览 GitHub 仓库插件
  ocr plugin install https://github.com/example/review-rules --dry-run

  # 安装并立即启用 HTTPS 压缩包插件
  ocr plugin install https://example.com/team-review.tar.gz --enable

  # 从本地目录安装
  ocr plugin install ./examples/ocr-plugins/project-notes
  ```

#### 2. `ocr plugin list`
列出所有已安装的全局插件、其版本号、来源路径以及启用状态（`[enabled]` 或 `[disabled]`）。

- **选项**：
  - `--json`：输出 JSON 数组格式的插件列表。
- **输出示例**：
  ```text
  team-review      1.0.0  [enabled]   https://example.com/team-review.tar.gz
  project-notes    1.2.0  [disabled]  ./examples/ocr-plugins/project-notes
  ```

#### 3. `ocr plugin show <id>`
查看已安装插件的详细信息，包括快照存放目录、规则清单、贡献的 MCP Server 以及宿主版本要求。

- **参数**：`<id>`（插件 ID）。
- **选项**：`--json`（输出完整的机器可读数据）。
- **敏感信息脱敏**：为保护凭证安全，管理展示中会自动隐去 MCP 命令参数、环境变量、认证 Header 以及源 URL 查询参数。

#### 4. `ocr plugin update <id>`
根据安装时记录的原始来源重新拉取并更新插件，同时保持当前的启用/禁用状态不变。

- **参数**：`<id>`（插件 ID）。
- **选项**：
  - `--accept-permissions`：显式确认接受新增或修改的 MCP 执行权限声明。
  - `--json`：输出更新结果 JSON。
- **安全拦截门禁**：如果新版本中**新增了 MCP Server**，或者**修改了现有 Server 的命令、参数、环境变量或通信协议**，OCR 会主动中断更新并报错（`PLUGIN_EXECUTION_CHANGED`），提示具体的变更项。用户确认安全后，必须附加 `--accept-permissions` 标志完成更新：
  ```sh
  ocr plugin update <id> --accept-permissions
  ```

#### 5. `ocr plugin enable <id>`
启用指定的插件。其贡献的评审规则将在后续审查中追加，声明的 MCP Server 工具将被注入到审查运行时。

#### 6. `ocr plugin disable <id>`
禁用指定的插件。插件保留在本地，但不会被审查或扫描加载。

#### 7. `ocr plugin remove <id>`
注销并删除指定的插件。正在运行的审查任务仍可使用已有的快照执行完毕。

---

### 项目级插件管理命令

在团队协作或 CI/CD 流程中，必须保证所有成员和自动化流水线使用完全一致的插件版本与确定的执行授权。项目级插件通过项目根目录下的 `.opencodereview/plugins.json`（声明文件）与 `.opencodereview/plugins.lock.json`（锁文件）进行管理。

当仓库中存在项目插件配置时，OCR 将进入**项目独占模式**：仅加载锁文件中声明的插件，完全忽略全局插件。

#### 1. `ocr plugin add <source>`
为当前项目添加插件依赖。解析指定的来源，更新 `.opencodereview/plugins.json`，并将对应平台的具体构件信息写入 `.opencodereview/plugins.lock.json`。

- **选项**：
  - `--plugin <id>`：多插件来源中指定插件 ID。
  - `--repo <path>`：指定项目根路径（默认当前目录）。
- **示例**：
  ```sh
  ocr plugin add https://github.com/example/team-review
  git add .opencodereview/plugins.json .opencodereview/plugins.lock.json
  ```

#### 2. `ocr plugin lock`
根据 `.opencodereview/plugins.json` 为当前操作系统与架构（`GOOS/GOARCH`）解析所有依赖，生成或更新 `.opencodereview/plugins.lock.json`。

- 锁文件记录了各平台的精确下载 URL、Commit Revision、文件 SHA-256 和内容树指纹。更新后请提交至 Git。

#### 3. `ocr plugin sync`
严格根据锁文件安装对应平台的精确快照。

- **选项**：
  - `--frozen`：**必须提供**。强制要求声明文件与锁文件完全一致，禁止自动解析新版本。
  - `--offline`：离线模式，仅使用已缓存的快照或仓库本地来源，不发起网络请求。
  - `--repo <path>`：项目根路径。
- **安全设计**：`sync` 仅下载和解压快照，**绝不会自动赋予可执行权限或运行插件代码**。

#### 4. `ocr plugin trust`
在当前机器上对当前项目锁文件中声明的 MCP Server 可执行构件授予本地执行授权。

- **选项**：
  - `--revoke`：撤销当前项目锁文件的本地执行授权。
  - `--repo <path>`：项目根路径。
- **信任边界隔离**：信任记录存储于用户目录外部（`~/.opencodereview/trust`），严格绑定到当前项目的规范绝对路径、锁文件内容哈希和当前系统平台。仓库无法通过提交配置自我提权。纯规则插件无需授权。

#### 5. `ocr plugin project`
以只读方式检查项目依赖声明、锁文件详情及本地授权状态，不下载构件，不修改信任。

- **选项**：`--repo <path>`，`--json`。

#### 6. `ocr plugin preflight`
在执行审查前或 CI 流水线中进行预检，验证锁文件中所有依赖是否已完整安装、构件校验和是否匹配、宿主版本是否满足要求、执行权限是否已得到授权。

- **选项**：
  - `--fingerprint`：计算并输出当前已验证插件锁与平台的确定性指纹哈希，供增量代码审查缓存与复用判断。
  - `--repo <path>`：项目根路径。
  - `--json`：以 JSON 格式输出预检详情。

---

### 统一 MCP 工具检查命令

#### `ocr mcp list`
提供统一的只读检查视图，汇总展示来自用户配置文件（`~/.opencodereview/config.json`）、全局启用插件以及项目锁定插件的所有 MCP Server。

```sh
ocr mcp list
ocr mcp list --repo /path/to/project --json
```

- **输出字段**：服务名称（插件形式为 `plugin/PLUGIN_ID/SERVER`）、来源（`user_config`、`global_plugin`、`project_plugin`）、传输协议（`stdio`、`remote`）、配置状态及授权状态。
- **安全保障**：该命令仅读取配置和清单，**绝对不会启动 Server 进程、不会执行 setup 安装脚本、不会连接外部网络端点，也不会读取 LLM 密钥**。

| 状态值 | 含义 |
| --- | --- |
| `configured` | 服务声明已被选中且已获授权，可在审查中正常调用 |
| `disabled` | 全局插件当前处于禁用状态 |
| `incompatible` | 插件声明的最低宿主版本高于当前运行的 OCR 版本 |
| `authorization_required` | 项目插件已安装，但尚未运行 `ocr plugin trust` 授予执行权限 |
| `server_name_conflict` | 插件服务名称与手动配置的服务名称发生冲突 |

---

## 典型操作工作流

### 全局插件完整工作流

```sh
# 1. 安装前预览插件内容与权限声明
ocr plugin install https://github.com/example/team-rules --dry-run

# 2. 正式安装并立即启用
ocr plugin install https://github.com/example/team-rules --enable

# 3. 检查安装状态与注入的 MCP 工具
ocr plugin show team-rules
ocr mcp list

# 4. 正常运行代码审查（插件规则与工具自动生效）
ocr review

# 5. 后续插件更新并确认权限变更
ocr plugin update team-rules --accept-permissions
```

### 项目依赖与 CI 流水线工作流

```sh
# 1. 项目管理员添加依赖并锁定
ocr plugin add https://github.com/example/security-pack
git add .opencodereview/
git commit -m "chore: 引入团队安全审查插件"

# 2. 开发者克隆代码或 CI 环境同步锁定的确切版本
ocr plugin sync --frozen

# 3. 检查插件内容并授予本地执行信任（针对含 MCP 进程的插件）
ocr plugin project
ocr plugin trust

# 4. 执行审查前预检
ocr plugin preflight

# 5. 启动代码审查
ocr review
```

---

## 插件包开发规范

在目录或 tar.gz 归档的根目录下放置 `ocr-plugin.json`：

```text
my-plugin/
├── ocr-plugin.json
├── rules.json
├── checks.md
└── server.mjs
```

清单示例：

```json
{
  "schema_version": 1,
  "id": "team-review",
  "name": "Team Review",
  "version": "1.0.0",
  "description": "团队定制审查规则与上下文增强工具",
  "rules": ["rules.json"],
  "mcp_servers": {
    "context": {
      "type": "stdio",
      "command": "node",
      "args": ["${plugin_dir}/server.mjs"],
      "tools": ["team_repository_context"]
    }
  },
  "requires": {
    "ocr_min_version": "1.2.0",
    "features": ["rules", "mcp"]
  },
  "metadata": {
    "homepage": "https://example.com/team-review"
  }
}
```

### 占位符支持

在 `mcp_servers` 的 `command`、`args` 和 `env` 中支持以下内置占位符：
- `${plugin_dir}`：插件安装快照所在的绝对目录路径。
- `${os}`：操作系统名称（`linux`、`darwin`、`windows`）。
- `${arch}`：架构名称（`amd64`、`arm64`）。
- `${exe}`：Windows 平台扩展为 `.exe`，其余平台为空字符串。

### 打包分发建议

在 Linux/macOS 上打包包含可执行脚本或二进制文件的插件时，请在打包前设置好文件的执行权限位（例如 `chmod +x bin/server`）。OCR 在解压 tar.gz 归档时会自动保留文件的可执行位：

```sh
tar -czf team-review-1.0.0.tar.gz -C my-plugin .
```

---

## 安全模型与完整性保障

1. **安装期零代码执行**：`install`、`sync`、`list`、`show`、`dry-run`、`lock` 和 `preflight` 不执行任何插件内的可执行程序或安装脚本。
2. **确定性数字指纹**：
   - `archive_sha256`：原始归档下载文件的 SHA-256。
   - `content_sha256`：基于 `ocr-package-tree-v1` 算法遍历所有文件计算的确定性内容哈希。
3. **权限变更拦截门禁**：全局插件更新若涉及 MCP 执行配置变动，必须显式附加 `--accept-permissions` 确认。
4. **项目信任与仓库隔离**：执行信任存储于仓库外部，新克隆的项目不会自动获得执行权限，必须由开发者显式执行 `ocr plugin trust`。
5. **CI 基线信任保护**：在 GitHub Actions 等 CI 环境中，插件策略始终从 PR 的基准分支（Base Commit）加载，绝不会被不可信的 PR Head 分支所篡改。
