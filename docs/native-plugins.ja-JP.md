# ネイティブ OCR プラグイン

OCR は、インストーラー、レジストリ、およびランタイムを通じて、レビュー規則（Review Rules）および MCP ツールを提供するプラグインをホストできます。これらのパッケージは `ocr-plugin.json` をマニフェストとして使用し、OCR を他のコーディングエージェントに統合する設定とは独立しています。

モジュール境界、ライフサイクル図、推奨レビュー手順については、[ネイティブプラグインのアーキテクチャ設計](plugin-architecture.md) を参照してください。

## サポートされているインストールソースと URL 入力

OCR プラグインの利用時によくある疑問：**どのようなソースがサポートされていますか？入力に URL は使用できますか？**

**回答：URL 入力は完全にサポートされています。** `ocr plugin install <source>` および `ocr plugin add <source>` の両方で、リモート URL およびローカルファイルパスを直接指定できます。

### サポートされているソースの種類

OCR は主に 4 種類のパッケージソースをサポートしています：

1. **パブリック GitHub リポジトリ（URL）**
   - **リポジトリルート**: `https://github.com/OWNER/REPO`
     リポジトリのルートにあるプラグインをインストールします。
   - **ブランチまたはタグ（サブディレクトリ指定可能）**: `https://github.com/OWNER/REPO/tree/REF/PATH`
     例: `https://github.com/example/monorepo/tree/v1.2.0/plugins/security-rules`
   - **コミット SHA（不変リビジョン指定）**: `https://github.com/OWNER/REPO/tree/FULL_40_CHAR_COMMIT_SHA/PATH`
     確定した 40 文字のコミット SHA を直接指定します。
   - *動作の仕組み*: ブランチ名やタグが指定された場合、OCR は GitHub API 経由で確定した 40 文字のコミット SHA に解決します（URL に既に 40 文字の SHA が含まれている場合は API 解決をスキップします）。その後、`codeload.github.com` から該当コミットの tar.gz アーカイブを隔離された一時ディレクトリにストリーミングダウンロードし、対象のプラグインディレクトリを展開します。

2. **リモート HTTPS アーカイブ（URL）**
   - **HTTPS tar.gz アーカイブ**: `https://example.com/team-review.tar.gz`
   - **HTTPS ZIP アーカイブ**: `https://example.com/team-review.zip`
   - **リリースアセット**: `https://github.com/OWNER/REPO/releases/download/v1.0.0/plugin.tar.gz`
   - *動作の仕組み*: HTTPS 経由でダウンロードし、マジックバイト（gzip は `0x1f 0x8b`、ZIP は `PK`）を検査して形式を自動判別します（URL に拡張子が含まれていなくても問題ありません）。サイズ制限内で隔離サンドボックスに展開し検証します。

3. **ローカルディレクトリパス（Path）**
   - ルートに `ocr-plugin.json` を含むローカルの相対または絶対パス：
     ```text
     ocr plugin install ./examples/ocr-plugins/review-checklist
     ocr plugin install /opt/company-plugins/code-style
     ```
   - OCR はマニフェストと内容を検証し、ローカルプラグインストアにスナップショットを複製します。

4. **ローカルアーカイブファイル（Path）**
   - ローカルの `.tar.gz` または `.zip` アーカイブ：
     ```text
     ocr plugin install ./team-review.tar.gz
     ocr plugin install ./team-review.zip
     ```
   - 隔離ディレクトリに展開・検証し、スナップショットを保存します。

### サポートされていない・禁止されているソース

決定論的な実行、再現可能なプロジェクトロック、およびシステムの安全性を維持するため、以下は**サポートされていません**：

- **平文 HTTP URL (`http://`)**: セキュリティ保護のため HTTPS が必須です。安全でない HTTP 接続や HTTP へのダウングレードリダイレクトは拒否されます。
- **認証情報やフラグメントが含まれる URL**: userinfo（例: `https://user:password@...`）や URL フラグメント（`#...`）を含む URL は拒否されます。
- **従来のマーケットプレイスカタログ / JSON インデックス**: カタログ検索機能（`ID@market` 構文）は廃止され、直接パッケージや URL の指定方式に統合されました。
- **生の Git SSH クローン URL (`git@github.com:...`)**: SSH 経由の直接 Git クローンはサポートされていません。パブリック HTTPS URL またはアーカイブを使用してください。
- **シンボリックリンク、ハードリンク、デバイスファイル**: ディレクトリトラバーサルや権限昇格を防ぐため、アーカイブ内のシンボリックリンクや特殊ファイルは展開時に厳格に拒否されます。
- **`@` を含むローカルファイル名**: 従来のマーケット ID との混同を防ぐため、裸の `@` を含む入力は拒否されます。ファイル名に `@` が含まれる場合は `./plugin@1.0.tar.gz` のように明示的なパスを指定してください。

### アーカイブおよびリソースの制限

展開爆弾やリソース枯渇を防ぐための上限：
- **マニフェスト最大サイズ**: 1 MiB (`ocr-plugin.json`)
- **アーカイブダウンロード最大サイズ**: 32 MiB
- **展開後合計サイズ上限**: 64 MiB
- **tar.gz 展開ストリーム上限**: 72 MiB（ヘッダー・メタデータを含む）
- **最大ファイル数**: 1 パッケージあたり最大 4,096 ファイル
- **プロジェクト設定ファイル上限**: 4 MiB (`plugins.json` および `plugins.lock.json`)

---

## 完全な CLI サブコマンドリファレンス

OCR は `ocr plugin` の下に、**グローバルプラグイン管理** と **プロジェクト単位のプラグイン管理** のコマンドを提供しており、ツールの確認には `ocr mcp list` を使用します。

```text
ocr plugin <command> [flags]
```

### グローバルプラグイン管理コマンド

グローバルプラグインは `~/.opencodereview/plugins` に配置されます。有効化されると、プロジェクト固有の設定がない限り、該当ユーザーが実行するすべてのレビューやスキャンに自動的に適用されます。

#### 1. `ocr plugin install <url|path>`
URL またはローカルパスからプラグインをインストール・登録し、スナップショットを作成します。

- **引数**: `<url|path>`（GitHub URL、HTTPS アーカイブ URL、ローカルディレクトリまたはアーカイブ）。
- **フラグ**:
  - `--enable`: インストール直後にプラグインを有効化します。
  - `--dry-run`: インストールや有効化を行わずにパッケージ内容をダウンロード・検査しプレビューを表示します。
  - `--plugin <id>`: 単一のソース内に複数のプラグインが存在する場合に ID を指定します。
  - `--json`: 構造化 JSON で出力します。
- **安全規則**: インストールはファイルのコピーとマニフェストの検証のみを行い、**プラグインコードの実行、インストールスクリプトの実行、パッケージマネージャーによる依存関係のビルドなどは一切行いません**。

#### 2. `ocr plugin list`
インストールされているグローバルプラグイン一覧、バージョン、ソース、および有効/無効状態（`[enabled]` / `[disabled]`）を表示します。

- **フラグ**: `--json`（JSON 配列で出力）

#### 3. `ocr plugin show <id>`
指定したプラグインの詳細情報（スナップショットディレクトリ、ルール一覧、提供される MCP サーバー、ホスト互換性要件など）を確認します。

- **引数**: `<id>`（プラグイン ID）
- **フラグ**: `--json`
- **情報の難読化・保護**: 認証情報の漏洩を防ぐため、MCP コマンドの引数、環境変数、認証ヘッダー、URL クエリパラメータは表示時に自動的に除外されます。

#### 4. `ocr plugin update <id>`
インストール時に記録された元のソースからプラグインを再取得して更新します（有効/無効状態は維持されます）。

- **引数**: `<id>`（プラグイン ID）
- **フラグ**:
  - `--accept-permissions`: 新規追加または変更された MCP 実行権限を明示的に承認します。
  - `--json`: 更新結果を JSON で出力します。
- **実行権限の変更検知**: 更新によって**新しい MCP サーバーが追加された場合**、または**既存サーバーのコマンド、引数、環境変数、トランスポートが変更された場合**、OCR は更新を中断してエラー（`PLUGIN_EXECUTION_CHANGED`）を返します。内容を確認後、`--accept-permissions` を付与して明示的に実行する必要があります：
  ```sh
  ocr plugin update <id> --accept-permissions
  ```

#### 5. `ocr plugin enable <id>`
プラグインを有効化します。レビュー規則と MCP ツールがその後の `ocr review` や `ocr scan` に読み込まれます。

#### 6. `ocr plugin disable <id>`
プラグインを無効化します。パッケージは残りますが、実行時に読み込まれなくなります。

#### 7. `ocr plugin remove <id>`
プラグインの登録を解除して削除します。現在実行中のレビューセッションは既存のスナップショットを引き続き使用できます。

---

### プロジェクト単位のプラグイン管理コマンド

チーム開発や CI/CD 環境では、全員が完全に同一のプラグインバージョンを使用し、明示的に実行権限を管理できる再現性が不可欠です。プロジェクトプラグインはリポジトリルートの `.opencodereview/plugins.json`（宣言）と `.opencodereview/plugins.lock.json`（ロック）で管理されます。

リポジトリ内にプロジェクトプラグインが宣言されている場合、OCR は**プロジェクト専用モード**となり、グローバルプラグインを無視してロックされたプラグインのみを読み込みます。

#### 1. `ocr plugin add <source>`
プロジェクトにプラグイン依存関係を追加します。ソースを解決し、宣言ファイルおよび現在のプラットフォーム向けロックファイルを更新します。

- **フラグ**:
  - `--plugin <id>`: 複数プラグインを含むソースから特定の ID を選択。
  - `--repo <path>`: プロジェクトルートのパス。

#### 2. `ocr plugin lock`
宣言ファイルに基づいて、現在の OS/アーキテクチャ（`GOOS/GOARCH`）向けに依存関係を解決し、ロックファイルを生成または更新します。

#### 3. `ocr plugin sync`
ロックファイルで指定された厳密な成果物スナップショットをインストールします。

- **フラグ**:
  - `--frozen`: **必須**。宣言とロックファイルが完全に一致していることを要求し、新しいバージョンの解決を防ぎます。
  - `--offline`: オフラインモード。ローカルキャッシュまたはリポジトリ内ソースのみを使用します。
  - `--repo <path>`: プロジェクトルートのパス。
- **安全性**: スナップショットの配置のみを行い、**プラグインコードを実行したり実行権限を勝手に付与したりしません**。

#### 4. `ocr plugin trust`
現在のプロジェクトロックで宣言された MCP サーバーの実行可能ファイルに対し、このマシン上でのローカル実行権限を明示的に付与します。

- **フラグ**:
  - `--revoke`: 実行権限を取り消します。
  - `--repo <path>`: プロジェクトルートのパス。
- **信頼境界**: 信頼記録はリポジトリ外（`~/.opencodereview/trust`）に保存され、プロジェクトの絶対パス、ロックファイルの内容ハ希、およびホストプラットフォームに暗号的にバインドされます。設定をコミットして自動的に権限を昇格させることはできません。

#### 5. `ocr plugin project`
プロジェクトの依存関係、ロックファイルの内容、およびローカル認証状態を読み取り専用で検査します。

#### 6. `ocr plugin preflight`
レビュー実行前または CI パイプライン内で事前検証を行います。ロックされた全パッケージがインストールされ、チェックサムが一致し、ホストバージョンが満たされ、実行信頼が付与されているかを確認します。

- **フラグ**:
  - `--fingerprint`: 検証済みのロックおよびプラットフォームの決定論的な指紋ハッシュを計算・出力します（差分レビューのキャッシュ判定用）。
  - `--repo <path>`: プロジェクトルートのパス。
  - `--json`: JSON 形式で出力。

---

### 統合 MCP インベントリ確認コマンド

#### `ocr mcp list`
ユーザー設定（`~/.opencodereview/config.json`）、グローバルプラグイン、プロジェクトプラグインから提供される全 MCP サーバーを横断的に一覧表示します。

```sh
ocr mcp list
ocr mcp list --repo /path/to/project --json
```

- **出力項目**: サーバー名（`plugin/PLUGIN_ID/SERVER` 形式）、提供元（`user_config`, `global_plugin`, `project_plugin`）、通信方式（`stdio`, `remote`）、状態、承認ステータス。
- **安全性**: 設定とマニフェストの読み取りのみを行い、**プロセスの起動、セットアップスクリプトの実行、ネットワーク接続の確立、LLM 認証情報の取得は一切行いません**。

| 状態 | 意味 |
| --- | --- |
| `configured` | 選択および承認済みであり、レビューで呼び出し可能 |
| `disabled` | グローバルプラグインが無効化されている |
| `incompatible` | 要求される OCR バージョンを満たしていない |
| `authorization_required` | インストール済みだが `ocr plugin trust` による実行承認が行われていない |
| `server_name_conflict` | 手動設定とプラグインサーバーの名前が衝突している |

---

## 典型的な運用ワークフロー

### グローバルプラグインの運用例

```sh
# 1. インストール前のドライラン
ocr plugin install https://github.com/example/team-rules --dry-run

# 2. インストールして有効化
ocr plugin install https://github.com/example/team-rules --enable

# 3. 提供機能の確認
ocr plugin show team-rules
ocr mcp list

# 4. レビューの実行（ルールとツールが自動適用されます）
ocr review

# 5. 更新時の権限確認
ocr plugin update team-rules --accept-permissions
```

### プロジェクトプラグイン & CI の運用例

```sh
# 1. 依存関係の追加とロックファイルのコミット
ocr plugin add https://github.com/example/security-pack
git add .opencodereview/
git commit -m "chore: セキュリティレビュープラグインを追加"

# 2. チームメンバーや CI 環境での完全同期
ocr plugin sync --frozen

# 3. パッケージの確認とローカル実行承認
ocr plugin project
ocr plugin trust

# 4. 事前検証
ocr plugin preflight

# 5. レビュー実行
ocr review
```

---

## パッケージの作成仕様

ディレクトリまたは tar.gz のルートに `ocr-plugin.json` を配置します：

```text
my-plugin/
├── ocr-plugin.json
├── rules.json
├── checks.md
└── server.mjs
```

マニフェスト例：

```json
{
  "schema_version": 1,
  "id": "team-review",
  "name": "Team Review",
  "version": "1.0.0",
  "description": "チーム特有のレビュー規則とリポジトリコンテキストツール",
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

### プレースホルダーのサポート

`command`, `args`, `env` では以下のプレースホルダーが使用可能です：
- `${plugin_dir}`: インストール先スナップショットディレクトリの絶対パス。
- `${os}`: OS 名（`linux`, `darwin`, `windows`）。
- `${arch}`: CPU アーキテクチャ（`amd64`, `arm64`）。
- `${exe}`: Windows の場合は `.exe`、Unix 系の場合は空文字列。

### 配布時のパッケージング

Linux/macOS で実行可能ファイルやスクリプトをパッケージ化する際は、アーカイブ作成前に実行権限を付与してください（例: `chmod +x bin/server`）。OCR は tar.gz 展開時にファイルの実行権限ビットを自動的に保持します：

```sh
tar -czf team-review-1.0.0.tar.gz -C my-plugin .
```

---

## セキュリティモデルと整合性保証

1. **インストール時のコード非実行**: `install`, `sync`, `list`, `show`, `dry-run`, `lock`, `preflight` はプラグインコードを実行しません。
2. **決定論的な整合性ハッシュ**:
   - `archive_sha256`: アーカイブファイルの厳密な SHA-256。
   - `content_sha256`: `ocr-package-tree-v1` による全ファイルの決定論的ツリーハッシュ。
3. **実行権限変更の安全ガード**: グローバルプラグイン更新時に MCP 実行設定の変更を検知した場合、`--accept-permissions` による明示的な承認を要求します。
4. **プロジェクト信頼の完全隔離**: 実行信頼はリポジトリ外に保管され、新たにクローンしたプロジェクトが勝手にローカル権限を行使することはありません。
5. **CI ベースコミットのポリシー保護**: GitHub Actions では、プラグイン宣言とロックは常に PR のベースコミット（信頼できる親コミット）から読み込まれ、フォーク元の不正なコードから保護されます。
