# ADR-0353: Verification-first work uses explicit judge-first states and one human approval gate for exemption plus judge

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plans:** 29, 22

## Context
SPEC §46 and §57 require a machine-visible verification mode, version-controlled exemption classes, no self-exemption, and the invariant "define the judge before producing the answer" for work that cannot use executable TDD. Master plan §7 lets plan 29 add judge-first states or map onto the test-first states. Mapping documentation or research onto `RED_VERIFYING` would make metrics, projections and alerts misleading, and some exemptions need a human decision while others can be approved deterministically.

## Decision
- Six verification-first states: `JUDGE_AUTHORING` (tester writes the judge artifact), `JUDGE_VALIDATING` (CI proves a deterministic judge is well-formed and fails on base), `JUDGE_AWAITING_APPROVAL` (human gate), `JUDGE_APPROVED`, `PRODUCING`, `JUDGING`. Outcome and held states (`TEST_NEEDS_WORK`, `ALREADY_SATISFIED`, `NEEDS_INVESTIGATION`, `TEST_SPEC_CONFLICT`, `GREEN_VERIFIED`, `REVIEW`, `CHANGES_REQUESTED`, `AWAITING_MERGE`, `NEEDS_HUMAN`, `BLOCKED`, terminal) are shared.
- Each exemption class in `policy/verification.yaml` declares allowed paths, judge kind and source (`artifact` or `acceptance-criteria`), executor, approval (`auto` or `human`) and whether it produces a mergeable change. `auto` is allowed only for deterministic judges or for classes that produce no mergeable change.
- When approval is `human`, one `/sc approve-judge` approves the exemption and the judge together. `/sc deny-exemption` switches the task to `test-first` (tightening only).
- Hermes may request an exemption; the Runner validates it against merged policy at creation and refuses unknown or disallowed classes (`EXEMPTION_NOT_ALLOWED`). Workers cannot request exemptions.

## Consequences
The judge is always fixed, and if needed approved, before production starts. Exemptions are auditable per task. The state machine grows from 18 to 24 states; F05 tests cover both modes.

## Alternatives rejected
- Reusing the RED/GREEN states for judges: misleading metrics and alerts.
- Separate human approvals for exemption and judge: two waits for one decision.
- Allowing Hermes to approve exemptions: Hermes is not trusted for enforcement (ADR-0003).

## Verification
GATE-F05 golden trajectories `exemption-auto-docs`, `exemption-human-approval`, `exemption-not-in-policy` (plan 21 §22) and 29-AC-1 reachability for the verification-first mode.
