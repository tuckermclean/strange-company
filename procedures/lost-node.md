# Procedure: Lost node

**Owning plan:** plans/47-ha-expansion.md
**SPEC references:** §12, §16, §96 ("node failure"), §113 ("lost node")
**Severity / urgency:** outage (single node) / degraded (small-ha)
**Expected duration:** triage 15–30 min; replacement 1–2 h (small-ha); single node: see `restore-etcd.md` or `total-cluster-loss.md`
**Requires break-glass:** `SC-BREAK-GLASS-INFRA` (provider console/rescue) if the node does not answer SSH

## When to use
- `SCNodeDown` (node NotReady or node-exporter unreachable for 5 min).
- Related symptoms routed here by the alert catalogue: `SCNodeMemoryPressure`, `SCNodeOOMKill`, `SCWireGuardPeerStale`, `SCCoreDNSDown` (§CoreDNS), `SCNodeClockSkew` (§Clock), `SCNetworkPolicyControllerDown` (§NetPol).

## Preconditions and required material
- `./bootstrap shell` (kubeconfig via any reachable server; the SSH forward can target node-02/03 with `./bootstrap shell --api-node <node>`, plan 50 assumed flag).
- Provider console access (paid) or physical/hypervisor access (`existing`).

## Safety checks before acting
1. Do **not** delete the Node object or remove its etcd member until the node is classified as permanently lost (step 3); a node that comes back after its member was removed must be wiped before it can rejoin.
2. On small-ha, confirm quorum first (`sc-etcdctl endpoint health --cluster` from a healthy server): 2 of 3 healthy is degraded-but-safe; fewer → `restore-etcd.md` §B.
3. Direct mode: about one third of new client connections fail while the dead node's A record exists; removing the record (step 4) is safe and reversible.

## Steps
1. **Classify reachability:** `./bootstrap exec -- ssh <node> true` (timeout 10 s); provider console status; `ping` (ICMP allowed by plan 08).
2. **If reachable:** inspect `systemctl status k3s`, `journalctl -u k3s -n 200`, `df -h /`, `free -m`, `chronyc tracking`, `nft list table inet sc_host`. Fix the cause (disk → `disk-pressure.md`; clock → §Clock; firewall → `add-node.md` §Firewall; k3s crash → `lost-etcd-member.md` §A). Expected: node Ready within 10 min of the fix. Done.
3. **If unreachable:** wait up to 30 minutes unless the provider reports the instance destroyed or its disk lost. After 30 minutes, or immediately on confirmed loss, classify as **permanently lost** and record the time.
4. **Small-ha, permanently lost:**
   1. Direct mode: `./bootstrap exec -- sc-bootstrap dns apply --exclude <node>`.
   2. Let Kubernetes and the operators react: pods are evicted after the 300 s `NoExecute` toleration; CNPG promotes a replica if the primary was there (< 60 s); Longhorn marks replicas on the node failed and rebuilds on the remaining nodes. Check `kubectl get pods -A -o wide | grep <node>`; pods stuck `Terminating` with RWO Longhorn volumes are force-deleted by Longhorn's `nodeDownPodDeletionPolicy` (plan 13); if not within 10 min, `kubectl delete pod <p> -n <ns> --force --grace-period=0` for those pods only.
   3. `lost-etcd-member.md` §B (delete Node, remove member).
   4. Remove the node from firewall peers and NetworkPolicy variables (`remove-node.md` steps 8–9).
   5. `add-node.md` with a new node name.
5. **Single node, permanently lost:** this is a cluster outage.
   - Disk intact and the node can be rebooted or reattached (provider rescue): repair and boot; if etcd fails to start, `restore-etcd.md` (ADR-0158 tree).
   - Disk lost: `total-cluster-loss.md` (plan 45, rebuild from Git and backups).

### §CoreDNS
`./bootstrap exec -- kubectl -n kube-system get deploy coredns`; `kubectl -n kube-system rollout restart deploy/coredns`. If the Deployment is missing: `./bootstrap configure --tags network` (re-places `sc-coredns.yaml`; k3s applies it within 1 min). Expected: `SCCoreDNSDown` resolves within 5 min.

### §Clock
`./bootstrap exec -- ssh <node> 'chronyc sources -v; chronyc tracking'`. If no source is reachable, set `node_ntp_servers` to the provider's NTP by PR and run `./bootstrap configure --nodes <node> --tags baseline`; for a skew above 1 s run `sudo chronyc makestep` once.

### §NetPol
The `netpol-canary` check found NetworkPolicy not enforced. Verify `disable-network-policy` is absent from `/etc/rancher/k3s/config.yaml` on every server, check `journalctl -u k3s | grep -i 'network policy'`, re-run `./bootstrap configure --tags k3s`, then trigger the canary: `./bootstrap exec -- kubectl -n monitoring create job --from=cronjob/netpol-canary np-now`. Expected: Job succeeds (connection denied as intended).

## Verification
- `kubectl get nodes` → all expected nodes Ready (or the replacement after `add-node.md`).
- `SCNodeDown` resolved; etcd members as expected; CNPG and Longhorn redundancy restored (`SCCnpgClusterDegraded`, `SCLonghornVolumeDegraded` resolved).
- `./bootstrap verify` passes.

## Rollback / abort
Before step 4.3 everything is reversible: if the node returns, uncordon it and restore its DNS record. After the member is removed, the old node must be wiped before reuse.

## Evidence to record
Timeline (detection, classification, recovery), provider ticket, decisions taken (permanent loss criteria), member lists, CNPG failover times. `s3://<prefix>-evidence/incidents/<UTC ts>-lost-node.json`.

## Follow-ups
- Root-cause analysis with the provider.
- If the loss revealed slow failover, schedule drill HA2 (plan 47).
