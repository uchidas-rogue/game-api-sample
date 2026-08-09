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

* **Phase 0（完了）:** テスト設計原則を `.claude/skills/go-testing-qa/` としてスキル化。指示書間の参照ずれ・実態との乖離を解消
* **Phase 1:** `.golangci.yml` を新設し、Clean Architecture の層間 import 規約（AGENTS.md §1）と Go コーディング規約（§2）を `depguard`（ホワイトリスト方式）・`gochecknoglobals`・`forbidigo` で静的強制する。現状 `make lint` は `go vet` のみ
* **Phase 2:** 生成物（mockgen / sqlc / `schema.sql`）の再生成漏れを `git diff --exit-code` で CI 検知する
* **Phase 3:** 層別カバレッジ目標（§3）を、サマリ表示のみから CI での閾値判定へ格上げする（ラチェット方式で段階的に引き上げ）
* **Phase 4:** `docs/testing/` に設計図（mermaid フローチャート）とテスト仕様表を導入し、Go では計測できない分岐カバレッジをパスカバレッジで代替する

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
  - テスト設計の考え方の正本は `go-testing-qa` スキル、Go 固有の規約は AGENTS.md §3
  - 層別方針（詳細は AGENTS.md §3）:
    - `domain`: 純粋関数・ビジネスルールの単体テスト（外部依存なし、カバレッジ 90% 以上）
    - `usecase`: `uber-go/mock` による網羅的テスト（カバレッジ 85% 以上）
    - `infrastructure`: sqlc `Querier` モック注入 / `miniredis` / `go-sqlmock` による単体テスト（カバレッジ 80% 以上）。`testcontainers-go` による実体テストは未導入
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
