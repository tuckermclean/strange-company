# 12 — CloudNativePG

**Status:** Stage-0 plan (draft)
**DAG nodes:** P04 (co-owned: operator and plugin healthy in the seed, `forgejo-pg` created), P10 (co-owned: backups, PITR, restore tests), H02 (co-owned: instances 1→3)
**Related plans:** [11](11-storage.md), [14](14-object-storage.md), [15](15-authentik.md), [16](16-forgejo.md), [18](18-flux.md), [19](19-seed-handoff.md), [20](20-woodpecker.md), [26](26-agent-runner.md), [35](35-secrets.md), [36](36-network-policy.md), [38](38-prometheus.md), [42](42-alerting-notifications.md), [44](44-backup.md), [45](45-recovery.md), [47](47-ha-expansion.md), [48](48-upgrade.md), [51](51-capacity.md)
**Contracts:** `restore-test-result.schema.yaml` (produced by the PITR tests, plan 44)
**ADRs:** ADR-0006 (binding); introduces ADR-0200 (Barman Cloud CNPG-I plugin and backup generations)

## 1. Purpose

Provide PostgreSQL for Forgejo, Authentik, Woodpecker and Strange Company through CloudNativePG, with node-local PGDATA, continuous WAL archiving and scheduled base backups to S3, point-in-time recovery, a GitOps-selected recovery bootstrap for black start, and a 1→3 instance path for small-ha. [SPEC §25, §26]

## 2. Requirements derived from SPEC.md

1. PostgreSQL SHALL use CloudNativePG. [SPEC §25]
2. PGDATA SHALL normally use node-local persistent storage, never Longhorn. [SPEC §25, §27]
3. `instances: 1` at bootstrap and `instances: 3` at small-ha. Expansion increases the instance count; it does not migrate technology. [SPEC §11, §12, §25]
4. External backup to S3 with base backups, WAL archival, PITR and a repeatable restore test. Replication is not backup. [SPEC §26]
5. RPO ≤ 15 min and RTO ≤ 4 h for the Forgejo, Authentik and Strange Company databases. [SPEC §108] Woodpecker gets the same targets (master plan §8).
6. Forgejo, Strange Company and Woodpecker relational state uses CNPG. [SPEC §30, §39, ADR-0006]
7. CNPG failure and failed backups raise alerts. [SPEC §96]
8. Workers cannot reach CNPG application databases. [SPEC §88]
9. **12-R-1** One `Cluster` per state owner (ADR-0006), each with its own object-store path, backup schedule and restore test.
10. **12-R-2** Object-store backups use the **Barman Cloud CNPG-I plugin**, not the deprecated in-tree `barmanObjectStore` (ADR-0200).
11. **12-R-3** A recovered cluster never archives into the WAL path it was restored from (backup generations, §9.4).
12. **12-R-4** Removing a `Cluster` from Git never deletes the database (Flux prune disabled on `Cluster` objects).
13. **12-R-5** Application credentials are deterministic across `initdb` and `recovery` bootstraps (SOPS-held `SC-PG-APP`).

## 3. Inputs

- `cluster-vars` (plan 18): `CNPG_INSTANCES`, `STORAGE_PROFILE`, `S3_ENDPOINT`, `S3_REGION`, `S3_BUCKET_PREFIX` (plan 18 §9.2 names), and per cluster `CNPG_<ID>_GEN`, where `<ID>` ∈ {`FORGEJO`, `AUTHENTIK`, `WOODPECKER`, `SC`}.
- SOPS Secrets: `<cluster>-app` (`SC-PG-APP`), `<cluster>-dr-marker` (`SC-PG-DR-MARKER`), `<cluster>-backup-s3` (`SC-OBJECT-STORAGE/pg-backups-<cluster>`, plan 14 §10).
- Bucket `<prefix>-pg-backups` with versioning and lifecycle (plan 14).
- cert-manager, which the plugin requires (seed set, plan 10).

## 4. Outputs

- Services per cluster: `<cluster>-rw`, `<cluster>-ro`, `<cluster>-r` on port 5432 in the owning namespace.
- Base backups and WAL under `s3://<prefix>-pg-backups/<cluster>/<cluster>-g<N>/`.
- Metrics through PodMonitors, and alerts (§21).
- The recovery overlay (§9.5) consumed by plan 45.

## 5. Dependencies

- **Hard:** P03 (k3s, `local-path`), P01 (bucket and credentials), cert-manager in the seed (plan 10 ordering inside the seed, plan 19).
- **Soft:** plan 36 (NetworkPolicies for `*-rw` access), plan 51 (memory totals and PriorityClasses), plan 38 (Prometheus Operator CRDs for PodMonitor). PodMonitors are applied in a layer that depends on the monitoring CRDs, so the seed does not need them.
- **Runtime:** object storage for archiving. PostgreSQL keeps serving during an outage while WAL accumulates (§15).

## 6. Owned state

| State | Location | Writers |
|---|---|---|
| PGDATA of `forgejo-pg`, `authentik-pg`, `woodpecker-pg`, `sc-pg` (register S-03, S-10, S-12…S-14, S-17) | `local-path` PVCs created by CNPG | the respective app; `restore-tester` (only `sc_dr` database) |
| Base backups and WAL archives | S3 `pg-backups/<cluster>/<cluster>-g<N>/{base,wals}/` | plugin sidecar with the per-cluster credential |
| CNPG-managed certificates (S-16) | Secrets `<cluster>-ca`, `<cluster>-server`, `<cluster>-replication` | operator (reconstructible) |
| `Backup` CRs | etcd | operator (records only; S3 is authoritative) |

Plan 12 owns the database *container*. Schema and content belong to the apps (plans 15, 16, 20, 26/32).

## 7. External interfaces

- **Connection contract for apps:** host `<cluster>-rw.<ns>.svc.cluster.local:5432`. Database and owner as in §9.2. Credentials come from Secret `<cluster>-app` (type `kubernetes.io/basic-auth`, keys `username` and `password`). Clients use `sslmode=require` at minimum. Go services (Runner, Gateway) use `sslmode=verify-full` with root CA from Secret `<cluster>-ca` key `ca.crt`, mounted read-only.
- **Read-only role for Grafana** on `sc-pg`: managed role `grafana_ro` (password in `sc-pg-grafana-ro`, a `SC-PG-APP` sub-entry). Plans 32 and 39 define its grants.
- **Read-only role `forgejo_ro`** on `forgejo-pg` (requested by plans 16 and 44): declared in `forgejo-pg`'s `spec.managed.roles` as `{name: forgejo_ro, login: true, inRoles: [pg_read_all_data], passwordSecret: {name: forgejo-pg-ro}, connectionLimit: 5}` (no write, no DDL, no superuser; `pg_read_all_data` is a PostgreSQL ≥ 14 predefined role, verify on the pinned image). The password is in SOPS Secret `forgejo/forgejo-pg-ro` (an `SC-PG-APP` instance, registered by plan 35). Consumers: the bundle rehydrate Job (plan 44 §9.2.6) and the restore tests' repository queries. Because roles live in the database, a restored or scratch copy of `forgejo-pg` contains the same role and password.
- **Restore-test interface (plan 44):** role `sc_dr` with login, owner of database `sc_dr`, and `EXECUTE ON pg_switch_wal()`. No other grants.
- **Operator plugin:** `kubectl cnpg` (the toolbox must include the CNPG kubectl plugin at the operator's version; this is a plan 02 requirement).

## 8. Internal interfaces

- CNPG operator ↔ barman-cloud plugin over CNPG-I (gRPC with mTLS issued by cert-manager), both in `cnpg-system`.
- Plugin sidecar (injected into each instance pod) ↔ S3.
- Prometheus ↔ instance pods on port 9187 (`metrics`).

## 9. Configuration

### 9.1 Operator and plugin install (seed set)

| Item | Decision |
|---|---|
| Operator | Upstream CNPG Helm chart `cloudnative-pg` (repo `https://cloudnative-pg.github.io/charts`) as a Flux `HelmRelease` `cnpg` in `cnpg-system`. **Baseline:** CNPG operator v1.30.x, chart `cloudnative-pg` 0.29.x (`plans/compatibility-baseline.yaml`, CD-29); a newer minor (e.g. 1.31, which may remove in-tree Barman Cloud) is a plan-amendment task. CNPG 1.30 supports Kubernetes 1.34–1.36 (the k3s baseline is 1.36) and PostgreSQL 14–18. |
| Plugin | Barman Cloud plugin chart `plugin-barman-cloud` (same repo), `HelmRelease` `barman-cloud` in `cnpg-system`, depending on `cnpg` and `cert-manager`. **Baseline:** Barman Cloud plugin v0.15.x, chart `plugin-barman-cloud` 0.8.x (`plans/compatibility-baseline.yaml`). |
| PostgreSQL | **Baseline:** PostgreSQL 18.x (image line chosen per plan 48 §9.1; the newest major CNPG 1.30 supports). Version-sensitive here and re-verified by 12-AC-12 at the baseline: `wal_compression: lz4` in the image build, `walSegmentSize: 4`, `pg_read_all_data` (role `forgejo_ro`), `pg_stat_archiver` readable by `sc_dr`. |
| Operator values | `replicaCount: 1` (2 at small-ha); `config.data.INHERITED_LABELS: strange.company/state-id`; `monitoring.podMonitorEnabled: false` (PodMonitors are applied separately after the monitoring CRDs exist); resources requests 50m/128Mi, limit 512Mi; `priorityClassName: platform-critical`. |
| Flux | Operator CRDs installed by the chart (`crds.create: true`), `install.crds: CreateReplace`, `upgrade.crds: CreateReplace`. |

### 9.2 Clusters

| Cluster | Namespace | Database / owner | Consumers |
|---|---|---|---|
| `forgejo-pg` | forgejo | `forgejo` / `forgejo` | Forgejo |
| `authentik-pg` | authentik | `authentik` / `authentik` | Authentik server and worker |
| `woodpecker-pg` | woodpecker | `woodpecker` / `woodpecker` | Woodpecker server |
| `sc-pg` | strange-company | `strange_company` / `sc_app` | Agent Runner, Model Gateway (role separation inside the database is plan 26/53's concern; additional roles are declared as CNPG `managed.roles`) |

### 9.3 Sizing (plan 51 holds the totals)

`STORAGE_PROFILE=preferred` (12 GiB node) / `constrained` (8 GiB node):

| Parameter | forgejo-pg | authentik-pg | woodpecker-pg | sc-pg |
|---|---|---|---|---|
| `resources.requests.memory` | 256Mi / 192Mi | 256Mi / 192Mi | 192Mi / 128Mi | 384Mi / 256Mi |
| `resources.limits.memory` | 512Mi / 384Mi | 512Mi / 384Mi | 384Mi / 256Mi | 768Mi / 512Mi |
| `resources.requests.cpu` (no CPU limit) | 100m | 100m | 50m | 100m |
| `shared_buffers` | 64MB / 32MB | 64MB / 32MB | 48MB / 24MB | 128MB / 64MB |
| `effective_cache_size` | 256MB / 128MB | 256MB / 128MB | 192MB / 96MB | 512MB / 256MB |
| `max_connections` | 60 | 80 | 40 | 60 |
| `work_mem` | 4MB | 4MB | 4MB | 8MB / 4MB |
| `storage.size` (nominal, local-path) | 6Gi / 4Gi | 3Gi / 2Gi | 8Gi / 4Gi | 10Gi / 6Gi |

`woodpecker-pg` stays at 8Gi / 4Gi because Woodpecker log retention is 30 days (plan 43 R-19: ≈ 30 pipelines/day × ≤ 3 MiB × 30 d ≈ 2.7 GiB of logs). Plan 20 asked for 12Gi if it kept 90-day log retention; it must set `retention.logDays: 30` instead. `sc-pg` includes ledger growth (plan 32) with reservations pruned after 30 days and denials after 90 days (plan 43 R-05).

Common parameters: `maintenance_work_mem: 32MB`, `wal_buffers: 4MB`, `max_wal_size: 512MB`, `min_wal_size: 64MB`, `checkpoint_timeout: 10min`, `archive_timeout: 10min`, `wal_compression: lz4` (verify that the image's PG build supports it; otherwise `on`), `max_slot_wal_keep_size: 2GB`, `log_min_duration_statement: 2000`, `idle_in_transaction_session_timeout: 10min`, `huge_pages: off`.

**Plugin sidecar** (`ObjectStore.spec.instanceSidecarConfiguration.resources`, verify field): requests 20m/64Mi, limit 256Mi.

**Per-instance memory envelope** (preferred): ≈ 2.2 GiB of limits for four clusters and ≈ 1.1 GiB of requests. At small-ha each node hosts one instance of each cluster, so the per-node envelope stays the same.

**`walSegmentSize: 4`** (MB, set at `initdb`). Rationale: with `archive_timeout` forcing switches on lightly loaded clusters, 16 MB segments create up to 4 × 144 × 16 MB ≈ 9 GiB/day of WAL files on the root disk while object storage is down. 4 MB segments cut that to ≈ 2.3 GiB/day, which gives roughly four days of outage tolerance inside the §12 budget of plan 11. Restored clusters inherit the segment size from the backup. 12-AC-12 re-verifies that barman-cloud handles 4 MB segments. The `dr-marker-heartbeat` writes (plan 44 RT-0, every 5 min) make every cluster non-idle, so the forced-switch estimate is the actual expected volume, not only an upper bound.

### 9.4 Backup configuration and generations (ADR-0200)

One `ObjectStore` per cluster, in the cluster's namespace:

```yaml
apiVersion: barmancloud.cnpg.io/v1          # verify at implementation
kind: ObjectStore
metadata:
  name: forgejo-pg-store
  namespace: forgejo
spec:
  retentionPolicy: "30d"
  configuration:
    destinationPath: s3://${S3_BUCKET_PREFIX}-pg-backups/forgejo-pg/
    endpointURL: ${S3_ENDPOINT}
    s3Credentials:
      accessKeyId:     {name: forgejo-pg-backup-s3, key: ACCESS_KEY_ID}
      secretAccessKey: {name: forgejo-pg-backup-s3, key: ACCESS_SECRET_KEY}
      region:          {name: forgejo-pg-backup-s3, key: REGION}
    wal:  {compression: gzip, maxParallel: 2}
    data: {compression: gzip, jobs: 1}
  instanceSidecarConfiguration:
    resources: {requests: {cpu: 20m, memory: 64Mi}, limits: {memory: 256Mi}}
```

The `Cluster` enables the plugin as WAL archiver with an explicit **generation-qualified server name**:

```yaml
spec:
  plugins:
    - name: barman-cloud.cloudnative-pg.io
      isWALArchiver: true
      parameters:
        barmanObjectName: forgejo-pg-store
        serverName: forgejo-pg-g${CNPG_FORGEJO_GEN}
```

- **Generation `N`** starts at `1` at fresh install. Every recovery (black start, PITR cut-over) restores **from** `g<N>` and archives **into** `g<N+1>`. The recovery commit increments `CNPG_<ID>_GEN`. CNPG/barman refuse to archive into a non-empty WAL path (verify), so reusing `g<N>` would stall archiving. Generations also keep the pre-recovery timeline intact for rollback.
- **Scheduled base backups** (`ScheduledBackup`, 6-field cron with seconds, UTC, `method: plugin`, `pluginConfiguration.name: barman-cloud.cloudnative-pg.io`, `backupOwnerReference: self`, `immediate: true`):

| Cluster | Schedule | Rationale |
|---|---|---|
| `forgejo-pg` | `0 10 1 * * *` | staggered 20 min apart to avoid concurrent I/O and egress |
| `authentik-pg` | `0 30 1 * * *` | |
| `woodpecker-pg` | `0 50 1 * * *` | |
| `sc-pg` | `0 10 2 * * *` | |

  `immediate: true` takes the first base backup as soon as the cluster exists, so PITR is possible from minute one (12-AC-5).
- **Retention:** `retentionPolicy: "30d"` (recovery window). The plugin deletes expired base backups and WAL. On the versioned bucket those deletes create delete markers, and the bucket's noncurrent-version expiry (14 d, plan 14) finally removes the bytes. The cluster credential cannot delete object versions (plan 14), so a compromised cluster cannot destroy backup history younger than 14 days past its logical expiry.
- **Encryption:** TLS in transit plus provider-side SSE (plan 14). No client-side encryption: barman-cloud does not offer it, and adding custom encryption would break the standard restore path (recorded as spec issue SI-G3-03).
- **WAL archiving RPO:** `archive_timeout: 10min` and `maxParallel: 2`. Worst case for an idle-then-written database is ≈ 10 min plus archive latency (< 1 min nominal), which is ≤ 15 min [SPEC §108]. Measured by `rt-cnpg-pitr` (`measurements.rpo_observed_seconds`) and continuously by `SCCnpgWalArchiveFailing`.

### 9.5 Bootstrap mode and the recovery overlay

Each cluster's GitOps directory (plan 18 layout: the content of its db layer `forgejo-db`, `authentik-db`, `woodpecker-db` or `sc-db`, e.g. `gitops/platform/forgejo-db/`; verify the exact directory name against plan 18 §8.2) contains `cluster.yaml` (common spec) and exactly one bootstrap component referenced from its `kustomization.yaml`:

- `components/cnpg-bootstrap-initdb` (normal install):
  ```yaml
  bootstrap:
    initdb:
      database: forgejo
      owner: forgejo
      secret: {name: forgejo-pg-app}
      walSegmentSize: 4
      postInitSQL:
        - CREATE ROLE sc_dr LOGIN
        - CREATE DATABASE sc_dr OWNER sc_dr
        - GRANT EXECUTE ON FUNCTION pg_switch_wal() TO sc_dr
  ```
- `components/cnpg-bootstrap-recovery` (black start or PITR re-creation):
  ```yaml
  bootstrap:
    recovery:
      source: origin
      database: forgejo
      owner: forgejo
      secret: {name: forgejo-pg-app}     # same SOPS secret → same password (12-R-5)
      recoveryTarget:
        targetTime: "${CNPG_FORGEJO_TARGET_TIME}"  # omitted entirely for "latest"
  externalClusters:
    - name: origin
      plugin:
        name: barman-cloud.cloudnative-pg.io
        parameters:
          barmanObjectName: forgejo-pg-store
          serverName: forgejo-pg-g${CNPG_FORGEJO_SOURCE_GEN}
  ```
  `CNPG_<ID>_SOURCE_GEN = N` and `CNPG_<ID>_GEN = N+1`. For "latest", the component variant `cnpg-bootstrap-recovery-latest` omits `recoveryTarget`, because Flux substitution cannot remove keys.
- **Selection (black start):** `./bootstrap recover` (plan 45/50) does **not** edit files under `gitops/platform/` (plan 50 commit-scope rule). It adds **per-cluster** components owned by plan 45 (ADR-0554 as revised) to `gitops/clusters/<cluster>/kustomization.yaml` and `seed/kustomization.yaml`: one of `gitops/components/cnpg-recovery-latest-<id>` or `cnpg-recovery-target-<id>` for each `<id>` ∈ {forgejo, authentik, woodpecker, sc}. Each sets Flux `spec.patches` (JSON 6902) on **one** db layer (`forgejo-db`, `authentik-db`, `woodpecker-db` or `sc-db`): `replace /spec/bootstrap` with the stanza of `cnpg-bootstrap-recovery-latest` (`-latest-` variant) or `cnpg-bootstrap-recovery` with `recoveryTarget.targetTime: $${CNPG_<ID>_TARGET_TIME}` (`-target-` variant; the variant is chosen by which component is listed, because substitution cannot add keys, DR-6a), and `add /spec/externalClusters` with `serverName: <cluster>-g$${CNPG_<ID>_SOURCE_GEN}`. The recovery commit also sets `CNPG_<ID>_SOURCE_GEN = N` and `CNPG_<ID>_GEN = N+1` in `cluster-vars`, which moves archiving to `<cluster>-g<N+1>` through the unchanged §9.4 `serverName`. The two platform-level components above remain the single source of the stanza content.
- **Selection (single-cluster PITR or replace):** `restore-cnpg.md` sets `CNPG_<ID>_SOURCE_GEN`/`CNPG_<ID>_GEN` (and `CNPG_<ID>_TARGET_TIME` for PITR) and selects the recovery stanza for **that one cluster only**: if its per-cluster component `cnpg-recovery-{latest,target}-<id>` is listed in the cluster kustomizations (left by an earlier black start), the change replaces it with `cnpg-recovery-target-<id>` (PITR) or `cnpg-recovery-latest-<id>` (replace); otherwise it swaps the platform-level component of that cluster (`cnpg-bootstrap-recovery` or `-latest`). The other clusters' components are not touched. CNPG ignores `bootstrap` on running clusters, so either form is safe for them.
  **Merge path (DR-3, STO-10):** the merge gate (ADR-0019: CI status, Runner `tdd-provenance` and `review` statuses, human approval, no admin override) depends on `woodpecker-pg` (CI), `sc-pg` (Runner) and `forgejo-pg` (Forgejo), and on `authentik-pg` for SSO approvers. Restoring any of these four therefore uses **direct apply**: the rendered layer is applied with the Flux field manager while the layer Kustomization stays suspended, and the PR is merged afterwards, once the service is back (a follow-up PR that the ratification path of `merge-without-provenance.md`, plan 55, covers if a status cannot be produced). For `authentik-pg` the approver is a Forgejo local account or `SC-BREAK-GLASS-FORGEJO`. PITR on a cluster whose service is still running requires every required status to be `success` on the PR **before** writers are stopped.
  **Source generation (DR-6b):** for a target time T, `SOURCE_GEN` is the generation whose recovery window contains T (`aws s3 ls …/<cluster>/` lists `<cluster>-g<N>/`; the window starts at that generation's first base backup `backup.info` `begin_time`), not necessarily the newest.
- **After recovery** the per-cluster `cnpg-recovery-*-<id>` components (and any platform-level recovery component) stays in Git, together with the new generations (ADR-0554 revised: the finalize PR removes only `recovery-mode` and `longhorn-volume-recovery`). CNPG ignores `bootstrap` after initialization (12-AC-13 verifies that an update to `bootstrap` on a running cluster is accepted and is a no-op). It is replaced only by the next recovery commit.
- **Creation guard (DR-7, plan 37 ADM-63 `sc-cnpg-cluster-create-guard`):** keeping a recovery stanza in Git is safe only if a deleted production `Cluster` is never re-created automatically: Flux would restore it from the *old* `SOURCE_GEN` and archive into the live `g<N+1>`. The VAP denies `CREATE` of a `postgresql.cnpg.io/Cluster` in `forgejo`, `authentik`, `woodpecker` and `strange-company` unless the ConfigMap `flux-system/sc-cnpg-create-window` exists with `mode: initial-install` (fresh install, removed when control transfer completes, plan 50) or `mode: recovery` and `runId` equal to the object's annotation `strange.company/recovery-run`. Therefore every recovery stanza carries that annotation: the per-cluster components `cnpg-recovery-{latest,target}-<id>` (plan 45) and the platform-level `cnpg-bootstrap-recovery` / `-latest` components (this plan) add `metadata.annotations["strange.company/recovery-run"]: $${SC_RECOVERY_RUN_ID}` to the `Cluster`, with `SC_RECOVERY_RUN_ID` written to `cluster-vars` by the recovery commit (plan 45) or by `restore-cnpg.md` (`restore-<cluster>-<UTC timestamp>`); the matching window ConfigMap is rendered by the recovery commit, or created imperatively by `restore-cnpg.md` and deleted after the cluster is Ready. In steady state the window is absent, so an accidental deletion stalls visibly (`SCCnpgClusterDown`) until the operator runs `restore-cnpg.md` §Replace. Recovery-component patches that reference layer variables are written `$${VAR}` so that the root Kustomization does not substitute them (SEED-4; F11 renders a recovery fixture, plan 18).
- **Prune protection:** `Cluster` and `ObjectStore` objects carry `kustomize.toolkit.fluxcd.io/prune: disabled`.
- **Emergency WAL-archiver detach:** component `components/cnpg-wal-archiver-detached` sets `isWALArchiver: false` on the cluster's plugin entry. It is used only by `object-storage-outage.md` §Outage step 4b when a long object-storage outage threatens the root disk, and is removed by reverting the PR. While it is active the cluster has a PITR gap that ends with the first base backup after re-attachment (verify that PostgreSQL then recycles `.ready` WAL).

### 9.6 TLS and authentication

- CNPG-managed CA and server certificates (reconstructible; S-16).
- `postgresql.pg_hba`: `hostnossl all all all reject` as the first user rule. All TCP clients must use TLS. CNPG's own replication rules (`hostssl … cert`) are unaffected.
- `enableSuperuserAccess: false` on all production clusters. Superuser work uses `kubectl cnpg psql` (local socket inside the pod) under break-glass cluster admin.
- Managed roles (`spec.managed.roles`): `sc_dr` (password from `<cluster>-dr-marker`, `login: true`, no other attributes) on every cluster; `forgejo_ro` on `forgejo-pg` (§7); `grafana_ro` on `sc-pg`. Additional app roles per owning plan.

### 9.7 High availability, failover and disruption

| Setting | instances = 1 | instances = 3 |
|---|---|---|
| `affinity.enablePodAntiAffinity` / `podAntiAffinityType` | true / preferred | true / **required** (`topologyKey: kubernetes.io/hostname`) |
| `enablePDB` (verify field) | **false**, so `kubectl drain` is not blocked; maintenance per plan 08 reboot procedure | true (CNPG default PDBs: primary `minAvailable 1`, replicas `maxUnavailable 1`) |
| `nodeMaintenanceWindow` | `inProgress: false, reusePVC: true`; plan 08 sets `inProgress: true` before draining and back after | same |
| `postgresql.synchronous` | none | `method: any`, `number: 1`, `dataDurability: preferred` (verify, CNPG ≥ 1.25): commits are acknowledged by one standby while one is available, and degrade to async otherwise |
| `primaryUpdateStrategy` / `primaryUpdateMethod` | unsupervised / restart | unsupervised / switchover |
| `failoverDelay` | 0 | 0 |
| `priorityClassName` | `platform-critical` | same |

`CNPG_ENABLE_PDB` (`false`/`true`), `CNPG_ANTI_AFFINITY` (`preferred`/`required`) and `CNPG_SYNC_NUMBER` (`0`/`1`) are `cluster-vars` set by the profile. The synchronous block is emitted by the component `cnpg-ha`, which the small-ha overlay adds.

**Failover behavior (instances = 3):** if the primary pod or node fails, the operator promotes the most advanced replica after its readiness checks fail. Expected time to new primary is under 60 s (measured at H04). The `-rw` Service follows the new primary, and clients reconnect. A returning former primary is resynchronized with `pg_rewind`. A permanently lost node leaves its instance Pending on a node-bound PVC, which needs operator action (`cnpg-replica-loss.md`).

**Node-local PV implications:** a node loss loses the PGDATA replica on that node. Recovery is (a) at instances = 3, destroy the dead instance (`kubectl cnpg destroy <cluster> <instance>`), after which CNPG re-clones a new instance from the primary with `pg_basebackup` onto a surviving or replacement node; (b) at instances = 1, restore from the object store (`restore-cnpg.md` in "replace" mode, or black start).

### 9.8 Flux layering and ordering

- Seed layer `cnpg` (operator, plugin) → layer `forgejo-db` (`forgejo-pg` `Cluster`, `ObjectStore`, `ScheduledBackup`; plan 18 §8.3 row 11, SEED-5) → `forgejo-upgrade-gate` (row 11a, plan 48) → layer `forgejo`. Other clusters arrive in their own `authentik-db`, `woodpecker-db` and `sc-db` layers after control transfer. Namespaces and NetworkPolicies come from the `namespaces` and `policies` layers, never from a db layer. The `backups` layer (row 20) depends on all four db layers.
- Each `Cluster` has a Flux health check (`Ready` condition). The app `HelmRelease` `dependsOn` the Kustomization that contains its `Cluster`.

## 10. Secrets

| Class | Object | Notes |
|---|---|---|
| `SC-PG-APP` | `<cluster>-app` (basic-auth) per cluster; `sc-pg-grafana-ro` | generated once at bootstrap (32-byte random), SOPS-encrypted in Git, two recipients (ADR-0012). Rotation: plan 35 procedure (update the Secret; CNPG reconciles the role password for `initdb.secret` owners and managed roles, verify; then restart apps). |
| `SC-PG-APP` instance `forgejo-pg-ro` | `forgejo/forgejo-pg-ro` (basic-auth, SOPS, both recipients) | role `forgejo_ro`; generated once at bootstrap like the app secret; rotation as for `SC-PG-APP` |
| `SC-PG-DR-MARKER` (new) | `<cluster>-dr-marker` in the cluster namespace **and** an identical copy in `dr-tests` | used only by `restore-tester` |
| `SC-OBJECT-STORAGE/pg-backups-<cluster>` (plan 14 §10) | `<cluster>-backup-s3` keys `ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`, `REGION` | prefix-scoped to `pg-backups/<cluster>/` where the provider supports it |
| `SC-OBJECT-STORAGE/dr-tests-reader` (plan 14 §10) | `dr-tests/s3-dr-tests-reader` | read-only on `pg-backups` (and the other backup buckets) |

## 11. Storage

PGDATA on `local-path` (plan 11 §9). `pg_wal` lives inside PGDATA: no separate `walStorage`, because local-path cannot isolate capacity, so a separate volume would only add bookkeeping. Nominal sizes are in §9.3. Database growth is watched by `SCCnpgDatabaseLarge`.

## 12. Resource requirements

| Component | self-hosting-minimum (preferred) | small-ha (per node) |
|---|---|---|
| CNPG operator | 50m / 128Mi req, 512Mi limit | same, 2 replicas cluster-wide |
| Barman Cloud plugin | 20m / 64Mi req, 256Mi limit | same, 2 replicas |
| Four instances + sidecars | ≈ 1.35 GiB req, ≈ 3.2 GiB limit | same per node |
| Restore-test scratch cluster (transient, one at a time) | 100m / 256Mi req, 512Mi limit | same |

## 13. Security boundary

- Namespaces are PSA `restricted`. CNPG instance pods comply (non-root, no privilege escalation, RuntimeDefault seccomp).
- NetworkPolicy (plan 36): ingress to instance pods on 5432 only from the owning app's pods, the operator (`cnpg-system`, ports 8000/5432), other instances of the same cluster, and `dr-tests` pods labeled `app.kubernetes.io/name=restore-tester` (`*-rw` only, port 5432). Prometheus is allowed on 9187. Egress from instances: S3 endpoint (443), DNS, the API server, and peers. **No path from `agent-jobs`, `ci-jobs` or `hermes`** [SPEC §88].
- The `sc_dr` role can only use its own database. `pg_hba` requires TLS.
- Backup credentials cannot delete object versions or change bucket configuration (plan 14).

## 14. Happy path

1. P04: the seed installs `cnpg` and `barman-cloud`; both report Ready. `forgejo-pg` initdb creates database `forgejo`, role `sc_dr` and database `sc_dr`. The `ObjectStore` starts archiving, and the immediate base backup completes.
2. After control transfer: `authentik-pg`, `woodpecker-pg` and `sc-pg` follow the same pattern in their own layers `authentik-db`, `woodpecker-db` and `sc-db` (plan 18 §8.3 rows 14, 16, 22); each is followed by its upgrade-gate layer (`*-upgrade-gate`, plan 48) and then the application layer.
3. Steady state: WAL archived continuously, daily base backups, plugin-enforced 30 d retention, weekly PITR restore test per cluster (plan 44).
4. H02: `CNPG_INSTANCES=3` and the `cnpg-ha` component bring two replicas per cluster, one per node, and synchronous `any 1`.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Primary pod crash (any profile) | `SCCnpgInstanceNotReady` | brief outage (1 instance) or failover (3) | kubelet restart; CNPG failover at 3 | — (self-heals) / `cnpg-primary-loss.md` if not |
| Primary node lost, instances = 3 | `SCCnpgClusterDown`, `SCNodeDown` | failover in < 60 s; one instance Pending | promote replica | `cnpg-primary-loss.md` |
| Primary node/disk lost, instances = 1 | `SCCnpgClusterDown` | database down | none | `cnpg-primary-loss.md` → `restore-cnpg.md` (replace mode) |
| Replica lost | `SCCnpgClusterDegraded` | reduced redundancy; sync degrades to async | none | `cnpg-replica-loss.md` |
| WAL archiving failing (S3 outage, credential) | `SCCnpgWalArchiveFailing` (critical) | RPO grows; `pg_wal` grows on root disk | plugin retries every archive cycle | `object-storage-outage.md` |
| WAL backlog large | `SCCnpgWalReadyBacklogHigh` (≥ 256 files ≈ 1 GiB), `…Critical` (≥ 1024 files ≈ 4 GiB) | root disk pressure | none | `object-storage-outage.md` (emergency step: detach the WAL archiver, then take a fresh base backup) |
| Base backup failing | `SCCnpgBackupFailed`, `SCCnpgBaseBackupStale` | longer replay; retention window drift | next schedule retries | `restore-test-failure.md` §backup |
| Logical corruption or bad migration | human / app errors | data wrong | none | `restore-cnpg.md` (PITR) |
| Archive into non-empty path after recovery (generation not bumped) | `SCCnpgWalArchiveFailing` immediately after recovery | no new backups | none | `restore-cnpg.md` step "bump generation" |
| Recovery window shrinks unexpectedly | `SCCnpgRecoveryWindowShort` | PITR horizon < 7 d | none | investigate bucket lifecycle and credential misuse (plan 14) |
| Database grows past nominal size | `SCCnpgDatabaseLarge` | root-disk budget at risk | none | resize per plan 51 / retention review (plan 43) |
| Operator or plugin down | `SCCnpgOperatorDown`, `SCCnpgPluginDown` | no reconciliation; running DBs unaffected; archiving stops if the plugin is down | Deployment restart | `cnpg-primary-loss.md` §operator |

## 16. Retry behavior

- WAL archive: PostgreSQL retries `archive_command` indefinitely, keeping `.ready` files. The plugin parallelizes (2). No data is dropped.
- `ScheduledBackup`: a failed `Backup` is not retried until the next schedule. `SCCnpgBackupFailed` fires immediately. The operator may create an on-demand `Backup` (procedure).
- The PITR restore test retries nothing: one failure equals one failed result (plan 44).

## 17. Idempotency behavior

- `Cluster`, `ObjectStore` and `ScheduledBackup` are declarative and server-side applied by Flux.
- Bootstrap sections run once per cluster lifetime.
- Generation numbers make re-running a recovery commit safe. A recovery into `g<N+1>` that fails before archiving can be retried after deleting the failed `Cluster` and its PVCs (the procedure checks that the `g<N+1>` path is empty first).
- `postInitSQL` runs only at initdb.

## 18. Bootstrap behavior

- Seed: `cnpg` → `barman-cloud` (needs cert-manager Ready) → `forgejo-pg` (initdb) → Forgejo. The Forgejo HelmRelease waits for `forgejo-pg` `Ready`.
- Black start (plan 45): same order, with the per-cluster `cnpg-recovery-latest-<id>` components (or `-target-<id>` with `--pitr-target`), applied by Flux `spec.patches` and the generation bump committed by `./bootstrap recover`. `forgejo-pg` must reach Ready from recovery before Forgejo starts. RTO accounting: recovery time ≈ base-backup download + WAL replay. The weekly restore test measures it (`measurements.restore_duration_seconds`).

## 19. Upgrade behavior

- **Operator:** Renovate PR bumps the chart; one minor at a time; read the release notes for deprecations (in-tree Barman removal, `enablePodMonitor` deprecation, PDB fields). The operator upgrade triggers a rolling update of instances (switchover at 3 instances, restart at 1, so expect ≈ 30–60 s of DB unavailability at 1 instance).
- **Plugin:** upgraded independently. 12-AC-12 and the PITR restore test re-verify backup and restore after each plugin bump (plan 48 gating).
- **PostgreSQL minor:** `imageName` digest bump, rolling.
- **PostgreSQL major:** CNPG offline in-place major upgrade (`imageName` major change, CNPG ≥ 1.26, verify) **only after** a fresh base backup and a passed restore test. Fallback is a new cluster bootstrapped with `initdb.import` from the old one. Plan 48 owns the procedure.
- **Plugin/in-tree migration** is not applicable: in-tree `barmanObjectStore` is never used.

## 20. Recovery behavior

- **PITR (logical error):** side-by-side verification, then in-place re-creation by name (`restore-cnpg.md`):
  1. Take an on-demand `Backup` of the current (bad) state so it remains recoverable.
  2. Create a verification cluster `<cluster>-pitr` in the same namespace from `g<N>` at the chosen target, without a WAL archiver. The app owner verifies the data.
  3. Stop the app and delete the production `Cluster` (prune protection only stops Flux, not humans).
  4. Re-create it with `cnpg-bootstrap-recovery` at the verified target time, `SOURCE_GEN=N`, `GEN=N+1`.
  5. Delete `<cluster>-pitr`.

  Rationale: apps hard-code `<cluster>-rw`, so re-creation by name avoids touching app configuration. Verifying side by side first prevents a destructive replace with the wrong target.
- **Replace (disk or node loss at 1 instance):** the same steps 3–4 with "latest".
- **Black start:** plan 45, using the overlay in §9.5.
- **Replica loss:** destroy and re-clone (`cnpg-replica-loss.md`).
- **Corrupt base backup or WAL gap (DR-B8).** Detection before it matters: the light daily check `cnpg-wal-continuity` (plan 44 RT-1a) lists, for each cluster's current generation, the WAL objects from the newest completed base backup's `begin_wal` to now and verifies a contiguous segment sequence per timeline; a gap, or a recorded WAL-archiver detach window (`evidence/backups/wal-gaps/<cluster>.json`, written by `object-storage-outage.md` §Outage 4c), fails it and raises `SCCnpgWalGapDetected` (critical, failed-backup). Recovery fallback (used by `restore-cnpg.md` §Replace and by `./bootstrap recover`): (1) if the recovery stops before the target (`recovery target not reached`, or replay ends at a gap), recover from the **next older** completed base backup with `recoveryTarget.backupID: <id>` (verify the field at the baseline CNPG 1.30) and a target time **before** the gap; (2) the operator must confirm the data loss explicitly (`restore-cnpg.md` typed decision; `recover` asks for `accept-older-backup <cluster> <backup-id>`, requested from plans 45/50); (3) `recover` never reports "latest" for a cluster whose archive has a recorded detach window or a continuity failure: it reports the real end point (the last contiguous WAL) and the gap. The heartbeat rows (plan 44 RT-0) show the actual RPO afterwards.

## 21. Observability

**Metrics:** a `PodMonitor` per namespace selects `cnpg.io/podRole=instance`, port `metrics`. The operator and plugin are scraped through their own monitors. Backup status for plugin-managed backups: the plugin exposes last-backup and first-recoverability-point metrics (names to verify at implementation; fallback is kube-state-metrics CustomResourceState on `backups.postgresql.cnpg.io` `status.stoppedAt`/`status.phase`, exported as `sc_cnpg_backup_completed_timestamp{cluster}`). The alert expressions below name the in-tree collector metrics. The implementation substitutes the verified plugin equivalents, and 12-AC-11 checks every alert expression returns series.

| alertname | Severity | Expression (sketch) | For |
|---|---|---|---|
| `SCCnpgClusterDown` | critical | `sc:cnpg_ready_instances == 0 or sc:cnpg_primaries == 0` (recording rules over `cnpg_collector_up`, the KSM CRS `sc_cnpg_cluster_ready` and `cnpg_pg_replication_in_recovery`) | 3m |
| `SCCnpgWalArchiveBacklog` | critical | `max by (cluster) (cnpg_pg_stat_archiver_seconds_since_last_archival) > 900` | 2m |
| `SCCnpgInstanceNotReady` | warning | `kube_pod_status_ready{condition="true", pod=~".*-pg-[0-9]+"} == 0` | 5m |
| `SCCnpgClusterDegraded` | warning | `cnpg_collector_nodes_used < on(cluster) sc_cnpg_cluster_instances_spec` (KSM CRS) | 15m |
| `SCCnpgReplicationLagHigh` | warning | `cnpg_pg_replication_lag > 300` | 10m |
| `SCCnpgSyncReplicationDegraded` | warning | `cnpg_collector_sync_replicas{value="observed"} < cnpg_collector_sync_replicas{value="expected"}` (small-ha) | 15m |
| `SCCnpgWalArchiveFailing` | critical | `cnpg_pg_stat_archiver_seconds_since_last_archival > 1200 and cnpg_pg_stat_archiver_last_failed_time > cnpg_pg_stat_archiver_last_archived_time` | 5m |
| `SCCnpgWalReadyBacklogHigh` | warning | `cnpg_collector_pg_wal_archive_status{value="ready"} > 256` | 10m |
| `SCCnpgWalReadyBacklogCritical` | critical | `cnpg_collector_pg_wal_archive_status{value="ready"} > 1024` | 5m |
| `SCCnpgBaseBackupStale` | critical | `time() - <last_available_backup_timestamp> > 93600` (26 h) | 15m |
| `SCCnpgBackupFailed` | critical | `<last_failed_backup_timestamp> > <last_available_backup_timestamp>` | 0m |
| `SCCnpgRecoveryWindowShort` | warning | `time() - <first_recoverability_point> < 7*86400` and cluster age > 8 d | 1h |
| `SCCnpgDatabaseLarge` | warning | `sum(cnpg_pg_database_size_bytes) > 0.8 * <CNPG line of plan 11 §12 for the node's profile>` (sum over all clusters on the node, STO-3; requested change to the catalogue expression) | 1h |
| `SCCnpgOperatorDown` / `SCCnpgPluginDown` | critical | `kube_deployment_status_replicas_available{namespace="cnpg-system"} == 0` | 10m |

Every critical alert in this table routes to the human notification receiver (SPEC §97 "failed backup"). Inhibition: `SCCnpgWalArchiveFailing` and `CNPGBaseBackup*` are inhibited by `SCObjectStorageUnreachable` / `SCObjectStorageCanaryFailing` (plan 14) for the notification only; the alert stays visible (plan 42).

**Alert names, severities and `for` durations** follow `contracts/alert-catalog.yaml` (CD-01) where they differ from the sketches above; plan 42 §23.6 maps the names used in earlier drafts. 12-AC-11 checks the catalogue names.

**Grafana:** the upstream CNPG dashboard (plan 39), plus a backup panel per cluster showing last base backup, last archived WAL age, ready-WAL count and recovery window.

## 22. Testing strategy

- **F11 offline:** render all four clusters for both profiles and both bootstrap components. Assert: `storageClass: local-path`, `walSegmentSize: 4`, plugin `serverName` matches `<cluster>-g\d+`, prune-disabled annotation present, `hostnossl … reject` first in `pg_hba`, no `barmanObjectStore` key anywhere (`grep -R barmanObjectStore gitops/` returns nothing).
- **k3d + SeaweedFS (plan 14 emulator), in CI (`make e2e-cnpg`):** install operator, plugin and cert-manager. Create a 1-instance cluster. Insert marker A, record T, insert marker B. Wait for archive. Recover to T into a new cluster → A present, B absent. Recover "latest" with generation bump → both present, and archiving into `g2` succeeds. Also re-verifies 4 MB segments and the plugin's CRD fields at the pinned versions.
- **Live:** acceptance criteria below and the weekly `rt-cnpg-pitr-*` tests (plan 44).

## 23. Acceptance criteria

### GATE-P04
- **12-AC-1** `./bootstrap exec -- kubectl -n cnpg-system get deploy -o jsonpath='{range .items[*]}{.metadata.name}={.status.availableReplicas}{"\n"}{end}'` lists the CNPG operator and the barman-cloud plugin, each with `≥1`. `kubectl get crd objectstores.barmancloud.cnpg.io clusters.postgresql.cnpg.io` exits 0.
- **12-AC-2** `./bootstrap exec -- kubectl -n forgejo get cluster forgejo-pg -o jsonpath='{.status.phase}|{.spec.instances}|{.spec.storage.storageClass}'` prints `Cluster in healthy state|1|local-path`.
- **12-AC-3** `./bootstrap exec -- kubectl -n forgejo get cluster forgejo-pg -o jsonpath='{.spec.plugins[0].name}|{.spec.plugins[0].isWALArchiver}|{.spec.plugins[0].parameters.serverName}'` prints `barman-cloud.cloudnative-pg.io|true|forgejo-pg-g1`.
- **12-AC-4** A connection without TLS is rejected: from a pod in `forgejo` with the app credentials, `psql "host=forgejo-pg-rw sslmode=disable …" -c 'select 1'` fails with `pg_hba.conf rejects connection … no encryption`, and `sslmode=require` returns `1`.
- **12-AC-5** Within 30 min of creation, `kubectl -n forgejo get backup -l cnpg.io/cluster=forgejo-pg -o jsonpath='{.items[0].status.phase}'` prints `completed`, and an object listing of `s3://<prefix>-pg-backups/forgejo-pg/forgejo-pg-g1/base/` is non-empty.

### GATE-P10
- **12-AC-6** For each of the four clusters: phase healthy, an immediate base backup completed, and `kubectl cnpg status <cluster> -n <ns>` shows `Working WAL archiving: OK` with `Last Archived WAL` younger than 15 min.
- **12-AC-7** RPO probe: insert a row in `sc_dr.markers` on each cluster, wait 11 min without calling `pg_switch_wal()`, then confirm the WAL file containing the insert LSN is listed in `pg_stat_archiver.last_archived_wal` (≥ the insert segment). This passes for all four clusters (proves `archive_timeout` ≤ 15 min RPO).
- **12-AC-8** `rt-cnpg-pitr-<cluster>` has produced one `passed` result for each cluster (plan 44, 44-AC-4).
- **12-AC-9** Recovery overlay (STO-18): `make e2e-cnpg` (k3d + SeaweedFS emulator, never the live bucket) restores `forgejo-pg` with the per-cluster `cnpg-recovery-latest-forgejo` component at "latest" from `g1` into `g2`; the cluster reaches healthy and archiving into `g2` succeeds within 15 min. A second run with `g2` already non-empty makes the recovery resolver abort before creating the cluster (`CNPG_TARGET_GENERATION_NOT_EMPTY`).
- **12-AC-10** NetworkPolicy: from a test pod in `agent-jobs`, a TCP connect to `sc-pg-rw.strange-company:5432` times out. From a `restore-tester` pod in `dr-tests` it connects.
- **12-AC-11** Every alert in §21 is loaded (`kubectl get prometheusrule -A -o json | jq` lists every alertname of §21), and each expression returns at least one series for every cluster in a Prometheus query (`count by (cluster)`), which proves the metric names were verified.
- **12-AC-12** CI `make e2e-cnpg` passes at the pinned operator and plugin versions (4 MB segments, PITR to time, latest with generation bump).
- **12-AC-13** `kubectl patch cluster forgejo-pg --type merge -p '{"spec":{"bootstrap":{"initdb":{"database":"forgejo","owner":"forgejo","secret":{"name":"forgejo-pg-app"}}}}}' --dry-run=server` succeeds without changing pods (`.metadata.generation` changes, and the instance pod UIDs are unchanged after 2 min when applied for real in the CI e2e). This proves recovery components can remain in Git.
- **12-AC-14** Removing `forgejo-pg` from the Kustomization in a k3d test does not delete the `Cluster` (prune disabled): `kubectl get cluster forgejo-pg` still exits 0 after reconciliation.
- **12-AC-19** Direct-apply restore drill (STO-10, k3d `make e2e-cnpg`): with a fixture Forgejo whose branch protection requires the `strange-company/tdd-provenance` status and the Runner (`sc-pg` consumer) scaled to 0, run `restore-cnpg.md` §Replace for `sc-pg` with step 7b: the rendered `sc-db` layer is applied with field manager `kustomize-controller` while `sc-db` stays suspended; the creation window (`flux-system/sc-cnpg-create-window`, `mode: recovery`) admits exactly this `Cluster` (a second create with a different `strange.company/recovery-run` is denied); `sc-pg` reaches healthy from the emulator backup, archives into `g<N+1>`, and the newest restored `heartbeat` marker is ≤ 900 s older than the failure time. After the Runner is back, merging the follow-up PR and resuming `sc-db` changes no `Cluster` field (the `.metadata.generation` diff is metadata-only).

### GATE-H02
- **12-AC-15** For each cluster, `kubectl cnpg status` shows 3 instances, 1 primary + 2 streaming replicas, each on a distinct node, and `Synchronous standby names` contains `ANY 1`.
- **12-AC-16** Failover drill: deleting the primary pod of `sc-pg` (`kubectl delete pod --wait=false`) results in a new primary within 60 s (`kubectl cnpg status` `Current Primary` changes). A write through `sc-pg-rw` succeeds within 90 s, and no rows committed before the deletion are missing (marker check).
- **12-AC-17** Replica re-clone drill: `kubectl cnpg destroy sc-pg <replica>` results in a new replica streaming within 15 min.
- **12-AC-18** `kubectl get pdb -n strange-company -l cnpg.io/cluster=sc-pg` lists the primary and replica PDBs, and `kubectl drain` of a replica's node succeeds while that of the primary's node triggers a switchover first.

## 24. Explicit non-goals

- No shared PostgreSQL cluster (ADR-0006).
- No in-tree `barmanObjectStore` and no volume-snapshot backups (local-path cannot snapshot).
- No PgBouncer `Pooler` in MVP (connection counts are small). Revisit if `max_connections` pressure appears.
- No cross-region replica clusters.
- No client-side backup encryption (SI-G3-03).

## 25. Open questions

- **Q-12-1** (blocking: no) Plugin metric names and the `instanceSidecarConfiguration` field path must be confirmed against the pinned plugin version. Resolution: 12-AC-11 and 12-AC-12 fail if they are wrong. Owner: implementation of P04/P10.
- **Q-12-6** (resolved) DR-7 creation guard: plan 37 ADM-63 with the window ConfigMap `flux-system/sc-cnpg-create-window` and the annotation `strange.company/recovery-run` (§9.5). Plans 45/50 render and remove the window.
- **Q-12-5** (resolved) Plan 45 Q-45-6: black start uses the per-cluster components `cnpg-recovery-{latest,target}-<id>` (Flux `spec.patches` on one db layer each), not a swap of the platform component, because the recovery commit may only touch `gitops/clusters/<cluster>/**`. Archive paths stay `serverName <cluster>-g<N>`, which ADR-0554 (revised) now uses.
- **Q-12-2** (blocking: no) Is a `bootstrap` change on a running Cluster accepted by the CNPG webhook? Proposed: 12-AC-13 tests it. If it is rejected, the recovery commit reverts the component to `initdb` *after* the cluster is healthy, and black start re-applies recovery. Owner: plan 12 implementation.
- **Q-12-3** (blocking: no) `dataDurability: preferred` availability depends on the CNPG version. If it is absent, small-ha uses `minSyncReplicas: 0` / `maxSyncReplicas: 1` (legacy fields) with the same "degrade to async" semantics. Owner: H02.
- **Q-12-4** (blocking: no) SI-G3-03: CNPG backups contain Authentik and Forgejo secrets protected only by provider SSE and scoped credentials. Proposed: accept for MVP and record it. Revisit if barman-cloud gains client-side encryption. Owner: plan 34 security review.
