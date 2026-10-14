# ADR-0158: When to restore an etcd snapshot and when to rebuild from Git

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 07

## Context
SPEC §110 asks Stage 0 to define when to restore etcd versus rebuild from source. Git is the canonical desired state (SPEC §4.2), so a cluster can always be rebuilt and re-seeded (plan 45). But etcd also holds state Git cannot recreate cheaply: PV/PVC bindings to node-local CNPG data and Longhorn volumes, Longhorn and CNPG custom-resource status, Job objects the Runner tracks. Restoring a snapshot onto machines whose disks no longer match it resurrects references to data that does not exist.

## Decision
1. **Quorum intact** (≥ 2 of 3, or 1 of 1 running): never restore. Replace missing members (`lost-etcd-member.md`).
2. **Quorum lost but one member's data intact** (1 of 3 survivors): `k3s server --cluster-reset` on the survivor **without** a snapshot (keeps the newest data), then rejoin the others (`restore-etcd.md` §B).
3. **No surviving etcd data, node data disks intact** (local-path PGDATA and `/var/lib/longhorn` present), the k3s token available, and a snapshot ≤ 24 h old: **snapshot restore** (`restore-etcd.md` §A single node, §C three nodes) of the newest snapshot that predates the failure. Recovery is minutes, with no data restores.
4. **Node disks lost, or all nodes replaced, or no usable snapshot/token:** **rebuild from Git** (plan 45 black start), restoring data from CNPG, Longhorn and repository backups.
5. **Logical damage** (bad change, deleted objects): fix by Git revert and Flux reconcile. Snapshot restore only if the damage destroyed controller-generated state Git cannot recreate **and** a pre-damage snapshot < 6 h old exists; the operator accepts losing every API write since the snapshot.
6. A restore always uses the token current when the snapshot was taken (`k3s_token_previous` is kept in the bundle for 30 days after rotation).

## Consequences
- Snapshots are an accelerator, not a backup of record; their absence never blocks recovery.
- Operators get a deterministic decision tree instead of a judgment call during an outage.

## Alternatives rejected
- Always rebuild: correct but slow (hours of data restores) when disks are intact.
- Always restore: dangerous on replaced hardware (dangling PVs, contradictory Longhorn/CNPG state).
- Velero-style resource backups: duplicates Git for desired state and cannot capture node-local data anyway.

## Verification
Lab (F10/H01 evidence): 07-AC-23 (three-node snapshot restore drill); H04 drill HA8 (plan 47) keeps that evidence ≤ 100 days old (the `SCHADrillOverdue` window; review DR-21).
