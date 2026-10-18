# One data region outside the home region: its own VPC, Postgres, uploads bucket and ClamAV scanner,
# peered back to the home VPC so the API there can reach them.
#   aws      = provider for THIS region
#   aws.home = provider for the home region (owns the peering requester and the home-side routes)
terraform {
  required_providers {
    aws = {
      source                = "hashicorp/aws"
      configuration_aliases = [aws.home]
    }
  }
}

variable "name" {
  description = "Resource name prefix including the region key, e.g. openhr-prod-eu"
  type        = string
}
variable "region_key" {
  description = "Logical data region: eu, us, asia or africa. Names the database and bucket."
  type        = string
}
variable "cidr" { type = string }
variable "home_vpc_id" { type = string }
variable "home_vpc_cidr" {
  description = "Must be a plan-time constant (not a module output): it keys for_each in the rds module."
  type        = string
}
variable "home_private_route_table_id" { type = string }

variable "bucket_prefix" { type = string }
variable "allowed_origins" { type = list(string) }

variable "clamav_image" { type = string }
variable "clamav_desired_count" {
  type    = number
  default = 1
}
variable "db_instance_class" {
  type    = string
  default = "db.t4g.small"
}
variable "db_backup_retention_days" {
  type    = number
  default = 7
}
variable "deletion_protection" {
  type    = bool
  default = true
}
variable "force_destroy_buckets" {
  type    = bool
  default = false
}
variable "nat_mode" {
  type    = string
  default = "instance"
}

data "aws_region" "this" {}

# ------------------------------------------------------------------ network
module "network" {
  source   = "../network"
  name     = var.name
  cidr     = var.cidr
  nat_mode = var.nat_mode
}

# ------------------------------------------------------------------ peering to home
resource "aws_vpc_peering_connection" "home" {
  provider    = aws.home
  vpc_id      = var.home_vpc_id
  peer_vpc_id = module.network.vpc_id
  peer_region = data.aws_region.this.name
  auto_accept = false
  tags        = { Name = "${var.name}-to-home" }
}

resource "aws_vpc_peering_connection_accepter" "this" {
  vpc_peering_connection_id = aws_vpc_peering_connection.home.id
  auto_accept               = true
  tags                      = { Name = "${var.name}-to-home" }
}

# Lets the home VPC resolve this region's RDS endpoint (and the NLB name) to private addresses, and vice versa.
resource "aws_vpc_peering_connection_options" "home_side" {
  provider                  = aws.home
  vpc_peering_connection_id = aws_vpc_peering_connection_accepter.this.id
  requester {
    allow_remote_vpc_dns_resolution = true
  }
}

resource "aws_vpc_peering_connection_options" "this_side" {
  vpc_peering_connection_id = aws_vpc_peering_connection_accepter.this.id
  accepter {
    allow_remote_vpc_dns_resolution = true
  }
}

resource "aws_route" "home_to_this" {
  provider                  = aws.home
  route_table_id            = var.home_private_route_table_id
  destination_cidr_block    = var.cidr
  vpc_peering_connection_id = aws_vpc_peering_connection.home.id
  depends_on                = [aws_vpc_peering_connection_accepter.this]
}

resource "aws_route" "this_to_home" {
  route_table_id            = module.network.private_route_table_id
  destination_cidr_block    = var.home_vpc_cidr
  vpc_peering_connection_id = aws_vpc_peering_connection.home.id
  depends_on                = [aws_vpc_peering_connection_accepter.this]
}

# ------------------------------------------------------------------ database
module "rds" {
  source                = "../rds"
  name                  = var.name
  vpc_id                = module.network.vpc_id
  subnet_ids            = module.network.private_subnet_ids
  allowed_cidrs         = [var.home_vpc_cidr]
  db_name               = "openhr_${var.region_key}"
  instance_class        = var.db_instance_class
  backup_retention_days = var.db_backup_retention_days
  deletion_protection   = var.deletion_protection
}

# ------------------------------------------------------------------ uploads
module "uploads" {
  source          = "../uploads"
  name_prefix     = var.bucket_prefix
  regions         = [var.region_key]
  allowed_origins = var.allowed_origins
  force_destroy   = var.force_destroy_buckets
}

# ------------------------------------------------------------------ ClamAV
# The API reaches the scanner through an internal NLB: its DNS name resolves to private addresses and works over peering
# (Cloud Map namespaces are per-VPC and would not resolve from the home region).
module "iam" {
  source      = "../iam"
  name        = var.name
  secret_arns = []
  bucket_arns = []
}

resource "aws_ecs_cluster" "this" {
  name = var.name
}

resource "aws_security_group" "clamav" {
  name        = "${var.name}-clamav"
  description = "clamd: reachable from this VPC and the home VPC"
  vpc_id      = module.network.vpc_id
}

resource "aws_vpc_security_group_ingress_rule" "clamav" {
  # var.cidr, not the VPC resource attribute: for_each keys must be known at plan time.
  for_each          = toset([var.cidr, var.home_vpc_cidr])
  security_group_id = aws_security_group.clamav.id
  cidr_ipv4         = each.value
  ip_protocol       = "tcp"
  from_port         = 3310
  to_port           = 3310
}

# freshclam signature updates and image pulls go out through the NAT.
resource "aws_vpc_security_group_egress_rule" "clamav" {
  security_group_id = aws_security_group.clamav.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

resource "aws_lb" "clamav" {
  name                             = "${var.name}-clam"
  load_balancer_type               = "network"
  internal                         = true
  subnets                          = module.network.private_subnet_ids
  enable_cross_zone_load_balancing = true
}

resource "aws_lb_target_group" "clamav" {
  name        = "${var.name}-clam"
  port        = 3310
  protocol    = "TCP"
  target_type = "ip"
  vpc_id      = module.network.vpc_id
  health_check {
    protocol            = "TCP"
    interval            = 30
    healthy_threshold   = 3
    unhealthy_threshold = 3
  }
  deregistration_delay = 30
}

resource "aws_lb_listener" "clamav" {
  load_balancer_arn = aws_lb.clamav.arn
  port              = 3310
  protocol          = "TCP"
  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.clamav.arn
  }
}

module "clamav" {
  source               = "../ecs_service"
  name                 = "clamav"
  cluster_arn          = aws_ecs_cluster.this.arn
  image                = var.clamav_image
  cpu                  = 1024
  memory               = 3072
  container_port       = 3310
  subnet_ids           = module.network.private_subnet_ids
  security_group_ids   = [aws_security_group.clamav.id]
  desired_count        = var.clamav_desired_count
  target_group_arn     = aws_lb_target_group.clamav.arn
  health_check_command = ["CMD-SHELL", "clamdcheck.sh"]
  start_period_seconds = 300
  execution_role_arn   = module.iam.execution_role_arn
  task_role_arn        = module.iam.task_role_arn
  depends_on           = [aws_lb_listener.clamav]
}

# ------------------------------------------------------------------ outputs
output "vpc_id" { value = module.network.vpc_id }
output "vpc_cidr" { value = module.network.vpc_cidr }
output "rds_address" { value = module.rds.address }
output "rds_username" { value = module.rds.username }
output "rds_password" {
  value     = module.rds.password
  sensitive = true
}
output "bucket_names" { value = module.uploads.bucket_names }
output "bucket_arns" { value = module.uploads.bucket_arns }
output "clamav_addr" { value = "${aws_lb.clamav.dns_name}:3310" }
