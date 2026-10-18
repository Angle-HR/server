data "aws_caller_identity" "current" {}

locals {
  env  = "staging"
  name = "openhr-staging"
  # S3 names are global; the account id keeps them unique.
  bucket_prefix = "openhr-staging-${data.aws_caller_identity.current.account_id}"

  app_domain    = "app.${var.zone_name}"
  admin_domain  = "admin.${var.zone_name}"
  origin_domain = "origin.${var.zone_name}"
  fluvio_domain = "fluvio.${var.zone_name}"

  namespace    = "${local.env}.openhr.internal"
  clamav_addr  = "clamav.${local.namespace}:3310"
  upper        = { for r in var.regions : r => upper(r) }
  image_prefix = module.ecr.urls

  # --- secrets (EMPTY in Terraform; values are set out-of-band, see README) ---
  secret_names = toset(concat(
    [
      "JWT_SECRET", "TOTP_ENCRYPTION_KEY", "ADMIN_BOOTSTRAP_PASSWORD",
      "ANGLEHR_REAPPLY_HMAC_KEY", "R2_ACCESS_KEY", "R2_SECRET_KEY",
      "SMTP_USER", "SMTP_PASSWORD", "FLUVIO_BASIC_AUTH_USER", "FLUVIO_BASIC_AUTH_PASSWORD",
    ],
    [for r in var.regions : "ANGLEHR_${upper(r)}_SENSITIVE_KEYS"],
    var.enable_companies_house_key ? ["COMPANIES_HOUSE_API_KEY"] : [],
  ))

  # DSNs are derived from infrastructure, so Terraform writes them (see aws_secretsmanager_secret_version.dsn).
  dsn_names = concat(["DB_URL_GLOBAL"], [for r in var.regions : "ANGLEHR_${upper(r)}_POSTGRES_DSN"])
  dsn_base  = "postgres://${module.rds.username}:${module.rds.password}@${module.rds.address}:5432"
  dsn_values = merge(
    { DB_URL_GLOBAL = "${local.dsn_base}/openhr_global?sslmode=require" },
    { for r in var.regions : "ANGLEHR_${upper(r)}_POSTGRES_DSN" => "${local.dsn_base}/openhr_${r}?sslmode=require" },
  )
  dsn_secrets = { for k in local.dsn_names : k => aws_secretsmanager_secret.dsn[k].arn }

  app_secrets = merge(
    local.dsn_secrets,
    { for k in ["JWT_SECRET", "TOTP_ENCRYPTION_KEY", "ADMIN_BOOTSTRAP_PASSWORD", "ANGLEHR_REAPPLY_HMAC_KEY", "R2_ACCESS_KEY", "R2_SECRET_KEY", "SMTP_USER", "SMTP_PASSWORD"] : k => module.secrets.arns[k] },
    { for r in var.regions : "ANGLEHR_${upper(r)}_SENSITIVE_KEYS" => module.secrets.arns["ANGLEHR_${upper(r)}_SENSITIVE_KEYS"] },
    var.enable_companies_house_key ? { COMPANIES_HOUSE_API_KEY = module.secrets.arns["COMPANIES_HOUSE_API_KEY"] } : {},
  )

  app_env = merge(
    {
      SERVER_PORT           = "8080"
      APP_ENV               = "staging"
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
      R2_ENDPOINT           = "https://s3.${var.region}.amazonaws.com"
      CLAMAV_ADDR           = local.clamav_addr
      SMTP_HOST             = "smtp-relay.brevo.com"
      SMTP_PORT             = "587"
      SMTP_FROM             = var.smtp_from
      SMTP_FROM_NAME        = "Angle HR"
    },
    { for r in var.regions : "ANGLEHR_${upper(r)}_R2_BUCKET" => module.uploads.bucket_names[r] },
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
    "hiring-retention"     = { schedule = "cron(0 10 * * ? *)" } # 10:00 UTC. Only succeeds while staging is started (scripts/staging-power.sh). Production: 02:00 UTC.
  }

  running_counts = {
    api          = 1
    email-worker = 1
    fluvio-ui    = 1
    clamav       = 1
  }
}

# ---------------------------------------------------------------- network
module "network" {
  source = "../../modules/network"
  name   = local.name
  # fck-nat on a t4g.nano (~$4/mo) instead of a managed NAT gateway (~$37/mo). No HA: if the instance dies, private
  # subnets lose internet egress (SMTP, ClamAV updates, Docker Hub) until it is replaced. Fine for staging.
  nat_mode = "instance"
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

resource "aws_security_group" "clamav" {
  name        = "${local.name}-clamav"
  description = "clamd, reachable from application tasks only"
  vpc_id      = module.network.vpc_id
}

resource "aws_vpc_security_group_ingress_rule" "clamav_from_app" {
  security_group_id            = aws_security_group.clamav.id
  referenced_security_group_id = aws_security_group.app.id
  ip_protocol                  = "tcp"
  from_port                    = 3310
  to_port                      = 3310
}

# freshclam + image pulls
resource "aws_vpc_security_group_egress_rule" "clamav_all" {
  security_group_id = aws_security_group.clamav.id
  cidr_ipv4         = "0.0.0.0/0"
  ip_protocol       = "-1"
}

# ---------------------------------------------------------------- data stores
module "rds" {
  source                = "../../modules/rds"
  name                  = local.name
  vpc_id                = module.network.vpc_id
  subnet_ids            = module.network.private_subnet_ids
  allowed_sg_ids        = concat([aws_security_group.app.id], module.bastion[*].security_group_id)
  instance_class        = "db.t4g.small"
  multi_az              = false
  backup_retention_days = 7
  deletion_protection   = false # staging only
}

# DB access from a laptop: infra/scripts/db-tunnel.sh. Off = no instance, no cost.
module "bastion" {
  count     = var.bastion_enabled ? 1 : 0
  source    = "../../modules/bastion"
  name      = local.name
  vpc_id    = module.network.vpc_id
  subnet_id = module.network.private_subnet_ids[0]
}

module "redis" {
  source         = "../../modules/redis"
  name           = local.name
  vpc_id         = module.network.vpc_id
  subnet_ids     = module.network.private_subnet_ids
  allowed_sg_ids = [aws_security_group.app.id]
}

module "uploads" {
  source          = "../../modules/uploads"
  name_prefix     = local.bucket_prefix
  regions         = var.regions
  allowed_origins = ["https://${local.app_domain}"]
  force_destroy   = true # staging only
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
  repositories = ["server", "email-worker", "jobs", "migrate"]
}

module "iam" {
  source      = "../../modules/iam"
  name        = local.name
  secret_arns = concat(values(module.secrets.arns), values(local.dsn_secrets))
  bucket_arns = module.uploads.bucket_arns
}

# ---------------------------------------------------------------- DNS + certificates
module "dns" {
  source    = "../../modules/dns"
  zone_name = var.zone_name
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
  force_destroy       = true # staging only
  depends_on          = [aws_route53_record.origin]
}

# ---------------------------------------------------------------- compute
resource "aws_ecs_cluster" "this" {
  name = local.name
  setting {
    name  = "containerInsights"
    value = "disabled" # staging: saves ~$10-15/mo; ECS service metrics in CloudWatch are still free
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
  source      = "../../modules/ecs_service"
  name        = "clamav"
  cluster_arn = aws_ecs_cluster.this.arn
  image       = var.clamav_image
  cpu         = 512
  memory      = 2048
  # The signature database is ~1.2 GB in memory. A concurrent reload holds two copies, which does not fit in 2 GB, so
  # clamd reloads in place instead: scans wait (uploads fail closed) for the ~30 s a reload takes, a few times a day.
  environment          = { CLAMD_CONF_ConcurrentDatabaseReload = "no" }
  container_port       = 3310
  subnet_ids           = module.network.private_subnet_ids
  security_group_ids   = [aws_security_group.clamav.id]
  desired_count        = var.services_enabled ? local.running_counts.clamav : 0
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
  image                = "${local.image_prefix["server"]}:${var.image_tag}"
  cpu                  = 256
  memory               = 512
  container_port       = 8080
  environment          = local.app_env
  secrets              = local.app_secrets
  subnet_ids           = module.network.private_subnet_ids
  security_group_ids   = [aws_security_group.app.id]
  desired_count        = var.services_enabled ? local.running_counts.api : 0
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
  image              = "${local.image_prefix["email-worker"]}:${var.image_tag}"
  environment        = local.worker_env
  secrets            = local.worker_secrets
  subnet_ids         = module.network.private_subnet_ids
  security_group_ids = [aws_security_group.app.id]
  desired_count      = var.services_enabled ? local.running_counts["email-worker"] : 0
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
  desired_count      = var.services_enabled ? local.running_counts["fluvio-ui"] : 0
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
  image               = "${local.image_prefix["jobs"]}:${var.image_tag}"
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

# Migrations: run on demand from CI (aws ecs run-task), never on a schedule.
module "migrate" {
  source             = "../../modules/ecs_job"
  name               = "${local.name}-migrate"
  cluster_arn        = aws_ecs_cluster.this.arn
  image              = "${local.image_prefix["migrate"]}:${var.image_tag}"
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
  allowed_subjects      = ["repo:${var.github_repo}:environment:staging"]
  ecr_repository_arns   = values(module.ecr.arns)
  cluster_arn           = aws_ecs_cluster.this.arn
  migrate_task_family   = module.migrate.task_definition_family
  pass_role_arns        = [module.iam.execution_role_arn, module.iam.task_role_arn]
  log_group_arn_pattern = "arn:aws:logs:${var.region}:${data.aws_caller_identity.current.account_id}:log-group:/ecs/${local.name}-migrate:*"
  frontend_bucket_arns  = module.edge.bucket_arns
  distribution_arns     = module.edge.distribution_arns
}
