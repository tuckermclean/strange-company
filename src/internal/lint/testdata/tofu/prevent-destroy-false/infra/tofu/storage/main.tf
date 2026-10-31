resource "aws_s3_bucket" "state" {
  bucket = "lab-tofu-state"
  lifecycle {
    prevent_destroy = false
  }
}
