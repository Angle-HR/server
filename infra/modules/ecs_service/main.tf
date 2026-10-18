variable "name" { type = string }
variable "cluster_arn" { type = string }
variable "image" { type = string }
variable "cpu" {
  type    = number
  default = 256
}
variable "memory" {
  type    = number
  default = 512
}
variable "cpu_architecture" {
  type    = string
  default = "X86_64"
}
variable "container_port" {
  type    = number
  default = null
}
variable "command" {
  type    = list(string)
  default = null
}
variable "environment" {
  type    = map(string)
  default = {}
}
variable "secrets" {
  description = "ENV_NAME => Secrets Manager ARN"
  type        = map(string)
  default     = {}
}
variable "subnet_ids" { type = list(string) }
variable "security_group_ids" { type = list(string) }
variable "desired_count" {
  type    = number
  default = 1
}
variable "target_group_arn" {
  type    = string
  default = null
}
variable "cloudmap_service_arn" {
  type    = string
  default = null
}
variable "health_check_command" {
  description = "Container health check, e.g. [\"CMD-SHELL\", \"wget -q -O /dev/null http://localhost:8080/api/v1/countries\"]"
  type        = list(string)
  default     = null
}
variable "execution_role_arn" { type = string }
variable "task_role_arn" { type = string }
variable "log_retention_days" {
  type    = number
  default = 30
}
variable "start_period_seconds" {
  type    = number
  default = 30
}

data "aws_region" "current" {}

resource "aws_cloudwatch_log_group" "this" {
  name              = "/ecs/${var.name}"
  retention_in_days = var.log_retention_days
}

locals {
  optional = {
    portMappings = var.container_port == null ? null : [{ containerPort = var.container_port, protocol = "tcp" }]
    command      = var.command
    healthCheck = var.health_check_command == null ? null : {
      command     = var.health_check_command
      interval    = 15
      timeout     = 5
      retries     = 3
      startPeriod = var.start_period_seconds
    }
  }

  container = merge(
    {
      name        = var.name
      image       = var.image
      essential   = true
      environment = [for k, v in var.environment : { name = k, value = v }]
      secrets     = [for k, v in var.secrets : { name = k, valueFrom = v }]
      logConfiguration = {
        logDriver = "awslogs"
        options = {
          awslogs-group         = aws_cloudwatch_log_group.this.name
          awslogs-region        = data.aws_region.current.name
          awslogs-stream-prefix = "app"
        }
      }
    },
    { for k, v in local.optional : k => v if v != null },
  )
}

resource "aws_ecs_task_definition" "this" {
  family                   = var.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.cpu
  memory                   = var.memory
  execution_role_arn       = var.execution_role_arn
  task_role_arn            = var.task_role_arn
  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.cpu_architecture
  }
  container_definitions = jsonencode([local.container])
}

resource "aws_ecs_service" "this" {
  name                               = var.name
  cluster                            = var.cluster_arn
  task_definition                    = aws_ecs_task_definition.this.arn
  desired_count                      = var.desired_count
  launch_type                        = "FARGATE"
  deployment_minimum_healthy_percent = 100
  deployment_maximum_percent         = 200
  enable_execute_command             = false

  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  network_configuration {
    subnets          = var.subnet_ids
    security_groups  = var.security_group_ids
    assign_public_ip = false
  }

  dynamic "load_balancer" {
    for_each = var.target_group_arn == null ? [] : [1]
    content {
      target_group_arn = var.target_group_arn
      container_name   = var.name
      container_port   = var.container_port
    }
  }

  dynamic "service_registries" {
    for_each = var.cloudmap_service_arn == null ? [] : [1]
    content {
      registry_arn = var.cloudmap_service_arn
    }
  }
}

output "service_name" { value = aws_ecs_service.this.name }
output "log_group" { value = aws_cloudwatch_log_group.this.name }
