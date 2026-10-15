# ADR-0452: The emergency stop has three independent sources; the effective level is their maximum; the Kubernetes source lives outside Flux

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 33 (runtime: 53, 26)

## Context
SPEC §81 requires "a way to stop new autonomous spending without shutting down core services". SPEC §4.7 requires rules to live in files, and §97 requires a budget stop to notify humans. A stop is needed most when something else is broken:
- the Runner may be crash-looping;
- `sc-pg` may be degraded;
- Hermes may be prompt-injected;
- an operator may be away from the Git workflow.

A stop that exists only in Git takes minutes (PR, CI, merge, Flux) and is lost if Forgejo is down. A stop that exists only in the database is lost by a PITR and is unavailable when the Runner is down. A Kubernetes object that Flux owns would be reverted by the next reconcile.

## Decision
1. **Levels:** `none` < `workers` < `all`.
   - `workers` denies Gateway requests made with execution tokens, cuts their in-flight streams, halts Runner dispatch, cancels queued and running executions, and moves their tasks to `BLOCKED`.
   - `all` additionally denies and cuts Hermes' orchestrator-token requests.
   - At both levels the Runner read APIs, Forgejo, Woodpecker, Flux, CNPG, monitoring, backups and the Hermes process keep running.
2. **Three sources:**
   1. **DB:** `ledger.emergency_stop_events` written by the Runner admin API (`PUT /v1/admin/emergency-stop`, `SC-RUNNER-ADMIN`). It is the fastest and audited path, read inside every Gateway reservation transaction.
   2. **Git:** `policy/budgets.yaml` `spec.emergencyStop`. It is durable, reviewable, survives database restores, and is used by the black-start recovery commit.
   3. **Kubernetes:** ConfigMap `strange-company/sc-emergency-stop`, created with cluster-admin. The Gateway reads it with a direct API GET every 5 s. It does not depend on the Runner, the DB, Forgejo or Flux. It is never in Git, CI rejects any rendered manifest that names it, and Flux's inventory-based pruning never touches it.
3. **Effective level = max(db, git, k8s, degraded).** `degraded = workers` when the Kubernetes source has not been read for 60 s. Any one source stops spending. Releasing requires clearing every source that is set. Malformed input (an unknown ConfigMap level) is treated as `all`.
4. **No automatic resumption.** Tasks stopped by an emergency stop or a shared-budget exhaustion move to `BLOCKED` with a stored resume state. Only a human transition resumes them (lesson L-4).
5. **Automatic trip (review BUD-B8).** The Gateway leader writes a DB-source event at level `workers` (actor `budget:anomaly`) when trailing-window spend from `sc-pg` exceeds `anomaly.absoluteWindowCapUsd` and `anomaly.autoStop` is true (default). This is the only automatic writer of a stop; release stays human-only. A missing or invalid DB state row counts as `all`.
6. **No Hermes or worker path.** No MCP tool exists to read or change the stop. Workers have no route to the admin listener or the Kubernetes API.

## Consequences
- The spend stop holds whenever *any* control channel works. When none does, the Gateway being down or the DB being down also means no spend (fail closed, ADR-0450).
- Operators must check three sources to release a stop. The Runner admin API and the Gateway status endpoint report all of them, and the procedure (`procedures/budget-emergency-stop.md`) walks through each.
- A kube-apiserver outage longer than 60 s automatically stops worker spending. Workers cannot be managed in that state anyway.
- Path 3 is deliberately cluster-admin only, because Kubernetes RBAC cannot restrict `create` by resource name.

## Alternatives rejected
- **A single DB flag:** unavailable when the Runner or the DB is broken, and lost by a PITR.
- **A Git-only flag:** too slow for an incident, and it depends on Forgejo, CI and Flux.
- **A Flux-managed ConfigMap toggled by `kubectl edit`:** reverted at the next reconcile, and not Git-explainable either.
- **Scaling the Gateway to zero as the stop:** it also kills Hermes, gives no per-level control, and Flux restores the replica count. It remains a documented last resort.
- **Automatic resume when the stop is lifted:** spending would restart without a human decision.

## Verification
GATE-C06 (plan 33): 33-AC-7 through 33-AC-14 and 33-AC-18. These cover every path and level, the in-flight cut, Hermes behavior at `all`, core-service health during a stop, Flux non-interference with the ConfigMap, RBAC, the degraded source, and persistence across Gateway restarts.
