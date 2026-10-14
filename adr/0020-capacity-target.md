# ADR-0020: Default capacity target is the SPEC "preferred" node; 8 GiB is reduced-profile and gated by measurement

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §11 sets the self-hosting-minimum node at a minimum of 4 vCPU / 8 GiB / 75 GiB SSD and a preference of 4 vCPU / 12 GiB / 100 GiB NVMe. A Stage-0 estimate of the Self-Hosting Threshold platform's steady-state memory comes to roughly 7–9 GiB *before* any worker or CI job: k3s/etcd, Longhorn, four PostgreSQL instances, Authentik, Forgejo, Woodpecker, the Prometheus stack, Grafana, the OTel Collector, Hermes, the Runner and the Gateway. SPEC §83's example implementation profile (4 GiB) plus one CI job (about 2 GiB) does not fit in 8 GiB. See `plans/spec-issues.md` SI-01.

## Decision
- Providers default to the **preferred** shape (OVH: a VPS class with ≥12 GiB and NVMe). Providers MAY choose larger (SPEC §11).
- 8 GiB nodes are accepted only with the `constrained` resource-profile set (plan 51). On those nodes, worker and heavy CI execution are **mutually exclusive** (a shared `heavy-execution` concurrency slot), and the implementation profile's memory limit is reduced. Preflight warns explicitly.
- Gate P12 includes a **measured memory budget**: platform steady-state working set ≤ (allocatable − largest permitted worker − one CI job − 10% headroom) on the chosen node. If the measurement fails on 8 GiB, the 8 GiB minimum is formally declared unsupported through a SPEC amendment proposal. It is not silently worked around.

## Consequences
SPEC's minimum is not contradicted, because profiles are tunable policy (SPEC §83). Its feasibility is made an explicit, measured gate instead of an assumption.

## Alternatives rejected
- Silently raising the minimum to 12 GiB: that changes SPEC without saying so (SPEC §0).
- Dropping platform components on small nodes: the Self-Hosting Threshold requires all of them (SPEC §117).

## Verification
P12 gate: the memory-budget measurement is recorded in the bootstrap verification report for the node size actually used.

## Amendments (operator decisions, 2026-10-14)
- **CD-23.** 8 GiB nodes remain allowed as the `constrained` configuration. There is no SPEC memory change. The P12 measured-budget gate (V-30) decides per node; if it fails, the operator moves to a larger class.
- **CD-24.** The OVH default stays **VPS-2 (12 GB / 100 GB NVMe)**. VPS-3 (24 GB / 200 GB) is the documented upgrade path. This supersedes the "providers default to the preferred shape" sentence above for disk, because VPS-2 meets the disk *minimum* but not the new *preferred* 200 GiB.
- **CD-25.** SPEC amended to **v1.2**: node disk minimum 100Gi and preferred 200Gi for both profiles (SI-32).
