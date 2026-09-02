# Project Overview
- プロジェクト: Go言語とEchoを用いたゲームバックエンドAPI
- アーキテクチャ: Clean Architecture
- AWS インフラ構成・Terraform モジュール分割・CI/CD ワークフローの安全装置は [terraform/ARCHITECTURE.md](terraform/ARCHITECTURE.md) を参照

# 指示書の読み手と置き場
**本ファイルが全 AI エージェント共通の入口。** 規約の正本はすべて本ファイルか、本ファイルからリンクされた `docs/` 配下にある。

- `AGENTS.md`（本ファイル）・`docs/**` — **全エージェント共通**。ここに正本を置く
- `CLAUDE.md`・`.claude/**`（skills / commands / agents / hooks） — **Claude Code 専用**。他のエージェントからは読めないので、**ここに規約の正本を置かない**。置いてよいのは Claude 固有の動作（発火条件・サブエージェント手順・hook）と、共通の正本への索引だけ
- 規約を追加するときは、まず「全エージェントが従うべきか」を判断する。従うべきなら本ファイルか `docs/` 配下に書き、`.claude/**` からはリンクする

# Infrastructure Change Rules
- Terraform 関連の変更時は **影響範囲を terraform/ 配下に限定せず**、必ず以下を併せて確認・更新する:
  - `terraform/**`（モジュール・環境定義）
  - `.github/workflows/terraform.yml`, `.github/workflows/deploy.yml`（CI/CD で terraform を実行する箇所）
  - `make/terraform.mk`, ルート `Makefile`（ローカル実行コマンド・変数）
  - `terraform/ARCHITECTURE.md`（構成図・解説）
- とくに backend 設定（bucket / key / ロック方式等）・IAM ロール ARN・terraform バージョン・リソース名は、上記ファイル間で値や前提が一致している必要がある。片方だけ変更しないこと
- **IAM ロールを新規追加する変更は CI の apply では通らない**。`tf_apply` の permissions boundary が boundary 未継承の `iam:CreateRole` を拒否するため、マージ後に一度だけ人手でローカル apply する必要がある。放置するとデプロイ全体が止まる。手順と理由の正本は [terraform/ARCHITECTURE.md](terraform/ARCHITECTURE.md)「新規 IAM ロールを追加する変更は初回だけ人手で apply する」

# 1. Architecture & Design Rules (Clean Architecture)
- 本節と §2 の規約のうち機械判定できるものは `.golangci.yml` で強制している（`make lint`）。規約を追加・変更したら、同ファイルの判定も併せて更新する。新しい外部ライブラリを使う場合は `depguard` の該当層の `allow` に理由付きで追加する（`//nolint` で個別に抜け道を作らない）
- 層構成: `driver`(interface adapters; HTTP handler / batch / worker 等の delivery) → `usecase` → `domain` の順に内側へ依存し、逆方向の import は禁止
- `driver` 配下は配信形式ごとにサブディレクトリを切る（`internal/driver/http`, `internal/driver/grpc`, `internal/driver/batch`, `internal/driver/worker`）。新規 driver（SQS consumer 等）を追加する際もこの配下に配置する
  - 配信形式のディレクトリ直下には `.go` を置かず、必ずさらにサブディレクトリを切る（`internal/driver/grpc/ranking` 等）。`internal/driver/grpc` 直下に置くとパッケージ名 `grpc` が `google.golang.org/grpc` と衝突する
  - protoc の生成物は `internal/driver/grpc/gen/` に置く。生成物は lint 対象外（`.golangci.yml` の `exclusions.paths`）かつテスト対象外（`.testignore`）で、この2つと `make/proto.mk` の `PROTO_ARTIFACT_PATHS` は**3点セットで更新する**（片方だけ直すと、カバレッジ分母に生成物が混ざるか drift 検知が効かなくなる）
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
  - 検証用の var には **var 自身の説明**（「X が Y を満たすことをコンパイル時に検証する」）を書く。**型の説明は型の直前に置く**。これを取り違えると型の doc コメントが `go doc` に出なくなる
  - 判定: 配置は `scripts/archcheck` の `CheckIfaceAssert`（リポジトリ全体の `.go` の AST を検査）が強制する。他パッケージの型を対象にした assertion は「直前」が構造的に定義できないため対象外で、`internal/di` のコンポジションルート用ブロックがこれにあたる（そこは配線漏れがビルドエラーになることで担保される）
  - 「assertion が無い型」の検出は対象外。判定に `go/types` の型解決が必要で、DI 用でない偶然の実装まで拾うため
- HTTP ミドルウェアの登録順: 観測系（`RequestID` → アクセスログ）を**先頭**に登録し、リクエストを短絡しうるミドルウェア（ボディ上限・認証・レート制限等）は必ずその**後ろ**に置く。前に置くと、弾いたリクエストがアクセスログにも request_id にも残らず、拒否されている事実を運用側から観測できなくなる
  - `echo.Echo.Pre` は使わない。`Pre` はアクセスログより外側で完結するため、上の順序を迂回してしまう。ルーティング前の書き換えが必要になったら、まず本規約の見直しから行う
  - 判定: 登録順は `TestNew_MiddlewareOrder`（`New` の AST を検査）、`Pre` の不使用は `.golangci.yml` の ruleguard が強制する
- gRPC インターセプタの登録順: 上の HTTP ミドルウェアと**同じ規約**が適用される。`grpc.ChainUnaryInterceptor` / `ChainStreamInterceptor` は先に渡したものが最外側になるため、観測系（RequestID → アクセスログ）を先頭に置き、recover 等は後ろに置く。unary と stream で同じ並びにする
  - 単数形の `grpc.UnaryInterceptor` / `grpc.StreamInterceptor` は使わない。Chain と併用すると片方が黙って無視されるうえ、AST 検査は Chain しか見ないため順序規約に穴が空く（`echo.Echo.Pre` を禁じているのと同じ理由）
  - 判定: 登録順は `TestNewGRPC_UnaryInterceptorOrder` / `TestNewGRPC_StreamInterceptorOrder`（`NewGRPC` の AST を検査）、単数形の不使用は `.golangci.yml` の ruleguard が強制する
- gRPC の graceful shutdown: `grpc.Server.GracefulStop()` は**進行中のストリームが終わるまでブロックする**。server streaming を持つ以上、これを単純に呼ぶと SIGTERM でシャットダウンが完了しない。「配信側へ停止を伝える → `GracefulStop` をタイムアウト付きで待つ → 超過したら `Stop`」の三段構えにする（実装は `internal/infrastructure/server/grpc.go`）
- 時刻: **業務ロジックでは** `time.Now()` を直接呼ばず、Clock インターフェースを DI してテスト可能にする
  - 例外は**観測用の経過時間計測**（アクセスログの latency 等）で、`infrastructure` 層に限り直接呼んでよい。計測値そのものをテストで固定する必要が無く、Clock を通しても検証内容が変わらないため。`.golangci.yml` の forbidigo も同じ範囲（`^internal/infrastructure/`）を除外しており、下の照合はその範囲を除いた層を見ている
  - 現時点で Clock インターフェースの実装は存在しない（時刻に依存する**業務ロジック**がまだ無いため）。時刻依存の機能を追加する際に、`usecase` 層へ interface を定義し `infrastructure` 層に実装を置いて DI する
    <!-- ssot-assert: absent-grep 'time\.Now\(\)' internal/domain internal/usecase internal/driver cmd configs --include=*.go --exclude=*_test.go -->
- 設定管理: 環境変数のパースは `configs/config.go` の `Config` に集約する。環境変数のキー名と既定値は同パッケージで `const` 定義し、各層には `*Config` を DI して渡す。新規の設定値追加時は `Config` 構造体・既定値・`Load()` の3箇所を更新する

# 3. Testing Rules (規約)
本節が定めるのは **Go 固有の閾値・配置・命名規約** のみ。設計の考え方と運用手順は他ファイルが正本なので、下表から**直接**引く（`go-testing-qa` スキルの `SKILL.md` を中継させない）。

| 問い | 正本 |
| --- | --- |
| 良いテストとは何か（テーブル駆動の形・モック対象の選択・境界値の取捨・カバレッジ指標の重み付け） | [docs/testing/principles/testing-principles.md](docs/testing/principles/testing-principles.md) |
| どの順で書き、いつ完了とみなすか（図→テスト→実装・改善ループ・縮約の罠） | [docs/testing/principles/tdd-workflow.md](docs/testing/principles/tdd-workflow.md) |
| 層別のテスト観点（Domain / UseCase / Repository / Driver / DI） | [docs/testing/principles/layered-architecture-testing.md](docs/testing/principles/layered-architecture-testing.md) §12 の逆引き表 |
| 静的検証（lint ルール）を足すときの設計原則 | [docs/testing/principles/deterministic-verification.md](docs/testing/principles/deterministic-verification.md) |
| `docs/testing/` に何をどう書くか（作成対象の判定・仕様表の列・並び順・保守トリガ・チェックリスト） | [docs/testing/README.md](docs/testing/README.md) |
| 層別カバレッジ閾値 | 本節（実行可能な写しが `scripts/coverage-check.sh`） |
| Go / 本リポジトリ固有の差分（言語機能による代替・分岐カバレッジが取れない制約） | `.claude/skills/go-testing-qa/SKILL.md`（**Claude Code 専用**。他エージェントは上の原則ファイルを直接読む） |

- テスト設計文書: `usecase` / `driver` の新規実装・分岐変更は、**実装より先に** `docs/testing/<機能>.md` にフローチャートとテスト仕様表を作る（順序は 図 → 失敗するテスト → 実装）。`domain` の純粋関数・値オブジェクトは図を作らない。作成対象の判定・仕様表の形・図の保守トリガ・レビューチェックリストは [docs/testing/README.md](docs/testing/README.md) が正本
  - 仕様表とテストコードの対応づけ（表の直前の `<!-- testcases: ... -->` と、テスト側の `// #<番号> <図のパス>` マーカー）は**必須**。`scripts/doccheck` が行数・ケース順・図の終端ノードの網羅を突合し、`make test` で落とす。書き方の正本は [docs/testing/README.md](docs/testing/README.md) §3
- API レスポンス契約: `driver/http` のレスポンス構造は `internal/driver/http/testdata/contracts/*.json` を正本とし、`internal/testutil/apicontract` で**構造のみ**を検証する（値の妥当性はハンドラのテストの責務）。json タグの追加・削除・リネームをしたら同じ PR で契約ファイルも更新する。これはレスポンスを解釈する側（k6 シナリオ等）の変更が必要になるサイン
- gRPC の契約: `driver/grpc` のレスポンス構造は `.proto` **自体が正本**なので、HTTP のような契約 JSON の写しは作らない。代わりに3点で担保する——命名は `make proto/lint`、wire 互換は `make proto/breaking`（フィールド番号の再利用・型変更・削除を検出）、pb と domain のフィールド集合の対応は `internal/driver/grpc/ranking/contract_test.go`（`protoreflect` で突合し、domain に足したフィールドを proto へ写し忘れると落ちる）
- アサーション: `testify/assert`/`require`（致命的失敗は `require`）
- モック: `uber-go/mock` を使用。配置は対象 interface と同じ層の `mock/` サブディレクトリ（例: `internal/usecase/gacha/mock/`）。`//go:generate` ディレクティブは interface 定義ファイルに記述する
  - 生成物は手動編集しない。更新は `make mock/gen` の再実行のみ。**再生成が必要かを事前に判断しなくてよい**（判断を誤っても `make gen/check` が再生成 + 差分検知で捕まえる。CI 必須）。interface を触ったら迷わず `make mock/gen` を実行する
  - mockgen のバージョンは go.mod の `go.uber.org/mock` から導出され、`make mock/gen` が `./bin` へ同じ版を導入する。`//go:generate` は裸の `mockgen` を呼ぶため、PATH 上の別バージョンを使うと生成結果が変わる。`go generate ./...` を直接叩かず必ず `make mock/gen` を使うこと
  - `//go:generate` の `-destination` / `-package` を変更した場合のみ、`make/app.mk` の `GEN_ARTIFACT_PATHS` と `.golangci.yml` の除外パス、`.testignore` も併せて更新する（この連動は `gen/check` では検知できない）
- 層別カバレッジ・テスト方針:
  - 指標は **文（statement）カバレッジ**。閾値は `make test/cover/check` が判定し、CI で必須（未達なら失敗）。閾値の実体は `scripts/coverage-check.sh` にあり、本節がその正本。**片方だけ変えない**
    - 閾値を設定した層が**1文も計測されていない場合も失敗**にする（`計測なし` → NG）。層のディレクトリを改名した・`.testignore` を広げすぎた・その層のテストが1本も走っていない、のいずれでも閾値がまるごと無効化されるため。以前は SKIP として通しており、`internal/domain` の計測行を消しても CI が緑になった
  - Go には分岐カバレッジを出す標準手段が無い。分岐の網羅は `docs/testing/` の設計図に対する**パスカバレッジ**で代替する（[docs/testing/README.md](docs/testing/README.md) §4）
  - `driver` 層: 変換とエラー経路の網羅。カバレッジ **90% 以上**
  - `domain` 層: 純粋関数・ビジネスルール（確率計算、エンティティ不変条件、sentinel error 判定 等）の単体テストを必須化。外部依存（DB/Redis/HTTP）禁止、モック不要。カバレッジ **100%**（外部依存が無く達成可能なため。スラックが無いので、未カバーが出たらテストを足すか、テスト対象外である理由を明記して `.testignore` で除外する）
  - `usecase` 層: 正常系・異常系・エッジケースを網羅。カバレッジ **90% 以上** を維持
  - `infrastructure` 層: 実リソースを起動しない単体テストを基本方針とする。MySQL repository は sqlc 生成の `Querier` インターフェースのモックを `export_test.go` のテスト専用コンストラクタ経由で注入、Redis は `miniredis`、`Transactor` は `go-sqlmock` を使う。検証対象は sqlc 型 ⇄ domain 型の変換、エラー変換（`sql.ErrNoRows` → `domain.ErrNotFound` 等）、クエリ構築引数（`FOR UPDATE` の有無・絞り込み条件）、`Transactor` のコミット/ロールバック挙動。カバレッジ **80% 以上**
    - `testcontainers-go` による実 DB/Redis テストは未導入。導入する場合は依存追加を含めて別タスクとして扱い、実装前にユーザーへ確認する
      <!-- ssot-assert: absent-grep 'testcontainers' go.mod -->
  - `configs`: 環境変数のパースのみで外部依存が無い。カバレッジ **90% 以上**。設定値を追加したら、既定値・パース失敗・値域違反（`0`・負値）の各分岐にテストを足す。同型の検証ブロックが並ぶため、**片方だけテストがある状態にしない**（対称性チェック）
    - 環境変数をクリアするリストは `configs/config_test.go` の `clearConfigEnv` 1箇所に集約してある。設定値を追加したらここにも追加する（漏れると実行環境の環境変数に結果が左右される）
- Race / 競合テスト:
  - 更新系の repository / usecase は `t.Parallel()` + 複数 goroutine による同時アクセスケースを最低1件持つ
  - race 検出は `make test/race` で実行し CI で必須化（ローカル `make test` は race 無しで高速実行を維持）
  - 単体テストは race 検出と最小同時実行に責務を絞り、規模負荷は k6 側に寄せる
- Flaky 防止:
  - 時刻: SUT と test で `time.Now()` を二重呼出しない (Clock DI または SUT 記録値を export_test.go 経由参照)。日付/月境界を assertion に含めない
  - 同期: `time.Sleep` ポーリングは最終手段。channel/sync・ライブラリ getter (内部ロック付き) 優先。`time.After(N)` は CI 高負荷時の余裕を考慮し N = ローカル想定 x 2~3
  - 並行: テストコードでパッケージスコープの変数を**書き換えない**（要素への書込みを含む）。`make lint` の ruleguard が検出する。`t.Parallel()` の有無を条件にしていないのは、今は無くても後から付けた瞬間に競合が生まれるため。違反したら `//nolint` で抜けず、フィクスチャを関数化する／`t.Setenv`・`t.Chdir` を使う／serial に分離する のいずれかを取る。宣言そのものは対象外（読み取り専用フィクスチャは可）

# 4. Database Rules (sqlc + golang-migrate)
- クエリ生成: `sqlc`。型安全・コンパイル時検証のため ORM (GORM 等) は使用しない
  - 例外: sqlc が生成できない**可変行数の複数行 INSERT**（bulk upsert 等、負荷対策でトランザクション内の DB 往復を集約する用途）は、`infrastructure` 層に限りプレースホルダを組み立てた**パラメータ化生 SQL**で発行してよい（値の文字列連結は禁止）。tx から生の `sqlc.DBTX` を取り出す seam（`repository/factory.go` の `execerFactory`）経由で実行し、go-sqlmock でクエリ・引数を検証する。ORM 導入は引き続き禁止
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
- 指示書の整合チェックは `make docs/check`（CI 必須）。規約の正本が1箇所に保たれているか、記述が実態と乖離していないかを判定する。判定の実体は `scripts/docs-ssot-check.sh`。`make docs/ssot` で本ファイル §3 の正本表を一覧できる
  - 検査を追加するときは、必ず「その検査が捕捉する既知の実例」をスクリプトのコメントに書く。実例を伴わない検査は誤検出源になり、いずれ無効化されるため
  - 「現時点では〜が無い」のような時点依存の記述には、実態を照合する `ssot-assert` ディレクティブを同じ行か直後の行に添える（記法の正本は `scripts/docs-ssot-check.sh` 冒頭のコメント）。機械照合が原理的に不可能な場合は `manual '<理由>'` で理由を残す。添えないと `make docs/check` が WARN を出す
  - ERROR は実態と矛盾している記述（必ず直す）。WARN は将来腐る形をしている記述（放置するなら、なぜ機械照合できないかを該当箇所に残す）
- Protocol Buffers の生成は `make proto/gen`（Go）と `make proto/gen/csharp`（Unity 向け C#）。`buf` を直接叩かない。buf のバージョンは `.buf-version`、protoc プラグインは `go.mod`（protoc-gen-go）と `.protoc-gen-go-grpc-version` で固定し、`make proto/gen` が同じ版を `./bin` へ導入する
  - C# 生成は buf の remote plugin を使うためネットワークが要る。CI の必須ゲートに外部サービスへの到達性を持ち込まないよう Go 生成と分離してあり、drift 検知（`make proto/gen/check`）は Go 生成物の差分と、`.proto` の FileDescriptorSet ダイジェストの突合で行う。既知の検出漏れは `make/proto.mk` のコメントを参照
- 静的解析は `make lint`（golangci-lint、全件）。`make lint/diff` は `origin/main` からの差分のみ、`make lint/fix` は自動修正可能な指摘の修正。golangci-lint のバージョンは `.golangci-version` の1箇所で管理し、`make lint` が同じ版を `./bin` へ導入する（ローカルと CI で同一の判定になるようにするため。この2つを別々に指定しない）
- AWS インフラの初回構築は `make tf/bootstrap`（state 保管先の S3/DynamoDB 作成）→ `make tf/init` → `terraform plan`/`apply` の順。詳細は [terraform/ARCHITECTURE.md](terraform/ARCHITECTURE.md)
- ポートフォリオサイト（`web/`）の知識源は `make site/gen` で文書から生成する。取り込み対象の正本は `scripts/sitegen` の `rootDocs()` と `docs/**` で、ここに一覧を写さない（写すと片方だけ古くなる。実際 `CLAUDE.md` が取り込み対象なのに一覧から漏れていた）。対象の文書を変更したら再生成が要る（サイト専用の写しを作らないための仕組み）。再生成漏れは `make site/check` が検知し、CI と `.github/workflows/pages.yml` が公開前に実行する
  - サイトのトップページ（`web/index.html`）が `README.md` の実測値を引用している箇所は、`ssot-assert: present-grep` ディレクティブで正本と照合している（`make docs/check` が html も走査する）。数値を引用するときは同じ形でディレクティブを添える。付け忘れは検知できないので、これが唯一の担保になる。カード本文の散文は手書きで機械判定の対象外——線引きの正本は [web/README.md](web/README.md)
  - ブラウザとプロキシが共有する上限値（参照チャンク数・質問文の長さ）は `scripts/sitegen` の const が正本で、索引に載せて両方に読ませる。2つのランタイムのどちらかに数字を直接書かない
  - チャットの中継（`web/worker/`）は Cloudflare Workers へ**手動デプロイ**する。索引は Worker にもバンドルされるため、文書を変えたら「`make site/gen` → 再デプロイ」までやらないとサイトと Worker で ID がずれる。手順は [web/worker/README.md](web/worker/README.md)
- コードレビュー用の差分取得は `make review/diff`（生成物を除外。範囲は `REVIEW_PATHS='internal/driver'` のように絞る）。全体像は `make review/diff/stat`、対象ファイル一覧は `make review/files`。比較元は `REVIEW_BASE`（既定 `origin/main`）
  - **`gh pr diff` を直接使わない。** `gh pr diff` はパス絞り込みができず、生成物が差分の過半を占めると Bash の読み取り窓（既定 30,000 文字・上限 150,000）が生成物で埋まり、実装コードに1行も到達しないままレビューが終わる。実際 PR #26 でこれが起き、レビューが 0 件のコメントで終了した
  - 生成物の判定はパス列挙ではなく生成マーカー（`DO NOT EDIT`）で行う。パスで持つと本節と §1・§3 に散る除外リストへ整合を機械判定できないまま1つ足すことになるため。同じ理由でマーカー判定を選んだ前例が `scripts/archcheck` にある。マーカーを持てない `web/data/index.json`（JSON にコメント構文が無い）だけ `make/review.mk` で名指しする
  - `.github/workflows/claude-code-review.yml` はこの3ターゲットを許可ツールに列挙し、`fetch-depth: 0` で base を解決できるようにしている。ターゲット名を変えたら同ファイルも直す
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
- カバレッジ未達時の改善ループ（**本項がプロジェクト規約としての正本**。他の指示書は本項を参照するだけにする。一般原則は [docs/testing/principles/tdd-workflow.md](docs/testing/principles/tdd-workflow.md) §11）: `make test/cover/check` が閾値未達で落ちた場合、未カバー箇所を埋めることを自己目的化しない
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
  - 指示書（本ファイル・`CLAUDE.md`・`ROADMAP.md`・`docs/**`・`.claude/**`）を変更した場合は `make docs/check` も実行する
  - `deployments/mysql/**` または `sqlc.yaml` を変更した場合は `make db/gen/check` も実行する
  - テストまたはテスト対象コードを追加・変更した場合は `make test/race` → `make test/cover/check` も実行する（`test/cover/check` は `test/race` が生成する `coverage.out` を判定するため、この順序で実行する）
- インターフェース変更時: `make mock/gen` でモックを再生成してからテストを実行する。再生成忘れは `make gen/check` が CI で検知する
- git 操作: ユーザーから明示的な指示があるまで commit / push しない
