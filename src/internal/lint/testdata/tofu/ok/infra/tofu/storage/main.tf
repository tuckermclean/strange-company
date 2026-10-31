locals {
  buckets = ["tofu-state", "recovery"]
}

resource "aws_s3_bucket" "this" {
  for_each = toset(local.buckets)
  bucket   = "lab-${each.key}"

  lifecycle {
    prevent_destroy = true
  }
}
