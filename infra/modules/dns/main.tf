# Hosted zone for this environment. For staging this is a sub-zone (e.g. staging.tryopenhr.com):
# add the printed NS records to the parent zone before certificates can validate.
variable "zone_name" { type = string }

resource "aws_route53_zone" "this" {
  name = var.zone_name
}

output "zone_id" { value = aws_route53_zone.this.zone_id }
output "name_servers" { value = aws_route53_zone.this.name_servers }
