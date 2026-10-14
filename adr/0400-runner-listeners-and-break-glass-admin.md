# ADR-0400: The Runner serves one listener per caller class; the admin API is loopback-only, with per-operator credentials

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 26

## Context
The Agent Runner has five kinds of callers with very different trust: Hermes (untrusted-but-authenticated, ADR-0003), worker pods (hostile code holding a task token), Forgejo webhooks and the Woodpecker configuration extension (machine peers), Prometheus/kubelet (no credentials), and humans who must occasionally perform transitions that no agent may perform (SPEC §22, L-4 "Blocked → * is human-only"). SPEC §60 requires the Hermes-facing surface to be substantially narrower than the Runner's administrative capabilities. One HTTP listener with route-level authorization would make every credential a candidate for every route and would make NetworkPolicy unable to separate them.

## Decision
- Separate listeners, each accepting exactly one credential type (plan 26 §7, `runner-api.openapi.yaml`):
  - `api` **127.0.0.1:8080**: `/v1/tasks*`, `/v1/executions*`, `/v1/admin/*`, `/v1/policy/*`; bearer `SC-RUNNER-ADMIN` (`scadm_<kid>.<secret>`). The actor is the identity bound to the key id — `human:<login>` for per-operator credentials, `breakglass:runner-admin` for the single break-glass credential — never a caller-supplied header; `X-SC-Actor` is rejected; `X-SC-Reason` is caller-supplied (amended by CD-30);
  - `mcp` :8081: Hermes, `SC-RUNNER-HERMES`;
  - `worker` :8082: worker pods, `SC-WORKER-TASK-TOKEN`;
  - `hooks` :8083: Forgejo (HMAC `SC-RUNNER-WEBHOOK`) and `sc-ci-config` (`SC-CI-INTENTS`, ADR-0302);
  - `ops` :8090 (container port named `metrics`, CD-02): probes and metrics, no data.
- The `api` listener binds loopback; startup fails otherwise. No Service exposes 8080. Human access is `kubectl port-forward` (which itself requires cluster break-glass rights) plus the operator's own `SC-RUNNER-ADMIN` credential (break-glass credential: plaintext only in the recovery bundle; Git holds only sha256 hashes).
- Every admin call is recorded with the human actor and raises the informational alert `SCRunnerAdminAccess` (break-glass audit evidence, SPEC §22).
- No Authentik integration for the Runner API in MVP; humans use Hermes, Forgejo and Grafana for routine work.

## Consequences
- NetworkPolicy (plan 36) can restrict each caller class to its own port; a leaked worker token cannot reach MCP or admin routes, and a leaked Hermes credential cannot perform human transitions.
- Operator procedures that need the admin API (quarantine, e-stop path 1, budget overrides, human transitions) require cluster access and the recovery bundle; this is deliberate friction for rare actions.
- A web UI for the admin API is post-MVP and would need an ADR (OIDC + forward-auth).

## Alternatives rejected
- Single listener with per-route auth: one misconfigured route exposes admin actions to workers or Hermes.
- Authentik-protected public admin endpoint: adds an internet-facing control surface and an identity dependency to break-glass actions.
- Kubernetes RBAC-only admin (CRDs edited by humans): not auditable as task transitions with reasons, and requires a controller.

## Verification
26-AC-16 (admin listener unreachable on the pod IP, 401 without token), 26-AC-31 (cross-listener credential rejection), 54-AC (Service exposes no 8080).

## Amendment (2026-10-14, CD-30, external review #4)
The original decision accepted a shared `SC-RUNNER-ADMIN` bearer plus a caller-supplied `X-SC-Actor`, so any holder could record any human identity. Replaced: per-operator admin credentials (key id → operator identity; Runner stores only `sha256` in a Git-delivered SOPS registry; issued and revoked by `sc-bootstrap access join|leave`, plans 15/50), plus one break-glass credential whose actor is always `breakglass:runner-admin` (plaintext only in the recovery bundle; always-notify on use). Immediate revocation through `POST /v1/admin/credentials/{kid}:revoke` (effective on the next request); registry changes take effect within 60 s of Flux delivering the file. Every audit row and admin-made transition records the actor and the credential key id. A request carrying `X-SC-Actor` is rejected (26-AC-50).
