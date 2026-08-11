output "cluster_arn" {
  value = aws_ecs_cluster.this.arn
}

output "cluster_name" {
  value = aws_ecs_cluster.this.name
}

output "alb_dns_name" {
  value = aws_lb.this.dns_name
}

output "service_names" {
  value = {
    api           = aws_ecs_service.api.name
    outbox_worker = aws_ecs_service.outbox_worker.name
  }
}

output "task_definition_families" {
  value = {
    api           = aws_ecs_task_definition.api.family
    outbox_worker = aws_ecs_task_definition.outbox_worker.family
    batch         = aws_ecs_task_definition.batch.family
    outbox_gc     = aws_ecs_task_definition.outbox_gc.family
    migrate       = aws_ecs_task_definition.migrate.family
  }
}

output "outbox_gc_schedule_name" {
  description = "outbox GC の EventBridge Scheduler スケジュール名"
  value       = aws_scheduler_schedule.outbox_gc.name
}

output "task_execution_role_arn" {
  value = aws_iam_role.task_execution.arn
}

output "task_role_arn" {
  value = aws_iam_role.task.arn
}
