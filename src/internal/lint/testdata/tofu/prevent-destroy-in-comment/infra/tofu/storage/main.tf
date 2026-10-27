# prevent_destroy = true
resource "aws_s3_bucket" "state" {
  bucket = "lab-tofu-state"
  tags = {
    note = "prevent_destroy = true"
  }
  /* lifecycle { prevent_destroy = true } */
}
