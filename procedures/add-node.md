# Procedure: Add node (replacement server)

**Owning plan:** plans/47-ha-expansion.md
**SPEC references:** §12, §16, §113 ("add node")
**Severity / urgency:** degraded (restoring redundancy) or routine
**Expected duration:** 45–90 min
**Requires break-glass:** no

## When to use
- A small-ha installation runs with 2 servers after `remove-node.md` or `lost-node.md`/`lost-etcd-member.md` §B.
- `SCHostFirewallNotLoaded` or firewall drift on an existing node → only the §Firewall section.
- Not for the first expansion (use `expand-to-small-ha.md`) and not to grow beyond 3 servers (not supported in MVP).

## Preconditions and required material
- Two healthy servers with quorum (`sudo sc-etcdctl endpoint health --cluster` → 2 healthy).
- `bootstrap.env` listing the new node (paid provider: `EXPECTED_NODE_COUNT=3`; `existing`: the new entry in `EXISTING_NODES`). The new node gets the **next unused** name (`node-04`, `node-05`, …); names are never reused (47-R-5).
- Recovery passphrase; Forgejo access to merge one PR.

## Safety checks before acting
1. No expansion, upgrade or other node operation is running (`./bootstrap status`).
2. The new node has the same shape as the others (preflight enforces; `K3S_MIXED_SIZE_CLASS` otherwise).
3. If the departed node was the last holder of something (Longhorn replica, CNPG instance), its data is already rebuilt or rebuilding elsewhere; check `SCLonghornVolumeDegraded` and `SCCnpgClusterDegraded` and understand why before adding load.
4. Blast radius: tls-san roll restarts the two existing servers one at a time; one join (the cluster has 2 voting members until the new one is started; losing one of the two existing servers during this time stops writes).

## Steps
1. `./bootstrap node add node-0N`. Expected: preflight passes; for paid providers the plan and cost are shown and confirmed with `node-add <cluster> <D8>`.
2. Provision (paid) or reach (existing) the node.
3. **Peer PR:** `bootstrap/node-add-<run-id>` replaces the departed node's IP slot (`SC_NODE_IP_n`) with the new node's internal IP. Exit 75 → merge → wait for `flux get kustomization policies` Ready at the merge revision → rerun step 1's command.
4. Configure (automatic): baseline + firewall on the new node (writes `/etc/strange-company/install-uid`), firewall peer update on the existing servers, tls-san roll (one server at a time), `pre-join` snapshot, join, toolbox image import if the toolbox was built locally, CoreDNS anti-affinity check, Longhorn node Ready. Exit 1 at the join → `lost-etcd-member.md` §C.
5. Converge (automatic, ≤ 90 min): CNPG schedules the missing instance of each cluster onto the new node and it streams from the primary; Longhorn rebuilds the missing replica of each volume (`replicaAutoBalance`).
6. Direct mode: `sc-bootstrap dns apply` adds the new node's A records once Traefik is Ready there (automatic).
7. Verify (automatic).

### §Firewall (re-apply only)
`./bootstrap configure --nodes <node> --tags firewall`. Expected: `changed=1` the first time (table reloaded), `SCHostFirewallNotLoaded` resolves within 10 min, and an external scan matches plan 08 §9.4.

## Verification
- `kubectl get nodes` → 3 Ready; `sc-etcdctl member list` → 3 started members.
- `kubectl cnpg status <cluster>` → 3 instances on 3 distinct nodes for all four clusters.
- Longhorn volumes healthy with 2 replicas on distinct nodes (47-AC-11 query).
- `sudo nft list set inet sc_host sc_peers_v4` on every node lists exactly the current three nodes.
- `./bootstrap verify` passes.

## Rollback / abort
- Before the join: revert the peer PR, delete the new node (paid) — nothing changed in the cluster.
- After the join: `remove-node.md` for the new node.

## Evidence to record
Run report, PR URL, member list before/after, time to CNPG and Longhorn redundancy. Attach to the incident or change issue.

## Follow-ups
- If this replaced a lost node, close the `lost-node.md` incident and run drill HA3 (plan 47) at the next quarterly campaign.
- Update provider-side records (DNS, monitoring of the external heartbeat if it pinged node IPs).
