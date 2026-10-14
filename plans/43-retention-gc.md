# 43 — Retention and garbage collection

**Status:** Stage-0 plan (draft)
**DAG nodes:** P10 (retention mechanisms live and audited), P11 (43-AC-3: OCI retention needs internal images, GC-01), H03 (Loki/Tempo retention), R02 (43-AC-8 and 43-AC-9: they need ≥ 7 days of fake-harness load and a P11 + 14 d dry run, GC-01). This plan has no GATE-P12 section; `dag.yaml` lists plan 43 on R02 (resolved at consolidation: no longer on P12).
**Related plans:** [07](07-k3s.md), [08](08-node-security.md), [11](11-storage.md), [12](12-cnpg.md), [13](13-longhorn.md), [14](14-object-storage.md), [15](15-authentik.md), [16](16-forgejo.md), [18](18-flux.md), [19](19-seed-handoff.md), [20](20-woodpecker.md), [23](23-supply-chain.md), [24](24-hermes.md), [26](26-agent-runner.md), [32](32-cost-accounting.md), [35](35-secrets.md), [38](38-prometheus.md), [40](40-opentelemetry.md), [41](41-logging-tracing.md), [42](42-alerting-notifications.md), [44](44-backup.md), [50](50-bootstrap-orchestrator.md), [51](51-capacity-and-scheduling.md), [53](53-model-gateway.md)
**Contracts:** produces `restore-test-result` documents (`retention-audit`, `oci-retention-audit`; schema owned by plan 44)
**ADRs:** ADR-0553 (check-CronJob pattern); relies on ADR-0202, ADR-0204
**Procedures:** none owned; uses `disk-pressure.md` (plan 08) and `restore-test-failure.md` (plan 44)

## 1. Purpose

SPEC §107 requires intentional retention and cleanup so that root disks never fill because a cleanup policy was omitted. This plan is the **single retention register**: for every kind of accumulating data (each SPEC §107 item and every other accumulator found in the plan set) it fixes the retention, the mechanism that enforces it, the deterministic check that proves enforcement, and the alert that fires when it fails. Mechanisms are implemented by their owning plans; this plan sets the numbers and owns the cross-cutting enforcers (`retention-audit`, `oci-retention`, `cr-retention`).

## 2. Requirements derived from SPEC.md

1. Stage 0 defines cleanup for Prometheus, Loki and Tempo data, worker logs, model telemetry, exact task records, CI artifacts, OCI images, Forgejo packages, repository archives, Longhorn snapshots, CNPG backups, Git bundles and bootstrap scratch state. [SPEC §107]
2. Root disks SHALL NOT fill because cleanup policy was omitted. [SPEC §107, §17 "disk-pressure behavior"]
3. Exact accounting records MAY be retained far longer than detailed traces. [SPEC §107, §79 historical pricing]
4. Verbose content capture has retention limits. [SPEC §94]
5. Workers are garbage-collected after exit. [SPEC §4.5]
6. Retention of backups must still meet the recovery objectives. [SPEC §108]
7. Persistent logs and traces use object storage, not root disks. [SPEC §95]
8. Destroying compute does not delete backups or remote state history. [SPEC §114]

Derived:
- **43-R-1** Every item in §9 has a numeric bound (age, count or size) or an explicit "indefinite" with a growth estimate and the alert that bounds its consequence.
- **43-R-2** Every bound is enforced by a mechanism that runs without human action, except the two deliberate manual prunes (ledger rows, plan 32; state history, never).
- **43-R-3** Every mechanism has a deterministic verification check that runs at least daily, or a render-time (F11) check for static configuration.
- **43-R-4** Object-storage retention is enforced by bucket lifecycle rules (plan 14), never by in-cluster deletion of object versions.
- **43-R-5** No retention mechanism may delete a container image digest referenced by GitOps state within the protected window (§9.3).
- **43-R-6** Every Kubernetes `Job` and `CronJob` job template rendered from `gitops/` sets `ttlSecondsAfterFinished`, and every CronJob sets `successfulJobsHistoryLimit ≤ 3` and `failedJobsHistoryLimit ≤ 3` (F11 check `job-retention`).
- **43-R-7** Every `emptyDir` used by a workload in `agent-jobs`, `ci-jobs`, `dr-tests`, `forgejo`, `strange-company`, `hermes` and `monitoring` sets `sizeLimit` (F11 check `emptydir-sizelimit`; VAP enforcement in `agent-jobs` and `ci-jobs` by plan 37).

## 3. Inputs

- Data inventories from plan 11 (register S-NN), plan 26 (Runner tables and S3 prefixes), plan 32 (ledger), plan 38 (Prometheus sizing), plan 07 (etcd, audit log, kubelet GC), plan 16 (Forgejo storage), plan 44 (backup streams).
- The node-disk budget from plan 11 §12.

## 4. Outputs

- The retention register (§9.1), whose IDs `R-NN` are cited by plan 11.
- Lifecycle numbers consumed by `infra/objstore/buckets.yaml` (plan 14). F11 check `retention-consistency` compares the two.
- CronJobs `retention-audit` (dr-tests, read-only), `cr-retention` (dr-tests, deletes old CNPG `Backup` objects and stray finished Jobs), `oci-retention-audit` (forgejo, read-only) and `oci-retention` (forgejo, deletes unreferenced package versions).
- F11 render checks `job-retention`, `emptydir-sizelimit`, `helm-max-history`, `retention-consistency`, `prometheus-retention-set`.
- Alerts (§21).

## 5. Dependencies

- **Hard:** none of its own (no DAG node yet). Checks require P06 (Flux on Forgejo), P09 (alerting), and the mechanisms of the owning plans at P10.
- **Soft:** plan 16 (bot `sc-gc`, Forgejo cron and package cleanup rules), plan 20 (`sc-ci-maint`: Woodpecker log retention and CI workspace PVC sweep, ADR-0305), plan 24 (Hermes session pruning), plan 26 (Runner sweepers and their metrics), plan 32 (ledger pruning functions), plan 41 (Loki/Tempo compactor configuration), plans 07/08 (journald, audit log, kubelet GC).

## 6. Owned state

None of the data itself. This plan owns the retention numbers (Git: this plan, `infra/objstore/buckets.yaml` lifecycle values, the `retention:` sections of component values) and the enforcer CronJobs' configuration in `gitops/platform/retention/`.

## 7. External interfaces

- **Register IDs** `R-01` … `R-47`, cited by plan 11 (§6, "Retention" column) and plan 14 (§9.1).
- **Metrics this plan requires from other plans** (assumed interfaces; owners must expose them):

| Metric | Owner | Meaning |
|---|---|---|
| `sc_runner_retention_oldest_seconds{table}` | 26 | age of the oldest row in `idempotency_keys`, `webhook_deliveries`, `execution_events` |
| `sc_ledger_usage_raw_oldest_seconds` | 32/53 | age of the oldest non-null `usage_raw` |
| `sc_ledger_denials_oldest_seconds` | 53 | age of the oldest `ledger.denials` row |
| `sc_ledger_reservations_oldest_closed_seconds` | 33/53 | age of the oldest settled or expired `ledger.reservations` row |
| `sc_node_journald_bytes` | 07/08 (`sc-node-facts`) | `journalctl --disk-usage` in bytes |
| `sc_etcd_snapshot_count{location}` | 07 (`sc-node-facts`) | local and S3 snapshot counts |
| `sc_hermes_sessions_oldest_inactive_seconds` | 24 | age of the oldest inactive session |
| `sc_bootstrap_local_bytes` | 50 (report field, not a metric) | size of `.bootstrap/` on the operator machine |

## 8. Internal interfaces

- `retention-audit` reads Kubernetes objects (read-only ClusterRole: `jobs`, `pods`, `persistentvolumes`, `secrets` metadata in `flux`-managed Helm storage namespaces (list only, `type=helm.sh/release.v1`), `snapshots.longhorn.io`, `backups.postgresql.cnpg.io`, `prometheuses.monitoring.coreos.com`, ConfigMaps of Loki/Tempo) and Prometheus (HTTP API `GET /api/v1/query`, NetworkPolicy allow from `dr-tests` to `prometheus-operated:9090`).
- `oci-retention` uses the Forgejo API (`GET /api/v1/packages/{owner}`, `DELETE /api/v1/packages/{owner}/{type}/{name}/{version}` (verify both)) with the `sc-gc` token, the OCI distribution API (`HEAD /v2/<name>/manifests/<digest>`), and a read-only clone of `strange-company` (token `sc-backup`).

## 9. Configuration

### 9.1 Retention register

Column legend. **Where:** storage location. **Retention:** the bound. **Mechanism:** what deletes data, and its owner. **Verification:** deterministic check (RA = `retention-audit` check ID, OA = plan 14 `objstore-audit`, F11 = render check). **Alert:** catalogue alert that fires when the bound is not held (new alerts are marked *new*).

#### Metrics, logs, traces, telemetry

| ID | Item (SPEC §107) | Where | Retention | Mechanism (owner) | Verification | Alert |
|---|---|---|---|---|---|---|
| R-01 | Prometheus data ✔ | local-path PVC | 15 d **and** 8 GiB (10 d / 5 GiB on `constrained` 8 GiB-memory nodes, plan 38) | Prometheus `retention` + `retentionSize` (38) | RA-01: `Prometheus` CR fields equal Git values; 38-AC-8 size cap test | `SCPrometheusStorageNearCap` (38) |
| R-02 | Loki data ✔ (small-ha) | S3 `telemetry/loki/` + `emptyDir` WAL (5 GiB `sizeLimit`, plan 41) | 30 d | Loki compactor `retention_enabled: true`, `retention_period: 720h` (41); bucket lifecycle `loki/` 35 d as backstop (14) | RA-02 (H03): Loki runtime config has retention 720h; OA-06 oldest `loki/` object ≤ 38 d | `SCLokiCompactorFailing` (41, requested) |
| R-03 | Tempo data ✔ (small-ha) | S3 `telemetry/tempo/` | 14 d | Tempo compactor `block_retention: 336h` (41); lifecycle `tempo/` 21 d | RA-03 (H03); OA-06 | `SCTempoCompactorFailing` (41, requested) |
| R-04 | Worker logs ✔ | S3 `evidence/logs/<task>/<exec>/worker.log.gz` + index row in `sc-pg` | 90 d (objects); index rows indefinite with `logs_status=expired` after expiry | lifecycle rule `logs/` 90 d (14); Runner marks the index on 404 (26) | OA-03 rule present; OA-06 oldest `logs/` object ≤ 93 d | `SCObjectStorageConfigDrift` |
| R-05 | Model telemetry ✔ | spans: not stored in MVP (ADR-0551); span metrics in Prometheus; `ledger.cost_records.usage_raw`; `ledger.denials`; `ledger.reservations`; Tempo at H03 | span metrics: as R-01; `usage_raw`: 90 d then set null; `denials`: 90 d; `reservations` (settled or expired): 30 d (plan 33/53 request); spans at H03: as R-03; verbose debug captures: only under a human-merged `TelemetryDebugGrant` (plan 40), stored in `evidence/telemetry-debug/` for the grant's `retentionDays` and at most 7 d (R-45) | `ledger.prune_usage_raw()`, denials pruning and reservation pruning run daily by the Gateway's maintenance loop (32/53) | Prometheus: `sc_ledger_usage_raw_oldest_seconds < 92*86400`, `sc_ledger_denials_oldest_seconds < 92*86400`, `sc_ledger_reservations_oldest_closed_seconds < 32*86400` | `SCRetentionTelemetryOverdue` *new* |
| R-06 | Exact task records ✔ | `sc-pg` (tasks, transitions, executions, results, evidence pointers) | **indefinite** (SPEC §107 allows long retention; provenance must survive squash, SPEC §56). Growth estimate < 0.5 GiB/year at self-hosting-minimum | none; bounded in effect by `SCCnpgDatabaseLarge` (12) and sizing review | yearly review item in plan 51 | `SCCnpgDatabaseLarge` (12, proposed) |
| R-35 | Runner short-lived tables | `sc-pg` `idempotency_keys`, `webhook_deliveries` (7 d), `execution_events` (400 d) | as stated | Runner sweeper every hour (26) | Prometheus: `sc_runner_retention_oldest_seconds{table="idempotency_keys"} < 8*86400` etc. | `SCRetentionRunnerSweeperStalled` *new* |
| R-07 | Ledger rows ✔ ("exact accounting records") | `sc-pg` `ledger.cost_records`, `pricing_snapshots` | `cost_records` ≥ 3 years, then **operator-initiated** prune only (`ledger.prune_cost_records(before)`); `pricing_snapshots` forever | manual (32), never automatic in MVP | none needed (no automatic deletion); size via `SCCnpgDatabaseLarge` | `SCCnpgDatabaseLarge` |
| R-33 | Container logs on nodes | `/var/log/pods` | 10 MiB × 3 files per container | kubelet `containerLogMaxSize`/`containerLogMaxFiles` (07/08) | RA-33: `kubectl get --raw /api/v1/nodes/<n>/proxy/configz` shows the values | `SCRetentionAuditFailed` *new* |
| R-23 | journald | node | `SystemMaxUse=1G`, `MaxRetentionSec=30day` | journald config (08) | Prometheus: `sc_node_journald_bytes < 1.1*2^30` | `SCNodeJournalOversize` *new* |
| R-24 | k3s audit log | `/var/lib/rancher/k3s/server/logs/` | 7 d, 50 MiB × (5 backups + 1) ≤ 300 MiB | kube-apiserver `audit-log-maxage/maxbackup/maxsize` (07) | RA-24: `sc_k3s_cluster_config_info` hash equals the rendered config (07) | `SCK3sServerConfigDrift` (07) |
| R-36 | Kubernetes Events | etcd | 1 h | kube-apiserver `--event-ttl` default (07) | — (default) | — |
| R-27 | Alertmanager silences and notification log | local-path PVC | 120 h | `alertmanagerSpec.retention` (38) | RA-01 checks the CR field | — |
| R-34 | Authentik events | `authentik-pg` | 365 d | Authentik event expiry via blueprint (15) | plan 15 15-AC (setting value) | — |

#### Workloads and nodes

| ID | Item | Where | Retention | Mechanism (owner) | Verification | Alert |
|---|---|---|---|---|---|---|
| R-20 | **Jobs TTL and finished pods** | etcd, node | worker Jobs: deleted by the Runner after log capture, fallback `ttlSecondsAfterFinished: 21600` (26); CronJob-created Jobs: `ttlSecondsAfterFinished: 604800`, history limits 1 success / 3 failed (44 §7.1); every other Job: `ttlSecondsAfterFinished` ≤ 604800; terminated pods: kube-controller-manager `terminated-pod-gc-threshold=500` (07) | Kubernetes TTL controller, Runner, pod GC; `cr-retention` deletes finished Jobs older than 8 d that lack a TTL (belt and braces) | F11 `job-retention`; RA-20: zero Jobs with `completionTime` older than 8 d, zero pods in `Succeeded`/`Failed` older than 8 d, zero `rt-*` scratch objects older than 6 h | `SCRetentionAuditFailed` |
| R-21 | Worker and CI workspaces | workers: `emptyDir`; CI: one `ci-workspace` PVC per Woodpecker workflow on the bounded filesystem `/var/lib/sc/ci-workspace` (ADR-0305) | workers: lifetime of the pod, `sizeLimit` (8Gi / 5Gi, plan 11 §9.2); CI: lifetime of the workflow, at most `maxPipelineTimeout + 15m` (default 105 min) | workers: kubelet eviction on `sizeLimit`, pod deletion (R-20); CI: Woodpecker deletes the PVC at workflow end, `sc-ci-maint` (plan 20, hourly) deletes leftovers older than 105 min, `reclaimPolicy: Delete` removes the directory; the filesystem size caps the total | F11 `emptydir-sizelimit`; VAP (37); RA-21: no PVC in `ci-jobs`/`ci-build` older than 2 h, and every PV of class `ci-workspace` is `Bound` or gone (none `Released` > 15 min) | `SCCIMaintFailed` (20), `SCCIWorkspaceFilesystemHigh` (20) |
| R-22 | Node container images | containerd image store | kubelet image GC at 80 % → 70 % disk (07), plus `imageMaximumGCAge: 720h` (unused images older than 30 d removed; Kubernetes ≥ 1.30, verify feature gate on the pinned k3s) | kubelet (07/08) | RA-22: kubelet `configz` shows the values | `SCNodeRootDiskLow` |
| R-28 | Flux artifacts and Helm release history | source-controller `emptyDir` (2Gi) ; Helm release Secrets | artifacts: latest per source; Helm history `maxHistory: 5` | Flux (18) | F11 `helm-max-history`; RA-28: ≤ 6 `sh.helm.release.v1.*` Secrets per release | `SCRetentionAuditFailed` |
| R-37 | CNPG `Backup` objects | etcd | 35 d (backup data itself: R-14) | `cr-retention` deletes `backups.postgresql.cnpg.io` in phase `completed` or `failed` older than 35 d (CNPG does not garbage-collect them, verify) | RA-37: no `Backup` older than 36 d | `SCRetentionAuditFailed` |
| R-38 | Longhorn `Backup`/`BackupVolume` objects | etcd | mirror of the backup target (Longhorn syncs them) | Longhorn (13) | — | — |
| R-39 | k3s `ETCDSnapshotFile` objects | etcd | mirror of snapshots | k3s (07) | — | — |
| R-40 | Released PersistentVolumes | etcd + disk | `longhorn-scratch`/`local-path` PVs: deleted with the claim (`Delete`); `longhorn` PVs (`Retain`): never automatic | reclaim policy (11, 13) | RA-40: no PV of class `local-path` or `longhorn-scratch` in phase `Released` older than 24 h; `longhorn` PVs in `Released` are reported (warning check, not deleted) | `SCRetentionAuditFailed` |

#### CI, images, packages, Forgejo

| ID | Item | Where | Retention | Mechanism (owner) | Verification | Alert |
|---|---|---|---|---|---|---|
| R-08 | **CI artifacts** ✔ | images and packages: R-09, R-10; RED/GREEN evidence: R-11; pipeline logs: R-19 | see those rows | — | — | — |
| R-09 | **OCI images** ✔ (Forgejo registry) | `forgejo-objects/packages/` + `forgejo-pg` metadata | **keep:** every digest in the protected set P (§9.3); all versions younger than 30 d. **delete:** other versions older than 30 d | `oci-retention` weekly (this plan) + Forgejo cron `cleanup_packages` for orphaned blobs (16); bucket noncurrent 30 d (14) | `oci-retention-audit` daily: every digest of P(a), P(b) and P(d) answers `HEAD /v2/<name>/manifests/<digest>` = 200 | `SCOciReferencedImageMissing` *new* (critical, black-start threat); `SCOciRetentionFailed` *new* |
| R-10 | **Forgejo packages** ✔ (non-container: generic, Helm, Go, etc.) | same | keep the newest 10 versions per package and every version matching `^v?[0-9]+\.[0-9]+\.[0-9]+$` for 365 d; delete others older than 30 d | Forgejo package **cleanup rules** per owner, declared in `forge.yaml` and applied by `forge apply` (16). No container-type rule (R-09 owns containers) | `oci-retention-audit` check `cleanup-rules-present` (API read of rules, verify endpoint) | `SCOciRetentionFailed` |
| R-18 | **Repository archives** ✔ (Forgejo-generated zip/tar.gz downloads) | `forgejo-objects/repo-archive/` | 24 h | Forgejo cron `archive_cleanup` with `OLDER_THAN = 24h` (16, verify key) | OA-06 for prefix `repo-archive/` (oldest ≤ 3 d) | `SCObjectStorageConfigDrift` |
| R-41 | Forgejo LFS objects, attachments, avatars | `forgejo-objects/{lfs,attachments,avatars,repo-avatars}/` | as long as referenced by Forgejo; deleted objects: 30 d as noncurrent versions | Forgejo (16) + lifecycle (14) | OA-03 | — |
| R-26 | Forgejo indexer and queues | Longhorn `forgejo-data` | Forgejo-managed (reconstructible) | Forgejo | volume usage | `SCLonghornVolumeNearlyFull` |
| R-19 | **Woodpecker pipeline logs** and pipelines | `woodpecker-pg` | logs: **30 d** (keeps `woodpecker-pg` inside its 8Gi/4Gi size, plan 12; plan 20's default of 90 d must change to `retention.logDays: 30`); pipeline records: kept (small) | `sc-ci-maint` CronJob (plan 20, hourly; deletes logs of pipelines finished > `retention.logDays` ago through the Woodpecker API, ≤ 500 per run). RED/GREEN evidence is already in S3 (R-11), so log deletion loses no provenance | check-cronjob `sc-ci-maint` (plan 20) | `SCCIMaintFailed` (20) |
| R-11 | RED/GREEN evidence, results | `evidence/{evidence,results}/` | **indefinite** (TDD provenance, SPEC §56); ≤ 1 MiB per execution | none; noncurrent versions 30 d | OA-03 | — |
| R-42 | Context packets | `evidence/context/` | 180 d | lifecycle (14) | OA-06 | `SCObjectStorageConfigDrift` |
| R-43 | Quarantined refs and objects | `evidence/quarantine/` | 90 d | lifecycle (14) | OA-06 | same |

#### Backups and recovery material

| ID | Item | Where | Retention | Mechanism (owner) | Verification | Alert |
|---|---|---|---|---|---|---|
| R-13 | **Longhorn snapshots** ✔ | `/var/lib/longhorn` | `snap-6h` retain 4; backup snapshots auto-cleaned; `snapshot-cleanup-daily`; hard cap `snapshotMaxCount: 20` | Longhorn recurring jobs and settings (13) | RA-13: ≤ 12 snapshots per volume | `SCRetentionAuditFailed` |
| R-13b | Longhorn backups | `longhorn-backups` | 14 twelve-hourly (7 d) + 4 weekly; deleted versions 14 d (noncurrent) | recurring-job `retain` (13) + lifecycle (14) | `rt-longhorn-volume` uses the newest; OA-03 | `SCLonghornBackupStale` |
| R-14 | **CNPG backups** ✔ | `pg-backups` | recovery window 30 d; deleted versions 14 d | barman-cloud `retentionPolicy: "30d"` (12) + lifecycle (14) | `SCCnpgRecoveryWindowShort` (12, proposed: window < 7 d) and OA-03 | same |
| R-15 | CNPG local WAL | `pg_wal` in PGDATA | bounded by archiving; `max_wal_size: 512MB`, `max_slot_wal_keep_size: 2GB` (12) | PostgreSQL | WAL backlog metrics | `SCCnpgWalArchiveBacklog`, `SCCnpgWalReadyBacklogHigh` (12) |
| R-16 | **etcd snapshots** | local `/var/lib/rancher/k3s/server/db/snapshots/` + `etcd-snapshots` | scheduled: 28 (7 d) local and S3 (k3s prunes); `pre-*` on demand: newest 10 (pruned weekly by `k3s etcd-snapshot prune --name pre --snapshot-retention 10` from plan 08's node maintenance timer, verify flags); S3: current versions older than 60 d expire (removes snapshots of nodes that no longer exist, which k3s never prunes; DR-31b); deleted versions 30 d | k3s (07), node timer (08), lifecycle (14) | Prometheus: `sc_etcd_snapshot_count{location="local"} <= 38` | `SCRetentionEtcdSnapshotsExcess` *new* |
| R-17 | **Git bundles** ✔ | `repo-backups/git/weekly/` 56 d; `git/monthly/` 400 d; `meta/` 56 d; deleted versions 14 d; cache PVC: mirror per existing repository | lifecycle (14); the bundle job removes cache mirrors of repositories deleted from Forgejo more than 7 d ago (44) | OA-03, OA-06; RA-17: `repo-backup-cache` usage < 80 % of nominal | `SCObjectStorageConfigDrift`, `SCRetentionAuditFailed` |
| R-12 | Restore-test results | `evidence/restore-tests/` | 400 d | lifecycle (14) | OA-06 | `SCObjectStorageConfigDrift` |
| R-46 | Recovery-check results (plan 35) | `evidence/checks/` | 400 d | lifecycle (14, STO-17) | OA-06 | `SCObjectStorageConfigDrift` |
| R-44 | Canary objects | `evidence/canary/` | 1 d | lifecycle (14) | OA-06 | same |
| R-45 | Telemetry debug captures (plan 40) | `evidence/telemetry-debug/` | grant `retentionDays` (≤ 7 d), hard bound 7 d; noncurrent 1 d | Runner grant-retention sweeper with `SC-OBJECT-STORAGE/telemetry-debug` (40); lifecycle rule `telemetry-debug/` 7 d (14) as backstop; no Object Lock on B9 (14 §10) | OA-03 rule present; OA-06 oldest `telemetry-debug/` object ≤ 8 d | `SCObjectStorageConfigDrift`; plan 40's debug-capture alerts |
| R-25 | **Hermes sessions** and `/opt/data` | Longhorn `hermes-data` | sessions inactive > 90 d deleted; learned memory kept (non-authoritative, SPEC §43); volume ≤ 80 % | Hermes session pruning through the gateway API (`DELETE /api/sessions/{id}`, L-1) run daily by a CronJob in `hermes` with `SC-HERMES-API` (24) | Prometheus: `sc_hermes_sessions_oldest_inactive_seconds < 92*86400` | `SCRetentionHermesSessionsOverdue` *new*; `SCLonghornVolumeNearlyFull` |
| R-29 | OpenTofu state, bootstrap journal, run logs | `tofu-state` | state: noncurrent 365 d and newest 100 kept; `runs/` 180 d; `conformance/` 1 d | lifecycle (14); never deleted by automation (06) | OA-03 | `SCObjectStorageConfigDrift` |
| R-30 | **Bootstrap scratch state** ✔ | operator `.bootstrap/{work,tmp}/`, `.bootstrap/runs/` | `work/<module>` recreated fresh every run; `tmp/` deleted at the end of every run (and at the start, after a crash); local `runs/`: newest 20 | `sc-bootstrap` (50) | `./bootstrap status` reports `.bootstrap/` size; preflight warning PF (50) when > 2 GiB | — (off-cluster) |
| R-31 | Seed snapshots | `bootstrap-seed/<cluster>/<sha>/` | ≥ 90 d after transfer **and** never fewer than the newest 3 per cluster; the snapshot of the current recovery or seed commit is never deleted | `sc-bootstrap seed-gc` at the end of the seed phase and in every `./bootstrap verify` (50/19), with `seed-write` (`DeleteObject` → delete markers; noncurrent 30 d) | `./bootstrap verify` V-check: count ≥ min(3, seeds ever) | — (off-cluster) |
| R-32 | Recovery bundles | `recovery` | current + **all** previous generations, noncurrent kept 3650 d (effectively never; each < 1 MiB; old generations hold previous tofu passphrases needed for old state versions, plan 06 §20.3) | lifecycle noncurrent 3650 d (14); no other deletion (35) | OA-03 | — |
| R-47 | Bootstrap capsules (CD-33, plan 45 §9.4) | `recovery/<cluster>/capsule/<capsule-id>/` (≈ 5 GiB each) and `capsule/LATEST.json` | keep the **3 newest capsules plus any younger than 35 days**; `LATEST.json` never expires | each capsule object carries a 35-day Object Lock retention (set by `capsule-writer`, plan 14); lifecycle rule `capsule-expiry` on prefix `<cluster>/capsule/2` (matches capsule ids, never `LATEST.json`) expires current versions after 100 d and noncurrent after 1 d. Because capsules are refreshed at least every 30 days (plan 45 refresh trigger 3), at least the 3 newest are always younger than 100 d; if refreshes stop, the daily capsule check fails at 31 days, long before the oldest of the 3 reaches 100 d | daily `rt-bootstrap-capsule` light check (plan 44 RT-8): newest capsule ≤ 31 d old, ≥ 3 capsules or all younger than 35 d listed, `LATEST.json` resolves; OA-03 rule present | `SCBootstrapCapsuleCheckFailed` |

✔ marks the SPEC §107 items. Every §107 item has a row.

### 9.2 Root disks never fill

Every consumer of a node's root filesystem has an enforced bound (plan 11 §12 gives the sizes):

| Consumer | Bound | Row |
|---|---|---|
| OS, packages | apt cache cleaned by plan 08's unattended-upgrades `AutocleanInterval=7` | plan 08 |
| journald | 1 GiB | R-23 |
| k3s audit log | 300 MiB | R-24 |
| etcd DB | `--quota-backend-bytes` default 2 GiB + `SCEtcdDbSizeHigh` (07) | plan 07 |
| etcd local snapshots | 38 files | R-16 |
| containerd images | kubelet GC 80 % → 70 %, max age 30 d | R-22 |
| pod logs | 30 MiB per container | R-33 |
| emptyDir | `sizeLimit` everywhere | R-21, 43-R-7 |
| CI workspaces | separate bounded filesystem `/var/lib/sc/ci-workspace` (12 GiB proposed at self-hosting-minimum, plan 11 §12); a full CI filesystem fails CI jobs only and cannot fill `/` | R-21, ADR-0305 |
| local-path PVCs (PGDATA, Prometheus, Alertmanager, bundle cache); Loki/Tempo WAL on `emptyDir` with `sizeLimit` (plan 41) | application caps (R-01, R-15, R-27, R-17) and size alerts | |
| Longhorn | `storageMinimalAvailablePercentage: 15` stops new replicas; volume size caps; snapshot caps | R-13 |
| Restore-test scratch | capacity gate before creation, deletion after (plan 44 §9.3) | R-20 |

Detection chain: predictive `SCNodeRootDiskFillingUp` (24 h ahead) → `SCNodeRootDiskLow` (75 % used) → image GC (80 %) → `SCNodeRootDiskCritical` and kubelet soft eviction (85 % used, `nodefs.available` 15 %, plan 51) → hard eviction (90 % used, `nodefs.available` 10 %) in PriorityClass order (plan 51: agent workers first). The budget (plan 11 §12) keeps year-one usage ≤ 70 %, below the first of these. The procedure is `disk-pressure.md` (plan 08).

### 9.3 OCI image retention (R-09)

**Protected set P** (computed weekly and daily; digest = `sha256:<64 hex>` of a manifest or index):
- (a) every digest referenced in `gitops/**`, `policy/**`, `charts/**`, `toolbox/image.lock` at `main` HEAD;
- (b) the same at every commit on `main` in the last **90 days** (the rollback window);
- (c) the same at the newest **10** release tags `v*` (the "N releases back" rule; N is `ociRetention.keepReleases`);
- (d) every image digest currently running in the cluster (`status.containerStatuses[].imageID` of all pods);
- (e) all OCI referrers (signatures, SBOMs, provenance attestations, cosign `sha256-<digest>.*` tags) of any digest in P;
- (f) every child manifest of an index in P.

**Candidates:** container package versions of every Forgejo owner, as listed by the packages API. A version is deleted only if all of: its digest ∉ P; it was created more than **30 days** ago; it is not one of the newest **3** versions of its package; and the run is in `enforce` mode. At most **200** deletions per run; more pending deletions carry over to the next week.

**Modes:** `ociRetention.mode: dry-run` for the first 14 days after P11 (the run writes the deletion list as evidence and deletes nothing), then `enforce` by PR. Every run uploads its decision list (`kept`, `deleted`, reason) to `evidence/retention/oci/<ts>.json`.

**Safety:** `oci-retention` first runs the full `oci-retention-audit` logic. If any digest from (a), (b) or (d) is missing, it aborts without deleting and exits 1. A digest referenced only in (c) but missing raises the audit warning without blocking.

**Blob GC:** deleting package versions leaves blobs; Forgejo's `cleanup_packages` cron (enabled, `OLDER_THAN = 24h`, verify key) removes unreferenced blobs from `forgejo-objects`. Their versions expire after 30 d (lifecycle).

### 9.4 Enforcer CronJobs

| CronJob | Namespace | Schedule (UTC) | Mode | ServiceAccount rights | Check label |
|---|---|---|---|---|---|
| `retention-audit` | dr-tests | `40 6 * * *` | read-only | ClusterRole read on the objects of §8; Prometheus query | `retention-audit` |
| `cr-retention` | dr-tests | `50 6 * * *` | deletes CNPG `Backup` (> 35 d, completed/failed), finished Jobs without TTL (> 8 d), `rt-*` scratch older than 6 h | ClusterRole: `delete` on `backups.postgresql.cnpg.io`, `jobs`; `delete` on `rt-*` objects in `dr-tests` (VAP-limited, plan 44) | `cr-retention` |
| `oci-retention-audit` | forgejo | `30 5 * * *` | read-only | Forgejo `sc-backup` token (packages read, verify scope `read:package`), cluster `list pods` (for P(d)) | `oci-retention-audit` |
| `oci-retention` | forgejo | `0 5 * * 0` | deletes package versions | Forgejo `sc-gc` token (`write:package`), cluster `list pods` | `oci-retention` |
| `hermes-session-prune` | hermes | `15 3 * * *` | deletes inactive sessions | `SC-HERMES-API` (plan 24) | `hermes-session-prune` |

All follow plan 44 §7.1's CronJob contract and write a `restore-test-result` document (`test_kind: retention-audit` or `oci-retention-audit`; the destructive jobs use `test_kind: retention-audit` with `subject` = the job name).

### 9.5 Retention-audit checks

| Check | Assertion |
|---|---|
| RA-01 | Prometheus CR `retention` and `retentionSize` equal the values in Git (ConfigMap `dr-tests/retention-expected`, generated from this plan's numbers at F11); Alertmanager `retention` = `120h` |
| RA-02, RA-03 | (small-ha) Loki `retention_period` = `720h` and compactor retention enabled; Tempo `block_retention` = `336h` |
| RA-13 | every Longhorn volume has ≤ 12 snapshots |
| RA-17 | `cache_bytes` exported by the newest `repo-bundle-backup` manifest (a `du` measurement, plan 44 §9.2.2 step 9) is < 1.3 × the Git data it mirrors and < 80 % of the free-space allowance in plan 11 §12.1 (no kubelet volume metric is assumed for hostPath-type local-path volumes, STO-15) |
| RA-21 | no PVC in `ci-jobs`/`ci-build` older than 2 h; no `ci-workspace` PV `Released` for more than 15 min (ADR-0305) |
| RA-20 | no Job with `completionTime` older than 8 d; no pod in `Succeeded`/`Failed` older than 8 d; no `rt-*` object older than 6 h |
| RA-22, RA-33 | kubelet `configz`: `imageGCHighThresholdPercent=80`, `imageGCLowThresholdPercent=70`, `containerLogMaxSize=10Mi`, `containerLogMaxFiles=3`, `imageMaximumGCAge=720h` (if supported) |
| RA-28 | ≤ 6 Helm release Secrets per release |
| RA-37 | no CNPG `Backup` older than 36 d |
| RA-40 | no `local-path`/`longhorn-scratch` PV `Released` for > 24 h |
| RA-50 | every `Job` and CronJob job template in the cluster (outside `kube-system`) has `ttlSecondsAfterFinished` set or is a CronJob child (runtime twin of F11 `job-retention`) |

## 10. Secrets

| Class / instance | Use |
|---|---|
| `SC-FORGEJO-BOT-TOKENS/sc-gc` (**new instance**, plan 16 creates the bot `sc-gc` in forge-init and plan 35 registers it) | `oci-retention` deletes container package versions; scope `write:package` only (verify Forgejo scope name); no repository write |
| `SC-FORGEJO-BOT-TOKENS/sc-backup` | `oci-retention-audit` (read packages, clone `strange-company`) |
| `SC-HERMES-API` | `hermes-session-prune` |

No retention job holds an object-storage credential that can delete versions (43-R-4).

## 11. Storage

None of its own. Evidence documents of retention runs go to `evidence/retention/` (R-11 class, indefinite; ≤ 50 KiB per run).

## 12. Resource requirements

Each enforcer: requests 20m/64Mi, limit 256Mi (512Mi for `oci-retention*`, which clones `strange-company` and walks 90 days of history), `activeDeadlineSeconds: 1800`. PriorityClass: `ci` for the enforcers in `dr-tests` (plan 51 allows nothing higher there; as light checks they do not request `strange.company/heavy-slot`, CD-16); `platform` for those in `forgejo` and `hermes`. Total runtime < 15 min/day.

## 13. Security boundary

- Destructive enforcers have the narrowest rights that work: `cr-retention` can delete only CNPG `Backup` objects and finished Jobs cluster-wide, plus `rt-*` scratch in `dr-tests`. A VAP (plan 37) denies its deletion of any Job that is not finished (`status.completionTime` or a `Failed` condition must be present).
- `sc-gc` can delete packages but cannot push code, change settings or touch repositories.
- Object-storage expiry happens only through lifecycle rules set with `admin` (plan 14), never through in-cluster deletes of versions.
- Workers have no route to any enforcer or its credentials.

## 14. Happy path

1. F11: render checks pass (`job-retention`, `emptydir-sizelimit`, `helm-max-history`, `retention-consistency`, `prometheus-retention-set`).
2. P01: bucket lifecycles are created from `infra/objstore/buckets.yaml` with this plan's numbers.
3. P04–P10: owning plans configure their mechanisms (Prometheus caps, Longhorn jobs, CNPG retention, kubelet GC, journald, audit log).
4. P10: enforcer CronJobs are reconciled (layer `backups` for `dr-tests`, `forgejo-config` for `forgejo`, the Hermes layer for `hermes-session-prune`; Woodpecker log retention and CI PVC sweeps run in plan 20's `sc-ci-maint`); first runs pass; `oci-retention` starts in `dry-run`.
5. P11 + 14 d: `oci-retention` switches to `enforce` by PR after the dry-run lists are reviewed.
6. Steady state: daily audits pass; nothing grows without bound; P12 projection holds.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| A mechanism silently stops (e.g. Runner sweeper stalled) | the matching Prometheus rule or RA check | growth | none | owning plan's procedure; `disk-pressure.md` if disk is affected |
| Lifecycle rule removed or ignored by the provider | OA-03 / OA-06 (plan 14) | object-storage cost growth; no disk effect | none | `object-storage-outage.md` §Drift |
| `oci-retention` would delete a referenced image | pre-run audit aborts | no deletion | abort, exit 1 → `SCOciRetentionFailed` | inspect the decision list; fix P computation |
| A referenced image is already missing | `oci-retention-audit` → `SCOciReferencedImageMissing` (critical) | rollback to that revision, or a pod reschedule, would fail to pull | none | rebuild from the tagged source through trusted CI (plan 23); restore the blob version from the bucket (noncurrent ≤ 30 d) |
| Woodpecker log retention not running | `SCCIMaintFailed` (plan 20) | `woodpecker-pg` grows | none | plan 20 `woodpecker-failure.md` §F |
| CNPG `Backup` objects accumulate | RA-37 | etcd object growth | `cr-retention` deletes | — |
| Root disk filling despite caps | `SCNodeRootDiskFillingUp`, then `SCNodeRootDiskLow` | evictions possible | kubelet image GC, eviction by priority | `disk-pressure.md` |
| Hermes sessions not pruned | `SCRetentionHermesSessionsOverdue` | `hermes-data` fills | none | plan 24 `hermes-failure.md` |
| Seed snapshots below 3 or GC failing | `./bootstrap verify` V-check | none (audit only) | — | plan 19 |

## 16. Retry behavior

Enforcers run on schedules with `backoffLimit: 0`; the next run retries. `oci-retention`'s 200-deletion cap makes progress incremental. No enforcer retries a failed delete within a run more than 3 times (2 s, 4 s, 8 s), and failures are listed in its evidence.

## 17. Idempotency behavior

All deletions are "delete if still matching the rule". A rerun after a crash recomputes from current state. Audits are read-only. Lifecycle rules are declarative (OpenTofu).

## 18. Bootstrap behavior

Bucket lifecycles exist from P01. In-cluster enforcers start at P10. Before P10 the only accumulators are seed-phase ones (CNPG WAL for `forgejo-pg`, Longhorn snapshots of `forgejo-data`, etcd snapshots), and their own mechanisms bound them from the moment they exist. During black start, `oci-retention` is suspended by the recovery component until the finalize PR, so it cannot delete images while Forgejo's package metadata is being restored.

## 19. Upgrade behavior

- Changing a retention number is a PR to this plan's values (`gitops/platform/retention/`, component values, `infra/objstore/buckets.yaml`). F11 `retention-consistency` fails if the three disagree. Lifecycle changes then need an operator-run storage apply (plan 14 §19).
- Shortening a backup retention below what plan 44 §9.6 needs for its RPO/RTO claims is rejected in review (the PR template for `infra/objstore/buckets.yaml` lists the backup rows).
- Component upgrades that rename retention settings (Prometheus, Loki, Tempo, Longhorn, Woodpecker, Forgejo cron keys) are caught by RA checks on the next day and by the owning plan's post-upgrade gate.

## 20. Recovery behavior

Retention never deletes recovery data inside its protection window. Over-deletion recovery paths: OCI blobs and package objects from noncurrent versions (≤ 30 d); CNPG `Backup` CRs are only records (S3 is authoritative; `cr-retention` never touches S3); Woodpecker pipelines deleted by purge are not recoverable except through `woodpecker-pg` PITR (≤ 30 d).

## 21. Observability

- **Check-CronJob alerts (new, for plan 42; ADR-0553 form):**

| alertname | CronJob | maxAge / failAfter | Severity / class |
|---|---|---|---|
| `SCRetentionAuditFailed` | `dr-tests/retention-audit` | 36h / 1h | warning / notify |
| `SCCrRetentionFailed` | `dr-tests/cr-retention` | 36h / 1h | warning / notify |
| `SCOciReferencedImageMissing` | `forgejo/oci-retention-audit` | 36h / 15m | critical / page, `alwaysNotify: black-start-threat` |
| `SCOciRetentionFailed` | `forgejo/oci-retention` | 8d / 1h | warning / notify |
| `SCRetentionHermesSessionsOverdue` | `hermes/hermes-session-prune` | 36h / 1h | warning / notify |

- **Prometheus-rule alerts (new):**

| alertname | Expression (sketch) | For | Severity |
|---|---|---|---|
| `SCNodeRootDiskFillingUp` | `predict_linear(node_filesystem_avail_bytes{mountpoint="/",fstype!~"tmpfs|overlay"}[6h], 24*3600) < 0.10 * node_filesystem_size_bytes{mountpoint="/",fstype!~"tmpfs|overlay"}` | 1h | warning / notify |
| `SCRetentionTelemetryOverdue` | `sc_ledger_usage_raw_oldest_seconds > 92*86400 or sc_ledger_denials_oldest_seconds > 92*86400 or sc_ledger_reservations_oldest_closed_seconds > 32*86400` | 1h | warning |
| `SCRetentionRunnerSweeperStalled` | `sc_runner_retention_oldest_seconds{table=~"idempotency_keys|webhook_deliveries"} > 8*86400 or sc_runner_retention_oldest_seconds{table="execution_events"} > 401*86400` | 1h | warning |
| `SCNodeJournalOversize` | `sc_node_journald_bytes > 1.1 * 2^30` | 1h | warning |
| `SCRetentionEtcdSnapshotsExcess` | `sc_etcd_snapshot_count{location="local"} > 38` | 6h | warning |

- **Dashboard (plan 39):** "Retention": per-row status from the latest `retention-audit`, bucket sizes (plan 14 OA-08), oldest object per expiring prefix, root-disk usage and the 24 h projection.

## 22. Testing strategy

- **F11 render checks** with negative fixtures: a Job without `ttlSecondsAfterFinished`, a CronJob with `successfulJobsHistoryLimit: 10`, an `emptyDir` without `sizeLimit` in `agent-jobs`, a HelmRelease with `maxHistory: 20`, a lifecycle value in `buckets.yaml` differing from the register.
- **Unit:** protected-set computation for `oci-retention` (fixture repository with digests across commits, tags and an index with children; referrers), candidate filtering (age, newest-3, cap 200), dry-run vs enforce.
- **k3d** (`make e2e-retention`): Forgejo test instance with a container registry. Push 6 image versions with fake creation dates (test hook), reference 2 of them in a fixture `gitops/` history, run `oci-retention` in enforce mode → exactly the expected versions remain; delete a referenced version manually → `oci-retention-audit` fails and the alert fires. `cr-retention` deletes a synthetic CNPG `Backup` aged 40 d (creation timestamp cannot be faked, so the test uses the `--now` override of the binary) and leaves one aged 10 d.
- **Live:** gates below.

## 23. Acceptance criteria

### GATE-P10
- **43-AC-1** `make render-check` passes `job-retention`, `emptydir-sizelimit`, `helm-max-history`, `retention-consistency` and `prometheus-retention-set` on the real tree, and each negative fixture of §22 fails with its check name.
- **43-AC-2** `retention-audit` latest result: `outcome: passed` with RA-01, RA-13, RA-17, RA-20, RA-21, RA-22, RA-28, RA-33, RA-37, RA-40, RA-50 passed (RA-02/RA-03 `skipped` before H03).
- **43-AC-4** Every alert in §21 exists in `contracts/alert-catalog.yaml` with `signalOwnerPlan` and a runbook, and `promtool test rules` covers each.
- **43-AC-5** Bucket lifecycles match: plan 14's 14-AC-7 and `objstore-audit` OA-03 passed (the lifecycle values in `infra/objstore/buckets.yaml` equal §9.1, enforced by `retention-consistency`).
- **43-AC-6** Job hygiene: 8 days after P10, `kubectl get jobs -A -o json | jq '[.items[] | select(.status.completionTime != null and ((now - (.status.completionTime | fromdateiso8601)) > 691200))] | length'` prints `0`.


### GATE-P11
- **43-AC-3** `oci-retention-audit` latest result passed, and the evidence lists ≥ 1 digest for P(a) and P(d). `oci-retention` has run once in `dry-run` and written `evidence/retention/oci/<ts>.json`.

### GATE-R02
- **43-AC-7** Each metric in §7 exists (moved from P10: the Runner, Gateway and Hermes metrics exist only after C01, C05 and C08, GC-01) in Prometheus (`count(<metric>) > 0`), or the owning plan has recorded it as unavailable with an alternative check in this register.
- **43-AC-8** Growth horizon (re-baselined for SPEC v1.2, CD-25): after ≥ 7 days of steady state at R02 including at least 20 fake-harness executions per day (plan 46 load profile), the measured growth rate of root-filesystem usage (`deriv(node_filesystem_avail_bytes{mountpoint="/"}[7d])`) projects the time until usage reaches the 70 % design line of plan 11 §12. Pass: ≥ 365 days on a 200 GiB (preferred) node; ≥ 120 days on a 100 GiB (minimum) node, where the report must also show the plan 11 §12.1 data ceiling and the months remaining. The values are recorded in the bootstrap verification report (section `disk`, together with 11-AC-9).
- **43-AC-9** `oci-retention` has switched to `enforce` (Git value `ociRetention.mode: enforce`), has completed one enforcing run with `outcome: passed`, and `oci-retention-audit` passed afterwards.

### GATE-H03
- **43-AC-10** RA-02 and RA-03 pass; `aws s3api get-bucket-lifecycle-configuration --bucket <prefix>-telemetry` contains `loki/` 35 d and `tempo/` 21 d; Loki's `/config` endpoint shows `retention_period: 720h`.

## 24. Explicit non-goals

- Automatic pruning of ledger rows, task records or evidence documents (exact accounting and provenance are long-lived by design).
- Deleting OpenTofu state history or recovery bundles.
- Per-tenant or per-project retention policies.
- Retention for data outside Strange Company's control (GitHub mirror history, provider-side logs).

## 25. Open questions

- **Q-43-1** (blocking: no; updated after GC-01) DAG pairs required: add `"43"` to P11 and R02 and remove it from P12 (43-AC-3, -8, -9 moved). Earlier text follows. `plans/dag.yaml` lists plan 43 on no node. Proposed: add `"43"` to P10, P12 and H03 during consolidation (same as plan 11's Q-11-1). Owner: master plan.
- **Q-43-2** (blocking: no) New Forgejo bot `sc-gc` (scope `write:package`). The master plan's machine-account list (§6.1) must add it, and plan 16's forge-init must create it. Alternative: let `sc-runner` delete packages; rejected because it widens the Runner's authority. Owner: plans 16, 35, master plan.
- **Q-43-3** (blocking: no) Assumed metrics from plans 07, 24, 26, 32 and 53 (§7). If an owner cannot expose one, it adds an equivalent check here by PR. Owner: those plans.
- **Q-43-4** (blocking: no) Version-sensitive keys to verify at implementation: Forgejo cron `archive_cleanup` and `cleanup_packages` keys, the package cleanup-rule API, the package delete API, Woodpecker log-deletion API (plan 20), kubelet `imageMaximumGCAge` on k3s, Longhorn `snapshotMaxCount`, k3s `etcd-snapshot prune` flags. Each is covered by an RA check or the k3d suite. Owner: implementation.
- **Q-43-5** (blocking: no) Plan 11 §12 earlier proposed node-filesystem warnings at 75 %/85 % used; the catalogue uses 85 %/92 %. Resolution: keep the catalogue thresholds and add the predictive `SCNodeRootDiskFillingUp` (plan 11 text updated). Owner: plan 42.
- **Q-43-6** (blocking: no) Should `restore-test-result` documents written by destructive enforcers use a distinct `test_kind` (e.g. `retention-run`)? Proposed: no for v1alpha1; they use `retention-audit` with a descriptive `subject`. Revisit at the contract's v1 bump. Owner: 44.
