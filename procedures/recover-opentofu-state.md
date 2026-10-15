# Procedure: OpenTofu state recovery

**Owning plan:** plans/06-opentofu-state.md (with 03, 50; buckets: 14; secrets: 35)
**SPEC references:** §15, §108 (RPO latest successful mutation, RTO ≤ 2 h), §109, §113 ("OpenTofu state recovery"), §114
**Severity / urgency:** degraded (provisioning, expand and destroy are blocked; the running cluster is unaffected) — disaster only when combined with total cluster loss
**Expected duration:** 15 min (stale lock, version restore) to 2 h (rebuild by import)
**Requires break-glass:** `SC-BREAK-GLASS-INFRA` only if provider or S3 console access is needed (lost credentials)

## When to use
- Alerts: `SCTofuStateLockStale`, `SCTofuStateRestoreTestFailed` (failed or > 35 days old), `SCObjectStorageConfigDrift` for `<prefix>-tofu-state` versioning (plan 06 §21; catalogue names, CD-01).
- sc-bootstrap errors: `TOFU_STATE_LOCKED`, `TOFU_STATE_INVALID`, `TOFU_STATE_MISSING`, decrypt failure (exit 8), `NoSuchBucket` on `<prefix>-tofu-state`.
- `./bootstrap state verify` exits 1.
- A plan unexpectedly shows changes on an unchanged installation (drift).
- Black start (`total-cluster-loss.md`, plan 45) reaches `state-recover`.

## Preconditions and required material
- Repository clone at the installation's commit (or newer), `bootstrap.env` (S3 and provider credentials), the recovery passphrase and the bundle (local or `s3://<prefix>-recovery/<cluster>/bundles/`).
- For lost-passphrase recovery: the offline recovery age identity (decrypts `secrets/offcluster/<cluster>/*.sops.yaml`).
- No cluster access is needed (SPEC §15).

## Safety checks before acting
1. Stop every other sc-bootstrap run: `./bootstrap status` shows no live run lock holder (`bootstrap-resume.md#lock`).
2. **Never** delete object versions in `<prefix>-tofu-state`, disable versioning, or upload a hand-edited state. History is the recovery path; every restore creates a new version on top.
3. Never run `tofu apply` by hand inside a work dir; use the `./bootstrap state …` subcommands, which apply the guard and typed confirmations.
4. If the current state may be correct but the S3 provider is down, this is `object-storage-outage.md` (plan 14), not a state recovery.

## Steps

1. **Assess.**
   ```sh
   ./bootstrap state status
   ```
   Expected: per module (`storage`, `compute`, `edge` if present) the object, current version id, last-modified, serial, lineage and lock holder. Then run the full test:
   ```sh
   ./bootstrap state verify
   ```
   Expected: exit 0 and a result object `restore-tests/<ts>.json` with `outcome: passed`. Only the modules that exist are tested: an installation with `PROVIDER=existing` and bucket mode `existing` has none and passes with `modules: []`. If it passes and you came here because of an overdue alert, you are done (see [restore test](#restore-test)). Otherwise choose the section matching the error.

<a id="stale-lock"></a>
### A. Stale lock (`TOFU_STATE_LOCKED`, `SCTofuStateLockStale`)
1. Confirm no sc-bootstrap run is active (safety check 1) and that the lock's `Who`/`Created` (shown by `state status`) belongs to a dead run.
2. ```sh
   ./bootstrap state unlock --module compute     # or storage / edge
   ```
   Type `unlock <cluster> <module>`. Expected: `lock released`; `state status` shows no lock.
3. Re-run the interrupted command (`bootstrap-resume.md`). If the lock reappears without any run, the S3 provider may not honor deletes promptly; re-run conformance with `./bootstrap state verify` and check plan 06 §14.3 results.

<a id="restore-version"></a>
### B. Corrupt, wrong-lineage or deleted current state (`TOFU_STATE_INVALID`, `TOFU_STATE_MISSING`)
1. List versions with validation:
   ```sh
   ./bootstrap state versions --module compute --limit 20
   ```
   Expected: newest first, each with time, size, serial, lineage, resource count and `decrypt: ok|fail`. Pick the **newest** version with `decrypt: ok`, the journal's lineage, and a resource count matching the provider (the node count).
2. ```sh
   ./bootstrap state restore --module compute --version-id <V>
   ```
   Type `restore <cluster> compute <V[0:8]>`. Expected: a new current version (history grows by one), `state status` serial equals the restored serial.
3. Reconcile with reality:
   ```sh
   ./bootstrap plan
   ```
   Expected: 0 changes. If it shows changes, go to [drift](#drift).
4. Run `./bootstrap state verify` → exit 0.

<a id="drift"></a>
### C. Drift between state and the provider
1. `./bootstrap plan` shows changes on an unchanged installation. Read `plan-summary.json`: which resources and attributes differ.
2. If the provider changed **display or metadata attributes** only (`update` actions the guard allows), the drift is harmless: the next guarded mutating run (`expand`, `recover`) converges it. To inspect it read-only now:
   ```sh
   ./bootstrap shell --unlock
   # inside the shell:
   eval "$(sc-env tofu compute)"        # prepares .bootstrap/work/compute and TF_ENCRYPTION (plan 02 §14.6)
   tofu -chdir=.bootstrap/work/compute plan -refresh-only -lock-timeout=60s
   ```
   Expected: only the attributes listed in `plan-summary.json` differ. Do not apply by hand. Record the drift in the evidence.
3. If the plan shows **replace** or **delete** of a node (`GUARD_REPLACE_FORBIDDEN`), a machine was changed or removed outside Strange Company (console resize, reinstall, termination). Do not apply. If the node is gone, follow `lost-node.md` (plan 47); if it was resized/reinstalled, follow `provider-outage.md#unusable-node`.
4. If resources exist at the provider that state does not know (`ORPHAN_PAID_RESOURCE`), import them: [rebuild by import](#rebuild-by-import) step 2 for the affected module.

<a id="lost-passphrase"></a>
### D. Lost passphrase (bundle cannot be opened; state cannot be decrypted)
1. Try again carefully (exit 8 after three attempts; there is no lockout). Check keyboard layout.
2. If the passphrase is truly lost but the **offline recovery identity** exists: the generated secrets can be read from `secrets/offcluster/<cluster>/*.sops.yaml`:
   ```sh
   ./bootstrap shell
   # inside:
   SOPS_AGE_KEY_FILE=/blackstart/<cluster>/recovery-identity.txt sops -d secrets/offcluster/<cluster>/tofu-encryption.sops.yaml
   ```
   (copy the identity into `~/.strange-company/<cluster>/` first; remove it afterwards). Expected: the OpenTofu passphrase. Then re-seal a new bundle with a new recovery passphrase: `./bootstrap rotate recovery-bundle` (plan 35 procedure `rotate-secrets.md#recovery-bundle`; it accepts the recovery identity in place of the old passphrase). Continue with step 1 of this procedure.
3. If neither passphrase nor recovery identity exists, the state cannot be decrypted. Go to [rebuild by import](#rebuild-by-import). All other bundle-only secrets (break-glass, k3s token) must be rotated per plan 35; record a SEV against black-start readiness.

<a id="rebuild-by-import"></a>
### E. Rebuild by import (state unrecoverable or bucket deleted)
1. If the bucket itself is gone (`NoSuchBucket`), recreate it with versioning **before** anything else: in `S3_BUCKET_MODE=create`, run `./bootstrap state import --module storage` (imports surviving buckets and creates missing ones through the guarded storage module); in `existing` mode, recreate it in the S3 console with versioning `Enabled`, then `./bootstrap preflight` (PF-12, PF-14 must pass).
2. Import compute:
   ```sh
   ./bootstrap state import --module compute
   ```
   Expected: sc-bootstrap runs the provider orphan scan, lists every `sc-<cluster>-node-*` resource, writes `import` blocks, generates a **new** state passphrase, and shows a plan containing only imports and no-ops. Type the confirmation. Expected afterwards: `./bootstrap plan` shows 0 changes.
3. Re-seal the bundle with the new `SC-TOFU-ENCRYPTION`: `./bootstrap rotate refresh-bundle`. Expected: bundle generation +1, `V-32` passes on the next verify.
4. `./bootstrap state verify` → exit 0. Timing target: ≤ 2 h total (SPEC §108).

<a id="versioning"></a>
### F. Versioning disabled (`SCObjectStorageConfigDrift` on `<prefix>-tofu-state`)
1. Someone (or the provider) suspended versioning on `<prefix>-tofu-state`. Check:
   ```sh
   ./bootstrap exec -- aws --profile sc-admin s3api get-bucket-versioning --bucket <prefix>-tofu-state
   ```
2. Re-enable it immediately:
   ```sh
   ./bootstrap exec -- aws --profile sc-admin s3api put-bucket-versioning --bucket <prefix>-tofu-state --versioning-configuration Status=Enabled
   ```
   Expected: `get-bucket-versioning` prints `"Status": "Enabled"`; the alert resolves after the next `objstore-audit` run (plan 14; trigger it now with `./bootstrap exec -- kubectl -n dr-tests create job --from=cronjob/objstore-audit objstore-audit-manual`).
3. Investigate who changed it (S3 provider audit log). Only `SC-OBJECT-STORAGE/admin` can; if it was not you, treat it as a credential compromise (`credential-compromise.md`, plan 35) and rotate it (`rotate-storage-credentials.md`).
4. Run `./bootstrap state verify`.

<a id="restore-test"></a>
### G. Restore test failed or overdue (`SCTofuStateRestoreTestFailed`)
1. Run `./bootstrap state verify`. Expected: exit 0; the alert resolves at the next daily check, or immediately with `./bootstrap exec -- kubectl -n dr-tests create job --from=cronjob/restore-test-tofu-state rt-tofu-state-manual`.
2. If it fails, the output names the failing step (download, decrypt, lineage, serial monotonicity, address coverage, scratch plan). Decrypt failures → D; lineage/serial anomalies → B; address coverage (a node missing from state) → C.

## Verification
- `./bootstrap state verify` exits 0 and the newest `s3://<prefix>-tofu-state/<cluster>/restore-tests/<ts>.json` has `outcome: passed`.
- `./bootstrap plan` reports 0 compute changes and an empty orphan scan.
- `./bootstrap state status` shows no lock and versioning enabled; the alerts above are resolved.

## Rollback / abort
Every restore and import creates a new object version; to undo a restore, restore the previous version id the same way (step B.2). Imports do not change provider resources. Aborting mid-command (Ctrl-C) leaves the current version untouched.

## Evidence to record
The version ids before and after, the restore-test result object key, the time from detection to `state verify` pass (RTO evidence), and for passphrase loss or versioning changes the cause.

## Follow-ups
- Passphrase or identity lost: schedule the plan 35 rotations and a black-start drill (plan 45).
- Versioning or deletion incidents: review S3 credential scopes (plan 14) — the `tofu-state` credential must not be able to delete versions (06-AC-10).
