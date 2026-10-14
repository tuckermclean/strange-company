# ADR-0354: Draft PR at RED, ready at GREEN; Runner-managed PR body; archive tags keep provenance reachable

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plans:** 55, 21

## Context
SPEC §67 says the Runner creates PRs after validating worker output; §100 lists the PR content (task, criteria, test commit, RED evidence, implementation summary, GREEN evidence, limitations, handoff); §56 requires provenance to survive squash merges. Workers have no API credential (ADR-0007). A PR opened only at GREEN hides the failing tests and the frozen criteria from humans during implementation; a PR opened at the first tester push would show unverified tests.

## Decision
- The Runner opens the PR when RED is verified (verification-first: when the judge is approved), as a work-in-progress PR (`WIP: ` title prefix; verify Forgejo semantics), and marks it ready at GREEN_VERIFIED. Required Runner statuses keep it unmergeable until review is accepted.
- The PR body is Runner-managed (`<!-- sc:managed-body v1 -->`), regenerated from the task record on every transition; model text is rendered inert; humans comment instead of editing.
- Before deleting `agent/<task>` (merge, cancel, generation reset) the Runner creates a protected tag `sc-archive/<task>/<merged|cancelled|declared-done|g<n>|x<n>>`; branch deletion waits for the tag. Policy violations get `sc-quarantine/<execution-id>`, except secret leaks, which are dropped without a tag. Only `sc-runner` may create these tags (tag protection), and they are never deleted automatically.

## Consequences
Humans see criteria and RED evidence early. Red and implementation commits stay reachable in Forgejo (and in repository bundles) after squash merges and branch deletion, so the SHAs in the task history always resolve. The repository accumulates tags (about one per task), which is cheap.

## Alternatives rejected
- PR at GREEN only: less visibility, same gating.
- Keeping agent branches forever: clutters branch lists and leaves pushable refs around.
- Recording provenance only in the database: SHAs could become unresolvable after `git gc`.

## Verification
55-AC-4 (draft PR at RED), 55-AC-12 (squash merge, archive tag at the PR head, branch deleted), 21-AC-12 (red commit resolvable after squash).
