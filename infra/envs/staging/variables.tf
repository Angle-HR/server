variable "region" {
  type    = string
  default = "eu-west-2"
}

variable "zone_name" {
  description = "Route 53 sub-zone for this environment; delegate it from the parent zone."
  type        = string
  default     = "staging.tryopenhr.com"
}

variable "github_repo" {
  description = "owner/name allowed to deploy via OIDC"
  type        = string
  default     = "Angle-HR/server" # case matters: the OIDC sub claim keeps GitHub's capitalisation
}

variable "image_tag" {
  description = "Immutable tag (git sha) pushed to ECR for server, email-worker, jobs and migrate. Never 'latest'."
  type        = string
}

variable "services_enabled" {
  description = "Keep false on the first apply (ECR is empty and secrets have no values yet), then true."
  type        = bool
  default     = false
}

variable "jobs_enabled" {
  type    = bool
  default = true
}

variable "regions" {
  description = "Logical data regions. The server requires all five at startup."
  type        = set(string)
  default     = ["uk", "us", "africa", "eu", "asia"]
}

variable "ops_allowed_cidrs" {
  description = "CIDRs allowed to reach the fluvio dashboard host directly (office / VPN). Empty = unreachable."
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

variable "clamav_image" {
  type    = string
  default = "clamav/clamav:1.4"
}

variable "fluvio_image" {
  type    = string
  default = "ghcr.io/software78/fluvio_ui:1.0.1"
}

variable "bastion_enabled" {
  description = "SSM bastion for DB access from a laptop (infra/scripts/db-tunnel.sh). ~$3/month while on."
  type        = bool
  default     = true
}

variable "rds_keep_stopped" {
  description = "Stop RDS again when AWS auto-starts it after 7 days stopped. Manual starts are not affected."
  type        = bool
  default     = true
}
