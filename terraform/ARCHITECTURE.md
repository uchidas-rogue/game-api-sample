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
            ModNet["network<br/>VPC, Subnet, IGW, NAT, RouteTable, SG"]
            ModDB["database<br/>Aurora MySQL, ElastiCache Redis, KMS"]
            ModReg["registry<br/>ECR x 4 (api/batch/worker/migrate)"]
            ModECS["compute_ecs<br/>ECS Cluster, TaskDef, Service, ALB, IAM"]
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

    classDef phase4 fill:#E5E7EB,stroke:#6B7280,color:#374151,stroke-dasharray: 5 5
    ModEKS["compute_eks<br/>(Phase 4 で追加予定)"]:::phase4
    Main -. "Phase 4" .-> ModEKS
    ModEKS -. "流用" .-> ModNet
    ModEKS -. "流用" .-> ModDB
    ModEKS -. "流用" .-> ModReg
```

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
- **破線枠**: Phase 4 で追加予定（EKS）

## 対象外（意図的に実装しない）

- **AWS WAF / Shield**（GCP 参考構成の Cloud Armor に相当する層）: 本プロジェクトのエンドユーザーは k6（負荷試験ツール）であり、ALB 前段に WAF を置くとレートベースルール等が k6 リクエストを弾いてフェーズ3 の負荷試験結果が歪む。攻撃耐性の検証は k6 シナリオ側に寄せる方針のため、**今後も導入しない**。詳細は [ROADMAP.md](../ROADMAP.md) フェーズ2「対象外」を参照。

## 関連ドキュメント

- [ROADMAP.md](../ROADMAP.md) — プロジェクト全体ロードマップ
- [AGENTS.md](../AGENTS.md) — コーディング規約
