# Strange Company

## Canonical System Specification

**Specification version:** 1.2  
**Status:** Frozen architecture baseline  
**Repository:** `strange-company`  
**System type:** Self-hosted autonomous software factory  
**Bootstrap target:** One inexpensive VPS  
**Production baseline:** Three-node resilient k3s cluster

**Amendments:**
- **1.2 (2026-10-14, operator decision during Stage 0).** Node disk sizing in §11 and §12 changed to minimum 100Gi and preferred 200Gi NVMe per node for both profiles. The previous values were 75Gi minimum and 100Gi preferred. Reason: the Stage-0 storage budget (plans 11 §12 and 51, spec issue SI-32) showed 75–100 GiB cannot hold the platform's images, snapshots, CI workspace and restore scratch with a useful growth horizon. Memory minimums are unchanged: 8Gi stays allowed as a constrained configuration (SI-12, ADR-0020).

---

# 0. Authority

This file is the canonical product and architecture specification for Strange Company.

All plans, contracts, schemas, architecture decisions, procedures, infrastructure, code, tests, Helm charts, documentation, and operational behavior SHALL conform to this document.

If another document conflicts with `SPEC.md`, `SPEC.md` wins.

Implementation agents MUST NOT silently reinterpret, weaken, or replace requirements.

If planning or implementation discovers that a requirement is:

- contradictory;
- impossible;
- unsafe;
- based on a false assumption;
- or disproportionately expensive relative to its value;

the conflict SHALL be surfaced explicitly.

A fundamental architectural change requires an explicit modification to `SPEC.md`.

`SPEC.md` defines **what the system is and what must be true**.

Implementation plans remain revisable.

---

# 1. Governing engineering principles

Strange Company SHALL dogfood its own engineering philosophy.

## 1.1 Reduce ambiguity before implementation

> **Reduce ambiguity before spending intelligence on implementation.**

Specification and verification should make implementation progressively more mechanical.

Expensive reasoning should be spent primarily where ambiguity actually exists.

---

## 1.2 Verification precedes construction

> **Define how success will be judged before producing the work being judged.**

For behavior-changing software, this normally means strict test-driven development:

```text
requirement
   ↓
acceptance criteria
   ↓
executable test
   ↓
prove RED
   ↓
implementation
   ↓
prove GREEN
   ↓
review
```

For work that cannot meaningfully use executable TDD, an equivalent verification-first artifact SHALL precede construction.

---

## 1.3 Important state outlives the process holding it

Important state SHALL be deliberately stored somewhere more durable than the process currently using it.

Git, object storage, database state, external recovery credentials, and reproducible configuration are preferred over local memory, ephemeral filesystem state, or conversation history.

---

# 2. Stage 0 — Complete Engineering Definition

Starting from an otherwise empty repository containing only `SPEC.md`, the first task is NOT to implement Strange Company.

The first task is:

> **Plan everything required to build and operate it.**

A capable planning agent SHALL derive a complete engineering definition before substantial implementation begins.

Stage 0 SHALL define:

- every subsystem;
- every important interface;
- every machine-readable contract;
- every durable state owner;
- every security boundary;
- every secret class;
- every lifecycle;
- every bootstrap phase;
- every migration;
- every major failure mode;
- every recovery path;
- every verification mechanism;
- every deterministic acceptance gate;
- every operational procedure;
- every implementation dependency.

Plans SHOULD be sufficiently complete that subsequent coding agents are primarily solving bounded engineering tasks rather than inventing product behavior.

Stage 0 SHOULD NOT attempt to predict arbitrary source-file or line-level implementation details that are better decided when code exists.

---

# 3. Mission

Strange Company is a self-hosted software-development factory.

A human provides relatively high-level engineering intent to a persistent orchestrator.

The orchestrator converts intent into bounded work.

Independent ephemeral agents may:

- research;
- specify;
- write tests;
- implement;
- review;
- debug;
- validate.

Durable work is recorded outside the worker that produced it.

Deterministic systems independently judge work wherever possible.

Approved desired state is represented in Git.

Flux reconciles approved state into Kubernetes.

Conceptually:

```text
Human
  │
  ▼
Hermes
  │
  ▼
Agent Runner
  │
  ├── test worker
  ├── implementation worker
  ├── research worker
  └── review worker
         │
         ▼
      Forgejo
         │
         ▼
     Woodpecker
         │
         ▼
      Git state
         │
         ▼
        Flux
         │
         ▼
     Kubernetes
```

---

# 4. Fundamental invariants

## 4.1 Git is organizational memory

Important engineering state SHALL survive the process currently using it.

Source code, architecture, policies, deployment definitions, repository context, validation rules, significant decisions, and durable work products SHOULD live in Git.

The following are NOT authoritative organizational state:

- worker filesystems;
- model conversations;
- model memory;
- local scratch files;
- human recollection.

---

## 4.2 Production must be explainable by Git

Normal production state SHALL originate from version-controlled desired state.

Canonical flow:

```text
specification
   ↓
tests / verification
   ↓
implementation
   ↓
pull request
   ↓
CI
   ↓
merge
   ↓
release artifact
   ↓
GitOps state
   ↓
Flux
   ↓
cluster
```

Coding agents SHALL NOT directly deploy production.

---

## 4.3 Models propose; deterministic systems decide

Models MAY:

- reason;
- research;
- specify;
- write tests;
- implement;
- review;
- inspect failures;
- suggest infrastructure changes.

Deterministic mechanisms SHALL decide wherever objective verification exists:

- compilation;
- tests;
- lint;
- schemas;
- manifests;
- policy;
- image builds;
- signatures;
- health checks;
- deployment readiness.

A model saying that something should work is not evidence that it works.

---

## 4.4 Test-first is an organizational invariant

For behavior-changing implementation work that can reasonably be tested, Strange Company SHALL enforce:

```text
SPECIFIED
   ↓
TEST_AUTHORING
   ↓
RED_VERIFYING
   ↓
RED_VERIFIED
   ↓
IMPLEMENTING
   ↓
GREEN_VERIFYING
   ↓
GREEN_VERIFIED
   ↓
REVIEW
```

An instruction in `AGENTS.md` is not sufficient enforcement.

The lifecycle SHALL be enforced by durable task state, Agent Runner policy, Git provenance, and trusted CI.

---

## 4.5 Workers are ephemeral

Workers SHALL normally be short-lived Kubernetes Jobs.

A worker:

```text
is created
  ↓
receives one bounded task
  ↓
receives relevant context
  ↓
clones repository
  ↓
performs one role
  ↓
produces durable artifacts
  ↓
reports structured result
  ↓
exits
  ↓
is garbage-collected
```

Workers SHALL NOT become long-lived pets.

---

## 4.6 Hermes is management

Hermes is the persistent orchestration and human-facing reasoning layer.

Hermes:

- receives human intent;
- decomposes work;
- constructs task context;
- chooses roles;
- chooses approved model/harness profiles;
- dispatches workers;
- consumes results and handoffs;
- initiates review;
- manages retries and escalation;
- reports project state.

Hermes SHALL NOT possess general-purpose Kubernetes workload creation privileges.

---

## 4.7 Rules live in files

Important policy SHALL be version-controlled.

This includes:

- validation policy;
- TDD requirements;
- exemption policy;
- retry limits;
- model routing;
- budget limits;
- resource profiles;
- protected paths;
- branch policy;
- merge policy;
- context rules;
- security requirements.

Prompt text MAY reinforce policy.

Prompt text SHALL NOT be the sole enforcement mechanism.

---

## 4.8 Retries are bounded

Autonomous work SHALL NOT retry indefinitely.

Failures SHALL produce evidence.

Subsequent attempts SHOULD use that evidence.

Exhaustion leads to an explicit state such as:

```text
Needs Human
```

---

## 4.9 Authority decreases near production

Creative freedom is broadest where mistakes are cheap and reversible.

Authority narrows as work approaches production:

```text
Hermes
   ↓
creative workers
   ↓
Git proposal
   ↓
trusted CI
   ↓
review / merge policy
   ↓
GitOps
   ↓
Flux
```

---

# 5. Canonical repository

There SHALL be one primary repository:

```text
strange-company
```

It is simultaneously:

- the bootstrap repository;
- infrastructure source;
- GitOps source;
- Strange Company application source;
- canonical specification;
- recovery seed;
- operational knowledge base.

There SHALL NOT be a separate bootstrap repository.

---

# 6. Repository hosting lifecycle

Before Strange Company exists:

```text
GitHub/strange-company
        │
        ▼
      clone
```

During bootstrap:

```text
local strange-company
        │
        ├── provision infrastructure
        ├── create k3s
        ├── seed platform
        ▼
Forgejo becomes available
        │
        ▼
Forgejo/strange-company created
        │
        ▼
repository pushed
        │
        ▼
Flux bootstrapped against Forgejo
```

After bootstrap:

```text
Forgejo/strange-company
        │
        ├── primary forge
        ├── developer origin
        └── Flux source
        │
        │ continuous mirror
        ▼
GitHub/strange-company
        │
        └── off-cluster recovery seed
```

Forgejo is primary while the platform is alive.

GitHub exists so the platform can be resurrected when Forgejo is not.

---

# 7. Operator experience

Target installation:

```bash
git clone https://github.com/<owner>/strange-company.git
cd strange-company

cp bootstrap.env.example bootstrap.env
$EDITOR bootstrap.env

./bootstrap --profile self-hosting-minimum
```

Local prerequisites SHOULD be limited to:

- Git;
- Docker or Podman;
- SSH key material;
- infrastructure-provider credentials;
- DNS/tunnel credentials;
- S3-compatible object-storage credentials;
- desired model API credentials.

The operator SHALL NOT need to manually install:

- OpenTofu;
- Ansible;
- kubectl;
- Helm;
- Flux CLI;
- SOPS;
- age;
- jq;
- yq.

---

# 8. Bootstrap CLI

The repository SHALL eventually expose approximately:

```text
./bootstrap
./bootstrap plan
./bootstrap status
./bootstrap verify
./bootstrap expand --profile small-ha
./bootstrap destroy
./bootstrap recover
```

Normal pipeline:

```text
preflight
   ↓
plan
   ↓
provision
   ↓
configure
   ↓
seed
   ↓
forge-init
   ↓
flux-bootstrap
   ↓
verify
   ↓
control-transfer
```

Bootstrap SHALL be:

- idempotent;
- safely resumable;
- observable;
- conservative around destructive operations.

---

# 9. Bootstrap toolbox

Bootstrap tooling SHALL execute from a version-pinned toolbox image.

Expected tools include:

```text
OpenTofu
Ansible
kubectl
Helm
Flux CLI
SOPS
age
SSH
curl
jq
yq
```

Tool versions SHALL be version-controlled.

---

# 10. Infrastructure profiles

Two canonical profiles exist:

```text
self-hosting-minimum
small-ha
```

`self-hosting-minimum` optimizes for inexpensive entry into self-hosting.

`small-ha` is the initial resilient production baseline.

---

# 11. Self-hosting minimum substrate

Canonical logical manifest:

```yaml
apiVersion: strange.company/v1alpha1
kind: BootstrapSubstrate

metadata:
  name: self-hosting-minimum

spec:
  nodes:
    count: 1

    minimumPerNode:
      cpu: 4
      memory: 8Gi

      storage:
        size: 100Gi
        class: ssd

    preferredPerNode:
      cpu: 4
      memory: 12Gi

      storage:
        size: 200Gi
        class: nvme

  kubernetes:
    distribution: k3s
    servers: 1
    datastore: embedded-etcd

  execution:
    maximumConcurrentAgents: 1
    maximumConcurrentCIJobs: 1

  storage:
    cnpg:
      instances: 1
      storageClass: local

    longhorn:
      enabled: true
      replicas: 1

    objectStorage:
      required: true
```

Provider implementations MAY choose a larger machine.

They SHOULD NOT silently choose a smaller one.

---

# 12. Small-HA profile

Canonical resilient target:

```yaml
apiVersion: strange.company/v1alpha1
kind: ClusterProfile

metadata:
  name: small-ha

spec:
  nodes:
    count: 3

    minimumPerNode:
      cpu: 4
      memory: 8Gi

      storage:
        size: 100Gi
        class: ssd

    preferredPerNode:
      storage:
        size: 200Gi
        class: nvme

  kubernetes:
    servers: 3
    datastore: embedded-etcd

  storage:
    cnpg:
      instances: 3

    longhorn:
      replicas: 2

  execution:
    maximumConcurrentAgents: 2
```

The one-node system is reduced redundancy, not a separate architecture.

Expansion SHALL not require rebuilding Strange Company from scratch.

---

# 13. Providers

Infrastructure providers SHALL exist behind a common contract.

Initial providers:

```text
ovh
existing
```

`existing` accepts already-provisioned SSH-accessible nodes.

Potential future implementations:

```text
Hetzner
Proxmox
VMware
other VPS providers
bare metal
```

All providers expose normalized node information.

Higher layers SHALL not depend on OVH-specific concepts.

---

# 14. Paid-resource guardrails

Before creating paid infrastructure, bootstrap SHALL validate:

- configuration;
- credentials;
- requested products;
- node count;
- location/region;
- SSH material;
- expected cost.

Configuration SHALL support:

```text
expected node count
expected machine class
maximum expected monthly infrastructure cost
```

Unexpected paid-resource creation SHALL fail closed.

The intended paid footprint SHALL be printed before first creation.

---

# 15. OpenTofu state

OpenTofu state SHALL survive loss of the machine executing bootstrap.

Temporary local state MAY exist only until remote state can be initialized.

Persistent state SHALL live outside the Kubernetes cluster in S3-compatible object storage.

Requirements:

- versioning;
- locking;
- encryption;
- restricted credentials;
- recovery procedure.

State encryption material is black-start material.

Recovery of OpenTofu state SHALL NOT require the Kubernetes cluster being recovered.

---

# 16. Kubernetes

Kubernetes distribution:

```text
k3s
```

The initial single node SHALL use embedded etcd from the beginning.

```text
node-01
  ├── k3s server
  ├── control plane
  ├── etcd member
  └── worker
```

Expansion:

```text
1 etcd member
   ↓
add node 2
   ↓
add node 3
   ↓
3-member quorum
```

Critical k3s server configuration SHALL remain consistent across control-plane nodes.

---

# 17. Node baseline

Stage 0 SHALL define:

- supported Linux distributions;
- update policy;
- reboot policy;
- time synchronization;
- SSH configuration;
- firewall policy;
- swap behavior;
- sysctls;
- filesystem requirements;
- container runtime assumptions;
- log rotation;
- disk-pressure behavior.

Nodes SHALL expose only required services.

---

# 18. Networking

Network-policy enforcement SHALL remain enabled.

Inter-node pod networking over public VPS infrastructure SHOULD be encrypted.

A simple native k3s/Flannel WireGuard configuration is preferred unless Stage 0 identifies a concrete reason not to use it.

Agent namespaces SHALL be protected by explicit NetworkPolicy.

Infrastructure control interfaces SHALL not be exposed publicly without justification.

---

# 19. Logical namespaces

Expected namespaces include:

```text
flux-system
cert-manager
cnpg-system
longhorn-system
authentik
forgejo
woodpecker
monitoring
strange-company
agent-jobs
ingress
apps
```

Stage 0 MAY refine exact names.

Agent workloads SHALL remain separated from platform infrastructure.

---

# 20. Platform components

Reference stack:

| Responsibility | Component |
|---|---|
| Kubernetes | k3s |
| Identity | Authentik |
| Certificates | cert-manager |
| Forge | Forgejo |
| PostgreSQL operator | CloudNativePG |
| Generic replicated PVC | Longhorn V1 |
| CI | Woodpecker |
| GitOps | Flux |
| Ingress | Traefik |
| External ingress | Cloudflare Tunnel |
| Metrics | Prometheus |
| Dashboards | Grafana |
| Telemetry transport | OpenTelemetry Collector |
| Logs at HA stage | Loki |
| Traces at HA stage | Tempo |
| Orchestration | Hermes |
| Worker control | Agent Runner |
| Worker execution | Kubernetes Jobs |
| Secret encryption | SOPS + age |
| External storage | S3-compatible object storage |

No Bitnami dependencies.

---

# 21. Identity

Authentik SHALL provide primary human identity.

Prefer native OIDC/OAuth integration wherever supported.

Expected consumers include:

- Forgejo;
- Grafana;
- Hermes UI;
- future internal applications.

Applications without satisfactory native SSO MAY use Authentik-backed forward authentication through ingress.

Human and machine identity SHALL remain separate.

Automation SHALL use scoped machine credentials.

---

# 22. Break-glass access

Failure of Authentik MUST NOT make Authentik unrecoverable.

Stage 0 SHALL define controlled break-glass access for at least:

- cluster administration;
- Authentik;
- Forgejo;
- Grafana;
- bootstrap infrastructure.

Break-glass credentials:

- are not used ordinarily;
- live outside the cluster;
- are documented;
- SHOULD be periodically tested;
- SHOULD produce audit evidence when used where practical.

---

# 23. TLS and certificates

Certificate lifecycle SHALL be explicit.

Cloudflare Tunnel MAY terminate public-facing TLS in the normal bootstrap path.

The architecture SHALL nevertheless define:

- internal TLS where needed;
- direct-ingress mode;
- certificate renewal;
- behavior without Cloudflare;
- failure recovery.

cert-manager is the default Kubernetes certificate automation mechanism.

---

# 24. Storage philosophy

There SHALL NOT be one universal storage mechanism.

Each type of state SHALL use the system most appropriate to replicate and recover it.

---

# 25. CloudNativePG

PostgreSQL SHALL use CloudNativePG.

CNPG `PGDATA` SHALL normally use node-local persistent storage.

Final topology:

```text
node-01          node-02          node-03

postgres-1       postgres-2       postgres-3
local disk       local disk       local disk

         PostgreSQL replication
```

Do not normally put Longhorn beneath replicated CNPG `PGDATA`.

Bootstrap:

```text
instances: 1
```

Small HA:

```text
instances: 3
```

Expansion SHALL increase CNPG instance count rather than migrate database technologies.

---

# 26. PostgreSQL disaster recovery

CNPG SHALL back up externally to S3-compatible storage.

Required capabilities:

- base backups;
- WAL archival;
- point-in-time recovery;
- repeatable restore test.

Replication is not backup.

---

# 27. Longhorn

Longhorn V1 provides replicated persistent block storage for applications that cannot replicate themselves.

Bootstrap:

```text
replicas: 1
```

Small HA:

```text
replicas: 2
```

Appropriate use includes:

- Forgejo repository storage;
- application upload directories;
- ordinary stateful apps.

Longhorn SHOULD NOT normally store:

- CNPG `PGDATA`;
- agent workspaces;
- CI scratch space;
- reconstructible caches.

---

# 28. Ephemeral workspaces

Workers SHALL normally use `emptyDir` for:

- clones;
- build trees;
- temporary outputs.

CI SHOULD use disposable workspaces where practical.

Durable code output belongs in Git.

---

# 29. Object storage

External S3-compatible storage is required at bootstrap.

Uses include:

- CNPG backups;
- WAL;
- Longhorn backups;
- Forgejo bulk objects where appropriate;
- repository backups;
- CI artifacts;
- OpenTofu state;
- Loki/Tempo storage when enabled.

The cluster SHALL not be the sole failure domain for critical backup data.

---

# 30. Forgejo

Forgejo is the engineering ledger.

It owns:

- repositories;
- branches;
- commits;
- PRs;
- issues;
- review history;
- releases;
- supported package/OCI metadata.

Forgejo relational state SHALL use CNPG.

Git repository storage SHALL use Longhorn.

Large suitable blobs SHOULD use object storage.

---

# 31. Repository-level backup

Storage-layer backup alone is insufficient.

Important repositories SHALL have repository-level offsite protection.

Potential mechanisms:

- remote Git mirrors;
- `git bundle`;
- Forgejo repository export;
- object-storage archives.

`strange-company` SHALL continuously mirror to GitHub.

Recovery of a single project SHOULD not require restoring an entire Longhorn volume if avoidable.

---

# 32. GitHub mirror

The `strange-company` repository SHALL continuously mirror from Forgejo to GitHub.

GitHub exists for:

- black start;
- bootstrap;
- external source backup;
- total-cluster-loss recovery.

Flux SHALL normally source Forgejo after bootstrap.

Mirror failure SHALL be visible.

Forgejo operation SHALL not depend upon GitHub being available.

---

# 33. Flux

Flux is the normal deployment authority.

After bootstrap:

```text
Flux
  ↓
Forgejo/strange-company
```

Normal production flow:

```text
PR
 ↓
CI
 ↓
merge
 ↓
Git desired state
 ↓
Flux
 ↓
cluster
```

CI SHALL not normally perform direct production deployment.

---

# 34. Bootstrap chicken-and-egg

Flux cannot source Forgejo before Forgejo exists.

Therefore bootstrap SHALL include a minimal seed phase.

Likely seed dependencies include:

- Kubernetes prerequisites;
- CNPG;
- Longhorn;
- Forgejo database;
- Forgejo;
- required ingress/tunnel pieces.

Stage 0 SHALL determine exact ordering.

Seeded resources MUST be installable such that Flux can assume management without duplicate-resource chaos.

Seed-to-Flux control transfer SHALL have an integration test.

---

# 35. Control transfer

Bootstrap is complete only when:

- Forgejo is healthy;
- `strange-company` exists there;
- repository push succeeds;
- Flux sources Forgejo;
- Flux reconciles successfully;
- seeded releases are represented in normal GitOps state;
- platform verification passes;
- GitHub mirror works.

The bootstrap program SHALL then report:

```text
CONTROL TRANSFER COMPLETE
```

Routine platform management occurs through Git after this point.

---

# 36. Woodpecker

Woodpecker SHALL provide trusted CI.

Expected responsibilities include:

- formatting;
- lint;
- unit tests;
- integration tests;
- TDD red/green verification;
- schema validation;
- Helm validation;
- Kubernetes validation;
- security checks;
- image builds;
- SBOM generation;
- release artifacts.

Agent-local validation is advisory.

Trusted CI determines whether required deterministic gates have passed.

---

# 37. Software supply chain

The build pipeline SHALL maintain traceability from runtime artifact to originating work.

Stage 0 SHALL plan:

- pinned production dependencies where practical;
- immutable production image references;
- vulnerability scanning;
- SBOM generation;
- build provenance;
- image/artifact signing where practical;
- signature verification policy;
- automated dependency-update PRs.

Desired trace:

```text
running artifact digest
   ↓
trusted CI build
   ↓
source commit
   ↓
pull request
   ↓
task
   ↓
acceptance criteria
```

Stochastic workers SHALL not receive release-signing keys.

---

# 38. Build security

Workers SHALL NOT receive:

- Docker socket;
- containerd socket;
- privileged DinD;
- production registry-push credentials;
- deployment credentials.

Trusted CI owns trusted artifact creation.

---

# 39. Strange Company control plane

The application layer includes:

- Hermes integration;
- Agent Runner;
- durable task state;
- TDD/verification state;
- context compilation;
- Forgejo integration;
- Kubernetes Job management;
- policy evaluation;
- model routing;
- cost accounting;
- budget enforcement;
- handoff management;
- telemetry.

Relational application state SHALL use CNPG.

---

# 40. Context hierarchy

Context SHALL be layered:

```text
Company Context
      ↓
Repository Context
      ↓
Task Context
      ↓
Worker Context
      ↓
Handoff Context
```

Workers receive the smallest useful projection for their role.

---

# 41. Company context

Company context contains durable organization-wide rules.

Examples:

- Git is authoritative;
- verification precedes implementation;
- workers do not deploy;
- deterministic evidence outranks agent claims;
- retries are bounded;
- protected branches are protected;
- cheap capable models should receive sufficiently bounded work.

Company context SHALL live in Git.

---

# 42. Repository context

Every managed repository SHALL support durable repository context.

Canonical structure:

```text
AGENTS.md

.strange-company/
├── repo.yaml
├── architecture.md
├── policy.yaml
├── validation.yaml
├── decisions/
└── roles/
```

Repository context MAY include:

- project purpose;
- architecture;
- important paths;
- build commands;
- test commands;
- coding conventions;
- compatibility constraints;
- known sharp edges;
- deployment model;
- prohibited actions;
- significant decisions.

Git context is authoritative.

`AGENTS.md` MAY instruct workers.

It SHALL NOT substitute for enforceable platform policy.

---

# 43. Learned context

Hermes MAY retain supplementary learned context such as:

- recurring test behavior;
- recent observations;
- recurring failure modes;
- undocumented but observed constraints.

Learned context SHALL not silently override Git.

Important learned knowledge SHOULD become a proposed repository-context change.

---

# 44. Task context

Every task SHALL include at least:

- objective;
- source;
- repository;
- acceptance criteria;
- exact base revision;
- constraints;
- relevant context references;
- required verification mode.

---

# 45. Acceptance criteria

Behavior-changing tasks SHALL define explicit acceptance criteria before test authoring.

Acceptance criteria SHOULD have stable IDs.

Example:

```yaml
acceptanceCriteria:
  - id: AC-1
    text: HTTP 307 preserves the original method.

  - id: AC-2
    text: HTTP 307 preserves the request body.

  - id: AC-3
    text: Existing HTTP 302 behavior remains unchanged.
```

Tests SHOULD map back to acceptance criteria.

---

# 46. Verification modes

Every work item SHALL have a machine-visible verification mode.

Examples:

```yaml
verification:
  mode: test-first
```

or:

```yaml
verification:
  mode: verification-first
  exemption: documentation-only
```

The normal mode for behavior-changing software is:

```text
test-first
```

Workers SHALL NOT freely exempt their own tasks.

Allowed exemption classes SHALL be version-controlled policy.

---

# 47. Test-driven development

For behavior-changing software, the acceptance mechanism SHALL precede implementation.

Canonical lifecycle:

```text
Human intent
   ↓
SPECIFIED
   ↓
TEST_AUTHORING
   ↓
RED_VERIFYING
   ↓
RED_VERIFIED
   ↓
IMPLEMENTING
   ↓
GREEN_VERIFYING
   ↓
GREEN_VERIFIED
   ↓
REVIEW
   ↓
DONE
```

---

# 48. Test worker

A separate test-authoring execution SHALL normally precede implementation.

The tester receives:

- task specification;
- acceptance criteria;
- repository context;
- relevant current behavior.

The tester SHALL NOT receive a proposed implementation.

The tester's role is to turn desired behavior into executable evidence.

The test worker and implementation worker MAY use the same underlying model family, but they SHALL be independent execution contexts.

---

# 49. Red commit

The test worker SHALL create a dedicated pre-implementation test commit.

Example:

```text
test(sc-184): specify 307 redirect behavior
```

The commit SHOULD contain only:

- tests;
- fixtures;
- minimal test infrastructure;
- verification artifacts.

It SHALL NOT contain the requested implementation.

---

# 50. Red verification

Trusted CI SHALL run the new tests against the pre-implementation code.

Implementation SHALL remain blocked until trusted CI records:

```text
RED_VERIFIED
```

Valid red verification means:

1. the relevant test runs;
2. at least one new requirement-specific assertion fails;
3. the failure corresponds to absent or incorrect behavior;
4. the failure is not merely broken test infrastructure, syntax error, missing dependency, or a deliberately impossible test.

A test that merely fails to compile is not automatically useful RED evidence.

---

# 51. Already-satisfied behavior

If the new test passes before implementation, Strange Company SHALL NOT manufacture an artificial failure.

The task transitions to investigation.

Possible causes:

- behavior already exists;
- requirement is redundant;
- test is insufficient;
- specification is wrong.

A state equivalent to:

```text
ALREADY_SATISFIED
```

or:

```text
NEEDS_INVESTIGATION
```

SHALL be represented.

---

# 52. Implementation gate

The normal implementation worker SHALL not be dispatched until:

```text
test commit exists
AND
RED_VERIFIED
```

Implementation SHALL begin from the red test commit or a descendant containing it.

The Git topology therefore proves ordering:

```text
BASE
  ↓
RED TEST COMMIT
  ↓
implementation commit(s)
  ↓
GREEN
```

---

# 53. Acceptance-test protection

The implementation worker SHALL NOT freely weaken the tests defining the task.

It SHALL NOT satisfy work by:

- deleting acceptance tests;
- skipping them;
- weakening assertions;
- changing expected values to match implementation;
- bypassing test discovery;
- substituting unconditional mocks that eliminate tested behavior.

If implementation reveals an invalid test, the task SHALL return an explicit condition equivalent to:

```text
TEST_SPEC_CONFLICT
```

Changing the acceptance test then requires a distinct reviewable action.

---

# 54. Green verification

Trusted CI SHALL rerun:

1. tests introduced for the task;
2. relevant existing tests;
3. all repository-required validation.

The task reaches:

```text
GREEN_VERIFIED
```

only when required deterministic validation succeeds.

---

# 55. Review gate

Review follows green verification.

The reviewer SHALL receive:

- original task;
- acceptance criteria;
- red commit;
- red CI evidence;
- implementation diff;
- green CI evidence;
- implementation handoff.

The reviewer SHALL examine whether the implementation passed legitimately rather than bypassing the verification.

---

# 56. TDD provenance

Task state SHALL retain equivalent fields to:

```yaml
tdd:
  required: true

  test_execution:
    id: ...

  test_commit: abc123

  red_verification:
    pipeline: ...
    result: expected_failure

  implementation_execution:
    id: ...

  implementation_commit: def456

  green_verification:
    pipeline: ...
    result: success
```

Final Git history MAY be squashed.

TDD provenance SHALL remain durable in Strange Company task history even if commits are later squashed.

---

# 57. Verification-first work

Not all work supports literal executable TDD.

Examples include:

- documentation;
- research;
- certain infrastructure changes;
- migrations;
- benchmarks;
- security policy;
- metadata changes.

The generalized invariant remains:

> **Define the judge before producing the answer.**

Examples:

```text
software feature
  → failing executable test first

bug fix
  → failing regression test first

infrastructure change
  → validation scenario / invariant first

performance optimization
  → benchmark and threshold first

migration
  → pre/post invariants first

security change
  → security assertion/test first

documentation
  → rubric / required facts first
```

---

# 58. TDD enforcement layers

TDD SHALL be enforced through multiple independent mechanisms:

```text
task state machine
        +
Agent Runner policy
        +
Git provenance
        +
trusted CI
        +
merge policy
```

An instruction to the agent is supplemental, not enforcement.

---

# 59. Agent Runner

Agent Runner is the trusted deterministic boundary between Hermes and Kubernetes execution.

```text
Hermes
   │
   │ narrow MCP
   ▼
Agent Runner
   │
   ├── task state machine
   ├── TDD/verification gate
   ├── policy
   ├── budget checks
   ├── Forgejo adapter
   └── Kubernetes adapter
            │
            ▼
           Job
```

Hermes SHALL NOT have general Kubernetes Job creation privileges.

---

# 60. Hermes-facing Runner surface

Initial MCP operations:

```text
agent_start
agent_status
agent_logs
agent_result
agent_cancel
```

An optional `agent_list` MAY be added.

The exposed interface SHALL remain substantially narrower than the Runner's administrative capabilities.

---

# 61. Internal Runner API

Core Runner behavior SHALL not be inseparably tied to MCP transport.

Stage 0 SHALL define an internal application interface, likely equivalent to:

```text
POST /tasks
GET  /tasks/:id
POST /tasks/:id/cancel
GET  /tasks/:id/logs
GET  /tasks/:id/result
```

Exact schemas are Stage-0 outputs.

---

# 62. Agent start request

Hermes may logically specify:

```json
{
  "repository": "projects/example",
  "base_ref": "main",
  "task": "Implement issue 184.",
  "role": "implementer",
  "harness": "claude",
  "profile": "implementation",
  "acceptance_criteria": [
    "AC-1",
    "AC-2"
  ]
}
```

Hermes SHALL NOT specify arbitrary:

- image;
- namespace;
- ServiceAccount;
- Secrets;
- volumes;
- host paths;
- privileged mode;
- arbitrary security context;
- arbitrary Kubernetes YAML;
- arbitrary resource values.

Trusted policy resolves those details.

---

# 63. Harness registry

Agent Runner SHALL maintain an allowlisted harness registry.

Conceptually:

```yaml
harnesses:

  claude:
    image: <digest-pinned-image>
    credentialClass: anthropic

  codex:
    image: <digest-pinned-image>
    credentialClass: openai

  opencode:
    image: <digest-pinned-image>
    credentialClass: configurable

  fake:
    image: <digest-pinned-image>
```

Harness and model are separate concepts.

Production harness images SHALL be digest-pinned.

---

# 64. Initial harnesses

The architecture SHOULD ultimately support:

- Claude Code;
- Codex;
- a provider-neutral/OpenCode-style harness;
- deterministic fake harnesses.

The Self-Hosting Threshold requires at least one real coding harness.

No one vendor's coding harness SHALL define the abstract concept of an agent.

---

# 65. Worker roles

Initial roles SHOULD include:

```text
researcher
planner
tester
implementer
reviewer
```

Role determines:

- instructions;
- context projection;
- allowed actions;
- expected output;
- default model tier;
- verification expectations.

Role and harness are independent.

---

# 66. Worker context

Hermes SHALL construct role-specific context packets.

A tester may receive:

- requirement;
- acceptance criteria;
- architecture;
- current repository behavior;
- relevant validation rules.

An implementer receives:

- requirement;
- acceptance criteria;
- red test commit;
- red evidence;
- relevant architecture;
- implementation constraints.

A reviewer receives:

- requirement;
- red evidence;
- implementation diff;
- green evidence;
- handoffs.

Workers SHALL not receive unrestricted Hermes history by default.

---

# 67. Worker lifecycle

Every coding harness SHALL implement a common conceptual lifecycle:

```text
start
 ↓
retrieve task
 ↓
obtain scoped credentials
 ↓
clone repository
 ↓
checkout exact required commit
 ↓
create agent/<task-id>
 ↓
load context/policy
 ↓
perform bounded role
 ↓
run allowed local validation
 ↓
commit
 ↓
push
 ↓
report structured result
 ↓
exit
```

Agent Runner SHOULD create PRs after validating worker output.

---

# 68. Worker result

Conceptual result:

```json
{
  "task_id": "sc-184",
  "state": "completed",
  "repository": "strange-company",
  "role": "implementer",
  "branch": "agent/sc-184",
  "base_commit": "...",
  "commit": "...",
  "summary": "...",
  "validation": {
    "command": "...",
    "exit_code": 0
  },
  "handoff": {
    "what_i_did": "...",
    "what_remains": "...",
    "recommended_next_role": null,
    "questions": [],
    "known_risks": []
  }
}
```

Exact schema SHALL be defined during Stage 0.

---

# 69. Handoffs

Every worker result SHALL support a structured handoff.

Minimum conceptual fields:

```yaml
what_i_did:
what_remains:
recommended_next_role:
questions:
known_risks:
```

Handoffs are part of MVP.

---

# 70. Agent collaboration

Direct live worker-to-worker chat is NOT required for MVP.

Workers collaborate through durable artifacts:

- Git;
- test commits;
- implementation commits;
- PRs;
- task state;
- structured results;
- handoffs;
- review comments;
- Hermes.

Example:

```text
implementer
   ↓
TEST_SPEC_CONFLICT
   ↓
Hermes
   ↓
tester/reviewer
   ↓
corrected test decision
   ↓
Hermes
   ↓
new implementation attempt
```

---

# 71. Human-facing task states

Canonical project states:

```text
Backlog
Ready
In Progress
Review
Done
Needs Human
Blocked
```

These MAY remain the simple human-facing workflow.

---

# 72. Machine execution states

Execution requires more detail.

Expected states include:

```text
Queued
Starting
Running
Succeeded
Failed
Cancelled
Timed Out
```

Verification workflow SHALL additionally represent states equivalent to:

```text
Specified
Test Authoring
Red Verifying
Red Verified
Test Needs Work
Already Satisfied
Test Spec Conflict
Implementing
Green Verifying
Green Verified
Review
```

Stage 0 SHALL define the authoritative state machine.

---

# 73. Task record

Durable task state SHALL include equivalent information to:

```text
id
title
repository
source
acceptance criteria
verification mode
state
claimed_by
parent_task
role
harness
model
profile
attempt
maximum attempts
test execution
red commit
red evidence
implementation execution
implementation commit
green evidence
branch
PR
artifacts
test results
handoff
created_at
updated_at
history
```

Every transition SHALL record:

```text
timestamp
actor
old state
new state
reason
```

Claims SHALL be atomic.

---

# 74. Retry policy

Retries are bounded.

A generic overall ceiling of approximately five autonomous attempts is acceptable unless specific policy says otherwise.

Retries SHALL incorporate actual evidence.

A failing cheap implementation MAY escalate to a more capable model.

Repeated deterministic failure SHALL not simply replay identical work.

Exhaustion leads to:

```text
Needs Human
```

---

# 75. Model capability tiers

Model names SHALL not be architectural constants.

Routing SHALL support logical capability tiers such as:

```text
clerical
bargain
standard
frontier
```

Providers MAY include:

- Anthropic;
- OpenAI;
- DeepSeek;
- Z.ai/GLM;
- Google;
- other suitable APIs.

---

# 76. Intelligence allocation

Conceptually:

```text
ambiguity
   ↓
frontier reasoning

clear specification
   ↓
strong standard engineering model

bounded implementation
   ↓
cheap capable model

verification
   ↓
deterministic systems
```

Test/spec creation MAY warrant a stronger model than implementation because a correct test can make implementation mechanical.

---

# 77. Subscription independence

Strange Company SHALL NOT require consumer AI subscriptions.

Model use SHOULD rely on automatable API credentials or equivalent machine-accessible accounts.

Human subscriptions MAY exist independently but SHALL not be infrastructure dependencies.

---

# 78. Exact model accounting

Every billable model invocation SHALL be attributable to:

- provider;
- model;
- harness;
- role;
- worker execution;
- attempt;
- task;
- PR where applicable;
- repository;
- project.

The smallest useful accounting unit is one provider request.

---

# 79. Invocation ledger

Where provider data permits, every invocation SHALL retain equivalent fields to:

```yaml
task_id:
execution_id:
attempt:
worker_id:

repository:
role:
harness:

provider:
model:
provider_request_id:

started_at:
finished_at:
latency_ms:

usage:
  input_tokens:
  cached_input_tokens:
  cache_write_tokens:
  output_tokens:
  reasoning_tokens:

pricing:
  currency: USD
  input_per_million:
  cached_input_per_million:
  cache_write_per_million:
  output_per_million:
  pricing_effective_at:
  pricing_version:

cost:
  input_usd:
  cache_usd:
  output_usd:
  total_usd:

outcome:
  provider_success:
  worker_success:
  red_verified:
  green_verified:
  ci_passed:
  review_accepted:
  human_accepted:
```

Historical pricing data SHALL be retained so old cost reports remain reproducible after provider price changes.

---

# 80. Cost aggregation

Accounting SHALL support:

```text
API invocation
    ↓
worker execution
    ↓
attempt
    ↓
TDD phase
    ↓
task
    ↓
PR
    ↓
repository
    ↓
project
    ↓
day / month
```

The system SHALL eventually answer:

- What did this PR cost?
- How much was spent authoring its tests?
- How much was spent implementing it?
- How much did review cost?
- Which model has the lowest cost per green task?
- How often does the bargain tier succeed?
- How much does escalation cost?
- Is Sonnet cheaper per accepted task than repeated cheaper-model attempts?
- Which harness performs best for this repository/language?
- How much money was spent on failed work?

---

# 81. Economic circuit breakers

Accounting SHALL support prevention, not merely reporting.

Policies SHALL support:

- maximum cost per provider request;
- maximum execution cost;
- maximum task cost;
- maximum attempts;
- maximum tokens;
- maximum wall time;
- daily provider budget;
- monthly provider budget;
- global monthly budget;
- emergency stop.

Budget exhaustion SHALL fail safely and produce visible human notification.

The platform SHALL provide a way to stop new autonomous spending without shutting down core services.

---

# 82. TDD economics

Cost accounting SHOULD distinguish:

```text
specification cost
test-authoring cost
red-verification cost
implementation cost
green-retry cost
review cost
```

This SHALL allow Strange Company to determine empirically whether verification-first work reduces overall cost.

---

# 83. Resource profiles

Hermes selects approved logical resource profiles rather than arbitrary Kubernetes resources.

Example:

```yaml
profiles:

  tiny:
    cpuLimit: "1"
    memoryLimit: 1Gi
    deadline: 10m

  tests:
    cpuLimit: "2"
    memoryLimit: 2Gi
    deadline: 20m

  implementation:
    cpuLimit: "2"
    memoryLimit: 4Gi
    deadline: 30m

  reasoning:
    cpuLimit: "2"
    memoryLimit: 4Gi
    deadline: 45m
```

Exact values are tunable policy.

Agent Runner owns the actual Kubernetes Job manifest.

---

# 84. Worker security

Worker Jobs SHALL normally:

- run non-root;
- disable privilege escalation;
- drop Linux capabilities;
- use RuntimeDefault seccomp;
- disable ServiceAccount token automount;
- avoid hostPath;
- avoid privileged mode;
- comply with Restricted Pod Security.

Worker ServiceAccounts SHOULD possess zero Kubernetes API rights.

---

# 85. Admission defense in depth

Runner validation SHALL not be the sole defense.

Kubernetes itself SHOULD independently enforce worker restrictions using:

- Pod Security Admission;
- RBAC;
- NetworkPolicy;
- native ValidatingAdmissionPolicy/CEL where practical.

Agent workloads SHALL be prevented from arbitrarily:

- becoming privileged;
- mounting hosts;
- selecting privileged service accounts;
- entering infrastructure namespaces;
- bypassing required security context.

---

# 86. Worker credentials

Workers receive only what is needed for their specific task.

Typical credentials:

```text
scoped Forge credential
specific model credential
task-specific Runner token
```

Workers SHALL NOT receive:

```text
cluster-admin
Flux credentials
CNPG admin
Longhorn admin
release-signing credentials
unrelated production secrets
```

---

# 87. Forge credentials

Workers SHOULD only be able to:

- clone approved repositories;
- push `agent/*` branches.

Agent Runner SHOULD own stronger Forgejo authority needed to:

- create PRs;
- update labels;
- post task metadata;
- report status.

Short-lived task-scoped credentials are preferred where practical.

---

# 88. Network policy

The agent environment SHALL default-deny unnecessary communication.

Workers normally need access only to:

- DNS;
- Forgejo;
- Agent Runner;
- approved model APIs;
- necessary package/source registries.

Workers SHALL NOT normally access:

- Kubernetes API;
- CNPG application databases;
- Longhorn management;
- Flux;
- production app databases.

Broader access requires explicit task/repository policy.

---

# 89. Resource protection

Infrastructure has higher scheduling priority than autonomous labor.

Conceptual priority:

```text
platform-critical
platform
application
CI
agent-worker
```

`agent-jobs` SHALL have:

- ResourceQuota;
- LimitRange;
- concurrency limit;
- per-job hard limits.

Bootstrap default:

```text
maximum agent workers: 1
maximum heavy CI jobs: 1
```

Small-HA initial default:

```text
maximum agent workers: 2
```

A slow worker is acceptable.

A worker OOM-killing etcd is not.

---

# 90. Observability

Observability has distinct responsibilities.

## Metrics

Prometheus.

## Exact accounting and task events

CNPG-backed Strange Company state.

## Distributed execution telemetry

OpenTelemetry.

Prometheus SHALL not be abused as a high-cardinality task ledger.

---

# 91. Prometheus

Prometheus is required by the Self-Hosting Threshold.

It SHOULD collect useful metrics from:

- nodes;
- Kubernetes;
- etcd;
- CNPG;
- Longhorn;
- Forgejo;
- Authentik;
- Flux;
- Woodpecker;
- Hermes;
- Agent Runner;
- workers;
- model-cost aggregates.

Task IDs and similarly unbounded identifiers SHOULD generally not become metric labels.

---

# 92. Grafana

Grafana is required by the Self-Hosting Threshold.

It SHALL provide dashboards for:

## Platform

- node state;
- CPU;
- memory;
- disk;
- etcd;
- CNPG;
- Longhorn;
- Forgejo;
- Authentik;
- Flux;
- Woodpecker.

## Strange Company

- queue depth;
- active workers;
- task duration;
- red/green transitions;
- retries;
- failures;
- `Needs Human`;
- TDD compliance.

## Economics

- spend today;
- spend this month;
- model/provider spend;
- harness spend;
- repo spend;
- cost per accepted task;
- cost per green task;
- cheap-tier success;
- escalation frequency;
- test-authoring versus implementation spend;
- estimated routing savings.

---

# 93. OpenTelemetry

Hermes, Agent Runner, and harnesses SHOULD emit OpenTelemetry-compatible telemetry.

Desired hierarchy:

```text
task
  └── worker execution
       ├── model invocation
       ├── tool action
       ├── test invocation
       └── model invocation
```

OTel complements but does not replace the exact accounting ledger.

---

# 94. Telemetry privacy

General telemetry SHALL NOT capture by default:

- full prompts;
- full completions;
- arbitrary source-code contents;
- environment variables;
- secret-bearing command output.

Safe default telemetry includes:

- provider;
- model;
- harness;
- task;
- repository;
- role;
- tokens;
- latency;
- cost;
- tool name;
- status;
- validation result.

Verbose content capture requires explicit debugging configuration and retention limits.

---

# 95. Loki and Tempo

Loki and Tempo are not required for the minimum bootstrap.

They SHOULD be added by or during `small-ha` unless measurements justify a different decision.

Persistent logs and traces SHOULD use object storage rather than filling VPS root disks.

---

# 96. Alerting

Dashboards alone are insufficient.

The system SHALL support alerts for at least:

- node failure;
- disk pressure;
- etcd degradation;
- CNPG failure;
- failed backups;
- failed restore tests;
- degraded Longhorn;
- Forgejo failure;
- Authentik failure;
- GitHub mirror failure;
- Flux failure;
- Woodpecker failure;
- Agent Runner failure;
- task exhaustion;
- TDD pipeline failure;
- budget thresholds;
- anomalous API spend.

---

# 97. Human notifications

Human-required conditions SHALL result in actual notifications.

At least one notification mechanism SHALL be configurable.

Possible transports:

- email;
- Matrix;
- ntfy;
- Slack;
- Discord;
- generic webhook.

Notifications SHALL include at least:

- `Needs Human`;
- budget stop;
- failed backup;
- critical platform outage;
- failed mirror;
- black-start-threatening condition.

---

# 98. Secrets

Git-resident Kubernetes secrets SHALL be encrypted using:

```text
SOPS
+
age
```

unless Stage 0 identifies a materially superior implementation with equivalent recovery properties.

The age identity is black-start material and SHALL live outside the cluster as well.

---

# 99. Secret ownership

Stage 0 SHALL identify for every secret class:

- creator;
- consumer;
- storage;
- rotation;
- recovery.

Secret classes include:

- k3s token;
- infrastructure credentials;
- OpenTofu state encryption;
- object-storage credentials;
- Authentik bootstrap admin;
- Forgejo admin;
- GitHub mirror token;
- Runner authentication;
- model API keys;
- SOPS/age;
- signing keys;
- break-glass credentials.

---

# 100. Forge workflow

Agent branches SHALL use a predictable convention:

```text
agent/<task-id>
```

Workers SHALL NOT push protected branches.

PRs SHOULD contain:

- task ID;
- acceptance criteria;
- test commit;
- RED evidence;
- implementation summary;
- GREEN evidence;
- known limitations;
- handoff.

---

# 101. Human work tracking

Forgejo Issues and/or Projects SHOULD initially provide the human-facing backlog.

Human states:

```text
Backlog
Ready
In Progress
Review
Done
Needs Human
Blocked
```

The Strange Company database is authoritative for machine execution state.

Forgejo is the durable collaborative record.

---

# 102. TDD merge enforcement

For a task marked `test-first`, merge policy SHALL verify equivalent facts to:

```text
acceptance criteria exist
test execution exists
test commit exists
red verification exists
red verification is valid
implementation descends from red test state
protected acceptance tests remain intact
green verification succeeds
required review succeeds
```

Missing TDD provenance SHALL be a merge-blocking condition unless an authorized exemption applies.

---

# 103. Fake workers

Deterministic fake harnesses SHALL include at least:

```text
fake-success
fake-failure
fake-timeout
fake-blocked
fake-push
fake-malformed-result
```

Additional TDD-specific fake scenarios SHOULD include:

```text
fake-red-valid
fake-red-broken-test
fake-already-satisfied
fake-test-spec-conflict
fake-green-after-retry
fake-test-tamper
```

These allow orchestration to be tested without spending model tokens.

---

# 104. CI without paid model calls

Default project CI SHALL NOT require paid model APIs.

Core orchestration, TDD policy, state transitions, security, and acceptance gates SHALL be testable deterministically.

Real-model smoke tests MAY exist separately.

---

# 105. Security acceptance tests

CI SHALL verify that workers cannot arbitrarily:

- run privileged;
- run as root where forbidden;
- mount hostPath;
- mount Kubernetes service tokens;
- choose arbitrary ServiceAccounts;
- choose arbitrary Secrets;
- choose arbitrary images;
- exceed resource profiles;
- enter infrastructure namespaces;
- bypass network policy;
- obtain signing/deployment credentials.

---

# 106. TDD acceptance tests

CI SHALL also test the organization itself.

At minimum:

```text
[ ] implementer cannot start before RED_VERIFIED
[ ] broken tests do not count as valid red evidence
[ ] already-passing test produces investigation state
[ ] implementation cannot silently delete acceptance tests
[ ] weakened acceptance tests are detected where policy permits
[ ] valid RED → implementation → GREEN path succeeds
[ ] TEST_SPEC_CONFLICT is supported
[ ] TDD provenance survives retry
[ ] exemption requires approved policy
```

---

# 107. Software retention and garbage collection

Retention SHALL be intentional.

Stage 0 SHALL define cleanup for:

- Prometheus data;
- Loki data;
- Tempo data;
- worker logs;
- model telemetry;
- exact task records;
- CI artifacts;
- OCI images;
- Forgejo packages;
- repository archives;
- Longhorn snapshots;
- CNPG backups;
- Git bundles;
- bootstrap scratch state.

Root disks SHALL not fill because cleanup policy was omitted.

Exact accounting records MAY be retained far longer than detailed traces.

---

# 108. Recovery objectives

Initial targets:

| Data/service | RPO target | RTO target |
|---|---:|---:|
| `strange-company` committed Git state | ≤1 hour, preferably near-zero mirror lag | ≤2 hours |
| OpenTofu state | latest successful mutation | ≤2 hours |
| Forgejo/Auth/Strange Company PostgreSQL | ≤15 minutes | ≤4 hours |
| project Git repositories | ≤1 hour | ≤4 hours |
| critical Longhorn data | ≤24 hours unless stricter | ≤8 hours |
| logs/traces | best effort / ≤24 hours | non-critical |
| historical Prometheus metrics | best effort | non-critical |

Stage 0 MAY tighten these.

Weakening them SHALL be surfaced explicitly.

---

# 109. Backup testing

A backup that has never been restored is unproven.

Repeatable restore testing SHALL exist for:

- CNPG;
- project repositories;
- important Longhorn data;
- OpenTofu state;
- `strange-company` GitHub black start.

Restore-test failure SHALL be visible and alertable.

---

# 110. etcd recovery

K3s etcd snapshots SHOULD be retained.

Important snapshots SHOULD be copied off-node.

GitOps remains canonical desired state, but etcd snapshots MAY accelerate recovery.

Stage 0 SHALL define when to restore etcd versus rebuild from source.

---

# 111. Disaster recovery

The entire Kubernetes cluster SHALL be recoverable after total loss.

Required off-cluster material includes:

```text
GitHub strange-company mirror
age identity
provider credentials
OpenTofu backend access
OpenTofu state-encryption material
DNS/tunnel credentials
object-storage credentials
break-glass credentials
documented recovery procedure
```

The old Forgejo installation SHALL not be required to learn how to recreate Forgejo.

---

# 112. Black start

Conceptual black start:

```text
clone strange-company from GitHub
    ↓
recover external secrets
    ↓
recover OpenTofu state
    ↓
provision/recover nodes
    ↓
install k3s
    ↓
seed core platform
    ↓
restore identity and Forgejo
    ↓
restore/reconnect repository state
    ↓
bootstrap Flux
    ↓
restore remaining state
    ↓
verify
    ↓
resume workers
```

The precise procedure SHALL exist before implementation begins.

---

# 113. Operational procedures

Stage 0 SHALL produce explicit procedures for at least:

```text
fresh installation
bootstrap preflight failure
partial bootstrap resume
seed-to-Flux handoff
failed Flux handoff
bootstrap verification
add node
remove node
one-node to three-node expansion
lost node
lost etcd member
etcd restore
CNPG primary loss
CNPG replica loss
CNPG PITR
Longhorn replica loss
Longhorn restore
Forgejo restore
Authentik restore
GitHub mirror failure
object-storage outage
OpenTofu state recovery
provider outage
certificate failure
rotate model credentials
rotate Forge credentials
rotate storage credentials
rotate age identity
rotate break-glass credentials
Woodpecker failure
Agent Runner failure
Hermes failure
monitoring failure
budget emergency stop
TDD pipeline recovery
stuck task recovery
total cluster loss
GitHub black start
safe destruction
```

Stage 0 SHALL add procedures discovered to be missing.

---

# 114. Destruction

`./bootstrap destroy` SHALL be guarded.

Destroying compute SHALL NOT automatically destroy:

- GitHub mirror;
- external backups;
- object-storage recovery data;
- recovery secrets;
- remote OpenTofu history.

Compute lifecycle and durable recovery lifecycle are distinct.

---

# 115. Strange Company Helm chart

Primary chart name:

```text
strange-company
```

The chart SHOULD install Strange Company-specific components rather than acting as an umbrella for the whole platform.

Likely chart scope:

- Agent Runner;
- Hermes configuration/integration;
- ServiceAccounts;
- RBAC;
- NetworkPolicies;
- ResourceQuota;
- LimitRange;
- worker policies;
- ConfigMaps;
- Services;
- monitoring integrations;
- optional ingress.

Platform dependencies SHOULD remain separately managed GitOps releases.

---

# 116. Chart quality

Every chart release SHALL eventually pass:

```text
helm lint
values schema validation
helm template
Kubernetes schema validation
unit tests
k3d installation
upgrade test
uninstall test
reinstall test
```

Critical images and dependencies SHALL be pinned.

---

# 117. Self-Hosting Threshold

Strange Company reaches its Self-Hosting Threshold when it can meaningfully continue building itself under its own rules.

Required platform:

- k3s;
- embedded etcd;
- CNPG;
- Longhorn;
- Authentik;
- Forgejo;
- GitHub mirror;
- Flux;
- Woodpecker;
- Prometheus;
- Grafana;
- OpenTelemetry Collector;
- Hermes;
- Agent Runner.

Required organizational capabilities:

- task state;
- acceptance criteria;
- repository context;
- structured handoffs;
- model accounting;
- budget controls;
- fake workers;
- one real coding harness;
- enforced TDD state machine.

---

# 118. First self-hosted task

The Self-Hosting Threshold SHALL be proven by a genuine test-first task against `strange-company`.

Recommended example:

> Add or improve one deterministic Agent Runner behavior.

Required flow:

```text
human requirement
   ↓
acceptance criteria
   ↓
tester worker
   ↓
RED test commit
   ↓
Woodpecker confirms meaningful failure
   ↓
RED_VERIFIED
   ↓
implementation worker
   ↓
GREEN verification
   ↓
review worker
   ↓
accepted PR
```

A system that can merely edit its own source does NOT yet satisfy the self-hosting definition.

It must be able to **test-first develop itself**.

---

# 119. Bootstrap implementation phases

Starting from only `SPEC.md`:

```text
PHASE 0
Complete engineering definition
external planning agent

        ↓

PHASE 1
repository structure
contracts
schemas
tests
bootstrap toolbox
fake workers
TDD state machine

        ↓

PHASE 2
one VPS
k3s
CNPG
Longhorn
Authentik
Forgejo
Flux
Woodpecker
Prometheus
Grafana
OTel

        ↓

PHASE 3
Agent Runner
Hermes
context
cost ledger
budget enforcement
verification-first orchestration
fake end-to-end flows

        ↓

PHASE 4
one real harness

        ↓

SELF-HOSTING THRESHOLD

        ↓

PHASE 5
Strange Company works on itself
using RED → GREEN → REVIEW

        ↓

PHASE 6
nodes 2 and 3
CNPG 1 → 3
Longhorn 1 → 2
etcd 1 → 3
Loki
Tempo
HA hardening

        ↓

SMALL-HA
```

---

# 120. Stage-0 repository structure

The planning agent SHALL produce a structured engineering definition approximately like:

```text
SPEC.md

plans/
├── 00-master-plan.md
├── 01-repository.md
├── 02-bootstrap-toolbox.md
├── 03-provider-interface.md
├── 04-ovh-provider.md
├── 05-existing-nodes.md
├── 06-opentofu-state.md
├── 07-k3s.md
├── 08-node-security.md
├── 09-networking.md
├── 10-tls-certificates.md
├── 11-storage.md
├── 12-cnpg.md
├── 13-longhorn.md
├── 14-object-storage.md
├── 15-authentik.md
├── 16-forgejo.md
├── 17-github-mirror.md
├── 18-flux.md
├── 19-seed-handoff.md
├── 20-woodpecker.md
├── 21-tdd-workflow.md
├── 22-verification-policy.md
├── 23-supply-chain.md
├── 24-hermes.md
├── 25-context.md
├── 26-agent-runner.md
├── 27-worker-contract.md
├── 28-harnesses.md
├── 29-task-state-machine.md
├── 30-handoffs.md
├── 31-model-routing.md
├── 32-cost-accounting.md
├── 33-budget-enforcement.md
├── 34-security.md
├── 35-secrets.md
├── 36-network-policy.md
├── 37-admission-policy.md
├── 38-prometheus.md
├── 39-grafana.md
├── 40-opentelemetry.md
├── 41-logging-tracing.md
├── 42-alerting-notifications.md
├── 43-retention-gc.md
├── 44-backup.md
├── 45-recovery.md
├── 46-self-hosting-threshold.md
├── 47-ha-expansion.md
├── 48-upgrade.md
└── 49-destroy.md

procedures/
├── fresh-install.md
├── bootstrap-resume.md
├── seed-flux-handoff.md
├── add-node.md
├── remove-node.md
├── restore-etcd.md
├── restore-forgejo.md
├── restore-authentik.md
├── restore-cnpg.md
├── restore-longhorn.md
├── recover-opentofu-state.md
├── rotate-secrets.md
├── rotate-age-key.md
├── break-glass-access.md
├── budget-emergency-stop.md
├── tdd-task-recovery.md
├── lost-node.md
├── provider-outage.md
├── object-storage-outage.md
├── total-cluster-loss.md
└── github-black-start.md

contracts/
├── provider.schema.yaml
├── substrate.schema.yaml
├── cluster-profile.schema.yaml
├── task.schema.yaml
├── acceptance-criteria.schema.yaml
├── verification.schema.yaml
├── red-result.schema.yaml
├── worker-result.schema.yaml
├── handoff.schema.yaml
├── harness.schema.yaml
├── context.schema.yaml
├── resource-profile.schema.yaml
├── model-routing.schema.yaml
└── cost-record.schema.yaml

adr/
└── ...

PLAN.md
STATUS.md
```

Exact organization MAY improve during Stage 0.

Completeness matters more than literal filename fidelity.

---

# 121. Required subsystem-plan structure

Every subsystem plan SHALL answer:

```text
Purpose

Requirements derived from SPEC.md

Inputs

Outputs

Dependencies

Owned state

External interfaces

Internal interfaces

Configuration

Secrets

Storage

Resource requirements

Security boundary

Happy path

Failure modes

Retry behavior

Idempotency behavior

Bootstrap behavior

Upgrade behavior

Recovery behavior

Observability

Testing strategy

Acceptance criteria

Explicit non-goals

Open questions
```

Omitted sections SHALL be intentionally justified.

---

# 122. Interface contracts

Significant cross-component boundaries SHOULD be machine-readable where practical.

Required contracts include at least:

- provider output;
- substrate manifest;
- cluster profile;
- task;
- acceptance criteria;
- verification policy;
- red verification result;
- Agent Runner API;
- MCP surface;
- worker input;
- worker result;
- handoff;
- repository context;
- resource profile;
- harness registry;
- model routing;
- cost record.

---

# 123. Dependency DAG

Stage 0 SHALL produce the authoritative dependency DAG.

A rough starting point:

```text
SPEC
 │
 ├── contracts
 ├── procedures
 └── subsystem plans
       │
       ▼
 bootstrap toolbox
       │
       ▼
 provider abstraction
       │
       ├── OVH
       └── existing nodes
       │
       ▼
      k3s
       │
       ├── networking / TLS
       ├── CNPG
       └── Longhorn
       │
       ▼
 Authentik + Forgejo
       │
       ├── GitHub mirror
       └── seed handoff
       │
       ▼
      Flux
       │
       ▼
   Woodpecker
       │
       ├── TDD gate mechanics
       ├── Prometheus
       ├── Grafana
       └── OTel
       │
       ▼
  Agent Runner
       │
       ▼
 fake workers
       │
       ▼
     Hermes
       │
       ▼
 context + accounting + budgets
       │
       ▼
 real harness
       │
       ▼
 SELF-HOSTING THRESHOLD
       │
       ▼
 HA expansion
```

The planning agent SHALL derive the real DAG rather than blindly copy this approximation.

---

# 124. Acceptance gates

Every implementation milestone SHALL have deterministic exit criteria.

Example Forgejo gate:

```text
[ ] HTTP healthy
[ ] Authentik login works
[ ] break-glass access works
[ ] DB uses CNPG
[ ] Git data uses intended storage
[ ] repository creation works
[ ] push works
[ ] clone works
[ ] mirror receives commit
[ ] backup works
[ ] restore works
[ ] metrics visible
[ ] required alerts exist
```

A milestone is complete because its evidence passes, not because files exist.

---

# 125. Stage-0 Definition of Done

Implementation SHALL NOT begin until:

```text
[ ] SPEC.md has no known contradiction

[ ] every subsystem has a plan

[ ] every significant interface has a contract

[ ] provider abstraction is defined

[ ] paid-resource guardrails are defined

[ ] OpenTofu state lifecycle is defined

[ ] node baseline is defined

[ ] networking/encryption is defined

[ ] TLS lifecycle is defined

[ ] Authentik identity architecture is defined

[ ] break-glass access is defined

[ ] bootstrap sequence is fully described

[ ] bootstrap resume/idempotency is described

[ ] seed → Forgejo → Flux transfer is fully described

[ ] TDD state machine is fully defined

[ ] verification-first exemption policy is defined

[ ] acceptance-criteria schema is defined

[ ] red-verification semantics are defined

[ ] test-protection policy is defined

[ ] CI enforcement of RED/GREEN provenance is defined

[ ] self-hosting threshold is exactly defined

[ ] one-node → three-node transition is defined

[ ] durable-state ownership is defined

[ ] secret ownership and rotation are defined

[ ] network boundaries are defined

[ ] admission/security boundaries are defined

[ ] model/harness abstraction is defined

[ ] cost-record schema is defined

[ ] economic circuit breakers are defined

[ ] observability architecture is defined

[ ] alerting/notification behavior is defined

[ ] retention/GC policy is defined

[ ] software supply-chain policy is defined

[ ] backup destinations are defined

[ ] RPO/RTO implementation is defined

[ ] restore testing is defined

[ ] black-start recovery is defined

[ ] destructive operations are defined

[ ] all major failure modes have procedures

[ ] dependency DAG exists

[ ] every DAG node has acceptance criteria

[ ] deterministic CI strategy exists

[ ] MVP/post-MVP boundary is explicit

[ ] no unresolved issue blocks implementation
```

---

# 126. MVP

MVP includes:

```text
one-node bootstrap

k3s
CNPG
Longhorn
Authentik
Forgejo
GitHub mirror
Woodpecker
Flux
Prometheus
Grafana
OpenTelemetry Collector

Hermes

Agent Runner:
  start
  status
  logs
  result
  cancel

repository context

acceptance criteria

enforced test-first workflow

structured handoffs

task state machine

bounded retries

model/harness abstraction

granular model accounting

budget limits

fake workers

at least one real coding harness

agent branch push

automatic PR creation

trusted CI

seed → Forgejo → Flux control transfer

self-hosted RED → GREEN → REVIEW acceptance test
```

---

# 127. Explicit MVP non-goals

Not required before Self-Hosting Threshold:

```text
live direct worker chat
persistent agent personalities
distributed worker message bus
multi-cluster federation
local frontier-model hosting
full Loki/Tempo retention
automatic merge everywhere
custom Git forge
custom CI engine
custom distributed filesystem
fully autonomous portfolio management
```

---

# 128. Post-MVP possibilities

Later capabilities MAY include:

- Loki;
- Tempo;
- PM agents;
- Scrum agents;
- task DAG scheduling;
- cross-repository orchestration;
- preview environments;
- low-risk autonomous merges;
- broader model providers;
- local workers;
- autoscaled workers;
- multiple clusters;
- richer agent messaging.

These SHALL not complicate the Self-Hosting Threshold unnecessarily.

---

# 129. Canonical end-to-end task

Human says:

> Add `/healthz` to example-service.

Hermes:

```text
creates specification
      ↓
creates stable acceptance criteria
      ↓
dispatches tester
```

Tester:

```text
writes acceptance test
      ↓
pushes RED commit
      ↓
reports handoff
      ↓
exits
```

Woodpecker:

```text
runs RED commit
      ↓
confirms expected behavior is absent
      ↓
RED_VERIFIED
```

Hermes dispatches implementer.

Implementer:

```text
starts from RED commit
      ↓
implements behavior
      ↓
runs local validation
      ↓
pushes implementation
      ↓
reports handoff
      ↓
exits
```

Woodpecker:

```text
runs acceptance tests
      ↓
runs regression tests
      ↓
GREEN_VERIFIED
```

Reviewer:

```text
receives:
  requirement
  acceptance criteria
  RED evidence
  implementation diff
  GREEN evidence
      ↓
reviews
      ↓
reports result
```

Human or policy merges.

Trusted CI builds artifact.

GitOps state changes.

Flux reconciles.

Throughout the flow the system knows:

- which models were used;
- which harnesses were used;
- what every invocation cost;
- what test specified the behavior;
- that the test failed first;
- which implementation made it green;
- which CI run independently verified it;
- which commit ultimately shipped.

At no point does the creative implementation worker possess production authority.

---

# 130. Bootstrap Definition of Done

A fresh operator with:

- Git;
- Docker or Podman;
- SSH key;
- provider credentials;
- DNS/tunnel credentials;
- S3 credentials;
- chosen model API credentials

can approximately run:

```bash
git clone https://github.com/<owner>/strange-company.git
cd strange-company

cp bootstrap.env.example bootstrap.env
$EDITOR bootstrap.env

./bootstrap --profile self-hosting-minimum
```

and obtain a functioning Strange Company without manually:

- provisioning VPSes;
- installing k3s;
- installing bootstrap tooling;
- configuring CNPG;
- configuring Longhorn;
- configuring Authentik;
- creating Forgejo;
- configuring Woodpecker;
- wiring Flux;
- configuring Prometheus/Grafana;
- creating agent namespaces;
- writing Runner RBAC;
- implementing worker admission policy.

---

# 131. Central architecture

```text
Authentik
    establishes human identity

Hermes
    understands intent and coordinates

Tester workers
    define executable expected behavior

Trusted CI
    proves RED before implementation

Implementation workers
    perform bounded creative labor

Agent Runner
    constrains, sequences, and accounts for execution

Kubernetes admission
    independently enforces workload boundaries

Trusted CI
    proves GREEN after implementation

Reviewer workers
    independently inspect evidence and implementation

Forgejo
    remembers engineering state

Git
    records authorized desired state

Flux
    makes approved state real

Prometheus / OpenTelemetry
    explain what is happening

Cost ledger
    explains what it cost

Budget controls
    decide when spending must stop

GitHub / object storage
    preserve what must survive catastrophe

Kubernetes
    provides disposable execution
```

The unifying rules are:

> **Important state must outlive the thing currently holding it.**

and:

> **The organization defines how an answer will be judged before asking an agent to produce the answer.**

---

# 132. Directive to the first planning agent

If this repository contains only `SPEC.md`, your assignment is **Stage 0: Complete Engineering Definition**.

Do NOT begin implementing Strange Company.

Read this specification completely.

Inspect the repository and confirm what, if anything, already exists.

Then plan the entire system.

You SHALL:

1. derive the authoritative dependency-ordered master plan;
2. create a detailed plan for every subsystem;
3. define every significant interface;
4. create required machine-readable schemas;
5. define provider abstraction;
6. define paid-resource guardrails;
7. define OpenTofu state lifecycle;
8. define node security and networking;
9. define TLS and certificate behavior;
10. define Authentik/OIDC and break-glass access;
11. define all durable-state ownership;
12. define bootstrap sequencing;
13. define bootstrap idempotency and resume;
14. define seed → Forgejo → Flux control transfer;
15. define the complete test-first state machine;
16. define acceptance-criteria semantics;
17. define valid RED verification;
18. define test-protection mechanisms;
19. define `ALREADY_SATISFIED` handling;
20. define `TEST_SPEC_CONFLICT` handling;
21. define verification-first exemptions;
22. define trusted CI enforcement of TDD provenance;
23. define worker security boundaries;
24. define Kubernetes admission enforcement;
25. define model/harness abstraction;
26. define granular invocation accounting;
27. define budget and emergency-stop behavior;
28. define metrics, telemetry, dashboards, alerts, and notifications;
29. define software supply-chain behavior;
30. define retention and garbage collection;
31. define backups;
32. map RPO/RTO targets to concrete mechanisms;
33. define repeatable restore testing;
34. define black-start recovery;
35. define one-node → three-node expansion;
36. define repository-context behavior;
37. define worker and handoff contracts;
38. define self-hosting acceptance;
39. define deterministic acceptance gates for every implementation milestone;
40. identify contradictory or underspecified requirements;
41. resolve non-fundamental ambiguity conservatively;
42. surface fundamental ambiguity instead of inventing product direction;
43. cross-review subsystem plans for circular dependencies;
44. independently cross-review security;
45. independently cross-review disaster recovery;
46. independently cross-review TDD enforcement for bypasses;
47. independently cross-review seed-to-Flux control transfer;
48. create and maintain `STATUS.md`;
49. stop only when every Stage-0 Definition-of-Done requirement is satisfied.

You MAY use subordinate reasoning agents.

You SHOULD use independent fresh contexts for critical review of:

- bootstrap;
- storage;
- security;
- Authentik and break-glass access;
- TDD enforcement;
- Agent Runner;
- cost accounting;
- disaster recovery;
- seed-to-Flux handoff.

Do not implement the platform merely to create the appearance of progress.

The desired result of Stage 0 is a project definition complete enough that implementation can be decomposed into bounded, independently testable tasks whose success criteria already exist before those tasks are attempted.

When Stage 0 is complete, report:

- architecture summary;
- dependency DAG;
- implementation sequence;
- subsystem-plan inventory;
- contract/schema inventory;
- procedure inventory;
- TDD/verification policy summary;
- unresolved non-blocking questions;
- explicit confirmation of every Stage-0 Definition-of-Done item.

Then stop.

Implementation is a separate task.
