# AWS インフラ構成図（Phase 2 完了時点）

本ドキュメントは ROADMAP フェーズ2 で構築する AWS インフラの構成・モジュール分割・デプロイフローを mermaid で可視化したもの。

## 全体構成図

```mermaid
flowchart TB
    subgraph Dev["開発者・CI"]
        Dev1["開発者<br/>(Apple Silicon Mac)"]
        GH["GitHub<br/>(uchidas-rogue/game-api-sample)"]
        GHA["GitHub Actions<br/>ci.yml / deploy.yml / terraform.yml"]
    end

    subgraph TFState["Terraform State 管理"]
        S3State["S3 Bucket<br/>tfstate (KMS暗号化, versioning)"]
        DDB["DynamoDB<br/>terraform-lock<br/>(LockID)"]
    end

    subgraph AWS["AWS Account"]
        OIDC["IAM OIDC Provider<br/>token.actions.githubusercontent.com"]
        RoleDeploy["IAM Role<br/>role-deploy<br/>(ECR push + ECS update)"]
        RoleTfPlan["IAM Role<br/>role-terraform-plan"]
        RoleTfApply["IAM Role<br/>role-terraform-apply<br/>(main + manual approval)"]

        ECR["ECR<br/>game-api-api<br/>game-api-batch<br/>game-api-outbox-worker<br/>game-api-migrate"]

        subgraph VPC["VPC 10.0.0.0/16"]
            subgraph PubAZ["Public Subnets (multi-AZ)"]
                ALB["ALB<br/>:80 → :8080"]
                NAT["NAT Gateway"]
            end
            subgraph PrivAZ["Private Subnets (multi-AZ)"]
                subgraph ECS["ECS Cluster (Fargate Graviton/arm64)"]
                    SvcAPI["Service: api<br/>(desiredCount=N)"]
                    SvcWorker["Service: outbox-worker"]
                    TaskBatch["ScheduledTask: batch"]
                    TaskMigrate["RunTask: migrate<br/>(deploy 前に1回)"]
                end
                Aurora[("Aurora MySQL<br/>Serverless v2<br/>Multi-AZ, KMS")]
                Redis[("ElastiCache Redis<br/>cluster mode disabled")]
            end
        end

        Logs["CloudWatch Logs"]
        Secrets["Secrets Manager<br/>(DB password など)"]
    end

    Dev1 -- "git push" --> GH
    GH -- "PR / push main" --> GHA

    GHA -. "OIDC JWT" .-> OIDC
    OIDC -. "AssumeRole" .-> RoleDeploy
    OIDC -. "AssumeRole" .-> RoleTfPlan
    OIDC -. "AssumeRole" .-> RoleTfApply

    GHA -- "terraform plan/apply" --> S3State
    GHA -- "lock" --> DDB
    S3State <-.-> DDB

    GHA -- "docker build/push<br/>(api/batch/worker/migrate)" --> ECR

    GHA -- "RunTask migrate<br/>(deploy 前)" --> TaskMigrate
    GHA -- "UpdateService" --> SvcAPI
    GHA -- "UpdateService" --> SvcWorker

    User["エンドユーザー / k6"] -- "HTTPS" --> ALB
    ALB --> SvcAPI
    SvcAPI --> Aurora
    SvcAPI --> Redis
    SvcWorker --> Aurora
    SvcWorker --> Redis
    TaskBatch --> Aurora
    TaskMigrate --> Aurora

    SvcAPI -.-> Secrets
    SvcWorker -.-> Secrets
    TaskMigrate -.-> Secrets

    SvcAPI -.-> Logs
    SvcWorker -.-> Logs
    TaskBatch -.-> Logs
    TaskMigrate -.-> Logs

    PrivAZ -- "egress" --> NAT
    NAT --> ECR

    classDef aws fill:#FF9900,stroke:#232F3E,color:#000
    classDef state fill:#7AA2F7,stroke:#1F3A8A,color:#000
    classDef ci fill:#22C55E,stroke:#14532D,color:#000
    class ECR,ALB,NAT,Aurora,Redis,Logs,Secrets,SvcAPI,SvcWorker,TaskBatch,TaskMigrate,OIDC,RoleDeploy,RoleTfPlan,RoleTfApply aws
    class S3State,DDB state
    class GH,GHA,Dev1 ci
```

## Terraform モジュール構成

```mermaid
flowchart LR
    subgraph TFRepo["terraform/"]
        subgraph EnvDev["environments/dev/"]
            Main["main.tf<br/>module 呼び出し"]
            Provider["provider.tf<br/>aws + S3/DynamoDB backend"]
        end

        subgraph Modules["modules/"]
            ModNet["network<br/>VPC, Subnet, IGW, NAT, RouteTable, SG<br/>(route table ID を output)"]
            ModDB["database<br/>Aurora MySQL, ElastiCache Redis, KMS<br/>(ElastiCache は可変構造)"]
            ModReg["registry<br/>ECR (for_each = var.repositories)<br/>初期: api/batch/worker/migrate"]
            ModECS["compute_ecs<br/>ECS Cluster, TaskDef(env=リスト), Service, ALB, IAM"]
            ModCI["iam_oidc<br/>OIDC Provider, role-deploy/tf-plan/tf-apply"]
        end
    end

    Main --> ModNet
    Main --> ModDB
    Main --> ModReg
    Main --> ModECS
    Main --> ModCI

    ModECS -. "VPC ID, Subnet IDs" .-> ModNet
    ModDB -. "VPC ID, Subnet IDs, SG IDs" .-> ModNet
    ModECS -. "ECR repository URL" .-> ModReg
    ModECS -. "DB endpoint, Redis endpoint" .-> ModDB

    classDef future fill:#E5E7EB,stroke:#6B7280,color:#374151,stroke-dasharray: 5 5
    ModEKS["compute_eks<br/>(Phase 4 で追加予定)"]:::future
    ModStorage["storage<br/>S3 x 2 + CloudFront<br/>(Phase 5 で追加予定)"]:::future
    ModNet2["+ S3 VPC Gateway Endpoint<br/>(Phase 5 で network に追加)"]:::future

    Main -. "Phase 4" .-> ModEKS
    ModEKS -. "流用" .-> ModNet
    ModEKS -. "流用" .-> ModDB
    ModEKS -. "流用" .-> ModReg

    Main -. "Phase 5" .-> ModStorage
    ModNet -. "Phase 5 で拡張" .-> ModNet2
    ModReg -. "Phase 5: packer 追加" .-> ModReg
```

## 後続フェーズへの前方互換（Phase 2 設計に組み込む配慮）

| 後続フェーズの追加要件 | Phase 2 設計での配慮 |
|---|---|
| **§フェーズ3**: Redis を cache/ranking 2系統に分離（`REDIS_CACHE_ADDR` / `REDIS_RANKING_ADDR`） | ECS TaskDef の env を**リスト構造**で記述し、後から `REDIS_RANKING_ADDR` を追加しても破壊変更にならない形にする。`database` モジュールの ElastiCache も**リスト/可変構造**で定義し、2台目を増やせる構造に |
| **§フェーズ4**: EKS 比較 / App Runner 候補 | `network` / `database` / `registry` を `compute_ecs` から疎結合に保ち、`compute_eks` を別モジュールで追加可能にする。VPC/Subnet は `compute_ecs` が所有しない |
| **§フェーズ5**: マスタデータ配信（S3 + CloudFront、S3 VPC Gateway Endpoint） | `network` モジュールが **route table ID を output として expose**。Phase 5 で `aws_vpc_endpoint`（S3 Gateway, 無料）を**後付け**できる形に。実装は Phase 5 と同 PR で行う（先取りしない） |
| **§フェーズ5**: パッカー用 ECR 追加 | `registry` モジュールは `for_each = var.repositories` でリポジトリ集合を扱い、Phase 5 で `packer` を1行追加できる形に |

## デプロイフロー（時系列）

```mermaid
sequenceDiagram
    autonumber
    participant Dev as 開発者
    participant GH as GitHub
    participant GHA as GitHub Actions
    participant STS as AWS STS
    participant ECR as ECR
    participant ECS as ECS
    participant DB as Aurora

    Dev->>GH: git push (main)
    GH->>GHA: trigger deploy.yml
    GHA->>STS: OIDC JWT で AssumeRole(role-deploy)
    STS-->>GHA: 一時クレデンシャル(数分有効)

    GHA->>GHA: docker buildx build (arm64)<br/>api / batch / worker / migrate
    GHA->>ECR: docker push :sha-xxx

    Note over GHA,ECS: マイグレーション先行実行
    GHA->>ECS: RunTask(migrate, image=:sha-xxx)
    ECS->>DB: golang-migrate up
    DB-->>ECS: schema_migrations 更新
    ECS-->>GHA: Task exit 0

    Note over GHA,ECS: 本体デプロイ
    GHA->>ECS: UpdateService(api, image=:sha-xxx)
    GHA->>ECS: UpdateService(outbox-worker, image=:sha-xxx)
    ECS->>ECS: rolling update (健全性チェック後に旧 task 停止)
    ECS-->>GHA: deployment COMPLETED
```

## 凡例

- **オレンジ系**: AWS リソース
- **青系**: Terraform state（S3 + DynamoDB）
- **緑系**: CI/CD（GitHub）
- **点線**: 認証・参照・依存
- **破線枠（グレー）**: 後続フェーズで追加予定（Phase 4: EKS / Phase 5: storage・S3 VPC Endpoint・packer ECR）

## 対象外（意図的に実装しない）

- **AWS WAF / Shield**（GCP 参考構成の Cloud Armor に相当する層）: 本プロジェクトのエンドユーザーは k6（負荷試験ツール）であり、ALB 前段に WAF を置くとレートベースルール等が k6 リクエストを弾いてフェーズ3 の負荷試験結果が歪む。攻撃耐性の検証は k6 シナリオ側に寄せる方針のため、**今後も導入しない**。詳細は [ROADMAP.md](../ROADMAP.md) フェーズ2「対象外」を参照。

## 関連ドキュメント

- [ROADMAP.md](../ROADMAP.md) — プロジェクト全体ロードマップ
- [AGENTS.md](../AGENTS.md) — コーディング規約
