# ADR-0003: Hermes is the upstream Nous Research Hermes Agent, integrated via managed scope and MCP

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §4.6, §39 and §115 treat Hermes as the persistent orchestration and human-facing reasoning layer, and the chart scope says "Hermes configuration/integration", not "Hermes implementation". The prior implementation deployed `nousresearch/hermes-agent` and verified its behavior (plans/reference/prior-implementation-lessons.md L-1). Hermes Agent supports MCP servers, persistent memory (SPEC §43 learned context), a gateway API and an OIDC-capable dashboard.

## Decision
- Hermes is the upstream **Hermes Agent**, deployed digest-pinned in namespace `hermes` with a Longhorn PVC for `/opt/data`.
- All operator-controlled configuration goes through **managed scope**: a read-only projected Secret or ConfigMap at `$HERMES_MANAGED_DIR`. That includes the model provider base URL (the Model Gateway), the gateway token, the MCP server definition for the Runner, the dashboard OIDC settings and the system instructions or skills. Dashboard-entered credentials cannot override pinned keys.
- Hermes reaches the Runner **only** through the MCP surface (plan 26), authenticated by `SC-RUNNER-HERMES`. Hermes has **no Kubernetes RBAC** and no NetworkPolicy path to the Kubernetes API, worker pods, databases or provider APIs. Its model calls go only to the Model Gateway.
- Hermes is **not trusted for enforcement**. Every invariant (TDD gates, budgets, role or harness or profile allowlists, context projection rules) is enforced by the Runner, the Gateway, CI and admission. Hermes may be wrong, prompt-injected or replaced, and the system stays safe.
- Hermes is replaceable: any MCP-capable orchestrator that holds the `SC-RUNNER-HERMES` credential can drive the Runner.

## Consequences
- No bespoke orchestration UI is built for MVP. The human surfaces are the Hermes dashboard (behind Authentik OIDC), Forgejo issues and PRs, and Grafana.
- Hermes learned memory on its PVC is non-authoritative (SPEC §43). Important learning becomes repository-context PRs.
- Version-sensitive Hermes behaviors (L-1) must be re-verified by an integration test at C08.

## Alternatives rejected
- Building a custom orchestrator: this is product invention SPEC does not ask for, and it duplicates a maintained upstream.
- Letting Hermes create Kubernetes Jobs directly: forbidden by SPEC §4.6 and §59.
- Routing worker model calls through the Hermes gateway: it ignores the requested model (L-1), which would break routing and accounting.

## Verification
C08 gate: Hermes can call all MCP tools; a forged or absent credential is rejected; Hermes cannot reach the Kubernetes API or provider APIs (NetworkPolicy test); a dashboard-entered provider key does not override the managed key.
