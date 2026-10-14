# Procedure: Remove node

**Owning plan:** plans/47-ha-expansion.md
**SPEC references:** §12, §16, §113 ("remove node"), §114
**Severity / urgency:** routine (planned replacement, OS release change, provider migration)
**Expected duration:** 30–90 min (dominated by Longhorn replica eviction)
**Requires break-glass:** no

## When to use
- Replacing a healthy server (OS release upgrade by replacement, plan 48 §14.2; hardware or provider change).
- Aborting an expansion after the joins (`--allow-two-to-one`, plan 47 §20).
- **Not** for a node that is already dead: use `lost-node.md`.
- **Not** to reduce small-ha permanently below 3 servers (unsupported); always follow with `add-node.md`.

## Preconditions and required material
- Three healthy servers (`sc-etcdctl endpoint health --cluster` → 3 healthy), all Longhorn volumes `healthy`, all CNPG clusters healthy.
- Free capacity on the remaining two nodes for the evicted Longhorn replicas and pods (check `kubectl describe node` allocatable vs requests; Longhorn node `storageAvailable`).

## Safety checks before acting
1. No other pipeline running; no critical alert.
2. Fresh etcd snapshot will be taken by the run; confirm S3 is reachable (`SCObjectStorageUnreachable` not firing).
3. Blast radius: after removal the cluster has 2 etcd members and tolerates **no** further server failure until `add-node.md` completes. CNPG runs with 2 instances (one pending); Longhorn volumes keep 2 replicas on the remaining nodes.

## Steps
1. `./bootstrap node remove <node>`. Expected: preflight passes; the run takes `pre-node-remove` etcd snapshot (S3 confirmed).
2. **Direct mode only:** the run removes the node's A records (`sc-bootstrap dns apply --exclude <node>`) and waits 2 × TTL (120 s).
3. Cordon: `kubectl cordon <node>` (automatic).
4. Longhorn eviction (automatic): node `allowScheduling=false`, `evictionRequested=true`; waits until `kubectl -n longhorn-system get replicas.longhorn.io -l longhornnode=<node>` is empty (timeout 60 min). Timeout → stop; check Longhorn UI via `./bootstrap exec -- kubectl -n longhorn-system port-forward svc/longhorn-frontend 8080:80` for blocked rebuilds (disk space).
5. Drain: `kubectl drain <node> --ignore-daemonsets --delete-emptydir-data --timeout=15m` (automatic). CNPG performs a switchover first if a primary is on the node. A PDB that blocks the drain stops the run: identify it (`kubectl get pdb -A`) and resolve the unhealthy workload, then rerun.
6. `kubectl delete node <node>`. Then on a remaining server `sudo sc-etcdctl member list`; if the removed node's member is still present, the run executes `sc-etcdctl member remove <id>`. Expected: 2 started members.
7. On the removed node (automatic over SSH): stop and disable k3s, run the pinned `k3s-killall.sh`, delete `/var/lib/rancher/k3s`, `/etc/rancher/k3s`, `/var/lib/longhorn`, `/var/lib/strange-company/local-path`, unmount and delete the `ci-workspace` image, delete `/etc/strange-company/install-uid`.
8. Firewall on the survivors: `sc_peers_v4`/`sc_servers_v4` without the node (automatic `configure --tags firewall`).
9. **Peer PR:** `bootstrap/node-remove-<run-id>` sets the node's `SC_NODE_IP_n` slot back to `SC_NODE_IP_1`. Exit 75 → merge → rerun.
10. Provider deletion (paid providers; guarded by plan 03 confirmation `node-remove <cluster> <D8>`). `existing` nodes are only cleaned, never powered off.
11. Immediately continue with `add-node.md`.

## Verification
- `kubectl get nodes` no longer lists the node; `sc-etcdctl member list` → 2 members, both healthy.
- No Longhorn replica and no CNPG instance references the node (`kubectl get pods -A -o wide | grep <node>` empty).
- External scan of the removed node's IP (if it still exists) shows no k3s ports.
- `./bootstrap verify --only V-10,V-18,V-19` reports the expected degraded-but-healthy state (2 nodes), with the run's waiver note `node-replacement-in-progress`.

## Rollback / abort
- Before step 6: `kubectl uncordon <node>`, set Longhorn `allowScheduling=true`, `evictionRequested=false`; restore the DNS record (direct mode). Nothing is lost.
- After step 6: the node cannot rejoin with its old data; re-add it as a new node with `add-node.md` after wiping (step 7) — its old name is retired.

## Evidence to record
Run report, snapshot name, member list before/after, eviction duration, PR URL, provider deletion confirmation. Attach to the change issue.

## Follow-ups
`add-node.md` (mandatory). After it completes, `SCEtcdMemberMissing` and degraded-redundancy alerts must be clear.
