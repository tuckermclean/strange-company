# 54 — `strange-company` Helm chart

**Status:** Stage-0 plan (draft)
**DAG nodes:** F13 (primary: chart skeleton and SPEC §116 gates); contributes chart re-runs to C01/C08 (owned by 26/24)
**Related plans:** [11](11-storage.md), [12](12-cnpg.md), [18](18-flux.md), [23](23-supply-chain.md), [24](24-hermes.md), [25](25-context.md), [26](26-agent-runner.md), [28](28-harnesses.md), [33](33-budget-enforcement.md), [35](35-secrets.md), [36](36-network-policy.md), [37](37-admission-policy.md), [38](38-prometheus.md), [42](42-alerting-notifications.md), [48](48-upgrade.md), [51](51-capacity.md), [52](52-ci-strategy.md), [53](53-model-gateway.md)
**Contracts:** produces `charts/strange-company/values.schema.json`; consumes `resource-profile`, `harness`, `role`, `model-routing`, `budget-policy`, `pricing`, `egress-policy` (as delivered policy files), `runner-api` (ports)
**ADRs:** relies on 0005, 0013, 0014, 0015, 0016, 0017, 0300, 0303, 0401, 0452, 0550; introduces ADR-0404 (chart scope excludes enforcement objects; in-cluster chart sourced from Git, releases published as signed OCI artifacts)

> Every section below is mandatory (SPEC §121).

## 1. Purpose

The `strange-company` chart installs the Strange Company–specific workloads [SPEC §115] — Agent Runner, Model Gateway, egress proxy, Hermes integration, the fake model upstream used before R02 — with their ServiceAccounts, RBAC, Services, configuration, the worker ServiceAccount and the optional Hermes dashboard route. It is **not** an umbrella for the platform: platform dependencies are separate GitOps releases [SPEC §115]. This plan fixes the chart's scope and exclusions, its values contract (`values.schema.json`), the SPEC §116 quality matrix and what each test asserts, image pinning, versioning and publishing.

## 2. Requirements derived from SPEC.md

1. Chart name `strange-company`; installs Strange Company components rather than the platform; platform dependencies stay separate GitOps releases [SPEC §115].
2. Likely scope: Agent Runner, Hermes configuration/integration, ServiceAccounts, RBAC, NetworkPolicies, ResourceQuota, LimitRange, worker policies, ConfigMaps, Services, monitoring integrations, optional ingress [SPEC §115] (see §2 interpretation below).
3. Every chart release passes helm lint, values schema validation, helm template, Kubernetes schema validation, unit tests, k3d installation, upgrade, uninstall and reinstall tests [SPEC §116].
4. Critical images and dependencies are pinned [SPEC §116, §37]; production images immutable [SPEC §37]; no Bitnami [SPEC §20].
5. Workers: non-root, no escalation, no capabilities, RuntimeDefault seccomp, no SA token, Restricted PSA; worker SA with zero API rights [SPEC §84].
6. Hermes has no Kubernetes workload privileges [SPEC §4.6, §59].
7. A fresh operator does not hand-write Runner RBAC or worker admission policy [SPEC §130].

**Interpretation (recorded, non-blocking; ADR-0404).** NetworkPolicies, ResourceQuota, LimitRange and "worker policies" (VAPs) for `strange-company`, `hermes` and `agent-jobs` are delivered by the `namespaces` (plan 51) and `policies` (plans 36, 37) layers, not by this chart. They are *enforcement* objects that must exist before and independently of the workloads they constrain (SPEC §85 "Runner validation SHALL not be the sole defense"): uninstalling, failing or rolling back this chart must never remove or weaken them. SPEC §115 lists them as "likely" scope; completeness (they exist, generated from the same policy files) is preserved. Likewise ServiceMonitors/PrometheusRules live in the central monitoring layers (ADR-0550); the chart provides the metrics endpoints.

Derived requirements:

- **54-R-1** No template exposes security-relevant pod fields as values: `securityContext`, `hostPath`, `hostNetwork`, `privileged`, capabilities, `serviceAccountName`, `automountServiceAccountToken` and volume types are hard-coded in templates.
- **54-R-2** Every image value matches `^[a-z0-9.-]+(:[0-9]+)?(/[a-z0-9._-]+)+@sha256:[a-f0-9]{64}$` (schema-enforced).
- **54-R-3** The chart renders no `Secret` objects and no secret values; it references Secrets by fixed names delivered by plan 35 (SOPS) or created at runtime (Runner, CNPG).
- **54-R-4** The chart never renders a ConfigMap named `sc-emergency-stop` (ADR-0452; `make lint-gitops`).
- **54-R-5** Uninstall leaves only the documented leftovers (§14.4) and never deletes data: the chart renders **no PVC** (`hermes-data` is plan 13's stable-named PV/PVC/Volume triplet in a Flux layer with `prune: disabled`, review STO-5, ADR-0203) and `sc-pg` is outside the chart.
- **54-R-6** No chart template renders a PersistentVolumeClaim whose name appears in the plan 11 storage register (F11 check, request to plan 18/52).

## 3. Inputs

| Input | Source |
|---|---|
| Chart source | `charts/strange-company/` in `strange-company` (Git) |
| Values (per cluster) | `gitops/platform/strange-company/base/values.yaml` + `gitops/clusters/<cluster>/values/strange-company.yaml` (profile overlay) → `configMapGenerator` `strange-company-values` (plan 18 §8.2) |
| Image digests | promotion PRs from trusted CI (plan 23) editing `images.*` in the base values |
| Hermes skills, SOUL, managed-config templates | `charts/strange-company/files/hermes/**` (chart files, read via `.Files.Glob`) |
| Runner JWKS (public keys) | values `runner.jwks` (public JWKs only), produced by plan 35's key generation (`sc-bootstrap seal` emits the public half) |
| Platform policy & company context | **not** chart inputs: ConfigMaps `sc-policy`, `sc-company-context` generated by `sc-gitops policy-configmaps` into `gitops/platform/strange-company/generated/` (committed; F11 freshness check), in the same Flux layer (§9.3) |

## 4. Outputs (rendered objects)

| Component | Namespace | Objects |
|---|---|---|
| Agent Runner | `strange-company` | Deployment `agent-runner` (1 replica, `Recreate`), Service `agent-runner` (ports `mcp` 8081, `worker` 8082, `hooks` 8083 (Forgejo webhooks and CI intents, CD-03), `metrics` 8090; **not** 8080), ServiceAccount `agent-runner`, ConfigMap `agent-runner-config` (`config.yaml`, plan 26 §9.1), ConfigMap `hermes-managed-template` (from `files/hermes/managed/*.tmpl`), Roles/RoleBindings exactly as plan 26 §13 (in `agent-jobs`, `strange-company` incl. `sc-emergency-stop` get/list/watch, `hermes`, and read-only `pods` list/watch in `renovate` and `dr-tests` only for the heavy-slot dispatch gate (plan 51; never `ci-jobs`/`ci-build`, review SEC-3)) |
| Model Gateway | `strange-company` | Deployment `model-gateway` (2 replicas, `maxUnavailable: 0`), Services `model-gateway` (8080) and `model-gateway-metrics` (9090), ServiceAccount `model-gateway` (token mounted, plan 53), Role `get configmaps/sc-emergency-stop`, PodDisruptionBudget `minAvailable: 1` |
| Runner JWKS | `strange-company` | ConfigMap `sc-runner-jwks` (`jwks.json`) mounted by the Gateway and the egress proxy |
| Egress proxy (plan 36, ADR-0150) | `strange-company` | Deployment `egress-proxy` (2 replicas), Service `egress-proxy` (3128 `agent`, 3129 `ci`, 3131 `dr` for `dr-tests`, 9090 `metrics`; plan 36), ServiceAccount (no token), PDB `minAvailable: 1`; mounts `sc-policy` (`egress.yaml`) and `sc-runner-jwks` |
| Fake model upstream (test-only provider; stays enabled after R02, review RA-m12) | `strange-company` | Deployment + Service `fake-model-upstream` (8080) when `modelGateway.fakeUpstream.enabled` (plan 53 §22.3) |
| Hermes (plan 24) | `hermes` | Deployment `hermes` (Recreate), Service `hermes` (8642, 9119), ServiceAccount `hermes` (no token, no bindings), `imagePullSecrets: [sc-registry-pull]` (the preflight init container uses the internal Runner image, review RA-9), a reference to the existing claim `hermes-data` (not rendered, 54-R-5), ConfigMap `hermes-skills`, IngressRoute `hermes` (Traefik CRD; when `hermes.dashboard.ingress.enabled`) |
| Workers | `agent-jobs` | ServiceAccount `agent-worker` (`automountServiceAccountToken: false`, no bindings). Jobs and per-execution Secrets are created at runtime by the Runner, never by the chart |

Pod-level settings in every Deployment (hard-coded, 54-R-1): `imagePullSecrets: [{name: sc-registry-pull}]` for internal images (plan 23; Secret delivered by plan 35 in each namespace), `runAsNonRoot`, `runAsUser`/`runAsGroup`/`fsGroup` 65532 (Hermes: per plan 24), `seccompProfile: RuntimeDefault`, `allowPrivilegeEscalation: false`, `capabilities.drop: [ALL]`, `readOnlyRootFilesystem: true` (Hermes: plan 24 Q5), `enableServiceLinks: false`, `/tmp` emptyDir with `sizeLimit`, `priorityClassName` per tier (plan 51), common labels `app.kubernetes.io/{name,instance,version,part-of=strange-company,managed-by=Helm}` and `strange.company/tier`.

## 5. Dependencies

**Hard (DAG):** F13 ← F11 (gitops tree renders offline), F12 (admission + NetworkPolicy bundle, so k3d install tests run under the real enforcement objects).

**Runtime / install-time prerequisites (outside the chart, plan 18 layer order):** namespaces with PSA/tier labels, PriorityClasses, ResourceQuota/LimitRange (plan 51); NetworkPolicies (plan 36); VAPs + params (plan 37); `sc-pg` Cluster and Secrets `sc-pg-app`, `sc-pg-gateway`, `sc-pg-reporting` (plan 12, layer `sc-db`); SOPS Secrets (plan 35): `agent-runner-signing`, `agent-runner-forgejo`, `agent-runner-woodpecker`, `agent-runner-s3`, `agent-runner-admin`, `forgejo-webhook` (keys `secret`, `secret_previous`), `agent-runner-ci-intents`, `sc-registry-pull`, `model-api-keys`; Forgejo `known_hosts` ConfigMap `forgejo-known-hosts` (plan 16 forge-init output); Longhorn StorageClass `longhorn` (plan 13); Traefik CRDs (plan 09). Flux layer `strange-company` depends on `sc-db`, `monitoring-config`, `woodpecker` (plan 18).

**Interfaces assumed:** plan 18 layer 23 sources this chart from GitRepository `flux-system`; plan 23 builds/signs images and the chart package and opens promotion PRs; plan 37 identities (`system:serviceaccount:strange-company:agent-runner`); plan 36 pod selectors (`app.kubernetes.io/name=agent-runner|model-gateway|egress-proxy|hermes|fake-model-upstream`).

## 6. Owned state

None durable besides Git. The chart owns rendered objects; persistent data it mounts (`hermes-data`) is owned by plan 13's Flux layer, so uninstall cannot delete it; `sc-pg` is excluded. Runtime-created objects (Jobs, per-execution Secrets, `hermes-managed-env`) belong to the Runner.

## 7. External interfaces

- **Values contract:** `values.schema.json` (JSON Schema draft 2020-12 if supported by the pinned Helm, else draft-07 with identical semantics; verify at F13) — the only interface operators and promotion bots use.
- **Flux:** `HelmRelease strange-company/strange-company` (plan 18 §8.2 component contract).
- **Published artifact:** `oci://git.<d>/strange-company/charts/strange-company:<version>` + cosign signature + SBOM attestation (plan 23).
- **Service names and ports** consumed by plans 24, 26, 27, 36, 38, 53 (§4).

## 8. Internal interfaces

Template structure (directory layout is guidance): `templates/runner/*`, `templates/gateway/*`, `templates/egress-proxy/*`, `templates/hermes/*`, `templates/workers/*`, `templates/fake-upstream/*`, `templates/_helpers.tpl` (labels, image helper that fails rendering on a non-digest reference, tier → PriorityClass map). `files/hermes/` holds Hermes content (plan 24 §9.3). `ci/` holds values fixtures; `tests/` holds helm-unittest suites.

## 9. Configuration

### 9.1 Values (top level; every object `additionalProperties: false`)

| Key | Type / default | Notes |
|---|---|---|
| `global.baseDomain` | string, required | `^[a-z0-9.-]+$` |
| `global.cluster` | string, required | cluster name |
| `global.profile` | enum `self-hosting-minimum`, `small-ha` | selects replica/topology defaults |
| `images.runner`, `.gateway`, `.egressProxy`, `.fakeModelUpstream`, `.hermes` | digest refs, required (54-R-2) | `hermes` from upstream (ADR-0013); `runner` image also used for the Hermes preflight init container |
| `runner.resources` | requests/limits object | defaults plan 26 §12 |
| `runner.config` | object mapped 1:1 onto plan 26 §9.1 keys that are tunable (`database.maxConns`, `logs.*`, `heartbeat.*`, `watchdog.*`, `cancel.graceSeconds`, `deployKeys.graceSeconds`, `hermes.credentialRotationDays`, `hermes.overlapHours`, `forgejo.managedRepositories`, `features.updatedSinceFilter`) | `listeners.*`, `kubernetes.*` are **not** values (hard-coded) |
| `runner.dispatchPaused` | bool, `false` | ADR-0554 / plan 33 |
| `runner.maintenanceWindow` | string, rendered from cluster-vars `SC_MAINTENANCE_WINDOW` (plan 08 format, Flux `postBuild` substitution) | read by the Runner as `dispatch.blackoutWindows` (plan 26 §14.3, GC-20) |
| `runner.jwks` | array of public JWK objects (`kty: OKP`, `crv: Ed25519`, `kid`, `x`) , minItems 1, maxItems 2 | current + previous during rotation (plan 35) |
| `modelGateway.*` | plan 53 §9 keys (`replicas`, `timeouts.*`, `limits.*`, `reservation.*`, `estop.*` fixed name, `db.*`, `otel.endpoint`, `logLevel`, `fakeUpstream.enabled`) | plan 53 owns the key names |
| `modelGateway.fakeUpstream.enabled` | bool, `true` (also after R02: test-only provider used by C10 regression runs and recovery checks; review RA-m12) | plan 53 key |
| `egressProxy.enabled` | bool, `true` | plan 36 |
| `hermes.enabled` | bool, `true` | |
| `hermes.replicas` | int 0–1, default 1 | 0 by PR to keep Hermes stopped during a volume restore without suspending the HelmRelease (review RA-m10) |
| `hermes.watchIntervalMinutes` | int 5 (2–60) | single owner of the watcher interval, rendered into the skill and scheduler config (plan 24) |
| `hermes.dashboard.ingress.enabled` / `.host` | bool `true` / `hermes.<baseDomain>` | IngressRoute, entryPoint `websecure`, TLS per plan 10 |
| `hermes.persistence.existingClaim` | fixed `hermes-data` (not a free value) | the PVC is plan 13's; the chart only references it |
| `hermes.orchestrator.{provider, apiFamily, model, auxiliaryModel}` (auxiliary from plan 31 `orchestrator.auxiliaryTier`) | strings | must equal plan 31 `ModelRouting.orchestrator` resolution (CI check `make check-chart-policy-sync`) |
| `hermes.resources` | object | plan 24 §12 |
| `priorityClasses.platform` | string (name from plan 51) | |

Forbidden by schema (invalid fixtures): `extraEnv`, `extraVolumes`, `podSecurityContext`, `securityContext`, `serviceAccount.name`, `hostNetwork`, any `*.image` with a tag, `runner.replicas` (fixed 1 in MVP), `networkPolicies`, `resourceQuota`.

### 9.2 Profile overlays

| Setting | self-hosting-minimum | small-ha |
|---|---|---|
| `modelGateway.replicas` / `egressProxy` replicas | 2 / 2 (same node) | 2 / 2 with hostname `topologySpreadConstraints` (`ScheduleAnyway`) |
| Runner | 1 replica | 1 replica (Lease-ready; active-active is a non-goal) |
| Hermes | 1 | 1 (Longhorn replica 2 via StorageClass, plan 13) |
| `modelGateway.fakeUpstream.enabled` | per phase | per phase |

### 9.3 Flux layer composition (`gitops/platform/strange-company/base/`)

`kustomization.yaml`: `GitRepository`-sourced `HelmRelease strange-company` (`chart.spec: {chart: ./charts/strange-company, sourceRef: {kind: GitRepository, name: flux-system, namespace: flux-system}, reconcileStrategy: Revision}`), `configMapGenerator strange-company-values`, `generated/sc-policy.yaml`, `generated/sc-company-context.yaml`, and the plan 35 SOPS Secrets for `strange-company`.

- `sc-policy`: one key per file under `policy/**`, path separators `/` encoded as `__` (e.g. `roles__tester.yaml`; plan 26 §9.2 decodes), no name-hash suffix (the Runner and Gateway hot-reload mounted files), total size ≤ 900 KiB (F11 check; ConfigMap limit 1 MiB).
- `sc-company-context`: `context/company/*.md`.
- Both generated by `sc-gitops policy-configmaps` (plan 18's toolbox binary) and committed; F11 regenerates and fails on any diff. Request to plan 22: `gitops/platform/strange-company/generated/**` and `gitops/platform/admission/**` are protected paths for agent branches (they encode policy).

HelmRelease settings: `interval: 10m`, `timeout: 10m`, `install.remediation.retries: 3`, `upgrade.remediation: {retries: 3, remediateLastFailure: true, strategy: rollback}` (safe: Runner migrations are expand/contract, plan 26 §19), `driftDetection: {mode: enabled, ignore: [{paths: ["/spec/template/metadata/annotations/strange.company~1managed-revision"], target: {kind: Deployment, name: hermes}}]}` so the Runner's restart annotation (ADR-0401) is not reverted (verify field names on the pinned helm-controller), `test.enable: false`.

### 9.4 Version selection rules

- Helm: the version in the toolbox lock (plan 02/01); chart `apiVersion: v2`; no chart dependencies (`dependencies: []`).
- helm-unittest plugin, kubeconform and Kubernetes JSON schemas: pinned in `versions/*.lock` (plan 01); kubeconform validates against the schema version matching the pinned k3s minor.
- k3d: pinned; its k3s image equals the production k3s minor (plan 07).

## 10. Secrets

The chart renders none (54-R-3). It references: `sc-pg-app`/`sc-pg-gateway`/`sc-pg-reporting` (`SC-PG-APP`), `agent-runner-signing` (`SC-RUNNER-SIGNING`), `agent-runner-forgejo` (`SC-FORGEJO-BOT-TOKENS`), `agent-runner-woodpecker` (`SC-WOODPECKER-RUNNER`), `agent-runner-s3` (`SC-OBJECT-STORAGE`/evidence), `agent-runner-admin` (`SC-RUNNER-ADMIN` hash), `forgejo-webhook` (`SC-RUNNER-WEBHOOK`, keys `secret` and `secret_previous` during rotation; name per plan 16 and `rotate-forge-credentials.md`), `agent-runner-ci-intents` (`SC-CI-INTENTS`), `sc-registry-pull` (`SC-REGISTRY-PULL`), `model-api-keys` (`SC-MODEL-API-KEYS`, CD-08), and `hermes-managed-env` (Runner-created, ADR-0401). A missing referenced Secret blocks the pod (`CreateContainerConfigError`), which Flux health checks report; no fallback values exist (L-4). No new secret class.

## 11. Storage

The chart creates no persistent storage. Hermes mounts the existing claim `hermes-data` (plan 13). All other pods use bounded `emptyDir` for `/tmp`. No PVC for the Runner, Gateway or egress proxy.

## 12. Resource requirements

| Workload | Replicas (min / ha) | requests | limits |
|---|---|---|---|
| agent-runner | 1 / 1 | 100m, 192Mi (ha 256Mi) | 1, 384Mi (ha 512Mi) |
| model-gateway | 2 / 2 | 50m, 96Mi | 1, 256Mi |
| egress-proxy | 2 / 2 | 50m, 64Mi | —, 256Mi |
| fake-model-upstream | 1 (until R02) | 10m, 32Mi | 100m, 64Mi |
| hermes | 1 / 1 | 100m, 512Mi | 1, 1Gi |
| **Total requests** | | ≈ 0.41 CPU, ≈ 1.1 GiB | |

Plan 51 owns the capacity gate; values are defaults.

## 13. Security boundary

- RBAC rendered = plan 26 §13 (Runner, plus `sc-emergency-stop` get/list/watch, ADR-0452), plan 53 §13 (Gateway), none for `hermes`, `agent-worker`, `egress-proxy`, `fake-model-upstream` (plan 37 §13.3 asserts). No ClusterRole/ClusterRoleBinding is rendered (unit-tested).
- Hard-coded pod security (54-R-1); PSA restricted is enforced by namespace labels (outside the chart) and the chart's pods comply.
- The Runner's admin listener is never exposed by any Service (Service ports exclude 8080).
- The Hermes dashboard is the only ingress the chart renders; the Hermes API, Runner, Gateway and proxy have no ingress.
- Enforcement objects are outside the chart (ADR-0404), so a chart uninstall leaves `agent-jobs` locked: the VAP binding's `paramRef` stays and continues to deny any pod not matching the allowlist.

## 14. Happy path

### 14.1 Change → release

1. A PR changes `charts/strange-company/**` and bumps `Chart.yaml` `version` (SemVer; `0.y.z` until R02). CI runs the §116 matrix (§22) via `make chart-test` (static) and the k3d lane (ADR-0303).
2. Human merge. Flux reconciles the HelmRelease from the Git revision (`reconcileStrategy: Revision`), so the cluster runs exactly the merged chart.
3. Trusted CI on `main` packages the chart; if `version` is new it pushes `oci://git.<d>/strange-company/charts/strange-company:<version>`, signs it with cosign, attaches SBOM/provenance (plan 23), and tags the commit `chart/strange-company/v<version>`.

### 14.2 Image promotion

Trusted CI builds `runner`, `gateway`, `egress-proxy`, `fake-model-upstream` images on `main`; plan 23's promotion PR updates `images.*` digests in `gitops/platform/strange-company/base/values.yaml`; merge → Flux upgrades the release (Runner `Recreate`, Gateway/proxy rolling).

### 14.3 Install order (fresh cluster)

Layer `strange-company` applies after `sc-db`: ConfigMaps → Services → RBAC → Deployments. The Runner waits for `sc-pg`, migrates, writes `hermes-managed-env`; Hermes' preflight waits for it (plan 24 §18); the Gateway waits for the ledger schema version (plan 53). HelmRelease `wait: true` succeeds when all Deployments are Available.

### 14.4 Uninstall / reinstall semantics

Uninstall (`helm uninstall` or HelmRelease deletion) removes every rendered object. Documented leftovers (not chart-owned): PVC `hermes/hermes-data` (plan 13 layer); Secret `hermes/hermes-managed-env` (Runner-created); any Runner-created Jobs/Secrets in `agent-jobs` (none when idle; the Runner deletes them after log capture; `ttlSecondsAfterFinished` 6 h as fallback). Reinstall re-binds `hermes-data` to the same PV, the Runner reuses `sc-pg` (migrations no-op) and re-renders `hermes-managed-env` if its credentials are unknown to the DB.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Missing referenced Secret | pod `CreateContainerConfigError`; HelmRelease not Ready; `SCFluxReconciliationFailing` | component down | Flux retries | plan 35 seal; `agent-runner-failure.md` |
| Image digest not pullable | `ImagePullBackOff` | component down; rollback remediation | upgrade remediation rollback after 3 retries | plan 23 registry |
| Runner migration fails during upgrade | Runner `CrashLoopBackOff` | dispatch stopped | rollback to previous release (N−1 compatible with schema N) | `agent-runner-failure.md` §Rollback |
| Values violate schema | `helm template` error in CI; in-cluster HelmRelease `InstallFailed` | blocked before merge | — | fix PR |
| Hermes Multi-Attach during upgrade (L-5) | plan 24 | Hermes down | — | `hermes-failure.md` §E |
| Drift on Hermes annotation reverted (if ignore rule unsupported) | Hermes restarts every reconcile | restart loop | `SCHermesRestartLoop` | disable drift detection for that field / adjust rule |
| Policy ConfigMap > 1 MiB | F11 size check | PR blocked | — | split policy files |
| Chart uninstall by mistake | Flux reports missing; Runner/Gateway gone | no orchestration or spend; data intact; workers blocked by admission | Flux reinstalls on next reconcile if the HelmRelease still exists | redeploy via Git |

## 16. Retry behavior

Flux/helm-controller retries (`install`/`upgrade` remediation 3) and periodic reconcile (10 min). The chart itself has no hooks or Jobs that retry.

## 17. Idempotency behavior

Rendering is deterministic (no `randAlphaNum`, no `lookup`, no `now`; unit-tested by rendering twice and comparing). Upgrades with unchanged values are no-ops. Generated policy ConfigMaps are deterministic (sorted keys).

## 18. Bootstrap behavior

Not in the seed set (needs Forgejo-created credentials and post-transfer layers; ADR-0005). First reconciled after control transfer once `sc-db`, `monitoring-config` and `woodpecker` are Ready (plan 18 layer 23). Images exist only after P11; before C01 the layer is either absent from the cluster kustomization or deployed with stub images only in CI (F13 tests), never on the live cluster.

## 19. Upgrade behavior

- SemVer chart versions; breaking values changes bump the minor while `0.y`, with a migration note in `charts/strange-company/UPGRADING.md`.
- Upgrade order inside one release: ConfigMaps/RBAC before Deployments (Helm kind order); the Runner's expand/contract migrations make Runner N−1 compatible with schema N (plan 26 §19).
- The upgrade test (§22 T8) gates every chart release.
- Removing a component is a values change; its objects are deleted by Helm.

## 20. Recovery behavior

The chart is rebuilt from Git; recovery is a reconcile. Data recovery belongs to `sc-pg` (plan 12) and `hermes-data` (plan 13/24). After black start (plan 45) the release installs normally; `runner.dispatchPaused: true` is set by the recovery commit (ADR-0554) until the finalize PR.

## 21. Observability

- Metrics endpoints (CD-02: every container port that serves `/metrics` is **named `metrics`**; NetworkPolicies and ServiceMonitors select it by name): Runner `metrics` 8090, Gateway `metrics` 9090 (Service `model-gateway-metrics`), egress proxy `metrics` 9090, Hermes via blackbox probes. Scrape objects and rules are owned by plans 38/42 (ADR-0550); the port names are unit-tested.
- Flux: HelmRelease Ready condition (`SCFluxReconciliationFailing`, plan 18/42).
- Chart version and image digests are visible as Prometheus `kube_deployment_*` labels via KSM and in `helm history`.

## 22. Testing strategy (SPEC §116 matrix)

Static tests run on every PR touching `charts/**` or `gitops/platform/strange-company/**` (`make chart-test`, no cluster). k3d tests run in the off-cluster privileged e2e lane (ADR-0303; Docker is never available to workers or verification pipelines, SPEC §38).

| # | SPEC §116 test | Command | Asserts |
|---|---|---|---|
| T1 | helm lint | `helm lint --strict charts/strange-company -f ci/values-<p>.yaml` for `p` ∈ {minimum, small-ha, constrained, stub} | exit 0 for each |
| T2 | values schema validation | `helm template … -f ci/values-<p>.yaml` (valid) and `-f ci/invalid/<case>.yaml` (each must fail) | valid pass; each invalid case fails with its expected message: tag-pinned image, `extraEnv`, `securityContext`, `runner.replicas`, unknown `global.profile`, `hermes.persistence.size: 100Mi`, `networkPolicies` key |
| T3 | helm template | render each valid fixture to `out/<p>.yaml`; render twice | byte-identical output; object inventory equals `tests/inventory/<p>.txt` |
| T4 | Kubernetes schema validation | `kubeconform -strict -kubernetes-version <pinned> -schema-location default -schema-location <traefik CRD schemas> out/*.yaml` | 0 invalid, 0 skipped except none |
| T5 | unit tests | `helm unittest charts/strange-company` | RBAC equals plan 26 §13 / plan 53 §13 (snapshot); every pod spec has the 54-R-1 fields; every image is digest-pinned; no `bitnami` string (CD-20: also no Bitnami chart dependency of any kind, `Chart.yaml` has `dependencies: []`); no PersistentVolumeClaim, Secret, NetworkPolicy, ResourceQuota, LimitRange, ValidatingAdmissionPolicy, ServiceMonitor, PrometheusRule, ClusterRole or ConfigMap `sc-emergency-stop` rendered; Runner Service has no port 8080 and `agent-runner-config` has `listeners.api: 127.0.0.1:8080`; every `/metrics`-serving container port is named `metrics`; PVC has `resource-policy: keep`; `agent-worker` SA has `automountServiceAccountToken: false`; drift-ignore rule present |
| T6 | k3d installation | `make chart-e2e-install`: k3d (pinned) → apply rendered `namespaces` + `policies` layers (plans 51/36/37) → CNPG + `sc-pg` + plan 35 test Secrets → `helm install` with `ci/values-stub.yaml` (F13) or `ci/values-k3d-real.yaml` (C01+) | all Deployments Available ≤ 10 min; `kubectl auth can-i` assertions of plan 26/37 pass; every pod admitted under PSA restricted |
| T7 | (part of T6) runtime smoke | stub: every Service port answers; real: Runner `/readyz` 200, Gateway `/readyz` 200, `fake-success` execution SUCCEEDED (C02+) | as stated |
| T8 | upgrade test | install the previous released version (`chart/strange-company/v<prev>` tag) → (C02+: start a long `fake-timeout` execution) → `helm upgrade` to HEAD | upgrade exit 0; `hermes-data` PV UID unchanged; no Deployment stuck; (C02+) the in-flight execution reaches its terminal state and no duplicate Job exists (26-AC-24) |
| T9 | uninstall test | `helm uninstall strange-company -n strange-company --wait` | remaining objects with the release's labels = ∅; leftovers exactly §14.4; a test pod submitted to `agent-jobs` with the golden worker spec is **denied** by admission (params intact, Runner identity not used) |
| T10 | reinstall test | `helm install` again | Ready ≤ 10 min; Hermes mounts the same pre-existing `hermes-data` PV (UID unchanged; PVC not rendered); Runner `schema_migrations` count unchanged |

Before the first release (no previous version), T8 upgrades from the same chart with modified values (`modelGateway.replicas: 1 → 2`) and records "first release" in the report.

## 23. Acceptance criteria

### GATE-F13

- [ ] **54-AC-1** `make chart-test` (T1–T5) exits 0 on the committed chart; the JUnit report lists every invalid-values case of T2 as an expected failure.
- [ ] **54-AC-2** `helm template` with `ci/values-stub.yaml` produces exactly the objects of §4 (inventory file diff empty) and `grep -ci bitnami` on the output is 0.
- [ ] **54-AC-3** Unit test: for every rendered Pod template, `securityContext.runAsNonRoot == true`, `seccompProfile.type == RuntimeDefault`, every container has `allowPrivilegeEscalation: false` and `capabilities.drop == [ALL]`; no `hostPath`, `hostNetwork`, `hostPID`, `privileged` anywhere (yq queries return empty).
- [ ] **54-AC-4** Unit test: the rendered object kinds contain none of `PersistentVolumeClaim`, `Secret`, `NetworkPolicy`, `ResourceQuota`, `LimitRange`, `ValidatingAdmissionPolicy`, `ValidatingAdmissionPolicyBinding`, `ServiceMonitor`, `PrometheusRule`, `ClusterRole`, `ClusterRoleBinding`, and no ConfigMap is named `sc-emergency-stop`.
- [ ] **54-AC-5** RBAC snapshot equals plan 26 §13 (+ the `sc-emergency-stop` row) for the Runner and plan 53 §13 for the Gateway; `hermes`, `agent-worker`, `egress-proxy` SAs have no RoleBinding in the render.
- [ ] **54-AC-6** T6 on k3d with stub images: all Deployments Available within 10 min under the plan-36/37 policies; `kubectl auth can-i --as=system:serviceaccount:agent-jobs:agent-worker --list -n agent-jobs` shows only default discovery rules.
- [ ] **54-AC-7** T8 (synthetic first-release form), T9 and T10 pass on k3d with stub images; T9's leftover set equals §14.4 exactly and the golden worker pod is denied after uninstall.
- [ ] **54-AC-8** `sc-gitops policy-configmaps` regenerates `generated/sc-policy.yaml` and `generated/sc-company-context.yaml` byte-identically (F11 check), every key of `sc-policy` decodes (`__` → `/`) to an existing `policy/**` path, and the total size is ≤ 921600 bytes.
- [ ] **54-AC-9** Publishing dry run: `helm package` of the commit produces a `.tgz` whose `Chart.yaml` version equals the committed one; the CI check `chart-version-bumped` fails on a fixture PR that changes a template without bumping `version`.

### GATE-C01 (chart with real images; contributes to plan 26's gate)

- [ ] **54-AC-20** T6–T10 pass with `ci/values-k3d-real.yaml` (real Runner, Gateway, egress-proxy, fake upstream images from trusted CI); T8 upgrades from the latest published chart version.
- [ ] **54-AC-21** On the live cluster: `kubectl -n strange-company get helmrelease strange-company -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'` = `True`, every image in `kubectl get pods -n strange-company -n hermes -o jsonpath='{..image}'` is digest-pinned and equals the base values, and `cosign verify` of the published chart artifact for the deployed version succeeds (plan 23 key).

## 24. Explicit non-goals

- Packaging platform components (Forgejo, CNPG, Longhorn, Woodpecker, monitoring) or `sc-pg`.
- Rendering enforcement objects (ADR-0404) or scrape/alert objects (ADR-0550).
- Supporting installation outside the Strange Company GitOps tree (no generic `values` for other clusters beyond the profiles).
- Multi-replica Runner or Hermes.
- Helm test hooks in-cluster.

## 25. Open questions

1. **Values schema draft** (blocking: no). Helm's bundled validator support for draft 2020-12 is version-sensitive; verified at F13 (T2); fallback draft-07 with the same constraints.
2. **helm-controller drift-ignore syntax** (blocking: no). Verified at C08 (24-AC-10 would loop otherwise); fallback: disable drift detection for the Hermes Deployment only.
3. **JWKS ConfigMap name** — resolved by CD-08: `sc-runner-jwks`.
4. **SPEC §115 scope interpretation** (blocking: no). Recorded in §2 and ADR-0404 for `spec-issues.md` consolidation.
5. **Model API key Secret name** — resolved by CD-08: `model-api-keys`.
