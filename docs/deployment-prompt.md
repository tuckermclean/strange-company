# Deployment brief: strange-company Helm chart

> Drop-in context for an agent or operator deploying this chart to a K3s
> cluster. Everything below was verified in CI against k3d/K3s v1.34.10-k3s1
> for **chart 0.1.0**, published and publicly pullable at
> `oci://ghcr.io/tuckermclean/charts/strange-company`.

## What you are deploying

One Helm chart, `strange-company`, that stands up four services:

| Component      | Kind                        | Service name                    | Port(s)      |
|----------------|-----------------------------|---------------------------------|--------------|
| PostgreSQL     | StatefulSet (chart-owned)   | `strange-company-postgresql`    | 5432         |
| Vikunja        | Deployment (Helm dependency)| `strange-company-vikunja`       | 3456         |
| Hermes Agent   | Deployment (chart-owned)    | `strange-company-hermes`        | 8642, 9119   |
| Control Plane  | Deployment (chart-owned)    | `strange-company-control-plane` | 8080         |

Service names assume the release is named `strange-company`. It is packaging
only — no application logic ships in this chart.

PostgreSQL, Vikunja and Hermes are each **independently** switchable between
bundled and external. Set `<component>.enabled: false` and fill in
`<component>.external.*`. There is no global mode flag.

## Cluster prerequisites

- Kubernetes **>= 1.25** (chart declares `kubeVersion: ">=1.25.0-0"`).
- A **default StorageClass**. The chart never names one; leaving
  `storageClass: ""` means "cluster default". K3s's built-in `local-path`
  works as-is.
- Three PVCs in the default configuration: PostgreSQL 8Gi, Hermes 2Gi,
  Vikunja 2Gi. All ReadWriteOnce. Shrink via values for small clusters.
- **No ingress controller is required.** All Services are ClusterIP and every
  ingress defaults to disabled.
- Outbound registry access to `docker.io` and (for the control-plane image)
  `ghcr.io`.

### Sizing

The Hermes image is **large — roughly 900 MB compressed across 38 layers**.
Budget several minutes for the first pull and set a generous
`helm install --wait --timeout` (CI uses 20m). Hermes wants ~1 GiB RAM to be
comfortable; PostgreSQL, Vikunja and the control plane are modest.

### Pod Security Admission — read this one

Hermes will **not** run under a `restricted` PodSecurity policy. Its image uses
an s6-overlay init that starts as root, chowns `/opt/data`, and then drops the
application to uid 10000. That requires five capabilities:
`CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETGID`, `SETUID`.

If the target namespace enforces `restricted`, the Hermes pod is rejected.
Label the namespace `baseline` (or leave PSA unset):

```bash
kubectl label namespace strange-company \
  pod-security.kubernetes.io/enforce=baseline --overwrite
```

Do **not** try to "fix" this by setting `runAsUser` on the Hermes container:
the upstream image explicitly refuses to start under an arbitrary UID and
tells you to pass `HERMES_UID`/`HERMES_GID` instead. Everything else in the
chart runs non-root with all capabilities dropped.

## Pinned versions

| Artifact              | Version                                    |
|-----------------------|--------------------------------------------|
| Vikunja Helm chart    | `2.2.1` from `oci://ghcr.io/go-vikunja/helm-chart` |
| PostgreSQL image      | `postgres:17.11`                           |
| Vikunja image         | `vikunja/vikunja:2.5.0`                    |
| Hermes image          | `nousresearch/hermes-agent:v2026.8.19`     |
| Helm                  | v3 (tested on v3.21.4)                     |
| **This chart**        | `oci://ghcr.io/tuckermclean/charts/strange-company` `0.1.0` |

`Chart.lock` is committed. `helm dependency build` is only needed when
installing from a source checkout; the published artifact already contains the
Vikunja dependency.

## Install

Prepare the namespace once (see the PodSecurity note above):

```bash
kubectl create namespace strange-company
kubectl label namespace strange-company \
  pod-security.kubernetes.io/enforce=baseline --overwrite
```

### From the registry (normal path)

No checkout required:

```bash
helm install strange-company \
  oci://ghcr.io/tuckermclean/charts/strange-company \
  --version 0.1.0 \
  --namespace strange-company \
  --values my-values.yaml \
  --wait --timeout 20m
```

The package is public, so no `helm registry login` is needed to pull it.

### From a source checkout (chart development)

```bash
helm dependency build charts/strange-company
helm install strange-company charts/strange-company \
  --namespace strange-company --values my-values.yaml \
  --wait --timeout 20m
```

### You must supply a control-plane image

**The chart's default `controlPlane.image.repository` is a placeholder that
does not exist yet.** Installing without overriding it gives you three healthy
services and a control-plane pod stuck in `ImagePullBackOff`. Point it at a
real image:

```yaml
# my-values.yaml
controlPlane:
  image:
    repository: ghcr.io/tuckermclean/strange-company-control-plane
    tag: "0.1.0"
```

That image must serve `GET /healthz` and `GET /readyz` on port 8080.

### Smoke-testing before the control plane exists

To validate the substrate on its own, substitute the same fixture CI uses. It
answers 200 on every path, which satisfies both probes:

```yaml
controlPlane:
  image:
    repository: traefik/whoami
    tag: "v1.12.0"
  args: ["--port", "8080"]
```

This is exactly `charts/strange-company/ci/values-batteries.yaml`, which is the
configuration the integration job installs and `helm test`s on every commit.

## Credentials

Nothing here requires an AI provider key to install or to pass its own tests.

- **PostgreSQL password** — generated on first install and preserved across
  upgrades by reading the existing Secret back. Override with
  `postgresql.auth.password` or `postgresql.auth.existingSecret`.
- **Hermes API key** — the Hermes API server refuses to start without
  `API_SERVER_KEY`. The chart generates one (also preserved across upgrades)
  and hands the *same* value to the control plane as `HERMES_API_KEY`. This is
  the chart's own shared secret, not a model provider credential.
- **Hermes dashboard** — its auth gate fails closed on any non-loopback bind,
  so basic-auth credentials are mandatory for port 9119 to listen at all. The
  chart generates a password; the username defaults to `hermes`. Read it with:
  ```bash
  kubectl -n strange-company get secret strange-company-secrets \
    -o jsonpath='{.data.hermes-dashboard-password}' | base64 -d
  ```
- **AI provider credentials** — optional. Supply them in your own Secret and
  set `hermes.existingSecret`; it is mounted with `envFrom`.
- **Vikunja token** — Vikunja has no bootstrap-token API, so a fresh install
  has none. Create one in the Vikunja UI and set `vikunja.token`, or provide
  `controlPlane.existingSecret`. The control plane's `VIKUNJA_TOKEN` reference
  is marked optional so nothing blocks startup meanwhile.

## The control-plane contract

The control-plane container always receives these, regardless of which
dependencies are bundled:

```
DATABASE_HOST  DATABASE_PORT  DATABASE_NAME     (ConfigMap)
DATABASE_USER  DATABASE_PASSWORD                (Secret ref)
VIKUNJA_URL                                     (ConfigMap)
VIKUNJA_TOKEN                                   (Secret ref, optional)
HERMES_GATEWAY_URL  HERMES_DASHBOARD_URL        (ConfigMap)
HERMES_API_KEY                                  (Secret ref, optional)
```

It must serve `GET /healthz` and `GET /readyz` on its service port (8080) —
those back the liveness and readiness probes.

## Two sharp edges

1. **The doubled `vikunja` key.** `vikunja.enabled`, `vikunja.external` and
   `vikunja.databaseSecretName` are this chart's. Everything under
   `vikunja.vikunja` goes to the upstream chart. Never add a `vikunja.ingress`
   key — it collides with the upstream chart's named-ingress map and breaks its
   renderer. Configure Vikunja's ingress at `vikunja.vikunja.ingress.main`.
   The chart fails the render with an explanation if you get this wrong.

2. **Vikunja's DB Secret name is a literal.** The upstream chart does not run
   `tpl` over `secretKeyRef` names. With bundled PostgreSQL the chart creates
   the Secret (`vikunja-db` by default) for you — which means **one release per
   namespace** unless you rename it. With external PostgreSQL, point both
   `vikunja.databaseSecretName` and
   `vikunja.vikunja.env.VIKUNJA_DATABASE_PASSWORD.valueFrom.secretKeyRef.name`
   at your own Secret, and make sure the `vikunja` database already exists on
   that server.

## Consuming it from another chart

The published artifact resolves as a normal Helm dependency — the release
workflow proves this by running `helm dependency build` against
`examples/parent-chart` after every push.

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
  enabled: true          # parent-level: satisfies `condition:` ONLY

strange-company:         # must match the dependency name (or its alias)
  postgresql:
    enabled: false
    external:
      host: my-postgres.databases.svc.cluster.local
      port: 5432
      database: strange-company
      existingSecret: acme-db-credentials
```

The `condition` key and the subchart value block are different things. Helm
reads conditions from the parent's own values, so `strangeCompany.enabled` must
sit **outside** the `strange-company:` block. Putting it inside silently
disables the dependency.

## Verify

```bash
kubectl -n strange-company wait --for=condition=Available deployment --all --timeout=300s
kubectl -n strange-company rollout status statefulset/strange-company-postgresql
helm test strange-company -n strange-company --logs
```

The test pod resolves and connects to every bundled Service and does
`GET /healthz` on the control plane. It never makes a model request.

Expect two databases on the bundled server, `strange-company` and `vikunja`,
created by an init script mounted into `/docker-entrypoint-initdb.d`:

```bash
kubectl -n strange-company exec strange-company-postgresql-0 -- \
  psql -U strange-company -d strange-company -tAc \
  "SELECT datname FROM pg_database ORDER BY 1"
```

## Upgrades

```bash
helm upgrade strange-company \
  oci://ghcr.io/tuckermclean/charts/strange-company \
  --version 0.1.0 -n strange-company \
  --values my-values.yaml --wait --timeout 20m
```

`helm upgrade` is safe to run: it does not rotate the database password or the
Hermes credentials, and it does not replace the PostgreSQL PVC. CI asserts all
three by comparing the stored values and the PVC UID before and after.

Chart versions are immutable in the registry, so bump `--version` to move
between releases rather than expecting `0.1.0` to change under you.

## Reaching the services without ingress

```bash
kubectl -n strange-company port-forward svc/strange-company-control-plane 8080:8080
kubectl -n strange-company port-forward svc/strange-company-hermes 9119:9119
kubectl -n strange-company port-forward svc/strange-company-vikunja 3456:3456
```

To expose them properly, enable the per-component ingress blocks
(`controlPlane.ingress`, `hermes.ingress`) with your controller's
`className`/`annotations`. No Traefik-specific annotations are assumed, so K3s's
bundled Traefik works but is not required.
