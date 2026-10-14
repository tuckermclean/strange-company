# Procedure: Object-storage outage, credential failure or bucket drift

**Owning plan:** plans/14-object-storage.md
**SPEC references:** §15, §26, §29, §96, §97, §108, §113 ("object-storage outage")
**Severity / urgency:** degraded (backups paused, services up) → black-start threat if it lasts days; outage only for Forgejo packages/LFS and new internal image pulls
**Expected duration:** provider-dependent; operator work 15–60 min
**Requires break-glass:** no for diagnosis; the `admin` object-storage credential (from `bootstrap.env` or the recovery bundle) for drift repair and credential re-minting

## When to use
- `SCObjectStorageUnreachable` (blackbox probe), `SCObjectStorageCanaryFailing`.
- Several backup alerts at once: `SCCnpgWalArchiveFailing`, `SCLonghornBackupTargetUnavailable`, `SCEtcdSnapshotUploadFailing`, `SCRepoBundleBackupStale`, `SCOffsiteBackupFailing24h`.
- `SCObjectStorageConfigDrift` (versioning, lifecycle, Object Lock or public-access changed): §Drift.
- A single stream failing with 403/`InvalidAccessKeyId`: §Credential.
- Bootstrap or `./bootstrap` commands failing with `JOURNAL_REMOTE_WRITE_FAILED`, `TOFU_STATE_*` network errors or seed upload errors.

## Preconditions and required material
- `./bootstrap shell` (aws, kubectl, jq). In the shell, `S3_ENDPOINT` and `S3_BUCKET_PREFIX` come from `bootstrap.env`.
- The provider's status page and console access.
- For §Drift and §Credential: the `admin` credential (`bootstrap.env` or recovery bundle, `./bootstrap rotate refresh-bundle` documents unlocking).

## Safety checks before acting
1. **Do not delete WAL files, `.ready` files, Longhorn snapshots or local etcd snapshots to free disk.** Deleting WAL breaks PITR and can corrupt the database.
2. Do not switch the installation to a different provider or bucket prefix during an outage (`S3_ENDPOINT` and `S3_BUCKET_PREFIX` are immutable; a provider change is a migration, plan 14 §19).
3. Do not run `./bootstrap destroy`, `expand` or `recover` during an outage: they need state and journal access.
4. The dominating risk is the root disk filling with WAL. Watch it from the first minute.

## Steps

### Triage (5 minutes)
1. Is it the endpoint or a credential? From the toolbox:
   `curl -s -o /dev/null -w '%{http_code}\n' "$S3_ENDPOINT"` (expected when healthy: 200/403/404, any HTTP answer); then
   `aws --endpoint-url "$S3_ENDPOINT" s3api head-bucket --bucket "${S3_BUCKET_PREFIX}-evidence"` with the `admin` profile.
   - Timeouts / 5xx / DNS errors → §Outage.
   - 403 with `admin` → §Account.
   - `admin` works, one in-cluster stream gets 403 → §Credential.
2. Latest canary result: `kubectl -n dr-tests logs job/$(kubectl -n dr-tests get jobs -l strange.company/check=objstore-canary --sort-by=.metadata.creationTimestamp -o name | tail -1 | cut -d/ -f2)`.
3. Check the provider status page and record the incident reference.

### §Outage (provider or network)
1. **Measure the WAL risk** (Prometheus, through Grafana or `kubectl -n monitoring port-forward svc/prometheus-operated 9090` in the shell):
   - `sum by (cluster) (cnpg_collector_pg_wal_archive_status{value="ready"})` (WAL segments waiting, 4 MiB each);
   - root disk free and projection: `node_filesystem_avail_bytes{mountpoint="/"}` and `predict_linear(node_filesystem_avail_bytes{mountpoint="/"}[2h], 12*3600)`.
   Tolerance (STO-3): **hours left ≈ (free bytes − 15 % of the filesystem) ÷ WAL growth per hour**, where WAL growth is `sum(deriv(cnpg_collector_pg_wal{value="size"}[1h]))` (verify the metric name) or, at minimum, ≈ 2.3 GiB/day for four clusters at idle with heartbeat writes (plan 12 §9.3). The 15 % subtracts kubelet soft eviction. Recompute every 2 hours; do not rely on a fixed number of days.
2. **Pause avoidable writers** to slow WAL growth (they are not needed during an outage): engage the worker emergency stop (`budget-emergency-stop.md`, scope `workers`) so no autonomous tasks run; CI keeps working.
3. **Wait** for the provider. Nothing else is required: every consumer retries (plan 14 §15.1). Re-check step 1 every 2 hours.
4. **Decision point — WAL emergency** (only if the projection says the root filesystem reaches < 10 % free within 12 hours **and** the provider gives no recovery time inside that window):
   a. Prefer more disk: if the provider can enlarge the disk or the operator can free non-WAL space (`disk-pressure.md`, plan 08: image prune, journald vacuum), do that first.
   b. Otherwise, detach the WAL archiver of the cluster with the largest backlog (usually `sc-pg` or `woodpecker-pg`), accepting a **PITR gap** for that cluster from now until a new base backup completes after the outage:
      - PR (Forgejo and Git keep working during an S3 outage) that removes `isWALArchiver: true` from that cluster's `plugins` entry (the component `cnpg-wal-archiver-detached`, plan 12), merged by a human; Expect the PR route to fail when CI images are not cached on the node (internal images live in `forgejo-objects`, which is down); then use the direct patch below.
      - or, if the disk will fill before a PR can merge: `flux suspend kustomization <db-layer>` and `kubectl -n <ns> patch cluster <cluster> --type json -p '[{"op":"replace","path":"/spec/plugins/0/isWALArchiver","value":false}]'`.
      Expected: within 15 minutes the `ready` count for that cluster drops to 0 as PostgreSQL recycles WAL after checkpoints (verify CNPG's behavior when the archiver is removed).
   c. Record the time of the detach: PITR for that cluster cannot target times between the detach and the first base backup after re-attachment.
5. **When the endpoint is back**: go to §Post-outage verification.

### §Credential (one stream gets 403)
1. Identify the instance from the failing consumer (plan 14 §10 table: e.g. CNPG plugin logs → `pg-backups-<cluster>`, Longhorn → `longhorn-backups`).
2. Test the key directly: `kubectl -n <ns> get secret <secret> -o json | jq -r '.data.ACCESS_KEY_ID|@base64d'` to read the key id, then check with the provider console whether it exists and is enabled. Do not print the secret part.
3. Re-mint per `rotate-storage-credentials.md` (plan 35): mint a new key for the instance with `admin`, update the SOPS file, merge, let Flux apply, restart the consumer if it does not re-read Secrets, then revoke the old key.
4. Expected: the stream's next operation succeeds; the canary for that instance passes.

### §Drift (`SCObjectStorageConfigDrift`)
1. Read the failing OA checks in the newest `objstore-audit` result (`evidence/restore-tests/objstore-audit/…` or the Job log).
2. **Treat unexpected drift as a possible compromise** (in-cluster credentials cannot change bucket configuration, plan 14 §10): check the provider's audit log for who changed it, and follow `credential-compromise.md#object-storage` (plan 35) if it was not an operator.
3. Re-apply the desired configuration with the storage root module from the toolbox (`./bootstrap storage apply`, requested from plan 50; until it exists, run `./bootstrap` resume with the storage step forced as plan 50 documents). Manual fallback with `admin`:
   - versioning: `aws --endpoint-url "$S3_ENDPOINT" s3api put-bucket-versioning --bucket <bucket> --versioning-configuration Status=Enabled`
   - lifecycle: render `infra/objstore/buckets.yaml` to JSON (`sc-bootstrap objstore render-lifecycle --bucket <purpose>`, plan 14) and `put-bucket-lifecycle-configuration --lifecycle-configuration file://lifecycle.json`
   - public access block: `put-public-access-block` (AWS) or the provider console.
   Object Lock cannot be re-enabled on most providers after it was removed; record it and plan new buckets (plan 14 §9.4).
4. Expected: a manual `objstore-audit` run passes: `kubectl -n dr-tests create job --from=cronjob/objstore-audit oa-manual-$(date +%s)`.

### §Undelete (objects deleted or overwritten by a compromised credential; STO-11)
Trigger: OA-09 (sudden loss), `SCObjectStorageConfigDrift`, or a restore test reporting `RT_BACKUP_NOT_FOUND` while backups were taken. History survives only for the noncurrent window (14 d for B4/B5/B8, 30 d for B6/B7/B9) or the lock period, so act within days.
1. **Freeze expiry first:** with `admin`, remove the bucket's `noncurrent` lifecycle rule for the duration of the incident (`aws s3api get-bucket-lifecycle-configuration` → save to `lifecycle-before.json`; `put-bucket-lifecycle-configuration` without the noncurrent rule). Record the change; `objstore-audit` will report drift until step 5.
2. **Find the damage:** `aws s3api list-object-versions --bucket <bucket> --prefix <prefix> --query 'DeleteMarkers[?IsLatest==\`true\` && LastModified>=\`<T>\`].[Key,VersionId]' --output text > markers.tsv` (T = earliest suspected compromise), and for overwrites `Versions[?IsLatest==\`true\` && LastModified>=\`<T>\`]`.
3. **Undelete:** for each delete marker, `aws s3api delete-object --bucket <bucket> --key <key> --version-id <marker-version-id>` (removing the marker makes the previous version current; `admin` only). For each malicious overwrite, copy the previous version over it: `aws s3api copy-object --bucket <bucket> --key <key> --copy-source <bucket>/<key>?versionId=<previous>`.
4. **Verify:** force the restore test of the affected stream (`restore-test-failure.md` §Manual run) and for `forgejo-objects` the `rt-app-forgejo` `lfs-sample` check.
5. **Restore the lifecycle** from `lifecycle-before.json` after the credential is rotated (`rotate-storage-credentials.md`) and the compromise is closed (`credential-compromise.md#object-storage`).


### §Bucket lost (bucket deleted or emptied)
1. Confirm with `admin`: `head-bucket` → 404, or `list-object-versions` shows the history is gone.
2. The live cluster is now the only copy of that data: treat it as a **black-start threat**. Inform the operator team.
3. Recreate the bucket with the storage root module (same name, versioning, lock, lifecycle) and run the capability suite (`./bootstrap preflight`).
4. Restart the stream:
   - `pg-backups`: archiving resumes into the same (now empty) generation path; take an on-demand `Backup` for every cluster at once (`restore-cnpg.md` §PITR step 1 manifest).
   - `longhorn-backups`: the backup target becomes available again; trigger a backup of each important volume.
   - `repo-backups`: run `kubectl -n forgejo create job --from=cronjob/repo-bundle-backup rb-full-$(date +%s)` with the environment override `SC_BUNDLE_FORCE_FULL=1` (plan 44 §9.2.2 starts new chains when no manifest is found).
   - `etcd-snapshots`: `k3s etcd-snapshot save --name pre-bucket-recreate` on a server node.
   - `recovery`: `./bootstrap rotate refresh-bundle` to upload the bundle again.
   - `tofu-state`: follow `recover-opentofu-state.md#rebuild-by-import` (plan 06).
5. Expected: every stream's freshness alert clears within its window; restore tests pass at their next run (force them).

### §Account (provider account suspended, quota exceeded)
1. `admin` gets `AccountProblem`, `QuotaExceeded` or 403 on every bucket. Resolve with the provider (billing, quota). Nothing in the cluster can fix it.
2. Meanwhile follow §Outage steps 1–4 (the effect is the same).

### §Post-outage verification
1. `kubectl cnpg status <cluster> -n <ns>` for all four clusters: `Working WAL archiving: OK`; the `ready` backlog drains to < 10 within 1 hour.
2. If the WAL archiver was detached (§Outage 4b): revert the PR (or resume the layer, which restores Git state), then take an on-demand base backup of that cluster at once and record the PITR gap.
3. Take on-demand base backups of every cluster whose last base backup is older than 24 h (`SCCnpgBaseBackupStale`).
4. Longhorn: `backuptargets.longhorn.io default` `available=true`; trigger backups of volumes whose last backup is older than 24 h.
5. Repository bundles: the next run catches up incrementally; confirm a new manifest for `strange-company` within 45 minutes.
6. Force the restore tests whose `maxAge` lapsed during the outage (`restore-test-failure.md` §Manual run).
7. Disengage the worker emergency stop if it was engaged in §Outage step 2.

## Verification
- `SCObjectStorageUnreachable`, `SCObjectStorageCanaryFailing` and every backup freshness alert resolved.
- The newest `objstore-canary` and `objstore-audit` results are `passed`.
- `sc:backup_last_success_timestamp_seconds` for every stream is within its window (DR dashboard).

## Rollback / abort
- The WAL-archiver detach is reverted by reverting its PR. The PITR gap cannot be undone; it ends at the first base backup after re-attachment.
- Credential rotation keeps the old key until the new one works.

## Evidence to record
- Outage start and end, provider incident reference, maximum WAL backlog and minimum root-disk free per node, any WAL-archiver detach (cluster, start, end, first base backup after), credentials re-minted, drift found and by whom.

## Follow-ups
- If the outage exceeded 24 h, review whether the provider meets the SPEC §29 requirement in practice; consider provider-side replication (plan 14 §24).
- If drift had no operator explanation: complete the compromise procedure.

## Appendix B2 — creating the bucket set in `existing` mode (plan 14 §9.2)
Backblaze B2 buckets are not created by OpenTofu. With the B2 CLI (in the toolbox if plan 02 adds it; otherwise from the B2 web console), for each bucket of plan 14 §9.1 (verify every command and flag against the current B2 CLI):
1. Create the bucket private, with file lock (Object Lock) enabled for B2, B4, B5, B6, B8: `b2 bucket create --file-lock-enabled <prefix>-<purpose> allPrivate`.
2. Set the default retention (`b2 bucket update --default-retention-mode compliance --default-retention-period <days>d …`).
3. Set lifecycle rules equivalent to plan 14 §9.1 with `--lifecycle-rule` (`daysFromHidingToDeleting` for the noncurrent window, `daysFromUploadingToHiding` for current-version expiry, `fileNamePrefix` per rule).
4. Create one application key per in-cluster instance, restricted to its bucket (and name prefix where plan 14 lists one), with capabilities `listBuckets,listFiles,readFiles,writeFiles` plus `deleteFiles` **only** for instances that need deletion (plan 14 §10: `D` column). Keys with `deleteFiles` can delete versions on B2, which is why Object Lock is mandatory there.
5. Put the keys into `bootstrap.env` as `S3_CRED_<INSTANCE>_ACCESS_KEY_ID`/`…_SECRET_ACCESS_KEY` and run `./bootstrap preflight`. Expected: the capability suite reports `protection_level: locked` for B4, B5, B6 and B8.
