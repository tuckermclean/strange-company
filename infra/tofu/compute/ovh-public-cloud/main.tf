# OVH Public Cloud instance compute module (plan 04 section 8, CD-72: nested blocks at ovh/ovh v2.22.0).
# Credentials come only from the provider's environment variables; no credential is a variable.
terraform {
  required_version = ">= 1.13.1"

  required_providers {
    ovh = {
      source  = "ovh/ovh"
      version = "= 2.22.0"
    }
  }
}

provider "ovh" {}

resource "ovh_cloud_project_instance" "node" {
  for_each       = toset(var.node_names)
  service_name   = var.cloud_project_id
  region         = var.region
  name           = "sc-${var.cluster}-${each.key}"
  billing_period = "hourly"
  flavor {
    flavor_id = var.flavor_id
  }
  boot_from {
    image_id = var.image_id
  }
  ssh_key_create {
    name       = "sc-${var.cluster}-${each.key}"
    public_key = var.ssh_public_key
  }
  network {
    public = true
  }
}
