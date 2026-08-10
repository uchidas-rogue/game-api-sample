# Project Roadmap: Go × AWS Game API (3 Months)

## プロジェクト概要
- **目的:** 負荷試験とチューニングを前提とした、高パフォーマンスなゲームバックエンドAPIの構築
- **期間:** 3ヶ月（フェーズ1〜3）
- **主要技術:** Go (Echo), Clean Architecture, MySQL (Aurora), Redis (ElastiCache), AWS (ECS Fargate), Terraform, k6
- **AI開発体制 (ハイブリッド戦略):**
  - **Claude Code (Main):** コード実装（TDD）、TerraformによるIaC構築、k6スクリプト作成、自律デバッグ
  - **Gemini (Sub):** コードレビュー（並行処理・DBロックの指摘）、大規模ログ解析（k6の実行結果からボトルネックを特定）

---

## 横断施策：決定論的検証基盤（フェーズをまたぐ / 随時）
**目標:** AGENTS.md に書かれた規約を「AI が読む助言」から「CI で落ちる判定」へ移し、AI に任せられる範囲を広げる。

方針は「ガードレールはコンテキスト（助言）ではなく実行される仕組み（判定）に置く」。フェーズ2の前提作業で整備した CI 品質ゲート（`make lint` / `make test/race` / カバレッジ計測）を、以下の段階で深化させる。

* **Phase 0（完了）:** テスト設計原則を `docs/testing/principles/` に集約し、`go-testing-qa` スキルからは索引のみ参照する形へ。指示書間の参照ずれ・実態との乖離を解消し、全 AI エージェントが同じ原則を読めるようにした
* **Phase 1（完了）:** `.golangci.yml` を新設し、Clean Architecture の層間 import 規約（AGENTS.md §1）と Go コーディング規約（§2）を `depguard`（ホワイトリスト方式）・`gochecknoglobals`・`forbidigo`・`mnd`・`gocritic`(ruleguard) で静的強制。`make lint`
* **Phase 2（完了）:** 生成物（mockgen / sqlc / `schema.sql`）の再生成漏れを `make gen/check` / `make db/gen/check` で CI 検知
* **Phase 3（完了）:** 層別カバレッジ目標（§3）を CI での閾値判定へ格上げ。`make test/cover/check`（判定の実体は `scripts/coverage-check.sh`）
* **Phase 4（完了）:** `docs/testing/` に設計図（mermaid フローチャート）とテスト仕様表を導入し、Go では計測できない分岐カバレッジをパスカバレッジで代替
* **Phase 5（完了）:** 指示書（AGENTS.md / `docs/**` / `.claude/**`）の SSoT 崩れと実態との乖離を `make docs/check` で CI 検知。判定の実体は `scripts/docs-ssot-check.sh`、記述側に置く照合条件は `ssot-assert` ディレクティブ。テスト設計原則を `docs/testing/principles/` へ移し、全 AI エージェントが同じ正本を読める配置にした（`.claude/**` は Claude 専用なので正本を置かない）
* **Phase 6（完了）:** AGENTS.md §3 Flaky 防止「テストコードでパッケージスコープの変数を書き換えない」を `scripts/ruleguard/rules.go` の `testGlobalWrite` で静的強制。`gochecknoglobals` は「宣言」を見るため読み取り専用フィクスチャまで落ちてしまい `_test.go` を除外しており、テストの書換だけが機械検証されない穴になっていた。宣言 = `gochecknoglobals`（本番コード）／書換 = ruleguard（テスト）で役割を分けている
  <!-- ssot-assert: present-grep 'func testGlobalWrite' scripts/ruleguard/rules.go -->
* **次の候補:** 指示書側に残る「機械判定できるのに文章で書いている規約」を継続的に `.golangci.yml` / `scripts/` へ移す（[docs/testing/principles/deterministic-verification.md](docs/testing/principles/deterministic-verification.md) §1）。現在の最有力は AGENTS.md §2「インターフェース実装検証: 実装型の定義直前に `var _ Iface = (*Type)(nil)` を置く」——宣言の有無そのものを見る仕組みが無く、`revive` の doc コメント検査が周辺の誤り（コメントが型ではなく検証用 var に付く）を副次的に拾っているだけ。「型の直前にあるか」は文の並びなので式単位の ruleguard では表現できず、`TestNew_MiddlewareOrder` と同じく AST を直接読む形になる

---

## フェーズ1：GoによるモダンなゲームAPIの実装（1ヶ月目 / ローカル完結）
**目標:** Clean Architectureに基づき、拡張性とテスト耐性を持ったAPIをローカルで完成させる。

* **インフラ準備:** Docker Composeを用いた MySQL 8.0 (TimeZone: Asia/Tokyo, utf8mb4) と Redis の構築
* **アーキテクチャ:** 依存関係を厳格に管理したClean Architecture（`interface` -> `usecase` -> `domain`）
* **コア機能実装:**
  1. **ヘルスチェックAPI:** エンドツーエンドの疎通確認
  2. **10連ガチャAPI:** アイテムマスタの読み込み、確率計算、ユーザー所持品の更新（トランザクションと行ロックを意識した実装）
  3. **スコア送信・ランキングAPI（GvG想定 / CQRS）:**
     - **書き込み（Command）:** プレイヤーが獲得したスコアを加算（累計加算方式）。所属ギルドのスコアにもリアルタイムで合算する
     - **読み取り（Query）:** 個人累計スコアランキングと、ギルド総合スコアランキングの2系統を Redis Sorted Set で提供
     - **設計方針:** 書き込み（個人スコア・ギルド集計）と読み取り（ランキング参照）の責務を分離し、参照負荷が書き込みDBを圧迫しない構成とする
* **テスト戦略 (TDDの徹底):**
  - 標準 `testing` パッケージによる Table-Driven Tests
  - 層別のテスト方針とカバレッジ閾値は **AGENTS.md §3 が正本**（値をここに写さない。判定は `make test/cover/check`）
  - 競合状態（Race Condition）: 更新系には `t.Parallel()` + goroutine 並行ケースを最低1件含め、`make test/race` で検出する
  - Claudeは必ずローカルでテストを実行し、パスするまで実装を修正すること

---

## フェーズ2：AWSインフラの構築とコンテナデプロイ（2ヶ月目）
**目標:** 商用サービスを想定したモダンなインフラ構成を、IaCを用いて構築する。

* **前提作業:** ECS デプロイ前に CI 品質ゲートを整備する（GitHub Actions で `make lint` / `make test/race` / カバレッジ計測 を PR・main push 時に必須化）。この品質ゲートの深化は「横断施策：決定論的検証基盤」を参照
* **IaC化:** Terraformを使用し、AWSリソースをコードで定義
* **主要コンポーネント:**
  - コンテナオーケストレーション: Amazon ECS (Fargate)
  - データベース: Amazon Aurora MySQL
  - キャッシュ: Amazon ElastiCache (Redis)
  - ネットワーク/セキュリティ: VPC設定、IAM最小権限の原則適用、暗号化設定。
* **デプロイ:** 作成したGoアプリケーションのDockerfile（マルチステージビルド）を作成し、ECSへデプロイ
* **コンテナアーキテクチャ:** `linux/arm64`（Fargate Graviton）前提。コスト最適化（同等性能で約 20% 安）と Apple Silicon ローカルとのビルド一致を狙う
* **マイグレーション戦略:** `Dockerfile.migrate`（`golang-migrate` + `deployments/mysql/migrations/` 同梱）を ECS RunTask で先行実行。init container / CI 直叩きは不採用（ECS Fargate に init container 概念が無く、Aurora を private subnet に置く方針と両立しないため）
* **Terraform 構成:** `modules/{network, database, registry, compute_ecs, iam_oidc}` + `environments/dev`。state は S3（暗号化・versioning）、state ロックは `use_lockfile`（同バケットに `*.tflock` を置く方式）。GitHub Actions ↔ AWS は OIDC AssumeRole（`role-deploy` / `role-tf-plan` / `role-tf-apply` の3ロール、apply は `production-apply` environment 経由）
* **前方互換配慮:**
  - TaskDefinition の env をリスト構造で記述 → フェーズ3 で `REDIS_RANKING_ADDR` を破壊変更なしで追加可能
  - `database` モジュールの ElastiCache は `for_each` で可変構造 → フェーズ3 で ranking 用を追加可能
  - `registry` モジュールは `for_each = var.repositories` → フェーズ5 で packer リポジトリを追加可能
  - `network` モジュールは `private_route_table_id` を output → フェーズ5 の S3 VPC Gateway Endpoint を後付け可能
  - `network` / `database` / `registry` は `compute_ecs` から疎結合 → フェーズ4 で `compute_eks` を別モジュールで追加可能
* **対象外（意図的に実装しない）:**
  - **AWS WAF / Shield**: 本プロジェクトはエンドユーザーが k6（負荷試験ツール）であり、WAF を ALB 前段に置くとレートベースルール等が k6 リクエストを弾き、フェーズ3 の負荷試験結果が歪む。攻撃耐性の検証は別途 k6 シナリオ側で行う方針のため、WAF は今後も導入しない（GCP 参考構成の Cloud Armor に相当する層は意図的に省く）

---

## フェーズ3：負荷試験の実施とチューニング（3ヶ月目）
**目標:** 高負荷時のシステムの振る舞いを知り、ボトルネックを解消する

* **負荷試験シナリオ作成:** k6を用いて、フェーズ1で作成したAPI（ガチャ、ランキング）に対する秒間数千リクエストのシナリオを実装・実行
* **ボトルネック特定:** AWS CloudWatchのメトリクスとk6の実行ログをGeminiに投入し、レイテンシ悪化の根本原因（CPUスパイク、DBの排他ロック/デッドロック、メモリ枯渇、N+1問題など）を特定
* **パフォーマンス改善:**
  - データベースのインデックス最適化
  - Redisを用いたキャッシュ戦略の拡張
    - **Redis の cache 用 / ranking 用 分離**（負荷試験で ranking ZSet 参照と outbox Pub/Sub・汎用キャッシュの相互影響が顕在化した場合に対応）:
      - アプリ側: `configs` の Redis アドレスを `REDIS_CACHE_ADDR` / `REDIS_RANKING_ADDR` の2系統に分け、DI（`internal/di/container.go`・各 `cmd/*/main.go`）で `RankingStore` ← ranking、`OutboxNotifier`/`Subscriber`・汎用キャッシュ ← cache へ注入。今は両方同一アドレスを向けるだけで挙動不変（工数 ~0.5日）
      - インフラ側: ElastiCache をもう1レプリケーショングループ追加（`cache.t4g.micro` で +$12〜$25/月目安）、ECS タスク定義の env に `REDIS_RANKING_ADDR` を追加
    - 汎用キャッシュ用途（GET/SET/TTL）の追加。現状 Redis は ZSet（ランキング）と Pub/Sub（outbox 通知）のみで key/value キャッシュは未実装
  - GoのGoroutine/Channelを用いた並行処理のチューニング
* **成果のアウトプット:** ビフォーアフターの数値と設計判断をまとめ、技術記事（Qiita等）として発信する

---

## フェーズ4：Kubernetes (EKS) への移植と比較検証（発展課題）
**目標:** フェーズ2で構築した ECS Fargate 構成と同一の API を EKS 上で動作させ、運用性・拡張性・コスト面の差分を実測・記事化する。

* **インフラ構築 (IaC):**
  - Terraform で EKS クラスタ（コントロールプレーン）と Fargate Profile / マネージドノードグループを構築
  - 主要アドオン: AWS Load Balancer Controller, ExternalDNS, Cluster Autoscaler（または Karpenter）, IRSA (IAM Roles for Service Accounts)
  - Aurora MySQL / ElastiCache Redis はフェーズ2の資産を流用（VPC共有 or ピアリング）
* **アプリケーションのマニフェスト化:**
  - api / batch / outbox-worker をそれぞれ `Deployment` / `CronJob` / `Deployment` として定義
  - Helm または Kustomize でテンプレート管理。環境差分は overlay で吸収
  - Secrets は AWS Secrets Manager + External Secrets Operator で同期
* **オートスケーリング戦略:**
  - api: HPA (CPU/メモリ + ALB リクエスト数)
  - outbox-worker: KEDA でキュー長（未処理 outbox 件数）ベースのスケール
* **GitOps / デプロイ:**
  - Argo CD でマニフェストの宣言的デプロイ。ECR への push を契機に同期
* **比較・検証項目:**
  - 同一 k6 シナリオでの ECS Fargate vs EKS のスループット・p99 レイテンシ・コスト比較
  - スケーリング応答性（スパイク時のスケールアウト時間）
  - 運用工数（マニフェスト量、デプロイ手順、障害対応）
* **比較候補（任意の拡張）:** AWS App Runner（GCP Cloud Run 相当のフルマネージドコンテナ。リクエスト駆動オートスケール、ただし完全なゼロスケールは不可）を3者目の比較対象に加えるか検討。記事ネタとして「ECS Fargate vs EKS vs App Runner」の運用性・コスト・スケール応答性の比較は価値があるが、構築コストとのトレードオフで判断
* **成果のアウトプット:** ECS Fargate と EKS のリアルな差分（コスト・運用性・拡張性）を技術記事として発信

---

## フェーズ5：マスタデータ配信フローの構築（発展課題 / フェーズ1〜3 完了後に着手）
**目標:** ゲーム設定値（マスタデータ）を「DB → 配信用ファイル → クライアント/サーバ」へ流すパイプラインを AWS 上に構築する。admin ツールは本プロジェクトでは実装しないため、パッキング処理は専用バッチで代替する。

* **データフロー（admin 抜きの簡易版）:**
  - パッキング用バッチ（ECS ScheduledTask または RunTask）が Aurora MySQL のマスタデータを読み取り、`gob`（サーバ向け）/ `SQLite`（クライアント向け）ファイルを生成して S3 にアップロード
  - api（Cloud Run 相当の ECS Service）は起動時/更新時に S3 から `gob` を取得してインメモリキャッシュ
  - クライアント向け `SQLite` は CloudFront 経由で配信（クライアントはローカルキャッシュ）
* **インフラ追加（Terraform）:**
  - `storage` モジュール新設: S3 バケット ×2（server master / client master）+ CloudFront ディストリビューション（OAC で S3 を保護）
  - **S3 VPC Gateway Endpoint を `network` モジュールに追加**（無料）— ECS タスクの S3 アクセスを NAT 経由から AWS 内部経路へ切り替え、NAT データ処理料を削減。※このタスクと同じ PR で必ず一緒に入れる
  - パッキング用 ECR リポジトリ追加（または `migrate` イメージに同梱）
  - IAM: api の task role に S3 read、パッカー task role に S3 write + Aurora read を追加
* **アプリ追加:**
  - パッカー: Aurora → gob/SQLite 生成のバッチコマンド（`internal/driver/batch` 配下）
  - ローダー: S3 から gob を取得しインメモリキャッシュする infrastructure 層 repository + DI 配線
* **想定コスト:** S3 ~$1/月、CloudFront 実トラフィック比例（ポートフォリオ規模なら実質無料枠内）、パッキングタスク < $1/月、Gateway Endpoint $0
