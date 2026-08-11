variable "name_prefix" {
  description = "リソース名プレフィックス"
  type        = string
}

variable "region" {
  description = "AWS region (CloudWatch Logs / awslogs driver で使用)"
  type        = string
}

variable "vpc_id" {
  description = "ALB / ターゲットグループを配置する VPC ID"
  type        = string
}

variable "public_subnet_ids" {
  description = "ALB を配置する public subnet ID のリスト"
  type        = list(string)
}

variable "app_subnet_ids" {
  description = "ECS task を配置する private subnet ID のリスト"
  type        = list(string)
}

variable "alb_security_group_ids" {
  description = "ALB に紐付ける Security Group ID"
  type        = list(string)
}

variable "app_security_group_ids" {
  description = "ECS task に紐付ける Security Group ID"
  type        = list(string)
}

variable "repository_urls" {
  description = "ECR リポジトリ URL（registry モジュールの output）"
  type        = map(string)
}

variable "image_tags" {
  description = "イメージタグ（api/batch/outbox-worker/migrate ごと）"
  type        = map(string)
}

variable "dsn_secret_arn" {
  description = "app/migrate 用 DSN を同梱した Secret の ARN（JSON: app / migrate）"
  type        = string
}

variable "db_kms_key_arn" {
  description = "DSN Secret を暗号化する CMK の ARN（GetSecretValue に伴う Decrypt 用）"
  type        = string
}

variable "redis_endpoints" {
  description = "Redis レプリケーショングループ名 → endpoint（Phase 3 で ranking を追加）"
  type        = map(string)
}

variable "api_desired_count" {
  description = "ECS Service api の desired count"
  type        = number
}

variable "worker_desired_count" {
  description = "ECS Service outbox-worker の desired count"
  type        = number
}

variable "api_cpu" {
  type    = number
  default = 256
}

variable "api_memory" {
  type    = number
  default = 512
}

variable "worker_cpu" {
  type    = number
  default = 256
}

variable "worker_memory" {
  type    = number
  default = 512
}

variable "batch_cpu" {
  type    = number
  default = 256
}

variable "batch_memory" {
  type    = number
  default = 512
}

variable "migrate_cpu" {
  type    = number
  default = 256
}

variable "migrate_memory" {
  type    = number
  default = 512
}

variable "log_retention_days" {
  type    = number
  default = 14
}

variable "outbox_gc_schedule_expression" {
  description = "outbox GC の実行スケジュール（EventBridge Scheduler の式。UTC 評価）"
  type        = string
  # 03:00 JST 毎日。日次で足りるのは GC がチャンク削除で1回の実行量に上限が無いため。
  default = "cron(0 18 * * ? *)"
}

variable "outbox_gc_enabled" {
  description = "outbox GC の定期実行を有効にするか（false で schedule を DISABLED にする）"
  type        = bool
  default     = true
}
