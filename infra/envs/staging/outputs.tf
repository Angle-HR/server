output "route53_name_servers" {
  description = "Add these as NS records for var.zone_name in the parent zone, or certificates never validate."
  value       = module.dns.name_servers
}
output "ecr_repository_urls" { value = module.ecr.urls }
output "secret_names" {
  description = "Set a value for each: aws secretsmanager put-secret-value --secret-id <name> --secret-string ..."
  value       = [for k in sort(tolist(local.secret_names)) : "openhr/${local.env}/${k}"] # DSN secrets are written by Terraform and not listed
}
output "rds_address" { value = module.rds.address }
output "frontend_buckets" { value = module.edge.bucket_names }
output "cloudfront_distribution_ids" { value = module.edge.distribution_ids }
output "uploads_buckets" { value = module.uploads.bucket_names }
output "github_deploy_role_arn" { value = module.github_deploy.role_arn }
output "migrate_run_task" {
  description = "Values the CI migrate step needs for aws ecs run-task."
  value = {
    cluster         = aws_ecs_cluster.this.name
    task_definition = module.migrate.task_definition_family
    subnets         = module.network.private_subnet_ids
    security_group  = aws_security_group.app.id
  }
}
output "bastion_instance_id" { value = one(module.bastion[*].instance_id) }
