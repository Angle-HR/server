output "route53_name_servers" {
  description = "Set these as the nameservers at the domain registrar for var.zone_name."
  value       = module.dns.name_servers
}
output "ecr_repository_urls" { value = module.ecr.urls }
output "secret_names" {
  description = "Set a value for each: aws secretsmanager put-secret-value --secret-id <name> --secret-string ... (DSN secrets are written by Terraform and not listed)"
  value       = [for k in sort(tolist(local.secret_names)) : "openhr/${local.env}/${k}"]
}
output "database_endpoints" {
  value = merge(
    { global_and_uk = module.rds.address },
    { for k, m in local.sat : k => m.rds_address },
  )
}
output "uploads_buckets" { value = local.bucket_names }
output "clamav_addresses" { value = local.clamav_addr }
output "cloudfront_distribution_ids" { value = module.edge.distribution_ids }
output "github_deploy_role_arn" { value = module.github_deploy.role_arn }
output "frontend_deploy_role_arns" {
  description = "AWS_DEPLOY_ROLE_ARN for the production environment of each frontend repo"
  value       = { for k, m in module.frontend_deploy : k => m.role_arn }
}
output "migrate_run_task" {
  description = "Values the CI migrate step needs for aws ecs run-task."
  value = {
    cluster         = aws_ecs_cluster.this.name
    task_definition = module.migrate.task_definition_family
    subnets         = module.network.private_subnet_ids
    security_group  = aws_security_group.app.id
  }
}
