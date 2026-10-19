variable "name_prefix" { type = string }
variable "repositories" { type = set(string) }
variable "mutable_repositories" {
  description = "Repositories whose tags can be overwritten, e.g. a frontend redeployed by re-pushing :staging"
  type        = set(string)
  default     = []
}
variable "keep_images" {
  type    = number
  default = 30
}

resource "aws_ecr_repository" "this" {
  for_each             = var.repositories
  name                 = "${var.name_prefix}/${each.key}"
  image_tag_mutability = contains(var.mutable_repositories, each.key) ? "MUTABLE" : "IMMUTABLE"
  image_scanning_configuration {
    scan_on_push = true
  }
  encryption_configuration {
    encryption_type = "AES256"
  }
}

resource "aws_ecr_lifecycle_policy" "this" {
  for_each   = aws_ecr_repository.this
  repository = each.value.name
  policy = jsonencode({
    rules = [{
      rulePriority = 1
      description  = "keep last N images"
      selection    = { tagStatus = "any", countType = "imageCountMoreThan", countNumber = var.keep_images }
      action       = { type = "expire" }
    }]
  })
}

output "urls" {
  value = { for k, r in aws_ecr_repository.this : k => r.repository_url }
}
output "arns" {
  value = { for k, r in aws_ecr_repository.this : k => r.arn }
}
