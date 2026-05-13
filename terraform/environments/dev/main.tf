data "aws_caller_identity" "current" {}

locals {
  name_prefix = "${var.project}-${var.environment}"

  tfstate_bucket_arn = "arn:aws:s3:::game-api-tfstate-${data.aws_caller_identity.current.account_id}"
  tflock_table_arn   = "arn:aws:dynamodb:${var.region}:${data.aws_caller_identity.current.account_id}:table/game-api-tflock-${data.aws_caller_identity.current.account_id}"
}

module "network" {
  source = "../../modules/network"

  name_prefix = local.name_prefix
  vpc_cidr    = var.vpc_cidr
  azs         = var.azs
}

module "database" {
  source = "../../modules/database"

  name_prefix              = local.name_prefix
  subnet_ids               = module.network.private_data_subnet_ids
  db_security_group_ids    = [module.network.sg_db_id]
  cache_security_group_ids = [module.network.sg_cache_id]

  aurora_master_username    = var.aurora_master_username
  aurora_database_name      = var.aurora_database_name
  aurora_serverless_min_acu = var.aurora_serverless_min_acu
  aurora_serverless_max_acu = var.aurora_serverless_max_acu
}

module "registry" {
  source = "../../modules/registry"

  name_prefix  = local.name_prefix
  repositories = var.ecr_repositories
  kms_key_arn  = module.database.kms_key_arn
}

module "compute_ecs" {
  source = "../../modules/compute_ecs"

  name_prefix = local.name_prefix
  region      = var.region

  vpc_id                 = module.network.vpc_id
  public_subnet_ids      = module.network.public_subnet_ids
  app_subnet_ids         = module.network.private_app_subnet_ids
  alb_security_group_ids = [module.network.sg_alb_id]
  app_security_group_ids = [module.network.sg_app_id]

  repository_urls = module.registry.repository_urls
  image_tags = {
    api             = var.api_image_tag
    batch           = var.batch_image_tag
    "outbox-worker" = var.worker_image_tag
    migrate         = var.api_image_tag
  }

  aurora_cluster_endpoint  = module.database.aurora_cluster_endpoint
  aurora_database_name     = module.database.aurora_database_name
  aurora_master_secret_arn = module.database.aurora_master_secret_arn
  redis_endpoints          = module.database.redis_endpoints

  api_desired_count    = var.api_desired_count
  worker_desired_count = var.worker_desired_count
}

module "iam_oidc" {
  source = "../../modules/iam_oidc"

  name_prefix = local.name_prefix

  github_owner = var.github_owner
  github_repo  = var.github_repo

  tfstate_bucket_arn  = local.tfstate_bucket_arn
  tflock_table_arn    = local.tflock_table_arn
  ecr_repository_arns = values(module.registry.repository_arns)
  ecs_cluster_arn     = module.compute_ecs.cluster_arn
}
