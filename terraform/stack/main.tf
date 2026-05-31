terraform {
  required_version = ">= 1.9"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.39"
    }
    tls = {
      source  = "hashicorp/tls"
      version = "~> 4.0"
    }
  }
}

module "tags" {
  source = "git::https://github.com/FelipeFuhr/ffreis-platform-terraform-modules.git//modules/tagging?ref=v2.0.0"

  project           = "platform-org"
  environment       = "prod"
  stack             = "platform-org"
  layer             = "bootstrap"
  terraform_repo    = "ffreis-platform-org"
  terraform_root    = "terraform/stack"
  terraform_version = "1.9.8"
  cost_center       = "platform"
  domain            = "internal"
  lifecycle_state   = "production"
  fixed_cost_tier   = "low"
}

provider "aws" {
  region  = var.aws_region
  profile = var.aws_profile

  default_tags {
    tags = module.tags.tags
  }
}
