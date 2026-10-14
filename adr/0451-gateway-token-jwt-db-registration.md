# ADR-0451: Gateway tokens are Runner-signed EdDSA JWTs that must also be registered in `sc-pg`

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 53 (consumers: 26, 24, 31, 33)

## Context
Workers and Hermes authenticate to the Model Gateway (ADR-0004). The credential has to carry trustworthy attribution claims (task, execution, attempt, role, harness, phase, repository, project, tiers; SPEC §78), an exact model allowlist (plan 31) and budget scopes (plan 33). It has to be revocable immediately, for example when an execution ends, is cancelled or is emergency-stopped. A leaked worker credential must be worth little. The Gateway must also never trust attribution supplied by the caller.

## Decision
1. A gateway token is a **compact JWS signed with EdDSA (Ed25519)** by the Agent Runner using `SC-RUNNER-SIGNING`. The header is `{alg: EdDSA, typ: sc-gw+jwt, kid}` and the claims follow `contracts/gateway-token.schema.yaml` (`aud: sc-model-gateway`). Runner task tokens use a different `aud`/`typ`, so neither verifier accepts the other's tokens.
2. There is **no separate gateway signing key.** The Gateway holds only the public JWKS (ConfigMap `sc-runner-jwks`). Key rotation keeps two `kid`s published with ≥ 24 h overlap (plan 35).
3. **Signature validity is necessary, not sufficient.** Before forwarding, the Gateway requires a row in `ledger.gateway_tokens` with the token's `jti`, `revoked_at IS NULL`, and `claims_sha256` equal to the SHA-256 of the received payload bytes. It checks this inside the reservation transaction (plan 33 §14.2), so revocation takes effect for the next request after the revoking transaction commits.
4. Execution tokens live for the Job deadline + 300 s (max 6 h) and are revoked by the Runner at every terminal state. The Hermes `orchestrator` token lives ≤ 31 days, is rotated by the Runner every 30 days with 24 h overlap (review SEC-13), is re-minted immediately when the Runner's signing `kid` changes (review COST-16; plan 35/ADR-0401 must implement the trigger) (ADR-0401, plan 26) and is revocable at any time.

## Consequences
- Revocation, cancellation and the emergency stop act at request granularity without key rotation.
- Forging a usable token requires **both** the private signing key and write access to `sc-pg`. Neither alone suffices.
- Each request costs one indexed lookup inside a transaction the Gateway runs anyway (the reservation).
- If `sc-pg` is down, no token validates. That is consistent with the fail-closed rule (ADR-0450 decision 4).

## Alternatives rejected
- **Opaque random tokens looked up in the DB:** claims would live only in the DB, and the Gateway would need the DB even to authenticate a denial. The JWT keeps claims self-describing and schema-validated, and the DB row adds revocation.
- **JWT only (no registration):** no immediate revocation, and a leaked signing key would mint unlimited spend authority.
- **A dedicated gateway signing key held by the Gateway:** the Gateway does not mint tokens, so it would be an unnecessary private key in the most secret-dense pod.
- **Provider keys per worker:** forbidden by ADR-0004 and SI-09.

## Verification
GATE-C05 (53-AC-13): a revoked jti returns `SC_TOKEN_REVOKED`; a token signed by a key absent from the JWKS returns `SC_TOKEN_INVALID`; a Runner task token (wrong `aud`) returns `SC_TOKEN_INVALID`; a token whose payload differs by one byte from the registered digest returns `SC_TOKEN_INVALID`.
