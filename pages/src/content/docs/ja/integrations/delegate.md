---
title: デリゲーションモード
sidebar:
  order: 5
---

OCR が確定的エンジニアリング（ファイル選択、ルール解決）を担当し、ホストエージェントが自身の LLM 能力を使って実際のコードレビューを行います。OCR 側に LLM エンドポイントは不要です。

## デリゲーションモードを使うタイミング

デリゲーションモードは、サブスクリプション型の AI コーディングエージェント向けに設計されています — Claude Code、Codex、Cursor、Open Code、Qoder など。これらのツールには LLM サブスクリプションが組み込まれており、デリゲーションモードを使えばホストエージェントの既存サブスクリプション枠をそのままコードレビューに活用できます。追加のモデル設定や API キーは不要です。

以下の場合に使用してください：

1. AI コーディングエージェントがサブスクリプション制で、その枠をコードレビューに再利用したい場合 — 追加の API キーやモデル設定は不要。
2. OCR のエンジニアリング機能のみ（ファイルフィルタリング、ルール解決、除外ロジック）を利用し、LLM 推論はホストエージェントに任せたい場合。
3. 構造化された入力（ファイルリスト＋ルール）を独自のレビューステップに必要とするカスタムエージェントパイプラインを構築している場合。

## 前提条件

`ocr` CLI がインストールされている必要があります：

```bash
which ocr || npm install -g @alibaba-group/open-code-review
```

LLM 設定（`ocr config set …` や環境変数）は不要です — デリゲーションモードは OCR 側で LLM を呼び出しません。

## Skill / Command のインストール

### Claude Code — Command

```bash
mkdir -p .claude/commands
curl -o .claude/commands/delegate-review.md \
  https://raw.githubusercontent.com/alibaba/open-code-review/main/plugins/open-code-review/claude-code/commands/delegate-review.md
```

### 任意のエージェント — Skill

```bash
npx skills add alibaba/open-code-review --skill open-code-review-delegate
```

または手動コピー：

```bash
cp -R /path/to/open-code-review/skills/open-code-review-delegate ~/.claude/skills/
```

## ワークフロー

### ステップ 1：Preview — レビュー対象の決定

```bash
ocr delegate preview [--from <ref> --to <ref>] [--commit <hash>] [--exclude <patterns>]
```

出力内容：

- **vcs** — git / svn
- **mode** — workspace / range / commit
- **ref メタデータ** — from、to、commit、merge\_base、および安全な解決済み SVN operative/peg revision
- **レビュー可能ファイルリスト** — パス、ステータス、挿入/削除行数
- **除外ファイル** — 除外理由付き

よく使うパターン：

| シナリオ | コマンド |
|----------|---------|
| ワークスペースの変更 | `ocr delegate preview` |
| ブランチ比較 | `ocr delegate preview --from main --to feature` |
| リモート SVN パス | `ocr delegate preview --from 120 --to 128 --svn-from-target <old-url@peg> --svn-to-target <new-url@peg>` |
| 単一コミット | `ocr delegate preview -c abc123` |

### ステップ 2：ファイルのルール取得

```bash
ocr delegate rule <path1> <path2> ...
```

ステップ 1 のレビュー可能パスを渡します。出力はルール内容でグループ化されます — 同じルールを共有するファイルは1つのグループにまとめられ、重複を避けます。

### ステップ 3：diff の取得

ステップ 1 の VCS と mode/ref 情報に基づいて取得します：

**Range モード**（merge\_base あり）：
```bash
git diff <merge_base>..<to> -- <path>
```

**Commit モード**：
```bash
git show <commit> -- <path>
```

SVN の Range または Commit モードでは、まず `svn info --show-item url` で選択 URL を取得し、
preview が返す数値 `resolved_base` と `resolved_head` を使用します。

```bash
svn diff --git --internal-diff --show-copies-as-adds --old <working-copy-url>@<resolved_base> --new <working-copy-url>@<resolved_head>
svn cat --revision <resolved_head> -- <working-copy-url>/<url-escaped-path>@<resolved_head>
```

明示的な URL peg により、dirty、古い、または mixed-revision のワーキングコピーから diff と宛先コンテンツを分離できます。

リポジトリパスを明示的に比較する場合、preview に `--svn-from-target` と
`--svn-to-target` を渡します。JSON は URL を意図的に出力しません。host agent の
runtime input に URL を保持し、各 URL と省略可能な peg suffix を分離したうえで、
URL 部分を安全な数値フィールド `resolved_base`、`resolved_head`、
`resolved_base_peg`、`resolved_head_peg` と組み合わせます。

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

これは厳密な SVN 端点の比較であり、Git merge-base は適用しません。先に履歴 URL を
解決することでパス移動も保持します。宛先内容は `svn info` が `resolved_head` で返す
destination URL から読み取ります。

**Workspace モード**：
```bash
git diff HEAD -- <path>        # Git 追跡ファイル
svn diff --git --internal-diff --show-copies-as-adds -- <path>  # SVN 管理対象ファイル
cat <path>                     # Git 未追跡 / SVN 未管理ファイル
```

### ステップ 4：各ファイルのレビュー

レビュー可能な各ファイルについて：

1. diff を取得（ステップ 3）
2. 対応するルールグループ（ステップ 2）をレビューチェックリストとして参照
3. コンテキスト探索を必要に応じて行い、徹底的にレビュー

### ステップ 5：レポート

重要度で分類：

- **Critical/High** — バグ、セキュリティ問題、データ損失リスク。常に報告。
- **Medium** — パフォーマンスの懸念、エラーハンドリングの欠落。コンテキスト付きで報告。
- **Low** — スタイルの提案、軽微な改善。明確に価値がない限り静かに破棄。

## サブコマンドリファレンス

| コマンド | 目的 |
|----------|------|
| `ocr delegate preview` | レビュー可能ファイル＋VCS/mode/ref メタデータの一覧 |
| `ocr delegate rule <path...>` | 内容別にグループ化されたレビュールールの解決 |

## 共通フラグ

| フラグ | 説明 |
|--------|------|
| `--from <ref>` | Range モードのソース参照 |
| `--to <ref>` | Range モードのターゲット参照 |
| `--svn-from-target <url[@peg]>` | 厳密なリモート範囲のソース SVN ディレクトリ |
| `--svn-to-target <url[@peg]>` | 厳密なリモート範囲の宛先 SVN ディレクトリ |
| `-c, --commit <hash>` | 単一コミットモード |
| `--repo <path>` | リポジトリルート（デフォルト：cwd） |
| `--rule <path>` | カスタム rule.json パス |
| `--exclude <patterns>` | カンマ区切りの除外パターン |
| `-b, --background <text>` | ビジネスコンテキスト |
| `-B, --background-file <path>` | Markdown ファイルからビジネスコンテキストを読み込み（`-b` より優先） |

## 他の統合モードとの比較

| モード | LLM を呼ぶのは？ | ユースケース |
|--------|-----------------|-------------|
| [Agent Skill](../agent-skill/) | OCR | Agent が `ocr review` を呼び出し、OCR が完全なレビューを駆動 |
| [Command（Claude Code）](../claude-code/) | OCR | Claude Code のスラッシュコマンド、OCR がレビューを駆動 |
| **デリゲーションモード** | ホストエージェント | OCR がスキャフォールディングを提供、Agent がレビューを駆動 |

## 関連項目

- [Agent Skill](../agent-skill/) — OCR がエージェントに代わって完全なレビューを駆動。
- [Command（Claude Code）](../claude-code/) — スラッシュコマンド形式、自動修正付き。
