# Spec: Hermes dashboard authentication modes

**Status:** ready to implement. **Scope:** chart only, no Go changes.

## Problem

The Hermes dashboard's auth gate engages on any non-loopback bind and **fails
closed** without an auth provider — it simply never listens on 9119. The chart
therefore always configures one, and today the only option it can configure is
HTTP basic auth.

That is wrong for a chart published for general use. Any operator already
running an identity provider — Keycloak, Auth0, Entra, Okta, authentik — wants
the dashboard behind it, not behind a second, weaker credential the chart
generated. Basic auth is right for a fresh install with no IdP, and wrong as the
only choice.

This is a general gap. It is **not** about matching any particular deployment.

## Requirements

1. `hermes.dashboard.auth.mode` selects `basic` (default) or `oidc`.
2. **`basic` must remain the zero-configuration default.** A batteries-included
   install with no values must still work end to end, exactly as today. The
   rendered output for a default install must be byte-identical to before this
   change.
3. In `oidc` mode the deployment sets `HERMES_DASHBOARD_OIDC_CLIENT_ID`,
   `HERMES_DASHBOARD_OIDC_ISSUER` and `HERMES_DASHBOARD_PUBLIC_URL`, and does
   **not** set the basic-auth variables. Setting both would leave which provider
   wins up to the image.
4. A confidential client may supply a client secret from an existing Secret.
   A public client using PKCE needs none, so the secret is optional and no
   `HERMES_DASHBOARD_OIDC_CLIENT_SECRET` is rendered when it is absent.
5. `values.schema.json` must reject `mode: oidc` without `clientId`, `issuer`
   and `publicUrl`. Misconfigured OIDC makes the dashboard fail closed at
   runtime — an opaque "port 9119 never opened" — so it has to fail at
   `helm install` instead, where the message can say why.
6. The chart never invents an OIDC client. Registering one with an IdP is the
   operator's job; the chart only carries the settings.
7. No vendor is named anywhere in values, templates or schema. `authentik`,
   `keycloak` and friends belong in documentation as examples only.

## Acceptance criteria

| ID | Criterion | Verification |
|----|-----------|--------------|
| AC1 | Default install renders basic auth and no OIDC variables | helm-unittest: default values, assert basic-auth env present and `HERMES_DASHBOARD_OIDC_CLIENT_ID` absent |
| AC2 | `mode: oidc` renders the three OIDC variables | helm-unittest: assert each present with the configured value |
| AC3 | `mode: oidc` renders no basic-auth variables | helm-unittest: assert both basic-auth env names absent |
| AC4 | OIDC client secret is rendered only when an existing Secret is given | helm-unittest: two cases, absent and present as a `secretKeyRef` |
| AC5 | Schema rejects `mode: oidc` with missing settings | CI: `helm template` with an invalid-values fixture must exit non-zero |
| AC6 | Default rendering is unchanged by this feature | existing chart tests continue to pass untouched |
| AC7 | No vendor name appears in chart sources | `grep -ril authentik charts/` returns nothing |

## Out of scope

Dashboard ingress, TLS, and the gateway's own `API_SERVER_KEY` — the latter is
already covered by `hermes.existingSecret` and `hermes.apiKeyKey`.
