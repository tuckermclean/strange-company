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

## Session creation, verified against the live gateway

`POST /api/sessions` creates a session **without a model call**, returns `201`
and the full session record. The fields that stick:

| Field | Effect |
|---|---|
| `title` | shown verbatim in the session list |
| `model` | stored on the session (`has_model_config: true`) |
| `system_prompt` | stored on the session (`has_system_prompt: true`) |

Sessions created this way get `source: "api_server"`, `hidden: false`, and ids
of the form `api_<epoch>_<hex>`. `PATCH` updates them, `DELETE` removes them
(both `200`), and `POST /api/sessions/{id}/fork` branches one (`201`), so the
control plane can clean up after itself.

Verified endpoints, `Authorization: Bearer <API_SERVER_KEY>`:

| Endpoint | Result |
|---|---|
| `GET /v1/models` | `200` — only `hermes-agent`, never the underlying models |
| `GET /api/sessions` | `200` — list |
| `POST /api/sessions` | `201` — creates without a model call |
| `GET/PATCH/DELETE /api/sessions/{id}` | `200` |
| `GET /api/sessions/{id}/messages` | `200` |
| `POST /api/sessions/{id}/fork` | `201` |
| `POST /api/sessions/{id}/chat` | the stateful turn endpoint |
| `POST /api/sessions/{id}/messages` | `405` — history is not writable |

## Do not pin a profile. Pin the model and the prompt.

The §10.2 plan assumed a `specifier` profile carrying the specification prompt.
Do not build on that, for two reasons.

**Profiles are a URL prefix, not a field.** A `profile` key in the create body
is accepted and silently ignored -- `{"profile": "definitely_not_a_real_profile"}`
returns `201`. The real mechanism is `POST /p/<profile>/v1/chat/completions`,
and it only works when `gateway.multiplex_profiles` is enabled.

**With multiplexing off, a wrong prefix is silently served by the default
profile.** On the live gateway `/p/specifier/v1/models` and `/p/nope/v1/models`
both return `200`. There is no observable difference between a correctly pinned
profile and a typo, so a control plane "pinning the specifier profile" would
report success while running whatever the default profile happens to be.

Setting `model` and `system_prompt` on the session avoids all of this, and keeps
the specification prompt in this repository under review, rather than in
out-of-band Hermes configuration that no install of this chart can guarantee.

## Two ways this API fails quietly

**A model name is not validated at create.** `{"model": "nonexistent/not-a-model"}`
returns `201` and stores the string verbatim. `GET /v1/models` advertises only
`hermes-agent`, so there is nothing to validate against. A typo surfaces at the
first turn, in the human's dashboard, not at the control plane's create call.

**Backend errors arrive as `HTTP 200`.** A failed turn comes back as a normal
completion whose `content` is the error text and whose `finish_reason` is
`"error"`:

```
{"choices":[{"message":{"role":"assistant",
  "content":"HTTP 400: {\"detail\":\"The 'anthropic/claude-opus-4.6' model is not
   supported when using Codex with a ChatGPT account.\"}"},
  "finish_reason":"error"}]}
```

A client that reads `choices[0].message.content` on `200` records an outage as a
model answer. Per §12.1 that is exactly the misclassification the attempt ledger
exists to prevent, so **any Hermes turn must check `finish_reason` before
believing the content.**

That specific error is also a live configuration fault on the operator's
cluster, not a property of Hermes: the gateway's default model is
`anthropic/claude-opus-4.6` while the backing account is a Codex/ChatGPT one
that refuses it. Every gateway chat call fails until the default model and the
credentialed backend agree.

## The handoff this enables

The control plane creates a session with `POST /api/sessions`, giving it a title
naming the card, the `specifier` model, and a system prompt built from the card,
its repository context and the ambiguity report, then records the session id on
the card. The human opens the dashboard and continues it there. Nothing new to
build or host, and no model call spent to open the conversation.

**Still unverified:** whether an `api_server` session appears in the *dashboard's*
session list. The dashboard is a separate listener (port 9119) behind authentik
OIDC and rejects the gateway bearer key, so this cannot be checked without the
operator's browser session. The dashboard reads the same session store and
applies source filters only when the caller passes them, and these sessions are
`hidden: false` -- but that is inference, not a verified fact. A session titled
"strange-company: does a gateway session show up here?" is parked on the live
gateway to settle it by eye.
