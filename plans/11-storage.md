# 11 — Storage philosophy and durable-state ownership

**Status:** Stage-0 plan (draft)
**DAG nodes:** P04 (StorageClasses present in the seed), P10 (the ownership register is verified by backup/restore coverage), P12 (disk measurement), H02 (storage topology after expansion). `dag.yaml` does not yet list plan 11 on these nodes (STO-14, GC-09; §25 Q-11-1).
**Related plans:** [07](07-k3s.md), [08](08-node-security.md), [12](12-cnpg.md), [13](13-longhorn.md), [14](14-object-storage.md), [16](16-forgejo.md), [17](17-github-mirror.md), [24](24-hermes.md), [26](26-agent-runner.md), [35](35-secrets.md), [38](38-prometheus.md), [43](43-retention-gc.md), [44](44-backup.md), [45](45-recovery.md), [47](47-ha-expansion.md), [51](51-capacity.md)
**Contracts:** consumes `substrate.schema.yaml` / `cluster-profile.schema.yaml` (`storage.cnpg.storageClass: local`, `storage.longhorn.replicas`), `restore-test-result.schema.yaml` (plan 44)
**ADRs:** ADR-0005, ADR-0006, ADR-0012, ADR-0020; introduces ADR-0203 (StorageClass policy and stable-named Longhorn volumes)

## 1. Purpose

SPEC §24 says there is no universal storage mechanism, and each type of state uses the system best suited to replicate and recover it. This plan:

1. defines the three storage media inside the cluster (`local-path`, `longhorn`, `emptyDir`) and the one outside it (S3-compatible object storage, plan 14), plus the two off-cluster systems of record (GitHub, operator-held material);
2. is the **authoritative durable-state ownership register** (SPEC §2 "every durable state owner", §125 "durable-state ownership is defined"). It expands master plan §8;
3. defines what is **reconstructible**, which is deliberately not backed up, and why;
4. sets the node-disk budget that every storage consumer must fit, so that root disks do not fill (SPEC §107).

Mechanisms are owned elsewhere: CNPG (plan 12), Longhorn (plan 13), object storage (plan 14), retention (plan 43), and backup and restore testing (plan 44).

## 2. Requirements derived from SPEC.md

1. There SHALL NOT be one universal storage mechanism. Each state type uses the most appropriate replication and recovery system. [SPEC §24]
2. Important state outlives the process that holds it. Git, object storage, databases and external recovery credentials are preferred over local or ephemeral state. [SPEC §1.3, §4.1]
3. CNPG `PGDATA` SHALL normally use node-local persistent storage and SHALL NOT normally sit on Longhorn. [SPEC §25, §27]
4. Longhorn V1 provides replicated block storage for applications that cannot replicate themselves: Forgejo repositories, upload directories, ordinary stateful apps. It is not used for PGDATA, agent workspaces, CI scratch space or reconstructible caches. [SPEC §27]
5. Workers use `emptyDir` for clones, build trees and temporary outputs. CI uses disposable workspaces. [SPEC §28]
6. External S3-compatible storage is required at bootstrap, and the cluster is not the sole failure domain for critical backup data. [SPEC §29]
7. Forgejo relational state uses CNPG, Git repository storage uses Longhorn, and large suitable blobs use object storage. [SPEC §30]
8. OpenTofu state lives outside the cluster in S3. [SPEC §15]
9. Recovery objectives per SPEC §108 are mapped to concrete mechanisms (here per state item; per mechanism in plan 44).
10. Root disks SHALL NOT fill because cleanup policy was omitted. [SPEC §107]
11. Stage 0 defines every durable state owner. [SPEC §2, §125, §132 item 11]
12. **11-R-1** Every PVC and `volumeClaimTemplate` rendered from `gitops/` names its `storageClassName` explicitly. No workload relies on the default StorageClass.
13. **11-R-2** Every state item in §6 has exactly one system of record. Copies are labeled *derivative* and are never written back as authoritative without a procedure.
14. **11-R-3** Every non-reconstructible item has an off-cluster backup with a restore test (plan 44) or is itself off-cluster.
15. **11-R-4** Reconstructible items are recorded with their reconstruction source and the cost of losing them.

## 3. Inputs

- Profile values: `storage.cnpg.instances`, `storage.longhorn.replicas`, node disk size and class (contracts `substrate`, `cluster-profile`; plan 03).
- Node filesystem layout from plan 08: a single root filesystem on the VPS, with `/var/lib/rancher/k3s` and `/var/lib/longhorn` on it.
- Bucket catalogue from plan 14.
- Workload storage needs declared by the owning plans (16, 20, 24, 26, 38, 41, 44).

## 4. Outputs

- StorageClass policy (§9) and its GitOps manifests (plan 13 renders `longhorn` and `longhorn-scratch`; `local-path` is k3s-provided).
- The durable-state ownership register (§6), consumed by plans 43, 44, 45 and 35.
- The node-disk budget (§12), consumed by plans 08, 43 and 51.
- The `storage-audit` CronJob specification (§21), which checks this plan's invariants daily on the live cluster.

## 5. Dependencies

- **Hard (DAG):** P03 (k3s with `local-path-provisioner` enabled, plan 07) before P04. P01 (buckets, plan 14) before any backup configuration.
- **Soft:** plan 08 (disk layout, kubelet GC and eviction thresholds), plan 51 (capacity totals), plan 18 (GitOps layout and the `cluster-vars` substitution ConfigMap).
- **Runtime:** object storage (plan 14) for every backup path. GitHub (plan 17) for the `strange-company` off-cluster copy.

## 6. Owned state — the authoritative durable-state ownership register

Column legend. **Owner** = system of record. **Medium** = where the authoritative bytes live. **Writers/readers** = components with write/read access. **Repl.** = replication inside the cluster. **Backup** = mechanism → destination. **RPO/RTO** = target (SPEC §108 row or Stage-0 decision). **Restore test** = plan 44 test ID (or "n/a — reconstructible"). **Retention** = plan 43 row.

### 6.1 Git and Forgejo

| # | State | Owner | Medium | Writers | Readers | Repl. | Backup → destination | RPO / RTO | Restore test | Retention |
|---|---|---|---|---|---|---|---|---|---|---|
| S-01 | `strange-company` committed Git (all refs incl. `refs/pull/*`) | Forgejo repo | Longhorn `forgejo-data` | humans, `sc-runner` (PR refs), worker deploy keys (`agent/*`) | Flux (RO key), workers, CI, mirror | Longhorn replicas (1→2) | (a) Forgejo push mirror → GitHub on every push (plan 17); (b) hourly incremental + weekly full `git bundle` → `repo-backups` (plan 44); (c) daily Longhorn backup → `longhorn-backups` | ≤1 h (mirror near-zero) / ≤2 h [§108] | `rt-github-mirror`, `rt-repo-bundle`, `rt-blackstart-drill` | 43 R-17 |
| S-02 | Project Git repositories | Forgejo repos | Longhorn `forgejo-data` | humans, workers (`agent/*`), `sc-runner` | CI, workers | Longhorn | hourly/weekly bundles → `repo-backups`; daily Longhorn backup | ≤1 h / ≤4 h [§108] | `rt-repo-bundle`, `rt-longhorn-volume` | 43 R-17 |
| S-03 | Forgejo issues, PRs, reviews, comments, labels, releases metadata, users, orgs, deploy keys, webhooks, package metadata | `forgejo-pg` | CNPG `local-path` PGDATA | Forgejo | Forgejo | CNPG streaming (1→3) | WAL archive + daily base → `pg-backups/forgejo-pg/`; weekly per-repo metadata export → `repo-backups/meta/` | ≤15 min / ≤4 h [§108] | `rt-cnpg-pitr-forgejo-pg` | 43 R-14 |
| S-04 | Forgejo LFS, attachments, avatars, release assets, packages and OCI blobs, repo-archives | Forgejo storage | S3 `forgejo-objects` | Forgejo (trusted CI pushes packages through Forgejo) | Forgejo, image pulls | provider durability | bucket versioning; protection per plan 14 B7 (`scoped` in scoped mode, COMPLIANCE lock in shared mode) | ≤1 h / ≤4 h | `objstore-audit` (versioning), `oci-retention-audit` (plan 43) | 43 R-09, R-10 |
| S-05 | Forgejo SSH host keys | **SOPS Secret in Git** (plan 16/35) | Git | forge-init (once), rotation procedure | Forgejo; Flux `known_hosts` pin (ADR-0016) | Git | Git → GitHub mirror | n/a / with S-01 | `rt-github-mirror` (SOPS check) | Git history |
| S-06 | Forgejo `SECRET_KEY`, `INTERNAL_TOKEN`, JWT/LFS secrets (they encrypt DB columns such as 2FA secrets) | SOPS Secret in Git (plan 16/35) | Git | forge-init | Forgejo | Git | Git → mirror | n/a | `rt-github-mirror` | Git history |
| S-07 | Forgejo `/data` non-repository content (bleve issue indexer, temp, session files if any) | derivative | Longhorn `forgejo-data` | Forgejo | Forgejo | Longhorn | none needed: **reconstructible** (re-index) | n/a | n/a — reconstructible | 43 R-26 |
| S-08 | Forgejo `app.ini` non-secret configuration | Git (Helm values, plan 16) | Git | PR | Flux | Git | mirror | n/a | n/a | Git history |

### 6.2 Databases (CNPG, plan 12)

| # | State | Owner | Medium | Writers | Readers | Repl. | Backup → destination | RPO / RTO | Restore test | Retention |
|---|---|---|---|---|---|---|---|---|---|---|
| S-10 | Authentik users, groups, sessions, flows state, tokens | `authentik-pg` | CNPG `local-path` | Authentik | Authentik | CNPG | WAL + daily base → `pg-backups/authentik-pg/` | ≤15 min / ≤4 h [§108] | `rt-cnpg-pitr-authentik-pg` | 43 R-14 |
| S-11 | Authentik configuration (blueprints) | Git (plan 15) | Git | PR | Authentik blueprint loader | Git | mirror | n/a | n/a | Git |
| S-12 | Woodpecker pipelines, pipeline logs, repo activation, Woodpecker secrets store | `woodpecker-pg` | CNPG `local-path` | Woodpecker server | Woodpecker, Runner (API) | CNPG | WAL + daily base → `pg-backups/woodpecker-pg/` | ≤15 min / ≤4 h | `rt-cnpg-pitr-woodpecker-pg` | 43 R-19 |
| S-13 | Strange Company task records, transitions, TDD provenance, attempts, handoffs, worker results | `sc-pg` | CNPG `local-path` | Agent Runner | Runner, Gateway, Grafana (RO role) | CNPG | WAL + daily base → `pg-backups/sc-pg/` | ≤15 min / ≤4 h [§108] | `rt-cnpg-pitr-sc-pg` | 43 R-06 |
| S-14 | Invocation ledger (cost records), pricing versions used, budgets state, emergency-stop flag | `sc-pg` | CNPG `local-path` | Model Gateway | Gateway, Runner, Grafana | CNPG | as S-13; `cost_records` stay in `sc-pg` ≥ 3 years and are pruned only by an explicit operator call (plan 32); no archive bucket in MVP | ≤15 min / ≤4 h | `rt-cnpg-pitr-sc-pg` | 43 R-07 |
| S-15 | CNPG app credentials (`<cluster>-app`) | SOPS Secret in Git (`SC-PG-APP`, plan 35) | Git | bootstrap (generate once), rotation | CNPG (`bootstrap.*.secret`), apps | Git | mirror | n/a | `rt-github-mirror` | Git |
| S-16 | CNPG CA, server and replication certificates (`<cluster>-ca`, `-server`, `-replication`) | CNPG operator | Kubernetes Secrets (etcd) | CNPG | PG instances, clients that verify | etcd | none: **reconstructible** (CNPG regenerates; clients re-read the Secret on restart) | n/a | n/a | auto-renewed |
| S-17 | DR marker database `sc_dr` (one per cluster) | each CNPG cluster | PGDATA | `restore-tester` | `restore-tester` | CNPG | with the cluster | — | it *is* the test probe | 43 R-14 |

### 6.3 Evidence, logs and telemetry

| # | State | Owner | Medium | Writers | Readers | Repl. | Backup → destination | RPO / RTO | Restore test | Retention |
|---|---|---|---|---|---|---|---|---|---|---|
| S-20 | RED/GREEN evidence documents | S3 `evidence` (content), digest in `sc-pg` | S3 | Runner | Runner, reviewers (via Runner) | provider | versioning; protection per plan 14 B9 (`scoped`, or COMPLIANCE lock in shared mode) | ≤15 min / ≤4 h | `objstore-audit`; digest re-verification by Runner (plan 26) | 43 R-11 |
| S-21 | Worker logs (scrubbed) | S3 `evidence/logs/` + index row in `sc-pg` (plan 26/27) | S3 | Runner | Runner, Hermes via `agent_logs` | provider | versioning | best effort ≤24 h | n/a | 43 R-04 |
| S-22 | Restore-test results | S3 `evidence/restore-tests/` | S3 | `restore-tester`, operator drills | Prometheus (indirectly via CronJob status), humans | provider | versioning | n/a | `rt-drill-freshness` (plan 45 check CronJob; each test's `maxAge` alert, plan 44 §9.4) | 43 R-12 |
| S-23 | Woodpecker pipeline logs | `woodpecker-pg` (S-12) | PGDATA | Woodpecker | Runner, humans | CNPG | as S-12 | ≤15 min | as S-12 | 43 R-19 |
| S-24 | Prometheus TSDB | Prometheus | `local-path` PVC 10Gi (`prometheus-db`) | Prometheus | Grafana, Alertmanager | none | none: **not backed up**, best effort [§108] | best effort / non-critical | n/a | 43 R-01 |
| S-25 | Alertmanager silences and notification log | Alertmanager | `local-path` PVC 1Gi | Alertmanager | Alertmanager | none (small-ha: 2 replicas gossip) | none: **reconstructible** (silences are re-created by humans; nflog loss can cause one duplicate notification) | n/a | n/a | 43 R-27 |
| S-26 | Grafana dashboards, datasources | Git (provisioning, plan 39) | Git | PR | Grafana | Git | mirror | n/a | n/a | Git |
| S-27 | Grafana runtime DB (users from OIDC, preferences) | Grafana | `emptyDir` (SQLite) | Grafana | Grafana | none | none: **reconstructible**; stars and preferences are lost on restart, which is accepted | n/a | n/a | — |
| S-28 | Loki chunks and Tempo blocks (small-ha) | S3 `telemetry` | S3 (+ WAL on `emptyDir`: Loki 5 GiB, Tempo 3 GiB `sizeLimit`, plan 41; unflushed ≤ 2 h is lost on pod deletion, accepted as best effort) | Loki, Tempo | Grafana | provider | none beyond provider durability: best effort [§108] | ≤24 h best effort | n/a | 43 R-02, R-03 |
| S-29 | OTel Collector queues | — | memory | — | — | — | none: **ephemeral** (spans in flight are lost on restart) | n/a | n/a | — |

### 6.4 Hermes, workers and CI

| # | State | Owner | Medium | Writers | Readers | Repl. | Backup → destination | RPO / RTO | Restore test | Retention |
|---|---|---|---|---|---|---|---|---|---|---|
| S-30 | Hermes `/opt/data`: sessions, `state.db`, learned memory, `config.yaml`, `auth.json`, `.env` | Hermes (non-authoritative per SPEC §43; it is not a cache, L-1) | Longhorn `hermes-data` 5Gi | Hermes | Hermes | Longhorn | daily Longhorn backup → `longhorn-backups` | ≤24 h / ≤8 h [§108 critical Longhorn row] | `rt-longhorn-volume` (monthly rotation) | 43 R-25 |
| S-31 | Hermes managed configuration (gateway URL, tokens, OIDC, MCP definition) | Git (plan 24) + SOPS | Git | PR | Hermes (read-only mount) | Git | mirror | n/a | n/a | Git |
| S-32 | Agent workspaces (clones, build trees, outputs) | — | `emptyDir` with `sizeLimit` | worker | worker | none | none: **ephemeral by design** [SPEC §28]; durable output is pushed to Git | n/a | n/a | 43 R-21 |
| S-33 | Per-execution Secrets (deploy key, task token, gateway token) | Runner | Kubernetes Secret | Runner | worker | etcd | none: **ephemeral**; deleted at terminal state (ADR-0007) | n/a | n/a | plan 26 |
| S-34 | CI pipeline workspaces (one RWO PVC per Woodpecker workflow; the Kubernetes backend has no emptyDir option, ADR-0305) | — | `ci-workspace` PVC on the bounded node filesystem `/var/lib/sc/ci-workspace` | Woodpecker step pods | same | none | none: **ephemeral** [SPEC §28]; deleted at workflow end, orphans swept by `sc-ci-maint` (plan 20) | n/a | n/a | 43 R-21 |
| S-35 | Repo-backup mirror cache | derivative of S-01/S-02 | `local-path` PVC `repo-backup-cache` (ns `forgejo`) | `repo-backup` CronJob | same | none | none: **reconstructible** (re-clone) | n/a | n/a | 43 R-17 |
| S-36 | Renovate cache | — | `emptyDir` | Renovate | Renovate | none | none: **reconstructible** | n/a | n/a | — |

### 6.5 Kubernetes, GitOps and platform control state

| # | State | Owner | Medium | Writers | Readers | Repl. | Backup → destination | RPO / RTO | Restore test | Retention |
|---|---|---|---|---|---|---|---|---|---|---|
| S-40 | Desired state of all Kubernetes objects (manifests, HelmReleases, policies) | Git (`gitops/`, `policy/`) | Git | PR + human merge | Flux | Git + GitHub | mirror | as S-01 | `rt-github-mirror` | Git |
| S-41 | Live etcd contents (runtime objects, CNPG/Longhorn CR status, Secrets created at runtime) | k3s etcd | node disk `/var/lib/rancher/k3s/server/db` | API server | all | etcd raft (1→3 members) | k3s scheduled snapshots → local + S3 `etcd-snapshots` (plan 07) | **accelerator only**; Git is canonical [§110] | `rt-etcd-snapshot` | 43 R-16 |
| S-42 | Flux runtime state (source artifacts, inventories, HelmRelease history Secrets) | Flux | `emptyDir` (source-controller storage) + etcd | Flux | Flux | — | none: **reconstructible** from Git (HelmRelease history Secrets are rebuilt on the first upgrade after rebuild) | n/a | n/a | 43 R-28 |
| S-43 | cert-manager issued certificate Secrets | cert-manager | etcd | cert-manager | Traefik, apps | etcd | none: **reconstructible** (re-issue). Hazard: ACME rate limits in direct mode (plan 10) | n/a | n/a | auto-renewed |
| S-44 | Internal CA key material (if plan 10 uses a self-signed CA issuer) | **plan 10 decides**; this plan requires: if the CA must survive rebuild, it is a SOPS Secret in Git | Git or etcd | plan 10 | cert-manager | — | per plan 10 | — | — | — |
| S-45 | ACME account key | cert-manager | etcd | cert-manager | cert-manager | etcd | none: **reconstructible** (new account) | n/a | n/a | — |
| S-46 | Longhorn metadata: Volume, Replica, Engine, Setting, RecurringJob, BackupTarget CRs | Longhorn (settings and recurring jobs from Git via Helm values and manifests) | etcd | longhorn-manager | longhorn | etcd | settings/jobs: Git. Volume CRs for important volumes: Git (stable names, ADR-0203). Backup-volume metadata: in the backup target itself (`longhorn-backups`) | n/a | `rt-longhorn-canary` | — |
| S-47 | Longhorn replica data | Longhorn | node disk `/var/lib/longhorn` | Longhorn engine | Longhorn | Longhorn replicas | see S-01/S-02/S-30 | per volume | per volume | 43 R-13 |
| S-48 | Container images cached on nodes | containerd | node disk | kubelet | kubelet | — | none: **reconstructible** (re-pull) | n/a | n/a | 43 R-22 |
| S-49 | Internal images (released) | Forgejo OCI registry | S3 `forgejo-objects/packages/` + `forgejo-pg` metadata | trusted CI (protected refs only) | kubelet | provider | bucket versioning; images are rebuildable from tagged source (not bit-identical) | ≤1 h / ≤4 h | `oci-retention-audit` | 43 R-09 |
| S-50 | Node OS configuration | Git (Ansible, plan 08) | Git | PR | Ansible | Git | mirror | n/a | n/a | Git |
| S-51 | k3s server CA and certificates, token-encrypted bootstrap data | k3s | node disk + etcd | k3s | k3s | etcd | inside etcd snapshots (restorable with `SC-K3S-TOKEN`); otherwise **reconstructible** (new CA on rebuild; kubeconfigs re-issued) | n/a | `rt-etcd-snapshot` (integrity only) | — |

### 6.6 Bootstrap, infrastructure and recovery material (off-cluster)

| # | State | Owner | Medium | Writers | Readers | Repl. | Backup → destination | RPO / RTO | Restore test | Retention |
|---|---|---|---|---|---|---|---|---|---|---|
| S-60 | OpenTofu state (all root modules incl. `storage`) | S3 `tofu-state` (plan 06) | S3, versioned, conditional-write locked, OpenTofu state encryption | toolbox (`SC-OBJECT-STORAGE/tofu-state`; `admin` only for the storage module) | toolbox | provider | versioning per plan 14 B1 (noncurrent 365 d, newest 100 kept; no Object Lock; separate account in shared mode) | latest successful mutation / ≤2 h [§108] | `rt-tofu-state` (operator, monthly + at every `./bootstrap plan`) | 43 R-29 |
| S-61 | Bootstrap journal | local `.bootstrap/journal/` (working copy) + S3 `tofu-state/<install>/journal/` (system of record after P01) | S3 | `sc-bootstrap` | `sc-bootstrap` | provider | versioning | latest phase / n/a | exercised by `bootstrap-resume` (plan 50) | 43 R-29 |
| S-62 | Bootstrap scratch state (rendered plans, temp files, downloaded artifacts) | — | local `.bootstrap/tmp/` | `sc-bootstrap` | `sc-bootstrap` | none | none: **ephemeral**. Plaintext secrets are never written there (plan 50) | n/a | n/a | 43 R-30 |
| S-63 | Seed snapshots (`git archive` of the seeded commit) | S3 `bootstrap-seed` | S3 | toolbox | Flux source-controller (seed only) | provider | none: **reconstructible** from Git; kept for audit | n/a | n/a | 43 R-31 |
| S-64 | Recovery bundle (passphrase-encrypted) | operator + S3 `recovery` (plan 35) | S3 + operator media | toolbox | operator | provider + operator copies | versioning per plan 14 B2 (noncurrent 3650 d, Object Lock 365 d) | n/a / ≤2 h to unlock [master §8] | `rt-blackstart-drill` (operator, quarterly); `rt-github-mirror` checks the bundle manifest | 43 R-32 |
| S-65 | Offline recovery age identity `SC-AGE-RECOVERY`, recovery passphrase | operator only | operator media | operator | operator | operator responsibility | n/a | n/a | `rt-blackstart-drill` | operator |
| S-66 | Cluster age identity `SC-AGE-CLUSTER` | recovery bundle | `flux-system/sops-age` + bundle | bootstrap | kustomize-controller | etcd | bundle | n/a | `rt-blackstart-drill` | plan 35 |
| S-67 | Backup age identity `SC-AGE-BACKUP` (new, see §10) | recovery bundle | `dr-tests/backup-age` + `forgejo/backup-age-recipients` (public part) + bundle | bootstrap | `restore-tester` (private), `repo-backup` (public recipients only) | etcd | bundle | n/a | `rt-repo-bundle` | plan 35 |
| S-68 | `strange-company` GitHub mirror | GitHub (plan 17) | GitHub | Forgejo push mirror only | black start, humans | GitHub | it *is* the off-cluster copy | ≤1 h | `rt-github-mirror` | GitHub |
| S-69 | DNS records, tunnel configuration | provider APIs via OpenTofu (plans 04, 09) | provider | toolbox | provider | provider | tofu state + Git | n/a | `rt-tofu-state` | — |

### 6.7 Reconstructible state: deliberately not backed up

| State | Reconstruction source | Cost of loss |
|---|---|---|
| Prometheus TSDB (S-24) | none; history lost | metric history; SPEC §108 says best effort |
| Alertmanager silences (S-25) | humans re-silence | possible duplicate notifications |
| Grafana runtime DB (S-27) | OIDC re-login; dashboards from Git | user preferences |
| CNPG certificates (S-16) | operator regenerates | client pod restarts |
| cert-manager certificates (S-43, S-45) | re-issue | ACME rate-limit exposure in direct mode |
| Flux runtime (S-42) | Git | one reconcile cycle |
| Forgejo indexer (S-07) | Forgejo re-index (`forgejo admin` index rebuild, verify) | search unavailable for minutes |
| Repo-backup cache (S-35), Renovate cache (S-36) | re-clone | one slow backup run |
| Seed snapshots (S-63) | `git archive` of any commit | none |
| Container images on nodes (S-48) | re-pull | pull time; and if `forgejo-objects` is also unavailable, internal images cannot be pulled (plan 14 outage table) |
| Worker and CI workspaces (S-32, S-34) | Git | the in-flight attempt (counted as an `infrastructure` failure) |
| k3s CA (S-51) on full rebuild | new CA | kubeconfigs re-issued (plan 07) |

## 7. External interfaces

- **StorageClass names** (stable API for every plan): `local-path`, `longhorn`, `longhorn-scratch`, `ci-workspace` (ADR-0305, plan 20). No other StorageClass may be referenced without amending this plan.
- **Register IDs** `S-NN`, cited by plans 43, 44, 45 and 35.
- **`cluster-vars` keys** (Flux postBuild substitution, plan 18 owns the mechanism): `LONGHORN_REPLICAS` (`1` | `2`), `CNPG_INSTANCES` (`1` | `3`), `STORAGE_PROFILE` (`preferred` | `constrained`).
- **Labels:** `strange.company/state-id: S-NN` on every PVC, CNPG `Cluster` and Longhorn `Volume` that holds a register item. `storage-audit` uses it.

## 8. Internal interfaces

- Plan 12 consumes `local-path` for PGDATA. Plan 13 owns the `longhorn` and `longhorn-scratch` definitions. Plan 14 owns the buckets. Plan 44 owns the backup jobs and restore tests that prove §6.
- `storage-audit` (a mode of the `sc-restore-test` binary, CronJob in `dr-tests`) checks the invariants in §21.

## 9. Configuration

### 9.1 StorageClass policy (ADR-0203)

| StorageClass | Provisioner | Default? | Reclaim | Binding | Used by | Not allowed for |
|---|---|---|---|---|---|---|
| `local-path` | k3s `rancher.io/local-path` (bundled) | **yes** (k3s default, unchanged) | Delete | WaitForFirstConsumer | CNPG PGDATA (all four clusters and restore-test scratch clusters), Prometheus TSDB, Alertmanager, `repo-backup-cache` | Forgejo repositories, Hermes data, anything in §6 marked "Longhorn" |
| `longhorn` | `driver.longhorn.io` | **no** | **Retain** | Immediate | Forgejo `/data` (`forgejo-data`), Hermes `/opt/data` (`hermes-data`), `dr-canary` | PGDATA, agent workspaces, CI scratch, caches [SPEC §27] |
| `longhorn-scratch` | `driver.longhorn.io` | no | Delete | Immediate | restore-test volumes only (`rt-*`) | everything else |
| `ci-workspace` (ADR-0305, owned by plan 20; filesystem by plan 08) | a **separate** local-path-provisioner instance (provisioner name `strange.company/ci-workspace`, own Deployment and ConfigMap, Flux-managed, independent of the k3s-managed `local-path-config` addon that k3s re-applies on restart; STO-8, requested from plan 20/ADR-0305) with base path `/var/lib/sc/ci-workspace` (a dedicated, size-bounded filesystem, not the root filesystem) | no | Delete | WaitForFirstConsumer | Woodpecker workflow workspaces in `ci-jobs` and `ci-build` only, created by the agent ServiceAccounts, request ≤ 8Gi (plan 37) | everything else |
| (`emptyDir`) | — | — | — | — | workers, CI pods, Grafana, Flux source storage, Renovate | durable state |

Decision rationale: `local-path` stays default because k3s ships it so, and changing it fights the k3s addon reconciler. Defaults do not matter anyway, because 11-R-1 forbids relying on them. That is enforced at render time (F11 check `pvc-explicit-storageclass`) and at runtime (`storage-audit`). `longhorn` is explicitly not default, so that a forgotten class can never silently put a cache onto replicated storage. `Retain` on `longhorn` protects Forgejo and Hermes data from an accidental PVC deletion.

`local-path` has **no capacity enforcement**: PVC `size` is nominal, and volumes are hostPath directories on the root filesystem. Every `local-path` consumer therefore has an application-level bound (PostgreSQL alerts on database size, Prometheus `retentionSize`, Alertmanager retention) and is covered by node filesystem alerts (§21). This is the main reason the disk budget in §12 exists.

### 9.2 Workload-to-medium map

| Workload | Namespace | Medium | Name | Nominal size (preferred / constrained) |
|---|---|---|---|---|
| `forgejo-pg` | forgejo | local-path | CNPG-managed | 6Gi / 4Gi |
| `authentik-pg` | authentik | local-path | CNPG-managed | 3Gi / 2Gi |
| `woodpecker-pg` | woodpecker | local-path | CNPG-managed | 8Gi / 4Gi (with Woodpecker log retention 30 d, plan 43 R-19; plan 20 asked for 12Gi if it kept 90 d) |
| `sc-pg` | strange-company | local-path | CNPG-managed | 10Gi / 6Gi |
| Forgejo `/data` | forgejo | longhorn | `forgejo-data` | 20Gi / 12Gi |
| Hermes `/opt/data` | hermes | longhorn | `hermes-data` | 5Gi / 3Gi |
| DR canary | dr-tests | longhorn | `dr-canary` | 1Gi / 1Gi |
| Prometheus | monitoring | local-path | `prometheus-db` | 10Gi / 5Gi |
| Alertmanager | monitoring | local-path | `alertmanager-db` | 1Gi / 1Gi |
| Repo-backup cache | forgejo | local-path | `repo-backup-cache` | 10Gi / 5Gi |
| Worker Job | agent-jobs | emptyDir | `workspace` | sizeLimit 8Gi / 5Gi (plan 26) |
| CI workflow workspace | ci-jobs, ci-build | `ci-workspace` PVC | per workflow (Woodpecker-named) | 6Gi (ci) / 10Gi (build), plan 20; on a filesystem of 12 GiB at self-hosting-minimum and 24 GiB at small-ha (CD-06) |
| Restore-test scratch | dr-tests | local-path / longhorn-scratch | `rt-*` | ≤ 2 × source actual size, capacity-gated (plan 44) |

Who may change it: a PR to the files in `gitops/` that define these objects, reviewed against §12.

## 10. Secrets

Consumed (plan 35 IDs): `SC-PG-APP`, `SC-OBJECT-STORAGE/<instance>` (instance split defined in plan 14 §10), `SC-AGE-CLUSTER`, `SC-AGE-RECOVERY`, `SC-RECOVERY-PASSPHRASE`, `SC-TOFU-ENCRYPTION`, `SC-K3S-TOKEN`.

New classes introduced by the G3 plans, which plan 35 must register:
- `SC-AGE-BACKUP`: age identity for repository-level backup encryption (plan 44, ADR-0202). Creator: bootstrap. Private key held in `dr-tests/backup-age` and in the recovery bundle. Public recipient in `forgejo/backup-age-recipients` (ConfigMap). Rotation: new recipient for new bundles; the old identity is kept in the bundle until the last bundle encrypted to it expires (56 d weekly chains, 400 d monthly fulls; plan 43 R-17).
- `SC-PG-DR-MARKER`: per-cluster password for the `sc_dr` role (plan 12/44).
- New `SC-OBJECT-STORAGE` instances (plan 14 §10): `pg-backups-<cluster>`, `seed-write`, `tofu-state-ro`, `dr-tests-results`, `objstore-audit`, `telemetry-debug`, `drill-reader`.

## 11. Storage

This whole plan is about storage. See §6 and §9.

## 12. Resource requirements — node-disk budget

Single root filesystem per node (plan 08), except the bounded `ci-workspace` filesystem (ADR-0305), which is carved out of the same disk. The budget is split into a **fixed** part (independent of how much data the installation holds) and a **data** part, and is designed so that year-one usage stays **≤ 70 %** of the disk: below the `SCNodeRootDiskLow` warning (75 % used), kubelet soft eviction (`nodefs.available` 15 %, plan 51), Longhorn's scheduling floor (`storageMinimalAvailablePercentage: 15`) and the 43-AC-8 projection. STO-3 (review) showed that the earlier budget allocated 95 % and contradicted all of these; SPEC v1.2 (CD-25) then raised the disk sizes, which this section uses.

### 12.1 Self-hosting-minimum (one node)

Disk sizes follow SPEC v1.2 (CD-25): **minimum 100 GiB, preferred 200 GiB NVMe** per node for both profiles; 75 GiB nodes are rejected by preflight (plans 03/05). The memory profile (`preferred` 12 GiB / `constrained` 8 GiB, ADR-0020, CD-23) is independent of the disk size; the CPU/memory-driven caps (Prometheus `retentionSize`) follow plan 38 for the memory profile.

**Fixed part** (GiB):

| Consumer | Minimum 100 GiB disk | Preferred 200 GiB disk | Bound / enforcement |
|---|---:|---:|---|
| OS, packages, journald (1 GiB cap), k3s audit log (≤ 300 MiB) | 8 | 8 | plan 08 journald, plan 07 audit flags |
| k3s binaries, etcd DB, local etcd snapshots (28 + ≤ 10 `pre-*`, compressed) | 4 | 4 | plan 07 snapshot retention |
| containerd images (working set; GC at 80 % is a backstop, not an allocation) | 10 | 15 | kubelet image GC 80 → 70 %, max age 30 d (plan 43 R-22); plan 51 uses the same figures |
| Pod logs | 3 | 3 | `containerLogMaxSize 10Mi` × 3 |
| Prometheus + Alertmanager | 9 | 9 | `retentionSize` 8 GiB (5 GiB on `constrained` memory) + 1 GiB (plan 38) |
| `ci-workspace` filesystem (CD-06) | 12 | 12 | fixed filesystem; `SCCIWorkspaceFilesystemHigh` (plan 20) |
| Heavy-execution scratch baseline: one worker `emptyDir` (exclusive with heavy restore tests through `strange.company/heavy-slot`, plan 51) | 8 | 8 | `emptyDir.sizeLimit` |
| **Fixed total** | **54** | **59** | |

**Data part** (grows with the installation): with `G` = Git data in `forgejo-data`, `P` = sum of PGDATA of the four clusters, `H` = `hermes-data` actual size:

| Consumer | Size | Bound |
|---|---|---|
| CNPG PGDATA + `pg_wal` | `P` + 1 GiB WAL headroom | `SCCnpgDatabaseLarge` (sum of clusters > 80 % of the data ceiling's `P`, plan 12) |
| Longhorn (one replica on this node) | `G` + `H` + ≈ 15 % snapshot overhead | snapshot caps (plan 13), `SCLonghornNodeStorageLow` |
| Repo-backup cache (mirror of all repositories; STO-15) | ≈ 1.1 × `G` | bundle job exports `cache_bytes`; RA-17 (plan 43) |
| Extra restore scratch above the 8 GiB baseline | max(0, 1.2 × max(`G`, largest single DB) + 3 − 8) | capacity gate (plan 44 §9.3) |

**Data ceiling** (data part ≤ 70 % of disk − fixed part): 16 GiB on 100 GiB, 81 GiB on 200 GiB. Solving with `H` ≈ 2 GiB gives approximately:

| Disk | Git data `G` | PGDATA `P` | Meaning |
|---|---:|---:|---|
| 100 GiB (minimum) | ≤ 4 GiB | ≤ 4.5 GiB | install-capable; a small team's first months; above this, grow the disk (OVH VPS-3, CD-24) or move to small-ha |
| 200 GiB (preferred) | ≤ 18 GiB | ≤ 18 GiB | the design target: one year of growth for a small team at the plan 32 ledger rate and typical repository growth |

The installation reports `G`, `P` and the remaining headroom (and months until the ceiling at the measured growth rate, plan 43 43-AC-8) in `./bootstrap verify` (section `disk`). `SCNodeRootDiskFillingUp` (24 h projection) plus `SCNodeRootDiskLow` (75 %) give notice before any eviction threshold.

### 12.2 Small-ha (per node, worst-case node)

At small-ha every node carries one instance of each CNPG cluster, and one node additionally carries the primaries, a `forgejo-data` replica, the Loki/Tempo WAL and the bundle cache. Worst-case node (STO-4):

| Consumer | GiB |
|---|---:|
| Fixed part as in §12.1 (minimum-disk column), but `ci-workspace` 24 GiB (CD-06) and Prometheus + Alertmanager per replica | 66 (71 on 200 GiB, images 15) |
| PGDATA of four instances (`P`) + WAL retained for a down replica (`max_slot_wal_keep_size` 2 GiB × 4) | `P` + 8 |
| `forgejo-data` replica (`G` + snapshots) + `hermes-data` replica | 1.15 × `G` + 2 |
| Loki/Tempo WAL `emptyDir` (plan 41) | 8 |
| Repo-backup cache | 1.1 × `G` |
| Extra restore scratch | max(0, 1.2 × max(`G`, largest DB) + 3 − 8) |

| Disk per node | Data that fits at ≤ 70 % | Meaning |
|---|---|---|
| 100 GiB (minimum) | ≈ 4 GiB in total (`G` ≈ 0) | not usable for small-ha with Loki/Tempo and a 24 GiB `ci-workspace`; plan 47's expansion precondition rejects it |
| 200 GiB (preferred) | `G` ≈ 12 GiB and `P` ≈ 12 GiB (≈ 137 GiB used of 140) | the small-ha target |

Plan 47 uses this table for its expansion precondition (small-ha requires the preferred 200 GiB per node in practice), and 11-AC-10 measures it.

### 12.3 Thresholds

Rules are owned by plan 42's catalogue (signals by plans 08/38): `SCNodeRootDiskLow` at < 25 % free (75 % used, warning), `SCNodeRootDiskCritical` at < 15 % free (critical), kubelet soft eviction `nodefs.available<15%` and hard `nodefs.available<10%` (plan 51 §9.3), Longhorn scheduling floor 15 %, and the predictive `SCNodeRootDiskFillingUp` (plan 43 §21). Image GC (80 % used) is a backstop above the 70 % design target. Gate P12 measures actual usage after install (≤ 40 %, 11-AC-9).

## 13. Security boundary

- `local-path` volumes are hostPath directories owned by the provisioner. Only pods bound to the PVC can mount them. PSA `restricted` namespaces cannot use raw hostPath (plan 37), so `local-path` access is only through PVCs.
- Longhorn volumes are block devices exposed through CSI. Longhorn management (`longhorn-system`) is reachable only from the API server and from Prometheus (plan 36). Workers have no network path to it [SPEC §88].
- Backups leave the cluster only to object storage, under per-purpose credentials (plan 14). Repository bundles are additionally age-encrypted (ADR-0202).
- No workload in `agent-jobs` may mount any PVC (VAP rule, plan 37). In `ci-jobs` and `ci-build`, only PVCs of class `ci-workspace` (≤ 8Gi, created by the Woodpecker agent ServiceAccount) are allowed, because the Woodpecker Kubernetes backend requires one per workflow (ADR-0305); every other volume there is `emptyDir`.

## 14. Happy path

1. P03: k3s comes up with `local-path-provisioner`, and `local-path` is the default class.
2. P04 (seed): Longhorn is installed with `longhorn` and `longhorn-scratch` (non-default). `forgejo-pg` is created on `local-path`. Forgejo's `forgejo-data` Volume, PV and PVC are created with stable names.
3. P06 onwards: every other stateful component is created against the medium in §9.2.
4. P10: backups and restore tests (plan 44) cover every non-reconstructible row of §6. `storage-audit` passes.
5. H02: Longhorn replicas 1→2 and CNPG instances 1→3. Data is spread over three node disks (plans 12, 13, 47).

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| A PVC is rendered without an explicit class | F11 render check; `storage-audit` | it would land on `local-path` (node-local) | CI fails; `SCStorageAuditFailed` | fix the manifest |
| `longhorn` accidentally marked default | `storage-audit` | new PVCs replicate unnecessarily | `SCStorageAuditFailed` | revert in Git |
| Root filesystem fills (any consumer) | `SCNodeRootDiskLow`/`SCNodeRootDiskCritical`/`SCNodeRootDiskFillingUp`, kubelet `DiskPressure` (`SCNodeDiskPressure`) | evictions; etcd and PG at risk | kubelet image GC, eviction of lowest-priority pods first (plan 51 priorities) | `disk-pressure.md` (plan 08/43) |
| Node disk lost (1 node) | node down | all local-path and Longhorn data on it is lost | none | `total-cluster-loss.md` (plan 45): CNPG and Longhorn restore from S3 |
| Node lost (small-ha) | `SCNodeDown`, CNPG/Longhorn alerts | the replica on that node is lost | CNPG failover; Longhorn rebuild on a surviving node | `lost-node.md` (plan 47), `cnpg-replica-loss.md`, `longhorn-replica-loss.md` |
| A register item has no working backup | restore-test failure or staleness | the RPO is not met | alert | `restore-test-failure.md` |
| Object storage outage | `SCObjectStorageUnreachable` / `SCObjectStorageCanaryFailing` | backups pause; WAL accumulates on disk | none destructive | `object-storage-outage.md` |

## 16. Retry behavior

N/A at this layer. Retries belong to the mechanisms: CNPG WAL archive retries (plan 12), Longhorn recurring jobs (plan 13), CronJob `backoffLimit` (plan 44).

## 17. Idempotency behavior

StorageClasses and stable-named volumes are declarative Flux objects. Re-applying them is a no-op. Longhorn `Volume` CRs carry `kustomize.toolkit.fluxcd.io/ssa: IfNotPresent`, so Flux creates them once and never patches the mutable spec back (ADR-0203). `storage-audit` is read-only.

## 18. Bootstrap behavior

The seed set (plan 19) includes the StorageClasses and the stable-named `forgejo-data` volume. `local-path` must be Ready before `forgejo-pg` is applied. CNPG PVCs use WaitForFirstConsumer, so they bind on the node where the instance schedules. During black start the same objects are applied with the recovery overlay (plans 12, 13, 45): PGDATA comes from `bootstrap.recovery`, and Longhorn volumes come from `fromBackup`.

## 19. Upgrade behavior

- A k3s upgrade may update the bundled `local-path-provisioner`. Existing hostPath directories are unaffected. 11-AC-3 re-checks the default-class annotation after every k3s upgrade (plan 48).
- Longhorn upgrade constraints are in plan 13 §19.
- Changing a workload's medium (e.g. moving Hermes to another class) is a data migration. It requires a plan amendment and a procedure. It is never done by editing `storageClassName` in place, because the field is immutable.

## 20. Recovery behavior

For each register row, the recovery path is the "Backup → destination" column, executed by the procedure listed in plan 44 §6 and the DR order in plan 45. Reconstructible rows (§6.7) are recovered by redeploying from Git.

## 21. Observability

`storage-audit` CronJob (ns `dr-tests`, daily 07:15 UTC, read-only RBAC on StorageClasses, PVCs, CNPG `Cluster`s and Longhorn `Volume`s). It exits non-zero if any check fails, and writes a `restore-test-result` document with `test_id: storage-audit`. Checks:

| Check | Expected |
|---|---|
| SA-1 | exactly one default StorageClass, `local-path` |
| SA-2 | `longhorn` is not default and has `reclaimPolicy: Retain` |
| SA-3 | every PVC in the cluster has a non-empty `spec.storageClassName` from {`local-path`, `longhorn`, `longhorn-scratch`, `ci-workspace`}; `ci-workspace` only in `ci-jobs`/`ci-build`, and those namespaces use no other class |
| SA-4 | no PVC in `agent-jobs`; no PVC in `ci-jobs`/`ci-build` older than 2 h (orphans that `sc-ci-maint` failed to sweep, ADR-0305) |
| SA-5 | every PVC, `Cluster` and Longhorn `Volume` labeled `strange.company/state-id` maps to a row of §6, and every §6 row with an in-cluster medium has at least one labeled object |
| SA-6 | CNPG `Cluster.spec.instances` == `CNPG_INSTANCES`; every PGDATA PVC class == `local-path` |
| SA-7 | every Longhorn `Volume` with class `longhorn` has `spec.numberOfReplicas` == `LONGHORN_REPLICAS` |
| SA-8 | every PV of class `ci-workspace` has its path (`.spec.hostPath.path` or `.spec.local.path`) under `/var/lib/sc/ci-workspace/`, so a lost provisioner mapping cannot silently put CI workspaces on `/` (STO-8) |

Alerts (catalogued by plan 42):
- `SCStorageAuditFailed` (warning): `time() - kube_cronjob_status_last_successful_time{namespace="dr-tests",cronjob="storage-audit"} > 129600` (36 h).
- Node filesystem alerts are catalogued by plan 42 (`SCNodeRootDiskLow`, `SCNodeRootDiskCritical`, `SCNodeDiskPressure`; predictive `SCNodeRootDiskFillingUp` proposed by plan 43) at the thresholds in §12.

## 22. Testing strategy

- **F11 (offline):** a render check fails on any PVC or `volumeClaimTemplate` without `storageClassName`, any class outside the allowed set, any `longhorn` use in a namespace not listed in §9.2, any PVC in `agent-jobs`, and any PVC in `ci-jobs`/`ci-build` of a class other than `ci-workspace`.
- **k3d (F12 bundle):** `storage-audit` runs against a k3d cluster seeded with a compliant manifest set (passes) and with each violation injected (fails with the matching check ID). Longhorn is replaced by a fake StorageClass named `longhorn` for the audit-only tests.
- **Live (P04, P10, P12, H02):** the acceptance criteria below.

## 23. Acceptance criteria

### GATE-P04
- **11-AC-1** `./bootstrap exec -- kubectl get storageclass -o jsonpath='{range .items[*]}{.metadata.name}{"|"}{.metadata.annotations.storageclass\.kubernetes\.io/is-default-class}{"|"}{.reclaimPolicy}{"\n"}{end}'` outputs exactly the four lines `local-path|true|Delete`, `longhorn||Retain` (or `longhorn|false|Retain`) `longhorn-scratch||Delete` (or `|false|`) and `ci-workspace||Delete` (or `|false|`; plan 20 renders it at P08, so at P04 three lines are expected).
- **11-AC-2** `./bootstrap exec -- kubectl -n forgejo get pvc forgejo-data -o jsonpath='{.spec.storageClassName}/{.spec.volumeName}'` prints `longhorn/forgejo-data`, and the PGDATA PVC of `forgejo-pg` (`kubectl -n forgejo get pvc -l cnpg.io/cluster=forgejo-pg -o jsonpath='{.items[*].spec.storageClassName}'`) prints `local-path`.
- **11-AC-3** `make render-check` (F11) fails with check `pvc-explicit-storageclass` on a fixture PVC lacking `storageClassName`, and passes on the real tree.

### GATE-P10
- **11-AC-4** `storage-audit` has run once successfully (SA-8 is evaluated from P08 on, when `ci-workspace` exists). `kubectl -n dr-tests get cronjob storage-audit -o jsonpath='{.status.lastSuccessfulTime}'` is non-empty, and the latest result document in `evidence/restore-tests/storage-audit/` has `outcome: passed` with checks SA-1…SA-7 all passed.
- **11-AC-5** Every §6 row marked with a backup has a passing restore test or check, listed in plan 44's catalogue. `sc-restore-test coverage --register plans/11-storage.md` (a plan-44 tool mode) exits 0, and reports zero rows without a test ID except rows marked reconstructible, ephemeral or Git.
- **11-AC-6** `./bootstrap exec -- kubectl get pvc -A -o json | jq '[.items[] | select(.metadata.namespace=="agent-jobs" or ((.metadata.namespace=="ci-jobs" or .metadata.namespace=="ci-build") and .spec.storageClassName!="ci-workspace"))] | length'` prints `0`.

### GATE-P12
- **11-AC-9** The disk-budget measurement in the bootstrap verification report (`./bootstrap verify` section `disk`) shows root-filesystem usage ≤ 40 % on the installed node, and lists per-consumer usage for every line of §12.

### GATE-H02
- **11-AC-7** After expansion, `storage-audit` passes with `CNPG_INSTANCES=3` and `LONGHORN_REPLICAS=2` (checks SA-6 and SA-7).
- **11-AC-8** `kubectl get pods -A -l cnpg.io/podRole=instance -o jsonpath='{range .items[*]}{.metadata.labels.cnpg\.io/cluster}{" "}{.spec.nodeName}{"\n"}{end}' | sort -u | awk '{print $1}' | uniq -c` shows `3` for each of the four clusters (one instance per node).
- **11-AC-10** Small-ha disk (STO-4): after H02, `./bootstrap verify` section `disk` reports, per node, root-filesystem usage and the §12.2 worst-case projection from measured `G` and `P`; every node's projected usage is ≤ 70 %, or the report names the node and the missing GiB (the gate fails).

## 24. Explicit non-goals

- No universal storage layer, distributed filesystem or RWX volumes [SPEC §24, §127].
- No backup of reconstructible state (§6.7).
- No capacity quotas on `local-path` (it cannot enforce them); bounding is application-level.
- No Longhorn V2 (SPDK) engine.

## 25. Open questions

- **Q-11-1** (blocking: no) STO-14/GC-09: add `"11"` to the `plans` of P04, P10, P12 and H02 in `dag.yaml`. Owner: lead (dag.yaml).
- **Q-11-2** (resolved by CD-25, SPEC v1.2) SI-G3-01/SI-32: node disk minimum 100 GiB and preferred 200 GiB for both profiles; §12 is re-baselined. Residual, recorded: a 100 GiB node holds only the §12.1 data ceiling (Git ≈ 4 GiB, PGDATA ≈ 4.5 GiB), and small-ha needs the preferred 200 GiB per node (§12.2). The measured gates 11-AC-9, 11-AC-10 and 43-AC-8 remain. Owner: plans 51, 47.
- **Q-11-3** (blocking: no) Hermes `/opt/data` may hold credentials that were entered through the dashboard (L-1). Longhorn backups are protected only by provider SSE (no client-side encryption, plan 13 §10). Proposed: plan 24 forbids dashboard credential entry through managed scope, so `/opt/data` holds no provider keys. Owner: plan 24.
- **Q-11-4** (blocking: no) The internal CA's survivability (S-44) is plan 10's decision. If plan 10 chooses a self-signed CA that clients pin, the CA key must be a SOPS Secret in Git. Owner: plan 10.
- **Q-11-6** (blocking: no) STO-25: the register is Markdown parsed by `sc-restore-test coverage`. Proposed: move it to `infra/storage/register.yaml` with a new contract `storage-register.schema.yaml` (to be added at F02) and render §6 from it at implementation (F02). Deferred: it does not change any decision, and the Markdown parser is covered by 11-AC-5's negative fixture. Owner: 11.
- **Q-11-5** (resolved by CD-06) SI-G3-05: the `ci-workspace` filesystem is 12 GiB at self-hosting-minimum and 24 GiB at small-ha; PVC caps 8Gi (`ci-jobs`, default 6Gi) and 10Gi (`ci-build`). §12 already uses these values.
