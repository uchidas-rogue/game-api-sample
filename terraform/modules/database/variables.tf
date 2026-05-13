variable "name_prefix" {
  description = "リソース名プレフィックス"
  type        = string
}

variable "subnet_ids" {
  description = "Aurora / ElastiCache を配置する private subnet ID のリスト"
  type        = list(string)
}

variable "db_security_group_ids" {
  description = "Aurora に紐付ける Security Group ID"
  type        = list(string)
}

variable "cache_security_group_ids" {
  description = "ElastiCache に紐付ける Security Group ID"
  type        = list(string)
}

variable "aurora_engine_version" {
  description = "Aurora MySQL のエンジンバージョン"
  type        = string
  default     = "8.0.mysql_aurora.3.07.1"
}

variable "aurora_master_username" {
  description = "Aurora マスターユーザー名"
  type        = string
}

variable "aurora_database_name" {
  description = "Aurora の初期データベース名"
  type        = string
}

variable "aurora_serverless_min_acu" {
  description = "Aurora Serverless v2 の最小 ACU"
  type        = number
}

variable "aurora_serverless_max_acu" {
  description = "Aurora Serverless v2 の最大 ACU"
  type        = number
}

variable "redis_instances" {
  description = <<-EOT
    ElastiCache Redis レプリケーショングループの定義リスト。
    Phase 2 は cache 1 つで開始。Phase 3 で ranking 用を追加する想定。
  EOT
  type = list(object({
    name      = string
    node_type = string
  }))
  default = [
    { name = "cache", node_type = "cache.t4g.micro" }
  ]
}
