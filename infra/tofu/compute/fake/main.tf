# Fake compute module (CD-70(d), plans/reviews/consolidation-decisions.md). It records the nodes of the fake provider in
# the OpenTofu state so the state commands (plan 06 sections 20.1 to 20.3) have a compute module to work on in tests.
# It uses only the built-in terraform_data resource: no provider block, no required_providers, no data source. The backend
# is rendered by sc-bootstrap. The module refuses to run outside SC_TEST_MODE=1.

variable "cluster" {
  description = "Cluster name."
  type        = string
  default     = ""
}

variable "nodes" {
  description = "Node name to internal IP, as the fake provider reports them."
  type        = map(string)
  default     = {}
}

variable "sc_test_mode" {
  description = "Value of SC_TEST_MODE (TF_VAR_sc_test_mode). The module only runs when it is 1."
  type        = string
  default     = ""

  validation {
    condition     = var.sc_test_mode == "1"
    error_message = "The fake compute module only runs with SC_TEST_MODE=1; it is a test fixture and never part of a real installation."
  }
}

resource "terraform_data" "node" {
  for_each = var.nodes

  input = {
    cluster = var.cluster
    name    = each.key
    ip      = each.value
  }
}

output "nodes" {
  description = "Node name to internal IP."
  value       = { for k, v in var.nodes : k => v }
}
