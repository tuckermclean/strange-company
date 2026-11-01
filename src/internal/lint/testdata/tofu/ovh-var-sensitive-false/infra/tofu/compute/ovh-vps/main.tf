resource "ovh_vps" "node" {
  for_each             = toset(var.node_names)
  display_name         = "sc-${each.key}"
  do_not_send_password = true
}

