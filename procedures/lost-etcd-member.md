# Procedure: Lost etcd member

**Owning plan:** plans/07-k3s.md (procedure listed under plan 47's node lifecycle as well)
**SPEC references:** §16, §110, §113 ("lost etcd member")
**Severity / urgency:** degraded (quorum held) / outage (quorum lost → `restore-etcd.md`)
**Expected duration:** §A 10 min; §B 30–60 min plus `add-node.md`; §C 20–40 min
**Requires break-glass:** no (provider console only if the node is unreachable and must be inspected)

## When to use
- `SCEtcdMemberMissing` firing on small-ha (fewer etcd members up than 3) while the API still answers.
- The expand or add-node pipeline stopped during a join (`JOIN_ABORTED`, plan 47 step C5) → §C.
- Not for single-node installations: one member lost there means `restore-etcd.md`.

## Preconditions and required material
- `./bootstrap shell` (kubeconfig through the SSH forward; recovery bundle unlocked for the k3s token if a rejoin is needed).
- SSH to the surviving servers as `sc-admin`.

## Safety checks before acting
1. Confirm quorum: `./bootstrap exec -- ssh <healthy server> 'sudo sc-etcdctl endpoint health --cluster'`. At least 2 of 3 endpoints healthy → continue. Fewer → **stop**, use `restore-etcd.md` §B.
2. Never remove a member while quorum is already lost.
3. Never remove a second member before the first replacement has joined: with 2 members, losing one more stops all writes.
4. Pause expansion and upgrades (`./bootstrap status` shows no running pipeline).

## Steps

### §A Member down, node alive
1. `./bootstrap exec -- ssh <node> 'sudo systemctl status k3s --no-pager; sudo journalctl -u k3s -n 200 --no-pager'`.
2. Typical causes and fixes: disk full (`disk-pressure.md`), clock skew (`chronyc tracking`; plan 08 §20.3), certificate expiry (`certificate-failure.md` §K3S), config drift (`./bootstrap configure --nodes <node> --tags k3s`).
3. `sudo systemctl restart k3s`; wait ≤ 300 s. Expected: `sc-etcdctl member list` shows the member `started`, `endpoint health --cluster` all healthy, alert resolves within 10 min.
4. If the member's data is corrupt (k3s logs `failed to recover v3 backend` or WAL errors): continue with §B steps 3–6 for this node (remove member, wipe, rejoin) — the node itself stays.

### §B Member permanently lost (node gone or to be replaced)
1. Pre-change snapshot from a healthy server: `sudo k3s etcd-snapshot save --name pre-member-remove`.
2. Follow `lost-node.md` to decide the node is permanently lost and to drain/delete it.
3. `./bootstrap exec -- kubectl delete node <node>` (k3s normally removes the etcd member when its Node object is deleted; verify).
4. `sudo sc-etcdctl member list -w table` on a healthy server. If the lost member is still listed: `sudo sc-etcdctl member remove <member-id>`. Expected: 2 members, both started.
5. Remove the node's IP from firewall peer sets and NetworkPolicy variables (`remove-node.md` steps 9–10).
6. Replace it: `add-node.md` (new node name). Until then the cluster tolerates **no** further failure.

### §C Joiner died during expansion or replacement
1. On node-01 (or the server you joined against): `sudo sc-etcdctl member list -w table`.
2. If the dead joiner appears as a **learner** (`IS LEARNER = true`): `sudo sc-etcdctl member remove <id>`. Quorum was never affected.
3. If it appears as a **voting** member and the other members are healthy (e.g. 3 members, 2 healthy): remove it as in §B step 4.
4. If it is a voting member and quorum is lost (2 members, 1 dead): `restore-etcd.md` §B on node-01 (reset without snapshot keeps node-01's data).
5. On the failed joiner: `sudo systemctl stop k3s; sudo k3s-killall.sh; sudo rm -rf /var/lib/rancher/k3s/server/db` (not idempotent; only on the failed joiner).
6. Resume: `./bootstrap expand --profile small-ha` (or `./bootstrap node add <name>`) resumes at the join step.

## Verification
- `sudo sc-etcdctl member list -w table`: the expected count (3 after replacement), all `started`, none learner.
- `sudo sc-etcdctl endpoint health --cluster`: every endpoint `is healthy`.
- `SCEtcdMemberMissing` resolved; `kubectl create configmap lem-check -n default --dry-run=server -o name` succeeds.
- `sha256sum /etc/rancher/k3s/config.yaml` equal on all servers.

## Rollback / abort
- Member removal is not reversible; a removed member can only rejoin with a wiped data directory (§B/§C steps).
- If quorum is lost at any point, stop and switch to `restore-etcd.md` §B.

## Evidence to record
Member list before/after, member IDs removed, snapshot name, timestamps, cause. `s3://<prefix>-evidence/incidents/<UTC ts>-lost-etcd-member.json`.

## Follow-ups
- Complete `add-node.md` if a node was removed.
- Check CNPG and Longhorn redundancy (`SCCnpgClusterDegraded`, `SCLonghornVolumeDegraded`).
