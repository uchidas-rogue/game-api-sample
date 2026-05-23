output "alb_dns_name" {
  description = "ALB DNS 名（HTTP 経由でアクセス可）"
  value       = module.compute_ecs.alb_dns_name
}

output "ecs_cluster_name" {
  value = module.compute_ecs.cluster_name
}

output "ecr_repository_urls" {
  value = module.registry.repository_urls
}

output "aurora_cluster_endpoint" {
  value = module.database.aurora_cluster_endpoint
}

output "redis_endpoints" {
  value = module.database.redis_endpoints
}

output "deploy_role_arn" {
  description = "GitHub Actions deploy.yml で AssumeRole する Role ARN"
  value       = module.iam_oidc.deploy_role_arn
}

output "tf_plan_role_arn" {
  value = module.iam_oidc.tf_plan_role_arn
}

output "tf_apply_role_arn" {
  value = module.iam_oidc.tf_apply_role_arn
}
