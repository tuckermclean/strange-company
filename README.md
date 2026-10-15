# Strange Company

**A self-hosted software factory that builds software test-first and can build itself.**

You give Strange Company engineering intent in plain language. It turns that intent into bounded tasks and decides how each result will be judged *before* any work starts. It sends disposable AI agents to write the tests and then the code. Deterministic machinery proves the tests failed first and pass afterward. Every model call is accounted for down to the token and the micro-dollar. Approved changes reach production only through Git. The whole organization runs on infrastructure you own: one inexpensive VPS to start, three nodes for resilience. It can be rebuilt from off-cluster backups if that infrastructure disappears.

The canonical specification is [`SPEC.md`](SPEC.md). This README summarizes it. Where they disagree, `SPEC.md` wins.

- [What Strange Company is](#what-strange-company-is)
- [The self-hosting minimum](#the-self-hosting-minimum)
- [Development roadmap](#development-roadmap)
- [Repository layout](#repository-layout)

---

## What Strange Company is

### One request, end to end

You tell Strange Company: *"Add `/healthz` to example-service."*

1. **Hermes**, the persistent orchestrator, turns the request into a specification and stable, numbered acceptance criteria.
2. Hermes asks the **Agent Runner** to dispatch a **tester**. The tester is an ephemeral Kubernetes Job. It writes acceptance tests, pushes a *RED* commit to an agent branch, writes a structured handoff, and exits.
3. **Woodpecker**, the trusted CI, runs the RED commit. The Runner classifies the result itself. The tests must fail for the right reason: an assertion failure, not a compile error or a missing import. Only then does the task reach `RED_VERIFIED`.
4. The Runner dispatches an **implementer**. It starts from the RED commit and cannot modify the protected acceptance tests. It writes the code, pushes, hands off, and exits.
5. Woodpecker runs the acceptance and regression suites. The task reaches `GREEN_VERIFIED`.
6. A **reviewer** gets the requirement, the acceptance criteria, the RED evidence, the diff and the GREEN evidence, and reports its verdict.
7. The Runner opens the pull request in **Forgejo** and posts the `tdd-provenance` and `review` statuses that branch protection requires. A human, or later a policy, merges.
8. Trusted CI builds and signs the artifact. The GitOps state changes, and **Flux** reconciles it into Kubernetes.

At the end the system can tell you which models and harnesses were used and what each invocation cost. It can tell you which test specified the behavior and prove the test failed first. It can tell you which implementation made it pass, which CI run verified it independently, and which commit shipped. The creative worker never held production authority at any point.

### Architecture

```text
Human ── Authentik SSO ──▶ Hermes dashboard · Forgejo · Grafana
                                │
                                ▼
Hermes (orchestrator) ── MCP ──▶ Agent Runner ── Kubernetes Jobs ──▶ tester / implementer / reviewer / research workers
                                │                                        │
             task state machine, TDD gates,                              ├── model calls ──▶ Model Gateway ──▶ model providers
             budgets, context, policy                                    │                   (ledger, prices, budgets, e-stop)
                                │                                        └── git push agent/<task>
                                ▼                                                 │
                          sc-pg (CNPG) ◀──── evidence ──── Woodpecker ◀── Forgejo ┘ ── continuous mirror ──▶ GitHub
                                                                          │
                                                          Flux ◀── approved Git state
                                                           │
                                                           ▼
                                                      Kubernetes (k3s)

Off-cluster: S3 object storage (backups, WAL, snapshots, repository bundles, OpenTofu state),
             the GitHub mirror (recovery seed), and an operator-held recovery bundle and passphrase.
```

| Component | Role |
|---|---|
| **Authentik** | Human identity (OIDC) for every human-facing surface, with a designed break-glass path. |
| **Hermes** | The upstream Hermes Agent. It understands intent and coordinates the work. It reaches the Runner only over MCP, holds no Kubernetes rights, and is never trusted to enforce anything. |
| **Agent Runner** | Go service. It owns the task state machine, the TDD gates, retries, context compilation, worker admission and the Forgejo, Woodpecker and Kubernetes adapters. |
| **Workers** | Ephemeral, single-role Jobs that run a harness CLI (Claude Code first) behind the `sc-worker` adapter. Each holds only task-scoped credentials. |
| **Model Gateway** | The only network path to model providers. It is a protocol-native pass-through. It checks budgets *before* forwarding and writes one immutable cost record per request. |
| **Forgejo** | The primary forge and the organization's engineering memory. Its tracking issues are where humans issue `/sc` commands. |
| **Woodpecker** | Trusted CI. Verification pipelines run with no secrets. Only protected-ref pipelines build, sign and push. |
| **Flux** | Reconciles approved Git state from Forgejo into the cluster. |
| **CNPG, Longhorn, object storage** | Durable state: PostgreSQL clusters, replicated volumes, and off-cluster backups. |
| **Prometheus, Grafana, OpenTelemetry, Alertmanager** | Explain what is happening and what it costs. They page a human through a configured transport and an external dead-man's switch. |
| **GitHub** | The off-cluster mirror. The platform can be resurrected from it when Forgejo is gone. |

### The rules it lives by

- **Verification precedes construction.** The organization defines how an answer will be judged before asking an agent for it. Behavior changes go RED → GREEN → REVIEW. Work that cannot be tested executably, such as documentation or research, needs an approved *judge* artifact first. Exemptions are declared in policy files, never improvised.
- **Models propose; deterministic systems decide.** The Runner, CI, branch protection and Kubernetes admission enforce every invariant. Hermes may be wrong, prompt-injected or replaced, and the system stays safe.
- **Git is organizational memory, and production is explainable by Git.** Desired state, policy and rules live in files under version control. Anything running in the cluster traces back to an approved commit.
- **Workers are ephemeral; important state outlives them.** A worker's useful output is a commit and a structured handoff. Conversation history is never the record.
- **Authority decreases near production.** Workers get a per-execution deploy key, a task token and a gateway token. They never get provider keys, Flux, registry-push or signing credentials.
- **Retries are bounded, and spending has hard ceilings.** Budgets are enforced per request at the Gateway. An emergency stop halts spending from any of three independent sources.

### Operating it

An operator needs only Git, Docker or Podman, an SSH key, and credentials for the infrastructure provider, DNS or tunnel, S3 storage and the chosen model APIs. Every other tool (OpenTofu, Ansible, kubectl, Helm, Flux, SOPS, age) runs from a version-pinned toolbox image.

```bash
git clone https://github.com/<owner>/strange-company.git
cd strange-company
cp bootstrap.env.example bootstrap.env
$EDITOR bootstrap.env
./bootstrap --profile self-hosting-minimum
```

The full walkthrough is [`procedures/fresh-install.md`](procedures/fresh-install.md). Bootstrap is idempotent and resumable. It runs preflight, plan, provision, configure, seed, forge-init, flux-bootstrap, verify and finally **control transfer**. After control transfer the repository lives in Forgejo, Flux reconciles from it, and GitHub becomes its continuously updated mirror. The same CLI covers the rest of the lifecycle:

```text
./bootstrap plan | status | verify
./bootstrap expand --profile small-ha
./bootstrap recover
./bootstrap destroy
```

### Infrastructure profiles

| | `self-hosting-minimum` | `small-ha` |
|---|---|---|
| Nodes | 1 | 3 |
| Per node (supported minimum) | 4 CPU, 12 GiB, 100 GiB SSD (200 GiB NVMe preferred) | same |
| Kubernetes | k3s, embedded etcd, 1 server | k3s, embedded etcd, 3 servers |
| PostgreSQL (CNPG) | 1 instance | 3 instances |
| Longhorn replicas | 1 | 2 |
| Concurrent agents | 1 | 2 |

The one-node system is the same architecture with less redundancy. Growing to three nodes never means rebuilding. Nodes with 8 GiB are accepted as a `constrained` configuration, with a warning and no capacity guarantee. The supported providers are OVH and `existing` (machines you already own, reached over SSH).

### Surviving disaster

The things needed to rebuild everything live off the cluster. The GitHub mirror holds the code and desired state. S3 object storage holds PostgreSQL backups and WAL, volume snapshots, repository bundles and OpenTofu state. The operator holds a passphrase-encrypted recovery bundle that includes an offline age key. Restores are tested repeatedly, not assumed. A documented **black start** rebuilds a working Strange Company on fresh machines from those three sources alone.

---

## The self-hosting minimum

Strange Company reaches its **Self-Hosting Threshold** when it can keep building itself *under its own rules*. Editing its own source is not enough. It must **test-first develop itself**. The threshold is proven by a genuine task against this repository, for example *"add or improve one deterministic Agent Runner behavior"*. The task must go through the full flow: human requirement, acceptance criteria, tester, RED commit, Woodpecker confirms a meaningful failure, implementer, GREEN verification, reviewer, accepted PR.

The minimum that must exist to get there:

**Substrate.** One node (4 CPU, 12 GiB RAM, 100–200 GiB disk), provisioned by `./bootstrap`, plus S3-compatible object storage and a GitHub repository. The target machine is an OVH VPS (6 vCPU, 12 GB, 100 GB NVMe), with `existing` machines as the fallback.

**Platform:**

- k3s with embedded etcd
- CloudNativePG and Longhorn
- Authentik with break-glass access
- Forgejo, with a continuous GitHub mirror
- Flux, after the seed → Forgejo → Flux control transfer
- Woodpecker as trusted CI
- Prometheus, Grafana and the OpenTelemetry Collector, with Alertmanager notifications
- Hermes and the Agent Runner

**Organizational capabilities:**

- the task state machine, with bounded retries
- acceptance criteria and repository context
- structured handoffs between workers
- the enforced TDD state machine: RED verification, test protection, `ALREADY_SATISFIED` and `TEST_SPEC_CONFLICT` handling, verification-first exemptions, and provenance enforced at merge
- the model and harness abstraction, with every call going through the Model Gateway
- exact per-invocation model accounting, budget limits and an emergency stop
- fake workers, so that every flow is testable in CI without paid model calls
- **one real coding harness** (Claude Code)
- agent branch push, automatic PR creation and human merge in Forgejo

**Deliberately not required** before the threshold: high availability, Loki and Tempo, autonomous merging, more than one real harness, live worker chat, persistent agent personalities, a worker message bus, task DAG scheduling, multi-cluster, local model hosting, preview environments, or a custom web UI. The MVP human surfaces are the Hermes dashboard, Forgejo and Grafana.

---

## Development roadmap

### Where things stand

**Stage 0, the complete engineering definition, is done.** No platform code exists yet, by design: SPEC §2 requires the whole system to be planned before it is built. Stage 0 produced:

- 55 subsystem plans;
- 79 ADRs;
- 52 machine-readable contracts (JSON Schema and OpenAPI);
- 62 operator procedures;
- a register of 59 spec issues, none of them blocking;
- a compatibility baseline pinning 61 third-party components;
- an acyclic **42-node dependency DAG** with **1,179 acceptance criteria** written in advance.

An executable validator (`tools/stage0/validate.py`, run in CI) checks the definition for internal consistency. [`STATUS.md`](STATUS.md) has the details.

Implementation is a sequence of DAG nodes, listed in [`PLAN.md`](PLAN.md). Each node is a bounded task whose success criteria already exist:

1. turn the node's gate criteria into executable checks;
2. commit them first and show them failing for the right reason;
3. implement until they pass in trusted CI;
4. record the gate evidence in `STATUS.md`.

A node starts only when all its dependencies have passed their gates. Until Woodpecker exists (node P08), trusted CI is GitHub Actions running the same containerized `make` targets.

### Phases

| Phase | Nodes | Delivers | Milestone |
|---|---|---|---|
| **1. Foundations** | F01–F13 | Repository skeleton and CI; contract tooling; the bootstrap toolbox image; the policy, state-machine and verifier libraries; the worker SDK and fake harness; the bootstrap orchestrator core; providers and paid-resource guardrails; the node baseline; the offline GitOps tree; admission and network-policy bundles; the Strange Company Helm chart skeleton. All of it is testable locally, with no paid infrastructure (QEMU lab VMs and an S3 emulator). | |
| **2. Platform on one node** | P01–P12 | Object storage and remote OpenTofu state; provisioning; k3s and node security; the seed; Forgejo; control transfer and the GitHub mirror; Authentik and break-glass; Woodpecker; observability and notifications; backups and restore tests; the supply chain; end-to-end bootstrap verification. | **Bootstrap Definition of Done** (P12) |
| **3. Control plane** | C01–C11 | Runner core; the Kubernetes, Forgejo and Woodpecker adapters with RED/GREEN evidence; the Model Gateway, ledger and pricing; budgets and the emergency stop; the context compiler; the MCP surface and Hermes; merge-policy enforcement; every TDD flow end to end with fake workers; Strange Company dashboards and alerts. | **Orchestration DoD** (C10) |
| **4. Real harness** | R01–R02 | The Claude Code harness, then the first genuine self-hosted RED → GREEN → REVIEW task. | **Self-Hosting Threshold** (R02) |
| **5. Self-development** | — | From here on, Strange Company builds itself using its own workflow. | |
| **6. Small-HA** | H01–H04 | Nodes 2 and 3 with an etcd quorum; CNPG 1 → 3 and Longhorn 1 → 2; Loki and Tempo; HA hardening and recovery drills. Ideally built *by* Strange Company. | **Small-HA** (H04) |

Critical path: F01 → F02 → F08 → P01 → P02 → P03 → P04 → P05 → P06 → P08 → P09 → P10 → P12 → P11 → C01 → (C03, C07) → C02 → C04 → C09 → C10 → R01 → R02.

**Next step: node F01** (repository skeleton and CI; plans 01 and 52).

### After the MVP

Possible later capabilities, none of which may complicate the threshold:

- low-risk autonomous merges;
- admission-time image signature verification;
- the Codex and OpenCode harnesses;
- task DAG scheduling and cross-repository orchestration;
- PM and Scrum agents;
- preview environments;
- autoscaled and local workers;
- richer agent messaging;
- multiple clusters.

---

## Repository layout

This repository is simultaneously the bootstrap repository, the infrastructure and GitOps source, the application source, the specification, the recovery seed and the operational knowledge base. There is no separate bootstrap repo.

| Path | Contents |
|---|---|
| [`SPEC.md`](SPEC.md) | The canonical specification (v1.3). It is authoritative over everything else. |
| [`PLAN.md`](PLAN.md) | The implementation sequence: DAG nodes, dependencies and gate criteria. Generated; do not hand-edit. |
| [`STATUS.md`](STATUS.md) | Current stage and gate evidence. |
| [`plans/`](plans/) | `00-master-plan.md`, the 55 subsystem plans, `dag.yaml`, spec issues, the compatibility baseline, the machine-account privilege matrix and the review records. |
| [`adr/`](adr/) | Architecture decision records. |
| [`contracts/`](contracts/) | Schemas, the Runner OpenAPI definition, the state machine, the alert catalogue and the secret-class register. |
| [`procedures/`](procedures/) | Operator runbooks: bootstrap, recovery, rotation, break-glass, destruction and more. |
| [`tools/stage0/`](tools/stage0/) | The Stage-0 validator. |

---

## License

Strange Company is free software, licensed under the [GNU Affero General Public License v3.0 or later](LICENSE) (`AGPL-3.0-or-later`). You may use, study, modify and share it. If you distribute it, or run a modified version as a network service that other people use, you must offer those users the complete corresponding source under the same license.
