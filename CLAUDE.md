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
- バージョン: Go 1.25 系の標準機能を積極的に活用すること
- エラーハンドリング: エラーは握りつぶさず、必ずコンテキストを付与してラップすること
  - 例: `fmt.Errorf("failed to get user: %w", err)`
- ログ: 標準の `log` ではなく `log/slog` を使用し、構造化ログとして出力すること。loggerは `slog.Default()` またはDI経由で受け渡すこと
- マジックナンバーの禁止: 状態コードや固定値は、必ず `domain` 層で定数（`const`）として定義すること
- Context伝播: ハンドラ層で `c.Request().Context()` により `context.Context` を取得し、usecase・infrastructure（DBアクセス含む）のすべてのメソッドの第一引数 (`ctx context.Context`) として引き回すこと
  - 用途: トランザクション制御、行ロックのタイムアウト、graceful shutdown、将来のトレーシング基盤への対応
  - 禁止事項: `context.Background()` や `context.TODO()` をリクエストスコープ内で新規生成しないこと（バックグラウンドジョブ等の例外を除く）

# 4. Testing Rules
- テスト手法: 標準の `testing` パッケージを使用し、Table-Driven Testsの形式で記述すること
- テストパッケージ: 外部テストパッケージ形式（`package xxx_test`）を採用し、公開APIのみをテスト対象とすること
- アサーション: `testify/assert` または `testify/require` を使用し、意図を明確にすること（致命的失敗で打ち切るべき箇所は `require` を選択）
- モック生成: `uber-go/mock` を使用すること
- カバレッジ: `usecase` 層のテストは、正常系だけでなく異常系のエッジケースも網羅し、**85%以上のカバレッジを維持すること**

# 5. Database Rules (sqlc + golang-migrate)
- **クエリ生成: `sqlc` を採用**し、`infrastructure` 層から呼ぶこと。GORM等のORMは使用禁止（負荷試験前提のため）
- **マイグレーション管理: `golang-migrate` を採用**し、SQLファイルベースで `up` / `down` を必ずペアで作成すること
- ディレクトリ配置:
  ```
  deployments/mysql/
    schema.sql                # sqlc 用スキーマ定義
    queries/                  # sqlc 用クエリ .sql ファイル群
    migrations/               # golang-migrate 用 .up.sql / .down.sql
  sqlc.yaml                   # sqlc 設定
  internal/infrastructure/mysql/
    sqlc/                     # sqlc 生成コード（コミット対象）
    repository/               # sqlc を呼ぶ Repository 実装
  ```
- **トランザクション制御方針 (A案: usecase主導)**
  - トランザクション境界は **`usecase` 層** で開始・コミット・ロールバックを制御すること
  - `repository` の各メソッドは引数として `*sql.Tx` または共通インターフェース（`DBTX`）を受け取り、トランザクション内外の双方で動作可能にすること
  - `context.Context` への暗黙的な tx 埋め込みは行わないこと（テスト容易性確保のため）
  - 行ロック (`SELECT ... FOR UPDATE`) を使う場面は必ずトランザクション内で実行し、ロック範囲とデッドロック回避順序をコメントで明記すること
- 設計ガイド: `domain` 層は sqlc 生成型に依存させないこと。infrastructure 層で sqlc 型 ⇄ domain 型の変換を行うこと

# 6. Makefile Rules (開発コマンドの統一)
- 以下の操作は必ず `make` 経由で実行すること。`go` コマンドを直接叩いてはならない

| コマンド | 内容 |
|---|---|
| `make run` | サーバ起動（`LOG_LEVEL=info`） |
| `make run/debug` | デバッグレベルで起動（`logs/` にもファイル出力） |
| `make test` | テスト実行（`go test ./...`） |
| `make build` | バイナリビルド（`./bin/api`） |
| `make lint` | 静的解析（`go vet ./...`） |
| `make mock/gen` | モック再生成（`go generate ./...`） |
| `make db/migrate/up` | マイグレーション適用（golang-migrate） |
| `make db/migrate/down` | マイグレーションを1段階ロールバック |
| `make db/sqlc/gen` | sqlc コード再生成 |

# 7. Agentic Behavior (自律実行のルール)
- テスト駆動: コードを生成・修正した後は、`make test` を自動実行し、エラーがなくなるまで自律的にデバッグと修正を繰り返すこと
  - 停止条件: 同一のテスト失敗・コンパイルエラーが3回連続で再発した場合は自律修正を中断し、原因分析と現状をユーザーに報告して指示を仰ぐこと（無限ループ防止）
- 静的解析: テストパス後は `make lint` も実行し、`go vet` の警告がないことを確認すること
- モック更新: インターフェースを変更した場合は `make mock/gen` でモックを再生成してからテストを実行すること
- 破壊的変更の確認: データベースの初期化やファイルの大量削除など、後戻りできない操作をターミナルで実行する前には、必ずユーザーに許可を求めること
- テスト委譲の原則: 以下のタスクは Agent ツール経由で `test-engineer` サブエージェントに委譲すること
  - 新規パッケージ・新規関数に対する単体テストの追加
  - `usecase` 層のカバレッジ向上（正常系・異常系・エッジケースの網羅）
  - インターフェース変更に伴うモック再生成とテスト追従
  - 既存テストの大規模リファクタリング
- 例外: 1〜2行の軽微な修正、コンパイルエラーの即時修復、命名追従などの機械的変更はメインエージェントが直接対応してよい