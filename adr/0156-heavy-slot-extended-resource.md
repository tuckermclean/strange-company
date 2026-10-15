# ADR-0156: Heavy execution is serialized by the node extended resource `strange.company/heavy-slot`; exclusive by default on one node

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 51 (refines ADR-0020)

## Context
ADR-0020 makes worker and heavy CI execution mutually exclusive on small nodes, and plan 51's budget shows that on the preferred 12 GiB node a 3 GiB worker and a 3 GiB CI workflow cannot run together beside the platform. Three mechanisms were proposed in parallel: a Lease `strange-company/heavy-execution` (plans 12/43/44), a per-pod `strange.company/ci-slot` (plan 20) and a heavy-slot resource (G2 brief). A Lease is advisory (a pod that does not ask is not stopped; a crashed holder keeps it until expiry). A one-unit-per-pod resource with capacity 1 deadlocks a Woodpecker workflow whose service pod and step pod run at the same time.

## Decision
- **One mechanism:** the node extended resource `strange.company/heavy-slot`, advertised on every node by CronJob `kube-system/heavy-slot-advertiser` (every 5 minutes, status-subresource patch, value from ConfigMap `heavy-slot-config`).
- **Units:** exclusive mode advertises 8 units per node; shared mode 64.
  - Worker pods request 8 (all units), set by the Runner and required by a VAP.
  - ci-lane pods (`ci-jobs`) and Renovate request 1 per container through LimitRange defaults.
  - Heavy restore tests (`dr-tests`) request all 8 units explicitly on the Job pod, which holds them while its scratch CNPG or Longhorn objects run; light checks request none (CD-16, review STO-23).
  - The Runner creates a worker Job only when a node has 8 free units, so a worker never sits Pending behind CI (review RA-7).
  - Build-lane pods (`ci-build`) request 8, so that a release build never overlaps a ci-lane workflow (SPEC §89 "maximum heavy CI jobs: 1"); build-lane workflows must therefore use sequential steps without services.
- **Modes:** self-hosting-minimum is exclusive at any node size until a node's own V-30 measurement passes with worker and CI together; small-ha is shared.
- The Runner also gates dispatch in exclusive mode (no CI or batch pod holding units, no queued Woodpecker workflow) to avoid long Pending periods; the scheduler is the enforcement.
- V-30's heavy term becomes `max(worker, ci, build)` in exclusive mode and `worker + ci` in shared mode.

## Consequences
- Enforcement by the scheduler with no client code; units are released when pods end.
- Restore tests and Renovate wait for workers (and vice versa) on one node; all use `preemptionPolicy: Never` classes, so nobody is killed to get a slot.
- Plans 12, 20, 26, 37, 43 and 44 align to this mechanism at consolidation.

## Alternatives rejected
- Lease-based mutex: advisory, needs client code in every participant, stale holders.
- Per-pod CI slot with capacity = concurrency: deadlocks workflows with services.
- Kueue or another batch scheduler: another controller for a two-party exclusion.
- Memory requests alone: they cannot express "not at the same time" when both fit on paper but not in measured working set.

## Verification
F12: 51-AC-2 (k3d unit-exclusion test). P12: 51-AC-6, 51-AC-7. C02: 51-AC-11 (worker waits for a running pipeline and vice versa).
