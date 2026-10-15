# ADR-0018: Interim CI on GitHub Actions until Woodpecker exists

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
Phase 1 (SPEC §119) produces contracts, tests, the toolbox, fake workers and the state machine *before* any Strange Company platform or Woodpecker exists. SPEC §1.2 still requires verification before construction. Something must run deterministic gates in that period.

## Decision
- Every CI gate is a **containerized Make target** (`make verify`, `make test`, `make contracts`, `make lint`, `make e2e-k3d`, ...). Its behavior is identical on any runner that can run containers.
- Until node P08, these targets run on **GitHub Actions** in the GitHub repository, which is the origin in that period. That is the interim trusted CI.
- From P08, Woodpecker runs the same targets and becomes the trusted CI. GitHub Actions workflows remain only as advisory checks on the mirror, plus the off-cluster mirror-freshness watchdog if plan 42 adopts it.
- During Phase 1, TDD discipline is enforced *by convention plus a CI check*. Each Phase-1 PR shows a test-only commit whose CI run failed as expected, before the implementation commit (plan 52, "bootstrap discipline").

## Consequences
CI logic is not tied to either CI product. The Forgejo→GitHub push mirror needs a token with workflow permission if `.github/workflows/**` is mirrored (L-6, plan 17).

## Alternatives rejected
- No CI until Woodpecker: this violates SPEC §1.2 for all Phase-1 work.
- Self-hosted Woodpecker on a laptop: not reproducible and not trusted.

## Verification
F01 gate: the same `make verify` passes locally inside the toolbox and on GitHub Actions. P08 gate: the same target passes on Woodpecker.

## Amendments (Stage-0 consolidation)
- **2026-10-13, refined by ADR-0303.** Tests that need a container runtime or `/dev/kvm` (k3d chart lifecycle, seed→Flux transfer, NetworkPolicy enforcement, the component e2e suites, the QEMU lab) never run on the production cluster. After P08 they run in an **off-cluster lane** (GitHub Actions on the read-only mirror) whose result **gates releases, not merges**. For that one lane, GitHub Actions is therefore not merely "advisory" after P08. Merges remain gated by Woodpecker only.
