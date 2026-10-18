# The four satellite data regions. Same module four times because provider aliases cannot be looped over.
# Each creates: VPC, Postgres, uploads bucket, ClamAV behind an internal NLB, and peering back to the home VPC.
# Needs the ClamAV image mirrored into home ECR (README step 6) before clamav_desired_count > 0 can start.

locals {
  clamav_image = { for k, v in local.satellites : k => "${local.account_id}.dkr.ecr.${v.region}.amazonaws.com/openhr/clamav:${var.clamav_tag}" }
}

module "sat_eu" {
  source    = "../../modules/satellite_region"
  providers = { aws = aws.eu, aws.home = aws }

  name                        = "${local.name}-eu"
  region_key                  = "eu"
  cidr                        = local.satellites.eu.cidr
  home_vpc_id                 = module.network.vpc_id
  home_vpc_cidr               = local.home_cidr
  home_private_route_table_id = module.network.private_route_table_id
  bucket_prefix               = local.bucket_prefix
  allowed_origins             = ["https://${local.app_domain}"]
  clamav_image                = local.clamav_image.eu
  clamav_desired_count        = var.services_enabled ? 1 : 0
}

module "sat_us" {
  source    = "../../modules/satellite_region"
  providers = { aws = aws.us, aws.home = aws }

  name                        = "${local.name}-us"
  region_key                  = "us"
  cidr                        = local.satellites.us.cidr
  home_vpc_id                 = module.network.vpc_id
  home_vpc_cidr               = local.home_cidr
  home_private_route_table_id = module.network.private_route_table_id
  bucket_prefix               = local.bucket_prefix
  allowed_origins             = ["https://${local.app_domain}"]
  clamav_image                = local.clamav_image.us
  clamav_desired_count        = var.services_enabled ? 1 : 0
}

module "sat_asia" {
  source    = "../../modules/satellite_region"
  providers = { aws = aws.asia, aws.home = aws }

  name                        = "${local.name}-asia"
  region_key                  = "asia"
  cidr                        = local.satellites.asia.cidr
  home_vpc_id                 = module.network.vpc_id
  home_vpc_cidr               = local.home_cidr
  home_private_route_table_id = module.network.private_route_table_id
  bucket_prefix               = local.bucket_prefix
  allowed_origins             = ["https://${local.app_domain}"]
  clamav_image                = local.clamav_image.asia
  clamav_desired_count        = var.services_enabled ? 1 : 0
}

module "sat_africa" {
  # Managed NAT here: the fck-nat community AMI may not be published in opt-in regions such as af-south-1.
  # Switch to "instance" if `terraform plan` finds the AMI and you want to save ~$28/mo.
  source    = "../../modules/satellite_region"
  providers = { aws = aws.africa, aws.home = aws }

  name                        = "${local.name}-africa"
  region_key                  = "africa"
  cidr                        = local.satellites.africa.cidr
  home_vpc_id                 = module.network.vpc_id
  home_vpc_cidr               = local.home_cidr
  home_private_route_table_id = module.network.private_route_table_id
  bucket_prefix               = local.bucket_prefix
  allowed_origins             = ["https://${local.app_domain}"]
  clamav_image                = local.clamav_image.africa
  clamav_desired_count        = var.services_enabled ? 1 : 0
  nat_mode                    = "managed"
}
