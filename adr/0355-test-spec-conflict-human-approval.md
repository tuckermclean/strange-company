# ADR-0355: A corrected acceptance test needs a human approval by default

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plans:** 21, 29

## Context
SPEC §53 says that when implementation reveals an invalid test the task enters `TEST_SPEC_CONFLICT`, and "changing the acceptance test then requires a distinct reviewable action". SPEC §70 sketches implementer → Hermes → tester/reviewer → corrected test decision → Hermes. If the implementer, the adjudicating reviewer and Hermes are all models, weakening a test is decided entirely by stochastic components that may share a model family, and the test is the judge of the implementation.

## Decision
- Adjudication by a read-only reviewer execution is required first (`test_valid`, `test_invalid`, `spec_ambiguous`).
- `test_valid` (keep the test) needs no human: it can only tighten.
- `test_invalid` (change the test) needs a human `/sc accept-test-change conflict=<id> digest=<12 hex of sha256(proposed_test_change)>` (ADR-0350; the approval is bound to that conflict and that proposed change, blind TDD-B13) when `testSpecConflict.resolutionApproval` is `human` — the platform default. Platform policy may set `adjudicator`; repositories can only tighten back to `human`.
- An approved change opens a new generation: new red commit on top of the previous red commit, new RED verification, new manifest; old provenance retained.

## Consequences
Human latency is added to conflicts (expected to be rare). The test that judges the work cannot be weakened without a human on record. Caps (`maxConflictsPerTask` 2, `maxGenerations` 3) bound loops.

## Alternatives rejected
- Adjudicator-only approval by default: model-only weakening of the judge.
- Letting the implementer edit the test with a justification: violates SPEC §53.

## Verification
Plan 21 `tdd-test-spec-conflict` (21-AC-13): the tester dispatch is refused until the human command, and the transition carries `actor = human:<login>`.
