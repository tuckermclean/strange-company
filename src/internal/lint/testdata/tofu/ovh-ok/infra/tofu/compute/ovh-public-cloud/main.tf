variable "region" {
  type = string
}

resource "ovh_cloud_project_instance" "node" {
  region = var.region
}

