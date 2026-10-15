# Procedure: Restore-test or backup-check failure

**Owning plan:** plans/44-backup.md
**SPEC references:** §26, §31, §96, §97 ("failed backup"), §108, §109
**Severity / urgency:** degraded (a backup stream is unproven or stale); becomes a black-start threat if not resolved within the stream's window
**Expected duration:** 15–90 min
**Requires break-glass:** no

## When to use
Any of these alerts (plan 42 catalogue, ADR-0553 check CronJobs):
- `SCRestoreTestCnpgFailed`, `SCRestoreTestReposFailed`, `SCRestoreTestLonghornFailed`, `SCRestoreTestEtcdFailed`, `SCTofuStateRestoreTestFailed`, `SCTofuStateLockStale`, `SCGitHubBlackStartTestFailed`, `SCRestoreTestAppsFailed`, `SCDrCanaryWriterFailed`;
- `SCRepoBundleBackupStale` (§Bundle backup), `SCLonghornBackupStale` (§Longhorn backup), `SCCnpgBaseBackupStale`/`SCCnpgBackupFailed` (§CNPG backup);
- `SCStorageAuditFailed`, `SCRetentionAuditFailed` (diagnose with §Read the result, then follow the failing check's owning plan).

## Preconditions and required material
- `./bootstrap shell` (kubectl, aws, jq).
- Read access to `s3://<prefix>-evidence/restore-tests/` (the `admin` profile, or the `dr-tests-reader` key from `dr-tests/s3-dr-tests-reader`).

## Safety checks before acting
1. A restore test only ever touches scratch objects named `rt-*` and DR marker rows. **Never "fix" a failed test by changing production data** or by deleting backups.
2. Distinguish `failed` (the backup is bad: urgent) from `errored` (the test could not run: important, not urgent) and stale (nothing ran).
3. Do not suspend or delete a check CronJob to silence an alert. Use an Alertmanager silence with an expiry and a reason if needed.

## Steps

### §Read the result
1. Newest Job and its log:
   ```bash
   CJ=<cronjob>; NS=<namespace>   # e.g. restore-test-cnpg dr-tests
   J=$(kubectl -n $NS get jobs -l strange.company/check=$CJ --sort-by=.metadata.creationTimestamp -o name | tail -1)
   kubectl -n $NS logs $J --all-containers | tail -100
   ```
2. Newest result documents:
   ```bash
   aws --endpoint-url "$S3_ENDPOINT" s3 ls "s3://${S3_BUCKET_PREFIX}-evidence/restore-tests/<test_id>/" --recursive | sort | tail -3
   aws --endpoint-url "$S3_ENDPOINT" s3 cp "s3://…/<key>.json" - | jq '{outcome, failure, checks: [.checks[] | select(.outcome != "passed")], measurements}'
   ```
   Expected: `outcome` and `failure.code` identify the branch below.
3. No Job ran in the expected window (stale): go to §Stale.

### §Manual run (used after every fix)
`kubectl -n <ns> create job --from=cronjob/<cronjob> manual-$(date -u +%Y%m%d%H%M)`
Expected: the Job completes, exits 0, and writes `passed` documents; the alert resolves within its evaluation interval. (Kubernetes records a manually created Job's success in the CronJob's `lastSuccessfulTime` only for Jobs it created itself; the alert therefore resolves at the next scheduled success if the manual run is not counted (verify). The result document is the evidence meanwhile.)

### §Errored (`outcome: errored`)
| Code | Meaning | Action |
|---|---|---|
| `RT_INSUFFICIENT_CAPACITY` | node disk or memory below the gate (plan 44 §9.3) | free disk (`disk-pressure.md`), check that nothing else heavy runs (CI, workers); rerun. If recurring on a `constrained` node, raise it in the plan 51 capacity review |
| `RT_LOCK_HELD` | another heavy test held `dr-tests/restore-slot` for > 30 min | `kubectl -n dr-tests get lease restore-slot -o yaml`; if the holder Job no longer exists, delete the Lease; rerun |
| `RT_TIMEOUT`, `RT_API_ERROR` | API or operator unavailable | check CNPG operator (`cnpg-primary-loss.md` §Operator), Longhorn manager, object storage; rerun |

### §CNPG (`rt-cnpg-pitr-<cluster>` failed)
| Code / check | Likely cause | Action |
|---|---|---|
| `RT_BACKUP_NOT_FOUND` | no base backup in the current generation | `kubectl -n <ns> get backups.postgresql.cnpg.io -l cnpg.io/cluster=<cluster>`; take an on-demand backup (`restore-cnpg.md` §PITR step 1 manifest); rerun |
| `RT_RESTORE_FAILED` | WAL missing or unreadable, plugin or credential problem | `kubectl -n dr-tests describe cluster rt-<cluster>` and the recovery pod logs; check `SCCnpgWalArchiveFailing`; check `object-storage-outage.md` §Credential for `dr-tests-reader`. **If WAL objects are missing from the bucket, PITR is broken for that window: take a base backup immediately and escalate (possible deletion: check bucket audit logs)** |
| `marker-b-absent` failed (`RT_MARKER_UNEXPECTED`) | recovery overshot the target time | record; compare `recovery_target_time` with the server log in the result; escalate to plan 12 (PITR precision) |
| `marker-a-present` failed (`RT_MARKER_MISSING`) | WAL containing marker A not archived or not replayed | check `measurements.rpo_observed_seconds`; check the archiver; rerun once; if it repeats, RPO is not met: escalate |
| `app-db-present` failed (`RT_SCHEMA_MISSING`) | backup of the wrong cluster or a broken base backup | verify `source.server_name` matches the production `serverName`; take a fresh base backup; rerun |
| `rto-budget` failed (`RT_RTO_BUDGET_EXCEEDED`) | restore slower than 3600 s | §RTO |

### §Repos (`rt-repo-bundle` failed)
| Code | Action |
|---|---|
| `RT_CHECKSUM_MISMATCH` | a bundle's ciphertext differs from its manifest: possible corruption or tampering. Check bucket versions of that key (`aws s3api list-object-versions --prefix <key>`); escalate as a possible compromise; force a full chain (`SC_BUNDLE_FORCE_FULL=1`, plan 44 §9.2) |
| `RT_DECRYPT_FAILED` | `SC-AGE-BACKUP` private key in `dr-tests/backup-age` does not match the recipient: compare `age-keygen -y` of the identity with ConfigMap `forgejo/backup-age-recipients`. Restore the identity from Git/bundle (plan 35); if lost, rotate per plan 44 §10 and force full chains |
| `RT_BUNDLE_INVALID`, `RT_FSCK_FAILED` | corrupt chain: force a new full chain for that repository; investigate the Forgejo repository itself (`forgejo doctor check --all`, verify) |
| `RT_REF_MISMATCH` | the restored refs differ from the manifest: bug or tampering. Keep the evidence, force a full chain, escalate to plan 44 owners |
| `RT_DIVERGED` (`strange-company` only) | restored `main` is not an ancestor of live `main`: a force-push on `main` or tampering. Check Forgejo's push audit; this is a security incident unless explained |

### §Longhorn (`rt-longhorn-canary` / `rt-longhorn-volume` failed)
| Code | Action |
|---|---|
| `RT_BACKUP_NOT_FOUND` / `RT_BACKUP_STALE` | §Longhorn backup |
| `RT_RESTORE_FAILED` | `kubectl -n longhorn-system describe volumes.longhorn.io rt-…` and `backupvolumes.longhorn.io <volume>`; check the backup target; rerun with the previous backup (`--backup <name>` flag of `sc-restore-test longhorn`) to find the last good one |
| `RT_CHECKSUM_MISMATCH` (canary) | backup data corrupt: run `snapshotDataIntegrity` check now (Longhorn UI or setting), trigger a new backup of `dr-canary` after the next `dr-canary-writer` run, rerun. Two consecutive failures: escalate (backup store integrity) |
| `RT_FSCK_FAILED` (forgejo-data) | the restored Forgejo repository is inconsistent: compare with bundles (they are independent); take an on-demand Longhorn backup; if production shows the same corruption, follow `restore-forgejo.md` |
| `rto-budget` | §RTO |

### §etcd (`rt-etcd-snapshot` failed)
| Code | Action |
|---|---|
| `RT_BACKUP_STALE` | newest S3 snapshot older than 13 h: see `SCEtcdSnapshotUploadFailing` and `restore-etcd.md` §E (plan 07) |
| `RT_ETCD_INTEGRITY` | `etcdutl snapshot restore` hash check failed: take a fresh snapshot on a server node (`k3s etcd-snapshot save --name pre-integrity-check`), rerun; repeated failures point to disk or upload corruption: escalate to plan 07 |

### §Tofu (`rt-tofu-state` failed)
| Code | Action |
|---|---|
| `RT_RESULT_STALE` | the monthly off-cluster test is overdue: run `./bootstrap state verify` from the operator machine (needs the recovery bundle passphrase). Expected: a new `passed` result in `tofu-state/<cluster>/restore-tests/`; then §Manual run |
| other | follow `recover-opentofu-state.md#restore-test` (plan 06) |
| `RT_TOFU_LOCK_STALE` (`check-tofu-lock-age`, `SCTofuStateLockStale`) | a `.tflock` object is older than 6 h: a crashed `tofu` run left its lock. Follow `recover-opentofu-state.md#stale-lock` (`./bootstrap state unlock --module <m>` after confirming no run is active) |

### §GitHub black start (`rt-github-mirror` failed)
| Check | Action |
|---|---|
| `clone` | GitHub unreachable or `SC-GITHUB-MIRROR-READ` invalid: check plan 17 probe, rotate the read credential (plan 17) |
| `refs-in-sync` | mirror lagging or failing: `github-mirror-failure.md` (plan 17) |
| `black-start-files`, `render` | `main` cannot be rendered from GitHub: a black start would fail. Fix by PR immediately (render failures also fail F11 CI, so check why CI allowed it) |
| `sops-recipients`, `sops-reachable` | a SOPS file lacks a recipient, or a tier-1 SOPS file is not reachable from the cluster root (plan 35 lint S-13): `sops-decryption-failure.md` (plan 35); re-encrypt with both recipients or fix the kustomization by PR. Decryptability itself is attested quarterly by `sc-bootstrap recovery-bundle verify --full` (plan 35), not by this test (CD-10) |
| `no-secret-no-lfs` | a GitHub token pattern (`RT_SECRET_PATTERN`) is in a mirrored ref: treat as a credential compromise (`credential-compromise.md`, plan 35), revoke the token, then rewrite history per plan 17; an LFS filter or pointer (`RT_LFS_POINTER`) breaks black start from GitHub: remove LFS use by PR (17-R-5) |

### §Apps (`rt-app-forgejo` / `rt-app-authentik` failed)
`RT_APP_UNHEALTHY`: the scratch application did not become healthy on the restored database. Read the scratch pod log (included in the Job log). Common causes: image digest no longer pullable (`SCOciReferencedImageMissing`, plan 43), schema migration attempted on startup (the scratch app must start the same version as production), missing marker user (plan 15 blueprint). A real incompatibility means production restore would fail: escalate to plans 15/16.

### §Bundle backup (`SCRepoBundleBackupStale`)
1. §Read the result for `forgejo/repo-bundle-backup`.
2. Causes: Forgejo down (`SCForgejoDown`), `sc-backup` token invalid (`SCForgeBotTokenInvalid`, `rotate-forge-credentials.md` §B), object storage (`object-storage-outage.md`), cache PVC full (`kubectl -n forgejo exec … -- df -h /cache`; the cache is reconstructible: delete the PVC content of removed repositories, or recreate the PVC through Flux).
3. §Manual run in namespace `forgejo`. Expected: a new manifest for `strange-company` within 45 minutes.

### §Longhorn backup (`SCLonghornBackupStale`)
1. `kubectl -n longhorn-system get backuptargets.longhorn.io default -o jsonpath='{.status.available}'`; if false → `object-storage-outage.md`.
2. `kubectl -n longhorn-system get recurringjobs.longhorn.io backup-12h -o yaml` and the volume's labels (`recurring-job-group.longhorn.io/critical: enabled`).
3. Trigger a backup now (plan 44 §9.7 `backup now --component longhorn`). Expected: `Completed` within 30 min.

### §CNPG backup (`SCCnpgBaseBackupStale`, `SCCnpgBackupFailed`)
1. `kubectl -n <ns> get backups.postgresql.cnpg.io -l cnpg.io/cluster=<cluster> --sort-by=.metadata.creationTimestamp | tail -3` and `kubectl -n <ns> describe backup <failed>`.
2. Object storage or credential → `object-storage-outage.md`; plugin down → `cnpg-primary-loss.md` §Operator.
3. On-demand backup (`restore-cnpg.md` §PITR step 1 manifest). Expected: `completed`.

### §RTO (`rto-budget` failed)
1. Compare `measurements.restore_duration_seconds` with the last 4 results (trend) and with `restored_bytes`.
2. Causes: database or volume grew (expected; update the budget by PR to `infra/dr/catalogue.yaml` only if the end-to-end RTO in plan 44 §9.6 still holds), slow object storage (provider), node contention (test ran concurrently with heavy work: check the `strange.company/heavy-slot` requests of the test pods and the `dr-tests/restore-slot` Lease).
3. If the end-to-end RTO target (SPEC §108) is at risk, record it as a SPEC-relevant finding (plan 44 owners) rather than raising budgets silently.

### §Stale (no run in the expected window)
1. `kubectl -n <ns> get cronjob <cronjob> -o jsonpath='{.spec.suspend} {.status.lastScheduleTime} {.status.lastSuccessfulTime}'`.
2. Suspended: find out why (recovery mode suspends restore tests until the finalize PR, ADR-0554); resume through Git, never by hand while recovery is active.
3. Missing CronJob: check Flux layer `backups` (`flux get kustomization backups`).
4. Jobs not starting: `kubectl -n <ns> get events --sort-by=.lastTimestamp | tail -20` (quota, PSA, VAP denial, image pull).

## Verification
- A §Manual run of the failed check exits 0 and writes `passed` results for every subject.
- The triggering alert has resolved (or will at the next scheduled run, see §Manual run note).
- `kubectl get clusters.postgresql.cnpg.io,pvc,pod -n dr-tests -o name | grep -c '/rt-'` = `0` after the run (no leftovers).

## Rollback / abort
Nothing in this procedure changes production except on-demand backups and forced full bundle chains, which are additive.

## Evidence to record
- Failing `result_id`, failure code, root cause, fix, and the `result_id` of the passing rerun, in the incident issue.

## Follow-ups
- Two consecutive `failed` results for the same stream: open a plan-level issue (the backup mechanism needs a change).
- Update thresholds in `infra/dr/catalogue.yaml` only by PR with a stated reason.
