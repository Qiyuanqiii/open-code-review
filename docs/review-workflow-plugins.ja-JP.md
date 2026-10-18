# レビューワークフロープラグイン（Review Workflow Plugins）

パッケージのインストール、実行権限、およびランタイムアダプターの関係については、[ネイティブプラグインのアーキテクチャ設計](plugin-architecture.md) を参照してください。

コードレビューにおいて特定の実行順序や完了基準が必要な場合（例えば、変更点を検査した後に、候補となる指摘を別の段階で再検証し、最終的な完了レポートを作成する場合など）に、**レビューワークフロープラグイン** を使用します。パッケージは各段階を宣言し、モデルからの提出物を検証します。OCR ホストは diff、ルール、ツール、およびトークン予算を提供します。

## single-pass サンプルの実行

Python 3 が `python` として利用可能な環境で、サンプルをインストールし、作業ツリーの変更に対してプレビューを実行します：

```sh
ocr plugin install ./examples/ocr-plugins/single-pass
ocr review --review-plugin single-pass --preview
```

モデルプロバイダーが設定され、レビュー対象の変更がある状態で以下を実行します：

```sh
ocr review --review-plugin single-pass --format json --output review.json
```

このサンプルは 1 つの段階で構成されています。モデルに指摘を求め、OCR が公開する前にプラグインが提出内容を検証します。`review.json` 内の `review_plugin` で完了ステータスとレポートを確認できます。インストールのみではパッケージコードは実行されず、プレビュー（`--preview`）でも Python プロセスやモデルへの問い合わせは行われません。

上記はユーザー単位でインストールされたパッケージを使用しています。リポジトリでプロジェクトプラグインが宣言されている場合は、後述のプロジェクトワークフローを使用して宣言・承認してください。

## プロジェクトでの選択と実行権限

リポジトリで管理されるワークフローの場合は、`ocr plugin add <source>` で宣言し、`.opencodereview/plugins.json` と `plugins.lock.json` をコミットした上で、`ocr plugin sync --frozen` で同期インストールします。`--review-plugin <id>` で選択する前に、ロックされたパッケージを確認して `ocr plugin trust` でローカル実行権限を付与してください。`ocr plugin preflight --json` を実行すると、ワークフローを開始したりモデルを呼び出したりすることなく、インストール内容、互換性、ローカル認証状態を検証できます。プロジェクトプラグインが宣言されている場合、ロックされたセットからのみ選択可能です。詳細は [ネイティブ OCR プラグインガイド](native-plugins.ja-JP.md#プロジェクト単位のプラグイン管理コマンド) を参照してください。

レビューワークフロープラグインは、ステージ計画、プロンプト指示、完了スキーマ、検証、およびステージ間状態を保持します。OCR は diff、ルール、モデル/ツール実行、セッション、トークン計算、コメント処理、出力を担当します。

単一実行でのワークフロー選択：

```sh
ocr review --review-plugin PACKAGE_ID --preview
ocr review --review-plugin PACKAGE_ID --format json --output review.json
```

プラグインのインストールや有効化だけではワークフローは実行されません。`--preview` はマニフェストを読み込むだけで実行ファイルは起動しません。実際のレビュー実行時には、ローカル MCP サーバーと同様にユーザーのローカル権限で実行ファイルが起動されます。`--review-plugin` で明示的に選択した場合、グローバルで無効化されていても該当プラグインのルールと MCP サーバーが読み込まれます。

## マニフェスト構成（Manifest）

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

`command` は実行可能ファイルのパスであり、シェルによる解釈は行われません。コマンドと引数では `${plugin_dir}`、`${os}`、`${arch}`、`${exe}`（Windows は `.exe`、Unix は空）が展開されます。PATH 上の `python` などのランタイムも指定可能です。同梱の Python サンプルは `python` コマンドを想定しています。環境が `python3` のみの場合は、パッケージング前にマニフェストを `python3` に変更してください。

プラグインはレビューワークフロー、ルール、MCP を自由に組み合わせられます。ステージ ID は一意の識別子であり、1〜64 個の順序付きステージを宣言できます。

## stdio-v1 プロトコル仕様

OCR はファイルグループごとに 1 つのプロセスを起動し、リポジトリルートを作業ディレクトリとします。プロセスはそのグループの全ステージを通じて状態を保持します。リクエストとレスポンスは 1 行の UTF-8 JSON オブジェクト（最大 4 MiB）です。各レスポンスは `"version": 1` を含み、診断情報は stderr に出力します。

### ステージ開始（`begin`）

各ステージ開始前に OCR が送信します：

```json
{"version":1,"method":"begin","stage":"inspect","paths":["main.go"],"severities":["critical","high","medium","low"],"categories":["bug","security","performance","maintainability","test","style","documentation","other"]}
```

プラグインはタスクと初期レポートを返します：

```json
{"version":1,"task":{"instructions":"Inspect the selected code.","input":"Check the public return contract.","completion":{"description":"Submit the result.","parameters":{"type":"object","properties":{"checked":{"type":"boolean"}},"required":["checked"]}}},"report":{"pending":["inspection"]}}
```

### モデル提出（`submit`）

モデルが `task_done` を呼び出すと、OCR は引数をそのまま転送します：

```json
{"version":1,"method":"submit","stage":"inspect","arguments":{"checked":true}}
```

提出を受け入れるレスポンスでは `accepted` と `complete` のブール値を返します：

```json
{"version":1,"accepted":true,"complete":true,"report":{"checked":true},"comments":[]}
```

モデルの提出に不備があり修正可能な場合は、`accepted:false`、空でない `feedback`、最新の `report` を返し、comments は空にします。OCR はリクエスト許容量内でモデルにフィードバックを返します。

## 完了状態と出力

宣言されたすべてのステージが完了する必要があります。ユーザーがトークン予算を指定した場合、未使用分は後続のステージで共有されます。

JSON 出力には `review_plugin` 配列が含まれ、各ステージの完了状態とレポートが記録されます。実行後は `ocr session show SESSION_ID --json` でセッション内のレポートを検査できます。未完了のステージがある場合はカバレッジに記録され、非ゼロの終了ステータスとなります。
