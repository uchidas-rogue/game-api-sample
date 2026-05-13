variable "name_prefix" {
  description = "リソース名プレフィックス（例: game-api-dev）"
  type        = string
}

variable "vpc_cidr" {
  description = "VPC の CIDR ブロック"
  type        = string
}

variable "azs" {
  description = "利用する AZ のリスト（2 以上）"
  type        = list(string)
}
