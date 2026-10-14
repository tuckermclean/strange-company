# 04 — OVH provider

**Status:** Stage-0 plan (draft)
**DAG nodes:** F09 (co-owner with 03, 05), P02 (co-owner with 03, 05)
**Related plans:** [03](03-provider-interface.md), [05](05-existing-nodes.md), [06](06-opentofu-state.md), [07](07-k3s.md), [08](08-node-security.md), [09](09-networking.md), [14](14-object-storage.md), [35](35-secrets.md), [45](45-recovery.md), [47](47-ha-expansion.md), [49](49-destroy.md), [50](50-bootstrap-orchestrator.md)
**Contracts:** conforms to `contracts/provider.schema.yaml` (ProviderOutput, `InfraPriceCatalog`, `TofuPlanGuard`); reads `bootstrap-config.schema.yaml` `spec.provider.ovh`
**ADRs:** relies on ADR-0020, ADR-0100, ADR-0102; introduces ADR-0104 (OVH product line and fallback chain)

## 1. Purpose

The `ovh` provider creates the paid compute for an installation at OVHcloud and reports it as normalized nodes (SPEC §13). It implements plan 03's provider interface for two product lines, selected by a mandatory **spike** (§14.1) with a fixed fallback chain:

- **(a) `vps`** — OVHcloud VPS ordered declaratively through the OpenTofu resource `ovh_vps` (default; cheapest shape meeting the SPEC preferred node).
- **(b) `public-cloud`** — OVHcloud Public Cloud instances (`ovh_cloud_project_instance`) of an equivalent flavor, hourly billed.
- **(c) manual order + `existing`** — the operator orders VPSes in the OVH control panel and installs with `PROVIDER=existing` (plan 05). No OVH API spend authority is given to sc-bootstrap.

DNS is never managed at OVH (Cloudflare, plan 09). Object storage may be OVH Object Storage but is configured independently (plan 14); the compute provider does not create buckets.

## 2. Requirements derived from SPEC.md

1. `ovh` is an initial provider behind the common contract; higher layers never see OVH concepts [SPEC §13].
2. The node meets the SPEC §11 minimum (SPEC v1.2: ≥ 4 vCPU, ≥ 8 GiB, ≥ 100 GiB SSD) and by default the preferred memory (12 GiB, ADR-0020); the default VPS-2's 100 GB disk meets the disk minimum but not the preferred 200 GiB (CD-24, CD-25) [SPEC §11]. A larger machine is allowed; a smaller one never silently [SPEC §11].
3. Before creation: configuration, credentials, product, node count, region, SSH material and expected cost are validated; the footprint is printed; unexpected creation fails closed [SPEC §14].
4. Expansion to three nodes uses the same product without rebuilding [SPEC §12, §16].
5. Destroy removes compute only [SPEC §114].
6. Provider credentials are infrastructure credentials, held outside the cluster [SPEC §99, §111].

Derived requirements:
- **04-R-1** Every OVH resource created by sc-bootstrap is named `sc-<cluster>-node-NN` (display name / instance name) so `OrphanScan` can find it without state.
- **04-R-2** No root password is generated, emailed or stored: `do_not_send_password = true` (VPS) and key-only images (public cloud). Access is by `SC-SSH-OPERATOR` only.
- **04-R-3** The monthly commitment is shown as `committed_minimum` and `reversible_within_hours: false` for `vps`; the footprint says that ordering cannot be undone for the current period.
- **04-R-4** The OVH API credential is a consumer key restricted to the routes in §10; a credential with `GET/POST/PUT/DELETE /*` is refused by PF-07 (`OVH_CREDENTIAL_TOO_BROAD`, warn in `existing`-equivalent read-only CI use, fail for installs). Rationale: least privilege for a spend-authorizing secret.

## 3. Inputs

| Input | Source (`bootstrap.env` → `BootstrapConfig`, plan 50 §9.2) |
|---|---|
| Product line | `OVH_PRODUCT_LINE` (`vps` default, `public-cloud`) → `/provider/ovh/productLine` |
| Region | `PROVIDER_REGION` → `/provider/region` (VPS datacenter code such as `GRA`, `SBG`, `RBX`, `WAW`, `DE`, `UK`, `BHS`; public cloud region such as `GRA11`) (verify current codes in the catalogue) |
| Machine class | `MACHINE_CLASS` → catalogue `productId` (§9.2) |
| API endpoint | `OVH_ENDPOINT` (`ovh-eu`, `ovh-ca`, `ovh-us`) |
| Billing subsidiary | `OVH_SUBSIDIARY` (default from `GET /me` `ovhSubsidiary`) |
| Public cloud project | `OVH_CLOUD_PROJECT_ID` (product line `public-cloud` only) |
| Credentials | `OVH_APPLICATION_KEY`, `OVH_APPLICATION_SECRET`, `OVH_CONSUMER_KEY` (`SC-INFRA-PROVIDER`) |
| SSH public key | `SSH_PUBLIC_KEY_PATH` (`SC-SSH-OPERATOR` public part) |
| OS image | plan 08's supported default: Debian 13 (§9.3) |
| Node count | `EXPECTED_NODE_COUNT` (1 at install, 3 at expand) |

## 4. Outputs

- `ProviderOutput` (plan 03) with `provider.name: ovh`, `product_line: vps|public-cloud`, `capabilities` per §7.1, nodes `node-NN`, and `cost_estimate`.
- OpenTofu state `s3://<prefix>-tofu-state/<cluster>/compute/terraform.tfstate` for root module `infra/tofu/compute/ovh-vps` or `infra/tofu/compute/ovh-public-cloud` (plan 06).
- Spike evidence `evidence/ovh-spike/<date>.json` + summary in `plans/reference/ovh-spike.md` (§14.1).

## 5. Dependencies

- **Hard:** plan 03 interface and guard (F09), plan 06 remote state (P01), plan 50 engine (F08), plan 02 toolbox (OpenTofu + provider plugin).
- **Runtime:** OVH API (`eu.api.ovh.com` for `ovh-eu`), OVH public order catalogue, OVH delivery pipeline, OpenTofu provider `ovh/ovh` (registry `registry.opentofu.org/ovh/ovh`).
- **Consumers:** plan 07/08 (normalized nodes), plan 47 (expand), plan 49 (destroy), plan 45 (recover into the same or another region).
- **Assumed interface (plan 08):** supported OS is Debian 13 (primary) and Ubuntu 24.04 LTS; the initial login user is the image's default cloud user (`debian` / `ubuntu`), which plan 08's baseline replaces with `sc-admin`.

## 6. Owned state

| State | Location | Writers |
|---|---|---|
| OVH compute resources | OVH account; tracked in the compute state (plan 06) | sc-bootstrap via guarded `tofu apply` only |
| OVH price catalogue | `infra/catalog/prices/ovh.yaml` | human PR (protected path) |
| Compute root modules | `infra/tofu/compute/ovh-vps/`, `infra/tofu/compute/ovh-public-cloud/` (+ `guard.yaml`, `guard-destroy.yaml`, `.terraform.lock.hcl`) | PR (protected path) |
| Spike evidence | `evidence/ovh-spike/` | spike operator, PR |

## 7. External interfaces

### 7.1 Capabilities (plan 03 `Capabilities()`)

| Field | `vps` | `public-cloud` |
|---|---|---|
| `provisions_compute` | true | true |
| `paid` | true | true |
| `reversible_within_hours` | **false** (monthly commitment) | true (hourly billing) |
| `supports_expand` | true | true |
| `supports_destroy_compute` | true | true |
| `host_key_source` | `tofu` (trust on first use, recorded and pinned) | `tofu` (console-log fingerprint check if exposed by the API; verify) |

### 7.2 Normalized node mapping

| ProviderOutput field | `vps` source | `public-cloud` source |
|---|---|---|
| `name` | `node-NN` from the resource key | same |
| `provider_id` | VPS service name (e.g. `vps-0a1b2c3d.vps.ovh.net`) | instance UUID |
| `machine_class` | `MACHINE_CLASS` | `MACHINE_CLASS` |
| `addresses.public_ipv4` / `public_ipv6` | `GET /vps/{serviceName}/ips` (verify) | instance `addresses` |
| `addresses.internal_ip` | = `public_ipv4` (no private network on VPS; ADR-0011) | = `public_ipv4` (vRack not used in MVP) |
| `addresses.external_ip` | `null` | `null` |
| `ssh` | `{host: public_ipv4, port: 22, user: debian}` | same |
| `hardware` | SSH discovery (plan 05 §14.2 library); before nodes exist, catalogue specs | same |
| `location.region` / `failure_domain` | datacenter code / same (no zones) | region / same |
| `labels` | `strange.company/install: <cluster>`, `strange.company/provider-product: <planCode>` | same |

Hardware facts **always** come from the shared SSH discovery after delivery (plan 03 §8), so OVH's marketing figures never reach substrate validation unmeasured. Disk class: discovery classifies virtio disks as `unknown`/`hdd` (rotational flag is unreliable in VMs); for `ovh` the class falls back to the catalogue's `storage.class` with `class_source: provider-catalog` (plan 05 §14.3 rules).

## 8. Internal interfaces

- Go package `src/internal/provider/ovh` implementing plan 03's operations. It talks to the OVH API through a small signed-request client (`X-Ovh-Application`, `X-Ovh-Consumer`, `X-Ovh-Timestamp`, `X-Ovh-Signature`; timestamp from `GET /auth/time`) for read-only preflight, orphan scan and inventory, and uses OpenTofu for every mutation.
- OpenTofu root modules (illustrative shape; the guard is the contract):

```hcl
# infra/tofu/compute/ovh-vps/main.tf (sketch)
resource "ovh_vps" "node" {
  for_each       = toset(var.node_names)          # ["node-01"] → later ["node-01","node-02","node-03"]
  ovh_subsidiary = var.ovh_subsidiary
  display_name   = "sc-${var.cluster}-${each.key}"
  plan = [{
    plan_code     = var.plan_code                 # from the catalogue entry of MACHINE_CLASS
    duration      = "P1M"
    pricing_mode  = "default"
    configuration = [
      { label = "vps_datacenter", value = var.datacenter },
      { label = "vps_os",         value = var.os_label },   # e.g. "Debian 13" (verify exact value)
    ]
  }]
  image_id             = var.image_id             # required when public_ssh_key is set (verify)
  public_ssh_key       = var.ssh_public_key
  do_not_send_password = true
  lifecycle { prevent_destroy = false }           # destroy is guarded by guard-destroy.yaml (plan 49)
}
```

```hcl
# infra/tofu/compute/ovh-public-cloud/main.tf (sketch)
resource "ovh_cloud_project_instance" "node" {
  for_each       = toset(var.node_names)
  service_name   = var.cloud_project_id
  region         = var.region
  name           = "sc-${var.cluster}-${each.key}"
  flavor         = { flavor_id = var.flavor_id }   # flavor of MACHINE_CLASS (verify attribute shape)
  boot_from      = { image_id = var.image_id }
  ssh_key_create = { name = "sc-${var.cluster}", public_key = var.ssh_public_key }   # (verify)
  billing_period = "hourly"
  network        = { public = true }
}
```

Resource names and argument shapes above are taken from the provider documentation at Stage 0 and **must be re-verified** in spike S-1 against the pinned provider version.

- Guard files (`TofuPlanGuard`, plan 03 §14.4):

| Module | `resources[].type` | `paid` | `allowedActions` | `maxCount` | `priceProductFrom` |
|---|---|---|---|---|---|
| `ovh-vps` | `ovh_vps` | true | `[no-op, read, create, update]` | `expectedNodeCount` | `plan[0].plan_code` |
| `ovh-public-cloud` | `ovh_cloud_project_instance` | true | `[no-op, read, create, update]` | `expectedNodeCount` | `flavor.flavor_id` (verify path) |

  `allowedProviders: [registry.opentofu.org/ovh/ovh]`, `forbidReplace: true`. Destroy guards allow only `delete` on the same single type. Region rule paths: `plan[0].configuration[?label=="vps_datacenter"].value` and `region`; SSH rule paths: `public_ssh_key` and `ssh_key_create.public_key` (provider-declared, plan 03 §14.4 rule 6). `update` is allowed because OVH may report drift on display attributes; an `update` that changes `plan_code`, datacenter or SSH key fails rules 5–6.

## 9. Configuration

### 9.1 Defaults and knobs

| Knob | Default | Source | Who may change |
|---|---|---|---|
| Product line | `vps` (unless spike outcome is (b)) | `OVH_PRODUCT_LINE` | operator |
| Enabled product lines | set by the spike PR | `infra/bootstrap/settings.yaml` `providers.ovh.enabledProductLines` | PR (protected path) |
| Machine class | `ovh-vps-6c-12g-100nvme` (VPS-2 class of the 2025 range; verify) | `MACHINE_CLASS` | operator |
| Region | none (required) | `PROVIDER_REGION` | operator |
| Commitment | `P1M` | module constant (catalogue `commitment` must equal it) | PR |
| OS | Debian 13 | plan 08 constant mapped to `vps_os` label and `image_id` lookup | PR |
| Delivery wait | 45 min (inside the 60 min provision timeout) | `infra/bootstrap/settings.yaml` `ovh.deliveryTimeout` | PR |
| Provider plugin | latest `ovh/ovh` release whose `ovh_vps` supports ordering with `plan`, `public_ssh_key`, `image_id`, `do_not_send_password` (verify) | `.terraform.lock.hcl` | PR |

**Region guidance.** Choose the compute region close to the operator; choose object storage in a **different** region or provider (SPEC §29: the cluster must not be the sole failure domain for backups). Preflight emits warn `PF-10 S3_SAME_REGION_AS_COMPUTE` when the S3 endpoint host contains the compute region code (heuristic; plan 14 may refine).

### 9.2 Catalogue entries (`infra/catalog/prices/ovh.yaml`)

| `productId` | Line | Specs (as marketed) | Use | `planCode` / flavor | Commitment |
|---|---|---|---|---|---|
| `ovh-vps-6c-12g-100nvme` | vps | 6 vCore / 12 GB / 100 GB NVMe | **default** (preferred shape, ADR-0020) | VPS-2 (2025 range) plan code (verify, S-1) | P1M |
| `ovh-vps-4c-8g-75ssd` | vps | 4 vCore / 8 GB / 75 GB | **not usable**: the 75 GB disk is below the SPEC v1.2 minimum of 100 GiB (CD-25); kept only so preflight can name the reason (`NODE_BELOW_MINIMUM` before ordering). An 8 GiB `constrained` node (CD-23) is possible with other providers or `existing` machines that have ≥ 100 GiB disk | VPS-1 plan code (verify) | P1M |
| `ovh-vps-8c-24g-200nvme` | vps | 8 vCore / 24 GB / 200 GB | larger option | VPS-3 plan code (verify) | P1M |
| `ovh-pci-4c-16g-100nvme` | public-cloud | 4 vCPU / 16 GB / 100 GB NVMe | fallback (b) | general-purpose flavor ≥ preferred shape, e.g. `b3-16` (verify) | PT1H |

Prices are entered only from OVH's public catalogue (`GET /order/catalog/public/vps?ovhSubsidiary=<SUB>` and the Public Cloud price list; verify endpoints) on the day of entry, with `verifiedAt` and `sourceUrl`. Stage 0 records no price as fact; third-party listings in October 2026 suggest VPS-2 ≈ 6–8 EUR/month excl. VAT, to be confirmed at S-1. A suggested `MAX_MONTHLY_COST` for one default node is 25 EUR; for small-ha 3 nodes, 60 EUR.

### 9.2.1 Default class evaluation: ≥ 16 GiB (SI-01 extension, plan 51 §12)

Plan 51's Stage-0 budget shows the full Self-Hosting Threshold platform fits on a 12 GiB node **only at the margin**: V-30 needs platform working set ≤ 6 040 MiB against an estimate of ≈ 6 040 MiB, with workers capped at 3 GiB and worker/CI mutually exclusive (plan 51 §12.1, §14.4). Plan 51 §14.4 step (3) already names "the next provider machine class (≥ 16 GiB)" as the response to a 12 GiB V-30 failure.

The OVH 2025 VPS range has no 16 GiB class (verify at S-6): after VPS-2 (6 vCore / 12 GB / 100 GB NVMe) the next class is VPS-3 (8 vCore / 24 GB / 200 GB NVMe, catalogue `ovh-vps-8c-24g-200nvme`). The Public Cloud fallback's `b3-16`-class flavor (16 GB) is several times more expensive per month (verify).

| Option | Monthly cost, one node (excl. VAT; Oct 2026 third-party listings, verify at S-6) | small-ha (3 nodes) | V-30 outlook | Workers |
|---|---|---|---|---|
| VPS-2 12 GB (current default) | ≈ 6–8 EUR | ≈ 18–24 EUR | passes only at the margin; a fail triggers a node replacement after the monthly commitment is already spent | 3 GiB, exclusive with CI |
| VPS-3 24 GB | ≈ 13–16 EUR (≈ +7–8 EUR) | ≈ 39–48 EUR (≈ +21–24 EUR) | ≈ 10 GiB headroom after the platform | 3 GiB or the SPEC §83 4 GiB example; MAY run shared mode (plan 51 §8) after its own V-30 pass |

**Decision (operator, CD-24, 2026-10-14):** the default stays `ovh-vps-6c-12g-100nvme` (VPS-2). It meets the SPEC v1.2 disk minimum (100 GB ≈ 93.1 GiB ≥ 0.92 × 100 GiB) and the preferred memory, but not the preferred 200 GiB disk, so substrate validation reports `meets_preferred: false` with a warning. `ovh-vps-8c-24g-200nvme` (VPS-3) is the documented **upgrade path** when V-30 (memory) or the disk-budget gate (plans 11 §12, 51) fails at P12, or when growth demands it; it is reached by node replacement (plan 47), never in place. Suggested `MAX_MONTHLY_COST`: 25 EUR for one VPS-2, 30 EUR if starting directly on VPS-3.

### 9.3 OS image mapping

`vps`: `vps_os` configuration label = the catalogue value for Debian 13 (verify exact string), plus `image_id` resolved at plan time by `Preflight` (`GET /vps/images…` or the catalogue; verify which endpoint lists images *before* a service exists — spike S-2). `public-cloud`: `GET /cloud/project/{id}/image?osType=linux&region=<r>` → newest image named `Debian 13` (verify). The resolved image id is an input to the plan (and therefore to the plan digest); a changed image id on an existing node is an `update`/replace the guard refuses.

## 10. Secrets

| Class | Use | Storage |
|---|---|---|
| `SC-INFRA-PROVIDER` (OVH application key/secret + consumer key) | preflight reads, OpenTofu provider env (`OVH_ENDPOINT`, `OVH_APPLICATION_KEY`, `OVH_APPLICATION_SECRET`, `OVH_CONSUMER_KEY`) | `bootstrap.env`, recovery bundle, `secrets/offcluster/<cluster>/infra-provider.sops.yaml` (plan 35); never in cluster, state or logs |
| `SC-SSH-OPERATOR` | public key injected at order time | operator machine |
| `SC-CI-OVH-READONLY` (introduced here; registered by plan 35) | plan-only CI (`make tofu-fixtures`, nightly provider-contract job) and the S-1 spike's read-only checks | as registered by plan 35: SOPS `gitops/clusters/<cluster>/secrets/ci-build/sc-ci-ovh-readonly.sops.yaml` → Secret `ci-build/sc-ci-ovh-readonly`, mounted only into the allowlisted provider-contract pipeline; before P08, a GitHub Actions secret on protected refs |

**Consumer key access rules** (requested with `POST /auth/credential`; the operator creates it once following `procedures/fresh-install.md` step "OVH credential"). Paths are the documented order and VPS routes (verify exact list in S-1):

```text
GET    /auth/time  /me  /me/*
GET    /order/catalog/*          POST /order/cart  POST /order/cart/*  GET /order/cart/*  DELETE /order/cart/*
GET    /me/order  /me/order/*    POST /me/order/*/payWithRegisteredPaymentMean
GET/PUT/POST/DELETE  /vps/*      POST /vps/*/terminate  POST /vps/*/confirmTermination
GET/POST/PUT/DELETE  /cloud/project/<OVH_CLOUD_PROJECT_ID>/*        (public-cloud only)
```

`SC-CI-OVH-READONLY` is limited to `GET /auth/time`, `GET /me`, `GET /order/catalog/*`, `GET /vps`, `GET /vps/*`. `SC-CI-OVH-READONLY` is registered in `contracts/secret-classes.yaml` (rotation yearly, recovery by re-issue in the OVH console).

## 11. Storage

None beyond the compute state (plan 06). The VPS root disk holds k3s, etcd, CNPG PGDATA and Longhorn replicas (plans 07, 11).

## 12. Resource requirements

Default node: 6 vCore / 12 GB / 100 GB NVMe (VPS-2 class). Small-ha: three identical nodes (`EXPECTED_NODE_COUNT=3`, same `MACHINE_CLASS`; plan 03 requires equal shapes, plan 07 Q-07-04).

## 13. Security boundary

- The OVH credential authorizes spending; it exists only on the operator machine, in the bundle and in the OpenTofu subprocess environment (plan 03 §13). The cluster never holds it.
- Ordering is reachable only through a guarded, digest-confirmed plan (ADR-0102). The orphan scan prevents duplicate orders after a crash.
- Nodes are reachable on SSH only (plan 08 firewall). OVH's network-level firewall is not used (it would be a second, out-of-Git policy); OVH anti-DDoS is the provider default.
- Host keys: trust on first use at delivery (recorded in the report and pinned in `.bootstrap/known_hosts` and `inventory.yaml`). A later key mismatch is fatal (`HOST_KEY_MISMATCH`, plan 50).
- Console/rescue access (KVM console, rescue boot) through the OVH control panel is the infrastructure break-glass path (`SC-BREAK-GLASS-INFRA`, plan 15/35).

## 14. Happy path

### 14.1 Spike S (mandatory before GATE-P02; performed once per provider plugin major)

Performed by a maintainer with a real OVH account, `make tofu-fixtures` plus a scripted run of `./bootstrap` with `PROVIDER=ovh` against a scratch cluster name, ending with destroy. Each sub-question has a pass criterion and is recorded in `evidence/ovh-spike/<date>.json` (fields: `id`, `question`, `result: pass|fail`, `evidence`, `provider_version`, `tofu_version`).

| ID | Question | Pass criterion |
|---|---|---|
| S-1 | Can `ovh_vps` order a VPS-2-class VPS declaratively, and does `tofu show -json` expose `plan[0].plan_code` before apply? | plan JSON contains the planned plan code and datacenter; apply delivers a running VPS; state records the service name |
| S-2 | Does SSH key injection at order time work with Debian 13 (`public_ssh_key` + `image_id`), and is no password emailed (`do_not_send_password = true`)? | key login as `debian` works within 15 min of delivery; no password e-mail; password login disabled (`sshd -T` shows `passwordauthentication no` after plan 08 baseline) |
| S-3 | What does destroy do: synchronous termination, or e-mailed confirmation token? When does billing stop? | documented behavior; if a token is required, plan 49's `blocked` path (exit 75) is confirmed workable |
| S-4 | Delivery time distribution (3 orders) and behavior when `tofu apply` is interrupted mid-delivery, including the sub-case "interrupted after the order, before the display name is set" | p95 delivery ≤ 45 min; after interruption, the VPS (or pending order) is visible to `OrphanScan` (§14.4), also when unnamed |
| S-5 | Can `GET /vps` + display name identify our VPS, and `GET /me/order` identify pending orders? | orphan scan finds the scratch VPS and a deliberately pending order |
| S-6 | Live catalogue endpoint and fields for price, plan code, datacenters, OS labels | `Preflight` parses them; recorded fixture committed under `test/fixtures/ovh/catalog/` |
| S-7 | Import: `./bootstrap state import --module compute` (plan 06 §20.4) recreates state for an existing VPS (`import` block by service name) | plan after import shows no changes |

**Fallback chain (ADR-0104):**
- All of S-1..S-5 pass → product line **(a) `vps`** is the default.
- S-1, S-2 or S-5 fails (cannot order, cannot inject keys safely, or cannot detect orphans) → repeat S-1..S-7 for **(b) `public-cloud`** with `ovh_cloud_project_instance`; if it passes, `OVH_PRODUCT_LINE` defaults to `public-cloud` and the catalogue's preferred machine class becomes `ovh-pci-4c-16g-100nvme`. Cost note: hourly Public Cloud instances cost several times a VPS per month (verify), so the footprint makes the difference explicit.
- (b) also fails → **(c)**: `ovh` provider stays plan-only; the supported OVH path is "order manually in the OVH control panel, then `PROVIDER=existing`" (plan 05), documented in `procedures/fresh-install.md` §"Manual OVH order". `PROVIDER=ovh` then fails PF-07 with `OVH_PRODUCT_LINE_DISABLED`. The enabled product lines are the list `providers.ovh.enabledProductLines` in `infra/bootstrap/settings.yaml` (plan 50 §9.3, `BootstrapSettings`), which the spike PR sets (`[vps]`, `[public-cloud]` or `[]`).
- S-3 failing (termination needs a manual step) does **not** disqualify (a); destroy goes `blocked` and the procedure handles it (plan 49).

### 14.2 Preflight (plan 03 `Preflight`; PF-07, PF-08, PF-10, PF-11)

1. `GET /auth/time` → clock skew ≤ 30 s (signature validity).
2. `GET /me` → account active; `ovhSubsidiary` (default for `OVH_SUBSIDIARY`); `currency.code` must equal `COST_CURRENCY` (else `PRICE_CURRENCY_MISMATCH`, exit 4).
3. Default payment method exists (`GET /me/payment/method?default=true` (verify)) → else `OVH_NO_PAYMENT_METHOD` (exit 3). OVH does not support `BANK_ACCOUNT` for orders (provider docs).
4. Credential scope check: `GET /auth/currentCredential` (verify) → rules ⊆ §10 list (04-R-4).
5. Live catalogue: plan code of `MACHINE_CLASS` exists, is orderable in `PROVIDER_REGION`, and the Debian 13 OS label is available → else `PF-08 OVH_PRODUCT_UNAVAILABLE` listing alternative regions from the catalogue.
6. Live price: monthly price for the plan code and subsidiary; plan 03 §14.5 `max(catalogue, live)` rule.
7. Quota (public cloud only): `GET /cloud/project/{id}/quota` (verify) has room for `EXPECTED_NODE_COUNT` instances of the flavor.
8. `OrphanScan` (read-only) → if non-empty at *preflight* of a fresh install, `ORPHAN_PAID_RESOURCE` (exit 4) before anything else is done.

### 14.3 Plan, confirm, apply

1. `Plan`: render `terraform.tfvars.json` in the work dir (cluster, node names `node-01..node-<count>`, subsidiary, plan code, datacenter, OS label, image id, SSH public key), `tofu plan -out`. Guard and cost (plan 03 §14.4–14.6).
2. Footprint (plan 03 §14.7) includes for `vps`: "Ordering is NOT instantly reversible: a VPS is billed per month; destroying it does not refund the current period" and `committed_minimum = count × monthly`.
3. `Apply` after the typed confirmation. The `ovh_vps` resource places the order, pays with the default payment method and waits for delivery (provider behavior; verify the provider's create timeout and set `timeouts { create = "45m" }` if supported).

### 14.4 Orphan scan (OVH specifics)

`OrphanScan` returns both:
- **delivered orphans:** `GET /vps` → for each service, `GET /vps/{sn}` `displayName` matching `^sc-<cluster>-node-[0-9]{2}$` and not present in state;
- **pending orders:** `GET /me/order?date.from=<install created_at>` → orders whose details (`GET /me/order/{id}/details`) contain a VPS plan code from the catalogue and whose status is not `delivered`/`cancelled` (verify field names). A pending order blocks apply with `ORPHAN_PENDING_ORDER` (exit 4) and the hint "wait for delivery, then `./bootstrap state import`" (bootstrap-resume.md#provision).
- **delivered unnamed orders (BOOT-28):** orders since the installation's `created_at` with a catalogue VPS plan code and status `delivered`, whose service (`GET /me/order/{id}/details` → domain/service name (verify)) is **not** in state, regardless of display name. This covers an apply interrupted after the order but before the display name was set; the result is `ORPHAN_PAID_RESOURCE` and the service is imported by provider id.

Public cloud: `GET /cloud/project/{id}/instance` filtered by name.

### 14.5 Commitment, billing and termination

- VPS: monthly billing from the order date; termination (destroy) takes effect according to OVH's termination workflow; the current month is not refunded (verify in S-3). The footprint, the plan 49 destroy footprint and the report all repeat this.
- Public cloud: hourly billing until the instance is deleted; `committed_minimum` = one hour per instance.
- **Cost guard summary:** MAX_MONTHLY_COST bound (plan 03 §14.6), committed minimum printed before confirmation, orphan scan against duplicate orders, guard `forbidReplace` (a replace would order a new month while the old one is still billed), and no automated re-ordering on failure (plan 03 §16).

### 14.6 Inventory

After apply: per node, read IPs (`GET /vps/{sn}/ips`), wait for SSH on `public_ipv4:22` (≤ 15 min, plan 03 §14.8), capture host keys (`ssh-keyscan -t ed25519,ecdsa,rsa`), then run the shared SSH discovery (plan 05 §14.2) as user `debian` with the operator key, and assemble `ProviderOutput`.

### 14.7 Expand and recover

- Expand (plan 47): `EXPECTED_NODE_COUNT=3`; the plan adds `ovh_vps.node["node-02"]`, `["node-03"]`; the guard counts prior state; the footprint shows two new monthly commitments.
- Replacing a node: new name `node-04` (plan 03 §8; never reused).
- Recover after a region outage (plan 45): recover with a different `PROVIDER_REGION` (immutable fields are cluster/domain/S3/provider name, not region; plan 50 §9.2). The old VPS, if it reappears, is found by `OrphanScan` and must be destroyed through the procedure `provider-outage.md`.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Credential invalid / too broad / clock skew | §14.2 steps 1, 4 | nothing ordered | exit 3 | bootstrap-preflight-failure.md#credentials |
| No default payment method | §14.2 step 3 | nothing ordered | exit 3 | bootstrap-preflight-failure.md#credentials |
| Product sold out in region | live catalogue | nothing ordered | exit 3 + alternatives | bootstrap-preflight-failure.md#product |
| Live price higher than catalogue | §14.2 step 6 | estimate raised | may exceed limit → exit 4 | bootstrap-preflight-failure.md#guardrail |
| Order accepted, delivery slow, apply times out | tofu error; OrphanScan pending order | phase failed; VPS later delivered | next run: `ORPHAN_PENDING_ORDER`/`ORPHAN_PAID_RESOURCE`, no re-order | bootstrap-resume.md#provision |
| Order rejected (payment refused, fraud check) | apply error | nothing delivered | phase failed (permanent) | bootstrap-preflight-failure.md#credentials |
| Delivered VPS without our SSH key / password mailed | SSH auth failure after delivery | node unusable | phase failed `SSH_KEY_NOT_INSTALLED` | provider-outage.md#unusable-node (reinstall via `./bootstrap state …` not automated: operator reinstalls in console with the key, or destroys) |
| Delivered machine smaller than catalogue | substrate validation (plan 03 §14.3) | node unusable | `NODE_BELOW_MINIMUM`; spend committed | bootstrap-preflight-failure.md#node-size |
| OVH API outage | 5xx / timeouts | no provision/expand/destroy | transient retries then fail | provider-outage.md |
| OVH datacenter incident | `SCNodeDown`, SSH timeouts | cluster down | none automatic | provider-outage.md |
| Termination requires e-mailed token | destroy response (S-3) | destroy `blocked` | exit 75 with instructions | safe-destruction.md#ovh-termination |
| Provider plugin changes `ovh_vps` schema | guard fixture tests / plan JSON format | plan refused | `make tofu-validate` fails in the upgrade PR | — |

## 16. Retry behavior

Read-only OVH API calls: plan 50 transient classification (HTTP 429/5xx, timeouts), backoff 5–60 s, ≤ 5 attempts. `tofu apply` (orders) is never retried within a run (plan 03 §16). Order-related 4xx are permanent.

## 17. Idempotency behavior

- Re-planning after a successful apply yields zero changes (provision `Check()`, plan 03 §17).
- A crashed apply cannot cause a second order: state records each delivered VPS as OpenTofu completes it; anything delivered outside state is caught by the orphan scan, and pending orders block apply.
- Inventory is deterministic for a given state and SSH facts.

## 18. Bootstrap behavior

Runs in `preflight`, `plan.compute-plan`, `plan.confirm`, `provision` (plan 50 §14.3–14.4). Nothing OVH-related runs in-cluster.

## 19. Upgrade behavior

- Plugin upgrades: `.terraform.lock.hcl` PR; `make tofu-validate` re-runs guard fixtures; if `ovh_vps` attributes changed, S-1/S-2 are repeated before merging (evidence file updated).
- OVH range changes (new VPS generation, renamed plan codes): add catalogue entries; existing installs keep their class. Moving an existing node to a new class is a node replacement (add `node-04`, remove `node-01`; plan 47), never an in-place plan change (`forbidReplace`).

## 20. Recovery behavior

- **Lost compute state:** `./bootstrap state import --module compute` with `import` blocks keyed by service name from `OrphanScan` (plan 06 §20.4; S-7 verifies).
- **Region down:** recover into another region (plan 45; `provider-outage.md`).
- **Account locked or credential lost:** `SC-BREAK-GLASS-INFRA` (OVH console account held by the operator) to create a new consumer key; `./bootstrap rotate refresh-bundle`.

## 21. Observability

- Preflight findings, plan summary, footprint and audit in the run directory (plan 03 §21); the report includes product line, plan code, monthly total and committed minimum.
- In-cluster node health is plans 38/42 (`SCNodeDown`). OVH invoices are not monitored (plan 03 non-goal).

## 22. Testing strategy

- **Unit:** OVH request signing (known-answer vectors from OVH documentation), catalogue parser against the recorded fixture (S-6), orphan scan (fixtures: delivered orphan, pending order, foreign VPS with a non-matching name, none), credential-scope evaluation, normalization mapping (fixture `GET /vps/{sn}` + `ips` responses → ProviderOutput validated against `provider.schema.yaml`).
- **Guard fixtures** (`test/fixtures/tofu-plans/ovh-vps/`, `…/ovh-public-cloud/`): valid create ×1, ×3 expand; wrong plan code; wrong datacenter; wrong SSH key; replace; extra resource type (`ovh_domain_zone_record`); destroy-mode with a create. Recorded once from real plans via `make tofu-fixtures` (tier O, `SC-CI-OVH-READONLY`), committed, reviewed.
- **Plan-only CI (tier O, nightly on protected refs):** `tofu plan` against the real provider with the read-only credential and a scratch cluster name; asserts guard verdict `pass` and digest stability; never applies.
- **Real (P02 / P12):** spike S; the fresh-VPS E2E (plan 50 `e2e-fresh-vps`) ends with destroy (plan 49).

## 23. Acceptance criteria

### GATE-F09 (co-owned with plans 03, 05)

- **04-AC-1** `infra/catalog/prices/ovh.yaml` validates against `provider.schema.yaml#/$defs/InfraPriceCatalog` and contains the four entries of §9.2 with `verifiedAt` ≤ 90 days old at merge time (`make locks-check`, `make contracts`).
- **04-AC-2** Guard fixtures for both modules pass/fail with the codes in plan 03 §14.4 (`GUARD_PRODUCT_MISMATCH` for a wrong plan code, `GUARD_REGION_MISMATCH`, `GUARD_SSH_KEY_MISMATCH`, `GUARD_REPLACE_FORBIDDEN`, `GUARD_UNEXPECTED_RESOURCE`, `GUARD_ACTION_FORBIDDEN` in destroy mode) — table test exit 0 in `make tofu-validate`.
- **04-AC-3** Normalization: the recorded OVH API fixtures produce a `ProviderOutput` that validates against `provider.schema.yaml` with `internal_ip == public_ipv4`, `capabilities.reversible_within_hours == false` for `vps`, and no field containing an OVH-specific key outside `provider_id`, `machine_class`, `region.provider_region`, `labels` (`jq` path scan).
- **04-AC-4** Orphan-scan unit suite: a delivered orphan yields `ORPHAN_PAID_RESOURCE`, a pending order yields `ORPHAN_PENDING_ORDER`, a foreign VPS (`displayName` not matching) yields nothing.
- **04-AC-5** Credential scope: a fixture credential with rule `{method: POST, path: /*}` fails PF-07 with `OVH_CREDENTIAL_TOO_BROAD`; the §10 rule set passes.
- **04-AC-6** `make tofu-validate`: every variable in `infra/tofu/compute/ovh-*/` whose name matches `(?i)(secret|password|token|key)` is `sensitive` and `ephemeral` (or absent because credentials come only from provider env vars), and `do_not_send_password = true` is present for `ovh_vps` (static HCL check).

### GATE-P02 (co-owned with plans 03, 05; applies when the reference install uses `PROVIDER=ovh`)

- **04-AC-20** Spike evidence `evidence/ovh-spike/<date>.json` exists with S-1..S-7 results for the pinned provider version, validated by a schema check in `make locks-check`, and the selected product line equals `OVH_PRODUCT_LINE`'s default in `bootstrap.env.example`.
- **04-AC-21** A real install provisions one node of `MACHINE_CLASS=ovh-vps-6c-12g-100nvme` (or the spike-selected class): `provider-output.json` validates, `hardware.cpu_count ≥ 4`, `memory_bytes ≥ 0.92 × 12 GiB`, root disk ≥ 0.92 × 100 GiB with `meets_minimum: true` and `meets_preferred: false` plus finding `BELOW_PREFERRED_DISK` (warn) (CD-24/25; `meets_preferred: true` for VPS-3).
- **04-AC-22** SSH: `ssh -o PasswordAuthentication=no debian@<ip> true` succeeds with the operator key; the run log contains no OVH password e-mail notice and the OVH account's e-mail history (manual check recorded in the evidence) shows no password e-mail for the service.
- **04-AC-23** No DNS change at OVH: `GET /domain/zone` (if the account has zones) is byte-identical before and after install (`jq -S` digest), and the guard's allowed types contain no `ovh_domain_*`.
- **04-AC-24** Re-running provision is a no-op (plan 03 03-AC-22) and the OVH API shows exactly `EXPECTED_NODE_COUNT` services named `sc-<cluster>-node-*`.
- **04-AC-25** The fresh-VPS E2E destroy (plan 49 49-AC-20) leaves no `sc-<cluster>-node-*` service in `GET /vps` after termination completes (or a documented `blocked` → completed sequence per S-3), and no pending order in `GET /me/order` for the scratch cluster.

## 24. Explicit non-goals

- OVH Dedicated servers, Kimsufi/SYS, Managed Kubernetes.
- OVH vRack / private networking in MVP (WireGuard over public IPs, ADR-0011).
- OVH DNS, OVH load balancers, OVH network firewall.
- Creating OVH Object Storage buckets or users through the compute provider (plan 14).
- Monitoring OVH invoices or automating refunds.

## 25. Open questions

| # | Question | Blocking | Proposed resolution | Owner |
|---|---|---|---|---|
| OQ-04-1 | Can the OVH OpenTofu provider order a VPS declaratively with key injection and no password e-mail? Provider documentation (Oct 2026) describes `ovh_vps` as ordering through OVH's order workflow and running the termination workflow on destroy, with `public_ssh_key` requiring `image_id`; unverified in practice. | **yes for P02 with `PROVIDER=ovh`** (not for F09, not for installs with `existing`) | Spike S decides; fallback chain (b) → (c) per ADR-0104. P02 can pass with `existing` (plan 05) while the spike is open. | 04 |
| OQ-04-2 | Exact current VPS-2 plan code, datacenter codes and OS label strings. | no | Taken from the live catalogue during S-6; fixtures committed. | 04 |
| OQ-04-3 | Does destroying `ovh_vps` terminate immediately or at the end of the billing period, and does it need an e-mailed token? (also plan 49 OQ-49-1) | no | S-3; plan 49 `blocked` path covers the token case. | 04 / 49 |
| OQ-04-4 | Should object storage default to OVH Object Storage in a different region, or another provider? | no | Plan 14 decides; this plan only warns on same-region (§9.1). | 14 |
| OQ-04-5 | (Resolved by the operator, CD-24) Default class stays VPS-2; VPS-3 is the documented upgrade path (§9.2.1). | no | — | — |
