# 24 — Hermes

**Status:** Stage-0 plan (draft)
**DAG nodes:** C08 (co-owner with 26: Hermes side of the MCP surface and the Hermes deployment)
**Related plans:** [13](13-longhorn.md), [15](15-authentik.md), [25](25-context.md), [26](26-agent-runner.md), [29](29-task-state-machine.md), [31](31-model-routing.md), [32](32-cost-accounting.md), [33](33-budget-enforcement.md), [35](35-secrets.md), [36](36-network-policy.md), [38](38-prometheus.md), [42](42-alerting-notifications.md), [44](44-backup.md), [51](51-capacity-and-scheduling.md), [53](53-model-gateway.md), [54](54-strange-company-chart.md)
**Contracts:** consumes `mcp-surface.schema.yaml` (26), `gateway-token.schema.yaml` (53), `model-routing.schema.yaml` (31)
**ADRs:** relies on 0003 (Hermes is upstream Hermes Agent), 0004, 0009, 0015, 0401 (Runner-managed Hermes credentials)

> Every section below is mandatory (SPEC §121).

## 1. Purpose

Hermes is the persistent, human-facing orchestration layer [SPEC §4.6]: it receives intent, decomposes work, writes acceptance criteria, chooses roles and approved harness/profile/tier names, dispatches workers through the Runner's narrow MCP surface, consumes results and handoffs, initiates review, manages retries and escalation, and reports state [SPEC §4.6, §59–§62]. Per ADR-0003 it is the **upstream Nous Research Hermes Agent**, digest-pinned, configured through *managed scope*, and treated as an **untrusted-but-authenticated client**: every invariant is enforced by the Runner, the Gateway, CI and admission, never by Hermes.

This plan defines the deployment, the managed configuration, the Git-resident instructions and skills, the human surface, accounting, the L-1 failure modes and their mitigations, network isolation, backup and the upgrade gate.

## 2. Requirements derived from SPEC.md

1. Hermes receives human intent, decomposes work, constructs task context, chooses roles and approved model/harness profiles, dispatches workers, consumes results and handoffs, initiates review, manages retries/escalation and reports state [SPEC §4.6].
2. Hermes has no general-purpose Kubernetes workload-creation privileges [SPEC §4.6, §59]; it reaches the Runner through a narrow MCP surface [SPEC §59, §60].
3. Hermes cannot specify image, namespace, ServiceAccount, Secrets, volumes, host paths, privileged mode, security context, raw YAML or resource values [SPEC §62].
4. The Hermes UI uses Authentik OIDC [SPEC §21]; human and machine identity stay separate [SPEC §21].
5. Learned context is supplementary, never silently overrides Git, and important learning becomes a proposed repository-context change [SPEC §43].
6. Workers do not receive unrestricted Hermes history [SPEC §66].
7. Every billable model invocation — including Hermes' own — is attributable and budgeted [SPEC §78, §81]; the emergency stop can stop autonomous spending without shutting down core services [SPEC §81].
8. Hermes is part of the Self-Hosting Threshold platform [SPEC §117] and has a failure procedure [SPEC §113]; Prometheus collects useful Hermes metrics [SPEC §91].
9. Important state outlives the process [SPEC §1.3]; Hermes' state is non-authoritative [SPEC §4.1, §43].

Derived requirements:

- **24-R-1** Every operator-controlled Hermes setting (model provider, base URL, credentials, MCP server, dashboard OIDC, listener settings, disabled toolsets) is pinned through managed scope (`$HERMES_MANAGED_DIR=/etc/hermes`), so dashboard-entered values cannot override it (L-1).
- **24-R-2** Hermes' only network peers are: the Runner MCP listener, the Model Gateway, the in-cluster ingress path to Authentik (OIDC), and DNS.
- **24-R-3** Hermes' instructions and skills are version-controlled in `strange-company` and mounted read-only.
- **24-R-4** `/opt/data` holds no provider credentials: a preflight removes credential-shaped keys before Hermes starts (answers plan 11 Q-11-3).
- **24-R-5** Every L-1 behavior the integration relies on is re-verified by `make hermes-contract-test` on every Hermes image change.

## 3. Inputs

| Input | Source | Delivery |
|---|---|---|
| Human intent and conversation | humans via the dashboard `https://hermes.<BASE_DOMAIN>` | Authentik OIDC session |
| Image `nousresearch/hermes-agent@sha256:…` | upstream registry (ADR-0013: upstream images by digest) | chart value `images.hermes` (plan 54) |
| Managed scope files `.env`, `config.yaml` | rendered by the Runner from the Git template + Runner-minted credentials (ADR-0401) | Secret `hermes/hermes-managed-env`, mounted read-only at `/etc/hermes` |
| Skills and system instructions | `charts/strange-company/files/hermes/` in Git | ConfigMap `hermes/hermes-skills`, mounted read-only |
| Orchestrator model allowlist and family | `policy/model-routing.yaml` `orchestrator` section (plan 31) | embedded into the managed `config.yaml` template values (plan 54) and the Hermes gateway token's `allowed_models` (plan 53) |
| MCP tool results | Runner `:8081/mcp` | `mcp-surface.schema.yaml` |
| Model responses | Gateway `:8080` | provider wire format (plan 53) |

## 4. Outputs

- MCP calls to the Runner (`agent_start`, `agent_status`, `agent_logs`, `agent_result`, `agent_cancel`, `agent_list`).
- Model requests through the Gateway (ledger rows with `token_kind=orchestrator`, `harness=hermes`, `role=orchestrator`, `phase=orchestration`, project `_platform`; plan 32/53).
- Conversation answers in the dashboard; sessions and learned memory in `/opt/data` (non-authoritative).
- Researcher tasks carrying context proposals (plan 25 §14.6).

## 5. Dependencies

**Hard (DAG):** C08 ← C01 (Runner core + MCP listener), C05 (Gateway, orchestrator token), P07 (Authentik + `hermes-dashboard` OIDC application, plan 15).

**Runtime:** Longhorn (`hermes-data` PVC), Runner, Gateway, Authentik (dashboard login only), Traefik/ingress (dashboard), DNS.

**Interfaces assumed:**
- Plan 15: OIDC application `hermes-dashboard` (public + PKCE by default), issuer `https://auth.<d>/application/o/hermes-dashboard/`, groups `sc-admins`, `sc-engineers` allowed; callback path verified at C08 (Q-15-05).
- Plan 09: in-cluster resolution of `auth.<d>` and `hermes.<d>` to Traefik (split-horizon) with a trusted certificate; Traefik pods in namespace `ingress`, `websecure` container port 8443 (verify with plan 09).
- Plan 53: orchestrator token claims (§14.5 there), route `/p/<provider>`; the `fake-model-upstream` supports scripted tool calls for deterministic Hermes tests (§22; request to plan 53).
- Plan 31: `ModelRouting.orchestrator` names the provider, API family and model(s) Hermes may use.
- Plan 26 / ADR-0401: the Runner renders and rotates `hermes-managed-env` and restarts Hermes.
- Plan 38: blackbox probes `hermes-gateway` (`http://hermes.hermes.svc:8642/health`, verify path) and `public-hermes`.

## 6. Owned state

| State | Location | Authority | Writer |
|---|---|---|---|
| Sessions, `state.db`, learned memory, user `config.yaml`/`.env`, `auth.json` | Longhorn PVC `hermes-data` (5 Gi, StorageClass `longhorn`, Retain) mounted at `/opt/data` | **non-authoritative** (SPEC §43); it is not a cache either (L-1) | Hermes |
| Skills, system instructions, managed-config templates | Git (`charts/strange-company/files/hermes/`) | authoritative | humans via PR |
| Managed scope rendering | Secret `hermes/hermes-managed-env` | derived (Git template + Runner credentials) | Runner (ADR-0401) |

Task state is never owned by Hermes: the Runner database is authoritative (SPEC §101). Losing `/opt/data` loses conversation history and learned notes, not work.

## 7. External interfaces

| Interface | Port / URL | Consumers | Auth |
|---|---|---|---|
| Dashboard (human UI) | Service `hermes:9119` → IngressRoute `hermes.<d>` (plan 54) | humans | Authentik OIDC (native; rejects the gateway bearer key, L-1) |
| Hermes gateway API | Service `hermes:8642` (ClusterIP only; not exposed publicly) | operators via `kubectl port-forward`, blackbox probe (`/health`) | `API_SERVER_KEY` (`SC-HERMES-API`) bearer; 401 otherwise |
| MCP client → Runner | `http://agent-runner.strange-company.svc.cluster.local:8081/mcp` | — | `SC-RUNNER-HERMES` bearer |
| Model client → Gateway | `http://model-gateway.strange-company.svc.cluster.local:8080/p/<provider>` | — | Hermes gateway token (`SC-GATEWAY-TOKEN`, instance `hermes`) |

## 8. Internal interfaces

Hermes internals are upstream. Strange Company relies on these upstream mechanisms (all re-verified by 24-R-5):

| Mechanism | Used for |
|---|---|
| `hermes gateway` server mode (no TTY) | the only run mode (L-1: bare `hermes` is an interactive TUI and crash-loops) |
| managed scope (`$HERMES_MANAGED_DIR`, per-leaf override of `config.yaml` and `.env`) | 24-R-1 |
| MCP client (streamable HTTP, custom headers) | Runner tools |
| skills directory (`$HERMES_HOME/skills/<name>/SKILL.md`, verify path) | Strange Company skills |
| built-in scheduler / cron jobs (verify feature and configuration path) | task polling (§14.4) |
| persistent memory | learned context [SPEC §43] |
| dashboard OIDC (`HERMES_DASHBOARD_OIDC_*`) | human surface |

## 9. Configuration

### 9.1 Deployment (rendered by the `strange-company` chart, plan 54)

```yaml
apiVersion: apps/v1
kind: Deployment
metadata: {name: hermes, namespace: hermes, labels: {app.kubernetes.io/name: hermes, strange.company/tier: platform}}
spec:
  replicas: 1
  strategy: {type: Recreate}                 # single RWO volume (L-5)
  template:
    metadata:
      annotations: {strange.company/managed-revision: "<set by the Runner, ADR-0401>"}
    spec:
      serviceAccountName: hermes             # no RoleBinding anywhere
      automountServiceAccountToken: false
      enableServiceLinks: false
      imagePullSecrets: [{name: sc-registry-pull}]   # the preflight init container uses the internal Runner image (review RA-9)
      priorityClassName: <tier platform, plan 51>
      securityContext: {runAsNonRoot: true, runAsUser: <image non-root uid, else 65532>, fsGroup: <same>, fsGroupChangePolicy: OnRootMismatch, seccompProfile: {type: RuntimeDefault}}
      initContainers:
        - name: managed-scope-preflight      # §9.4
          image: <images.runner digest>       # agent-runner binary, subcommand hermes-preflight
          args: [hermes-preflight, --managed-dir=/etc/hermes, --data-dir=/opt/data]
      containers:
        - name: hermes
          image: docker.io/nousresearch/hermes-agent@sha256:<digest>
          args: [gateway]                    # i.e. `hermes gateway` (verify entrypoint form)
          env: [HERMES_HOME=/opt/data, HERMES_MANAGED_DIR=/etc/hermes, TMPDIR=/tmp]   # all other settings via managed scope
          ports: [{name: api, containerPort: 8642}, {name: dashboard, containerPort: 9119}]
          startupProbe: {tcpSocket: {port: 8642}, periodSeconds: 5, failureThreshold: 60}
          readinessProbe: {httpGet: {path: /health, port: 8642}, periodSeconds: 10}      # verify path; fallback tcpSocket
          livenessProbe: {tcpSocket: {port: 8642}, periodSeconds: 20, failureThreshold: 6}
          resources: {requests: {cpu: 100m, memory: 512Mi}, limits: {cpu: "1", memory: 1Gi}}
          securityContext: {allowPrivilegeEscalation: false, readOnlyRootFilesystem: true, capabilities: {drop: [ALL]}}   # readOnlyRootFilesystem verify at C08
          volumeMounts:
            - {name: data, mountPath: /opt/data}
            - {name: managed, mountPath: /etc/hermes, readOnly: true}
            - {name: skills, mountPath: /opt/data/skills/strange-company, readOnly: true}
            - {name: tmp, mountPath: /tmp}
      volumes:
        - {name: data, persistentVolumeClaim: {claimName: hermes-data}}
        - {name: managed, secret: {secretName: hermes-managed-env, defaultMode: 0440}}
        - {name: skills, configMap: {name: hermes-skills}}
        - {name: tmp, emptyDir: {sizeLimit: 256Mi}}
```

PVC `hermes-data` (5 Gi, `longhorn`, RWO) is **not rendered by the chart** (review STO-5, ADR-0203): it is the stable-named PV/PVC/Volume triplet of plan 13's Flux layer (`prune: disabled`), and the Deployment references it as an existing claim, so black-start restores (`fromBackup`) and restore tests address the volume Hermes actually mounts. Services: `hermes` (ClusterIP; ports `api` 8642, `dashboard` 9119).

Replicas: chart value `hermes.replicas` (1; set to 0 by PR to keep Hermes stopped during a volume restore without suspending the whole HelmRelease, review RA-m10).

Version selection rule: latest upstream Hermes Agent release tag published ≥ 14 days before the change, referenced by digest; upgrades via Renovate PR gated by 24-R-5.

### 9.2 Managed scope (`/etc/hermes`)

Rendered by the Runner (ADR-0401) from the templates `charts/strange-company/files/hermes/managed/env.tmpl` and `config.yaml.tmpl` (mounted into the Runner at `/etc/strange-company/hermes/`; placeholders `{{SC_HERMES_API_KEY}}`, `{{SC_RUNNER_MCP_TOKEN}}`, `{{SC_HERMES_GATEWAY_TOKEN}}`). Key names marked (verify) are confirmed against the pinned Hermes version by `make hermes-contract-test`.

`.env` (pinned leaf keys):

| Key | Value |
|---|---|
| `API_SERVER_KEY` | `SC-HERMES-API` value (32 random bytes, base64url) |
| `API_SERVER_HOST` / `API_SERVER_PORT` | `0.0.0.0` / `8642` |
| `HERMES_DASHBOARD` / `HERMES_DASHBOARD_PORT` | `1` / `9119` |
| `HERMES_DASHBOARD_OIDC_ISSUER` | `https://auth.<d>/application/o/hermes-dashboard/` |
| `HERMES_DASHBOARD_OIDC_CLIENT_ID` | `hermes-dashboard` |
| `HERMES_DASHBOARD_PUBLIC_URL` | `https://hermes.<d>` |
| provider key variables Hermes recognizes for the orchestrator family (e.g. `ANTHROPIC_API_KEY` or `OPENAI_API_KEY`) | the Hermes gateway token |
| the matching base-URL variables (e.g. `ANTHROPIC_BASE_URL`, `OPENAI_BASE_URL`) | `http://model-gateway.strange-company.svc.cluster.local:8080/p/<provider>[/v1]` |
| every *other* provider key variable Hermes recognizes (list fixed at C08, e.g. `OPENROUTER_API_KEY`, `NOUS_API_KEY`) | literal `sc-disabled` (pins them so a dashboard-entered real key can never take effect; the NetworkPolicy blocks those providers anyway) |

`config.yaml` (pinned leaves; verify key names):

```yaml
model:
  provider: <custom / anthropic per plan 31 orchestrator family>
  base_url: http://model-gateway.strange-company.svc.cluster.local:8080/p/<provider>
  api_key: "<hermes gateway token>"
  default: <ModelRouting.orchestrator default model>
auxiliary:                               # every auxiliary slot Hermes uses (summarization, vision, compression, …)
  <slot>: {provider: <same>, base_url: <same>, api_key: "<hermes gateway token>", model: <orchestrator auxiliary model>}   # plan 31 §14.6: auxiliary resolved from orchestrator.auxiliaryTier (same provider/family; the primary when none) and present in the token's allowed_models (review COST-6)
mcp_servers:
  strange-company:
    url: http://agent-runner.strange-company.svc.cluster.local:8081/mcp
    headers: {Authorization: "Bearer <SC-RUNNER-HERMES>"}
    timeout: 120
disabled_toolsets: [terminal, code_execution, browser, web, image_generation, delegation]   # (verify names) orchestration only
gateway:
  multiplex_profiles: false              # profiles are not used (L-1)
```

Rationale for disabled toolsets: Hermes orchestrates; repository work happens in workers. Disabling them is defense in depth (the NetworkPolicy already denies the internet and the pod has no SA token).

### 9.3 Skills and system instructions (Git, read-only)

`charts/strange-company/files/hermes/SOUL.md` (system persona/instructions; delivered per the pinned version's mechanism, verify) and one directory per skill under `files/hermes/skills/<name>/SKILL.md`. Content requirements (the texts are written at C08 and reviewed like code):

| Skill | Must encode |
|---|---|
| `sc-intake` | Turn intent into a task definition: title, objective (one bounded outcome), repository, `base_ref`, constraints, verification mode (`test-first` for behavior changes; never invent exemptions; request an exemption class only with justification), acceptance criteria per `sc-writing-acceptance-criteria`. Ask the human when the request is ambiguous before calling `agent_start`. Split large intents into several tasks (`parent_task_id`). |
| `sc-writing-acceptance-criteria` | IDs `AC-1…AC-n`, stable; one observable behavior each; phrased so a deterministic test can decide it; include regression ACs for existing behavior that must not change (SPEC §45 example AC-3); no implementation instructions inside ACs. |
| `sc-dispatch-tdd` | The canonical sequence (SPEC §129): `agent_start` tester (role `tester`, harness `claude`, profile `tests`, tier `standard`) → wait for `RED_VERIFIED` → implementer (`implementation`, tier `bargain`) → wait for `GREEN_VERIFIED` → reviewer (`reasoning`, tier `standard`) → report the PR link and evidence to the human, who merges. Dispatch a role only if it is in `agent_status.next_allowed_roles`. Idempotency key = `hermes-<uuid>`, one fresh UUID per logical dispatch intent, kept in Hermes memory and reused only for transport retries of that intent; on a replay, compare the returned `execution_id`/`attempt` with the intended attempt before proceeding (review RA-m8). Never pass raw infrastructure fields (the Runner rejects them). Keep `orchestrator_notes` ≤ 2 000 characters, factual, labelled as hints; never give the tester implementation hints. |
| `sc-handle-outcomes` | `TEST_NEEDS_WORK` → re-dispatch the tester (the Runner supplies red evidence); `ALREADY_SATISFIED`/`NEEDS_INVESTIGATION` → dispatch a researcher to investigate, then ask the human whether to close or respecify (never manufacture failure, SPEC §51); `TEST_SPEC_CONFLICT` → dispatch a read-only reviewer with purpose `adjudication` (plan 21 §14.6), then present its `conflict_decision` to the human: a test change needs the human's `/sc accept-test-change` in Forgejo (ADR-0355; Hermes cannot issue it, CD-15); `test_valid` → re-dispatch the implementer; never tell the implementer to edit tests; `CHANGES_REQUESTED` → re-dispatch the implementer; `NEEDS_HUMAN` → summarize `needs_human_reason`, evidence and options, and stop; `BLOCKED` → only humans unblock (L-4); `BUDGET_EXCEEDED`/`EMERGENCY_STOP` → stop dispatching and tell the human; `ATTEMPTS_EXHAUSTED` → never work around it with a new task for the same objective without human approval. |
| `sc-task-watch` | Polling routine (§14.4): call `agent_list` for non-terminal states (with `updated_since` once R02 ships), compare with the last seen state stored in memory, continue each task per `sc-dispatch-tdd`/`sc-handle-outcomes`, and post a short digest to the session. |
| `sc-learned-context` | When an observation recurs (≥ 2 tasks) or a human states a durable fact, start a researcher task asking for a `context_proposals` entry (plan 25 §14.6); never treat memory as authoritative over Git; cite tasks/evidence. |
| `sc-status-reporting` | Report per task: state, human state, current execution, attempts by class, evidence summary, cost (`cost.complete=false` means a floor), PR link. |

Mounted at `/opt/data/skills/strange-company/` (read-only), so Hermes cannot alter them. The dashboard may add personal skills elsewhere in `/opt/data/skills/`; those are non-authoritative and cannot change enforcement.

### 9.4 `managed-scope-preflight` init container

Runs `agent-runner hermes-preflight` (Runner image; no network):

1. `/etc/hermes/.env` and `/etc/hermes/config.yaml` exist, are non-empty, parse, and contain every pinned key in §9.2 with a non-empty value (L-4: an empty secret is worse than a missing one). Failure → exit 1 (pod not ready → `SCHermesDown`).
2. In `/opt/data/.env`: delete every key that is pinned by managed scope and every key whose name matches `(?i)(api_key|_token|secret|password)$` or whose value matches a provider-key pattern (plan 27 §13.4). In `/opt/data/config.yaml`: delete `model.api_key`, `auxiliary.*.api_key`, `custom_providers[*].api_key`. A `.bak` copy is **not** kept (it would retain the credential).
3. Write `/opt/data/.sc-preflight.json` `{at, removed_keys_count, managed_revision}` and log the count (never names of values).

### 9.5 Chart values (excerpt; plan 54 owns the schema)

`hermes.enabled` (true), `hermes.dashboard.ingress.enabled` (true), `hermes.dashboard.host` (`hermes.<d>`), `hermes.persistence.size` (5Gi), `hermes.resources`, `hermes.orchestrator.provider|family|model|auxiliaryModel` (rendered from the plan 31 resolution; CI-checked) (from plan 31 policy; CI-checked for consistency), `hermes.disabledToolsets`.

## 10. Secrets

| Class | Holder in Hermes | Creator / rotation (ADR-0401) |
|---|---|---|
| `SC-HERMES-API` (`API_SERVER_KEY`) | managed `.env` | Runner-generated; rotated every 30 days (ADR-0401; review SEC-13); operators read it with `kubectl -n hermes get secret hermes-managed-env` (cluster break-glass) |
| `SC-RUNNER-HERMES` (MCP bearer) | managed `config.yaml` header | Runner-generated; Runner stores sha256 only; 30-day rotation with 24 h overlap (plan 26 config) |
| `SC-GATEWAY-TOKEN` instance `hermes` | managed `.env`/`config.yaml` | Runner-minted JWS (`sub: orchestrator:hermes`, `harness: hermes`, `role: orchestrator`, `phase: orchestration`, `budget_scopes: [orchestration]`, lifetime ≤ 31 days; re-minted when the signing `kid` changes, review COST-16); registered in `ledger.gateway_tokens`; rotated every 30 days with overlap |
| `SC-OIDC-CLIENTS` instance `hermes-dashboard` (review GC-16) | managed `.env` (client id; a client secret only if plan 15 makes the client confidential) | plan 15/35 (SOPS); included in the template as `{{SC_HERMES_OIDC_CLIENT_SECRET}}` when present |

Never present in Hermes: provider keys (`SC-MODEL-API-KEYS`), Forgejo tokens or accounts, Kubernetes tokens, `SC-RUNNER-ADMIN`. **Hermes holds no Forgejo account (CD-15):** it cannot comment, so it can never issue `/sc` human commands (ADR-0350, accepted only from human accounts); human decisions it needs are requested from humans in the dashboard. **Conflict to resolve at consolidation:** `contracts/secret-classes.yaml` currently records `SC-RUNNER-HERMES`, `SC-HERMES-API` and the Hermes gateway-token instance as created by `sc-bootstrap seal` and stored SOPS-encrypted in Git; this plan and plan 26 follow ADR-0401 (Runner-generated, Secret-only, regenerated on loss). Plan 35 must update the register (creator `agent-runner`, storage `k8s-secret hermes/hermes-managed-env` + sha256/jti in `sc-pg`, recovery `regenerate`).

## 11. Storage

`hermes-data` (Longhorn, 5 Gi, replicas 1 → 2 at small-ha, plan 13); daily Longhorn recurring backup to S3 `longhorn-backups` (retain 7 daily + 4 weekly, plan 13/43); RPO ≤ 24 h, RTO ≤ 8 h, non-authoritative (master plan §8). Expected size < 1 Gi in the first year (sessions + `state.db`). Hermes logs are excluded from Loki (ADR-0552).

## 12. Resource requirements

| Profile | requests | limits |
|---|---|---|
| self-hosting-minimum | cpu 100m, memory 512Mi | cpu 1, memory 1Gi |
| small-ha | cpu 100m, memory 512Mi | cpu 1, memory 1Gi |

Single replica in both profiles (one RWO volume; Hermes sessions are not designed for active-active). Plan 51 measures the working set at C08 and may adjust.

## 13. Security boundary

- **Trust:** untrusted-but-authenticated (ADR-0003). Hermes may be prompt-injected by humans, issue text or model output; the damage it can do is bounded by the MCP surface (create/cancel tasks within policy and budget; read sanitized results/logs).
- **Kubernetes:** namespace `hermes` (PSA restricted, ADR-0015); SA `hermes` with no RoleBinding and no token mounted; the Runner is the only other principal with rights in `hermes` (create/update `hermes-managed-env`, patch Deployment `hermes`; plan 26 §13).
- **NetworkPolicy** (default-deny and allow rows from plan 36's matrix in the `policies` layer; this is the intent those rows must encode):
  - Ingress: `dashboard` 9119 from `ingress` namespace pods `app.kubernetes.io/name=traefik`; 8642 and 9119 from `monitoring` pods `app.kubernetes.io/name=prometheus-blackbox-exporter` (probes). Nothing else (port-forward traffic does not traverse NetworkPolicy).
  - Egress: DNS (kube-system `k8s-app=kube-dns`, UDP/TCP 53); `strange-company` pods `app.kubernetes.io/name=agent-runner` TCP 8081; `strange-company` pods `app.kubernetes.io/name=model-gateway` TCP 8080; Traefik for OIDC discovery/token/JWKS of `auth.<d>` only. Because a plain Traefik `websecure` allow would reach **every** ingress-routed hostname (`git.`, `ci.`, `grafana.`, …; review SEC-16), the requirement on plans 09/36 is a dedicated internal Traefik entrypoint that routes only `auth.<d>`, and HE-2 allows Hermes only to that entrypoint. Node IPs are never reachable (review SEC-5). No egress to the Kubernetes API, `agent-jobs`, `sc-pg`, Forgejo, Woodpecker or the internet.
- **Credential precedence (L-1):** managed scope + preflight (§9.4) + network isolation.
- **Human identity:** dashboard login only through Authentik (groups `sc-admins`, `sc-engineers`); the gateway API key is a machine credential and is rejected by the dashboard (L-1). If the pinned Hermes' native OIDC is absent or cannot enforce group membership (verified at C08), the fallback is an Authentik forward-auth proxy provider on `hermes.<d>` restricted to those groups (plan 15 middleware), with the native login disabled (review AUTH-26).
- **Break-glass:** if Authentik is down, operators use the Hermes gateway API through `kubectl port-forward` with `SC-HERMES-API` (requires cluster break-glass), or drive the Runner admin API directly (plan 26). No local dashboard password exists.

## 14. Happy path

### 14.1 Startup

Flux reconciles the chart → the Runner ensures `hermes-managed-env` exists (ADR-0401; plan 26 §18) → the preflight passes → `hermes gateway` starts both listeners → the readiness probe passes → the blackbox probe `hermes-gateway` reports 1.

### 14.2 Human intent to dispatch

1. A human signs in at `https://hermes.<d>` and writes: "Add `/healthz` to example-service" (SPEC §129).
2. Skill `sc-intake` drafts objective and ACs (e.g. AC-1 `GET /healthz` returns 200 with body `ok`; AC-2 existing routes unchanged), shows them, and the human confirms.
3. Hermes calls `agent_start` with the embedded `task` and role `tester` (SI-02). The Runner creates the task (`SPECIFIED → TEST_AUTHORING`) and queues the tester.
4. Hermes reports the task id and `human_state` to the human.

### 14.3 Learning outcomes (polling)

Hermes is a client; the Runner does not push to it. Two complementary mechanisms:

- **In-conversation:** after a dispatch, Hermes calls `agent_status` at most every 60 s while the human is engaged, for at most 10 checks, then hands over to the watcher.
- **Watcher:** a Hermes scheduled job `sc-task-watch` every `hermes.watchIntervalMinutes` (chart value, default 5, rendered into the skill and scheduler config; single owner, review RA-m6) runs skill `sc-task-watch` with the orchestrator auxiliary model (plan 31 `orchestrator.auxiliaryTier`; COST-6). **During C10** (review RA-12) a test overlay points the orchestrator at provider `fake`, model `fake-hermes-watch` (plan 46 §9.5): `hermes.orchestrator.model` and `auxiliaryModel` = `fake-hermes-watch`, and the test `ModelRouting.orchestrator` resolves to it, so the Runner re-mints the orchestrator token with that model in `allowed_models` and re-renders the managed config; the watcher then runs plan 53's scripted conversation deterministically. If the pinned Hermes version has no usable scheduler (verified by `make hermes-contract-test`), the fallback is a CronJob `hermes-watch` in `hermes` (same interval) that posts the watcher prompt to `POST /api/sessions/{id}/chat` with `SC-HERMES-API` and checks `finish_reason` (review RA-m13). The job does: `agent_list` for non-terminal states → for each task whose `state`/`updated_at` changed since the last run (kept in Hermes memory), continue the workflow (dispatch the next role, or summarize for the human). After R02 the call uses `updated_since` (plan 46) so a quiet system returns an empty list and the run costs one short model turn. Cost bound: the orchestration budget scopes (plan 33) — at roughly 3 000 tokens per run and 288 runs per day this is a small, visible daily cost; the watcher interval is a value in `files/hermes/` (Git).

### 14.4 Completion

On `REVIEW` with an approving reviewer result, Hermes posts the PR URL, the RED/GREEN evidence summary and the cost to the session. The human merges in Forgejo (ADR-0019). Hermes learns `DONE` on the next watcher run.

### 14.5 Accounting and emergency stop

Every Hermes model call carries the Hermes gateway token: one `ledger.cost_records` row per provider request with `token_kind=orchestrator`, `harness=hermes`, `role=orchestrator`, `phase=orchestration`, task/execution/attempt null, project `_platform` (plans 32/53). Emergency stop `all` makes the Gateway deny Hermes (`SC_EMERGENCY_STOP`); `workers` does not, and neither does the Runner's dispatch-only pause `runner.dispatchPaused` (Hermes can still explain the situation, but `agent_start` returns `EMERGENCY_STOP`). Levels and sources: plan 33, ADR-0452. Hermes surfaces Gateway denials as errors in the session (with HTTP 200 + `finish_reason: "error"` from Hermes' own API, L-1).

## 15. Failure modes

| # | Failure (L-1/L-5 where applicable) | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|---|
| H1 | Started as bare `hermes` (TUI) → crash loop | `CrashLoopBackOff`; `SCHermesDown` | no orchestration | none; args pinned to `gateway` in the chart and verified by 24-R-5 | `hermes-failure.md` §A |
| H2 | `API_SERVER_KEY` missing/empty → refuses to start | preflight exit 1 / container log | no orchestration | Runner re-renders managed scope on its 60 s ensure loop | `hermes-failure.md` §B |
| H3 | Dashboard-entered credential shadows managed config (L-1 precedence hazard) | preflight `removed_keys_count > 0` (log); ledger shows no orchestrator rows while Hermes reports answers | wrong model/provider possible | managed scope wins per leaf; preflight scrubs; NetworkPolicy blocks other providers | `hermes-failure.md` §C |
| H4 | Unknown model string accepted at create, fails at first turn (L-1) | first turn returns `finish_reason: "error"`; Gateway `SC_MODEL_NOT_ALLOWED` denial row | turns fail | model pinned in managed config from plan 31 allowlist; CI consistency check | `hermes-failure.md` §D |
| H5 | Backend errors returned as HTTP 200 with `finish_reason: "error"` (L-1) | contract test; Hermes API clients check `finish_reason` | silent failure if a client trusts content | every SC client of the Hermes API (probe, procedures) checks `finish_reason` | `hermes-failure.md` §D |
| H6 | Hermes ignores a requested model / profile prefix silently served by default (L-1) | contract test | wrong model | no per-session models or profiles are used; `multiplex_profiles: false` | — |
| H7 | Multi-Attach after rollout (L-5) | pod `ContainerCreating` > 5 min with `Multi-Attach error`; `SCHermesDown` | Hermes down | Recreate strategy reduces it; none automatic beyond retry | `hermes-failure.md` §E |
| H8 | Longhorn volume degraded/faulted | Longhorn alerts (plan 13) | Hermes down or read-only | — | `hermes-failure.md` §F, `restore-longhorn.md` |
| H9 | `/opt/data` corrupted (`state.db`) | Hermes errors at start | Hermes down | — | `hermes-failure.md` §F (restore from Longhorn backup or reset — non-authoritative) |
| H10 | Runner MCP unreachable / 401 | Hermes tool errors; `sc_runner_mcp_requests_total` flat; `SCRunnerDown` | no dispatch | — | `agent-runner-failure.md`, `hermes-failure.md` §G |
| H11 | Gateway down or Hermes token revoked/expired | turns fail (`finish_reason: "error"`); `SCModelGatewayDown` | no reasoning | Runner rotation timer re-mints; Gateway procedures | `hermes-failure.md` §G |
| H12 | Budget (orchestration scope) exhausted / e-stop `all` | Gateway denial rows; plan 33 alerts | Hermes cannot reason; workers unaffected unless also stopped | fail-safe by design | `budget-emergency-stop.md` |
| H13 | Authentik down | dashboard login fails (fail closed) | humans cannot use the dashboard | — | `restore-authentik.md`; break-glass §13 |
| H14 | Prompt injection / Hermes misbehaves (dispatch storm, wrong roles) | Runner rejections (`GATE_NOT_SATISFIED`, `RATE_LIMITED`), budget alerts | bounded by Runner policy and budgets | Runner rate limit 10/s, 200/min | `hermes-failure.md` §H (revoke `SC-RUNNER-HERMES`) |
| H15 | Credential rotation failed | `SCHermesCredentialRotationFailed` (plan 26) | old credentials valid until overlap ends | hourly retry | `hermes-failure.md` §B |
| H16 | Watcher cost runaway (scheduler misconfigured to seconds) | `SCBudgetThresholdReached` / `SCBudgetHourlyCapReached` on the orchestration scopes (plan 33; review COST-17); ledger rows per hour | spend | orchestration budget caps it | fix `files/hermes/` by PR |

## 16. Retry behavior

- Hermes' MCP calls use idempotency keys and retry with the same key on transport errors (skill rule); the Runner deduplicates.
- Model-call retries are Hermes-internal; Gateway budget/e-stop denials are 403 (not retried by SDKs; plan 53).
- Pod restarts: Kubernetes restart policy; the preflight is re-run on every start.

## 17. Idempotency behavior

- The deployment is declarative (Flux). The managed Secret is re-rendered deterministically from (template digest, credential jtis); re-rendering without credential change produces identical bytes and no restart.
- The preflight is idempotent (deleting already-deleted keys is a no-op).
- Dispatch via `agent_start` is idempotent per key (plan 26).

## 18. Bootstrap behavior

Hermes is not in the seed set. It is reconciled in the `strange-company` layer (plan 18 layer 23) after `sc-db`, monitoring config and Woodpecker, and becomes functional at C08 once the Runner (C01), Gateway (C05) and Authentik `hermes-dashboard` (P07) exist. Until the Runner first renders `hermes-managed-env`, the pod stays in `Init` (preflight waits up to 10 min for the Secret's files by polling, then exits 1) — this ordering is expected and covered by the chart install test (plan 54) with the Runner present. `./bootstrap verify` check V-36 makes Hermes `required` from C08 (plan 50).

## 19. Upgrade behavior

1. Renovate (plan 23) opens a PR bumping `images.hermes` digest.
2. CI runs `make hermes-contract-test` (k3d; §22) against the new digest. It re-verifies every L-1 fact used here. Failure blocks the PR.
3. Human merge → Flux → Recreate rollout (≈ 1–2 min of dashboard/orchestration downtime; Runner, workers and CI are unaffected; in-flight tasks continue).
4. Rollback = revert the PR. Hermes' on-disk format may not be backward compatible across upstream versions: before an upgrade the operator triggers an on-demand Longhorn backup of `hermes-data` (procedure `hermes-failure.md` §I); restoring it is the rollback path for data.

## 20. Recovery behavior

- Pod/node loss: Recreate on another node (small-ha; Longhorn replica 2); L-5 handling in `hermes-failure.md` §E.
- Volume loss: restore the latest Longhorn backup (RPO ≤ 24 h), or start empty: tasks and evidence are in `sc-pg`/S3/Forgejo, and Hermes resynchronizes through `agent_list`. Learned memory since the backup is lost; durable knowledge should already be in Git via context proposals (plan 25).
- Credential loss (Secret deleted): the Runner regenerates (ADR-0401) and restarts Hermes.
- Total cluster loss: plan 45 restores `hermes-data` from Longhorn backup or recreates it empty (non-authoritative).

## 21. Observability

- **Probes (plan 38):** `hermes-gateway` (internal `/health`, verify) and `public-hermes` (dashboard through ingress).
- **Metrics (derived; Hermes exports none we rely on):** `sc_runner_mcp_requests_total{tool, code}` (Runner side, plan 38 catalogue; review RA-m6), Gateway ledger aggregates for `harness="hermes"` (plan 32/39), `kube_deployment_status_replicas_available{deployment="hermes"}`, `kube_pod_container_status_restarts_total{namespace="hermes"}`.
- **Logs:** pod logs stay on the node (excluded from Loki, ADR-0552); preflight logs counts only.
- **Traces (plan 40):** Hermes emits no OTel itself. It is represented by the Runner's `sc.runner.mcp` SERVER spans (one per MCP call, linked to the task anchor when a task is named) and by the Gateway's `sc.model.invocation` spans with `token_kind=orchestrator`, `phase=orchestration` (own trace per request).
- **Alerts:** `SCHermesDown` (catalog, plan 42), `SCHermesCredentialRotationFailed` (plan 26), `SCHermesRestartLoop` (new; warning: > 3 restarts in 30 min), `SCHermesMcpSilent` (new; warning: Hermes ready but zero MCP requests for 2 h while non-terminal tasks exist — the watcher is broken: `sum(increase(sc_runner_mcp_requests_total[2h])) == 0 and sc_runner_tasks{state!~"DONE|CANCELLED"} > 0`).

## 22. Testing strategy

Deterministic, no paid calls. `make hermes-contract-test` (k3d, CI at C08 and on every Hermes image change) deploys the chart with the real Hermes image, the real Runner and Gateway, the Gateway's `fake-model-upstream`, and a fake OIDC issuer (or Authentik test instance). **Request to plan 53:** `fake-model-upstream` supports scripted tool calls — when the last user message contains `[fake:tool <name> <json-args>]`, it answers with an OpenAI Chat Completions (and Anthropic Messages) response carrying one tool call to `<name>`; otherwise it answers text. With that, a Hermes conversation driven through `POST /api/sessions/{id}/chat` can deterministically exercise MCP tools.

L-1 re-verification checklist (each a test case): `gateway` mode starts without TTY; start fails without `API_SERVER_KEY`; 401 on missing/wrong key; `GET /v1/models` lists only `hermes-agent`; `POST /api/sessions` creates without a model call (no ledger row); backend error yields HTTP 200 with `finish_reason: "error"`; managed scope wins over `/opt/data/.env` for a pinned key; dashboard refuses the gateway bearer; dashboard with no auth provider on a non-loopback bind refuses to start; MCP server with custom header connects and lists six tools. Added by review RA-m13: a dashboard-written `config.yaml` leaf (`model.base_url`) is overridden by managed scope; invoking a disabled toolset (`terminal`) is refused; an extra `mcp_servers` entry written through the dashboard does not appear in the tools Hermes can call (or is shown harmless and documented); the scheduler (or the fallback CronJob) runs the watcher at the configured interval.

## 23. Acceptance criteria

### GATE-C08 (Hermes side; Runner side in plan 26)

- [ ] **24-AC-1** `kubectl -n hermes get deploy hermes -o jsonpath='{.spec.template.spec.containers[0].image}'` matches `@sha256:[a-f0-9]{64}$`, `.spec.strategy.type` = `Recreate`, `.spec.template.spec.automountServiceAccountToken` = `false`, and `kubectl auth can-i --list --as=system:serviceaccount:hermes:hermes -n hermes` shows no rights beyond the default non-resource discovery rules.
- [ ] **24-AC-2** `make hermes-contract-test` passes all L-1 checks of §22 (ten upstream facts plus the four RA-m13 checks) against the pinned digest (exit 0; JUnit report lists 14 passed).
- [ ] **24-AC-3** Through `kubectl port-forward svc/hermes 18642:8642`: `curl -s -o /dev/null -w '%{http_code}' localhost:18642/v1/models` = `401`; with `Authorization: Bearer <API_SERVER_KEY>` = `200` and the body lists exactly one model `hermes-agent`.
- [ ] **24-AC-4** MCP end to end (fake upstream scripted): a chat turn containing `[fake:tool agent_list {}]` increases `sc_runner_mcp_requests_total{tool="agent_list"}` by ≥ 1, and the response has `finish_reason` ≠ `error`; a turn scripted to call `agent_start` with a field `image` yields a Runner `FORBIDDEN_FIELD` result visible in the session and no Job in `agent-jobs`.
- [ ] **24-AC-5** Accounting: each chat turn in 24-AC-4 produces ≥ 1 `ledger.cost_records` row with `token_kind = 'orchestrator'`, `harness = 'hermes'`, `role = 'orchestrator'`, `task_id IS NULL`.
- [ ] **24-AC-6** Credential precedence: with Hermes scaled to 0, a test Job writes `ANTHROPIC_API_KEY=sk-ant-test-canary` and `OPENAI_API_KEY=sk-test-canary` into `/opt/data/.env`; after scaling to 1, `/opt/data/.sc-preflight.json` shows `removed_keys_count ≥ 2`, `grep -c canary /opt/data/.env` = 0, and the next chat turn's ledger row uses the Hermes gateway token's jti.
- [ ] **24-AC-7** NetworkPolicy (with plan 36): from a test pod labelled like Hermes in `hermes`, TCP connects to `kubernetes.default.svc:443`, `sc-pg-rw.strange-company:5432`, `forgejo-http.forgejo:3000`, `agent-runner.strange-company:8082`, an external IP on 443, and **every node IP** on ports 22, 2379, 2380, 2381, 6443, 9100 and 10250 fail (review SEC-5); `agent-runner.strange-company:8081` and `model-gateway.strange-company:8080` succeed; an HTTPS request for `https://auth.<d>/` succeeds while one for `https://git.<d>/` through the same path fails (review SEC-16).
- [ ] **24-AC-8** (moved to plan 46 GATE-C10 as 46-AC-6: the e-stop is delivered at C06, not upstream of C08; review RA-11/GC-01.)
- [ ] **24-AC-9** Dashboard: `curl -s -o /dev/null -w '%{redirect_url}' https://hermes.<d>/` starts with `https://auth.<d>/application/o/authorize/` and contains `client_id=hermes-dashboard`; a request to the dashboard with `Authorization: Bearer <API_SERVER_KEY>` is not authenticated (redirect or 401).
- [ ] **24-AC-10** Rotation: deleting Secret `hermes-managed-env` results within 2 min in a new Secret whose MCP-token sha256 differs, a new Hermes pod (annotation `strange.company/managed-revision` changed), and a successful scripted `agent_list` turn.
- [ ] **24-AC-11** Skills: `kubectl -n hermes exec deploy/hermes -- ls /opt/data/skills/strange-company` lists the seven skills of §9.3, and writing to that path fails (read-only mount).
- [ ] **24-AC-12** Watcher configuration: `make hermes-contract-test` proves that either the Hermes scheduler lists job `sc-task-watch` with the configured interval or the fallback CronJob `hermes-watch` exists; the end-to-end watcher behavior, driven by plan 53's scripted conversation `fake-hermes-watch`, is plan 46 E2E-18 (C10 is downstream of C08; review RA-11, RA-12).
- [ ] **24-AC-13** (moved to plan 46 GATE-R02 as 46-AC-17: the Longhorn backup schedule is delivered at P10, which is not upstream of C08.)
- [ ] **24-AC-14** Fresh-install readiness (review RA-9): from a clean install of the chart, the Hermes pod (including the `managed-scope-preflight` init container from the internal Runner image) reaches Ready without `ImagePullBackOff`; `kubectl -n hermes get pod -l app.kubernetes.io/name=hermes -o jsonpath='{.items[0].spec.imagePullSecrets[0].name}'` = `sc-registry-pull`.
- [ ] **24-AC-15** Containment (review RA-10): after `POST /v1/admin/hermes-credentials:revoke`, Hermes' scripted `agent_list` turn fails (Runner 401), the Secret is not re-rendered for 10 min, and after `:enable` a scripted turn succeeds within 2 min.
- [ ] **24-AC-16** Auxiliary model (review COST-6): a conversation that triggers context compression yields zero `SC_MODEL_NOT_ALLOWED` denials for the Hermes jti.
- [ ] **24-AC-17** Dashboard group enforcement (review AUTH-26): an Authentik test user outside `sc-admins`/`sc-engineers` is refused by `https://hermes.<d>/` after login.

## 24. Explicit non-goals

- No custom orchestrator or custom UI (ADR-0003, master plan §15).
- No Hermes access to Forgejo (no account, CD-15), Kubernetes, databases or provider APIs.
- No authoritative state in Hermes; no high availability for Hermes in MVP (single replica, RWO).
- No use of Hermes profiles or per-session model overrides (L-1).
- No Hermes-driven notifications: human notifications go through Alertmanager (ADR-0009).

## 25. Open questions

1. **Exact upstream key names** for managed `config.yaml` (model, auxiliary slots, MCP headers, disabled toolsets, scheduler, skills path, system prompt delivery) (blocking: no). Resolved at C08 by `make hermes-contract-test`; this plan fixes the intent (pin every leaf; disable non-orchestration toolsets).
2. **Orchestrator API family** (blocking: no). Anthropic Messages via `/p/anthropic` if Hermes' native Anthropic provider accepts a custom base URL; otherwise OpenAI-compatible Chat Completions on a provider that offers it (plan 31 decides the model).
3. **Secret-register conflict** (blocking: no): §10 — plan 35 to align with ADR-0401.
4. **Scripted tool calls and conversations in `fake-model-upstream`** — resolved: marker-driven tool calls (53-AC-25) and the scripted conversation `fake-hermes-watch` (plan 53 §22.3). Blocking: no.
6. **Hermes dashboard OIDC** (blocking: no). Native support unverified; forward-auth fallback defined in §13 (AUTH-26).
5. **Hermes non-root UID and read-only root filesystem** (blocking: no). Verified at C08; fallback `readOnlyRootFilesystem: false` with a recorded exception (plan 37 PSA restricted does not require it).
