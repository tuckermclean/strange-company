# ADR-0007: Workers receive a per-task ephemeral write deploy key; the Runner holds Forgejo API authority

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §87 says workers should only clone approved repositories and push `agent/*` branches, while the Runner holds stronger authority (PRs, labels, statuses). Forgejo access tokens belong to user accounts, and a token with repository write scope can also create commit statuses and pull requests. A worker holding such a token could forge the very statuses that merge policy depends on (SPEC §102).

## Decision
- For each worker execution that must push, the Runner generates an ed25519 key pair and registers it as a **write deploy key on exactly one repository**, named `sc-<execution-id>`. It delivers the private key to the Job through a per-execution Secret, and **deletes the deploy key** when the execution reaches a terminal state, or at the deadline plus a grace period, through a periodic sweeper. Read-only executions get a read-only deploy key.
- Deploy keys authenticate Git over SSH only. They have no API access, so workers cannot create statuses, PRs, reviews, labels or releases.
- Branch protection on protected branches (`main`, `release/*`, plus every pattern in `RepositoryPolicy.protectedBranches`) disallows pushes by deploy keys.
- Forgejo does not restrict deploy-key pushes to a branch namespace, so the Runner **verifies after the fact**. On result ingestion it compares every repository branch head against its expectations. Any ref other than `agent/<task-id>` that was created or moved during the execution window and is attributable to the execution's key (Forgejo push events/webhooks) causes `POLICY_VIOLATION` and that ref is quarantined or deleted.
- The Runner's bot account `sc-runner` holds a scoped API token for PRs, statuses, labels and issues (repository write, never admin; CD-38). Deploy-key management uses a separate account, `sc-keys`, the only repository-admin machine account. Its token lives only in the Runner's key-broker container (CD-40).

## Consequences
- Strong separation between push ability and merge-gating signals.
- Worker clones use SSH to the in-cluster Forgejo SSH service. Forgejo's SSH listener therefore must be reachable from `agent-jobs`, but not necessarily from the public edge.
- The sweeper is critical. An orphaned deploy key is a standing write credential, so orphan count is an alert (plan 42).

## Alternatives rejected
- Per-task bot user plus scoped token: the API token can post statuses and PRs.
- A shared long-lived worker deploy key: not task-scoped, and a leak is persistent.
- Server-side pre-receive hooks for namespace enforcement: Forgejo custom git hooks are an admin-level code-execution feature and are disabled by default for good reason.

## Verification
C03 and F12 gates: a worker attempting `POST /repos/.../statuses/<sha>` has no credential to do so; a push to `main` with a worker key is rejected by Forgejo; a push to `agent/<other-task>` is detected and the execution fails with `POLICY_VIOLATION`; the key is gone within the grace period after the execution ends.

## Amendments (Stage-0 review fixes)
- **2026-10-14, RA-4 / TDD-14.** The deploy key is **deleted when the Runner receives the result**, before any Git fact is judged. It is no longer deleted only at terminal state, so a harness cannot push after the ref audit. The worker pod has two containers (plan 27):
  - `sc-worker` alone mounts the deploy key and the Runner task token;
  - `harness` runs the model CLI and the repository's commands, and holds only the gateway token and an egress token (`contracts/task-token.schema.yaml`).
  A tester's harness therefore cannot use the key to read other refs (SPEC §48).
- **2026-10-14, SEC-2.** If Forgejo's glob branch protection with rule priority can restrict deploy-key pushes to `agent/**` on the pinned version (re-verified by an AC in plan 16), it is used as **prevention**, and the after-the-fact detection remains as defense in depth.
