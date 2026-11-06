variable "cluster" {
  type        = string
  description = "Cluster name; VPS display names are sc-<cluster>-<node>."
}

variable "node_names" {
  type        = list(string)
  description = "Node names, for example [\"node-01\"]; later [\"node-01\", \"node-02\", \"node-03\"]."
}

variable "ovh_subsidiary" {
  type        = string
  description = "OVH subsidiary of the order, for example FR."
}

variable "plan_code" {
  type        = string
  description = "Catalogue plan code of the machine class (infra/catalog/prices/ovh.yaml)."
}

variable "datacenter" {
  type        = string
  description = "VPS datacenter code, for example GRA."
}

variable "os_label" {
  type        = string
  description = "vps_os configuration label, for example Debian 13."
}

variable "image_id" {
  type        = string
  description = "OVH image id; required by the provider when public_ssh_key is set."
}

variable "ssh_public_key" {
  type        = string
  description = "Public SSH key installed on every node (not a secret; exempt from the ephemeral rule by name)."
}
