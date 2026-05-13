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

  # Backend は `terraform init -backend-config=backend.hcl` で値を注入する想定。
  # backend.hcl の例:
  #   bucket         = "game-api-tfstate-123456789012"
  #   key            = "dev/terraform.tfstate"
  #   region         = "ap-northeast-1"
  #   dynamodb_table = "game-api-tflock-123456789012"
  #   encrypt        = true
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
