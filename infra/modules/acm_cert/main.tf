# DNS-validated certificate. Instantiate once per region needed (CloudFront certs must be in us-east-1).
terraform {
  required_providers {
    aws = {
      source = "hashicorp/aws"
    }
  }
}

variable "domain" { type = string }
variable "sans" {
  type    = list(string)
  default = []
}
variable "zone_id" { type = string }
variable "create_validation_records" {
  description = "false when another certificate in the account already manages the records (same domains = same CNAMEs in every region)"
  type        = bool
  default     = true
}

resource "aws_acm_certificate" "this" {
  domain_name               = var.domain
  subject_alternative_names = var.sans
  validation_method         = "DNS"
  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_record" "validation" {
  for_each = !var.create_validation_records ? {} : {
    for o in aws_acm_certificate.this.domain_validation_options : o.domain_name => {
      name   = o.resource_record_name
      type   = o.resource_record_type
      record = o.resource_record_value
    }
  }
  zone_id         = var.zone_id
  name            = each.value.name
  type            = each.value.type
  records         = [each.value.record]
  ttl             = 60
  allow_overwrite = true
}

resource "aws_acm_certificate_validation" "this" {
  certificate_arn         = aws_acm_certificate.this.arn
  validation_record_fqdns = var.create_validation_records ? [for r in aws_route53_record.validation : r.fqdn] : null
}

output "arn" { value = aws_acm_certificate_validation.this.certificate_arn }
