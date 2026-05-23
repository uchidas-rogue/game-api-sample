terraform {
  required_version = ">= 1.10.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.70"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }

  # Backend は `make tf/init`（terraform init -backend-config=...）で値を注入する想定。
  # 注入する値:
  #   bucket       = "game-api-tfstate-123456789012"
  #   key          = "dev/terraform.tfstate"
  #   region       = "ap-northeast-1"
  #   use_lockfile = true   # state ロックは S3 上の *.tflock で行う（DynamoDB 不要）
  #   encrypt      = true
  backend "s3" {}
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project     = "game-api"
      Environment = var.environment
      ManagedBy   = "terraform"
    }
  }
}
