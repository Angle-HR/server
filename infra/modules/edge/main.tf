# CloudFront + WAF in front of the origin ALB. Every site is served by the ALB, which routes on headers CloudFront adds:
#   X-Origin-Verify (shared secret: the request came through CloudFront) and X-Edge-Site (which site: app, admin, api).
#   app_domain   : client SSR app; server_path_patterns go to the API instead, except frontend_override_patterns
#   admin_domain : admin SSR app
#   api_domain   : optional; every path to the API (its docs at / and /api/docs)
# Only cached_path_patterns (hashed build assets) are cached; everything else passes through uncached.
# cloudfront_enabled = false skips the distributions and ALB rules and points the names at the ALB directly
# (modules/direct_edge does the rest).
terraform {
  required_providers {
    aws = {
      source                = "hashicorp/aws"
      configuration_aliases = [aws.us_east_1]
    }
  }
}

variable "name" { type = string }
variable "zone_id" { type = string }
variable "app_domain" { type = string }
variable "admin_domain" { type = string }
variable "api_domain" {
  description = "Optional host that sends every path to the API. Null = the API is only reachable via server_path_patterns on app_domain."
  type        = string
  default     = null
}
variable "certificate_arn" {
  description = "us-east-1 certificate covering app_domain, admin_domain and api_domain"
  type        = string
}
variable "origin_domain" {
  description = "DNS name of the origin ALB record (must match the ALB certificate)"
  type        = string
}
variable "origin_verify_value" {
  type      = string
  sensitive = true
}
variable "https_listener_arn" { type = string }
variable "target_group_arns" {
  description = "app, admin and api => ALB target group ARN"
  type        = map(string)
}
# Server routes (internal/app/app.go): the JSON API, admin and public endpoints included, is all under /api/v1;
# /j/{id} and /j/{id}/privacy are server-rendered pages; /fluvio/* is the Fluvio API.
variable "server_path_patterns" {
  description = "Paths on app_domain served by the API"
  type        = list(string)
  default     = ["/api/*", "/j/*", "/fluvio/*"]
}
# More specific than "/j/*": the Apply page is a frontend route, not a server route.
variable "frontend_override_patterns" {
  type    = list(string)
  default = ["/j/*/apply*"]
}
variable "cached_path_patterns" {
  description = "Per site, paths whose responses never change (content-hashed build output)"
  type        = map(list(string))
  default = {
    app   = ["/_next/static/*"] # Next.js
    admin = ["/assets/*"]       # Vite
  }
}
variable "global_rate_limit" {
  description = "Requests per 5 minutes per IP, all paths"
  type        = number
  default     = 2000
}
variable "submit_rate_limit" {
  description = "POST requests per 5 minutes per IP to the candidate endpoints (/api/v1/public/*)"
  type        = number
  default     = 100
}
variable "cloudfront_enabled" {
  description = "false = no distributions; the DNS names alias the ALB instead (see modules/direct_edge)."
  type        = bool
  default     = true
}
variable "alb_dns_name" {
  description = "Required when cloudfront_enabled = false"
  type        = string
  default     = null
}
variable "alb_zone_id" {
  type    = string
  default = null
}

locals {
  # AWS managed policy ids
  caching_optimized      = "658327ea-f89d-4fab-a63d-7e88639e58f6"
  caching_disabled       = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"
  all_viewer_except_host = "b689b0a8-53d0-40ab-baf2-68738e2966ac"
  methods_all            = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
  hosts                  = merge({ app = var.app_domain, admin = var.admin_domain }, var.api_domain != null ? { api = var.api_domain } : {})
  distributions          = aws_cloudfront_distribution.this
  alb_rules              = var.cloudfront_enabled ? 1 : 0
}

module "waf" {
  source            = "../waf"
  providers         = { aws = aws.us_east_1 }
  name              = var.name
  scope             = "CLOUDFRONT"
  global_rate_limit = var.global_rate_limit
  submit_rate_limit = var.submit_rate_limit
}

moved {
  from = aws_wafv2_web_acl.this
  to   = module.waf.aws_wafv2_web_acl.this
}

resource "aws_cloudfront_distribution" "this" {
  for_each        = var.cloudfront_enabled ? local.hosts : {}
  enabled         = true
  is_ipv6_enabled = true
  http_version    = "http2and3"
  price_class     = "PriceClass_100"
  aliases         = [each.value]
  web_acl_id      = module.waf.arn
  comment         = "${var.name} ${each.key}"

  origin {
    origin_id   = "alb"
    domain_name = var.origin_domain
    custom_origin_config {
      http_port                = 80
      https_port               = 443
      origin_protocol_policy   = "https-only"
      origin_ssl_protocols     = ["TLSv1.2"]
      origin_read_timeout      = 30
      origin_keepalive_timeout = 5
    }
    custom_header {
      name  = "X-Origin-Verify"
      value = var.origin_verify_value
    }
    custom_header {
      name  = "X-Edge-Site"
      value = each.key
    }
  }

  default_cache_behavior {
    target_origin_id         = "alb"
    viewer_protocol_policy   = "redirect-to-https"
    allowed_methods          = local.methods_all
    cached_methods           = ["GET", "HEAD"]
    compress                 = true
    cache_policy_id          = local.caching_disabled
    origin_request_policy_id = local.all_viewer_except_host
  }

  dynamic "ordered_cache_behavior" {
    for_each = lookup(var.cached_path_patterns, each.key, [])
    content {
      path_pattern           = ordered_cache_behavior.value
      target_origin_id       = "alb"
      viewer_protocol_policy = "redirect-to-https"
      allowed_methods        = ["GET", "HEAD"]
      cached_methods         = ["GET", "HEAD"]
      compress               = true
      cache_policy_id        = local.caching_optimized
    }
  }

  restrictions {
    geo_restriction {
      restriction_type = "none"
    }
  }

  viewer_certificate {
    acm_certificate_arn      = var.certificate_arn
    ssl_support_method       = "sni-only"
    minimum_protocol_version = "TLSv1.2_2021"
  }
}

# ALB routing for requests from CloudFront. Requests with only X-Origin-Verify (the api site) fall through to the alb
# module's API rule (priority 20).
resource "aws_lb_listener_rule" "app_frontend_overrides" {
  count        = length(var.frontend_override_patterns) > 0 ? local.alb_rules : 0
  listener_arn = var.https_listener_arn
  priority     = 11
  action {
    type             = "forward"
    target_group_arn = var.target_group_arns["app"]
  }
  condition {
    http_header {
      http_header_name = "X-Origin-Verify"
      values           = [var.origin_verify_value]
    }
  }
  condition {
    http_header {
      http_header_name = "X-Edge-Site"
      values           = ["app"]
    }
  }
  condition {
    path_pattern {
      values = var.frontend_override_patterns
    }
  }
}

resource "aws_lb_listener_rule" "app_server_paths" {
  count        = length(var.server_path_patterns) > 0 ? local.alb_rules : 0
  listener_arn = var.https_listener_arn
  priority     = 12
  action {
    type             = "forward"
    target_group_arn = var.target_group_arns["api"]
  }
  condition {
    http_header {
      http_header_name = "X-Origin-Verify"
      values           = [var.origin_verify_value]
    }
  }
  condition {
    http_header {
      http_header_name = "X-Edge-Site"
      values           = ["app"]
    }
  }
  condition {
    path_pattern {
      values = var.server_path_patterns
    }
  }
}

resource "aws_lb_listener_rule" "site" {
  for_each     = var.cloudfront_enabled ? { app = 13, admin = 14 } : {}
  listener_arn = var.https_listener_arn
  priority     = each.value
  action {
    type             = "forward"
    target_group_arn = var.target_group_arns[each.key]
  }
  condition {
    http_header {
      http_header_name = "X-Origin-Verify"
      values           = [var.origin_verify_value]
    }
  }
  condition {
    http_header {
      http_header_name = "X-Edge-Site"
      values           = [each.key]
    }
  }
}

# With CloudFront off the names alias the ALB, so switching modes updates these records in place.
resource "aws_route53_record" "a" {
  for_each = local.hosts
  zone_id  = var.zone_id
  name     = each.value
  type     = "A"
  alias {
    name                   = var.cloudfront_enabled ? local.distributions[each.key].domain_name : var.alb_dns_name
    zone_id                = var.cloudfront_enabled ? local.distributions[each.key].hosted_zone_id : var.alb_zone_id
    evaluate_target_health = !var.cloudfront_enabled
  }
}

# The ALB is IPv4 only.
resource "aws_route53_record" "aaaa" {
  for_each = var.cloudfront_enabled ? local.hosts : {}
  zone_id  = var.zone_id
  name     = each.value
  type     = "AAAA"
  alias {
    name                   = local.distributions[each.key].domain_name
    zone_id                = local.distributions[each.key].hosted_zone_id
    evaluate_target_health = false
  }
}

output "distribution_ids" {
  value = { for k, d in local.distributions : k => d.id }
}
output "distribution_arns" {
  value = [for d in values(local.distributions) : d.arn]
}
