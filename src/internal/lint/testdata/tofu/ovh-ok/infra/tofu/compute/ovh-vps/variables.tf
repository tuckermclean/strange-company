variable "node_names" {
  type = list(string)
}

variable "api_token" {
  type      = string
  sensitive = true
  ephemeral = true
}

variable "root_password" {
  type      = string
  sensitive = true
  ephemeral = true
}

