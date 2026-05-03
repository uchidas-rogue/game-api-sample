# Project Overview
- プロジェクト: Go言語とEchoを用いたゲームバックエンドAPI
- アーキテクチャ: Clean Architecture
- インフラ構成: AWS (ECS Fargate, Aurora MySQL, ElastiCache Redis)
- ローカル開発: `deployments/docker-compose.yml` で MySQL を起動し、API は `make run` で動作させる
- 負荷試験: k6

# 1. Communication
- 言語: 日本語でやり取りし、コードコメントも日本語で記述する
- スタイル: 前置きを省き、結論から簡潔に述べる

# 2. Architecture & Design Rules (Clean Architecture)
- 層構成: `handler`(interface) → `usecase` → `domain` の順に内側へ依存し、逆方向の import は禁止
- `infrastructure` 層は `usecase` が定義する interface を実装する。`usecase`/`domain` から `infrastructure` を直接 import してはならない（DI は `internal/di` で行う）
- `domain` 層: ビジネスルールとエンティティのみ。Echo・sqlc・MySQL 等の外部技術に依存しない
- `usecase` 層: ユースケース実装とトランザクション境界制御。リポジトリ等の interface もこの層で定義する
- `handler` 層: HTTP の request/response DTO を定義し、`domain` 型へ変換してから `usecase` に渡す

# 3. Go Coding Standards
- バージョン: Go 1.25。`slices`/`maps` 等の標準パッケージや range-over-int を優先する
- エラーハンドリング:
  - 握りつぶし禁止。コンテキストを付与し `%w` でラップする（例: `fmt.Errorf("get user: %w", err)`）
  - ビジネス上のエラーは `domain` 層で sentinel error または型として定義し、`errors.Is`/`errors.As` で判定する
- ログ: `log/slog` で構造化ログを出力する。logger は DI 経由を原則とし、`slog.Default()` は main/初期化など DI 不可の箇所に限る
- マジックナンバー禁止: ビジネス上の状態コードや業務固定値は `domain` 層で `const` 定義する。各層固有の定数はその層で定義してよい
- Context 伝播: handler で `c.Request().Context()` を取得し、usecase・infrastructure のメソッド第一引数 `ctx context.Context` として引き回す。リクエストスコープ内で `context.Background()`/`context.TODO()` を生成しない（バックグラウンドジョブを除く）
- インターフェース実装検証: 実装型の定義直前に `var _ Iface = (*Type)(nil)` を記述する（値レシーバのみは `Type{}` 可）
- panic/recover: ライブラリコードで panic しない。`recover` は HTTP ミドルウェアでのみ使用する
- goroutine: リクエストスコープで起動する goroutine は `ctx` に連動させ、`errgroup` 等で確実に終了させる
- 時刻: `time.Now()` を直接呼ばず、Clock インターフェースを DI してテスト可能にする
- 設定管理: 環境変数のパースは `configs/config.go` の `Config` に集約する。環境変数のキー名と既定値は同パッケージで `const` 定義し、各層には `*Config` を DI して渡す。新規の設定値追加時は `Config` 構造体・既定値・`Load()` の3箇所を更新する

# 4. Testing Rules (規約)
- 手法: 標準 `testing` + Table-Driven。アサーションは `testify/assert`/`require`（致命的失敗は `require`）
- パッケージ: 原則 `package xxx_test`（外部テスト）。内部実装の検証が必要な場合のみ `package xxx` を併用可
- モック: `uber-go/mock` を使用。配置は対象 interface と同じ層の `mock/` サブディレクトリ（例: `internal/usecase/gacha/mock/`）。`//go:generate` ディレクティブは interface 定義ファイルに記述する
- カバレッジ: `usecase` 層は正常系・異常系・エッジケースを網羅し **85% 以上** を維持
- テスト作業の委譲ルールは §7 を参照（手順は `.claude/agents/test-engineer.md`）

# 5. Database Rules (sqlc + golang-migrate)
- クエリ生成: `sqlc`。型安全・コンパイル時検証のため ORM (GORM 等) は使用しない
- マイグレーション: `golang-migrate` の連番形式で `up`/`down` をペア作成。down が困難な変更（カラム削除等）は事前にユーザーへ確認する
- `schema.sql` は `make db/schema/dump` の生成物。手動編集しない
- トランザクション制御:
  - 境界は `usecase` 層で開始・コミット・ロールバックする。`internal/infrastructure/mysql/transactor.go` の Transactor 経由で制御する
  - repository メソッドは sqlc の `DBTX` を介してクエリを実行し、トランザクション内外の双方で動作する
  - `context.Context` への tx の暗黙的埋め込みは禁止（テスト容易性のため）
  - 行ロック (`SELECT ... FOR UPDATE`) はトランザクション内に限定する
  - 分離レベルは MySQL デフォルト。変更が必要な場合はトランザクション開始時に明示する
- 型変換: `domain` 層は sqlc 生成型に依存しない。`infrastructure` 層で sqlc 型 ⇄ domain 型を変換する
- エラー変換: `sql.ErrNoRows` 等の DB 固有エラーは `infrastructure` 層で domain 層のエラー (例: `ErrNotFound`) に変換する
- コネクション・タイムアウト:
  - プール設定 (`SetMaxOpenConns` 等) は `configs` で設定可能にする
  - クエリには `context.Context` のタイムアウトを設定する（`usecase` 層で付与）
- 新規スキーマ追加手順:
  1. `make db/migrate/new name=xxx` でマイグレーションファイルを作成し、`up`/`down` を記述
  2. `make db/schema/dump` で `schema.sql` を再生成
  3. `deployments/mysql/queries/` にクエリを追加し、`make db/sqlc/gen` で生成コードを更新
  4. `make db/migrate/up` でローカル DB に適用
  5. `internal/infrastructure/mysql/repository/` にリポジトリ実装、`internal/di/container.go` で DI を登録

# 6. Makefile Rules (開発コマンドの統一)
- 日常的な開発操作は `make` 経由で実行する。デバッグ目的のピンポイント実行（`go test -run` 等）は許容
- 利用可能なコマンドは `make help` で確認する（Makefile の `##` コメントが説明として表示される）
- マイグレーションの DSN は `MIGRATE_DSN` 環境変数で上書き可能。ローカル以外の環境では明示的に指定する

# 7. Agentic Behavior (自律実行のルール)
- 自律修正の範囲: コンパイルエラー・型エラー・自明なテスト失敗（モック未更新等）は自律的に修正する
- エスカレーション: 以下は自律で進めず、ユーザーに方針を確認する
  - 新規パッケージ追加・ディレクトリ構造変更を伴う実装
  - 公開インターフェース・ドメインモデルの変更
  - 同一のテスト失敗・コンパイルエラーが3回連続で再発した場合
  - DB の初期化、マイグレーションの down 適用、ファイルの大量削除など後戻りできない操作
- 報告フォーマット: エスカレーション時は「現状」「試したこと」「仮説」「次の選択肢」を簡潔に提示する
- タスク完了時の確認: コード変更を伴うタスクの完了前に `make test` と `make lint` を1回ずつ実行する（変更ごとの逐次実行は不要）
- インターフェース変更時: `make mock/gen` でモックを再生成してからテストを実行する
- テスト作業の委譲: 新規パッケージのテスト追加、`usecase` 層のカバレッジ向上、テスト大規模リファクタリングは `test-engineer` サブエージェントに委譲する。新規実装に伴う最小限のテスト（数ケース程度）はメインエージェントが書いてよい
- git 操作: ユーザーから明示的な指示があるまで commit / push しない
