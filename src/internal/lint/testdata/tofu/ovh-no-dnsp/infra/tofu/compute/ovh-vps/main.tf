variable "dnsp" {
  type = bool
}

resource "ovh_vps" "node" {
  display_name = "sc"
}

