variable "dnsp" {
  type = bool
}

resource "ovh_vps" "node" {
  do_not_send_password = var.dnsp
}

