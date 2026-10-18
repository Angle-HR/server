# Role assumed by GitHub Actions via OIDC: push images, run the migrate task, publish frontends.
variable "name" { type = string }
variable "allowed_subjects" {
  description = "OIDC sub claims, e.g. repo:Angle-HR/server:environment:staging"
  type        = list(string)
}
variable "ecr_repository_arns" { type = list(string) }
variable "cluster_arn" { type = string }
variable "migrate_task_family" { type = string }
variable "pass_role_arns" { type = list(string) }
variable "log_group_arn_pattern" { type = string }
variable "frontend_bucket_arns" {
  type    = list(string)
  default = []
}
variable "distribution_arns" {
  type    = list(string)
  default = []
}

data "aws_region" "current" {}
data "aws_caller_identity" "current" {}

data "aws_iam_openid_connect_provider" "github" {
  url = "https://token.actions.githubusercontent.com"
}

data "aws_iam_policy_document" "assume" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [data.aws_iam_openid_connect_provider.github.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "token.actions.githubusercontent.com:aud"
      values   = ["sts.amazonaws.com"]
    }
    condition {
      test     = "StringLike"
      variable = "token.actions.githubusercontent.com:sub"
      values   = var.allowed_subjects
    }
  }
}

data "aws_iam_policy_document" "perm" {
  statement {
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }
  statement {
    actions = [
      "ecr:BatchCheckLayerAvailability", "ecr:InitiateLayerUpload", "ecr:UploadLayerPart",
      "ecr:CompleteLayerUpload", "ecr:PutImage", "ecr:BatchGetImage", "ecr:GetDownloadUrlForLayer",
    ]
    resources = var.ecr_repository_arns
  }
  statement {
    actions   = ["ecs:RunTask"]
    resources = ["arn:aws:ecs:${data.aws_region.current.name}:${data.aws_caller_identity.current.account_id}:task-definition/${var.migrate_task_family}:*"]
    condition {
      test     = "ArnEquals"
      variable = "ecs:cluster"
      values   = [var.cluster_arn]
    }
  }
  statement {
    actions   = ["ecs:DescribeTasks", "ecs:DescribeTaskDefinition", "ecs:ListTaskDefinitions"]
    resources = ["*"]
  }
  statement {
    actions   = ["iam:PassRole"]
    resources = var.pass_role_arns
  }
  statement {
    actions   = ["logs:GetLogEvents", "logs:DescribeLogStreams"]
    resources = [var.log_group_arn_pattern]
  }
}

# Frontend publishing lives in its own policy so image pushes and migrations work before the edge
# (CloudFront) exists.
data "aws_iam_policy_document" "frontend" {
  dynamic "statement" {
    for_each = length(var.frontend_bucket_arns) > 0 ? [1] : []
    content {
      actions   = ["s3:PutObject", "s3:DeleteObject", "s3:GetObject"]
      resources = [for a in var.frontend_bucket_arns : "${a}/*"]
    }
  }
  dynamic "statement" {
    for_each = length(var.frontend_bucket_arns) > 0 ? [1] : []
    content {
      actions   = ["s3:ListBucket"]
      resources = var.frontend_bucket_arns
    }
  }
  dynamic "statement" {
    for_each = length(var.distribution_arns) > 0 ? [1] : []
    content {
      actions   = ["cloudfront:CreateInvalidation"]
      resources = var.distribution_arns
    }
  }
}

resource "aws_iam_role" "this" {
  name               = "${var.name}-github-deploy"
  assume_role_policy = data.aws_iam_policy_document.assume.json
}

resource "aws_iam_role_policy" "this" {
  name   = "deploy"
  role   = aws_iam_role.this.id
  policy = data.aws_iam_policy_document.perm.json
}

resource "aws_iam_role_policy" "frontend" {
  count  = length(var.frontend_bucket_arns) + length(var.distribution_arns) > 0 ? 1 : 0
  name   = "frontend"
  role   = aws_iam_role.this.id
  policy = data.aws_iam_policy_document.frontend.json
}

output "role_arn" { value = aws_iam_role.this.arn }
