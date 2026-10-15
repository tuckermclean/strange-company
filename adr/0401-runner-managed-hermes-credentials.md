# ADR-0401: The Runner generates, delivers and rotates Hermes' machine credentials

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 26 (consumers: 24, 53)

## Context
Hermes needs three machine credentials: the MCP bearer for the Runner (`SC-RUNNER-HERMES`), its own gateway API key (`API_SERVER_KEY`, `SC-HERMES-API`) and a Model Gateway token (`SC-GATEWAY-TOKEN`, instance `hermes`). The gateway token is a JWS signed with `SC-RUNNER-SIGNING` that is only usable once registered in `ledger.gateway_tokens` (ADR-0451); the MCP bearer must be known to the Runner as a hash. All three must reach Hermes through managed scope (`$HERMES_MANAGED_DIR`, L-1) and must be rotated (90 days or less) without human toil. Storing them SOPS-encrypted in Git would require an operator to mint a JWS, register its jti in the database and update a hash file in a coordinated PR for every rotation; a missed step silently breaks Hermes or leaves a stale credential valid.

## Decision
- The Runner is the creator. At startup and every 60 s it ensures Secret `hermes/hermes-managed-env` exists and is current; on a daily check it rotates credentials older than `hermes.credentialRotationDays` (30), keeping the previous MCP bearer and gateway token valid for `hermes.overlapHours` (24). `POST /v1/admin/hermes-credentials:rotate` forces rotation (`after_overlap` or `immediately`).
- The Secret holds the rendered managed-scope files `.env` and `config.yaml`, produced from Git templates (`charts/strange-company/files/hermes/managed/*.tmpl`, mounted read-only into the Runner) by substituting the credentials. Non-secret managed settings therefore stay Git-explainable; only credential values are generated.
- The Runner stores only sha256 (static bearers) and jti (gateway token) in `sc-pg`; plaintext exists only in the Secret.
- After writing the Secret, the Runner patches the pod-template annotation `strange.company/managed-revision` of Deployment `hermes/hermes` (RBAC `get, patch` on that name only; the chart's HelmRelease drift detection ignores that annotation, plan 54) to restart Hermes.
- Recovery: if the Secret is lost (namespace deleted, cluster rebuilt) the Runner regenerates it; nothing needs to be restored from backup.
- **Containment is not rotation** (review RA-10): `POST /v1/admin/hermes-credentials:revoke` revokes every Hermes credential and sets `hermes_disabled` in `sc-pg`; while set, the ensure loop does not re-render the Secret and the MCP listener rejects every bearer. Only a human `:enable` call mints fresh credentials.
- The orchestrator gateway token (lifetime ≤ 31 days) is re-minted whenever its JOSE `kid` differs from the current signing `kid`, so a signing-key rotation never strands Hermes (review COST-16). All three credentials rotate every 30 days with a 24 h overlap.

## Consequences
- The Runner gains write rights in `hermes`: `get/update/patch` on Secret `hermes-managed-env` and `get/patch` on Deployment `hermes` are name-restricted by RBAC, but RBAC **cannot** name-restrict `create`, so plan 37's ADM-42 must admit Secret CREATE only for `metadata.name == 'hermes-managed-env'` and Deployment UPDATE only when nothing but the `strange.company/managed-revision` pod-template annotation changes (reviews RA-m7, SEC-24).
- `contracts/secret-classes.yaml` (plan 35) must record creator `agent-runner`, storage `k8s-secret hermes/hermes-managed-env` + `sc-pg` hash/jti, recovery `regenerate` for `SC-RUNNER-HERMES`, `SC-HERMES-API` and the `hermes` instance of `SC-GATEWAY-TOKEN` (the register currently says `sc-bootstrap seal` + SOPS; consolidation item).
- Operators read `API_SERVER_KEY` from the Secret with cluster access when they need Hermes' API.

## Alternatives rejected
- SOPS in Git with manual rotation: multi-step coordinated changes for every rotation; JWS minting needs the private signing key on an operator machine.
- A separate credentials controller: another component with the same rights.
- Long-lived, never-rotated credentials: violates plan 35's rotation policy.

## Verification
24-AC-10 (deleting the Secret yields new credentials and a working Hermes within 2 min), 26-AC-31 (old MCP bearer rejected after the overlap in the rotation test), 24-AC-5 (gateway rows use the current jti).

## Amendment (2026-10-14, review COST-16)
The Runner re-mints the Hermes orchestrator gateway token whenever the token's JOSE `kid` differs from the current `SC-RUNNER-SIGNING` key id (checked on every policy/JWKS load and by the 60 s credential guard), re-renders `hermes-managed-env` and restarts Hermes. A signing-key rotation therefore never strands Hermes until the next 30-day rotation. This matches the secret register (plan 35).
