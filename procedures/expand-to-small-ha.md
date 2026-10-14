# Procedure: Expand from one node to small-ha (three nodes)

**Owning plan:** plans/47-ha-expansion.md
**SPEC references:** §8, §12, §16, §25, §27, §113 ("one-node to three-node expansion"), §119 PHASE 6
**Severity / urgency:** routine (planned)
**Expected duration:** 2–4 hours of machine time plus three PR reviews; workers are paused throughout
**Requires break-glass:** no

## When to use
The installation runs `self-hosting-minimum`, the Self-Hosting Threshold work is in a stable state (sequencing preference after R02), and the operator wants the small-ha baseline.

## Preconditions and required material
- `bootstrap.env` with `EXPECTED_NODE_COUNT=3`, the same `MACHINE_CLASS` as node-01 (or, for `existing`, two more nodes of the same shape in `EXISTING_NODES`), and `MAX_MONTHLY_COST` covering three nodes.
- Recovery passphrase (the pipeline unlocks the bundle).
- Forgejo access to review and merge three PRs.
- A maintenance window of ≥ 4 h with no planned upgrade (plan 48 freezes upgrades during expansion).

## Safety checks before acting
1. `./bootstrap verify` passed within the last 24 h. If not, run it and fix failures first.
2. Backups fresh (the pipeline checks too): `./bootstrap exec -- kubectl get backups.postgresql.cnpg.io -A --sort-by=.status.stoppedAt | tail -4` shows completed backups < 26 h old for all four clusters; no `SCLonghornBackupStale`, `SCEtcdSnapshotUploadFailing` or critical alert firing.
3. node-01 root filesystem ≥ 30 % free (`./bootstrap exec -- ssh node-01 'df -h /'`).
4. Do not touch the cluster by hand while the pipeline runs; every step is journaled.
5. Blast radius: node-01's k3s restarts once (API unavailable ≈ 1 min; workloads keep running). During the two-member etcd window (≤ 20 min) the loss of either member stops API writes.

## Steps

1. **Start:** `./bootstrap expand --profile small-ha`. Expected: preflight passes, the plan shows two new nodes and the monthly cost, and asks for the confirmation token `expand <cluster> <D8>`. Exit 3 → read the failing preflight check in the output and fix it; exit 4 → cost/guardrail refusal (adjust `MAX_MONTHLY_COST` or machine class).
2. **Provision:** continues automatically (paid providers create node-02 and node-03). Expected: `./bootstrap status` shows `expand.provision succeeded`. Resumable on failure (rerun step 1).
3. **Peer PR:** the run opens `bootstrap/expand-peers-<run-id>` and exits **75**. Review: the diff only changes `SC_NODE_IP_2` and `SC_NODE_IP_3` in `gitops/clusters/<cluster>/vars/cluster-vars.yaml`. Merge it. Wait until `./bootstrap exec -- flux get kustomization policies` shows `Ready` at the merge revision. Rerun `./bootstrap expand --profile small-ha`.
4. **Configure** (automatic, ~30–60 min): baseline and firewall on the new nodes, firewall peers on all three, Runner dispatch paused (it waits for running workers; cancel them with `agent_cancel` from Hermes or `./bootstrap exec -- sc-bootstrap runner cancel --all` if you cannot wait), tls-san roll on node-01, `pre-join` snapshot, join node-02, join node-03, toolbox image import (only if the toolbox was built locally), CoreDNS to 2 replicas, Longhorn nodes Ready, `ci-workspace` grown to 24 GiB.
   - Exit 1 at a join step (`JOIN_ABORTED`) → **stop** and follow `lost-etcd-member.md` §C, then rerun.
   - Exit 1 elsewhere → `./bootstrap status` names the step and error; fix and rerun (steps are idempotent).
5. **Platform PR:** the run opens `bootstrap/expand-<run-id>-platform` and exits 75. Review: the three profile lines switch to `small-ha`; `CI_MAX_CONCURRENT=2`; `CNPG_INSTANCES` and `LONGHORN_REPLICAS` stay `1`. Merge. Rerun. Expected: `expand.converge.platform` succeeds within 30 min (replica counts, kured, heavy-slot 64; Loki/Tempo are not part of this PR, GC-06). Direct mode: DNS A records for node-02/03 are added automatically.
6. **Data PR:** the run opens `bootstrap/expand-<run-id>-data` and exits 75. Review: `CNPG_INSTANCES=3`, CNPG PDB/anti-affinity/sync variables, `LONGHORN_REPLICAS=2`. Merge. Rerun. Expected: `expand.converge.data` succeeds within 90 min (each CNPG cluster 3 instances on 3 nodes; every Longhorn volume 2 healthy replicas on 2 nodes).
7. **Verify:** automatic. Expected final line `EXPAND COMPLETE small-ha`; Runner dispatch resumed.

## Verification
- `./bootstrap exec -- kubectl get nodes` → 3 Ready.
- `./bootstrap exec -- ssh node-01 'sudo sc-etcdctl member list -w table'` → 3 started, no learners.
- `./bootstrap exec -- kubectl cnpg status sc-pg -n strange-company` → 3 instances, `ANY 1` synchronous.
- `./bootstrap exec -- kubectl -n longhorn-system get volumes.longhorn.io` → every volume `healthy`, `numberOfReplicas` 2.
- `./bootstrap verify` passes with small-ha expectations; plan 47 GATE-H01/H02 items.

## Rollback / abort
Follow plan 47 §20 by abort point:
- Before the joins: close the open PR (or revert the merged peer PR) and delete the new nodes (`./bootstrap destroy` is **not** used; plan 03 guarded node deletion via `./bootstrap node remove <name> --not-joined`).
- After the joins, before the platform PR: `remove-node.md` for node-03, then node-02 with `--allow-two-to-one`; revert the peer PR.
- After the platform PR, before the data PR: revert the platform PR, wait for Flux, then as above.
- After the data PR: no supported automatic rollback; open an incident and consult plans 12/13 (Q-47-04).

## Evidence to record
The expand report (`./bootstrap report --json`), including `etcd_two_member_window_seconds`; the three PR URLs; the verify report. Attach to the expansion issue.

## Follow-ups
- **Telemetry (H03):** after ≥ 3 days of small-ha data, plan 41's `telemetry-decide` opens the telemetry PR (`deploy`, `tempo-only`) or records `defer`; review and merge it like the others.
- **Drills:** run the H04 drill set HA1–HA8 within 14 days (`./bootstrap exec -- sc-bootstrap drill all`), which also clears `SCHADrillOverdue`. Repeat quarterly.
- Re-run the capacity measurement after a week of normal load (`./bootstrap verify --only V-30`).
- Update the operator runbook copy of node IPs (DNS, provider console).
