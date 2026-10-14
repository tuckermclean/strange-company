# 44 — Backup and restore testing

**Status:** Stage-0 plan (draft)
**DAG nodes:** P10 (primary owner: backups, restore tests, RPO/RTO verification; co-owners 12, 13, 35), C08 (`rt-longhorn-volume` for `hermes-data`, GC-01), H02 (data side: backup streams and restore tests survive the 1→3 expansion), H04 (bootstrap-capsule restore test, CD-33). P10's ACs need `woodpecker-pg` (P08): DAG edge P10 → P08 is required (GC-01).
**Related plans:** [06](06-opentofu-state.md), [07](07-k3s.md), [11](11-storage.md), [12](12-cnpg.md), [13](13-longhorn.md), [14](14-object-storage.md), [15](15-authentik.md), [16](16-forgejo.md), [17](17-github-mirror.md), [18](18-flux.md), [35](35-secrets.md), [36](36-network-policy.md), [37](37-admission-policy.md), [38](38-prometheus.md), [42](42-alerting-notifications.md), [43](43-retention-gc.md), [45](45-recovery.md), [47](47-ha-expansion.md), [49](49-destroy.md), [51](51-capacity-and-scheduling.md)
**Contracts:** owns `contracts/restore-test-result.schema.yaml`; consumes `alert-catalog.yaml` (42), `secret-classes.yaml` (35), `recovery-bundle.schema.yaml` (35)
**ADRs:** ADR-0006, ADR-0012, ADR-0553 (check-CronJob pattern), ADR-0554 (recovery generations); introduces ADR-0202 (repository bundles: incremental chains, age encryption, write-without-delete)
**Procedures:** owns `procedures/restore-test-failure.md`, `procedures/restore-project-repository.md`; contributes to `restore-cnpg.md`, `restore-longhorn.md` (plans 12, 13)

## 1. Purpose

Replication is not backup, and a backup that has never been restored is unproven (SPEC §26, §109). This plan is the single catalogue of every backup stream, the repository-level backup that SPEC §31 requires, the **restore tests** that prove each stream on a schedule, the evidence they produce (`restore-test-result`), the alerts on failure and staleness, and the mapping from each SPEC §108 recovery objective to the mechanism that meets it and the measurement that verifies it.

## 2. Requirements derived from SPEC.md

1. CNPG backs up externally with base backups, WAL archival, PITR and a repeatable restore test. [SPEC §26]
2. Storage-layer backup alone is insufficient; important repositories have repository-level offsite protection; `strange-company` continuously mirrors to GitHub; single-project recovery should not need a whole Longhorn restore. [SPEC §31, §32]
3. The cluster is not the sole failure domain for critical backup data. [SPEC §29]
4. Recovery objectives per SPEC §108 are mapped to concrete mechanisms and verified. [SPEC §108, §125 "RPO/RTO implementation is defined"]
5. Repeatable restore testing exists for CNPG, project repositories, important Longhorn data, OpenTofu state and the `strange-company` GitHub black start; failure is visible and alertable. [SPEC §109, §96, §97]
6. etcd snapshots are retained and copied off-node. [SPEC §110] (Mechanism: plan 07. Integrity test: here.)
7. Backup failures notify humans. [SPEC §97 "failed backup"]
8. Destroying compute does not destroy backups. [SPEC §114]
9. Repository backups and CNPG backups have intentional retention. [SPEC §107] (Numbers: plan 43.)

Derived:
- **44-R-1** Every backup stream has: an owner, a schedule, a destination outside the cluster, a freshness alert, and at least one automated restore test with a freshness alert (`maxAge`).
- **44-R-2** Restore tests never write to production data except DR marker rows in the dedicated `sc_dr` database (plan 12) and never read a production secret they do not need.
- **44-R-3** Every restore test materializes the restore into a scratch target that is deleted afterwards, and verifies content, not just the existence of objects.
- **44-R-4** Every result is a schema-valid `restore-test-result` document in S3; the Job's exit code is the alert signal (ADR-0553).
- **44-R-5** Repository bundles are age-encrypted and written by a credential that cannot delete (ADR-0202).
- **44-R-6** Restore tests are capacity-gated and mutually exclusive, so they cannot starve the platform (plan 51).

## 3. Inputs

- Backup mechanisms owned by plans 07 (etcd), 12 (CNPG), 13 (Longhorn), 06 (state), 17 (mirror), 35 (bundle).
- Buckets and credentials from plan 14 (`pg-backups`, `longhorn-backups`, `etcd-snapshots`, `repo-backups`, `evidence`; instances `repo-backups`, `dr-tests-reader`, `dr-tests-results`, `tofu-state-ro`).
- Forgejo bot `sc-backup` and its read token (plan 16 §4.4, `SC-FORGEJO-BOT-TOKENS/sc-backup`).
- `SC-AGE-BACKUP` (age identity, ADR-0202), `SC-PG-DR-MARKER` (plan 12). The black-start test holds **no** age identity (CD-10).
- Retention numbers from plan 43.

## 4. Outputs

- CronJobs in `forgejo` (`repo-bundle-backup`) and `dr-tests` (restore tests and checks, §9.4), delivered by Flux layers `forgejo-config` and `backups` (plan 18 §8.3).
- Repository bundle chains and metadata exports in `repo-backups`.
- `restore-test-result` documents under `s3://<prefix>-evidence/restore-tests/<test_id>/…`.
- The machine-readable catalogue `infra/dr/catalogue.yaml` (§9.1) and the coverage check `sc-restore-test coverage` (plan 11 11-AC-5; F11 check `dr-coverage`).
- The RPO/RTO verification table (§9.6) and the `dr` section of `./bootstrap verify` (V-checks requested from plan 50, §25 Q-44-5).

## 5. Dependencies

- **Hard (DAG P10):** P06 (Flux on Forgejo; layers `forgejo-config`, `backups` reconcile after control transfer) and P09 (alerts and notifications must exist for restore-test failures to be visible).
- **Hard (H02):** H01 (nodes 2–3), then plans 12/13 scale-out.
- **Soft:** plan 51 (extended resource `strange.company/heavy-slot` 8 units for heavy restore tests only, plan 51 §8.4; `dr-tests` memory quota; PriorityClass `ci`; CD-05, CD-16), plan 36 (NetworkPolicies of §13), plan 37 (VAP restricting `restore-tester` to `rt-*` objects), plan 38 (KSM CronJob metrics), plan 39 (DR dashboard), plan 17 (mirror health signal).
- **Runtime:** object storage, Forgejo (bundle source), GitHub (black-start test), CNPG operator (scratch clusters), Longhorn (scratch volumes).

## 6. Owned state

| State | Location | Writers |
|---|---|---|
| Repository bundle chains, metadata exports (register S-01/S-02 repo-level copies) | `s3://<prefix>-repo-backups/{git,meta}/` | `repo-bundle-backup` (instance `repo-backups`, put-only) |
| Bundle mirror cache (S-35, reconstructible) | `forgejo/repo-backup-cache` (local-path PVC) | `repo-bundle-backup` |
| DR marker rows (S-17) | database `sc_dr`, table `public.markers`, in each CNPG cluster | `restore-tester` (role `sc_dr`) |
| DR canary data | Longhorn volume `dr-tests/dr-canary` | `dr-canary-writer` |
| Restore-test results (S-22) | `s3://<prefix>-evidence/restore-tests/` | `restore-tester`, `objstore-*`, `storage-audit`, `retention-audit`, operator drills |
| Backup catalogue | `infra/dr/catalogue.yaml` (Git) | PR |

## 7. External interfaces

### 7.1 Check-CronJob contract (ADR-0553)

Every check CronJob carries the label `strange.company/check: "<cronjob-name>"`, runs `sc-restore-test <mode>`, writes one `restore-test-result` document per subject, and **exits 0 if and only if every document it wrote has `outcome: passed`**. `concurrencyPolicy: Forbid`, `startingDeadlineSeconds: 3600`, `successfulJobsHistoryLimit: 1`, `failedJobsHistoryLimit: 3`, `backoffLimit: 0`, `ttlSecondsAfterFinished: 604800`. The catalogue alert (plan 42) uses `kube_cronjob_status_last_successful_time` with the `maxAge` and `failAfter` in §9.4.

### 7.2 Result storage

`s3://<prefix>-evidence/restore-tests/<test_id>/<YYYY>/<MM>/<started_at>-<result_id>.json` (UTC, RFC 3339 basic). Plan 06 writes its off-cluster `rt-tofu-state` results to `s3://<prefix>-tofu-state/<cluster>/restore-tests/<started_at>.json`; the in-cluster check copies the summary into `evidence/restore-tests/rt-tofu-state/`. Retention 400 d (plan 43 R-12).

Plan 35's recovery checks (`restore-test-recovery-bundle`, `recovery-bundle-currency`, `recovery-key-attestation`) write the same document with `test_kind: recovery-check` under `s3://<prefix>-evidence/checks/<test_id>/…` (credential `dr-tests-results`, plan 14 §10; retention 400 d, plan 43 R-46).

### 7.3 Interfaces offered

| Consumer | Interface |
|---|---|
| Plan 49 (destroy pre-check), plan 45 (recover) | `backup.LastSuccess(s3)` (plan 14 §7.3) plus the newest `passed` result per `test_id` (`sc-restore-test last-results --json`) |
| Plan 16 (backup-first upgrade gate) | on-demand backup set `backup now --component <c>` (§9.7), writing `evidence/backups/<component>/<ts>.json` |
| Plan 45 (black start) | bundle-chain restore algorithm (§9.2.5); Longhorn backup resolution (plan 13 §9.7); CNPG generations (plan 12 §9.4) |
| Plans 11, 12, 13, 15, 16 | test IDs and AC references: 44-AC-4 (CNPG PITR per cluster), 44-AC-6 (Longhorn), 44-AC-8 (apps) |
| Plan 39 | DR dashboard reads the newest result per `test_id` (Grafana Infinity/JSON datasource over a read-only presigned listing, or a `dr-tests` results ConfigMap; plan 39 decides) |

## 8. Internal interfaces

- `restore-tester` → CNPG API (create/delete `Cluster`, `ObjectStore` named `rt-*` in `dr-tests`), Longhorn API (create/delete `volumes.longhorn.io` named `rt-*`), Kubernetes core (PV/PVC/Pod named `rt-*` in `dr-tests`).
- `restore-tester` → production CNPG `<cluster>-rw:5432` as `sc_dr` (marker rows only).
- `repo-bundle-backup` → Forgejo HTTP API and HTTPS Git (`forgejo-http.forgejo.svc:3000`) with the `sc-backup` token.
- `restore-test-github-black-start` → `https://github.com` (443) and Forgejo HTTPS.

## 9. Configuration

### 9.1 Backup catalogue (authoritative; machine form `infra/dr/catalogue.yaml`)

| ID | Stream | Register | Mechanism (owner) | Schedule (UTC) | Destination | Retention (plan 43) | Encryption | Freshness alert | Restore test |
|---|---|---|---|---|---|---|---|---|---|
| BK-01 | `forgejo-pg` WAL + base | S-03 | barman-cloud plugin (12) | WAL continuous (`archive_timeout` 10 min); base `01:10` daily | `pg-backups/forgejo-pg/forgejo-pg-g<N>/` | 30 d window + 14 d noncurrent | TLS + SSE | `SCCnpgWalArchiveFailing`, `SCCnpgWalArchiveBacklog`, `SCCnpgBaseBackupStale`, `SCCnpgBackupFailed` | `rt-cnpg-pitr-forgejo-pg` (weekly), `rt-app-forgejo` (monthly) |
| BK-02 | `authentik-pg` | S-10 | same | base `01:30` | `pg-backups/authentik-pg/…` | same | same | same | `rt-cnpg-pitr-authentik-pg`, `rt-app-authentik` |
| BK-03 | `woodpecker-pg` (pipeline logs kept 30 d, plan 43 R-19) | S-12, S-23 | same | base `01:50` | `pg-backups/woodpecker-pg/…` | same | same | same | `rt-cnpg-pitr-woodpecker-pg` |
| BK-04 | `sc-pg` (ledger reservations kept 30 d, denials 90 d, plan 43 R-05) | S-13, S-14 | same | base `02:10` | `pg-backups/sc-pg/…` | same | same | same | `rt-cnpg-pitr-sc-pg` |
| BK-05 | Longhorn `forgejo-data` | S-01, S-02 | recurring jobs (13) | snapshot 6 h; backup `03:30` and `15:30` (every 12 h, STO-22), `04:00` Sunday | `longhorn-backups` | 14 twelve-hourly + 4 weekly | TLS + SSE | `SCLonghornBackupStale` (26 h) | `rt-longhorn-volume` (odd ISO weeks) |
| BK-06 | Longhorn `hermes-data` | S-30 | same | same | same | same | same | same | `rt-longhorn-volume` (even ISO weeks) |
| BK-07 | Longhorn `dr-canary` | probe | same | same | same | same | same | same | `rt-longhorn-canary` (weekly) |
| BK-08 | etcd snapshots | S-41 | k3s etcd-s3 (07) | every 6 h + `pre-*` on demand | `etcd-snapshots/<install-id>/` | 28 + noncurrent 30 d | TLS + SSE | `SCEtcdSnapshotUploadFailing` | `rt-etcd-snapshot` (weekly) |
| BK-09 | Repository bundles (all repositories of orgs `strange-company`, `projects`, and every other org `sc-backup` can read) | S-01, S-02 | `repo-bundle-backup` (this plan) | incremental `7,37 * * * *`; weekly full at the Sunday `02:07` run | `repo-backups/git/weekly/…`, monthly copy in `git/monthly/…` | weekly chains 56 d; monthly fulls 400 d | **age** (ADR-0202) | `SCRepoBundleBackupStale` (2 h) | `rt-repo-bundle` (weekly) |
| BK-10 | Repository metadata exports (issues, PRs, comments, labels, milestones, releases as JSON) | S-03 (partial copy) | same CronJob, Sunday `02:07` run | weekly | `repo-backups/meta/<org>/<repo>/<ts>.json.age` | 56 d | age | part of BK-09 freshness | parse check inside `rt-repo-bundle` |
| BK-11 | `strange-company` GitHub mirror | S-68 | Forgejo push mirror (17) | on every push | GitHub | GitHub history | GitHub | `SCGitHubMirrorFailing`, `SCGitHubMirrorRPOBreach` (17/42) | `rt-github-mirror` (weekly black-start dry run) |
| BK-12 | OpenTofu state | S-60 | S3 versioning (06) | every mutation | `tofu-state` | noncurrent 365 d, newest 100 | OpenTofu encryption | — (state changes only on operator runs) | `rt-tofu-state` (monthly, operator; checked daily in cluster) |
| BK-13 | Recovery bundle | S-64 | `./bootstrap rotate …` (35) | at every rotation | `recovery` + operator copies | never expire | age passphrase | `SCRecoveryBundleOutdated` (35) | `restore-test-recovery-bundle` (35, weekly) |
| BK-14 | Forgejo objects (LFS, packages, OCI blobs, attachments) | S-04, S-49 | none needed: the system of record is already off-cluster; versioning protects against deletion (14) | — | `forgejo-objects` | noncurrent 30 d | SSE | `SCObjectStorageCanaryFailing` | `oci-retention-audit` (43) proves referenced images are pullable |
| BK-15 | Evidence, worker logs, results | S-20…S-22 | written directly to S3 by the Runner (26) | synchronous | `evidence` | per prefix (43) | SSE | Runner alerts (26) | digest re-verification by the Runner on read (26) |
| BK-16 | Desired state, SOPS secrets, policies | S-40, S-05, S-06, S-15 | Git → GitHub mirror (BK-11) | on push | GitHub | Git history | SOPS/age (two recipients) | as BK-11 | `rt-github-mirror` (SOPS recipients and S-13 reachability), `recovery-bundle verify --full` attestation (plan 35, quarterly) and `rt-blackstart-drill` (operator drills RD2 quarterly / RD3 yearly, plan 45) |

Not backed up (reconstructible, plan 11 §6.7): Prometheus TSDB, Alertmanager state, Grafana runtime DB, Flux runtime, cert-manager certificates, CNPG certificates, caches, workspaces.

### 9.2 Repository bundles (BK-09, BK-10; ADR-0202)

**9.2.1 Runtime.** CronJob `repo-bundle-backup` in namespace `forgejo` (label `strange.company/check: repo-bundle-backup`), schedule `7,37 * * * *`, image `restore-tester` (binary `sc-restore-test bundle-backup`), ServiceAccount `repo-bundle-backup` with **no** RBAC rights, PVC `repo-backup-cache` (local-path; its real size is ≈ 1.1 × the Git data in `forgejo-data`, budgeted in plan 11 §12) mounted at `/cache`, `activeDeadlineSeconds: 1500`, requests 100m/256Mi, limit 1Gi, `ephemeral-storage` limit 1Gi (no bundle is written to local disk: step 7 streams), PriorityClass `platform`. Secrets: `forgejo/forgejo-sc-backup` (token), `forgejo/s3-repo-backups`, ConfigMap `forgejo/backup-age-recipients` (public key only).

**9.2.2 Algorithm per run.**
1. List repositories: `GET /api/v1/repos/search?limit=50&page=N` with the `sc-backup` token until exhausted (verify that `sc-backup`'s org-team read membership makes private repos visible; plan 16 §4.4 grants `sc-bots-read` on all repos). Archived repositories are included.
2. For each repository: `git -C /cache/<org>/<repo>.git fetch --prune --prune-tags origin '+refs/*:refs/*'` (create with `git clone --mirror` if absent), using `http.extraHeader="Authorization: token <t>"` passed through `GIT_CONFIG_*` environment variables, never on the command line.
3. Compute the ref map (`git for-each-ref --format='%(refname) %(objectname)'`). Exclude `refs/pull/*/merge` (recomputed by Forgejo), keep `refs/pull/*/head`.
4. Read the chain state `/cache/<org>/<repo>.state.json` (last uploaded manifest: chain id, sequence, ref map). If the cache was lost, download the newest manifest from S3 (chain state is reconstructible from S3; the cache is only an optimization).
5. Decide:
   - **full**: no chain exists, or the run is the Sunday `02:07` run, or the previous chain has ≥ 400 incrementals, or the previous chain's newest object is older than 8 days → new chain id `c-<UTC timestamp>`, `git bundle create full.bundle --all` (the excluded refs are not in the mirror refspec).
   - **incremental**: ref map changed since the last manifest → `git bundle create inc.bundle <changed and new refs> --not <every object id of the previous manifest's ref map>`. Deleted refs are recorded in the manifest only (a bundle cannot express deletion).
   - **unchanged**: write nothing for this repository.
6. Stream (STO-15): `git bundle create - <args> | tee >(sha256 of plaintext) | age -r <SC-AGE-BACKUP public key> | multipart upload` (16 MiB parts, ciphertext sha256 computed in-stream). No temporary file is written, so repository size is bounded by object storage, not by local disk.
7. Verify after upload: re-download is not needed; the plaintext digest and `git bundle list-heads` of the stream are recorded, and RT-2 verifies the chain weekly with `git bundle verify`.
8. Upload with `If-None-Match: *` (never overwrite; plan 14 conformance) to `repo-backups/git/weekly/<org>/<repo>/<chain-id>/<seq:04d>-<full|inc>-<ts>.bundle.age`, then the manifest `…/<chain-id>/manifest-<seq:04d>.json` (plaintext: refs and object ids are not secret; no content). The manifest is uploaded **last** and marks completion.
9. Export `cache_bytes` (`du -sb /cache`) and per-repository sizes into the run log and into the newest manifest's `cache_bytes` field; `retention-audit` RA-17 reads it (no kubelet volume metric is assumed for hostPath-type local-path volumes).
9a. For a full bundle created in the first Sunday run of a month, also `CopyObject` it and its manifest to `repo-backups/git/monthly/<org>/<repo>/<YYYY-MM>/`.
10. Sunday run only: metadata export (BK-10): for each repository, paginate issues, pull requests, comments, review comments, labels, milestones and releases through the API into one JSON document, encrypt with age, upload to `repo-backups/meta/<org>/<repo>/<ts>.json.age`.
11. Update the cache state. The backup job writes no `restore-test-result` document: its signal is the Job exit code, and its evidence is the manifests. It exits 1 if any repository failed (the others still complete).

**9.2.3 Manifest** (`manifest-<seq>.json`, snake_case): `schema: sc-repo-bundle/v1`, `org`, `repo`, `chain_id`, `seq`, `kind` (`full`|`inc`), `created_at`, `bundle_key`, `bundle_sha256` (ciphertext), `bundle_bytes`, `prerequisites` (object ids), `refs` (map ref → object id after this bundle), `deleted_refs`, `forgejo_repo_id`, `age_recipient` (public key fingerprint), `tool_version`.

**9.2.4 Bounds.** Total upload per day for an idle installation ≈ 0. **Every repository gets 30-minute incrementals regardless of size** (incrementals are small; only weekly fulls scale with size, and they stream). There is no per-repository rate limit, so SPEC §108's ≤ 1 h holds for every project repository (STO-13, DR-19). Freshness is alerted per stream with `maxAge: 1h` and `failAfter: 30m` (DR-19; catalogue `SCRepoBundleBackupStale`), and `objstore-audit` OA-07 alerts per repository when its newest manifest is older than 60 min (result-based, from `backup.LastSuccess`).

**9.2.5 Restore algorithm** (used by `rt-repo-bundle`, `restore-project-repository.md`, plan 45):
1. Choose the chain: newest `chain_id` under `git/weekly/<org>/<repo>/` whose `manifest-0000.json` exists (or a monthly full for older points in time).
2. Choose the point: highest `seq` whose `created_at` ≤ target time (default: newest).
3. **Streamed (STO-15):** for `0000-full` … `<seq>` in order, `GET object | tee >(sha256 → compare with the manifest's ciphertext digest) | age -d -i <SC-AGE-BACKUP> | sc-restore-test bundle-apply repo.git`. `bundle-apply` reads the bundle header (prerequisites and ref tips) from the stream, checks the prerequisites exist in `repo.git` (`git cat-file -e`), pipes the remaining pack stream into `git -C repo.git index-pack --stdin --fix-thin`, and then sets the refs from the header (`git update-ref`, `+refs/*:refs/*` semantics). No bundle or ciphertext is written to disk. A digest mismatch is detected at end of stream; the repository is then discarded and the result is `RT_CHECKSUM_MISMATCH`.
4. `repo.git` starts as an empty bare repository (`git init --bare`); applying the chain in order equals `git clone --mirror` of the full bundle followed by fetching each incremental. Manual procedures may still use the file-based form (`restore-project-repository.md`), which needs about 2–3 × the repository size.
5. Apply `deleted_refs` of each manifest in order (`git update-ref -d`).
6. `git -C repo.git fsck --full --strict`.
7. Compare `git for-each-ref` with the target manifest's `refs`: equal sets and equal object ids.

**9.2.6 Rehydrate Forgejo from bundles** (STO-9; the fallback when `forgejo-pg` is restored but no usable `forgejo-data` backup exists). Mode `sc-restore-test repo-rehydrate`, run as a one-shot Job in namespace `forgejo` with Forgejo scaled to 0 and `forgejo-data` mounted read-write:
1. Query the restored `forgejo-pg` (read-only role `forgejo_ro`, requested from plan 16) for every repository's owner and lowercase name.
2. For each, run §9.2.5 into `/data/git/repositories/<owner>/<name>.git` (bare), newest point ≤ the DB restore target time.
3. Run `forgejo admin regenerate hooks`, `forgejo admin regenerate keys` and `forgejo doctor check --all` (verify the commands at the pinned Forgejo version) in a Forgejo-image container of the same Job.
4. Compare each repository's refs with its manifest; write a `restore-test-result` (`test_kind: repo-rehydrate`).
Then Forgejo is scaled up. Plan 16's `restore-forgejo.md` calls this step instead of `POST /orgs/<org>/repos` (which fails because the repository rows already exist).

### 9.3 Restore tester runtime

- Namespace `dr-tests` (PSA `restricted`, tier `platform`). Image `restore-tester`. **Image source (STO-1):** until the installation's own trusted build lane exists (P11, after control transfer per CD-19), the image comes from the project's public GHCR publication of internal bootstrap images (the interim lane that also publishes the toolbox; plan 23), referenced by digest from `versions/internal-images.lock.yaml`. A digest PR switches the reference to the Forgejo-registry build after P11. Black start uses the GHCR digest too, so no restore or check job ever depends on the Forgejo registry being populated. Binary `sc-restore-test` (Go, ADR-0002) with modes `cnpg-pitr`, `repo-bundle`, `longhorn`, `etcd-snapshot`, `tofu-state-check`, `github-black-start`, `app-restore`, `storage-audit` (11), `objstore-audit`/`objstore-canary` (14), `retention-audit`/`oci-retention-audit` (43), `bundle-backup`, `dr-canary-write`, `dr-marker-heartbeat`, `repo-rehydrate`, `repo-restore --restore-to <dir>` (the §9.2.5 algorithm for procedures and plan 45), `longhorn-replica-sync` (13), `coverage`, `last-results`. The image also contains `git`, `age`, `sops`, `etcdutl`, `psql`, `sc-gitops` (plan 18).
- ServiceAccount `restore-tester`. RBAC (Role in `dr-tests`): create/get/list/watch/delete `clusters.postgresql.cnpg.io`, `objectstores.barmancloud.cnpg.io`, `backups.postgresql.cnpg.io`, PVCs, Pods, Secrets (only to read the copies listed in §10), Leases. ClusterRole: create/get/delete `persistentvolumes`; get/list/create/delete `volumes.longhorn.io`, get/list `backups.longhorn.io`, `backupvolumes.longhorn.io` in `longhorn-system`; get `clusters.postgresql.cnpg.io` cluster-wide (to read generations). A ValidatingAdmissionPolicy (plan 37) rejects any create or delete by `system:serviceaccount:dr-tests:restore-tester` of an object whose name does not start with `rt-`, and additionally (STO-6) constrains the **spec** of what it creates: a PersistentVolume must have `spec.csi.driver == driver.longhorn.io`, `spec.csi.volumeHandle == metadata.name`, `storageClassName == longhorn-scratch` and no `hostPath`/`local`/`nfs` source; a PVC in `dr-tests` must use class `local-path` (scratch CNPG) or `longhorn-scratch` with an empty `volumeName` or one starting with `rt-`. Negative admission tests: a hostPath PV and a PV with `volumeHandle: forgejo-data` are rejected.
- **Mutual exclusion and capacity gate (44-R-6):** every heavy test (`cnpg-pitr`, `longhorn`, `app-restore`) first acquires Lease `dr-tests/restore-slot` (holder = Job name, duration 2 h, renewed every 5 min), which only serializes restore tests among themselves. Mutual exclusion with workers and CI is **not** a Lease: every pod of a heavy test (test driver, scratch CNPG instance, scratch application pod, Longhorn restore pod) sets `resources.limits["strange.company/heavy-slot"]` = `workerUnits` (8) explicitly, like a build-lane pod (plan 51 §8.4, authoritative; CD-05, CD-16), so in exclusive mode a heavy test runs alone and its memory (counted in plan 51's `dr-tests` quota) never co-runs with a worker or a CI workflow, and runs with PriorityClass `ci` (`preemptionPolicy: Never`), so in exclusive mode the scheduler keeps restore tests and a worker from running together and a waiting test never preempts a worker. A test pod that stays Pending for heavy-slot longer than 2 h makes the run `errored` with `RT_INSUFFICIENT_CAPACITY`. **Light checks** (`objstore-canary`, `objstore-audit`, `storage-audit`, `retention-audit`, `cr-retention`, `restore-test-tofu-state`, `check-tofu-lock-age`, `restore-test-etcd`, `restore-test-github-black-start`, `restore-test-repos`, `dr-canary-writer`) do **not** request `strange.company/heavy-slot` at all, which equals zero units (CD-16); they never wait behind a worker. **Heavy tests** (`restore-test-cnpg` and `restore-test-apps` with their scratch CNPG clusters and scratch application pods, and `restore-test-longhorn`) set 8 units explicitly (plan 51 §8.4; LimitRange `dr-tests-limits` has no default and max 8). Every restore-test CronJob carries the label `strange.company/restore-test: "true"`, which plan 45's `recovery-mode` component uses to suspend them during recovery. Before creating a scratch target it checks: node filesystem free ≥ 1.2 × (actual source size) + 3 GiB **and** projected root-filesystem usage after the scratch target ≤ 80 % (below kubelet soft eviction at 85 %, plan 51) (from `node_filesystem_avail_bytes` through the Kubernetes summary API `/api/v1/nodes/<n>/proxy/stats/summary`, verify RBAC `nodes/proxy` get), and node allocatable memory minus requests ≥ 768 MiB. If either check fails, the result is `errored` with `RT_INSUFFICIENT_CAPACITY` (this still fails the Job and alerts, because a test that cannot run is not a passed test).
- Scratch objects carry label `strange.company/rt-run: <result_id>`. A `finally` path deletes them. A janitor step at the start of every run deletes `rt-*` objects older than 6 h left by a crashed run.

### 9.4 Restore-test and check schedule

| CronJob (namespace) | Schedule (UTC) | Test IDs | `maxAge` / `failAfter` (catalogue) | Catalogue alert |
|---|---|---|---|---|
| `repo-bundle-backup` (forgejo) | `7,37 * * * *` | — (backup) | 1h / 30m (DR-19; matches the catalogue) | `SCRepoBundleBackupStale` |
| `dr-marker-heartbeat` (dr-tests) | `*/5 * * * *` | — (writes heartbeat rows; no result document) | 30m / 15m | `SCDrMarkerHeartbeatFailed` (new) |
| `dr-canary-writer` (dr-tests) | `0 3 * * *` | — | 26h / 1h | `SCDrCanaryWriterFailed` (new) |
| `restore-test-cnpg` | `0 4 * * 1` (Mon) | `rt-cnpg-pitr-<cluster>` ×4 | 8d / 3h | `SCRestoreTestCnpgFailed` |
| `restore-test-repos` | `0 4 * * 2` (Tue) | `rt-repo-bundle` (one document per repository) | 8d / 2h | `SCRestoreTestReposFailed` |
| `restore-test-longhorn` | `0 4 * * 3` (Wed) | `rt-longhorn-canary`, `rt-longhorn-volume` | 8d / 2h | `SCRestoreTestLonghornFailed` |
| `restore-test-etcd` | `0 4 * * 4` (Thu) | `rt-etcd-snapshot` | 8d / 1h | `SCRestoreTestEtcdFailed` (new) |
| `restore-test-github-black-start` | `0 4 * * 5` (Fri) | `rt-github-mirror` | 8d / 1h | `SCGitHubBlackStartTestFailed` |
| `restore-test-tofu-state` | `0 5 * * *` | `rt-tofu-state` (freshness of the off-cluster result) | 8d / 1h | `SCTofuStateRestoreTestFailed` |
| `check-tofu-lock-age` | `30 * * * *` | `rt-tofu-lock-age` (no `.tflock` object older than 6 h; plan 06 §21) | 26h / 1h | `SCTofuStateLockStale` |
| `restore-test-apps` | `0 4 1 * *` (1st of the month) | `rt-app-forgejo`, `rt-app-authentik` | 35d / 4h | `SCRestoreTestAppsFailed` (new) |
| `restore-test-bootstrap-capsule-light` (from H04) | `30 6 * * *` | `rt-bootstrap-capsule` (light) | 26h / 1h | `SCBootstrapCapsuleCheckFailed` (CD-33) |
| `restore-test-bootstrap-capsule-full` (from H04) | `0 4 * * 6` (Sat) | `rt-bootstrap-capsule` (full) | 8d / 3h | `SCBootstrapCapsuleFullCheckFailed` (new) |
| `objstore-canary`, `objstore-audit` | plan 14 §21 | | | plan 14 |
| `storage-audit` | `15 7 * * *` | plan 11 | 36h | `SCStorageAuditFailed` (new, plan 11) |
| `retention-audit`, `oci-retention-audit`, `oci-retention` | plan 43 §21 | | | plan 43 |

Cron fields are `minute hour day-of-month month day-of-week`; every CronJob sets `timeZone: Etc/UTC`. Weekly tests run on different weekdays so that only one heavy test is due on any day. A run that is `skipped` exits 0, so freshness for scheduled checks always comes from runs that test something (no check here skips by schedule; see §25 Q-44-3).

### 9.5 Restore tests in detail

**RT-0 `dr-marker-heartbeat`** (DR-8, light check): every 5 minutes, as `sc_dr` on each of the four `<cluster>-rw`, insert one row into `sc_dr.public.markers` with `label='heartbeat'` and delete heartbeat rows older than 48 h. This (a) gives every database a write at least every 5 minutes, so `archive_timeout` (10 min) always forces a WAL switch and `cnpg_pg_stat_archiver_seconds_since_last_archival > 900` is a valid RPO signal even for idle applications (STO-7), and (b) gives plan 45 a measurable RPO after a real loss: observed RPO = loss time − newest restored heartbeat; heartbeat interval (5 min) + `archive_timeout` (10 min) ≤ 15 min [SPEC §108].

**RT-1 `restore-test-cnpg` → `rt-cnpg-pitr-<cluster>`** (for `forgejo-pg`, `authentik-pg`, `woodpecker-pg`, `sc-pg`, sequentially; per-cluster timeout 60 min):
1. Read the production `Cluster`'s plugin `serverName` → generation `N` (plan 12 §9.4).
2. As `sc_dr` on `<cluster>-rw`: `CREATE TABLE IF NOT EXISTS markers (id bigserial primary key, run_id uuid not null, label text not null, inserted_at timestamptz not null default clock_timestamp())`; insert marker A (`label='A'`); sleep 5 s; T := `clock_timestamp()` (from the server); sleep 5 s; insert marker B; record B's insert LSN (`pg_current_wal_lsn()`) and wall time; `SELECT pg_switch_wal()`; poll `pg_stat_archiver.last_archived_wal` until it is ≥ `pg_walfile_name(<B LSN>)` (timeout 10 min) → `measurements.per_subject.<cluster>.archive_latency_seconds` = archive time − B time. This is the archive latency after a forced switch, **not** the RPO. The RPO evidence is the heartbeat (§9.5 RT-0 below): `rpo_observed_seconds` = (recovery target T) − (newest heartbeat row present in the scratch cluster with `inserted_at ≤ T`), which must be ≤ 900 s (verify that `sc_dr` may read `pg_stat_archiver` and call `pg_current_wal_lsn()`; otherwise `pg_monitor` is granted to `sc_dr` by plan 12's `postInitSQL`).
3. Create `ObjectStore rt-store-<cluster>` in `dr-tests` pointing at `s3://<prefix>-pg-backups/<cluster>/` with the `dr-tests-reader` credential, and `Cluster rt-<cluster>`: `instances: 1`, `storage: {storageClass: local-path, size: <production size>}`, `bootstrap.recovery` from `externalClusters[origin]` with `serverName: <cluster>-g<N>` and `recoveryTarget.targetTime: T`, **no** `plugins` entry (no WAL archiver, so the scratch cluster can never write to the bucket), `enableSuperuserAccess: true` (scratch only; generated Secret), resources 100m/256Mi, limit 512Mi, PriorityClass `ci`, `strange.company/heavy-slot` = 8 (heavy test, plan 51).
4. Wait for `Ready` (timeout 45 min) → `restore_duration_seconds`.
5. Checks: `marker-a-present` (A of this `run_id` exists), `marker-b-absent` (B absent), `app-db-present` (the application database exists and has ≥ the expected number of user tables: forgejo ≥ 50, authentik ≥ 50, woodpecker ≥ 10, strange_company ≥ 5; thresholds in `infra/dr/catalogue.yaml`), `rto-budget` (`restore_duration_seconds` ≤ 3600).
6. Delete the scratch `Cluster`, its PVCs and the `ObjectStore`. Delete production markers older than 30 days.

**RT-2 `restore-test-repos` → `rt-repo-bundle`** (per repository; `strange-company/strange-company` always, other repositories rotated so each is tested at least every 4 weeks, with a 60-minute total budget): run §9.2.5 (streamed) for the newest point into an `emptyDir` sized per run from the manifests: `sizeLimit` = 1.3 × (sum of `bundle_bytes` of the chain of the largest repository in this run's rotation) + 1 GiB, set on the test pod the driver creates. **Bound:** the only local copy is the restored repository itself (≈ its packed size), so RT-2 handles any repository whose restored size fits the capacity gate of §9.3 (1.2 × source + 3 GiB free, ≤ 80 % projected use); above that it reports `RT_INSUFFICIENT_CAPACITY` naming the repository. Checks: `ciphertext-sha256`, `bundle-verify`, `fsck-full`, `refs-match-manifest`, `metadata-export-decrypts` (newest `meta/` document decrypts and parses, Sunday exports only), and for `strange-company/strange-company` `main-not-diverged`: the restored `main` equals the live `main` or is its ancestor (`git fetch` of live `main` over HTTPS with `sc-backup`, then `git merge-base --is-ancestor`). Measurements: `backup_age_seconds`, `refs_compared`, `restored_bytes`, `restore_duration_seconds`.

**RT-3 `restore-test-longhorn`:**
- `rt-longhorn-canary`: newest `Completed` backup of `dr-canary` → `Volume rt-canary-<id8>` with `fromBackup`, `numberOfReplicas: 1` → PV/PVC `rt-canary-<id8>` (`longhorn-scratch`) → read-only mount in a test pod → `sha256sum -c canary/MANIFEST.sha256` passes for all files, and the newest `canary/<YYYYMMDD>.bin` is from the backup's day (written 03:00 by `dr-canary-writer`, backed up 03:30).
- `rt-longhorn-volume`: alternating `forgejo-data` (odd ISO weeks) and `hermes-data` (even): restore the newest backup the same way. Forgejo checks: `/data/git/repositories/strange-company/strange-company.git` exists; `git fsck --connectivity-only` passes there; its `main` is an ancestor of, or equal to, the `main` in the newest bundle manifest created after the backup's `backupCreatedAt` (checked in a bundle restore of `strange-company` in the same pod). Hermes checks: `/opt/data/state.db` passes `PRAGMA integrity_check` = `ok`; `config.yaml` parses. Both: `restore_duration_seconds` ≤ 7200 (`rto-budget`), `backup_age_seconds` ≤ 93600.
- `dr-canary-writer` (daily 03:00): mounts `dr-canary` read-write, writes `canary/<YYYYMMDD>.bin` (4 MiB from `/dev/urandom`), rewrites `MANIFEST.sha256` for the newest 14 files, deletes older ones, unmounts. It does not hold a Longhorn credential.

**RT-4 `restore-test-etcd` → `rt-etcd-snapshot`:** download the newest object under `etcd-snapshots/<install-id>/` with `dr-tests-reader`; checks `snapshot-age` (≤ 13 h), `decompress` (k3s compressed snapshot unzips), `etcdutl-status` (`etcdutl snapshot status -w json`: `revision > 0`, `totalKey ≥ 100`), `etcdutl-restore` (`etcdutl snapshot restore <db> --data-dir /scratch/etcd` succeeds, which verifies the snapshot integrity hash), then delete the data. This proves integrity, not a cluster restore. Full etcd restore is drilled by plan 07 (lab) and H04.

**RT-5 `restore-test-tofu-state` → `rt-tofu-state`, and `check-tofu-lock-age`:** both are check CronJobs in `dr-tests` (ADR-0553), listed in plan 45's D1 test set and catalogued by plan 42 (Q-42-2), replacing the metrics plan 06 §21 first proposed. `check-tofu-lock-age` uses the same `tofu-state-ro` credential to `HEAD` the `<cluster>/{storage,compute,edge}/terraform.tfstate.tflock` objects and fails if one is older than 6 h (`RT_TOFU_LOCK_STALE`). `restore-test-tofu-state`: with `tofu-state-ro`, read the newest `tofu-state/<cluster>/restore-tests/*.json` (written by `./bootstrap state verify`, plan 06 §20.1). Checks: `schema-valid`, `outcome-passed`, `fresh` (≤ 35 d), `versioning-enabled` (`GetBucketVersioning` = `Enabled`), `state-objects-present` (HEAD on the state key of every module the bootstrap journal records as existing: `storage` only in bucket mode `create`, `compute` only when the provider uses OpenTofu; for `existing` + `existing` installations the check is `skipped` per module and the document still passes, BOOT-4). The full decrypting test stays off-cluster (06-R-4). A stale result fails this check, which turns the monthly operator task into an alert.

**RT-6 `restore-test-github-black-start` → `rt-github-mirror`** (sandbox pod; egress only to GitHub 443 and Forgejo HTTPS; no Kubernetes API token):
1. `git clone --mirror https://github.com/<owner>/<repo>.git` with `SC-GITHUB-MIRROR-READ` (or anonymously if the mirror is public).
2. `refs-in-sync`: for `main`, `release/*` and tags, the GitHub ref equals the Forgejo ref (via `git ls-remote` with `sc-backup`), or is its ancestor while the Forgejo ref is younger than 60 min (mirror lag; rechecked once after 10 min).
3. `fsck-full` on the clone.
4. `black-start-files`: `bootstrap`, `toolbox/Containerfile`, `toolbox/tools.lock.yaml`, `gitops/clusters/<cluster>/` exist at `main`.
5. `render`: `sc-gitops render --cluster <cluster>` exits 0 on the GitHub checkout.
6. `sops-recipients`: every `*.sops.yaml` file lists exactly the two age recipients in ConfigMap `dr-tests/age-recipients` (cluster and recovery public keys).
7. `sops-reachable`: plan 35 lint **S-13** passes on the GitHub checkout: every tier-1 `*.sops.yaml` under `gitops/clusters/<cluster>/` is referenced by a kustomization reachable from the cluster root, so the live "every Kustomization Ready" signal implies that every tier-1 file decrypts with the cluster key.
8. `no-secret-no-lfs` (plan 17 §20.2 item 5, BG-4, 17-R-5): across every mirrored ref (`main`, `release/*`, tags), `git grep -E 'github_pat_[A-Za-z0-9_]{20,}|ghp_[A-Za-z0-9]{36}'` finds no match (the matching line is never logged; only path and ref are recorded), and no `.gitattributes` at any mirrored ref declares `filter=lfs`, so the GitHub copy is complete without LFS objects and contains no LFS pointer files. Failure codes `RT_SECRET_PATTERN`, `RT_LFS_POINTER`.
RT-6 holds no age identity: the in-cluster copy of the cluster key proposed earlier was rejected (CD-10, plan 35 / ADR-0500). Actual decryptability with the bundled cluster key **and** with the offline recovery identity is proven by the quarterly operator attestation `sc-bootstrap recovery-bundle verify --full` (plan 35; freshness alert `SCRecoveryKeyAttestationOverdue`), by plan 35's `restore-test-recovery-bundle`, and by the operator drills RD2 (quarterly) and RD3 (yearly, `rt-blackstart-drill`), plan 45.

**RT-7 `restore-test-apps` (monthly):**
- `rt-app-forgejo`: record the production repository list (`sc-backup` API) at time T, then PITR `forgejo-pg` "latest" into `rt-forgejo-pg` exactly as RT-1 (no archiver). Start a scratch Forgejo pod `rt-forgejo` (production image digest read from the Forgejo Deployment; `OFFLINE_MODE=true`, `[mirror] ENABLED=false`, `[cron] ENABLED=false`, `[webhook] ALLOWED_HOST_LIST=loopback`, a random `SECRET_KEY` (encrypted columns are not tested), repository root on an empty `emptyDir`, NetworkPolicy egress only to `rt-forgejo-pg`). Checks: `lfs-sample` (STO-11): for up to 20 random rows of `lfs_meta_object` in the scratch DB, `HEAD` the object under `forgejo-objects/lfs/` with `dr-tests-reader` and compare size (sha256 for objects ≤ 50 MiB); `/api/healthz` passes; `select count(*) from repository` on the scratch DB ≥ the production count at T minus repositories created after T; every repository name from T exists. Measures `restore_duration_seconds` (DB + app start).
- `rt-app-authentik`: PITR `authentik-pg` "latest" into `rt-authentik-pg`; start a scratch Authentik server (production image digest, random `AUTHENTIK_SECRET_KEY`, no outbound email, egress only to the scratch DB, blueprint discovery disabled). Checks: `/-/health/ready/` returns 200; the user `sc-dr-marker` (created by plan 15's blueprint) exists in `authentik_core_user`; the brand `sc` exists.
- Deviation from plan 16 16-AC-29's "marker issue": no writer may create issues in production for testing (44-R-2), so repository presence replaces it (§25 Q-44-4).

**RT-8 `rt-bootstrap-capsule`** (CD-33, plan 45 §9.4; active from H04, when the capsule exists). Reads with `recovery-reader` (plan 14 §10), which now covers `<cluster>/capsule/`.
- **Light, daily** (`restore-test-bootstrap-capsule-light`, light check: no heavy-slot request): resolve `capsule/LATEST.json`; verify `capsule.json` with `cosign verify-blob --bundle capsule.json.sig` against `security/cosign/trusted-keys.yaml` from the deployed revision (the same key file the recovery bundle carries); `HeadObject` every file listed in `capsule.json` (size equals the manifest; `x-amz-object-lock-retain-until-date` present and ≥ upload time + 34 d); the capsule's lock-file sha256 values equal those of current `main` (a mismatch older than the 24 h refresh grace fails, `RT_CAPSULE_STALE`); the newest capsule is ≤ 31 days old; at least 3 capsules exist, or every existing capsule is younger than 35 days (plan 43 R-47).
- **Full, weekly** (`restore-test-bootstrap-capsule-full`, heavy test: `strange.company/heavy-slot` = 8, `emptyDir` sized to 1.2 × the capsule size from `capsule.json`, capacity gate of §9.3): stream every file through sha256 and compare with `capsule.json`; `skopeo inspect oci-archive:<file>` per image (the digest equals the lock-file digest); `helm show chart` per chart archive (version equals `versions/charts.lock.yaml`; vendored library subcharts are `type: library`, CD-26); `git bundle verify` of the repository bundle; check that the toolbox OCI archive's digest equals `toolbox/image.lock`. The toolbox `docker load` + `./bootstrap version` smoke test needs a container runtime, which a restricted pod does not have; it runs weekly in plan 52's off-cluster lane (requested) and in drill RD2.
- Results: `test_kind: bootstrap-capsule`, `subject: capsule:<capsule-id>`, failure codes `RT_CAPSULE_SIGNATURE`, `RT_CAPSULE_MISSING_OBJECT`, `RT_CAPSULE_RETENTION`, `RT_CAPSULE_STALE`, `RT_CAPSULE_DIGEST`.

### 9.6 RPO/RTO → mechanism → measured verification

| Data / service (SPEC §108) | Target RPO / RTO | Mechanism | RPO verification | RTO verification |
|---|---|---|---|---|
| `strange-company` committed Git | ≤ 1 h (near-zero) / ≤ 2 h | push mirror on every push (BK-11); bundles every 30 min (BK-09); Longhorn daily (BK-05) | mirror lag metric and `SCGitHubMirrorRPOBreach` (plan 17); `SCRepoBundleBackupStale` (2 h) | `rt-github-mirror` (render and SOPS structure from GitHub, weekly); `rt-repo-bundle` `restore_duration_seconds`; black-start drill end-to-end time (plan 45, quarterly) ≤ 2 h for the Git part |
| OpenTofu state | latest successful mutation / ≤ 2 h | versioning, conditional-write locking, encryption (BK-12) | RPO is structural: every apply writes a new version (06-AC-8) | `./bootstrap state verify` duration (plan 06) recorded in `rt-tofu-state`; import-rebuild path < 30 min (plan 06 §20.4) |
| Forgejo / Authentik / Strange Company PostgreSQL (and Woodpecker) | ≤ 15 min / ≤ 4 h | WAL archiving with `archive_timeout: 10min` + daily base (BK-01…04) | continuous: `SCCnpgWalArchiveBacklog` fires at > 900 s since last archival; this is a valid RPO signal only because RT-0 `dr-marker-heartbeat` writes to every database every 5 min, so an idle database still switches WAL every `archive_timeout` (STO-7); after a real loss, plan 45 measures the observed RPO from the newest restored heartbeat (DR-8); weekly: `rpo_observed_seconds` in `rt-cnpg-pitr-*`; P10: 12-AC-7 | weekly `rt-cnpg-pitr-*` `restore_duration_seconds` ≤ 3600 (`rto-budget` check), monthly `rt-app-*` (DB + app start); quarterly drill (plan 45) |
| Project Git repositories | ≤ 1 h / ≤ 4 h | bundles every 30 min (BK-09) + Longhorn daily (BK-05) | `SCRepoBundleBackupStale` with `maxAge: 1h`, `failAfter: 30m` on a 30-min schedule (DR-19, catalogue) plus OA-07 per-repository manifest age > 60 min; every repository gets 30-min incrementals (no size-based rate limit). The alert fires when the newest bundle is 1 h old, i.e. at the RPO target and not after it | `rt-repo-bundle` per-repository restore time; `restore-project-repository.md` is ≤ 1 h by construction |
| Critical Longhorn data | ≤ 24 h / ≤ 8 h | backups every 12 h (BK-05/06) + 6-hourly local snapshots | `SCLonghornBackupStale` (26 h): with 12-hour backups one missed run is tolerated before the alert, and the alert fires before the 24 h RPO is breached by a second miss | `rt-longhorn-volume` `restore_duration_seconds` ≤ 7200 |
| Logs / traces | best effort, ≤ 24 h / non-critical | Loki/Tempo on S3 (H03, plan 41) | — | — |
| Historical Prometheus metrics | best effort / non-critical | none (plan 11 §6.7) | — | — |
| etcd | accelerator (SPEC §110) | snapshots every 6 h, S3 copy (BK-08) | `SCEtcdSnapshotUploadFailing` (13 h) | `rt-etcd-snapshot` integrity; restore drill plan 07 |
| Recovery bundle | every rotation | BK-13 | `SCRecoveryBundleOutdated` | `restore-test-recovery-bundle` (35) |

No SPEC §108 target is weakened. Two are tightened: repository bundles run every 30 minutes instead of hourly, so the ≤ 1 h RPO holds including run time; and PostgreSQL archives at most every 10 minutes.

### 9.7 On-demand backup set (`backup now`)

Plan 16 requires a backup-first gate before Forgejo upgrades, and plan 13 before Longhorn upgrades. Semantics (CLI form owned by plan 50; plan 16 writes it as `./bootstrap exec -- sc-bootstrap backup now --component <c>`):

| Component | Actions | Done when |
|---|---|---|
| `forgejo` | CNPG `Backup` `forgejo-pg-ondemand-<ts>` (`method: plugin`); Longhorn `Backup` of `forgejo-data` (snapshot then backup, verify CR form); trigger `repo-bundle-backup` once (`kubectl create job --from=cronjob/repo-bundle-backup`) | CNPG `Backup` `completed`, Longhorn backup `Completed`, Job succeeded |
| `authentik`, `woodpecker`, `sc` | CNPG on-demand `Backup` | `completed` |
| `longhorn` | Longhorn backup of every important volume | all `Completed` |
| `hermes` | Longhorn backup of `hermes-data` | `Completed` |

The command writes `s3://<prefix>-evidence/backups/<component>/<ts>.json` with the backup ids. Timeout 60 min. It is idempotent: a rerun creates new backups, which is harmless.

### 9.8 Configuration ownership

| Knob | Location | Default | Change |
|---|---|---|---|
| Schedules, `maxAge`, budgets, table-count thresholds | `infra/dr/catalogue.yaml` → rendered into `gitops/platform/dr-tests/` and the catalogue alerts | §9.4, §9.5 | PR |
| Bundle cadence | `gitops/platform/forgejo/backup/` | `7,37 * * * *` | PR (RPO impact reviewed) |
| Rotation window for repository tests | catalogue | 4 weeks | PR |
| RTO budgets per test | catalogue | 3600 s (CNPG), 7200 s (Longhorn) | PR |

## 10. Secrets

| Class / instance | Object | Consumer | Notes |
|---|---|---|---|
| `SC-AGE-BACKUP` (new, plan 11 §10, ADR-0202) | private: `dr-tests/backup-age` (key `identity`); public: ConfigMap `forgejo/backup-age-recipients` | private: `restore-tester` (RT-2, RT-3); public: `repo-bundle-backup` | generated by plan 50 pre-seed; private key SOPS-encrypted in Git (both recipients) and in the recovery bundle. Rotation: add a new recipient (bundles encrypt to the new key only), keep the old identity in Git and the bundle until 400 d after the last bundle encrypted to it |
| `SC-FORGEJO-BOT-TOKENS/sc-backup` | `forgejo/forgejo-sc-backup`; copy `dr-tests/forgejo-sc-backup` | bundle backup, RT-2, RT-6, RT-7 | read-only scopes (plan 16) |
| `SC-OBJECT-STORAGE/repo-backups` | `forgejo/s3-repo-backups` | bundle backup | put-only (plan 14) |
| `SC-OBJECT-STORAGE/dr-tests-reader`, `/dr-tests-results`, `/tofu-state-ro` | `dr-tests/s3-*` | tests | plan 14 |
| `SC-PG-DR-MARKER` | `dr-tests/<cluster>-dr-marker` (copies) | RT-1 | plan 12 |
| `SC-GITHUB-MIRROR-READ` (new; owner plan 17) | `dr-tests/github-mirror-read` | RT-6 | collected from `bootstrap.env` before transfer (CD-19 (b); variable owned by plan 50/17); read-only (`contents:read`) on the mirror repository; GitHub App installation token preferred (L-6). Not needed when the mirror is public |

No restore test receives application superuser credentials of production clusters. Scratch clusters generate their own superuser secret.

## 11. Storage

- `repo-backup-cache`: local-path PVC in `forgejo` (plan 11 §9.2), mirror clones of every repository. Reconstructible.
- Scratch targets: local-path (CNPG `rt-*`), `longhorn-scratch` (`rt-*` volumes), `emptyDir` with `sizeLimit` (bundles, etcd). Capacity-gated (§9.3).
- `dr-canary`: 1Gi Longhorn.

## 12. Resource requirements

| Workload | Requests | Limits | Duration |
|---|---|---|---|
| `repo-bundle-backup` | 100m / 256Mi | 1Gi; ephemeral 4Gi | < 2 min typical; full weekly run < 15 min |
| `restore-tester` pod (driver) | 50m / 128Mi | 512Mi; ephemeral 4Gi | ≤ 2 h |
| Scratch CNPG instance | 100m / 256Mi | 512Mi | ≤ 60 min per cluster |
| Scratch Forgejo / Authentik (monthly) | 100m / 384Mi | 768Mi | ≤ 30 min |
| Longhorn scratch replica | instance-manager share | — | ≤ 2 h |

Only one heavy test runs at a time (Lease `dr-tests/restore-slot`). Exclusion against workers and CI comes from the `strange.company/heavy-slot` extended resource and PriorityClass `ci` (plan 51 §8.4, CD-05), consistent with ADR-0020's mutual exclusion.

## 13. Security boundary

- `dr-tests` is PSA `restricted`, tier `platform`. Default-deny NetworkPolicy (plan 36) plus allows: egress to the S3 endpoint (443), DNS, the API server (restore-tester only), `<cluster>-rw:5432` in `forgejo`, `authentik`, `woodpecker`, `strange-company` (pods labeled `app.kubernetes.io/name=restore-tester`, matching plan 12 §13), `forgejo-http:3000`, `github.com:443` (only pods labeled `strange.company/check=restore-test-github-black-start`; approximated per plan 36's FQDN strategy), and scratch pods to their scratch DB only. Scratch Forgejo and Authentik pods have **no** egress except the scratch DB. **Egress hardening (SEC-23):** `dr-tests` holds `SC-AGE-BACKUP`, repository read tokens and S3 readers while processing attacker-influenced restored data, so its Internet egress goes through the egress proxy (plan 36) with a profile allowing only the S3 endpoint and `github.com`/`api.github.com`; there is no general 443 egress.
- `repo-bundle-backup` (namespace `forgejo`): egress to `forgejo-http:3000` and the S3 endpoint only; no Kubernetes API token (`automountServiceAccountToken: false`).
- The restore-tester can read every backup. It is therefore a high-trust component: digest-pinned image built only by trusted CI, no shell entrypoint, no interactive access, and scratch objects restricted to `rt-*` by VAP.
- Bundles are encrypted to `SC-AGE-BACKUP`, so read access to `repo-backups` alone does not reveal code. CNPG, Longhorn and etcd backups rely on credential scoping and provider SSE (SI-G3-03).
- No worker, CI pod or Hermes can reach `dr-tests` or any backup credential [SPEC §88].

## 14. Happy path

1. P06: Flux reconciles `forgejo-config`; `repo-bundle-backup` takes the first full bundles within 30 minutes.
2. P10: Flux reconciles `backups` (`dr-tests` namespace content, CronJobs, `dr-canary` triplet, copies of secrets). `./bootstrap verify` itself triggers the first run of every restore test as one-shot Jobs created from the CronJobs (`verify.dr-prime`, plan 50, CD-19) and waits for them, so no manual step is needed for `CONTROL TRANSFER COMPLETE`. Restore tests that need components from later nodes (`rt-app-*` and `rt-longhorn-volume` for `hermes-data`) are scheduled but not required before those nodes.
3. Weekly: Monday CNPG, Tuesday repositories, Wednesday Longhorn, Thursday etcd, Friday GitHub black start; daily tofu-state freshness; monthly apps.
4. Each run writes results; Grafana's DR dashboard shows the newest result per test; alerts stay quiet.
5. H02: streams continue unchanged (same generations, same paths); restore tests pass after expansion.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Restore test finds bad backup (`failed`) | Job exit 1 → `SCRestore…Failed` within `failAfter` | backup unproven | none | `restore-test-failure.md` (by failure code) |
| Restore test cannot run (`errored`, e.g. `RT_INSUFFICIENT_CAPACITY`, API timeout) | same | proof is aging | none | `restore-test-failure.md` §Errored |
| Test not scheduled (CronJob suspended or deleted) | `maxAge` breach (stale variant) | proof is aging | none | `restore-test-failure.md` §Stale |
| Bundle job failing | `SCRepoBundleBackupStale` | repo RPO at risk | next run retries | `restore-test-failure.md` §Bundle backup |
| `SC-AGE-BACKUP` private key lost | RT-2 `RT_DECRYPT_FAILED` | bundles unreadable | none | restore the key from the recovery bundle; if lost there too, rotate (new recipient) and take a full chain immediately |
| Scratch objects left behind after a crash | janitor at next run; `retention-audit` RA-20 counts `rt-*` objects older than 6 h | disk use | janitor deletes | — |
| Marker table missing in a cluster (new or restored cluster) | RT-1 creates it (`IF NOT EXISTS`) | none | self-healing | — |
| Restore duration over budget | check `rto-budget` failed | RTO claim unproven | none | `restore-test-failure.md` §RTO |
| Mirror out of sync during RT-6 | `refs-in-sync` failed | black start would miss commits | none | plan 17 `github-mirror-failure.md` |
| SOPS file missing a recipient or not reachable from the cluster root | `sops-recipients` / `sops-reachable` failed | black start would fail | none | plan 35 `sops-decryption-failure.md` |

**Failure codes** (stable, `restore-test-result.failure.code`): `RT_INSUFFICIENT_CAPACITY`, `RT_TIMEOUT`, `RT_API_ERROR`, `RT_BACKUP_NOT_FOUND`, `RT_BACKUP_STALE`, `RT_RESTORE_FAILED`, `RT_MARKER_MISSING`, `RT_MARKER_UNEXPECTED`, `RT_SCHEMA_MISSING`, `RT_CHECKSUM_MISMATCH`, `RT_DECRYPT_FAILED`, `RT_BUNDLE_INVALID`, `RT_FSCK_FAILED`, `RT_REF_MISMATCH`, `RT_DIVERGED`, `RT_RENDER_FAILED`, `RT_SOPS_RECIPIENTS`, `RT_SECRET_PATTERN`, `RT_LFS_POINTER`, `RT_ETCD_INTEGRITY`, `RT_RTO_BUDGET_EXCEEDED`, `RT_APP_UNHEALTHY`, `RT_RESULT_STALE`, `RT_LOCK_HELD`, `RT_CAPSULE_SIGNATURE`, `RT_CAPSULE_MISSING_OBJECT`, `RT_CAPSULE_RETENTION`, `RT_CAPSULE_STALE`, `RT_CAPSULE_DIGEST`, `RT_TOFU_LOCK_STALE`, `RT_SOPS_UNREACHABLE`.

## 16. Retry behavior

- Restore tests: `backoffLimit: 0`. One failure is one result. A manual rerun is a new result (procedure).
- Inside a test, only transient API calls are retried (3 × with 2 s/4 s/8 s backoff). Content checks never retry, except RT-6 `refs-in-sync` (one recheck after 10 min for mirror lag).
- `repo-bundle-backup`: per-repository failures do not stop others; the next run retries with an incremental from the last uploaded manifest. Three consecutive failed runs exceed `maxAge` and alert.
- `restore-slot` Lease busy: wait up to 30 min, then `errored` with `RT_LOCK_HELD`.

## 17. Idempotency behavior

- Bundle uploads use `If-None-Match: *` and unique keys; a rerun never overwrites. A crash after uploading a bundle but before its manifest leaves an orphan object without a manifest. The restore algorithm ignores objects without manifests, and plan 43's lifecycle removes them.
- Restore tests create uniquely named scratch objects per `result_id` and delete them. The janitor makes reruns safe after crashes.
- Marker inserts are per `run_id`; old markers are pruned.
- `backup now` creates new backups each time.

## 18. Bootstrap behavior

- During seed (P04) only CNPG WAL archiving and base backups (plan 12) run for `forgejo-pg`; the Longhorn backup target is configured in the seed (plan 13). Bundles start after control transfer (layer `forgejo-config`). Restore tests start at P10.
- Before P10, backups exist but are unproven. `./bootstrap verify` (P12) requires each restore test to have produced a `passed` result (§23).
- Black start (plan 45): the restore algorithms here are reused. While recovery mode is active, the heavy and light restore-test CronJobs in `dr-tests` (all `restore-test-*`, `dr-canary-writer`) are suspended; `repo-bundle-backup`, `dr-marker-heartbeat`, `objstore-canary`/`objstore-audit` and `storage-audit` keep running. The suspension is a `spec.patches` entry of plan 45's `recovery-mode` component on the `backups` layer (DR-10; requested from plan 45/ADR-0554). `recover --step finalize` force-runs every restore test once against the new generations before it reports `CONTROL TRANSFER COMPLETE (RECOVERY)` (DR-1).

## 19. Upgrade behavior

- `restore-tester` image updates go through trusted CI and Renovate like every internal image. Each version must pass `make e2e-dr` (§22) before release.
- CNPG plugin, Longhorn and k3s upgrades re-run the relevant restore test within 24 h after the upgrade (plan 48's upgrade pipeline triggers it as a one-shot Job, like `verify.dr-prime`: `kubectl create job --from=cronjob/restore-test-<x>`).
- Changing the bundle format (`sc-repo-bundle/v1`) requires restore support for the previous format for 400 d (the longest retention).

## 20. Recovery behavior

| Scenario | Procedure |
|---|---|
| CNPG PITR or replacement | `restore-cnpg.md` (plan 12) |
| Longhorn volume restore | `restore-longhorn.md` (plan 13) |
| Single project repository | `restore-project-repository.md` (this plan) |
| Whole Forgejo | `restore-forgejo.md` (plan 16) using BK-01, BK-05, BK-09 |
| Total loss / black start | `total-cluster-loss.md`, `github-black-start.md` (plan 45) |
| A restore test failed | `restore-test-failure.md` |

## 21. Observability

- **Signals:** kube-state-metrics CronJob series for every check (ADR-0553); CNPG, Longhorn and etcd native metrics for backup freshness (plans 12, 13, 07); results in S3 for evidence.
- **Catalogue alerts consumed:** `SCRepoBundleBackupStale`, `SCRestoreTestCnpgFailed`, `SCRestoreTestReposFailed`, `SCRestoreTestLonghornFailed`, `SCTofuStateRestoreTestFailed`, `SCGitHubBlackStartTestFailed`, `SCOffsiteBackupFailing24h` (recording rule `sc:backup_last_success_timestamp_seconds` over all streams of §9.1).
- **New alerts for plan 42** (all `category: restore-test` or `backup`, `alwaysNotify: failed-backup`, `signalOwnerPlan: "44"`, runbook `procedures/restore-test-failure.md`):

| alertname | Source | Parameters |
|---|---|---|
| `SCRestoreTestEtcdFailed` | check-cronjob `restore-test-etcd` (dr-tests) | maxAge 8d, failAfter 1h, warning |
| `SCRestoreTestAppsFailed` | check-cronjob `restore-test-apps`  | maxAge 35d, failAfter 4h, warning |
| `SCDrCanaryWriterFailed` | check-cronjob `dr-canary-writer` | maxAge 26h, failAfter 1h, warning (`alwaysNotify: none`; the Longhorn test catches consequences) |
| `SCDrMarkerHeartbeatFailed` | check-cronjob `dr-marker-heartbeat` | maxAge 30m, failAfter 15m, warning (`alwaysNotify: failed-backup`: without heartbeats the RPO signal is invalid) |
| `SCBootstrapCapsuleCheckFailed` | check-cronjob `restore-test-bootstrap-capsule-light` | maxAge 26h, failAfter 1h, warning, `black-start-threat` (CD-33; already named by plan 45) |
| `SCBootstrapCapsuleFullCheckFailed` | check-cronjob `restore-test-bootstrap-capsule-full` | maxAge 8d, failAfter 3h, warning, `black-start-threat` |

- **Dashboard (plan 39):** "Disaster recovery": newest result per `test_id` (outcome, age, `restore_duration_seconds`, `backup_age_seconds`), backup freshness per stream, the §9.6 table with live values.

## 22. Testing strategy

- **Unit:** bundle decision logic (full vs incremental vs unchanged, deleted refs, chain rollover), manifest generation, restore algorithm with deleted refs and force-pushes, cron first-Saturday guard, result-document generation (validated against the schema), failure-code mapping.
- **Contract:** every result document produced in tests validates against `restore-test-result.schema.yaml` (F02).
- **k3d + SeaweedFS emulator** (`make e2e-dr`, deterministic, no paid APIs): Forgejo (plan 16 test chart) with three repositories → `repo-bundle-backup` (full, then incremental after a push, then a force-push and a branch deletion) → RT-2 passes; corrupt one ciphertext byte → RT-2 fails with `RT_CHECKSUM_MISMATCH`. CNPG (plan 12's `make e2e-cnpg` cluster) → RT-1 passes; delete the WAL objects after T → RT-1 fails with `RT_RESTORE_FAILED` or `RT_MARKER_MISSING`. RT-5 with a fixture result 40 days old → `RT_RESULT_STALE`. RT-6 against a local Git HTTP server standing in for GitHub, with a SOPS fixture missing one recipient → `RT_SOPS_RECIPIENTS`. Each failing Job raises its alert in the k3d Prometheus within `failAfter + 2m`.
- **Lab VMs** (with plan 13): RT-3 and RT-4 against real Longhorn and k3s snapshots.
- **Live:** gates below.

## 23. Acceptance criteria

### GATE-P10
- **44-AC-1** `./bootstrap exec -- kubectl get cronjob -A -l strange.company/check -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}'` lists at least: `forgejo/repo-bundle-backup`, `dr-tests/dr-canary-writer`, `dr-tests/dr-marker-heartbeat`, `dr-tests/restore-test-cnpg`, `dr-tests/restore-test-repos`, `dr-tests/restore-test-longhorn`, `dr-tests/restore-test-etcd`, `dr-tests/restore-test-github-black-start`, `dr-tests/restore-test-tofu-state`, `dr-tests/check-tofu-lock-age`, `dr-tests/restore-test-apps`, `dr-tests/objstore-canary`, `dr-tests/objstore-audit`, `dr-tests/storage-audit`.
- **44-AC-2** Bundles: `./bootstrap exec -- aws s3api list-objects-v2 --bucket <prefix>-repo-backups --prefix git/weekly/strange-company/strange-company/ --query 'Contents[?ends_with(Key, `.json`)] | sort_by(@, &LastModified)[-1].LastModified'` is < 45 min old, and the matching bundle object is not plaintext (`head -c 21` of the downloaded object = `age-encryption.org/v1`).
- **44-AC-3** Put-only: the `repo-backups` key cannot delete (`aws s3api delete-object` → `AccessDenied`) and cannot overwrite (a second `put-object` with `If-None-Match: *` on an existing key → 412).
- **44-AC-4** For each of `forgejo-pg`, `authentik-pg`, `woodpecker-pg`, `sc-pg`: the newest document under `evidence/restore-tests/rt-cnpg-pitr-<cluster>/` validates against the schema, has `outcome: passed`, checks `marker-a-present`, `marker-b-absent`, `app-db-present`, `rto-budget` all `passed`, and `measurements.restore_duration_seconds` ≤ 3600.
- **44-AC-5** `rt-repo-bundle` has a passed result for `strange-company/strange-company` and for every other repository existing at P10 (one document each), each with `refs-match-manifest` passed and `refs_compared` ≥ 1.
- **44-AC-6** `rt-longhorn-canary` passed, and `rt-longhorn-volume` passed for `forgejo-data` (first run triggered by `./bootstrap verify` with `--volume forgejo-data`) with `restore_duration_seconds` ≤ 7200.
- **44-AC-7** `rt-etcd-snapshot` passed with `etcdutl-restore` passed; `rt-tofu-state` passed (requires a `./bootstrap state verify` run, 06-AC-7); `rt-github-mirror` passed with all eight checks (none of which needs an age identity, CD-10).
- **44-AC-8** `rt-app-forgejo` and `rt-app-authentik` passed (first run triggered by `./bootstrap verify`, CD-19), satisfying 16-AC-29 and plan 15's restore-test criterion.
- **44-AC-9** Failure visibility (live, non-destructive): create a one-off Job from `restore-test-etcd` with `--inject-failure RT_ETCD_INTEGRITY` (test flag compiled into the image, accepted only together with label `strange.company/drill=true`). The Job exits 1 and `SCRestoreTestEtcdFailed` reaches the notification test receiver within `failAfter + 5m`. A subsequent normal run resolves it.
- **44-AC-10** Staleness: `kubectl -n dr-tests patch cronjob restore-test-etcd -p '{"spec":{"suspend":true}}'` in the k3d e2e environment with `maxAge` overridden to 10 min raises the stale alert within 12 min (live gate: verified by promtool unit test of the rendered rule, plus the k3d run).
- **44-AC-11** Coverage: `./bootstrap exec -- sc-restore-test coverage --register plans/11-storage.md --catalogue infra/dr/catalogue.yaml` exits 0 (every register row with a backup has a stream and a test; this is plan 11's 11-AC-5).
- **44-AC-12** No scratch leftovers: 6 h after the last test, `kubectl get clusters.postgresql.cnpg.io,pvc,pod -n dr-tests -o name | grep -c '/rt-'` = `0`, and `kubectl -n longhorn-system get volumes.longhorn.io -o name | grep -c '/rt-'` = `0`.
- **44-AC-13** On-demand set: `backup now --component forgejo` completes within 60 min and writes `evidence/backups/forgejo/<ts>.json` naming a completed CNPG backup, a completed Longhorn backup and a successful bundle Job.
- **44-AC-14** `make e2e-dr` passes in CI (all negative cases fail with the expected codes and raise their alerts).
- **44-AC-18** Heartbeat (DR-8): `kubectl -n dr-tests get cronjob dr-marker-heartbeat -o jsonpath='{.status.lastSuccessfulTime}'` is < 15 min old, and on each of the four clusters `select max(inserted_at) from markers where label='heartbeat'` is < 10 min old. In the k3d e2e, a scratch PITR to "latest" taken 20 min after the last insert into `woodpecker-pg` (no application writes) contains a heartbeat row ≤ 900 s older than the restore target.
- **44-AC-19** Rehydrate (STO-9, k3d `make e2e-dr`): restore `forgejo-pg` with three repositories onto an empty `forgejo-data`, run `repo-rehydrate`; all three repositories clone with refs equal to their manifests, a push to a protected branch with a non-admin key is rejected, and a push to an unprotected branch fires the organization webhook.
- **44-AC-20** Admission (STO-6, F12 k3d): as `restore-tester`, creating PV `rt-x` with `hostPath: /` is denied, PV `rt-y` with `csi.volumeHandle: forgejo-data` is denied, and PV `rt-z` with `volumeHandle: rt-z` and class `longhorn-scratch` is admitted.
- **44-AC-21** No size rate limit (STO-13): in `make e2e-dr`, a 3 GiB fixture repository receives an incremental bundle within 35 min of a push, and the bundle job's `ephemeral-storage` usage stays < 1 GiB during its weekly full (streaming).
- **44-AC-23** Streamed restore (STO-15, `make e2e-dr`): RT-2 restores the 3 GiB fixture repository with `refs-match-manifest` passed while the test pod's `emptyDir` usage stays ≤ 1.3 × the restored repository size (measured by `du` in the result's `measurements.restored_bytes` against the pod's ephemeral-storage stats), and a single corrupted ciphertext byte yields `RT_CHECKSUM_MISMATCH`.

### GATE-C08
- **44-AC-22** `rt-longhorn-volume` passed for `hermes-data` (moved from P10 because `hermes-data` arrives with the Hermes layer at C08; GC-01).

### GATE-H02
- **44-AC-15** After expansion to three CNPG instances, each cluster's plugin `serverName` is unchanged (`<cluster>-g<N>`, no generation bump), WAL archiving is OK, and a base backup completed after the expansion (`kubectl cnpg status`).
- **44-AC-16** A forced run of `restore-test-cnpg` and `restore-test-longhorn` after H02 passes. The Longhorn result shows the source volume had `numberOfReplicas: 2` (recorded in `checks[].observed` of `source-replicas`).
- **44-AC-17** `repo-bundle-backup` keeps producing manifests during the node-loss drill (13-AC-17): no gap > 1 h in `git/weekly/strange-company/strange-company/` manifests (the CronJob reschedules to a surviving node; its cache PVC is local-path, so on a different node it starts from S3 state, §9.2.2 step 4).

### GATE-H04
- **44-AC-24** Bootstrap capsule (CD-33): `kubectl -n dr-tests get cronjob restore-test-bootstrap-capsule-light restore-test-bootstrap-capsule-full` exist; the newest `rt-bootstrap-capsule` documents (light and full) have `outcome: passed`; the light document names the capsule that `capsule/LATEST.json` points to, and the full document's checks include every image of `versions/images.lock.yaml` (one `skopeo-inspect` check each). In the k3d e2e (`make e2e-dr`), a capsule with one object removed fails the light check with `RT_CAPSULE_MISSING_OBJECT`, and a lock-file change on `main` older than 24 h without a new capsule fails it with `RT_CAPSULE_STALE`; `SCBootstrapCapsuleCheckFailed` reaches the test receiver within `failAfter + 5m`.

## 24. Explicit non-goals

- Backing up reconstructible state (plan 11 §6.7).
- Restoring production in place from an automated test (tests are always scratch).
- Client-side encryption of CNPG, Longhorn or etcd backups (SI-G3-03).
- Forgejo `dump` archives as a backup mechanism: they duplicate CNPG + bundles + Longhorn, need write locks and are large. Rejected.
- Automated full black-start rehearsals on paid infrastructure (the operator drills are RD2 quarterly and RD3 yearly, plan 45).
- Backing up the GitHub mirror itself (it is a copy).

## 25. Open questions

- **Q-44-1** (resolved by CD-10) The in-cluster copy of `SC-AGE-CLUSTER` for RT-6 is rejected. RT-6 uses the structural recipient check, lint S-13 and the quarterly `recovery-bundle verify --full` attestation.
- **Q-44-2** (blocking: no) New secret classes: `SC-AGE-BACKUP` (plan 11 §10) and `SC-GITHUB-MIRROR-READ` (owner plan 17). Owner: plans 35, 17.
- **Q-44-3** (blocking: no) The ADR-0553 alert form uses `kube_cronjob_status_last_successful_time`, which a skipped run would refresh. Monthly tests on a "first Saturday" schedule need a result-based freshness variant. Proposed: plan 42 adds `resultBased: true`, evaluated from a KSM-exported ConfigMap or from a Job label `strange.company/outcome=passed` set by the tester on its own Job (requires `patch jobs` on itself, verify). Simpler fallback: schedule `restore-test-apps` with `0 4 1 * *` (1st of the month) and drop the Saturday preference. **Resolution chosen until plan 42 decides: the fallback (`0 4 1 * *`).** Owner: plan 42.
- **Q-44-4** (blocking: no) Plan 16 16-AC-29 mentions a "marker issue". Production writes for testing are excluded (44-R-2), so RT-7 checks repository presence instead. Owner: plan 16 to align the AC text.
- **Q-44-5** (blocking: no) `./bootstrap verify` (plan 50) should include a `dr` section: V-check "every `rt-*` test id has a passed result younger than its `maxAge`" (implemented with `sc-restore-test last-results --json`). Owner: plan 50.
- **Q-44-6** (blocking: no) Plan 16 §20.3 uses the bundle path `repo-backups/<org>/<repo>/<ts>.bundle`. The authoritative layout is §9.2.2 (`git/weekly/<org>/<repo>/<chain>/…bundle.age`). Owner: plan 16 to align.
- **Q-44-7** (blocking: no) The catalogue's `SCRepoBundleBackupStale` runbook is `restore-forgejo.md`; the better runbook is `restore-test-failure.md` §Bundle backup. Owner: plan 42.
- **Q-44-8** (resolved) ADR-0554 (revised) uses plan 12's `serverName <cluster>-g<N>` and the cluster-level `cnpg-recovery` component.
- **Q-44-10** (resolved by plan 51) STO-23: heavy restore tests set 8 heavy-slot units and plan 51 adds a `dr-tests` memory quota. The earlier rejection is withdrawn.
- **Q-44-11** (blocking: no) STO-21: repositories in personal namespaces are invisible to `sc-backup`. Proposed: plan 16 sets `[repository] MAX_CREATION_LIMIT = 0` for regular users, so all repositories live in organizations the bot can read. Until then, the coverage check compares the organization repository count with the bundled set only. Owner: plan 16.
- **Q-44-9** (resolved by CD-16) Light checks omit the `strange.company/heavy-slot` request entirely; heavy restore tests (scratch CNPG, Longhorn restore, scratch apps) request units like a CI job.
