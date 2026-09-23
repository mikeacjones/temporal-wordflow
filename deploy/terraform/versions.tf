terraform {
  required_version = ">= 1.10.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    external = {
      source  = "hashicorp/external"
      version = "~> 2.3"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.7"
    }
    temporalcloud = {
      source  = "temporalio/temporalcloud"
      version = "~> 1.9"
    }
    time = {
      source  = "hashicorp/time"
      version = "~> 0.13"
    }
  }
}

provider "aws" {
  region = var.aws_region

  default_tags {
    tags = {
      Application = var.project_name
      ManagedBy   = "Terraform"
    }
  }
}

provider "temporalcloud" {
  allowed_account_id = var.temporal_account_id
}
