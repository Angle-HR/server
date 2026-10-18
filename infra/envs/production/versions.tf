terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.80"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }

  # Partial configuration: terraform init -backend-config=backend.hcl
  backend "s3" {}
}

locals {
  default_tags = {
    Project     = "openhr"
    Environment = "production"
    ManagedBy   = "terraform"
  }
}

# Home region: API, workers, global + UK database, Redis, edge.
provider "aws" {
  region = var.region
  default_tags { tags = local.default_tags }
}

# Satellite data regions. One alias each because Terraform cannot loop over providers.
provider "aws" {
  alias  = "eu"
  region = "eu-central-1"
  default_tags { tags = local.default_tags }
}

provider "aws" {
  alias  = "us"
  region = "us-east-1"
  default_tags { tags = local.default_tags }
}

provider "aws" {
  alias  = "asia"
  region = "ap-southeast-1"
  default_tags { tags = local.default_tags }
}

# af-south-1 is an opt-in region: enable it in the account (Billing > Account > AWS Regions) before the first plan.
provider "aws" {
  alias  = "africa"
  region = "af-south-1"
  default_tags { tags = local.default_tags }
}

# CloudFront certificates and WAF web ACLs must live in us-east-1.
provider "aws" {
  alias  = "us_east_1"
  region = "us-east-1"
  default_tags { tags = local.default_tags }
}
