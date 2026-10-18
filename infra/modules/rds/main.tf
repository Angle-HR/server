variable "name" { type = string }
variable "vpc_id" { type = string }
variable "subnet_ids" { type = list(string) }
variable "allowed_sg_ids" {
  type    = list(string)
  default = []
}
# Cross-region peering cannot reference security groups, so remote callers are allowed by CIDR.
variable "allowed_cidrs" {
  type    = list(string)
  default = []
}
variable "db_name" {
  description = "Database created with the instance. The migrate task creates the others."
  type        = string
  default     = "openhr_global"
}
variable "instance_class" {
  type    = string
  default = "db.t4g.small"
}
variable "engine_version" {
  type    = string
  default = "16"
}
variable "allocated_storage" {
  type    = number
  default = 20
}
variable "max_allocated_storage" {
  type    = number
  default = 100
}
variable "multi_az" {
  type    = bool
  default = false
}
# Decision: 7 days. Backups outlive retention-task deletions by this many days; state it in the privacy notice.
variable "backup_retention_days" {
  type    = number
  default = 7
}
variable "deletion_protection" {
  type    = bool
  default = true
}

resource "aws_db_subnet_group" "this" {
  name       = var.name
  subnet_ids = var.subnet_ids
}

resource "aws_security_group" "this" {
  name        = "${var.name}-rds"
  description = "Postgres from application tasks"
  vpc_id      = var.vpc_id
}

resource "aws_vpc_security_group_ingress_rule" "pg" {
  # count, not for_each: the SG ids are unknown until apply, but the list length is known at plan time.
  count                        = length(var.allowed_sg_ids)
  security_group_id            = aws_security_group.this.id
  referenced_security_group_id = var.allowed_sg_ids[count.index]
  ip_protocol                  = "tcp"
  from_port                    = 5432
  to_port                      = 5432
}

# Generated here (lands in encrypted state) rather than RDS-managed: a managed master secret rotates every
# 7 days and would silently invalidate the DSN secrets the app reads.
resource "aws_vpc_security_group_ingress_rule" "pg_cidr" {
  for_each          = toset(var.allowed_cidrs)
  security_group_id = aws_security_group.this.id
  cidr_ipv4         = each.value
  ip_protocol       = "tcp"
  from_port         = 5432
  to_port           = 5432
}

resource "random_password" "master" {
  length  = 32
  special = false
}

resource "aws_db_instance" "this" {
  identifier                      = var.name
  engine                          = "postgres"
  engine_version                  = var.engine_version
  instance_class                  = var.instance_class
  allocated_storage               = var.allocated_storage
  max_allocated_storage           = var.max_allocated_storage
  storage_type                    = "gp3"
  storage_encrypted               = true
  db_name                         = var.db_name
  username                        = "openhr"
  password                        = random_password.master.result
  db_subnet_group_name            = aws_db_subnet_group.this.name
  vpc_security_group_ids          = [aws_security_group.this.id]
  publicly_accessible             = false
  multi_az                        = var.multi_az
  backup_retention_period         = var.backup_retention_days
  copy_tags_to_snapshot           = true
  deletion_protection             = var.deletion_protection
  skip_final_snapshot             = !var.deletion_protection
  final_snapshot_identifier       = var.deletion_protection ? "${var.name}-final" : null
  auto_minor_version_upgrade      = true
  performance_insights_enabled    = false
  enabled_cloudwatch_logs_exports = ["postgresql"]
}

output "address" { value = aws_db_instance.this.address }
output "identifier" { value = aws_db_instance.this.identifier }
output "arn" { value = aws_db_instance.this.arn }
output "username" { value = aws_db_instance.this.username }
output "password" {
  value     = random_password.master.result
  sensitive = true
}
