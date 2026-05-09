# Project Overview
- プロジェクト: Go言語とEchoを用いたゲームバックエンドAPI
- アーキテクチャ: Clean Architecture

# 1. Communication
- 言語: 日本語でやり取りする
- スタイル: 前置きを省き、結論から簡潔に述べる
- 略語: 初出時のみ正式名称（必要なら和訳）を併記する。例: `YAGNI = "You Aren't Gonna Need It"（それ、たぶん要らないよ）`。同一会話内の2回目以降は略称のみで可
- 専門用語: 初出時のみ簡単な解説（1行程度）を併記する。例: `冪等性（同じ操作を複数回実行しても結果が同じになる性質）`。同一会話内の2回目以降は用語のみで可

# 2. Architecture & Design Rules (Clean Architecture)
- 層構成: `driver`(interface adapters; HTTP handler / batch / worker 等の delivery) → `usecase` → `domain` の順に内側へ依存し、逆方向の import は禁止
- `driver` 配下は配信形式ごとにサブディレクトリを切る（`internal/driver/http`, `internal/driver/batch`, `internal/driver/worker`）。新規 driver（gRPC, SQS consumer 等）を追加する際もこの配下に配置する
- `infrastructure` 層は `usecase` が定義する interface を実装する。`usecase`/`domain` から `infrastructure` を直接 import してはならない（DI は `internal/di` で行う）
- `domain` 層: ビジネスルールとエンティティのみ。Echo・sqlc・MySQL 等の外部技術および sqlc 生成型に依存しない
- `usecase` 層: ユースケース実装とトランザクション境界制御。リポジトリ等の interface もこの層で定義する
- `driver/http` 層: HTTP の request/response DTO を定義し、`domain` 型へ変換してから `usecase` に渡す。`driver/batch` / `driver/worker` も同様に外部入力（cron 起動・outbox イベント等）を `usecase` 呼び出しに変換する責務を持つ

# 3. Go Coding Standards
- エラーハンドリング: ビジネス上のエラーは `domain` 層で sentinel error または型として定義し、`errors.Is`/`errors.As` で判定する
- ログ: `log/slog` で構造化ログを出力する。logger は DI 経由を原則とし、`slog.Default()` は main/初期化など DI 不可の箇所に限る
  - DI 対象のコンストラクタ（`NewXxx`）は logger を必須引数とし、nil チェック・`slog.Default()` フォールバックを実装しない
  - エラーをログに含める際は `slog.Any("error", err)` を使用する（`slog.String("error", err.Error())` は使わない）
- マジックナンバー禁止: ビジネス上の状態コードや業務固定値は `domain` 層で `const` 定義する。各層固有の定数はその層で定義してよい
- Context 伝播: batch/worker などバックグラウンドジョブのエントリポイントを除き、リクエストスコープ内で `context.Background()`/`context.TODO()` を生成しない
- インターフェース実装検証: 実装型の定義直前に `var _ Iface = (*Type)(nil)` を記述する（値レシーバのみは `Type{}` 可）
- 時刻: `time.Now()` を直接呼ばず、Clock インターフェースを DI してテスト可能にする
- 設定管理: 環境変数のパースは `configs/config.go` の `Config` に集約する。環境変数のキー名と既定値は同パッケージで `const` 定義し、各層には `*Config` を DI して渡す。新規の設定値追加時は `Config` 構造体・既定値・`Load()` の3箇所を更新する

# 4. Testing Rules (規約)
- アサーション: `testify/assert`/`require`（致命的失敗は `require`）
- モック: `uber-go/mock` を使用。配置は対象 interface と同じ層の `mock/` サブディレクトリ（例: `internal/usecase/gacha/mock/`）。`//go:generate` ディレクティブは interface 定義ファイルに記述する
- 層別カバレッジ・テスト方針:
  - `domain` 層: 純粋関数・ビジネスルール（確率計算、エンティティ不変条件、sentinel error 判定 等）の単体テストを必須化。外部依存（DB/Redis/HTTP）禁止、モック不要。カバレッジ **90% 以上**
  - `usecase` 層: 正常系・異常系・エッジケースを網羅。カバレッジ **85% 以上** を維持
  - `infrastructure` 層: `testcontainers-go` を用いた MySQL/Redis 実体テストを基本方針とする（`miniredis` / `go-sqlmock` は補助的位置付け）。検証対象は sqlc 型 ⇄ domain 型の変換、エラー変換（`sql.ErrNoRows` → `domain.ErrNotFound` 等）、`Transactor` のコミット/ロールバック挙動。カバレッジ **80% 以上**
- Race / 競合テスト:
  - 更新系の repository / usecase は `t.Parallel()` + 複数 goroutine による同時アクセスケースを最低1件持つ
  - race 検出は `make test/race` で実行し CI で必須化（ローカル `make test` は race 無しで高速実行を維持）
  - 単体テストは race 検出と最小同時実行に責務を絞り、規模負荷は k6 側に寄せる
- テスト作業の委譲ルールは §7 を参照（手順は `.claude/agents/test-engineer.md`）

# 5. Database Rules (sqlc + golang-migrate)
- クエリ生成: `sqlc`。型安全・コンパイル時検証のため ORM (GORM 等) は使用しない
- マイグレーション: `golang-migrate` の連番形式で `up`/`down` をペア作成。down が困難な変更（カラム削除等）は事前にユーザーへ確認する
- `schema.sql` は `make db/schema/dump` の生成物。手動編集しない
- トランザクション制御:
  - 境界は `usecase` 層で開始・コミット・ロールバックする。`internal/infrastructure/mysql/transactor.go` の Transactor 経由で制御する
  - repository メソッドは sqlc の `DBTX` を介してクエリを実行し、トランザクション内外の双方で動作する
  - `context.Context` への tx の暗黙的埋め込みは禁止（テスト容易性のため）
  - 分離レベルは MySQL デフォルト。変更が必要な場合はトランザクション開始時に明示する
- 型変換: `infrastructure` 層で sqlc 型 ⇄ domain 型を変換する
- エラー変換: `sql.ErrNoRows` 等の DB 固有エラーは `infrastructure` 層で domain 層のエラー (例: `ErrNotFound`) に変換する
- プール設定 (`SetMaxOpenConns` 等) は `configs` で設定可能にする
- 新規スキーマ追加手順: `make db/migrate/new` → `up`/`down` 記述 → `make db/schema/dump` → `deployments/mysql/queries/` にクエリ追加 → `make db/sqlc/gen` → `make db/migrate/up` → `internal/infrastructure/mysql/repository/` に実装、`internal/di/container.go` で DI 登録

# 6. Makefile Rules (開発コマンドの統一)
- 日常的な開発操作は `make` 経由で実行する。デバッグ目的のピンポイント実行（`go test -run` 等）は許容
- 利用可能なコマンドは `make help` で確認する（Makefile の `##` コメントが説明として表示される）
- マイグレーションの DSN は `MIGRATE_DSN` 環境変数で上書き可能。ローカル以外の環境では明示的に指定する
- `make test` / `make test/race` は自動生成パッケージ（`**/mock`, `**/sqlc`）を除外対象とする。新規生成系パッケージを追加する際はディレクトリ名を `mock` / `sqlc` に揃えるか、`Makefile` の `TEST_PKGS` フィルタを更新する

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
