# ADR-0014: Admission defense with Pod Security Admission plus ValidatingAdmissionPolicy (CEL)

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §84–§85 and §105 require that Kubernetes independently prevent worker escalation, with native ValidatingAdmissionPolicy where practical. Kyverno and Gatekeeper are capable, but they add controllers and memory and their webhooks can fail closed on the API server.

## Decision
- PSA labels: `restricted` (enforce, audit, warn) on `agent-jobs`, `ci-jobs`, `strange-company`, `hermes`, `forgejo`, `authentik`, `woodpecker`, `renovate`, `dr-tests`, `apps`.
- **ValidatingAdmissionPolicies** (GA since Kubernetes 1.30), bound to `agent-jobs` (and to `ci-jobs` where applicable). Pods there must: use the `agent-worker` ServiceAccount; set `automountServiceAccountToken: false`; reference images only from the harness-registry digest allowlist (supplied as policy params); mount only Secrets labeled with the pod's own `strange.company/execution-id`; have no hostPath, hostNetwork, hostPID or hostIPC; have no privileged or added capabilities; set resource limits within the largest profile; and set `activeDeadlineSeconds`. Only the Runner's ServiceAccount may create Jobs or Secrets there (RBAC), and the VAP double-checks the requesting user through `request.userInfo`.
- A cluster-wide VAP forbids any workload from using the `agent-worker` ServiceAccount outside `agent-jobs`, and forbids pods in `agent-jobs` that do not belong to a Job owned by the Runner.

## Consequences
No extra admission controller to run. Policies are YAML in Git, testable in k3d (F12). CEL can't verify image signatures, so admission-time signature verification is post-MVP (plan 23).

## Alternatives rejected
- Kyverno: richer (signature verification, mutation), but heavier and webhook-based. Revisit with signature verification post-MVP.
- Runner-only validation: forbidden by SPEC §85.

## Verification
F12 gate (k3d): every SPEC §105 escape attempt is denied by admission even when submitted with cluster-admin credentials impersonating the Runner ServiceAccount, and the reason is recorded.
