# Terraform plan 作成リソース一覧（mermaid）

`terraform plan` が `will be created` と報告した **71 リソース**を、モジュール別・依存関係付きで可視化したもの。
すべて新規作成（`+ create`）の状態。

## モジュール別リソース数

| モジュール | リソース数 | 主な内容 |
|---|---|---|
| `network` | 22 | VPC / Subnet x6 / IGW / EIP / NAT / RT x2 / RTAssoc x6 / SG x4 |
| `database` | 13 | Aurora MySQL / ElastiCache Redis / KMS / Secrets |
| `compute_ecs` | 18 | ECS Cluster / TaskDef x4 / Service x2 / ALB / IAM |
| `registry` | 8 | ECR Repository x4 / Lifecycle Policy x4 |
| `iam_oidc` | 10 | OIDC Provider / IAM Role x3 / Policy 群 |
| 合計 | **71** | SG ingress/egress は SG リソースにインライン定義（別リソース化されない） |

## network モジュール（VPC 基盤）

```mermaid
flowchart TB
    VPC["aws_vpc.this<br/>10.0.0.0/16"]
    IGW["aws_internet_gateway.this"]
    EIP["aws_eip.nat"]
    NAT["aws_nat_gateway.this<br/>(public-1a に集約)"]

    subgraph Subnets["aws_subnet (6)"]
        Pub["public[1a/1c]"]
        PApp["private_app[1a/1c]"]
        PData["private_data[1a/1c]"]
    end

    subgraph RT["Route Table"]
        RTPub["route_table.public<br/>→ IGW"]
        RTPriv["route_table.private<br/>→ NAT"]
    end

    subgraph Assoc["route_table_association (6)"]
        APub["public x2"]
        AApp["private_app x2"]
        AData["private_data x2"]
    end

    subgraph SG["aws_security_group (4)"]
        SGAlb["alb :80 ← 0.0.0.0/0"]
        SGApp["app :8080 ← alb"]
        SGDb["db :3306 ← app"]
        SGCache["cache :6379 ← app"]
    end

    VPC --> IGW
    VPC --> Subnets
    VPC --> SG
    IGW --> EIP --> NAT
    Pub --> NAT
    IGW --> RTPub
    NAT --> RTPriv
    Pub --> APub --> RTPub
    PApp --> AApp --> RTPriv
    PData --> AData --> RTPriv
    SGAlb --> SGApp --> SGDb
    SGApp --> SGCache
```

## database モジュール（Aurora / Redis）

```mermaid
flowchart TB
    KMS["aws_kms_key.this<br/>(rotation 有効)"]
    KMSAlias["aws_kms_alias.this<br/>alias/...-db"]

    subgraph SM["Secrets Manager"]
        Secret["secretsmanager_secret.aurora_master"]
        SecretVer["secretsmanager_secret_version.aurora_master"]
        Pass["random_password.aurora<br/>(length 32)"]
    end

    subgraph AuroraGrp["Aurora MySQL Serverless v2"]
        DBSubnet["db_subnet_group.aurora"]
        CPG["rds_cluster_parameter_group.aurora<br/>utf8mb4 / Asia-Tokyo"]
        Cluster["rds_cluster.aurora<br/>(暗号化, backup 7日)"]
        Inst0["rds_cluster_instance.aurora[0]"]
        Inst1["rds_cluster_instance.aurora[1]"]
    end

    subgraph RedisGrp["ElastiCache Redis"]
        RSubnet["elasticache_subnet_group.redis"]
        RPG["elasticache_parameter_group.redis<br/>family redis7"]
        RRepl["elasticache_replication_group.this[cache]<br/>engine 7.1, failover 無効"]
    end

    KMS --> KMSAlias
    KMS --> Secret
    KMS --> Cluster
    KMS --> Inst0
    KMS --> Inst1
    KMS --> RRepl
    Pass --> SecretVer
    Pass --> Cluster
    Secret --> SecretVer
    DBSubnet --> Cluster
    CPG --> Cluster
    Cluster --> Inst0
    Cluster --> Inst1
    RSubnet --> RRepl
    RPG --> RRepl
```

## compute_ecs モジュール（ECS / ALB / IAM）

```mermaid
flowchart TB
    subgraph IAM["IAM (Task)"]
        RoleExec["iam_role.task_execution"]
        RoleTask["iam_role.task"]
        ExecManaged["role_policy_attachment.task_execution_managed<br/>(ECSTaskExecutionRolePolicy)"]
        ExecSecrets["iam_role_policy.task_execution_secrets<br/>(secretsmanager:GetSecretValue + kms:Decrypt)"]
    end

    Cluster["aws_ecs_cluster.this<br/>(containerInsights)"]

    subgraph Logs["CloudWatch Log Group (4)"]
        LG["log_group.this[api/batch/<br/>outbox-worker/migrate]"]
    end

    subgraph ALB["ALB"]
        LB["aws_lb.this :80"]
        TG["lb_target_group.api :8080<br/>health /health"]
        Listener["lb_listener.http :80 → TG"]
    end

    subgraph TaskDef["Task Definition (4, Fargate ARM64)"]
        TDApi["task_definition.api"]
        TDWorker["task_definition.outbox_worker"]
        TDBatch["task_definition.batch"]
        TDMigrate["task_definition.migrate"]
    end

    subgraph Svc["ECS Service (2)"]
        SvcApi["ecs_service.api<br/>(ALB 接続, circuit breaker)"]
        SvcWorker["ecs_service.outbox_worker<br/>(circuit breaker)"]
    end

    RoleExec --> ExecManaged
    RoleExec --> ExecSecrets
    RoleExec --> TaskDef
    RoleTask --> TaskDef
    LG --> TaskDef
    LB --> Listener
    TG --> Listener
    Cluster --> SvcApi
    Cluster --> SvcWorker
    TDApi --> SvcApi
    TDWorker --> SvcWorker
    TG --> SvcApi
    Listener -.->|depends_on| SvcApi
```

> `batch` / `migrate` は Service を持たず RunTask 起動（plan には TaskDef のみ作成される）。

## registry モジュール（ECR）

```mermaid
flowchart LR
    subgraph Repos["aws_ecr_repository.this (4, IMMUTABLE + KMS暗号化)"]
        RApi["api"]
        RBatch["batch"]
        RWorker["outbox-worker"]
        RMigrate["migrate"]
    end
    subgraph LC["aws_ecr_lifecycle_policy.this (4)"]
        LApi["api"]
        LBatch["batch"]
        LWorker["outbox-worker"]
        LMigrate["migrate"]
    end
    RApi --> LApi
    RBatch --> LBatch
    RWorker --> LWorker
    RMigrate --> LMigrate
```

## iam_oidc モジュール（GitHub Actions 認証）

```mermaid
flowchart TB
    OIDC["aws_iam_openid_connect_provider.github<br/>token.actions.githubusercontent.com"]

    subgraph Deploy["Role: deploy (main push)"]
        RDeploy["iam_role.deploy"]
        PDeploy["iam_role_policy.deploy<br/>(ECR push + ECS update)"]
    end

    subgraph TfPlan["Role: tf-plan (PR)"]
        RPlan["iam_role.tf_plan"]
        APlanRO["policy_attachment.tf_plan_readonly<br/>(ReadOnlyAccess)"]
        PPlanState["iam_role_policy.tf_plan_state<br/>(tfstate S3 R/W)"]
        PPlanSecret["iam_role_policy.tf_plan_secret_read<br/>(Aurora Secret GetSecretValue + kms:Decrypt)"]
    end

    subgraph TfApply["Role: tf-apply (production-apply env)"]
        RApply["iam_role.tf_apply"]
        AApplyPower["policy_attachment.tf_apply_power<br/>(PowerUserAccess)"]
        AApplyIam["policy_attachment.tf_apply_iam<br/>(IAMFullAccess)"]
        PApplyState["iam_role_policy.tf_apply_state<br/>(tfstate S3 R/W)"]
    end

    OIDC --> RDeploy --> PDeploy
    OIDC --> RPlan
    RPlan --> APlanRO
    RPlan --> PPlanState
    OIDC --> RApply
    RApply --> AApplyPower
    RApply --> AApplyIam
    RApply --> PApplyState
```

## モジュール間の参照（plan 全体の依存構造）

```mermaid
flowchart LR
    NET["network<br/>VPC / Subnet / SG"]
    DB["database<br/>Aurora / Redis / KMS"]
    REG["registry<br/>ECR"]
    ECS["compute_ecs<br/>ECS / ALB / IAM"]
    OIDC["iam_oidc<br/>OIDC / Roles"]

    NET -->|subnet_ids / SG ids| DB
    NET -->|vpc / subnet / SG ids| ECS
    DB -->|KMS key arn| REG
    DB -->|cluster endpoint / secret arn| ECS
    REG -->|repository urls / arns| ECS
    ECS -->|cluster arn| OIDC
    REG -->|repository arns| OIDC
```

## 凡例

- すべてのリソースは `terraform plan` 上で **新規作成（`+ create`）** 状態。
- 実線矢印: リソース間の依存（作成順序の前後関係）。
- 点線 `depends_on`: 明示的な依存指定。
- `[...]`: `for_each` / `count` によるインスタンス（例: `subnet.public[1a]`）。

## 関連ドキュメント

- [terraform/ARCHITECTURE.md](../terraform/ARCHITECTURE.md) — 構成図・モジュール分割・デプロイフロー
