# Creates EMPTY secrets. Values are set out-of-band (aws secretsmanager put-secret-value) so keys never enter Terraform state.
variable "prefix" { type = string }
variable "names" { type = set(string) }

resource "aws_secretsmanager_secret" "this" {
  for_each                = var.names
  name                    = "${var.prefix}/${each.key}"
  recovery_window_in_days = 7
}

output "arns" {
  value = { for k, s in aws_secretsmanager_secret.this : k => s.arn }
}
