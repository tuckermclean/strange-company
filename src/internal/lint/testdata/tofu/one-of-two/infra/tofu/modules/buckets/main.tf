resource "aws_s3_bucket" "protected" {
  bucket = "a-bucket"
  lifecycle {
    prevent_destroy = true
  }
}

resource "aws_s3_bucket" "forgotten" {
  bucket = "b-bucket"
}
