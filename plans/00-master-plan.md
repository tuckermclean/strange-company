# 00 — Master Plan

**Stage:** 0 (Complete Engineering Definition)
**Authority:** Subordinate to `SPEC.md`. This file binds every other plan, contract, and procedure. Where a subsystem plan conflicts with this file, this file wins until a reviewed change says otherwise. Where this file conflicts with `SPEC.md`, `SPEC.md` wins and the conflict must be recorded in [`spec-issues.md`](spec-issues.md).
**Normative language:** RFC 2119 / RFC 8174 keywords (MUST, SHALL, SHOULD, MAY) as in `SPEC.md`.

---

## 1. How to use this plan set

| Artifact | Location | Role |
|---|---|---|
| Canonical spec | `SPEC.md` | What must be true. |
| Master plan | `plans/00-master-plan.md` | Global decisions, inventories, conventions, DAG, sequence. |
| Machine-readable DAG | `plans/dag.yaml` | Authoritative dependency graph and gate ownership. |
| Subsystem plans | `plans/NN-*.md` | Per-subsystem engineering definition (structure: §121, template `plans/_template.md`). |
| Spec issues | `plans/spec-issues.md` | Contradictions, ambiguities, false assumptions, resolutions. |
| ADRs | `adr/NNNN-*.md` | Binding architectural decisions with rationale and rejected alternatives. |
| Contracts | `contracts/*.schema.yaml`, `contracts/*.openapi.yaml`, `contracts/*.yaml` | Machine-readable interfaces. |
| Procedures | `procedures/*.md` | Operator runbooks. |
| Implementation sequence | `PLAN.md` | Ordered, bounded implementation tasks. |
| Status | `STATUS.md` | Current stage, DoD checklist, open items. |
| Reference input | `plans/reference/*.md` | Non-normative evidence (e.g. prior verified third-party behavior). |

Reading order for an implementation agent picking up a DAG node: `SPEC.md` → this file → `plans/dag.yaml` node → the owning plan → referenced contracts → referenced ADRs → `PLAN.md` task.

---

## 2. Architecture summary

Strange Company is a GitOps-managed k3s platform plus an application layer that turns human intent into **test-first, deterministically verified** pull requests produced by ephemeral agent Jobs.

```text
                         Human (browser / Hermes dashboard / Forgejo)
                                         │  Authentik OIDC
                                         ▼
 ┌──────────────────────────── k3s (embedded etcd, flannel wireguard-native, netpol on) ─────────────────────────┐
 │                                                                                                              │
 │  hermes ns            strange-company ns                              agent-jobs ns                          │
 │ ┌─────────┐  MCP   ┌───────────────────────────────┐  Job API   ┌──────────────────────┐                     │
 │ │ Hermes  │──────▶│ Agent Runner                   │──────────▶│ worker Job (1 role)   │                     │
 │ │ Agent   │       │  task state machine, TDD gate, │           │  sc-worker adapter +  │                     │
 │ └────┬────┘       │  policy, budgets, context,     │◀──result──│  harness CLI          │                     │
 │      │ model      │  Forgejo/Woodpecker/K8s adapt. │           └──────┬────────┬───────┘                     │
 │      ▼ calls      ├───────────────────────────────┤                  │ model   │ git push agent/<task>       │
 │ ┌──────────────────────────────┐   sc-pg (CNPG)   │                  ▼ calls   ▼                             │
 │ │ Model Gateway (ledger,       │◀─────────────────┼──────────────────┘     Forgejo (forgejo ns) ──mirror──▶ GitHub
 │ │ pricing, budgets, e-stop)    │──▶ provider APIs  │                         │ webhooks / API
 │ └──────────────────────────────┘                  │                         ▼
 │                                   Runner triggers verification ───▶ Woodpecker (woodpecker ns) → ci-jobs ns pods
 │                                                                                  │ RED/GREEN evidence (pulled by Runner)
 │  Flux (flux-system) ◀── GitRepository: Forgejo/strange-company (read-only deploy key)                          │
 │  CNPG, Longhorn, cert-manager, Traefik+cloudflared (ingress), Authentik, Prometheus/Alertmanager/Grafana/OTel │
 └──────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
          │ backups/WAL/snapshots/bundles/state                                   ▲
          ▼                                                                       │ black start
   S3-compatible object storage (outside cluster)          GitHub/strange-company (off-cluster recovery seed)
```

Core properties:

1. **Git is the only source of desired state.** Flux applies `gitops/` from Forgejo. Platform policy is GitOps-deployed configuration under `policy/`. The Runner's *machine execution state* lives in CNPG (`sc-pg`) and is the authority for task state (SPEC §101).
2. **Models propose and deterministic systems decide.** The Agent Runner enforces the task state machine and the TDD gates. Woodpecker produces RED/GREEN evidence. Forgejo branch protection plus Runner-posted provenance status enforce merge policy. Kubernetes admission enforces workload boundaries independently of the Runner.
3. **Authority narrows toward production.** Hermes holds no Kubernetes write rights. Workers hold a task-scoped deploy key, a task-scoped Runner token and a task-scoped gateway token. They never hold provider API keys, Flux, registry-push or signing credentials. CI verification pipelines run with no secrets. Only protected-ref pipelines build, sign and push.
4. **Every billable model call crosses the Model Gateway.** This includes Hermes. Exact accounting and budget enforcement happen at the request boundary, not from harness self-reports (ADR-0004, lessons L-3).
5. **Recovery is designed first.** Everything needed to rebuild lives off-cluster: GitHub mirror, object storage, and the operator-held recovery bundle plus passphrase. Off-cluster material is listed in `plans/35-secrets.md` and `plans/45-recovery.md`.

---

## 3. Global decisions register

Each decision is binding for all plans. Rationale and rejected alternatives are in the ADR.

| ID | Decision | ADR |
|---|---|---|
| D-01 | Stage-0 document and contract conventions (JSON Schema 2020-12 in YAML; OpenAPI 3.1; naming rules in §6). | ADR-0001 |
| D-02 | Strange Company services and CLIs are written in **Go** (single module `src/`). `./bootstrap` is a POSIX `sh` wrapper that runs the toolbox container. | ADR-0002 |
| D-03 | **Hermes = upstream Nous Research Hermes Agent**, digest-pinned and configured through *managed scope*. Treated as an untrusted-but-authenticated client of the Runner MCP surface. Not a policy enforcement point. | ADR-0003 |
| D-04 | **Model Gateway**: a Strange Company–owned, protocol-native pass-through proxy. It holds provider keys, records one ledger row per provider request, enforces budgets and emergency stop, and is the *only* egress path to model providers for workers and Hermes. | ADR-0004 |
| D-05 | **Seed via Flux from an object-storage snapshot** (`Bucket` source) of the exact local commit. Control transfer is a source switch to the Forgejo `GitRepository` at the same commit. No `helm install`-then-adopt. | ADR-0005 |
| D-06 | **One CNPG `Cluster` per state owner** (`forgejo-pg`, `authentik-pg`, `woodpecker-pg`, `sc-pg`), each with its own barman-cloud backup path. | ADR-0006 |
| D-07 | **Worker Forgejo credential = per-task ephemeral write deploy key**, created and deleted by the Runner. Workers have no Forgejo API token, so they cannot post statuses, open PRs, or approve. The Runner's `sc-runner` bot account holds API authority. | ADR-0007 |
| D-08 | **Runner-triggered verification pipelines.** The Runner triggers Woodpecker pipelines for RED and GREEN, then *pulls* evidence over the Woodpecker API. Agent branches that modify protected paths (`.woodpecker/**`, `.strange-company/**`, `policy/**`, etc.) are rejected before any evidence is accepted. | ADR-0008 |
| D-09 | **Alertmanager is the single human-notification hub.** The Runner raises human-required conditions as alerts through the Alertmanager API. An external dead-man's-switch covers total outage. | ADR-0009 |
| D-10 | **Ingress:** k3s bundled Traefik is disabled. Traefik is Flux-managed in `ingress`. The default edge is Cloudflare Tunnel (`cloudflared` in `ingress`). Direct-ingress mode (node ports 80/443 + cert-manager ACME DNS-01) is a supported alternative. | ADR-0010 |
| D-11 | **Networking:** flannel `wireguard-native` backend, k3s embedded network-policy controller enabled, default-deny NetworkPolicies per namespace. | ADR-0011 |
| D-12 | **Secrets:** SOPS + age. Every Git-resident secret is encrypted to **two recipients**: the cluster age key and the offline recovery age key. A passphrase-encrypted **recovery bundle** is stored in object storage and held by the operator. | ADR-0012 |
| D-13 | **Images:** internal images live in the Forgejo OCI registry, built only by trusted CI on protected refs. The bootstrap toolbox can also be built locally from a checksum-pinned Containerfile, so black start does not depend on any registry Strange Company runs. | ADR-0013 |
| D-14 | **Admission:** Pod Security Admission (`restricted` on `agent-jobs`, `ci-jobs`, `hermes`, `strange-company`) + native ValidatingAdmissionPolicy (CEL) + RBAC + NetworkPolicy. No Kyverno or Gatekeeper in MVP. | ADR-0014 |
| D-15 | **Namespaces** refined from SPEC §19 (see §5). Hermes gets its own namespace. CI pipeline pods get `ci-jobs`. | ADR-0015 |
| D-16 | **Flux manifests are committed** (pre-rendered, version-pinned `gotk-components.yaml`). Flux reads Forgejo through a **read-only deploy key**. `flux bootstrap` (which needs write access) is not used. | ADR-0016 |
| D-17 | **Policy is GitOps configuration.** Platform policy files in `policy/` are delivered to the Runner and Gateway as Flux-generated ConfigMaps at the deployed revision. Repository context and policy (`.strange-company/`) are read **from the task's base revision**, never from the agent branch head. | ADR-0017 |
| D-18 | **Interim CI before Woodpecker:** Phase-1 work is verified by the same containerized `make` targets running on GitHub Actions. Woodpecker becomes the trusted CI at node P08, and GitHub Actions becomes advisory. | ADR-0018 |
| D-19 | **Merge in MVP is human-performed**, gated by Forgejo branch protection that requires the Woodpecker status, the Runner's `strange-company/tdd-provenance` status and the `strange-company/review` status. The Runner re-verifies provenance from its own database before posting. Autonomous merge is post-MVP. | ADR-0019 |
| D-20 | **Capacity:** default OVH class VPS-2 (12 GiB / 100 GiB NVMe). 8 GiB is allowed as the `constrained` configuration. SPEC v1.2 disk minimum is 100 GiB and preferred is 200 GiB. Every node is gated by the measured memory and disk budgets (CD-23..CD-25, SI-01/12/32). | ADR-0020 |

Subsystem plans MAY add ADRs for subsystem-local decisions. Number ranges are reserved per authoring group so parallel work cannot collide:
`0100–0149` bootstrap/infra, `0150–0199` cluster/networking, `0200–0249` storage/backup, `0250–0299` identity/forge/mirror, `0300–0349` GitOps/seed/CI, `0350–0399` TDD/verification/forge-workflow, `0400–0449` Runner/Hermes/workers/context, `0450–0499` routing/accounting/budgets/gateway, `0500–0549` security/secrets/admission/supply chain, `0550–0599` observability/DR.

---

## 4. Component inventory

| Component | Namespace | Delivered by | Owning plan | State |
|---|---|---|---|---|
| OpenTofu (provider modules) | — (toolbox) | `./bootstrap` phase `provision` | 03, 04, 05, 06 | S3 `tofu-state` |
| Ansible (node baseline, k3s) | — (toolbox) | `./bootstrap configure` | 07, 08 | none (idempotent) |
| Bootstrap orchestrator `sc-bootstrap` | — (toolbox) | `./bootstrap` | 50 | journal (local + S3) |
| k3s server/agent, embedded etcd | host | Ansible | 07 | etcd on node disk; snapshots → S3 |
| Flux controllers | `flux-system` | seed (`kubectl apply`), then Flux self-manages | 18, 19 | Git |
| cert-manager | `cert-manager` | Flux (seed set) | 10 | Git + Secrets (reissuable) |
| CNPG operator + barman-cloud plugin | `cnpg-system` | Flux (seed set) | 12 | — |
| Longhorn V1 | `longhorn-system` | Flux (seed set) | 13 | node disks; backups → S3 |
| Traefik | `ingress` | Flux (seed set) | 09, 10 | — |
| cloudflared | `ingress` | Flux (seed set, tunnel mode) | 09 | tunnel credential (SOPS) |
| Forgejo + `forgejo-pg` | `forgejo` | Flux (seed set) | 16 | CNPG, Longhorn PVC, S3 bucket |
| Repository backup CronJobs | `forgejo` | Flux | 44 | S3 `repo-backups` |
| Authentik + `authentik-pg` | `authentik` | Flux (post-transfer) | 15 | CNPG (+ media on S3 or Longhorn) |
| Woodpecker server/agent + `woodpecker-pg` | `woodpecker` | Flux (post-transfer) | 20 | CNPG |
| Woodpecker pipeline pods | `ci-jobs` | Woodpecker k8s backend | 20, 23 | emptyDir only |
| kube-prometheus-stack (Prometheus, Alertmanager, kube-state-metrics) | `monitoring` | Flux | 38, 42 | local-path PVC (non-critical) |
| Grafana | `monitoring` | Flux | 39 | dashboards from Git; effectively stateless |
| OpenTelemetry Collector | `monitoring` | Flux | 40 | — |
| Loki, Tempo (small-ha) | `monitoring` | Flux | 41 | S3 `telemetry` |
| Agent Runner (`agent-runner`) | `strange-company` | Flux → `strange-company` chart | 26 | `sc-pg` |
| Model Gateway (`model-gateway`) | `strange-company` | Flux → `strange-company` chart | 53 | `sc-pg` (ledger) |
| `sc-pg` | `strange-company` | Flux | 12, 26 | CNPG |
| Hermes Agent | `hermes` | Flux → `strange-company` chart (Hermes integration values) | 24 | Longhorn PVC `/opt/data` (non-authoritative) |
| Worker Jobs | `agent-jobs` | Agent Runner | 27, 28 | emptyDir only |
| Renovate | `renovate` | Flux (CronJob) | 23 | none |
| Restore-test Jobs | `dr-tests` | Flux (CronJobs) | 44 | ephemeral |
| Egress proxy `egress-proxy` (listeners: workers 3128, ci 3129, build 3130, dr 3131) | `strange-company` | Flux → `strange-company` chart | 36 (ADR-0150), 54 | none |
| `sc-ci-config` (exclusive Woodpecker config extension, agent audit loop) and `sc-ci-maint` CronJob | `woodpecker` | Flux | 20 (ADR-0301/0302) | none |
| `woodpecker-agent-build` (build lane) | `woodpecker` → pods in `ci-build` | Flux | 20, 23 | none |
| `platform-probes` (mirror freshness, TLS, HA, CI-lane signals) | `monitoring` | Flux | 15, 17 | none |
| blackbox exporters (`blackbox`, `blackbox-public`) | `monitoring` | Flux | 38 | none |
| node-exporter | `monitoring-node` | Flux | 38 | none |
| Owned CoreDNS manifest (public-host rewrite) | `kube-system` | Ansible-placed k3s manifest | 09 (ADR-0153) | none |
| `heavy-slot-advertiser` CronJob | `kube-system` | Flux (`capacity` layer) | 51 (ADR-0156) | none |
| kured (small-ha only) | `kube-system` | Flux (expand overlay) | 08, 47 | none |
| Upgrade-gate Jobs (`<app>-upgrade-gate`) | per app namespace | Flux | 48 | gate ConfigMaps |
| Check and restore-test CronJobs (`rt-*`, `check-*`, `silence-audit`, `dr-marker-heartbeat`, retention enforcers) | `dr-tests`, `monitoring`, per app | Flux | 42, 43, 44 (ADR-0553) | S3 `evidence`, `dr-tests-results` |
| OTel agent DaemonSet (small-ha, H03) | `monitoring` | Flux (`telemetry-h03` component) | 41 (ADR-0552) | — |
| Project applications | `apps` | Flux | — | per app |

"Seed set" = installed from the object-storage snapshot before Forgejo exists (ADR-0005, plan 19). Everything else is reconciled by Flux after control transfer, and before `verify` completes.

---

## 5. Namespaces (refined from SPEC §19; ADR-0015)

| Namespace | Purpose | PSA enforce level | Notes |
|---|---|---|---|
| `kube-system` | k3s system (CoreDNS, metrics-server, local-path-provisioner) | privileged (k3s default) | k3s bundled Traefik and ServiceLB are disabled |
| `flux-system` | Flux controllers | restricted-compatible where upstream allows | |
| `cert-manager` | cert-manager | restricted | |
| `cnpg-system` | CNPG operator and plugin | restricted | |
| `longhorn-system` | Longhorn | privileged (required by Longhorn) | isolated by NetworkPolicy |
| `ingress` | Traefik, cloudflared | baseline (tunnel mode); privileged plus VAP `sc-hostport-traefik-only` in direct mode (ADR-0151, CD-13) | |
| `authentik` | Authentik, `authentik-pg` | restricted | |
| `forgejo` | Forgejo, `forgejo-pg`, repo-backup CronJobs | restricted | |
| `woodpecker` | Woodpecker server/agent, `woodpecker-pg` | restricted | |
| `ci-jobs` | Woodpecker pipeline pods (lane `ci`) | restricted | no secrets; `ci-workspace` PVCs ≤ 8Gi only (ADR-0305, CD-06) |
| `ci-build` | Woodpecker build-lane pods (protected refs only, ADR-0301) | baseline (buildah, ADR-0501) | release secrets; `ci-workspace` PVCs ≤ 10Gi |
| `monitoring` | Prometheus stack, Grafana, OTel, (Loki, Tempo) | restricted where upstream allows, otherwise baseline (plan 37) | |
| `monitoring-node` | node-exporter (host access) | privileged | isolated by NetworkPolicy (CD-13) |
| `strange-company` | Agent Runner, Model Gateway, `sc-pg` | restricted | |
| `hermes` | Hermes Agent | restricted | no RBAC rights |
| `agent-jobs` | worker Jobs | **restricted** + VAP | ResourceQuota, LimitRange, default-deny |
| `renovate` | Renovate CronJob | restricted | |
| `dr-tests` | restore-test Jobs | restricted | |
| `apps` | deployed applications | restricted | |

Namespace labels: `strange.company/tier` ∈ {`platform-critical`, `platform`, `application`, `ci`, `agent-worker`} drive PriorityClass selection and policy (plan 51).

---

## 6. Conventions

### 6.1 Naming
- Kubernetes-style configuration manifests (substrate, profiles, policy files, repository context) use `apiVersion: strange.company/v1alpha1`, a `kind`, and **camelCase** fields, as in SPEC §11/§12/§45.
- API payloads, database records, ledger rows, worker results and evidence documents use **snake_case** JSON, as in SPEC §62/§68/§79.
- Task IDs: `sc-<decimal>`, a per-installation monotonic sequence allocated by the Runner (e.g. `sc-184`). Execution IDs: `<task-id>-x<n>` (e.g. `sc-184-x3`). Acceptance criteria IDs: `AC-<n>`, stable within a task.
- Agent branches: `agent/<task-id>` (SPEC §100). Red test commit subject: `test(<task-id>): <summary>`.
- Forgejo machine accounts: `sc-runner`, `sc-woodpecker` (OAuth app owner), `sc-renovate`, `sc-backup`. Flux uses a read-only *deploy key*, not an account.
- Hostnames (under `BASE_DOMAIN`): `auth.` (Authentik), `git.` (Forgejo), `ci.` (Woodpecker), `grafana.`, `hermes.` (Hermes dashboard). The Runner API, Model Gateway, Prometheus, Alertmanager, Longhorn UI and Flux are **not public**. Human access to those goes through Authentik forward-auth only where a plan explicitly justifies it.
- Object-storage buckets: `<prefix>-<purpose>` with purposes `tofu-state`, `recovery`, `bootstrap-seed`, `pg-backups`, `longhorn-backups`, `etcd-snapshots`, `forgejo-objects`, `repo-backups`, `evidence`, `telemetry`. Plan 14 is authoritative for the final set and the credential split.

### 6.2 Contracts
- JSON Schema **2020-12**, written as YAML, with file suffix `.schema.yaml`. Each schema has `$schema`, `$id: https://strange.company/contracts/<file-stem>/v1alpha1`, `title`, `description`, and at least one valid entry in `examples`. Schemas should also carry `x-invalid-examples` (each with `reason`) for negative contract tests.
- HTTP APIs use OpenAPI **3.1** (`*.openapi.yaml`). The MCP surface uses `mcp-surface.schema.yaml` (tool name, input schema, output schema, errors).
- Versioning: `v1alpha1` until the Self-Hosting Threshold. Breaking changes after that require a version bump and a migration note.
- Contracts are validated by CI node F02 (meta-schema validation, examples validate, invalid examples fail).

### 6.3 Plans
- Every subsystem plan follows `plans/_template.md` (SPEC §121). Any omitted section is written as `N/A — <justification>`.
- Requirements derived from SPEC carry trace tags such as `[SPEC §50]`.
- Acceptance criteria are stable checklist items with IDs `<PLAN>-AC-<n>` (e.g. `21-AC-4`), grouped under the DAG gate they belong to (`GATE-<node>`).
- Third-party versions are fixed by the **Stage-0 compatibility baseline** (`plans/compatibility-baseline.yaml`, ADR-0022, CD-29). It records the exact release, or release line, each plan was reasoned against, with evidence. Implementation pins **within the baseline line**: patch updates are free. A newer minor or major is an explicit plan-amendment task that updates the baseline, re-runs the affected `(verify)` ACs and is reviewed. Exact versions and digests go into plan 01's lock files. Plans still name every version-sensitive behavior they depend on, and require a test that re-verifies it.

### 6.4 Policy and configuration files

Paths are relative to the `strange-company` repository root. Plan 01 is authoritative for the final layout.

| File | Kind | Schema | Owning plan |
|---|---|---|---|
| `policy/verification.yaml` | `VerificationPolicy` | `contracts/verification.schema.yaml` | 22 |
| `policy/harnesses.yaml` | `HarnessRegistry` | `contracts/harness.schema.yaml` | 28 |
| `policy/resource-profiles.yaml` | `ResourceProfiles` | `contracts/resource-profile.schema.yaml` | 26 |
| `policy/model-routing.yaml` | `ModelRouting` | `contracts/model-routing.schema.yaml` | 31 |
| `policy/pricing/*.yaml` | `PricingTable` | `contracts/pricing.schema.yaml` | 32 |
| `policy/budgets.yaml` | `BudgetPolicy` | `contracts/budget-policy.schema.yaml` | 33 |
| `policy/verification.yaml` `spec.retry` | retry policy (no separate file, CD-11) | `contracts/verification.schema.yaml` | 29 |
| `policy/roles/*.yaml` | `RoleDefinition` | `contracts/role.schema.yaml` | 25 |
| `context/company/*.md` | company context | — | 25 |
| `<any repo>/.strange-company/repo.yaml` | `RepositoryContext` | `contracts/context.schema.yaml` | 25 |
| `<any repo>/.strange-company/policy.yaml` | `RepositoryPolicy` | `contracts/repo-policy.schema.yaml` | 22 |
| `<any repo>/.strange-company/validation.yaml` | `RepositoryValidation` | `contracts/repo-validation.schema.yaml` | 22 |

---

## 7. Canonical state names (authoritative definition: plan 29, `contracts/state-machine.yaml`)

All plans use these exact identifiers.

**Task workflow states:**
`SPECIFIED`, `TEST_AUTHORING`, `RED_VERIFYING`, `RED_VERIFIED`, `TEST_NEEDS_WORK`, `ALREADY_SATISFIED`, `NEEDS_INVESTIGATION`, `TEST_SPEC_CONFLICT`, `IMPLEMENTING`, `GREEN_VERIFYING`, `GREEN_VERIFIED`, `REVIEW`, `CHANGES_REQUESTED`, `AWAITING_MERGE`, `DONE`, `NEEDS_HUMAN`, `BLOCKED`, `CANCELLED`.

Verification-first tasks use the judge-first states `JUDGE_AUTHORING`, `JUDGE_VALIDATING`, `JUDGE_AWAITING_APPROVAL`, `JUDGE_APPROVED`, `PRODUCING` and `JUDGING` (plan 29, ADR-0353). The authoritative list (24 states, 87 transitions) is `contracts/state-machine.yaml`.

**Execution states** (one worker Job): `QUEUED`, `STARTING`, `RUNNING`, `SUCCEEDED`, `FAILED`, `CANCELLED`, `TIMED_OUT`.

**Human-facing states** (Forgejo issue labels or project columns): `Backlog`, `Ready`, `In Progress`, `Review`, `Done`, `Needs Human`, `Blocked`. Plan 29 defines the many-to-one projection from task states. Forgejo is a projection target, never the authority (SPEC §101).

**Failure classes:** each failed execution records one of `merit` (the work was judged and failed), `infrastructure` (the work could not be run or judged), `policy` (a policy violation was detected) or `budget` (a budget stopped it). Retry budgets are tracked per class (lesson L-4).

---

## 8. Durable-state ownership (summary; authoritative: plan 11)

| State | Owner (system of record) | Replication / backup | RPO / RTO target (SPEC §108) |
|---|---|---|---|
| `strange-company` committed Git | Forgejo repo (Longhorn PVC) | push mirror → GitHub on every push + hourly bundle → S3 | ≤1 h (near-zero mirror lag) / ≤2 h |
| Project Git repositories | Forgejo repos | hourly `git bundle` → S3 `repo-backups` + Longhorn backups | ≤1 h / ≤4 h |
| Forgejo DB | `forgejo-pg` | CNPG WAL archive + daily base backup → S3; PITR | ≤15 min / ≤4 h |
| Authentik DB | `authentik-pg` | same | ≤15 min / ≤4 h |
| Strange Company task state + ledger | `sc-pg` | same | ≤15 min / ≤4 h |
| Woodpecker DB (pipeline history) | `woodpecker-pg` | same (lower criticality; evidence is copied to `sc-pg` + S3 `evidence`) | ≤15 min / ≤4 h |
| Forgejo LFS, attachments, packages, OCI images | S3 `forgejo-objects` | bucket versioning | ≤1 h / ≤4 h |
| RED/GREEN evidence artifacts | S3 `evidence` + digests in `sc-pg` | bucket versioning | ≤15 min / ≤4 h |
| Hermes `/opt/data` | Longhorn PVC | Longhorn recurring backup → S3 (daily) | ≤24 h / ≤8 h (non-authoritative) |
| etcd | k3s on node disk | k3s etcd snapshots → S3 `etcd-snapshots` | accelerator only; Git is canonical |
| OpenTofu state | S3 `tofu-state` (versioned, locked, encrypted) | bucket versioning | latest successful mutation / ≤2 h |
| Bootstrap journal | local `.bootstrap/` + S3 `tofu-state/<install>/journal` | versioning | latest phase / n/a |
| Age identities, recovery secrets | operator offline + S3 `recovery` (passphrase-encrypted bundle) | operator responsibility | n/a / ≤2 h to unlock |
| Metrics (Prometheus TSDB) | local-path PVC | none (best effort) | best effort |
| Logs/traces (small-ha) | Loki/Tempo on S3 `telemetry` | bucket | best effort ≤24 h |

---

## 9. Secret classes (summary; authoritative: plan 35 + `contracts/secret-classes.yaml`)

The authoritative register is `contracts/secret-classes.yaml` (67 classes, validated against `contracts/secret-classes.schema.yaml`), described in plan 35. For each class it defines creator, consumers, storage, rotation, recovery and compromise response (SPEC §99). No plan may use a secret class that is not registered; the `secrets-lint` check in plan 52 enforces this. The table below is generated from the register.

| Class | Title | Owner plan | In recovery bundle |
|---|---|---|---|
| `SC-ACME-ACCOUNT` | ACME account key | 10 | no |
| `SC-AGE-BACKUP` | Repository-backup age identity | 44 | yes |
| `SC-AGE-CI` | CI release-credential age identity | 35 | yes |
| `SC-AGE-CLUSTER` | Cluster age identity | 35 | yes |
| `SC-AGE-RECOVERY` | Offline recovery age identity | 35 | no |
| `SC-AUTHENTIK-AUDIT-TOKEN` | Authentik read-only audit token | 15 | no |
| `SC-AUTHENTIK-BOOTSTRAP` | Authentik bootstrap admin (single use) | 15 | no |
| `SC-AUTHENTIK-SECRET-KEY` | Authentik secret key | 15 | no |
| `SC-BREAK-GLASS-AUTHENTIK` | Authentik break-glass account | 15 | yes |
| `SC-BREAK-GLASS-CLUSTER` | Cluster break-glass SSH key | 15 | yes |
| `SC-BREAK-GLASS-FORGEJO` | Forgejo break-glass account | 15 | yes |
| `SC-BREAK-GLASS-GRAFANA` | Grafana break-glass login | 15 | yes |
| `SC-BREAK-GLASS-INFRA` | Infrastructure break-glass (provider console) | 15 | no |
| `SC-CI-INTENTS` | CI verification-intent bearer | 20 | no |
| `SC-CI-OVH-READONLY` | Read-only OVH credential for provider contract tests | 04 | no |
| `SC-DNS-ACME` | ACME DNS-01 solver credential | 10 | no |
| `SC-DNS-API` | DNS provider credential (toolbox) | 10 | yes |
| `SC-DNS-REGISTRAR` | Domain registrar account | 09 | no |
| `SC-DNS-TUNNEL` | Cloudflare Tunnel credential | 09 | yes |
| `SC-FLUX-DEPLOY-KEY` | Flux read-only deploy key | 18 | no |
| `SC-FORGEJO-ADMIN` | Forgejo automation site administrator | 16 | yes |
| `SC-FORGEJO-APP-SECRETS` | Forgejo application secrets | 16 | no |
| `SC-FORGEJO-BOT-TOKENS` | Forgejo machine-account API tokens | 16 | no |
| `SC-FORGEJO-METRICS-TOKEN` | Forgejo metrics bearer token | 16 | no |
| `SC-FORGEJO-SSH-HOST-KEY` | Forgejo SSH host key | 16 | no |
| `SC-GATEWAY-TOKEN` | Model Gateway caller token | 53 | no |
| `SC-GHCR-PUSH` | GHCR toolbox publication token | 23 | no |
| `SC-GITHUB-MIRROR` | GitHub mirror push credential | 17 | yes |
| `SC-GITHUB-MIRROR-READ` | GitHub mirror read credential for restore tests | 17 | no |
| `SC-GITHUB-PROBE` | GitHub mirror freshness probe token | 17 | no |
| `SC-GRAFANA-ADMIN` | Grafana local admin and secret key | 39 | no |
| `SC-HERMES-API` | Hermes gateway API key | 24 | no |
| `SC-INFRA-PROVIDER` | Infrastructure provider API credentials | 04 | yes |
| `SC-INTERNAL-CA` | Internal CA root key | 10 | no |
| `SC-K3S-ADMIN-KUBECONFIG` | Operator kubeconfig (per-operator client certificate) | 50 | no |
| `SC-K3S-ENCRYPTION-CONFIG` | k3s secrets-encryption keys | 07 | no |
| `SC-K3S-SERVER-CA` | k3s cluster CA and service-account signing keys | 07 | no |
| `SC-K3S-TOKEN` | k3s server/agent join token | 07 | yes |
| `SC-LAB-SSH` | Ephemeral CI lab SSH key | 50 | no |
| `SC-MODEL-API-KEYS` | Model provider API keys | 53 | no |
| `SC-NOTIFY-TRANSPORT` | Notification transport credentials | 42 | no |
| `SC-OBJECT-STORAGE` | S3-compatible object-storage credentials (per bucket/purpose) | 14 | yes |
| `SC-OIDC-CLIENTS` | Authentik OIDC client secrets | 15 | no |
| `SC-PG-APP` | CNPG application credentials | 12 | no |
| `SC-PG-DR-MARKER` | CNPG restore-test role password | 12 | no |
| `SC-RECOVERY-PASSPHRASE` | Recovery bundle passphrase | 35 | no |
| `SC-REGISTRY-PULL` | Forgejo registry pull credential | 23 | no |
| `SC-REGISTRY-PUSH` | Forgejo registry push token | 23 | no |
| `SC-RELEASE-SIGNING` | Release signing key (cosign) | 23 | no |
| `SC-RENOVATE` | Renovate credentials | 23 | no |
| `SC-RUNNER-ADMIN` | Runner break-glass admin token | 26 | yes |
| `SC-RUNNER-HERMES` | Hermes to Runner MCP credential | 26 | no |
| `SC-RUNNER-SIGNING` | Runner token-issuer signing key | 26 | no |
| `SC-RUNNER-WEBHOOK` | Forgejo to Runner webhook HMAC secret | 26 | no |
| `SC-SSH-OPERATOR` | Operator SSH key for nodes | 50 | no |
| `SC-SYNTHETIC-USER` | Synthetic verification user | 15 | yes |
| `SC-TOFU-ENCRYPTION` | OpenTofu state encryption passphrase | 06 | yes |
| `SC-WOODPECKER-ADMIN` | Short-lived Woodpecker admin token | 20 | no |
| `SC-WOODPECKER-AGENT` | Woodpecker agent credentials | 20 | no |
| `SC-WOODPECKER-MAINT` | Woodpecker maintenance token | 20 | no |
| `SC-WOODPECKER-METRICS-TOKEN` | Woodpecker metrics bearer token | 20 | no |
| `SC-WOODPECKER-OAUTH` | Woodpecker Forgejo OAuth application | 20 | no |
| `SC-WOODPECKER-RUNNER` | Runner Woodpecker API token | 20 | no |
| `SC-WOODPECKER-SIGNING` | Woodpecker server signing key | 20 | no |
| `SC-WOODPECKER-USER-TOKENS` | Forgejo OAuth tokens held by Woodpecker | 20 | no |
| `SC-WORKER-DEPLOY-KEY` | Per-execution worker deploy key | 26 | no |
| `SC-WORKER-TASK-TOKEN` | Per-execution Runner task token | 26 | no |

## 10. Contract inventory (owners)

All contracts are validated by CI node F02. Each schema has meta-schema validation, its examples must validate, and its `x-invalid-examples` must fail. Data files (`state-machine.yaml`, `secret-classes.yaml`, `alert-catalog.yaml`) are validated against their schemas. SPEC §122 coverage: provider output, substrate, cluster profile, task, acceptance criteria, verification policy, red verification result, Runner API, MCP surface, worker input, worker result, handoff, repository context, resource profile, harness registry, model routing and cost record are all present. Interfaces without a machine-readable schema are listed with a justification in their owning plan (GC-19).

| Contract file | Title | Owning plan |
|---|---|---|
| `acceptance-criteria.schema.yaml` | AcceptanceCriteriaVersion | 21 |
| `alert-catalog.schema.yaml` | AlertCatalog | 42 |
| `alert-catalog.yaml` | AlertCatalog | 42 |
| `bootstrap-config.schema.yaml` | BootstrapConfig | 50 |
| `bootstrap-journal.schema.yaml` | BootstrapJournal | 50 |
| `budget-policy.schema.yaml` | BudgetPolicy | 33 |
| `ci-policy.schema.yaml` | CIPolicy | 20/52 |
| `compatibility-baseline.schema.yaml` | Stage-0 compatibility baseline (`plans/compatibility-baseline.yaml`) | lead (ADR-0022) |
| `cluster-profile.schema.yaml` | ClusterProfile | 03 |
| `context.schema.yaml` | Context | 25 |
| `control-transfer-report.schema.yaml` | ControlTransferReport | 19 |
| `cost-record.schema.yaml` | CostRecord | 32 |
| `egress-policy.schema.yaml` | EgressPolicy | 36 |
| `forge-configuration.schema.yaml` | ForgeConfiguration | 16 |
| `forge-init-result.schema.yaml` | Forge-init result | 16 |
| `gateway-status.schema.yaml` | GatewayStatus | 53 |
| `gateway-token.schema.yaml` | GatewayTokenClaims | 53 |
| `github-mirror-config.schema.yaml` | GitHubMirrorConfig | 17 |
| `green-result.schema.yaml` | GreenResult | 22 |
| `handoff.schema.yaml` | Handoff | 30 |
| `harness.schema.yaml` | HarnessRegistry | 28 |
| `mcp-surface.schema.yaml` | RunnerMcpSurface | 26 |
| `model-routing.schema.yaml` | ModelRouting | 31 |
| `notification.schema.yaml` | NotificationPost | 42 |
| `object-storage-layout.schema.yaml` | ObjectStorageLayout | 14 |
| `pricing.schema.yaml` | PricingTable | 32 |
| `provider.schema.yaml` | ProviderOutput | 03 |
| `recovery-bundle.schema.yaml` | RecoveryBundleManifest | 35 |
| `recovery-record.schema.yaml` | RecoveryRecord | 45 |
| `red-result.schema.yaml` | RedResult | 22 |
| `release-trace.schema.yaml` | ReleaseTrace | 23 |
| `repo-policy.schema.yaml` | RepositoryPolicy | 22 |
| `repo-validation.schema.yaml` | RepositoryValidation | 22 |
| `resource-profile.schema.yaml` | ResourceProfiles | 26 |
| `restore-test-result.schema.yaml` | RestoreTestResult | 44 |
| `role.schema.yaml` | RoleDefinition | 25 |
| `runner-api.openapi.yaml` | Strange Company Agent Runner internal API | 26 |
| `secret-classes.schema.yaml` | SecretClassRegister | 35 |
| `secret-classes.yaml` | SecretClassRegister | 35 |
| `seed-manifest.schema.yaml` | SeedManifest | 19 |
| `state-machine.schema.yaml` | TaskStateMachine | 29 |
| `state-machine.yaml` | TaskStateMachine | 29 |
| `substrate.schema.yaml` | BootstrapSubstrate | 03 |
| `task-token.schema.yaml` | WorkerTaskTokenClaims | 26 |
| `task.schema.yaml` | TaskRecord | 29 |
| `telemetry.schema.yaml` | TelemetryConventions | 40 |
| `test-manifest.schema.yaml` | TestManifest | 22 |
| `test-quarantine.schema.yaml` | TestQuarantine | 52 |
| `verification.schema.yaml` | VerificationPolicy | 22 |
| `version-locks.schema.yaml` | VersionLocks | 01 |
| `worker-input.schema.yaml` | WorkerTaskPacket | 27 |
| `worker-result.schema.yaml` | WorkerResult | 27 |

## 11. Plan inventory (owners)

SPEC §120 numbering is preserved. Plans 50–55 cover subsystems that SPEC §120 did not list but the definition of done requires.

| Plan | Subject | Plan | Subject |
|---|---|---|---|
| 01 | Repository layout, version locks, Make targets | 28 | Harnesses |
| 02 | Bootstrap toolbox | 29 | Task state machine |
| 03 | Provider interface, substrate, profiles | 30 | Handoffs |
| 04 | OVH provider | 31 | Model routing |
| 05 | Existing-nodes provider | 32 | Cost accounting |
| 06 | OpenTofu state | 33 | Budget enforcement |
| 07 | k3s | 34 | Security architecture |
| 08 | Node baseline & security | 35 | Secrets |
| 09 | Networking & ingress | 36 | Network policy |
| 10 | TLS & certificates | 37 | Admission policy |
| 11 | Storage & durable-state ownership | 38 | Prometheus |
| 12 | CNPG | 39 | Grafana |
| 13 | Longhorn | 40 | OpenTelemetry |
| 14 | Object storage | 41 | Logging & tracing (Loki/Tempo) |
| 15 | Authentik & break-glass | 42 | Alerting & notifications |
| 16 | Forgejo | 43 | Retention & GC |
| 17 | GitHub mirror | 44 | Backup & restore testing |
| 18 | Flux & GitOps tree | 45 | Disaster recovery & black start |
| 19 | Seed & control transfer | 46 | Self-Hosting Threshold |
| 20 | Woodpecker | 47 | HA expansion |
| 21 | TDD workflow | 48 | Upgrades |
| 22 | Verification policy, RED/GREEN, test protection | 49 | Destroy |
| 23 | Supply chain | 50 | Bootstrap orchestrator & CLI |
| 24 | Hermes | 51 | Capacity, scheduling priority & quotas |
| 25 | Context | 52 | Deterministic CI strategy |
| 26 | Agent Runner | 53 | Model Gateway |
| 27 | Worker contract | 54 | `strange-company` Helm chart |
| | | 55 | Forge workflow (branches, PRs, issues, merge policy) |

---

## 12. Procedure inventory (owners)

Every SPEC §113 procedure exists. Procedures discovered during Stage 0 have been added (SPEC §113: "Stage 0 SHALL add procedures discovered to be missing"). Every procedure follows `procedures/_template.md`. Every alert runbook path in `contracts/alert-catalog.yaml` resolves to a file here. The table below is generated from the files.

| Procedure | Owning plan |
|---|---|
| `add-node.md` | 47 |
| `agent-runner-failure.md` | 26 |
| `authentik-outage.md` | 15 |
| `bootstrap-preflight-failure.md` | 50 |
| `bootstrap-resume.md` | 50 |
| `bootstrap-verify.md` | 50 |
| `break-glass-access.md` | 15 |
| `budget-emergency-stop.md` | 33 |
| `certificate-failure.md` | 10 |
| `cnpg-primary-loss.md` | 12 |
| `cnpg-replica-loss.md` | 12 |
| `credential-compromise.md` | 35 |
| `disk-pressure.md` | 08 |
| `edge-ingress-failure.md` | 09 |
| `emergency-merge.md` | 55 |
| `expand-to-small-ha.md` | 47 |
| `failed-flux-handoff.md` | 19 |
| `flux-reconciliation-failure.md` | 18 |
| `fresh-install.md` | 50 |
| `gateway-failure.md` | 53 |
| `github-black-start.md` | 45 |
| `github-mirror-failure.md` | 17 |
| `hermes-failure.md` | 24 |
| `longhorn-replica-loss.md` | 13 |
| `lost-etcd-member.md` | 07 |
| `lost-node.md` | 47 |
| `manage-human-access.md` | 15 |
| `merge-without-provenance.md` | 55 |
| `monitoring-failure.md` | 38 |
| `notification-failure.md` | 42 |
| `object-storage-outage.md` | 14 |
| `pricing-update.md` | 32 |
| `provider-outage.md` | 03 |
| `quarantine-worker.md` | 26 |
| `recover-opentofu-state.md` | 06 |
| `recovery-drill.md` | 45 |
| `remove-node.md` | 47 |
| `replace-object-storage.md` | 14 |
| `restore-authentik.md` | 15 |
| `restore-cnpg.md` | 12 |
| `restore-etcd.md` | 07 |
| `restore-forgejo.md` | 16 |
| `restore-longhorn.md` | 13 |
| `restore-project-repository.md` | 44 |
| `restore-test-failure.md` | 44 |
| `rotate-age-key.md` | 35 |
| `rotate-break-glass.md` | 35 |
| `rotate-forge-credentials.md` | 16 |
| `rotate-model-credentials.md` | 53 |
| `rotate-secrets.md` | 35 |
| `rotate-storage-credentials.md` | 35 |
| `safe-destruction.md` | 49 |
| `seed-flux-handoff.md` | 19 |
| `signing-key-rotation.md` | 23 |
| `sops-decryption-failure.md` | 35 |
| `stuck-task-recovery.md` | 29 |
| `supply-chain-init.md` | 23 |
| `tdd-task-recovery.md` | 21 |
| `total-cluster-loss.md` | 45 |
| `upgrade-k3s.md` | 48 |
| `upgrade-platform-component.md` | 48 |
| `woodpecker-failure.md` | 20 |

## 13. Dependency DAG

The authoritative machine-readable DAG is `plans/dag.yaml`. It is acyclic, and `make dag-check` (plan 01) verifies that every gate AC depends only on nodes upstream of its gate. Each node's gate is the union of its `### GATE-<node>` sections across the plans listed for it. `PLAN.md` is generated from both. Edges are hard dependencies. The text below is generated from `dag.yaml`, with each node's dependencies shown after the arrow.

```text
PHASE 1 — Foundations
  F01  repo-skeleton                                ← —
  F02  contract-tooling                             ← F01
  F03  toolbox-image                                ← F01
  F04  policy-lib                                   ← F02
  F05  state-machine-lib                            ← F02
  F06  verifier-lib                                 ← F02, F04
  F07  worker-sdk-and-fake-harness                  ← F02
  F08  bootstrap-orchestrator-core                  ← F02, F03
  F09  providers-and-paid-guardrails                ← F08
  F10  node-baseline-and-k3s-roles                  ← F03, F08, F09
  F11  gitops-tree-offline                          ← F02
  F12  admission-and-netpol-bundle                  ← F11
  F13  strange-company-chart-skeleton               ← F11, F12

PHASE 2 — Platform on one node
  P01  object-storage-and-remote-state              ← F08
  P02  provision                                    ← F09, P01
  P03  configure-node-and-k3s                       ← P02, F10
  P04  seed                                         ← P03, F11, F12
  P05  forge-init                                   ← P04
  P06  control-transfer-and-mirror                  ← P05
  P07  authentik-sso-break-glass                    ← P06
  P08  woodpecker-trusted-ci                        ← P06
  P09  observability-and-notifications              ← P06, P07, P08
  P10  backups-and-restore-tests                    ← P06, P09, P08
  P11  supply-chain                                 ← P08, P12
  P12  bootstrap-verify-control-transfer-complete   ← P07, P08, P09, P10

PHASE 3 — Control plane
  C01  runner-core                                  ← F04, F05, F13, P11
  C02  k8s-adapter                                  ← C01, F07, F12, C03, C07
  C03  forgejo-adapter                              ← C01, P05
  C04  woodpecker-adapter-red-green-evidence        ← C01, P08, F06, C02, C03
  C05  model-gateway-ledger-pricing                 ← C01
  C06  budgets-emergency-stop-notifications         ← C05, P09, C02, C04, C08
  C07  context-compiler                             ← C01, F04
  C08  mcp-surface-and-hermes                       ← C01, C05, P07
  C09  merge-policy-enforcement                     ← C03, C04, C02
  C10  fake-end-to-end-tdd-flows                    ← C02, C03, C04, C05, C06, C07, C08, C09
  C11  sc-dashboards-and-alerts                     ← C06, P09, C10

PHASE 4 — Threshold
  R01  real-harness                                 ← C10
  R02  self-hosting-threshold                       ← R01, C11, P12

PHASE 6 — Small-HA
  H01  nodes-2-3-etcd-quorum                        ← P12, R02
  H02  cnpg-3-longhorn-2                            ← H01, C08
  H03  loki-tempo                                   ← H01, P09, R02
  H04  ha-hardening-drills                          ← H02, H03, R02
```

Changes from the SPEC §123 approximation, all recorded in ADRs and consolidation decisions:
- Authentik is not a dependency of the seed or of Flux (SI-05).
- The Model Gateway (C05) is a first-class node.
- Admission and network policy (F12) are validated offline before any cluster exists.
- P11 (the installation's supply chain) runs **after** control transfer (CD-19), but still gates C01.
- Small-HA (H01–H04) follows the Self-Hosting Threshold (R02).
- The review round (GC-01) moved acceptance criteria to the gate where their prerequisites exist, or added the edge.

## 14. Implementation sequence

`PLAN.md` (generated from `dag.yaml` and the gate sections) holds the ordered list of nodes and their gate criteria. Summary:

1. **Phase 1:** F01 → (F02, F03) → (F04, F05, F07, F11) → (F06, F08, F12) → (F09, F13) → F10 (node roles need the orchestrator core F08 and the providers F09, BOOT-11). There is no cloud spend in this phase. Every node is test-first, with RED and GREEN demonstrated in interim CI (plan 52 §bootstrap discipline).
2. **Phase 2:** P01 → P12 against one real node (OVH VPS-2-class or `existing`). P12 is the **Bootstrap Definition of Done** (SPEC §130).
3. **Phase 3:** C01 → C11. C10 is the **orchestration definition of done**: every fake scenario passes on the live cluster.
4. **Phase 4:** R01 → R02, the **Self-Hosting Threshold** (SPEC §117–118).
5. **Phase 5:** Strange Company works on itself, and new work enters through Hermes.
6. **Phase 6:** H01 → H04, which reaches **small-HA**.

---

## 15. MVP / post-MVP boundary

**MVP** = DAG nodes F01–F13, P01–P12, C01–C11, R01–R02 (SPEC §126). That includes: one-node bootstrap; k3s; CNPG; Longhorn; Authentik; Forgejo; GitHub mirror; Woodpecker; Flux; Prometheus; Grafana; OTel Collector; Hermes; Runner start/status/logs/result/cancel (+ `agent_list`); repository context; acceptance criteria; enforced test-first; handoffs; state machine; bounded retries; model/harness abstraction; Model Gateway ledger; budgets and emergency stop; fake workers; one real harness; agent branch push; automatic PR creation; trusted CI; seed→Forgejo→Flux control transfer; self-hosted RED→GREEN→REVIEW acceptance.

**Explicitly post-MVP** (SPEC §127–128, plus Stage-0 deferrals): H01–H04 (small-HA, Loki, Tempo); autonomous merge; admission-time image signature verification (signing and promotion-time verification are MVP, plan 23); second and third real harnesses; task DAG scheduling and cross-repo orchestration; preview environments; autoscaled or local workers; worker-to-worker chat; multi-cluster; a Strange Company–specific web UI (Hermes dashboard, Forgejo and Grafana are the MVP human surfaces); off-cluster GitHub Actions mirror-freshness watchdog beyond the MVP dead-man's switch (plan 42 decides).

---

## 16. Cross-cutting rules every plan must honor

1. **No Bitnami** charts or images anywhere (SPEC §20). CI node F11 renders all manifests and fails on any `bitnami` or `bitnamilegacy` reference.
2. **Pinned artifacts:** production images are digest-pinned, Helm charts version-pinned, binaries checksum-pinned (SPEC §37, §116).
3. **Fail closed** on missing policy, unknown model price, schema-invalid input, missing provenance, or unverifiable evidence.
4. **No secret in plaintext in Git**, logs, telemetry, task records or handoffs (SPEC §94). Worker logs are scrubbed against the known secret set before persistence (plan 27).
5. **Idempotency:** every bootstrap phase, controller reconcile, Runner operation and procedure step states its idempotency behavior.
6. **Separation of compute and recovery lifecycles:** no automated path deletes object-storage recovery data, the GitHub mirror, or remote state history (SPEC §114).
7. **Infrastructure outranks agents** in scheduling (plan 51). A worker can be slow, but must never starve the platform.
8. **Deterministic CI:** every gate is runnable without paid model APIs (SPEC §104). Real-model smoke tests are separate and opt-in.

---

## 17. Stage-0 completion

Stage 0 is complete. `STATUS.md` holds the SPEC §125 checklist with evidence. Ten independent fresh-context reviews (SPEC §132 items 43–47 and the critical areas of §132) and three closure verifications are in `plans/reviews/`. The binding consolidation decisions are in `plans/reviews/consolidation-decisions.md` (CD-01..CD-26). They take precedence over any earlier wording in individual plans that they explicitly resolve.
