# 49 — Destroy

**Status:** Stage-0 plan (draft)
**DAG nodes:** none of its own. Destroy is exercised by the P02/P12 E2E teardown (plan 50 §22) and gated here by acceptance criteria attached to **GATE-P02** (49-AC-1..10) and **GATE-P12** (49-AC-20..22).
**Related plans:** [03](03-provider-interface.md), [06](06-opentofu-state.md), [12](12-cnpg.md), [13](13-longhorn.md), [14](14-object-storage.md), [17](17-github-mirror.md), [35](35-secrets.md), [44](44-backup.md), [45](45-recovery.md), [50](50-bootstrap-orchestrator.md)
**Contracts:** `bootstrap-journal.schema.yaml` (pipeline `destroy`), `provider.schema.yaml` (`$defs/TofuPlanGuard` destroy mode)
**ADRs:** relies on ADR-0100 (separate storage/compute root modules), ADR-0102 (plan guard)
**Procedures:** owns `procedures/safe-destruction.md`

## 1. Purpose

`./bootstrap destroy` removes an installation's **compute** (nodes and everything running on them) while leaving its **durable recovery lifecycle** intact. That lifecycle covers object-storage buckets and their history, the GitHub mirror, the recovery bundle and secrets, remote OpenTofu state history, and DNS/tunnel configuration (SPEC §114). After a destroy, `./bootstrap recover` can rebuild the installation. This plan defines the guards, the dry run, the sequence, idempotency and resume.

## 2. Requirements derived from SPEC.md

1. `./bootstrap destroy` is guarded [SPEC §114, §8 "conservative around destructive operations"].
2. Destroying compute does not automatically destroy the GitHub mirror, external backups, object-storage recovery data, recovery secrets or remote OpenTofu history [SPEC §114].
3. Compute lifecycle and durable recovery lifecycle are distinct [SPEC §114].
4. A safe-destruction procedure exists [SPEC §113].
5. No automated path deletes object-storage recovery data, the mirror or remote state history (master plan §16.6).

Derived requirements:
- **49-R-1** Destroy refuses to run unless the recovery bundle is `confirmed` in the journal **and** its S3 copy exists with a matching digest.
- **49-R-2** Destroy shows the exact compute destroy plan (dry run) and the data-loss window (time since the last successful backup per state owner) before asking for confirmation.
- **49-R-3** Destroy plans and applies only the `compute` root module. The storage root module is never initialized during destroy.
- **49-R-4** Destroy is idempotent and resumable. An already-destroyed installation reports success without side effects.
- **49-R-5** Two typed confirmations are required: the installation name, then the destroy-plan digest. There is no `--force` and no `--yes`. Non-interactive destroy needs both `--confirm-install <cluster>` and `--confirm-plan <D8>`.

## 3. Inputs

- `bootstrap.env` (provider and S3 credentials), the passphrase (bundle unlock), the journal (S3/local), the remote compute state, and `SC-K3S-ADMIN-KUBECONFIG` (optional; used for the final backup when the cluster is reachable).
- Flags: `--dry-run`, `--skip-final-backup` (requires the extra typed acknowledgement `accept-data-loss <cluster>`), `--confirm-install`, `--confirm-plan`, `--accept-unmirrored-loss`.

## 4. Outputs

- Compute resources deleted (paid providers). For `existing`: nodes decommissioned (k3s and Strange Company removed; machines untouched).
- Journal: pipeline `destroy` completed; `installation.state = compute-destroyed`; `compute_destroyed_at` set.
- Destroy report (`runs/<run-id>/destroy-report.txt|json` + S3 `reports/destroy-<run-id>.json`) listing everything **retained**.

## 5. Dependencies

- **Hard:** plan 50 engine (F08), plan 03 provider `DestroyCompute` and guard (F09), plan 06 remote state (P01).
- **Soft:** plan 44 on-demand backup triggers (final backup), plan 17 mirror freshness check, plan 12/13 backup status readers.

## 6. Owned state

None durable beyond the journal's `destroy` pipeline entries (plan 50) and the destroy report.

## 7. External interfaces

- CLI: `./bootstrap destroy [--dry-run] [--skip-final-backup] [--confirm-install C] [--confirm-plan D8] [--accept-unmirrored-loss]` (plan 50 §7.1). Exit codes per plan 50 §7.2.
- Destroy-mode plan guard: `infra/tofu/compute/<module>/guard-destroy.yaml` (`TofuPlanGuard`: only `delete` actions on compute types; no create or update).

## 8. Internal interfaces

- `provider.DestroyCompute(ctx)` → destroy-mode plan (plan 03 §8).
- `backup.TriggerFinal(kubeconfig)` → on-demand CNPG backups (`Backup` CRs for each of the four clusters, plan 12), an etcd snapshot uploaded to S3 (plan 07), and a Longhorn backup of each Longhorn volume labeled critical (plan 13). Waits for completion (≤ 20 min total).
- `backup.LastSuccess(s3)` → per state owner, the timestamp of the latest successful backup **read from object storage**, not from the cluster (barman-cloud `base/` and `wals/` listings, Longhorn backup target, etcd snapshot keys, repo bundles; formats per plans 12/13/44).
- `mirror.Freshness()` → compares the Forgejo `main` SHA (if reachable) with GitHub `main` (plan 17).
- `existing.Decommission(nodes)` → Ansible `infra/ansible/decommission.yml` (plan 07: k3s uninstall; plan 08: revert firewall to an SSH-only baseline).

## 9. Configuration

| Knob | Default | Source |
|---|---|---|
| Final backup timeout | 20 m | `infra/bootstrap/settings.yaml` |
| Mirror freshness tolerance | 0 commits behind | constant |
| Backup-age warning threshold | > 24 h since last base backup or > 15 min since last WAL for any CNPG cluster | constants aligned with SPEC §108 |
| Destroy phase timeout | 60 m | settings |

## 10. Secrets

Consumes `SC-INFRA-PROVIDER`, `SC-OBJECT-STORAGE` (`tofu-state`, plus read-only listing of backup buckets via `admin`), `SC-TOFU-ENCRYPTION`, `SC-K3S-ADMIN-KUBECONFIG`, `SC-SSH-OPERATOR`, and `SC-GITHUB-MIRROR` (read-only use: GET ref). **Never deletes or rotates any secret.** The bundle, `recovery-identity.txt` and SOPS files are untouched.

## 11. Storage

No new storage. Destroy writes only the journal, run directory and report.

## 12. Resource requirements

Operator machine only. The final backup adds load on the cluster for ≤ 20 min.

## 13. Security boundary

Destroy runs with operator authority (provider deletion rights). Protections:
- the two typed confirmations;
- the destroy-mode guard;
- the structural separation of root modules: the storage module is not even initialized, so its buckets cannot appear in any destroy plan;
- `prevent_destroy` on buckets as a second layer;
- no code path in sc-bootstrap calls S3 `DeleteBucket` or `DeleteObjectVersion` outside the conformance prefix (plan 06 §14.3). A static test asserts this (49-AC-9).

## 14. Happy path

Pipeline `destroy` = `preflight → destroy-guard → final-backup → destroy-compute → destroy-report`.

### 14.1 preflight

Plan 50 preflight subset: PF-01, 02, 04 (the repo may be dirty for destroy; downgraded to warn), 06, 07, 12, 13, 22. The installation must be in a state other than `new`. If it is `new` or `planned` (nothing provisioned), destroy reports `nothing to destroy` and exits 0.

### 14.2 destroy-guard

1. Unlock the bundle (passphrase) and run the **49-R-1** check: `recovery_bundle.status = confirmed`, and `HEAD s3://<prefix>-recovery/<cluster>/bundles/LATEST.json` → the referenced bundle object exists with a sha256 equal to the pointer and the journal. Failure: `DESTROY_REFUSED_BUNDLE_UNCONFIRMED` (exit 8), procedure `safe-destruction.md#bundle`.
2. Run `./bootstrap state verify` (plan 06 §20.1). Failure: `DESTROY_REFUSED_STATE_UNVERIFIED` (exit 1). The installation must remain recoverable after destroy.
3. **Mirror freshness.** If Forgejo is reachable, its `main` SHA must equal GitHub `main`. Otherwise `DESTROY_REFUSED_UNMIRRORED` (exit 1) lists the unmirrored commits, unless `--accept-unmirrored-loss` is given, which needs the typed acknowledgement `accept-unmirrored-loss <cluster>` and is recorded as a confirmation. If Forgejo is unreachable, the check result is `unknown` (warn), and the operator is shown GitHub's `main` SHA and date.
4. **Data-loss window.** Print a table: per CNPG cluster, last base backup and last archived WAL; last Longhorn backup per volume; last etcd snapshot; last repository bundle (from S3 listings). Any value older than the thresholds in §9 is marked `STALE`.
5. **Dry run.** `provider.DestroyCompute` → destroy-mode guard → print the destroy footprint:

```text
DESTROY COMPUTE — cluster prod (ovh / vps, region GRA)
  DELETE  1 × ovh_vps  sc-prod-node-01 (vps-0a1b2c3d.vps.ovh.net)
  Billing: OVH VPS termination follows OVH's termination workflow; the current monthly period is not refunded (plan 04 §14.5).
RETAINED (never touched by destroy):
  buckets: sc-prod-tofu-state, sc-prod-recovery, sc-prod-pg-backups, … (with object counts and sizes)
  GitHub mirror: github.com/example-org/strange-company @ <sha>
  recovery bundle v3 (S3 + /blackstart/prod/), OpenTofu state history (<n> versions), DNS zone records and Cloudflare tunnel
Plan digest: 7c1e09ab…
```

   With `--dry-run`, stop here and exit 0. The journal records the run but no confirmation.
6. **Confirmation 1:** type the installation name exactly (`prod`). **Confirmation 2:** type `destroy prod 7c1e09ab`. Both are recorded (`destroy-install-name`, `destroy-plan`).

### 14.3 final-backup

If the cluster API is reachable through the SSH tunnel within 60 s, call `backup.TriggerFinal`, wait for completion (≤ 20 min), and re-print the data-loss window. On failure or unreachable cluster: `FINAL_BACKUP_INCOMPLETE`. Destroy stops (exit 1) unless the operator passes `--skip-final-backup` and types `accept-data-loss <cluster>`, which is recorded. Done check: `Backup` CRs created by this run id are `completed`, or skip is recorded.

The final backup also stops autonomous work first. It scales the Runner to 0 by applying a Flux suspend on the `strange-company` Kustomization, which is a deliberate out-of-Git action. The cluster is about to cease to exist, and the report notes it. This prevents new commits or spend during teardown.

### 14.4 destroy-compute

- Paid providers: re-plan, recompute the digest, and compare it with confirmation 2 (`PLAN_DIGEST_MISMATCH` → abort, and the next run re-confirms). Then `tofu apply <destroy planfile>`, followed by a post-apply audit: compute state is empty and `OrphanScan` is empty.
- `existing`: `Decommission` through Ansible. Each node is uninstalled; per-node completion is recorded as a step. Nothing is deleted at the provider. **Post-decommission access (BOOT-26):** the `sc-admin` account and its `authorized_keys` are kept, and the `AllowGroups sc-admin` drop-in is removed so the provider's initial user can log in again; the destroy report states both (`ssh sc-admin@<host>` keeps working; plan 08 implements this in `decommission.yml`).

Then delete `/blackstart/<cluster>/kubeconfig` (useless and stale) and keep everything else.

### 14.5 destroy-report

Write the report. Set `installation.state = compute-destroyed`. Print `COMPUTE DESTROYED — DURABLE RECOVERY DATA RETAINED`, followed by the retained list and the command to rebuild: `./bootstrap recover`. The `gitops/clusters/<cluster>/` directory **stays in Git**, because recover needs it.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Bundle unconfirmed / S3 copy missing | destroy-guard 1 | refused | exit 8 | safe-destruction.md#bundle |
| State unverifiable | destroy-guard 2 | refused | exit 1 | recover-opentofu-state.md |
| Unmirrored commits | destroy-guard 3 | refused | exit 1 unless accepted | github-mirror-failure.md (plan 17) |
| Backups stale | destroy-guard 4 | warning shown | operator decides at confirmation | safe-destruction.md#stale-backups |
| Destroy plan contains create/update or non-compute type | destroy-mode guard | refused | exit 4 | safe-destruction.md#guard |
| Final backup fails | final-backup | stopped | exit 1 | safe-destruction.md#final-backup |
| Provider deletion partially fails | apply error | some nodes remain | phase failed; rerun re-plans the remainder (re-confirm if the digest changed) | safe-destruction.md#resume |
| Provider API outage | 5xx | stopped | transient retries, then fail | provider-outage.md |
| Termination needs out-of-band confirmation (OVH termination email/token) | provider response (plan 04) | resource pending termination | phase `blocked`, exit 75 with instructions; rerun completes the audit | safe-destruction.md#ovh-termination |
| Operator interrupts | SIGINT | consistent (per-resource) | exit 130; resume | safe-destruction.md#resume |

## 16. Retry behavior

Read-only guard steps are retried on transient errors. `tofu apply` (destroy) is not retried within a run. A rerun re-plans the remainder.

## 17. Idempotency behavior

- `destroy-guard` is re-run on every invocation. Confirmations are reusable only while the destroy-plan digest is unchanged *and* they belong to the same pipeline generation.
- `final-backup` is done when `Backup` objects tagged with this pipeline generation are completed, or when skip was recorded.
- `destroy-compute` is done when compute state has no resources and `OrphanScan` is empty (`existing`: every node reports k3s absent).
- Running destroy on `compute-destroyed` prints the previous report again and exits 0 without contacting the provider beyond the read-only audit.

## 18. Bootstrap behavior

Destroy is never invoked automatically by bootstrap, including failed installs. A failed install's compute stays until the operator runs destroy, so spend continues and the report says so.

## 19. Upgrade behavior

Destroy uses the provider plugin versions locked in the repository at HEAD. Destroying with a newer checkout than the one that installed is supported: the state format is upgraded by OpenTofu, and the guard uses HEAD's guard files.

## 20. Recovery behavior

After destroy: `./bootstrap recover` (plan 45; plan 50 §14.9). Destroy is reversible except for:
- data written after the last backup (the data-loss window, shown before confirmation);
- the provider's committed spend;
- node identity (IPs change, so DNS updates are handled by plan 09 on recovery).

## 21. Observability

Run log, report and journal. In-cluster alerts become irrelevant once the cluster is gone. The external heartbeat (ADR-0009) **will** fire, and the procedure tells the operator to pause it before destroying.

## 22. Testing strategy

- **Unit:** destroy-mode guard fixtures (stray create, foreign type, storage resource presence); confirmation logic (wrong name, wrong digest, reuse across generations); report renderer.
- **Integration** (`make e2e-bootstrap-fake`): fake provider install → destroy → recover (stubs) → destroy again. Asserts buckets and objects unchanged (`s3-emulator` (SeaweedFS, ADR-0205) object version listing digest before and after), bundle untouched, and create/delete counters.
- **Lab:** the `existing` decommission on QEMU VMs leaves the machine reachable over SSH with k3s absent.
- **Real:** the fresh-VPS E2E always ends with destroy (plan 50 §22), and OVH termination behavior is verified there (plan 04).

## 23. Acceptance criteria

### GATE-P02 (destroy path of provisioned compute)

- **49-AC-1** On a fake install whose `recovery_bundle.status = uploaded` (not confirmed), `./bootstrap destroy --non-interactive --confirm-install x --confirm-plan y` exits 8 with `DESTROY_REFUSED_BUNDLE_UNCONFIRMED`, and the fake delete counter is 0.
- **49-AC-2** After deleting the S3 bundle object referenced by `LATEST.json` in a test, destroy exits 8.
- **49-AC-3** `./bootstrap destroy --dry-run` exits 0, prints `DESTROY COMPUTE` and `RETAINED`, records no confirmation in the journal, and the delete counter is 0.
- **49-AC-4** Wrong install name → exit 5. Right name and wrong digest → exit 5. Both correct → exit 0, delete counter = node count.
- **49-AC-5** A destroy-mode plan fixture containing a `create` exits 4 `GUARD_ACTION_FORBIDDEN`. A fixture containing an `aws_s3_bucket` delete exits 4 `GUARD_UNEXPECTED_RESOURCE`.
- **49-AC-6** Nothing deleted (BOOT-23): for every plan 14 bucket, the set of `(key, versionId)` pairs and delete markers from `aws --profile sc-admin s3api list-object-versions` **before** destroy is a subset of the set **after** destroy (no version or delete marker removed; new versions from the final backup and `state verify` are allowed), and the `list-buckets` count is unchanged.
- **49-AC-7** Crash injection at every destroy step followed by a rerun ends in `compute-destroyed`, with delete counter = node count (no double delete) and no error.
- **49-AC-8** Destroy on a `compute-destroyed` install exits 0 and prints the previous report, and the delete counter is unchanged.
- **49-AC-9** Static check (`make lint`): no Go code outside `src/internal/tofustate/conformance` references `DeleteBucket` or `DeleteObjectVersion`. The check fails on an injected violation.
- **49-AC-10** Unmirrored fixture (Forgejo `main` ahead of GitHub) → exit 1 `DESTROY_REFUSED_UNMIRRORED`. With `--accept-unmirrored-loss` and the typed acknowledgement it proceeds, and the journal shows `accept-unmirrored-loss`.

### GATE-P12 (fresh-VPS E2E teardown)

- **49-AC-20** The fresh-VPS E2E ends with `./bootstrap destroy` (non-interactive with both confirmations). Exit 0, the provider shows no `sc-<cluster>-node-*` service (`OrphanScan` empty), and `report.json` lists every plan 14 bucket as retained.
- **49-AC-21** Immediately after, `./bootstrap state verify` exits 0 (state history intact and decryptable), and GitHub `main` equals the SHA recorded at destroy.
- **49-AC-22** The destroy report contains the line `COMPUTE DESTROYED — DURABLE RECOVERY DATA RETAINED` and the `./bootstrap recover` hint.

## 24. Explicit non-goals

- Deleting buckets, backups, the GitHub repository, DNS zones, Cloudflare tunnels, recovery bundles or state history. These are manual, documented and irreversible steps in `safe-destruction.md` §"Full teardown (manual)", performed by the operator with their own tools.
- Partial destroy (single node). That is `remove-node.md` (plan 47).
- Refund or billing management.

## 25. Open questions

| # | Question | Blocking | Proposed resolution | Owner |
|---|---|---|---|---|
| OQ-49-1 | Does OVH VPS termination through the OpenTofu provider complete synchronously, or does it require an emailed confirmation token? | no | Plan 04 spike S-3 verifies it. If a token is required, destroy-compute goes `blocked` (exit 75) with instructions, and the rerun audit completes once OVH terminates. | 04 |
| OQ-49-2 | Should destroy also delete the Cloudflare tunnel and DNS records to avoid dangling hostnames? | no | No (conservative). They are needed for a quick `recover` and cost nothing. The report lists them, and manual removal is in the procedure. | 09 |
