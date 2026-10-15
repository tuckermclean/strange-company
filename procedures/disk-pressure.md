# Procedure: Disk and memory pressure

**Owning plan:** plans/08-node-security.md (thresholds and host side), plans/51-capacity-and-scheduling.md (eviction ranking, budgets)
**SPEC references:** §17 ("disk-pressure behavior"), §89, §96 ("disk pressure"), §107
**Severity / urgency:** degraded (warning) / outage risk (critical)
**Expected duration:** 15–60 min
**Requires break-glass:** no

## When to use

| Alert | Section |
|---|---|
| `SCNodeRootDiskLow` (< 25 % free) | §Disk, steps 1–4 |
| `SCNodeRootDiskCritical` (< 15 % free), `SCNodeDiskPressure` (kubelet DiskPressure) | §Disk, all steps, urgently |
| `SCEtcdSlowDisk` | §Slow disk |
| `SCEtcdSnapshotDirLarge` | §Etcd snapshots |
| `SCNodeMemoryPressure`, `SCPlatformPodEvicted`, repeated `SCPlatformPodOOMKilled` | §Memory |
| `SCCIWorkspaceFilesystemHigh` (plan 20) | §CI workspace |

## Preconditions and required material
`./bootstrap shell`; SSH to the node.

## Safety checks before acting
1. Never delete files under `/var/lib/rancher/k3s/server/db/etcd`, `/var/lib/longhorn`, or `/var/lib/strange-company/local-path` by hand: that is live etcd, Longhorn replica and PostgreSQL data.
2. Never delete WAL files from a PGDATA directory to free space; a growing `pg_wal` means archiving is failing (`SCCnpgWalArchiveFailing`) — fix archiving instead (plan 12).
3. Prefer stopping new load first: pause autonomous dispatch (`./bootstrap exec -- sc-bootstrap runner pause`, plan 26 admin API) while you work if the node is critical.

## Steps

### §Disk
1. **Measure:** `./bootstrap exec -- ssh <node> 'df -h / /var/lib/sc/ci-workspace; sudo du -xh --max-depth=2 /var/lib 2>/dev/null | sort -h | tail -20; sudo journalctl --disk-usage'`.
2. **Find the consumer** against the plan 11 §12 budget lines:
   - `/var/lib/rancher/k3s/agent/containerd` (images) large → `sudo k3s crictl rmi --prune` (removes unused images; safe). Expected: kubelet image GC would do this at 80 %; doing it now frees space immediately.
   - `/var/log/pods` or `/var/log` large → find the noisy container (`sudo du -sh /var/log/pods/* | sort -h | tail`); container logs are capped at 10 MiB × 3, so many containers or a crash-looping pod is the cause; fix the pod.
   - `/var/lib/strange-company/local-path` large → which PVC: `sudo du -sh /var/lib/strange-company/local-path/*`; map directory names to PVCs (`kubectl get pv -o custom-columns=N:.metadata.name,HOSTPATH:.spec.hostPath.path,LOCAL:.spec.local.path,C:.spec.claimRef.name`; k3s local-path PVs are usually hostPath-type, so one of the two path columns is set; STO-24). PostgreSQL: check `SCCnpgWalArchiveBacklog`, database size, and plan 43 retention; Prometheus: `retentionSize` (plan 38).
   - `/var/lib/longhorn` large → snapshots: `kubectl -n longhorn-system get snapshots.longhorn.io` count per volume; plan 13 recurring snapshot retention; delete only snapshots older than the newest backup via Longhorn (UI port-forward or `kubectl delete snapshots.longhorn.io <name>`).
   - Ephemeral pod storage (worker or CI) → evictions handle it; check `kubectl get events -A --field-selector reason=Evicted`.
   - `/var/lib/rancher/k3s/server/db/snapshots` → §Etcd snapshots.
3. **Free space** with the safe action for that consumer. Expected: free space back above 25 % within 15 min; `SCNodeDiskPressure` clears once above the soft threshold (15 %) and the kubelet re-evaluates (≈ 1 min).
4. **If nothing is reclaimable:** the node is undersized for its data. Plan a fix by PR: lower retention (plan 43), move to a larger disk/machine class (plan 04; for `existing`, operator action), or for small-ha spread singletons. Record in the incident.
5. **Pods evicted:** after space is back, Deployments recreate their pods automatically; failed Jobs are retried by their owners (workers count as infrastructure failures, plan 26).

### §Slow disk (`SCEtcdSlowDisk`)
1. `./bootstrap exec -- ssh <node> 'iostat -x 5 3'` (sysstat; install transiently if missing) — look for high `%util`/`await` on the root device.
2. Identify the I/O source: Longhorn rebuilds, CNPG base backup, image pulls, a CI build. Expected to be transient; if it persists > 1 h, pause heavy work (Runner pause; let CI drain).
3. Persistent slowness on an otherwise idle node means the disk is below the etcd requirement (plan 08 fio test): migrate to a better machine class (`add-node.md`/`remove-node.md` at small-ha; rebuild on one node).

### §Etcd snapshots (`SCEtcdSnapshotDirLarge`)
1. `./bootstrap exec -- ssh <node> 'sudo k3s etcd-snapshot ls; sudo du -sh /var/lib/rancher/k3s/server/db/snapshots'`.
2. Check the prune timer: `systemctl status sc-etcd-snapshot-prune.timer`, `journalctl -u sc-etcd-snapshot-prune -n 50`. Run it now: `sudo systemctl start sc-etcd-snapshot-prune`.
3. If scheduled snapshots exceed the retention count (28), the cluster file's `etcd-snapshot-retention` drifted: `upgrade-k3s.md` §Drift.

### §Memory
1. `./bootstrap exec -- kubectl top pods -A --sort-by=memory | head -20` and `kubectl describe node <node> | sed -n '/Allocated resources/,$p'`.
2. Evicted platform pods: list them (`kubectl get pods -A --field-selector status.phase=Failed | grep -v -E 'agent-jobs|ci-jobs'`) and compare their working set with their memory request. A platform pod above its request is evicted before workers (kubelet ranking, plan 51 §8.1): raise its request to its p95 by PR in the owning plan's values (the `./bootstrap verify --only V-30` calibration table gives the number).
3. Repeated OOMKill of a platform container: raise its limit by PR.
4. Worker or CI pods are never the root cause of platform memory loss (their requests equal limits); if the node is simply overcommitted, lower `maxWorkers` or profile limits by PR to `policy/resource-profiles.yaml` (plan 51), or switch the slot mode to exclusive.
5. Expected: `SCNodeMemoryPressure` clears once `memory.available` > 600 MiB for 90 s.

### §CI workspace
The `ci-workspace` filesystem is isolated from `/` (ADR-0305): CI jobs fail, nothing else. Delete orphaned workflow PVCs (`kubectl -n ci-jobs get pvc` older than 105 min; normally `sc-ci-maint` does this, plan 20). If legitimately too small, raise `node_ci_workspace_size` by PR and run `./bootstrap configure --tags baseline` (grow only).

## Verification
- `df -h /` on the node shows > 25 % free; no `SCNode*Disk*`, `SCNodeDiskPressure`, `SCNodeMemoryPressure` alert firing.
- `kubectl get nodes` → no `DiskPressure`/`MemoryPressure` condition.
- Evicted platform pods have been replaced and are Ready.

## Rollback / abort
The safe actions above are not reversible deletions of state (unused images, logs, old snapshots) except Longhorn snapshot deletion, which is acceptable only for snapshots older than the newest successful backup.

## Evidence to record
Before/after `df`, the consumer identified, actions taken, any evicted pods. Incident issue for critical cases.

## Follow-ups
- If a budget line in plan 11 §12 was exceeded, open a PR adjusting retention or sizing.
- If platform requests were raised, re-run `./bootstrap verify --only V-30`.
