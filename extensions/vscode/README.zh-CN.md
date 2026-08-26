<p align="center">
  <a href="README.md">English</a> | 简体中文
</p>

# Open Code Review (VSCode 插件)

基于 [`open-code-review`](https://www.npmjs.com/package/@alibaba-group/open-code-review) (`ocr`) CLI 的 VSCode 代码审查插件。以 Preact WebView 还原原型交互体验，把 AI 代码审查能力集成进编辑器：在侧边栏发起审查、流式查看日志、在编辑器内逐条应用/忽略/标记误报评论，并与侧边栏双向同步。

---

## 功能

- **自动检测 Git 与 SVN**：侧边栏明确显示当前 VCS，并分别使用 Git 分支/提交或 SVN revision。
- **三种审查模式**：工作区变更、Git 分支对比 / 精确 SVN revision 范围（`--from` / `--to`）、单个 Git commit / SVN revision（`--commit`）。SVN 范围模式还可成对填写远端源/目标路径。
- **待审查文件预览**：Git 使用仓库状态；SVN 使用 `ocr delegate preview`，复用 CLI 的精确 revision 语义与过滤规则，不依赖 VS Code Git 插件。
- **自定义审查提示词**：可选地为本次审查追加 `--background` 提示。
- **流式日志**：审查过程中实时滚动 CLI 输出，支持随时取消。
- **结果展示 + 双向同步**：完成后在侧边栏列出评论卡片，同时在编辑器内渲染 CommentThread；应用/忽略/误报操作在两侧同步。
- **空 / 取消 / 失败态**：无问题、用户取消、CLI 失败均有对应视图（失败可重试，并展示 CLI 返回的真实错误）。
- **配置管理**：在插件内查看/编辑 LLM 提供商配置（写入通过 `ocr config set`）。
- **模型切换 / 连通性测试**：状态栏切换模型、测试与 LLM 的连通性。

---

## 前置依赖

1. 全局安装 `ocr` CLI：

   ```bash
   npm i -g @alibaba-group/open-code-review
   ```

2. 配置可用的 LLM（接口地址、API Key、模型）。可用 CLI 直接配置，或在插件内的配置视图填写：

   ```bash
   ocr config set llm.url https://api.anthropic.com/v1/messages
   ocr config set llm.auth_token sk-...
   ocr config set llm.model claude-opus-4-6
   ocr config set llm.use_anthropic true
   ```

   配置写入 `~/.opencodereview/config.json`。

3. SVN 评审还需安装 1.7 或更新版本的 `svn` 命令行客户端，并确保其位于 `PATH`。打开 VS Code 前，请在 SVN 客户端中配置好仓库认证与证书信任；目标 URL 不得包含凭据。

SVN 工作区评论可挂载到本地编辑器文件。不可变 SVN revision / 远端评审的评论仅显示在侧边栏，
因为 VS Code 内置的快照 URI provider 只支持 Git；实际评审内容仍来自 OCR 冻结后的 SVN 目标 revision。

---

## 开发

### 环境准备

- Node.js ≥ 18，包管理器使用 **Yarn**（仓库自带 `yarn.lock`）。
- VS Code ≥ 1.74。
- 全局可用的 `ocr` CLI（见上文「前置依赖」），插件本质上是 `ocr` 的图形前端。

### 启动开发环境

```bash
cd extensions/vscode
yarn install      # 安装依赖
yarn watch        # 监听式开发构建（推荐：改代码自动重新打包 out/）
```

然后在 VS Code 中打开 `extensions/vscode` 目录，按 **F5** 启动 Extension Development Host
（调试配置已在 `.vscode/launch.json` 提供）。在弹出的新窗口里打开一个有 Git 或 SVN 变更的项目，
即可在活动栏看到 Open Code Review 图标并发起审查。

> 改了代码后：WebView 改动需在开发宿主窗口里 **重新打开侧边栏**（或执行命令 `Developer: Reload Webviews`）；
> Extension Host 改动需 **重启调试**（调试工具栏的 ⟳ 或在宿主窗口按 `Cmd+R`）。

### 常用脚本

```bash
yarn compile      # 单次开发构建（webpack development）
yarn watch        # 监听式开发构建
yarn build        # 生产构建（webpack production，打包前自动执行）
yarn test         # 运行 Jest 单测
yarn lint         # ESLint 检查
yarn package      # 生成可分发的 .vsix 安装包（见下文「构建发布包」）
```

### 调试要点

- **双端通信**：WebView 与 Extension Host 通过 `postMessage` 通信，消息类型定义在
  `src/shared/messages.ts`。两端发收都走 `dispatch` / `handle`，定位问题先看这里。
- **CLI 调用**：所有 `ocr` 子命令由 `src/extension/services/CliService.ts` 通过 `child_process.spawn` 执行。
  `runRaw` 会在 CLI 退出码非 0 时 reject 并带上 stderr 中的 `Error:` 文本，便于排查“审查失败/连接失败”。
- **配置读写**：`ConfigService` 读取 `~/.opencodereview/config.json`，写入则委托 `ocr config set`。
  WebView 端字段为 camelCase（如 `useAnthropic`），磁盘/CLI 端为 snake_case（如 `use_anthropic`），
  转换在 `src/extension/services/configParse.ts`。

---

## 构建

### 仅编译产物

```bash
yarn build        # 生产构建（webpack production）
```

产物：`out/extension.js`（Extension Host）+ `out/webview.js`（WebView SPA）。

### 构建发布包（.vsix）

```bash
yarn package      # = vsce package --no-yarn
```

该命令会：

1. 自动触发 `vscode:prepublish` → 执行 `yarn build` 生产构建；
2. 按 `.vscodeignore` 排除源码、测试、开发文件；
3. 在当前目录生成 `open-code-review-vscode-<version>.vsix`。

> 打包工具为 `@vscode/vsce`，已作为 devDependency 安装，无需全局安装或联网下载。
> `--no-yarn` 用于跳过 vsce 默认的 npm 依赖树校验（本项目用 Yarn）。

发布包只包含运行必需文件：`package.json`、`README.md`、`resources/icon.svg`、`out/extension.js`、`out/webview.js`。

### 本地安装 / 验证

```bash
code --install-extension open-code-review-vscode-<version>.vsix
```

或在 VS Code 中：扩展面板 → 右上角 `⋯` → **Install from VSIX…** → 选择生成的 `.vsix` 文件。

> 发布到 Marketplace 时改用 `vsce publish`（需要 publisher 账号与 PAT），日常分发用上面的 `.vsix` 即可。

---

## 架构

采用 **Monolithic WebView + Thin Extension Host** 方案：

- **WebView** 是独立构建的 Preact SPA，还原原型的全部视觉与交互。
- **Extension Host** 层轻薄，只负责 CLI 调用、VCS 检测与路由、文件系统、Git 原生 diff 和编辑器评论。SVN 文件选择委托给 OCR，不经过 Git 服务。
- 两者通过 `postMessage` 通信，用 `src/shared/` 中的 TypeScript 共享类型保证类型安全。

```
src/
├── extension/      Extension Host（Node.js）：services / providers / commands
├── webview/        WebView SPA（Preact）：views / components / store / bridge
└── shared/         双端共享类型与 postMessage 协议（不依赖 vscode）
```

---

## License

Apache-2.0
