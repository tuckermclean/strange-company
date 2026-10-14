# NN — <Subsystem name>

**Status:** Stage-0 plan (draft | reviewed)
**DAG nodes:** <ids from plans/dag.yaml that this plan owns or co-owns>
**Related plans:** <links>
**Contracts:** <contracts this plan owns or consumes>
**ADRs:** <ADRs this plan relies on or introduces>

> Every section below is mandatory (SPEC §121). An omitted section must read
> `N/A — <justification>`. Requirements carry SPEC trace tags such as `[SPEC §50]`.

## 1. Purpose
## 2. Requirements derived from SPEC.md
Numbered list, each with SPEC trace tags. Add derived requirements as `NN-R-<n>`.
## 3. Inputs
## 4. Outputs
## 5. Dependencies
Hard dependencies (DAG) and soft and runtime dependencies.
## 6. Owned state
Which durable state this subsystem is the system of record for, where it lives, and who may write it.
## 7. External interfaces
Interfaces other subsystems or humans consume (with contract references).
## 8. Internal interfaces
## 9. Configuration
Every configuration knob, its source of truth (Git path), its default, and who may change it.
## 10. Secrets
Secret classes consumed or produced (IDs from plan 35).
## 11. Storage
## 12. Resource requirements
Requests and limits for the self-hosting-minimum and small-ha profiles.
## 13. Security boundary
Trust level, what it can reach, what can reach it, RBAC, NetworkPolicy, PSA.
## 14. Happy path
## 15. Failure modes
A table of failure → detection → effect → automated response → procedure.
## 16. Retry behavior
## 17. Idempotency behavior
## 18. Bootstrap behavior
## 19. Upgrade behavior
## 20. Recovery behavior
## 21. Observability
Metrics, logs, traces, alerts (alert IDs from plan 42).
## 22. Testing strategy
Unit, contract, integration, e2e, and how each runs deterministically in CI without paid model calls.
## 23. Acceptance criteria
Grouped by DAG gate: `### GATE-<node>` with checklist items `NN-AC-<n>`. Each item must be a deterministic, observable check.
## 24. Explicit non-goals
## 25. Open questions
Each with: blocking? (yes/no), proposed conservative resolution, and owner.
