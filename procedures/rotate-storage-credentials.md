# Procedure: Rotate object-storage credentials

**Owning plan:** plans/35-secrets.md (credential split and bucket policies: plan 14)
**SPEC references:** §15, §26, §29, §99, §111, §113 (rotate storage credentials), §114
**Severity / urgency:** routine (every ≤ 180 days) · urgent on compromise (start at `credential-compromise.md#object-storage`)
**Expected duration:** 15 min per in-cluster instance; 45–90 min for all instances
**Requires break-glass:** `SC-K3S-ADMIN-KUBECONFIG` for verification (`./bootstrap shell`); `SC-BREAK-GLASS-INFRA` only if `admin` itself is lost

## When to use

- `SCSecretRotationDue`/`SCSecretRotationOverdue` for `SC-OBJECT-STORAGE/<instance>`.
- Provider-forced key expiry, operator change, recovery fencing (ADR-0554), or compromise.

Instances (register `SC-OBJECT-STORAGE`): `admin` (alias `bootstrap-admin`), `tofu-state`, `tofu-state-ro`, `recovery`, `recovery-reader`, `seed-write`, `seed-read`, `dr-tests-results`, `objstore-audit`, `telemetry-debug`, `drill-reader`, `pg-backups-{forgejo-pg,authentik-pg,woodpecker-pg,sc-pg}`, `longhorn-backups`, `etcd-snapshots`, `forgejo-objects`, `repo-backups`, `evidence`, `telemetry`, `dr-tests-reader`.

## Preconditions and required material

- Toolbox; recovery passphrase; `bootstrap.env` with the current `admin` key (or the bundle copy).
- The provider supports two concurrent keys per scoped identity (most S3-compatible providers do; if not, see Step 2 note).
- Current inventory: `./bootstrap exec -- sc-bootstrap secrets status --class SC-OBJECT-STORAGE`.

## Safety checks before acting

1. No backup is mid-flight that must not fail: check `SCCnpgWalArchiveFailing`, `SCLonghornBackupStale` are not firing; for `pg-backups-*`, rotate one cluster at a time.
2. Bucket versioning is `Enabled` on every bucket (`./bootstrap exec -- sc-bootstrap buckets inventory`); never rotate while a bucket is unversioned.
3. **Never** use `admin` to delete objects, versions or buckets during this procedure. The tool refuses delete operations (SPEC §114).
4. Rotate `admin` last, and only after every other instance verifies.

## Steps

1. **Pick the instance.** `./bootstrap rotate secret SC-OBJECT-STORAGE/<instance>`. The tool opens the bundle (passphrase) and stages a candidate.
   *Expected:* prompt "creating new key for <instance>" or, for providers without an API, instructions to create it in the console and paste it.
2. **Create the new key with the same policy** (tool: provider IAM API with `admin`; console fallback). *Expected:* the tool tests the new key: `PutObject`/`GetObject` of `rotation-probe/<ts>` on the instance's own bucket/prefix (read-only instances: `ListObjectsV2` and `HeadObject` only), then checks that the key **cannot** access another bucket (`AccessDenied` expected on `<prefix>-recovery` for non-recovery instances). *If the scope test fails:* abort (`--abort`); fix the policy per plan 14; do not continue with an over-privileged key.
   *Provider without two keys:* the tool creates a second scoped identity `<instance>-g<N+1>` instead and deletes the old identity in step 6.
3. **Deliver the new key** by kind of storage:
   - in-cluster instances (`pg-backups-*`, `longhorn-backups`, `forgejo-objects`, `repo-backups`, `evidence`, `telemetry`, `dr-tests-reader`, `tofu-state-ro`, `recovery-reader`): the tool writes the SOPS Secret and opens a PR; merge it; Flux applies. Consumers: CNPG barman plugin reloads the Secret (verify per plan 12) — the tool bumps the `ObjectStore` annotation to force reload; Longhorn reads the backup-target Secret on the next backup; Forgejo, Runner and CronJobs restart through the bumped `strange.company/secret-generations` annotation.
   - `etcd-snapshots`: node file; the tool re-runs the Ansible `k3s` role (`--tags k3s-config`) and restarts servers one at a time (plan 07).
   - toolbox-only instances (`tofu-state`, `recovery`, `seed-write`, `seed-read`): `secrets/offcluster/<cluster>/s3-<instance>.sops.yaml` (PR) and the bundle.
   - `admin`: update `bootstrap.env`, then the bundle.
4. **Verify the consumer uses the new key** (the tool waits up to 30 min and prints each result):
   - `pg-backups-<cluster>`: `kubectl cnpg backup <cluster> -n <ns>` completes and `kubectl -n <ns> get cluster <cluster> -o jsonpath='{.status.conditions[?(@.type=="ContinuousArchiving")].status}'` = `True`.
   - `longhorn-backups`: a manual backup of a test volume completes (plan 13).
   - `etcd-snapshots`: `k3s etcd-snapshot save --name rot-<ts>` uploads (plan 07).
   - `forgejo-objects`: `GET /api/v1/repos/<o>/<r>/releases` with an attachment download returns 200; a test package push/pull succeeds.
   - `repo-backups`: `kubectl -n forgejo create job --from=cronjob/repo-backup rb-rot-<ts>` completes.
   - `evidence`: the Runner's `sc_runner_log_archive_failures_total` does not increase during a fake execution.
   - `tofu-state`: `./bootstrap state verify` exits 0.
   - `recovery`: `./bootstrap exec -- sc-bootstrap recovery-bundle status` reads the pointer.
   - `recovery-reader`, `tofu-state-ro`, `dr-tests-reader`: trigger the dependent check CronJobs (`kubectl -n dr-tests create job --from=cronjob/<name> …`) and see exit 0.
5. **Seal the bundle** (bundled instances: `admin`, `tofu-state`, `recovery`, `seed-*`, `etcd-snapshots`): automatic; `recovery-bundle status` shows `currency: ok`.
6. **Revoke the old key** (tool, after step 4 passed). *Expected:* a request with the old key returns `InvalidAccessKeyId`/`AccessDenied`.
7. Ledger entry written; the tool prints the final JSON.

Repeat per instance. Recommended order: in-cluster instances → `etcd-snapshots` → `tofu-state`, `recovery`, `seed-*` → `admin`.

## Verification

- For every rotated instance: step 4 check passed and old key rejected.
- `SCCnpgWalArchiveFailing`, `SCLonghornBackupStale`, `SCEtcdSnapshotUploadFailing`, `SCRepoBundleBackupStale`, `SCRecoveryBundleOutdated` not firing 2 h later.
- `./bootstrap exec -- sc-bootstrap secrets status --class SC-OBJECT-STORAGE` shows today's `rotated_at` for each rotated instance.

## Rollback / abort

- Before step 6, `./bootstrap rotate secret SC-OBJECT-STORAGE/<instance> --abort` reverts the PR (if merged, opens a revert PR), restores node files, and deletes the new key. The old key was never revoked, so service continues.
- After step 6 there is no rollback; if a consumer still fails, fix forward by re-delivering the new key (re-run the command; it resumes at step 3).

## Evidence to record

Per instance: old and new key ids (never the secrets), scope-test output, consumer verification output, revocation timestamp, PR URLs, bundle version. Attach to the rotation issue.

## Follow-ups

- If a scope test revealed an over-broad key, raise a plan 14 issue.
- If the rotation was part of recovery fencing (ADR-0554), record it in the recovery evidence.
