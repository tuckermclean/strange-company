# Procedure: Restore etcd

**Owning plan:** plans/07-k3s.md (decision rule: ADR-0158)
**SPEC references:** §110, §113 ("etcd restore"), §16
**Severity / urgency:** outage
**Expected duration:** §A 20–40 min; §B 20–40 min; §C 45–90 min; §D 15 min; §E 15 min
**Requires break-glass:** no for the steps themselves (SSH as `sc-admin` with the operator key). Yes (`SC-BREAK-GLASS-INFRA`, provider console) only if SSH is unavailable.

## When to use
- `SCEtcdNoLeader` firing, the Kubernetes API unavailable, and the ADR-0158 decision tree (below) points to a restore or reset.
- §D: `SCEtcdDbSizeHigh`, or API writes failing with `mvcc: database space exceeded`.
- §E: `SCEtcdSnapshotMissing` (no local snapshot for 13 h).
- Lab drills (07-AC-23, plan 47 drill D8). **Never drill §A–§C on production.**

## Preconditions and required material
- Toolbox: `./bootstrap shell` (it unlocks the recovery bundle with the passphrase, which holds `SC-K3S-TOKEN` and `k3s_token_previous`).
- SSH access to every server as `sc-admin`.
- The k3s token that was current **when the chosen snapshot was taken** (rotation date is in the bundle manifest).
- Snapshot names: `./bootstrap exec -- ssh node-01 'sudo k3s etcd-snapshot ls'` (local and S3; the S3 listing works without a running cluster because credentials are in `/etc/rancher/k3s/config.yaml`).

## Safety checks before acting
1. Walk the ADR-0158 tree and write down the branch taken:
   - Is any server's k3s running with a healthy etcd (`sudo sc-etcdctl endpoint health`)? If quorum is intact → **stop**: use `lost-etcd-member.md`.
   - Quorum lost but one member's data directory intact (`/var/lib/rancher/k3s/server/db/etcd/member` exists and k3s on that node fails only for lack of quorum) → **§B**.
   - No surviving etcd data, node data disks intact (`/var/lib/strange-company/local-path` and `/var/lib/longhorn` present), token available, snapshot ≤ 24 h old → **§A** (one server) or **§C** (three servers).
   - Otherwise → **stop**: `total-cluster-loss.md` (plan 45, rebuild from Git).
2. Logical damage only (deleted objects, bad change) → **stop**: revert in Git first. Restore only per ADR-0158 rule 5, with written acceptance that all API writes since the snapshot are lost.
3. Pause autonomous work if the API is partly up: `./bootstrap exec -- kubectl -n strange-company scale deploy/agent-runner --replicas=0` (it will be restored by Flux later). In §A–§C the API is down, so the **fence** in §Fence is mandatory instead: the restored snapshot (up to 24 h old) resurrects the Runner, worker Jobs, CronJobs and stale CNPG/Longhorn status (DR-11). Do not run `./bootstrap configure` or `upgrade` during this procedure.
4. Blast radius: every API write after the snapshot is lost (CNPG status, Longhorn replica records, Runner Jobs). Data on disks is not touched by §A–§C.

## Steps

### §Fence (used by §A, §B, §C; DR-11, DR-B6)
**0. Before the first start after a reset** (and before starting the survivor in §B), write on that server the drop-in `/etc/rancher/k3s/config.yaml.d/99-restore-fence.yaml` containing `disable-agent: true` (k3s `--disable-agent`, verify on baseline k3s v1.36.x, 07-AC-23). The server then runs the API, etcd and controllers but **no kubelet**: no pod from the restored snapshot can run, so nothing acts on stale state while you fence and compare. The drop-in is node-local and does not change the cluster file (no drift alert). Pods bound to this NotReady node may be marked for deletion after the 300 s taint-based eviction; their controllers recreate them after unfencing, which is expected. Then, after the first `/readyz` = `ok`, apply steps 1–4 through the API (they need no running pod):
1. `./bootstrap exec -- kubectl -n strange-company create configmap sc-emergency-stop --from-literal=level=all --from-literal=reason=etcd-restore` (plan 33 Kubernetes e-stop source; the Gateway and Runner refuse all autonomous work while it exists).
2. `./bootstrap exec -- flux suspend kustomization --all` (Flux would otherwise scale the fenced components back).
3. `./bootstrap exec -- kubectl -n cnpg-system scale deploy --all --replicas=0` and `./bootstrap exec -- kubectl -n longhorn-system patch ds longhorn-manager --type merge -p '{"spec":{"template":{"spec":{"nodeSelector":{"strange.company/fence":"on"}}}}}'` (no node carries the label, so the managers stop).
4. `./bootstrap exec -- kubectl -n strange-company scale deploy/agent-runner --replicas=0`; `./bootstrap exec -- kubectl get cronjob -A -o name | xargs -n1 ./bootstrap exec -- kubectl patch --type merge -p '{"spec":{"suspend":true}}'` (record the list to unsuspend exactly those later).
Expected: `kubectl get pods -A --field-selector status.phase=Running -o name | wc -l` = 0 (no kubelet yet), and the fence objects exist.

### §Compare (data state versus restored API state; DR-11)
With the fence in place, and on §C **before** starting k3s on node-02/03:
1. CNPG: for each Cluster, read `kubectl get cluster <c> -n <ns> -o jsonpath='{.status.currentPrimary} {.status.timelineID}'`. On every node, list the newest timeline history file of each instance's PGDATA: `sudo sh -c 'ls /var/lib/strange-company/local-path/*<c>*/pgdata/pg_wal/*.history 2>/dev/null | sort | tail -1'`. Expected: the instance on the node with the highest timeline is `currentPrimary`, and that timeline equals `timelineID`.
2. Longhorn: for each volume with a `strange.company/state-id` label, list its replicas (`kubectl -n longhorn-system get replicas.longhorn.io -l longhornvolume=<v> -o custom-columns=N:.metadata.name,NODE:.spec.nodeID,FAILED:.spec.failedAt`) and, on each node, the newest `volume-head-*.img` modification time under `/var/lib/longhorn/replicas/<v>-*`. Expected: the newest on-disk replica belongs to a replica that is not marked failed.
3. **CNPG generation (DR-B6):** for each cluster, compare the restored `Cluster`'s backup `serverName` (barman-cloud plugin parameters, plan 12; e.g. `forgejo-pg-g3`), the generation in Git (`cluster-vars` `CNPG_<ID>_GEN` at the current `main`), and the newest `gen-*` prefix in `s3://<prefix>-pg-backups/<cluster>/` (`./bootstrap exec -- aws s3 ls …`, read-only credential). Expected: Git and the bucket agree; the restored object may be older (it is corrected in step 5b before the operator runs). If Git and the bucket disagree, stop and follow `restore-cnpg.md` (plan 12) before unfencing.
4. **Any mismatch** in steps 1–2 → stop: the restored API state contradicts the disks. Use the rebuild path (ADR-0158 rule 4, `total-cluster-loss.md`), restoring data from backups. Record the mismatch in the evidence.
5. No mismatch → **unfence in this order** (DR-B6):
   a. delete `/etc/rancher/k3s/config.yaml.d/99-restore-fence.yaml` and `sudo systemctl restart k3s` (kubelet starts; the fenced components stay down because their objects are fenced);
   b. `flux resume kustomization forgejo-db authentik-db woodpecker-db sc-db` (database layers only) and wait until every `Cluster`'s `serverName` equals `<cluster>-g<CNPG_<ID>_GEN>` from Git;
   c. `flux resume kustomization cnpg` (the operator comes back, against corrected `Cluster` objects); re-check step 1 (`currentPrimary` unchanged);
   d. `flux resume kustomization longhorn longhorn-config` (drift correction removes the longhorn-manager nodeSelector; or remove it with `kubectl -n longhorn-system patch ds longhorn-manager --type json -p '[{"op":"remove","path":"/spec/template/spec/nodeSelector/strange.company~1fence"}]'`), and wait for every volume `healthy`;
   e. `flux resume kustomization --all`, unsuspend the recorded CronJobs, and delete `sc-emergency-stop` only after the Runner is Ready and `SCExecutionStuck` is clear (plan 26 reconciles executions from `sc-pg`).

### §A Single server: restore a snapshot

1. Choose the **newest** snapshot that predates the failure. Expected: a name such as `etcd-snapshot-node-01-<unix>` or `pre-<op>-…`.
2. Stop k3s: `./bootstrap exec -- ssh node-01 'sudo systemctl stop k3s && sudo /usr/local/bin/k3s-killall.sh'`. Expected: `systemctl is-active k3s` = `inactive`. Running pods are stopped by `k3s-killall.sh`; this is an outage you already have.
3. Keep the broken data aside (not idempotent if skipped): `sudo mv /var/lib/rancher/k3s/server/db /var/lib/rancher/k3s/server/db.broken-$(date -u +%Y%m%dT%H%M%SZ)`.
4. If the snapshot is only in S3, keep `etcd-s3` keys in the config (they are) and run the reset with the snapshot **name**; if local, use the full path:
   `sudo k3s server --cluster-reset --cluster-reset-restore-path=<name-or-path>` (verify on the pinned k3s that S3 snapshots are fetched by name when `etcd-s3: true` is configured).
   Expected: the process logs a message that the reset is complete and the server should be restarted without the reset flags, then exits. If it reports a token/decryption error, you are using the wrong token: set the token from the snapshot's era in `/etc/rancher/k3s/config.yaml` (`token:`) temporarily and retry.
5. Write the §Fence step 0 drop-in, then `sudo systemctl start k3s`. Wait ≤ 300 s for `sudo k3s kubectl get --raw /readyz` = `ok`, then run **§Fence** and **§Compare**.
6. If step 4 used a previous token, put the current token back is **not** possible without rotation: run `sudo k3s token rotate --new-token <current token>` (verify) so that the bundle's current value is valid again, then restore the config file with `./bootstrap configure --tags k3s`.

### §B Quorum lost, one surviving member: reset without a snapshot

1. On the survivor (say node-02): `sudo systemctl stop k3s`.
2. `sudo k3s server --cluster-reset`. Expected: completes and exits, leaving a one-member etcd with the survivor's newest data.
3. Edit nothing by hand: the survivor's node file contains `server: https://<node-01>:6443`; k3s ignores it after a reset (verify) — if it does not start, temporarily run `./bootstrap configure --nodes node-02 --tags k3s -e k3s_force_cluster_init=true` (plan 07 role variable) to render `cluster-init: true` for this node.
4. Write the §Fence step 0 drop-in, `sudo systemctl start k3s`; wait for `/readyz` = `ok` and `sudo sc-etcdctl member list` = 1 member; run **§Fence**, and **§Compare** steps 1–4 before starting the other servers; unfence (§Compare step 5) after they have joined.
5. For each other server: `sudo systemctl stop k3s; sudo k3s-killall.sh; sudo mv /var/lib/rancher/k3s/server/db /var/lib/rancher/k3s/server/db.stale-<ts>`; render its node file with `server: https://<survivor internal_ip>:6443` (`./bootstrap configure --nodes <n> --tags k3s -e k3s_join_target=<survivor>`); `sudo systemctl start k3s`. One node at a time; after each: `sudo sc-etcdctl member list` shows one more started voting member. Expected end: 3 members.

### §C Three servers: restore a snapshot

1. Stop k3s on **all** servers (`systemctl stop k3s && k3s-killall.sh`) and move each `server/db` aside as in §A step 3.
2. On node-01 only: §A step 4 (reset with the chosen snapshot), then write the §Fence step 0 drop-in, start k3s and wait for `/readyz`. Run **§Fence**, then **§Compare** steps 1–4 while k3s on node-02/03 is still stopped (their disks are inspected over SSH).
3. On node-02, then node-03: start k3s (their node files point at node-01). Expected: each joins as a new member within 600 s. If a node refuses with "etcd cluster join failed: duplicate node name" or similar, delete its stale member from node-01 with `sudo sc-etcdctl member remove <id>` and retry.
4. Verify `sudo sc-etcdctl member list -w table` = 3 started members, `endpoint health --cluster` all healthy. Only then perform §Compare step 5 (unfence). node-02/03 start with their kubelets; that is safe because the fence objects are already in place.

### §D Database space: compact and defragment

1. `sudo sc-etcdctl endpoint status -w table --cluster` → note `DB SIZE` and `IN USE`.
2. Find the cause before cleaning: `./bootstrap exec -- kubectl get events -A --sort-by=.lastTimestamp | tail`, and count objects by kind (`sudo sc-etcdctl get / --prefix --keys-only | cut -d/ -f3 | sort | uniq -c | sort -rn | head`). Typical culprits: Events storms, orphaned Jobs in `agent-jobs`, CRD status churn. Fix the source first (delete objects, fix the controller).
3. Defragment **one member at a time**: `sudo sc-etcdctl defrag --endpoints=https://<node-ip>:2379`. Expected: `Finished defragmenting etcd member`. k3s compacts revisions automatically; no manual compaction is needed unless `etcd_mvcc_db_total_size_in_use_in_bytes` stays high after defrag (then `sc-etcdctl compact <current revision>`).
4. If an `NOSPACE` alarm was raised: `sudo sc-etcdctl alarm list` then `sudo sc-etcdctl alarm disarm`.

### §E No recent local snapshot

1. `sudo journalctl -u k3s --since -14h | grep -i snapshot` → look for errors (disk full, permission, schedule).
2. `df -h /var/lib/rancher` → if low, follow `disk-pressure.md` first.
3. Take a snapshot now: `sudo k3s etcd-snapshot save --name manual-<UTC ts>`. Expected: success and an S3 object within 120 s.
4. If scheduled snapshots still do not appear by the next cron slot (every 6 h), check that `etcd-snapshot-schedule-cron` is in `/etc/rancher/k3s/config.yaml` (drift: `SCK3sServerConfigDrift`) and re-run `./bootstrap configure --tags k3s`.

## Verification
- `./bootstrap exec -- kubectl get --raw /readyz` = `ok`; `kubectl get nodes` all Ready.
- `sudo sc-etcdctl member list` = expected member count (1 or 3), all started, none learner.
- `./bootstrap exec -- flux get kustomizations -A` all Ready within 30 min (Flux reconciles forward from Git).
- §Compare recorded no mismatch, and after unfencing each CNPG Cluster's `currentPrimary` is still the instance holding the highest timeline (no promotion happened during the fence).
- CNPG: every Cluster `Cluster in healthy state` (`kubectl get cluster -A`); Longhorn: all volumes `healthy` (single node: `healthy` with 1 replica).
- `sc-emergency-stop` deleted, Flux resumed, CronJobs unsuspended (`kubectl get cronjob -A -o jsonpath='{..suspend}'` shows no `true` that was not suspended before), and `/etc/rancher/k3s/config.yaml.d/99-restore-fence.yaml` is absent on every server.
- `./bootstrap verify` passes.
- §D: `SCEtcdDbSizeHigh` resolved; §E: `SCEtcdSnapshotMissing` resolved.

## Rollback / abort
- Before step §A.4 / §C.2: start k3s again with the moved-aside data (`mv db.broken-* db`) if you change your mind.
- After a reset: the previous state is the `db.broken-*` directory; keep it until verification passes plus 7 days, then delete.
- If the restored cluster is worse than expected (PVs pointing to missing data), switch to `total-cluster-loss.md` (rebuild).

## Evidence to record
Decision branch and reason; snapshot name and timestamp; token generation used; command outputs of `member list` and `/readyz` before and after; Flux and CNPG status after; total downtime. Store as `s3://<prefix>-evidence/incidents/<UTC ts>-restore-etcd.json` and in the incident issue.

## Follow-ups
- Re-enable the Runner (Flux restores its replica count on the next reconcile; confirm `agent-runner` Ready).
- The Runner reconciles executions from `sc-pg` (plan 26); check `SCExecutionStuck`.
- Take a fresh snapshot: `sudo k3s etcd-snapshot save --name post-restore`.
- Root-cause the failure; if disk-related, see `disk-pressure.md`.
