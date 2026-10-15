# Procedure: Safe destruction

**Owning plan:** plans/49-destroy.md (with 03, 04, 05, 06, 50)
**SPEC references:** §8 ("conservative around destructive operations"), §113 ("safe destruction"), §114
**Severity / urgency:** routine (planned teardown) — irreversible for compute and for data written after the last backup
**Expected duration:** 30–60 min (including a final backup ≤ 20 min); OVH termination may complete later
**Requires break-glass:** no

## When to use
- Tearing down an installation's compute on purpose: end of a test installation, the fresh-VPS E2E teardown, moving to new hardware via `recover`, or abandoning a failed install.
- Not for: removing one node (`remove-node.md`, plan 47); deleting backups, buckets, the GitHub mirror, DNS or secrets — those are the manual steps in [full teardown](#full-teardown) and are never automated (SPEC §114).

## Preconditions and required material
- Repository clone, `bootstrap.env`, recovery passphrase.
- The recovery bundle confirmed (`./bootstrap status` → `recovery_bundle: confirmed`) and its S3 copy present.
- If the cluster is reachable: SSH access (final backup runs through the API tunnel).
- Your external heartbeat provider account (to pause the dead-man's switch).

## Safety checks before acting
1. You are destroying the **right installation**: `CLUSTER_NAME` in `bootstrap.env` and the `cluster` shown by `./bootstrap status` match what you intend.
2. Nobody depends on the installation: no unmerged work in Forgejo that is not on GitHub (the destroy guard checks `main` only — other branches and PRs, issues and review history live in the Forgejo database backup, not on GitHub).
3. The installation will be recoverable afterwards: `./bootstrap state verify` passes (destroy runs it too) and recent backups exist (destroy prints the data-loss window).
4. Pause the external heartbeat check now, or it will page you when Alertmanager disappears (ADR-0009).
5. An OVH VPS month already started is **not refunded** (plan 04 §14.5).

## Steps

1. **Dry run.**
   ```sh
   ./bootstrap destroy --dry-run
   ```
   Expected: exit 0 and a `DESTROY COMPUTE` block listing exactly your nodes (`sc-<cluster>-node-NN`), the billing note, the **RETAINED** list (buckets with object counts, GitHub mirror SHA, bundle, state history, DNS/tunnel), the data-loss window table, and a plan digest. Nothing is changed and no confirmation is recorded.
   If it exits 8 → [bundle](#bundle); exit 1 with `DESTROY_REFUSED_STATE_UNVERIFIED` → `recover-opentofu-state.md`; `DESTROY_REFUSED_UNMIRRORED` → step 2; exit 4 → [guard](#guard).

2. **Mirror freshness.** If the dry run reported unmirrored commits, fix the mirror first (`github-mirror-failure.md`, plan 17) and repeat step 1. Only if Forgejo's extra commits are knowingly disposable, add `--accept-unmirrored-loss` in step 4 and type `accept-unmirrored-loss <cluster>` when asked.

3. <a id="stale-backups"></a>**Review the data-loss window.** Rows marked `STALE` (base backup > 24 h, WAL > 15 min, Longhorn backup, etcd snapshot or repository bundle older than expected) mean recent data would be lost. If the cluster is healthy, the final backup in step 4 refreshes them. If the cluster is unhealthy and backups are stale, stop and consider fixing backups first (`restore-test-failure.md` / component procedures) or accept the loss explicitly in step 4 (`--skip-final-backup`).

4. **Destroy.**
   ```sh
   ./bootstrap destroy
   ```
   - Type the installation name (`<cluster>`) when asked, then `destroy <cluster> <D8>` with the digest shown. Expected: confirmations recorded.
   - <a id="final-backup"></a>**Final backup:** the tool suspends the `strange-company` Kustomization (stopping autonomous work), triggers on-demand CNPG backups for all four clusters, an etcd snapshot to S3 and Longhorn backups of critical volumes, and waits ≤ 20 min. Expected: `final backup complete` and a refreshed data-loss table with no `STALE` row. On `FINAL_BACKUP_INCOMPLETE` (exit 1): investigate the failing backup (CNPG `kubectl -n <ns> get backups`, Longhorn UI via port-forward) and rerun step 4; or, only when the cluster is broken and you accept the loss, rerun with `--skip-final-backup` and type `accept-data-loss <cluster>`.
   - **Compute destruction:** paid providers re-plan, re-check the digest and apply the guarded destroy plan; `existing` nodes are decommissioned with Ansible (k3s removed, firewall back to SSH-only, machines kept). Afterwards log in as `sc-admin` (kept, with your key) or as the provider's initial user again (the `AllowGroups sc-admin` restriction is removed) (plan 49 §14.4).
   Expected: `COMPUTE DESTROYED — DURABLE RECOVERY DATA RETAINED`, the retained list, and the hint `./bootstrap recover`. Exit 0.

5. <a id="ovh-termination"></a>**OVH termination confirmation (only if the run exits 75 `blocked`).** OVH may require confirming termination with a token sent by e-mail (plan 04 spike S-3). Follow the printed instructions: confirm in the OVH console (or with the e-mailed link) for each listed service, then rerun `./bootstrap destroy`. Expected: the rerun's audit shows compute state empty and an empty orphan scan; exit 0. Billing stops according to OVH's termination terms.

6. **Clean up local material.** `~/.strange-company/<cluster>/kubeconfig` is deleted by destroy. Keep `bootstrap.env`, the passphrase, the bundle copies and the recovery identity — they are needed for `recover`.

<a id="bundle"></a>
### Destroy refused: bundle not confirmed or missing (exit 8, `DESTROY_REFUSED_BUNDLE_UNCONFIRMED`)
1. `./bootstrap status` shows `recovery_bundle.status`. If `uploaded`, re-run the confirmation by running `./bootstrap` once (it asks for the passphrase and the S3 round trip) — on a transferred install this is part of preflight + verify.
2. If the S3 copy is missing or its digest differs: re-upload by re-sealing, `./bootstrap rotate recovery-bundle` (plan 35 `rotate-secrets.md#recovery-bundle`). Expected: V-32 passes on `./bootstrap verify --only V-32`.
3. Repeat step 1 of the main steps.

<a id="guard"></a>
### Destroy guard refused (exit 4)
`GUARD_ACTION_FORBIDDEN` (the destroy plan contains a create/update) or `GUARD_UNEXPECTED_RESOURCE` (a non-compute type) means the checkout or state is inconsistent. Do not edit `guard-destroy.yaml`. Check `git status` (clean release checkout), run `./bootstrap state verify`, and report a bug with `plan-summary.json`.

<a id="resume"></a>
### Interrupted or partially failed destroy
Rerun `./bootstrap destroy`. Expected: completed steps are skipped, the remaining resources are re-planned; if the digest changed you confirm the new (smaller) plan; no resource is deleted twice. If the provider API is down, wait (`provider-outage.md`). A destroy on an already `compute-destroyed` installation prints the previous report and exits 0.

<a id="drill"></a>
### Drill installation teardown (plan 45 D3)
A scratch black-start drill (`./bootstrap recover --drill --drill-source <cluster>`) is a separate installation with its own `CLUSTER_NAME`, state and buckets prefix. Tear it down with `./bootstrap destroy` run against the **drill's** `bootstrap.env` (never the source's): the safety checks above apply unchanged; the mirror-freshness check is skipped for drills (they have no own GitHub mirror). Then delete the drill's buckets and records per [full teardown](#full-teardown) steps 3 and 7, because a drill keeps no recovery value. Never run full teardown with the source installation's prefix.

<a id="full-teardown"></a>
### Full teardown (manual, irreversible, never automated)
Only when the installation and **all** of its recovery data must cease to exist (e.g. a disposable test install). After this, `recover` is impossible.
1. Complete the destroy above first.
2. Delete the GitHub mirror repository (GitHub UI) — or keep it as an archive.
3. Delete the buckets `<prefix>-*` with your provider's tools, including every object **version** (versioned buckets cannot be deleted while versions remain), e.g. `./bootstrap exec -- aws --profile sc-admin s3 rb s3://<bucket> --force` does **not** remove noncurrent versions; use the provider console's "empty bucket" or a versions-aware deletion, then `rb`.
4. Delete the Cloudflare tunnel and DNS records for `BASE_DOMAIN` hostnames (`auth`, `git`, `ci`, `grafana`, `hermes`) in the Cloudflare dashboard.
5. Revoke credentials: OVH consumer key, S3 keys, Cloudflare token, GitHub token, model API keys.
6. Destroy the bundle copies, recovery identity, `bootstrap.env` and passphrase records.
7. Remove `gitops/clusters/<cluster>/` and `secrets/offcluster/<cluster>/` from the repository by PR (and the `.sops.yaml` rules for the cluster).

## Verification
- `./bootstrap status --json | ./bootstrap exec -- jq -r .installation.state` prints `compute-destroyed`.
- Provider: no `sc-<cluster>-node-*` service/instance remains (`./bootstrap plan` orphan scan empty; OVH console), or for `existing`, `ssh <node> 'command -v k3s'` prints nothing.
- `./bootstrap state verify` exits 0 (history intact) and `./bootstrap exec -- aws --profile sc-admin s3api list-buckets` still lists every plan 14 bucket.
- GitHub `main` SHA equals the SHA printed in the destroy report.

## Rollback / abort
Before typing the second confirmation: abort freely (nothing changed). After the final backup started: Ctrl-C leaves the cluster with the `strange-company` Kustomization suspended — resume it with `./bootstrap exec -- flux resume kustomization strange-company -n flux-system` if you abandon the destroy. After compute deletion: the only way back is `./bootstrap recover` (plan 45), restoring from the retained data.

## Evidence to record
The destroy report (`.bootstrap/runs/<run-id>/destroy-report.json`, S3 `reports/destroy-<run-id>.json`), the journal confirmations (`destroy-install-name`, `destroy-plan`, and any `accept-*`), OVH termination confirmation, and for full teardown the list of deleted buckets and revoked credentials.

## Follow-ups
- Resume or remove the external heartbeat check.
- If the installation will be rebuilt: `total-cluster-loss.md` / `./bootstrap recover` (plan 45).
- Note the provider's final invoice against the reported committed spend.
