variable "name_prefix" {
  description = "リソース名プレフィックス"
  type        = string
}

variable "repositories" {
  description = "作成する ECR リポジトリ名のリスト"
  type        = list(string)
}

variable "image_retention_count" {
  description = "ライフサイクルポリシーで残すイメージ数（未タグは別ルール）"
  type        = number
  default     = 30
}

variable "kms_key_arn" {
  description = "ECR イメージ暗号化に使う KMS キー ARN（database モジュールの KMS を流用）"
  type        = string
}
