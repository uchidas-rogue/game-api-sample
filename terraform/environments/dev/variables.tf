variable "region" {
  description = "AWS region"
  type        = string
  default     = "ap-northeast-1"
}

variable "environment" {
  description = "環境名（dev / stg / prod）"
  type        = string
  default     = "dev"
}

variable "project" {
  description = "プロジェクト名（リソース名プレフィックスに使用）"
  type        = string
  default     = "game-api"
}

variable "vpc_cidr" {
  description = "VPC の CIDR ブロック"
  type        = string
  default     = "10.0.0.0/16"
}

variable "azs" {
  description = "利用する AZ（マルチ AZ で 2 つ）"
  type        = list(string)
  default     = ["ap-northeast-1a", "ap-northeast-1c"]
}

variable "github_owner" {
  description = "GitHub Actions OIDC で許可するリポジトリのオーナー"
  type        = string
  default     = "uchidas-rogue"
}

variable "github_repo" {
  description = "GitHub Actions OIDC で許可するリポジトリ名"
  type        = string
  default     = "game-api-sample"
}

variable "aurora_master_username" {
  description = "Aurora マスターユーザー名（パスワードは Secrets Manager で自動生成）"
  type        = string
  default     = "admin"
}

variable "aurora_database_name" {
  description = "Aurora の初期データベース名"
  type        = string
  default     = "game_db"
}

variable "aurora_serverless_min_acu" {
  description = "Aurora Serverless v2 の最小 ACU"
  type        = number
  default     = 0.5
}

variable "aurora_serverless_max_acu" {
  description = "Aurora Serverless v2 の最大 ACU"
  type        = number
  default     = 2
}

variable "ecr_repositories" {
  description = "作成する ECR リポジトリ名のリスト（Phase 5 で packer 等を追加可能）"
  type        = list(string)
  default     = ["api", "batch", "outbox-worker", "migrate"]
}

variable "ecr_force_delete" {
  description = "イメージが残っていても ECR リポジトリ削除を許可するか。dev は作り直し前提のため true"
  type        = bool
  default     = true
}

variable "api_desired_count" {
  description = "ECS Service api の desired count"
  type        = number
  default     = 1
}

variable "worker_desired_count" {
  description = "ECS Service outbox-worker の desired count"
  type        = number
  default     = 1
}

variable "api_image_tag" {
  description = "ECS にデプロイする api イメージのタグ（CI が -var で上書きする想定）"
  type        = string
  default     = "latest"
}

variable "batch_image_tag" {
  description = "batch イメージのタグ"
  type        = string
  default     = "latest"
}

variable "worker_image_tag" {
  description = "outbox-worker イメージのタグ"
  type        = string
  default     = "latest"
}
