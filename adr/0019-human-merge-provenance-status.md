# ADR-0019: MVP merges are human-performed and gated by Runner provenance and review statuses

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §102 requires merge policy to verify TDD provenance for `test-first` tasks, and §127 lists "automatic merge everywhere" as an MVP non-goal. SPEC §129 says "Human or policy merges".

## Decision
- Protected branches require: (1) the Woodpecker CI status for the head SHA; (2) `strange-company/tdd-provenance` = success; (3) `strange-company/review` = success; and (4) one human approval for repositories whose policy requires it (default: required for `strange-company`).
- Only `sc-runner` posts statuses (2) and (3). Workers cannot post statuses (ADR-0007). Before posting, the Runner re-derives every SPEC §102 fact from its own database and evidence store, never from status or label state. Any later push to the PR head invalidates those statuses (the Runner reposts `pending`).
- The human merges in Forgejo. Admin merge override of required checks is disabled in branch protection where Forgejo supports it, and plan 55 records the residual risk where it does not.
- Low-risk autonomous merge is post-MVP. It would be a policy-gated Runner action, using the same checks plus merge-time re-verification.

## Consequences
The Self-Hosting Threshold PR is accepted by a human with full provenance visible in the PR body (SPEC §100).

## Alternatives rejected
- Autonomous Runner merge in MVP: an explicit SPEC non-goal.
- Woodpecker-only gating: CI status cannot express red-before-green ordering or review acceptance.

## Verification
C09 gate: a PR without RED evidence cannot be merged through the UI or API by a non-admin; after a force-push to the PR head, the provenance status returns to pending.

## Amendments (Stage-0 review fixes)
- **2026-10-14, GC-02 / TDD-17 / DR-3 (SI-56).** The merge gate depends on the Runner, Woodpecker, `sc-ci-config`, the egress proxy and Authentik, so a failure of any of them would block the PR that repairs it. Two audited exceptions exist, and nothing else may bypass the required contexts:
  1. **Emergency merge** (`procedures/emergency-merge.md`, plan 55 §14.8a). A break-glass human merges a revert or repair PR whose diff is limited to the failed component's paths. The commit carries a recovery trailer, which raises an always-notify alert. A follow-up PR, required and tracked, must pass the full gate once the component is healthy. Drilled by 55-AC-20.
  2. **Direct apply for state restores** (`procedures/restore-cnpg.md`, `restore-authentik.md`). Restoring `forgejo-pg`, `authentik-pg`, `sc-pg` or `woodpecker-pg` may apply the recovery component directly with cluster-admin. A Git-explainable follow-up PR must reconcile Git with the applied state within 24 h. Until then, Flux drift is expected and alerted.
- **2026-10-14, TDD-11 / TDD-10.** Merge-time provenance additionally requires F11, that the merge is the task's PR into the task's target branch, and F12, that policy is unchanged since the RED verification or that re-verification has passed (plan 22).
