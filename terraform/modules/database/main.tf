locals {
  redis_instances_indexed = { for r in var.redis_instances : r.name => r }
}

# ---- KMS (保管時暗号化用、Aurora と Secrets Manager で共用) ----

resource "aws_kms_key" "this" {
  description             = "${var.name_prefix} database encryption key"
  deletion_window_in_days = 7
  enable_key_rotation     = true

  tags = {
    Name = "${var.name_prefix}-kms"
  }
}

resource "aws_kms_alias" "this" {
  name          = "alias/${var.name_prefix}-db"
  target_key_id = aws_kms_key.this.key_id
}

# ---- Aurora MySQL (Serverless v2, Multi-AZ) ----

resource "aws_db_subnet_group" "aurora" {
  name       = "${var.name_prefix}-aurora"
  subnet_ids = var.subnet_ids

  tags = {
    Name = "${var.name_prefix}-aurora-subnet-group"
  }
}

resource "random_password" "aurora" {
  length  = 32
  special = false
}

resource "aws_secretsmanager_secret" "aurora_master" {
  name        = "${var.name_prefix}/aurora/master"
  description = "Aurora master credentials"
  kms_key_id  = aws_kms_key.this.arn

  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "aurora_master" {
  secret_id = aws_secretsmanager_secret.aurora_master.id
  secret_string = jsonencode({
    username = var.aurora_master_username
    password = random_password.aurora.result
  })
}

# アプリ(api/worker/batch)と migrate が読む完全な DSN を Secrets Manager に格納する。
# パスワードは secret のため task def の env では合成できない（ECS は secret を
# 単独 env 値としてしか注入できず文字列補間不可）。terraform が random_password を
# 持つため、ここで完全な DSN を組み立てて secret 化し、task def から valueFrom で注入する。
# app/migrate は DSN 形式が異なる（go-sql-driver native vs golang-migrate URL、
# クエリも parseTime vs multiStatements）ため JSON の2キーに分けて1 secret に同梱する。
locals {
  aurora_dsn_authority = "${var.aurora_master_username}:${random_password.aurora.result}@tcp(${aws_rds_cluster.aurora.endpoint}:3306)/${var.aurora_database_name}"
  app_mysql_dsn        = "${local.aurora_dsn_authority}?parseTime=true&loc=Local"
  migrate_dsn          = "mysql://${local.aurora_dsn_authority}?multiStatements=true"
}

resource "aws_secretsmanager_secret" "dsn" {
  name        = "${var.name_prefix}/aurora/dsn"
  description = "Aurora connection DSNs (app / migrate)"
  kms_key_id  = aws_kms_key.this.arn

  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "dsn" {
  secret_id = aws_secretsmanager_secret.dsn.id
  secret_string = jsonencode({
    app     = local.app_mysql_dsn
    migrate = local.migrate_dsn
  })
}

resource "aws_rds_cluster_parameter_group" "aurora" {
  name        = "${var.name_prefix}-aurora-cpg"
  family      = "aurora-mysql8.0"
  description = "${var.name_prefix} aurora cluster parameter group"

  parameter {
    name  = "character_set_server"
    value = "utf8mb4"
  }

  parameter {
    name  = "collation_server"
    value = "utf8mb4_unicode_ci"
  }

  parameter {
    name  = "time_zone"
    value = "Asia/Tokyo"
  }
}

resource "aws_rds_cluster" "aurora" {
  cluster_identifier              = "${var.name_prefix}-aurora"
  engine                          = "aurora-mysql"
  engine_mode                     = "provisioned"
  engine_version                  = var.aurora_engine_version
  database_name                   = var.aurora_database_name
  master_username                 = var.aurora_master_username
  master_password                 = random_password.aurora.result
  db_subnet_group_name            = aws_db_subnet_group.aurora.name
  vpc_security_group_ids          = var.db_security_group_ids
  db_cluster_parameter_group_name = aws_rds_cluster_parameter_group.aurora.name
  storage_encrypted               = true
  kms_key_id                      = aws_kms_key.this.arn
  backup_retention_period         = 7
  preferred_backup_window         = "17:00-18:00" # JST 02:00-03:00
  skip_final_snapshot             = true

  serverlessv2_scaling_configuration {
    min_capacity = var.aurora_serverless_min_acu
    max_capacity = var.aurora_serverless_max_acu
  }

  enabled_cloudwatch_logs_exports = ["error", "slowquery"]

  lifecycle {
    ignore_changes = [master_password]
  }
}

resource "aws_rds_cluster_instance" "aurora" {
  count = 2

  identifier                      = "${var.name_prefix}-aurora-${count.index}"
  cluster_identifier              = aws_rds_cluster.aurora.id
  instance_class                  = "db.serverless"
  engine                          = aws_rds_cluster.aurora.engine
  engine_version                  = aws_rds_cluster.aurora.engine_version
  db_subnet_group_name            = aws_db_subnet_group.aurora.name
  performance_insights_enabled    = true
  performance_insights_kms_key_id = aws_kms_key.this.arn
}

# ---- ElastiCache Redis ----

resource "aws_elasticache_subnet_group" "redis" {
  name       = "${var.name_prefix}-redis"
  subnet_ids = var.subnet_ids
}

resource "aws_elasticache_parameter_group" "redis" {
  name        = "${var.name_prefix}-redis-pg"
  family      = "redis7"
  description = "${var.name_prefix} redis parameter group"
}

resource "aws_elasticache_replication_group" "this" {
  for_each = local.redis_instances_indexed

  replication_group_id       = "${var.name_prefix}-${each.value.name}"
  description                = "${var.name_prefix} ${each.value.name}"
  engine                     = "redis"
  engine_version             = "7.1"
  node_type                  = each.value.node_type
  num_cache_clusters         = 1
  parameter_group_name       = aws_elasticache_parameter_group.redis.name
  subnet_group_name          = aws_elasticache_subnet_group.redis.name
  security_group_ids         = var.cache_security_group_ids
  port                       = 6379
  at_rest_encryption_enabled = true
  transit_encryption_enabled = false
  kms_key_id                 = aws_kms_key.this.arn
  automatic_failover_enabled = false
  apply_immediately          = true
}
