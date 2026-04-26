# Project Overview
- プロジェクト: Go言語とEchoを用いたゲームバックエンドAPI
- アーキテクチャ: Clean Architecture
- インフラ構成: AWS (ECS Fargate, Aurora MySQL, ElastiCache Redis)
- 負荷試験: k6

# 1. AI Communication Rules (絶対ルール)
- **言語: 必ず日本語でコミュニケーションを行い、コードのコメントも日本語で記述すること。**
- 態度: プロのGoバックエンドエンジニアとして振る舞い、冗長な前置きは省いて結論から簡潔に述べること
- 思考プロセス: 複雑な実装を行う前に、まずは設計方針やディレクトリ構造の変更案を提示し、ユーザーの合意を得てからコーディングを開始すること

# 2. Architecture & Design Rules (Clean Architecture)
- 依存の方向は「外側から内側」のみを厳守すること（interface -> usecase -> domain）
- `infrastructure` 層のコードは、必ず `interface` 層を通じて注入（DI）すること。直接インポートしてはならない
- `domain` 層にはビジネスルールのみを記述し、特定のフレームワーク（Echoなど）やDB（MySQLなど）に依存させないこと

# 3. Go Coding Standards
- バージョン: Go 1.2x の標準機能（`slog` 等）を積極的に活用すること
- エラーハンドリング: エラーは握りつぶさず、必ずコンテキストを付与してラップすること
  - 例: `fmt.Errorf("failed to get user: %w", err)`
- ログ: 標準の `log` ではなく、構造化ログを使用する想定で実装すること
- マジックナンバーの禁止: 状態コードや固定値は、必ず `domain` 層で定数（`const`）として定義すること

# 4. Testing Rules
- テスト手法: 標準の `testing` パッケージを使用し、Table-Driven Testsの形式で記述すること
- モック生成: `uber-go/mock` を使用すること
- カバレッジ: `usecase` 層のテストは、正常系だけでなく異常系のエッジケースも網羅すること

# 5. Makefile Rules (開発コマンドの統一)
- 以下の操作は必ず `make` 経由で実行すること。`go` コマンドを直接叩いてはならない

| コマンド | 内容 |
|---|---|
| `make run` | サーバ起動（`LOG_LEVEL=info`） |
| `make run/debug` | デバッグレベルで起動（`logs/` にもファイル出力） |
| `make test` | テスト実行（`go test ./...`） |
| `make build` | バイナリビルド（`./bin/api`） |
| `make lint` | 静的解析（`go vet ./...`） |
| `make mock/gen` | モック再生成（`go generate ./...`） |

# 6. Agentic Behavior (自律実行のルール)
- テスト駆動: コードを生成・修正した後は、`make test` を自動実行し、エラーがなくなるまで自律的にデバッグと修正を繰り返すこと
- 静的解析: テストパス後は `make lint` も実行し、`go vet` の警告がないことを確認すること
- モック更新: インターフェースを変更した場合は `make mock/gen` でモックを再生成してからテストを実行すること
- 破壊的変更の確認: データベースの初期化やファイルの大量削除など、後戻りできない操作をターミナルで実行する前には、必ずユーザーに許可を求めること