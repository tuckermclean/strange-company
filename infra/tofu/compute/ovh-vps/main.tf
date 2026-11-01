# OVH VPS compute module (plan 04 section 8). Credentials come only from the provider's environment variables
# (OVH_ENDPOINT, OVH_APPLICATION_KEY, OVH_APPLICATION_SECRET, OVH_CONSUMER_KEY); no credential is a variable.
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

resource "ovh_vps" "node" {
  for_each       = toset(var.node_names)
  ovh_subsidiary = var.ovh_subsidiary
  display_name   = "sc-${var.cluster}-${each.key}"
  plan = [{
    plan_code    = var.plan_code
    duration     = "P1M"
    pricing_mode = "default"
    configuration = [
      { label = "vps_datacenter", value = var.datacenter },
      { label = "vps_os", value = var.os_label },
    ]
  }]
  image_id             = var.image_id
  public_ssh_key       = var.ssh_public_key
  do_not_send_password = true
}
