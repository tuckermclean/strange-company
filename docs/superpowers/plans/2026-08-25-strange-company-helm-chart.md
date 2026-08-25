# Strange Company Helm Chart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task-by-task.
> Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a `strange-company` Helm chart that deploys PostgreSQL, Vikunja,
Hermes Agent and a Control Plane, where each of the three dependencies is
independently switchable between bundled and external, proven by GitHub Actions
CI against a disposable k3d/K3s cluster.

**Architecture:** One application chart owning plain Kubernetes primitives
(StatefulSet, Deployment, Service, Secret, ConfigMap, PVC, Ingress) plus exactly
one conditional Helm dependency (Vikunja). All bundled-vs-external branching is
concentrated in `templates/_helpers.tpl` resolution functions; every consumer
template calls a helper and never re-tests `.enabled`. The control plane receives
an identical environment contract in all four topologies via a ConfigMap
(non-secret endpoints) plus `env` entries with `secretKeyRef` (credentials).

**Tech Stack:** Helm v3.21.4, helm-unittest v1.1.2, kubeconform v0.8.0,
k3d v5.9.0 (K3s v1.34.10-k3s1), GitHub Actions, GHCR/OCI.

**Spec:** `docs/specs/strange-company-helm-chart.md`

## Verification Environment

**This workstation has no `helm` and no `k3d`, and they will not be installed.**
All verification happens in GitHub Actions on `tuckermclean/strange-company`
(public). The loop is: commit → push → `gh run watch` → read failures → fix.
Treat a red CI run as the test-failure signal that TDD would normally give
locally. Never claim a Definition-of-Done item passes without a green job that
exercised it.

## Global Constraints

Exact values, copied from the spec or resolved by inspecting the real upstream
artifacts. Every task inherits these.

- Chart name `strange-company`; `apiVersion: v2`; `type: application`;
  `version: 0.1.0`; `appVersion: "0.1.0"`.
- Description: `Batteries-optional infrastructure for the Strange Company autonomous engineering system`
- Helm **v3** (not v4). Target K3s. No cloud-provider-specific resources.
- OWNER is `tuckermclean`. OCI target `oci://ghcr.io/tuckermclean/charts`.
- **No Bitnami**, no PostgreSQL subchart, no PostgreSQL operator (spec §7, DoD 14).
- Exactly one Helm dependency: Vikunja, conditional.
- **Pinned versions (do not float):**
  - Vikunja chart `2.2.1` from `oci://ghcr.io/go-vikunja/helm-chart`
    (verified present; layer digest
    `sha256:1ed4ef76435b5c6d24ae0de6af23e09a384c65fafc3e93b3fb818e47b9b7d170`)
  - PostgreSQL image `postgres:17.11` (Debian variant; `postgres` uid/gid **999**)
  - Hermes image `nousresearch/hermes-agent:v2026.8.19`
  - CI fixture control plane `traefik/whoami:v1.12.0`
  - helm `v3.21.4`, helm-unittest `v1.1.2`, kubeconform `v0.8.0` (schemas for
    Kubernetes `1.34.0`), k3d `v5.9.0`, k3s `v1.34.10-k3s1`
- No component-level `batteriesIncluded` mode flag. `<component>.enabled` is
  authoritative (spec §4).
- Storage: `storageClass: ""` always means "cluster default" — emit **no**
  `storageClassName` key at all in that case. Never hard-code `local-path`.
- Security baseline for chart-owned pods: `runAsNonRoot: true`,
  `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`,
  `seccompProfile: RuntimeDefault`, `automountServiceAccountToken: false`,
  no hostPath / hostNetwork / privileged.
- No AI provider credentials may be required to `helm template` or to pass CI.

## Upstream Facts (verified by pulling the chart — do not re-derive)

The Vikunja chart `2.2.1` wraps [bjw-s common `1.5.1`], **vendored inside the
chart tarball**, so `helm dependency build` needs no extra repository.

Its `templates/vikunja.yaml` does:
```
{{- $_ := get .Values "vikunja" | mergeOverwrite $ctx.Values -}}
```
so the subchart's own values live under a nested `vikunja:` key. **From this
parent chart the value path is therefore `vikunja.vikunja.*`** — e.g.
`vikunja.vikunja.env.VIKUNJA_DATABASE_TYPE`. Our own `vikunja.enabled` and
`vikunja.external` keys sit alongside at `vikunja.*` and are ignored by the
subchart (bjw-s common ships no `values.schema.json`).

- Service name resolves via `bjw-s.common.lib.chart.names.fullname`: since
  release `strange-company` does not contain the string `vikunja`, the Service is
  **`<release>-vikunja`**, port **3456**, name `http`, type `ClusterIP`.
- Chart default `vikunja.ingress.main.enabled: true` with host `vikunja.local` —
  **must be overridden to `false`** in our `values.yaml`.
- Chart default DB is SQLite with a `database` PVC — **must be disabled** when
  pointing at PostgreSQL.
- `env` values accept either a scalar or a map with `valueFrom`, so
  `secretKeyRef` for the DB password works.
- Vikunja's Go code splits `VIKUNJA_DATABASE_HOST` on `:` to obtain the port;
  there is no separate port variable. Pass `host:port`.
- `serviceAccount.create` defaults to `false`; `automountServiceAccountToken`
  defaults to `true` and must be set `false`.

## File Structure

```
charts/strange-company/
├── Chart.yaml                      # metadata + single conditional Vikunja dep
├── Chart.lock                      # generated by CI, committed
├── values.yaml                     # commented, understandable without templates
├── values.schema.json              # validates the integration seams only
├── templates/
│   ├── _helpers.tpl                # names, labels, and ALL endpoint resolution
│   ├── configmap.yaml              # non-secret control-plane contract
│   ├── secret.yaml                 # chart-created creds, lookup-preserving
│   ├── serviceaccount.yaml         # control-plane SA, no RBAC
│   ├── postgresql-secret.yaml      # bundled PG credentials, lookup-preserving
│   ├── postgresql-statefulset.yaml # incl. init ConfigMap volume for 2nd DB
│   ├── postgresql-service.yaml
│   ├── postgresql-init-configmap.yaml   # creates the `vikunja` database
│   ├── hermes-deployment.yaml
│   ├── hermes-service.yaml
│   ├── hermes-pvc.yaml
│   ├── hermes-secret.yaml          # only when inline creds are supplied
│   ├── control-plane-deployment.yaml
│   ├── control-plane-service.yaml
│   ├── ingress.yaml                # control plane + hermes dashboard + vikunja
│   ├── networkpolicy.yaml          # behind networkPolicy.enabled=false
│   ├── NOTES.txt
│   └── tests/
│       └── test-connections.yaml   # helm test hook
├── ci/
│   ├── values-batteries.yaml
│   ├── values-external.yaml
│   ├── values-hybrid-a.yaml
│   └── values-hybrid-b.yaml
└── tests/                          # helm-unittest suites
    ├── batteries_test.yaml
    ├── external_test.yaml
    ├── hybrid_test.yaml
    ├── helpers_test.yaml
    └── schema_test.yaml
examples/parent-chart/              # proves §50 composability
├── Chart.yaml
└── values.yaml
.github/workflows/helm.yml          # lint → unit → integration → package
.github/workflows/release.yml       # tag → OCI push
```

Rationale for the two additions beyond the spec's tree:
`postgresql-init-configmap.yaml` is the "mounted initialization ConfigMap"
option that spec §11 explicitly allows, and it is the only chart-owned way to
create the second `vikunja` database. `hermes-secret.yaml` implements the
"optionally support explicit values" half of spec §18. `networkpolicy.yaml` is
spec §53's optional, default-off item.

---

### Task 1: Repository skeleton, Chart.yaml, and the CI workflow that proves nothing yet

**Files:**
- Create: `.gitignore`, `charts/strange-company/Chart.yaml`,
  `charts/strange-company/.helmignore`, `.github/workflows/helm.yml`
- Create: `README.md`

**Interfaces:**
- Produces: chart directory path `charts/strange-company` used by every later
  task and every CI job; workflow job names `lint`, `unit`, `integration`,
  `package`.

- [ ] **Step 1: Write `Chart.yaml` with the pinned Vikunja dependency**

```yaml
apiVersion: v2
name: strange-company
description: Batteries-optional infrastructure for the Strange Company autonomous engineering system
type: application
version: 0.1.0
appVersion: "0.1.0"
kubeVersion: ">=1.25.0-0"
home: https://github.com/tuckermclean/strange-company
sources:
  - https://github.com/tuckermclean/strange-company
keywords: [strange-company, vikunja, hermes, postgresql, control-plane]
maintainers:
  - name: Tucker McLean
dependencies:
  - name: vikunja
    repository: oci://ghcr.io/go-vikunja/helm-chart
    version: "2.2.1"
    condition: vikunja.enabled
```

- [ ] **Step 2: Write the CI workflow with all four jobs**

`lint` installs helm + kubeconform, runs `helm dependency build`, `helm lint`,
renders the four fixtures and pipes each through
`kubeconform -strict -summary -kubernetes-version 1.34.0 -schema-location default
-schema-location 'https://raw.githubusercontent.com/yannh/kubernetes-json-schema/master/{{.NormalizedKubernetesVersion}}-standalone-strict/{{.ResourceKind}}{{.KindSuffix}}.json'`.
`lint` also uploads `Chart.lock` as an artifact so it can be committed.
`unit` runs `helm unittest charts/strange-company`.
`integration` creates a k3d cluster and runs install/test/upgrade/uninstall.
`package` runs `helm package` and installs the resulting `.tgz`.

- [ ] **Step 3: Commit and push; confirm the workflow is picked up**

```bash
git add -A && git commit -m "chore: chart skeleton and CI workflow"
git push -u origin main
gh run watch
```

Expected: `lint` **fails** — there is no `values.yaml` and no templates yet.
That red run is the baseline.

---

### Task 2: Helper functions — the single place bundled/external is decided

**Files:**
- Create: `charts/strange-company/templates/_helpers.tpl`
- Test: `charts/strange-company/tests/helpers_test.yaml`

**Interfaces:**
- Produces, consumed by every later template:
  - `strange-company.name`, `strange-company.fullname`, `strange-company.chart`
  - `strange-company.labels`, `strange-company.selectorLabels` (take a dict
    `(dict "ctx" $ "component" "hermes")`)
  - `strange-company.serviceAccountName`
  - `strange-company.postgresql.fullname` → `<fullname>-postgresql`
  - `strange-company.hermes.fullname` → `<fullname>-hermes`
  - `strange-company.controlPlane.fullname` → `<fullname>-control-plane`
  - `strange-company.vikunja.fullname` → `<release>-vikunja`
  - `strange-company.databaseHost`, `.databasePort`, `.databaseName`,
    `.databaseUser`
  - `strange-company.databaseSecretName`, `.databasePasswordKey`,
    `.databaseUserKey`
  - `strange-company.vikunjaUrl`, `.vikunjaSecretName`, `.vikunjaTokenKey`
  - `strange-company.hermesGatewayUrl`, `.hermesDashboardUrl`,
    `.hermesSecretName`, `.hermesApiKeyKey`
  - `strange-company.imagePullSecrets`

- [ ] **Step 1: Write the failing unit test first**

```yaml
suite: endpoint resolution helpers
templates:
  - templates/configmap.yaml
tests:
  - it: resolves the internal database host when postgresql is bundled
    set:
      postgresql.enabled: true
    asserts:
      - equal:
          path: data.DATABASE_HOST
          value: RELEASE-NAME-strange-company-postgresql
  - it: resolves the external database host when postgresql is external
    set:
      postgresql.enabled: false
      postgresql.external.host: postgres.database.svc
      postgresql.external.existingSecret: strange-company-db
    asserts:
      - equal:
          path: data.DATABASE_HOST
          value: postgres.database.svc
```

- [ ] **Step 2: Push and confirm the `unit` job fails**

Expected: FAIL — `templates/configmap.yaml` does not exist.

- [ ] **Step 3: Implement `_helpers.tpl`**

Each resolver is a single `if`. Example shape to follow for all three
dependencies:

```
{{- define "strange-company.databaseHost" -}}
{{- if .Values.postgresql.enabled -}}
{{- include "strange-company.postgresql.fullname" . -}}
{{- else -}}
{{- required "postgresql.external.host is required when postgresql.enabled is false" .Values.postgresql.external.host -}}
{{- end -}}
{{- end -}}
```

`strange-company.vikunjaUrl` returns `http://<release>-vikunja:3456` when
bundled, else `.Values.vikunja.external.url`.
`strange-company.hermesGatewayUrl` returns
`http://<fullname>-hermes:<hermes.gateway.port>` when bundled, else
`.Values.hermes.external.gatewayUrl`.
`strange-company.hermesDashboardUrl` returns
`http://<fullname>-hermes:<hermes.dashboard.port>` when bundled **and**
`hermes.dashboard.enabled`, else `.Values.hermes.external.dashboardUrl`
(may be empty — the ConfigMap omits the key when empty).

- [ ] **Step 4: Push; confirm the two helper assertions pass**

---

### Task 3: values.yaml and values.schema.json

**Files:**
- Create: `charts/strange-company/values.yaml`
- Create: `charts/strange-company/values.schema.json`
- Test: `charts/strange-company/tests/schema_test.yaml`

**Interfaces:**
- Produces: the complete value surface every other task reads. Component keys
  `global`, `postgresql`, `vikunja`, `hermes`, `controlPlane`, `serviceAccount`,
  `networkPolicy`.

- [ ] **Step 1: Write `values.yaml`** following spec §8/§15/§20/§31 exactly,
  with `postgresql.image.tag: "17.11"`, `hermes.image.tag: "v2026.8.19"`, and
  `controlPlane.image.tag: ""` (empty → falls back to `.Chart.AppVersion`, so no
  `latest` ships in a release; documented deviation from the spec's literal
  `latest`). Add the Vikunja subchart block under `vikunja.vikunja` disabling its
  ingress and SQLite PVC.

- [ ] **Step 2: Write `values.schema.json`** with `allOf`/`if`/`then` blocks:
  - `postgresql.enabled === false` → `postgresql.external` requires
    `host` (minLength 1), `port`, `database`, `existingSecret` (minLength 1)
  - `vikunja.enabled === false` → `vikunja.external.url` required, minLength 1
  - `hermes.enabled === false` → `hermes.external.gatewayUrl` required, minLength 1
  - `controlPlane.enabled === true` → `controlPlane.image.repository` minLength 1
  - ports: `integer`, `minimum: 1`, `maximum: 65535`
  - `controlPlane.replicas`: `integer`, `minimum: 0`
  - persistence `size`: `type: string`
  - Do **not** set `additionalProperties: false` on `vikunja` — the subchart's
    values live there and must pass through unvalidated (spec §32).

- [ ] **Step 3: Write the schema rejection test**

helm-unittest asserts failure with `failedTemplate`. Verify that disabling
postgresql without an external host is rejected.

- [ ] **Step 4: Push; confirm `helm lint` now succeeds and schema tests pass**

---

### Task 4: PostgreSQL — StatefulSet, Service, Secret, init ConfigMap

**Files:**
- Create: `postgresql-secret.yaml`, `postgresql-init-configmap.yaml`,
  `postgresql-statefulset.yaml`, `postgresql-service.yaml`
- Test: extend `tests/batteries_test.yaml`, `tests/external_test.yaml`

**Interfaces:**
- Consumes: `strange-company.postgresql.fullname`, `strange-company.labels`.
- Produces: Service `<fullname>-postgresql:5432`; Secret
  `<fullname>-postgresql` with keys `username`, `password`, `database`.

- [ ] **Step 1: Write failing tests** asserting the StatefulSet exists with
  `postgres:17.11`, a `volumeClaimTemplates` entry of size `8Gi` with **no**
  `storageClassName` key when `storageClass` is `""`, a `pg_isready` readiness
  probe, and that all four documents are absent when `postgresql.enabled: false`.

- [ ] **Step 2: Push; confirm failure**

- [ ] **Step 3: Implement.** Key details that are easy to get wrong:
  - Guard every file with `{{- if .Values.postgresql.enabled }}`.
  - The Secret must be **lookup-preserving** (spec §24, DoD 16):

```
{{- $existing := (lookup "v1" "Secret" .Release.Namespace $name) -}}
{{- $password := "" -}}
{{- if .Values.postgresql.auth.password }}
  {{- $password = .Values.postgresql.auth.password }}
{{- else if $existing }}
  {{- $password = index $existing.data "password" | b64dec }}
{{- else }}
  {{- $password = randAlphaNum 24 }}
{{- end }}
```
  - Skip the Secret entirely when `postgresql.auth.existingSecret` is set.
  - `PGDATA: /var/lib/postgresql/data/pgdata` — a **subdirectory** of the mount,
    otherwise `initdb` fails on a non-empty volume (`lost+found`).
  - `securityContext`: pod `fsGroup: 999`, container `runAsUser: 999`,
    `runAsGroup: 999`, `runAsNonRoot: true`, drop ALL caps.
  - Mount the init ConfigMap at `/docker-entrypoint-initdb.d`. Its script:

```sql
SELECT 'CREATE DATABASE vikunja OWNER "strange-company"'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'vikunja')\gexec
```
    Wrap in a `.sh` that runs `psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER"
    --dbname "$POSTGRES_DB"`. Guard the database name with the value of
    `vikunja.database` so it stays configurable.
  - `volumeClaimTemplates` only when `persistence.enabled`; otherwise an
    `emptyDir` volume named identically so the container spec is unchanged.
  - `serviceName` must equal the Service name; selector labels must be stable
    across upgrades (DoD 17).

- [ ] **Step 4: Push; confirm unit + lint green**

---

### Task 5: Hermes — Deployment, Service, PVC, optional Secret

**Files:**
- Create: `hermes-deployment.yaml`, `hermes-service.yaml`, `hermes-pvc.yaml`,
  `hermes-secret.yaml`
- Test: extend `tests/batteries_test.yaml`, `tests/external_test.yaml`

**Interfaces:**
- Consumes: `strange-company.hermes.fullname`, `strange-company.hermesSecretName`.
- Produces: Service `<fullname>-hermes` with ports `gateway` 8642 and
  `dashboard` 9119; PVC `<fullname>-hermes` mounted at `/opt/data`.

- [ ] **Step 1: Write failing tests** — Deployment/Service/PVC present when
  bundled with persistence on; PVC absent when `persistence.enabled: false`;
  all three absent when `hermes.enabled: false`.

- [ ] **Step 2: Push; confirm failure**

- [ ] **Step 3: Implement.**
  - PVC is a standalone `PersistentVolumeClaim` (spec §16 names the file), not a
    `volumeClaimTemplate`, because Hermes is a Deployment.
  - Add `helm.sh/resource-policy: keep`? **No** — spec §46 wants uninstall to
    remove chart-owned workloads and says nothing about retaining Hermes data.
    Leave default behavior.
  - Probes: TCP socket on the gateway port only. Do **not** invent an HTTP path
    (spec §54).
  - Credentials: `envFrom.secretRef` when `hermes.existingSecret` is set,
    otherwise when `hermes.env` contains entries render a chart-owned Secret.
    Rendering with neither must succeed (spec §18, DoD 26).
  - `automountServiceAccountToken: false` and no ServiceAccount reference
    (spec §25).

- [ ] **Step 4: Push; confirm green**

---

### Task 6: Control plane — ConfigMap, Secret, ServiceAccount, Deployment, Service

**Files:**
- Create: `configmap.yaml`, `secret.yaml`, `serviceaccount.yaml`,
  `control-plane-deployment.yaml`, `control-plane-service.yaml`
- Test: extend all four unit suites

**Interfaces:**
- Consumes: every resolver from Task 2.
- Produces: ConfigMap `<fullname>-config`; Secret `<fullname>-secrets`;
  ServiceAccount `<fullname>`; Service `<fullname>-control-plane:8080`.

- [ ] **Step 1: Write failing tests asserting the §21 contract in all four modes**

The same nine variables must be present in every mode; only their values differ.
Non-secret ones come from the ConfigMap via `envFrom`; the four credential ones
come from `env[].valueFrom.secretKeyRef`.

- [ ] **Step 2: Push; confirm failure**

- [ ] **Step 3: Implement.**
  - ConfigMap holds `DATABASE_HOST`, `DATABASE_PORT`, `DATABASE_NAME`,
    `DATABASE_USER`, `VIKUNJA_URL`, `HERMES_GATEWAY_URL`, and
    `HERMES_DASHBOARD_URL` (omitted when empty).
  - Deployment env: `DATABASE_PASSWORD`, `VIKUNJA_TOKEN`, `HERMES_API_KEY` via
    `secretKeyRef` pointing at whichever Secret the resolvers selected. Mark
    optional so rendering and startup do not hard-fail in CI where no Vikunja
    token exists.
  - `envFrom`: `configMapRef` first, then `secretRef` for
    `controlPlane.existingSecret` when set, so user values win.
  - Probes `/healthz` (liveness) and `/readyz` (readiness) on the service port.
  - ServiceAccount: created by default, `automountServiceAccountToken: false`,
    **no Role/RoleBinding at all** (spec §25).

- [ ] **Step 4: Push; confirm green**

---

### Task 7: Ingress, NetworkPolicy, NOTES.txt

**Files:**
- Create: `ingress.yaml`, `networkpolicy.yaml`, `NOTES.txt`

- [ ] **Step 1: Implement three independent Ingress documents** in one file,
  each `{{- if and .Values.X.ingress.enabled ... }}`, using `hosts: []` plus
  `className`, `annotations`, `tls`. Support the spec's singular `host` field as
  a fallback for backwards compatibility with §15/§20's literal shape. No
  Traefik-specific annotations anywhere.

- [ ] **Step 2: NetworkPolicy behind `networkPolicy.enabled: false`.**

- [ ] **Step 3: NOTES.txt** printing resolved control plane / Vikunja / Hermes
  gateway / Hermes dashboard URLs and port-forward commands when ingress is off.
  Never print Secret contents.

- [ ] **Step 4: Push; confirm lint + kubeconform green on all four fixtures**

---

### Task 8: CI fixtures and the helm test pod

**Files:**
- Create: `ci/values-batteries.yaml`, `ci/values-external.yaml`,
  `ci/values-hybrid-a.yaml`, `ci/values-hybrid-b.yaml`
- Create: `templates/tests/test-connections.yaml`

- [ ] **Step 1: Write the four fixtures** per spec §56. Batteries sets
  `controlPlane.image.repository: traefik/whoami`, `tag: v1.12.0`, and
  `controlPlane.args: ["--port", "8080"]` so whoami binds 8080 as non-root and
  answers 200 on `/healthz` and `/readyz`. Persistence off or 1Gi. No ingress.

- [ ] **Step 2: Write the helm test pod.** One Pod, `helm.sh/hook: test`, image
  pinned, running a script that:
  - `nc -z <pg-host> <pg-port>` when postgresql is bundled
  - `nc -z <hermes-host> 8642` and dashboard port when bundled
  - `wget -q -O- http://<vikunja>:3456/api/v1/info` when bundled
  - `wget -q -O- http://<control-plane>:8080/healthz` when the control plane is on
  Each check is skipped by Helm templating when its component is external, so the
  same pod works in every mode. No model request is ever made.

- [ ] **Step 3: Push; confirm the `integration` job installs and `helm test` passes**

---

### Task 9: Lifecycle, packaging, parent-chart example, release workflow

**Files:**
- Modify: `.github/workflows/helm.yml`
- Create: `.github/workflows/release.yml`
- Create: `examples/parent-chart/Chart.yaml`, `examples/parent-chart/values.yaml`
- Create: `docs/USAGE.md`

- [ ] **Step 1: Extend `integration`** with the upgrade (`--set
  controlPlane.replicas=2`, then assert `.spec.replicas == 2` via `kubectl get`),
  a credential-stability assertion (capture the PG password before and after the
  upgrade and compare — DoD 16), a PVC-identity assertion (compare PVC UID before
  and after — DoD 17), then `helm uninstall` and assert chart-owned workloads are
  gone.

- [ ] **Step 2: Extend `package`** to `helm package`, then install the produced
  `.tgz` into a fresh k3d cluster (DoD 42), and `helm template` the `.tgz` under
  `values-external.yaml`.

- [ ] **Step 3: Write `release.yml`** — on tag `v*`, `helm package` and
  `helm push` to `oci://ghcr.io/tuckermclean/charts` using `GITHUB_TOKEN` with
  `packages: write`.

- [ ] **Step 4: Write the parent-chart example** documenting that Helm requires
  the subchart's values under the **dependency's name or alias** — i.e. a
  top-level `strange-company:` key — and that the `condition` key
  (`strangeCompany.enabled`) is a separate parent-level value.

- [ ] **Step 5: Commit `Chart.lock`** downloaded from the `lint` job artifact.

- [ ] **Step 6: Final full green run; then verify every DoD item against real
  job output** using superpowers:verification-before-completion.

---

## Self-Review

**Spec coverage.** §1 Task 1/3/4/5/6/7/8; §2 Task 1; §3–6 Tasks 2/3/8; §7–11
Task 4; §12–14 Tasks 1/3/4; §15–19 Task 5; §20–24 Task 6; §25 Task 6; §26
Task 2; §27 Task 7; §28 Tasks 4/5; §29 Task 3; §30 Tasks 2/3; §31–32 Task 3;
§33–38 Tasks 1–8 (tests written per task); §39–45 Task 8; §46–48 Task 9;
§49–50 Task 9; §51 Global Constraints + Task 9 Step 5; §52 Global Constraints;
§53 Task 7; §54 Tasks 4/5/6; §55 Task 7; §56 Task 8; §57–58 Tasks 1/9;
§59 — enforced by containing no application containers other than a fixture;
§60 — enforced by the file structure; §61 Task 9 Step 6.

**Placeholder scan.** No TBDs. The two spec values deliberately left symbolic in
the spec (`<PINNED_TESTED_VERSION>`) are resolved in Global Constraints.

**Type consistency.** Helper names in Task 2's Interfaces are the exact strings
used in Tasks 4–8. Secret key names `username`/`password`/`database` are used
consistently by Task 4's Secret and Task 6's `secretKeyRef`.

**Known risk carried into execution:** the Hermes image's actual startup
requirements are unverified — it may refuse to boot without provider credentials,
which would break DoD 21–24 while spec §18/§26 forbid supplying real ones. If CI
shows that, the fallback is a dummy non-routable API key value plus a TCP-only
probe, and the constraint gets reported rather than silently worked around.
