# Procedure: Longhorn replica loss, stuck attachment and degraded volumes

**Owning plan:** plans/13-longhorn.md
**SPEC references:** §27, §96, §108, §113 ("Longhorn replica loss")
**Severity / urgency:** degraded (one replica lost) to outage (no healthy replica, or the consumer cannot attach)
**Expected duration:** 10–60 min
**Requires break-glass:** cluster administration if the normal kubeconfig is unavailable

## When to use
- `SCLonghornVolumeDegraded` (a volume has fewer healthy replicas than configured).
- `SCLonghornVolumeFaulted` at small-ha (no healthy replica): §Salvage first, then `restore-longhorn.md`.
- `SCLonghornVolumeAttachStuck` / `SCLonghornVolumeAttachStuckCritical`, plan 16 `SCForgejoPodStuckPending`, or a pod stuck in `ContainerCreating` with event `FailedAttachVolume … Multi-Attach error`: §Stuck attachment (lesson L-5).
- `SCLonghornManagerDown`: §Manager.
- Attach failures mentioning iSCSI or "already mounted or mount point busy": §Node prerequisites.
- Replica-sync Job failure or `storage-audit` SA-7 failure: §Replica count.

## Preconditions and required material
- `./bootstrap shell` (kubectl, jq). The Longhorn UI is not needed.
- For node actions: SSH to the node (`SC-OPERATOR-SSH`) through the toolbox (`ssh` is in the toolbox).

## Safety checks before acting
1. **Never force-delete a pod whose node is reachable** (`kubectl delete pod --force --grace-period=0`) while its containers may still run: two nodes writing one ext4 volume corrupt it.
2. Never edit or delete Longhorn `Engine` or `Replica` objects of a volume that still has a healthy replica.
3. Never delete a Longhorn `Volume`, PV or PVC named `forgejo-data`, `hermes-data` or `dr-canary` in this procedure. Replacement is `restore-longhorn.md`.
4. At self-hosting-minimum (1 replica), a "lost replica" is lost data: go directly to `restore-longhorn.md`.

## Steps

### Triage
1. `kubectl -n longhorn-system get volumes.longhorn.io -o custom-columns=NAME:.metadata.name,STATE:.status.state,ROBUSTNESS:.status.robustness,NODE:.status.currentNodeID,REPLICAS:.spec.numberOfReplicas`
2. `kubectl -n longhorn-system get replicas.longhorn.io -l longhornvolume=<volume> -o custom-columns=NAME:.metadata.name,NODE:.spec.nodeID,STATE:.status.currentState,FAILED:.spec.failedAt`
3. `kubectl -n longhorn-system get nodes.longhorn.io` (Ready, Schedulable) and `kubectl get nodes`.
Choose the section that matches.

### §Degraded (one replica lost, small-ha)
1. Expected automatic behavior: Longhorn rebuilds a replica on another schedulable node. Check progress: `kubectl -n longhorn-system get replicas.longhorn.io -l longhornvolume=<volume>` shows a new replica in `running` with a rebuild in progress (`kubectl -n longhorn-system get volumes.longhorn.io <volume> -o jsonpath='{.status.robustness}'` returns to `healthy`).
2. If no rebuild starts within 10 minutes:
   a. Is any other node schedulable with enough space? `kubectl -n longhorn-system get nodes.longhorn.io -o json | jq '.items[] | {name:.metadata.name, schedulable:(.status.conditions[]|select(.type=="Schedulable").status), disks:[.status.diskStatus[]|{avail:.storageAvailable, sched:.storageScheduled, max:.storageMaximum}]}'`.
   b. If every node is unschedulable because of `storageMinimalAvailablePercentage`: free disk per `disk-pressure.md` (plan 08), then wait.
   c. If only one node exists (a node was lost at small-ha): the volume stays degraded until a node is added (`lost-node.md`, plan 47). This is safe; one healthy replica serves I/O.
3. If the failed replica's node is permanently gone, remove its replica record after a new replica is healthy: `kubectl -n longhorn-system delete replicas.longhorn.io <failed-replica>`.
Expected: robustness `healthy`, replicas on two distinct nodes.

### §Salvage (all replicas faulted, small-ha)
1. Longhorn auto-salvage (default on) tries to reuse the most recent replica when the volume is detached. Ensure the consumer is scaled to 0 so the volume detaches: `kubectl -n <ns> scale deploy/<consumer> --replicas=0`.
2. Wait 5 minutes and re-check robustness. Expected: `degraded` or `healthy` with at least one replica `running`; then scale the consumer back up (or resume its Flux layer).
3. If the volume stays `faulted`: the data must come from backup. Go to `restore-longhorn.md` §Replace. Record the faulted replicas' node names for the root-cause analysis before replacing.

### §Stuck attachment (L-5)
Symptom: new pod `ContainerCreating` > 2 min with `FailedAttachVolume` or `Multi-Attach error`; `longhorn_volume_state` stuck at attaching (4) or detaching (5).
1. Find every pod using the claim:
   `kubectl get pods -A -o json | jq -r '.items[] | select(any(.spec.volumes[]?; .persistentVolumeClaim.claimName=="<pvc>")) | "\(.metadata.namespace)/\(.metadata.name) \(.spec.nodeName) \(.status.phase)"'`
   Expected: exactly one pod (the new one). If an old pod still exists and is Running on a Ready node, the rollout is not done: wait for it to terminate (Recreate strategy). Do not delete it forcibly.
2. Find the attachment: `kubectl get volumeattachments.storage.k8s.io -o json | jq -r '.items[] | select(.spec.source.persistentVolumeName=="<pv>") | "\(.metadata.name) \(.spec.nodeName) attached=\(.status.attached)"'` and `kubectl -n longhorn-system get volumeattachments.longhorn.io <volume> -o yaml` (attachment tickets, verify kind name).
3. **Old node NotReady:** confirm with the provider console (or `ssh`) that the machine is powered off or unreachable for good. Then:
   `kubectl taint node <old-node> node.kubernetes.io/out-of-service=nodeshutdown:NoExecute`
   Expected: within 2 minutes the old VolumeAttachment is removed, the volume attaches to the new node, and the pod starts. Remove the taint once the node is repaired or deleted.
   If you cannot confirm the machine is down, wait for Kubernetes' 6-minute force-detach instead. Do not taint a machine that might still be writing.
4. **Old node Ready, no pod on it uses the claim, but a VolumeAttachment for the PV still names it:**
   `kubectl delete volumeattachments.storage.k8s.io <name-on-old-node>`
   Expected: the CSI attacher detaches the volume from the old node within 1 minute. Then retry the pending pod at once: `kubectl -n <ns> delete pod <new-pod>` (its controller recreates it).
5. If the volume is stuck in `detaching` with no attachment ticket on any node for more than 10 minutes, restart the manager on the node that holds it: `kubectl -n longhorn-system delete pod -l app=longhorn-manager --field-selector spec.nodeName=<node>`. Expected: state settles to `detached`, then attaches for the new pod.
6. Do not modify the Longhorn `Engine`. If none of the above works within 30 minutes, escalate and keep the consumer scaled to 0 (no data risk while detached).

### §Manager
1. `kubectl -n longhorn-system get ds,deploy,pods -o wide | grep -v Running`.
2. `kubectl -n longhorn-system logs <pod> --previous | tail -50`. Common causes: image pull failure, missing `iscsid` on the node, webhook certificate issues.
3. An instance-manager crash detaches the volumes on that node, and consumer pods restart automatically (`autoDeletePodWhenVolumeDetachedUnexpectedly`). Expected after the manager recovers: volumes `attached`/`healthy`.
4. `flux reconcile helmrelease longhorn -n longhorn-system` if the HelmRelease is not Ready. Do **not** roll back the chart version (13-R-4).

### §Node prerequisites
1. On the node: `systemctl is-active iscsid` (expected `active`); `lsmod | grep iscsi_tcp`; `multipath -t 2>/dev/null | grep -A2 blacklist` (expected: the `^sd[a-z0-9]+` devnode blacklist) or `systemctl is-enabled multipathd` ≠ `enabled`.
2. Fix by re-running the node baseline from the toolbox (plan 08; the Ansible role is idempotent): `./bootstrap --from-phase configure` (plan 50 resume semantics) or the plan 08 targeted tag if available.
3. Expected: attach succeeds on the next retry.

### §Replica count (profile change did not converge)
1. `kubectl -n longhorn-system get jobs -l app.kubernetes.io/name=longhorn-replica-sync` and `kubectl -n longhorn-system logs job/longhorn-replica-sync-r<N>`.
2. Typical cause: not enough schedulable nodes or disk for N replicas. Fix capacity (plan 47), then let the Job be recreated: `kubectl -n longhorn-system delete job longhorn-replica-sync-r<N>` (Flux recreates it within 10 minutes).
3. Expected: Job `succeeded`, every important volume has `numberOfReplicas: N` and N running replicas on distinct nodes.

## Verification
- Every important volume: `status.robustness` = `healthy`, `spec.numberOfReplicas` = `LONGHORN_REPLICAS`, replicas on distinct nodes (13-AC-15 command).
- Consumers Ready (Forgejo `/api/healthz`, Hermes health).
- The triggering alert has resolved.
- If data integrity is in doubt after a salvage: run `kubectl -n dr-tests create job --from=cronjob/restore-test-longhorn rt-manual-$(date +%s)` and, for Forgejo, `forgejo doctor check --all` in the pod (verify command).

## Rollback / abort
- Every step above is non-destructive except the out-of-service taint (forces pods off a node) and VolumeAttachment deletion (detaches from the old node). Both are safe only under the stated confirmations. If unsure, stop with the consumer scaled to 0: a detached volume does not lose data.

## Evidence to record
- Volume, node(s), the section used, timestamps, and any taint or VolumeAttachment deletion performed.

## Follow-ups
- Repeated stuck attachments on rollouts: review the consumer's `terminationGracePeriodSeconds` and Recreate strategy (plan 16/24); record in the L-5 notes.
- Lost node: `lost-node.md` (plan 47).
- Faulted volumes: root-cause disk or I/O errors (`dmesg` on the node).
