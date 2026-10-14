# 03 — Provider interface, substrate and cluster profiles, paid-resource guardrails

**Status:** Stage-0 plan (draft)
**DAG nodes:** F09 (co-owner with 04, 05), P02 (co-owner with 04, 05)
**Related plans:** [04](04-ovh-provider.md), [05](05-existing-nodes.md), [06](06-opentofu-state.md), [07](07-k3s.md), [08](08-node-security.md), [47](47-ha-expansion.md), [49](49-destroy.md), [50](50-bootstrap-orchestrator.md), [51](51-capacity-and-scheduling.md)
**Contracts:** owns `contracts/provider.schema.yaml` (including `$defs/InfraPriceCatalog` and `$defs/TofuPlanGuard`), `contracts/substrate.schema.yaml`, `contracts/cluster-profile.schema.yaml`
**ADRs:** relies on ADR-0002, ADR-0020; introduces ADR-0102 (paid-resource guard by plan-JSON allowlist and version-controlled price catalogue)
**Procedures:** owns `procedures/provider-outage.md`

## 1. Purpose

This plan defines the contract that every infrastructure provider implements (SPEC §13), so that higher layers depend only on normalized node information and never on OVH-specific concepts. It also defines how substrate manifests (SPEC §11) and cluster profiles (SPEC §12) are loaded, merged and validated against real machines, and it defines the **paid-resource guardrails** (SPEC §14) that stand between a configuration file and a credit-card charge.

## 2. Requirements derived from SPEC.md

1. Providers sit behind a common contract. The initial providers are `ovh` and `existing` [SPEC §13].
2. All providers expose normalized node information. Higher layers do not depend on OVH-specific concepts [SPEC §13].
3. Future providers (Hetzner, Proxmox, VMware, other VPS providers, bare metal) must fit without changing higher layers [SPEC §13].
4. Two canonical profiles exist: `self-hosting-minimum` and `small-ha` [SPEC §10, §11, §12].
5. Providers MAY choose a larger machine, and SHOULD NOT silently choose a smaller one [SPEC §11].
6. Expansion does not rebuild from scratch [SPEC §12].
7. Before creating paid infrastructure, bootstrap validates configuration, credentials, requested products, node count, location or region, SSH material and expected cost [SPEC §14].
8. Configuration supports expected node count, expected machine class and maximum expected monthly cost [SPEC §14].
9. Unexpected paid-resource creation fails closed [SPEC §14].
10. The intended paid footprint is printed before first creation [SPEC §14].

Derived requirements:
- **03-R-1** No paid resource is created unless the saved OpenTofu plan has passed the allowlist guard, the price check and a typed confirmation of that plan's digest.
- **03-R-2** A guard decision is computed from `tofu show -json <planfile>` and nothing else. Provider code cannot bypass the guard, because only the guarded plan file is applied.
- **03-R-3** A price that is unknown, stale or in an unexpected currency fails closed. There is no FX conversion.
- **03-R-4** Provider-native identifiers stay inside `provider_id`, `machine_class`, `region.provider_region` and `tofu.*`. Nothing downstream branches on them.
- **03-R-5** Measured node capacity is compared with a declared, uniform tolerance (§14.3), so that "8 GB" marketing does not cause false rejections and real undersizing is still caught.

## 3. Inputs

- `BootstrapConfig.spec.provider` (plan 50 §9.2): `name`, `region`, `expectedNodeCount`, `machineClass`, `maxMonthlyCost`, and the provider-specific blocks.
- The profile file `infra/profiles/<name>.yaml` (BootstrapSubstrate or ClusterProfile).
- The price catalogue `infra/catalog/prices/<provider>.yaml` (`InfraPriceCatalog`).
- Guard files `infra/tofu/compute/<module>/guard.yaml`, `infra/tofu/compute/<module>/guard-destroy.yaml`, `infra/tofu/storage/guard.yaml` (`TofuPlanGuard`).
- SSH public key (`SC-SSH-OPERATOR` public part).
- Provider credentials (`SC-INFRA-PROVIDER`) as environment variables in the provider subprocess only.

## 4. Outputs

- `ProviderOutput` documents (`contracts/provider.schema.yaml`), written by sc-bootstrap to `.bootstrap/runs/<run-id>/provider-output.json` and to S3 `…/tofu-state/<cluster>/provider/output.json`.
- `plan-summary.json` (per run): guard verdict, resource changes, cost estimate, plan digest.
- The non-secret projection `gitops/clusters/<cluster>/inventory.yaml` (`kind: NodeInventory`, carrying `nodes[]` exactly as in `ProviderOutput.nodes` minus `substrate_validation`), committed by plan 50. `ssh.admin_user` is `null` at provision and set to `sc-admin` by configure's admin-transition commit; every SSH client uses `admin_user ?? user` (BOOT-8).

## 5. Dependencies

- **Hard:** F08 (orchestrator core: phases, journal, config), F02 (contract validation), F03 (toolbox: OpenTofu, ssh). P02 also needs P01 (remote state, plan 06).
- **Consumers:** plan 50 (phases), 07/08 (inventory for Ansible), 47 (expand), 49 (destroy-compute), 45 (recover), 51 (node shape for capacity profiles).

## 6. Owned state

| State | Where | Writers |
|---|---|---|
| Provider-managed compute | OpenTofu state `s3://<prefix>-tofu-state/<cluster>/compute/terraform.tfstate` (plan 06) | sc-bootstrap via `tofu apply <guarded planfile>` |
| Last provider output | S3 `…/provider/output.json` (versioned) + `inventory.yaml` in Git | sc-bootstrap |
| Price catalogue | Git `infra/catalog/prices/` | human PR only (protected path, plan 01 §13.2) |
| Guard allowlists | Git `infra/tofu/**/guard*.yaml` | human PR only (protected path) |
| Profiles | Git `infra/profiles/` | human PR only |

## 7. External interfaces

- **`ProviderOutput`** (`contracts/provider.schema.yaml`), consumed by configure (07/08), verify (50), expand (47), destroy (49) and recover (45).
- **Profiles** (`substrate.schema.yaml`, `cluster-profile.schema.yaml`), consumed by plans 07, 12, 13 and 51 through `cluster-vars` (plan 18), which receive the *effective profile* values: `servers`, `cnpg.instances`, `longhorn.replicas`, `maximumConcurrentAgents`, `maximumConcurrentCIJobs`.
- **Footprint and confirmation UX** (§14.7), seen by operators.

## 8. Internal interfaces

The provider interface is a Go interface in `src/internal/provider` with one implementation package per provider (`ovh`, `existing`, `fake`). Every operation is context-bound, honors the phase timeout, and returns typed errors with a `Retryable()` classification.

| Operation | Behavior | Mutates? |
|---|---|---|
| `Capabilities()` | Static `capabilities` object (provider.schema.yaml) | no |
| `Validate(cfg, profile)` | Offline checks: required config present, region in the provider allowlist, `machineClass` in the catalogue, catalogue specs ≥ profile minimum (with §14.3 tolerance), node count = `expectedNodeCount` | no |
| `Preflight(ctx, cfg)` | Online read-only checks: credentials work, account can order (provider-specific), product offered in region (live catalogue), quota sufficient, live price (for §14.5). Returns PF findings (PF-07, PF-08, PF-10, PF-11) | no |
| `Plan(ctx, desired)` | Produces a `ProviderPlan`: for OpenTofu providers, `tofu plan -out=<work>/compute.tfplan` + `tofu show -json` + resource list; for `existing`, SSH discovery and a no-op plan. Includes the cost estimate | no (reads state) |
| `Apply(ctx, plan)` | Applies **exactly** the confirmed planfile (`tofu apply <planfile>`). For `existing`: no-op | **yes** |
| `Inventory(ctx)` | Builds `ProviderOutput` from OpenTofu outputs plus live API refresh plus SSH discovery (hardware facts are always from SSH discovery after nodes exist, and from the catalogue before) | no |
| `OrphanScan(ctx)` | Lists provider resources that carry this install's naming or tags but are absent from state. Returns a list; non-empty blocks apply (§14.8) | no |
| `Expand(ctx, targetCount)` | `Plan` with the node count raised; same guard; may only add | no |
| `DestroyCompute(ctx)` | Produces a destroy-mode plan (`tofu plan -destroy` on the compute root module only) guarded by `guard-destroy.yaml`. Execution is plan 49's | no (plan only) |

Normalized node naming: `node-01` … `node-09`, stable for the life of the node. The provider resource name embeds it: `sc-<cluster>-node-01`. A replaced node gets the **next unused** number (never reused), so stale host keys and etcd member names cannot collide.

Adding a provider means adding one Go package, one compute root module with guard files, catalogue entries (if paid) and tests. Nothing in configure, seed or later phases changes. The `provider.name` enum in the contracts gains one value.

## 9. Configuration

### 9.1 Profile files

- `infra/profiles/self-hosting-minimum.yaml`: SPEC §11 verbatim (the `substrate.schema.yaml` example).
- `infra/profiles/small-ha.yaml`: SPEC §12 verbatim (the `cluster-profile.schema.yaml` example).
- `infra/profiles/lab-small.yaml`, `infra/profiles/lab-ha.yaml`: lab substrates labeled `strange.company/lab: "true"` (plan 05 §14.6).

**Command/kind rules.** `./bootstrap --profile X` (fresh install) requires `kind: BootstrapSubstrate`. `./bootstrap expand --profile X` requires `kind: ClusterProfile`. `recover` uses the profile recorded in the committed config. A mismatch fails PF-03.

**Decision (conservative):** a fresh install directly into `small-ha` is not supported. The only path to `small-ha` is install followed by expand, so the expansion path (SPEC §12 "not a separate architecture") is the tested one.

### 9.2 Substrate floor rules (enforced by PF-03, beyond the schema)

- A substrate without the lab label must have `minimumPerNode ≥ {cpu: 4, memory: 8Gi, storage: 100Gi ssd}` (the SPEC §11 minimum, SPEC v1.2 / CD-25). A node whose measured root disk is below 0.92 × 100 GiB is rejected by preflight and substrate validation (`NODE_BELOW_MINIMUM`); 75 GiB-class machines are therefore rejected.
- A lab-labeled substrate is refused when `Capabilities().paid` is true.
- `servers ≤ count`, `cnpg.instances ≤ count`, `longhorn.replicas ≤ count`, and `preferredPerNode ≥ minimumPerNode` field-wise.

### 9.3 Effective profile

`effective = substrate ⊕ clusterProfile`. Each field takes the ClusterProfile value when present and otherwise the value from the installation's substrate. For `small-ha` this yields:

| Field | Value | Source |
|---|---|---|
| `nodes.count` | 3 | ClusterProfile |
| `kubernetes.servers` | 3 | ClusterProfile |
| `cnpg.instances` | 3 | ClusterProfile |
| `longhorn.replicas` | 2 | ClusterProfile |
| `maximumConcurrentAgents` | 2 | ClusterProfile |
| `maximumConcurrentCIJobs` | 1 | inherited |
| `preferredPerNode` | 4 / 12Gi (inherited) / 200Gi nvme (ClusterProfile, SPEC v1.2) | merged field-wise |
| `cnpg.storageClass` | `local` | inherited |
| `distribution` | k3s | inherited |
| `objectStorage.required` | true | inherited |

The effective profile is written to `cluster-vars` (plan 18 key names) and recorded in `bootstrap-config.yaml` (`cluster.profile`). An expansion must not decrease any count (PF-03, `PROFILE_SHRINK_REFUSED`).

### 9.4 Guardrail configuration

| Knob | Source | Default | Who may change |
|---|---|---|---|
| `EXPECTED_NODE_COUNT` | `bootstrap.env` | — (required) | operator |
| `MACHINE_CLASS` | `bootstrap.env` | — (required for paid) | operator |
| `MAX_MONTHLY_COST` + `COST_CURRENCY` | `bootstrap.env` | — / `EUR` | operator |
| Catalogue freshness warn / fail | `infra/catalog/prices/<p>.yaml` + sc-bootstrap constants | warn > 90 days since `verifiedAt`, fail > 180 days | PR |
| `SC_ACCEPT_STALE_PRICES=1` | runtime env | unset | operator, per run (recorded as confirmation `accept-stale-prices`) |
| Region allowlist | provider package constant + catalogue `regions` | catalogue | PR |
| Guard allowlists | `guard*.yaml` | per module | PR |

## 10. Secrets

Consumes `SC-INFRA-PROVIDER` (environment of the OpenTofu or provider subprocess only; never in state, logs or `ProviderOutput`) and `SC-SSH-OPERATOR` (public key injected into nodes; private key used for discovery). Introduces no new secret class. For CI plan-only tests, plan 04 introduces `SC-CI-OVH-READONLY`.

**State hygiene:** compute modules must not put secrets into resource attributes. In particular OVH must not email or return root passwords (plan 04: `do_not_send_password = true` (verify)), and no provider credential may appear in variables persisted to state. `make tofu-validate` fails if a root module declares a `variable` with a name matching `(?i)(secret|password|token|key)` without `sensitive = true` and `ephemeral = true` (OpenTofu ≥ 1.11 ephemeral variables (verify); with ≤ 1.10, credentials are only allowed through provider-native environment variables).

## 11. Storage

None beyond §6. Provider plan files live in `.bootstrap/work/compute/` and are encrypted by OpenTofu plan encryption (plan 06). They are deleted after a successful apply and never uploaded.

## 12. Resource requirements

Off-cluster only. A guard evaluation takes < 1 s for plans under 1000 resource changes. SSH discovery takes < 30 s per node.

## 13. Security boundary

- Provider credentials grant spending authority. They exist only in `bootstrap.env`, in the bundle (plan 35) and in subprocess environments, and never in the cluster. The cluster has no provider credential at all (SPEC §86 spirit). Expansion and destroy run from the operator machine.
- Guard files and the price catalogue are protected paths (plan 01 §13.2). An agent task cannot loosen the guard.
- `Apply` accepts only a planfile whose digest the journal records as confirmed. The digest is computed by sc-bootstrap from the planfile JSON at confirm time and recomputed immediately before apply. A mismatch aborts with `PLAN_DIGEST_MISMATCH`.

## 14. Happy path

### 14.1 Sequence inside plan 50's phases

1. `preflight`: `Validate`, `Preflight`.
2. `plan.compute-plan`: `Plan` → guard (§14.4) → cost (§14.6).
3. `plan.confirm`: footprint and typed confirmation (§14.7).
4. `provision`: `OrphanScan` → `Apply` → post-apply audit → `Inventory` → substrate validation (§14.3) → host key pinning.

### 14.2 Validation summary (SPEC §14 checklist → mechanism)

| SPEC §14 item | Mechanism | Fails with |
|---|---|---|
| configuration | schema (PF-02), profile rules (PF-03) | exit 2/3 |
| (object storage, interpreted as paid infrastructure; BOOT-12) | plan 50 `plan.storage-confirm`: bucket set, Object Lock periods and principals printed and typed-confirmed (`storage <cluster>`) before `plan.storage-apply` | exit 5 |
| credentials | `Preflight` (PF-07) | exit 3 |
| requested products | catalogue lookup + live catalogue availability (PF-08) + guard `priceProductFrom` match | exit 3/4 |
| node count | PF-09; guard `maxCount: expectedNodeCount`; post-apply audit | exit 3/4 |
| location/region | PF-10; guard checks the region attribute against config | exit 3/4 |
| SSH material | PF-06; the planned public key attribute equals the configured key's fingerprint | exit 3/4 |
| expected cost | §14.6 | exit 4 |

### 14.3 Substrate validation of real nodes

After `Inventory`, each node's discovered facts are compared with the effective profile:
- `cpu_count ≥ minimumPerNode.cpu`;
- `memory_bytes ≥ 0.92 × memory`, where `memory_bytes` is MemTotal and Gi is binary;
- root disk `size_bytes ≥ 0.92 × storage.size`;
- `class` rank ≥ required rank (nvme 3 > ssd 2 > hdd 1 > unknown 0).

The 0.92 factor exists because:
- OVH and others market decimal GB (100 GB = 93.1 GiB = 0.931 × 100 Gi, so an OVH VPS-2 meets the 100Gi minimum within tolerance but not the 200Gi preferred size);
- the kernel reserves memory (an 8 GiB guest typically reports 7.6–7.8 GiB MemTotal).

This is recorded as an interpretation of SPEC §11 (open question OQ-03-1). The result is reported per node as `meets_minimum` and `meets_preferred`. A node below the minimum fails the phase with `NODE_BELOW_MINIMUM`, and that is never waivable. For paid providers that means stopping before configure. The operator decides whether to destroy the node (plan 49), and the report points out the committed spend.

Below-preferred but at-minimum nodes produce a `warn` and the ADR-0020 `constrained` notice. Plan 51 then selects the constrained resource-profile set via `cluster-vars` (`SC_CAPACITY_CLASS=constrained`; the key name is plan 51's).

### 14.4 Plan-JSON allowlist guard (ADR-0102)

Input: `tofu show -json <planfile>`. The rules are evaluated in order, and the first failure stops evaluation:

1. `format_version` is a version the guard was tested with (allowlist in code; unknown major version → `GUARD_PLAN_FORMAT_UNSUPPORTED`).
2. Every entry in `configuration.provider_config` and every `resource_changes[].provider_name` is in `allowedProviders`.
3. For each `resource_changes[]` entry with `mode = "managed"`:
   - `type` must appear in `resources[]`, otherwise `GUARD_UNEXPECTED_RESOURCE`.
   - Every action in `change.actions` must be in `allowedActions`, otherwise `GUARD_ACTION_FORBIDDEN`.
   - `["delete","create"]` or `["create","delete"]` (replace) on a `paid: true` type fails when `forbidReplace` is true (`GUARD_REPLACE_FORBIDDEN`).
4. Count rule: per type, (instances in prior state minus deletes plus creates) ≤ `maxCount` (`expectedNodeCount` resolves to config). Otherwise `GUARD_COUNT_EXCEEDED`.
5. Product rule: for paid types with `priceProductFrom`, the planned attribute value (for example `plan[0].plan_code`) must equal the catalogue `planCode` of `MACHINE_CLASS`. Otherwise `GUARD_PRODUCT_MISMATCH`. An unknown value (computed at apply) fails closed.
6. Region and SSH rules: provider-specific attribute paths, declared by the provider package, must equal config (`GUARD_REGION_MISMATCH`, `GUARD_SSH_KEY_MISMATCH`).
7. `mode = "data"` reads are allowed. Data sources cannot create resources.
8. **Storage module rule** (`infra/tofu/storage/guard.yaml`): `allowedActions` excludes `delete`. Together with `prevent_destroy` (plan 06), a bucket can never be planned for deletion by sc-bootstrap.
9. **Destroy mode** (`guard-destroy.yaml`, plan 49): only `delete` on the compute types is allowed, and the plan must contain **no** create or update. Any resource outside the compute module is impossible by construction, because only the compute root module is planned.

The guard output (`plan-summary.json`) lists every change with type, address, actions, paid flag and price. The plan digest is `sha256` over the canonical JSON (sorted keys, no insignificant whitespace) of `{resource_changes[].{address,type,change.actions,change.after (paid types only, sensitive values masked)}, configuration.provider_config keys}`. It is therefore stable across re-renders of the same plan and independent of timestamps. `D8` is the first 8 hex characters.

### 14.5 Price catalogue

`infra/catalog/prices/<provider>.yaml` (`InfraPriceCatalog`). One file per provider holds entries with `productId` (the `MACHINE_CLASS` value), `planCode`, specs, regions, monthly and hourly prices, setup fee, commitment period, tax flag, `effectiveFrom`, `verifiedAt` and `sourceUrl`.
- **Freshness:** `verifiedAt` older than 90 days → warn. Older than 180 days → fail (`PRICE_CATALOG_STALE`) unless `SC_ACCEPT_STALE_PRICES=1`, which is recorded as a confirmation.
- **Live cross-check:** when the provider offers a public catalogue API (plan 04), `Preflight` fetches the live price. The estimate uses `max(catalogue, live)` (`price_source: live-catalog-higher` when live is higher). If the live price is lower, the catalogue is used and a warning suggests a catalogue update PR. If the live fetch fails, the catalogue is used with a warning.
- **Currency:** catalogue currency ≠ `COST_CURRENCY` → `PRICE_CURRENCY_MISMATCH` (exit 4). No conversion.
- **Tax:** catalogue prices state `taxIncluded`. The comparison uses the catalogue figure as stated, and the footprint prints "excl. tax" or "incl. tax".
- **Updates:** a PR updates `verifiedAt` and the price. `make locks-check` validates the catalogue against the schema and checks that `metadata.version` increased whenever entries changed (compared with the merge base).

### 14.6 Cost check

`monthly_total = Σ(count_after_apply × unitMonthly)` across paid types. Hourly-billed products count as `hourlyPrice × 730`. `committed_minimum = Σ(new creates × unitMonthly × commitment months) + setup fees`. The check passes iff `complete` (every paid resource priced) and `monthly_total ≤ maxMonthlyCost`. A failure exits 4 (`COST_OVER_LIMIT` or `COST_INCOMPLETE`). For `existing` and `fake` (paid = false), the estimate is 0 and `MAX_MONTHLY_COST=0` is valid.

### 14.7 Footprint and typed confirmation

Printed before any creation, and also by `./bootstrap plan`:

```text
PAID FOOTPRINT — cluster prod (ovh / vps, region GRA)
  CREATE  1 × ovh_vps  ovh-vps-6c-12g-100nvme (plan code vps-2025-model2)  6 vCPU / 12 GiB / 100 GB nvme
          9.99 EUR/month excl. tax, commitment P1M, setup 0.00
  Monthly total: 9.99 EUR  (limit 25.00 EUR)   Committed even if destroyed now: 9.99 EUR
  Catalogue ovh 2026.10.01 (verified 2026-10-01); live price check: OK
  Unchanged: 0   Updates: 0   Deletes: 0
  Ordering is NOT instantly reversible: a VPS is billed per month (plan 04 §14.5).
Plan digest: 3f9a1c27…  To proceed type exactly:  create prod 3f9a1c27
```

The confirmation input must match exactly (case-sensitive, no surrounding whitespace) and gets 3 attempts. Non-interactive runs require `--confirm-plan 3f9a1c27`. The confirmation is recorded with `subject_digest` = the full plan digest. For `existing`, the same layout shows `0 paid resources`, the node list with SSH endpoints, and the warning "configure will take over these hosts (k3s, firewall, sysctls)". The token is `<cluster>`. Expansion uses `expand <cluster> <D8>`.

### 14.8 Apply, audit and orphan scan

1. **OrphanScan** before every apply. If any provider resource named `sc-<cluster>-node-*` (or tagged `strange.company/install=<cluster>` where tags exist) is not in state, abort with `ORPHAN_PAID_RESOURCE` (exit 4) and list it. The operator follows the provider's import step in `bootstrap-resume.md#provision` (OpenTofu `import` block generated by `./bootstrap state import`, plan 06 §7.2). This prevents a second order after a crash during a slow delivery.
2. Recompute the planfile digest and compare it with the confirmation (`PLAN_DIGEST_MISMATCH` otherwise). Then `tofu apply -input=false -lock-timeout=60s <planfile>`. An OpenTofu stale-plan error ("Saved plan is stale") becomes `PLAN_STALE`: the phase fails as retryable and the next run re-plans and re-confirms.
3. **Post-apply audit:** `tofu show -json` (state) resources ⊆ the planned addresses, and per-type counts equal planned counts. The provider listing (OrphanScan again) must return empty. Any discrepancy → `POST_APPLY_AUDIT_FAILED`, the phase fails, and the report prints every unexpected resource with the procedure `provider-outage.md#unexpected-resources`.
4. `Inventory` → substrate validation (§14.3) → wait for SSH (≤ 15 min; transient retries) → host key capture and pinning (provider-declared `host_key_source`; trust on first use is recorded in the report).

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Invalid provider credentials | PF-07 | nothing created | exit 3 | bootstrap-preflight-failure.md#credentials |
| Product not offered in region | PF-08 (live catalogue) | nothing created | exit 3 with alternatives from catalogue | bootstrap-preflight-failure.md#product |
| Catalogue stale | §14.5 | nothing created | exit 4 unless accepted | bootstrap-preflight-failure.md#guardrail |
| Price over limit / unknown | §14.6 | nothing created | exit 4 | bootstrap-preflight-failure.md#guardrail |
| Plan contains unexpected type / action / count / product | §14.4 | nothing created | exit 4, offending changes listed | bootstrap-preflight-failure.md#guardrail |
| Planfile changed after confirmation | digest recompute | nothing created | `PLAN_DIGEST_MISMATCH` exit 4 | rerun |
| Plan stale at apply | OpenTofu | nothing created | retryable fail; re-plan/re-confirm next run | bootstrap-resume.md#provision |
| Orphan paid resource from a previous crash | OrphanScan | apply blocked | exit 4, list | bootstrap-resume.md#provision |
| Apply partially succeeded | OpenTofu error + state | some resources exist | phase failed; next run re-plans (creates only the remainder; guard counts include prior state) | bootstrap-resume.md#provision |
| Post-apply resource set ≠ plan | audit | possible unexpected spend | fail; print list | provider-outage.md#unexpected-resources |
| Node smaller than minimum | §14.3 | node unusable | fail `NODE_BELOW_MINIMUM` | bootstrap-preflight-failure.md#node-size |
| Provider API outage | `Preflight`/`Plan` 5xx | cannot provision/expand/destroy; running cluster unaffected | transient retries, then fail | provider-outage.md |
| Node unreachable due to provider incident | SSH/kube timeouts, alert `SCNodeDown` (plan 42) | cluster degraded/down | none automatic (no auto-recreate) | provider-outage.md |

## 16. Retry behavior

`Preflight`, `Plan`, `Inventory` and `OrphanScan` are read-only and are retried on transient errors (plan 50 §16). `Apply` is **never** retried within a run. A failed apply ends the phase. The next run starts again at OrphanScan → Plan → guard → (re-confirm if the digest changed) → Apply. Re-confirmation is required whenever the digest differs from the recorded confirmation. Because a partially applied plan has a different remainder, the operator always sees the new footprint.

## 17. Idempotency behavior

- `Plan` is pure with respect to state. Re-planning an applied install yields zero changes, and provision's `Check()` uses exactly this test.
- `Apply` of a confirmed planfile is atomic per resource. OpenTofu records each created resource in state as it completes. Combined with OrphanScan, a crash can never lead to a duplicate order.
- `Inventory` is deterministic given state and SSH facts (sorted by node name).

## 18. Bootstrap behavior

Provider operations run only inside sc-bootstrap phases (`preflight`, `plan`, `provision`, `expand`, `destroy`, `recover`). They never run in-cluster.

## 19. Upgrade behavior

- OpenTofu provider plugin upgrades go through `.terraform.lock.hcl` PRs (plan 01, plan 06). A plugin upgrade can change plan JSON shapes, so guard tests with recorded plan fixtures run in `make tofu-validate` for every module (§22).
- When the provider changes a product (renamed plan codes, new ranges), a catalogue PR adds the new entry. Existing installs keep their `MACHINE_CLASS` until the operator changes it. Changing `MACHINE_CLASS` on a provisioned install yields a replace plan, which the guard forbids (`forbidReplace`). Resizing is a node replacement procedure (plan 47 add-node then remove-node), never an in-place replace.
- Contract changes after the threshold require a version bump (master plan §6.2).

## 20. Recovery behavior

- **Lost bootstrap machine:** provider state is remote (plan 06), so `Inventory` and `OrphanScan` work from any clone.
- **Lost OpenTofu state:** plan 06 §20.4 (import from `OrphanScan` results).

### 20.1 Compute state during `recover` (DR-12)

In the `recover` pipeline only, before `plan.compute-plan`, `Inventory` is attempted for every node resource in state with a 2-minute timeout per node. Nodes that are unreachable, or whose provider API (region) does not answer, are **fenced out of managed state**: sc-bootstrap shows the list and requires typed `fence-nodes <cluster> <count>`, then runs `tofu state rm <address>` for each and records `{address, provider_id, fenced_at}` in the journal (`facts.fenced_nodes`). The compute plan then creates replacement nodes with **new** names (never reused, §8) in the configured region. In recover mode only, guard rule 6 accepts a region different from the one recorded in state, provided every resource in the old region is in `facts.fenced_nodes`; replace and delete stay forbidden. Fenced machines are cleaned up later by provider id once their region answers (`total-cluster-loss.md` §Fenced installation cleanup, plan 45; `provider-outage.md` step 4.4). A fake-provider unit AC covers this (03-AC-12).
- **Provider region outage:** see `procedures/provider-outage.md`. Plan 45 owns "rebuild elsewhere": recover with a different `PROVIDER_REGION`, or provider `existing` on other hardware, using object-storage backups.

## 21. Observability

- `plan-summary.json`, `provider-output.json` and the footprint text go to the run directory, and their S3 copies are retained per plan 50 §11.
- The control-transfer report includes the footprint and the catalogue version (plan 50 §14.11).
- In-cluster alerting on node health is plans 38 and 42 (`SCNodeDown` etc.). Provider *billing* is not monitored by Strange Company (non-goal). The report reminds the operator of the expected monthly total.

## 22. Testing strategy

- **Unit:** guard rule-by-rule with recorded `tofu show -json` fixtures under `test/fixtures/tofu-plans/<module>/` (valid create; extra resource type; extra count; replace; delete in normal mode; product mismatch; region mismatch; unknown planned value; foreign provider; destroy-mode with stray create). Digest stability: the same plan rendered twice yields the same digest, and a one-attribute change yields a different digest. Price math with hourly, monthly, setup fees, currency mismatch, stale and missing prices. Effective-profile merge for every field. Substrate tolerance boundaries (0.919 vs 0.920 vs 0.921 × quantity).
- **Contract:** the three schemas (F02). Every `infra/profiles/*.yaml`, `infra/catalog/prices/*.yaml` and `guard*.yaml` validates against its schema or `$defs`. Every `ProviderOutput` produced by tests validates.
- **Fake provider** (`provider=fake`; delivered with **F08**, not F09, because plan 50's GATE-F08 crash and guard tests use it, BOOT-11): an OpenTofu-free provider that simulates create, delete, delay, partial failure, orphan creation and capacity values from `test/fixtures/fake-provider/*.yaml`, with a create counter. It is used by plan 50 crash tests and all guard E2E tests. It is refused outside `SC_TEST_MODE=1`.
- **Plan-only OVH:** plan 04 §22 (real API, read-only credential, never apply).
- **Lab:** the `existing` provider against QEMU VMs (plan 05 §22).

## 23. Acceptance criteria

### GATE-F09

- **03-AC-1** `make contracts` validates `provider.schema.yaml`, `substrate.schema.yaml` and `cluster-profile.schema.yaml`, and their `x-defs-examples`: all examples pass and all invalid examples fail.
- **03-AC-2** `infra/profiles/self-hosting-minimum.yaml` equals the SPEC §11 block and `infra/profiles/small-ha.yaml` equals the SPEC §12 block (`make profiles-check` compares the parsed YAML of each file with the block extracted from `SPEC.md`).
- **03-AC-3** The guard unit suite passes for every fixture class in §22, and each negative fixture yields its specific error code (table-driven test asserts the code).
- **03-AC-4** For an unchanged fixture plan, the digest is identical across 100 runs. Mutating `plan_code` changes the digest.
- **03-AC-5** With the fake provider, a fake plan containing an unlisted resource type makes `./bootstrap` exit 4, and the fake create counter stays 0.
- **03-AC-6** With the fake provider and `--non-interactive` without `--confirm-plan`, the run exits 5 and the create counter is 0. With a wrong D8 it also exits 5. With the right D8 the counter is 1.
- **03-AC-7** Crash between fake "order accepted" and "state written" (fake `orphan` scenario): the rerun exits 4 with `ORPHAN_PAID_RESOURCE`, and the create counter stays 1 (no duplicate order).
- **03-AC-8** Substrate tolerance: fake nodes with memory 0.919 × 8 Gi fail `NODE_BELOW_MINIMUM`, and 0.921 × 8 Gi pass with `meets_preferred: false` and a warn finding.
- **03-AC-9** `./bootstrap --profile small-ha` (fresh install) exits 3 with PF-03 `PROFILE_KIND_MISMATCH`. `./bootstrap expand --profile self-hosting-minimum` exits 3 likewise.
- **03-AC-10** A stale catalogue (`verifiedAt` 181 days old in a fixture) exits 4 `PRICE_CATALOG_STALE`. With `SC_ACCEPT_STALE_PRICES=1` the run proceeds and the journal shows confirmation `accept-stale-prices`.
- **03-AC-11** A lab-labeled substrate with the fake provider set to `paid: true` exits 3 (`LAB_SUBSTRATE_PAID_REFUSED`).
- **03-AC-12** Recover fencing (DR-12): with the fake provider, state holding `node-01` whose fake API returns `unavailable`, `./bootstrap recover --non-interactive` without the `fence-nodes` confirmation exits 5; with it, the journal lists `node-01` in `facts.fenced_nodes`, the compute plan creates `node-02` in a different fake region, and the guard verdict is `pass`.

### GATE-P02

- **03-AC-20** Real provision (OVH per plan 04, or `existing` per plan 05): `provider-output.json` validates against `provider.schema.yaml`. Every node has `substrate_validation.meets_minimum = true`, and the node count equals `EXPECTED_NODE_COUNT`.
- **03-AC-21** The footprint was printed before creation. The run log contains the `PAID FOOTPRINT` block with a timestamp earlier than the first `tofu apply` event, and the journal holds a `paid-footprint` confirmation whose `subject_digest` equals the applied plan digest.
- **03-AC-22** Re-running `./bootstrap` after provision: the compute plan has 0 changes (`plan-summary.json` `.changes | length` = 0) and provision is `skipped (already done)`.
- **03-AC-23** The post-apply audit passed: `plan-summary.json` audit block shows `unexpected: []` and `OrphanScan: []`.
- **03-AC-24** `gitops/clusters/<cluster>/inventory.yaml` is committed, equals `provider-output.json` `.nodes` without `substrate_validation`, and contains no secret (`make secrets-scan` passes).

## 24. Explicit non-goals

- Monitoring provider invoices or reconciling billed amounts. The guard bounds *planned* spend only.
- Automatic node replacement on provider failure (procedures decide, plan 45/47).
- Multi-provider installs (nodes from two providers in one cluster). `existing` can be used to absorb external nodes later (post-MVP).
- DNS provisioning (plans 09/10) and object-storage provisioning (plans 14/06) through the provider interface.
- Currency conversion.

## 25. Open questions

| # | Question | Blocking | Proposed resolution | Owner |
|---|---|---|---|---|
| OQ-03-1 | SPEC §11 uses binary units (100Gi, 8Gi since v1.2), while providers sell decimal GB: a "100 GB" disk is 93.1 GiB, so a literal reading would reject the very machines sold at the minimum. | no | Apply the uniform 0.92 measurement tolerance (§14.3) and record it as an interpretation in `spec-issues.md` during consolidation. A SPEC wording amendment ("≈ GB as marketed") is optional. | 03 / lead |
| OQ-03-2 | OpenTofu `show -json` format stability across versions | no | Guard allowlists `format_version` majors and re-verifies the fixtures at every OpenTofu bump (03-AC-3 runs in `make tofu-validate`). | 03 |
| OQ-03-3 | Should guard fixtures be generated from real provider plans? | no | Yes: `make tofu-fixtures` (opt-in, needs the read-only CI credential) regenerates fixtures for review. Committed fixtures remain the test input. | 04 |
