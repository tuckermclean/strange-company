# ADR-0250: Authentik runs PostgreSQL-only, is configured by Git blueprints, and uses a single-use bootstrap credential

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 15

## Context
SPEC §21 makes Authentik the primary human identity provider. SPEC §22 requires that its failure never make it unrecoverable, and SPEC §4.2/§4.7 require security-relevant configuration to be explainable by Git. Authentik historically needed Redis, and its Helm chart carries an optional Bitnami PostgreSQL subchart (SPEC §20 forbids Bitnami). Authentik's first start needs an administrator credential, and a permanent superuser password inside the cluster would be a standing credential nobody uses.

## Decision
- Authentik **≥ 2025.10** (Redis removed upstream), deployed with the official goauthentik chart, `postgresql.enabled: false`, no Redis or valkey in any form. Its database is the CNPG cluster `authentik-pg` (ADR-0006), `max_connections ≥ 150`.
- Every object policy depends on (groups, flows, MFA stages, OIDC providers and applications, forward-auth proxy providers, outpost assignment, the read-only `sc-audit` service account and its RBAC role) is defined by **blueprints in Git** (`gitops/platform/authentik/blueprints/`, label `sc: "true"`), delivered as a ConfigMap and re-applied by Authentik. Admin-UI edits to those objects are overwritten on re-apply. Users, MFA devices and the break-glass account are not in blueprints because they carry secrets.
- `AUTHENTIK_SECRET_KEY` is generated once before seed and SOPS-encrypted in Git to both age recipients (no bundle copy: the offline recovery identity decrypts it from the GitHub clone). It is rotated only on compromise (`SC-AUTHENTIK-SECRET-KEY`; sessions are invalidated, OIDC links survive because of `user_uuid`). OIDC providers use `sub_mode: user_uuid` so subjects survive restores.
- **Bootstrap credential:** `sso-init` creates the ephemeral Secret `authentik/authentik-bootstrap` (password + API token) immediately before Authentik first starts. The chart reads it through `optional: true` env references. When `sso-init` has created the break-glass and operator accounts, it deletes the Secret, restarts Authentik, expires the bootstrap token and deactivates `akadmin`. A probe alerts if `akadmin` is active or the token exists.
- **Upstream flows are closed (review AUTH-1).** Blueprint `05-hardening.yaml` binds a deny policy to (or removes) every upstream flow (`default-authentication-flow`, enrollment, recovery, source flows, `initial-setup`), makes brand `sc` the default brand, and gives `sc-recovery` a flow-token guard, `never_create` and the MFA stage. Mandatory MFA therefore holds in every flow a human can reach, and nobody can claim `akadmin` through `initial-setup` on an empty database. Whenever Authentik would start on an empty `authentik-pg`, the HelmRelease stays suspended until `sso-init --from A0` has created the bootstrap Secret.
- **Post-bootstrap admin channel (AUTH-5).** After teardown, administrative repairs use `kubectl exec … ak shell` with a versioned script: cluster-admin gated and audited by the kube-apiserver `exec` entry and Authentik events.
- Authentik is reconciled by Flux **after** control transfer (not in the seed set). Only the `authentik` Namespace object is created during seed so the bootstrap Secret can precede first start.

## Consequences
- Restoring Authentik needs only the CNPG backup, the Git blueprints and the secret key from Git. No Authentik login is needed to restore Authentik.
- More PostgreSQL connections than with Redis (upstream note), sized in plan 12.
- Blueprint schema changes between Authentik releases are an upgrade risk, covered by the k3d blueprint-apply test on every version bump.

## Alternatives rejected
- Redis/valkey alongside Authentik: unnecessary from 2025.10 and one more stateful component.
- Configuration through the admin UI or Terraform: UI state is not Git-explainable, and a Terraform provider would need an admin token in automation.
- A permanent bootstrap superuser stored in the cluster: a standing, unused superuser credential.

## Verification
GATE-P07 (plan 15): 15-AC-2 (no Redis, no Bitnami, version ≥ 2025.10), 15-AC-3 (CNPG), 15-AC-4 (blueprints successful and idempotent), 15-AC-16 (bootstrap teardown), 15-AC-29/30 (upstream flows denied, empty-DB takeover impossible), 15-AC-31 (admin channel), 15-AC-17 (secret key in the pod matches Git).
