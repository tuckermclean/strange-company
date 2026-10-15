# Procedure: Replace the object-storage provider, account or bucket prefix

**Owning plan:** plans/14-object-storage.md (with plans 06, 12, 13, 35, 44, 50)
**SPEC references:** §15, §26, §29, §108, §111, §113 ("Stage 0 SHALL add procedures discovered to be missing"), §114
**Severity / urgency:** disaster (provider or account lost: the cluster has no off-cluster backups) or routine (planned migration)
**Expected duration:** 2–4 h of operator work; the first full backups complete within about 24 h
**Requires break-glass:** cluster administration; new provider credentials (`admin` of the new account)

## When to use
- The object-storage provider or account is permanently lost (closure, deletion, unrecoverable compromise). Black-start material in S3 (tofu state, journal, bundle copy) is gone along with the backups (review finding DR-13).
- A planned move to another provider, endpoint or `S3_BUCKET_PREFIX`.
- Not for an outage that will end: use `object-storage-outage.md`.

## Preconditions and required material
- A working cluster (for a black start without object storage, see "Black start without object storage" below).
- The recovery bundle **file** and passphrase from the operator's local black-start directory: the S3 copy may be gone.
- A new provider account that passes the capability suite (plan 14 §14.3), and its `admin` key. In `shared` mode, a second account for `tofu-state`/`recovery` as well (plan 14 14-R-2a).
- `./bootstrap shell` at a checkout of `strange-company` `main`.

## Safety checks before acting
1. Until the first new backups complete, **the cluster is the only copy** of every database and repository. Do not run upgrades, expansions or destructive procedures in the meantime. Engage the worker emergency stop (`budget-emergency-stop.md`, Git or Kubernetes source).
2. `S3_ENDPOINT`, `S3_PROVIDER` and `S3_BUCKET_PREFIX` are immutable for normal runs (plan 50). This procedure changes them only through the typed confirmation `migrate-object-storage <cluster>` (requested from plan 50, CD-14).
3. Do not reuse CNPG generations or Longhorn backup targets across providers: archives start fresh.

## Steps
1. **Edit `bootstrap.env`:** new `S3_ENDPOINT`, `S3_REGION`, `S3_PROVIDER`, credentials, and `S3_BUCKET_PREFIX` if it changes. Keep the old values in a note for step 11.
2. **Preflight:** `./bootstrap preflight`. Expected: PF-12/PF-13 pass for the new endpoint; the immutable-key change is reported as `CONFIG_IMMUTABLE_CHANGED` and accepted only with step 3's confirmation.
3. **Storage apply and credentials:** `./bootstrap storage apply --migrate` (requested from plan 50) with typed `migrate-object-storage <cluster>`. It runs `plan.storage-confirm`, creates the ten buckets, mints every instance (plan 14 §10), writes the in-cluster SOPS files, and runs the capability suite including OC-17. Expected: `objstore-capabilities.json` with `fatal: []`.
4. **OpenTofu state:** if the old state is readable (planned migration), copy the current version of each module key to the new `tofu-state` bucket with `./bootstrap state migrate-backend` (requested from plan 06/50); otherwise rebuild by import: `recover-opentofu-state.md#rebuild-by-import` (plan 06 §20.4). Expected: `./bootstrap state verify` passes against the new bucket.
5. **Recovery bundle:** `./bootstrap rotate refresh-bundle` re-seals the bundle with the new credentials and uploads it to the new `recovery` bucket. Expected: `restore-test-recovery-bundle` passes at its next run (force it).
5a. **Copy the systems of record that have no other copy (DR-B1; planned migration, or a lost account whose old buckets are still readable):** `forgejo-objects` (B7: internal OCI registry, LFS, attachments, packages) and `evidence` (B9: TDD provenance, logs, results), and optionally `telemetry` (B10). Use a server-side copy where both buckets are at the same provider, otherwise `rclone copy --checksum` from the toolbox with the old and new `admin` keys (verify `rclone` is in the toolbox, plan 02): `rclone copy --checksum old:<oldprefix>-forgejo-objects new:<newprefix>-forgejo-objects` and the same for `evidence`. Copy current versions only (history is not migrated). Expected: per-prefix object counts and total bytes equal (`rclone check`). In `shared` mode the new B7/B9 are COMPLIANCE-locked, so copied objects get the lock at upload.
   If the old provider is unreadable (provider lost): internal images come back from the bootstrap capsule's image archives or the GHCR bootstrap digests (plan 44 §9.3) through the plan 45 image-source override; LFS objects and attachments come back from `repo-backups/lfs/` and `attachments/` **only if** `repo-backups` itself survived (same provider: usually not); otherwise LFS content and attachments are lost and the evidence record says so.
6. **Git change (one PR, or direct apply if Forgejo or the merge gate is impaired; same rule as `restore-cnpg.md` safety check 5):** new `S3_ENDPOINT`/`S3_REGION`/`S3_BUCKET_PREFIX` in `cluster-vars`, the new SOPS credential files, and **every** `CNPG_<ID>_GEN` incremented by one (`SOURCE_GEN` unchanged; no recovery component: running clusters only change their archive destination).
7. **CNPG:** after Flux applies, each cluster archives into `<cluster>-g<N+1>` on the new provider. Take an on-demand base backup of each at once (`restore-cnpg.md` §PITR step 1 manifest). Expected: `kubectl cnpg status` shows working archiving and a completed backup for all four clusters.
8. **Longhorn:** the `BackupTarget default` points at the new bucket; trigger a backup of every important volume (`backup now --component longhorn`, plan 44 §9.7). Expected: `Completed`.
9. **Repository bundles:** `kubectl -n forgejo create job --from=cronjob/repo-bundle-backup rb-full-$(date +%s)` with `SC_BUNDLE_FORCE_FULL=1`. Expected: a new chain for every repository.
10. **etcd:** `k3s etcd-snapshot save --name pre-migrate` on a server node; the node config now carries the new etcd-s3 credential (plan 07 configure re-run). Expected: the object appears in the new `etcd-snapshots` bucket.
11. **Old provider (planned migration only):** keep it read-only until every restore test has passed against the new provider (step 12) and the new backups are older than the longest RPO window; then revoke the old in-cluster keys. Deleting the old buckets is a manual decision (`safe-destruction.md`).
12. **Force all restore tests** (`restore-test-failure.md` §Manual run, or `./bootstrap verify` which triggers them, CD-19) and disengage the emergency stop when they pass.

### Black start without object storage (provider lost together with the cluster)
`./bootstrap recover` cannot restore databases or volumes without backups. Run it with the new provider configured in `bootstrap.env` and the flags `--no-backups` (requested from plan 50/45): it performs steps 2–5 first, then seeds with `initdb` bootstraps, and repositories come back from GitHub (`strange-company`) only. This is total data loss for everything except Git on GitHub, as plan 45 S16 states.

## Verification
- `./bootstrap verify` passes, including the object-storage checks and every restore test against the new buckets.
- `objstore-audit` and `objstore-canary` pass; `SCOffsiteBackupFailing24h` resolved.
- `backup.LastSuccess` (plan 14 §7.3) reports every stream younger than its window on the new provider.

## Rollback / abort
- Before step 6 the cluster still writes to the old provider (if it exists): abort by restoring the old `bootstrap.env` values.
- After step 6 the generations have moved; to go back, repeat this procedure towards the old provider (with a further generation bump).

## Evidence to record
A `restore-test-result`-style record under `evidence/migrations/<ts>.json` (written with `dr-tests-results` from the toolbox): old and new endpoint and prefix, new generations, completion time of the first backup per stream, and the restore-test result ids.

## Follow-ups
- Update the bootstrap report and any operator documentation that names the old endpoint.
- Review the cost estimate for the new provider (plan 14 §12).
