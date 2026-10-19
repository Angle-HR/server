variable "region" {
  description = "Home region: API, workers, Redis, edge, and the global + UK databases."
  type        = string
  default     = "eu-west-2"
}

variable "zone_name" {
  description = "Apex zone for the product. Created in this (production) account."
  type        = string
  default     = "tryopenhr.com"
}

variable "staging_name_servers" {
  description = "NS records of the staging account's staging.<zone_name> zone. Empty = no delegation record."
  type        = list(string)
  default     = []
}

variable "github_repo" {
  description = "owner/name allowed to deploy via OIDC"
  type        = string
  default     = "Angle-HR/server" # case matters: the OIDC sub claim keeps GitHub's capitalisation
}

variable "image_tag" {
  description = "Immutable tag (git sha) for server, email-worker, jobs and migrate. Never 'latest'."
  type        = string
}

variable "clamav_tag" {
  description = "Tag of the ClamAV image you mirrored into ECR as openhr/clamav (see README)."
  type        = string
  default     = "1.4"
}

variable "services_enabled" {
  description = "Keep false on the first apply (ECR is empty, secrets have no values), then true."
  type        = bool
  default     = false
}

variable "frontends_enabled" {
  description = "Run the client and admin services. Leave false until each repo's CI has pushed its :production image."
  type        = bool
  default     = false
}

variable "jobs_enabled" {
  type    = bool
  default = true
}

variable "api_desired_count" {
  description = "API tasks. 1 matches the light-traffic sizing decision; 2 gives zero-downtime deploys and survives a task crash."
  type        = number
  default     = 1
}

variable "home_db_multi_az" {
  description = "Multi-AZ for the home RDS instance (global registry, auth and the UK region). Decision so far: single-AZ; recommended before real customers."
  type        = bool
  default     = false
}

variable "home_nat_mode" {
  description = "managed = NAT gateway (~$32/mo); instance = fck-nat (~$4/mo, no HA)."
  type        = string
  default     = "managed"
}

variable "ops_allowed_cidrs" {
  description = "CIDRs allowed to reach the fluvio dashboard host directly. Empty = unreachable."
  type        = list(string)
  default     = []
}

variable "smtp_from" {
  type    = string
  default = "no-reply@tryopenhr.com"
}

variable "admin_bootstrap_email" {
  type    = string
  default = "admin@tryopenhr.com"
}

variable "enable_companies_house_key" {
  description = "Create the COMPANIES_HOUSE_API_KEY secret (set a real value before enabling services)."
  type        = bool
  default     = false
}

variable "fluvio_image" {
  type    = string
  default = "ghcr.io/software78/fluvio_ui:1.0.1"
}
