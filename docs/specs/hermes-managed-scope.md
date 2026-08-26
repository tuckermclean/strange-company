# Spec: operator-pinned Hermes credentials via managed scope

Status: proposed
Chart: `strange-company`
Related: [`reference/hermes-integration-notes.md`](../reference/hermes-integration-notes.md)

## Problem

Provider credentials for the bundled Hermes can be typed into its dashboard.
`PUT /api/env` writes them to `~/.hermes/.env` and reconciles the `config.yaml`
mirrors (`model.api_key`, `auxiliary.*.api_key`, `custom_providers[*]`). Both
live on the Hermes PVC, so a value entered once survives every redeploy.

At startup `hermes_cli/env_loader.py` loads that file with `override=True`, so
**the hand-entered value beats the container environment**. Credentials the
chart supplies through `hermes.env` -- including values sourced from a
Kubernetes Secret -- are therefore not authoritative. The cluster's declared
state and its running state can disagree silently, and nothing in `helm diff`
shows it.

Credentials should be supplied by secrets. Humans mistype keys, and a mistyped
key that outranks the declared one is a failure that survives redeploys and
leaves no trace in the chart.

## Non-goals

- Preventing all dashboard configuration. Only the keys an operator explicitly
  pins become immutable; everything else stays adjustable, which is the point of
  a per-key mechanism.
- Managing control-plane credentials. Those already come from
  `strange-company-secrets` and are not affected.
- Any new Hermes feature. The mechanism below already exists upstream.

## Mechanism

`hermes_cli/managed_scope.py` is an "IT-pushed, user-immutable config & env
layer": a directory supplying `config.yaml` and `.env` values that win over the
user's copies **per leaf key**, applied last with `override=True`. It resolves
from `$HERMES_MANAGED_DIR` first, then `/etc/hermes`.

A read-only projected Kubernetes Secret is exactly the root-owned,
not-user-writable directory this expects.

**Mount at `/etc/hermes-managed` and set `HERMES_MANAGED_DIR`, not `/etc/hermes`.**
Mounting a volume at `/etc/hermes` would replace whatever the image ships there;
the environment-variable tier is documented, resolves first, and cannot collide.

## Values

```yaml
hermes:
  managed:
    enabled: false          # default off: existing installs render unchanged
    mountPath: /etc/hermes-managed
    env: {}                 # map[string]string -> .env, one KEY=value per line
    config: {}              # free-form mapping -> config.yaml
    existingSecret: ""      # BYO secret; keys ".env" and/or "config.yaml"
    existingSecretKeys:
      env: ".env"
      config: "config.yaml"
```

Rules:

1. `enabled: false` (default) renders no Secret, no volume, no mount, and no
   `HERMES_MANAGED_DIR`. A chart upgrade must be a no-op for every existing
   install.
2. `existingSecret` and inline `env`/`config` are mutually exclusive. Setting
   both must `fail` at render time with a message naming both keys, rather than
   silently preferring one.
3. `enabled: true` with all of `env`, `config` and `existingSecret` empty must
   `fail`. An empty managed scope is always a mistake -- it reads as "pinned"
   while pinning nothing.
4. The generated Secret is mounted `readOnly: true`.
5. `.env` values are written verbatim as `KEY=value`. Keys must match
   `^[A-Za-z_][A-Za-z0-9_]*$`; anything else fails at render time, because
   `env_loader` silently skips lines it cannot parse and a skipped credential
   looks exactly like a working one until a call fails.
6. `config.yaml` is rendered with `toYaml`, so operators can pin any leaf key
   (`model.default` is the expected common case alongside `model.api_key`).

## Acceptance criteria

Rendering (helm-unittest, `charts/strange-company/tests/`):

- default values: no managed Secret, no volume, no volumeMount, no
  `HERMES_MANAGED_DIR` on the Hermes container.
- `enabled: true` with `env`: Secret contains a `.env` key whose decoded content
  is the `KEY=value` lines; the deployment mounts it at `mountPath` with
  `readOnly: true`; `HERMES_MANAGED_DIR` equals `mountPath`.
- `enabled: true` with `config`: Secret contains a `config.yaml` key whose
  decoded content parses as the supplied mapping.
- `existingSecret`: no Secret is generated, the named secret is mounted, and the
  key names come from `existingSecretKeys`.
- each failure in Rules 2, 3 and 5 produces its specific message.

Behaviour (k3d integration job, `.github/workflows/helm.yml`):

- install with **both** `hermes.env.HERMES_MANAGED_PROBE=from-container-env`
  and `hermes.managed.env.HERMES_MANAGED_PROBE=from-managed-scope`, then assert
  the value Hermes actually resolves is `from-managed-scope`.

That last one is the whole point of the change and the only test that proves the
precedence claim rather than restating a reading of upstream source. It must
fail if the mount path, the `HERMES_MANAGED_DIR` wiring, or the upstream
precedence is wrong. If a probe env var turns out not to be observable through
the gateway API, assert against the process environment instead -- but do not
drop the test and do not replace it with a rendering assertion.

## Documentation

- `values.schema.json`: full schema for the block, `additionalProperties: false`
  on the sub-object, so a typo like `managed.envs` fails validation instead of
  rendering nothing.
- `values.yaml`: comment stating the precedence problem in two lines, so an
  operator reading the file learns that `hermes.env` is not authoritative for
  credentials.
- `docs/USAGE.md`: one worked example pinning a provider API key.
