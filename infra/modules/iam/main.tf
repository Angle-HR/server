# Shared ECS roles. Execution role pulls images and injects secrets; task role is what the app code runs as (S3 access).
variable "name" { type = string }
variable "secret_arns" { type = list(string) }
variable "bucket_arns" { type = list(string) }

data "aws_iam_policy_document" "ecs_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "execution" {
  name               = "${var.name}-ecs-exec"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

resource "aws_iam_role_policy_attachment" "execution" {
  role       = aws_iam_role.execution.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

data "aws_iam_policy_document" "execution_secrets" {
  dynamic "statement" {
    for_each = length(var.secret_arns) > 0 ? [1] : []
    content {
      actions   = ["secretsmanager:GetSecretValue"]
      resources = var.secret_arns
    }
  }
}

resource "aws_iam_role_policy" "execution_secrets" {
  count  = length(var.secret_arns) > 0 ? 1 : 0
  name   = "read-secrets"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.execution_secrets.json
}

resource "aws_iam_role" "task" {
  name               = "${var.name}-ecs-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_assume.json
}

data "aws_iam_policy_document" "task_s3" {
  dynamic "statement" {
    for_each = length(var.bucket_arns) > 0 ? [1] : []
    content {
      actions   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"]
      resources = [for a in var.bucket_arns : "${a}/*"]
    }
  }
  dynamic "statement" {
    for_each = length(var.bucket_arns) > 0 ? [1] : []
    content {
      actions   = ["s3:ListBucket", "s3:ListBucketMultipartUploads"]
      resources = var.bucket_arns
    }
  }
}

resource "aws_iam_role_policy" "task_s3" {
  count  = length(var.bucket_arns) > 0 ? 1 : 0
  name   = "uploads-buckets"
  role   = aws_iam_role.task.id
  policy = data.aws_iam_policy_document.task_s3.json
}

output "execution_role_arn" { value = aws_iam_role.execution.arn }
output "task_role_arn" { value = aws_iam_role.task.arn }
