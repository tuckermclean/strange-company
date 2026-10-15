# ADR-0017: Platform policy is GitOps configuration; repository policy is read from the base revision

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §4.7 requires important policy to be version-controlled. Prompt text cannot be the sole enforcement. Policy must not be changeable by the work it governs.

## Decision
- **Platform policy** (`policy/**` in `strange-company`) reaches the Runner and Gateway as Flux-generated ConfigMaps at the reconciled revision. The Runner records the policy revision (Git SHA plus content digest) on every task transition and dispatch. Changes go through PR, CI and human merge. `policy/**` is a protected path for agent tasks.
- **Repository policy and context** (`.strange-company/**` in any managed repository) are read by the Runner **from the task's recorded base commit** through the Forgejo API, never from the agent branch head. Repository policy may tighten platform policy but **never loosen** it. The merge function is defined in plan 22, and loosening attempts are rejected at load.
- If policy fails to load or validate, the Runner fails closed: no dispatch, an alert, and the last-known-good policy is kept for read-only operations.

## Consequences
An agent cannot change the rules that judge its own work. Policy changes are auditable and attributable to a commit.

## Alternatives rejected
- Policy in the database editable through the API: not Git-explainable (SPEC §4.2).
- Reading repository policy from the branch head: self-judging.

## Verification
F04 and C01 gates: a task whose agent branch edits `.strange-company/validation.yaml` is judged with the base-revision validation, and the edit itself causes `PROTECTED_PATH_VIOLATION`; a repository policy that loosens a platform limit is rejected.
