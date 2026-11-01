variable "cluster" {
  type        = string
  description = "Cluster name; instance names are sc-<cluster>-<node>."
}

variable "node_names" {
  type        = list(string)
  description = "Node names, for example [\"node-01\"]."
}

variable "cloud_project_id" {
  type        = string
  description = "Public Cloud project id (service_name)."
}

variable "region" {
  type        = string
  description = "Public Cloud region, for example GRA11."
}

variable "flavor_id" {
  type        = string
  description = "Flavor id of the machine class, for example b3-16."
}

variable "image_id" {
  type        = string
  description = "Image id to boot from."
}

variable "ssh_public_key" {
  type        = string
  description = "Public SSH key installed on every node (not a secret; exempt from the ephemeral rule by name)."
}
