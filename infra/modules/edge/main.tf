# CloudFront + WAF + private S3 frontends.
#   app.<zone>   : product SPA (S3) with API paths routed to the origin ALB, and /j/*/apply routed back to the SPA
#   admin.<zone> : admin console SPA (S3)
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
variable "certificate_arn" {
  description = "us-east-1 certificate covering app_domain and admin_domain"
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
# Server routes (internal/app/app.go): the JSON API, admin and public endpoints included, is all under /api/v1;
# /j/{id} and /j/{id}/privacy are server-rendered pages; /fluvio/* is the Fluvio API.
variable "api_path_patterns" {
  type    = list(string)
  default = ["/api/*", "/j/*", "/fluvio/*"]
}
# More specific than "/j/*": the Apply page is a frontend route, not a server route.
variable "frontend_override_patterns" {
  type    = list(string)
  default = ["/j/*/apply*"]
}
variable "global_rate_limit" {
  description = "Requests per 5 minutes per IP, all paths"
  type        = number
  default     = 2000
}
variable "submit_rate_limit" {
  description = "POST requests per 5 minutes per IP to /public/*"
  type        = number
  default     = 100
}
variable "bucket_prefix" {
  description = "Prefix for S3 bucket names (must be globally unique). Defaults to name."
  type        = string
  default     = null
}
variable "force_destroy" {
  type    = bool
  default = false
}

locals {
  # AWS managed policy ids
  caching_optimized       = "658327ea-f89d-4fab-a63d-7e88639e58f6"
  caching_disabled        = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"
  all_viewer_except_host  = "b689b0a8-53d0-40ab-baf2-68738e2966ac"
  security_headers_policy = "67f7725c-6f97-4210-82d7-5512b31e9d03"
  frontends               = { app = var.app_domain, admin = var.admin_domain }
  alb_origin_id           = "alb"
  methods_all             = ["GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"]
}

resource "aws_s3_bucket" "frontend" {
  for_each      = local.frontends
  bucket        = "${coalesce(var.bucket_prefix, var.name)}-${each.key}-web"
  force_destroy = var.force_destroy
}

resource "aws_s3_bucket_public_access_block" "frontend" {
  for_each                = aws_s3_bucket.frontend
  bucket                  = each.value.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "frontend" {
  for_each = aws_s3_bucket.frontend
  bucket   = each.value.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_cloudfront_origin_access_control" "s3" {
  name                              = "${var.name}-s3"
  origin_access_control_origin_type = "s3"
  signing_behavior                  = "always"
  signing_protocol                  = "sigv4"
}

# SPA fallback done in a viewer-request function, NOT as a distribution-wide custom error response,
# so 404s from the API are never rewritten into index.html.
resource "aws_cloudfront_function" "spa" {
  name    = "${var.name}-spa-rewrite"
  runtime = "cloudfront-js-2.0"
  publish = true
  code    = <<-JS
    function handler(event) {
      var req = event.request;
      var last = req.uri.split('/').pop();
      if (last.indexOf('.') === -1) { req.uri = '/index.html'; }
      return req;
    }
  JS
}

resource "aws_wafv2_web_acl" "this" {
  provider = aws.us_east_1
  name     = var.name
  scope    = "CLOUDFRONT"

  default_action {
    allow {}
  }

  rule {
    name     = "common"
    priority = 10
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesCommonRuleSet"
        vendor_name = "AWS"
        # Applications can legitimately post bodies larger than 8 KB.
        rule_action_override {
          name = "SizeRestrictions_BODY"
          action_to_use {
            count {}
          }
        }
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name}-common"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "bad-inputs"
    priority = 20
    override_action {
      none {}
    }
    statement {
      managed_rule_group_statement {
        name        = "AWSManagedRulesKnownBadInputsRuleSet"
        vendor_name = "AWS"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name}-bad-inputs"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "rate-all"
    priority = 30
    action {
      block {}
    }
    statement {
      rate_based_statement {
        limit              = var.global_rate_limit
        aggregate_key_type = "IP"
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name}-rate-all"
      sampled_requests_enabled   = true
    }
  }

  rule {
    name     = "rate-public-submit"
    priority = 40
    action {
      block {}
    }
    statement {
      rate_based_statement {
        limit              = var.submit_rate_limit
        aggregate_key_type = "IP"
        scope_down_statement {
          and_statement {
            statement {
              byte_match_statement {
                search_string         = "POST"
                positional_constraint = "EXACTLY"
                field_to_match {
                  method {}
                }
                text_transformation {
                  priority = 0
                  type     = "NONE"
                }
              }
            }
            statement {
              byte_match_statement {
                search_string         = "/public/"
                positional_constraint = "STARTS_WITH"
                field_to_match {
                  uri_path {}
                }
                text_transformation {
                  priority = 0
                  type     = "NONE"
                }
              }
            }
          }
        }
      }
    }
    visibility_config {
      cloudwatch_metrics_enabled = true
      metric_name                = "${var.name}-rate-submit"
      sampled_requests_enabled   = true
    }
  }

  visibility_config {
    cloudwatch_metrics_enabled = true
    metric_name                = var.name
    sampled_requests_enabled   = true
  }
}

resource "aws_cloudfront_distribution" "this" {
  for_each            = local.frontends
  enabled             = true
  is_ipv6_enabled     = true
  http_version        = "http2and3"
  price_class         = "PriceClass_100"
  aliases             = [each.value]
  default_root_object = "index.html"
  web_acl_id          = aws_wafv2_web_acl.this.arn
  comment             = "${var.name} ${each.key}"

  origin {
    origin_id                = "s3"
    domain_name              = aws_s3_bucket.frontend[each.key].bucket_regional_domain_name
    origin_access_control_id = aws_cloudfront_origin_access_control.s3.id
  }

  dynamic "origin" {
    for_each = each.key == "app" ? [1] : []
    content {
      origin_id   = local.alb_origin_id
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
    }
  }

  default_cache_behavior {
    target_origin_id           = "s3"
    viewer_protocol_policy     = "redirect-to-https"
    allowed_methods            = ["GET", "HEAD", "OPTIONS"]
    cached_methods             = ["GET", "HEAD"]
    compress                   = true
    cache_policy_id            = local.caching_optimized
    response_headers_policy_id = local.security_headers_policy
    function_association {
      event_type   = "viewer-request"
      function_arn = aws_cloudfront_function.spa.arn
    }
  }

  # Frontend routes that sit under an API prefix (e.g. /j/<id>/apply). Listed first = higher precedence.
  dynamic "ordered_cache_behavior" {
    for_each = each.key == "app" ? var.frontend_override_patterns : []
    content {
      path_pattern               = ordered_cache_behavior.value
      target_origin_id           = "s3"
      viewer_protocol_policy     = "redirect-to-https"
      allowed_methods            = ["GET", "HEAD", "OPTIONS"]
      cached_methods             = ["GET", "HEAD"]
      compress                   = true
      cache_policy_id            = local.caching_optimized
      response_headers_policy_id = local.security_headers_policy
      function_association {
        event_type   = "viewer-request"
        function_arn = aws_cloudfront_function.spa.arn
      }
    }
  }

  dynamic "ordered_cache_behavior" {
    for_each = each.key == "app" ? var.api_path_patterns : []
    content {
      path_pattern             = ordered_cache_behavior.value
      target_origin_id         = local.alb_origin_id
      viewer_protocol_policy   = "redirect-to-https"
      allowed_methods          = local.methods_all
      cached_methods           = ["GET", "HEAD"]
      compress                 = true
      cache_policy_id          = local.caching_disabled
      origin_request_policy_id = local.all_viewer_except_host
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

data "aws_iam_policy_document" "frontend" {
  for_each = aws_s3_bucket.frontend
  statement {
    actions   = ["s3:GetObject"]
    resources = ["${each.value.arn}/*"]
    principals {
      type        = "Service"
      identifiers = ["cloudfront.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "AWS:SourceArn"
      values   = [aws_cloudfront_distribution.this[each.key].arn]
    }
  }
}

resource "aws_s3_bucket_policy" "frontend" {
  for_each = aws_s3_bucket.frontend
  bucket   = each.value.id
  policy   = data.aws_iam_policy_document.frontend[each.key].json
}

resource "aws_route53_record" "a" {
  for_each = local.frontends
  zone_id  = var.zone_id
  name     = each.value
  type     = "A"
  alias {
    name                   = aws_cloudfront_distribution.this[each.key].domain_name
    zone_id                = aws_cloudfront_distribution.this[each.key].hosted_zone_id
    evaluate_target_health = false
  }
}

resource "aws_route53_record" "aaaa" {
  for_each = local.frontends
  zone_id  = var.zone_id
  name     = each.value
  type     = "AAAA"
  alias {
    name                   = aws_cloudfront_distribution.this[each.key].domain_name
    zone_id                = aws_cloudfront_distribution.this[each.key].hosted_zone_id
    evaluate_target_health = false
  }
}

output "bucket_names" {
  value = { for k, b in aws_s3_bucket.frontend : k => b.bucket }
}
output "bucket_arns" {
  value = [for b in aws_s3_bucket.frontend : b.arn]
}
output "distribution_ids" {
  value = { for k, d in aws_cloudfront_distribution.this : k => d.id }
}
output "distribution_arns" {
  value = [for d in aws_cloudfront_distribution.this : d.arn]
}
