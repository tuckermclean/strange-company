# 37 — Admission policy

**Status:** Stage-0 plan (draft)
**DAG nodes:** **F12 admission-and-netpol-bundle** (primary, with 36), **C02 k8s-adapter** (co-owner with 26, 27: the live SPEC §105 suite). Contributes verify check V-13 to P12 (plan 50) and the seed-admission check to P04 (plan 19).
**Related plans:** [07](07-k3s.md), [18](18-flux.md), [19](19-seed-handoff.md), [20](20-woodpecker.md), [23](23-supply-chain.md), [26](26-agent-runner.md), [27](27-worker-contract.md), [28](28-harnesses.md), [34](34-security.md), [35](35-secrets.md), [36](36-network-policy.md), [38](38-prometheus.md), [42](42-alerting-notifications.md), [44](44-backup.md), [48](48-upgrade.md), [51](51-capacity.md), [54](54-strange-company-chart.md)
**Contracts:** consumes `harness.schema.yaml` (28), `resource-profile.schema.yaml` (26), `secret-classes.yaml` (35); no contract owned (admission objects are Kubernetes-native; their parameters are generated, §9)
**ADRs:** relies on ADR-0014, ADR-0015, ADR-0007, ADR-0008, ADR-0550; introduces **ADR-0503** (VAP parameters as Git-generated ConfigMaps, `parameterNotFoundAction: Deny`, staged `Warn`→`Deny` for upstream-controlled namespaces)

> Every section below is mandatory (SPEC §121).

## 1. Purpose

Kubernetes must enforce worker and CI restrictions **independently of the Agent Runner** [SPEC §85]. This plan defines the complete admission layer: Pod Security Admission (PSA) levels per namespace, every ValidatingAdmissionPolicy (VAP) with its match, CEL, parameters, binding and actions, the RBAC of every Strange Company ServiceAccount, and the deterministic test suite that attempts every SPEC §105 escape and asserts the denial reason. The Runner's Job builder (plan 26 §13) is the *first* line; this plan is the line that still holds if the Runner is wrong, compromised or impersonated.

## 2. Requirements derived from SPEC.md

1. Worker Jobs run non-root, disable privilege escalation, drop capabilities, use RuntimeDefault seccomp, disable SA token automount, avoid hostPath and privileged mode, and comply with Restricted Pod Security [SPEC §84].
2. Worker ServiceAccounts possess zero Kubernetes API rights [SPEC §84].
3. Runner validation is not the sole defense; PSA, RBAC, NetworkPolicy and native VAP/CEL enforce independently [SPEC §85].
4. Agent workloads cannot become privileged, mount hosts, select privileged ServiceAccounts, enter infrastructure namespaces or bypass the required security context [SPEC §85].
5. Hermes cannot specify image, namespace, ServiceAccount, Secrets, volumes, host paths, privileged mode, security context, raw YAML or resources [SPEC §62]; Hermes has no Job-creation privileges [SPEC §4.6, §59].
6. CI verifies that workers cannot arbitrarily run privileged, run as root, mount hostPath, mount service tokens, choose ServiceAccounts, Secrets or images, exceed resource profiles, enter infrastructure namespaces, bypass network policy, or obtain signing/deployment credentials [SPEC §105].
7. Workers never receive Docker/containerd sockets, privileged DinD, registry-push or deployment credentials [SPEC §38].
8. `agent-jobs` has ResourceQuota, LimitRange, concurrency and per-job hard limits [SPEC §89] (quota objects: plan 51; per-job limits: here).
9. Network-policy enforcement stays on [SPEC §18] (plan 36).
10. A fresh install implements worker admission policy without manual steps [SPEC §130].

Derived requirements:

- **37-R-1** Every VAP has `failurePolicy: Fail`; every binding that takes parameters uses `parameterNotFoundAction: Deny`. A missing or malformed parameter denies, never admits.
- **37-R-1a (review SEC-6) No cluster-wide parameter dependency.** Every policy bound cluster-wide (#6–10, 15–17, 21, 23–27) is **parameter-free**: `sc-gitops admission-params` generates its constants (identities, namespace lists, allowlists) as literals into the policy's CEL `variables`, from the same Git inputs (ADR-0503). Only namespace-bound policies take a parameter ConfigMap, so a missing ConfigMap can deny requests only inside that namespace. Every cluster-wide policy also carries `matchConditions` that exclude `system:node:*` users, namespace `kube-node-lease`, and UPDATEs by `system:serviceaccount:kube-system:{generic-garbage-collector,ttl-after-finished-controller,job-controller}` that change only `metadata.finalizers`, `metadata.ownerReferences` or `status` (verify that `matchConditions` are evaluated before parameter resolution on the pinned minor, 37-AC-1). Parameter ConfigMaps are applied in a Flux layer `policies-params` that the `policies` layer `dependsOn` (plan 18 interface).
- **37-R-2** VAPs for Strange Company–controlled namespaces (`agent-jobs`, `ci-jobs`, `ci-build`, `hermes`, `strange-company`) use `validationActions: [Deny, Audit]`. VAPs for namespaces whose pods are created by upstream controllers start at `[Warn, Audit]` and are promoted to `[Deny, Audit]` by PR once 37-AC-40 shows zero audit hits for 7 days (ADR-0503).
- **37-R-3** Parameters are generated from Git (`policy/harnesses.yaml`, `policy/resource-profiles.yaml`, `versions/*.lock`, `gitops/platform/admission/params.yaml`) by `sc-gitops admission-params`; F11 fails if the committed generated ConfigMaps differ from a fresh generation.
- **37-R-4** Every policy message starts with a stable code `ADM-<NN>`; tests assert on the code, not on prose.
- **37-R-5** Admission tests use server-side dry-run (`kubectl … --dry-run=server`), which runs admission without persisting, plus a small set of real creates for behaviour dry-run cannot show (ownerReferences from the job controller).
- **37-R-6** No Kyverno, Gatekeeper or custom admission webhook in MVP (ADR-0014).

## 3. Inputs

| Input | Source | Used for |
|---|---|---|
| Harness registry (digest-pinned images) | `policy/harnesses.yaml` (plan 28) | `allowedImages` of `sc-agent-jobs-*` |
| Resource profiles | `policy/resource-profiles.yaml` (plan 26) | maxima (cpu, memory, ephemeral storage, emptyDir, deadlines) |
| Version locks | `versions/*.lock` (plan 01), `versions/k3s.lock.yaml` | image allowlists for `monitoring-node`, `longhorn-system`, `ingress`, `ci-build`; PSA `enforce-version` |
| Static admission params | `gitops/platform/admission/params.yaml` (`kind: AdmissionParams`) | identities, uid range, key names, hostPath allowlists, ingress mode |
| Namespace list and tiers | master plan §5 + ADR-0550 + this plan §13.1 | PSA labels |
| Secret register | `contracts/secret-classes.yaml` | `sc-secret-nonempty` scope |

## 4. Outputs

- Namespace PSA labels (rendered into the `namespaces` layer, plan 18).
- `gitops/platform/admission/` → layer `policies`: every `ValidatingAdmissionPolicy`, `ValidatingAdmissionPolicyBinding` and parameter `ConfigMap` (namespace `flux-system`, names `sc-admission-<policy>`).
- RBAC expectations for every SC ServiceAccount (§13.3), rendered by the owning charts (plan 54 for SC components) and asserted here.
- The k3d admission test suite `test/admission/` (`make e2e-admission`) and the live subset (`make e2e-admission-live`, V-13).
- PSA admission configuration file content for k3s (§9.3; installed by plan 07).

## 5. Dependencies

**Hard (DAG):** F12 ← F11 (rendered tree, layer structure). C02 ← C01, F07, F12.

**Interfaces assumed:**

| Plan | Assumption |
|---|---|
| 07 | k3s ≥ 1.30 (VAP GA) per its version rule; installs `/etc/rancher/k3s/psa.yaml` from §9.3 via `--kube-apiserver-arg=admission-control-config-file` (or the k3s `pod-security-admission-config-file` flag, verify); runs the controller-manager with per-controller ServiceAccount credentials so pods created by the Job controller carry `system:serviceaccount:kube-system:job-controller` (verify, 37-AC-3; fallback: accept `system:kube-controller-manager`). |
| 18 | Layer `policies` (dependsOn `namespaces`, `crds`) carries this plan's objects and is in the seed set; `strange-company`, `woodpecker` and `apps` transitively depend on it. |
| 26 | Job/Secret shape exactly as plan 26 §13: two containers `sc-worker` + `harness` with the same digest, `creds` only in `sc-worker`, `harness-creds` (gateway and egress tokens) only in `harness`, heavy-slot units summing to `workerUnits`, `imagePullSecrets: [{name: sc-registry-pull}]`. |
| 20/23 | Woodpecker kubernetes backend: ci-lane agent `woodpecker-agent` → `ci-jobs`, build-lane agent `woodpecker-agent-build` → `ci-build` (label `sc-lane`); step pods labelled `strange.company/ci-lane` and `app.kubernetes.io/managed-by=woodpecker`; workspace is one `ci-workspace` PVC per workflow (ADR-0305); release secrets are Woodpecker secrets mapped to env names with the `releaseSecretEnvPrefixes` (plan 23). |
| 36 | NetworkPolicy suite; SPEC §105 "bypass network policy" tests are 36's and are invoked from this suite's runner. |
| 51 | ResourceQuota and LimitRange for `agent-jobs`, `ci-jobs`, `ci-build`; PriorityClasses per tier; extended resource `strange.company/heavy-slot` and ConfigMap `kube-system/heavy-slot-config` (`workerUnits`) used as the parameter of `sc-worker-heavy-slot`. |
| 50 / 12 / 45 | ConfigMap `flux-system/sc-cnpg-create-window` (`mode: initial-install`) is rendered into the cluster directory at install and removed by the commit that completes control transfer (plan 50); the recovery commit renders `mode: recovery`, `runId` and the finalize PR removes it (plan 45); CNPG `Cluster` manifests carry `strange.company/recovery-run` in recovery mode (plan 12). |
| 08 | Host firewall relies on `sc-no-nodeport-lb` and `sc-hostport-traefik-only` because DNAT'd traffic bypasses the input chain. |
| 43 / 44 / 48 | Identities `cr-retention`, `restore-tester` and `upgrade-gate` are narrowed by `sc-dr-identities-scope` and `sc-upgrade-gate-scope`. |
| 44 | `dr-tests` restore-tester RBAC (CNPG `Cluster` create in `dr-tests`) is plan 44's; this plan only asserts it does not reach other namespaces. |

## 6. Owned state

None durable. All objects are Git-rendered and reconciled by Flux. Kubernetes stores the live objects; Git is the system of record. Admission denials are evidence in the API-server audit log (plan 07) and metrics (§21).

## 7. External interfaces

- The admission behaviour itself: any client creating objects in matched namespaces receives a `403`/`Invalid` with message prefix `ADM-<NN>` (or the PSA message).
- `sc-gitops admission-params` (CLI in the toolbox, plan 18's binary) regenerates parameter ConfigMaps.
- Test targets: `make e2e-admission` (k3d, F12), `make e2e-admission-live` (live cluster through `./bootstrap exec`, C02 and V-13).

## 8. Internal interfaces

- Parameter generation: `policy/harnesses.yaml` → `allowedImages` (every `spec.harnesses[*].image` with `enabled: true` or `stage: test-only`); `policy/resource-profiles.yaml` → maxima over **all** profile sets (`standard`, `constrained`): `maxCpu`, `maxMemory`, `maxEphemeralStorage`, `maxEmptyDir`, `maxPodDeadlineSeconds` = max `deadline`, `maxJobDeadlineSeconds` = max `deadline` + Runner `startupGraceSeconds` (300).
- Promotion PRs (plan 23) that change a harness digest regenerate the parameters in the same commit, so the Runner's registry and the admission allowlist move together.

## 9. Configuration

### 9.1 `gitops/platform/admission/params.yaml` (static parameters; PR-changed)

```yaml
apiVersion: strange.company/v1alpha1
kind: AdmissionParams
metadata: {name: strange-company}
spec:
  identities:
    runner: system:serviceaccount:strange-company:agent-runner
    podCreators: [system:serviceaccount:kube-system:job-controller]     # verify (plan 07)
    fluxApplier: system:serviceaccount:flux-system:kustomize-controller
    woodpeckerVerifyAgent: system:serviceaccount:woodpecker:woodpecker-agent
    woodpeckerBuildAgent: system:serviceaccount:woodpecker:woodpecker-agent-build
  untrustedNamespaces: [agent-jobs, ci-jobs, ci-build, hermes]
  zeroRightsServiceAccounts: ["agent-jobs:*", "ci-jobs:*", "ci-build:*", "hermes:*", "renovate:*"]
  agentJobs:
    uidMin: 1000
    uidMax: 65535
    serviceAccount: agent-worker
    priorityClassName: agent-worker
    allowedSecretKeys: [task-token, egress-token, gateway-token, deploy-key, known_hosts]
    harnessSecretKeys: [gateway-token, egress-token]     # the only keys projected into the harness container (plan 26 two-container isolation)
    maxPrivateMemoryDir: 64Mi                            # the sc-worker-only `private` volume (medium Memory)
    fluxManagedSecrets: [sc-registry-pull]
    allowedTolerationKeys: [node.kubernetes.io/not-ready, node.kubernetes.io/unreachable]
  ciJobs:
    serviceAccount: default             # step pods run as the namespace default SA, token never mounted (plan 20 …_SERVICE_ACCOUNT_NAME_ALLOW_FROM_STEP=false)
    priorityClassName: ci
    maxCpu: "2"
    maxMemory: 3Gi                      # = plan 20 lanes.ci.max (generated from policy/ci.yaml)
    workspaceStorageClass: ci-workspace # ADR-0305
    maxWorkspace: 8Gi                   # CD-06 / ADR-0305 rule 3 (Woodpecker default request 6Gi)
  ciBuild:
    serviceAccount: default
    maxWorkspace: 10Gi                  # CD-06 (plan 23 builds one image at a time)
    builderCapabilities: [CHOWN, DAC_OVERRIDE, FOWNER, FSETID, KILL, SETGID, SETUID, SETFCAP, SYS_CHROOT]
    requireUserNamespaces: false        # set true by PR after 23-AC-6 passes on the pinned k3s/containerd/kernel
    releaseStepImages: [sc-release, provider-contract]   # names resolved to digests from versions lock
    releaseSecretEnvPrefixes: [SC_COSIGN_, SC_REGISTRY_PUSH_, SC_RELEASE_PR_, SC_GHCR_, SC_GITHUB_PROBE, SC_CI_OVH_]   # env names the release workflow maps from_secret values to
  monitoringNodeProfiles:               # per image (review SEC-10)
    node-exporter: {hostPathsReadOnly: [/, /proc, /sys, /var/lib/node_exporter/textfile], runAsNonRoot: true, addCapabilities: []}
    otel-agent:    {hostPathsReadOnly: [/var/log/pods, /var/lib/rancher/k3s/server/logs], hostPathsReadWrite: [/var/lib/otel-agent], runAsUser: 0, addCapabilities: [DAC_READ_SEARCH]}
  priorityClassesByTier:             # plan 51 §9.1
    platform-critical: [platform-critical, "system-*"]
    platform: [platform]
    application: [application]
    ci: [ci]
    agent-worker: [agent-worker]
  heavySlotParamsRef: kube-system/heavy-slot-config   # plan 51 §8.3 (key workerUnits)
  allowedStorageClasses: [local-path, longhorn, longhorn-scratch, ci-workspace]   # plan 11 SA-3
  ingress:
    mode: tunnel                        # from cluster-vars INGRESS_MODE
    hostPorts: [80, 443, 2222]          # 2222 only when Forgejo SSH is opted in (ADR-0151)
```

### 9.2 Generated parameter ConfigMaps (namespace `flux-system`)

ConfigMap values are strings; lists are newline-separated and split in CEL with `split('\n')` (Kubernetes CEL strings extension). One ConfigMap per policy family: `sc-admission-agent-jobs`, `sc-admission-ci`, `sc-admission-identities`, `sc-admission-images` (per-namespace image allowlists for privileged/baseline namespaces). The generator emits a trailing `generatedFrom: <sha256 of inputs>` key; F11 regenerates and compares.

### 9.3 PSA defaults (k3s admission configuration, installed by plan 07)

```yaml
apiVersion: apiserver.config.k8s.io/v1
kind: AdmissionConfiguration
plugins:
  - name: PodSecurity
    configuration:
      apiVersion: pod-security.admission.config.k8s.io/v1
      kind: PodSecurityConfiguration
      defaults: {enforce: restricted, enforce-version: latest, audit: restricted, audit-version: latest, warn: restricted, warn-version: latest}
      exemptions: {usernames: [], runtimeClasses: [], namespaces: [kube-system]}
```

Effect: any namespace created without labels (by mistake or by an upstream chart) is `restricted` by default. `kube-system` stays k3s-managed (CoreDNS, metrics-server, local-path helper pods need hostPath).

### 9.4 Knobs

| Knob | Source | Default | Who may change |
|---|---|---|---|
| PSA level per namespace | §13.1 table, rendered into `namespaces` layer | §13.1 | PR (security review) |
| `enforce-version` | `versions/k3s.lock.yaml` minor | `v1.<pinned minor>` | upgrade PR (plan 48) |
| Static params | §9.1 | §9.1 | PR |
| Generated params | §8 | generated | regenerated by PR only |
| Upstream-namespace VAP actions | binding `validationActions` | `[Warn, Audit]` → `[Deny, Audit]` after 37-AC-40 | PR |

## 10. Secrets

None consumed. This plan constrains secret *use*: `sc-agent-jobs-pod` (worker pods mount only their own execution Secret), `sc-agent-jobs-secret` (only the Runner writes Secrets in `agent-jobs`), `sc-ci-build-release-steps` (release credentials only into the `sc-release` image) and `sc-secret-nonempty` (35-R-5).

## 11. Storage

N/A — admission objects are stateless; Git is the store (§6).

## 12. Resource requirements

No workloads. VAP evaluation runs inside kube-apiserver. CEL cost: each policy stays under the per-expression and per-policy cost limits of the pinned Kubernetes (verify via `kubectl apply --dry-run=server` of every policy in F12; a cost-limit error fails the apply). Measured added admission latency target: p99 < 5 ms per pod create (37-AC-42).

## 13. Security boundary

### 13.1 Namespaces and Pod Security levels

Labels on every namespace: `pod-security.kubernetes.io/{enforce,audit,warn}` and `…-version: v1.<minor>`, plus `strange.company/tier`.

| Namespace | enforce | audit / warn | Tier | Compensating VAP (when weaker than restricted) |
|---|---|---|---|---|
| `kube-system` | privileged (exempt in §9.3) | — | platform-critical | `sc-untrusted-sa-no-mutation`, `sc-runner-scope` cover writes from SC identities; k3s-managed |
| `flux-system` | restricted (fallback baseline per plan 18 Q4, recorded here if used) | restricted | platform-critical | — |
| `cert-manager`, `cnpg-system` | restricted | restricted | platform-critical | — |
| `longhorn-system` | **privileged** (Longhorn requires host access) | restricted | platform-critical | `sc-longhorn-images` (pods only from the Longhorn lock digests) |
| `ingress` (tunnel mode) | baseline (plan 09, ADR-0151) | restricted | platform-critical | — (tightening to `restricted` is evaluated by 37-AC-43, non-blocking) |
| `ingress` (direct mode) | **privileged** (Traefik DaemonSet with host ports; forbidden by `baseline`, ADR-0151) | restricted | platform-critical | `sc-hostport-traefik-only` (host ports 80/443/2222 only on DaemonSet `traefik`; everything else restricted-equivalent) |
| `authentik`, `forgejo`, `woodpecker` | restricted | restricted | platform | — |
| `monitoring` | baseline (ADR-0550) | restricted | platform | — |
| `monitoring-node` | **privileged** (ADR-0550) | restricted | platform | `sc-monitoring-node` (node-exporter / otel-agent images only; hostPath allowlist, read-only) |
| `strange-company` | restricted | restricted | platform | `sc-runner-scope` (identity side) |
| `hermes` | restricted | restricted | application | `sc-untrusted-sa-no-mutation`; Hermes pods may reference `imagePullSecrets: [sc-registry-pull]` (review RA-9; no policy forbids it) |
| `agent-jobs` | restricted | restricted | agent-worker | `sc-agent-jobs-*` (worker shape, Job, Secret, kinds) |
| `ci-jobs` | restricted | restricted | ci | `sc-ci-jobs-pod`, `sc-ci-workspace` (ADR-0305) |
| `ci-build` (new, plan 23) | **baseline** (trusted builds run a root builder with default capabilities) | restricted | ci | `sc-ci-build-pod`, `sc-ci-build-release-steps`, `sc-ci-workspace` |
| `renovate`, `dr-tests`, `apps` | restricted | restricted | ci / platform / application | — |

The master plan lists `ingress` as `baseline`; that holds in tunnel mode, while direct mode is `privileged` + VAP (ADR-0151; baseline cannot admit host ports). `ci-build` and `monitoring-node` are added to master plan §5 by CD-13.

### 13.2 ValidatingAdmissionPolicies (complete list)

All: `apiVersion: admissionregistration.k8s.io/v1`, `failurePolicy: Fail`. Params: `paramKind: {apiVersion: v1, kind: ConfigMap}`, binding `paramRef: {name: <cm>, namespace: flux-system, parameterNotFoundAction: Deny}` where marked P (namespace-bound policies only). **G** = parameter-free, constants generated into the CEL (37-R-1a).

| # | Policy | Match (resources / operations) | Bound to | P | Actions | Purpose |
|---|---|---|---|---|---|---|
| 1 | `sc-agent-jobs-pod` | `pods` CREATE | ns `agent-jobs` | P | Deny, Audit | complete worker pod shape (§13.2.1) |
| 2 | `sc-agent-jobs-job` | `batch/jobs` CREATE, UPDATE | ns `agent-jobs` | P | Deny, Audit | only the Runner creates/updates Jobs; template shape; UPDATE may change only `spec.suspend` |
| 3 | `sc-agent-jobs-secret` | `secrets` CREATE, UPDATE | ns `agent-jobs` | P | Deny, Audit | only the Runner writes per-execution Secrets (name = execution id, allowed keys, owner Job); Flux may write only `fluxManagedSecrets` |
| 4 | `sc-agent-jobs-kinds` | `pods` (bare), `apps/*` workloads, `batch/cronjobs`, `persistentvolumeclaims`, `serviceaccounts`, `rbac/*` CREATE | ns `agent-jobs` | P | Deny, Audit | no other workload kinds; no PVCs; SA/RBAC only from Flux |
| 5 | `sc-pod-subresource-guard` | `pods/ephemeralcontainers` UPDATE; `pods/exec`, `pods/attach`, `pods/portforward` CONNECT | ns `agent-jobs`, `ci-jobs`, `ci-build`, `hermes` | P | Deny, Audit | no ephemeral containers; exec/attach/port-forward only for `system:masters` (break-glass) |
| 6 | `sc-agent-worker-sa-confinement` | `pods`, `serviceaccounts` CREATE | cluster-wide | G | Deny, Audit | `agent-worker` SA usable/creatable only in `agent-jobs` |
| 7 | `sc-zero-rights-subjects` | `rbac/rolebindings`, `rbac/clusterrolebindings` CREATE, UPDATE | cluster-wide | G | Deny, Audit | no binding may name an SA in `zeroRightsServiceAccounts` (agent-worker, Hermes, CI step SAs, Renovate) |
| 8 | `sc-untrusted-sa-no-mutation` | all resources (`*/*`) CREATE, UPDATE, DELETE, CONNECT | cluster-wide | G | Deny, Audit | requests authenticated as an SA of an `untrustedNamespaces` namespace are denied (defense in depth over RBAC) |
| 9 | `sc-runner-scope` | all resources CREATE, UPDATE, DELETE | cluster-wide | G | Deny, Audit | the Runner SA may write only: Jobs/Secrets in `agent-jobs`; Secret `hermes-managed-env` and Deployment `hermes` (patch) in `hermes`; Lease `agent-runner-leader` in `strange-company` |
| 10 | `sc-woodpecker-agent-scope` | all resources CREATE, UPDATE, DELETE | cluster-wide | G | Deny, Audit | verify agent writes only in `ci-jobs`; build agent only in `ci-build` |
| 11 | `sc-ci-jobs-pod` | `pods` CREATE | ns `ci-jobs` | P | Deny, Audit | created by the ci-lane agent; default SA without token; digest-pinned images; no Secret volumes, `secretKeyRef` or `envFrom` (only `imagePullSecrets: sc-registry-pull`); volumes only emptyDir + `ci-workspace` PVCs; limits ≤ lane max; PriorityClass `ci`; no host ports (plan 20 §13.4 A1–A8) |
| 12 | `sc-ci-build-pod` | `pods` CREATE | ns `ci-build` | P | Deny, Audit | images only from the build allowlist (clone, builder, `sc-release`, provider-contract); root only for the builder image; builder caps ⊆ allowlist; `hostUsers: false` when `requireUserNamespaces` |
| 13 | `sc-ci-build-release-steps` | `pods` CREATE | ns `ci-build` | P | Deny, Audit | a container carrying any env var with a `releaseSecretEnvPrefixes` prefix, any `secretKeyRef`/`envFrom`/Secret volume must use a `releaseStepImages` image and must not set `command`/`args` (plugin entrypoint only); Kubernetes Secret volumes are otherwise forbidden |
| 14 | `sc-ci-workspace` | `persistentvolumeclaims` CREATE | ns `ci-jobs`, `ci-build` | P | Deny, Audit | StorageClass `ci-workspace`, request ≤ 8Gi (`ci-jobs`) / 10Gi (`ci-build`) (CD-06), created by the agent of that namespace (ADR-0305) |
| 15 | `sc-secret-nonempty` | `secrets` CREATE, UPDATE | cluster-wide, objectSelector `strange.company/secret-class` exists **or** `app.kubernetes.io/managed-by=agent-runner` | G | Deny, Audit | no empty value in `data`/`stringData` (35-R-5, L-4) |
| 16 | `sc-namespace-guard` | `namespaces` UPDATE, DELETE | cluster-wide | G | Deny, Audit | PSA and tier labels change, and protected namespaces are deleted, only by the Flux applier or `system:masters` |
| 17 | `sc-images-digest-pinned` | `pods` CREATE | all namespaces except `kube-system` | G | Deny, Audit in SC namespaces; Warn, Audit elsewhere (37-R-2) | every container image (incl. init and ephemeral) ends `@sha256:<64 hex>` [SPEC §37] |
| 18 | `sc-longhorn-images` | `pods` CREATE | ns `longhorn-system` | P | Warn, Audit → Deny | images only from the Longhorn lock |
| 19 | `sc-monitoring-node` | `pods` CREATE | ns `monitoring-node` | P | Deny, Audit | images from the node-exporter/otel-agent lock; hostPath ⊆ allowlist and `readOnly: true`; no `privileged` |
| 20 | `sc-hostport-traefik-only` (name fixed by ADR-0151) | `pods` CREATE | ns `ingress` (bound only when `ingress.mode=direct`) | P | Deny, Audit | host ports only {80, 443, 2222} and only on pods owned by DaemonSet `traefik` with the Traefik image; no hostNetwork/privileged/hostPath/added capabilities |
| 21 | `sc-no-nodeport-lb` | `services` CREATE, UPDATE | cluster-wide | G | Deny, Audit | `spec.type` ∉ {`NodePort`, `LoadBalancer`} (plan 08 §13: DNAT bypasses the host input chain) |
| 22 | `sc-worker-heavy-slot` | `pods` CREATE | ns `agent-jobs`, `ci-jobs`, `ci-build` only (CD-16) | P (`kube-system/heavy-slot-config`) | Deny, Audit | `agent-jobs` worker pods: the **sum** over both containers of `limits["strange.company/heavy-slot"]` equals `workerUnits`; `ci-jobs`/`ci-build` pipeline pods (creator = that namespace's Woodpecker agent): at least one container requests `strange.company/heavy-slot` > 0 (lane default injected by `sc-ci-config`, plan 51 §8.4). Not bound anywhere else: light check CronJobs (restore-test checks, audits, retention) request no heavy-slot units (CD-16); heavy restore tests request units by plan 44's own manifests |
| 23 | `sc-priorityclass-by-namespace` | `pods` CREATE | all namespaces except `kube-system` | G | Deny, Audit for tiers `agent-worker`, `ci`, `application`; Warn, Audit → Deny for `platform*` (37-R-2) | `priorityClassName` must be the class allowed for the namespace tier (`agent-worker`→`agent-worker`, `ci`→`ci`, `application`→`application`, `platform`→`platform`, `platform-critical`→`platform-critical` or `system-*`) (plan 51 §9.1) |
| 24 | `sc-upgrade-gate-scope` | `longhorn.io/backups`, `longhorn.io/snapshots`, `postgresql.cnpg.io/backups` CREATE | cluster-wide | G | Deny, Audit | requests by any `…:upgrade-gate` ServiceAccount must carry label `strange.company/upgrade-gate` (plan 48) |
| 25 | `sc-runner-label-guard` | all namespaced resources CREATE, UPDATE (objectSelector `app.kubernetes.io/managed-by=agent-runner`, matched on old or new object) | cluster-wide | G | Deny, Audit | the label can be set or kept only by the Runner identity, and on Pods by the Job controller, and only in `agent-jobs` (prevents forging Runner ownership) |
| 26 | `sc-dr-identities-scope` | all resources CREATE, UPDATE, DELETE | cluster-wide | G | Deny, Audit | `dr-tests:restore-tester` may mutate only objects named `rt-*` (namespace `dr-tests`, Longhorn volumes in `longhorn-system`, cluster-scoped PVs); `dr-tests:cr-retention` may delete only finished Jobs (`oldObject.status.completionTime` set or a `Failed` condition), CNPG `Backup`s in phase `completed`/`failed`, and `rt-*` objects in `dr-tests` (plans 43, 44) |
| 27 | `sc-storageclass-scope` | `persistentvolumeclaims` CREATE | cluster-wide | G | Deny, Audit | plan 11 SA-3/SA-4: `spec.storageClassName` is set and ∈ {`local-path`, `longhorn`, `longhorn-scratch`, `ci-workspace`}; `ci-workspace` only in `ci-jobs`/`ci-build`, and those namespaces use no other class; no PVC at all in `agent-jobs` (also `ADM-35`) |
| 28 | `sc-cnpg-cluster-create-guard` (authoritative definition; ConfigMap name `flux-system/sc-cnpg-create-window`, plans 12/45/50 follow it) | `postgresql.cnpg.io/clusters` CREATE | ns `forgejo`, `authentik`, `woodpecker`, `strange-company` | P (`flux-system/sc-cnpg-create-window`, `parameterNotFoundAction: Deny`) | Deny, Audit | review DR-7: a production `Cluster` may be created only while a creation window exists — `mode: initial-install` (rendered for a fresh install, removed by the commit that completes control transfer) or `mode: recovery` with `runId` equal to the object's annotation `strange.company/recovery-run` (rendered by the recovery commit while `sc-recovery-mode` is active, plan 45). In steady state the ConfigMap is absent, so an accidental `Cluster` deletion stalls visibly (`SCCnpgClusterDown`) instead of re-creating from a stale generation |

#### 13.2.1 `sc-agent-jobs-pod` (full sketch)

```yaml
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicy
metadata: {name: sc-agent-jobs-pod}
spec:
  failurePolicy: Fail
  paramKind: {apiVersion: v1, kind: ConfigMap}
  matchConstraints:
    resourceRules:
      - {apiGroups: [""], apiVersions: [v1], resources: [pods], operations: [CREATE]}
  variables:
    - name: p
      expression: params.data
    - name: sc
      expression: "has(object.spec.securityContext) ? object.spec.securityContext : {}"
    - name: execId
      expression: "object.metadata.?labels[?'strange.company/execution-id'].orValue('')"
    - name: images
      expression: "variables.p.allowedImages.split('\\n')"
  validations:
    - expression: "variables.p.podCreators.split('\\n').exists(u, u == request.userInfo.username)"
      messageExpression: "'ADM-01 pods in agent-jobs are created only by the Job controller, not ' + request.userInfo.username"
    - expression: "variables.execId.matches('^sc-[0-9]+-x[0-9]+$') && object.metadata.labels['app.kubernetes.io/managed-by'] == 'agent-runner' && variables.execId.startsWith(object.metadata.labels['strange.company/task-id'] + '-x')"
      message: "ADM-02 execution/task/managed-by labels missing or inconsistent"
    - expression: "object.metadata.?ownerReferences.orValue([]).exists(o, o.apiVersion == 'batch/v1' && o.kind == 'Job' && o.?controller.orValue(false) && o.name == variables.execId)"
      message: "ADM-03 pod must be controlled by the Job named after its execution id"
    - expression: "object.spec.?serviceAccountName.orValue('default') == variables.p.serviceAccount"
      message: "ADM-04 serviceAccountName must be agent-worker"
    - expression: "object.spec.?automountServiceAccountToken.orValue(true) == false"
      message: "ADM-05 automountServiceAccountToken must be false"
    - expression: "!object.spec.?hostNetwork.orValue(false) && !object.spec.?hostPID.orValue(false) && !object.spec.?hostIPC.orValue(false)"
      message: "ADM-06 host namespaces are forbidden"
    - expression: "variables.sc.?runAsNonRoot.orValue(false) && variables.sc.?runAsUser.hasValue() && variables.sc.runAsUser >= int(variables.p.uidMin) && variables.sc.runAsUser <= int(variables.p.uidMax) && variables.sc.?runAsGroup.orValue(0) >= int(variables.p.uidMin)"
      message: "ADM-07 pod must run as non-root within the allowed uid/gid range"
    - expression: "variables.sc.?fsGroup.orValue(int(variables.p.uidMin)) >= int(variables.p.uidMin) && variables.sc.?supplementalGroups.orValue([]).all(g, g >= int(variables.p.uidMin)) && object.spec.containers.all(c, c.?securityContext.?runAsGroup.orValue(int(variables.p.uidMin)) >= int(variables.p.uidMin))"
      message: "ADM-07b fsGroup, supplementalGroups and container runAsGroup must be >= uidMin (no gid 0; review SEC-19)"
    - expression: "variables.sc.?seccompProfile.?type.orValue('') == 'RuntimeDefault' && !has(variables.sc.sysctls)"
      message: "ADM-08 RuntimeDefault seccomp required; sysctls forbidden"
    - expression: "object.spec.?initContainers.orValue([]).size() == 0 && object.spec.?ephemeralContainers.orValue([]).size() == 0"
      message: "ADM-09 init and ephemeral containers are forbidden"
    - expression: >-
        object.spec.containers.all(c, has(c.securityContext)
          && c.securityContext.?allowPrivilegeEscalation.orValue(true) == false
          && !c.securityContext.?privileged.orValue(false)
          && c.securityContext.?capabilities.?drop.orValue([]).exists(d, d == 'ALL')
          && c.securityContext.?capabilities.?add.orValue([]).size() == 0
          && c.securityContext.?readOnlyRootFilesystem.orValue(false)
          && c.securityContext.?procMount.orValue('Default') == 'Default'
          && c.securityContext.?seccompProfile.?type.orValue('RuntimeDefault') == 'RuntimeDefault'
          && (!c.securityContext.?runAsUser.hasValue() || (c.securityContext.runAsUser >= int(variables.p.uidMin) && c.securityContext.runAsUser <= int(variables.p.uidMax))))
      message: "ADM-10 container security context: no escalation, not privileged, drop ALL, add none, read-only root FS"
    - expression: "object.spec.containers.all(c, c.image.matches('^[^@]+@sha256:[a-f0-9]{64}$') && variables.images.exists(i, i == c.image))"
      messageExpression: "'ADM-11 image not in the harness-registry allowlist: ' + object.spec.containers.map(c, c.image).join(',')"
    - expression: "object.spec.containers.all(c, c.?ports.orValue([]).all(pt, pt.?hostPort.orValue(0) == 0))"
      message: "ADM-12 host ports are forbidden"
    - expression: "object.spec.containers.all(c, !has(c.envFrom) && c.?env.orValue([]).all(e, !has(e.valueFrom)))"
      message: "ADM-13 env valueFrom/envFrom are forbidden (no Secret or ConfigMap selection through env)"
    - expression: "object.spec.?volumes.orValue([]).all(v, has(v.emptyDir) || has(v.secret))"
      message: "ADM-14 only emptyDir and the execution Secret volume are allowed (no hostPath, PVC, projected or token volumes)"
    - expression: "object.spec.?volumes.orValue([]).all(v, !has(v.secret) || v.secret.secretName == variables.execId)"
      message: "ADM-15 a worker may mount only the Secret named after its own execution id"
    # Two-container pod (plan 26 §13, review TDD-14/ADR-0402): exactly `sc-worker` and `harness`, same image digest.
    - expression: "size(object.spec.containers) == 2 && object.spec.containers.exists(c, c.name == 'sc-worker') && object.spec.containers.exists(c, c.name == 'harness') && object.spec.containers[0].image == object.spec.containers[1].image"
      message: "ADM-15a a worker pod has exactly the containers sc-worker and harness, with the same image"
    - expression: >-
        object.spec.?volumes.orValue([]).all(v, !has(v.secret) || (v.name == 'creds' && !has(v.secret.items))
          || (v.name == 'harness-creds' && v.secret.?items.orValue([]).size() == 2
              && v.secret.items.all(i, variables.p.harnessSecretKeys.split('\n').exists(k, k == i.key))))
        && object.spec.containers.all(c, c.?volumeMounts.orValue([]).all(m,
              (m.name != 'creds' || c.name == 'sc-worker') && (m.name != 'harness-creds' || c.name == 'harness')
              && (m.name != 'private' || c.name == 'sc-worker')))
      message: "ADM-15b the full execution Secret (creds) and the private memory volume mount only into sc-worker; harness gets only gateway-token and egress-token (harness-creds)"
    - expression: "object.spec.?volumes.orValue([]).all(v, !has(v.emptyDir) || (has(v.emptyDir.sizeLimit) && quantity(string(v.emptyDir.sizeLimit)).compareTo(quantity(variables.p.maxEmptyDir)) <= 0 && (v.emptyDir.?medium.orValue('') != 'Memory' || (v.name == 'private' && quantity(string(v.emptyDir.sizeLimit)).compareTo(quantity(variables.p.maxPrivateMemoryDir)) <= 0))))"
      message: "ADM-16 emptyDir needs a sizeLimit within the profile maximum; memory-backed only for the 64Mi sc-worker `private` volume"
    - expression: >-
        object.spec.containers.all(c, has(c.resources) && has(c.resources.limits)
          && ['cpu', 'memory', 'ephemeral-storage'].all(r, r in c.resources.limits)
          && quantity(string(c.resources.limits['cpu'])).compareTo(quantity(variables.p.maxCpu)) <= 0
          && quantity(string(c.resources.limits['memory'])).compareTo(quantity(variables.p.maxMemory)) <= 0
          && quantity(string(c.resources.limits['ephemeral-storage'])).compareTo(quantity(variables.p.maxEphemeralStorage)) <= 0)
        && quantity(string(object.spec.containers[0].resources.limits['memory'])).add(quantity(string(object.spec.containers[1].resources.limits['memory']))).compareTo(quantity(variables.p.maxMemory)) <= 0
        && quantity(string(object.spec.containers[0].resources.limits['cpu'])).add(quantity(string(object.spec.containers[1].resources.limits['cpu']))).compareTo(quantity(variables.p.maxCpu)) <= 0
      message: "ADM-17 cpu, memory and ephemeral-storage limits are required; each container and the pod sum stay within the largest resource profile"
    - expression: "object.spec.?activeDeadlineSeconds.orValue(0) > 0 && object.spec.activeDeadlineSeconds <= int(variables.p.maxPodDeadlineSeconds)"
      message: "ADM-18 activeDeadlineSeconds is required and bounded by the largest profile deadline"
    - expression: >-
        !has(object.spec.nodeName) && !has(object.spec.nodeSelector) && !has(object.spec.affinity)
          && !has(object.spec.hostAliases) && !has(object.spec.runtimeClassName) && !has(object.spec.dnsConfig)
          && object.spec.?dnsPolicy.orValue('ClusterFirst') == 'ClusterFirst'
          && !object.spec.?shareProcessNamespace.orValue(false)
          && object.spec.?enableServiceLinks.orValue(true) == false
          && object.spec.?restartPolicy.orValue('Always') == 'Never'
          && object.spec.?priorityClassName.orValue('') == variables.p.priorityClassName
          && object.spec.?tolerations.orValue([]).all(t, variables.p.allowedTolerationKeys.split('\\n').exists(k, k == t.?key.orValue('')))
      message: "ADM-19 scheduling, DNS, host aliases, runtime class, priority and restart policy are fixed by policy"
    - expression: "object.spec.?imagePullSecrets.orValue([]).all(s, s.name == 'sc-registry-pull')"
      message: "ADM-20 only the sc-registry-pull image pull secret is allowed"
  auditAnnotations:
    - key: sc-execution-id
      valueExpression: "variables.execId"
---
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata: {name: sc-agent-jobs-pod}
spec:
  policyName: sc-agent-jobs-pod
  validationActions: [Deny, Audit]
  paramRef: {name: sc-admission-agent-jobs, namespace: flux-system, parameterNotFoundAction: Deny}
  matchResources:
    namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: agent-jobs}}
```

Notes: `quantity()` comes from the Kubernetes CEL quantity library and `split`/`join` from the strings extension (both verify on the pinned minor, 37-AC-1). Resource quantities in CEL are typed as int-or-string; `string()` normalises them before `quantity()` (verify; the F12 dry-run suite is the re-verification).

#### 13.2.2 `sc-agent-jobs-job` (CEL sketch)

```text
ADM-21  request.userInfo.username == p.runnerIdentity
ADM-22  request.operation == 'CREATE' ? true :
             object.spec.template == oldObject.spec.template && object.spec.?parallelism == oldObject.spec.?parallelism
             (UPDATE may change only spec.suspend, labels and annotations)
        matchConditions exempt the garbage collector, the TTL-after-finished controller and the Job controller when only
        finalizers / ownerReferences / status change (foreground deletion, review SEC-6); positive controls E49–E52
ADM-23  object.metadata.name.matches('^sc-[0-9]+-x[0-9]+$') && object.metadata.labels['strange.company/execution-id'] == object.metadata.name
           && object.spec.template.metadata.labels['strange.company/execution-id'] == object.metadata.name
ADM-24  object.spec.?backoffLimit.orValue(6) == 0 && object.spec.?parallelism.orValue(1) == 1 && object.spec.?completions.orValue(1) == 1
           && has(object.spec.ttlSecondsAfterFinished)
ADM-25  object.spec.?activeDeadlineSeconds.orValue(0) > 0 && object.spec.activeDeadlineSeconds <= int(p.maxJobDeadlineSeconds)
ADM-26  template checks duplicated from ADM-04, -05, -09, -11, -14, -15 (against object.metadata.name), -17, -20 for early, clear errors
```

#### 13.2.3 `sc-agent-jobs-secret` (CEL sketch)

```text
ADM-30  request.userInfo.username == p.runnerIdentity
             || (request.userInfo.username == p.fluxApplier && p.fluxManagedSecrets.split('\n').exists(n, n == object.metadata.name))
ADM-31  runner-created: object.metadata.name.matches('^sc-[0-9]+-x[0-9]+$')
           && object.metadata.labels['strange.company/execution-id'] == object.metadata.name
           && object.metadata.labels['app.kubernetes.io/managed-by'] == 'agent-runner'
           && object.?type.orValue('Opaque') == 'Opaque'
ADM-32  object.?data.orValue({}).all(k, p.allowedSecretKeys.split('\n').exists(a, a == k))   (no extra keys)
ADM-33  object.metadata.?ownerReferences.orValue([]).exists(o, o.kind == 'Job' && o.name == object.metadata.name)
ADM-34  request.operation == 'UPDATE' → only the Flux applier, for fluxManagedSecrets (per-execution Secrets are immutable)
```

#### 13.2.4 Other policies (CEL sketches)

In policies marked **G**, `p.<name>` below denotes a constant that the generator writes into the policy's `variables` (no parameter object, 37-R-1a); in **P** policies it is the parameter ConfigMap.

```text
sc-agent-jobs-kinds        ADM-35  kind in {Pod with Job controller owner (handled by #1), Job, Secret} or
                                      (request.userInfo.username == p.fluxApplier && kind in {ServiceAccount, Role, RoleBinding,
                                       ResourceQuota, LimitRange, NetworkPolicy, ConfigMap})  else deny
sc-pod-subresource-guard   ADM-36  request.subResource == 'ephemeralcontainers' → false
                           ADM-37  request.subResource in ['exec','attach','portforward'] → 'system:masters' in request.userInfo.groups
sc-agent-worker-sa-confinement
                           ADM-38  pods: object.spec.?serviceAccountName.orValue('default') != 'agent-worker' || request.namespace == 'agent-jobs'
                           ADM-39  serviceaccounts: object.metadata.name != 'agent-worker' || request.namespace == 'agent-jobs'
sc-zero-rights-subjects    ADM-40  object.?subjects.orValue([]).all(s, s.kind != 'ServiceAccount' ||
                                        !p.zeroRightsServiceAccounts.split('\n').exists(z,
                                           z == s.namespace + ':' + s.name || z == s.namespace + ':*'))
                                      (+ Group subjects 'system:serviceaccounts:<untrusted ns>' denied)
sc-untrusted-sa-no-mutation
                           ADM-41  !p.untrustedNamespaces.split('\n').exists(ns,
                                        request.userInfo.username.startsWith('system:serviceaccount:' + ns + ':'))
sc-runner-scope            ADM-42  request.userInfo.username != p.runnerIdentity ||
                                      (request.namespace == 'agent-jobs' && request.resource.resource in ['jobs','secrets']) ||
                                      (request.namespace == 'hermes' && ((request.resource.resource == 'secrets' &&
                                         object.metadata.name == 'hermes-managed-env') ||   /* CREATE included, review SEC-24 */
                                        (request.resource.resource == 'deployments' && request.name == 'hermes' && request.operation == 'UPDATE'
                                         /* annotation-only rollout trigger: nothing but spec.template.metadata.annotations may change */
                                         && object.spec.template.spec == oldObject.spec.template.spec
                                         && object.spec.?replicas == oldObject.spec.?replicas && object.spec.selector == oldObject.spec.selector
                                         && object.spec.template.metadata.?labels == oldObject.spec.template.metadata.?labels))) ||
                                      (request.namespace == 'strange-company' && request.resource.resource == 'leases')
sc-woodpecker-agent-scope  ADM-43  username == p.woodpeckerVerifyAgent → request.namespace == 'ci-jobs';
                                      username == p.woodpeckerBuildAgent → request.namespace == 'ci-build'
sc-ci-jobs-pod             ADM-44  creator == p.woodpeckerVerifyAgent; SA == p.serviceAccount; automount false; no host namespaces/ports;
                                      priorityClassName == 'ci'; images digest-pinned; volumes ⊆ {emptyDir, persistentVolumeClaim} and every
                                      claim is a ci-workspace PVC (claim names are not knowable to CEL, so ADM-47 constrains PVC creation);
                                      no secret volumes, no env valueFrom.secretKeyRef, no envFrom.secretRef; limits present and
                                      ≤ p.ciMaxCpu / p.ciMaxMemory; PSA restricted fields as ADM-10
sc-ci-build-pod            ADM-45  creator == p.woodpeckerBuildAgent; SA == p.serviceAccount; automount false; images ∈ p.buildImages;
                                      runAsUser 0 only for image == p.builderImage; builder caps ⊆ p.builderCapabilities, others drop ALL;
                                      no privileged, no host namespaces/ports/hostPath, seccomp RuntimeDefault;
                                      p.requireUserNamespaces == 'true' → object.spec.?hostUsers.orValue(true) == false
sc-ci-build-release-steps  ADM-46  containers.all(c, (c.?env.orValue([]).exists(e, p.releaseSecretEnvPrefixes.split('\n').exists(x, e.name.startsWith(x)))
                                        || c.?env.orValue([]).exists(e, has(e.valueFrom) && has(e.valueFrom.secretKeyRef)) || has(c.envFrom))
                                        ? (p.releaseStepImages.split('\n').exists(i, i == c.image) && !has(c.command) && !has(c.args)) : true)
                                      && volumes.all(v, !has(v.secret))
sc-ci-workspace            ADM-47  storageClassName == p.workspaceStorageClass ('ci-workspace'); requests.storage ≤ p.maxWorkspace (8Gi in ci-jobs, 10Gi in ci-build; CD-06);
                                      creator is the namespace's Woodpecker agent
sc-secret-nonempty         ADM-48  object.?data.orValue({}).all(k, size(object.data[k]) > 0) &&
                                      object.?stringData.orValue({}).all(k, object.stringData[k].trim() != '')
sc-namespace-guard         ADM-49  UPDATE: labels with prefix 'pod-security.kubernetes.io/' or 'strange.company/tier' unchanged unless
                                      username == p.fluxApplier || 'system:masters' in groups
                           ADM-50  DELETE of a namespace in the master-plan list: same identities only
sc-images-digest-pinned    ADM-51  object.spec.containers + initContainers + ephemeralContainers: image matches '@sha256:[a-f0-9]{64}$'
                                      (three separate .all() clauses; the lists have different element types)
sc-longhorn-images         ADM-52  every image ∈ p.longhornImages
sc-monitoring-node         ADM-53  per image (review SEC-10), from p.monitoringNodeProfiles:
                                      node-exporter image → hostPaths ⊆ {/, /proc, /sys, /var/lib/node_exporter/textfile}, all readOnly,
                                        runAsNonRoot, capabilities.add empty;
                                      otel-agent image (H03, plan 41) → hostPaths ⊆ {/var/log/pods, /var/lib/rancher/k3s/server/logs} readOnly
                                        plus /var/lib/otel-agent read-write, runAsUser 0 allowed, capabilities.add ⊆ ['DAC_READ_SEARCH'];
                                      any other image → denied; never privileged, never hostNetwork
sc-hostport-traefik-only   ADM-54  hostPort ∈ {80, 443, 2222} only when image == p.traefikImage and the controller owner is DaemonSet 'traefik';
                                      no hostNetwork/PID/IPC, no privileged, no hostPath; capabilities.add empty (Traefik binds 8000/8443);
                                      runAsNonRoot; seccomp RuntimeDefault
sc-no-nodeport-lb          ADM-55  !(object.spec.?type.orValue('ClusterIP') in ['NodePort', 'LoadBalancer'])
sc-worker-heavy-slot       ADM-56  request.namespace == 'agent-jobs'
                                        ? object.spec.containers.map(c, int(string(c.?resources.?limits[?'strange.company/heavy-slot'].orValue('0'))))
                                            .sum() == int(params.data.workerUnits)   /* pod SUM (two containers; plan 26 puts all units on harness) */
                                        : object.spec.containers.exists(c, int(string(c.?resources.?limits[?'strange.company/heavy-slot'].orValue('0'))) > 0)
                                      (binding: namespaces agent-jobs, ci-jobs, ci-build only — CD-16)
sc-priorityclass-by-namespace
                           ADM-57  variables.tier = namespaceObject.metadata.?labels[?'strange.company/tier'].orValue('');
                                      variables.tier == '' ||  /* unlabeled namespaces are restricted by PSA defaults and quotas */
                                      p.allowedPriorityClasses[variables.tier] (newline list) .exists(c, c == object.spec.?priorityClassName.orValue('')
                                        || (c.endsWith('*') && object.spec.?priorityClassName.orValue('').startsWith(c.substring(0, size(c) - 1))))
sc-upgrade-gate-scope      ADM-58  !request.userInfo.username.matches('^system:serviceaccount:[a-z0-9-]+:upgrade-gate$')
                                      || object.metadata.?labels[?'strange.company/upgrade-gate'].hasValue()
sc-runner-label-guard      ADM-59  request.userInfo.username == p.runnerIdentity
                                      || (request.resource.resource == 'pods' && p.podCreators.split('\n').exists(u, u == request.userInfo.username))
                                      || (request.operation == 'UPDATE' && 'system:masters' in request.userInfo.groups);
                                      and request.namespace == 'agent-jobs'
sc-dr-identities-scope     ADM-60  username == 'system:serviceaccount:dr-tests:restore-tester' → (object or oldObject).metadata.name.startsWith('rt-')
                                        && (request.namespace in ['dr-tests', 'longhorn-system'] || request.namespace == '')
                                      PV CREATE by restore-tester (review STO-6): spec.csi.driver == 'driver.longhorn.io'
                                        && spec.csi.volumeHandle == metadata.name && spec.storageClassName == 'longhorn-scratch'
                                        && !has(spec.hostPath) && !has(spec.local) && !has(spec.nfs)
                                      PVC CREATE in dr-tests: storageClassName ∈ {'local-path', 'longhorn-scratch'} and
                                        (spec.volumeName empty or startsWith('rt-'))
                           ADM-61  username == 'system:serviceaccount:dr-tests:cr-retention' → request.operation == 'DELETE' &&
                                        ((request.resource.resource == 'jobs' && (has(oldObject.status.completionTime)
                                            || oldObject.status.?conditions.orValue([]).exists(c, c.type == 'Failed' && c.status == 'True')))
                                         || (request.resource.resource == 'backups' && request.resource.group == 'postgresql.cnpg.io'
                                             && oldObject.status.?phase.orValue('') in ['completed', 'failed'])
                                         || (request.namespace == 'dr-tests' && oldObject.metadata.name.startsWith('rt-')))
sc-storageclass-scope      ADM-62  has(object.spec.storageClassName) && p.allowedStorageClasses.split('\n').exists(c, c == object.spec.storageClassName)
                                      && ((request.namespace in ['ci-jobs', 'ci-build']) == (object.spec.storageClassName == 'ci-workspace'))
                                      && request.namespace != 'agent-jobs'
sc-cnpg-cluster-create-guard
                           ADM-63  params.data.mode == 'initial-install'
                                      || (params.data.mode == 'recovery'
                                          && object.metadata.?annotations[?'strange.company/recovery-run'].orValue('') == params.data.runId)
                                      (missing ConfigMap = Deny; affects only CNPG Cluster CREATE in the four namespaces)
```

### 13.3 RBAC of every Strange Company ServiceAccount

"none" = no Role, ClusterRole or binding names the SA; `automountServiceAccountToken: false` on the SA and the pod. `sc-zero-rights-subjects` enforces "none" for the listed SAs.

| ServiceAccount | Rights | Token mounted | Rendered by |
|---|---|---|---|
| `strange-company/agent-runner` | exactly plan 26 §13 table (Jobs, Pods read, pods/log, Secrets, events in `agent-jobs`; Lease in `strange-company`; `hermes-managed-env` Secret and `hermes` Deployment patch in `hermes`). No ClusterRole | yes | plan 54 |
| `strange-company/model-gateway` | Role: `get` `configmaps` `resourceNames: [sc-emergency-stop]` in `strange-company` (plan 53 §13) | yes | plan 54 |
| `hermes/hermes` | **none** | no | plan 54 / 24 |
| `agent-jobs/agent-worker` | **none** | no | plan 54 |
| `woodpecker/woodpecker-server` | none | no | plan 20 |
| `woodpecker/woodpecker-agent` (verify pool) | Role in `ci-jobs`: `pods` create/delete/get/list/watch; `pods/log` get; `persistentvolumeclaims` create/delete/get; `services` create/delete/get (service containers); `secrets` none unless plan 20 shows the backend needs them (verify) | yes | plan 20 |
| `woodpecker/woodpecker-agent-build` (build pool) | same verbs, Role in `ci-build` only | yes | plan 20 / 23 |
| `ci-jobs/ci-step`, `ci-build/ci-step` | **none** | no | plan 20 / 23 |
| `renovate/renovate` | **none** | no | plan 23 |
| `dr-tests/recovery-checker` | **none** | no | plan 35 |
| `dr-tests/restore-tester` | per plan 44 (namespace `dr-tests` only) | per plan 44 | plan 44 |
| `monitoring/platform-probes` | **none** | no | plan 15 |
| `forgejo/forgejo`, `authentik/*` | **none** | no | plans 16, 15 |
| Upstream controllers (Flux, CNPG, Longhorn, cert-manager, kube-prometheus-stack, Grafana sidecar) | upstream chart RBAC, version-pinned; reviewed at each upgrade (plan 48) | yes | owning plans |

Humans: no RBAC binding to any human or Authentik group exists in MVP (master plan: Kubernetes administration is break-glass, plan 15). Flux kustomize-controller and helm-controller run with upstream `cluster-admin` (accepted residual risk R-3 in plan 34).

## 14. Happy path

1. **F11/F12 (offline):** `sc-gitops render` renders namespaces with PSA labels and the `policies` layer; `sc-gitops admission-params` output equals the committed ConfigMaps; `kubeconform` validates VAP objects against the pinned Kubernetes schema.
2. **F12 (k3d):** `make e2e-admission` creates a k3d cluster at the pinned k3s version with the §9.3 PSA config, applies `namespaces` + `policies` + the plan-36 NetworkPolicies + plan-51 quotas, then runs the escape matrix (§22.2).
3. **P04 (seed):** the `policies` layer is reconciled at seed step 3 (plan 19), before any workload layer; every seed-set workload is admitted (plan 19 G-checks fail otherwise).
4. **C02 (live):** the Runner dispatches fake executions whose Jobs are admitted; the live escape subset runs against the production cluster with server-side dry-run.
5. **P12:** verify check V-13 runs the live subset (`make e2e-admission-live --subset v13`).

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Legitimate Runner Job denied (policy/registry skew, e.g. harness digest updated in policy but params not yet applied) | Runner dispatch error `ADMISSION_DENIED` (infra failure); `SCAdmissionDenialSpike` | executions fail as infrastructure failures | Runner retries per infra budget; Flux converges params | `tdd-task-recovery.md`; fix via PR |
| Params ConfigMap missing/renamed | requests matched by that namespace-bound policy denied (`parameterNotFoundAction: Deny`); `SCAdmissionDenialSpike` | fail closed **only in that namespace** (no worker/CI pods); cluster-wide policies are parameter-free (37-R-1a), so nodes, controllers and Flux keep working and Flux re-creates the ConfigMap | Flux re-applies `policies-params` | revert the PR |
| CEL runtime error / cost limit | `apiserver_validating_admission_policy_check_total{error_type!=""}` > 0 | fail closed | none | fix policy via PR |
| Upstream chart change makes a platform pod non-compliant | Warn/Audit hits (`SCAdmissionAuditHits`) before promotion; Deny-mode denial events after | release stuck (`SCFluxReconciliationFailing`) | none | update allowlist/params via PR |
| Someone deletes or edits a VAP/binding | Flux drift detection reverts (`driftDetection: enabled`); audit log; `SCAdmissionPolicyMissing` (count of expected policies via KSM custom resource state, verify) | protection gap until reverted (≤ Flux interval) | Flux re-applies | `credential-compromise.md#cluster-and-node` if unexplained |
| PSA label removed from a namespace | `sc-namespace-guard` denies non-Flux edits; §9.3 default `restricted` applies anyway | none | — | — |
| Admission bypass by cluster-admin (deletes policies) | audit log (system:admin / break-glass); `SCBreakGlassUsed` | by design: break-glass outranks admission | — | `break-glass-access.md` |
| Job controller identity differs from assumption | 37-AC-3 fails on the pinned k3s | worker pods denied | — | switch `podCreators` param |

## 16. Retry behavior

Admission is synchronous and stateless; no retries inside this subsystem. Callers retry: the Runner treats admission denial of its own Job as an infrastructure failure (plan 26 retry budget, max 3 consecutive); Flux retries layer application every `retryInterval`.

## 17. Idempotency behavior

Policies are declarative; re-applying identical objects is a no-op (server-side apply). The parameter generator is deterministic (sorted keys and lists, LF line endings); identical inputs produce byte-identical ConfigMaps.

## 18. Bootstrap behavior

- k3s starts with the §9.3 PSA defaults (plan 07 configure).
- Seed (plan 19) applies `namespaces` (PSA labels) then `policies` before any pods except Flux controllers (already running in `flux-system`, created by `kubectl apply` before policies exist; they are restricted-compliant and re-admitted on restart).
- The `ingress` binding of `sc-hostport-traefik-only` is included only by `components/ingress-direct` (plan 18).
- `sc-ci-*` policies apply from seed but match nothing until Woodpecker (P08) and the build pool (P11) exist.

## 19. Upgrade behavior

- Kubernetes minor upgrade (plan 48): before the upgrade PR merges, CI runs `make e2e-admission` on k3d at the **target** minor; `enforce-version` labels move in the same PR.
- New harness image: promotion PR regenerates params (§8).
- Promotion of upstream-namespace bindings from `[Warn, Audit]` to `[Deny, Audit]` is a PR gated by 37-AC-40.
- VAP API version: `admissionregistration.k8s.io/v1` (GA 1.30). No beta APIs are used.

## 20. Recovery behavior

Admission objects are recreated from Git by Flux at black start (seed step 3). No state to restore. Cluster-wide policies cannot deadlock on a missing parameter (37-R-1a). If a broken policy blocks Flux itself from applying the fix (e.g. `sc-namespace-guard` mis-scoped), the break-glass path relies on `admissionregistration.k8s.io` objects not being subject to VAP (verify on the pinned minor, 37-AC-5) and is: `kubectl delete validatingadmissionpolicybinding <name>` as `system:admin` via `SC-BREAK-GLASS-CLUSTER`, merge the fix, let Flux re-create the binding (procedure `break-glass-access.md`, plan 15).

## 21. Observability

**Metrics** (kube-apiserver, scraped by plan 38): `apiserver_validating_admission_policy_check_total{policy, policy_binding, enforcement_action, error_type}` and `apiserver_validating_admission_policy_check_duration_seconds` (names verify on the pinned minor, 37-AC-1); PSA: `pod_security_evaluations_total{decision, mode, policy_level, resource, subresource}` (verify).

**Alerts** (catalogue names, CD-01):

| Alert | Expression sketch | Severity |
|---|---|---|
| `SCAdmissionDenialSpike` | `sum by (policy) (increase(apiserver_validating_admission_policy_check_total{enforcement_action="deny"}[15m])) > 5` | warning (security) |
| `SCAdmissionPolicyError` | `sum(increase(apiserver_validating_admission_policy_check_total{error_type!=""}[10m])) > 0` | critical |
| `SCAdmissionAuditHits` | `sum by (policy) (increase(apiserver_validating_admission_policy_check_total{enforcement_action="warn"}[1h])) > 0` | info |
| `SCPodSecurityViolation` | `sum by (policy_level) (increase(pod_security_evaluations_total{decision="deny",mode="enforce"}[15m])) > 0` | warning |
| `SCAdmissionPolicyMissing` | count of `ValidatingAdmissionPolicyBinding` objects named `sc-*` (KSM custom-resource state) < expected (from `cluster-vars`) for 10m | critical |

Denials of `sc-agent-jobs-*` also surface as Runner `SCWorkerPolicyViolation`/dispatch failures (plan 26) and feed `SCWorkerPolicyViolation` (catalogue).

**Logs:** API-server audit log (plan 07) records the policy name in audit annotations (`validation.policy.admission.k8s.io/validation_failure`, verify) and `sc-execution-id` for worker pods.

## 22. Testing strategy

### 22.1 Offline (F11/F12, no cluster)

- `kubeconform` of every policy and binding; `sc-gitops admission-params --check`.
- CEL unit tests: the policy expressions are compiled and evaluated with the Kubernetes CEL environment library at the pinned version (`k8s.io/apiserver/pkg/admission/plugin/policy/validating` test harness) against fixture objects under `test/admission/fixtures/{allow,deny}/<policy>/`; each deny fixture asserts its `ADM-<NN>` code.

### 22.2 k3d escape matrix (F12; also live at C02 with server-side dry-run)

Identities: **R** = impersonating the Runner (`--as=system:serviceaccount:strange-company:agent-runner`), **A** = cluster-admin without impersonation, **W** = impersonating the worker (`--as=system:serviceaccount:agent-jobs:agent-worker`), **H** = impersonating Hermes (`--as=system:serviceaccount:hermes:hermes`). Base object: plan 26's golden Job (`test/admission/fixtures/golden-job.yaml`) and its pod template rendered as a Pod with a fake Job owner (pods are created as **A** with `--as=system:serviceaccount:kube-system:job-controller` for pod-level cases). Each case mutates one field.

| # | SPEC §105 item | Attempt (one mutation) | Identity | Expected denial |
|---|---|---|---|---|
| E1 | run privileged | `containers[0].securityContext.privileged: true` (Job) | R, A | PSA `restricted` "privileged" **and** `ADM-10` |
| E2 | run privileged | add capability `SYS_ADMIN` | R | PSA + `ADM-10` |
| E3 | run privileged | `allowPrivilegeEscalation: true` | R | PSA + `ADM-10` |
| E4 | run as root | `runAsUser: 0`, `runAsNonRoot: false` | R, A | PSA + `ADM-07` |
| E5 | run as root | uid 999 (outside range) | R | `ADM-07` |
| E6 | mount hostPath | volume `hostPath: /` | R, A | PSA + `ADM-14` |
| E7 | mount hostPath | `hostNetwork: true` / `hostPID: true` | R | PSA + `ADM-06` |
| E8 | mount Kubernetes service tokens | `automountServiceAccountToken: true` | R, A | `ADM-05` |
| E9 | mount Kubernetes service tokens | projected `serviceAccountToken` volume | R | `ADM-14` |
| E10 | choose arbitrary ServiceAccounts | `serviceAccountName: default` | R, A | `ADM-04` |
| E11 | choose arbitrary ServiceAccounts | `serviceAccountName: agent-runner` | R | `ADM-04` |
| E12 | choose arbitrary ServiceAccounts | pod with `serviceAccountName: agent-worker` in `apps` | A | `ADM-38` |
| E13 | choose arbitrary Secrets | secret volume `sc-184-x2` in pod of `sc-184-x3` | R | `ADM-15` |
| E14 | choose arbitrary Secrets | `env.valueFrom.secretKeyRef: sc-registry-pull` / `envFrom` | R | `ADM-13` |
| E15 | choose arbitrary Secrets | Runner creates Secret with extra key `aws-credentials` | R | `ADM-32` |
| E16 | choose arbitrary Secrets | worker identity reads Secrets: `kubectl auth can-i get secrets -n agent-jobs --as W` | W | `no` (RBAC) |
| E17 | choose arbitrary images | `image: alpine:3` (tag) | R, A | `ADM-11` (+ `ADM-51`) |
| E18 | choose arbitrary images | digest-pinned image not in registry | R | `ADM-11` |
| E19 | choose arbitrary images | init container / ephemeral container (`kubectl debug`) | R, A | `ADM-09`, `ADM-36` |
| E20 | exceed resource profiles | memory limit `64Gi` | R | `ADM-17` |
| E21 | exceed resource profiles | no limits | R | `ADM-17` (LimitRange defaults are not accepted as a bypass: VAP evaluates after mutation, so the test also asserts limits injected by LimitRange ≤ max) |
| E22 | exceed resource profiles | `activeDeadlineSeconds` missing / 86400 | R | `ADM-18`, `ADM-25` |
| E23 | exceed resource profiles | emptyDir without `sizeLimit` / `medium: Memory` | R | `ADM-16` |
| E24 | exceed resource profiles | `parallelism: 5` | R | `ADM-24` |
| E25 | enter infrastructure namespaces | Runner creates Job in `strange-company`, `kube-system`, `forgejo` | R | RBAC forbidden **and** `ADM-42` |
| E26 | enter infrastructure namespaces | Runner creates Pod directly in `agent-jobs` | R | RBAC forbidden **and** `ADM-01` |
| E27 | enter infrastructure namespaces | worker / Hermes identity creates anything anywhere | W, H | RBAC forbidden **and** `ADM-41` |
| E28 | enter infrastructure namespaces | RoleBinding granting `edit` to `agent-jobs:agent-worker` or `hermes:hermes` | A | `ADM-40` |
| E29 | enter infrastructure namespaces | exec into a worker pod | R, A (non-masters user) | `ADM-37` |
| E30 | enter infrastructure namespaces | Job created in `agent-jobs` by any identity other than the Runner (e.g. A, or Flux applier) | A | `ADM-21` |
| E31 | bypass network policy | plan 36 suite: worker pod → kube-apiserver, `sc-pg:5432`, Longhorn UI, Flux, Prometheus, Hermes, public internet except allowed endpoints | (pod) | connection refused/timeout (36-AC-*) |
| E32 | bypass network policy | worker creates a NetworkPolicy | W | RBAC forbidden + `ADM-41` |
| E33 | obtain signing/deployment credentials | `kubectl auth can-i get secrets -n {ci-build,flux-system,strange-company} --as W` and `--as R` | W, R | `no` (R: only `agent-jobs`) |
| E34 | obtain signing/deployment credentials | a `ci-jobs` pod with `env.valueFrom.secretKeyRef` (any Secret) and a `ci-build` pod with image `golang@sha256:…` carrying env `SC_COSIGN_KEY` | woodpecker agents | `ADM-44` / `ADM-46` |
| E35 | obtain signing/deployment credentials | `sc-release` image pod with `command: [sh, -c, …]` and env `SC_COSIGN_KEY` | build agent | `ADM-46` |
| E36 | obtain signing/deployment credentials | worker Secret contains only the keys of its execution: inspect the live per-execution Secret during a fake run | (Runner) | keys ⊆ {task-token, gateway-token, deploy-key, known_hosts} |
| E37 | (PSA config) | namespace created without labels; privileged pod in it | A | PSA default `restricted` denies |
| E38 | (defense) | remove the PSA label from `agent-jobs` as a non-Flux identity | A (not masters, impersonating a test user with namespace update RBAC) | `ADM-49` |
| E39 | (empty secret) | Secret with label `strange.company/secret-class` and `data.k: ""` | A | `ADM-48` |
| E40 | (positive control) | the unmodified golden Job as R; its pod as the job controller | R | **admitted** |
| E41 | (resource protection) | worker pod without `strange.company/heavy-slot` limit, or with `1` instead of `workerUnits`; a `ci-jobs` step pod without any heavy-slot request (a light `dr-tests` check pod without heavy-slot is admitted, CD-16) | job controller, CI agent | `ADM-56` |
| E42 | enter infrastructure namespaces | Service `type: NodePort` in `apps`; `type: LoadBalancer` in `ingress` | A | `ADM-55` |
| E43 | exceed resource profiles | worker pod with `priorityClassName: platform-critical` | job controller | `ADM-57` |
| E44 | (forged ownership) | a Deployment in `apps` labelled `app.kubernetes.io/managed-by=agent-runner`; relabelling a Runner Job as cluster-admin non-masters user | A | `ADM-59` |
| E46 | (storage) | PVC in `agent-jobs`; `ci-workspace` PVC in `apps`; `longhorn` PVC in `ci-jobs`; `ci-workspace` PVC of 9Gi in `ci-jobs` / 11Gi in `ci-build` | A, agents | `ADM-62`, `ADM-47` |
| E47 | obtain signing/deployment credentials (review SEC-1) | with a worker deploy key: create branch `release/sc-probe`, push tag `v0.0.0-probe`, and push a tag on a worker-created commit; then wait for Woodpecker | worker key | Forgejo rejects both pushes **and** no `ci-build` pod and no release secret use occurs (executed by plan 23 `test/security/release-lane_test.go`, 23-AC-20) |
| E48 | obtain signing/deployment credentials (review SEC-3) | during a fixture release, `kubectl get pods -n ci-build -o json` as the Runner SA, the CI agent SA of `ci-jobs` and a `dr-tests` SA; grep every `ci-build` pod spec for `SC_COSIGN_` | R, others | `forbidden` for all except the build agent; no pod spec contains a release-secret value (23-AC-3) |
| E49 | (positive control, SEC-6) | a fake worker Job runs to completion | job controller | admitted; Job `Complete` |
| E50 | (positive control, SEC-6) | TTL-after-finished deletes a finished worker Job (foreground) | TTL controller, GC | admitted; Job and pods gone within 2 min |
| E51 | (positive control, SEC-6) | the Runner deletes a Job with `propagationPolicy: Background` after log capture | R, GC | admitted; Secret garbage-collected |
| E52 | (positive control, SEC-6) | `agent_cancel`: Runner patches `suspend`, then deletes | R | admitted |
| E53 | (node host access, SEC-10) | `monitoring-node` pod with the otel-agent image mounting `/` | A | `ADM-53` |
| E54 | (node host access, SEC-10) | `monitoring-node` pod with the node-exporter image adding `DAC_READ_SEARCH` or running as uid 0 | A | `ADM-53` |
| E55 | run as root where forbidden (SEC-19) | worker pod with `fsGroup: 0`, `supplementalGroups: [0]` or container `runAsGroup: 0` | job controller | `ADM-07b` |
| E56 | (storage escalation, STO-6) | as `dr-tests:restore-tester`: PV `rt-x` with `hostPath: /`; PV `rt-y` with `csi.volumeHandle: forgejo-data` | impersonated SA | `ADM-60` |
| E57 | (secret scope, SEC-24) | as the Runner: create Secret `other` in `hermes` | R | `ADM-42` |
| E58 | (credential isolation, TDD-14) | worker pod whose `harness` container mounts `creds`, or whose `harness-creds` projects `deploy-key`, or a third container | job controller | `ADM-15b` / `ADM-15a` |
| E59 | (CNPG creation guard, DR-7) | create `Cluster forgejo-pg` with no `sc-cnpg-create-window` ConfigMap; then with `mode: recovery` and a mismatching `recovery-run` annotation | A | `ADM-63` |
| E60 | (Hermes rollout scope) | as the Runner: patch Deployment `hermes` changing the image or adding a volume | R | `ADM-42` |
| E45 | (narrow destructive identities) | as `dr-tests:cr-retention`, delete a running Job in `forgejo`; as `dr-tests:restore-tester`, create a CNPG `Cluster` named `forgejo-pg` in `dr-tests` | impersonated SAs | `ADM-61`, `ADM-60` |

E40 is the positive control. Each case is a table-driven Go test (`test/admission/escape_test.go`) that runs `kubectl apply --dry-run=server` (or `kubectl auth can-i`) through client-go impersonation and asserts: (a) denied, (b) the error contains the expected code or PSA phrase, (c) nothing was persisted. E40 and the C02 live run use real creates.

### 22.3 Live (C02 and V-13)

`make e2e-admission-live` runs E1, E4, E6, E8, E10, E13, E17, E20, E22, E25, E28, E33, E40 against the live cluster with server-side dry-run (no persistence), plus a real fake-harness execution (plan 26 26-AC-22). V-13 runs E1, E6, E10 (plan 50 table).

## 23. Acceptance criteria

### GATE-F12

- [ ] **37-AC-1** Version re-verification on the pinned k3s minor (k3d): VAP v1 with `paramKind` ConfigMap, `parameterNotFoundAction: Deny`, `variables`, `messageExpression`, `auditAnnotations`, CEL `quantity()`, strings `split`/`join`, optional `?.`/`orValue`, and CONNECT matching for `pods/exec` all work: `make e2e-admission TEST=TestCELFeatures` exits 0; the metric `apiserver_validating_admission_policy_check_total` exists on `/metrics`.
- [ ] **37-AC-2** `sc-gitops admission-params --check` exits 0 on `main`; changing one harness digest in `policy/harnesses.yaml` without regenerating makes F11 fail naming `admission-params-drift`.
- [ ] **37-AC-3** On k3d at the pinned version, a pod created by the Job controller for a Job in `agent-jobs` has `request.userInfo.username` equal to the `podCreators` entry (asserted from the audit log line of the create); if not, the test prints the observed identity and fails.
- [ ] **37-AC-4** Every escape case E1–E39, E41–E46 and E53–E60 is denied with its expected code/phrase, E40 and E49–E52 are admitted (`go test ./test/admission/... -run TestEscapeMatrix` exits 0; the report lists 58 cases; E47/E48 run in plan 23's suite)
- [ ] **37-AC-5** Deleting `flux-system/sc-admission-agent-jobs` makes the golden Job (E40) denied (fail closed), and re-creating it admits again. Then, for **each** parameter ConfigMap in turn (k3d): delete it and require that all nodes stay `Ready` for 5 min, `kube-node-lease` renewals succeed, a Deployment in `apps` can still be scaled, and Flux restores the ConfigMap within one reconcile interval (review SEC-6). As `system:admin`, deleting a `ValidatingAdmissionPolicyBinding` succeeds while every `sc-*` policy is active.
- [ ] **37-AC-6** Rendered namespaces (`sc-gitops inventory --kind Namespace --cluster ci-shm-tunnel`) carry exactly the §13.1 enforce/audit/warn levels and `enforce-version` equal to the pinned minor; `ci-sha-direct` renders `ingress` as `privileged` with the `sc-hostport-traefik-only` binding, `ci-shm-tunnel` as `baseline` without it.
- [ ] **37-AC-7** RBAC assertions (k3d, rendered chart): for each "none" SA in §13.3, `kubectl auth can-i --list --as=system:serviceaccount:<ns>:<sa>` shows only the default discovery/self-review rules; for the Runner, the list equals plan 26 §13 (26-AC-20 shared); `kubectl get clusterrolebindings,rolebindings -A -o json | jq` finds no subject in `zeroRightsServiceAccounts`.
- [ ] **37-AC-8** Every policy and binding object in `gitops/platform/admission/` has `failurePolicy: Fail`, and every binding with a `paramRef` has `parameterNotFoundAction: Deny` (`yq` check in F11).
- [ ] **37-AC-9** CEL unit fixtures: every `ADM-<NN>` code has at least one deny fixture and every policy at least one allow fixture (`go test ./test/admission/cel/...`).
- [ ] **37-AC-10** `kubectl debug` (ephemeral container) on a running fake worker pod in k3d is denied with `ADM-36` even as cluster-admin, and `kubectl exec` is denied for a non-`system:masters` identity holding `pods/exec` RBAC (`ADM-37`).
- [ ] **37-AC-30** (review SEC-4) For every ServiceAccount in the rendered install (k3d), `kubectl auth can-i get secrets -n flux-system --as system:serviceaccount:<ns>:<sa>` is `no`, except the identities in the committed allowlist `gitops/platform/admission/secret-readers-allowlist.yaml` (kustomize-controller, helm-controller and each upstream operator whose chart grants cluster-wide Secret read, recorded with its chart version); a new identity not in the list fails the test (plan 34 R-22).

### GATE-C02

- [ ] **37-AC-20** On the live cluster, `./bootstrap exec -- make e2e-admission-live` exits 0 (the §22.3 subset) and `kubectl get jobs,pods,secrets -n agent-jobs -l sc-test=admission` is empty afterwards (dry-run persisted nothing).
- [ ] **37-AC-21** A fake-harness execution dispatched by the Runner (plan 26 26-AC-22) is admitted; its pod spec satisfies every `sc-agent-jobs-pod` rule (re-evaluated offline from `kubectl get pod -o yaml` by the CEL test harness).
- [ ] **37-AC-22** Runner admission independence (shared with 26-AC-28): the golden Job with `serviceAccountName: default`, with `privileged: true`, and with a non-registry image, submitted with the Runner's identity, are each denied by admission.
- [ ] **37-AC-23** Live verify V-13 passes in `./bootstrap verify --only V-13`.

Additional gate items owned here but evaluated later:

- [ ] **37-AC-41** (C02/P11, with plans 20/23) Woodpecker kubernetes-backend behaviour re-verification: a verify-pool pipeline's pods and PVC are admitted by `sc-ci-jobs-pod`/`sc-ci-workspace`, and the observed pod labels, SA, PVC name prefix and storage class match the params.
- [ ] **37-AC-43** (non-blocking tightening probe) In k3d, tunnel-mode `ingress` pods (Traefik, cloudflared) rendered with PSA `restricted` become Ready; the result is recorded in `versions/compat.yaml` (`ingressRestricted: true|false`) and, if true, a PR may tighten §13.1.

### GATE-C10 (contributed)

- [ ] **37-AC-42** Admission latency: p99 of `apiserver_validating_admission_policy_check_duration_seconds{policy="sc-agent-jobs-pod"}` < 0.005 over a C10 fake run.

### GATE-R02 (contributed; needs ≥ 7 days of live operation)

- [ ] **37-AC-40** (promotion gate for 37-R-2) For 7 consecutive days on the live cluster, `sum(increase(apiserver_validating_admission_policy_check_total{enforcement_action="warn",policy=~"sc-images-digest-pinned|sc-longhorn-images"}[7d]))` = 0; then the PR switching those bindings to `[Deny, Audit]` merges and `make e2e-admission-live` still passes.

## 24. Explicit non-goals

- Admission-time image **signature** verification (post-MVP; plan 23 §14.7 describes the target: Kyverno `verifyImages` or sigstore policy-controller).
- Mutating admission (MutatingAdmissionPolicy or webhooks): policy never "fixes" a request.
- Admission control of human `kubectl` beyond PSA/VAP: humans have no RBAC except break-glass.
- Seccomp profiles stricter than RuntimeDefault (custom Localhost profiles) and AppArmor/SELinux policy authoring.
- Network policy content (plan 36).

## 25. Open questions

1. **Job-controller identity** (blocking: no). Assumed `system:serviceaccount:kube-system:job-controller`; 37-AC-3 re-verifies; the param switches to `system:kube-controller-manager` if k3s does not use per-controller credentials. Owner: 37/07.
2. **Woodpecker kubernetes backend specifics** (blocking: no for F12, yes for promoting `sc-ci-*` beyond `[Deny, Audit]` on the live cluster): how Woodpecker injects `from_secret` values into step pods (inline env vs. per-step Kubernetes Secret), whether plugin steps carry `command`, and the pod labels. Owner: 20/23/37, verified by 37-AC-41.
3. **Plan 11 says no PVC in `ci-jobs`** (blocking: no). Resolved by ADR-0305 (plan 20): only `ci-workspace` PVCs, enforced by `sc-ci-workspace`. Owner: 11/20/37.
4. **Master plan §5 changes**: resolved by CD-13 (`ci-build` baseline, `monitoring-node` privileged, `ingress` baseline/tunnel and privileged + `sc-hostport-traefik-only`/direct).
5. **Flux `dependsOn` and revision skew** (blocking: no): if the `strange-company` layer can apply a new harness digest before `policies` applies the new params, early dispatches fail as infrastructure failures until Flux converges (bounded by one reconcile interval). Accepted; verify whether Flux waits for the dependency at the same source revision. Owner: 18/37.
