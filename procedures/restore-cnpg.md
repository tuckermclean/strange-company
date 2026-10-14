# Procedure: CNPG point-in-time recovery (PITR) and replacement

**Owning plan:** plans/12-cnpg.md (backup machinery and restore tests: plan 44)
**SPEC references:** §26, §108, §109, §113 ("CNPG PITR")
**Severity / urgency:** outage for the affected application during steps 5–9; otherwise routine
**Expected duration:** 45–120 min (PITR with side-by-side verification); 30–60 min (replace with "latest")
**Requires break-glass:** cluster administration if the normal operator kubeconfig is unavailable; Forgejo admin (`SC-BREAK-GLASS-FORGEJO`) only for the `forgejo-pg` variant if no human Forgejo admin can merge after recovery

## When to use
- **PITR:** logical damage (bad migration, accidental deletion, corrupted application data) in `forgejo-pg`, `authentik-pg`, `woodpecker-pg` or `sc-pg`. Called from `restore-forgejo.md`, `restore-authentik.md` and plan 48 upgrade rollbacks.
- **Replace ("latest"):** the only instance's data is lost (instances = 1 node or disk loss, unrecoverable corruption). Called from `cnpg-primary-loss.md` Branch B.
- Not for black start: `./bootstrap recover` (plan 45) applies the same components through the recovery commit.

## Preconditions and required material
- `./bootstrap shell` in a checkout of `strange-company` at `main` (kubectl, `kubectl cnpg`, `flux`, `aws`, `sc-gitops`, `git`, `yq`).
- Object storage healthy (`SCObjectStorageUnreachable` not firing).
- The current generation `N` of the cluster: `kubectl -n <ns> get cluster <cluster> -o jsonpath='{.spec.plugins[0].parameters.serverName}'` → `<cluster>-g<N>`.
- Free node disk ≥ 2 × the database size + 5 GiB for the side-by-side verification cluster (`df -h /` on the node, or `SCNodeRootDiskLow` not firing).
- The target time `T` in UTC, RFC 3339 (`2026-10-13T14:05:00Z`), agreed with the application owner. It must lie inside the recovery window: after the oldest base backup (`kubectl -n <ns> get backups.postgresql.cnpg.io -l cnpg.io/cluster=<cluster>`) and before the last archived WAL (`kubectl cnpg status` → `Last Archived WAL`).

Names used below: `<ns>`/`<cluster>`/layers per cluster:

| Cluster | Namespace | DB layer | App layer | App writers to stop |
|---|---|---|---|---|
| `forgejo-pg` | forgejo | `forgejo-db` | `forgejo` | `deploy/forgejo` |
| `authentik-pg` | authentik | `authentik-db` | `authentik` | Authentik server and worker Deployments |
| `woodpecker-pg` | woodpecker | `woodpecker-db` | `woodpecker` | Woodpecker server |
| `sc-pg` | strange-company | `sc-db` | `strange-company` | `deploy/agent-runner`, `deploy/model-gateway` (stopping the Gateway stops all model spending, which is fail-safe) |

`<ID>` is `FORGEJO`, `AUTHENTIK`, `WOODPECKER` or `SC`.

## Safety checks before acting
1. **Never delete the production `Cluster` before a verification cluster (PITR) or the object-store contents (replace) have been checked.** Steps 2–3 exist for this.
2. Confirm the generation path `N+1` is empty: `aws --endpoint-url "$S3_ENDPOINT" s3 ls "s3://${S3_BUCKET_PREFIX}-pg-backups/<cluster>/<cluster>-g<N+1>/"` prints nothing. If it is not empty (an earlier failed attempt), use the next empty generation and record it.
3. Do not edit `bootstrap:` on the running cluster expecting a restore: CNPG ignores `bootstrap` after initialization.
4. Restoring `sc-pg` rolls back task state and the ledger to `T`. Tasks running after `T` must be treated as lost (plan 26 marks them infrastructure failures on Runner restart). Engage the emergency stop first (COST-14) through the **Kubernetes source** (`kubectl -n strange-company create configmap sc-emergency-stop --from-literal=level=all --from-literal=reason="restore <cluster>"`, plan 33 §9; or the Git source `policy/budgets.yaml` `spec.emergencyStop.level`) as described in `budget-emergency-stop.md`, never the database source, because a DB-held stop is wiped by the restore (DR-31c). After the restore, reconcile the ledger: spend between `T` and now is no longer in `ledger.cost_records`, so compare provider usage for that window (plan 32 reconciliation) and record it before releasing the stop.
5. **Merge path (DR-3).** Restoring `sc-pg`, `woodpecker-pg`, `forgejo-pg` or `authentik-pg` disables part of the merge gate (Runner statuses, CI, Forgejo, SSO approvers). For these four, step 7 always uses **7b (direct apply)** and the PR merges afterwards (step 10). For PITR while the service still runs, wait until every required status on the PR is `success` (and an approval exists; for `authentik-pg` a Forgejo local account or `SC-BREAK-GLASS-FORGEJO` approves) **before** step 5 stops writers.
6. **Stale recovery hazard (DR-7):** if a production `Cluster` was deleted by accident, Flux must not re-create it from an old recovery stanza; the creation guard (plan 12 §9.5) blocks that. This procedure opens the guard for exactly one creation (step 7).

## Steps

### §PITR

1. **Back up the current (bad) state** so it remains recoverable:
   ```bash
   cat <<EOF | kubectl apply -f -
   apiVersion: postgresql.cnpg.io/v1
   kind: Backup
   metadata: {name: <cluster>-pre-pitr-$(date -u +%Y%m%d%H%M), namespace: <ns>}
   spec:
     cluster: {name: <cluster>}
     method: plugin
     pluginConfiguration: {name: barman-cloud.cloudnative-pg.io}
   EOF
   ```
   Expected: `kubectl -n <ns> get backup <name> -o jsonpath='{.status.phase}'` = `completed` within 30 min. If it fails, continue only if the application owner accepts losing the bad state.
2. **Create the side-by-side verification cluster** (no WAL archiver, so it cannot write to the bucket):
   ```bash
   IMAGE=$(kubectl -n <ns> get cluster <cluster> -o jsonpath='{.status.image}')
   SIZE=$(kubectl -n <ns> get cluster <cluster> -o jsonpath='{.spec.storage.size}')
   cat <<EOF | kubectl apply -f -
   apiVersion: postgresql.cnpg.io/v1
   kind: Cluster
   metadata: {name: <cluster>-pitr, namespace: <ns>}
   spec:
     instances: 1
     imageName: ${IMAGE}
     storage: {storageClass: local-path, size: ${SIZE}}
     enableSuperuserAccess: true
     bootstrap:
       recovery:
         source: origin
         recoveryTarget: {targetTime: "<T>"}
     externalClusters:
       - name: origin
         plugin:
           name: barman-cloud.cloudnative-pg.io
           parameters: {barmanObjectName: <cluster>-store, serverName: <cluster>-g<N>}
   EOF
   ```
   Choose `<N>` (DR-6b): the generation whose recovery window contains `T`, which is not always the newest. List generations with `aws --endpoint-url "$S3_ENDPOINT" s3 ls "s3://${S3_BUCKET_PREFIX}-pg-backups/<cluster>/"` and, for each, the oldest `base/*/backup.info` `begin_time`; use the highest `N` whose oldest base backup began before `T`.
   Expected: `kubectl -n <ns> get cluster <cluster>-pitr -o jsonpath='{.status.phase}'` = `Cluster in healthy state` within 45 min.
   If recovery fails with "no target backup found" or "recovery target not reached": `T` is outside the window. Choose another `T` and recreate (delete `<cluster>-pitr` and its PVC first).
3. **Verify the data** with the application owner:
   `kubectl -n <ns> exec -it <cluster>-pitr-1 -c postgres -- psql -U postgres -d <db>` and run the agreed checks (for example, the deleted rows exist; the bad migration's schema version is absent).
   Expected: the owner confirms. Otherwise go back to step 2 with a different `T`. **Record the checks as SQL statements with expected results**; step 7 runs them again on production before any writer restarts.
4. **Prepare the Git change** on a branch `restore/<cluster>-<YYYYMMDD>`:
   - `gitops/clusters/<cluster-name>/vars/cluster-vars.yaml`: set `CNPG_<ID>_SOURCE_GEN: "<N>"`, `CNPG_<ID>_GEN: "<N+1>"`, `CNPG_<ID>_TARGET_TIME: "<T>"` and `SC_RECOVERY_RUN_ID: "restore-<cluster>-<UTC timestamp>"`.
   - The cluster's `kustomization.yaml` under `gitops/platform/<component>/`: replace the component `cnpg-bootstrap-initdb` (or the previous recovery component) with `cnpg-bootstrap-recovery`.
   - If `gitops/clusters/<cluster-name>/kustomization.yaml` and `seed/kustomization.yaml` list this cluster's per-cluster component `cnpg-recovery-latest-<id>` or `cnpg-recovery-target-<id>` (left by an earlier black start, plan 45), do **not** swap the platform component: replace that entry with `cnpg-recovery-target-<id>` (PITR) or `cnpg-recovery-latest-<id>` (§Replace) in both files instead. A `-latest-` entry left in place would re-create production at "latest" and re-apply the damage (DR-6a). Leave the other clusters' components untouched.
   - `make render-check` passes locally, and the rendered `Cluster` for this cluster contains `recoveryTarget.targetTime: <T>` (`yq 'select(.kind=="Cluster" and .metadata.name=="<cluster>") | .spec.bootstrap.recovery.recoveryTarget' out/<cluster-name>/<db-layer>.yaml`). Push the branch and open a PR. Do **not** merge yet.
5. **Stop writers and Flux for the affected layers:**
   ```bash
   flux suspend kustomization <db-layer> <app-layer> -n flux-system
   kubectl -n <ns> scale <app writers> --replicas=0
   ```
   Expected: `kubectl -n <ns> get pods` shows no application pod Running.
6. **Delete the production cluster:**
   ```bash
   kubectl -n <ns> delete cluster <cluster> --wait=true
   kubectl -n <ns> get pvc -l cnpg.io/cluster=<cluster>      # expected: none; delete leftovers
   ```
   The `prune: disabled` annotation only stops Flux; a human delete works.
7. **Re-create it from the backup at `T`.** First open the creation window (plan 12 §9.5, plan 37 ADM-63) with the run id that step 4 wrote into `cluster-vars` as `SC_RECOVERY_RUN_ID` (`restore-<cluster>-<UTC timestamp>`; it is rendered onto the `Cluster` as annotation `strange.company/recovery-run`): `kubectl -n flux-system create configmap sc-cnpg-create-window --from-literal=mode=recovery --from-literal=runId=<run-id>`.
   - **7a (`authentik-pg` with an SSO-independent approver available, or any other cluster not listed in safety check 5):** merge the PR in Forgejo (all required statuses already `success`), then `flux reconcile source git flux-system && flux resume kustomization <db-layer>`. Expected: Flux applies the `Cluster` with `bootstrap.recovery` and archiving into `g<N+1>`.
   - **7b (`sc-pg`, `woodpecker-pg`, `forgejo-pg`, and `authentik-pg` without such an approver):** apply the rendered layer from the PR branch directly with Flux's field manager, without Secrets (they already exist in the cluster):
     ```bash
     git checkout restore/<cluster>-<YYYYMMDD>
     sc-gitops render --cluster <cluster-name> --out out/
     yq 'select(.kind != "Secret")' out/<cluster-name>/<db-layer>.yaml \
       | kubectl apply --server-side --field-manager=kustomize-controller -f -
     ```
     Keep `<db-layer>` suspended until step 10.
   Expected (both): `kubectl -n <ns> get cluster <cluster> -o jsonpath='{.status.phase}'` = `Cluster in healthy state` within 45 min, and `kubectl -n <ns> get cluster <cluster> -o jsonpath='{.spec.plugins[0].parameters.serverName}'` = `<cluster>-g<N+1>`.
   Then re-run the SQL checks recorded in step 3 against the restored production cluster (`kubectl -n <ns> exec <cluster>-1 -c postgres -- psql -U postgres -d <db> -c '<check>'`). Expected: every result equals step 3's. If not (for example the cluster was restored to "latest"), stop: keep writers stopped and repeat from step 4. Afterwards close the window: `kubectl -n flux-system delete configmap sc-cnpg-create-window`.
8. **Check archiving into the new generation:** within 15 min, `aws --endpoint-url "$S3_ENDPOINT" s3 ls "s3://${S3_BUCKET_PREFIX}-pg-backups/<cluster>/<cluster>-g<N+1>/wals/" --recursive | head` lists objects, and `kubectl cnpg status <cluster> -n <ns>` shows `Working WAL archiving: OK`. The immediate base backup of the `ScheduledBackup` completes within 30 min.
   If archiving fails with "WAL archive not empty": the generation was not bumped; stop and redo steps 4–7 with the next generation.
9. **Start the application:** `flux resume kustomization <app-layer> -n flux-system` (Git restores the replica count). Expected: the application's health check passes.
10. **Direct-apply variant (7b) only:** once Forgejo, Woodpecker and the Runner are back, merge the PR. If a required status cannot be produced for it because a merge-gate component is still broken, use `procedures/emergency-merge.md` (plan 55, break-glass) and its ratification follow-up (`merge-without-provenance.md`). Then `flux resume kustomization <db-layer>`. Expected: `flux get kustomization <db-layer>` Ready at the merge commit, with no change to the `Cluster` (`kubectl -n <ns> get cluster <cluster> -o jsonpath='{.metadata.generation}'` unchanged after reconcile, or changed only in metadata).
11. **Clean up:** `kubectl -n <ns> delete cluster <cluster>-pitr` and its PVCs.

### §Replace (data loss, recovery to "latest")
Same as §PITR with these differences:
- Skip steps 1–3 (there is nothing to back up, and nothing to compare against). Instead, confirm the newest WAL in `…/<cluster>-g<N>/wals/` is recent: `aws s3 ls … --recursive | sort | tail -1` shows a timestamp within the last 15 minutes of the failure.
- In step 4 use the component `cnpg-bootstrap-recovery-latest` and do not set `CNPG_<ID>_TARGET_TIME`.
- In Replace mode the service is already down, so for the four clusters of safety check 5 step 7 is always 7b; the "statuses before stopping writers" rule does not apply.
- Step 7's data check is: the newest `heartbeat` row in `sc_dr.markers` (plan 44 RT-0) is within 15 minutes of the failure time (the observed RPO); record it.
- Step 6: if the old `Cluster` object still exists (node gone, PVC lost), delete it and its PVCs.

## Verification
- `kubectl cnpg status <cluster> -n <ns>`: healthy, archiving OK, `serverName` `<cluster>-g<N+1>`.
- Application checks agreed in step 3 pass on the production cluster.
- Run the restore test once: `kubectl -n dr-tests create job --from=cronjob/restore-test-cnpg rt-manual-$(date +%s)`. Expected: a `passed` result for `rt-cnpg-pitr-<cluster>` that names `server_name: <cluster>-g<N+1>`.
- `flux get kustomizations` shows the DB and app layers Ready and not suspended.

## Rollback / abort
- Before step 6 nothing in production has changed: delete `<cluster>-pitr`, resume the layers, close the PR.
- After step 6 the old cluster is gone, but its history remains under `g<N>` (and the pre-PITR backup from step 1). To return to the pre-PITR state, repeat §Replace with source generation `N`, target generation `N+2`, and component `cnpg-bootstrap-recovery-latest`; this restores the state of step 1.

## Evidence to record
- `T`, `N`, `N+1`, PR number, the step-1 backup name, timestamps of steps 5 and 9 (application downtime), and the restore-test result id.
- Attach the evidence to the incident issue in Forgejo, and for upgrades to the plan 48 evidence record.

## Follow-ups
- Generations `g<N>` and older stay until their lifecycle expires (plan 43 R-14). Do not delete them by hand.
- If the cause was an application upgrade, revert its version PR (plans 16, 15, 20, 26).
- Review whether the restore duration stays within the RTO budget (≤ 4 h end to end, plan 44 §9.6).
