# Strange Company Helm Chart — Build Spec

> Source: task specification provided 2026-08-25. Reproduced here so the
> implementation plan can argue from it. Section numbering is the spec's own.

## Goal

Create a Helm chart named `strange-company` that can be:

1. built and fully exercised in CI;
2. installed into K3s with all dependencies included;
3. installed into an existing cluster using externally provided dependencies;
4. configured in hybrid mode, where any bundled component can be independently disabled;
5. consumed as a dependency of another Helm chart;
6. packaged and published as an OCI Helm chart.

This task is only Kubernetes packaging, prerequisites, and service plumbing.

**Do not implement:** Kanban business logic; agent orchestration; model routing;
retry or escalation logic; GitHub issue automation; coding-agent behavior;
PM/Scrum bot behavior; company workflow semantics.

Those belong in the control-plane application later. The purpose of this chart is
to create a clean deployment substrate for those applications.

## 1. Chart Layout

```
charts/strange-company/
├── Chart.yaml
├── Chart.lock
├── values.yaml
├── values.schema.json
├── templates/
│   ├── _helpers.tpl
│   ├── configmap.yaml
│   ├── secret.yaml
│   ├── serviceaccount.yaml
│   ├── postgresql-statefulset.yaml
│   ├── postgresql-service.yaml
│   ├── postgresql-secret.yaml
│   ├── hermes-deployment.yaml
│   ├── hermes-service.yaml
│   ├── hermes-pvc.yaml
│   ├── control-plane-deployment.yaml
│   ├── control-plane-service.yaml
│   ├── ingress.yaml
│   ├── tests/
│   │   └── test-connections.yaml
│   └── NOTES.txt
└── ci/
    ├── values-batteries.yaml
    ├── values-external.yaml
    ├── values-hybrid-a.yaml
    └── values-hybrid-b.yaml
```

Use Helm v3. Primary deployment target: Kubernetes / K3s. Do not make the chart
dependent on a particular cloud provider.

## 2. Naming

The project, chart, release examples, and resources use `strange-company`.

```yaml
apiVersion: v2
name: strange-company
description: Batteries-optional infrastructure for the Strange Company autonomous engineering system
type: application
version: 0.1.0
appVersion: "0.1.0"
```

OCI publication target: `oci://ghcr.io/OWNER/charts/strange-company`

## 3. Components

The chart wires together four conceptual services: `controlPlane`, `hermes`,
`vikunja`, `postgresql`.

```
                   Ingress
                ┌─────┴─────┐
                │           │
             Kanban      Hermes UI
                │           │
                └─────┬─────┘
                      │
                Control Plane
                 │          │
               SQL      Hermes API
```

The chart must support each dependency being either **bundled** or **external**,
where appropriate.

## 4. Batteries-Included Mode

Default installation should be capable of deploying PostgreSQL, Vikunja, Hermes
Agent and the Control Plane. Top-level switches:

```yaml
postgresql: { enabled: true }
vikunja:    { enabled: true }
hermes:     { enabled: true }
controlPlane: { enabled: true }
```

There is **no** required `global.batteriesIncluded` flag. The component-level
switches are authoritative. This permits arbitrary combinations without
introducing deployment modes into application logic.

## 5. Batteries-Optional Architecture

Every bundled component must have an external equivalent. The control plane
should receive the same logical environment configuration regardless of whether
the dependency is internal or external.

```yaml
postgresql:
  enabled: false
  external:
    host: postgres.database.svc
    port: 5432
    database: strange-company
    existingSecret: strange-company-db
    usernameKey: username
    passwordKey: password

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
    dashboardUrl: https://hermes.example.com
    existingSecret: strange-company-hermes
    apiKeyKey: api-key
```

Application code must not need to know whether the corresponding dependency is
bundled.

## 6. Required Installation Combinations

| Fixture   | PostgreSQL | Vikunja  | Hermes   | Control Plane |
|-----------|------------|----------|----------|---------------|
| batteries | bundled    | bundled  | bundled  | bundled       |
| external  | external   | external | external | bundled       |
| hybrid-a  | bundled    | bundled  | external | bundled       |
| hybrid-b  | external   | bundled  | bundled  | bundled       |

The chart architecture must not special-case these exact combinations. They are
simply test fixtures proving independent composition.

## 7. PostgreSQL — No Bitnami

Do not use Bitnami PostgreSQL, another PostgreSQL Helm subchart, or an embedded
PostgreSQL operator. When `postgresql.enabled: true` the chart renders its own
minimal PostgreSQL installation using the standard PostgreSQL container image:
a StatefulSet, a Service, a Secret, and a PVC through `volumeClaimTemplates`.

## 8. PostgreSQL Values

```yaml
postgresql:
  enabled: true
  image: { repository: postgres, tag: "17", pullPolicy: IfNotPresent }
  auth:
    username: strange-company
    database: strange-company
    password: ""
    existingSecret: ""
    usernameKey: username
    passwordKey: password
  service: { port: 5432 }
  persistence: { enabled: true, size: 8Gi, storageClass: "" }
  resources: {}
  external:
    host: ""
    port: 5432
    database: strange-company
    existingSecret: ""
    usernameKey: username
    passwordKey: password
```

Pin the tested PostgreSQL image version rather than using `latest` before release.

## 9. PostgreSQL Scope

Bundled PostgreSQL is intended for CI, development, demonstrations, homelabs,
small installations, and single-cluster deployments. It is intentionally one
StatefulSet / one server / one replica. It is **not** intended to provide HA,
automated failover, database operators, multi-region replication, managed
backups, or cloud database functionality.

## 10. PostgreSQL Persistence

Use a StatefulSet. When persistence is enabled, `volumeClaimTemplates` must
request storage using `postgresql.persistence.size` and `.storageClass`. An empty
storage class means "use the cluster default StorageClass". Do not hard-code
`local-path` even though K3s commonly provides it. When persistence is disabled,
use ephemeral storage suitable for CI.

## 11. Database Initialization

The bundled PostgreSQL instance must create the primary application database
`strange-company` with application user `strange-company`. Vikunja may use the
same PostgreSQL server but should use a logically separate database. Preferred
layout: databases `strange-company` and `vikunja`.

Do not run multiple PostgreSQL servers merely to separate the applications.
Initialization may use init scripts, a mounted ConfigMap, a Job, or another
simple deterministic mechanism. Keep initialization comprehensible and testable.
Do not build a database provisioning framework.

## 12. Vikunja

Use the official Vikunja Helm chart as an optional dependency, declared with a
condition:

```yaml
dependencies:
  - name: vikunja
    repository: oci://ghcr.io/go-vikunja/helm-chart
    version: "<PINNED_TESTED_VERSION>"
    condition: vikunja.enabled
```

Pin an exact tested chart version. Do not use a floating dependency version.

## 13. Bundled Vikunja

When `vikunja.enabled: true` the dependency must be configured to use the
*selected* PostgreSQL endpoint — bundled if PostgreSQL is bundled, external if
PostgreSQL is external. The fact that Vikunja is bundled must not imply that
PostgreSQL is bundled. These switches are independent.

## 14. External Vikunja

When `vikunja.enabled: false` render no bundled Vikunja workloads. The control
plane should receive the resolved endpoint through a stable configuration
contract: `VIKUNJA_URL`, `VIKUNJA_TOKEN`.

## 15. Hermes

Do not include Hermes through another Helm chart. Render Hermes directly from
`strange-company` using the Hermes Agent container image.

```yaml
hermes:
  enabled: true
  image: { repository: nousresearch/hermes-agent, tag: "<PINNED>", pullPolicy: IfNotPresent }
  gateway: { port: 8642 }
  dashboard: { enabled: true, port: 9119 }
  persistence: { enabled: true, size: 2Gi, storageClass: "" }
  existingSecret: ""
  env: {}
  resources: {}
  ingress: { enabled: false, className: "", annotations: {}, host: "", tls: [] }
```

Pin a known-working Hermes image version before release. Do not use `latest`.

## 16. Hermes Deployment

Create `hermes-deployment.yaml`, `hermes-service.yaml`, `hermes-pvc.yaml`.
Ports: `8642` gateway/API, `9119` dashboard. Persistent Hermes state lives under
`/opt/data`; mount the optional PVC there.

## 17. Hermes Persistence

`hermes.persistence.{enabled,size,storageClass}`. Disabled means ephemeral. Empty
`storageClass` must use the cluster default. Do not hard-code a K3s-specific
storage class.

## 18. Hermes Credentials

Do not require AI provider credentials merely to `helm template` the chart.
Credentials should normally be supplied via `hermes.existingSecret`. For
development, optionally support explicit values that cause the chart to create a
Secret. Prefer existing Secrets for real installations. The chart should not need
to understand provider-specific business logic — it only provides
environment/configuration plumbing.

## 19. External Hermes

When `hermes.enabled: false` render no Hermes Deployment, Service, PVC, or
Ingress. Use `hermes.external.{gatewayUrl,dashboardUrl,existingSecret,apiKeyKey}`.
The control plane receives the same contract regardless: `HERMES_GATEWAY_URL`,
`HERMES_API_KEY`.

## 20. Control Plane

The chart does not implement the control-plane application. It only deploys an
image supplied by the project.

```yaml
controlPlane:
  enabled: true
  image: { repository: ghcr.io/OWNER/strange-company-control-plane, tag: "latest", pullPolicy: IfNotPresent }
  replicas: 1
  service: { type: ClusterIP, port: 8080 }
  existingSecret: ""
  env: {}
  resources: {}
  ingress: { enabled: false, className: "", annotations: {}, host: "", tls: [] }
```

For a release, pin a real application version. For CI, the image may be replaced
with a fixture.

## 21. Stable Control-Plane Configuration Contract

The control plane must receive stable environment variables independent of
deployment topology. At minimum:

```
DATABASE_HOST  DATABASE_PORT  DATABASE_NAME  DATABASE_USER  DATABASE_PASSWORD
VIKUNJA_URL    VIKUNJA_TOKEN
HERMES_GATEWAY_URL  HERMES_API_KEY
```

Optional: `HERMES_DASHBOARD_URL`. Bundled/external switching must happen in Helm
templates, not application code.

## 22. Endpoint Resolution Helpers

Put endpoint resolution in `templates/_helpers.tpl`:

```
strange-company.databaseHost / databasePort / databaseName
strange-company.vikunjaUrl
strange-company.hermesGatewayUrl / hermesDashboardUrl
```

Behavior: if component enabled → return internal Kubernetes service; else return
external configured endpoint. Do not duplicate this condition logic across
Deployments. One resolution function per dependency is preferred.

## 23. ConfigMap

Create a ConfigMap for non-secret control-plane configuration (`DATABASE_HOST`,
`DATABASE_PORT`, `DATABASE_NAME`, `VIKUNJA_URL`, `HERMES_GATEWAY_URL`,
`HERMES_DASHBOARD_URL`). Credentials remain in Secrets. The control-plane
Deployment may use `envFrom` with `configMapRef` and `secretRef`.

## 24. Secrets

Support two credential patterns:

- **Existing Secret (preferred):** `controlPlane.existingSecret: strange-company-secrets`
- **Chart-created Secret:** allowed for CI, demos, development, batteries-included installs.

Do not generate credentials in a way that changes passwords on every
`helm upgrade`. If random credential generation is implemented, preserve an
existing Secret using Helm's `lookup`. Alternatively, require users to specify
values. Either behavior is acceptable if installs are deterministic and upgrades
do not unexpectedly rotate credentials.

## 25. Service Accounts

Create a normal ServiceAccount for the control plane. Do not grant
`cluster-admin`, arbitrary Secret access, or broad Kubernetes write access. For
this chart-only phase the control plane does not need permission to spawn
coding-agent Jobs — do not preemptively add those permissions. Hermes should not
receive Kubernetes API credentials merely because it runs in Kubernetes. Where
possible use `automountServiceAccountToken: false`.

## 26. Services

Use `ClusterIP` by default. Expected internal services:
`strange-company-control-plane`, `strange-company-hermes`,
`strange-company-postgresql`. Vikunja's service naming follows the bundled
dependency. Avoid depending on release-name assumptions outside helper functions.

## 27. Ingress

Support ingress independently for `controlPlane`, the Hermes dashboard, and
Vikunja. Default `enabled: false`. Do not require ingress for CI. Do not assume
K3s Traefik annotations. Use standard configurable fields (`className`,
`annotations`, `hosts`, `tls`). Users may configure Traefik, nginx, HAProxy, or
another ingress controller.

## 28. Storage

All chart-owned storage must permit `storageClass: ""` meaning "use the cluster's
default StorageClass". Do not encode `local-path` into chart logic. Chart-owned
persistent components: PostgreSQL, Hermes. Vikunja storage is configured through
its dependency values.

## 29. Resource Configuration

Every chart-owned Deployment/StatefulSet must accept `resources: {}`. Do not
invent aggressive default limits. CI fixture values may provide small explicit
limits. Support `nodeSelector`, `tolerations`, `affinity` for chart-owned
workloads where straightforward. Do not turn the initial chart into a generic
enterprise platform framework.

## 30. Image Pull Secrets

Support `imagePullSecrets` at global or component scope. Prefer a simple global
value if that satisfies all chart-owned workloads: `global.imagePullSecrets: []`.

## 31. values.yaml

`values.yaml` should be understandable without reading templates. Comment the
important values. Avoid hundreds of decorative options.

## 32. values.schema.json

Provide JSON-schema validation. At minimum:

- If `postgresql.enabled: false`, require usable external values including
  `host`, `port`, `database`, `existingSecret`.
- If `vikunja.enabled: false`, require `external.url`.
- If `hermes.enabled: false`, require `external.gatewayUrl`.
- Validate ports are valid; replica counts are nonnegative/valid; persistence
  sizes are strings; an enabled control plane has an image repository;
  malformed obvious configuration fails early.

Do not attempt to model every possible nested Vikunja dependency value. Validate
the integration seams.

## 33. CI Overview

Use GitHub Actions. The chart must be testable before the actual Strange Company
control plane exists. Stages: static Helm validation → Helm unit tests → K3s
integration test → lifecycle test → package test. No real AI provider calls are
made in CI.

## 34. Static Helm Validation

`helm dependency build`, `helm lint`, render all four fixtures, and validate
rendered manifests using `kubeconform` or equivalent.

## 35–38. Helm Unit Tests

Use `helm-unittest`.

- **Batteries included:** assert the bundled configuration renders the PostgreSQL
  StatefulSet and Service, Hermes Deployment/Service/PVC, Control Plane
  Deployment and Service; assert the Vikunja dependency is enabled; assert
  control-plane configuration references internal service endpoints.
- **External mode:** assert no PostgreSQL StatefulSet/Service, no Hermes
  Deployment/Service/PVC; assert the control-plane configuration contains the
  supplied external endpoints.
- **Hybrid modes:** test bundled PG + bundled Vikunja + external Hermes, and
  external PG + bundled Vikunja + bundled Hermes. Verify each dependency resolves
  to the correct internal or external endpoint independently.

## 39. CI K3s Integration Environment

Use `k3d` rather than only Kind — the production target is K3s.

## 40. CI Fixture Control Plane

Do not make chart development depend on the actual control-plane application. For
CI, override `controlPlane.image` with a small fixture image. Required:
`GET /healthz → 200`, `GET /readyz → 200`. The fixture may also expose
environment values for integration assertions. Do not put company logic into the
test fixture.

## 41. Integration Readiness

Wait for bundled workloads using Kubernetes readiness (`kubectl wait
--for=condition=Available deployment --all`), and wait appropriately for
PostgreSQL StatefulSet readiness. Do not rely exclusively on arbitrary `sleep`.

## 42. Helm Test Pod

Provide `templates/tests/test-connections.yaml` using Helm's test hook. Verify
basic internal networking: resolve and connect to the PostgreSQL TCP port; the
Hermes gateway port; the Vikunja HTTP port; and `GET /healthz` on the control
plane. The purpose is plumbing validation. Do not make a real model request.

## 43. Database Integration Check

In batteries-included CI: PostgreSQL must accept a connection; application
credentials must work; required databases must exist. If Vikunja initialization
creates its own schema, wait until it becomes ready before considering the test
successful.

## 44. Hermes Integration Check

CI must prove: Hermes Pod Ready; Hermes Service has endpoints; the gateway
TCP/HTTP endpoint is reachable; the dashboard endpoint is reachable when enabled.
Do not require Anthropic/OpenAI/DeepSeek credentials or a successful model
generation.

## 45. Vikunja Integration Check

CI must prove: Vikunja workload Ready; Vikunja Service has endpoints; HTTP
endpoint reachable. Do not require end-user workflow configuration.

## 46. Lifecycle Test

CI must exercise install → upgrade → uninstall. The upgrade must perform a real
value change (e.g. `controlPlane.replicas: 1 → 2`) and the new state must be
verified. Uninstall must remove chart-owned namespaced workloads. Do not require
automatic deletion of persistent data unless Kubernetes/Helm semantics naturally
do so.

## 47. Upgrade Safety

Upgrading must not unexpectedly regenerate database passwords, destroy PostgreSQL
storage, replace PVCs, rotate Hermes credentials, switch bundled dependencies to
external, or overwrite externally managed Secrets. Stable names and selectors
matter. Treat upgrade compatibility as a first-class requirement.

## 48. Packaging

`helm package charts/strange-company` → `strange-company-X.Y.Z.tgz`. Validate the
packaged chart can itself be installed into the CI K3s cluster. Do not test only
the source directory.

## 49. OCI Publishing

Tagged releases should support publishing to `oci://ghcr.io/OWNER/charts` via
`helm push`.

## 50. Use as a Dependency

`strange-company` must be usable as a dependency of another Helm chart. Choose and
document the exact parent value convention that Helm requires. The requirement is
composability, not a specific alias syntax.

## 51. Dependency Pinning

Before merging: pin the Vikunja chart version, PostgreSQL image, Hermes image, and
CI tool versions where practical; commit `Chart.lock`. CI should detect accidental
dependency drift.

## 52. Security Baseline

Chart-owned containers should run as non-root where their upstream images support
it; avoid privileged mode, host networking, hostPath, cluster-admin, and mounting
service-account tokens unless required. Do not add capabilities without
demonstrated need.

## 53. Network Policy

NetworkPolicy support is optional for the initial implementation. If implemented,
it should be behind `networkPolicy.enabled: false`.

## 54. Health Checks

Chart-owned services should have appropriate readiness/liveness checks where
supported. Control plane: `/healthz`, `/readyz`. Hermes probes should use known
service behavior appropriate to its gateway/dashboard. PostgreSQL should use
`pg_isready` or equivalent. Do not invent an HTTP probe for a service that does
not expose one.

## 55. NOTES.txt

On successful install, print useful commands: control plane, Vikunja, Hermes
gateway and dashboard URLs (internal or external), plus port-forward examples when
ingress is disabled. Avoid dumping Secrets.

## 56. CI Fixtures

- `values-batteries.yaml` — bundled PG/Vikunja/Hermes, fixture control plane,
  ephemeral or small CI storage, no ingress, no real AI credentials.
- `values-external.yaml` — PG/Vikunja/Hermes disabled with dummy but valid
  external configuration sufficient for rendering/unit tests.
- `values-hybrid-a.yaml` — PG bundled, Vikunja bundled, Hermes external.
- `values-hybrid-b.yaml` — PG external, Vikunja bundled, Hermes bundled.

## 57–58. GitHub Actions

`.github/workflows/helm.yml` with jobs `lint → unit → integration → package`. A
release workflow may separately publish OCI artifacts. The behavior, not the exact
command list, is the requirement.

## 59. Explicit Non-Goals

Kanban card schema; card state machine; GitHub issue ingestion; atomic card
claims; Hermes worker spawning; Meeseeks behavior; coding-agent Kubernetes Jobs;
Claude Code; Codex; Superpowers integration; model routing; test-first agent
loops; model retries; model cost accounting; PM bot; Scrum bot; autonomous pull
requests; human review workflow.

## 60. Implementation Philosophy

Keep the chart boring. Prefer Deployment / StatefulSet / Service / Secret /
ConfigMap / PVC / Ingress / Helm dependency over elaborate abstractions. The
important architectural property is substitutability. The chart owns plumbing; the
control plane will later own behavior.

## 61. Definition of Done

1. The chart is named `strange-company`.
2. `helm dependency build` succeeds.
3. `helm lint` succeeds.
4. `values.schema.json` rejects materially invalid external-dependency configurations.
5. All dependency/image versions used for release are pinned.
6. `Chart.lock` is committed.
7. Batteries-included configuration renders successfully.
8. External-dependency configuration renders successfully.
9. Hybrid A renders successfully.
10. Hybrid B renders successfully.
11. All rendered fixtures pass Kubernetes schema validation.
12. Helm unit tests pass.
13. Bundled PostgreSQL uses the standard PostgreSQL container and chart-owned resources.
14. No Bitnami PostgreSQL dependency exists.
15. Bundled PostgreSQL starts successfully in K3s.
16. PostgreSQL credentials survive a Helm upgrade.
17. PostgreSQL persistent storage is not recreated during a normal upgrade.
18. Bundled Vikunja starts successfully.
19. Bundled Vikunja can use bundled PostgreSQL.
20. Bundled Vikunja can use external PostgreSQL.
21. Bundled Hermes starts successfully.
22. Hermes gateway Service has working endpoints.
23. Hermes dashboard Service has working endpoints when enabled.
24. Hermes can run with persistent storage.
25. Hermes persistence can be disabled for CI.
26. No real AI provider request is required during CI.
27. External Hermes configuration renders no Hermes workload.
28. External Vikunja configuration renders no bundled Vikunja workload.
29. External PostgreSQL configuration renders no PostgreSQL workload.
30. Each dependency can be switched independently between bundled and external.
31. The control-plane Deployment receives the same logical configuration variables in all modes.
32. The chart can be installed into a disposable K3s cluster using k3d.
33. All bundled services become Ready.
34. The Helm test Pod can resolve and contact bundled PostgreSQL.
35. The Helm test Pod can resolve and contact bundled Vikunja.
36. The Helm test Pod can resolve and contact the Hermes gateway.
37. The Helm test Pod can contact the fixture control plane.
38. `helm upgrade` succeeds against the installed chart.
39. A real configuration change is observed after upgrade.
40. `helm uninstall` succeeds.
41. The chart can be packaged into a `.tgz`.
42. The packaged `.tgz`, not only the source directory, installs successfully.
43. The chart is structured so it can be pushed to an OCI registry.
44. The resulting OCI artifact can be referenced as a Helm dependency.
45. The chart contains no business logic for the future autonomous company.
