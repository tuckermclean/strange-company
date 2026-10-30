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

variable "access_key" {
  description = "Access key of the main account (sc-bootstrap passes it as TF_VAR_access_key)."
  type        = string
  default     = ""
}

variable "secret_key" {
  description = "Secret key of the main account."
  type        = string
  default     = ""
  sensitive   = true
}

# The optional second account (S3_STATE_*, plan 06 section 14.2) holds the state buckets B1 and B2. Left empty, the
# aliased provider follows the main account.
variable "state_endpoint" {
  description = "S3_STATE_ENDPOINT; empty means the main endpoint."
  type        = string
  default     = ""
}

variable "state_region" {
  description = "S3_STATE_REGION; empty means the main region."
  type        = string
  default     = ""
}

variable "state_path_style" {
  description = "S3_STATE_FORCE_PATH_STYLE; null means the main setting."
  type        = bool
  default     = null
}

variable "state_access_key" {
  description = "Access key of the state account; empty means the main account's."
  type        = string
  default     = ""
}

variable "state_secret_key" {
  description = "Secret key of the state account."
  type        = string
  default     = ""
  sensitive   = true
}

# Credentials stay explicit and empty means "from the environment" (AWS_* of the process, as before).
provider "aws" {
  region                      = var.region
  s3_use_path_style           = var.path_style
  access_key                  = var.access_key != "" ? var.access_key : null
  secret_key                  = var.secret_key != "" ? var.secret_key : null
  skip_credentials_validation = true
  skip_region_validation      = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true

  endpoints {
    s3 = var.endpoint
  }
}

# The state account: the same shape, fed only by the state_* variables (main values fill the empty ones, so a single
# account install needs no extra input).
provider "aws" {
  alias                       = "state"
  region                      = var.state_region != "" ? var.state_region : local.main_region
  s3_use_path_style           = var.state_path_style != null ? var.state_path_style : local.main_path_style
  access_key                  = var.state_access_key != "" ? var.state_access_key : local.main_access_key
  secret_key                  = var.state_secret_key != "" ? var.state_secret_key : local.main_secret_key
  skip_credentials_validation = true
  skip_region_validation      = true
  skip_requesting_account_id  = true
  skip_metadata_api_check     = true

  endpoints {
    s3 = var.state_endpoint != "" ? var.state_endpoint : local.main_endpoint
  }
}

locals {
  main_region     = var.region
  main_endpoint   = var.endpoint
  main_path_style = var.path_style
  main_access_key = var.access_key != "" ? var.access_key : null
  main_secret_key = var.secret_key != "" ? var.secret_key : null

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

  # B1 and B2 live in the state account (provider aws.state); the other eight in the main account.
  state_purposes = ["tofu-state", "recovery"]
  main_purposes  = [for p in local.purposes : p if !contains(local.state_purposes, p)]
}

resource "aws_s3_bucket" "this" {
  for_each = toset(local.main_purposes)
  bucket   = "${var.bucket_prefix}-${each.key}"

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "this" {
  for_each = toset(local.main_purposes)
  bucket   = aws_s3_bucket.this[each.key].id

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "this" {
  for_each                = toset(local.main_purposes)
  bucket                  = aws_s3_bucket.this[each.key].id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "this" {
  for_each = toset(local.main_purposes)
  bucket   = aws_s3_bucket.this[each.key].id

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

resource "aws_s3_bucket" "state" {
  for_each = toset(local.state_purposes)
  bucket   = "${var.bucket_prefix}-${each.key}"
  provider = aws.state

  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "state" {
  for_each = toset(local.state_purposes)
  bucket   = aws_s3_bucket.state[each.key].id
  provider = aws.state

  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_public_access_block" "state" {
  for_each                = toset(local.state_purposes)
  bucket                  = aws_s3_bucket.state[each.key].id
  provider                = aws.state
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_lifecycle_configuration" "state" {
  for_each = toset(local.state_purposes)
  bucket   = aws_s3_bucket.state[each.key].id
  provider = aws.state

  rule {
    id     = "abort-incomplete-multipart"
    status = "Enabled"

    filter {}

    abort_incomplete_multipart_upload {
      days_after_initiation = 7
    }
  }

  depends_on = [aws_s3_bucket_versioning.state]
}

# Installations created before the alias kept B1 and B2 in the "this" resources: the state follows them.
moved {
  from = aws_s3_bucket.this["tofu-state"]
  to   = aws_s3_bucket.state["tofu-state"]
}

moved {
  from = aws_s3_bucket.this["recovery"]
  to   = aws_s3_bucket.state["recovery"]
}

moved {
  from = aws_s3_bucket_versioning.this["tofu-state"]
  to   = aws_s3_bucket_versioning.state["tofu-state"]
}

moved {
  from = aws_s3_bucket_versioning.this["recovery"]
  to   = aws_s3_bucket_versioning.state["recovery"]
}

moved {
  from = aws_s3_bucket_public_access_block.this["tofu-state"]
  to   = aws_s3_bucket_public_access_block.state["tofu-state"]
}

moved {
  from = aws_s3_bucket_public_access_block.this["recovery"]
  to   = aws_s3_bucket_public_access_block.state["recovery"]
}

moved {
  from = aws_s3_bucket_lifecycle_configuration.this["tofu-state"]
  to   = aws_s3_bucket_lifecycle_configuration.state["tofu-state"]
}

moved {
  from = aws_s3_bucket_lifecycle_configuration.this["recovery"]
  to   = aws_s3_bucket_lifecycle_configuration.state["recovery"]
}

output "cluster" {
  value = var.cluster
}

output "buckets" {
  description = "Bucket purpose to bucket name."
  value       = merge({ for purpose, b in aws_s3_bucket.this : purpose => b.id }, { for purpose, b in aws_s3_bucket.state : purpose => b.id })
}
