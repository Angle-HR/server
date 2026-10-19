# Stand-in for CloudFront while an account can't create distributions: the ALB faces the internet directly.
#   api_domain   : every path to the API target group
#   app_domain   : server_path_patterns to the API (minus frontend_override_patterns), the rest to the app target group
#   admin_domain : the admin target group
# Pair with edge's cloudfront_enabled = false, which points the DNS names here. Remove once CloudFront is available.
variable "name" { type = string }
variable "alb_arn" { type = string }
variable "alb_security_group_id" { type = string }
variable "https_listener_arn" { type = string }
variable "certificate_arn" {
  description = "Regional certificate covering app_domain, admin_domain and api_domain"
  type        = string
}
variable "api_target_group_arn" { type = string }
variable "app_domain" { type = string }
variable "admin_domain" { type = string }
variable "api_domain" { type = string }
variable "server_path_patterns" {
  description = "Paths on app_domain served by the API, as in edge's server_path_patterns"
  type        = list(string)
  default     = ["/j/*"]
}
variable "frontend_override_patterns" {
  type    = list(string)
  default = ["/j/*/apply*"]
}
variable "app_target_group_arn" { type = string }
variable "admin_target_group_arn" { type = string }
variable "global_rate_limit" {
  type    = number
  default = 2000
}
variable "submit_rate_limit" {
  type    = number
  default = 100
}
# ---------------------------------------------------------------- ALB
resource "aws_vpc_security_group_ingress_rule" "public" {
  for_each          = toset(["80", "443"])
  security_group_id = var.alb_security_group_id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "tcp"
  from_port         = tonumber(each.value)
  to_port           = tonumber(each.value)
}

resource "aws_lb_listener" "http" {
  load_balancer_arn = var.alb_arn
  port              = 80
  protocol          = "HTTP"
  default_action {
    type = "redirect"
    redirect {
      protocol    = "HTTPS"
      port        = "443"
      status_code = "HTTP_301"
    }
  }
}

resource "aws_lb_listener_certificate" "this" {
  listener_arn    = var.https_listener_arn
  certificate_arn = var.certificate_arn
}

resource "aws_lb_listener_rule" "api" {
  listener_arn = var.https_listener_arn
  priority     = 30
  action {
    type             = "forward"
    target_group_arn = var.api_target_group_arn
  }
  condition {
    host_header {
      values = [var.api_domain]
    }
  }
}

# Frontend routes under a server prefix (e.g. /j/<id>/apply). Lower priority number = evaluated first.
resource "aws_lb_listener_rule" "app_frontend_overrides" {
  count        = length(var.frontend_override_patterns) > 0 ? 1 : 0
  listener_arn = var.https_listener_arn
  priority     = 40
  action {
    type             = "forward"
    target_group_arn = var.app_target_group_arn
  }
  condition {
    host_header {
      values = [var.app_domain]
    }
  }
  condition {
    path_pattern {
      values = var.frontend_override_patterns
    }
  }
}

resource "aws_lb_listener_rule" "app_server_paths" {
  count        = length(var.server_path_patterns) > 0 ? 1 : 0
  listener_arn = var.https_listener_arn
  priority     = 41
  action {
    type             = "forward"
    target_group_arn = var.api_target_group_arn
  }
  condition {
    host_header {
      values = [var.app_domain]
    }
  }
  condition {
    path_pattern {
      values = var.server_path_patterns
    }
  }
}

resource "aws_lb_listener_rule" "app" {
  listener_arn = var.https_listener_arn
  priority     = 50
  action {
    type             = "forward"
    target_group_arn = var.app_target_group_arn
  }
  condition {
    host_header {
      values = [var.app_domain]
    }
  }
}

resource "aws_lb_listener_rule" "admin" {
  listener_arn = var.https_listener_arn
  priority     = 51
  action {
    type             = "forward"
    target_group_arn = var.admin_target_group_arn
  }
  condition {
    host_header {
      values = [var.admin_domain]
    }
  }
}

module "waf" {
  source            = "../waf"
  name              = var.name
  scope             = "REGIONAL"
  global_rate_limit = var.global_rate_limit
  submit_rate_limit = var.submit_rate_limit
}

resource "aws_wafv2_web_acl_association" "alb" {
  resource_arn = var.alb_arn
  web_acl_arn  = module.waf.arn
}
