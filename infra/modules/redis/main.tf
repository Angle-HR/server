variable "name" { type = string }
variable "vpc_id" { type = string }
variable "subnet_ids" { type = list(string) }
variable "allowed_sg_ids" { type = list(string) }
variable "node_type" {
  type    = string
  default = "cache.t4g.micro"
}
variable "replicas" {
  type    = number
  default = 0
}

resource "aws_elasticache_subnet_group" "this" {
  name       = var.name
  subnet_ids = var.subnet_ids
}

resource "aws_security_group" "this" {
  name        = "${var.name}-redis"
  description = "Redis from application tasks"
  vpc_id      = var.vpc_id
}

resource "aws_vpc_security_group_ingress_rule" "redis" {
  # count, not for_each: the SG ids are unknown until apply, but the list length is known at plan time.
  count                        = length(var.allowed_sg_ids)
  security_group_id            = aws_security_group.this.id
  referenced_security_group_id = var.allowed_sg_ids[count.index]
  ip_protocol                  = "tcp"
  from_port                    = 6379
  to_port                      = 6379
}

resource "aws_elasticache_replication_group" "this" {
  replication_group_id       = var.name
  description                = "OpenHR rate limiting and Fluvio support"
  engine                     = "redis"
  engine_version             = "7.1"
  node_type                  = var.node_type
  num_cache_clusters         = 1 + var.replicas
  automatic_failover_enabled = var.replicas > 0
  port                       = 6379
  subnet_group_name          = aws_elasticache_subnet_group.this.name
  security_group_ids         = [aws_security_group.this.id]
  at_rest_encryption_enabled = true
  transit_encryption_enabled = true
  snapshot_retention_limit   = 0
}

# rediss:// because in-transit encryption is on.
output "url" { value = "rediss://${aws_elasticache_replication_group.this.primary_endpoint_address}:6379/0" }
