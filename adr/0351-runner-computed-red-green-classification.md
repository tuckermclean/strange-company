# ADR-0351: The Runner computes RED/GREEN validity from sc-verify observations and its own Git facts

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 22

## Context
SPEC §50 defines valid RED by meaning (the relevant test runs, a requirement-specific assertion fails for absent behavior, the failure is not broken infrastructure or an impossible test), not by exit code. ADR-0008 and ADR-0302 put `sc-verify` inside the trusted `verify` workflow, but that step shares a container and uid with the code under test, so anything it *concludes* can be influenced by that code. Test frameworks also report results in different formats, and the line between an assertion failure and an error varies per tool.

## Decision
- `sc-verify run` emits only **observations** (`stage: observed`): commits checked out, suites run, per-test status at base and head normalized to `passed | failed_assertion | failed_error | stub_unimplemented | skipped | absent`, run-level build/timeout/report facts. The schema forbids a verdict in an observed document.
- The Runner (verifier library F06) produces the **classification** (`stage: classified`) from the observations plus facts it reads itself from Forgejo (changed paths, blob SHAs, ancestry, stubs, discovery configuration), the tester's declared AC mapping, the frozen acceptance criteria and the merged policy. Rules R0–R11 (RED) and G0–G8 (GREEN) in plan 22 are exhaustive and ordered.
- RED runs the suites at the base commit and the red commit in the same pipeline (unless a recorded baseline for the same base, validation blob and verifier version is reused). New tests are those absent at base; only assertion-class failures count; anything unclassifiable is an error (fail closed).
- Result formats: JUnit XML is the common model, with adapters for `go test -json`, pytest, jest, vitest and cargo-nextest; adapter classification rules are policy data (`policy/verification.yaml`), re-verified against recorded tool output on every toolchain upgrade.

## Consequences
- Verdict logic lives in one tested library and changes only with policy or code review; evidence records the policy revision and library version.
- RED costs a full base run (cached per base SHA) plus repeated head runs of the new tests; GREEN adds repeats, an isolation run without newly added test files and a replay of the change tests against the red commit (plan 22 G4b, G4c, G9). These close the fixture/helper, monkeypatch and phase-conditional bypasses found in review (TDD-1..TDD-4).
- Forged observations remain possible (SI-07); manifest blob checks, static diff checks and review bound the damage.

## Alternatives rejected
- Exit-code-only RED: cannot distinguish a compile error from a failing assertion (SPEC §50).
- Classification inside `sc-verify`: same trust as the code under test.
- Running baseline in a separate pipeline: doubles scheduling and identity checks for no trust gain.

## Verification
22-AC-3 (rule fixtures, deterministic output), 22-AC-12 (adapter re-verification), 22-AC-16 (observed documents with a verdict are rejected).
