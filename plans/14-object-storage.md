# 14 — Object storage

**Status:** Stage-0 plan (draft)
**DAG nodes:** P01 (co-owner with plan 06: bucket set, credentials, capability preflight). This plan also defines checks verified at P10 (`objstore-audit`, `objstore-canary`); see §25 Q-14-1 about adding plan 14 to P10 in `plans/dag.yaml`.
**Related plans:** [03](03-provider-interface.md), [04](04-ovh-provider.md), [06](06-opentofu-state.md), [07](07-k3s.md), [11](11-storage.md), [12](12-cnpg.md), [13](13-longhorn.md), [16](16-forgejo.md), [19](19-seed-handoff.md), [26](26-agent-runner.md), [35](35-secrets.md), [36](36-network-policy.md), [38](38-prometheus.md), [41](41-logging-tracing.md), [42](42-alerting-notifications.md), [43](43-retention-gc.md), [44](44-backup.md), [45](45-recovery.md), [49](49-destroy.md), [50](50-bootstrap-orchestrator.md), [52](52-deterministic-ci.md)
**Contracts:** consumes `bootstrap-config.schema.yaml` (plan 50), `secret-classes.yaml` (plan 35), `alert-catalog.yaml` (plan 42); produces `restore-test-result.schema.yaml` documents (`objstore-audit`, `objstore-canary`; schema owned by plan 44). Owns `contracts/object-storage-layout.schema.yaml` (kind `ObjectStorageLayout`, the schema of `infra/objstore/buckets.yaml`).
**ADRs:** ADR-0005, ADR-0012, ADR-0100 (plan 06); introduces ADR-0204 (backup-protection model), ADR-0205 (CI S3 emulator)
**Procedures:** owns `procedures/object-storage-outage.md`, `procedures/replace-object-storage.md`

## 1. Purpose

External S3-compatible object storage is the off-cluster failure domain for every backup stream and the home of OpenTofu state, the recovery bundle and the seed snapshot (SPEC §15, §29). This plan defines:

1. the **authoritative bucket set**, with purpose, writers, readers, versioning, Object Lock and lifecycle per bucket;
2. the **credential split**: one credential per purpose, scoped as tightly as the provider allows, and a protection model under which **no credential held by the cluster can permanently destroy backup history**;
3. a **provider capability matrix** (AWS, OVH, Cloudflare R2, Backblaze B2, generic) and a **preflight capability suite** that decides support at runtime instead of trusting the matrix;
4. bucket creation responsibility (consistent with plan 06), encryption, a per-consumer outage-behavior table, cost, and the CI S3 emulator.

## 2. Requirements derived from SPEC.md

1. External S3-compatible storage is required at bootstrap. [SPEC §29, §7, §130]
2. It holds CNPG backups and WAL, Longhorn backups, Forgejo bulk objects, repository backups, CI artifacts, OpenTofu state and Loki/Tempo storage. [SPEC §29]
3. The cluster SHALL NOT be the sole failure domain for critical backup data. [SPEC §29]
4. OpenTofu state needs versioning, locking, encryption and restricted credentials. [SPEC §15]
5. CNPG backs up externally (base backups, WAL, PITR). [SPEC §26]
6. Object-storage credentials are a secret class with creator, consumer, storage, rotation and recovery. [SPEC §99]
7. Destroying compute does not destroy external backups or object-storage recovery data. [SPEC §114]
8. An object-storage outage has a procedure. [SPEC §113]
9. Persistent logs and traces use object storage rather than node disks. [SPEC §95]
10. Retention is intentional. [SPEC §107] (Numbers: plan 43; enforcement in bucket lifecycle: this plan.)

Derived:
- **14-R-1** Every bucket in §9.1 has versioning `Enabled`. A provider without object versioning is unsupported (it cannot meet requirement 4 or 14-R-2).
- **14-R-2** For every bucket with `systemOfRecord: true` in the layout (B1, B2, B4–B9; STO-2), the installation reaches protection level `scoped`, `locked` or `separate-account` (§9.4) against every credential the cluster holds. Otherwise bootstrap fails closed with `OBJSTORE_PROTECTION_INSUFFICIENT`, unless the operator sets `S3_ALLOW_WEAK_PROTECTION=true`, which is recorded in the report and raises `SCObjectStorageProtectionWeak` permanently.
- **14-R-2a** In `shared` credential mode the cluster necessarily holds the account-wide key. Therefore (STO-2, BOOT-3): (a) B1 `tofu-state` and B2 `recovery` must live in a **second object-storage account** (same or another provider) whose key the cluster never holds (`S3_STATE_ACCESS_KEY_ID`/`S3_STATE_SECRET_ACCESS_KEY`, optional `S3_STATE_ENDPOINT`; requested from plan 50), or bootstrap fails closed with `OBJSTORE_STATE_UNPROTECTED` (override only with `S3_ALLOW_UNPROTECTED_STATE=true`, recorded and permanently alerted); (b) every in-cluster-written system-of-record bucket (B4–B9) is created with Object Lock `COMPLIANCE`, including B7 `forgejo-objects` and B9 `evidence`, which have no lock in `scoped` mode. B3 (`bootstrap-seed`, reconstructible) and B10 (`telemetry`, best effort) stay unprotected in shared mode, which is accepted.
- **14-R-3** No in-cluster credential may: delete object versions (`DeleteObjectVersion`), change bucket versioning, lifecycle, policy, encryption or Object Lock configuration, bypass governance retention, or delete buckets. Where the provider cannot express a denial, Object Lock in `COMPLIANCE` mode compensates (§9.4).
- **14-R-4** Capabilities are **tested**, never assumed from the matrix (§14.3). The suite runs at P01, in `./bootstrap preflight`, and daily in-cluster (read-only subset).
- **14-R-5** Only TLS endpoints are used outside CI (`https://`). Plain HTTP is accepted only by the CI emulator profile.
- **14-R-6** No automated Strange Company path deletes a bucket or an object version outside the plan 06 conformance prefix and the `_capability/` prefix of §14.3 (plan 49 §90, 49-AC-9).
- **14-R-7** Lifecycle rules are rendered from one source (`infra/objstore/buckets.yaml`) and verified daily against the live bucket configuration.

## 3. Inputs

- `bootstrap.env` → `BootstrapConfig.spec.objectStorage` (plan 50): `S3_ENDPOINT`, `S3_REGION`, `S3_BUCKET_PREFIX` (default `sc-<CLUSTER_NAME>`), `S3_FORCE_PATH_STYLE`, `S3_BUCKET_MODE` (`create`|`existing`), `S3_ACCESS_KEY_ID`/`S3_SECRET_ACCESS_KEY` (instance `admin`).
- **New keys requested from plan 50** (§9.6): `S3_PROVIDER`, `S3_CREDENTIAL_MODE`, `S3_OBJECT_LOCK`, `S3_ALLOW_WEAK_PROTECTION`.
- Retention numbers per bucket and prefix from plan 43 §9.
- For `S3_PROVIDER=ovh` with `S3_CREDENTIAL_MODE=scoped`: the OVH API credential and Public Cloud project id (`SC-INFRA-PROVIDER`, plan 04), used to create S3 users and policies.

## 4. Outputs

- The bucket set of §9.1, created by plan 06's `storage` root module (create mode) or verified (existing mode).
- Per-instance credentials (§10), written by `sc-bootstrap` directly into SOPS files under `gitops/clusters/<cluster>/secrets/<namespace>/s3-<instance>.sops.yaml` (in-cluster instances) or into the off-cluster store and recovery bundle (operator-side instances). Plan 35 path conventions.
- `objstore-capabilities.json` (§14.3) under `s3://<prefix>-tofu-state/<cluster>/runs/<run-id>/` and in the bootstrap report. It contains the per-bucket protection level and the capability flags.
- `cluster-vars` keys (plan 18 §9.2 additions): `S3_OBJECT_LOCK_MODE` (`COMPLIANCE`|`GOVERNANCE`|`NONE`), `S3_PROTECTION_LEVEL` (`scoped`|`locked`|`scoped+locked`|`weak`), `S3_CA_BUNDLE` (empty unless the provider uses a private CA).
- In-cluster checks `objstore-audit` and `objstore-canary` (§21) and their `restore-test-result` documents.
- The function `backup.LastSuccess(s3)` used by plan 49 (destroy pre-check) and by `objstore-audit` (§7.3).

## 5. Dependencies

- **Hard (DAG):** F08 (orchestrator core) for P01. P01 is co-owned with plan 06, which owns the root-module wiring, backend, state encryption and the conditional-write conformance tests C1–C8.
- **Soft:** plan 02 (the toolbox ships AWS CLI v2 as `aws`, plus `age` and `git`; requested in §25 Q-14-3); plan 35 (registration of the instances in §10); plan 43 (retention numbers); plan 38 (blackbox probe `object-storage`, KSM metrics for check CronJobs); plan 36 (egress from consumers to the S3 endpoint on 443).
- **Runtime:** every backup stream (plans 07, 12, 13, 44), Forgejo (16), the Runner (26), Loki/Tempo (41), seed (19) and OpenTofu (06).

## 6. Owned state

| State | Location | Writers |
|---|---|---|
| Bucket configuration (versioning, Object Lock, lifecycle, encryption, public-access block, policies) | the provider; desired form in `infra/objstore/buckets.yaml` and `infra/tofu/modules/buckets/` | `sc-bootstrap` storage apply with `admin` only |
| Credential principals (IAM users, OVH S3 users, B2 application keys) and their policies | the provider | `sc-bootstrap` with `admin` (and the OVH API credential) |
| Bucket *contents* | — | owned by each consumer (§9.1 "System of record") |

## 7. External interfaces

### 7.1 Naming

- Bucket: `<S3_BUCKET_PREFIX>-<purpose>` (master plan §6.1). The prefix matches `^[a-z0-9][a-z0-9-]{1,40}$`, so the longest name (`<prefix>-longhorn-backups`) stays ≤ 63 characters. `S3_BUCKET_PREFIX` is immutable after `plan.install-identity` (plan 50).
- Credential instance: `SC-OBJECT-STORAGE/<instance>` (§10). Kubernetes Secret name: `s3-<instance>` in the consumer's namespace, except where a consumer's chart requires another name (CNPG: `<cluster>-backup-s3`; Longhorn: `longhorn-backup-s3`; Flux seed: `seed-bucket-credentials`).
- Secret keys: `ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`, `REGION`, `ENDPOINT` (generic). Charts that require other key names get them through a second key set in the same Secret (Longhorn: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_ENDPOINTS`; Forgejo: `accessKeyId`, `secretAccessKey`, plan 16; Flux `Bucket`: `accesskey`, `secretkey`).

### 7.2 Bucket layout summary (consumers own the paths inside)

| Bucket | Top-level prefixes (owner plan) |
|---|---|
| `tofu-state` | `<cluster>/{storage,compute,edge}/`, `journal/`, `locks/`, `runs/`, `reports/`, `provider/`, `restore-tests/`, `conformance/` (06, 50) |
| `recovery` | `<cluster>/bundles/`, `<cluster>/manifests/` (35) |
| `bootstrap-seed` | `<cluster>/<sha>/`, `<cluster>/reports/` (19) |
| `pg-backups` | `<cnpg-cluster>/<cnpg-cluster>-g<N>/{base,wals}/` (12) |
| `longhorn-backups` | `backupstore/` (Longhorn-native, 13) |
| `etcd-snapshots` | `<install-id>/` (07) |
| `forgejo-objects` | `lfs/`, `attachments/`, `packages/`, `avatars/`, `repo-avatars/`, `repo-archive/` (16) |
| `repo-backups` | `git/weekly/`, `git/monthly/`, `meta/` (44) |
| `evidence` | `evidence/`, `results/`, `logs/`, `context/`, `quarantine/` (26); `restore-tests/`, `canary/` (44, this plan); `retention/` (43); `telemetry-debug/` (40); `backups/` (16); `breakglass-drills/` (15); `provider-smoke/` (53) |
| `telemetry` | `loki/`, `tempo/` (41) |
| every bucket | `_capability/` (this plan, §14.3) |

### 7.3 Go package `src/internal/objstore` (used by `sc-bootstrap`, `sc-restore-test`)

| Function | Behavior |
|---|---|
| `Layout(cfg) → []Bucket` | parses `infra/objstore/buckets.yaml` and applies the prefix |
| `Capabilities(ctx, admin, instances) → Report` | runs the suite of §14.3 and returns per-check results and per-bucket protection levels |
| `EnsureCredentials(ctx, provider, instances) → []Credential` | creates principals and policies and mints keys (provider adapters `aws`, `ovh`, `b2`, `shared`); idempotent by principal name `sc-<cluster>-<instance>` |
| `RevokeKey(ctx, provider, keyID)` | used by `rotate-storage-credentials.md` (plan 35) |
| `RenderLifecycle(purpose) → JSON` | renders a bucket's lifecycle rules as S3 `LifecycleConfiguration` JSON for manual repair (`sc-bootstrap objstore render-lifecycle --bucket <purpose>`, used by `object-storage-outage.md` §Drift) |
| `AuditConfig(ctx, ro) → Report` | the read-only daily subset (OA checks, §21) |
| `backup.LastSuccess(ctx, ro) → map[stream]time` | newest successful object per backup stream, read from S3 only: barman `base/*/backup.info` with `status=DONE`, newest WAL key under `wals/`, Longhorn `backupstore/volumes/**/backups/backup_*.cfg`, newest `etcd-snapshots/<install-id>/` key, newest `repo-backups/git/**/manifest-*.json`. Consumed by plan 49 and by `objstore-audit` |

## 8. Internal interfaces

- `infra/objstore/buckets.yaml` → `infra/tofu/modules/buckets` (via `yamldecode`) → provider resources.
- The same file → ConfigMap `dr-tests/objstore-expected` (rendered by Flux from `gitops/platform/dr-tests/`, generated at F11 from the YAML so both always match; F11 check `objstore-expected-current`).
- Provider adapters inside `objstore.EnsureCredentials`: AWS IAM API; OVH API (`/cloud/project/{id}/user`, `/user/{id}/s3Credentials`, `/user/{id}/policy` (verify paths)); B2 native API (`b2_create_key`); `shared` (no API: reuses `admin`).

## 9. Configuration

### 9.1 Authoritative bucket set

All buckets: versioning `Enabled`, public access blocked, default SSE where supported (§9.5), and the lifecycle rule `abort-incomplete-multipart` at 7 days. Retention numbers come from plan 43; enforcement is the lifecycle column.

| # | Bucket | Purpose | System of record | In-cluster writers (instance) | Off-cluster writers | Object Lock (when available) | Lifecycle (summary) |
|---|---|---|---|---|---|---|---|
| B1 | `tofu-state` | OpenTofu state, bootstrap journal, run logs, off-cluster restore-test results | yes (S-60, S-61) | none | `tofu-state`, `admin` (storage module, conformance cleanup) | **none** (conformance cleanup and `.tflock` handling need version deletion by `admin`; the credential is never in the cluster). In `shared` mode B1 lives in the separate state account (14-R-2a) | noncurrent: expire after 365 d, keeping the newest 100 noncurrent versions per key; `*/runs/` current 180 d; `*/conformance/` current and noncurrent 1 d |
| B2 | `recovery` | passphrase-encrypted recovery bundles and manifests | yes (S-64) | none | `recovery` | 365 d (`GOVERNANCE` if `scoped`, `COMPLIANCE` if `shared`; bundles are < 1 MiB, so a year of locked generations costs nothing) | noncurrent 3650 d (10 years: effectively never, but the contract requires every bucket to bound its history) |
| B3 | `bootstrap-seed` | seed snapshots, control-transfer reports | no (reconstructible, S-63) | none (Flux reads with `seed-read` during seed only) | `seed-write` | none | noncurrent 30 d. No current-version expiry: snapshot GC is count- and age-aware and done by `sc-bootstrap` (plan 43 R-31) |
| B4 | `pg-backups` | CNPG base backups and WAL, all four clusters | yes (S-03, S-10, S-12…S-14 backups) | `pg-backups-forgejo-pg`, `pg-backups-authentik-pg`, `pg-backups-woodpecker-pg`, `pg-backups-sc-pg` | — | default retention 14 d (`GOVERNANCE` if `scoped`, else `COMPLIANCE`) | noncurrent 14 d. Current versions are deleted by barman retention (30 d, plan 12) |
| B5 | `longhorn-backups` | Longhorn backup target | yes (S-01/S-02/S-30 volume backups) | `longhorn-backups` | — | 14 d (mode as B4) | noncurrent 14 d. Current versions deleted by Longhorn recurring-job retention (plan 13) |
| B6 | `etcd-snapshots` | k3s etcd snapshots (accelerator) | yes (S-41 copies) | `etcd-snapshots` (node config file) | — | 7 d (mode as B4) | current 60 d (removes snapshots of nodes that no longer exist, which k3s never prunes; live nodes' snapshots are always < 7 d old, DR-31b); noncurrent 30 d (plan 07 requirement ≥ 30 d) |
| B7 | `forgejo-objects` | LFS, attachments, packages and OCI blobs, avatars, repo archives | yes (S-04, S-49) | `forgejo-objects` | — | none in `scoped` mode (Forgejo package GC deletes routinely; history comes from versioning and `scoped`); `COMPLIANCE` 14 d in `shared` mode (14-R-2a) | noncurrent 30 d |
| B8 | `repo-backups` | repository bundle chains and metadata exports | yes (S-01/S-02 repo-level copies) | `repo-backups` (write without delete) | — | 14 d (mode as B4) | `git/weekly/` current 56 d; `git/monthly/` current 400 d; `meta/` current 56 d; noncurrent 14 d |
| B9 | `evidence` | RED/GREEN evidence, worker logs, results, context packets, restore-test results, canary, telemetry debug captures (plan 40) | yes (S-20…S-22) | `evidence` (Runner, write without delete), `dr-tests-results` (write without delete) | toolbox (drills) | none in `scoped` mode; `COMPLIANCE` 14 d in `shared` mode (14-R-2a; early deletion of `telemetry-debug/` is then impossible and the 7-day lifecycle is the bound) | per prefix (plan 43 R-04, R-11, R-12): `checks/` 400 d (plan 35 recovery checks, STO-17), `logs/` 90 d, `context/` 180 d, `quarantine/` 90 d, `restore-tests/` 400 d, `canary/` 1 d, `telemetry-debug/` 7 d with noncurrent 1 d (plan 40); `evidence/`, `results/`, `retention/`, `backups/`, `migrations/`, `breakglass-drills/`, `provider-smoke/` no current expiry; noncurrent 30 d |
| B10 | `telemetry` | Loki chunks and index, Tempo blocks (small-ha) | yes, best effort (S-28) | `telemetry` | — | none | noncurrent 1 d; `loki/` current 35 d and `tempo/` current 21 d as backstops behind the compactors (plan 41/43) |

"CI artifacts" (SPEC §29) map onto B7 (images, packages, SBOM and provenance attestations stored as OCI referrers by plan 23) and B9 (RED/GREEN evidence, worker logs). Woodpecker keeps pipeline logs in `woodpecker-pg` (plan 20), so no bucket is needed for them.

All ten buckets are created at P01, including `telemetry`, which stays empty until H03. An empty bucket costs nothing, and creating it later would need an operator-run storage apply.

**Source file** `infra/objstore/buckets.yaml` (owned by this plan; schema `contracts/object-storage-layout.schema.yaml`, validated by F02 and by F11 check `retention-consistency`):

```yaml
apiVersion: strange.company/v1alpha1
kind: ObjectStorageLayout
metadata: {name: strange-company}
spec:
  defaults: {versioning: Enabled, blockPublicAccess: true, abortIncompleteMultipartDays: 7, serverSideEncryption: provider-default}
  buckets:                              # keyed by purpose; all ten required, no others allowed
    pg-backups:
      description: CNPG base backups and WAL for all four clusters
      systemOfRecord: true
      backupStream: true                # subject to 14-R-2; requires objectLock.enabled
      sharedModeProtection: compliance-lock   # 14-R-2a; B1/B2 use separate-account, B3/B10 none-accepted
      inClusterWriters: [pg-backups-forgejo-pg, pg-backups-authentik-pg, pg-backups-woodpecker-pg, pg-backups-sc-pg]
      offClusterWriters: []
      readers: [dr-tests-reader, drill-reader, objstore-audit]
      objectLock: {enabled: true, defaultRetentionDays: 14}   # mode chosen per §9.4
      lifecycle:
        - {id: noncurrent, noncurrentDays: 14, retentionRef: R-14}
    repo-backups:
      # …
      lifecycle:
        - {id: weekly,  prefix: git/weekly/,  currentDays: 56,  retentionRef: R-17}
        - {id: monthly, prefix: git/monthly/, currentDays: 400, retentionRef: R-17}
        - {id: meta,    prefix: meta/,        currentDays: 56,  retentionRef: R-17}
        - {id: noncurrent, noncurrentDays: 14, retentionRef: R-17}
    # … one entry per bucket in the table above; the contract's example is the complete file
```

### 9.2 Provider capability matrix

The matrix records **expectations** used for planning and error messages. The capability suite (§14.3) is the only authority at runtime. Entries marked (verify) are re-checked by 14-AC-9 against the live provider before the reference installation is declared supported.

| Capability | AWS S3 | OVH Object Storage (S3 API) | Cloudflare R2 | Backblaze B2 (S3 API) | Generic S3 |
|---|---|---|---|---|---|
| Versioning (`PutBucketVersioning`, `ListObjectVersions`, GET by `versionId`) | yes | yes (verify) | **no** (`PutBucketVersioning`/`GetBucketVersioning` "not implemented" in Cloudflare's S3 compatibility table, checked 2026-10-13) | yes; B2 keeps versions natively and reports `Enabled` (verify) | test |
| Conditional create `If-None-Match: *` → 412 (tofu lock, plan 06 C1–C3, C7) | yes (since 2024) | yes, documented by OVHcloud; one client report of HTTP 501 exists (verify with C2/C3) | yes | unknown (verify) | test |
| `If-Match` (plan 06 C5, informational) | yes | yes (documented) | yes | unknown | test |
| Object Lock (bucket default retention, GOVERNANCE/COMPLIANCE) | yes; can be enabled on existing buckets | yes (verify); assume enable-at-creation only | **no** via S3 API (R2 "bucket locks" are a separate R2 feature, not used) | yes (verify); enable at creation | test; most require enable-at-creation |
| Lifecycle `Expiration` with prefix filter | yes | yes (verify) | yes | **not via S3 API** (B2 lifecycle rules are set through the B2 native API) (verify) | test |
| Lifecycle `NoncurrentVersionExpiration` (+ `NewerNoncurrentVersions`) | yes (both) | yes (verify `NewerNoncurrentVersions`) | n/a (no versions) | native `daysFromHidingToDeleting` (verify) | test; `NewerNoncurrentVersions` optional |
| Scoped credentials per bucket | IAM user policy | S3 users with JSON user policies (verify action granularity) | API tokens per bucket | application keys per bucket and name prefix | operator-provided or none |
| Write without delete | yes | yes, if policies accept per-action lists (verify) | no (Object Read & Write includes delete) (verify) | yes (`writeFiles` without `deleteFiles`) (verify) | unknown |
| Deny `DeleteObjectVersion` while allowing `DeleteObject` | yes | verify | n/a | **no**: `deleteFiles` deletes versions (verify) | unknown |
| Default SSE | SSE-S3 (default on) | SSE-OMK (verify) | always encrypted at rest | SSE-B2 (verify) | test (`GetBucketEncryption`) |
| Credential minting API used by `EnsureCredentials` | IAM `CreateUser`/`PutUserPolicy`/`CreateAccessKey` | OVH API S3 users and policies (verify paths) | — | `b2_create_key` | none (`shared`) |
| **Strange Company support status** | **supported** (`scoped+locked`) | **supported, reference provider**, subject to 14-AC-9 (`scoped+locked` expected) | **unsupported**: no versioning (14-R-1). Revisit if R2 adds S3 versioning | **supported in `existing` bucket mode only**: the operator creates buckets, Object Lock and lifecycle with the B2 console or CLI following `procedures/object-storage-outage.md` §Appendix B2; sc-bootstrap verifies. Protection comes from Object Lock (`locked`) | **supported if the suite passes**, with `S3_CREDENTIAL_MODE=shared` and Object Lock `COMPLIANCE`; otherwise fails closed (14-R-2) |

The OpenTofu storage module uses the `hashicorp/aws` provider with an endpoint override for AWS, OVH and generic. B2 is not created by OpenTofu (existing mode). This keeps the module provider-neutral (SPEC §13) and avoids a B2-specific module in MVP.

### 9.3 Credential protection by in-cluster consumers

Writers that delete as part of normal operation (CNPG barman retention, Longhorn retention, k3s snapshot pruning, Forgejo package GC, Loki/Tempo compaction) hold `DeleteObject`, which on a versioned bucket only creates a delete marker. Writers that never need deletion (repo bundles, evidence, restore-test results) do not hold it. **None** holds `DeleteObjectVersion`, bucket-configuration writes or `BypassGovernanceRetention`.

### 9.4 Protection levels (ADR-0204)

Per in-cluster backup bucket (`backupStream: true`), the suite computes:

| Level | Condition | Effect of a fully compromised cluster credential |
|---|---|---|
| `scoped` | the instance credential is denied `DeleteObjectVersion`, versioning, lifecycle, lock and policy writes, governance bypass and other buckets (OC-08 negative tests pass) | can create delete markers and new versions; history remains for the noncurrent window (≥ 14 d) |
| `locked` | Object Lock is enabled with a default retention ≥ the bucket's lock days, and the instance cannot shorten it: mode `COMPLIANCE`, or mode `GOVERNANCE` together with `scoped` | cannot remove any version younger than the lock period |
| `scoped+locked` | both | as above, and also cannot overwrite bucket configuration |
| `separate-account` | the bucket lives in an account whose credentials the cluster never holds (B1, B2 in `shared` mode) | none from the cluster |
| `weak` | none of the above | can destroy history. **Fails closed** (14-R-2) |

Mode selection: `S3_CREDENTIAL_MODE=scoped` → `GOVERNANCE` (the operator's `admin` can still clean up in an emergency); `shared` → `COMPLIANCE` (nobody can shorten retention, which is the only protection when the cluster holds the admin key). **What shared mode does not prevent:** a compromised cluster holding the account key can still rewrite lifecycle rules, so every version older than the lock period can be purged at once, and it can delete B3/B10. Protection in shared mode therefore equals the lock period (14 d), not the noncurrent window; the off-cluster records (B1, B2) are safe only because they live in the separate account. The default lock periods (B4/B5/B8: 14 d, B6: 7 d) bound the cost of COMPLIANCE mistakes, because nothing locked can outlive them by more than the period.

`S3_OBJECT_LOCK`: `auto` (default; enable on lock-listed buckets if OC-07 shows support), `required` (fail if unsupported), `off` (never; then `scoped` must hold). Object Lock is decided **at bucket creation**, because most providers cannot enable it later. Changing the decision afterwards requires new buckets and a migration (non-goal for MVP; §24).

### 9.5 Encryption

| Layer | Decision |
|---|---|
| In transit | HTTPS only (14-R-5). Consumers verify the certificate. A private CA is supplied through `S3_CA_BUNDLE` (ConfigMap `s3-ca` in consumer namespaces) and the toolbox trust store. |
| At rest, provider | Default bucket SSE where the provider offers it (`aws_s3_bucket_server_side_encryption_configuration` with `AES256` on AWS; provider default elsewhere). Informational check OC-09. Not relied on for confidentiality against the provider. |
| Client-side | OpenTofu state: OpenTofu encryption (plan 06). Recovery bundle: age passphrase (plan 35). Repository bundles and metadata exports: age to `SC-AGE-BACKUP` (plan 44, ADR-0202). **No** client-side encryption for CNPG (barman-cloud has none, SI-G3-03), Longhorn backups (no native client-side encryption for the backup store in V1 without encrypted volumes) or etcd snapshots (k3s; Secrets inside are encrypted at rest if plan 07 enables secrets encryption). Residual risk recorded in §13. |

### 9.6 Configuration knobs

| Knob | Source | Default | Who may change |
|---|---|---|---|
| `S3_PROVIDER` (`aws`\|`ovh`\|`b2`\|`generic`) | `bootstrap.env` (new, plan 50) | `generic` | operator; immutable after install |
| `S3_CREDENTIAL_MODE` (`scoped`\|`shared`) | `bootstrap.env` (new) | `scoped` for `aws`, `ovh`; `shared` for `b2` (existing mode, key per bucket created by the operator is also accepted: `scoped`), `generic` | operator |
| `S3_STATE_ACCESS_KEY_ID`, `S3_STATE_SECRET_ACCESS_KEY`, `S3_STATE_ENDPOINT` (default `S3_ENDPOINT`) | `bootstrap.env` (new, plan 50) | unset; **required** in `shared` mode (14-R-2a) | operator |
| `S3_ALLOW_UNPROTECTED_STATE` | `bootstrap.env` (new) | `false` | operator; recorded and permanently alerted |
| `S3_OBJECT_LOCK` (`auto`\|`required`\|`off`) | `bootstrap.env` (new) | `auto` | operator, before first install |
| `S3_ALLOW_WEAK_PROTECTION` | `bootstrap.env` (new) | `false` | operator; recorded and permanently alerted |
| Bucket set, lock days, lifecycle | `infra/objstore/buckets.yaml` | §9.1 | PR (human review); lifecycle values must equal plan 43 (F11 check `retention-consistency`) |
| Credential rotation age | plan 35 register | 180 d | PR to plan 35 |
| Canary interval | `gitops/platform/dr-tests/` CronJob | `*/15 * * * *` | PR |

## 10. Secrets

Class `SC-OBJECT-STORAGE` (plan 35 register). This table is authoritative for the instance list; plan 35 must mirror it (differences from the current register are listed in §25 Q-14-2).

Legend: L = List, G = Get (incl. by version), P = Put, D = Delete (delete marker), DV = DeleteObjectVersion, CFG = bucket configuration write, BYP = bypass governance. Read-only bucket-configuration reads (`GetBucketVersioning`, `GetBucketLifecycleConfiguration`, `GetObjectLockConfiguration`) are written as "cfg-r".

| Instance | Buckets / prefix | Rights | Held by | Storage | In bundle |
|---|---|---|---|---|---|
| `admin` (called `bootstrap-admin` in plans 35 and 50 and in `secret-classes.yaml` until G1 adopts this name; same credential, `S3_ACCESS_KEY_ID`/`S3_SECRET_ACCESS_KEY` in `bootstrap.env`) | all (account) | all, incl. CreateBucket, DV, CFG, credential minting | operator, `sc-bootstrap` storage module and rotation | `bootstrap.env`, bundle | yes |
| `tofu-state` | B1 | L G P D (no DV, no CFG) | toolbox (OpenTofu, journal) | off-cluster SOPS + bundle | yes |
| `tofu-state-ro` | B1 `<cluster>/{restore-tests,storage,compute,edge}/` | L, HeadObject, cfg-r (versioning) | `dr-tests` `restore-test-tofu-state` and `check-tofu-lock-age` | SOPS `dr-tests/s3-tofu-state-ro` | no |
| `recovery` | B2 | L G P (no D, no DV) | toolbox | off-cluster SOPS + bundle | yes |
| `recovery-reader` | B2 `<cluster>/{bundles,attestations}/` | L G | recovery check CronJobs in `dr-tests` (plan 35: `restore-test-recovery-bundle`, `recovery-bundle-currency`, `recovery-key-attestation`) | SOPS `dr-tests/s3-recovery-reader` | no |
| `seed-write` | B3 | L G P D | toolbox (seed upload and snapshot GC) | off-cluster SOPS + bundle | yes |
| `seed-read` | B3 `<cluster>/` | L G | Flux source-controller during seed only (deleted at transfer, plan 19) | `flux-system/seed-bucket-credentials` (imperative) + bundle (black start re-seeds) | yes |
| `pg-backups-<cluster>` ×4 | B4 `<cluster>/` (bucket-wide where prefix scoping is unavailable) | L G P D | CNPG barman-cloud plugin sidecar of that cluster | SOPS `<ns>/<cluster>-backup-s3` | no (tier-1 SOPS in Git, recoverable with the bundled cluster key; matches the register, DR-20) |
| `longhorn-backups` | B5 | L G P D | longhorn-manager | SOPS `longhorn-system/longhorn-backup-s3` | no (tier-1 SOPS, as above) |
| `etcd-snapshots` | B6 `<install-id>/` | L G P D | k3s servers | node file (plan 07) + off-cluster SOPS + bundle | yes |
| `forgejo-objects` | B7 | L G P D | Forgejo | SOPS `forgejo/forgejo-objects-s3` | no |
| `repo-backups` | B8 | L G P (no D) | `repo-bundle-backup` CronJob | SOPS `forgejo/s3-repo-backups` | no |
| `evidence` | B9 | L G P (no D) | Agent Runner | SOPS `strange-company/s3-evidence` | no |
| `telemetry-debug` (plan 40) | B9 `telemetry-debug/` only | P D where the provider supports prefix scoping (D used by the Runner's grant-retention sweeper); P only otherwise, and retention then relies on the lifecycle rule alone | Model Gateway (writes debug captures), Runner sweeper (deletes) | SOPS `strange-company/s3-telemetry-debug` | no |
| `dr-tests-results` | B9 `restore-tests/`, `canary/`, `checks/`, `retention/`, `backups/`, `migrations/` (STO-17) | L G P (no D) | restore-test and check CronJobs in `dr-tests`; plan 35's recovery checks (`checks/`); `oci-retention*` in `forgejo` (a second Secret copy `forgejo/s3-dr-tests-results`); the toolbox for `backup now` (`backups/`) | SOPS `dr-tests/s3-dr-tests-results` | no |
| `dr-tests-reader` | B4, B5, B6, B8 (+ B7 `packages/` HEAD for `oci-retention-audit`) | L G | restore tests | SOPS `dr-tests/s3-dr-tests-reader` | no |
| `objstore-audit` | all ten buckets | L, cfg-r | `objstore-audit` CronJob | SOPS `dr-tests/s3-objstore-audit` | no |
| `telemetry` | B10 | L G P D | Loki, Tempo (H03) | SOPS `monitoring/s3-telemetry` | no |
| `drill-reader` (plan 45 RD3 drills) | B4, B5, B6, B7, B8 of the **source** installation, read-only (B7 `forgejo-objects` added for DR-9: the drill copies it server-side into `<prefix>-drill-forgejo-objects` to serve internal images and packages, plan 45 §20.7) | L G (no P, no D, cfg-r none) | the drill installation's `sc-bootstrap recover --drill` and its scratch restores; never placed in the source cluster | off-cluster SOPS + bundle of the source installation | **yes (bundled**, registered by plan 35) |

Notes:
- **Object Lock and `telemetry-debug/`:** the `evidence` bucket (B9) has no Object Lock, so debug captures can be deleted before their 7-day lifecycle expiry when a grant's `retentionDays` is shorter (SPEC §94 retention limits). This is decided deliberately: debug captures may contain prompt and completion content, and keeping them longer than granted is the bigger risk. Plan 40's alternative of a separate bucket is not needed.
- `shared` credential mode: every in-cluster instance's Secret holds the account key (not the separate state-account key, 14-R-2a). The Secrets and consumers stay the same, so switching a provider to `scoped` later is a rotation, not a redesign. In that mode B9 is COMPLIANCE-locked, so early deletion of debug captures is impossible and the 7-day lifecycle is the bound.
- **Fencing (ADR-0554):** recovery rotates every in-cluster instance before seeding unless the operator attests the old installation is destroyed. `EnsureCredentials` with `--rotate-all` does this; the set includes the write instances `dr-tests-results` and `telemetry-debug` (DR-30). In `shared` mode fencing means rotating the account key itself and re-sealing the bundle (`./bootstrap rotate refresh-bundle`); the state account key is not rotated (the cluster never held it). The rotated keys are written as SOPS files into the recovery commit (plan 45, DR-4).
- **Rotation** (`rotate-storage-credentials.md`, plan 35): mint new key → update SOPS → Flux applies → consumers restart or re-read (CNPG plugin and Longhorn re-read Secrets on the next operation, verify) → canary passes with the new key → revoke the old key. Overlap ≤ 24 h. Max age 180 d.

## 11. Storage

External only. Expected volumes at self-hosting-minimum after one year (planning estimate; `objstore-audit` measures actual bytes per bucket as `sc_objstore_bucket_bytes`):

| Bucket | Estimate | Basis |
|---|---|---|
| `pg-backups` | 25–40 GiB | 30 daily base backups of ≈ 0.6 GiB compressed for four clusters + WAL (4 MiB segments, gzip) + 14 d noncurrent |
| `longhorn-backups` | 10–20 GiB | incremental block backups of `forgejo-data`, `hermes-data`, `dr-canary` (7 daily + 4 weekly) |
| `repo-backups` | 5–30 GiB | 8 weekly full chains + 12 monthly fulls; grows with repository size |
| `etcd-snapshots` | 2–6 GiB | 28 × compressed snapshots + 30 d noncurrent |
| `forgejo-objects` | 20–60 GiB | internal images, packages, attachments (plan 43 R-09/R-10 bound it) |
| `evidence` | 2–5 GiB | logs ≤ 10 MiB per execution, 90 d |
| others | < 1 GiB | state, bundles, seed snapshots |
| **Total** | ≈ 65–160 GiB | |

## 12. Resource requirements

Negligible in-cluster: `objstore-audit` (daily, ≤ 2 min) and `objstore-canary` (every 15 min, ≤ 20 s), each 20m/64Mi request and 128Mi limit, PriorityClass `ci` (the `dr-tests` rule of plan 51). As light checks they do **not** request `strange.company/heavy-slot` at all (CD-16, plan 44 §9.3), so a running worker cannot starve the 15-minute canary.

**Cost** (selection rule: the operator's provider price list at install time; the figures below are planning estimates, verify): about 100 GiB-month of storage is roughly €1–3 per month on OVH Standard object storage and about USD 2.5 on AWS S3 Standard. Request costs are dominated by WAL archiving (≈ 600–3 000 PUT/day) and Longhorn block uploads, typically under USD 1 per month. Egress is dominated by restore tests (≈ 10 GiB per week; free inside OVH's region (verify), about USD 3–4 per month on AWS to an external VPS). `./bootstrap plan` prints the object-storage estimate as a separate line in the paid footprint (plan 03 §14.5, requested).

## 13. Security boundary

- The object store is outside the cluster and outside the provider account that hosts compute when the operator chooses so (recommended: a separate account or project, so a compromised infrastructure credential cannot delete backups; §25 Q-14-5).
- Threats and controls:

| Threat | Control |
|---|---|
| Compromised node or namespace exfiltrates backups | per-instance scoping limits reads to that stream; CNPG and Longhorn backups are not client-encrypted (accepted residual, SI-G3-03); repo bundles are age-encrypted |
| Compromised cluster destroys backups | 14-R-2/14-R-3: no DV rights; Object Lock; lifecycle only via `admin` |
| Compromised cluster rewrites lifecycle to expire everything | no CFG rights for in-cluster instances (OC-08); `objstore-audit` detects drift daily (`SCObjectStorageConfigDrift`) |
| Operator laptop compromise (`admin`) | out of the cluster's threat model; Object Lock `COMPLIANCE` (shared mode) still protects; recovery bundle needs the passphrase |
| Public exposure | public-access block on every bucket; OC-10 anonymous GET returns 403 |
| Ransomware-style overwrite | versioning keeps previous versions for ≥ the noncurrent window; locked versions cannot be removed |

- Network: only the pods listed in §10 have egress to the S3 endpoint (plan 36). Workers (`agent-jobs`), CI (`ci-jobs`) and Hermes have no route to it [SPEC §88].

## 14. Happy path

### 14.1 Install (P01), create mode

The step order is plan 50's (authoritative, BOOT-10); this plan specifies the object-storage content of each step.
1. `plan.install-identity` (plan 50): prefix fixed.
2. Preflight PF-12 (endpoint, `admin` validity, and in `shared` mode the state-account key), PF-13 (clock).
3. `plan.storage-confirm` (requested from plan 50, BOOT-12): prints the bucket set, Object Lock periods per bucket, principals to be created and "usage-billed; locked objects cannot be deleted before their retention ends (≤ 365 d for `recovery`, ≤ 14 d elsewhere)", and requires the typed confirmation `storage <cluster>` (journal kind `storage-footprint`). SPEC §14's "intended paid footprint printed before first creation" therefore covers object storage too.
4. `plan.storage-apply` (plan 06): the storage root module creates the ten buckets from `infra/objstore/buckets.yaml` (B1/B2 in the state account in `shared` mode): `aws_s3_bucket` (with `object_lock_enabled` per §9.4 decision), `aws_s3_bucket_versioning`, `aws_s3_bucket_object_lock_configuration`, `aws_s3_bucket_lifecycle_configuration`, `aws_s3_bucket_public_access_block`, `aws_s3_bucket_server_side_encryption_configuration`, and, for `aws`, IAM users and policies (no access keys in state). Every bucket has `prevent_destroy`.
5. Bundle upload and remote journal (plan 50).
6. `plan.storage-credentials` (plan 50): `objstore.EnsureCredentials` mints one key per instance through the provider adapter, writes in-cluster instances into SOPS files (both age recipients) and operator-side instances into the off-cluster store, and stages them for the bundle.
7. `plan.objstore-capabilities` (plan 50): the suite of §14.3, including the consumer-client probes OC-17. Fatal results stop the pipeline (exit 3) before any paid compute is created.
8. `plan.state-conformance` (with the `tofu-state` credential minted in step 6) and `plan.state-migrate` (plan 06).
9. The recovery bundle is re-sealed with the credentials (plan 35) and uploaded to `recovery`.

### 14.2 Existing mode

Steps 3 and 4 are replaced by verification: every bucket exists with versioning `Enabled` (PF-12), and the operator supplies per-instance keys in `bootstrap.env` as `S3_CRED_<INSTANCE>_ACCESS_KEY_ID`/`…_SECRET_ACCESS_KEY` (requested plan 50 keys) or uses `shared` mode. Step 5 runs unchanged.

### 14.3 Capability suite (`plan.objstore-capabilities`; also `./bootstrap preflight` once buckets exist)

Runs with `admin` plus every instance key. Test objects live under `_capability/<run-id>/` of each bucket. They are 1 KiB each, and in locked buckets they remain until the lock expires (accepted; < 100 KiB per run).

| # | Check | Pass condition | Fatal? | Code |
|---|---|---|---|---|
| OC-01 | HTTPS reachability and certificate validity of `S3_ENDPOINT` | TLS handshake verifies | yes | `OBJSTORE_TLS` |
| OC-02 | `HeadBucket` on every bucket with `admin` | 200 (403 = name owned by another account: `OBJSTORE_BUCKET_NAME_TAKEN`) | yes | `OBJSTORE_BUCKET_MISSING` |
| OC-03 | `GetBucketVersioning` = `Enabled` on every bucket | all | yes | `OBJSTORE_VERSIONING_DISABLED` |
| OC-04 | Version history: PUT twice, `ListObjectVersions` ≥ 2, GET first `versionId` returns first content (one bucket, B9) | pass | yes | `OBJSTORE_VERSIONING_UNSUPPORTED` |
| OC-05 | Conditional create semantics (delegated to plan 06 C1–C3, C7 on B1) | pass | yes | plan 06 codes |
| OC-06 | `GetBucketLifecycleConfiguration` equals the rendered rules (normalized: order-insensitive, defaults expanded) | equal | yes | `OBJSTORE_LIFECYCLE_MISMATCH` (`OBJSTORE_LIFECYCLE_UNSUPPORTED` if the call is not implemented) |
| OC-07 | Object Lock on lock-listed buckets: `GetObjectLockConfiguration` enabled with expected mode and days; a new PUT returns `x-amz-object-lock-retain-until-date` ≈ now + days (±1 d); `DeleteObjectVersion` on it with `admin` is denied in `COMPLIANCE` mode, and in `GOVERNANCE` mode is denied without the bypass header | as configured | if `S3_OBJECT_LOCK=required` | `OBJSTORE_LOCK_UNSUPPORTED` |
| OC-08 | Per-instance least privilege: positive tests (each allowed right in its own prefix succeeds) and negative tests, all **harmless if wrongly allowed**: `DeleteObjectVersion` on a version created for this test; `PutBucketVersioning Status=Enabled`; `PutBucketLifecycleConfiguration` with the *current* rules; `PutObjectLockConfiguration` with the *current* configuration; GET/PUT on another bucket; `DeleteObject` for write-without-delete instances | positives succeed; negatives return 403 | positives yes; negatives feed OC-16 | `OBJSTORE_CREDENTIAL_BROKEN` |
| OC-09 | `GetBucketEncryption` | recorded | no | — |
| OC-10 | Anonymous GET of a test object (no `Authorization` header) | 403 | yes | `OBJSTORE_PUBLIC_ACCESS` |
| OC-11 | Clock skew (plan 06 C8) | ≤ 60 s | yes | `S3_CLOCK_SKEW` |
| OC-12 | Multipart upload: 2 × 5 MiB parts, complete, GET size; abort-MPU rule present | pass | yes | `OBJSTORE_MULTIPART` |
| OC-13 | Checksum header compatibility (plan 06 C6), against a lock-enabled bucket as well (Object Lock uploads need `Content-MD5` or an SDK checksum, verify) | at least one mode works | yes | `S3_CHECKSUM_INCOMPATIBLE` |
| OC-14 | Addressing: path-style or virtual-host per `S3_FORCE_PATH_STYLE` | pass | yes | `OBJSTORE_ADDRESSING` |
| OC-15 | Per-consumer client smoke (CI and P10 only, not preflight): barman-cloud `barman-cloud-check-wal-archive`, Longhorn backup target `Available`, `k3s etcd-snapshot save`, Forgejo attachment upload, Flux `Bucket` Ready. Each against its real bucket | pass | at P10 | consumer-specific |
| OC-17 | **Consumer-client probes at P01** (STO-12), run before any compute exists and before the lock decision is relied on: the `./bootstrap` wrapper on the operator host runs the digest-pinned barman-cloud plugin image's `barman-cloud-wal-archive` against a scratch server name `oc17-<run-id>` in B4; `sc-bootstrap` runs Go probes pinned to the S3 client library and version that the pinned Longhorn backupstore and k3s etcd-s3 vendor (verify per version; recorded in the probe's output) against `_capability/` in B5 and B6. Each probe uploads into the lock-enabled bucket with that client's default checksum behavior | every probe's PUT and multipart upload succeeds | yes | `OBJSTORE_CLIENT_INCOMPATIBLE:<consumer>` |
| OC-16 | Protection level per system-of-record bucket (§9.4) from OC-07, OC-08 and the account placement | `scoped`, `locked`, `scoped+locked` or `separate-account` for every bucket with `systemOfRecord: true` | yes unless `S3_ALLOW_WEAK_PROTECTION=true` | `OBJSTORE_PROTECTION_INSUFFICIENT` |

The result `objstore-capabilities.json` lists every check with latency, the derived flags (`object_lock_mode`, `lifecycle_noncurrent_newer_versions_supported`, `skip_s3_checksum`, `protection_level` per bucket) and the provider. Cleanup deletes test-object versions where not locked (with `admin`, in `_capability/` only).

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Provider lacks versioning (e.g. R2) | OC-03/OC-04 | install impossible | fail closed exit 3 | `bootstrap-preflight-failure.md#object-storage` (plan 50) |
| Provider lacks conditional writes | plan 06 C2 | no remote state | fail closed | same |
| Weak protection | OC-16 | backups destroyable by cluster | fail closed | same; operator chooses scoped credentials, Object Lock or a different provider |
| Endpoint outage (5xx, timeouts, DNS) | `SCObjectStorageUnreachable` (blackbox, plan 38), `SCObjectStorageCanaryFailing` | backups pause (§15.1) | consumers retry | `object-storage-outage.md` |
| Credential revoked or expired (one instance) | consumer errors 403; canary per instance; `SCObjectStorageCanaryFailing{instance}` | one stream stops | none | `object-storage-outage.md` §Credential, `rotate-storage-credentials.md` |
| Bucket configuration drift (versioning suspended, lifecycle changed, lock removed) | `objstore-audit` OA-01..OA-04 → `SCObjectStorageConfigDrift` (critical, black-start threat) | protection lost | none | `object-storage-outage.md` §Drift |
| Bucket deleted | OA-02 404 | stream lost | none | `object-storage-outage.md` §Bucket lost |
| Quota or billing suspension at the provider | 403/`QuotaExceeded`/`AccountProblem` on writes, canary | all writes fail | none | `object-storage-outage.md` §Account |
| Lifecycle silently not applied by the provider | OA-06: oldest current object under an expiring prefix older than days + 3 | cost grows; disk unaffected | none | `object-storage-outage.md` §Drift |
| Stream silent (no new objects while consumer reports success) | OA-07 compares `backup.LastSuccess` with consumer metrics | RPO miss undetected by the consumer | `SCOffsiteBackupFailing24h` (plan 42) | `restore-test-failure.md` |

### 15.1 Outage behavior per consumer

| Consumer | During outage | Local accumulation and bound | Time to harm (self-hosting-minimum) | Recovery after outage |
|---|---|---|---|---|
| CNPG WAL archiving (4 clusters) | PostgreSQL keeps serving; `archive_command` retries; `.ready` files accumulate in `pg_wal` | ≈ 2.3 GiB/day for all four at idle with 4 MiB segments (plan 12 §9.3); more under write load | `SCCnpgWalArchiveBacklog` after 15 min; root disk 75 % warning after ≈ 3–4 days on 100 GiB | archiving drains automatically (`maxParallel: 2`); take an on-demand base backup afterwards |
| CNPG base backups | `Backup` fails | none | `SCCnpgBackupFailed` immediately | next schedule or on-demand `Backup` |
| Longhorn recurring backups | backup jobs fail; snapshots continue locally (retain 4) | snapshot space on `/var/lib/longhorn` | `SCLonghornBackupStale` after the volume's window (26 h) | next schedule; on-demand backup |
| etcd snapshots | local snapshots continue; S3 copy fails | none beyond local retention (plan 07) | `SCEtcdSnapshotUploadFailing` after 13 h | next scheduled snapshot uploads; old local ones are not re-uploaded (accepted) |
| Repo bundles | run fails before upload; mirror cache keeps fetching | cache PVC only | `SCRepoBundleBackupStale` after 2 h | next run builds an incremental from the last *uploaded* manifest, so nothing is skipped |
| Forgejo objects | Git, issues and PRs keep working; LFS, attachments, package push/pull and **internal image pulls** fail | none | immediately for new pods needing an uncached internal image (`ImagePullBackOff`) | automatic |
| Runner evidence and logs | the Runner cannot persist RED/GREEN evidence, so it does not record verification transitions (fail closed, plan 26); log archival retries; Job deletion waits up to the 6 h TTL | Jobs kept up to TTL | tasks stall in `*_VERIFYING`; logs of Jobs older than 6 h are lost (`logs_status=lost`) | Runner retries; stalled tasks resume |
| Loki/Tempo (small-ha) | ingesters buffer in WAL, then drop | WAL on local-path (bounded by plan 41) | best effort | automatic |
| Flux seed (bootstrap only) | seed blocked | — | immediate | `./bootstrap` resume |
| OpenTofu state, journal | provisioning, expand, destroy, recover and journal writes blocked; cluster unaffected | — | immediate for those commands | retry later |
| Recovery bundle upload | rotation blocked; existing bundle remains valid | — | none | retry |
| Restore tests | results `errored` | — | alerts after the check's `maxAge` | rerun |

The dominating risk is root-disk exhaustion from WAL. `object-storage-outage.md` defines the emergency step (temporarily detaching the WAL archiver of the largest producer after a decision point) and its cost (a PITR gap until a fresh base backup completes).

## 16. Retry behavior

- Each consumer retries with its own policy (barman: every archive cycle; Longhorn: next recurring schedule; k3s: next schedule; repo bundles: next run; Runner: 60 s loop).
- The capability suite does not retry fatal results. Transient network errors within a check are retried 3 times with 1 s/2 s/4 s backoff before the check counts as failed.
- `objstore-canary` does not retry inside a run. Freshness alerts tolerate three consecutive failures (`maxAge: 45m`).

## 17. Idempotency behavior

- Bucket creation and configuration: OpenTofu converges; a rerun shows 0 changes.
- `EnsureCredentials`: principals are looked up by name `sc-<cluster>-<instance>`. An existing principal whose key validates (OC-08 positives) is kept. A new key is minted only if the SOPS file is missing or the key fails, and the previous key is then revoked after the new one is applied.
- The capability suite writes only under `_capability/<run-id>/`.
- `objstore-audit` is read-only. `objstore-canary` writes one new object per run under `canary/` (expired by lifecycle after 1 d).

## 18. Bootstrap behavior

P01 is the first point at which any external durable state exists. Ordering is in §14.1. The bucket set is created before any compute, so a failed capability suite costs nothing. During black start (plan 45), buckets already exist: `./bootstrap recover` runs the suite in existing mode, refuses to continue on `OBJSTORE_VERSIONING_DISABLED` or drift, and rotates in-cluster instances for fencing (ADR-0554).

## 19. Upgrade behavior

- Provider API or behavior changes are caught by the daily `objstore-audit` and by the suite on every `./bootstrap preflight`.
- Changing the bucket layout (new bucket, new prefix rule) is a PR to `infra/objstore/buckets.yaml` followed by an operator-run `./bootstrap` (storage apply is off-cluster by design). The F11 check `retention-consistency` ties it to plan 43.
- Changing the provider, endpoint or bucket prefix is a migration, executed by `procedures/replace-object-storage.md` (DR-13): new buckets, credentials and bundle, an import rebuild of OpenTofu state (plan 06 §20.4), a CNPG generation bump to fresh base backups (plan 12 §9.4), a new Longhorn backup target, full bundle chains and an evidence record. Old history is not copied (it stays readable in the old provider while it exists).

## 20. Recovery behavior

- **Outage:** wait it out. Consumers resume. `object-storage-outage.md` covers disk protection and post-outage verification.
- **Credential loss:** re-mint with `admin` (`rotate-storage-credentials.md`).
- **Bucket or history destroyed by the operator or provider:** the other failure domains remain (live cluster, GitHub mirror). Take fresh backups immediately into recreated buckets (new CNPG generation, plan 12 §9.4).
- **Provider total loss while the cluster is alive (or a lost account):** `procedures/replace-object-storage.md`. Backup history before the loss is gone, and the cluster is the only copy until the first fresh backups complete; the procedure is ordered so that the most critical streams (state, `pg-backups`, `repo-backups`) restart first. **Bucket contents deleted by a compromised credential** (delete markers, overwrites inside the noncurrent window): `object-storage-outage.md` §Undelete (STO-11).

## 21. Observability

**Checks** (ADR-0553 check-CronJob pattern, namespace `dr-tests`, image `restore-tester`, binary `sc-restore-test`):

| CronJob | Schedule | Credential | Checks | Alert (catalogue) |
|---|---|---|---|---|
| `objstore-canary` | `*/15 * * * *` | `dr-tests-results` (PUT/GET `canary/<ts>`), `dr-tests-reader` (HEAD on B4, B5, B6, B8) | write, read-back digest, per-bucket HEAD; latency recorded | `SCObjectStorageCanaryFailing` (new; `maxAge: 45m`, `failAfter: 30m`, critical, `black-start-threat`) |
| `objstore-audit` | `20 6 * * *` | `objstore-audit` | OA-01 versioning `Enabled` on all ten; OA-02 bucket exists; OA-03 lifecycle equals `objstore-expected`; OA-04 Object Lock configuration equals the install record; OA-05 public access still blocked (anonymous GET 403); OA-06 lifecycle effectiveness (oldest current object per expiring prefix ≤ days + 3); OA-07 newest object per backup stream (`backup.LastSuccess`) within the stream's window; OA-08 bytes and object counts per bucket (exported); OA-09 (STO-11) sudden loss: for each bucket and top-level prefix, current-object count or bytes dropped by > 20 % (and > 100 objects) since the previous run, or more than 100 delete markers created since the previous run in a bucket whose writer never deletes (B8, B9) | `SCObjectStorageConfigDrift` (new; check-cronjob, `maxAge: 26h`, `failAfter: 1h`, critical, `black-start-threat`) |

Metrics: kube-state-metrics CronJob series (pattern) plus a small per-run textfile-free export. The CronJobs write the `restore-test-result` document. For OA-08 sizes, `sc-restore-test` posts nothing to Prometheus; the bucket sizes are read in Grafana from the latest result document through the Grafana JSON/Infinity datasource (plan 39 decides), so no new exporter is needed.

**Existing catalogue alerts consumed:** `SCObjectStorageUnreachable` (blackbox `probe_success{probe="object-storage"}`, plan 38), `SCOffsiteBackupFailing24h`.
**New alerts for plan 42:** `SCObjectStorageCanaryFailing`, `SCObjectStorageConfigDrift`, `SCObjectStorageProtectionWeak` (info-level `prometheus-rule` on `kube_configmap_info`-style static marker `sc_objstore_protection_weak == 1` exported by KSM from ConfigMap `dr-tests/objstore-expected` label `strange.company/protection-weak`; `severity: warning`, `notifyClass: notify`, repeat 7 d).

**Inhibition:** `SCObjectStorageUnreachable` inhibits the *notification* of `SCCnpgWalArchiveFailing`, `SCCnpgBackupFailed`, `SCLonghornBackupStale`, `SCEtcdSnapshotUploadFailing`, `SCRepoBundleBackupStale` and `SCObjectStorageCanaryFailing` (one outage, one page). The alerts stay visible.

## 22. Testing strategy

- **Unit:** layout parsing and lifecycle normalization (golden files); protection-level computation from synthetic OC results (all combinations of scoped/locked/modes); adapter request building for `aws`, `ovh`, `b2` against recorded HTTP fixtures; `backup.LastSuccess` against fixture listings of barman, Longhorn, k3s and bundle layouts.
- **CI emulator (ADR-0205):** **SeaweedFS** (`weed server -s3 -s3.port=9000`, single binary, digest-pinned image) runs as `s3-emulator:9000` on the k3d Docker network (plan 19) and as a service container for `make test-integration`. Selection rule: latest SeaweedFS release that passes `make test-s3-emulator`, which asserts: `If-None-Match: *` returns 412 on an existing key (8-way concurrent create: exactly one success), versioning with `ListObjectVersions` and GET by version, Object Lock `COMPLIANCE` blocking `DeleteObjectVersion`, lifecycle configuration round-trip, multipart upload. Rejected: MinIO community edition (the community distribution model changed in 2025: binaries and images for the community edition were discontinued and the console reduced (verify current state); a pinned old image would not receive fixes), Garage (no object versioning or Object Lock (verify)). Fallback if SeaweedFS fails the suite at a pinned version: versitygw with its POSIX backend (verify versioning and conditional-write support).
  Consequence (CD-12): every integration test in plans 06, 16, 19, 49, 50 and 52 uses this shared `s3-emulator` service.
- The emulator cannot test provider IAM. OC-08 negatives therefore run only against real providers (P01 live; opt-in `make e2e-s3-provider S3_ENV=<file>`).
- **Live:** acceptance criteria below.

## 23. Acceptance criteria

### GATE-P01 (bucket set, credentials, capability preflight; state side is plan 06)

- **14-AC-1** `make test-s3-emulator` passes at the pinned SeaweedFS digest: conditional create (1 success, 7 × 412), versioning round-trip, COMPLIANCE lock denies `DeleteObjectVersion`, lifecycle round-trip, multipart.
- **14-AC-2** `make test-integration` runs the full capability suite against the emulator in `shared` mode with Object Lock `COMPLIANCE`. `objstore-capabilities.json` shows `fatal: []` and `protection_level: locked` for B4, B5, B6 and B8. A fixture bucket created without versioning makes the suite exit 3 with `OBJSTORE_VERSIONING_DISABLED`, and no compute step runs (fake provider create counter = 0).
- **14-AC-3** Weak-protection gate: in the emulator with Object Lock disabled and `shared` credentials, `./bootstrap` exits 3 with `OBJSTORE_PROTECTION_INSUFFICIENT`. With `S3_ALLOW_WEAK_PROTECTION=true` it continues, and the report contains `object_storage.protection_level: weak`.
- **14-AC-4** On the reference installation (P01 live): `./bootstrap exec -- aws --endpoint-url "$S3_ENDPOINT" s3api list-buckets --query "Buckets[?starts_with(Name, '<prefix>-')].Name"` lists exactly the ten buckets of §9.1, and `get-bucket-versioning` returns `"Status": "Enabled"` for each.
- **14-AC-5** On the reference installation, `objstore-capabilities.json` reports every OC check `passed` (OC-09 recorded) and `protection_level` ∈ {`scoped`, `locked`, `scoped+locked`} for B4, B5, B6 and B8.
- **14-AC-6** Least privilege (reference installation): with the `repo-backups` key, `aws s3api delete-object --bucket <prefix>-repo-backups --key _capability/probe` returns `AccessDenied`. With the `pg-backups-sc-pg` key, `aws s3api put-bucket-versioning --bucket <prefix>-pg-backups --versioning-configuration Status=Enabled` returns `AccessDenied`, and `delete-object --version-id <v>` on a test version returns `AccessDenied`. With any in-cluster key, `get-object` on `<prefix>-tofu-state` returns `AccessDenied`.
- **14-AC-7** Lifecycle: `aws s3api get-bucket-lifecycle-configuration --bucket <prefix>-repo-backups` contains rules `weekly` (56 d, prefix `git/weekly/`), `monthly` (400 d), `meta` (56 d), `noncurrent` (14 d) and `abort-incomplete-multipart` (7 d). The same holds for every bucket against `infra/objstore/buckets.yaml` (checked by OC-06 = passed).
- **14-AC-8** Anonymous access: `curl -s -o /dev/null -w '%{http_code}' "$S3_ENDPOINT/<prefix>-evidence/_capability/<run-id>/probe"` (path-style) prints `403`.
- **14-AC-9** Matrix re-verification: for the reference provider (OVH), every row of §9.2 marked (verify) has a recorded outcome in `docs/reference/s3-provider-<provider>.md` (generated from `objstore-capabilities.json`), and the §9.2 OVH column matches it. A mismatch fails the gate until this plan is amended.
- **14-AC-10** In **every** mode (STO-2): no in-cluster secret holds the `tofu-state`, `recovery`, `seed-write`, `drill-reader` or state-account keys, and in `scoped` mode not the `admin` key either. In `shared` mode the emulator scenario proves the in-cluster key gets `AccessDenied`/`NoSuchBucket` on B1 and B2 (separate account), and a shared-mode install without `S3_STATE_*` exits 3 with `OBJSTORE_STATE_UNPROTECTED`. Method: the E2E harness, which knows the key IDs, finds none of them in `kubectl get secrets -A -o json` after control transfer.

### GATE-P10 (live checks; see Q-14-1)

- **14-AC-11** `kubectl -n dr-tests get cronjob objstore-canary objstore-audit -o jsonpath='{range .items[*]}{.metadata.name}={.status.lastSuccessfulTime}{"\n"}{end}'` shows both with a timestamp younger than their `maxAge`. The newest `objstore-audit` result in `evidence/restore-tests/objstore-audit/` has `outcome: passed` with OA-01…OA-08.
- **14-AC-12** Drift detection (k3d + emulator): suspending versioning on a fixture bucket makes the next `objstore-audit` Job exit 1 with check `OA-01` failed, and `SCObjectStorageConfigDrift` fires within `failAfter + 2m` (promtool test plus live k3d).
- **14-AC-13** Outage drill (k3d + emulator): stopping the emulator for 20 minutes fires `SCObjectStorageCanaryFailing`. CNPG keeps accepting writes (marker insert succeeds), `pg_stat_archiver.failed_count` increases, and after the emulator is restarted `last_archived_wal` advances within 10 minutes without operator action.
- **14-AC-14** OC-15 consumer smoke passes on the live installation: `barman-cloud-check-wal-archive` equivalent (`kubectl cnpg status` shows working archiving for all four clusters), Longhorn `BackupTarget default` `status.available=true`, an etcd snapshot appears in B6 within 120 s of `k3s etcd-snapshot save`, and Forgejo uploads an attachment to B7.
- **14-AC-15** Alerts `SCObjectStorageCanaryFailing`, `SCObjectStorageConfigDrift` and `SCObjectStorageProtectionWeak` exist in `contracts/alert-catalog.yaml` with `signalOwnerPlan: "14"` and runbook `procedures/object-storage-outage.md`.
- **14-AC-16** OC-17 (STO-12) in `make test-integration` against the emulator and on the reference provider at P01: the barman-cloud, Longhorn-backupstore and k3s etcd-s3 client probes each upload into a lock-enabled bucket and `objstore-capabilities.json` records their client versions. `make test-s3-emulator` records whether SeaweedFS rejects a lock-bucket PUT without a checksum; if it does not, the report states that only the provider run proves it.
- **14-AC-17** OA-09 (STO-11, k3d + emulator): deleting 150 objects under `evidence/results/` with the admin key makes the next `objstore-audit` fail check OA-09.

## 24. Explicit non-goals

- Running object storage inside the cluster (it would make the cluster its own backup failure domain, SPEC §29).
- Cross-provider replication of buckets in MVP (the operator may add provider-side replication; Strange Company does not depend on it).
- Provider migration tooling.
- Client-side encryption of CNPG, Longhorn or etcd backups (SI-G3-03).
- Cloudflare R2 support while R2 lacks S3 object versioning.
- Bucket creation through OpenTofu for B2 (existing mode only).

## 25. Open questions

- **Q-14-1** (blocking: no) `plans/dag.yaml` lists plan 14 only on P01. Proposed: add `"14"` to P10 (live audit and canary) during consolidation. Owner: master plan.
- **Q-14-2** (blocking: no) `contracts/secret-classes.yaml` (plan 35) already mirrors most of §10, including the four `pg-backups-<cluster>` instances and `tofu-state-ro`; it still lacks `dr-tests-results`, `objstore-audit`, `telemetry-debug` and `drill-reader`, and calls `admin` `bootstrap-admin` (G1 adopts `admin`). Seed instances use the register's names `seed-write`/`seed-read`. Owner: plan 35.
- **Q-14-3** (blocking: no) The toolbox must contain AWS CLI v2 (`aws`) for procedures and gates. Proposed: plan 02 adds it, checksum-pinned. Owner: plan 02.
- **Q-14-4** (resolved) `contracts/object-storage-layout.schema.yaml` now defines `infra/objstore/buckets.yaml`.
- **Q-14-5** (blocking: no) Should the operator be required to use a different provider account (or project) for object storage than for compute? Proposed: recommended, not required; preflight warns (`PF-12` warning) when `S3_PROVIDER=ovh` and the S3 users live in the same Public Cloud project as the nodes. Owner: 14 / 04.
- **Q-14-6** (resolved by CD-12) Every plan uses the SeaweedFS `s3-emulator` (ADR-0205); plans 06, 16, 49, 50 and 52 conform.
- **Q-14-7** (blocking: no) New `bootstrap.env` keys (`S3_PROVIDER`, `S3_CREDENTIAL_MODE`, `S3_OBJECT_LOCK`, `S3_ALLOW_WEAK_PROTECTION`, `S3_CRED_<INSTANCE>_*`) and new orchestrator steps (`plan.storage-credentials`, `plan.objstore-capabilities`), plus a post-install subcommand `./bootstrap storage apply` that re-runs the storage root module and the capability suite (used by `object-storage-outage.md` §Drift and §Bucket lost). Proposed: plan 50 adds them to `bootstrap-config.schema.yaml` and the pipeline. Owner: plan 50.
- **Q-14-8** (blocking: no) Plan 06's storage guard allow-list must include `aws_s3_bucket_object_lock_configuration`, `aws_s3_bucket_server_side_encryption_configuration`, `aws_iam_user` and `aws_iam_user_policy` (AWS only). Owner: plan 06.
- **Q-14-10** (blocking: no) SPEC §29/§114 with single-key providers (STO-2, BOOT-3): the cluster cannot avoid holding the account key in `shared` mode, so 14-R-2a requires a second account for B1/B2 and COMPLIANCE locks elsewhere. Even then a compromised cluster can purge versions older than the lock period by rewriting lifecycle. Recorded as a spec issue (false assumption extending SI-17). Owner: master plan / spec-issues.
- - **Q-14-9** (blocking: no) Plan 50 (line "tofu-state lifecycle: noncurrent ≥ 90 days") and plan 06 (≥ 365 days, ≥ 100 versions) differ. Resolution here: 365 days and newest 100 kept (the stricter value). Owner: plans 06/50 to align text.
