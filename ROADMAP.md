# Project Roadmap: Go × AWS Game API (3 Months)

## プロジェクト概要
- **目的:** 負荷試験とチューニングを前提とした、高パフォーマンスなゲームバックエンドAPIの構築
- **期間:** 3ヶ月（フェーズ1〜3）
- **主要技術:** Go (Echo), Clean Architecture, MySQL (Aurora), Redis (ElastiCache), AWS (ECS Fargate), Terraform, k6
- **AI開発体制 (ハイブリッド戦略):**
  - **Claude Code (Main):** コード実装（TDD）、TerraformによるIaC構築、k6スクリプト作成、自律デバッグ
  - **Gemini (Sub):** コードレビュー（並行処理・DBロックの指摘）、大規模ログ解析（k6の実行結果からボトルネックを特定）

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
  - 層別方針（詳細は CLAUDE.md §4）:
    - `domain`: 純粋関数・ビジネスルールの単体テスト（外部依存なし、カバレッジ 90% 以上）
    - `usecase`: `uber-go/mock` による網羅的テスト（カバレッジ 85% 以上）
    - `infrastructure`: `testcontainers-go` を用いた MySQL/Redis 実体テスト（カバレッジ 80% 以上）
  - 競合状態（Race Condition）: 更新系には `t.Parallel()` + goroutine 並行ケースを最低1件含め、`make test/race` で検出する
  - Claudeは必ずローカルでテストを実行し、パスするまで実装を修正すること

---

## フェーズ2：AWSインフラの構築とコンテナデプロイ（2ヶ月目）
**目標:** 商用サービスを想定したモダンなインフラ構成を、IaCを用いて構築する。

* **前提作業:** ECS デプロイ前に CI 品質ゲートを整備する（GitHub Actions で `make lint` / `make test/race` / カバレッジ計測 を PR・main push 時に必須化）
* **IaC化:** Terraformを使用し、AWSリソースをコードで定義
* **主要コンポーネント:**
  - コンテナオーケストレーション: Amazon ECS (Fargate)
  - データベース: Amazon Aurora MySQL
  - キャッシュ: Amazon ElastiCache (Redis)
  - ネットワーク/セキュリティ: VPC設定、IAM最小権限の原則適用、暗号化設定。
* **デプロイ:** 作成したGoアプリケーションのDockerfile（マルチステージビルド）を作成し、ECSへデプロイ

---

## フェーズ3：負荷試験の実施とチューニング（3ヶ月目）
**目標:** 高負荷時のシステムの振る舞いを知り、ボトルネックを解消する

* **負荷試験シナリオ作成:** k6を用いて、フェーズ1で作成したAPI（ガチャ、ランキング）に対する秒間数千リクエストのシナリオを実装・実行
* **ボトルネック特定:** AWS CloudWatchのメトリクスとk6の実行ログをGeminiに投入し、レイテンシ悪化の根本原因（CPUスパイク、DBの排他ロック/デッドロック、メモリ枯渇、N+1問題など）を特定
* **パフォーマンス改善:**
  - データベースのインデックス最適化
  - Redisを用いたキャッシュ戦略の拡張
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
* **成果のアウトプット:** ECS Fargate と EKS のリアルな差分（コスト・運用性・拡張性）を技術記事として発信
