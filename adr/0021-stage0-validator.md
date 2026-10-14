# ADR-0021: A committed, deterministic Stage-0 validator verifies the engineering definition

**Status:** Accepted (Stage 0, post-review) · **Date:** 2026-10-14 · **Resolves:** external review X#1 (CD-27)

## Context
`STATUS.md` claimed that the structural checks passed: valid schemas and examples, a sound state machine, a complete DAG with gates, no dangling references, consistent alert and secret names. The checks had been run ad hoc and were not committed, and the PR showed no CI result. That contradicts the project's own rule that models propose and deterministic systems decide (SPEC §4.3). Stage 0 must not certify its own acceptance criteria in prose.

## Decision
- `tools/stage0/validate.py` performs every structural check `STATUS.md` claims (checks C01–C14 in its docstring) and exits non-zero on any violation. It is deterministic: no network, no clock, no randomness.
- `tools/stage0/genplan.py` generates `PLAN.md` from `plans/dag.yaml` and the `GATE-<node>` sections. The validator fails if the committed `PLAN.md` differs from a fresh generation, and CI regenerates it under several hash seeds to prove the output is deterministic.
- Dependencies are pinned in `tools/stage0/requirements.txt`.
- `.github/workflows/stage0-validate.yaml` runs the validator on every PR and every push to `main`, with read-only permissions, no secrets, and actions pinned by commit SHA. Until Woodpecker exists, this is the interim trusted CI of ADR-0018 for Stage-0 artifacts. After P08 the same command runs in Woodpecker (`make stage0-validate`, plan 01).
- `STATUS.md` cites the validator's output, not prose.
- **Language exception to ADR-0002:** the validator is Python, because it is Stage-0 tooling that must exist before the Go module (F01) does, and the JSON Schema 2020-12 reference implementation is Python. It does not ship in any Strange Company image. F02 (contract tooling, Go) re-implements the contract checks for product use and must agree with this validator on the committed contracts.

## Consequences
Every claim in `STATUS.md` is reproducible by anyone with Python. Stage-0 artifacts cannot drift silently: a PR that breaks a schema, the DAG, gate coverage, an alert or secret name, or `PLAN.md` fails CI.

## Alternatives rejected
- Waiting for F02 (Go contract tooling): it leaves Stage 0 unverifiable at merge time, which is the defect being fixed.
- Prose assurance plus reviewer sign-off: not deterministic (SPEC §4.3).

## Verification
The `stage0-validate` workflow is green on the PR that merges Stage 0. Introducing any of these makes it red:
- an invalid example in a schema;
- a plan missing a section;
- a gate-less DAG node;
- an unknown alert name;
- a stale `PLAN.md`.
