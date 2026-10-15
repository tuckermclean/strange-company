# 51 — Capacity, scheduling priority and quotas

**Status:** Stage-0 plan (draft)
**DAG nodes:** F12, P04, P12, C02, H04 (all listed in `plans/dag.yaml`, GC-09), plus R02 (contributes 51-AC-16; add `"51"` to R02).
**Related plans:** [07](07-k3s.md) (kubelet flags), [08](08-node-security.md) (host services), [11](11-storage.md) (disk budget), [12](12-cnpg.md), [18](18-flux.md) (`namespaces` layer), [20](20-woodpecker.md), [26](26-agent-runner.md) (dispatcher, Job builder, `resource-profile.schema.yaml`), [36](36-network-policy.md), [37](37-admission-policy.md), [38](38-prometheus.md), [42](42-alerting-notifications.md), [44](44-backup.md), [47](47-ha-expansion.md), [50](50-bootstrap-orchestrator.md) (V-30), [54](54-strange-company-chart.md)
**Contracts:** consumes `resource-profile.schema.yaml` (owned by plan 26; the numbers are owned here); produces none
**ADRs:** relies on ADR-0020, ADR-0014; introduces ADR-0156 (heavy-execution slot as a node extended resource; exclusive by default on one node)

## 1. Purpose

Guarantee that infrastructure always outranks autonomous labor for CPU, memory and disk, and make the capacity of a node an explicit, measured budget rather than an assumption. This plan owns the PriorityClasses, the kubelet reservation and eviction numbers (rendered by plan 07), the ResourceQuota and LimitRange objects for `agent-jobs`, `ci-jobs` and `apps`, the concurrency limits and where each is enforced, the per-component memory budget for 12 GiB and 8 GiB nodes, the numbers of the `standard` and `constrained` resource-profile sets, the heavy-execution slot that keeps workers and CI from running together on small nodes, the ephemeral-disk budget, and the method and formula of the measured budget gate.

## 2. Requirements derived from SPEC.md

1. Infrastructure has higher scheduling priority than autonomous labor: platform-critical > platform > application > CI > agent-worker. [SPEC §89]
2. `agent-jobs` has a ResourceQuota, a LimitRange, a concurrency limit and per-job hard limits. [SPEC §89]
3. Bootstrap defaults: at most 1 agent worker and 1 heavy CI job; small-HA initial default: at most 2 agent workers. [SPEC §89, §11, §12]
4. "A slow worker is acceptable. A worker OOM-killing etcd is not." [SPEC §89]
5. Hermes selects logical resource profiles; exact values are tunable policy; the Runner owns the Job manifest. [SPEC §83, §62]
6. Workers cannot exceed resource profiles (CI-verified). [SPEC §105]
7. Root disks must not fill because cleanup was omitted. [SPEC §107]
8. Node shapes for both profiles (SPEC v1.3, CD-23, CD-25): **supported minimum** 4 vCPU / 12 GiB / 100 GiB; **constrained lower bound** 4 vCPU / 8 GiB / 100 GiB (the `constrained` resource profile, with a warning and no capacity guarantee: expected to fail V-30 on self-hosting-minimum, and not budgeted for small-ha, §12.2–§12.3); preferred 4 vCPU / 12 GiB / 200 GiB NVMe. Providers may choose larger, never silently smaller. The measured V-30 gate is authoritative. [SPEC §11, §12]

Derived requirements:

- **51-R-1** Worker and CI pods have memory requests equal to limits. A worker can only ever exhaust its own container limit.
- **51-R-2** The k3s process (and therefore etcd) is outside the `kubepods` cgroup, whose `memory.max` equals node allocatable (`enforce-node-allocatable=pods`). Pods collectively cannot take memory reserved for k3s and the OS (plan 07 07-AC-16/18 verify).
- **51-R-3** Platform memory requests are set to the measured p95 working set (±10 %), so that kubelet eviction ranking ("usage above request first") does not single out platform pods. The P12 and H04 gates check this.
- **51-R-4** On a single node (`sharedHeavySlot: true`), at most one of {a worker, a CI workflow, a restore test, a Renovate run} executes at a time.
- **51-R-5** Every capacity number has one source in Git and is checked by CI against the others (profile limits × concurrency ≤ quota; quota + platform requests ≤ modeled allocatable).

## 3. Inputs

| Input | Source | Used for |
|---|---|---|
| Node MemTotal, vCPU, root fs size | provider output (plan 03), Ansible facts | size class, reservations, ephemeral reserve |
| Size class | `standard` if MemTotal ≥ 11 GiB, else `constrained` (plan 07 §9.1) | reservations, active profile set |
| Profile (`self-hosting-minimum` / `small-ha`) | `cluster-vars.SC_PROFILE` | concurrency, slot mode |
| Component requests/limits | owning plans' §12 | budget table |
| Measured working sets | Prometheus at P12/H04 | gate, request calibration |

## 4. Outputs

- `infra/ansible/group_vars/all/kubelet.yaml`: reservation and eviction values (rendered into the k3s cluster file by plan 07).
- `gitops/platform/namespaces/base/priorityclasses.yaml` (layer `namespaces`, seed set).
- `gitops/platform/namespaces/base/{agent-jobs,ci-jobs,apps}-quota.yaml` and `-limitrange.yaml` (layer `namespaces`; profile overlays set the numbers).
- `gitops/platform/capacity/` (new layer `capacity`, §8.3): ConfigMap `kube-system/heavy-slot-config`, CronJob `kube-system/heavy-slot-advertiser`, its ServiceAccount and RBAC.
- The numbers in `policy/resource-profiles.yaml` (`spec.sets.standard`, `spec.sets.constrained`, `spec.concurrency`) (§9.5).
- `make capacity-check` (static model) and the V-30 evidence format (§14).

## 5. Dependencies

**Hard:** F11 (render) for the static checks; P03 for live allocatable; P09 (Prometheus) for measurement.

**Soft / runtime and assumed interfaces:**
- **Plan 07** renders §9.2–§9.3 into `kubelet-arg`.
- **Plan 18** adds layer `capacity` (`dependsOn: [namespaces]`, not in the seed set) and makes `woodpecker` and `strange-company` depend on it, because their pods request `strange.company/heavy-slot` (Q-51-02).
- **Plan 26** (Runner): sets `priorityClassName: agent-worker` and `resources.limits["strange.company/heavy-slot"]` = `heavySlot.workerUnits` on worker containers; sets the worker container `ephemeral-storage` limit to `ephemeralStorageLimit + workspaceSize` (the kubelet counts emptyDir usage against the pod's summed container limits; verify, 51-AC-14); applies the dispatch gate in §9.7 **before creating the Job** (RA-7): the execution stays `QUEUED`, and the Job is created only when the gate is open **and** some node has at least `workerUnits` unallocated heavy-slot units, computed without reading CI pods (SEC-3): node `status.allocatable` minus the units of non-terminated pods the Runner may list (`agent-jobs`, `renovate`, `dr-tests`), and treated as fully allocated in exclusive mode while the Woodpecker queue reports any pending or running workflow (both lanes). A Job is therefore never Pending behind CI, so neither its Job-level `activeDeadlineSeconds` nor the `startTimeoutSeconds` watchdog runs while waiting. A residual race (CI pod scheduled between the check and the worker pod) is bounded by the normal start timeout. An execution that stays `QUEUED` past plan 29's `spec.retry.infrastructure.queueTimeout` (default 2 h, `policy/verification.yaml`) fails with `QUEUE_TIMEOUT` (failure class infrastructure, no attempt consumed; plan 29 is the single definition). If the Woodpecker queue API is unreachable, the gate stays **closed** (fail-closed) and `sc_runner_dispatch_gate_unknown` is raised. The Runner has read-only `pods` `list,watch` RoleBindings only in `renovate` and `dr-tests` (plan 54 renders them). It has **no** pod read in `ci-jobs` or `ci-build` (SEC-3: release step pod specs may carry injected secrets); CI activity is known only through the Woodpecker queue API.
- **Plan 20** (Woodpecker): agent `WOODPECKER_MAX_WORKFLOWS=1`; step pods get `priorityClassName: ci` through the Kubernetes backend's priority-class setting (verify the option name; if the pinned Woodpecker cannot set it, step pods run at the `application` global default and §9.1's quota still allows that); pipeline timeout ≥ 120 min on installations with `sharedHeavySlot: true` (a step may wait for a running worker).
- **Plan 37:** VAP `sc-worker-heavy-slot` requires every container of an `agent-jobs` pod to set `limits["strange.company/heavy-slot"]` equal to `heavySlot.workerUnits` (from VAP params) so that the Runner cannot forget the slot; VAP `sc-priorityclass-by-namespace` as a backstop to the quotas in §9.1.
- **Plan 44** schedules restore-test CronJobs with `concurrencyPolicy: Forbid` and `priorityClassName: ci`, requesting 1 heavy-slot unit.

## 6. Owned state

| State | Location | System of record | Writers |
|---|---|---|---|
| Reservation/eviction numbers | `infra/ansible/group_vars/all/kubelet.yaml` | Git | PR with re-measurement |
| PriorityClasses, quotas, limit ranges | `gitops/platform/namespaces/` | Git | PR |
| Profile numbers and concurrency | `policy/resource-profiles.yaml` | Git (protected path) | PR, human merge |
| Heavy-slot mode | `gitops/platform/capacity/overlays/<profile>/heavy-slot-config.yaml` | Git | PR (plan 47 at expand) |
| Advertised capacity | Node `.status.capacity["strange.company/heavy-slot"]` | derived | `heavy-slot-advertiser` |
| Gate evidence | bootstrap verify report (plan 50), H04 drill record | journal / S3 | `sc-bootstrap verify` |

## 7. External interfaces

| Interface | Consumer | Definition |
|---|---|---|
| PriorityClass names | every workload plan | §9.1 |
| Extended resource `strange.company/heavy-slot` | Runner Job builder, Woodpecker step pods (LimitRange default), restore tests, Renovate | §9.6 |
| `policy/resource-profiles.yaml` numbers | Runner (plan 26), Hermes (names only) | `resource-profile.schema.yaml` |
| Quota/LimitRange names | V-14 (plan 50), plan 37 tests | §9.4 |
| V-30 formula | plan 50 `verify` | §14.2 |

## 8. Internal interfaces

### 8.1 Priority, preemption and eviction: how a worker is prevented from starving the platform

1. **Admission and scheduling:** worker and CI pods declare `requests == limits` for memory (LimitRange `maxLimitRequestRatio: memory: 1`). The scheduler places them only where requests fit allocatable, alongside platform requests.
2. **Isolation:** the container memory limit caps a worker; exceeding it OOM-kills that container only.
3. **Node-level reservation:** `kubepods` `memory.max` = allocatable (51-R-2). k3s/etcd and host daemons live outside it, with `oom_score_adj` −999 for k3s (plan 07).
4. **Preemption:** platform pods (`platform-critical`, `platform`) that cannot schedule preempt `ci` and `agent-worker` pods. `ci` and `agent-worker` never preempt (`preemptionPolicy: Never`).
5. **Eviction ranking under pressure** (kubelet order: usage above request first, then priority, then usage relative to request): workers and CI never exceed their requests, so they are ranked behind any platform pod that has exceeded its request. 51-R-3 (requests ≈ measured p95) makes "platform pod above request" rare; when two pods both exceed requests, priority decides and workers lose.
6. **Slot exclusion** (single node): §9.6.

A slow worker results only from CPU contention (CPU requests give the platform its share; workers are CPU-limited at 2 cores). No path lets a worker take memory from etcd.

### 8.2 Heavy-slot units (ADR-0156)

The G2 brief described capacity 1 (exclusive) and 1 unit per pod. A Woodpecker workflow runs several pods at once (step pods plus service pods), so with capacity 1 a workflow with a service container would deadlock against itself. Units fix this while keeping the exclusion:

| Mode | Node capacity | Worker pod requests | Each `ci-jobs` / `renovate` / `dr-tests` pod requests | Effect |
|---|---|---|---|---|
| exclusive (`sharedHeavySlot: true`) | 8 | 8 (all units) | 1 (`ci-jobs`, `renovate`); **8** (`ci-build`, heavy restore tests in `dr-tests`, STO-23); light checks 0 (CD-16) | a worker excludes all CI/batch pods and vice versa; up to 8 ci-lane/batch pods coexist; a build-lane pod excludes everything else |
| shared (`sharedHeavySlot: false`) | 64 | 8 | same | no exclusion in practice; quotas, Woodpecker and the Runner limit concurrency |

Plan 20 proposed a separate `strange.company/ci-slot` with capacity `CI_MAX_CONCURRENT` and one unit per pod. That deadlocks a workflow whose service pod and step pod run together on a capacity-1 node; this resource replaces it (Q-51-07). Build-lane pods take all 8 units so that, on one node, a release build never runs beside a ci-lane workflow (SPEC §89 "maximum heavy CI jobs: 1" across both lanes). The consequence is a constraint on plan 23: build-lane workflows (`.woodpecker/release.yaml`, protected path) use sequential steps and no `services:`; otherwise they would wait on themselves. `SCHeavySlotDeadlock` (§21) detects a violation.

### 8.3 Heavy-slot advertiser

- ConfigMap `kube-system/heavy-slot-config`: `mode: exclusive|shared`, `exclusiveCapacity: "8"`, `sharedCapacity: "64"`, `workerUnits: "8"`.
- CronJob `kube-system/heavy-slot-advertiser`: `schedule: "*/5 * * * *"`, `concurrencyPolicy: Forbid`, `startingDeadlineSeconds: 120`, `successfulJobsHistoryLimit: 1`, `failedJobsHistoryLimit: 3`, Job `activeDeadlineSeconds: 120`, `backoffLimit: 1`, `priorityClassName: system-cluster-critical`, image `registry.k8s.io/kubectl:v1.36.5` (baseline kubectl entry, `plans/compatibility-baseline.yaml`, which records that the image exists; fallback `rancher/kubectl`; digest recorded in `versions/platform.lock.yaml`, plan 01), restricted security context. For every node it runs `kubectl patch node <n> --subresource=status --type=merge -p '{"status":{"capacity":{"strange.company/heavy-slot":"<cap>"}}}'` (verify subresource patch on the pinned kubectl/k3s, 51-AC-6) and annotates `strange.company/heavy-slot-advertised-at=<unix>`. It is idempotent and re-asserts the value in case a kubelet restart drops it.
- RBAC: ClusterRole `sc-heavy-slot-advertiser` with `nodes` get/list/patch and `nodes/status` patch; nothing else.
- Lowering capacity (shared → exclusive) never evicts running pods; it applies to new scheduling.

### 8.4 One exclusion mechanism (authoritative)

`strange.company/heavy-slot` is the **only** mutual-exclusion mechanism for heavy execution (consolidation decision CD-05). The Lease `strange-company/heavy-execution` (formerly assumed by plans 12, 43 and 44) and the `strange.company/ci-slot` (formerly proposed by plan 20) are **withdrawn**; no component may create, acquire or wait on either. A Lease is advisory: a holder that crashes keeps it until expiry, and nothing stops a pod that never asks for it. An extended resource is enforced by the scheduler for every pod, released when the pod terminates, and needs no client code.

| Pod kind | How it acquires units | Units (exclusive / shared) | Who guarantees it |
|---|---|---|---|
| Worker Job pod (`agent-jobs`) | the Runner's Job builder sets `resources.limits["strange.company/heavy-slot"]` on the worker container (plan 26) | 8 / 8 | VAP `sc-worker-heavy-slot` rejects worker pods without it (plan 37) |
| Woodpecker ci-lane step and service pods (`ci-jobs`) | LimitRange `ci-jobs-limits` default and defaultRequest `strange.company/heavy-slot: 1` per container (fallback: Woodpecker backend default resources, plan 20) | 1 per container | LimitRange max 1; VAP in `ci-jobs` requires the resource (plan 37) |
| Woodpecker build-lane pods (`ci-build`) | LimitRange default 8 per container (plan 23 namespace, this plan's requirement) | 8 | same pattern |
| Heavy restore-test Jobs (`dr-tests`: scratch CNPG cluster or Longhorn restore) | the Job's pod sets `resources.limits["strange.company/heavy-slot"]` = `workerUnits` (8) explicitly, like a build-lane pod (CD-16 "request units like a CI job"; STO-23: the scratch cluster's ≈ 1.3–2 GiB would otherwise run beside a full CI workflow and be counted nowhere). The Job pod holds the units for the whole test, so the CNPG and Longhorn pods it creates (which request none) run under its lock. Light checks (restore-test checks, audits, retention jobs) request none (CD-16) | 8 / 0 | plan 44 keeps its own Lease `dr-tests/restore-slot` only to serialize restore tests among themselves |
| Renovate (`renovate`) | LimitRange default 1 | 1 | — |

The Runner additionally gates dispatch (§9.7) so that, in exclusive mode, a worker is not created while CI or batch pods hold units; the scheduler is the enforcement, the gate only avoids long Pending periods.

**Priority classes of batch work:** restore tests and Renovate use `ci` (`preemptionPolicy: Never`), not `platform`. With `platform` they would preempt a running worker whenever they wait for units, turning every nightly restore test into a killed task. Plan 44 uses `ci` (Q-51-09), and the `dr-tests` zero-pod quota (§9.1) denies `platform`.

## 9. Configuration

### 9.1 PriorityClasses (layer `namespaces`)

| Name | Value | preemptionPolicy | globalDefault | Used by |
|---|---|---|---|---|
| `platform-critical` | 1000000 | PreemptLowerPriority | false | Flux, cert-manager, CNPG operator and all CNPG instances, Longhorn, Traefik, cloudflared, Forgejo, kured, egress-proxy is **not** here |
| `platform` | 100000 | PreemptLowerPriority | false | Authentik, Woodpecker server/agent, monitoring stack (Prometheus, Alertmanager, KSM, Grafana, OTel, blackbox, platform-probes, Loki, Tempo, node-exporter, otel-agent), Agent Runner, Model Gateway, egress-proxy, Hermes |
| `application` | 10000 | PreemptLowerPriority | **true** | `apps` (and any pod that names no class) |
| `ci` | 1000 | Never | false | `ci-jobs` and `ci-build` step pods, Renovate, restore tests (`dr-tests`) |
| `agent-worker` | 100 | Never | false | `agent-jobs` only |

CoreDNS, metrics-server and the heavy-slot advertiser use the built-in `system-cluster-critical`. Reading of the brief: `preemptionPolicy: Never` applies to `ci` and `agent-worker` (they wait instead of evicting); `application` may preempt them (SPEC orders application above CI and workers).

**Zero-pod quotas** (ResourceQuota `deny-higher-priority`, `hard: {pods: "0"}`):

| Namespace | `scopeSelector` PriorityClass `In` |
|---|---|
| `agent-jobs` | `platform-critical`, `platform`, `application`, `ci` (so a worker must name `agent-worker` explicitly) |
| `ci-jobs`, `ci-build`, `renovate`, `dr-tests` | `platform-critical`, `platform` |
| `apps` | `platform-critical`, `platform` |

### 9.2 Node reservations (rendered by plan 07)

| Item | standard (MemTotal ≥ 11 GiB) | constrained (< 11 GiB) |
|---|---|---|
| `system-reserved` | `cpu=250m,memory=512Mi,ephemeral-storage=<computed>` | `cpu=250m,memory=448Mi,ephemeral-storage=<computed>` |
| `kube-reserved` | `cpu=500m,memory=1024Mi` | `cpu=500m,memory=896Mi` |
| `enforce-node-allocatable` | `pods` | `pods` |
| ephemeral reserve | `fsSize − floor(0.20 × fsSize)` (allocatable ephemeral ≈ 20 % of root) | same |

### 9.3 Eviction and garbage collection (rendered by plan 07)

| Signal | Soft (grace) | Hard | Minimum reclaim |
|---|---|---|---|
| `memory.available` | `600Mi` (90 s) | `300Mi` | `200Mi` |
| `nodefs.available` | `15%` (2 m) | `10%` | `2Gi` |
| `nodefs.inodesFree` | — | `5%` | — |
| `imagefs.available` | — | `10%` | — |

`eviction-max-pod-grace-period=60`; image GC high/low 80/70; container logs 10 MiB × 3; `shutdown-grace-period=90s`, `shutdown-grace-period-critical-pods=30s` (graceful node shutdown; verify that k3s's kubelet honors it with systemd inhibitor locks, 51-AC-15).

### 9.4 ResourceQuota and LimitRange

**`agent-jobs`** (values for the active set; `W` = `concurrency.maxWorkers`, `M` = largest `memoryLimit` in the active set, `E` = largest `ephemeralStorageLimit + workspaceSize`):

| Object | Field | standard, W=1 | standard, W=2 (small-ha) | constrained, W=1 |
|---|---|---|---|---|
| ResourceQuota `agent-jobs-compute` | `pods` | 2 (W+1: a terminating pod still counts) | 3 | 2 |
| | `requests.cpu` / `limits.cpu` | 1 / 2 | 2 / 4 | 1 / 2 |
| | `requests.memory` = `limits.memory` | 3Gi (W × M) | 6Gi | 2Gi |
| | `requests.ephemeral-storage` = `limits.ephemeral-storage` | 8Gi (W × E) | 16Gi | 5Gi |
| | `requests.strange.company/heavy-slot` | 8 | 16 | 8 |
| | `persistentvolumeclaims`, `services`, `services.nodeports`, `services.loadbalancers` | 0 | 0 | 0 |
| | `count/jobs.batch`, `secrets`, `configmaps` | 10, 6, 4 | 10, 8, 4 | 10, 6, 4 |
| LimitRange `agent-jobs-limits` (Container) | `max` | cpu 2, memory 3Gi, ephemeral-storage 8Gi | same | cpu 2, memory 2Gi, ephemeral-storage 5Gi |
| | `min` | cpu 100m, memory 256Mi | same | same |
| | `default` / `defaultRequest` | the `tiny` profile (cpu 1 / 250m, memory 1Gi / 1Gi, ephemeral 3Gi / 3Gi) | same | cpu 1 / 250m, memory 768Mi / 768Mi, ephemeral 3Gi |
| | `maxLimitRequestRatio` | memory 1 | memory 1 | memory 1 |

The policy loader invariant in `resource-profile.schema.yaml` (`maxWorkers × largest memoryLimit ≤ agent-jobs limits.memory`) holds by construction; `make capacity-check` re-checks it against the rendered quota.

**`ci-jobs`:**

| Object | Field | standard | constrained |
|---|---|---|---|
| ResourceQuota `ci-jobs-compute` | `pods` | 8 | 6 |
| | `requests.memory` = `limits.memory` | 3Gi | 2Gi |
| | `requests.cpu` / `limits.cpu` | 2 / 4 | 1 / 2 |
| | `requests.ephemeral-storage` = `limits.ephemeral-storage` | 4Gi (workspace is the `ci-workspace` PVC, not ephemeral) | 3Gi |
| | `requests.strange.company/heavy-slot` | 8 | 8 |
| | `ci-workspace.storageclass.storage.k8s.io/persistentvolumeclaims` | `CI_MAX_CONCURRENT` + 1 (one PVC per workflow, ADR-0305) | 2 |
| | `ci-workspace.storageclass.storage.k8s.io/requests.storage` | 12Gi (self-hosting-minimum: the filesystem size) / 24Gi (small-ha) | 12Gi |
| LimitRange `ci-jobs-limits` (PersistentVolumeClaim) | `max.storage` / `default` (request) | 8Gi / 6Gi (CD-06) | 8Gi / 6Gi |
| | `local-path…/persistentvolumeclaims`, `longhorn…/persistentvolumeclaims`, `longhorn-scratch…/persistentvolumeclaims` | 0 | 0 |
| | `services.nodeports`, `services.loadbalancers` | 0 | 0 |
| | `services` | 4 (Woodpecker service containers) | 4 |
| LimitRange `ci-jobs-limits` (Container) | `max` | memory 2Gi, cpu 2, ephemeral-storage 4Gi | memory 1536Mi, cpu 2, ephemeral-storage 3Gi |
| | `default` / `defaultRequest` | memory 1Gi / 1Gi, cpu 2 / 250m, ephemeral-storage 2Gi / 2Gi, `strange.company/heavy-slot` 1 / 1 | memory 768Mi / 768Mi, others same |
| | `maxLimitRequestRatio` | memory 1 | memory 1 |

LimitRange defaults for an extended resource are version-sensitive (verify, 51-AC-7). Fallback: Woodpecker's Kubernetes backend default resource settings (plan 20) add the unit; if neither works, the `ci-jobs` exclusion falls back to the Runner dispatch gate alone (§9.7) and this is recorded as a residual risk.

**`ci-build`** (namespace and memory quota owned by plan 23): this plan requires (a) `deny-higher-priority` as for `ci-jobs`; (b) LimitRange default and max `strange.company/heavy-slot` = 8 per container; (c) `limits.memory` quota ≤ the `ci-jobs` value (3Gi standard, 2Gi constrained), so the heavy term in §14.2 stays `max(worker, ci, build)`; (d) the same `ci-workspace` PVC quota with `persistentvolumeclaims` 2 and a PVC request cap of 10Gi (CD-06).

**`apps`:** `deny-higher-priority` only, plus LimitRange defaults (`defaultRequest` cpu 50m, memory 64Mi; `default` memory 256Mi) so that no app pod is BestEffort. Per-app quotas are added with each app.

**`renovate`, `dr-tests`:** `deny-higher-priority`; LimitRange `renovate-limits` with default and max `strange.company/heavy-slot` 1 per container; LimitRange `dr-tests-limits` with **no** heavy-slot default (CD-16: heavy restore tests set 8 units explicitly, light checks set none) and max 8; ResourceQuota `dr-tests-compute` `limits.memory` 2Gi (standard) / 1536Mi (constrained), so the V-30 heavy term `max(worker, ci, build, dr_tests)` stays bounded (STO-23); both with `maxLimitRequestRatio: memory: 1`, default memory 512Mi; their CronJobs (and CNPG scratch instances in `dr-tests`) set memory requests == limits.

### 9.5 Resource-profile numbers (`policy/resource-profiles.yaml`)

| Profile | standard (cpu req/lim, memory, ephemeral + workspace, deadline) | constrained |
|---|---|---|
| `tiny` | 250m / 1, 1Gi, 1Gi + 2Gi, 10m | 250m / 1, 768Mi, 1Gi + 2Gi, 10m |
| `tests` | 500m / 2, 2Gi, 2Gi + 4Gi, 20m | 500m / 2, 1536Mi, 1Gi + 3Gi, 25m |
| `implementation` | 500m / 2, **3Gi**, 2Gi + 6Gi, 30m | 500m / 2, 2Gi, 1Gi + 4Gi, 40m |
| `reasoning` | 500m / 2, **3Gi**, 2Gi + 6Gi, 45m | 500m / 2, 2Gi, 1Gi + 4Gi, 50m |

`spec.concurrency`: `maxWorkers` 1 (self-hosting-minimum) / 2 (small-ha, plan 47); `sharedHeavySlot` `true` (single node, any size class) / `false` (small-ha); `queue.maxQueued` 20; the queue age limit is plan 29's `spec.retry.infrastructure.queueTimeout` (default 2 h); the `queue.maxQueueAge` field of `resource-profile.schema.yaml` is not used as a separate value (plan 26 aligns it). `harnessReserve` and `maxHarnessTurns` as in the `resource-profile.schema.yaml` example.

The `implementation` and `reasoning` limits are **3 GiB**, below SPEC §83's 4 GiB example, because the budget in §12 shows 4 GiB does not schedule beside the platform on a 12 GiB node. SPEC §83 says exact values are tunable policy; the change is recorded as Q-51-03 and SPEC issue input for SI-01.

### 9.6 Heavy-slot mode per profile

| Profile | Mode | Rationale |
|---|---|---|
| self-hosting-minimum (12 GiB or 8 GiB) | exclusive | §12: a 3 GiB worker plus a 3 GiB CI quota does not fit beside the platform |
| small-ha (3 nodes) | shared | the scheduler spreads heavy pods across nodes; per-node budgets in §12.3 |

A single node of ≥ 16 GiB MAY switch to shared after its own V-30 pass with `heavy = worker + ci` (PR to the capacity overlay).

### 9.7 Concurrency limits and enforcement points

| Limit | self-hosting-minimum | small-ha | Primary enforcement | Backstops |
|---|---|---|---|---|
| Concurrent worker executions | 1 | 2 | Runner dispatcher (`concurrency.maxWorkers`) | `agent-jobs` quota (`limits.memory`, `pods`); heavy-slot units |
| Queue length / age | 20 / `spec.retry.infrastructure.queueTimeout` (default 2 h, plan 29) | same | Runner (`CONCURRENCY_LIMIT`, `QUEUE_TIMEOUT`) | — |
| Concurrent ci-lane workflows | 1 | 2 (`CI_MAX_CONCURRENT`, plan 20 proposal accepted) | Woodpecker agent `WOODPECKER_MAX_WORKFLOWS=${CI_MAX_CONCURRENT}` (plan 20) | `ci-jobs` quota |
| Concurrent build-lane workflows | 1 | 1 | `woodpecker-agent-build` `WOODPECKER_MAX_WORKFLOWS=1` | heavy-slot (8 units on one node excludes the ci lane) |
| Worker ⟂ CI/batch | exclusive | not applied | Runner dispatch gate: in exclusive mode the Runner dispatches only when (a) no pod in `renovate` or `dr-tests` holds a heavy-slot unit and (b) the Woodpecker queue reports no pending or running workflow in either lane (`GET /api/queue/info`, verify; this replaces any pod read in `ci-jobs`/`ci-build`, SEC-3) | heavy-slot extended resource (the scheduler cannot place a conflicting pod) |
| Restore tests | 1 at a time | 1 | CronJob `concurrencyPolicy: Forbid`, staggered schedules (plan 44) | heavy-slot |
| Renovate | 1 | 1 | `concurrencyPolicy: Forbid` | heavy-slot |
| Per-worker CPU, memory, disk, time | per profile | per profile | Runner Job builder | LimitRange max, VAP (plan 37), `activeDeadlineSeconds` |

## 10. Secrets

None. This plan handles no credentials.

## 11. Storage

### 11.1 Ephemeral-disk budget (consumes plan 11 §12; SPEC v1.2 / CD-25)

SPEC v1.2 sets the node disk to **≥ 100 GiB, preferred 200 GiB NVMe, for both profiles**; 75 GiB nodes are refused by preflight (plans 03/05/08). Allocatable ephemeral storage is about 20 % of the root filesystem (§9.2): 20 GiB on 100 GiB, 40 GiB on 200 GiB. The per-profile figures below depend on the **memory** size class (profile set), not on the disk:

| Consumer | `standard` set | `constrained` set | Bound |
|---|---|---|---|
| One worker (ephemeral + workspace) | 8 GiB | 5 GiB | container limit, emptyDir `sizeLimit`, quota |
| CI workflow (container layers and logs only) | 4 GiB (quota) | 3 GiB | quota, LimitRange |
| Platform pods (logs, writable layers) | ≈ 3 GiB | ≈ 3 GiB | container log rotation (plan 07) |
| **Total** | 15 GiB (exclusive mode: peak 11 GiB) | 11 GiB (peak 8 GiB) | |

Both totals fit the 20 GiB ephemeral allocatable of the 100 GiB minimum disk. The heavy-execution scratch baseline line of plan 11 §12 (8 / 5 GiB) is this table's worker row.

CI workspaces live on the separate `ci-workspace` filesystem (ADR-0305, CD-06; plan 08 §9.6 creates it): **12 GiB** on self-hosting-minimum and **24 GiB** on small-ha, whatever the disk size. The largest workflow volume is 6Gi (8Gi request cap in `ci-jobs`, 10Gi in `ci-build`), so one ci-lane workflow plus headroom fits; the build lane does not run beside it on one node (§8.2).

The whole-disk budget (fixed part, data ceiling, small-ha worst-case node) is plan 11 §12's. This plan keeps only the ephemeral and image lines and the measured gates (§14); thresholds are those of §9.3 (hard eviction `nodefs.available<10%`, plan 07; STO-3).

### 11.2 Image budget

containerd images: the plan 11 §12 fixed-part line (10 GiB on the 100 GiB disk; plan 11 sets the 200 GiB value), with kubelet image GC 80 → 70 % as the backstop, not an allocation (STO-3). Harness images are the largest single items (plan 28); each additional harness image costs 1–3 GiB and is budgeted there.

## 12. Resource requirements: memory budget

### 12.1 Self-hosting-minimum, 12 GiB node (`standard`)

Assumed MemTotal 11.68 GiB (11 960 MiB; typical for a "12 GB" VPS). Allocatable = 11 960 − 512 − 1 024 − 300 (hard eviction) = **10 124 MiB (9.89 GiB)**.

| Component (namespace) | PriorityClass | Budgeted request | Limit | Est. working set | Owner plan / note |
|---|---|---|---|---|---|
| k3s server + embedded etcd (host) | — | in `kube-reserved` 1 024 | — | 800–1 100 | 07 |
| OS + host daemons (host) | — | in `system-reserved` 512 | — | 150–300 | 08 |
| CoreDNS ×1 (kube-system) | system-cluster-critical | 70 | 170 | 40 | 09 |
| metrics-server, local-path-provisioner | system | 88 | 384 | 55 | k3s |
| Flux (4 controllers) | platform-critical | 400 | 2 304 | 350 | 18 |
| cert-manager (3) | platform-critical | 240 | 640 | 150 | 10 |
| CNPG operator + barman plugin | platform-critical | 192 | 768 | 150 | 12 |
| Longhorn (manager, driver, CSI, instance-manager) | platform-critical | 600 | — | 700 | 13 (estimate; not yet written) |
| Traefik, cloudflared | platform-critical | 144 | 384 | 120 | 09 |
| 4 × CNPG instance (forgejo 256, authentik 256, woodpecker 192, sc 384) | platform-critical | 1 088 | 3 200 | 1 020 | 12 (plan 12 lists ≈ 1 380 requests; calibrate at P12) |
| Forgejo | platform-critical | 384 | 1 024 | 350 | 16 |
| Authentik server + worker | platform | 768 | 2 048 | 800 | 15 (worker request 320 proposed vs 384) |
| Woodpecker server + agent | platform | 160 | 768 | 120 | 20 |
| Prometheus | platform | 1 024 | 2 048 | 900 | 38 |
| Alertmanager, operator, KSM, blackbox ×2, node-exporter | platform | 352 | 768 | 250 | 38 |
| Grafana | platform | 128 | 384 | 150 | 39 |
| OTel Collector | platform | 128 | 384 | 120 | 40 |
| platform-probes | platform | 48 | 128 | 40 | 15 |
| Agent Runner | platform | 192 | 384 | 150 | 26 |
| Model Gateway ×2 | platform | 192 | 512 | 100 | 53 |
| egress-proxy ×2 | platform | 128 | 512 | 60 | 36 |
| Hermes | platform | 384 | 1 024 | 400 | 24 (estimate) |
| **Platform pods total** | | **6 710 MiB (6.55 GiB)** | | **≈ 6 040 MiB (5.9 GiB)** | |
| Heavy term (exclusive): max(worker 3 072, CI quota 3 072) | | 3 072 | | | §9.5 |

Checks:
- **Schedulability:** 6 710 + 3 072 = 9 782 ≤ 10 124 MiB. Passes with 342 MiB spare. A 4 GiB worker (9 806 + 4 096 − 3 072 …) does **not** fit: 6 710 + 4 096 = 10 806 > 10 124.
- **V-30 budget:** platform working set must be ≤ 10 124 − 3 072 − 1 012 (10 %) = **6 040 MiB**. The estimate is ≈ 6 040 MiB. **The 12 GiB node passes only at the margin.** Expected outcomes at P12: pass after request calibration, or fail by a few hundred MiB; §14.4 defines the response.
- With shared mode (worker and CI together) the budget would be 10 124 − 6 144 − 1 012 = 2 968 MiB: impossible. Exclusive mode is mandatory on this node.

### 12.2 Self-hosting-minimum, 8 GiB node (`constrained`)

Assumed MemTotal 7.76 GiB (7 950 MiB). Allocatable = 7 950 − 448 − 896 − 300 = **6 306 MiB (6.16 GiB)**.

Constrained tuning reduces Prometheus (768 request / 1 536 limit, `retentionSize` 5 GiB per plan 38) and CNPG `shared_buffers`; the rest of the platform cannot shrink much (Authentik, Longhorn, Hermes). Estimated platform working set ≈ 5 600–5 800 MiB; requests ≈ 6 200 MiB.

- **Schedulability:** 6 200 + 2 048 (constrained worker) = 8 248 > 6 306 MiB. **No worker can be scheduled.**
- **V-30 budget:** 6 306 − 2 048 − 631 = 3 627 MiB versus ≈ 5 700 MiB. **Predicted fail by ≈ 2 GiB.** Even the `tiny` profile (768 MiB) fails (budget 4 907 MiB).

**Stage-0 conclusion and operator decision (CD-23):** by this estimate the full Self-Hosting Threshold platform leaves no room for a useful worker on an 8 GiB node. The operator has decided that **8 GiB stays allowed as the `constrained` configuration** (no SPEC change): worker and CI execution are serialized (exclusive heavy slot), the constrained profile set (2–3 GiB, §9.5) applies, and the **P12 measured gate V-30 decides** for each node; a node that fails it needs a larger class (the operator accepts this risk). Nothing in this plan forbids 8 GiB; the estimate is a warning, not a gate.

### 12.3 Small-ha, per node (3 × 12 GiB)

Replicated components add per-node copies (CNPG: one instance of each cluster per node; Prometheus and Alertmanager 2 replicas; Authentik server 2; Traefik/cloudflared 2; CoreDNS 2; Longhorn per node; Loki and Tempo added, plan 41) while singletons (Flux, Forgejo, Hermes, Grafana, Runner) occupy one node. Estimated per-node platform working set 4.0–5.0 GiB, worst node ≈ 5.2 GiB. With shared slot mode and the scheduler spreading heavy pods, each node must hold at most one worker (3 GiB) or one CI workflow (3 GiB): budget 10 124 − 3 072 − 1 012 = 6 040 MiB ≥ 5.2 GiB. Passes with ≈ 0.8 GiB margin. H04 re-measures (§14.5).

### 12.4 CPU

Platform CPU requests total ≈ 1.6 cores (12 GiB node); kube-reserved 0.5 and system-reserved 0.25 leave ≈ 1.6 of 4 cores for a worker (request 0.5, limit 2) or CI (request 2, limit 4 quota). CPU limits are not set on platform pods (throttling avoidance; plans 16, 18, 38). CPU contention slows workers and CI; it never threatens the platform's requested share.

## 13. Security boundary

- Quotas and limit ranges are tamper-resistant only because workers have no API rights (plan 37) and the Runner's RBAC cannot modify ResourceQuota or LimitRange objects (plan 26 RBAC table has no such verbs).
- The heavy-slot advertiser has cluster-wide `nodes/status` patch. It can set arbitrary extended resources on nodes and labels/annotations via `nodes` patch; it runs a pinned image with no other permissions, in `kube-system`. Accepted.
- PriorityClass misuse (a low-trust namespace naming `platform-critical`) is blocked by the zero-pod quotas and the plan 37 VAP.

## 14. Happy path

### 14.1 Install

1. configure (P03): kubelet args from §9.2–§9.3 (plan 07).
2. seed (P04): layer `namespaces` creates PriorityClasses, quotas and limit ranges.
3. After control transfer: layer `capacity` starts the advertiser; within 5 minutes every node advertises `strange.company/heavy-slot`.
4. P12: V-30 runs (§14.2).

### 14.2 Measured-budget gate (V-30; plan 50 runs it)

Window `W = SC_VERIFY_MEMORY_WINDOW` (default 30 m) after `SC_VERIFY_SETTLE` (10 m), with the platform idle except for Prometheus scrapes and one synthetic CI pipeline run (`make verify`, so that CI-side platform load such as Woodpecker server and Forgejo clone traffic is included). Per node `n`:

```text
platform_ws  = max_over_time( sum(container_memory_working_set_bytes{container!="",container!="POD",
                 namespace!~"agent-jobs|ci-jobs|ci-build|dr-tests|renovate", node="n"})[W:1m] )
alloc        = kube_node_status_allocatable{resource="memory", node="n"}
worker_max   = largest memoryLimit in the active set × (exclusive ? 1 : workers that can land on n)
ci_quota     = ci-jobs ResourceQuota limits.memory
build_quota  = ci-build ResourceQuota limits.memory (plan 23)
dr_quota     = dr-tests ResourceQuota limits.memory (STO-23)
heavy        = exclusive ? max(worker_max, ci_quota, build_quota, dr_quota) : worker_max + ci_quota
requests_sum = sum(kube_pod_container_resource_requests{resource="memory", node="n",
                 namespace!~"agent-jobs|ci-jobs|ci-build|dr-tests|renovate"})
```

PASS iff all hold:
1. `platform_ws ≤ alloc − heavy − 0.10 × alloc`;
2. `min_over_time(node_memory_MemAvailable_bytes{node="n"}[W]) ≥ heavy` (catches host processes outside pods);
3. `requests_sum + heavy ≤ alloc` (a worker is actually schedulable);
4. **request calibration:** for every platform container, `quantile_over_time(0.95, container_memory_working_set_bytes[W])` ≤ 1.10 × its memory request. Violations are listed with the suggested request (p95 rounded up to 16 MiB); the gate fails until a PR adjusts the owning plan's values (51-R-3).

ADR-0020 originally specified `heavy = worker_max + ci_job`. This plan refines it with the exclusive-slot term, because in exclusive mode the two cannot coexist; the refinement is recorded in ADR-0156 and Q-51-05 for plan 50.

Evidence (`report.json` V-30 section): node shape (provider output), size class, profile set, mode, every term in bytes, the PromQL text, the window, and the request-calibration table.

### 14.3 Static model (`make capacity-check`, CI on every PR touching `gitops/`, `policy/resource-profiles.yaml` or `infra/ansible/group_vars/all/kubelet.yaml`)

Renders the cluster for each fixture (`ci-shm-tunnel` 12 GiB, a synthetic 8 GiB fixture, `ci-sha-direct` 3 × 12 GiB) and computes, from rendered requests and the modeled allocatable (§12): `requests_sum + heavy ≤ alloc` per node (all platform pods on node-01 for the single-node fixtures), and the quota invariants of §9.4. The 12 GiB and small-ha fixtures must pass (error); the 8 GiB fixture reports its predicted result as a warning that references Q-51-04.

### 14.4 Response to a V-30 failure

- **12 GiB:** (1) apply the calibration table (requests to measured p95); (2) if condition 1 still fails by ≤ 512 MiB, reduce `implementation`/`reasoning` to 2560Mi by PR and re-run; (3) otherwise the installation uses the next provider machine class (≥ 16 GiB; plan 04 selects it; SPEC §11 allows larger) and the report says so. A failure is never waived (50-R, ADR-0020).
- **8 GiB (`constrained`, CD-23):** apply the calibration table, then, if condition 1 still fails, lower the constrained `implementation`/`reasoning` limits within the 2–3 GiB range by PR and re-run; if it still fails, report `8 GiB node failed the measured memory gate (V-30); use a larger machine class` and stop. The gate is never waived.

### 14.5 Re-measurement

H04 (plan 47) repeats V-30 on each small-ha node with shared mode. **The decisive single-node measurement is at R02** (GC-13, 51-AC-16): V-30 with the Runner, Gateway, egress-proxy and Hermes running and a full C10 suite inside the window. At P12 those components do not exist yet, so the P12 V-30 result is provisional for SI-12. A PR that changes any platform request by more than 20 % triggers `make capacity-check` and, on the next `./bootstrap verify`, V-30.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Node memory pressure | `SCNodeMemoryPressure`, `SCPlatformPodEvicted` | evictions (workers/CI first by ranking) | kubelet eviction | `disk-pressure.md` §Memory |
| Platform pod evicted | `SCPlatformPodEvicted` | component restart elsewhere/later | controller recreates | `disk-pressure.md` §Memory; recalibrate requests |
| Platform pod OOM-killed | `SCPlatformPodOOMKilled` (catalogue) | restart | kubelet | raise its limit by PR |
| Worker cannot schedule (no capacity, slot held) | `SCWorkerPendingCapacity` | tasks wait (acceptable) | Runner queue | check what holds the slot; `stuck-task-recovery.md` |
| Advertiser stale or capacity missing | `SCHeavySlotAdvertiserStale`, `SCHeavySlotMissing` | worker/CI pods unschedulable (they request the resource) | CronJob retries every 5 m | §20 |
| Quota exhausted unexpectedly (leaked Jobs) | Runner dispatch error `exceeded quota` → infrastructure failure | dispatch blocked | Runner Job GC (plan 26) | `stuck-task-recovery.md` |
| Woodpecker step waits for a worker past timeout | pipeline `killed`/timeout | CI failure (infrastructure class) | none | raise pipeline timeout (plan 20) |
| Disk pressure from ephemeral use | `SCNodeDiskPressure` | evictions | kubelet | `disk-pressure.md` |

## 16. Retry behavior

The advertiser re-runs every 5 minutes. The Runner re-evaluates its dispatch gate on every queue tick (plan 26). No other component in this plan retries.

## 17. Idempotency behavior

All objects are declarative. The advertiser's patch is a merge of a fixed value. Re-running V-30 produces a new evidence record without side effects.

## 18. Bootstrap behavior

Covered in §14.1. Black start: identical (objects from Git). V-30 is part of every `verify`, including recovery.

## 19. Upgrade behavior

- Changing a number here is a PR touching the owning file and, for platform requests, the owning component plan's values; CI runs `make capacity-check`.
- k3s upgrades re-verify 51-AC-6/7/14/15 in the lab (version-sensitive kubelet and kubectl behavior), as part of plan 48's k3s gate.
- Raising `maxWorkers` or switching slot mode requires a V-30 pass recorded in the PR.

## 20. Recovery behavior

- **Heavy slot missing on a node:** `./bootstrap exec -- kubectl -n kube-system create job --from=cronjob/heavy-slot-advertiser hs-manual`; verify `kubectl get node <n> -o jsonpath='{.status.capacity.strange\.company/heavy-slot}'`.
- **Evictions of platform pods:** read the V-30 calibration report or run `./bootstrap verify --only V-30`; adjust requests by PR; if a worker profile is the cause (it should not be, by 51-R-1), lower its limit by PR to `policy/resource-profiles.yaml`.
- **Merge-gate components:** if the heavy-slot advertiser or a quota change prevents CI from running the revert, use plan 55's `emergency-merge.md` (GC-02).
- **Emergency:** stop new autonomous work with the Runner emergency stop or `runner.dispatchPaused` (plan 33/26); CI can be paused by disabling the Woodpecker agent (plan 20).

## 21. Observability

- KSM: `kube_node_status_capacity{resource="strange_company_heavy_slot"}` (KSM sanitizes the name, verify), `kube_resourcequota{namespace=~"agent-jobs|ci-jobs"}`, `kube_pod_status_reason{reason="Evicted"}`, `kube_pod_container_resource_requests`.
- Grafana "Capacity" dashboard (plan 39): allocatable vs requests vs working set per node; heavy-slot holder; quota usage; evictions.
- **Alerts introduced (plan 42 to register):**

| alertname | Expression sketch | for | Severity / class | Runbook |
|---|---|---|---|---|
| `SCPlatformPodEvicted` | `sum by (namespace) (increase(kube_pod_status_reason{reason="Evicted",namespace!~"agent-jobs|ci-jobs|apps|dr-tests|renovate"}[15m])) > 0` (or KSM equivalent for evicted pods) | 0m | warning / notify | `disk-pressure.md` §Memory |
| `SCWorkerPendingCapacity` | `max(time() - kube_pod_created{namespace="agent-jobs"} * on(pod) group_left kube_pod_status_phase{namespace="agent-jobs",phase="Pending"}) > 1800` | 0m | warning / notify | `stuck-task-recovery.md` |
| `SCHeavySlotAdvertiserStale` | check-cronjob `heavy-slot-advertiser` (ADR-0553; `failAfter: 15m`, `maxAge: 20m`) | — | warning / notify | §20 |
| `SCHeavySlotDeadlock` | `max(time() - kube_pod_created{namespace="ci-build"} * on(pod) group_left kube_pod_status_phase{namespace="ci-build",phase="Pending"}) > 3600` | 0m | warning / notify | §8.2 (build-lane workflow uses services or parallel steps) |
| `SCHeavySlotMissing` | `count(kube_node_status_condition{condition="Ready",status="true"} == 1) > count(kube_node_status_capacity{resource="strange_company_heavy_slot"} > 0)` | 10m | warning / notify | §20 |

## 22. Testing strategy

- **Static (F11/F12):** `make capacity-check` (§14.3); quota invariants; every Deployment/StatefulSet/DaemonSet in `gitops/` sets `priorityClassName` from §9.1 consistent with its namespace tier, and every container sets a memory request and limit (F11 check `resources-declared`).
- **k3d (F12):** apply PriorityClasses, quotas and limit ranges; assert: a pod with `priorityClassName: platform` in `agent-jobs` is rejected by quota; a pod without a class in `agent-jobs` is rejected; a `ci-jobs` pod gets the heavy-slot default from the LimitRange (51-AC-7); with node capacity 8 (patched by the advertiser), a pod requesting 8 units blocks a pod requesting 1 and vice versa.
- **Lab:** eviction drill on a 3-VM lab with a memory hog in `agent-jobs` (limit respected; no platform pod restarts); graceful shutdown check.
- **Live:** gates below.

## 23. Acceptance criteria

### GATE-F12 (contributed)

- **51-AC-1** `make capacity-check` exits 0 for `ci-shm-tunnel` and `ci-sha-direct`, prints the predicted 8 GiB result as a warning, and fails on a fixture where `maxWorkers: 2` is set with the single-node quota.
- **51-AC-2** `make e2e-capacity` (k3d) passes the four assertions in §22 k3d.

### GATE-P04 (contributed)

- **51-AC-3** `kubectl get priorityclass platform-critical platform application ci agent-worker -o custom-columns=N:.metadata.name,V:.value,P:.preemptionPolicy,D:.globalDefault --no-headers` prints exactly the §9.1 values.
- **51-AC-4** `kubectl -n agent-jobs get resourcequota agent-jobs-compute -o jsonpath='{.spec.hard.limits\.memory}'` = `3Gi` and `kubectl -n agent-jobs get limitrange agent-jobs-limits -o jsonpath='{.spec.limits[0].maxLimitRequestRatio.memory}'` = `1` (V-14 overlap).
- **51-AC-5** `kubectl -n agent-jobs run pc-test --image=<pause digest> --restart=Never --overrides='{"spec":{"priorityClassName":"platform"}}' --dry-run=server` fails with a quota error naming `deny-higher-priority`.

### GATE-P12 (contributed; plan 50 runs V-30)

- **51-AC-6** `kubectl get node node-01 -o jsonpath='{.status.capacity.strange\.company/heavy-slot}'` = `8` (exclusive), and the node annotation `strange.company/heavy-slot-advertised-at` is less than 10 minutes old (re-verifies the status-subresource patch).
- **51-AC-7** A Woodpecker step pod created by the synthetic pipeline shows `resources.limits["strange.company/heavy-slot"] = 1` (re-verifies the LimitRange extended-resource default or the plan 20 fallback).
- **51-AC-8** `./bootstrap verify --only V-30 --json` returns `pass` with all four conditions of §14.2 evaluated and every term present in the evidence; or `fail` with the §14.4 response recorded. The P12 gate requires `pass` on the installation's node class.
- **51-AC-9** `cat /sys/fs/cgroup/kubepods.slice/memory.max` (via `./bootstrap exec -- ssh node-01`) equals `kube_node_status_allocatable{resource="memory"} + 300Mi` within ±16 MiB (the hard eviction threshold is part of the node's allocatable arithmetic but not of the cgroup limit; verify on the pinned k3s and adjust the expected value once, recording it here).

### GATE-C02 (contributed)

- **51-AC-10** The fake harness scenario `fake-memhog` (allocates until killed) ends in `FAILED` with an OOM reason in the worker result, and no pod outside `agent-jobs` restarted during the run (`kube_pod_container_status_restarts_total` unchanged for platform namespaces).
- **51-AC-11** With a Woodpecker pipeline running a 5-minute sleep step, a Runner dispatch of a fake worker keeps the execution `QUEUED` (dispatch gate) until the pipeline finishes, then it starts within 2 minutes; the reverse case (worker running, then a pipeline is triggered) leaves the step pod Pending with an event naming `strange.company/heavy-slot` until the worker ends.
- **51-AC-12** A worker Job with `memoryLimit` above the LimitRange max (submitted by a test with the Runner's identity) is rejected at admission (VAP or LimitRange message).
- **51-AC-13** `kubectl -n agent-jobs get pod <fake worker> -o jsonpath='{.spec.priorityClassName}'` = `agent-worker` and its heavy-slot limit = 8.
- **51-AC-14** Lab: a fake worker that writes `workspaceSize − 100Mi` into its workspace emptyDir completes without eviction (re-verifies the ephemeral accounting assumption in §5); writing `workspaceSize + 100Mi` is evicted with reason `Evicted` mentioning the emptyDir limit.

### GATE-R02 (contributed)

- **51-AC-16** (GC-13) `./bootstrap verify --only V-30 --json` returns `pass` on the self-hosting-minimum node while `agent-runner`, `model-gateway`, `egress-proxy` and `hermes` are Running and a complete C10 fake end-to-end run (plan 46) lies inside the measurement window; the evidence lists those components in the calibration table. The result is the one cited for SI-12 / Q-51-04.

### GATE-H04 (contributed)

- **51-AC-15** V-30 passes on each of the three small-ha nodes in shared mode, and `kubectl get node -o jsonpath='{.items[*].status.capacity.strange\.company/heavy-slot}'` prints `64 64 64`. Lab evidence: `systemctl reboot` of a lab node logs graceful pod termination (`shutdown-grace-period`) in the kubelet journal.

## 24. Explicit non-goals

- Vertical/horizontal pod autoscaling, cluster autoscaling, node pools.
- Kueue or another batch scheduler (the heavy slot plus the Runner queue suffice at this scale).
- Per-tenant fair sharing between repositories.
- Overcommitting worker memory.

## 25. Open questions

- **Q-51-01** (resolved) `dag.yaml` lists plan 51 under F12, P04, P12, C02 and H04; R02 must be added for 51-AC-16. *Owner:* master plan.
- **Q-51-02** (blocking: no) New layer `capacity` (plan 18 table), with `woodpecker` and `strange-company` depending on it. *Owner:* 18.
- **Q-51-03** (blocking: no; interpretation of SPEC §83) Standard `implementation`/`reasoning` limits are 3 GiB instead of SPEC's 4 GiB example, because 4 GiB is not schedulable beside the platform on 12 GiB (§12.1). *Proposed:* accept as tunable policy; raise after H04/C10 measurements if headroom exists. Plan 26's schema example (4Gi) remains an example only. *Owner:* 51/26.
- **Q-51-04** (resolved by operator decisions CD-23 and CD-25) 8 GiB stays allowed as `constrained`, decided per node by V-30 (§12.2, §14.4); the Stage-0 estimate predicts a failure, which the operator accepts. Disk: SPEC v1.2 (100 GiB minimum, 200 GiB preferred). *Owner:* —.
- **Q-51-05** (blocking: no) V-30 formula refinement (exclusive term `max(worker, ci)`; schedulability and calibration conditions) differs from plan 50 §14.8.1 and ADR-0020's text. *Proposed:* plan 50 adopts §14.2 verbatim. *Owner:* 50.
- **Q-51-06** (blocking: no) Woodpecker's Kubernetes backend options for priority class and default resources are version-sensitive. Plan 20 §9.4 already sets `…_PRIORITY_CLASS=ci` (verified against 3.19 docs). *Proposed:* plan 20 also verifies how default resources/extended resources can be set; fallbacks are in §9.4. *Owner:* 20.
- **Q-51-10** (blocking: no; false assumption, SPEC §12; proposed SI-32) A small-ha cluster of 8 GiB nodes is not budgeted (§12.3 assumes 12 GiB) and probably fails like §12.2. *Proposed:* declare 8 GiB small-ha nodes unsupported until H04 measures one; plan 03 preflight warns. *Owner:* master plan / 03.
- **Q-51-07** (resolved, CD-05) Plan 20's `ci-slot` is withdrawn in favor of the heavy-slot units of §8.2; plan 20's 20-AC-22 counts running workflows across both lanes. *Owner:* 20.
- **Q-51-09** (resolved, CD-05/CD-16, refined by STO-23) The Lease `strange-company/heavy-execution` is withdrawn; heavy restore tests request `workerUnits` (8) explicitly and light checks request none. Both use PriorityClass `ci`, as does Renovate. Plan 44 keeps only `dr-tests/restore-slot` and has adopted this rule (plan 44 §9.3 and Q-44-10, STO-23). **Settled rule:** heavy restore tests request 8 heavy-slot units, light checks request 0, PriorityClass `ci`, plus the 2Gi `dr-tests` memory quota. With 8 units a restore test is mutually exclusive with a full CI workflow, which keeps V-30's heavy term at max(ci, dr) and not ci + dr. *Owner:* 44/12/43.
- **Q-51-08** (resolved at consolidation) The `ci-workspace` filesystem is 12 GiB on self-hosting-minimum and 24 GiB on small-ha (ADR-0305 amended; plan 11 §12 budgets 12 GiB on both disk sizes). No further action. *Owner:* 11/20.
