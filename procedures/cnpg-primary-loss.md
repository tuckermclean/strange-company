# Procedure: CNPG primary loss

**Owning plan:** plans/12-cnpg.md
**SPEC references:** §25, §26, §96, §108, §113 ("CNPG primary loss")
**Severity / urgency:** outage (the application using the cluster cannot write)
**Expected duration:** 5–15 min (instances = 3, automatic failover); 30–90 min (instances = 1, restore from object storage)
**Requires break-glass:** yes, cluster administration (`SC-BREAK-GLASS-CLUSTER` kubeconfig) if the normal operator kubeconfig is unavailable; no for the common case

## When to use
- `SCCnpgClusterDown` (no ready instance) for `forgejo-pg`, `authentik-pg`, `woodpecker-pg` or `sc-pg`.
- `SCCnpgClusterDegraded` where the missing instance is the **primary** and no promotion has happened after 5 minutes (small-ha).
- Application symptoms: `SCForgejoDown` with database errors, Authentik 500s, Runner or Gateway failing health on the database.
- CNPG operator or Barman Cloud plugin down (`SCCnpgOperatorDown`/`SCCnpgPluginDown`, proposed): see §Operator.

## Preconditions and required material
- The toolbox: run every command below inside `./bootstrap shell` (it provides `kubectl`, `kubectl cnpg`, `jq`, `aws`).
- A working kubeconfig (normal operator path, or break-glass per `break-glass-access.md`).
- For the instances = 1 data-loss branch: object storage reachable (`SCObjectStorageUnreachable` not firing) and procedure `restore-cnpg.md` at hand.

## Safety checks before acting
1. **Do not delete PVCs, Pods or the `Cluster` of a database on reflex.** At instances = 1 the PVC holds the only live copy of data written since the last archived WAL (≤ 10 minutes).
2. Confirm which cluster and namespace: `kubectl get clusters.postgresql.cnpg.io -A`.
3. Check whether object storage is healthy before any restore decision: `kubectl -n dr-tests get cronjob objstore-canary -o jsonpath='{.status.lastSuccessfulTime}'` (expected: < 30 min ago).
4. Never run two primaries: do not promote a replica manually while the old primary may still be running on a reachable node (§Branch A step 4).

Blast radius: one application (one cluster per state owner, ADR-0006).

## Steps

### Triage (all profiles)
1. `kubectl cnpg status <cluster> -n <ns>`
   Expected: the output shows `Current Primary`, the instance list with status, and `Working WAL archiving`.
   If the command fails with "operator not reachable" or shows no status: go to §Operator.
2. `kubectl -n <ns> get pods -l cnpg.io/cluster=<cluster> -o wide` and `kubectl -n <ns> get pvc -l cnpg.io/cluster=<cluster>`
   Record pod phase, node and PVC status.
3. `kubectl get nodes` — is the primary's node `Ready`?
4. Choose the branch: `spec.instances` = 3 → Branch A; = 1 → Branch B.

### Branch A — instances = 3 (failover)
1. Wait up to 2 minutes, then re-run `kubectl cnpg status <cluster> -n <ns>`.
   Expected: `Current Primary` names a different instance and the status says the cluster is healthy or "Failing over" finished. Writes through `<cluster>-rw` work again.
2. Verify writes: `kubectl -n <ns> exec <new-primary-pod> -c postgres -- psql -U postgres -tAc 'select pg_is_in_recovery()'` prints `f`.
   (Local socket inside the pod; superuser access over TCP is disabled.)
3. If the old primary's node is back and Ready, CNPG rejoins the old instance as a replica (with `pg_rewind`). Expected within 10 min: `kubectl cnpg status` lists 1 primary + 2 replicas streaming.
   If the old node is permanently lost, the old instance stays `Pending` on its node-bound PVC: continue with `cnpg-replica-loss.md` §Destroy and re-clone.
4. **If no failover happened after 5 minutes**:
   a. Check that the old primary pod is really not running: `kubectl -n <ns> get pod <old-primary> -o wide`. If its node is NotReady and the machine is confirmed powered off (provider console), taint it: `kubectl taint node <node> node.kubernetes.io/out-of-service=nodeshutdown:NoExecute`.
   b. Find the most advanced replica: `kubectl cnpg status <cluster> -n <ns>` (column `Received LSN`/`Replay LSN`).
   c. `kubectl cnpg promote <cluster> <instance> -n <ns>` for that replica.
   Expected: promotion within 60 s. If promotion fails, capture `kubectl -n cnpg-system logs deploy/cnpg-cloudnative-pg --since=30m` and escalate (Rollback / abort).
5. Remove the out-of-service taint after the node is repaired or removed (`kubectl taint node <node> node.kubernetes.io/out-of-service-`).

### Branch B — instances = 1
1. Pod exists but crash-loops (node Ready, PVC `Bound`):
   - `kubectl -n <ns> logs <pod> -c postgres --previous | tail -50`.
   - OOM (`kubectl -n <ns> get pod <pod> -o jsonpath='{.status.containerStatuses[0].lastState.terminated.reason}'` = `OOMKilled`): raise `resources.limits.memory` by PR in `gitops/platform/<component>/…/cluster.yaml` (plan 51 budget), or as an emergency `kubectl -n <ns> patch cluster <cluster> --type merge -p '{"spec":{"resources":{"limits":{"memory":"<new>"}}}}'` followed by the same change in Git within 24 h (Flux would revert it at the next reconcile otherwise; suspend the layer with `flux suspend kustomization <layer>` until the PR merges).
   - Disk full on the node (`No space left on device` in logs): follow `disk-pressure.md` (plan 08) first; do not delete WAL files by hand.
   - Corrupted data directory (`invalid page`, `could not read block`): treat as data loss, continue with step 3.
   Expected: pod Ready after the cause is fixed; `kubectl cnpg status` healthy.
2. Pod Pending because its node is gone or not Ready:
   - If the node will come back (reboot, provider maintenance): wait for it. Expected: the pod starts on the same node (local-path PVC is node-bound).
   - If the node or its disk is permanently lost: continue with step 3.
3. **Data loss at instances = 1**: run `restore-cnpg.md` §Replace (recovery to "latest", generation bump). Expected RPO ≤ 15 min, RTO ≤ 4 h [SPEC §108].
   For `sc-pg`, `woodpecker-pg`, `forgejo-pg` and `authentik-pg` the merge gate depends on the lost service, so use the direct-apply variant (`restore-cnpg.md` step 7b, safety check 5).

### Operator
1. `kubectl -n cnpg-system get deploy,pods`
   Expected: the CNPG operator and the `barman-cloud` plugin Deployments available.
2. If not available: `kubectl -n cnpg-system describe pod <pod>` and `kubectl -n cnpg-system logs <pod> --previous`. Common causes: image pull failure (internal registry or upstream), cert-manager certificate for the plugin not Ready (`kubectl -n cnpg-system get certificate`), webhook TLS errors.
3. Running databases are not affected by an operator outage; WAL archiving **is** affected if the plugin is down (`SCCnpgWalArchiveFailing` will follow). Fix the operator before touching any `Cluster`.
4. Flux reconciles the operator HelmRelease: `flux reconcile helmrelease cnpg -n cnpg-system --with-source`. Expected: Ready within 10 min.

## Verification
- `kubectl cnpg status <cluster> -n <ns>` shows a primary, `Working WAL archiving: OK`, and (instances = 3) two streaming replicas.
- `kubectl -n <ns> get cluster <cluster> -o jsonpath='{.status.phase}'` = `Cluster in healthy state`.
- The alert that triggered the procedure has resolved in Alertmanager.
- The application health check is green (Forgejo `/api/healthz`, Authentik `/-/health/ready/`, Runner `/readyz`).
- Within 24 h, run the restore test for the cluster once by hand: `kubectl -n dr-tests create job --from=cronjob/restore-test-cnpg rt-manual-$(date +%s)` and expect `outcome: passed` for that cluster (plan 44).

## Rollback / abort
- A manual promotion cannot be undone; if the wrong replica was promoted, the cluster continues on it and the other instances re-sync from it. Data written only to the old primary after its last streamed LSN is lost (bounded by synchronous `ANY 1` at small-ha).
- Abort Branch B before deleting anything if object storage is unhealthy: keep the broken instance and escalate. A restore without WAL access would lose up to 24 h.

## Evidence to record
- Timeline (alert time, decision, promotion or restore time) in the incident issue on Forgejo (or locally if Forgejo is down).
- Output of `kubectl cnpg status` before and after.
- For Branch B step 3: the `restore-cnpg.md` evidence.

## Follow-ups
- Root-cause the node or pod failure (plan 08, plan 47 `lost-node.md`).
- If the failover took more than 60 s at instances = 3, record the measured time for the H04 drill review.
- Check that the next scheduled base backup completes (`SCCnpgBaseBackupStale` stays quiet).
