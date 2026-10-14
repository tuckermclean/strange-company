# 47 — HA expansion (self-hosting-minimum → small-ha)

**Status:** Stage-0 plan (draft)
**DAG nodes:** H01 (co-owned with 07: nodes 2–3 join, etcd quorum), H02 (co-owned with 12, 13: sequencing of CNPG 1→3, Longhorn 1→2, anti-affinity), H04 (co-owned with 45: HA hardening drills)
**Related plans:** [03](03-provider-interface.md) (profiles, provisioning), [07](07-k3s.md) (join mechanics), [08](08-node-security.md) (baseline, firewall peers, kured), [09](09-networking.md) (CoreDNS, Traefik, cloudflared, DNS), [12](12-cnpg.md), [13](13-longhorn.md), [18](18-flux.md) (`sc-gitops set-profile`), [20](20-woodpecker.md), [26](26-agent-runner.md), [36](36-network-policy.md) (node IP variables), [38](38-prometheus.md), [41](41-logging-tracing.md) (H03), [42](42-alerting-notifications.md), [45](45-recovery.md), [48](48-upgrade.md), [50](50-bootstrap-orchestrator.md) (expand pipeline, CLI), [51](51-capacity-and-scheduling.md)
**Contracts:** consumes `cluster-profile.schema.yaml`, `provider.schema.yaml`, `bootstrap-journal.schema.yaml`; produces none
**ADRs:** relies on ADR-0005, ADR-0011, ADR-0154, ADR-0155, ADR-0156, ADR-0158

## 1. Purpose

Define how a running one-node installation becomes the three-node `small-ha` baseline without rebuilding: what `./bootstrap expand --profile small-ha` does, in which order, with which safety gates; how nodes are later added, removed or replaced after loss; what changes between profiles and what deliberately does not; and which drills prove the result (H04).

## 2. Requirements derived from SPEC.md

1. `small-ha` has 3 nodes (each ≥ 4 vCPU / 8 GiB / 100 GiB SSD, preferred 12 GiB / 200 GiB NVMe; SPEC v1.2, CD-25), 3 k3s servers with embedded etcd, CNPG 3 instances, Longhorn 2 replicas, up to 2 concurrent agents. [SPEC §12]
2. The one-node system is reduced redundancy, not a separate architecture; expansion does not rebuild Strange Company. [SPEC §12]
3. etcd grows 1 → node 2 → node 3 → 3-member quorum; critical k3s server configuration stays consistent. [SPEC §16]
4. Expansion increases CNPG instance count rather than migrating technologies; CNPG PGDATA stays node-local. [SPEC §25]
5. Longhorn goes from 1 to 2 replicas. [SPEC §27]
6. Loki and Tempo are added by or during small-ha. [SPEC §95, §119 PHASE 6]
7. `./bootstrap expand --profile small-ha` exists. [SPEC §8]
8. Procedures exist for add node, remove node, one-node to three-node expansion, lost node. [SPEC §113]
9. The one-node → three-node transition is defined before implementation. [SPEC §125]
10. Paid-resource guardrails apply to new nodes. [SPEC §14]

Derived requirements:

- **47-R-1** Expansion is a resumable pipeline with explicit human checkpoints (PR merges); every step is idempotent and journaled (plan 50).
- **47-R-2** The two-voting-member etcd window lasts ≤ 20 minutes and starts only after both new nodes are provisioned, baselined and firewall-open, with a fresh pre-join snapshot in S3.
- **47-R-3** Data-tier scaling (CNPG, Longhorn) happens only after the three-member control plane and all node-level networking are verified, in a separate PR from the platform-tier changes.
- **47-R-4** Expansion needs fresh backups: it refuses to start unless every CNPG cluster has a base backup < 26 h and WAL archiving healthy, Longhorn backups are < 26 h old, and an etcd snapshot is in S3 < 13 h.
- **47-R-5** Node names are never reused. A replacement node gets the next free `node-NN` name.
- **47-R-6** Steady-state server count is 1 or 3. Two servers exist only transiently (expansion, replacement).

## 3. Inputs

| Input | Source |
|---|---|
| Target profile `infra/profiles/small-ha.yaml` (`ClusterProfile`) | plan 03 |
| `EXPECTED_NODE_COUNT=3`, machine class, cost cap | `bootstrap.env` (plan 50 PF-09) |
| Provider output for node-02, node-03 | plan 03/04/05 |
| Journal state `transferred`, last verify result | plan 50 |
| Toolbox provenance (`pulled` or `built`, digest) | plan 50 journal / report (ADR-0013, ADR-0103) |
| Backup freshness signals | Prometheus (plans 12, 13, 07) |

## 4. Outputs

- Three Ready k3s servers with identical cluster files and a three-member etcd.
- Merged PRs: `bootstrap/expand-peers-<run-id>` (node IP variables), `bootstrap/expand-<run-id>-platform` (profile switch), `bootstrap/expand-<run-id>-data` (CNPG and Longhorn scaling).
- Journal pipeline `expand` with steps, `etcd_two_member_window_seconds`, evidence of every gate.
- `bootstrap-config.yaml` `cluster.profile: small-ha`.

## 5. Dependencies

**Hard (DAG):** H01 needs P12. H02 needs H01. H04 needs H02 and H03. Sequencing preference: after R02 (`dag.yaml` `after`).

**Assumed interfaces:**
- **Plan 50** owns the CLI and pipeline engine. This plan **amends** plan 50 §14.10's pipeline to: `preflight → plan → provision → peer-commit → configure → profile-commit (platform) → converge → profile-commit (data) → converge → verify`, followed later by the separate telemetry PR (§14.4 step 6; GC-06) (Q-47-01). CLI forms used here and in the procedures (plan 50 to add): `./bootstrap expand --profile small-ha [--confirm-plan D8]`, `./bootstrap node add <name>`, `./bootstrap node remove <name>`, `./bootstrap configure [--nodes n1,n2] [--tags …]`, `./bootstrap verify [--only …]`, `./bootstrap exec -- …`.
- **Plan 18:** `sc-gitops set-profile --cluster <c> --profile small-ha` edits the three profile lines; the small-ha profile adds layer `kured` (plan 08). The `loki`, `tempo` and `otel-agent` layers are **not** part of the profile switch: they come from a kustomize component `components/telemetry-h03` that only the telemetry PR enables, per plan 41's `telemetry-decide` outcome (GC-06). Plans 18, 41 and 50 align.
- **Plan 36:** `cluster-vars.SC_NODE_IP_1..5`.
- **Plans 12, 13:** `CNPG_INSTANCES`, `CNPG_ENABLE_PDB`, `CNPG_ANTI_AFFINITY`, `CNPG_SYNC_NUMBER`, component `cnpg-ha`; `LONGHORN_REPLICAS`, the Longhorn replica-sync Job, `nodeDrainPolicy`.
- **Plan 26/54:** the small-ha overlay of the `strange-company` component sets `concurrency.maxWorkers: 2` and `sharedHeavySlot: false` in the generated resource-profile policy, and the `agent-jobs` quota for W=2 (plan 51 §9.4).
- **Plan 20:** `CI_MAX_CONCURRENT=2` at small-ha.
- **Plan 15/02:** when the installation used a locally built toolbox image (ADR-0013 fallback), the in-cluster toolbox image (`platform-probes`, uid 10001) is imported into every node's containerd; expansion repeats this for new nodes (§14.3 step C6).

## 6. Owned state

None durable of its own. The journal (plan 50) records progress; Git records the result (cluster-vars, profile lines).

## 7. External interfaces

| Interface | Consumer | Definition |
|---|---|---|
| `./bootstrap expand --profile small-ha` | operator | §14 |
| `./bootstrap node add|remove <name>` | operator, procedures | §14.6, §14.7 |
| Profile delta | all component plans | §9.1 |
| Drill set and evidence | plan 45, H04 gate | §14.8 |

## 8. Internal interfaces

- Ansible `playbooks/expand.yml` (plans 07/08/09 roles with `serial` control): plays `baseline+firewall` on new nodes → `firewall` on all → `k3s` cluster-file roll on node-01 → `k3s-join` on node-02 → `k3s-join` on node-03 → `network` (CoreDNS replicas) → `verify`.
- Ansible `playbooks/node-remove.yml`: `k3s-leave` on the target → `firewall` on survivors → marker removal on the target.
- Expand journal step names (stable; plan 50 journal): `expand.preflight`, `expand.plan`, `expand.provision`, `expand.peer-commit`, `expand.configure.baseline`, `expand.configure.firewall`, `expand.configure.tls-san-roll`, `expand.configure.pre-join-snapshot`, `expand.configure.join-node-02`, `expand.configure.join-node-03`, `expand.configure.toolbox-import`, `expand.configure.network`, `expand.profile-commit.platform`, `expand.converge.platform`, `expand.profile-commit.data`, `expand.converge.data`, `expand.verify`. The telemetry PR (`bootstrap/expand-<run-id>-telemetry`) is plan 41's step after its measurement window, outside this pipeline.

## 9. Configuration

### 9.1 Profile delta (self-hosting-minimum → small-ha)

| Item | self-hosting-minimum | small-ha | Mechanism |
|---|---|---|---|
| k3s servers / etcd members | 1 | 3 | Ansible join (plan 07) |
| Firewall peer sets | node-01 | 3 nodes (internal + external IPs) | plan 08 |
| NetworkPolicy node IPs | `SC_NODE_IP_1` (others repeat it) | 3 distinct | peer-commit PR |
| CoreDNS | 1 replica | 2, required anti-affinity, PDB `minAvailable: 1` | Ansible (plan 09) |
| Traefik | 1 (tunnel) / DaemonSet | 2 + PDB (tunnel) / DaemonSet on 3 nodes | small-ha overlay |
| cloudflared | 1 | 2, preferred anti-affinity, PDB | overlay |
| cert-manager controller / webhook | 1 / 1 | 2 / 2, PDB | overlay (plan 10) |
| CNPG operator, barman plugin | 1 | 2 | overlay (plan 12) |
| CNPG clusters (4) | 1 instance | 3 instances, required anti-affinity, PDB, synchronous `ANY 1` (`dataDurability: preferred`) | data PR (plan 12) |
| Longhorn | replicas 1; CSI sidecars 1 | replicas 2 (new and existing volumes); CSI sidecars 2; `nodeDrainPolicy: block-if-contains-last-replica` | data PR (plan 13) |
| Forgejo, Hermes | 1 replica, Longhorn RWO, `Recreate` | unchanged (failover = reschedule + reattach, ≤ 10 min) | — |
| Authentik server / worker | 1 / 1 | 2 (PDB) / 1 | overlay (plan 15) |
| Woodpecker server / agents / `sc-ci-config` | 1 / 1+1 / 1 | 1 / 1+1 / 2 | overlay (plan 20) |
| `CI_MAX_CONCURRENT` | 1 | 2 | cluster-vars (plan 20) |
| Prometheus / Alertmanager | 1 / 1 | 2 / 2 | overlay (plan 38) |
| Grafana, OTel Collector | 1 | 1 / per plan 40 | — |
| Loki, Tempo, otel-agent | absent | present only if plan 41's `telemetry-decide` says `deploy` (or Tempo only for `tempo-only`), after H02 | telemetry PR enabling `components/telemetry-h03` (GC-06) |
| Agent Runner | per plan 26 (leader-elected) | per plan 26 | — |
| Model Gateway, egress-proxy | 2 | 2, topology spread | overlay |
| Flux controllers | 1 each | 1 each (leader-elected singletons) | — |
| kured | absent | DaemonSet, Sun 04:00–06:00 UTC, 1 node at a time (plan 08 §19) | profile layer `kured` |
| `sc-maintenance` | reboot + monthly k3s restart | monthly k3s restart only, staggered by node | Ansible (plan 08) |
| Heavy-slot mode | exclusive (capacity 8) | shared (capacity 64) | capacity overlay (plan 51) |
| `maxWorkers` | 1 | 2 | strange-company overlay |
| `agent-jobs` quota | W=1 | W=2 | namespaces overlay (plan 51 §9.4) |
| Alerts `SCEtcdMemberMissing`, `SCWireGuardPeerStale`, `SCHADrillOverdue` | inactive | active | `profiles:` in the alert catalogue (plan 42) |

### 9.2 What does NOT change

- The architecture: same namespaces, components, charts, versions, admission policies and NetworkPolicy rules (only the node IP variables change).
- Data technology and location: CNPG stays on `local-path`; Longhorn stays V1; Forgejo repositories stay on the same Longhorn volume (no migration).
- Identity and secrets: no secret is rotated; the k3s token, age keys, CA and every credential stay the same.
- Backup destinations and generations: CNPG `CNPG_<ID>_GEN` unchanged; new replicas do not archive (only the primary does).
- Flux source and repository; public hostnames, certificate, ingress mode.
- Resource-profile numbers (`standard` set). Only concurrency and slot mode change.
- No application downtime is planned. The only interruptions are one k3s restart on node-01 (API unavailable ≈ 30–60 s; pods keep running) and rolling restarts of components whose replica count changes.

### 9.3 Knobs

| Knob | Git path | Default |
|---|---|---|
| `expand.joinTimeout` | `infra/bootstrap/settings.yaml` | 600 s per joiner |
| `expand.twoMemberWindowMax` | same | 1200 s (abort threshold for the second join) |
| `expand.convergeTimeout.platform` / `.data` | same | 30 min / 90 min |
| `expand.backupFreshness` | same | CNPG base < 26 h, Longhorn < 26 h, etcd S3 < 13 h |

## 10. Secrets

Consumed only: `SC-INFRA-PROVIDER` (provision), `SC-SSH-OPERATOR` (Ansible), `SC-K3S-TOKEN` (join, from the bundle), `SC-FORGEJO-ADMIN` (bootstrap PRs, plan 50), `SC-DNS-API` (direct-mode DNS records). No new secret class.

## 11. Storage

- New nodes get the same baseline layout (plan 08 §9.6), including the `ci-workspace` filesystem at 24 GiB (small-ha default; node-01's 12 GiB is grown to 24 GiB by the same configure run).
- CNPG replicas: each new instance clones from the primary via streaming `pg_basebackup` onto the new node's `local-path` (plan 12). Size ≈ database size (< 2 GiB each at threshold scale).
- Longhorn: each existing volume gains a replica on another node (`replicaAutoBalance: best-effort` spreads them, plan 13).
- Per-node disk budget: plan 11 §12's **small-ha per-node table** (STO-4; plan 11 adds it: worst-case node = all four CNPG primaries + one `forgejo-data` replica (50Gi at small-ha) + Loki/Tempo WAL + `ci-workspace` 24 GiB + slot-retained WAL), and the minimum disk it implies (≈ 180 GiB at 10 GiB of Git data and 10 GiB of PGDATA, so the SPEC v1.2 preferred **200 GiB** is the small-ha default; the 100 GiB minimum holds only a small data ceiling). Expansion preflight refuses nodes below 100 GiB (`NODE_DISK_TOO_SMALL`) and warns (`NODE_DISK_BELOW_PROFILE_BUDGET`) when the plan 11 §12.2 projection for the current `G` and `P` exceeds 70 % of the disk.

## 12. Resource requirements

Three nodes of the same shape (plan 03 refuses mixed shapes; plan 07 `K3S_MIXED_SIZE_CLASS`). Per-node memory budget: plan 51 §12.3. Transient during converge: CNPG base backups and Longhorn rebuilds add CPU and disk I/O for up to ≈ 30 min; workers are paused during `converge.data` (§14.4) so they cannot compete.

## 13. Security boundary

- New nodes receive the same hardening before joining (baseline, firewall, install-uid marker). A node that fails baseline verification is never joined.
- The k3s join uses the full token with the CA hash (`K10<sha256>::server:<secret>`, plan 07 §8), so a joiner cannot be pointed at an impostor server.
- Bootstrap PRs are opened by the Forgejo admin identity (plan 50 50-R-5) and require human merge; branch protection applies.
- Workers are paused (`runner.dispatchPaused: true`) during the configure and converge steps: no autonomous code runs while the control plane changes.

## 14. Happy path

### 14.1 Preflight (`expand.preflight`)

Refuses (exit 3) unless:
1. installation state is `transferred` and the last `./bootstrap verify` passed within 24 h with the same input digest;
2. 47-R-4 backup freshness holds (Prometheus queries: `time() - sc:cnpg_last_backup_success_timestamp_seconds` < 93 600 for all four clusters and `cnpg_collector_pg_wal_archive_status{value="failed"}` not increasing; Longhorn `SCLonghornBackupStale` not firing; `time() - sc:etcd_last_s3_snapshot_timestamp_seconds < 46 800`);
3. no critical alert is firing (Alertmanager API);
4. `EXPECTED_NODE_COUNT=3`, target profile is `small-ha`, provider class equals node-01's shape;
5. node-01 root filesystem has ≥ 30 % free (Longhorn and CNPG need headroom for rebuild bookkeeping);
6. the operator confirmed the plan digest (`expand <cluster> <D8>`).

### 14.2 Provision and peer-commit

1. `expand.plan` / `expand.provision`: provider plan and creation of node-02 and node-03 with paid-resource guardrails (plans 03/04). Output is normalized node records.
2. `expand.peer-commit`: branch `bootstrap/expand-peers-<run-id>` sets `SC_NODE_IP_2` and `SC_NODE_IP_3` to the new internal IPs in `cluster-vars` (slots 4–5 keep node-01's IP). PR opened, exit 75 until merged and the `policies` layer is Ready at the merge SHA. This must precede the joins: once a server joins, the `kubernetes` Service endpoints include its IP, and pods whose NetworkPolicy does not allow that IP would fail API calls intermittently (plan 36 §5).

### 14.3 Configure (`playbooks/expand.yml`)

- **C1** `baseline` + `firewall` on node-02/03 (install-uid marker written first; preflight per plan 08, including fio). Fail → stop; nothing in the cluster has changed yet.
- **C2** `firewall` on all three nodes (peer sets now contain all three). Verify from each node: `nc -z -w 3 <peer> 6443` succeeds for every peer pair once k3s runs; at this point verify UDP 51820 reachability is deferred to C5.
- **C3** Runner dispatch paused: PR-free toggle through the Runner admin API (`POST /admin/dispatch/pause`, plan 26; journal records it) and wait until no worker execution is `RUNNING` (≤ the longest profile deadline; the operator may cancel instead).
- **C4** Roll the cluster file with the final three-server `tls-san` list to node-01 (k3s restart; ≤ 300 s to Ready). `sc-etcdctl endpoint health` healthy.
- **C5** `k3s etcd-snapshot save --name pre-join`; confirm the S3 object within 120 s. Join node-02 (≤ 600 s: node Ready, 2 started voting members, endpoint health all healthy). **Immediately** join node-03 (same checks, 3 members). Record `etcd_two_member_window_seconds`; if the second join has not completed when the window reaches `twoMemberWindowMax`, stop and follow `lost-etcd-member.md` §C. Verify WireGuard: `wg show flannel-wg peers` lists 2 peers on every node with a handshake < 3 min.
- **C6** Toolbox image import (coordinator convention): if the journal records `toolbox.source = built`, export the exact image from the operator's engine (`docker|podman save <digest>`) and import it on node-02/03 (`k3s ctr -n k8s.io images import`), then confirm `k3s crictl images --digests` lists the recorded digest. Pulled toolboxes need nothing (nodes pull by digest).
- **C7** `network`: CoreDNS replicas 2 with required anti-affinity and PDB; label servers `strange.company/node-role=server`.
- **C8** Longhorn on new nodes: DaemonSets start automatically; wait for `nodes.longhorn.io` of node-02/03 `Ready=True` and `allowScheduling=true` (≤ 10 min).
- **C9** Grow node-01's `ci-workspace` filesystem to 24 GiB (plan 08, online `resize2fs`).

### 14.4 Profile commits and converge

1. **Platform PR** (`bootstrap/expand-<run-id>-platform`): `sc-gitops set-profile --cluster <c> --profile small-ha`; `bootstrap-config.yaml` `profile: small-ha`; `cluster-vars`: `NODE_COUNT=3`, `CI_MAX_CONCURRENT=2`; **but** `CNPG_INSTANCES=1`, `LONGHORN_REPLICAS=1` kept explicitly (data tier unchanged in this PR). Exit 75 until merged.
2. **converge.platform** (≤ 30 min): every Kustomization and HelmRelease Ready at the merge SHA; replica counts per §9.1 reached; heavy-slot capacity 64 on all nodes (plan 51); kured DaemonSet ready. Loki/Tempo are **not** checked here (GC-06). Direct mode: when Traefik is Ready on all three nodes, `sc-bootstrap dns apply` adds node-02/03 A records.
3. **Data PR** (`bootstrap/expand-<run-id>-data`): `CNPG_INSTANCES=3`, `CNPG_ENABLE_PDB=true`, `CNPG_ANTI_AFFINITY=required`, `CNPG_SYNC_NUMBER=1` (component `cnpg-ha`), `LONGHORN_REPLICAS=2`. Exit 75 until merged.
4. **converge.data** (≤ 90 min), checked every 30 s:
   - each CNPG cluster: 3 instances Ready on 3 distinct nodes, 2 streaming replicas, `Synchronous standby names` contains `ANY 1` (plan 12 gate);
   - each Longhorn volume with a `strange.company/state-id` label: `spec.numberOfReplicas = 2`, robustness `healthy`, replicas on 2 distinct nodes (plan 13 sync Job succeeded);
   - no `SCCnpg*` or `SCLonghorn*` alert firing.
   Order inside Flux is not forced across the four CNPG clusters; each clones independently (databases are small). Longhorn rebuild runs concurrently with CNPG cloning; Longhorn throttles with `concurrentReplicaRebuildPerNodeLimit` (plan 13).
5. Resume Runner dispatch (journal records it). The small-ha overlay's `maxWorkers: 2` is now in effect.
6. **Telemetry PR (H03, plan 41):** after converge.data and plan 41's measurement window (≥ 3 days of small-ha Prometheus data), plan 41's `telemetry-decide` opens `bootstrap/expand-<run-id>-telemetry` enabling `components/telemetry-h03` for `deploy` or `tempo-only`, or records `defer` (no PR). It is gated by GATE-H03, not by this pipeline.

### 14.5 Verify

`./bootstrap verify` with small-ha expectations: V-10 (3 nodes, etcd members 3), V-11 (external scan per plan 08 matrix, 3 nodes), V-18 (CNPG 3 instances), V-19 (Longhorn replicas 2), V-30 per node in shared mode (plan 51 §12.3), plus the H01/H02 gate checks in §23. The run ends with `EXPAND COMPLETE small-ha` and the journal state returns to `transferred`.

### 14.6 Add node (replacement after loss or removal)

`./bootstrap node add node-0N` (N = next unused number, 47-R-5): preflight (quorum currently healthy with 2 members, or 3 members when adding a 4th is attempted → refused, steady state is 3), provision (if paid), peer-commit (replace the departed node's IP slot), configure C1–C2, C4 (tls-san roll on the two survivors one at a time), C5 join (single join), C6, C7–C8, then Flux needs no change (replica counts are fixed by profile; CNPG and Longhorn reschedule their missing instance/replica onto the new node automatically), verify. Procedure: `procedures/add-node.md`.

### 14.7 Remove node

`./bootstrap node remove <name>`: refused unless 3 servers are healthy (removing from 1 is destroy, plan 49; from 2 it would leave 1 member and is only allowed with `--allow-two-to-one` during an aborted expansion). Steps: pre-remove snapshot; direct mode: remove the node's A records first and wait 2 × TTL; cordon; Longhorn: `allowScheduling=false`, `evictionRequested=true` on the node and wait until no replica remains there (other nodes have room, plan 13); `kubectl drain --ignore-daemonsets --delete-emptydir-data --timeout=15m` (PDBs respected; CNPG switchover happens first if the primary is there); `kubectl delete node`; confirm `sc-etcdctl member list` shows 2 members, removing the member explicitly if k3s did not (plan 07 Q-07-03); on the node: stop and disable k3s, run the pinned `k3s-killall.sh`, remove `/var/lib/rancher/k3s`, `/etc/rancher/k3s`, `/var/lib/longhorn`, the install-uid marker; firewall on survivors (peer sets without the node); peer-commit PR removing its IP; provider deletion (plan 03, guarded). Two servers remain: this is a transient state that `add-node.md` ends. Procedure: `procedures/remove-node.md`.

### 14.8 Lost node

Detection `SCNodeDown` (5 min). Decision: transient if the node returns within 30 minutes or the provider reports maintenance; otherwise permanent. Permanent: `kubectl delete node` (pods on it are force-removed; Longhorn and CNPG rebuild elsewhere within their own policies), remove its etcd member (`lost-etcd-member.md`), then `add-node.md`. Single-node installations: a lost node is total cluster loss (plan 45) unless the disk survives (then `restore-etcd.md`). Procedure: `procedures/lost-node.md`.

### 14.9 H04 drills (with plan 45)

Each drill has an automated runner (`./bootstrap exec -- sc-bootstrap drill <id>`, plan 50 to add) that injects the fault, measures, restores, and writes `s3://<prefix>-evidence/ha-drills/<UTC ts>-<id>.json`. On success of the full set it patches ConfigMap `monitoring/ha-drill-status` `data.lastSuccess: <unix>`.

| ID (prefix `HA`, distinct from plan 45's `RD` black-start drills; DR-21) | Fault | Pass criteria |
|---|---|---|
| HA1 | kured reboot of each node in turn (window forced) | no PDB violation; `probe_success` for `git.`, `auth.`, `ci.` ≥ 95 % over the drill; all pods Ready within 10 min after each reboot |
| HA2 | hard power-off (provider API) of the node hosting `sc-pg` and `forgejo-pg` primaries and Forgejo | new primaries within 60 s; writes through `*-rw` succeed within 90 s; Forgejo `api/healthz` 200 within 10 min (Longhorn reattach; Multi-Attach recovery per plan 13 L-5); API writes continue (quorum 2/3) |
| HA3 | stop k3s on one server for 15 min | `SCEtcdMemberMissing` fires within 5 min and resolves within 10 min of restart; `kubectl create configmap` succeeds throughout |
| HA4 | remove-node then add-node (replacement with a new name) | `remove-node.md` and `add-node.md` complete with all verify checks green; etcd 3 members; Longhorn and CNPG back to profile counts |
| HA5 | network partition of one node (nftables drop of peer traffic on that node for 10 min) | majority side keeps serving; `SCWireGuardPeerStale` fires; after heal, no split-brain (single CNPG primary per cluster; etcd member list consistent) |
| HA6 | delete one Longhorn replica of `forgejo-data` | rebuild to 2 healthy replicas within 30 min |
| HA7 | V-30 per node under one worker + one CI workflow concurrently on different nodes | plan 51 §14.2 pass |
| HA8 | `restore-etcd.md` §C on the lab three-VM cluster (never on production) | lab evidence of 07-AC-23 within the last 100 days |

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Backups stale at start | preflight | expansion refused | exit 3 | fix backups (`object-storage-outage.md`, plan 12/13) |
| Provisioning fails / cost guard | plan 03 | no new nodes | exit 4 or 1 | rerun |
| Peer PR never merged | exit 75 | waits | none | merge or abort (§20) |
| New node fails baseline | C1 | not joined | stop | fix, rerun (resumes at C1) |
| node-01 does not come back after tls-san roll | C4 timeout | single-node API outage | stop | `restore-etcd.md` §A if etcd damaged; usually a config error → revert render |
| Joiner fails in the 2-member window | C5 timeout / window exceeded | quorum at risk | stop; journal `JOIN_ABORTED` | `lost-etcd-member.md` §C |
| CNPG replica cannot clone (disk, network) | converge.data timeout; `SCCnpgClusterDegraded` | redundancy not reached | stop | `cnpg-replica-loss.md` (plan 12) |
| Longhorn rebuild stuck | converge.data timeout; `SCLonghornVolumeDegraded` | volume at 1 replica | stop | `longhorn-replica-loss.md` (plan 13) |
| Volume below profile replicas later | `SCLonghornReplicaCountBelowProfile` | reduced redundancy | none | plan 13 sync Job rerun |
| Drills overdue | `SCHADrillOverdue` | unproven HA | none | run the drill set |

## 16. Retry behavior

Every step is resumable from the journal. Automatic retries: provider API calls (plan 03), Ansible package installs (plan 08). k3s joins and PR merges are never retried automatically (quorum risk; human checkpoint).

## 17. Idempotency behavior

- Peer-commit and profile PRs are detected by branch name and content; a rerun reuses an open PR or skips a merged one.
- Join steps are no-ops when the node is already a started voting member with matching peer URL (plan 07 §17).
- `converge.*` steps are pure checks.
- `node add` on a node already Ready and in the member list exits 0 after verify.

## 18. Bootstrap behavior

Fresh installs always start as `self-hosting-minimum` and then expand (plan 50 non-goal: direct small-ha install). Black start of a small-ha installation (plan 45) restores to one node first and re-runs expand.

## 19. Upgrade behavior

Upgrades are not combined with expansion: `expand.preflight` refuses while any plan 48 upgrade PR is open or a k3s upgrade run is unfinished, and plan 48 refuses upgrades during expansion (journal state `expanding`).

## 20. Recovery behavior (abort and rollback)

| Abort point | Rollback |
|---|---|
| Before C5 | revert the peer PR; destroy new nodes (plan 03, guarded); resume Runner dispatch |
| During C5 (2 members) | `lost-etcd-member.md` §C: remove the failed joiner's member while quorum holds, or `cluster-reset` on node-01 if quorum is already lost; then either retry the join or roll back as above |
| After C5, before the platform PR | `remove-node.md` for node-03 then node-02 (`--allow-two-to-one`), revert the peer PR |
| After the platform PR, before the data PR | revert the platform PR (Flux scales components back), then as above |
| After the data PR | supported only by a reverse data PR (`CNPG_INSTANCES=1`, `LONGHORN_REPLICAS=1`), which plans 12 and 13 must verify; scale-down to one node is not an MVP-supported operation (§24) |

## 21. Observability

- Journal step durations and `etcd_two_member_window_seconds` in the expand report.
- `platform-probes` module `ha` (this plan owns the module; plan 15 hosts the component) reads ConfigMap `monitoring/ha-drill-status` and exports `sc_ha_drill_last_success_timestamp_seconds`.
- Recording rule `sc:longhorn_expected_replicas` as defined by plan 42 (`vector(<LONGHORN_REPLICAS>)` from `cluster-vars`).
- **Alerts introduced (plan 42 to register):**

| alertname | Expression sketch | for | Severity / class | Runbook |
|---|---|---|---|---|
| `SCLonghornReplicaCountBelowProfile` | `min(sc_longhorn_volume_number_of_replicas{state_id!=""}) < on() sc:longhorn_expected_replicas` (catalogue expression; metric from plan 38's KSM custom-resource state on `volumes.longhorn.io` `spec.numberOfReplicas`) | 30m | warning / notify (small-ha) | `procedures/longhorn-replica-loss.md` (plan 13) |
| `SCHADrillOverdue` | `time() - sc_ha_drill_last_success_timestamp_seconds > 100*86400` (small-ha) | 1h | warning / notify | `expand-to-small-ha.md` §Drills |

Existing alerts activated at small-ha: `SCEtcdMemberMissing` (07), `SCWireGuardPeerStale` (09), CNPG sync alerts (12).

## 22. Testing strategy

- **Static (F11):** render `ci-sha-direct` (small-ha fixture) and assert every §9.1 row; the platform/data PR split is tested by rendering an intermediate fixture (`small-ha` + `CNPG_INSTANCES=1`, `LONGHORN_REPLICAS=1`).
- **Lab (plan 05 VMs, `make e2e-lab-expand`):** install one VM, run `./bootstrap expand` against two more VMs with the SeaweedFS `s3-emulator` as S3 (CD-12); assert §23 H01/H02 items; crash injection at each journal step (plan 50 `SC_TEST_CRASH_AT`) and resume; abort rollbacks for "before C5" and "after C5"; D1–D6 drills on the lab.
- **Live (H01/H02/H04):** gates below. No paid model calls.

## 23. Acceptance criteria

### GATE-H01

- **47-AC-1** `./bootstrap status --json | jq '.pipelines.expand.steps | map(select(.status!="succeeded")) | length'` = 0 and the steps appear in the order of §8 (journal timestamps monotonic).
- **47-AC-2** `kubectl get nodes -l strange.company/node-role=server -o name | wc -l` = 3, all Ready (plan 07 07-AC-19 covers etcd membership and config hash).
- **47-AC-3** On each node: `sudo nft list set inet sc_host sc_peers_v4` lists the internal and external IPs of all three nodes; `wg show flannel-wg latest-handshakes` shows 2 peers with handshakes < 180 s.
- **47-AC-4** `kubectl -n flux-system get cm cluster-vars -o json | jq -r '.data | [.SC_NODE_IP_1,.SC_NODE_IP_2,.SC_NODE_IP_3] | unique | length'` = 3, and the merge commit of the peer PR precedes the `join-node-02` step timestamp.
- **47-AC-5** `kubectl -n kube-system get deploy coredns -o jsonpath='{.status.readyReplicas}'` = 2 with pods on distinct nodes; `kubectl -n kube-system get pdb coredns` exists.
- **47-AC-6** If the journal records `toolbox.source=built`: on node-02 and node-03, `sudo k3s crictl images --digests | grep -c <toolbox digest>` ≥ 1, and a one-shot pod `toolbox-import-check` (toolbox image by digest, `imagePullPolicy: Never`, `nodeName: <node>`, command `true`) created on each of node-02 and node-03 reaches `Succeeded` (GC-22: one deterministic predicate). If `toolbox.source=pulled`, the same pod with `imagePullPolicy: IfNotPresent` reaches `Succeeded`.
- **47-AC-7** `etcd_two_member_window_seconds` in the expand report ≤ 1200.
- **47-AC-8** `kubectl get node -o jsonpath='{.items[*].status.capacity.strange\.company/heavy-slot}'` = `64 64 64` after converge.platform, and `kubectl -n kube-system get ds kured -o jsonpath='{.status.numberReady}'` = 3 with the kured flags of plan 08 §19 visible in the pod args (re-verifies kured flag names).

### GATE-H02

- **47-AC-9** The data PR merge commit is later than converge.platform's success timestamp, and in the platform PR's diff `CNPG_INSTANCES` and `LONGHORN_REPLICAS` are both `1` (47-R-3).
- **47-AC-10** For every CNPG cluster, `kubectl cnpg status <c> -n <ns>` shows 3 instances on 3 distinct nodes (plan 12 12-AC-15 covers sync and failover).
- **47-AC-11** `kubectl -n longhorn-system get volumes.longhorn.io -l strange.company/state-id -o json | jq '[.items[] | select(.spec.numberOfReplicas != 2 or .status.robustness != "healthy")] | length'` = 0, and each volume's replicas (`replicas.longhorn.io` by `longhornvolume` label) are on 2 distinct nodes.
- **47-AC-12** Two fake-harness workers dispatched together are both `RUNNING` at the same time (Runner `agent_status`), on nodes whose combined memory requests fit (scheduler placed them); a third waits `QUEUED`.
- **47-AC-17** After converge.data, on every node `df --output=pcent /` ≤ 60 % and `df --output=pcent /var/lib/sc/ci-workspace` reports a 24 GiB filesystem (STO-4; the per-node budget line of plan 11 §12 is the reference).
- **47-AC-13** `kubectl get pdb -A -o json | jq -r '.items[].metadata.name'` includes the PDBs listed in §9.1 (coredns, traefik (tunnel), cloudflared, cert-manager, cert-manager-webhook, authentik-server, model-gateway, egress-proxy, CNPG primary/replica PDBs).

### GATE-H04

- **47-AC-14** Drill evidence objects HA1–HA7 exist in `s3://<prefix>-evidence/ha-drills/` from one drill campaign, each with `result: pass`; HA8 lab evidence ≤ 100 days old (same window as `SCHADrillOverdue`; DR-21); `sc_ha_drill_last_success_timestamp_seconds` is less than 1 day old after the campaign.
- **47-AC-15** During HA2, the recorded CNPG failover time ≤ 60 s for both clusters and Forgejo recovery ≤ 600 s.
- **47-AC-16** `SCHADrillOverdue` and `SCLonghornReplicaCountBelowProfile` are loaded (`kubectl get prometheusrule -A -o json | jq` lists both) and a promtool unit test fixture for each passes.

## 24. Explicit non-goals

- Fresh installs directly into small-ha; agent-only (non-server) nodes; more than 3 servers in MVP (5 is a later option).
- Scaling small-ha back down to one node (recover from backups onto one node instead, plan 45).
- Multi-zone or multi-provider placement; mixed node shapes.
- Making Forgejo, Hermes, Grafana, Woodpecker server or Flux multi-replica (they fail over by rescheduling).

## 25. Open questions

- **Q-47-01** (blocking: no) Plan 50 §14.10 defines the expand pipeline as `preflight → plan → provision → configure → profile-commit → verify` with one PR. This plan needs `peer-commit` before configure (node IPs in NetworkPolicy before joins), two profile PRs (platform, then data) with converge steps, and the Runner pause/resume. *Proposed:* plan 50 adopts §14 verbatim. *Owner:* 50.
- **Q-47-02** (blocking: no) CLI subcommands `./bootstrap node add|remove`, `./bootstrap configure`, `sc-bootstrap drill <id>` and the Runner admin `dispatch pause/resume` endpoint. *Owner:* 50/26.
- **Q-47-03** (blocking: no) Expansion pauses workers for the whole run (possibly hours with PR waits). *Proposed:* accept; expansion is a rare, planned operation. *Owner:* 47.
- **Q-47-04** (blocking: no) Reverse data scaling (3→1 CNPG, 2→1 Longhorn) is untested in plans 12/13. *Proposed:* plans 12/13 add a lab test or declare it unsupported, in which case abort after the data PR means "restore onto one node" (plan 45). *Owner:* 12/13.
