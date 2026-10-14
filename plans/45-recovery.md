# 45 — Disaster recovery and black start

**Status:** Stage-0 plan (draft)
**DAG nodes:** H04 (co-owner with 47: HA hardening drills, including the RD3 scratch black start, and the bootstrap capsule of §9.4, CD-33); P10 (co-owner: the recovery pipeline proven in k3d, 45-AC-3/4/7/9/12–16 — lead adds plans 45 and 50 to P10 in `dag.yaml`, DR-17); contributes to P12 (state-only check), P06 (black-start fixture for 19-AC-14)
**Related plans:** 03 (provider), 06 (OpenTofu state), 07 (etcd restore vs rebuild), 11 (ownership register), 12 (CNPG recovery, generations), 13 (Longhorn restore), 14 (buckets), 15 (Authentik, break-glass), 16 (Forgejo, forge-init `--recover`), 17 (GitHub mirror, `forge-reconnect`), 18 (GitOps tree), 19 (seed and transfer, black-start variant), 26 (Runner reconciliation), 33 (emergency stop), 35 (recovery bundle, fencing rotations), 42 (alerts), 43 (retention), 44 (backups and restore tests), 47 (HA expansion), 49 (destroy), 50 (`./bootstrap recover`)
**Contracts:** owns `contracts/recovery-record.schema.yaml` (§7.4); consumes `recovery-bundle.schema.yaml`, `bootstrap-journal.schema.yaml`, `bootstrap-config.schema.yaml`, `restore-test-result.schema.yaml`
**ADRs:** ADR-0005, ADR-0012, ADR-0554 (recovery overlay; owned here), ADR-0158 (plan 07: etcd restore vs rebuild), ADR-0200 (plan 12: backup generations)
**Procedures (owned):** `procedures/total-cluster-loss.md`, `procedures/github-black-start.md`, `procedures/recovery-drill.md`

## 1. Purpose

Make the SPEC §108 recovery objectives concrete, prove them, and define the exact procedure that rebuilds Strange Company after any loss up to and including the whole cluster [SPEC §111, §112]. The plan maps every RPO/RTO target to a mechanism and a measurement (§7.1), classifies every failure scenario with its recovery path and its unrecoverable remainder (§7.2), specifies the black start step by step (§14), defines the recovery overlay that makes a recovering cluster explainable by Git (ADR-0554, §9), and defines the drills that keep all of this proven (§20.6).

## 2. Requirements derived from SPEC.md

1. RPO/RTO targets of SPEC §108, which Stage 0 may tighten but not weaken silently [SPEC §108].
2. Repeatable restore testing for CNPG, project repositories, important Longhorn data, OpenTofu state and the `strange-company` GitHub black start; failures alertable [SPEC §109].
3. Define when to restore etcd versus rebuild from source [SPEC §110] (decision in plan 07 §20.1; applied here).
4. The entire cluster is recoverable after total loss with only off-cluster material: GitHub mirror, age identity, provider credentials, OpenTofu backend access, state-encryption material, DNS/tunnel credentials, object-storage credentials, break-glass credentials, documented procedure [SPEC §111].
5. The old Forgejo installation is not required to learn how to recreate Forgejo [SPEC §111].
6. Black start order: clone from GitHub → recover external secrets → recover OpenTofu state → provision/recover nodes → install k3s → seed core platform → restore identity and Forgejo → restore/reconnect repository state → bootstrap Flux → restore remaining state → verify → resume workers [SPEC §112]. The precise procedure exists before implementation [SPEC §112].
7. Break-glass access works when Authentik is down [SPEC §22].
8. Destroying compute never destroys recovery data [SPEC §114] (plan 49).
9. Procedures exist for total cluster loss and GitHub black start (and every SPEC §113 item this plan touches) [SPEC §113].
- **45-R-1** A recovering installation is fully explained by Git: the recovery commit (ADR-0554) is the only source of recovery-specific configuration.
- **45-R-2** No push to GitHub and no write into a pre-recovery backup path happens until refs are reconciled and the new backup generation is in force.
- **45-R-3** Workers do not run (emergency stop level `workers`) from the start of recovery until the finalize PR has merged and reconciled.
- **45-R-4** Every recovery and every drill writes machine-readable evidence with per-phase durations and the measured data loss.
- **45-R-5** An unproven recovery path is a black-start threat: drill freshness is alerted (`SCRecoveryDrillOverdue`).
- **45-R-6** `./bootstrap recover --scope state-only` never mutates credentials, Git, buckets or a cluster (DR-2). Fencing runs only in a full recovery, after `state-recover`, and refuses to fence an installation that looks alive unless the operator types `fence-live <cluster>`.
- **45-R-7** A recovery prints `CONTROL TRANSFER COMPLETE` only after the GitHub mirror is re-enabled and verified and the restore tests have run against the new generations (SPEC §35 as applied by plan 17). Before that, the report says `RECOVERY VERIFIED — MIRROR PENDING` (DR-1).
- **45-R-8** Everything the recovering cluster needs is in the recovery commit, including credentials minted by fencing (SOPS-encrypted) and the Longhorn restore sources (DR-4, DR-5).

## 3. Inputs

| Input | Source |
|---|---|
| `strange-company` repository | GitHub `main` (fresh clone; plan 17 BG-1..BG-6) |
| Recovery bundle + passphrase | operator copy `/blackstart/<cluster>/bundles/…` or `s3://<prefix>-recovery/<cluster>/bundles/LATEST.json` (plan 35) |
| Offline recovery age identity | operator (needed only if the bundle is unusable or for decrypting tier-2 files; ADR-0012) |
| `bootstrap.env` | operator (credentials; immutable fields must equal the committed config, plan 50 §9.2) |
| OpenTofu state | `s3://<prefix>-tofu-state/<cluster>/…` (plan 06) |
| Backups | `pg-backups` (CNPG generations), `longhorn-backups`, `repo-backups`, `etcd-snapshots`, `forgejo-objects`, `evidence` (plans 12, 13, 14, 44) |
| Committed installation config | `gitops/clusters/<cluster>/` (bootstrap-config, cluster-vars, inventory, SOPS files, forge-init outputs) |

## 4. Outputs

- The internals of plan 50's `recover` pipeline phases `restore`, plus the recovery steps inside `plan` (fencing, recovery commit) and `forge-init` (ref reconciliation), defined in §14.
- Shared kustomize Components `gitops/components/cnpg-recovery-{latest,target}-<id>/` and `gitops/components/recovery-mode/` (this plan), and plan 13's per-volume `longhorn-volume-recovery-{forgejo,hermes}/`.
- `gitops/clusters/<cluster>/config/recovery.yaml` (`kind: RecoveryRecord`, §7.4), written by the recovery commit.
- Recovery report `s3://<prefix>-tofu-state/<cluster>/reports/recovery-<run-id>.json` and the `CONTROL TRANSFER COMPLETE (RECOVERY)` report (plan 50 §14.9).
- Drill evidence `s3://<prefix>-evidence/restore-tests/rt-blackstart-drill/<ts>.json` and `…/rt-recovery-drill/<ts>.json` (`restore-test-result` schema).
- The `rt-drill-freshness` check CronJob (namespace `dr-tests`, ADR-0553) → `SCRecoveryDrillOverdue`.
- Procedures `total-cluster-loss.md`, `github-black-start.md`, `recovery-drill.md`.
- The black-start fixture for plan 19's k3d test (`test/fixtures/blackstart/`, §22).

## 5. Dependencies

- **Hard (DAG):** H04 depends on H02 and H03. The procedures must exist at Stage 0 (SPEC §112) and are exercised in parts from P10 (plan 44 restore tests) and fully at H04.
- **Mechanisms owned elsewhere and assumed:** plan 50 `recover` pipeline and journal; plan 06 `state-recover`; plan 03 provider plan/apply; plan 07 install path; plan 19 seed/transfer and §18.3 black-start variant; plan 12 recovery stanza and generations; plan 13 `fromBackup`; plan 16 forge-init `--recover`; plan 17 `forge-reconnect` (§20.3) and `expectMirror`; plan 35 bundle, fencing rotations, attestation; plan 33 Kubernetes emergency-stop source; plan 44 restore tests; plan 26 restart reconciliation.
- **Interface requirements placed on other plans** (§25 lists them as open items): plan 50 adds steps `recover.fence` (in `plan`), `recover.commit` (before `seed`), `recover.estop` (during `seed`), `recover.reconcile-refs` (in `forge-init`), `recover.restore` (phase `restore`), `recover.finalize` (after `verify`); plan 12 accepts the cluster-level `cnpg-recovery` component (Flux `spec.patches` on the db layers) as the realization of its §9.5 selection. Plan 50 also adds the **recovery verify profile** and the two-stage report (DR-1), runs `recover.fence` after `state-recover` (DR-2), and lets the recovery commit carry fencing SOPS outputs and the Longhorn variables (DR-4, DR-5).

## 6. Owned state

| State | Location | Writer |
|---|---|---|
| Recovery overlay components | Git `gitops/components/{cnpg-recovery,recovery-mode}/` | PR |
| `RecoveryRecord` | Git `gitops/clusters/<cluster>/config/recovery.yaml` | `./bootstrap recover` (recovery commit) |
| Recovery report and drill evidence | S3 `tofu-state/<cluster>/reports/`, `evidence/restore-tests/rt-blackstart-drill/`, `…/rt-recovery-drill/` | `sc-bootstrap`, operator drills |
| Recovery journal | plan 50 journal (pipeline `recover`) | `sc-bootstrap` |

No runtime state of its own.

## 7. External interfaces

### 7.1 RPO/RTO mapping (SPEC §108; mechanism + measured verification)

**Two RTOs (CD-32).** The **data-availability RTO** ends when the data is readable or restorable by the operator; SPEC §108's RTO column is interpreted as this RTO. The **service-restoration RTO** ends when the service performs its function again; Stage 0 adds the targets SR-1 and SR-2 below. Both clocks start at `decidedAt` (DR-27), and drills measure and report both.

| Data / service | SPEC RPO / data-availability RTO | Mechanism (owner) | Worst-case RPO by design | Data-availability path and budget | Measured by | Alert when the design RPO is at risk |
|---|---|---|---|---|---|---|
| `strange-company` committed Git | ≤ 1 h (near-zero mirror lag) / ≤ 2 h | push mirror on every push + 10-min interval (17); bundles every 30 min (44 BK-09); Longhorn daily (13) | mirror lag (seconds); ≤ 1 h guaranteed by alerting | GitHub clone usable at T+5 min (read and black-start source); Forgejo serving `main` is service restoration SR-1 (below) | `sc_github_mirror_lag_seconds`; drill RD3 phase durations | `SCGitHubMirrorStale` (15 min), `SCGitHubMirrorRPOBreach` (1 h), `SCGitHubMirrorDiverged` |
| OpenTofu state | latest successful mutation / ≤ 2 h | S3 versioned, conditional-write locked, encrypted (06) | 0 (every mutation writes state) | `state-recover` ≤ 10 min; import rebuild ≤ 30 min if undecryptable (06 §20.4) | `rt-tofu-state` (06 §20.1), RD2/RD3 | `SCTofuStateRestoreTestFailed`, `SCObjectStorageConfigDrift` |
| Forgejo / Authentik / Strange Company (+ Woodpecker) PostgreSQL | ≤ 15 min / ≤ 4 h | WAL archiving `archive_timeout: 10min`, daily base backups, 30 d window (12) | ≈ 10 min + archive latency | CNPG `bootstrap.recovery` from `g<N>`: forgejo-pg during seed (≈ 15 min), others in parallel after transfer (≈ 20 min) | `rt-cnpg-pitr-*` `restore_duration_seconds` (44); RD3 heartbeat-marker RPO (§14.2 step 9.3) | `SCCnpgWalArchiveBacklog` (900 s), `SCCnpgWalArchiveFailing`, `SCCnpgBaseBackupStale` |
| Project Git repositories | ≤ 1 h / ≤ 4 h | bundles every 30 min (44 BK-09) + Longhorn daily (13) | ≈ 30 min + run time (≤ 55 min) | Longhorn `fromBackup` of `forgejo-data` then fast-forward from bundles (16 §20.1) | `rt-repo-bundle` restore time (44) | `SCRepoBundleBackupStale` (2 h) |
| Critical Longhorn data (`forgejo-data`, `hermes-data`) | ≤ 24 h / ≤ 8 h | recurring backup daily + 6-h local snapshots (13) | 24 h (+ snapshot interval does not help after node loss) | `fromBackup` restore ≈ size / throughput (20 GiB at ≥ 30 MiB/s ≈ 12 min) | `rt-longhorn-volume` `restore_duration_seconds` ≤ 7200 (44) | `SCLonghornBackupStale` (26 h) |
| Forgejo LFS, attachments, packages, OCI | (not in §108; master plan: ≤ 1 h / ≤ 4 h) | S3 `forgejo-objects` versioned (14) | 0 (written directly to S3) | nothing to restore; Forgejo reconnects | `objstore-audit` (14) | `SCObjectStorageConfigDrift` |
| RED/GREEN evidence, worker logs | (master plan: ≤ 15 min / ≤ 4 h) | S3 `evidence` versioned (14) | 0 | nothing to restore | `objstore-audit` | `SCObjectStorageConfigDrift` |
| Logs / traces | best effort / ≤ 24 h; non-critical | Loki/Tempo on S3 `telemetry` (41) | ≤ 2 h unflushed | redeploy; data in S3 remains | — | `SCTelemetryStorageWriteFailing` |
| Historical Prometheus metrics | best effort / non-critical | none (38) | total loss | Prometheus starts empty | — | — |
| etcd | accelerator only (§110) | snapshots every 6 h, S3 copy (07) | 6 h | used only by the plan 07 decision tree, never in total loss | `rt-etcd-snapshot` (44) | `SCEtcdSnapshotUploadFailing`, `SCEtcdSnapshotMissing` |
| Recovery material | n/a / ≤ 2 h to unlock (master plan §8) | bundle in S3 `recovery` + operator copies; offline identity; passphrase (35) | n/a | unlock ≤ 5 min | `restore-test-recovery-bundle` weekly, attestation 90 d (35) | `SCRecoveryBundleOutdated`, `SCRecoveryBundleVerifyFailed`, `SCRecoveryKeyAttestationOverdue` |

No SPEC §108 target is weakened. In a total-loss black start the databases' data-availability RTO ends at the §14.2 step 9 probe (all four clusters Ready, Longhorn volumes restored): 3 h 05 – 3 h 35 min in the §14.3 budget against ≤ 4 h. For `strange-company` Git it ends when the GitHub clone is usable (T+5 min against ≤ 2 h).

**Service-restoration targets (CD-32, Stage-0 addition):**

| ID | Service restored | Clock stops at | §14.3 budget | Target | Worst-case margin |
|---|---|---|---|---|---|
| SR-1 | Forgejo serves `main` and Flux reconciles from Forgejo | step 8 probe: root Kustomization Ready from `GitRepository/flux-system` at the seeded SHA | 2 h 35 – 3 h 05 min | ≤ 4 h | 55 min |
| SR-2 | Strange Company resumed | first increase of `sc_runner_dispatch_total` after recovery verify passed and the finalize PR set the emergency stop to `none` | 4 h 10 – 5 h 10 min | ≤ 6 h | 50 min |

All three targets are met with margin only while provider delivery (step 4) stays ≤ 75 min; a slower provider consumes the data-availability margin first (25 min). The recovery report and the drill evidence record `rto_data_availability_seconds`, `rto_sr1_seconds` and `rto_sr2_seconds` (§14.3).

### 7.2 Failure-scenario matrix

"Unrecoverable remainder" = what is lost even when the procedure succeeds.

| # | Scenario | Detection | Recovery path (procedure) | RTO estimate | Unrecoverable remainder |
|---|---|---|---|---|---|
| S1 | Pod crash / eviction | kubelet; component alerts | automatic restart/reschedule | seconds–minutes | in-flight requests; a worker attempt (counted `infrastructure`) |
| S2 | Node lost, self-hosting-minimum (the node is the cluster) | `SCNodeDown`, external heartbeat | if the disk survives: reboot/repair (`lost-node.md`); if the disk is gone: **S11 total loss** | minutes / per S11 | per S11 |
| S3 | Node lost, small-ha | `SCNodeDown`, `SCEtcdMemberMissing` | CNPG failover, Longhorn rebuild, replace node (`lost-node.md`, plan 47) | < 1 min for DB failover; hours for replacement | none (sync replication `any 1`; Longhorn 2 replicas) beyond in-flight work |
| S4 | One etcd member lost (small-ha) | `SCEtcdMemberMissing` | `lost-etcd-member.md` (plan 07 §20.3) | 30 min | none |
| S5 | etcd quorum lost, a member's data intact | `SCEtcdNoLeader` | `cluster-reset` on the survivor, rejoin others (`restore-etcd.md` §B) | 30–60 min | none |
| S6 | All etcd data lost, node disks (PGDATA, Longhorn) intact, snapshot ≤ 24 h, `SC-K3S-TOKEN` available | API down | **etcd snapshot restore** (`restore-etcd.md` §A/§C; ADR-0158) | 30–60 min | API writes since the snapshot (Runner Jobs reconcile from `sc-pg`, plan 26) |
| S7 | All etcd lost and (disks gone, or no recent snapshot, or token lost) | API down | **rebuild from Git** = black start with the recovery overlay (**S11 path**) on the same or new nodes | per §14.3 | per S11 |
| S8 | Logical damage to Kubernetes objects | humans, Flux drift | Git revert + Flux; etcd restore only per plan 07 §20.1 rule | minutes | per plan 07 |
| S9 | Longhorn volume lost/corrupt (`forgejo-data`, `hermes-data`) | `SCLonghornVolumeFaulted` | `restore-longhorn.md` (replace from backup); for Forgejo then repo fast-forward from bundles/GitHub (`restore-forgejo.md`) | ≤ 1 h | Forgejo repos: none beyond bundle RPO (≤ 55 min; `strange-company` main from GitHub ≈ 0); Hermes: ≤ 24 h of non-authoritative memory |
| S10a | One CNPG cluster lost (instances 1) | `SCCnpgClusterDown` | `restore-cnpg.md` replace mode (recovery from `g<N>` into `g<N+1>`) | ≤ 1 h | ≤ 15 min of writes |
| S10b | CNPG logical corruption | humans | `restore-cnpg.md` PITR (side-by-side verification) | ≤ 2 h | writes after the target time |
| S11 | **Total cluster loss** (all nodes gone, or S7) | external heartbeat; humans | **`total-cluster-loss.md`** = §14 black start | data 3 h 05 – 3 h 35; SR-1 2 h 35 – 3 h 05; SR-2 4 h 10 – 5 h 10 (§14.3) | DB writes ≤ 15 min; project repos ≤ 55 min (bundles) or ≤ 24 h if bundles are also lost; Hermes memory ≤ 24 h; Prometheus history; Loki/Tempo unflushed ≤ 2 h; in-flight worker attempts; agent branches pushed after the last bundle |
| S12 | Forgejo failure (DB or volume) | `SCForgejoDown` | `restore-forgejo.md` (plan 16 §20.1) | ≤ 4 h | per S9/S10 |
| S13 | Authentik failure | `SCAuthentikDown` | break-glass logins (`break-glass-access.md`), then `restore-authentik.md` (PITR of `authentik-pg`; blueprints from Git) | ≤ 4 h; humans work through break-glass meanwhile | ≤ 15 min of identity changes |
| S14 | Object storage outage (temporary) | `SCObjectStorageUnreachable`, canary | `object-storage-outage.md` (plan 14): platform keeps running; WAL accumulates (≈ 4 days of budget, plan 12) | provider-dependent | none if the outage ends before disk exhaustion |
| S15 | Object storage **data loss** (bucket deleted or provider loss) | `SCObjectStorageConfigDrift`, canary | live cluster unaffected. Same endpoint and account still usable: re-create buckets (plan 14), immediate fresh base backups into a new CNPG generation, Longhorn backups, full bundle chains, re-seal and re-upload the bundle from the operator copy. Provider or account lost: `replace-object-storage.md` (**to be written by plan 14/50**, DR-13: endpoint migration with typed `migrate-object-storage <cluster>`) | hours | backup history (PITR window restarts); OpenTofu state history (state re-imported, plan 06 §20.4); seed snapshots; evidence objects and worker logs (audit history lost); Loki/Tempo data |
| S16 | Object storage loss **and** cluster loss together | — | black start without backups: Git from GitHub, empty databases (fresh `initdb` via `accept-empty`), repositories from GitHub (`strange-company` only). An empty `authentik-pg` requires `sso-init --from A0` with the bootstrap Secret **before** the Authentik HelmRelease is resumed (plan 15 §18, AUTH-1), otherwise the upstream `initial-setup` flow is open; recover keeps the `authentik` layer suspended until then. A new endpoint needs `replace-object-storage.md` first | as S11 minus restores | **all** database content (tasks, ledger, Forgejo issues/PRs, Authentik users), project repositories not on GitHub, evidence. This is why plan 14 requires buckets in a different failure domain from the nodes where the provider allows (§25 Q-45-3). |
| S17 | Provider outage (region) | `SCNodeDown`, `SCEdgeDown` | wait (`provider-outage.md`), or black start into another region (`PROVIDER_REGION` is mutable) or onto `existing` hardware (changing `PROVIDER` needs plan 50 support, §25 Q-45-2) | per S11 + provider | per S11 |
| S18 | GitHub loss (repository deleted, account lost, GitHub outage) | `SCGitHubMirrorFailing`, `SCGitHubMirrorNotConfigured`, `SCGitHubMirrorSignalMissing` | live platform unaffected (Forgejo is primary, SPEC §32): create a new repository, rotate `SC-GITHUB-MIRROR`, `mirror recreate` (`github-mirror-failure.md` §D); history comes from Forgejo | ≤ 1 h | none if Forgejo is alive |
| S19a | GitHub **repository or account** lost during total cluster loss | — | `github-black-start.md` §C: clone from the newest `strange-company` bundle in `repo-backups` (age-encrypted with `SC-AGE-BACKUP`, which is in the recovery bundle; plan 44) or from any operator clone; then normal §14 with a new GitHub repository | per S11 + 30 min | commits after the newest bundle (≤ 55 min) |
| S19b | GitHub **platform** outage during total cluster loss | githubstatus.com | **From H04:** `./bootstrap recover --from-capsule` (§9.4, CD-33; `github-black-start.md` §E) — no GitHub service is needed. **Before H04 (accepted, recorded risk, SI-43):** wait; the toolbox image (GHCR), k3s release assets, Flux/CNPG images (`ghcr.io`) and several chart repositories (`*.github.io`) are GitHub-hosted. If an operator machine holds a built toolbox and an image/chart cache, §C seeds from bundles with `seed-without-github` | from H04: per S11 + 20 min (capsule download); before: GitHub's outage + S11 | as S19a |
| S20 | Operator laptop lost | — | nothing in the cluster depends on it. New machine: clone GitHub, obtain the bundle from S3 (`SC-OBJECT-STORAGE/recovery` credential from the operator's records or the `admin` credential in `bootstrap.env`), passphrase from memory/vault; `bootstrap.env` recreated from provider consoles (or exported from the bundle with `sc-bootstrap bundle export-env`, **requested from plan 50/35**, DR-29; then `./bootstrap rotate refresh-bundle` so PF-21 fingerprints match). Rotate `SC-OPERATOR-SSH` using the infrastructure break-glass SSH key and any credential that was on the laptop (`credential-compromise.md`) | ≤ 2 h | the local `.bootstrap/runs/` history (redacted copies are in S3) |
| S21 | **Recovery age identity lost** (bundle + passphrase intact) | attestation fails (`SCRecoveryKeyAttestationOverdue`) | `rotate-age-key.md` §recovery-key lost variant (plan 35 §14.3): regenerate tier-2 files from bundled values, new identity | ≤ 2 h | none |
| S22 | **Bundle or passphrase lost** (recovery identity intact) | attestation, bundle verify | live cluster: `rotate-age-key.md` §lost-bundle; **during black start**: `rotate-age-key.md` §lost-bundle-black-start (**to be written by plan 35**, DR-15: generate the new cluster key offline, re-encrypt tier 1 with the recovery identity, put the re-encrypted files and `.sops.yaml` into the recovery commit, seal a new bundle before seed) | ≤ 2 h | `SC-TOFU-ENCRYPTION` if it exists only in the bundle → state rebuilt by import (plan 06 §20.4) |
| S23 | **Both** recovery identity **and** bundle/passphrase lost, cluster alive | attestation | the live cluster still holds `sops-age` (cluster key): export nothing; instead rotate both identities from the cluster key immediately (`rotate-age-key.md` §both-lost-cluster-alive, **to be written by plan 35** (DR-15): a break-glass Job runs `sops updatekeys` with new recipients using the in-cluster key), then seal a new bundle | ≤ 4 h | off-cluster-only secrets (tofu passphrase → import rebuild; break-glass passwords → reset through admin APIs) |
| S24 | Both lost **and** cluster lost | — | **no black start from Git secrets is possible** (plan 35). Fresh install with new credentials; data from S3 backups can still be restored where the backup credentials can be re-issued by the provider (CNPG backups are not client-side encrypted, plan 12) but SOPS-encrypted Git secrets are unrecoverable | days | every SOPS-encrypted value; continuity of identities (OIDC clients, host keys); repository bundles (age-encrypted with `SC-AGE-BACKUP`, which was in the bundle) |
| S25 | Cluster age key (`SC-AGE-CLUSTER`) compromised or lost from the cluster | Flux `DecryptionFailed`, `SCSopsDecryptionFailing` | restore `sops-age` from the bundle (`sops-decryption-failure.md` §C), then rotate if compromised (`rotate-age-key.md`) | 30 min | none |
| S26 | Break-glass credential lost | `SCBreakGlassAccountNotReady`, drill | `rotate-break-glass.md` (reset through admin APIs while the platform is up) | 1 h | none |

### 7.3 `./bootstrap recover` (plan 50 surface; internals here)

```text
./bootstrap recover [--scope full|state-only] [--bundle local|s3] [--old-installation destroyed|unknown]
                    [--pitr-target <RFC3339>] [--skip-longhorn-restore forgejo-data,hermes-data]
                    [--drill --drill-source <cluster>] [--step forge-reconnect|finalize] [--confirm-plan D8]
                    [--from-capsule <s3-url|dir>]                                  # H04, §9.4
typed confirmations: destroyed <cluster> | fence-live <cluster> | accept-empty <cluster-pg>
                     accept-github-commits <n> | seed-without-github <cluster> | accept-generation <cluster-pg> <N>
                     defer-mirror <cluster>                                         # finalize with GitHub down, §9.4
```

- `--old-installation destroyed` requires the typed confirmation `destroyed <cluster>` and skips fencing (recorded). The default `unknown` fences (§14.2 step 4.1), after the liveness probe.
- `--scope state-only` runs preflight, unlock and `state-recover` only and mutates nothing (45-R-6); it is what drill RD2 uses.
- `--pitr-target` applies one target time to all four CNPG clusters (default: latest), selects for each cluster the generation whose recovery window contains it, and limits repository refs to commits ≤ T (§14.2 steps 6.1, 7.2).
- `--step forge-reconnect` and `--step finalize` are the two operator-gated steps after the recovery verify (§14.2 step 10); finalize ends with `CONTROL TRANSFER COMPLETE` (45-R-7).
- Plan 50 (CLI authority, CD-14) must list these flags, the typed confirmations above and the codes `FENCE_UNVERIFIED`, `FENCE_REFUSED_LIVE`, `NO_CNPG_BACKUP`, `GENERATION_MISMATCH`, `REFS_DIVERGED`, `GITHUB_COMMITS_UNTRUSTED` (DR-23).
- `--drill` runs the RD3 drill mode (§20.7) and is refused unless `CLUSTER_NAME` differs from `--drill-source`.

Exit codes are plan 50's. A recovery resumes from the journal like any pipeline.

### 7.4 `RecoveryRecord` (`gitops/clusters/<cluster>/config/recovery.yaml`; contract `contracts/recovery-record.schema.yaml`)

```yaml
apiVersion: strange.company/v1alpha1
kind: RecoveryRecord
metadata: {name: <cluster>-recovery-<UTC yyyymmddHHMM>}
spec:
  runId: <plan 50 run id>
  mode: recovery                      # recovery | drill (drill adds drillSource; stop level all)
  decidedAt: "2026-10-13T07:40:00Z"   # RTO clock start: operator decided to rebuild (DR-27)
  startedAt: "2026-10-13T08:00:00Z"
  sourceKind: github                  # github | repo-bundle | operator-clone
  sourceCommit: <seeded base SHA>
  githubTrust: {anchorRef: <newest repo-backups manifest main SHA>, extraCommitsAccepted: 0}
  oldInstallation: unknown            # destroyed | unknown
  fencing: {objectStorage: rotated, githubMirror: rotated, tunnel: rotated, dnsRecords: not-applicable,
            drTestsResults: rotated, telemetryDebug: rotated, notifyTransport: kept, modelKeys: kept}
  fencedResources: [ovh-vps-sc-prod-node-01]   # moved out of compute state (§14.2 step 4.2)
  cnpg:                               # SOURCE_GEN, GEN (and TARGET_TIME) also written to vars/cluster-vars.yaml
    forgejo-pg:    {sourceGeneration: 3, generation: 4, target: latest}
    authentik-pg:  {sourceGeneration: 3, generation: 4, target: latest}
    woodpecker-pg: {sourceGeneration: 3, generation: 4, target: latest}
    sc-pg:         {sourceGeneration: 3, generation: 4, target: latest}
  longhorn:                           # backupUrl also written to vars/cluster-vars.yaml (DR-5)
    forgejo-data: {restored: true, backupUrl: "s3://<prefix>-longhorn-backups@<region>/?backup=backup-…&volume=forgejo-data", backupCreatedAt: "…"}
    hermes-data:  {restored: false}   # no usable backup: volume created empty, component not included
  emergencyStop: {level: workers, source: git, file: policy/budgets.yaml}
```

`sc-bootstrap` validates the record against `contracts/recovery-record.schema.yaml` and the semantic invariants in its description (generation = source + 1, target ≤ start, drills never fence) before committing. The record is documentation and audit; Flux never reads it. Every value Flux needs is duplicated into `vars/cluster-vars.yaml` (DR-5).

## 8. Internal interfaces

| Step | Calls |
|---|---|
| unlock | plan 35 `bundle.Open`, manifest `install_uid` check (plan 50 §14.9) |
| state-recover | plan 06 `InitRemote`, `Verify`, provider inventory compare — read-only |
| liveness probe | SSH to the inventory nodes (`k3s kubectl get --raw /readyz`), external heartbeat age (operator-entered), `https://git.<d>/api/healthz`, newest object age under `pg-backups/<cluster>/<cluster>-g<N>/wals/` |
| fence | plan 35 rotation entry points (`rotate storage --fence`, GitHub token replacement, `rotate dns-tunnel`), writing SOPS files locally only |
| backup discovery | S3 listing of `pg-backups/<cluster>/` generations; Longhorn backup store listing (`backupstore/volumes/…` metadata, plan 13); `repo-backups` manifests (plan 44) |
| recovery commit | plan 18 tree; plan 12 generation vars; plan 13 component; fencing SOPS outputs |
| seed / transfer | plan 19 `seed.Run`, `transfer.Run` (black-start variant §18.3) |
| forge-init | plan 16 `--recover`; plan 17 `compare-refs`, `forge-reconnect` |
| restore phase | Flux reconciliation of post-transfer layers; CNPG/Longhorn status polling; plan 44 `sc-restore-test verify-marker` for heartbeat markers |
| verify | the plan 50 **recovery verify profile** (§14.2 step 10.1) |
| finalize | plan 17 `mirror verify` (V-08), plan 44 one-shot restore-test Jobs (V-37), plan 16 `forge finalize`, plan 35 bundle seal and attestation |

## 9. Configuration — the recovery overlay (ADR-0554)

### 9.1 Layout (recovery-specific changes are inside `gitops/clusters/<cluster>/`, plan 50 commit-scope rule, except the e-stop line in `policy/budgets.yaml` and, in the lost-bundle case only, `.sops.yaml`)

| File | Change in the recovery commit | Removed by the finalize PR? |
|---|---|---|
| `vars/cluster-vars.yaml` | `CNPG_<ID>_SOURCE_GEN = N`, `CNPG_<ID>_GEN = N+1` for `<ID>` ∈ {FORGEJO, AUTHENTIK, WOODPECKER, SC}; `CNPG_<ID>_TARGET_TIME` when `--pitr-target` is set; `LONGHORN_FORGEJO_DATA_BACKUP_URL`, `LONGHORN_HERMES_DATA_BACKUP_URL` for each volume that has a usable backup (plan 18 §9.2 must list these keys, DR-5) | generations stay; the Longhorn keys are removed |
| `secrets/**/*.sops.yaml` (fencing outputs) | every credential re-minted by fencing (§14.2 step 4.1), SOPS-encrypted to both recipients with the bundle's cluster key (DR-4) | no (they are the current credentials) |
| `config/recovery.yaml` | the `RecoveryRecord` (§7.4) | no (kept as history; superseded by the next recovery) |
| `kustomization.yaml`, `seed/kustomization.yaml` | add `components:` per §9.2: one `cnpg-recovery-<variant>-<id>` per CNPG cluster, `longhorn-volume-recovery-forgejo` and/or `-hermes` (only for volumes with a usable backup), and `recovery-mode` | `cnpg-recovery-*` stay; the others are removed |
| `config/github-mirror.yaml` (plan 17 `GitHubMirrorConfig`) | `expectMirror: false` | yes (`true` again) |
| `policy/budgets.yaml` (outside the cluster dir: **requires plan 50 to allow this one path, `spec.emergencyStop` only, for recovery commits**, Q-45-5) | `spec.emergencyStop: {level: workers, reason: …}` (`all` in drill mode) (plan 33 §20) | yes (`level: none`) |

### 9.2 Components (shared, in `gitops/components/`)

- **`cnpg-recovery-latest-<id>` and `cnpg-recovery-target-<id>`** for `<id>` ∈ {forgejo, authentik, woodpecker, sc} (owned here; realizes plan 12 §9.5). Each patches **one** db layer `Kustomization` (`forgejo-db`, `authentik-db`, `woodpecker-db` or `sc-db`) with Flux `spec.patches` (JSON 6902) on its `Cluster`: `replace /spec/bootstrap` with plan 12's recovery stanza and `add /spec/externalClusters` (source `serverName: <cluster>-g${CNPG_<ID>_SOURCE_GEN}`). The `-target-` variant includes `recoveryTarget.targetTime: ${CNPG_<ID>_TARGET_TIME}`; the `-latest-` variant has no `recoveryTarget` (substitution cannot remove keys, so the variant is chosen by which component is listed — DR-6a). Variables are written as `$${…}` so the root Kustomization's substitution leaves them for the layer's `substituteFrom: cluster-vars` (verify the escaping behavior on the pinned Flux; 45-AC-3). The archive `serverName` already uses `${CNPG_<ID>_GEN}` (plan 12 §9.4), so the bump in `cluster-vars` alone moves archiving to `g<N+1>`. Plan 12 / `restore-cnpg.md` use the same per-cluster components for single-cluster PITR (needs-other-owner, DR-6a).
- **`longhorn-volume-recovery-forgejo`, `longhorn-volume-recovery-hermes`** (realize plan 13 §9.7 per volume): `spec.fromBackup: ${LONGHORN_<VOLUME>_BACKUP_URL}` on that volume only. A volume without a usable backup gets no component, so it can never be created with an empty or literal URL; Forgejo then restores repositories from bundles/GitHub (§14.2 step 7.3). Plan 13 renames its single component accordingly (DR-5).
- **`recovery-mode`** (owned here): (a) Forgejo `app.ini` override `[mirror] ENABLED = false` via a values patch on the `forgejo` HelmRelease (verify that this stops push-mirror syncs without deleting mirror records, 45-AC-4); (b) ConfigMap `flux-system/sc-recovery-mode` (`data.since`, `data.runId`) as a visible marker; (c) a `spec.patches` entry on the `backups` layer that sets `spec.suspend: true` on every CronJob labelled `strange.company/restore-test` (heavy restore tests must not run against half-restored state, DR-10); `repo-bundle-backup`, `objstore-canary`, `dr-marker-heartbeat` and the check CronJobs stay active. Workers are stopped by the Git emergency-stop source set in the same recovery commit (§9.1, plan 33 §20).

### 9.3 Selection, removal and the creation guard

- Selection: only `./bootstrap recover` creates the recovery commit (`recover.commit` step), on top of the cloned `main` (or of the restored Forgejo `main`, §14.2 step 7). Humans never hand-edit it.
- Removal (`recover --step finalize`): opens a Forgejo PR `bootstrap/recovery-finalize-<run-id>`, **authored by `sc-admin`** (the bot), so the single human operator can approve and merge it without self-approval (DR-26). It removes `recovery-mode` and the `longhorn-volume-recovery-*` components and the Longhorn keys, sets `expectMirror: true`, and sets `policy/budgets.yaml` `spec.emergencyStop.level: none`. Woodpecker CI runs; a human merges (ADR-0019). Removing the Longhorn components is a no-op for the live volumes (`ssa: IfNotPresent`, plan 13). `cnpg-recovery-*` stay: CNPG ignores `bootstrap` after initialization (12-AC-13), and removing them changes nothing.
- **Creation guard (DR-7).** Keeping the recovery stanza committed is *not* a safety feature: if a production `Cluster` object were deleted after recovery, Flux would re-create it from `SOURCE_GEN = N` (stale) and archive into the non-empty `g<N+1>`. Therefore a ValidatingAdmissionPolicy `sc-cnpg-cluster-create-guard` (**plan 37 to add**) denies CREATE of a `Cluster` in `forgejo`, `authentik`, `woodpecker`, `strange-company` unless the ConfigMap `flux-system/sc-cnpg-create-window` (plan 37's guard input; rendered by plan 50 only while an install or recovery run creates clusters, ADM-63) exists and its `runId` equals the `Cluster`'s annotation `strange.company/recovery-run` (set by plan 12's recovery components from the run id). After finalize, an accidental deletion therefore stalls visibly (`SCCnpgClusterDown`) and the operator runs `restore-cnpg.md` §Replace with fresh generations.

### 9.4 Bootstrap capsule (CD-33; implemented at H04)

**Purpose.** Black start must work with GitHub entirely unavailable (Git service, GHCR, release assets, `*.github.io` chart repositories). The capsule places every GitHub-hosted input of §14.2 steps 1–9 in the recovery failure domain (object storage, B2 `recovery`). **Until H04 a GitHub-wide outage during total loss is an accepted, recorded risk (SI-43)**; S19b then means waiting.

**Location and lock (plan 14).** `s3://<prefix>-recovery/<cluster>/capsule/<capsule-id>/` (`<capsule-id>` = `<UTC yyyymmddHHMM>-<release tag or short SHA>`), plus the pointer `capsule/LATEST.json` (written last, with a conditional write `If-Match` on the previous ETag so two builds cannot interleave). Every capsule object is written with an explicit Object Lock retention of 35 days in the bucket's mode (overriding B2's 365-day default, which is sized for < 1 MiB bundles). The writer is a new instance `SC-OBJECT-STORAGE/capsule-writer` (put-only on `<cluster>/capsule/`, may set retention, no delete, no `BypassGovernanceRetention`); the restore test reads with the existing read-only recovery reader.

**Contents** (all pinned by the release commit's lock files; one architecture, the installation's):

| Item | Form | Source lock |
|---|---|---|
| Toolbox image | OCI archive + cosign signature bundle | `toolbox/image.lock` |
| k3s air-gap bundle | `k3s` binary, `k3s-airgap-images-<arch>.tar.zst`, install script, upstream checksums | `versions/k3s.lock.yaml` |
| Seed-set and restore-path charts | Helm chart archives incl. vendored library subcharts (CD-26): Flux, cert-manager, CNPG + Barman Cloud plugin, Longhorn, Traefik, cloudflared, Forgejo | `versions/charts.lock.yaml` |
| Their images | one OCI archive per image (operators, operands incl. the PostgreSQL image, Longhorn engine/instance-manager/CSI sidecars, Flux controllers), each image reference kept as `name:tag` and `name@digest` | `versions/images.lock.yaml` |
| OpenTofu providers | `tofu providers mirror` tree (provider releases are GitHub-hosted) | `.terraform.lock.hcl` |
| Ansible collections | `ansible-galaxy collection download` tarballs | `infra/ansible/requirements.lock.yml` |
| `strange-company` repository | full `git bundle` of all refs at the release commit | — (newer commits come from the `repo-backups` chain, §13 trust anchor) |
| `capsule.json` | digest manifest: every file's path, size and sha256; every image digest, chart version and digest; the release commit; the sha256 of each lock file above | — |
| `capsule.json.sig` | cosign `sign-blob` bundle of `capsule.json` | — |

Not in the capsule: secrets (the recovery bundle holds them), backups (already in object storage), images that the restored Forgejo registry serves (internal images in `forgejo-objects`).

**Refresh triggers.** The capsule is rebuilt (1) on every release (plan 23 release pipeline), (2) when a PR changes any lock file listed above (upgrade of k3s, a seed-set chart or image, the toolbox, providers or collections), within 24 h of its merge, and (3) at least every 30 days, so the repository bundle stays recent enough for the trust-anchor fast-forward from `repo-backups`.

**Build and signing (plan 23).** A Woodpecker workflow on the `sc-lane=build` lane (the release trust chain, ADR-0502) assembles the capsule from the pinned digests, verifies each upstream checksum or signature where upstream publishes one, writes `capsule.json`, signs it with `SC-RELEASE-SIGNING` (`cosign sign-blob --bundle`), uploads with `capsule-writer` and updates `LATEST.json` last. Verification needs a public key that does not come from the capsule itself: the recovery bundle carries a copy of `security/cosign/trusted-keys.yaml` (plan 35).

**Restore test (plan 44).** `rt-bootstrap-capsule` in `dr-tests`: **daily** it verifies the `capsule.json` signature, `HeadObject` size and retention of every listed file, and that the capsule's lock-file hashes equal those of current `main` (stale after the 24-h grace → failure); **weekly** it downloads everything, recomputes every sha256, runs `skopeo inspect oci-archive:` per image, `helm show chart` per chart, `git bundle verify` and a toolbox `docker load` + `./bootstrap version` smoke test. Alert `SCBootstrapCapsuleCheckFailed` (black-start threat, gate H04).

**`./bootstrap recover --from-capsule <s3-url|local dir>` (plans 02 and 50).** On a clean machine without GitHub: download the capsule with the operator's object-storage credentials (`aws s3 cp --recursive`; a documented one-liner in `github-black-start.md` §E), verify `capsule.json` with the key from the unlocked recovery bundle, `git clone` the repository bundle, then run `./bootstrap recover --from-capsule`. The toolbox loader (plan 02) loads the toolbox from the OCI archive by digest instead of pulling or building. `recover` (plan 50) then: fast-forwards `main` from the newest `repo-backups` chain when it is newer (trust anchor, §13); sets `sourceKind: capsule`; uses the provider mirror for OpenTofu and the collection tarballs for Ansible; copies the k3s air-gap bundle and every image archive to `/var/lib/rancher/k3s/agent/images/` on each node before k3s starts (containerd imports them; digest-reference resolution of imported archives is verified at H04); uploads the chart archives into the seed Bucket; and adds the component `capsule-chart-sources` to the recovery commit, which points the seed-set and restore-path HelmReleases at that Bucket (plan 18 owns the component). With GitHub down, finalize cannot verify the mirror: the operator may type `defer-mirror <cluster>`, so the finalize PR resets the emergency stop while `expectMirror` stays false, and `SCGitHubMirrorRecoveryModeTooLong` keeps notifying until the mirror is reconnected. A later PR removes `capsule-chart-sources` once upstream sources are reachable again.

**Size and cost.** Estimate per capsule: toolbox ≈ 1.5 GiB, Longhorn images ≈ 1.3 GiB, CNPG operator, plugin and PostgreSQL ≈ 0.8 GiB, k3s air-gap ≈ 0.3 GiB, other images ≈ 0.6 GiB, providers ≈ 0.2 GiB, charts, collections and repository bundle ≈ 0.2 GiB: **≈ 5 GiB**. With 35-day locks and about two refreshes a month, at most ≈ 4 capsules exist (≈ 20 GiB): about US$ 0.10–0.50 per month at typical S3-compatible prices, plus the weekly full verification read (≈ 20 GiB/month of egress where the provider charges it) and one 5 GiB download per black start (≈ 5–10 min on 100 Mbit/s, which is why S19b budgets +20 min).

**Retention (plan 43).** Keep the 3 newest capsules plus any capsule younger than 35 days. Older capsule prefixes are deleted by a lifecycle rule after their lock expires. `LATEST.json` is never expired.

## 10. Secrets

Consumed: `SC-RECOVERY-PASSPHRASE` (prompt), `SC-AGE-CLUSTER` and every bundled class (plan 35 bundle), `SC-AGE-RECOVERY` (fallback), `SC-INFRA-PROVIDER`, `SC-DNS-TUNNEL`, `SC-OBJECT-STORAGE` (admin + instances), `SC-TOFU-ENCRYPTION`, `SC-K3S-TOKEN` (reused so pre-loss snapshots stay decryptable, plan 07 §18), `SC-GITHUB-MIRROR`, `SC-FORGEJO-ADMIN` (`sc-admin` for pushes under the bootstrap protection stage and as author of the finalize PR), `SC-BREAK-GLASS-*` (operator use when SSO is down), `SC-AGE-BACKUP` (bundle decryption in S19a, plan 44).

**Fencing** (`--old-installation unknown`, plan 35 §20; runs only in a full recovery, after `state-recover`, §14.2 step 4.1). Rotated: every in-cluster write instance of `SC-OBJECT-STORAGE` (`pg-backups`, `longhorn-backups`, `etcd-snapshots`, `forgejo-objects`, `repo-backups`, `evidence`, `telemetry`, `dr-tests-results`, `telemetry-debug`; in `shared` credential mode this means rotating `admin` itself plus `./bootstrap rotate refresh-bundle`), `SC-GITHUB-MIRROR` (the operator issues a new fine-grained token and revokes the old one; `sc-bootstrap` verifies the old token returns 401), `SC-DNS-TUNNEL` (new tunnel credential; the old connector loses the tunnel). Direct ingress mode: DNS A/AAAA records are re-applied to the new nodes (`dns apply`) before seed. **Kept, with reasons recorded:** model API keys (`SC-MODEL-API-KEYS`) — an alive old Gateway would still spend, so if the liveness probe was inconclusive the operator is prompted to rotate them at the providers (`rotate-model-credentials.md`); notification transport — duplicates are harmless. Every minted credential is written as a SOPS file under `gitops/clusters/<cluster>/secrets/**` and enters the recovery commit (DR-4); fencing never opens a PR (no Forgejo exists yet). A new bundle version (`reason.trigger: black-start`) is sealed before seed. No new secret class.

## 11. Storage

Recovery writes only new paths: CNPG `g<N+1>` archives, new Longhorn backups after restore, new bundle versions, reports and evidence. Old generations remain until plan 43 lifecycle expires them (they are PITR sources until then).

## 12. Resource requirements

Operator machine: plan 50 §12. Cluster: the normal profile, plus transient restore load (CNPG recovery pods, Longhorn restore I/O). Recovery is serialized as §14.2 orders it, so peak memory stays within the profile's platform budget plus one CNPG restore pod at a time per cluster.

## 13. Security boundary

- Recovery runs with the operator's full authority (plan 50 §13), inside the toolbox, from a fresh GitHub clone. Preflight PF-05 warns when HEAD is not a release tag; recovery from an untagged commit is allowed and recorded.
- **Trust in GitHub content (DR-16):** the mirror PAT can append commits to GitHub `main` (rulesets block only rewrites), and that PAT is stored in the Forgejo DB and its backups. `recover` therefore compares `G` with the **anchor**: `refs/heads/main` in the newest `repo-backups` manifest of `strange-company/strange-company` (plaintext manifest, written ≤ 30 min before the loss by the put-only `repo-backups` credential, plan 44). If `G` equals the anchor or is its ancestor, it is trusted. If `G` has commits beyond the anchor, `recover` prints them (author, committer date, Forgejo merge-PR trailer) and requires `accept-github-commits <n>`; otherwise it seeds at the anchor (fetched from the bundle). The confirmation and count are recorded in `RecoveryRecord.githubTrust`. Signed merge commits are a post-MVP strengthening.
- Fencing prevents a surviving old installation from corrupting backups, GitHub or the public hostname (ADR-0554).
- Workers cannot run during recovery (45-R-3), so no autonomous code executes against half-restored state.
- The recovery commit is pushed to protected `main` only through plan 16's `bootstrap` protection stage (`sc-admin` allowlisted), restored to `steady` by `forge finalize`.

## 14. Happy path

### 14.1 Decide the path

1. Is any node's etcd data intact or the plan 07 snapshot-restore condition met? → plan 07 decision tree (`restore-etcd.md`, `lost-etcd-member.md`). Stop here if it applies.
2. Is the problem limited to one component (S9, S10, S12, S13)? → its procedure.
3. Otherwise: total cluster loss → `total-cluster-loss.md` (this §14.2). If GitHub is unavailable or unusable as well → `github-black-start.md` first; a GitHub **platform** outage uses the bootstrap capsule from H04 (§9.4, `github-black-start.md` §E) and is an accepted risk before H04.

### 14.2 Black start in precise order

Each step names the plan 50 phase/step, its idempotency probe, and what the journal records. The order follows SPEC §112 (brackets) and plan 50 §14.9: `preflight → unlock → state-recover → plan (fence, provision plan) → provision → configure → seed → forge-init → flux-bootstrap → restore → verify (recovery profile) → forge-reconnect → finalize`.

1. **Clone from GitHub** [clone strange-company from GitHub]. On a clean machine with Git and Docker/Podman: `git clone https://github.com/<owner>/strange-company.git && cd strange-company`. Restore `bootstrap.env` (credentials; immutable fields equal the committed `gitops/clusters/<cluster>/bootstrap-config.yaml`). `./bootstrap version` pulls the toolbox by digest or builds it locally (ADR-0013). The clone's `main` SHA is `G`. Record `decidedAt` (RTO clock start, DR-27) when prompted. During a GitHub *platform* outage this step is impossible without a local cache (S19b).
2. **Unlock the bundle** [recover external secrets] — phases `preflight`, `unlock`: `./bootstrap recover` prompts for the passphrase, opens the newest bundle (local, else S3 `LATEST.json`), checks `install_uid`, loads the remote journal. If the bundle is unusable during black start: `rotate-age-key.md` §lost-bundle-black-start (S22). **Probe:** journal records the bundle version opened. Mutates nothing.
3. **Recover OpenTofu state** — phase `state-recover` (plan 06 §20.5): init the remote backend, decrypt, `state verify`, compare with the provider inventory (refresh disabled for addresses the provider API cannot reach). `--scope state-only` stops here and has mutated nothing (45-R-6). **Probe:** `state verify` passed in this run.
4. **Fence and provision** [provision/recover nodes] — phases `plan`, `provision` (plan 03):
   1. **Liveness probe, then fencing** (`recover.fence`, skipped with `--old-installation destroyed`). The probe checks: SSH to each inventory node and `/readyz` of k3s; the external heartbeat age (operator-entered); `https://git.<d>/api/healthz`; the age of the newest WAL object under `pg-backups/<cluster>/<cluster>-g<N>/`. If **any** check says the installation is alive (API ready, healthz 200, or WAL younger than 15 min), recover exits `FENCE_REFUSED_LIVE` unless the operator types `fence-live <cluster>` (DR-2). Then it rotates the credentials of §10, writes their SOPS files locally, verifies the old credentials are rejected, and seals a bundle version. **Probe:** every fenced instance's SOPS file decrypts to the key minted in this run, and the old key is rejected.
   2. **Old node resources (DR-12).** Compute resources of the lost installation that still exist in state are listed. With `destroyed` they are destroyed through the provider if reachable; with `unknown` they are moved out of the managed set (`tofu state rm`, recorded in the journal and in `RecoveryRecord.fencedResources`) after the typed confirmation, and destroyed later by `total-cluster-loss.md` §Fenced installation cleanup once reachable. Recovering into another region is allowed only in `recover` (plan 03's guard must accept a region change for this pipeline). **Probe:** the compute plan contains only creates for the target node count.
   3. **Provision** as plan 03: new node names `node-NN` with unused numbers; guardrails and the typed confirmation apply unchanged. **Probe:** inventory node count = profile count.
5. **Install k3s** — phase `configure` (plan 07): the normal install path with the bundled `SC-K3S-TOKEN`; a **new** etcd (rebuild path, ADR-0158). For small-ha the three servers join as in plan 47. **Probe:** `kubectl get nodes` Ready = expected.
6. **Seed with the recovery overlay** [seed core platform; restore identity and Forgejo] — steps `recover.commit`, `recover.estop`, phase `seed` (plan 19 §18.3):
   1. **Backup discovery.** Per CNPG cluster: without `--pitr-target`, `N` = the highest generation with a completed base backup; with `--pitr-target T`, `N` = the generation whose window (first base backup … newest WAL) contains `T` (DR-6b). Cross-checks (DR-28): if `N` differs from `CNPG_<ID>_GEN` committed at `G`, recover exits `GENERATION_MISMATCH` until the operator types `accept-generation <cluster> <N>`; if the newest WAL in `g<N>` is older than 15 min before `decidedAt`, it prints the expected data loss and asks for confirmation. Per Longhorn important volume: the newest completed backup. A cluster with **no** usable generation → `NO_CNPG_BACKUP <cluster>` (operator: wait for object storage, or `accept-empty <cluster>`). A volume without a usable backup gets no restore component (§9.2).
   2. **Recovery commit `G'`** on top of `G` per §9.1 — including the fencing SOPS files, `CNPG_*`/`LONGHORN_*` variables, components, `expectMirror: false` and the Git emergency stop — author `sc-bootstrap`, subject `recover(<cluster>): recovery overlay run <run-id>`, trailer `SC-Run-Id`. **Probe:** HEAD carries the trailer for this run and the rendered files equal a fresh render.
   3. **Emergency stop (Git source, plan 33 §20):** part of `G'`: `policy/budgets.yaml` `spec.emergencyStop: {level: workers, reason: "black-start recovery <install-id> run <run-id>"}`. The Runner and the Gateway load it with the policy before they can dispatch or forward, and it holds regardless of the restored database's e-stop row (plan 33 §14.6). **Probe:** the seeded tree's `policy/budgets.yaml` has `level: workers`; after step 9, Gateway `/sc/v1/status` shows source `git` = `workers`.
   4. **Create window (ADM-63).** Before the seed, plan 50 renders `flux-system/sc-cnpg-create-window` (`runId: <run-id>`); every `cnpg-recovery-*` component sets the `Cluster` annotation `strange.company/recovery-run: <run-id>` (plan 12), so `sc-cnpg-cluster-create-guard` admits the four creations. Plan 50 deletes the window after step 9 (all four clusters exist). **Probe:** the ConfigMap exists before seed and is NotFound after step 9.
   5. **Seed** `G'` exactly as plan 19 §14.1 (Bucket source snapshot of `G'`). The seed set restores **`forgejo-pg`** from `g<N>` (CNPG `bootstrap.recovery`, archiving into `g<N+1>`) and **`forgejo-data`** from its Longhorn backup (`fromBackup`); Forgejo starts only after both are Ready (plan 12 §18, plan 13 health expression). With `[mirror] ENABLED=false`, the restored push mirror cannot push. The Forgejo SSH host key and `SECRET_KEY` come from Git, so Flux `known_hosts` and encrypted DB columns stay valid (plan 16 §20.4). **Probe:** plan 19 seed checks; `Cluster forgejo-pg` Ready with `status.timelineID` > the source's; Forgejo healthz pass.
7. **Restore and reconnect repository state; reconcile refs** [restore/reconnect repository state] — phase `forge-init` in `--recover` mode (plan 16 §18) plus step `recover.reconcile-refs`:
   1. forge-init S01–S14 verify rather than create (expected `verified`); missing bot tokens are re-minted; branch protection goes to stage `bootstrap` (`sc-admin` push allowlist).
   2. **`strange-company` main, newest commits win (fast-forward only):** let `F` = restored Forgejo `main`. If `F` is an ancestor of `G` (usual: GitHub newer) → push `G'` (fast-forward). If `F` is a strict descendant of `G` (GitHub lagged) → rebuild the recovery commit on `F` (`G'' = F + recovery commit`; the commit touches only §9.1 paths, a conflict halts), re-seed at `G''` (plan 19 §17 re-seed; plan 19 §18.3 must reference this step, SEED-15), push `G''`. If `F` and `G` diverged → **halt** (`REFS_DIVERGED`): the operator decides per `github-mirror-failure.md` §C (never force). Same rule for each `release/*` branch (without the recovery commit). Tags present only on GitHub are fetched into Forgejo. With `--pitr-target T` (DR-6c): candidate heads are limited to commits whose Forgejo merge time (committer date of the merge commit) is ≤ T; recover shows the chosen heads and requires confirmation, so post-T commits are not re-introduced.
   3. **Other repositories:** for every repository with a newer bundle in `repo-backups` (plan 44 manifests; with `--pitr-target`, the newest bundle sequence ≤ T), fetch each ref from the bundle: fast-forward when the bundle ref descends from the disk ref; keep the disk ref when it descends from the bundle ref; on divergence keep the disk ref and store the bundle ref as `refs/recovered/<run-id>/<ref>` and report it. Repositories missing on disk are created from their newest bundle (plan 16 §20.3).
   4. **Probe:** Forgejo `main` = the seeded SHA (`G'` or `G''`); the ref report is written to the journal.
8. **Bootstrap Flux (source switch)** [bootstrap Flux] — phase `flux-bootstrap` (plan 19 T0–T8 unchanged): the root Kustomization moves from the Bucket to the Forgejo `GitRepository` at the same SHA; no seeded object changes UID; the Bucket source is deleted. **Probe:** plan 19 transfer checks (V-04, V-06).
9. **Restore remaining state** [restore remaining state] — phase `restore`: Flux now reconciles the post-transfer layers with the recovery overlay in force:
   1. `authentik-pg`, `woodpecker-pg`, `sc-pg` recover from `g<N>` into `g<N+1>` (in parallel). Authentik starts only after its database is Ready; if `authentik-pg` was `accept-empty`, the `authentik` layer stays suspended until `sso-init --from A0` has run (AUTH-1). Then Woodpecker, the Runner, the Gateway and Hermes (`hermes-data` from its Longhorn backup, or empty) start. The Runner's startup reconciliation (plan 26 §20) marks executions without Jobs `JOB_LOST` and, because the effective emergency stop is `workers`, moves affected tasks to `BLOCKED (emergency_stop)` (plan 33 §14.5); no dispatch happens.
   2. Each recovered CNPG cluster takes an immediate base backup into `g<N+1>` (`ScheduledBackup immediate: true`); Longhorn takes a fresh backup of both restored volumes (`recover` triggers a `Backup` CR). Restore-test CronJobs stay suspended (§9.2).
   3. **Observed RPO (DR-8):** plan 44's `dr-marker-heartbeat` CronJob (**requested from plan 44**: every 5 min inserts a timestamped row into the `sc_dr` heartbeat table of each cluster, keeps 48 h) provides the newest restored heartbeat per database. Observed RPO = `decidedAt`-reported loss time (or RD3 drill start) − newest restored heartbeat. Expected bound: heartbeat interval (5 min) + `archive_timeout` (10 min) = 15 min.
   4. **Probe:** all four clusters Ready; first base backup completed under `g<N+1>`; Longhorn volumes healthy.
10. **Verify, reconnect, finalize, resume** [verify; resume workers]:
    1. Phase `verify` with the **recovery verify profile** (plan 50 must add it, DR-1): every P12 check **except** V-08 (mirror verify) and V-37 (restore-test freshness), which are replaced by: `mirror compare-refs` (GitHub refs are ancestors of the Forgejo refs; nothing pushed), first base backup under `g<N+1>` for each cluster, observed RPO per database, restored Longhorn volumes healthy, and "no GitHub push yet". On success the report is titled **`RECOVERY VERIFIED — MIRROR PENDING`** (not `CONTROL TRANSFER COMPLETE`).
    2. `./bootstrap recover --step forge-reconnect` (operator-confirmed): plan 17 §20.3 — compare refs again, fast-forward only, recreate the push-mirror configuration with the fenced token while the mirror is still disabled globally.
    3. `./bootstrap recover --step finalize`: opens the finalize PR (§9.3, authored by `sc-admin`). After human approval and merge and Flux reconciliation, the same command continues: Forgejo restarts with mirrors enabled; **V-08** `mirror verify` (GitHub `main` == Forgejo `main`); restore-test CronJobs are unsuspended and **one run of each restore test is forced** (one-shot Jobs from the CronJobs, plan 44, as in CD-19) against the new generations, which satisfies **V-37**; `sc-bootstrap forge finalize` returns branch protection to `steady`; a `black-start` bundle version is sealed and `recovery-bundle verify --full` produces a fresh attestation (plan 35 §20). Only then does it print `CONTROL TRANSFER COMPLETE` and, on the second line, `CONTROL TRANSFER COMPLETE (RECOVERY)` (45-R-7).
    4. **Resume workers:** the finalize PR also set `spec.emergencyStop.level: none` (plan 33 §20); the operator confirms the effective level is `none` (no other source active) and resumes `BLOCKED` tasks selectively through the human transition API (plan 33 §14.5). The recovery report records the time workers resumed.

### 14.3 RTO budgets (data availability and service restoration, CD-32; self-hosting-minimum, preferred node; measured by RD3)

The RTO clock starts at `decidedAt` (the operator's decision to rebuild); detection time before that is recorded separately in the incident (DR-27).

| Step | Budget |
|---|---|
| 1 clone + toolbox (pull; build adds ≈ 15 min) | 10 min |
| 2–3 unlock + state recover | 15 min |
| 4 liveness probe + fencing (incl. the operator creating a GitHub token) + provision (provider delivery dominates) | 45–75 min |
| 5 configure | 20 min |
| 6 seed incl. `forgejo-pg` recovery (≈ 15 min) and `forgejo-data` restore (≈ 12 min, parallel) | 40 min |
| 7 forge-init + ref reconciliation | 15 min |
| 8 source switch | 10 min |
| 9 remaining restores and starts | 30 min |
| 10.1 recovery verify (settle 10 min + checks; memory window 5 min, §25 Q-45-4) | 25 min |
| **Milestone SR-1 (end of step 8): Forgejo serving `main`, Flux reconciling** | **2 h 35 – 3 h 05 min** (target ≤ 4 h) |
| **Milestone data availability (end of step 9): all databases and volumes restored** | **3 h 05 – 3 h 35 min** (SPEC §108 ≤ 4 h) |
| Milestone `RECOVERY VERIFIED — MIRROR PENDING` (end of 10.1) | 3 h 30 – 4 h |
| 10.2–10.3 reconnect, finalize PR (human review, CI, merge, reconcile; forced restore tests run in parallel) | 30–60 min |
| **Milestone SR-2: Runner dispatching (first dispatch after the finalize PR reset the emergency stop)** | **4 h 10 – 5 h 10 min** (target ≤ 6 h) |

`recover` writes each milestone timestamp to the journal and the report as `rto_data_availability_seconds`, `rto_sr1_seconds` and `rto_sr2_seconds`; the finalize PR copies the first two into `RecoveryRecord.spec.rto` (§7.4). SR-2 happens after the run's last Git write, so it lives in the report and the drill evidence only. In drill mode the emergency stop stays `all` (§20.7 rule 5), so SR-2 stops when the drill's finalize step reports `resume-ready` (all resume preconditions met).

### 14.4 What the old Forgejo is not needed for (SPEC §111)

Everything required to recreate Forgejo comes from GitHub (manifests, chart pins, `forge/forge.yaml`, SOPS secrets including the SSH host key and `SECRET_KEY`), the bundle (admin and break-glass credentials, age key) and S3 (database generations, Longhorn backups, bundles, LFS/packages). The old Forgejo, its pods, PVCs and API are never contacted. 45-AC-6 asserts it by running RD3 with the source installation's Forgejo unreachable.

## 15. Failure modes

| Failure during recovery | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Bundle missing or passphrase wrong | unlock exit 8 | cannot proceed with bundle | none | `total-cluster-loss.md` §Bundle, plan 35 S22 path |
| Fencing cannot prove the old credential is dead | `recover.fence` fails (`FENCE_UNVERIFIED`) | recovery halts (risk of split brain) | none | operator revokes manually at the provider, or attests `destroyed` |
| The installation looks alive (API, healthz or fresh WAL) | liveness probe (`FENCE_REFUSED_LIVE`) | no fencing, recovery halts | none | stop: this is probably not a total loss (`lost-node.md`, `restore-etcd.md`); `fence-live <cluster>` only if certain |
| GitHub `main` has commits beyond the trust anchor | step 1 (`GITHUB_COMMITS_UNTRUSTED`) | halt | none | `github-black-start.md` §B |
| Highest S3 generation ≠ Git generation | step 6.1 (`GENERATION_MISMATCH`) | halt | none | inspect `pg-backups` listing; `accept-generation` |
| No CNPG generation with a completed base backup | backup discovery | that database starts empty (`initdb`) only if the operator confirms `accept-empty <cluster>` | halt otherwise | `total-cluster-loss.md` §No backup |
| CNPG archive path `g<N+1>` not empty (repeated recovery attempt) | plan 12 check | archiving would stall | halt; procedure deletes the failed `Cluster` and PVCs and uses `g<N+2>` | plan 12 §17 |
| Longhorn backup unreadable | restore status | volume restored empty | continue; repos from bundles/GitHub | `restore-forgejo.md` |
| `F` and `G` diverged | step 7.2 | recovery halts before any push | none (never force) | `github-mirror-failure.md` §C |
| Provider capacity unavailable in region | provision fails | — | none | `provider-outage.md` (other region, S17) |
| Flux substitution escaping wrong (`$${…}`) | `Cluster` renders with an empty server name | CNPG refuses / recovery fails at seed | seed check fails | fix component; 45-AC-3 prevents |
| Old installation still alive and unfenced (`destroyed` attested wrongly) | duplicate tunnel connector, interleaved backups | data corruption risk | none | immediately fence (`total-cluster-loss.md` §Split brain) |
| Recovery verify fails | phase `verify` (recovery profile) | no report; workers stay stopped | none | `bootstrap-verify.md`; recovery stays resumable |
| Finalize checks fail (mirror verify or a forced restore test) | `--step finalize` | no `CONTROL TRANSFER COMPLETE`; platform serving, mirror state per failure | none | `github-mirror-failure.md` / `restore-test-failure.md`; rerun finalize |

## 16. Retry behavior

Plan 50 rules: in-run retries for transient errors, cross-run resume from the journal, `RETRY_CEILING` after 5 identical failures. Recovery-specific: backup discovery and restore polling retry with backoff up to the phase timeout (seed 45 min, restore 60 min); fencing verification retries 3 times over 2 min; ref reconciliation never retries a rejected non-fast-forward.

## 17. Idempotency behavior

Every step has a probe (§14.2). Fencing is probed by "each fenced instance's SOPS file decrypts to the key minted in this run and the old key is rejected"; a resumed run never fences twice. The recovery commit is detected by its `SC-Run-Id` trailer, so a resumed run never creates a second one. Generations make CNPG recovery re-runnable (plan 12 §17). Longhorn `fromBackup` matters only at volume creation. Ref reconciliation is fast-forward only and therefore idempotent. The finalize PR is keyed by `bootstrap/recovery-finalize-<run-id>`.

## 18. Bootstrap behavior

Recovery is a bootstrap pipeline (plan 50 `recover`). It reuses install code paths with the overlay. The normal install never touches this plan's components.

## 19. Upgrade behavior

Changes to any plan on the recovery path (06, 07, 12, 13, 16, 17, 19, 35, 50, 45) require a passing k3d black-start fixture test (§22) and schedule an RD3 drill within 30 days (`recovery-drill.md`); `rt-drill-freshness` alerts if it is not done. Component version changes that alter `bootstrap.recovery` or `fromBackup` semantics are re-verified by 45-AC-3/45-AC-4 in k3d.

## 20. Recovery behavior

### 20.1–20.5 Scenario procedures

Covered by §7.2 and the owned procedures: `total-cluster-loss.md` (S7, S11, S16, S17), `github-black-start.md` (S19 and the GitHub-specific parts of S11), `recovery-drill.md` (§20.6). Missing procedures requested from other owners: `replace-object-storage.md` (plan 14/50, DR-13), `rotate-age-key.md` §lost-bundle-black-start and §both-lost-cluster-alive (plan 35, DR-15), `safe-destruction.md` §Drill (plan 49, DR-9), `bootstrap-resume.md#lost-operator-machine` (plan 50, DR-29). Component procedures are owned by their plans.

### 20.6 Drills (schedule, evidence, H04)

Drill IDs are prefixed `RD` (recovery drills, this plan) to avoid collision with plan 47's `HA1–HA8` (DR-21). Plan 44 states "RD2 quarterly, RD3 yearly".

| Drill | What | Frequency | Paid infra? | Evidence (`test_kind` / `test_id`, plan 44 schema) |
|---|---|---|---|---|
| RD1 automated restore tests | CNPG PITR ×4, repos, Longhorn, etcd, tofu-state freshness, GitHub black-start render, recovery-bundle **structural** check (plan 44 / 35 CronJobs). Per CD-10 there is no in-cluster copy of the cluster age key outside `flux-system`, so in-cluster tests check SOPS recipients only; decryption is proven by RD2 | weekly / monthly | no | `rt-cnpg-pitr-*`, `rt-repo-bundle`, `rt-longhorn-*`, `rt-etcd-snapshot`, `rt-tofu-state`, `rt-github-mirror`, plan 35 checks |
| RD2 recovery-material drill | on a clean machine: clone GitHub, toolbox, unlock the bundle, `./bootstrap recover --scope state-only` (**no fencing, no mutation**, 45-R-6), `recovery-bundle verify --full` (decrypts every SOPS file with the bundle and with the offline identity; attestation, plan 35), break-glass drill (plan 15) | quarterly (≤ 100 d) | no | `recovery-drill` / `rt-recovery-drill` |
| RD3 scratch black start | `./bootstrap recover --drill --drill-source <cluster>` into an isolated drill installation (§20.7), followed by `./bootstrap destroy` | yearly (≤ 400 d), at H04, and within 30 days after a change listed in §19 | yes (hours of one VPS) | `blackstart-drill` / `rt-blackstart-drill` with `measurements.per_subject` and `measurements.step_durations` |

`rt-drill-freshness` (CronJob `dr-tests`, daily) reads the newest evidence objects and exits 1 if RD2 is older than 100 d or RD3 older than 400 d (or 30 d after a recovery-path change, detected from Git history of the paths in §19) → `SCRecoveryDrillOverdue` (black-start threat). **Requested from plan 44 (DR-18, STO-17):** `restore-test-result.schema.yaml` gains `test_kind` values `recovery-drill`, `blackstart-drill`, `drill-freshness`, and `measurements.per_subject` (map subject → `{rpo_observed_seconds, restore_duration_seconds}`) and `measurements.step_durations` (map step → seconds); the outcome field is `outcome` (not `result`).

### 20.7 RD3 drill mode (precise design, DR-9)

The drill must prove black start end to end without touching the source installation. Rules:

1. **Same configuration and secrets, separate identity.** The drill reuses the source's cluster directory `gitops/clusters/<source>/` and its secrets, unlocked with the **source bundle and passphrase** (tier-1 secrets such as Forgejo `SECRET_KEY`, Authentik key, `SC-PG-APP`, SSH host key decrypt with the bundled cluster key). A drill env file overrides, for the drill only, `CLUSTER_NAME=<source>-drill` (provider resource names, tofu state key, journal), `S3_BUCKET_PREFIX=<prefix>-drill` (the drill's own buckets for its new generations, Longhorn target, journal and bundle copy) and `BASE_DOMAIN=drill.<domain>`. `recover --drill` runs the storage-apply step of the install pipeline for the drill prefix (plan 50 adds it to `recover` in drill mode).
2. **Reads only.** Source backups are read with `SC-OBJECT-STORAGE/drill-reader` (read-only on the source's `pg-backups`, `longhorn-backups`, `etcd-snapshots`, `repo-backups` and `forgejo-objects`; **plan 14 adds B7** to its scope). The credential is in the source bundle (plan 35, DR-20). Before the seed, `recover --drill` copies the source's `forgejo-objects` (B7: packages and OCI blobs, LFS, attachments) server-side into `<prefix>-drill-forgejo-objects` with `drill-reader` as the read credential (plan 50 drill step), so the drill Forgejo writes only to its own bucket. CNPG recovers with **no** WAL archiver to the source path: the drill's `cluster-vars` point archiving at `<prefix>-drill-pg-backups`.
3. **Isolated names.** The drill recovery commit is a local branch `drill/<run-id>` pushed only to the drill Forgejo. It writes `gitops/clusters/<source>/…` changes exactly like a real recovery (so the overlay under test is the real one), plus the drill overrides; plan 50's commit-scope rule must allow `emergencyStop.level: all` for `mode: drill`.
4. **No shared public surface.** No tunnel or DNS for the production names. The drill runs the normal ingress mode `INGRESS_MODE=tunnel` (there is no `none-drill` mode; plan 50 removes it) with `BASE_DOMAIN=drill.<domain>`: public hostnames live under `drill.<domain>` with a temporary tunnel created by the drill and deleted at teardown (certificates: production ACME for `*.drill.<domain>`; RD3 counts against the Let's Encrypt weekly limit for that name, at most one issuance per drill — AUTH-14). Node container runtimes resolve `git.drill.<domain>` to the drill's Traefik ClusterIP through the same managed `/etc/hosts` entry that CD-21 defines, so internal images are pulled from the **drill** Forgejo (served from the drill's copy of `forgejo-objects`, rule 2), never from the source (45-AC-6).
5. **No mirror, no spend.** `expectMirror: false` for the whole drill; emergency stop `all`; no GitHub token is used for writes.
6. **Teardown.** `./bootstrap destroy` on the drill installation (plan 49), then removal of the drill's own buckets, tofu state and journal per `safe-destruction.md` §Drill (**plan 49 to add**), and deletion of the drill tunnel and DNS records.

### 20.8 What H04 drills (with plan 47)

1. Node loss on each of the three nodes in turn (plan 47 HA drills): `SCNodeDown` delivered (38-AC-20), CNPG failover < 60 s, Longhorn rebuild, Loki/Tempo reschedule (41-AC-11), Alertmanager single delivery (42-AC-20).
2. etcd member loss and rejoin (07-AC-22); quorum-loss `cluster-reset` on a lab copy (07-AC-23).
3. CNPG primary kill per cluster; CNPG replica loss and re-clone.
4. Longhorn replica loss and `forgejo-data` replace-from-backup (`restore-longhorn.md`).
5. Forgejo restore from backups while GitHub stays authoritative (`restore-forgejo.md`).
6. Authentik outage with break-glass logins (15-AC-23).
7. **RD3 scratch black start of the small-ha installation** (three drill nodes, or one node with the self-hosting-minimum overlay when the operator chooses cost over fidelity, recorded).
8. Object-storage outage simulation (credential revoked for 1 h on the drill installation) with WAL accumulation within budget.

## 21. Observability

- Alerts: `SCRecoveryDrillOverdue` (here), plus the black-start-threat set of plan 42 §7.5 that this plan relies on.
- Console and journal: per-step progress lines; the recovery report (per-step durations, observed RPO per database, ref-reconciliation outcome, fencing outcome, generations, `githubTrust`).
- Grafana `sc-backups-dr` (plan 39) shows drill and restore-test freshness.

## 22. Testing strategy

All deterministic in CI; paid tests are opt-in.

- **Unit (`sc-bootstrap`):** backup-generation discovery on fixture S3 listings (gaps, empty `g<N>`, multiple clusters with different `N`, `--pitr-target` inside/outside each generation's window, Git-vs-S3 generation mismatch); Longhorn backup-store listing against a fixture `backupstore/` layout (DR-5); ref-reconciliation decision table (ancestor, descendant, diverged, missing, tag-only-on-GitHub, `--pitr-target` cut-off) with fixture repositories; GitHub trust-anchor comparison (equal, ancestor, extra commits); recovery-commit rendering golden files (with and without Longhorn backups, latest and target CNPG variants); `RecoveryRecord` validation; fencing verification and the liveness probe against fake S3/GitHub/Cloudflare/SSH endpoints; old-node state handling with the fake provider (DR-12).
- **F11 render check (requested from plan 18/52, DR-17):** on every PR, `gitops/clusters/ci-blackstart/` (a fixture cluster dir with a fixture `RecoveryRecord`, all recovery components and both CNPG variants) renders and validates offline.
- **k3d black-start fixture (`make e2e-blackstart`, also used by plan 19's 19-AC-14):** a fixture "GitHub" bare repository with a cluster dir; the SeaweedFS `s3-emulator` (CD-12) with a CNPG backup of a fixture `forgejo-pg` (generation 1, created by a setup run) and a Longhorn-less variant (Forgejo data from a bundle); run `recover` with the fake provider through seed, forge-init `--recover`, transfer, restore, recovery verify, forge-reconnect and finalize. Asserts: archives under `forgejo-pg-g2/`; Forgejo `main` = recovery commit; no push to the fixture GitHub before finalize (its reflog unchanged); the report reads `RECOVERY VERIFIED — MIRROR PENDING` before finalize and `CONTROL TRANSFER COMPLETE` after; seeded UIDs unchanged across transfer; the finalize PR leaves every `Cluster` UID and `timelineID` unchanged.
- **Lab (plan 13 lab VMs, requested):** the Longhorn overlay end to end with `fromBackup` (`forgejo-data` restored from a lab backup target), because k3d is Longhorn-less.
- **Paid (opt-in, `make e2e-recover-vps`):** RD3 on a real VPS.
- **Capsule (H04):** unit tests of `capsule.json` generation and verification (fixture capsule; tampered file, wrong key, stale lock hash each fail); the RD3 `--from-capsule` variant with the GitHub sinkhole (45-AC-20).

## 23. Acceptance criteria

Black-start code must be implemented and tested before production self-hosting begins (DR-17). The k3d/unit ACs therefore sit at **GATE-P10** (backups and restore tests: the recovery pipeline is the consumer of those backups); the lead adds plans 45 and 50 to P10's `plans:` in `dag.yaml` (or creates a dedicated node `P13 recover-pipeline` depending on P10 and gating R01 — both acceptable).

### GATE-P10 (contribution: recovery pipeline proven in k3d)

- **45-AC-3** k3d (`make e2e-blackstart`): every recovered cluster's first base backup is under `<cluster>-g<N+1>/` (`aws --endpoint-url $S3_EMULATOR s3 ls`), and `kubectl get cluster -A -o jsonpath='{range .items[*]}{.spec.plugins[0].parameters.serverName}{"\n"}{end}'` lists only `-g<N+1>` names (re-verifies the `$${…}` escaping and plan 12 generations).
- **45-AC-4** k3d: with `recovery-mode` active, a commit pushed to the fixture Forgejo `main` does not reach the fixture GitHub within 15 min (GitHub ref unchanged; Forgejo mirror records still present, re-verifying `[mirror] ENABLED=false` semantics); after the finalize PR, the next push arrives within 2 min.
- **45-AC-7** Fencing (k3d with fake S3/GitHub/Cloudflare): with `--old-installation unknown`, after `recover.fence` the old credentials are rejected (403/401/absent) **and** the k3d recovery reaches seed Ready using only the newly minted keys from the recovery commit's SOPS files (DR-4); with `destroyed` the typed confirmation is required and recorded in the journal.
- **45-AC-9** `rt-drill-freshness` exists in `dr-tests`, and with a test-shortened threshold and no evidence object, `SCRecoveryDrillOverdue` fires (42-AC-12 method).
- **45-AC-12** State-only safety (DR-2): a `./bootstrap recover --scope state-only` run against fake S3/GitHub/Cloudflare and a live fake installation leaves every rotation counter at 0, creates no Git commit and no bucket object; a full `recover` against the same live installation exits `FENCE_REFUSED_LIVE` without `fence-live <cluster>`.
- **45-AC-13** Report stages (DR-1): in `make e2e-blackstart`, `report.txt` after `verify` has the first line `RECOVERY VERIFIED — MIRROR PENDING` and contains no `CONTROL TRANSFER COMPLETE`; after `--step finalize` the first line is `CONTROL TRANSFER COMPLETE` and the second `CONTROL TRANSFER COMPLETE (RECOVERY)`, V-08 and V-37 are `pass`, and the fixture GitHub `main` equals Forgejo `main`.
- **45-AC-14** Trust anchor (DR-16): with a fixture GitHub `main` carrying one commit beyond the newest `repo-backups` manifest ref, `recover` exits `GITHUB_COMMITS_UNTRUSTED` and shows the commit; `accept-github-commits 1` proceeds and records `githubTrust.extraCommitsAccepted: 1`.
- **45-AC-15** Longhorn sources (DR-5): the recovery-commit golden test shows `LONGHORN_FORGEJO_DATA_BACKUP_URL` in `vars/cluster-vars.yaml` and component `longhorn-volume-recovery-forgejo` listed; with no Hermes backup, neither the key nor `longhorn-volume-recovery-hermes` appears, and the rendered `hermes-data` Volume has no `fromBackup`.
- **45-AC-16** Single operator (DR-26): in the k3d fixture with one human Forgejo account, the finalize PR (authored by `sc-admin`) is approved and merged by that human.

### GATE-P06 (contribution; plan 19 owns 19-AC-14)

- **45-AC-11** The black-start fixture `test/fixtures/blackstart/` exists and `make e2e-handoff BLACKSTART=1` uses it; 19-AC-14 is unconditional once P10's 45-AC-3 passes.

### GATE-P12 (contribution, unattended per CD-19)

- **45-AC-10** During `./bootstrap verify`, `./bootstrap recover --scope state-only` runs non-interactively (passphrase from `SC_RECOVERY_PASSPHRASE_FILE` in the E2E harness) and exits 0 with no mutation (45-AC-12 counters). The first interactive RD2 (offline identity) is due within 30 days after control transfer; `SCRecoveryDrillOverdue` enforces it.

### GATE-H04 (co-owned with plan 47)

- **45-AC-1** An RD3 drill of the small-ha installation completes: the drill's `report.json` contains `CONTROL TRANSFER COMPLETE (RECOVERY)`; measured from `decidedAt`, the data-availability RTO is ≤ 4 h, SR-1 ≤ 4 h and SR-2 (`resume-ready` in drill mode) ≤ 6 h (CD-32; evidence `measurements.rto_data_availability_seconds`, `rto_sr1_seconds`, `rto_sr2_seconds`); the evidence has `measurements.per_subject` entries with `rpo_observed_seconds` for the four databases and `restore_duration_seconds` for `forgejo-data` and `hermes-data`, plus `measurements.step_durations` for every §14.2 step.
- **45-AC-2** In that drill, observed RPO ≤ 900 s for each database (heartbeat-marker method, §14.2 step 9.3) and the restored `strange-company` `main` equals the source's GitHub `main` at drill start.
- **45-AC-5** After the finalize PR in the drill: `kubectl get cm -n flux-system sc-recovery-mode` → NotFound; every CNPG `Cluster` keeps its UID and `status.timelineID`; `PQ 'max(sc_emergency_stop_level{source="git"})'` = 2 (drill level `all`) for the whole drill, including after the drill finalize PR (§20.7 rule 5: no spend); between recovery start and resume, `sc_runner_dispatch_total` did not increase (`PQ 'increase(sc_runner_dispatch_total[<window>])'` = 0).
- **45-AC-6** The RD3 drill runs with the source installation's Forgejo unreachable from the drill environment (no route; image pulls resolve to the drill Forgejo) and still passes (SPEC §111).
- **45-AC-8** H04 drill list §20.8 items 1–6 and 8 each have a passing evidence object or the owning plan's AC (38-AC-20, 07-AC-22, 41-AC-11, 42-AC-20, 15-AC-23) passed within the H04 window.
- **45-AC-18** Bootstrap capsule (CD-33): for the H04 release, `s3://<prefix>-recovery/<cluster>/capsule/LATEST.json` names a capsule whose `capsule.json` verifies with `cosign verify-blob` against the key copy in the recovery bundle; every listed file's sha256 matches; the lock-file hashes equal those of the release commit; `get-object-retention` on every capsule object shows ≥ 35 days; a `DeleteObject` with `capsule-writer` returns AccessDenied.
- **45-AC-19** `rt-bootstrap-capsule` has a `passed` daily result and a `passed` weekly full result within the H04 window, and a fixture capsule with one corrupted image archive makes the weekly run fail (RED proof).
- **45-AC-20** RD3 variant `--from-capsule` with GitHub blocked: the drill machine and drill nodes resolve `github.com`, `*.github.com`, `*.githubusercontent.com`, `ghcr.io` and `*.github.io` to a sinkhole, and the drill still reaches `RECOVERY VERIFIED — MIRROR PENDING` with the data-availability RTO and SR-1 ≤ 4 h; the sinkhole log shows the attempted connections and none succeeded; the `RecoveryRecord` has `sourceKind: capsule`.
- **45-AC-17** Creation guard (DR-7, needs plan 37's VAP): in the drill after finalize, `kubectl delete cluster sc-pg -n strange-company` followed by Flux reconcile leaves no new `Cluster` (admission denial `sc-cnpg-cluster-create-guard` in the Flux events) and `SCCnpgClusterDown` fires.

## 24. Explicit non-goals

- Automatic failover to another region or provider.
- Continuous replication to a standby installation.
- Restoring Prometheus history, Alertmanager silences or Grafana preferences.
- Restoring in-flight worker executions (they become `JOB_LOST` / `BLOCKED`).
- etcd snapshot restore onto new machines after total loss (plan 07 forbids it).
- Automated quarterly drills on paid infrastructure (RD3 is operator-run).

## 25. Open questions

| # | Question | Blocking? | Proposed resolution | Owner |
|---|---|---|---|---|
| Q-45-1 | SPEC §108's ≤ 2 h RTO for `strange-company` Git: Forgejo serving `main` again within 2 h depends on provider delivery. | no | **Resolved by CD-32:** the §108 column is data-availability RTO (GitHub clone usable at T+5 min); Forgejo serving `main` is service restoration SR-1 (≤ 4 h, §7.1). | 45 / lead |
| Q-45-2 | `PROVIDER` is immutable in plan 50; recovering onto a different provider (S17 onto `existing`) changes it. | no | Plan 50 allows a `PROVIDER` change only in the `recover` pipeline with typed confirmation `migrate-provider <cluster>`; `CLUSTER_NAME`, `BASE_DOMAIN`, `S3_ENDPOINT`, `S3_BUCKET_PREFIX` stay immutable. | 50 |
| Q-45-3 | S16 (object storage lost together with compute) loses all databases. | no | Recommend (plan 14 preflight warning) that buckets live in a different failure domain (provider or region) from the nodes; record the operator's choice in the bootstrap report. | 14 |
| Q-45-4 | The ADR-0020 memory-budget check needs a 30-min window; it adds nothing to a recovery verify. | no | `recover` runs verify with `SC_VERIFY_MEMORY_WINDOW=5m` and marks V-30 `warn` if inconclusive. | 50 |
| Q-45-5 | Plan 50 must widen the recovery commit's scope to `policy/budgets.yaml` (`spec.emergencyStop` only) and needs the recovery steps `recover.fence`, `recover.commit`, `recover.estop`, `recover.reconcile-refs`, `recover.restore`, `recover.finalize` and the `--old-installation`, `--pitr-target`, `--step`, `--drill` flags. | no | Plan 50 adds them to §7.1/§14.9 at consolidation. | 50 |
| Q-45-6 | Plan 12 §9.5 describes swapping the bootstrap component inside the platform directory, which plan 50's commit scope forbids. | no | Realized by the cluster-level `cnpg-recovery` component (Flux `spec.patches`, §9.2); plan 12 references it. | 12 |
| Q-45-7 | RD3 needs a read-only drill credential for `pg-backups` and `longhorn-backups` of the source installation. | no | Plan 14/35 add instance `SC-OBJECT-STORAGE/drill-reader` (read-only on backup buckets; not in-cluster at the source). | 14 / 35 |
| Q-45-9 | ADR-0019 disables admin merge override, but restoring `sc-pg`, `woodpecker-pg` or `authentik-pg` breaks the merge gate itself (DR-3). | no | Owned by plan 12 / `restore-cnpg.md`: direct apply with the Flux field manager and a follow-up PR once the service is back; record as a SPEC interpretation (lead). Black start is unaffected: the recovery commit is pushed under the `bootstrap` protection stage. | 12 / lead |
| Q-45-10 | A GitHub platform outage makes black start impossible (toolbox, k3s, images and charts are GitHub-hosted) (DR-14, X#7). | no | **Decided by CD-33:** the bootstrap capsule (§9.4) at H04; until then an accepted, recorded risk (SI-43). Requests to plans 14, 43, 44, 02/50, 23, 35 and 18 are listed in §9.4. | 45 / lead |
| Q-45-11 | RTO margins (DR-27, CD-32), clock at `decidedAt`: data availability 3 h 05 – 3 h 35 vs 4 h (25 min worst-case margin); SR-1 2 h 35 – 3 h 05 vs 4 h; SR-2 4 h 10 – 5 h 10 vs 6 h. | no | Accept and measure in RD3; if two RD3s miss any of the three, the lead records a SPEC §108 deviation (SI-27) or a CD-32 target change rather than weakening silently. | 45 / lead |
| Q-45-8 | Plan 13 §9.7 expects the finalize PR to remove its component; ADR-0554 as first drafted removed all recovery components. | no | Resolved in ADR-0554 (revised): finalize removes `recovery-mode` and `longhorn-volume-recovery`; `cnpg-recovery` stays (plans 12, 19). | — |
