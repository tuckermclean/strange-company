# ovh-public-cloud plan fixtures

source: derived, not recorded (CD-71, no OVH account). Every `*.json` here is the output of a real
`tofu plan -out` + `tofu show -json` (OpenTofu 1.13.1, format_version 1.2) of the scratch root module below
with the pinned `ovh/ovh` provider v2.22.0 and dummy credentials (`OVH_APPLICATION_KEY=dummy` and so on).
They are re-recorded with SC-CI-OVH-READONLY credentials before GATE-P02 (replace the derived documents; keep the
file names and `expect.yaml`).

## How the provider plans without an account

The provider's configure step calls `GET /auth/time` and `GET /auth/details`. Planning therefore ran against a
throwaway local HTTP stub (`OVH_ENDPOINT=http://127.0.0.1:<port>`, the provider accepts a URL as endpoint) that
answered `/auth/time` with the current epoch and every other path with an empty JSON object. No request left the
machine, no resource read happened (`-refresh=false` where state was used), and no `apply` ran. The stub and the
script that drove the plans are not committed; a `make tofu-fixtures` target may replace them (planned path).

## Fixtures and how each was derived

| File | Derivation | Guard verdict |
|---|---|---|
| valid-create.json | plan of the module below, `node_names=["node-01"]`, no state | pass |
| valid-expand.json | same variables with `node_names` of 3 names against a state holding node-01 (hand-built from valid-create.json, see below) | pass (node-01 no-op, two creates) |
| product-mismatch.json | `b3-8` instead of the expected product code | GUARD_PRODUCT_MISMATCH |
| region-mismatch.json | region SBG5 instead of GRA11 | GUARD_REGION_MISMATCH |
| ssh-key-mismatch.json | another example public key | GUARD_SSH_KEY_MISMATCH |
| replace.json | the region-mismatch variables planned against the state holding node-01: the provider marks the attribute "forces replacement", so the action is delete+create | GUARD_REPLACE_FORBIDDEN |
| extra-resource-type.json | valid-create plus an `ovh_domain_zone_record` in the same root module | GUARD_UNEXPECTED_RESOURCE |
| destroy-valid.json | `tofu plan -destroy` against the state holding node-01 | pass with guard-destroy.yaml |
| destroy-stray-create.json | a copy of valid-create.json: a create judged by guard-destroy.yaml | GUARD_ACTION_FORBIDDEN |

The state used for expand, replace and destroy was built by a throwaway script from valid-create.json: every
attribute of the planned resource kept, every "known after apply" attribute replaced by an invented value
(`computed-<name>`, 1, false, `[]`). It never touched an OVH API. Values in the documents (ids, project id,
image id, flavor, SSH key) are invented; the SSH key is the documented example key, not a real key.

## Scratch module the fixtures were planned with

The module the implementer writes under `infra/tofu/compute/ovh-public-cloud` must produce plans of this shape (same resource type, same
attribute names, same block order, so the `expect.yaml` paths below hold). Variable names are free except that
none may match `(?i)(secret|password|token|key)` unless it is `sensitive` and `ephemeral`, or also matches `(?i)public` (04-AC-6, amended); an
ephemeral variable cannot feed a normal resource argument in OpenTofu 1.13.1 ("Ephemeral value used in
non-ephemeral context"), so the module names its public key variable `ssh_public_key` (exempt by the `public` rule). The recorded plan JSON keeps the scratch name `ssh_login_pub` in its `variables`/`configuration` sections (left as recorded, CD-71); the variable name is not a guarded value.

```hcl
terraform {
  required_providers {
    ovh = {
      source  = "ovh/ovh"
      version = "= 2.22.0"
    }
  }
}

provider "ovh" {}

variable "cluster" { type = string }
variable "node_names" { type = list(string) }
variable "cloud_project_id" { type = string }
variable "region" { type = string }
variable "flavor_id" { type = string }
variable "image_id" { type = string }
variable "ssh_public_key" { type = string }

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
```

Variable values of valid-create.json: `(cluster prod, an invented 32-hex project id, region GRA11, flavor_id b3-16, example image id)`.

## expect.yaml

`expect.yaml` is the table the `tofu-guard-fixtures` rule of `sc-lint tofu-validate` (part of
`make tofu-validate`) evaluates against `infra/tofu/compute/ovh-public-cloud/guard.yaml` and `guard-destroy.yaml`. It carries the
configuration side of the guard decision (product code, node count per case, region and SSH-key attribute
paths in the guard's dotted syntax; plan 03 section 14.4 rule 6 calls these provider-declared) and the exact
expected verdict per file.
