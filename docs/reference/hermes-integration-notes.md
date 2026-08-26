# Hermes integration notes

Verified against the operator's live cluster on 2026-08-26, not from
documentation. These are the facts the §10.2 Fable specification conversation
has to build on.

## The interaction surface is the Hermes dashboard

Decision: the interactive specification conversation happens in the **Hermes
dashboard**, and this project builds **no UI of its own**. A bespoke UI would be
thrown away when the single interaction abstraction arrives, and §10.2/§33
already put the conversation there.

## What the live deployment actually looks like

```
API_SERVER_HOST                 0.0.0.0
API_SERVER_PORT                 8642
API_SERVER_KEY                  <- secret strange-company-hermes-auth/HERMES_API_KEY
HERMES_DASHBOARD                1
HERMES_DASHBOARD_PORT           9119
HERMES_DASHBOARD_OIDC_CLIENT_ID hermes-dashboard
HERMES_DASHBOARD_OIDC_ISSUER    https://auth.dcxxiv.com/application/o/hermes/
HERMES_DASHBOARD_PUBLIC_URL     https://hermes.dcxxiv.com
```

Two differences from what this chart currently renders, both of which the chart
should adopt rather than override:

1. **The dashboard is behind authentik OIDC**, not the basic auth this chart
   defaults to. The chart needs `HERMES_DASHBOARD_OIDC_*` and
   `HERMES_DASHBOARD_PUBLIC_URL` passthrough, with basic auth as the fallback
   for installs without an IdP.
2. **The gateway key lives in `strange-company-hermes-auth/HERMES_API_KEY`**,
   while this chart wires `strange-company-secrets/hermes-api-key`. Upgrading
   the chart as it stands would rotate the key out from under a running Hermes
   and break every gateway call. The chart must honour an existing secret.

## Verified gateway API

With `Authorization: Bearer <API_SERVER_KEY>`:

| Endpoint | Result |
|---|---|
| `GET /v1/models` | `200` — `{"data":[{"id":"hermes-agent",...}]}` |
| `GET /api/sessions` | `200` — list of sessions with `id`, `source`, `model`, `title`, `started_at`, `ended_at` |

Sessions carry a `source` (observed: `tui`) and a human-readable `title`, and an
observed session ran `anthropic/claude-opus-4.6`. Unauthenticated and
wrong-key requests both return `401`.

## The handoff this enables

The control plane creates a session seeded with the card, its repository context
and the ambiguity report, gives it a title naming the card, and records the
session id on the card. The human opens the dashboard and continues it there.
Nothing new to build or host.

**Still unverified:** whether a session created through the gateway appears in
the dashboard's session list for a human to resume, and how to pin a session to
a specific profile (the `specifier` profile with Fable). Both need checking
against a real session-create call before the M4c PR is written -- do not assume.
