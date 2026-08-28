# Using the strange-company chart

## The idea

Four components, three of which can be either **bundled** (this chart renders
them) or **external** (you already run them somewhere):

```
                    strange-company
                            │
        ┌───────────────────┼───────────────────┐
        ▼                   ▼                   ▼
   PostgreSQL            Vikunja              Hermes
 bundled/external   bundled/external    bundled/external
        └───────────────────┼───────────────────┘
                            ▼
                      Control Plane
```

The switches are independent. `vikunja.enabled: true` says nothing about
`postgresql.enabled`. There is no "mode" flag, and no combination is
special-cased.

## The configuration contract

Whatever you switch, the control plane container receives the same variables:

| Variable               | Source                               |
|------------------------|--------------------------------------|
| `DATABASE_HOST`        | ConfigMap                            |
| `DATABASE_PORT`        | ConfigMap                            |
| `DATABASE_NAME`        | ConfigMap                            |
| `DATABASE_USER`        | Secret (`secretKeyRef`)              |
| `DATABASE_PASSWORD`    | Secret (`secretKeyRef`)              |
| `VIKUNJA_URL`          | ConfigMap                            |
| `VIKUNJA_TOKEN`        | Secret (`secretKeyRef`, optional)    |
| `HERMES_GATEWAY_URL`   | ConfigMap                            |
| `HERMES_API_KEY`       | Secret (`secretKeyRef`, optional)    |
| `HERMES_DASHBOARD_URL` | ConfigMap (omitted when unavailable) |

All of the bundled-versus-external branching happens in
`templates/_helpers.tpl`. Application code never learns which it got.

## Installing

### Everything bundled

```bash
helm install strange-company ./charts/strange-company \
  --namespace strange-company --create-namespace
```

### Nothing bundled

```yaml
postgresql:
  enabled: false
  external:
    host: postgres.database.svc
    port: 5432
    database: strange-company
    existingSecret: strange-company-db
vikunja:
  enabled: false
  external:
    url: https://kanban.example.com
    existingSecret: strange-company-vikunja
    tokenKey: token
hermes:
  enabled: false
  external:
    gatewayUrl: http://hermes.ai.svc:8642
    existingSecret: strange-company-hermes
    apiKeyKey: api-key
```

### From an OCI registry

```bash
helm install strange-company \
  oci://ghcr.io/tuckermclean/charts/strange-company \
  --version 0.1.0 \
  --namespace strange-company --create-namespace
```

## Two sharp edges worth knowing

### 1. The doubled `vikunja` key

Bundled Vikunja is the upstream chart, declared as a Helm dependency. Helm
requires a subchart's values to live under the dependency's name, and the
upstream chart *also* nests its own values under `vikunja`. So:

```yaml
vikunja:
  enabled: true          # <- this chart's switch
  external: {}           # <- this chart's external endpoint
  databaseSecretName: …  # <- this chart
  vikunja:               # <- everything below is the UPSTREAM chart
    image: {}
    persistence: {}
    ingress:
      main:
        enabled: true
```

Anything you put directly under `vikunja:` other than `vikunja.vikunja` is
merged into the upstream chart's *root* values. That is why:

- **Vikunja's Ingress is configured at `vikunja.vikunja.ingress.main`**, not at
  `vikunja.ingress`. The upstream chart iterates `ingress` as a map of named
  ingresses, so a bare `enabled:` boolean there crashes its renderer. The chart
  fails the render with an explanation if you try.
- The chart's own Ingress values (`controlPlane.ingress`, `hermes.ingress`) use
  the standard `className` / `annotations` / `hosts` / `tls` shape, because
  those workloads are chart-owned and have no such collision.

### 2. Vikunja's database Secret is a literal name

The upstream chart does not run `tpl` over `secretKeyRef` names, so its database
password reference cannot be templated per release. Two consequences:

- With **bundled PostgreSQL**, this chart creates a Secret named
  `vikunja.databaseSecretName` (default `vikunja-db`) holding the resolved
  credentials. It is namespace-scoped, so run one release per namespace or
  override the name.
- With **external PostgreSQL**, point both `vikunja.databaseSecretName` *and*
  `vikunja.vikunja.env.VIKUNJA_DATABASE_PASSWORD.valueFrom.secretKeyRef.name` at
  your own Secret. See `charts/strange-company/ci/values-hybrid-b.yaml`.

The chart cross-checks these at render time and fails with an actionable message
if they disagree, or if the inlined Vikunja database host stops matching the
bundled PostgreSQL Service name.

## Using it as a dependency of another chart

See `examples/parent-chart/`. The convention Helm imposes:

```yaml
# parent Chart.yaml
dependencies:
  - name: strange-company
    repository: oci://ghcr.io/tuckermclean/charts
    version: "0.1.0"
    condition: strangeCompany.enabled
```

```yaml
# parent values.yaml
strangeCompany:
  enabled: true        # parent-level: satisfies `condition:` only

strange-company:       # must match the dependency name (or its alias)
  postgresql:
    enabled: false
    external:
      host: my-postgres.svc
```

The `condition` key and the subchart value block are different things: Helm
reads conditions from the parent's own values, so `strangeCompany.enabled` must
sit *outside* the `strange-company:` block.

## Credentials and upgrades

- The bundled PostgreSQL password is generated once and then read back from the
  cluster on every subsequent render, so `helm upgrade` never rotates it. Set
  `postgresql.auth.password` or `postgresql.auth.existingSecret` to take
  control.
- Hermes credentials come from `hermes.existingSecret`. Nothing about the chart
  requires AI provider credentials to render, install, or pass CI.
- Vikunja has no bootstrap-token API, so a fresh bundled install has no
  `VIKUNJA_TOKEN`. Create a token in the UI and set `vikunja.token`, or supply
  `controlPlane.existingSecret`.

### Provider credentials in one place

`credentials` is the one place to put AI provider credentials so a fresh
install actually works. Each entry becomes a Secret whose name and keys are
exactly what `control-plane/internal/policy/defaults/providers.yaml` (or your
own `policy.providers`) references. The chart never invents a credential: an
empty value renders no Secret at all, so an unconfigured provider fails closed
rather than authenticating with `""`. See
[`specs/single-source-credentials.md`](specs/single-source-credentials.md).

The minimum values file that makes the shipped default `providers.yaml`'s
`anthropic-api` provider work:

```yaml
credentials:
  anthropic-api-credentials:
    api-key: sk-ant-...
```

`credentials` is a free-form map -- the four documented entries in
`values.yaml` are defaults for the shipped `providers.yaml`, not an allowlist.
Add any other entry to create a Secret for a provider this chart has never
heard of. A name that collides with a Secret the chart already owns (its own
credentials Secret, the bundled PostgreSQL Secret, the Vikunja database
Secret, or Hermes's development-convenience Secret) fails the render rather
than silently overwriting it.

### Pinning a Hermes provider credential against dashboard drift

`hermes.env` is not authoritative for credentials: Hermes lets a human type a
provider API key into its own dashboard, which writes it to `~/.hermes/.env`
on the Hermes PVC and reloads it with `override=True` on every restart -- so a
hand-entered value silently beats whatever the chart declared, forever, with
no trace in `helm diff`. See
[`specs/hermes-managed-scope.md`](specs/hermes-managed-scope.md).

`hermes.managed` pins specific keys so they win instead, on every restart,
regardless of what the dashboard has stored:

```yaml
hermes:
  managed:
    enabled: true
    env:
      ANTHROPIC_API_KEY: sk-ant-...
    config:
      model:
        default: anthropic/claude-test
```

`env` and `config` are mutually exclusive with `existingSecret` -- use one or
the other. Everything *not* pinned here stays adjustable in the dashboard, as
normal.

To pin the *same* value that also backs a provider Secret above -- so the
gateway's dashboard and the coding runners can never drift apart -- reference
it from `credentials` instead of retyping it:

```yaml
credentials:
  anthropic-api-credentials:
    api-key: sk-ant-...

hermes:
  managed:
    enabled: true
    fromCredentials:
      ANTHROPIC_API_KEY: anthropic-api-credentials.api-key
```

`fromCredentials` composes with `env` above (a key set in both fails the
render), requires `hermes.managed.enabled: true`, and each
`<secretName>.<key>` reference must resolve to a non-empty value in
`credentials` -- an unresolvable or empty reference fails the render, naming
the reference.

## Storage

`storageClass: ""` means "use the cluster default StorageClass" — the chart
emits no `storageClassName` key at all in that case. No storage class is ever
hard-coded, including K3s's `local-path`.

## What this chart deliberately does not do

No Kanban logic, no agent orchestration, no model routing, no GitHub issue
automation, no coding-agent Jobs, no RBAC for spawning workloads. It is
plumbing. Behavior belongs to the control plane, later.

## How a card's tests are verified

The §11.3 red gate and the §19 green gate both need one fact about a ref: did
its tests pass. Two backends can answer, chosen by
`controlPlane.verification.mode`.

### `github-actions` (default)

The control plane reads the check runs your own workflows produced. Nothing is
added to your repository, and the tests are whatever CI already says they are.

**Your workflows must trigger on `agent/**` branches.** The agent pushes its
work to `agent/<card-id>`, and if no workflow runs there the ref has no checks
at all — which the control plane refuses rather than reading as a pass, because
"nothing failed" and "nothing ran" look identical and only one is safe.

```yaml
on:
  push:
    branches: ["main", "agent/**"]
```

The red gate compares the checks on your base ref with the checks on the agent
branch. The baseline costs nothing: those checks already ran when the commit
landed.

### `test-command`

For repositories with no CI to read. Commit an executable script at
`.strange-company/test-command` that runs the tests:

```sh
#!/bin/sh
set -eu
exec npm test
```

It must exist **at your base ref before the first card**, because the red gate
runs it there to establish a baseline. A missing script exits 64, which is
reported as a malformed test phase rather than a failing suite.

An empty suite must exit 0. The baseline has to be green for anything in the
candidate run to be attributable to the new tests.

### What the gates cannot check

The red gate proves the new tests fail, and that they fail because of the new
tests — the same command passes at the base ref. It cannot tell a correct
failing test from one that runs, fails, and asserts the wrong thing. Nothing
short of judgement can, and §11.3 forbids a model from grading.

### Agents never change CI

The runner refuses to commit anything under `.github/workflows`. The gates
derive their authority from those workflows, so an agent able to edit them
could weaken the checks that verify it. Give the control plane's GitHub token
`contents:write` **without** `workflows:write` — then it is not merely policy,
it is a credential that cannot do it.
