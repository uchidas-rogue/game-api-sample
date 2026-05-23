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

variable "aurora_cluster_endpoint" {
  description = "Aurora ライターエンドポイント"
  type        = string
}

variable "aurora_database_name" {
  description = "Aurora の初期データベース名"
  type        = string
}

variable "aurora_master_secret_arn" {
  description = "Aurora マスター認証情報 Secret の ARN"
  type        = string
}

variable "db_kms_key_arn" {
  description = "Aurora Secret を暗号化する CMK の ARN（GetSecretValue に伴う Decrypt 用）"
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
