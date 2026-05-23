variable "name_prefix" {
  description = "リソース名プレフィックス"
  type        = string
}

variable "github_owner" {
  description = "GitHub オーナー名"
  type        = string
}

variable "github_repo" {
  description = "GitHub リポジトリ名"
  type        = string
}

variable "tfstate_bucket_arn" {
  description = "tfstate を置く S3 バケットの ARN（terraform role がアクセスする）"
  type        = string
}

variable "ecr_repository_arns" {
  description = "deploy role が push する ECR リポジトリの ARN リスト"
  type        = list(string)
}

variable "ecs_cluster_arn" {
  description = "deploy role が UpdateService 対象とする ECS クラスタの ARN"
  type        = string
}

variable "aurora_master_secret_arn" {
  description = "tf-plan role が refresh 時に読む Aurora マスター認証情報 Secret の ARN"
  type        = string
}

variable "db_kms_key_arn" {
  description = "Aurora Secret を暗号化する CMK の ARN（GetSecretValue に伴う Decrypt 用）"
  type        = string
}
