# Spec: one place to put credentials, so a fresh install works

Status: proposed
Chart: `strange-company`
Related: [`hermes-managed-scope.md`](hermes-managed-scope.md)

## Problem

A wipe-and-install does not currently produce a working system.

`control-plane/internal/policy/defaults/providers.yaml` references Secrets by
name and key -- `anthropic-api-credentials/api-key`,
`anthropic-oauth-credentials/oauth-token`, `deepseek-credentials/api-key`,
`openai-credentials/api-key`. **The chart creates none of them.** A fresh
install therefore comes up with every provider failing closed, and the operator
has to hand-create four Secrets whose names are only discoverable by reading a
file compiled into the control-plane image.

The bundled Hermes needs the same credentials again, by a different route, to
serve the specification conversation. Today that means the same key is entered
twice, in two formats, with nothing keeping them in agreement. Drift between
them is invisible until a run fails.

## Non-goals

- Generating or managing provider credentials. The chart must never invent a
  credential; an empty value stays empty and the run that needs it fails closed.
- Replacing `policy.providers`. Operators who supply their own `providers.yaml`
  keep naming whatever Secrets they like, including ones this chart never made.

## Values

```yaml
credentials:
  # -- Provider credentials, in one place. Each entry becomes a Secret whose
  # name and keys are exactly what policy/providers.yaml references. Empty
  # values render no Secret at all, so an unset provider fails closed rather
  # than authenticating with "".
  anthropic-api-credentials:
    api-key: ""
  anthropic-oauth-credentials:
    oauth-token: ""
  deepseek-credentials:
    api-key: ""
  openai-credentials:
    api-key: ""

hermes:
  managed:
    # -- Env var -> "<secretName>.<key>" naming an entry in `credentials`
    # above. The literal value is pinned into the Hermes managed scope, so the
    # gateway and the coding runners cannot drift apart.
    fromCredentials: {}
      # ANTHROPIC_API_KEY: anthropic-api-credentials.api-key
```

Rules:

1. An entry whose values are all empty renders **no Secret**. A Secret holding
   an empty string is worse than a missing one: policy resolves it, the run
   authenticates with nothing, and the failure surfaces as a provider error
   rather than as missing configuration.
2. Individual empty keys within an otherwise-populated entry are omitted, for
   the same reason.
3. A Secret name that collides with one this chart already creates must `fail`
   at render time, naming both. Silently shadowing the database or Hermes
   secret would be an outage nobody could read from the values file.
4. `fromCredentials` values must be `<secretName>.<key>` and must resolve to a
   **non-empty** value in `credentials`. An unresolvable or empty reference
   fails at render, naming the reference -- a managed pin that silently pins
   nothing is exactly the failure managed scope exists to prevent.
5. `fromCredentials` composes with the existing `hermes.managed.env`: both feed
   the same generated `.env`. A key set in both must `fail` rather than one
   quietly winning.
6. `credentials` is independent of `hermes.managed.enabled`. Provider Secrets
   are created either way; only `fromCredentials` requires managed scope, and
   using it with `hermes.managed.enabled: false` must `fail` rather than pin
   nothing.

## Acceptance criteria

Rendering (helm-unittest):

- default values: no credential Secrets at all (every default is empty).
- one populated entry: exactly one Secret, with that name, containing only the
  non-empty keys.
- a populated and an empty entry together: only the populated one renders.
- a name colliding with a chart-owned Secret fails, naming both.
- `fromCredentials` resolving to a populated value puts `KEY=value` in the
  managed `.env`.
- `fromCredentials` naming a missing entry, a missing key, or an empty value
  fails, and the message names the reference.
- `fromCredentials` with `hermes.managed.enabled: false` fails.
- a key present in both `managed.env` and `managed.fromCredentials` fails.

Behaviour (extend the existing `integration-hermes-managed` k3d job rather than
adding another cluster):

- install with one `credentials` entry populated and a `fromCredentials`
  mapping for it; assert the provider Secret exists with that key, and that the
  value Hermes resolves for the env var is the same literal. Both halves come
  from one values entry, which is the whole point.

## Documentation

- `values.schema.json` for both blocks.
- `docs/USAGE.md`: the minimum values file that produces a working install.
