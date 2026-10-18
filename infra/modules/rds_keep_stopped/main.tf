# RDS starts a stopped instance by itself after 7 days, and that cannot be turned off. This stops it again: the
# auto-start (and only the auto-start) emits RDS-EVENT-0154, which starts a small state machine that waits until the
# instance is available and stops it. Manual starts (scripts/staging-power.sh start) emit other events and are untouched.
variable "name" { type = string }
variable "db_instance_identifier" { type = string }
variable "db_instance_arn" { type = string }

locals {
  # aws-sdk integrations use the SDK's field casing (DbInstanceIdentifier, DbInstances, DbInstanceStatus).
  definition = jsonencode({
    Comment        = "Stop ${var.db_instance_identifier} again after RDS auto-starts it at the 7-day limit"
    TimeoutSeconds = 3600
    StartAt        = "Describe"
    States = {
      Describe = {
        Type       = "Task"
        Resource   = "arn:aws:states:::aws-sdk:rds:describeDBInstances"
        Parameters = { DbInstanceIdentifier = var.db_instance_identifier }
        ResultSelector = {
          "status.$" = "$.DbInstances[0].DbInstanceStatus"
        }
        Next = "Ready?"
      }
      "Ready?" = {
        Type = "Choice"
        Choices = [
          { Variable = "$.status", StringEquals = "available", Next = "Stop" },
          { Variable = "$.status", StringEquals = "stopped", Next = "AlreadyStopped" },
          { Variable = "$.status", StringEquals = "stopping", Next = "AlreadyStopped" },
        ]
        Default = "Wait" # starting, backing-up, modifying, ...
      }
      Wait = {
        Type    = "Wait"
        Seconds = 60
        Next    = "Describe"
      }
      Stop = {
        Type       = "Task"
        Resource   = "arn:aws:states:::aws-sdk:rds:stopDBInstance"
        Parameters = { DbInstanceIdentifier = var.db_instance_identifier }
        End        = true
      }
      AlreadyStopped = { Type = "Succeed" }
    }
  })
}

data "aws_iam_policy_document" "sfn_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["states.amazonaws.com"]
    }
  }
}

data "aws_iam_policy_document" "sfn" {
  statement {
    actions   = ["rds:DescribeDBInstances", "rds:StopDBInstance"]
    resources = [var.db_instance_arn]
  }
}

resource "aws_iam_role" "sfn" {
  name               = "${var.name}-rds-keep-stopped"
  assume_role_policy = data.aws_iam_policy_document.sfn_assume.json
}

resource "aws_iam_role_policy" "sfn" {
  name   = "stop-rds"
  role   = aws_iam_role.sfn.id
  policy = data.aws_iam_policy_document.sfn.json
}

resource "aws_sfn_state_machine" "this" {
  name       = "${var.name}-rds-keep-stopped"
  role_arn   = aws_iam_role.sfn.arn
  definition = local.definition
}

resource "aws_cloudwatch_event_rule" "auto_start" {
  name        = "${var.name}-rds-auto-started"
  description = "RDS restarted ${var.db_instance_identifier} after 7 days stopped"
  event_pattern = jsonencode({
    source        = ["aws.rds"]
    "detail-type" = ["RDS DB Instance Event"]
    detail = {
      EventID          = ["RDS-EVENT-0154"]
      SourceIdentifier = [var.db_instance_identifier]
    }
  })
}

data "aws_iam_policy_document" "events_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["events.amazonaws.com"]
    }
  }
}

data "aws_iam_policy_document" "events" {
  statement {
    actions   = ["states:StartExecution"]
    resources = [aws_sfn_state_machine.this.arn]
  }
}

resource "aws_iam_role" "events" {
  name               = "${var.name}-rds-auto-started"
  assume_role_policy = data.aws_iam_policy_document.events_assume.json
}

resource "aws_iam_role_policy" "events" {
  name   = "start-keep-stopped"
  role   = aws_iam_role.events.id
  policy = data.aws_iam_policy_document.events.json
}

resource "aws_cloudwatch_event_target" "sfn" {
  rule     = aws_cloudwatch_event_rule.auto_start.name
  arn      = aws_sfn_state_machine.this.arn
  role_arn = aws_iam_role.events.arn
}

output "rule_name" { value = aws_cloudwatch_event_rule.auto_start.name }
output "state_machine_arn" { value = aws_sfn_state_machine.this.arn }
