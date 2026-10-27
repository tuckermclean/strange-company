# The bucket set of plan 14 section 9.1 (B1..B10), created in `create` mode by the storage root module
# (plan 06 section 14.2). Versioning is Enabled on every bucket (14-R-1), public access is blocked, and the
# lifecycle rule abort-incomplete-multipart (7 days) is on all of them. Retention rules per bucket come from
# plan 14's source file and plan 43; this module carries only what every bucket shares.
#
# Every bucket carries lifecycle { prevent_destroy = true } and infra/tofu/storage/guard.yaml allows no delete
# action: sc-bootstrap can never plan a bucket for deletion (`make tofu-validate`, 06-AC-9).

terraform {
  required_version = ">= 1.13.1"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.69.0"
    }
  }
}

variable "bucket_prefix" {
  description = "S3_BUCKET_PREFIX: every bucket is named <prefix>-<purpose>."
  type        = string
}

variable "cluster" {
  description = "Cluster name, reported in the outputs."
  type        = string
}

variable "region" {
  description = "S3_REGION."
  type        = string
}

variable "endpoint" {
  description = "S3_ENDPOINT."
  type        = string
}

variable "path_style" {
  description = "S3_FORCE_PATH_STYLE."
  type        = bool
  default     = false
}

provider "aws" {
  region                      = var.region
  s3_use_path_style           = var.path_style
  skip_credentials_validation = true
  skip_region_validation      = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true

  endpoints {
    s3 = var.endpoint
  }
}

locals {
  # plan 14 section 9.1; the order is the B1..B10 order.
  purposes = [
    "tofu-state",
    "recovery",
    "bootstrap-seed",
    "pg-backups",
    "longhorn-backups",
    "etcd-snapshots",
    "forgejo-objects",
    "repo-backups",
    "evidence",
    "telemetry",
  ]
}

resource "aws_s3_bucket" "this" {
  for_each = toset(local.purposes)
  bucket   = "${var.bucket_prefix}-${each.key}"

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "this" {
  for_each = aws_s3_bucket.this
  bucket   = each.value.id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "this" {
  for_each                = aws_s3_bucket.this
  bucket                  = each.value.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "this" {
  for_each = aws_s3_bucket.this
  bucket   = each.value.id

  rule {
    id     = "abort-incomplete-multipart"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  depends_on = [aws_s3_bucket_versioning.this]
}

output "cluster" {
  value = var.cluster
}

output "buckets" {
  description = "Bucket purpose to bucket name."
  value       = { for purpose, b in aws_s3_bucket.this : purpose => b.id }
}
