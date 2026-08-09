# Project Overview
- プロジェクト: Go言語とEchoを用いたゲームバックエンドAPI
- アーキテクチャ: Clean Architecture
- AWS インフラ構成・Terraform モジュール分割・CI/CD ワークフローの安全装置は [terraform/ARCHITECTURE.md](terraform/ARCHITECTURE.md) を参照

# Infrastructure Change Rules
- Terraform 関連の変更時は **影響範囲を terraform/ 配下に限定せず**、必ず以下を併せて確認・更新する:
  - `terraform/**`（モジュール・環境定義）
  - `.github/workflows/terraform.yml`, `.github/workflows/deploy.yml`（CI/CD で terraform を実行する箇所）
  - `make/terraform.mk`, ルート `Makefile`（ローカル実行コマンド・変数）
  - `terraform/ARCHITECTURE.md`（構成図・解説）
- とくに backend 設定（bucket / key / ロック方式等）・IAM ロール ARN・terraform バージョン・リソース名は、上記ファイル間で値や前提が一致している必要がある。片方だけ変更しないこと

# 1. Architecture & Design Rules (Clean Architecture)
- 本節と §2 の規約のうち機械判定できるものは `.golangci.yml` で強制している（`make lint`）。規約を追加・変更したら、同ファイルの判定も併せて更新する。新しい外部ライブラリを使う場合は `depguard` の該当層の `allow` に理由付きで追加する（`//nolint` で個別に抜け道を作らない）
- 層構成: `driver`(interface adapters; HTTP handler / batch / worker 等の delivery) → `usecase` → `domain` の順に内側へ依存し、逆方向の import は禁止
- `driver` 配下は配信形式ごとにサブディレクトリを切る（`internal/driver/http`, `internal/driver/batch`, `internal/driver/worker`）。新規 driver（gRPC, SQS consumer 等）を追加する際もこの配下に配置する
- `infrastructure` 層は `usecase` が定義する interface を実装する。`usecase`/`domain` から `infrastructure` を直接 import してはならない（DI は `internal/di` で行う）
- `domain` 層: ビジネスルールとエンティティのみ。Echo・sqlc・MySQL 等の外部技術および sqlc 生成型に依存しない
- `usecase` 層: ユースケース実装とトランザクション境界制御。リポジトリ等の interface もこの層で定義する
- `driver/http` 層: HTTP の request/response DTO を定義し、`domain` 型へ変換してから `usecase` に渡す。`driver/batch` / `driver/worker` も同様に外部入力（cron 起動・outbox イベント等）を `usecase` 呼び出しに変換する責務を持つ

# 2. Go Coding Standards
- エラーハンドリング: ビジネス上のエラーは `domain` 層で sentinel error または型として定義し、`errors.Is`/`errors.As` で判定する
- ログ: `log/slog` で構造化ログを出力する。logger は DI 経由を原則とし、`slog.Default()` は main/初期化など DI 不可の箇所に限る
  - DI 対象のコンストラクタ（`NewXxx`）は logger を必須引数とし、nil チェック・`slog.Default()` フォールバックを実装しない
  - エラーをログに含める際は `slog.Any("error", err)` を使用する（`slog.String("error", err.Error())` は使わない）
- マジックナンバー禁止: ビジネス上の状態コードや業務固定値は `domain` 層で `const` 定義する。各層固有の定数はその層で定義してよい
- Context 伝播: batch/worker などバックグラウンドジョブのエントリポイントを除き、リクエストスコープ内で `context.Background()`/`context.TODO()` を生成しない
- インターフェース実装検証: 実装型の定義直前に `var _ Iface = (*Type)(nil)` を記述する（値レシーバのみは `Type{}` 可）
- 時刻: `time.Now()` を直接呼ばず、Clock インターフェースを DI してテスト可能にする
  - 現時点で Clock インターフェースの実装は存在しない（`time.Now()` を使う機能がまだ無いため）。時刻依存の機能を追加する際に、`usecase` 層へ interface を定義し `infrastructure` 層に実装を置いて DI する
- 設定管理: 環境変数のパースは `configs/config.go` の `Config` に集約する。環境変数のキー名と既定値は同パッケージで `const` 定義し、各層には `*Config` を DI して渡す。新規の設定値追加時は `Config` 構造体・既定値・`Load()` の3箇所を更新する

# 3. Testing Rules (規約)
- **テスト設計の考え方の正本は `go-testing-qa` スキル**（`.claude/skills/go-testing-qa/`）。テーブル駆動テストの正しい形、モック対象の選択基準、境界値ケースの取捨、カバレッジ指標の重み付け、設計図とテスト仕様表の作り方はそちらを参照する。本節は Go 固有の閾値・配置・命名規約を定める
- アサーション: `testify/assert`/`require`（致命的失敗は `require`）
- モック: `uber-go/mock` を使用。配置は対象 interface と同じ層の `mock/` サブディレクトリ（例: `internal/usecase/gacha/mock/`）。`//go:generate` ディレクティブは interface 定義ファイルに記述する
  - 生成物は手動編集しない。更新は `make mock/gen` の再実行のみ。再生成忘れ・手動編集は `make gen/check` が検知する（CI 必須）
  - mockgen のバージョンは go.mod の `go.uber.org/mock` から導出され、`make mock/gen` が `./bin` へ同じ版を導入する。`//go:generate` は裸の `mockgen` を呼ぶため、PATH 上の別バージョンを使うと生成結果が変わる。`go generate ./...` を直接叩かず必ず `make mock/gen` を使うこと
  - 再生成が必要かどうかの判断:

    | 変更内容 | 再生成 |
    | --- | --- |
    | interface のメソッド追加・削除・リネーム | 必要 |
    | interface のメソッドのシグネチャ変更（引数・戻り値の型や数） | 必要 |
    | interface のコメント・並び順の変更 | 必要（生成物にコメントが転記されるため） |
    | interface が参照する型の**定義**の変更（フィールド追加等） | 不要（生成物は型名しか持たない） |
    | 実装側（`infrastructure` 等）の内部ロジックのみの変更 | 不要 |
    | `//go:generate` の `-destination` / `-package` の変更 | 必要。併せて `make/app.mk` の `GEN_ARTIFACT_PATHS` と `.golangci.yml` の除外パス、`.testignore` も更新する |
- 層別カバレッジ・テスト方針:
  - 指標は **文（statement）カバレッジ**。閾値は `make test/cover/check` が判定し、CI で必須（未達なら失敗）。閾値の実体は `scripts/coverage-check.sh` にあり、本節がその正本。**片方だけ変えない**
  - Go には分岐カバレッジを出す標準手段が無い。分岐の網羅は `docs/testing/` の設計図に対する**パスカバレッジ**で担保する方針（`go-testing-qa` スキル参照）
  - `driver` 層: 変換とエラー経路の網羅。カバレッジ **90% 以上**
  - `domain` 層: 純粋関数・ビジネスルール（確率計算、エンティティ不変条件、sentinel error 判定 等）の単体テストを必須化。外部依存（DB/Redis/HTTP）禁止、モック不要。カバレッジ **100%**（外部依存が無く達成可能なため。スラックが無いので、未カバーが出たらテストを足すか、テスト対象外である理由を明記して `.testignore` で除外する）
  - `usecase` 層: 正常系・異常系・エッジケースを網羅。カバレッジ **90% 以上** を維持
  - `infrastructure` 層: 実リソースを起動しない単体テストを基本方針とする。MySQL repository は sqlc 生成の `Querier` インターフェースのモックを `export_test.go` のテスト専用コンストラクタ経由で注入、Redis は `miniredis`、`Transactor` は `go-sqlmock` を使う。検証対象は sqlc 型 ⇄ domain 型の変換、エラー変換（`sql.ErrNoRows` → `domain.ErrNotFound` 等）、クエリ構築引数（`FOR UPDATE` の有無・絞り込み条件）、`Transactor` のコミット/ロールバック挙動。カバレッジ **80% 以上**
    - `testcontainers-go` による実 DB/Redis テストは未導入。導入する場合は依存追加を含めて別タスクとして扱い、実装前にユーザーへ確認する
  - `configs`: 環境変数のパースのみで外部依存が無い。カバレッジ **90% 以上**。設定値を追加したら、既定値・パース失敗・値域違反（`0`・負値）の各分岐にテストを足す。同型の検証ブロックが並ぶため、**片方だけテストがある状態にしない**（対称性チェック）
    - 環境変数をクリアするリストは `configs/config_test.go` の `clearConfigEnv` 1箇所に集約してある。設定値を追加したらここにも追加する（漏れると実行環境の環境変数に結果が左右される）
- Race / 競合テスト:
  - 更新系の repository / usecase は `t.Parallel()` + 複数 goroutine による同時アクセスケースを最低1件持つ
  - race 検出は `make test/race` で実行し CI で必須化（ローカル `make test` は race 無しで高速実行を維持）
  - 単体テストは race 検出と最小同時実行に責務を絞り、規模負荷は k6 側に寄せる
- Flaky 防止:
  - 時刻: SUT と test で `time.Now()` を二重呼出しない (Clock DI または SUT 記録値を export_test.go 経由参照)。日付/月境界を assertion に含めない
  - 同期: `time.Sleep` ポーリングは最終手段。channel/sync・ライブラリ getter (内部ロック付き) 優先。`time.After(N)` は CI 高負荷時の余裕を考慮し N = ローカル想定 x 2~3
  - 並行: `t.Parallel()` 配下で pkg-global 書換禁止 (serial 分離)

# 4. Database Rules (sqlc + golang-migrate)
- クエリ生成: `sqlc`。型安全・コンパイル時検証のため ORM (GORM 等) は使用しない
  - sqlc のバージョンは `.sqlc-version` の1箇所で管理し、`make db/sqlc/gen` が同じ版を `./bin` へ導入する。`sqlc generate` を直接叩かない（PATH 上の別バージョンで生成結果が変わる）
  - `schema.sql` と sqlc 生成物の再生成忘れ・手動編集は `make db/gen/check` が検知する（CI 必須）
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

# 5. Makefile Rules (開発コマンドの統一)
- 日常的な開発操作は `make` 経由で実行する。デバッグ目的のピンポイント実行（`go test -run` 等）は許容
- 静的解析は `make lint`（golangci-lint、全件）。`make lint/diff` は `origin/main` からの差分のみ、`make lint/fix` は自動修正可能な指摘の修正。golangci-lint のバージョンは `.golangci-version` の1箇所で管理し、`make lint` が同じ版を `./bin` へ導入する（ローカルと CI で同一の判定になるようにするため。この2つを別々に指定しない）
- AWS インフラの初回構築は `make tf/bootstrap`（state 保管先の S3/DynamoDB 作成）→ `make tf/init` → `terraform plan`/`apply` の順。詳細は [terraform/ARCHITECTURE.md](terraform/ARCHITECTURE.md)
- 利用可能なコマンドは `make help` で確認する（Makefile の `##` コメントが説明として表示される）
- マイグレーションの DSN は `MIGRATE_DSN` 環境変数で上書き可能。ローカル以外の環境では明示的に指定する
- `make test` / `make test/race` は `.testignore` に列挙されたパッケージを除外対象とする。除外理由はファイル内のコメントを参照すること。新規にテスト対象外としたいパッケージが出た場合は、**除外理由をコメントで明記したうえで** `.testignore` にパターンを追加する。逆に interface 定義のみのパッケージへ実装を足す等で除外を解除する場合は、`.testignore` から該当行を削除し §3 のカバレッジ目標に従ってテストを整備する

# 6. Agentic Behavior (自律実行のルール)
- 自律修正の範囲: コンパイルエラー・型エラー・自明なテスト失敗（モック未更新等）は自律的に修正する
- エスカレーション: 以下は自律で進めず、ユーザーに方針を確認する
  - 新規パッケージ追加・ディレクトリ構造変更を伴う実装
  - 公開インターフェース・ドメインモデルの変更
  - 同一のテスト失敗・コンパイルエラーが3回連続で再発した場合
  - DB の初期化、マイグレーションの down 適用、ファイルの大量削除など後戻りできない操作
- カバレッジ未達時の改善ループ: `make test/cover/check` が閾値未達で落ちた場合、未カバー箇所を埋めることを自己目的化しない
  - 未カバー箇所をまず「通常の分岐（到達可能）／エントリポイント等の設計上テスト対象外／デッドコード（到達不可能）／外部要因依存で再現困難」に分類し、**到達可能なものだけ**テストを追加する
  - **過剰モック禁止**: 未カバー行を実行させるためだけの不自然なモック・テストケースを追加しない。デッドコードや設計上の制約をテストで無理にカバーしない
  - デッドコードと判定したものはリファクタ候補として報告する
  - 試行には上限を設ける（下記の「3回連続で再発したら中断」に準じる）。上限に達したら打ち切り、未カバー箇所の内訳（行・分類・理由）を報告して終了する。数値目標の達成を無限に追求しない
- テスト縮約・リファクタ後は必ずカバレッジを再計測する。低下していたら黙って戻さず、縮約方針自体の見直しが必要かを含めて報告し、方針を確認してから修正する
- 報告フォーマット: エスカレーション時は「現状」「試したこと」「仮説」「次の選択肢」を簡潔に提示する
- 想定外の通知: タスク実行中に次のいずれかを検知したら、その時点で応答内に **`【想定外】`** の見出しを付けた目立つブロックを出力し、通常の進捗報告に埋もれさせない
  - エラー・テスト失敗系: 予期しないコンパイル/テスト/ビルドの失敗、想定と異なる実行結果やエラーログ
  - 計画からの逸脱系: 当初の方針・前提が崩れた、想定と異なる実装が必要になった、副作用や影響範囲が事前見積りより大きい
  - ブロック内容: 「何が想定外か」「当初の想定との差分」「影響と次の選択肢」を簡潔に記す
  - 既存のエスカレーション規約に該当する場合は作業を止めて確認する。該当しない場合も、検知した事実は本ルールに従い必ず明示してから作業を続行する
- タスク完了時の確認: コード変更を伴うタスクの完了前に `make lint` / `make gen/check` / `make test` を1回ずつ実行する（変更ごとの逐次実行は不要）
  - `deployments/mysql/**` または `sqlc.yaml` を変更した場合は `make db/gen/check` も実行する
  - テストまたはテスト対象コードを追加・変更した場合は `make test/race` → `make test/cover/check` も実行する（`test/cover/check` は `test/race` が生成する `coverage.out` を判定するため、この順序で実行する）
- インターフェース変更時: `make mock/gen` でモックを再生成してからテストを実行する。再生成忘れは `make gen/check` が CI で検知する
- git 操作: ユーザーから明示的な指示があるまで commit / push しない
