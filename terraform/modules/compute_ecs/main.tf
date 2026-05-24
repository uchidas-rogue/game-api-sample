data "aws_caller_identity" "current" {}

locals {
  # 共通環境変数。TaskDef ごとに list 形式で結合する想定。
  # Phase 3 で REDIS_RANKING_ADDR を追加する際は redis_endpoints を増やすだけで env リストに増分される。
  common_env = concat(
    [
      { name = "LOG_LEVEL", value = "info" },
    ],
    [
      for k, v in var.redis_endpoints :
      { name = k == "cache" ? "REDIS_ADDR" : "REDIS_${upper(k)}_ADDR", value = "${v}:6379" }
    ],
  )

  # アプリ(api/worker/batch)は config.go が単一の MYSQL_DSN を読む。同梱 secret の
  # app キーから注入する（DSN を secret 化している理由は database モジュール参照）。
  # migrate は別形式のため migrate キーから別途注入する（各 task def 参照）。
  app_secrets = [
    { name = "MYSQL_DSN", valueFrom = "${var.dsn_secret_arn}:app::" },
  ]

  # 全 TaskDef 共通のコンテナセキュリティ設定（コンテナ侵害時の影響範囲を狭めるデプスディフェンス）。
  # readonly root: 現状アプリはローカル書き込みをしないため writable volume は不要。
  # capabilities drop ALL: nonroot 実行かつ待受は 8080(>1024) のため追加 capability は不要。
  container_security = {
    readonlyRootFilesystem = true
    linuxParameters = {
      # add = [] は AWS が自動補完する空フィールド。これを config 側にも明示しないと
      # provider の container_definitions 正規化が一致せず、毎 plan で task def が
      # delete/create（永続差分）になり deploy.yml の precheck を誤検知させる。
      capabilities = { add = [], drop = ["ALL"] }
    }
  }
}

# ---- ECS Cluster ----

resource "aws_ecs_cluster" "this" {
  name = "${var.name_prefix}-cluster"

  setting {
    name  = "containerInsights"
    value = "enabled"
  }
}

# ---- IAM: Task Execution Role (ECR pull, CloudWatch Logs, Secrets Manager) ----

data "aws_iam_policy_document" "ecs_task_assume" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "task_execution" {
  name               = "${var.name_prefix}-ecs-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_task_assume.json
}

resource "aws_iam_role_policy_attachment" "task_execution_managed" {
  role       = aws_iam_role.task_execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "task_execution_secrets" {
  statement {
    effect    = "Allow"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [var.dsn_secret_arn]
  }
  # Secret は CMK 暗号化のため、GetSecretValue には対象鍵への Decrypt が必須。
  statement {
    effect    = "Allow"
    actions   = ["kms:Decrypt"]
    resources = [var.db_kms_key_arn]
  }
}

resource "aws_iam_role_policy" "task_execution_secrets" {
  name   = "secrets-read"
  role   = aws_iam_role.task_execution.id
  policy = data.aws_iam_policy_document.task_execution_secrets.json
}

# ---- IAM: Task Role (アプリ自身の AWS API 呼び出し用、現状は最小) ----

resource "aws_iam_role" "task" {
  name               = "${var.name_prefix}-ecs-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_task_assume.json
}

# ---- CloudWatch Logs ----

resource "aws_cloudwatch_log_group" "this" {
  for_each = toset(["api", "batch", "outbox-worker", "migrate"])

  name              = "/ecs/${var.name_prefix}/${each.value}"
  retention_in_days = var.log_retention_days
}

# ---- ALB ----

resource "aws_lb" "this" {
  name               = "${var.name_prefix}-alb"
  load_balancer_type = "application"
  subnets            = var.public_subnet_ids
  security_groups    = var.alb_security_group_ids

  enable_deletion_protection = false
}

resource "aws_lb_target_group" "api" {
  name        = "${var.name_prefix}-api-tg"
  port        = 8080
  protocol    = "HTTP"
  target_type = "ip"
  vpc_id      = var.vpc_id

  health_check {
    path                = "/healthz"
    interval            = 15
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 3
    matcher             = "200"
  }

  deregistration_delay = 30
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.api.arn
  }
}

# ---- Task Definitions ----

resource "aws_ecs_task_definition" "api" {
  family                   = "${var.name_prefix}-api"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.api_cpu
  memory                   = var.api_memory
  execution_role_arn       = aws_iam_role.task_execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    cpu_architecture        = "ARM64"
    operating_system_family = "LINUX"
  }

  container_definitions = jsonencode([
    merge(local.container_security, {
      name      = "api"
      image     = "${var.repository_urls["api"]}:${var.image_tags["api"]}"
      essential = true
      portMappings = [
        { containerPort = 8080, protocol = "tcp" }
      ]
      environment = concat(local.common_env, [
        { name = "PORT", value = "8080" }
      ])
      secrets = local.app_secrets
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.this["api"].name
          awslogs-region        = var.region
          awslogs-stream-prefix = "ecs"
        }
      }
    })
  ])
}

resource "aws_ecs_task_definition" "outbox_worker" {
  family                   = "${var.name_prefix}-outbox-worker"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.worker_cpu
  memory                   = var.worker_memory
  execution_role_arn       = aws_iam_role.task_execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    cpu_architecture        = "ARM64"
    operating_system_family = "LINUX"
  }

  container_definitions = jsonencode([
    merge(local.container_security, {
      name        = "outbox-worker"
      image       = "${var.repository_urls["outbox-worker"]}:${var.image_tags["outbox-worker"]}"
      essential   = true
      environment = local.common_env
      secrets     = local.app_secrets
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.this["outbox-worker"].name
          awslogs-region        = var.region
          awslogs-stream-prefix = "ecs"
        }
      }
    })
  ])
}

resource "aws_ecs_task_definition" "batch" {
  family                   = "${var.name_prefix}-batch"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.batch_cpu
  memory                   = var.batch_memory
  execution_role_arn       = aws_iam_role.task_execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    cpu_architecture        = "ARM64"
    operating_system_family = "LINUX"
  }

  container_definitions = jsonencode([
    merge(local.container_security, {
      name        = "batch"
      image       = "${var.repository_urls["batch"]}:${var.image_tags["batch"]}"
      essential   = true
      environment = local.common_env
      secrets     = local.app_secrets
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.this["batch"].name
          awslogs-region        = var.region
          awslogs-stream-prefix = "ecs"
        }
      }
    })
  ])
}

resource "aws_ecs_task_definition" "migrate" {
  family                   = "${var.name_prefix}-migrate"
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.migrate_cpu
  memory                   = var.migrate_memory
  execution_role_arn       = aws_iam_role.task_execution.arn
  task_role_arn            = aws_iam_role.task.arn

  runtime_platform {
    cpu_architecture        = "ARM64"
    operating_system_family = "LINUX"
  }

  container_definitions = jsonencode([
    merge(local.container_security, {
      name      = "migrate"
      image     = "${var.repository_urls["migrate"]}:${var.image_tags["migrate"]}"
      essential = true
      # migrate は golang-migrate を実行するのみで MIGRATE_DSN だけ読む（接続先・
      # source は Dockerfile.migrate の ENV と MIGRATE_DSN に集約）。common_env は不要。
      secrets = [
        { name = "MIGRATE_DSN", valueFrom = "${var.dsn_secret_arn}:migrate::" },
      ]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.this["migrate"].name
          awslogs-region        = var.region
          awslogs-stream-prefix = "ecs"
        }
      }
    })
  ])
}

# ---- Services ----

resource "aws_ecs_service" "api" {
  name            = "${var.name_prefix}-api"
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.api.arn
  desired_count   = var.api_desired_count
  launch_type     = "FARGATE"

  network_configuration {
    subnets         = var.app_subnet_ids
    security_groups = var.app_security_group_ids
  }

  load_balancer {
    target_group_arn = aws_lb_target_group.api.arn
    container_name   = "api"
    container_port   = 8080
  }

  deployment_minimum_healthy_percent = 50
  deployment_maximum_percent         = 200

  # 起動失敗・ヘルスチェック失敗が続いたらデプロイを止め、直前の安定版へ自動ロールバックする。
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  lifecycle {
    ignore_changes = [task_definition, desired_count]
  }

  depends_on = [aws_lb_listener.http]
}

resource "aws_ecs_service" "outbox_worker" {
  name            = "${var.name_prefix}-outbox-worker"
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.outbox_worker.arn
  desired_count   = var.worker_desired_count
  launch_type     = "FARGATE"

  network_configuration {
    subnets         = var.app_subnet_ids
    security_groups = var.app_security_group_ids
  }

  # ALB 非接続のため判定材料はタスク起動失敗のみ。起動後のハング検知には
  # task definition のコンテナ healthCheck（別途対応）が前提となる。
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  lifecycle {
    ignore_changes = [task_definition, desired_count]
  }
}

# batch / migrate は Service ではなく RunTask 起動。
# migrate: deploy.yml が ECS サービス更新前に RunTask。
# batch: 回復用。make batch/run または GitHub Actions の手動実行でトリガ（定期実行はしない）。

