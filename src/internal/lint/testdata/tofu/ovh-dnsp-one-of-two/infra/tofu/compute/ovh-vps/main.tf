resource "ovh_vps" "a" {
  do_not_send_password = true
}

resource "ovh_vps" "b" {
  display_name = "b"
}

