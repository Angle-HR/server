data "aws_caller_identity" "current" {}

locals {
  env           = "production"
  name          = "openhr-prod"
  account_id    = data.aws_caller_identity.current.account_id
  bucket_prefix = "openhr-prod-${local.account_id}" # S3 names are global; the account id keeps them unique.

  app_domain    = "app.${var.zone_name}"
  admin_domain  = "admin.${var.zone_name}"
  origin_domain = "origin.${var.zone_name}"
  fluvio_domain = "fluvio.${var.zone_name}"

  # CIDRs must not overlap: every satellite VPC is peered to the home VPC.
  # They are constants (not module outputs) because they key for_each blocks that must be known at plan time.
  home_cidr = "10.10.0.0/16"

  regions = ["uk", "eu", "us", "asia", "africa"] # the server requires all five at startup

  satellites = {
    eu     = { region = "eu-central-1", cidr = "10.11.0.0/16" }
    us     = { region = "us-east-1", cidr = "10.12.0.0/16" }
    asia   = { region = "ap-southeast-1", cidr = "10.13.0.0/16" }
    africa = { region = "af-south-1", cidr = "10.14.0.0/16" }
  }

  sat = {
    eu     = module.sat_eu
    us     = module.sat_us
    asia   = module.sat_asia
    africa = module.sat_africa
  }

  namespace   = "${local.env}.openhr.internal"
  clamav_home = "clamav.${local.namespace}:3310"
  clamav_addr = merge(
    { uk = local.clamav_home },
    { for k, m in local.sat : k => m.clamav_addr },
  )

  s3_endpoint = merge(
    { uk = "https://s3.${var.region}.amazonaws.com" },
    { for k, v in local.satellites : k => "https://s3.${v.region}.amazonaws.com" },
  )
  bucket_names = merge(
    { uk = module.uploads.bucket_names["uk"] },
    { for k, m in local.sat : k => m.bucket_names[k] },
  )
  bucket_arns = concat(module.uploads.bucket_arns, flatten([for m in values(local.sat) : m.bucket_arns]))

  # --- secrets (EMPTY in Terraform; values are set out-of-band, see README) ---
  secret_names = toset(concat(
    [
      "JWT_SECRET", "TOTP_ENCRYPTION_KEY", "ADMIN_BOOTSTRAP_PASSWORD",
      "ANGLEHR_REAPPLY_HMAC_KEY", "R2_ACCESS_KEY", "R2_SECRET_KEY",
      "SMTP_USER", "SMTP_PASSWORD", "FLUVIO_BASIC_AUTH_USER", "FLUVIO_BASIC_AUTH_PASSWORD",
    ],
    [for r in local.regions : "ANGLEHR_${upper(r)}_SENSITIVE_KEYS"],
    var.enable_companies_house_key ? ["COMPANIES_HOUSE_API_KEY"] : [],
  ))

  # DSNs are derived from infrastructure, so Terraform writes them. Global + UK share the home instance;
  # every other region has its own instance reached over peering.
  dsn_names = concat(["DB_URL_GLOBAL"], [for r in local.regions : "ANGLEHR_${upper(r)}_POSTGRES_DSN"])
  dsn_home  = "postgres://${module.rds.username}:${module.rds.password}@${module.rds.address}:5432"
  dsn_values = merge(
    {
      DB_URL_GLOBAL           = "${local.dsn_home}/openhr_global?sslmode=require"
      ANGLEHR_UK_POSTGRES_DSN = "${local.dsn_home}/openhr_uk?sslmode=require"
    },
    { for k, m in local.sat : "ANGLEHR_${upper(k)}_POSTGRES_DSN" => "postgres://${m.rds_username}:${m.rds_password}@${m.rds_address}:5432/openhr_${k}?sslmode=require" },
  )
  dsn_secrets = { for k in local.dsn_names : k => aws_secretsmanager_secret.dsn[k].arn }

  app_secrets = merge(
    local.dsn_secrets,
    { for k in ["JWT_SECRET", "TOTP_ENCRYPTION_KEY", "ADMIN_BOOTSTRAP_PASSWORD", "ANGLEHR_REAPPLY_HMAC_KEY", "R2_ACCESS_KEY", "R2_SECRET_KEY", "SMTP_USER", "SMTP_PASSWORD"] : k => module.secrets.arns[k] },
    { for r in local.regions : "ANGLEHR_${upper(r)}_SENSITIVE_KEYS" => module.secrets.arns["ANGLEHR_${upper(r)}_SENSITIVE_KEYS"] },
    var.enable_companies_house_key ? { COMPANIES_HOUSE_API_KEY = module.secrets.arns["COMPANIES_HOUSE_API_KEY"] } : {},
  )

  app_env = merge(
    {
      SERVER_PORT           = "8080"
      APP_ENV               = "production"
      LOG_LEVEL             = "info"
      PUBLIC_API_URL        = "https://${local.app_domain}"
      PUBLIC_SITE_URL       = "https://${local.app_domain}"
      APP_URL               = "https://${local.app_domain}"
      ADMIN_APP_URL         = "https://${local.admin_domain}"
      CORS_ALLOWED_ORIGINS  = "https://${local.app_domain},https://${local.admin_domain}"
      FLUVIO_UI_ORIGIN      = "https://${local.fluvio_domain}"
      AUTH_DEFAULT_REGION   = "uk"
      JWT_ACCESS_TTL        = "3600"
      JWT_REFRESH_TTL       = "604800"
      ADMIN_BOOTSTRAP_EMAIL = var.admin_bootstrap_email
      ADMIN_BOOTSTRAP_NAME  = "Bootstrap Admin"
      REDIS_URL             = module.redis.url
      R2_ENDPOINT           = local.s3_endpoint["uk"] # single endpoint today; see CODE_CHANGES #3
      CLAMAV_ADDR           = local.clamav_home       # fallback; see CODE_CHANGES #2
      SMTP_HOST             = "smtp-relay.brevo.com"
      SMTP_PORT             = "587"
      SMTP_FROM             = var.smtp_from
      SMTP_FROM_NAME        = "Angle HR"
    },
    { for r in local.regions : "ANGLEHR_${upper(r)}_R2_BUCKET" => local.bucket_names[r] },
    # Read by the code only after CODE_CHANGES #2 and #3 are made; harmless until then.
    { for r in local.regions : "ANGLEHR_${upper(r)}_R2_ENDPOINT" => local.s3_endpoint[r] },
    { for r in local.regions : "ANGLEHR_${upper(r)}_CLAMAV_ADDR" => local.clamav_addr[r] },
  )

  # The email worker only needs a subset.
  worker_secrets = { for k in ["DB_URL_GLOBAL", "SMTP_USER", "SMTP_PASSWORD"] : k => local.app_secrets[k] }
  worker_env = {
    for k in ["APP_ENV", "LOG_LEVEL", "REDIS_URL", "SMTP_HOST", "SMTP_PORT", "SMTP_FROM", "SMTP_FROM_NAME", "APP_URL", "ADMIN_APP_URL"] : k => local.app_env[k]
  }

  jobs = {
    "hiring-outbox"        = { schedule = "rate(2 minutes)" }
    "hiring-expire"        = { schedule = "rate(1 hour)" }
    "kyb-sweep"            = { schedule = "rate(1 hour)" }
    "hiring-uploads-sweep" = { schedule = "rate(15 minutes)" }
    "hiring-retention"     = { schedule = "cron(0 2 * * ? *)" } # nightly, UTC
  }
}

# ---------------------------------------------------------------- network
module "network" {
  source   = "../../modules/network"
  name     = local.name
  cidr     = local.home_cidr
  nat_mode = var.home_nat_mode
}

resource "aws_security_group" "app" {
  name        = "${local.name}-app"
  description = "ECS tasks (API, workers, jobs, dashboard)"
  vpc_id      = module.network.vpc_id
}

resource "aws_vpc_security_group_egress_rule" "app_all" {
  security_group_id = aws_security_group.app.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

resource "aws_vpc_security_group_ingress_rule" "app_from_alb_api" {
  security_group_id            = aws_security_group.app.id
  referenced_security_group_id = module.alb.security_group_id
  ip_protocol                  = "tcp"
  from_port                    = 8080
  to_port                      = 8080
}

resource "aws_vpc_security_group_ingress_rule" "app_from_alb_fluvio" {
  security_group_id            = aws_security_group.app.id
  referenced_security_group_id = module.alb.security_group_id
  ip_protocol                  = "tcp"
  from_port                    = 80
  to_port                      = 80
}

# UK scanner: runs here in the home region. The other four regions run theirs inside their own VPC.
resource "aws_security_group" "clamav" {
  name        = "${local.name}-clamav"
  description = "clamd for the UK region, reachable from application tasks only"
  vpc_id      = module.network.vpc_id
}

resource "aws_vpc_security_group_ingress_rule" "clamav_from_app" {
  security_group_id            = aws_security_group.clamav.id
  referenced_security_group_id = aws_security_group.app.id
  ip_protocol                  = "tcp"
  from_port                    = 3310
  to_port                      = 3310
}

resource "aws_vpc_security_group_egress_rule" "clamav_all" {
  security_group_id = aws_security_group.clamav.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

# ---------------------------------------------------------------- data stores (home region)
# Holds openhr_global (created with the instance) and openhr_uk (created by the migrate task).
module "rds" {
  source                = "../../modules/rds"
  name                  = local.name
  vpc_id                = module.network.vpc_id
  subnet_ids            = module.network.private_subnet_ids
  allowed_sg_ids        = [aws_security_group.app.id]
  instance_class        = "db.t4g.small"
  multi_az              = var.home_db_multi_az
  backup_retention_days = 7
  deletion_protection   = true
}

module "redis" {
  source         = "../../modules/redis"
  name           = local.name
  vpc_id         = module.network.vpc_id
  subnet_ids     = module.network.private_subnet_ids
  allowed_sg_ids = [aws_security_group.app.id]
}

# UK bucket only; each satellite creates its own.
module "uploads" {
  source          = "../../modules/uploads"
  name_prefix     = local.bucket_prefix
  regions         = ["uk"]
  allowed_origins = ["https://${local.app_domain}"]
  force_destroy   = false
}

resource "aws_secretsmanager_secret" "dsn" {
  for_each                = toset(local.dsn_names)
  name                    = "openhr/${local.env}/${each.key}"
  recovery_window_in_days = 7
}

resource "aws_secretsmanager_secret_version" "dsn" {
  for_each      = toset(local.dsn_names)
  secret_id     = aws_secretsmanager_secret.dsn[each.key].id
  secret_string = local.dsn_values[each.key]
}

module "secrets" {
  source = "../../modules/secrets"
  prefix = "openhr/${local.env}"
  names  = local.secret_names
}

# ---------------------------------------------------------------- registry + IAM
module "ecr" {
  source       = "../../modules/ecr"
  name_prefix  = "openhr"
  repositories = ["server", "email-worker", "jobs", "migrate", "clamav"]
}

# Satellite regions run only ClamAV. Mirror the public image into openhr/clamav once and it replicates to them,
# which avoids Docker Hub rate limits behind a shared NAT address. Only images pushed AFTER this exists replicate.
resource "aws_ecr_replication_configuration" "clamav" {
  replication_configuration {
    rule {
      dynamic "destination" {
        for_each = local.satellites
        content {
          region      = destination.value.region
          registry_id = local.account_id
        }
      }
      repository_filter {
        filter      = "openhr/clamav"
        filter_type = "PREFIX_MATCH"
      }
    }
  }
}

module "iam" {
  source      = "../../modules/iam"
  name        = local.name
  secret_arns = concat(values(module.secrets.arns), values(local.dsn_secrets))
  bucket_arns = local.bucket_arns
}

# ---------------------------------------------------------------- DNS + certificates
module "dns" {
  source    = "../../modules/dns"
  zone_name = var.zone_name
}

# Hands staging.<zone> to the staging account's zone.
resource "aws_route53_record" "staging_delegation" {
  count   = length(var.staging_name_servers) > 0 ? 1 : 0
  zone_id = module.dns.zone_id
  name    = "staging.${var.zone_name}"
  type    = "NS"
  ttl     = 300
  records = var.staging_name_servers
}

module "cert_regional" {
  source  = "../../modules/acm_cert"
  domain  = local.origin_domain
  sans    = [local.fluvio_domain]
  zone_id = module.dns.zone_id
}

module "cert_cloudfront" {
  source    = "../../modules/acm_cert"
  providers = { aws = aws.us_east_1 }
  domain    = local.app_domain
  sans      = [local.admin_domain]
  zone_id   = module.dns.zone_id
}

# ---------------------------------------------------------------- edge
resource "random_password" "origin_verify" {
  length  = 40
  special = false
}

module "alb" {
  source              = "../../modules/alb"
  name                = local.name
  vpc_id              = module.network.vpc_id
  public_subnet_ids   = module.network.public_subnet_ids
  certificate_arn     = module.cert_regional.arn
  origin_verify_value = random_password.origin_verify.result
  fluvio_enabled      = true
  fluvio_host         = local.fluvio_domain
  ops_allowed_cidrs   = var.ops_allowed_cidrs
}

resource "aws_route53_record" "origin" {
  zone_id = module.dns.zone_id
  name    = local.origin_domain
  type    = "A"
  alias {
    name                   = module.alb.dns_name
    zone_id                = module.alb.zone_id
    evaluate_target_health = true
  }
}

resource "aws_route53_record" "fluvio" {
  zone_id = module.dns.zone_id
  name    = local.fluvio_domain
  type    = "A"
  alias {
    name                   = module.alb.dns_name
    zone_id                = module.alb.zone_id
    evaluate_target_health = true
  }
}

module "edge" {
  source = "../../modules/edge"
  providers = {
    aws           = aws
    aws.us_east_1 = aws.us_east_1
  }
  name                = local.name
  bucket_prefix       = local.bucket_prefix
  zone_id             = module.dns.zone_id
  app_domain          = local.app_domain
  admin_domain        = local.admin_domain
  certificate_arn     = module.cert_cloudfront.arn
  origin_domain       = local.origin_domain
  origin_verify_value = random_password.origin_verify.result
  force_destroy       = false
  depends_on          = [aws_route53_record.origin]
}

# ---------------------------------------------------------------- compute
resource "aws_ecs_cluster" "this" {
  name = local.name
  setting {
    name  = "containerInsights"
    value = "enabled"
  }
}

resource "aws_service_discovery_private_dns_namespace" "this" {
  name = local.namespace
  vpc  = module.network.vpc_id
}

resource "aws_service_discovery_service" "clamav" {
  name = "clamav"
  dns_config {
    namespace_id = aws_service_discovery_private_dns_namespace.this.id
    dns_records {
      type = "A"
      ttl  = 10
    }
    routing_policy = "MULTIVALUE"
  }
  health_check_custom_config {
    failure_threshold = 1
  }
}

module "clamav" {
  source               = "../../modules/ecs_service"
  name                 = "clamav"
  cluster_arn          = aws_ecs_cluster.this.arn
  image                = "${module.ecr.urls["clamav"]}:${var.clamav_tag}"
  cpu                  = 1024
  memory               = 3072
  container_port       = 3310
  subnet_ids           = module.network.private_subnet_ids
  security_group_ids   = [aws_security_group.clamav.id]
  desired_count        = var.services_enabled ? 1 : 0
  cloudmap_service_arn = aws_service_discovery_service.clamav.arn
  health_check_command = ["CMD-SHELL", "clamdcheck.sh"]
  start_period_seconds = 300
  execution_role_arn   = module.iam.execution_role_arn
  task_role_arn        = module.iam.task_role_arn
}

module "api" {
  source               = "../../modules/ecs_service"
  name                 = "api"
  cluster_arn          = aws_ecs_cluster.this.arn
  image                = "${module.ecr.urls["server"]}:${var.image_tag}"
  cpu                  = 256
  memory               = 512
  container_port       = 8080
  environment          = local.app_env
  secrets              = local.app_secrets
  subnet_ids           = module.network.private_subnet_ids
  security_group_ids   = [aws_security_group.app.id]
  desired_count        = var.services_enabled ? var.api_desired_count : 0
  target_group_arn     = module.alb.api_target_group_arn
  health_check_command = ["CMD-SHELL", "wget -q -O /dev/null http://localhost:8080/api/v1/countries || exit 1"]
  execution_role_arn   = module.iam.execution_role_arn
  task_role_arn        = module.iam.task_role_arn
  depends_on           = [module.alb]
}

module "email_worker" {
  source             = "../../modules/ecs_service"
  name               = "email-worker"
  cluster_arn        = aws_ecs_cluster.this.arn
  image              = "${module.ecr.urls["email-worker"]}:${var.image_tag}"
  environment        = local.worker_env
  secrets            = local.worker_secrets
  subnet_ids         = module.network.private_subnet_ids
  security_group_ids = [aws_security_group.app.id]
  desired_count      = var.services_enabled ? 1 : 0
  execution_role_arn = module.iam.execution_role_arn
  task_role_arn      = module.iam.task_role_arn
}

module "fluvio_ui" {
  source         = "../../modules/ecs_service"
  name           = "fluvio-ui"
  cluster_arn    = aws_ecs_cluster.this.arn
  image          = var.fluvio_image
  container_port = 80
  environment    = { VITE_API_BASE_URL = "https://${local.app_domain}" }
  secrets = {
    VITE_API_BASIC_AUTH_USER     = module.secrets.arns["FLUVIO_BASIC_AUTH_USER"]
    VITE_API_BASIC_AUTH_PASSWORD = module.secrets.arns["FLUVIO_BASIC_AUTH_PASSWORD"]
  }
  subnet_ids         = module.network.private_subnet_ids
  security_group_ids = [aws_security_group.app.id]
  desired_count      = var.services_enabled ? 1 : 0
  target_group_arn   = module.alb.fluvio_target_group_arn
  execution_role_arn = module.iam.execution_role_arn
  task_role_arn      = module.iam.task_role_arn
  depends_on         = [module.alb]
}

# Scheduled one-shot binaries; all ship in one "jobs" image (see CODE_CHANGES.md).
module "job" {
  for_each            = local.jobs
  source              = "../../modules/ecs_job"
  name                = "${local.name}-${each.key}"
  cluster_arn         = aws_ecs_cluster.this.arn
  image               = "${module.ecr.urls["jobs"]}:${var.image_tag}"
  command             = ["/usr/local/bin/${each.key}"]
  environment         = local.app_env
  secrets             = local.app_secrets
  subnet_ids          = module.network.private_subnet_ids
  security_group_ids  = [aws_security_group.app.id]
  execution_role_arn  = module.iam.execution_role_arn
  task_role_arn       = module.iam.task_role_arn
  schedule_expression = each.value.schedule
  schedule_enabled    = var.jobs_enabled && var.services_enabled
}

# Migrations: run on demand from CI (aws ecs run-task), never on a schedule. Reaches all six databases.
module "migrate" {
  source             = "../../modules/ecs_job"
  name               = "${local.name}-migrate"
  cluster_arn        = aws_ecs_cluster.this.arn
  image              = "${module.ecr.urls["migrate"]}:${var.image_tag}"
  environment        = { FORCE_MIGRATE = "1" }
  secrets            = local.dsn_secrets
  subnet_ids         = module.network.private_subnet_ids
  security_group_ids = [aws_security_group.app.id]
  execution_role_arn = module.iam.execution_role_arn
  task_role_arn      = module.iam.task_role_arn
}

# ---------------------------------------------------------------- CI access
module "github_deploy" {
  source                = "../../modules/github_deploy"
  name                  = local.name
  allowed_subjects      = ["repo:${var.github_repo}:environment:production"]
  ecr_repository_arns   = values(module.ecr.arns)
  cluster_arn           = aws_ecs_cluster.this.arn
  migrate_task_family   = module.migrate.task_definition_family
  pass_role_arns        = [module.iam.execution_role_arn, module.iam.task_role_arn]
  log_group_arn_pattern = "arn:aws:logs:${var.region}:${local.account_id}:log-group:/ecs/${local.name}-migrate:*"
  frontend_bucket_arns  = module.edge.bucket_arns
  distribution_arns     = module.edge.distribution_arns
}
