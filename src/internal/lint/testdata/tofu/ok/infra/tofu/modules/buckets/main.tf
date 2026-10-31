variable "name" { type = string }

resource "aws_s3_bucket" "extra" {
  bucket = var.name
  lifecycle {
    ignore_changes  = [tags]
    prevent_destroy = true
  }
}

resource "aws_s3_bucket_versioning" "extra" {
  bucket = aws_s3_bucket.extra.id
  versioning_configuration { status = "Enabled" }
}
