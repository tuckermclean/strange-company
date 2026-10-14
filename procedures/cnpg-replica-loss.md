# Procedure: CNPG replica loss

**Owning plan:** plans/12-cnpg.md
**SPEC references:** §25, §96, §113 ("CNPG replica loss")
**Severity / urgency:** degraded (redundancy reduced; the application keeps working)
**Expected duration:** 15–60 min (re-clone time scales with database size)
**Requires break-glass:** no

## When to use
- `SCCnpgClusterDegraded` (fewer ready instances than desired) where the primary is healthy.
- `SCCnpgReplicationLagHigh` (lag > 300 s for 10 min).
- `SCCnpgSyncReplicationDegraded` (proposed): synchronous standby missing; commits degraded to asynchronous.
- A replica pod stuck `Pending` after a node loss (small-ha only; at instances = 1 there are no replicas).

## Preconditions and required material
- `./bootstrap shell` (kubectl, `kubectl cnpg`).
- At least one other schedulable node with free disk ≥ the database size + 2 GiB (local-path) for a re-clone.

## Safety checks before acting
1. Confirm the primary is healthy first: `kubectl cnpg status <cluster> -n <ns>` shows `Current Primary` Ready and `Working WAL archiving: OK`. If not, use `cnpg-primary-loss.md` instead.
2. Never destroy the **primary** instance with this procedure. Double-check the instance name against `Current Primary`.
3. Destroying a replica deletes its PVC and data. That is safe only because the primary and the object store hold the data.

## Steps
1. Identify the lost or lagging instance:
   `kubectl cnpg status <cluster> -n <ns>` and `kubectl -n <ns> get pods -l cnpg.io/cluster=<cluster> -o wide`.
   Expected: one instance not Ready, Pending, or with high lag.
2. **Replica pod crash-looping on a healthy node:** check `kubectl -n <ns> logs <pod> -c postgres --previous | tail -50`.
   - WAL gap (`requested WAL segment … has already been removed`): the replica fell behind beyond retained WAL. Go to step 4 (destroy and re-clone).
   - Resource or disk issue: fix it as in `cnpg-primary-loss.md` Branch B step 1; expected: replica catches up, lag falls under 60 s.
3. **Replica Pending on a NotReady node:**
   - If the node will return within the maintenance window (planned reboot): wait. Expected: the pod starts on the same node and catches up.
   - If the node is permanently lost or its disk is gone: continue with step 4.
4. **Destroy and re-clone** (§Destroy and re-clone):
   `kubectl cnpg destroy <cluster> <instance-number> -n <ns>`
   Expected: CNPG deletes the instance's pod and PVC and creates a new instance (new number) on a schedulable node, cloned from the primary with `pg_basebackup`. With `podAntiAffinityType: required` the new instance lands on a node without another instance of the cluster.
   If the new instance stays `Pending` with `didn't match pod anti-affinity rules`: not enough nodes; add or repair a node (`lost-node.md`, plan 47). The cluster runs with two instances meanwhile.
5. Wait for streaming: `kubectl cnpg status <cluster> -n <ns>` every 2 minutes.
   Expected within 15 min (≈ 5 GiB database): the new replica `Streaming`, lag < 60 s, and at small-ha `Synchronous standby names` contains `ANY 1`.
6. **Lag without failure** (`SCCnpgReplicationLagHigh` with all instances Ready): check node I/O (`node_disk_io_time_seconds_total`), network between nodes (WireGuard), and long-running transactions on the primary (`select pid, now()-xact_start, state from pg_stat_activity where xact_start is not null order by 2 desc limit 5` via `kubectl -n <ns> exec <primary> -c postgres -- psql -U postgres -c …`). Expected: lag recovers once the cause is removed. Do not destroy a lagging but streaming replica.

## Verification
- `kubectl cnpg status <cluster> -n <ns>`: instances = `spec.instances`, one primary, all replicas streaming, each on a distinct node:
  `kubectl -n <ns> get pods -l cnpg.io/cluster=<cluster> -o jsonpath='{range .items[*]}{.spec.nodeName}{"\n"}{end}' | sort -u | wc -l` = 3 at small-ha.
- `SCCnpgClusterDegraded` and `SCCnpgReplicationLagHigh` resolved.
- `storage-audit` SA-6 passes at its next run.

## Rollback / abort
- A destroyed replica cannot be brought back; the re-clone replaces it. If the re-clone cannot schedule, the cluster runs degraded until capacity returns. That is safe, and synchronous replication degrades to asynchronous (`dataDurability: preferred`).

## Evidence to record
- Instance destroyed, new instance name and node, re-clone duration (from `kubectl cnpg status` timestamps).

## Follow-ups
- If the cause was a lost node, finish `lost-node.md` (plan 47).
- If WAL gaps recur, review `max_slot_wal_keep_size` (plan 12 §9.3) against replica downtime.
