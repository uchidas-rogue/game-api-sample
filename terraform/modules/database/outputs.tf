output "aurora_cluster_endpoint" {
  value = aws_rds_cluster.aurora.endpoint
}

output "aurora_reader_endpoint" {
  value = aws_rds_cluster.aurora.reader_endpoint
}

output "aurora_database_name" {
  value = aws_rds_cluster.aurora.database_name
}

output "aurora_master_secret_arn" {
  value     = aws_secretsmanager_secret.aurora_master.arn
  sensitive = true
}

output "redis_endpoints" {
  description = "Redis レプリケーショングループ名 → primary endpoint"
  value = {
    for k, rg in aws_elasticache_replication_group.this : k => rg.primary_endpoint_address
  }
}

output "kms_key_arn" {
  value = aws_kms_key.this.arn
}
