# 26 — Agent Runner

**Status:** Stage-0 plan (draft)
**DAG nodes:** F04 (primary, policy library; co-owner 22), C01 (primary; co-owner 29), C02 (primary; co-owners 27, 37), C03 (co-owner with 55: adapter mechanics), C08 (co-owner with 24: Runner side of the MCP surface)
**Related plans:** [21](21-tdd-workflow.md), [22](22-verification-policy.md), [24](24-hermes.md), [25](25-context.md), [27](27-worker-contract.md), [28](28-harnesses.md), [29](29-task-state-machine.md), [30](30-handoffs.md), [31](31-model-routing.md), [32](32-cost-accounting.md), [33](33-budget-enforcement.md), [35](35-secrets.md), [36](36-network-policy.md), [37](37-admission-policy.md), [42](42-alerting-notifications.md), [51](51-capacity.md), [53](53-model-gateway.md), [54](54-strange-company-chart.md), [55](55-forge-workflow.md)
**Contracts:** owns `runner-api.openapi.yaml`, `mcp-surface.schema.yaml`, `resource-profile.schema.yaml`, `task-token.schema.yaml` (review RA-8); consumes `worker-input`, `worker-result`, `harness`, `role`, `context` (G6), `task`, `state-machine` (29), `handoff` (30), `red-result`/`green-result`/`test-manifest`/`verification`/`repo-policy`/`repo-validation` (22), `acceptance-criteria` (21), `model-routing` (31), `budget-policy` (33), `gateway-token` (53)
**ADRs:** relies on 0003, 0004, 0007, 0008, 0014, 0017, 0019, 0452; introduces ADR-0400 (Runner listeners and break-glass admin access) and ADR-0401 (Runner-managed Hermes credentials)

> Every section below is mandatory (SPEC §121).

## 1. Purpose

The Agent Runner (`agent-runner`) is the **trusted deterministic boundary** between Hermes and Kubernetes execution [SPEC §59]. It is the only component that:

- owns durable task and execution state in `sc-pg` (with the state-machine library of plan 29) [SPEC §39, §73, §101];
- turns a narrow, policy-checked request into a worker Job whose image, namespace, ServiceAccount, Secrets, volumes, security context and resources come from trusted policy only [SPEC §62, §83, §84];
- mints and revokes the task-scoped credentials a worker holds (task token, gateway token, deploy key) [SPEC §86, §87];
- judges worker output against Git facts and policy before any verification is triggered, and records the result [SPEC §4.3, §58];
- drives Forgejo (deploy keys, PRs, statuses, labels; plan 55) and Woodpecker (verification pipelines; plans 20/22);
- loads, validates and versions all platform policy (`policy/**`) with fail-closed semantics (F04, ADR-0017) [SPEC §4.7].

The Runner is a Go service (ADR-0002) built from `src/cmd/agent-runner`. MVP runs one replica holding a Lease; the design is replica-safe.

## 2. Requirements derived from SPEC.md

1. Hermes reaches the Runner only through a narrow MCP surface: `agent_start`, `agent_status`, `agent_logs`, `agent_result`, `agent_cancel`, plus `agent_list` [SPEC §60, §126]. No administrative capability is exposed there [SPEC §60].
2. Core behavior is reachable through a transport-independent internal API equivalent to `POST /tasks`, `GET /tasks/:id`, `POST /tasks/:id/cancel`, `GET /tasks/:id/logs`, `GET /tasks/:id/result` [SPEC §61].
3. Hermes cannot specify image, namespace, ServiceAccount, Secrets, volumes, hostPath, privileged mode, security context, raw YAML or resource values [SPEC §62]. Trusted policy resolves them.
4. Hermes has no Kubernetes Job creation privileges [SPEC §4.6, §59].
5. An allowlisted, digest-pinned harness registry is enforced [SPEC §63].
6. Resource profiles are logical names; the Runner owns the Job manifest [SPEC §83].
7. Worker Jobs: non-root, no privilege escalation, all capabilities dropped, RuntimeDefault seccomp, no SA token, no hostPath, not privileged, Restricted PSA; worker SA has zero API rights [SPEC §84].
8. Workers receive only a scoped Forge credential, a model credential (gateway token, SI-09) and a task-specific Runner token [SPEC §86, §87].
9. The Runner holds the stronger Forgejo authority (PRs, labels, statuses, metadata) [SPEC §87] and creates PRs after validating worker output [SPEC §67].
10. Task records hold the SPEC §73 fields; every transition records timestamp, actor, old state, new state, reason; claims are atomic [SPEC §73].
11. Retries are bounded; exhaustion leads to NEEDS_HUMAN with a notification [SPEC §4.8, §74, §97].
12. Implementer dispatch is refused until the test commit exists and RED_VERIFIED is recorded [SPEC §52]; the Runner is one of the TDD enforcement layers [SPEC §58].
13. Concurrency limit: 1 worker at self-hosting-minimum, 2 at small-ha [SPEC §11, §12, §89].
14. Runner validation is not the only defense; admission and NetworkPolicy enforce independently [SPEC §85] (plans 36/37).
15. Workers' Git branches are `agent/<task-id>`; workers cannot push protected branches [SPEC §100].
16. Important state outlives the process: in-flight executions survive Runner restarts [SPEC §1.3].
17. Runner exposes Prometheus metrics without unbounded labels and OTel traces [SPEC §90, §91, §93]; Agent Runner failure is alertable [SPEC §96].
18. Policy is version-controlled; prompt text is never the sole enforcement [SPEC §4.7].

Derived requirements:

- **26-R-1** All Runner side effects (Job create, deploy key create/delete, PR, status, label, alert) are idempotent and driven from durable intent rows (outbox), so a crash at any point converges on restart.
- **26-R-2** Every dispatch records the policy revision (content digest) and policy commit used; every result is judged under the revision recorded at dispatch.
- **26-R-3** No secret value is ever written to `sc-pg`, logs, traces, task records or handoffs. Tokens are stored as `jti` + hash; deploy keys only as Forgejo key id + public fingerprint.
- **26-R-4** The Runner never trusts worker claims about Git; it re-derives branch head, ancestry, changed paths and diff size from Forgejo.
- **26-R-5** The `agent_list.updated_since` input is reserved for the first self-hosted task (plan 46); until then the Runner returns `NOT_IMPLEMENTED` (501) whenever it is present.
- **26-R-6** Reserved for plan 46's fallback task: until R02 the Runner accepts `orchestrator_notes` containing control characters other than `\n`/`\t` and strips them in the context compiler (plan 25 §9.5); rejecting them at the MCP boundary with `VALIDATION_FAILED` (`field: /orchestrator_notes`) is the fallback first self-hosted task and MUST NOT be implemented earlier.

## 3. Inputs

| Input | Source | Format |
|---|---|---|
| MCP tool calls | Hermes (`hermes` ns) | `mcp-surface.schema.yaml` |
| Admin API calls | operator via port-forward | `runner-api.openapi.yaml` |
| Worker packet fetch, heartbeats, results | worker pods (`agent-jobs`) | `worker-input` / `worker-result` |
| Forgejo webhooks (push, pull_request, pull_request_review, create, delete) | Forgejo (`forgejo` ns) | upstream payloads, HMAC-signed |
| Platform policy | ConfigMap `sc-policy` (Flux-generated from `policy/**`, ADR-0017) mounted at `/etc/strange-company/policy/` | YAML per §6.4 of master plan |
| Company context | ConfigMap `sc-company-context` (Flux-generated from `context/company/*.md`) mounted at `/etc/strange-company/context/company/` | Markdown |
| Repository policy/context/validation | Forgejo API at the task's base commit | `repo-policy`, `repo-validation`, `context` (RepositoryContext) |
| Woodpecker pipelines and step logs | Woodpecker API | plan 20; evidence docs `red-result`/`green-result` (22) |
| Kubernetes Job/Pod status, pod logs | kube-apiserver | k8s API |
| Budget and emergency-stop state | `ledger.budget_scopes`, `ledger.denials`, `ledger.emergency_stop_events`/`_state` (plan 33), Git `policy/budgets.yaml` `spec.emergencyStop`, ConfigMap `strange-company/sc-emergency-stop` (informer; never in Git), Gateway status `GET model-gateway-metrics:9090/sc/v1/status` | plan 33, ADR-0452 |
| Runner configuration | ConfigMap `agent-runner-config` (chart, plan 54) | `config.yaml` (§9) |

## 4. Outputs

- Task, execution, transition, evidence-reference rows in `sc-pg` schema `runner` (§6).
- Worker Jobs and per-execution Secrets in `agent-jobs`.
- Forgejo: deploy keys `sc-<execution-id>`, PRs, commit statuses `strange-company/tdd-provenance` and `strange-company/review` (ADR-0019), labels `status/*` (plan 55), PR comments with provenance, archive tags `sc-archive/*` (plan 21).
- Woodpecker pipeline triggers for RED/GREEN (ADR-0008, plan 22).
- Gateway token rows in `ledger.gateway_tokens` (plan 53) and their revocation.
- Evidence objects in S3 `<prefix>-evidence`: `logs/<task>/<execution>/worker.log.gz`, `results/<task>/<execution>/result.json`, `evidence/<task>/<phase>/<pipeline>.json` (plan 22), `context/<task>/<execution>/packet.json`, `quarantine/<execution>/…`.
- Alerts posted to Alertmanager v2 API (ADR-0009) and Prometheus metrics, OTel spans.
- Secret `hermes-managed-env` in `hermes` (ADR-0401; plan 24).

## 5. Dependencies

**Hard (DAG):**
- F04 ← F02 (contract tooling validates all policy schemas).
- C01 ← F04, F05 (state-machine library), F13 (chart skeleton), P11 (Runner image built, signed and pushed by trusted CI).
- C02 ← C01, F07 (sc-worker + fake harness), F12 (admission + netpol bundle).
- C03 ← C01, P05 (Forgejo with `sc-runner` bot). Mechanics here; PR/label/status semantics in plan 55.
- C07 ← C01, F04 (context compiler, plan 25, runs inside the Runner).
- C08 ← C01, C05 (Gateway for Hermes tokens), P07 (Authentik for the Hermes dashboard).

**Runtime:** `sc-pg` (CNPG, plan 12), kube-apiserver, Forgejo API + SSH, Woodpecker API, Model Gateway (reads the token registry the Runner writes; the Runner only reads the Gateway status endpoint — no Gateway admin API exists, ADR-0452), Alertmanager, object storage (`evidence` bucket), OTel Collector. Behavior when each is down: §15.

## 6. Owned state

System of record for machine execution state (SPEC §101). Database `strange_company` in CNPG cluster `sc-pg`, schema `runner`, owned by role `runner_owner` (migrations) and used by role `runner_app` (DML only). **The Runner owns every `sc-pg` schema migration, including schema `ledger`** (CD-04): forward-only, in-process at startup under `pg_advisory_lock(7453271)`; the Gateway never migrates and refuses readiness until `runner.schema_migrations` reaches its required version. Table *semantics* belong to the plans that define them (29: task state; 30: handoffs; 32/33: ledger; 55: forge projection); plan 26 ships their DDL. The Gateway is the writer of its ledger tables (plan 53). `runner_app` grants on `ledger` (review RA-m9): `INSERT` on `gateway_tokens` and `UPDATE (revoked_at, revoked_reason)` on it; `INSERT` on `emergency_stop_events`, `budget_limit_overrides` and `cost_adjustments`; `INSERT` and `UPDATE (limit_*)` on the `task:`/`execution:` rows of `budget_scopes` (column grants); `SELECT` on `denials`, `budget_scopes`, `emergency_stop_state` and the plan-32 cost views. Cost adjustments are **insert-only** for the Runner: `POST /v1/admin/ledger/adjustments` inserts the `ledger.cost_adjustments` row with `applied_at = null`, and the Gateway leader applies it to the `spent` columns (plans 32 §20, 33 §20); the Runner never writes Gateway-owned columns.

| Table | Purpose | Key columns (sketch) |
|---|---|---|
| `task_seq` (sequence) | `sc-<n>` allocation | — |
| `tasks` | SPEC §73 record; column set and enums are owned by `task.schema.yaml` and plan 29 §6 | as plan 29 §6, including `trace_id` and `anchor_span_id` (plan 40 §7.3, ADR-0551), `resume_state`, `current_generation`, `version` (optimistic lock) |
| `task_ac_versions` | versioned acceptance criteria (plan 29; supersedes the earlier `acceptance_criteria` table) | (`task_id`, `version`), document, `status`, `digest`, `frozen_at`; no UPDATE/DELETE grant |
| `task_generations`, `verifications`, `task_conflicts`, `task_reviews`, `rejected_events` | TDD generations, RED/GREEN/judge verifications, TEST_SPEC_CONFLICTs, review records, rejected events | columns, constraints and retention per plan 29 §6 |
| `generation_candidates`, `generation_implementation_commits` | append-only per-generation child rows (plan 29 §6.1, review TDD-7): every tester candidate and every accepted implementer commit of a generation, `(task_id, generation, seq, commit, execution_id[, red_result])`; per-execution commits also live in `executions.result_commit` | per plan 29 §6.1 |
| `adopted_commits` | human `/sc accept-external-commit` adoptions (plan 29/55), including on non-task PRs | `id`, `repository`, `pr_number`, `task_id` (nullable), `commit`, `adopted_by`, `reason`, `comment_id`, `adopted_at`; append-only |
| `handoffs` | normalized handoffs | per plan 30 §6 |
| `forge_projection`, `merge_audits` | Forgejo projection state and merge audits (incl. status-forgery and bypass detections) | per plan 55 §6 |
| `transitions` | append-only history | `id`, `task_id`, `at`, `actor`, `credential_kid` (admin key id for admin-API transitions, null otherwise; CD-30), `from_state`, `to_state`, `reason`, `policy_revision`, `execution_id`, `evidence_id` |
| `admin_credential_revocations` | sticky immediate revocations of admin credentials (CD-30) | `kid` (PK), `revoked_at`, `revoked_by_kid`, `reason` |
| `admin_audit` | one row per admin-API request (CD-30), 400-day retention | `id`, `at`, `kid`, `actor`, `kind` (`operator`, `breakglass`), `method`, `route`, `reason` (`X-SC-Reason`), `status`, `idempotency_key`, `request_sha256` |
| `executions` | one row per worker Job | `id` (`sc-184-x3`), `task_id`, `role`, `harness`, `image_digest`, `profile`, `model_tier`, `provider`, `model`, `phase`, `attempt`, `state`, `failure_class`, `failure_code`, `queued_at`, `started_at`, `finished_at`, `deadline_at`, `last_heartbeat_at`, `worker_phase`, `start_commit`, `branch_mode`, `result_digest`, `result_commit`, `policy_revision`, `policy_commit`, `context_digest`, `ref_snapshot jsonb` (branches **and tags**), `cancel_requested`, `span_id` (plan 40), and plan 29's `kind` (`producing`, `reviewing`, `auxiliary`), `generation`, `return_state`, `fingerprint`, `failure_signature`, `refunded` |
| `execution_events` | append-only execution state changes and notes | `execution_id`, `at`, `from`, `to`, `code`, `detail` |
| `results` | raw validated worker result (secret-scrubbed) + verdict | `execution_id` (PK), `received_at`, `source` (`post`, `log_recovery`), `body jsonb`, `digest`, `outcome`, `violations jsonb`, `handoff_redacted`, `facts jsonb` (judged Git-fact snapshot, §14.4 step 11), `judged_at`, `facts_rederived_at` |
| `harness_signatures` | cosign verification cache per harness image digest (§14.2 step 5) | `image_digest` (PK), `key_ref`, `verified_at`, `result`, `detail` |
| `hermes_state` | single row: Hermes credential state (ADR-0401) | `hermes_disabled`, `changed_at`, `changed_by`, `managed_revision` |
| `push_records` | Forgejo push/create/delete events per ref, 400-day retention (SEC-1) | `id`, `repository`, `ref`, `before`, `after`, `pusher_login`, `pusher_kind`, `delivery_id`, `received_at` |
| `id_floor` | PITR id floor (§20) | `task_seq_floor`, `set_at`, `reason` |
| `evidence` | pointers to evidence objects | `id`, `task_id`, `kind` (`red`,`green`,`log`,`result`,`context`,`quarantine`), `pipeline`, `commit`, `object_key`, `sha256`, `classification`, `recorded_at` |
| `tokens` | issued task tokens and gateway-token jtis (no values) | `jti`, `kind` (`task`,`gateway`,`hermes_mcp`,`hermes_api`), `execution_id`, `issued_at`, `expires_at`, `revoked_at`, `revoked_reason`, `value_sha256` (only for static bearer kinds) |
| `deploy_keys` | Forgejo deploy keys created | `execution_id`, `repository`, `forgejo_key_id`, `fingerprint`, `read_only`, `created_at`, `deleted_at` |
| `idempotency_keys` | request dedup, 7-day TTL | `caller`, `key`, `request_sha256`, `response jsonb`, `status`, `created_at` |
| `outbox` | durable side-effect intents | `id`, `kind`, `target`, `payload jsonb`, `dedup_key` UNIQUE, `state`, `attempts`, `next_attempt_at`, `last_error` |
| `policy_revisions` | every loaded policy set (last-known-good) | `revision` (sha256), `commit`, `loaded_at`, `valid`, `error`, `content jsonb` |
| `webhook_deliveries` | dedup of Forgejo deliveries, 7-day TTL | `delivery_id`, `event`, `received_at`, `processed_at` |
| `verification_intents` | ADR-0302 intents and served attestations | `intent_id`, `task_id`, `execution_id`, `phase`, `repository`, `branch`, `base_sha`, `expected_sha`, `nonce`, `created_at`, `expires_at` (= created + 15 min), `woodpecker_repo_id`, `pipeline_number`, `served_config_revision`, `served_config_sha256`, `served_workflows`, `served_at`, `outcome` |
| `emergency_stop` (view) | read-only view over `ledger.emergency_stop_events`/`ledger.emergency_stop_state` (plan 33 owns the tables; the Runner admin API inserts events) | `level`, `set_at`, `set_by`, `reason` |
| `schema_migrations` | applied migration versions | `version`, `applied_at`, `checksum` |
| `reporting.*` views | read-only views for Grafana (plan 39 §7.4; role `sc_reporting`/`grafana_ro`, `security_invoker = false`) | `reporting.v_tasks_needing_human` (`task_id, title, repository, reason, entered_at, task_url, trace_id`), `reporting.v_tdd_provenance` (`task_id, repository, done_at, provenance_complete, missing_facts`), `reporting.v_task_traces` (`task_id, trace_id, created_at`); migrations `GRANT SELECT` on every `reporting.*` view (including `reporting.v_tasks_needing_human`) to the Grafana reporting role `grafana_ro` (plan 12 managed role; also `sc_reporting`), and no grant on `runner.*` base tables |

SQL function `runner.repair_task_state(task_id, state, actor, reason)` (migration-provided, `SECURITY DEFINER`, executable only by `runner_owner`): inserts a `machine_migration`-style transition and sets the state atomically. It is the **only** sanctioned state-changing SQL (break-glass, `procedures/stuck-task-recovery.md`, plan 29) and may only target `NEEDS_HUMAN`, `BLOCKED`, `CANCELLED` or the state the task's last valid transition left (review TDD-25); any other target raises an exception, so provenance-bearing states (`RED_VERIFIED`, `GREEN_VERIFIED`, `AWAITING_MERGE`, `DONE`) can only be reached through the state machine and `/sc` commands.

Writers: only `agent-runner` (as `runner_app`). Humans change state only through the admin API (recorded as `human:<name>`), never by SQL, except the documented break-glass steps in `procedures/agent-runner-failure.md`.

Retention: tasks, transitions, executions, results, evidence pointers kept indefinitely (exact accounting may outlive traces, SPEC §107); `idempotency_keys` and `webhook_deliveries` 7 days; `execution_events` 400 days. Object retention is plan 43.

## 7. External interfaces

| Interface | Consumer | Contract | Notes |
|---|---|---|---|
| MCP streamable HTTP `:8081/mcp` | Hermes | `mcp-surface.schema.yaml` | §7.1 |
| Internal API `127.0.0.1:8080` | operator (port-forward), plan 50 CLI wrappers | `runner-api.openapi.yaml` | ADR-0400 |
| Worker API `:8082/v1/worker/*` | worker pods | `runner-api.openapi.yaml`, `worker-input`, `worker-result` | plan 27 |
| Webhook receiver `:8083/v1/hooks/forgejo` (CD-03) | Forgejo | `runner-api.openapi.yaml` | HMAC `SC-RUNNER-WEBHOOK`; verified against `secret` and, during rotation, `secret_previous` |
| Verification-intent endpoints `:8083` `GET /v1/ci/intents`, `POST /v1/ci/intents/{intent_id}/served` | Woodpecker configuration extension `sc-ci-config` (plan 20 §7.3) | `runner-api.openapi.yaml` (`VerificationIntent`) | bearer `SC-CI-INTENTS`; ADR-0302 |
| Ops listener, container port **named `metrics`** `:8090` (CD-02) | kubelet, Prometheus | `/healthz`, `/readyz`, `/metrics` | no auth, no sensitive data |
| Forgejo REST API (as `sc-runner`) | Runner → Forgejo | upstream API v1 | §7.3 |
| Woodpecker API (as `sc-runner`, `SC-WOODPECKER-RUNNER`) | Runner → Woodpecker | upstream; plan 20 | trigger + read pipelines/logs |
| Gateway status endpoint `GET :9090/sc/v1/status` | Runner → Gateway | plan 53 §7.4 | read-only: Gateway effective e-stop level for `gateway_effective_level`; the Runner never forwards the stop (ADR-0452) |
| Alertmanager v2 `POST /api/v2/alerts` | Runner → Alertmanager | plan 42 alert catalog | ADR-0009 |

### 7.1 MCP surface

- Transport: MCP **streamable HTTP**, endpoint `/mcp`, implemented with the official MCP Go SDK. Accepted protocol revisions: 2025-06-18 through 2026-07-28 (verified 2026-10-13: 2026-07-28 is the latest revision and removes the `initialize` handshake; upstream Hermes still performs `initialize`, so the server MUST negotiate down). Selection rule: latest Go SDK release at implementation time that supports both the newest revision and 2025-11-25 negotiation (verify; 26-AC-30).
- Authentication: `Authorization: Bearer <SC-RUNNER-HERMES>`; compared in constant time against `tokens.value_sha256` rows of kind `hermes_mcp` that are not revoked (two may be valid during rotation overlap). 401 for missing/invalid. Every call is attributed `actor = hermes`.
- `tools/list` returns exactly the six tools of `x-mcp-tools` with their `inputSchema`/`outputSchema`. A tool result carries `structuredContent` (the output object) and a JSON text rendering; errors return `isError: true` with a `ToolError`.
- Server-side validation order (deterministic; first failure wins): JSON-schema validation of input → **forbidden-field detection**: if any unknown property name (case-insensitive, `-`/`_` normalized) is in the set {`image`, `images`, `namespace`, `serviceaccount`, `serviceaccountname`, `service_account`, `secret`, `secrets`, `env`, `envfrom`, `volume`, `volumes`, `volumemounts`, `hostpath`, `host_path`, `privileged`, `securitycontext`, `security_context`, `resources`, `limits`, `requests`, `yaml`, `manifest`, `podspec`, `command`, `args`, `nodeselector`, `tolerations`, `affinity`} the error is `FORBIDDEN_FIELD` (field = JSON pointer), otherwise schema errors are `VALIDATION_FAILED` → idempotency → semantic checks (§14.2).
- Rate limit: 10 requests/s and 200/min per caller, `RATE_LIMITED` with `retry_after_seconds`.

### 7.2 Internal API

All of `runner-api.openapi.yaml`. Human-only operations (`POST /v1/tasks/{id}/transitions`, `POST /v1/executions/{id}/quarantine`, `PUT /v1/admin/emergency-stop`, `PUT /v1/admin/budget-scopes/{scope_key}` (task keys only, plan 33 §14.11), `POST /v1/admin/hermes-credentials:rotate` (ADR-0401), `POST /v1/admin/ledger/adjustments` (plan 32 §20, plan 33 §20), `POST /v1/executions/{id}/reingest` (break-glass re-ingestion of the stored result after a PITR, plan 29), `POST /v1/admin/gateway-tokens:verify` (≤ 10-min verification token for `./bootstrap verify` and Gateway smoke checks, synthetic task `sc-0`, auto-revoked; plans 50/53; review RA-8), `POST /v1/admin/hermes-credentials:revoke` and `:enable` (Hermes containment, ADR-0401; review RA-10), `POST /v1/admin/model-quarantine/{provider}/{model}:acknowledge` (lifts the Gateway's `BOUND_VIOLATION` model quarantine after a corrective PR, plan 53, CD-28; `runner_app` gets `UPDATE (acknowledged_at, acknowledged_by, acknowledged_kid)` on `ledger.model_quarantine`)) exist **only** on the loopback `api` listener. `POST /v1/tasks/{id}/transitions` requires `decision.command` (one of plan 29's human command names, the same set as `/sc` comments, ADR-0350) and is checked by the state machine exactly like the Forgejo command channel. `GET /v1/tasks` supports `state` and `blocked_reason` filters (plan 33 resume procedure). Authentication (CD-30; ADR-0400): **identity comes from the credential, never from a header.** The bearer is `scadm_<kid>.<secret>` (`kid` = 12 lower-case base32 characters, `secret` = 32 random bytes, base64url). The Runner looks up `kid` in the admin-credential registry (§9.1 `adminCredentials.file`), compares `sha256(secret)` in constant time, and rejects unknown, expired or revoked keys with 401. The recorded actor is the registry's identity for that key: `human:<login>` for a per-operator credential, always `breakglass:runner-admin` for the single break-glass credential. Any request carrying an `X-SC-Actor` header is rejected with 400 `VALIDATION_FAILED` (`field: /headers/X-SC-Actor`). Mutating calls carry a caller-supplied `X-SC-Reason`. Every admin request writes an `admin_audit` row (actor **and** `kid`), every admin-made transition records `credential_kid`, and each admin call raises the informational alert `SCRunnerAdminAccess` (labels `actor`, `kind`); use of the break-glass credential is additionally an always-notify page (SPEC §22). No Authentik integration in MVP (ADR-0400): the API is not exposed publicly and access already requires cluster credentials for `port-forward`.

### 7.3 Forgejo adapter (C03 mechanics)

Uses the `sc-runner` bot token (`SC-FORGEJO-BOT-TOKENS`) over the in-cluster HTTP Service `http://forgejo-http.forgejo.svc:3000/api/v1` (Service name owned by plan 16; assumed). Calls (verify paths against the pinned Forgejo API at C03, 26-AC-40):

| Purpose | Call |
|---|---|
| resolve base ref | `GET /repos/{o}/{r}/commits/{ref}` (or `/branches/{b}`) → commit SHA |
| read file at commit | `GET /repos/{o}/{r}/raw/{path}?ref={sha}`; directory list `GET /repos/{o}/{r}/contents/{dir}?ref={sha}` |
| branch heads snapshot | `GET /repos/{o}/{r}/branches?limit=50&page=n` |
| compare/diff | `GET /repos/{o}/{r}/compare/{base}...{head}` (files, stats) and `GET /repos/{o}/{r}/git/commits/{sha}` (parents) |
| deploy key create/delete | `POST /repos/{o}/{r}/keys` `{title: "sc-<execution-id>", key, read_only}`; `DELETE /repos/{o}/{r}/keys/{id}`; list `GET /repos/{o}/{r}/keys` |
| branch delete / archive tag | `DELETE /repos/{o}/{r}/branches/{name}`; `POST /repos/{o}/{r}/tags` |
| PR create/update | `POST /repos/{o}/{r}/pulls`, `PATCH /repos/{o}/{r}/pulls/{n}` (body/labels per plan 55) |
| commit status | `POST /repos/{o}/{r}/statuses/{sha}` |
| labels | `POST/DELETE /repos/{o}/{r}/issues/{n}/labels` |
| webhook verify (CD-18; org-level hooks are declared in plan 16 `forge.yaml`, the Runner never creates them) | `GET /orgs/{org}/hooks` (push, pull_request, pull_request_review, create, delete → `http://agent-runner.strange-company.svc:8083/v1/hooks/forgejo`, secret `SC-RUNNER-WEBHOOK`) |

Managed repositories are listed in Runner config `forgejo.managedRepositories` (plus `sc-e2e/fake-target`). A repository without `.strange-company/repo.yaml` at the base commit is `REPOSITORY_NOT_MANAGED`.

Client behaviour: 10 s timeout per call, 3 retries with jittered backoff (0.5, 1, 2 s) on 5xx/timeouts only, circuit breaker opens after 10 consecutive failures for 30 s → `DEPENDENCY_UNAVAILABLE`.

## 8. Internal interfaces

The binary contains a service core with adapters around it. Every loop runs only on the **leader** (Lease `agent-runner-leader`, ns `strange-company`, lease duration 15 s, renew deadline 10 s, retry period 2 s). Non-leaders serve read-only API/MCP calls and forward mutating calls with 503 `DEPENDENCY_UNAVAILABLE` + `retry_after_seconds: 2` (MVP has one replica, so this path is exercised only in tests).

| Component | Responsibility | Trigger / interval |
|---|---|---|
| API server / MCP adapter / worker API / hooks | §7 | requests |
| Service core | validation, policy resolution, state-machine calls (plan 29 library), idempotency | per request |
| Policy loader (F04) | load, validate, cross-check `policy/**`; revision tracking; LKG | fsnotify on mount + every 30 s |
| Dispatcher | QUEUED → STARTING: concurrency, slot, budget pre-check, token/key minting, context compile, Job create | every 2 s + on enqueue |
| Job watcher | informer on Jobs/Pods in `agent-jobs` (label `app.kubernetes.io/managed-by=agent-runner`); detects start, completion, DeadlineExceeded, image-pull errors, eviction | watch + 60 s resync |
| Result ingester | validates and judges results (§14.4) | on result receipt; retry from outbox |
| Log archiver | pod log → scrub → S3; then deletes Job | on pod terminal |
| Evidence poller | registers the intent, checks the branch head = `expected_sha` through Forgejo, triggers the pipeline on `agent/<task-id>` (ADR-0302), records the pipeline number, then polls; on a terminal pipeline runs identity checks **I1–I7** of plan 20 §8.6 (any failure → `EVIDENCE_IDENTITY_MISMATCH:<check>`, evidence discarded; I3 failure → cancel pipeline, `EVIDENCE_COMMIT_MISMATCH`, retry per plan 22), then fetches the `sc-verify` step log, parses the framed block (nonce, length, sha256) and hands the document to the verifier lib (F06, plan 22) | every 10 s per pending pipeline |
| Forgejo sync | PR/status/label projection from task state (plan 55); webhook processing; ref audit; an open PR whose head is an `agent/*` branch not bound to a task the Runner created (unknown task id or a different head repository) gets `strange-company/tdd-provenance` = `failure` with description `unbound agent branch` (plan 55) | outbox + webhook + 5 min resync |
| Deploy-key sweeper | deletes keys of terminal or unknown executions within 60 s; keys of non-terminal executions past `deadline_at + deployKeys.graceSeconds` are deleted too and the execution is failed `JOB_LOST` (review RA-m2) | every 60 s |
| Hermes credential guard | ensures `hermes-managed-env` (ADR-0401) unless `hermes_state.hermes_disabled`; re-mints the orchestrator token when its `kid` differs from the current signing `kid` (review COST-16) | every 60 s |
| Stuck-task watchdog | §15 thresholds | every 30 s |
| Outbox worker | executes side effects with `dedup_key` | continuous; backoff 1 s → 5 min |
| Context compiler (plan 25) | builds `ContextPacket` | at dispatch |
| Token issuer | task tokens, gateway tokens, Hermes credentials (ADR-0401) | at dispatch / rotation timer (daily check) |
| Alert publisher | posts/resolves Needs-Human, exhaustion and other Runner-posted alerts: resolves the headless Service `alertmanager-operated.monitoring.svc` and POSTs `/api/v2/alerts` to **every** replica (success = ≥ 1 replica, plan 42 §7.4; `sc_runner_alert_posts_total{alertname,result}`); re-posts firing alerts every 5 min (`endsAt` semantics) | continuous |
| Status-forgery watcher (design: plan 55, CD-07) | on `status` webhooks and the 5-min resync, any `strange-company/*` commit status whose Forgejo `creator` is not `sc-runner` → repost the correct status, write a `merge_audits` row, alert `SCProvenanceStatusForged` (critical) | webhook + resync |
| Release-provenance checker (review SEC-11; plan 23 §14.5) | on `promote/*` PR open/sync: posts required status `strange-company/release-provenance` = success only if the provenance `invocationId` names an existing Woodpecker pipeline with event `tag` run on the registered build agent, the tag exists, is protected, was pushed by an allowed identity (per `push_records`) and points to a commit on `main`; otherwise `failure` with the failing fact | webhook + 5-min resync |
| Push recorder (review SEC-1) | stores every Forgejo push/create/delete webhook (ref, before, after, pusher login and kind `human`/`bot`/`deploy_key`, delivery id) in `push_records`; serves the read-only `GET /v1/ci/push-records` (hooks listener, `SC-CI-INTENTS`) to `sc-ci-config`/`sc-release` so a release can refuse a tag not pushed by an allowed identity | webhook |
| Consistency checker `sc-runner-consistency` | nightly (03:30) plan-29 invariant checks; exports `sc_task_invariant_violations` | daily |

## 9. Configuration

### 9.1 Runner config (`/etc/strange-company/runner/config.yaml`, ConfigMap `agent-runner-config`, rendered by plan 54 from chart values)

| Key | Default | Notes |
|---|---|---|
| `installation.baseDomain` | from cluster values | |
| `database.dsnFile` | `/var/run/secrets/sc-pg/uri` | CNPG app Secret `sc-pg-app` (`SC-PG-APP`) |
| `database.maxConns` | 10 | |
| `leaderElection.leaseName` | `agent-runner-leader` | |
| `listeners.api` | `127.0.0.1:8080` | MUST be loopback; startup fails otherwise |
| `listeners.mcp` / `.worker` / `.hooks` / `.ops` | `:8081` / `:8082` / `:8083` / `:8090` | |
| `kubernetes.jobsNamespace` | `agent-jobs` | the only namespace the Runner creates Jobs in |
| `kubernetes.workerServiceAccount` | `agent-worker` | |
| `kubernetes.priorityClassName` | `agent-worker` | |
| `kubernetes.jobTTLSeconds` | 21600 | fallback GC; normal path deletes after log capture |
| `kubernetes.startupGraceSeconds` | 300 | added to the Job-level deadline |
| `forgejo.apiURL` | `http://forgejo-http.forgejo.svc:3000/api/v1` | plan 16 |
| `forgejo.sshCloneHost` | `forgejo-ssh.forgejo.svc.cluster.local:22` | plan 16 |
| `forgejo.knownHostsFile` | `/etc/strange-company/forgejo/known_hosts` | pinned host key recorded at forge-init (plan 16) |
| `forgejo.managedRepositories` | `[strange-company/strange-company]` | plus test repos |
| `woodpecker.apiURL` | `http://woodpecker-server.woodpecker.svc:8000/api` | plan 20 |
| `gateway.baseURL` / `gateway.statusURL` | `http://model-gateway.strange-company.svc.cluster.local:8080` / `http://model-gateway-metrics.strange-company.svc:9090/sc/v1/status` | plan 53; no Gateway admin port exists (ADR-0452) |
| `dispatch.blackoutWindows` | derived from chart value `runner.maintenanceWindow` (cluster-vars `SC_MAINTENANCE_WINDOW`, plan 08/54) | dispatch blackout around node maintenance (GC-20) |
| `adminCredentials.file` | `/var/run/secrets/agent-runner-admin/credentials.yaml` | CD-30 registry (hashes only); reloaded every 60 s; an unparseable file keeps the last valid registry and raises `SCRunnerPolicyInvalid`-class error log + metric `sc_runner_admin_registry_valid` = 0 |
| `dispatchPaused` (chart `runner.dispatchPaused`) | `false` | dispatch-only pause (ADR-0554 recovery; not an e-stop level). `agent_start` returns `EMERGENCY_STOP` with `details.dispatch_paused: true`; running executions continue |
| `emergencyStop.configMapName` | `sc-emergency-stop` | Kubernetes source of ADR-0452, watched by informer |
| `alertmanager.url` | `http://alertmanager-operated.monitoring.svc:9093` | plan 42 |
| `objectStorage.evidenceBucket` | `<prefix>-evidence` | plan 14 |
| `logs.maxBytes` | 10485760 | per execution |
| `logs.tailMaxBytes` | 65536 | agent_logs |
| `results.maxBytes` | 262144 | |
| `heartbeat.intervalSeconds` / `staleSeconds` | 30 / 180 | |
| `watchdog.startTimeoutSeconds` | 600 | STARTING → FAILED `START_TIMEOUT` |
| `watchdog.taskStallHours` | 24 | `SCTaskStuck` |
| `cancel.graceSeconds` | 60 | before Job deletion |
| `deployKeys.graceSeconds` | 600 | applies only to **non-terminal** executions past their deadline; terminal or unknown executions lose their key within 60 s |
| `idempotency.ttlHours` | 168 | |
| `hermes.credentialRotationDays` / `overlapHours` | 30 / 24 | ADR-0401, plan 53 |
| `hermes.managedEnvPins` | map | non-secret pinned keys written into `hermes-managed-env` (plan 24) |
| `telemetry.otlpEndpoint` | `otel-collector.monitoring.svc:4317` | plan 40 |
| `features.updatedSinceFilter` | `false` | 26-R-5; the R02 task flips this as part of its implementation |

Changes go through PR → CI → merge → Flux (chart values). The Runner reloads only `policy/**` and company context live; config changes roll the Deployment.

### 9.2 Policy (F04)

Files and kinds per master plan §6.4. The loader maps `kind` → schema `$id` and validates every file:

| Kind | Schema | Loaded by |
|---|---|---|
| `VerificationPolicy` | `verification.schema.yaml` (22) | Runner |
| `HarnessRegistry` | `harness.schema.yaml` | Runner |
| `ResourceProfiles` | `resource-profile.schema.yaml` | Runner |
| `ModelRouting` | `model-routing.schema.yaml` (31) | Runner + Gateway |
| `PricingTable` | `pricing.schema.yaml` (32) | Gateway (Runner validates only) |
| `BudgetPolicy` | `budget-policy.schema.yaml` (33) | Gateway + Runner (pre-check) |
| (retry policy) | `VerificationPolicy.spec.retry` inside `policy/verification.yaml` (CD-11; plan 29) — there is no separate retry file | Runner |
| `RoleDefinition` | `role.schema.yaml` | Runner |

Loader algorithm (also the F04 library used by the Gateway and by CI `make policy-check`):

1. Read every regular file under the mount (ignore `..data` symlink internals). ConfigMap keys cannot contain `/`, so a key `roles__tester.yaml` is read as relative path `roles/tester.yaml` (plan 54 generates the keys; a real policy file name containing `__` is rejected); reject unknown kinds, duplicate (kind, name), files > 1 MiB, YAML anchors/aliases (disabled in the parser), and non-UTF-8.
2. Validate each against its schema; collect all errors (no short-circuit).
3. Cross-file invariants: every role's `allowedHarnesses` exist in the registry and list the role in `supportedRoles`; every role's profiles exist in **every** profile set; `defaultModelTier ∈ allowedModelTiers` and each tier is routable for the role (plan 31 routing); production harnesses have `capabilities.ignoresRepoConfig: true`; `fake.disableWorkerTimeout` only with `stage: test-only`; profile invariants listed in `resource-profile.schema.yaml`; exactly the five roles of SPEC §65 exist; verification-policy references (plan 22) resolve.
4. Revision = `sha256:` of the canonical concatenation of `(relative path, NUL, file bytes, NUL)` sorted by path.
5. Commit resolution: list commits touching `policy/` on `main` (`GET /repos/strange-company/strange-company/commits?path=policy&limit=20`), recompute the digest of `policy/` at each via the contents API, and record the first match. No match within 20 → `commit = null`, alert `SCRunnerPolicyCommitUnknown` (warning). Gateway-token `policy_revision` claims require a commit; while `commit = null`, dispatch is refused with `POLICY_UNAVAILABLE`.
6. Valid → atomically swap in-memory snapshot, insert into `policy_revisions` (`valid = true`), metric `sc_runner_policy_revision_info{revision}` = 1.
7. Invalid → keep the last-known-good snapshot for reads, insert `valid = false` with the error, set `dispatch_enabled = false` (every `agent_start` returns `POLICY_UNAVAILABLE`), fire `SCRunnerPolicyInvalid` (critical). Recovery is automatic on the next valid revision.
8. At startup with no valid mounted policy: load the newest `valid` row from `policy_revisions` as LKG (reads only; dispatch disabled). With neither, `/readyz` is 503.

Repository policy merge (tighten-only) is plan 22's function; the Runner calls it with the base-revision `.strange-company/policy.yaml` at task creation and at each dispatch.

## 10. Secrets

| Class | Use | Storage in cluster |
|---|---|---|
| `SC-PG-APP` (sc-pg) | DB login `runner_app` | CNPG-generated Secret `sc-pg-app`, mounted file |
| `SC-RUNNER-SIGNING` | Ed25519 key signing task and gateway tokens | SOPS Secret `agent-runner-signing` (`private.pem`, `kid`); public JWKS published to the Gateway by plan 35/53 |
| `SC-FORGEJO-BOT-TOKENS` (`sc-runner`) | Forgejo API | SOPS/forge-init Secret `agent-runner-forgejo` |
| `SC-WOODPECKER-RUNNER` | Woodpecker API (ADR-0008) | Secret `agent-runner-woodpecker` |
| `SC-OBJECT-STORAGE` (evidence, read/write) | S3 evidence bucket | Secret `agent-runner-s3` |
| `SC-WORKER-TASK-TOKEN` (ephemeral) | minted per execution | per-execution Secret in `agent-jobs`, key `task-token`; DB stores `jti` only |
| `SC-GATEWAY-TOKEN` (ephemeral + Hermes) | minted per execution (and for Hermes) | per-execution Secret key `gateway-token`; `ledger.gateway_tokens` row |
| `SC-WORKER-DEPLOY-KEY` (ephemeral) | generated per execution | per-execution Secret key `deploy-key`; Forgejo holds the public key |
| `SC-RUNNER-HERMES` | Hermes → MCP bearer | generated by the Runner (ADR-0401), written to `hermes/hermes-managed-env`; DB stores sha256 |
| `SC-HERMES-API` | Hermes gateway API key | generated by the Runner (ADR-0401), same Secret; DB stores sha256 |
| **`SC-RUNNER-ADMIN` (new; CD-30)** | admin bearers for the loopback admin API: one instance per operator (`operator:<login>`) plus one `breakglass` instance | registry of `{kid, actor, kind, sha256, created_at, expires_at}` (hashes only) in SOPS Secret `agent-runner-admin` (key `credentials.yaml`), rendered from `gitops/clusters/<c>/secrets/strange-company/runner-admin-credentials.sops.yaml`; plaintext only with the operator (per-operator) or in the recovery bundle (break-glass) |
| **`SC-RUNNER-WEBHOOK` (new; CD-08)** | HMAC key for Forgejo webhooks to the Runner | SOPS Secret `strange-company/forgejo-webhook`, keys `secret` and optional `secret_previous` (plan 16, `rotate-forge-credentials.md`); the Runner sets `secret` on each repo hook and accepts a signature by either key while `secret_previous` exists |
| `SC-REGISTRY-PULL` (plan 23/35) | Runner signature checks of harness images; worker `imagePullSecrets` | Secret `sc-registry-pull` in `strange-company` and `agent-jobs` |
| `SC-CI-INTENTS` (introduced by plan 20) | bearer `sc-ci-config` presents on `/v1/ci/intents*` | SOPS Secret `agent-runner-ci-intents` (file; constant-time compare) |

New classes (for plan 35's register):
- **SC-RUNNER-ADMIN** (CD-30) — *Per-operator instances:* created by `sc-bootstrap access join <login>` (plans 15/50): the toolbox generates `kid` and secret locally, hands the plaintext to that operator only (shown once, for their password manager), and adds `{kid, actor: human:<login>, kind: operator, sha256, expires_at: +365 d}` to the SOPS registry by PR; the credential works after merge and Flux reconcile (the Runner re-reads the mounted file every 60 s). `sc-bootstrap access leave <login>` (a) calls `POST /v1/admin/credentials/{kid}:revoke` for each of the operator's keys — **effective on the next request** (sticky `admin_credential_revocations` row) — and (b) opens the PR that deletes the registry entries. Rotation: yearly (expiry) or on suspicion (revoke + join). Key ids are never reused; a revoked kid stays revoked even if a stale registry still lists it. *Break-glass instance:* exactly one entry with `kind: breakglass`, actor fixed `breakglass:runner-admin`, created at bootstrap (P06+) by `sc-bootstrap`, plaintext only in the recovery bundle; rotated after every use and yearly (`rotate-secrets.md`). Recovery: registry from Git; plaintexts from operators / the bundle; a lost per-operator key is revoked and re-issued. The registry file holds only hashes, but stays SOPS-encrypted to avoid publishing operator logins and key ids.
- **SC-RUNNER-WEBHOOK** — creator: bootstrap; consumer: Runner (verifies), Forgejo (signs; set by the Runner through the hooks API); storage: SOPS; rotation: move the current value to `secret_previous`, commit the new `secret` → Runner re-PATCHes every managed hook on next resync and accepts both keys until a later PR removes `secret_previous` (no rejected deliveries); recovery: regenerate (no data protected by it).

Rules: secret values only in files under `/var/run/secrets/...`; never environment variables; never logged (the logger has a redaction hook keyed on the loaded secret set).

## 11. Storage

- `sc-pg` as in §6. Expected volume at MVP: < 1 GiB/year (results are ≤ 256 KiB each).
- S3 `evidence`: ≤ 10 MiB log + ≤ 1 MiB other per execution (gzip). Retention plan 43.
- No PVC. The pod uses an `emptyDir` `/tmp` (256Mi) only.

## 12. Resource requirements

| Profile | requests | limits |
|---|---|---|
| self-hosting-minimum | cpu 100m, memory 192Mi | cpu 1, memory 384Mi |
| small-ha | cpu 100m, memory 256Mi | cpu 1, memory 512Mi |

PriorityClass `platform` (plan 51). Ephemeral storage limit 512Mi. Plan 51 measures and may adjust.

## 13. Security boundary

**Trust:** the Runner is trusted infrastructure (PSA `restricted`, non-root 65532, read-only root FS, no capabilities). It is the only holder of: Job/Secret create in `agent-jobs`, the `sc-runner` Forgejo token, the Woodpecker trigger token, the signing key.

**RBAC (exact; rendered by plan 54; no ClusterRole):**

| Namespace | Role | apiGroups / resources | verbs | resourceNames |
|---|---|---|---|---|
| `agent-jobs` | `agent-runner` | `batch` / `jobs` | get, list, watch, create, patch, delete | — |
| `agent-jobs` | `agent-runner` | `""` / `pods` | get, list, watch | — |
| `agent-jobs` | `agent-runner` | `""` / `pods/log` | get | — |
| `agent-jobs` | `agent-runner` | `""` / `secrets` | get, list, watch, create, delete | — |
| `agent-jobs` | `agent-runner` | `""`, `events.k8s.io` / `events` | get, list, watch | — |
| `strange-company` | `agent-runner-leader` | `coordination.k8s.io` / `leases` | create | — |
| `strange-company` | `agent-runner-leader` | `coordination.k8s.io` / `leases` | get, update | `agent-runner-leader` |
| `strange-company` | `agent-runner-estop` | `""` / `configmaps` | get, list, watch | `sc-emergency-stop` (list/watch with `fieldSelector=metadata.name=sc-emergency-stop`; plan 33) |
| `hermes` | `agent-runner-hermes` | `""` / `secrets` | create | — |
| `hermes` | `agent-runner-hermes` | `""` / `secrets` | get, update, patch | `hermes-managed-env` |
| `hermes` | `agent-runner-hermes` | `apps` / `deployments` | get, patch | `hermes` |
| `renovate`, `dr-tests` | `agent-runner-heavy-slot-read` | `""` / `pods` | list, watch | — (read-only; heavy-slot dispatch gate, plan 51 §9.7). **No** pod read in `ci-jobs` or `ci-build` (review SEC-3: pod specs there may carry inlined release credentials); CI occupancy comes from the Woodpecker queue API instead (§14.3) |

`patch` on Jobs is used only to flip `spec.suspend` and is double-checked by the plan-37 VAP (the Runner's identity is the only one allowed to create/patch in `agent-jobs`). The `agent-worker` SA has no RoleBinding at all.

**Reachable from:** Hermes pods → 8081; `agent-jobs` pods labelled `app.kubernetes.io/managed-by=agent-runner` → 8082; Forgejo pods → 8083 (`/v1/hooks/forgejo`); `woodpecker` pods `app.kubernetes.io/name=sc-ci-config` → 8083 (`/v1/ci/intents*` only, plan 20/36); `monitoring` (Prometheus and the blackbox exporter; probes target `:8090/readyz`, never the loopback 8080, review GC-03) → 8090; kubelet → 8090. 8080 is loopback (reachable only through `kubectl port-forward`, which requires `pods/portforward` in `strange-company`, held by cluster break-glass only).

**Reaches:** kube-apiserver; `sc-pg` 5432; Forgejo HTTP 3000; Woodpecker 8000; Gateway status 9090; Alertmanager 9093 (every replica, headless Service); OTel 4317; object storage 443 (egress rule per plan 36); the Forgejo OCI registry through the Traefik ClusterIP `10.43.0.20:443` (CD-21) for harness signature checks; DNS. Plan 36 renders these as NetworkPolicies.

**Job manifest builder (exact; C02).** For execution `sc-184-x3`, role implementer, harness `claude`, profile `implementation` (standard set):

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: sc-184-x3
  namespace: agent-jobs
  labels: &labels
    app.kubernetes.io/managed-by: agent-runner
    app.kubernetes.io/name: agent-worker
    strange.company/tier: agent-worker
    strange.company/task-id: sc-184
    strange.company/execution-id: sc-184-x3
    strange.company/role: implementer
    strange.company/harness: claude
  annotations:
    strange.company/policy-revision: sha256:…
    strange.company/attempt: "2"
spec:
  suspend: true                      # flipped to false after the Secret exists
  backoffLimit: 0
  activeDeadlineSeconds: 2100        # profile.deadline (1800) + startupGraceSeconds (300)
  ttlSecondsAfterFinished: 21600
  template:
    metadata:
      labels: *labels
    spec:
      serviceAccountName: agent-worker
      imagePullSecrets: [{name: sc-registry-pull}]
      automountServiceAccountToken: false
      enableServiceLinks: false
      restartPolicy: Never
      priorityClassName: agent-worker
      activeDeadlineSeconds: 1800    # profile.deadline
      terminationGracePeriodSeconds: 30
      securityContext:
        runAsNonRoot: true
        runAsUser: 65532
        runAsGroup: 65532
        fsGroup: 65532
        seccompProfile: {type: RuntimeDefault}
      shareProcessNamespace: false   # the harness container must not see sc-worker's processes or /proc/<pid>/root
      containers:
        - name: sc-worker            # trusted adapter (ADR-0402): Git, credentials, Runner API, framing; runs no repository code
          image: git.<domain>/strange-company/harness-claude-code@sha256:…   # same digest as `harness`; from the registry only
          imagePullPolicy: IfNotPresent
          command: ["/usr/local/bin/sc-worker", "run"]
          env:
            - {name: SC_RUNNER_URL, value: "http://agent-runner.strange-company.svc:8082"}
            - {name: SC_TASK_ID, value: sc-184}
            - {name: SC_EXECUTION_ID, value: sc-184-x3}
            - {name: SC_SECRETS_DIR, value: /var/run/sc/secrets}
            - {name: SC_WORKSPACE, value: /workspace}
            - {name: SC_PRIVATE_DIR, value: /var/sc/private}
            - {name: HOME, value: /var/sc/private/home}
            - {name: TMPDIR, value: /var/sc/private/tmp}
            - {name: OTEL_EXPORTER_OTLP_ENDPOINT, value: "http://otel-collector-workers.monitoring.svc:4318"}   # plan 40 §7.3: sc-worker spans only
            - {name: OTEL_EXPORTER_OTLP_PROTOCOL, value: http/protobuf}
            - {name: OTEL_TRACES_EXPORTER, value: otlp}
            - {name: OTEL_METRICS_EXPORTER, value: none}
            - {name: OTEL_LOGS_EXPORTER, value: none}
          resources:                 # fixed adapter slice, carved out of the profile
            requests: {cpu: 100m, memory: 256Mi, ephemeral-storage: 64Mi}
            limits: {cpu: 250m, memory: 256Mi, ephemeral-storage: 256Mi}
          securityContext: &csc
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            privileged: false
            capabilities: {drop: ["ALL"]}
          volumeMounts:
            - {name: workspace, mountPath: /workspace}
            - {name: private, mountPath: /var/sc/private}
            - {name: creds, mountPath: /var/run/sc/secrets, readOnly: true}
        - name: harness              # untrusted: harness CLI and local validation, started by `sc-worker shim` on request
          image: git.<domain>/strange-company/harness-claude-code@sha256:…
          imagePullPolicy: IfNotPresent
          command: ["/usr/local/bin/sc-worker", "shim"]
          env:
            - {name: SC_WORKSPACE, value: /workspace}
            - {name: SC_HARNESS_SECRETS_DIR, value: /var/run/sc/harness-secrets}
            - {name: HOME, value: /home/worker}
            - {name: TMPDIR, value: /tmp}
          resources:                 # profile minus the adapter slice; memory request = limit
            requests: {cpu: 400m, memory: 3840Mi, ephemeral-storage: 192Mi, strange.company/heavy-slot: "8"}
            limits: {cpu: 1750m, memory: 3840Mi, ephemeral-storage: 8Gi, strange.company/heavy-slot: "8"}   # ephemeral = ephemeralStorageLimit + workspaceSize (plan 51); all heavy-slot units on this container (CD-05)
          securityContext: *csc
          volumeMounts:
            - {name: workspace, mountPath: /workspace}
            - {name: home, mountPath: /home/worker}
            - {name: tmp, mountPath: /tmp}
            - {name: harness-creds, mountPath: /var/run/sc/harness-secrets, readOnly: true}
      volumes:
        - {name: workspace, emptyDir: {sizeLimit: 6Gi}}
        - {name: private, emptyDir: {medium: Memory, sizeLimit: 64Mi}}   # sc-worker only: SSH key copy, git-internals snapshot, commit message
        - {name: home, emptyDir: {sizeLimit: 512Mi}}
        - {name: tmp, emptyDir: {sizeLimit: 1Gi}}
        - name: creds                # sc-worker only
          secret: {secretName: sc-184-x3, defaultMode: 0440}
        - name: harness-creds        # harness only: gateway and egress tokens, nothing else
          secret: {secretName: sc-184-x3, defaultMode: 0440, items: [{key: gateway-token, path: gateway-token}, {key: egress-token, path: egress-token}]}
```

Per-execution Secret `agent-jobs/sc-184-x3`: type `Opaque`, same labels, `ownerReferences: [{apiVersion: batch/v1, kind: Job, name: sc-184-x3, uid: <uid>, controller: false, blockOwnerDeletion: false}]`, keys `task-token` (`token_use: runner`), `egress-token` (`token_use: egress`; both per `contracts/task-token.schema.yaml`), `gateway-token`, `deploy-key` (OpenSSH ed25519 private key), `known_hosts` (pinned Forgejo host key). Read-only roles get a read-only deploy key; no role ever gets an API token.

**Two-container isolation (review TDD-14; ADR-0402 amendment).** Every piece of untrusted code — the harness CLI, the model's tool calls and the repository's local validation commands — runs in the `harness` container, which mounts neither the deploy key nor the Runner task token. The `sc-worker` container holds them, performs all Git and Runner I/O, and runs no repository-controlled code (plan 27 §13.3). The containers coordinate only through files in the shared `workspace` emptyDir (plan 27 §7.4 shim protocol). Consequences: a hostile harness cannot push any ref, fetch other refs (SPEC §48), forge a result, or read the task token; the egress token lets it reach only the proxy's per-execution allowlist; the gateway token is bound to the execution's budget and models. Resource split: `sc-worker` takes a fixed 250m/256Mi slice; the `harness` container gets the profile's remaining CPU and memory and all heavy-slot units (pod totals equal the profile, so quotas and plan 37 maxima are unchanged).

The builder is a pure function `(execution, resolved policy) → (Job, Secret)` with golden-file tests; no field is copied from Hermes input. Exactly the two containers above (same image digest); no `nodeSelector`, affinity, tolerations, hostNetwork/PID/IPC, `shareProcessNamespace: true`, `hostAliases`, init containers, other sidecars or `imagePullSecrets` other than `sc-registry-pull` (plan 23). Pod labels always include `strange.company/execution-id` and `strange.company/task-id` (plans 36/40/41 select on them). The heavy-slot request is mandatory (plan 37 VAP `sc-worker-heavy-slot`; with two containers the VAP must check the pod **sum** equals `workerUnits`, request to plan 37).

## 14. Happy path

### 14.1 Task creation (`agent_start` with `task`, or `POST /v1/tasks`) — one atomic operation (SI-02; review RA-3)

1. Validate input (§7.1), idempotency check (`caller=hermes`, key). A replay returns the stored `AgentStartOutput`.
2. `repository` is managed. **Base and target** (review RA-1, TDD-10): `target_branch` (default `main`) must be in the merged `protectedBranches` ∪ {`main`} and be a branch protected in Forgejo; `base_ref` must be that branch name, or a 40-hex commit that is an ancestor of the branch tip, at most `spec.task.maxBaseLag` behind it (VerificationPolicy, default 20 commits and 24 h) and whose `.strange-company/{repo,policy,validation}.yaml` blobs equal the tip's. Tags, `agent/*`, `scratch/*`, `sc-archive/*`, `sc-quarantine/*` and unprotected branches → `BASE_REF_NOT_ALLOWED`; an unknown ref → `BASE_REF_NOT_FOUND`. Resolve to `base_commit` (40-hex).
3. Read `.strange-company/repo.yaml`, `policy.yaml`, `validation.yaml` at `base_commit` (ADR-0017); validate; merge repository policy into platform policy (tighten-only, plan 22).
4. Validate acceptance criteria against `acceptance-criteria.schema.yaml` (plan 21) and the mandatory-context size limit (plan 25 §9.4); verification mode and exemption request against `VerificationPolicy` (plan 22) — a requested exemption not allowed for this repository/class → `EXEMPTION_NOT_ALLOWED`.
5. Run **all** dispatch checks of §14.2 against the would-be task (they need no task row). Any failure returns its error and **nothing is created**.
6. One DB transaction: allocate `sc-<n>`; insert the task (`SPECIFIED`), `task_ac_versions` v1, transition `∅ → SPECIFIED` (actor `hermes`), the execution (`QUEUED`), the dispatch transition (e.g. `SPECIFIED → TEST_AUTHORING`) and the idempotency row (plan 29 §8.3). The idempotency row is written **only** in this transaction, so a crash before commit leaves nothing and a same-key retry creates exactly one task and one execution.

### 14.2 Dispatch (`agent_start`)

Semantic checks, in order (first failure returns its code):

1. Task exists (`TASK_NOT_FOUND`), not terminal (`TASK_TERMINAL`).
2. Effective emergency-stop level (max of DB, Git and Kubernetes sources, ADR-0452) ≥ `workers`, or `dispatchPaused: true` → `EMERGENCY_STOP` (`details.level` / `details.dispatch_paused`).
3. Policy loaded and dispatch enabled → else `POLICY_UNAVAILABLE`.
4. Role defined → else `ROLE_NOT_ALLOWED`.
5. Harness exists, `enabled`, allowed for the role, `stage: test-only` only on `spec.testRepositories`, and its image digest has a verified signature in the Runner's signature cache → else `HARNESS_NOT_ALLOWED` (details.allowed = permitted names; detail `unsigned`). Signatures are verified once per digest at policy load (cosign against `sc-cosign-trusted-keys`, plan 23, pulling through `sc-registry-pull` via the Traefik ClusterIP, CD-21) and cached in `harness_signatures`; dispatch itself makes no registry call (review RA-m14).
6. Profile ∈ role `allowedProfiles` and exists in the active set → `PROFILE_NOT_ALLOWED`.
7. Tier ∈ role `allowedModelTiers`; routing (plan 31) yields a model for (role, tier, harness API families); an explicit `model` must be in that allowlist → `MODEL_TIER_NOT_ALLOWED` / `MODEL_NOT_ALLOWED`; no eligible candidate → `ROUTING_UNRESOLVABLE` (409, `details.rejected`).
8. State-machine gate (plan 29 library): the transition implied by dispatching this role from the current state is allowed (e.g. implementer requires `RED_VERIFIED` and a recorded red commit) → `GATE_NOT_SATISFIED` with `details.task_state`; no non-terminal execution exists → `ALREADY_RUNNING`; attempts per class remain → `ATTEMPTS_EXHAUSTED`. Implementer dispatch is also refused (`GATE_NOT_SATISFIED`, detail `cumulative_diff_too_large`) when the cumulative `red..head` diff already exceeds the reviewer's `implementation_diff` cap (plan 25 §9.4; review TDD-13).
9. Budget pre-check exactly as plan 33 §14.1 (scopes `task:`, `execution:`, shared scopes; per-tier `minimumHeadroomUsd`) → else `BUDGET_EXCEEDED`; plan 33 §14.5 decides the task consequence (review COST-7).

When a dispatch is refused with `BUDGET_EXCEEDED` on the `task:` scope or with `ATTEMPTS_EXHAUSTED`, the Runner also fires the state-machine event `dispatch_refused` (transitions T132/T133 of `contracts/state-machine.yaml`; actor `runner`), which moves the task to `NEEDS_HUMAN` and posts `SCTaskNeedsHuman` (plan 33 §14.5). Refusals that waiting can fix (shared-scope budget, e-stop, concurrency) fire no event.
10. Context refs resolvable at base commit and not forbidden for the role → `CONTEXT_REF_INVALID` / `CONTEXT_PROJECTION_VIOLATION` (projection rules: plan 25).
11. Queue not full (`maxQueued`) → `CONCURRENCY_LIMIT`.

Then, in one transaction (for an existing task; for a new task this is step 6 of §14.1): allocate execution id `<task>-x<n>` (n = executions so far + 1), compute `attempt`, `phase` (role accounting), `branch_mode`/`start_commit` (plan 21 §14.9, executed by plan 27), record resolved image digest/profile/model/policy revision + commit, and for reviewers the `purpose`; insert execution `QUEUED`, apply the task transition (actor `hermes`), store the idempotency response. Return `AgentStartOutput`.

### 14.3 Dispatcher (QUEUED → STARTING → RUNNING)

**Admission to start** (review RA-7). An execution stays `QUEUED` — no Job exists — until all hold: `STARTING + RUNNING < maxWorkers`; the heavy-slot gate is open (plan 51 §9.7, CD-05): in `exclusive` mode no pod in `renovate` or `dr-tests` holds `strange.company/heavy-slot` units and the Woodpecker queue API (`GET /api/queue/info`, verify) reports no pending or running workflow in `ci-jobs`/`ci-build`; and at least `workerUnits` heavy-slot units are free on some node (`status.allocatable` minus requests of pods the Runner can see, plus its own). If the Woodpecker API is unreachable the gate is **closed** (fail closed), with metric `sc_runner_dependency_up{dep="woodpecker"} = 0` and alert `SCRunnerDependencyDown`. An execution QUEUED longer than `VerificationPolicy.spec.retry.infrastructure.queueTimeout` (plan 29; default 2 h) fails `QUEUE_TIMEOUT` (class infrastructure, attempt refunded per plan 29). Consequently the Job and pod deadlines start only when a slot is free, and `startTimeoutSeconds` applies only to image pull and container start. A QUEUED execution never has a Job, suspended or not (review TDD/G5).

**Maintenance blackout** (review GC-20): Runner config `dispatch.blackoutWindows` (from chart value `runner.maintenanceWindow`, rendered from cluster-vars `SC_MAINTENANCE_WINDOW`, the node-maintenance reboot window of plan 08) keeps new executions QUEUED from (window start − the longest active-set profile deadline − startup grace) until window end, so no worker is running when `sc-maintenance` reboots a node; queue age during a blackout does not count toward `QUEUE_TIMEOUT`. `sc-maintenance` additionally checks `sc_runner_executions{state=~"STARTING|RUNNING"} == 0` before rebooting (plan 08).

For the oldest admissible QUEUED execution (FIFO by `queued_at`):

1. Re-check emergency stop, policy, signature cache, budget and routing (state may have changed while queued; plan 31 §14.5 re-validation).
2. Compile the context packet (plan 25) and store it in S3 `context/<task>/<execution>/packet.json` (digest recorded).
3. Snapshot all branch heads **and tags** of the repository (`ref_snapshot`).
4. Upsert the `task:<id>` and `execution:<id>` rows of `ledger.budget_scopes` with their limits (plan 33 §7.2; plan 53 §8 contract item 1).
5. **Credential unit** (steps 5a–5d, review RA-2). 5a: create the Job (`suspend: true`, two containers, §13). 5b: generate the ed25519 deploy key in memory and register it on Forgejo (title `sc-<execution-id>`, read-only per role `forgeAccess`); record the `deploy_keys` row (id, fingerprint). 5c: mint the runner task token, the egress token (`contracts/task-token.schema.yaml`; exp = Job deadline + 300 s; `egress_allow` = `agent-base` ∪ the repository's delegated `egressHosts`, plan 36 §5) and the gateway token (claims per `gateway-token.schema.yaml`: `worker_id` = execution id, `phase`, `allowed_models`, `budget_scopes [execution:…, task:…]`, `policy_revision` = policy commit, `trace_parent` = the execution traceparent, plan 40); insert `ledger.gateway_tokens` and `tokens` rows (jti only). 5d: create the Secret `sc-<execution-id>` with ownerReference to the Job UID. Private key and token values exist only in memory between 5b and 5d and are never persisted (26-R-3).
6. Patch `suspend: false`. Execution → `STARTING` (`started_at`).
7. On the first authenticated `GET /v1/worker/packet` → `RUNNING`. The packet's `deadline_at` is computed from the pod's `status.startTime` + profile deadline (not from dispatch time). The packet is assembled from the execution row + stored context packet + base-revision policy excerpts.

**Crash resume** (each step writes a checkpoint column; review RA-2): steps 2–4 are idempotent and re-run. For the credential unit: if the Secret `sc-<execution-id>` **does not exist**, the Runner deletes any Forgejo deploy key titled `sc-<execution-id>`, revokes every `tokens`/`ledger.gateway_tokens` jti of the execution (`revoked_reason: dispatch_resume`), creates the Job if absent, and redoes 5b–5d with fresh material; if the Secret **exists**, it only performs step 6. A key is never "reused".

### 14.4 Result ingestion (C02, judged under the dispatch-time policy revision)

On `POST /v1/worker/result` (runner task token valid, unrevoked, `sub` = execution, execution `RUNNING`), or on recovery of a framed result from the archived `sc-worker` log (§14.5, review RA-13):

1. Size ≤ `results.maxBytes`; JSON-schema validation against `worker-result` (with the plan-30 handoff schema). Invalid → store raw body (scrubbed) as evidence, respond 422, violation `RESULT_SCHEMA_INVALID`, failure class `infrastructure`.
2. Identity: `task_id`, `execution_id`, `role`, `repository`, `base_commit`, `start_commit`, `branch` equal the execution record, and for reviewers `role_output.purpose` equals the dispatched purpose (absent only for `final_review`; review TDD-20) → else `RESULT_IDENTITY_MISMATCH`.
3. State allowed for the role (`expectedOutput.allowedResultStates`) → else `STATE_NOT_ALLOWED_FOR_ROLE`.
4. Store the result (`results` row, digest) and respond 202 (an identical replay returns 200; a different body returns 409). Then, **before judging** (review RA-4): delete the execution's Forgejo deploy key, revoke its egress and gateway tokens, and request Job deletion if the pod still runs (logs are captured first, §14.5). The **runner** task token is narrowed instead of revoked: from receipt it is accepted **only** on `POST /v1/worker/result` (packet and heartbeat answer 401), and only until ingestion completes or until `deadline_at + startupGraceSeconds`, whichever is later; a replay whose canonical body has the same digest returns 200 with the stored receipt, a different body 409. After that the token is revoked. This keeps the worker's result retry idempotent (plan 27 §7.1). Ingestion continues asynchronously via the outbox in two phases.

**Phase A — judge** (one read of Forgejo, one DB transaction; review RA-5):

5. **Git facts** (Forgejo, never the claim): for pushing roles, branch `agent/<task>` head == `commit` (`BRANCH_MISMATCH`/`COMMIT_MISMATCH`); the commit exists (`COMMIT_NOT_FOUND`); exactly one new commit whose parent is `start_commit` (`MULTIPLE_COMMITS`, `NOT_DESCENDANT_OF_START`); trailers `SC-Task`, `SC-Execution` present and correct; for `completed` with `commitRequiredOnCompleted` a commit exists. Read-only roles: branch head unchanged since `ref_snapshot`.
6. **Diff rules:** changed paths from `compare(start_commit, commit)`: none matches protected paths (`PROTECTED_PATH_VIOLATION`, ADR-0008); role allow/deny (`ROLE_DIFF_VIOLATION`; tester = `@testPaths`, `@fixturePaths`, `@testInfraPaths`, `@stubPaths`); implementer does not modify/delete paths in the protected acceptance-test manifest (`TEST_TAMPER`, detection semantics plan 22); deletions allowed only if `allowDeletions` (`DELETION_NOT_ALLOWED`); files and bytes within limits, and for implementers the **cumulative** `red..commit` diff ≤ the reviewer's `implementation_diff` cap (plan 25 §9.4) (`DIFF_TOO_LARGE`; review TDD-13).
7. **Secret scan** (also triggered by a worker-reported `scrub.redactions > 0`, plan 27 §13.4) of the diff, summary, handoff, validation output and role output: exact-match against all secret values the Runner issued to this execution plus the known-secret set (plan 34) → `SECRET_DETECTED`; the stored result and handoff are redacted (`handoff_redacted: true`).
8. **Ref audit** (ADR-0007): compare current branch heads **and tags** with `ref_snapshot`; any ref other than `agent/<task>` created/moved/deleted during the window and attributable to this execution → `REF_VIOLATION`. Attribution: a Forgejo push webhook during the window whose `pusher` is the repository owner (Stage-0 assumption, 26-AC-41) or whose new commits carry this execution's `SC-Execution` trailer; if the assumption fails verification, every unexplained non-human ref change during a write-key window is attributed to all executions holding write keys on that repository (fail closed). The same audit also runs (a) on **every** terminal path without a result (timeout, cancel, `JOB_LOST`, `RESULT_MISSING`, quarantine) and (b) in real time on each push webhook received while any write key of the repository is live (review SEC-2); a real-time hit triggers quarantine of the execution immediately.
9. **Harness report:** a `permission_denials` entry whose tool matches the role's forbidden-action list (push, commit, network fetch) → `PERMISSION_DENIAL` (policy).
10. Verdict: no violations → `outcome = accepted`; else `rejected`, failure class `policy`, code `POLICY_VIOLATION`. Execution terminal state: accepted + worker state `completed`/`blocked`/`test_spec_conflict`/`already_satisfied` → `SUCCEEDED`; worker `failed` → `FAILED` with class and code per plan 27 §14.15. **Budget and emergency-stop codes are derived from `ledger.denials` for the execution's token jti, never from the worker's claim** (plan 33 §14.5): any budget/e-stop denial row → class `budget` with the plan-33 code regardless of the reported reason; a worker claiming `budget_denied` without a denial row is classified infrastructure (`BUDGET_CLAIM_UNSUPPORTED`). A `SC_BUDGET_CONTENDED` denial (headroom held only by open reservations, plan 33 §14.2) is **not** a budget failure: class infrastructure, code `BUDGET_CONTENDED`, attempt refunded (plan 29 refund rule). Rejected → `FAILED` (policy).
11. Commit **one** transaction containing: `results.outcome`, `violations`, the fact snapshot (`facts jsonb`: head SHA, changed-paths digest, cumulative diff bytes, ref snapshot after), the execution's terminal state and failure class/code, and **all** phase-B outbox intents (quarantine/reset, foreign-tag deletion, log archive, Job deletion, state-machine event, PR update, alerts). Replays and `POST /v1/executions/{id}/reingest` never re-judge when `facts` exists; without it (e.g. after a PITR) they judge once and set `facts_rederived_at`.

**Phase B — apply** (outbox, keyed by `dedup_key`, idempotent):

12. Quarantine on rejection: tag `sc-quarantine/<execution-id>` at the rejected head and reset `agent/<task>` to the last accepted commit (plan 21 §14.9); for `SECRET_DETECTED` the commit is dropped **without** a tag; foreign branches are tagged and deleted; foreign tags are recorded in evidence and deleted so the mirror prunes them (plan 17); alert `SCWorkerPolicyViolation`.
13. Archive logs (§14.5); delete the Job.
14. Hand the outcome to the state machine (plan 29), actor `runner`: tester accepted → `RED_VERIFYING` and the evidence poller triggers RED (plan 22, ADR-0008); implementer accepted → `GREEN_VERIFYING`; reviewer results → plan 29 review transitions and `strange-company/review` (plan 55); `test_spec_conflict` → `TEST_SPEC_CONFLICT`; blocked → per plan 29; retryable failure → retry budget → `NEEDS_HUMAN` + `SCTaskNeedsHuman` on exhaustion.
15. PR creation/update (plan 55) after the first accepted pushing result: the PR body carries task id, ACs, test commit, RED evidence, implementation summary, GREEN evidence, known risks, handoff [SPEC §100].

### 14.5 Logs

- On pod terminal (Succeeded/Failed/DeadlineExceeded): read `pods/log?container=sc-worker&limitBytes=<logs.maxBytes>` (the `harness` container's own stdout carries only shim status lines and is not archived; harness output reaches the log only through `sc-worker`'s scrubbing framer, plan 27 §14.8); scrub (same scrubber as §14.4 step 7, framing markers retained); gzip; upload to `logs/<task>/<execution>/worker.log.gz`; record sha256 and `END` marker presence (missing END ⇒ killed mid-run, L-2). Then delete the Job (`propagationPolicy: Background`); the Secret is garbage-collected through its ownerReference.
- **Framed-result recovery** (review RA-13): if the execution has no stored result and the archived log contains exactly one complete `::SC-RESULT-BEGIN::`…`::SC-RESULT-END::` block (plan 27 §14.12) whose sha256 matches its header, the Runner ingests it exactly like a POST (§14.4, all facts re-derived). This turns a Runner outage during result posting into a delayed ingestion instead of `RESULT_MISSING`.
- `agent_logs` on a running execution: `pods/log?tailLines=N&limitBytes=65536`, scrubbed; on a finished execution: the archived object, last N lines. `source = unavailable` if neither exists.

### 14.6 Cancellation and timeouts

- `agent_cancel`: task transition (plan 29: `CANCELLED` or `NEEDS_HUMAN`), set `cancel_requested` on active executions, revoke the gateway token immediately (stops spend), the worker sees `cancel_requested` on its next heartbeat (≤ 30 s) and exits; after `cancel.graceSeconds` (60 s) the Runner captures logs and deletes the Job. QUEUED executions are cancelled without a Job. Execution → `CANCELLED`.
- Pod deadline (`DeadlineExceeded`) or `failed/harness_timeout` → `TIMED_OUT`.
- Every terminal path (cancel, timeout, `JOB_LOST`, `RESULT_MISSING`, quarantine) revokes the execution's tokens, deletes its deploy key within 60 s of becoming terminal, and runs the ref audit of §14.4 step 8 (review SEC-2, RA-m2).

### 14.7 Emergency stop (semantics owned by plan 33, ADR-0452)

Levels `none` < `workers` < `all`; no `dispatch` level (a dispatch-only pause is `dispatchPaused`). `PUT /v1/admin/emergency-stop` appends a row to `ledger.emergency_stop_events` (idempotent by key) in the same transaction as the audit record; the Gateway reads the DB source itself. The Runner computes the effective level as max(DB, Git `policy/budgets.yaml` `spec.emergencyStop`, ConfigMap `sc-emergency-stop` via informer, degraded) and applies its part: at ≥ `workers` the dispatcher halts, QUEUED executions are `CANCELLED`, running executions are cancelled with class `budget`, code `EMERGENCY_STOP`, and their tasks move to `BLOCKED` (`blocked_reason: emergency_stop`, `resume_state` per plan 33 §14.5). Releasing requires every source at `none`; tasks resume only by human transition. `GET /v1/admin/emergency-stop` returns `effective_level`, `sources` and `gateway_effective_level` (read from the Gateway status endpoint every 10 s); a disagreement for > 2 min raises `SCEmergencyStopNotApplied` (plan 33).

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Runner pod crash / restart | Deployment restarts; `up{job="agent-runner"}==0` | API/MCP unavailable briefly; Jobs keep running | On start: migrations, LKG policy, leader election, **reconcile**: list Jobs with the managed-by label and match to non-terminal executions; replay outbox; resume dispatcher checkpoints | `agent-runner-failure.md` |
| Runner down > 5 min | `SCRunnerDown` (critical) | No dispatch, no ingestion; `sc-worker` retries the result post for `min(10 min, deadline_at − now − 30 s)`, then exits 3 | Jobs finish; on recovery the framed result in the archived `sc-worker` log is ingested (§14.5, review RA-13); only executions with neither → `RESULT_MISSING` (infra) | `agent-runner-failure.md` |
| `sc-pg` unavailable | readiness check; `SCRunnerDatabaseDown` | Runner unready; nothing mutates | Retry connection with backoff; no in-memory mutation queue | `agent-runner-failure.md`, `cnpg-primary-loss.md` |
| Migration failure at startup | non-zero exit, `CrashLoopBackOff`, log `migration <n> failed` | New version never ready; old ReplicaSet unavailable (Recreate) | None (fail closed) | `agent-runner-failure.md` §G (§Rollback) |
| Policy invalid | loader errors; `SCRunnerPolicyInvalid` | dispatch refused (`POLICY_UNAVAILABLE`); reads work | LKG kept; auto-recover on next valid revision | fix policy via PR |
| Policy commit unknown | `SCRunnerPolicyCommitUnknown` | dispatch refused | retries every 30 s | inspect Flux/Forgejo |
| Forgejo API down | circuit breaker; `SCRunnerDependencyDown{dep="forgejo"}` | task creation/dispatch/ingestion stall | outbox retries; executions keep their deadline | `restore-forgejo.md` |
| Woodpecker down | evidence poller errors | tasks wait in `*_VERIFYING` | poller retries; plan 22 verification timeout → infra failure | `woodpecker-failure.md` |
| Gateway down | token insert fails / workers' model calls fail | dispatch blocked (`DEPENDENCY_UNAVAILABLE`); running workers fail `model_unavailable` | infra retry budget | plan 53 procedure |
| Image pull failure | Pod `ErrImagePull`/`ImagePullBackOff` > 120 s | execution cannot start | `FAILED` `IMAGE_PULL` (infra), alert `SCExecutionInfraFailuresHigh` if > 3/h | check plan 23 registry |
| STARTING > 600 s (image pull / container start; heavy-slot waiting cannot occur because Jobs are created only when units are free, §14.3) | watchdog | slot held | `FAILED` `START_TIMEOUT` (infrastructure), delete Job | — |
| QUEUED > `spec.retry.infrastructure.queueTimeout` (plan 29, default 2 h; heavy-slot gate closed, no free units, `maxWorkers` reached) | dispatcher | no work | `FAILED` `QUEUE_TIMEOUT` (infrastructure, attempt refunded) | `agent-runner-failure.md` |
| Woodpecker queue API unreachable | gate check | heavy-slot gate closed (fail closed) | executions stay QUEUED; `SCRunnerDependencyDown{dep="woodpecker"}` | `woodpecker-failure.md` |
| Crash inside the credential unit (§14.3 step 5) | checkpoint without Secret | possible orphan key/tokens | resume rule: delete key, revoke jtis, regenerate (§14.3) | — |
| Crash between judge and apply (§14.4) | outbox entries pending | verdict already persisted | apply phase replays from outbox; never re-judges | — |
| Hermes disabled by an operator | `hermes_state.hermes_disabled` | MCP returns 401 to every bearer; no re-render | none until `:enable` | `hermes-failure.md` §H |
| Heartbeat stale > 180 s, pod running | watchdog | possible hung worker | event `heartbeat_lost`; no kill (deadline bounds it) | `quarantine-worker.md` if suspicious |
| Job/pod vanished (eviction, node loss) | informer: Job missing or pod `Evicted` for non-terminal execution | work lost | `FAILED` `JOB_LOST` (infra); revoke creds; retry per policy | — |
| Pod finished, no result | Job complete + no `results` row after 30 s and no framed result in the archived log | — | `FAILED` `RESULT_MISSING` (infra); ref audit runs | — |
| Result rejected (violation) | ingestion | branch quarantined | `POLICY_VIOLATION`; alert `SCWorkerPolicyViolation`; revoke | `quarantine-worker.md` |
| Secret found in result/log | scrubber | — | redaction, credential revocation, `SECRET_DETECTED` | `quarantine-worker.md` |
| Orphan deploy key (sweeper failing) | `sc_runner_deploy_keys_orphaned > 0` for 10 min → `SCRunnerOrphanDeployKeys` | standing write credential | sweeper retries every 60 s | `quarantine-worker.md` §M |
| Orphan Jobs (no execution row) | reconcile | resource use | delete after logging their labels; alert `SCRunnerOrphanJobs` | — |
| Log archive failing (S3) | upload errors; `SCRunnerLogArchiveFailing` | Job deletion delayed (TTL 6 h fallback) | retry every 60 s; after TTL logs are lost and `logs_status = lost` recorded | `object-storage-outage.md` |
| Alertmanager unreachable | publisher errors | notifications delayed | retry; metric `sc_runner_alert_publish_failures_total`; Prometheus rules on Runner metrics still fire | `monitoring-failure.md` |
| Webhook signature invalid | 401 count | missed events | 5-min resync covers; alert if > 10/h | rotate `SC-RUNNER-WEBHOOK` |
| Unexplained ref change | ref audit | possible escape | `SCRunnerUnexplainedRefChange` (warning), fail-closed attribution rule | `quarantine-worker.md` |
| Task stuck (no transition 24 h in a non-human state) | watchdog | | `SCTaskStuck` | `stuck-task-recovery.md` (plan 29) |
| Leader lost (split brain risk) | Lease renew failure | loops stop | process exits after renew deadline; restarted | — |
| Hermes credential rotation fails | `SCHermesCredentialRotationFailed` | old creds valid until overlap ends | retry hourly | `hermes-failure.md` |

## 16. Retry behavior

- Task-level retries (merit, infrastructure, policy, budget classes) are decided by plan 29's retry policy; the Runner supplies the class and code. Defaults it assumes: overall ≤ 5 autonomous attempts per task [SPEC §74], consecutive infrastructure failures ≤ 3, policy violations: 0 automatic retries (→ NEEDS_HUMAN).
- Retries carry evidence: the next execution's context includes `prior_attempt_evidence` (plan 25) and, for green retries, may escalate the tier per routing (plan 31). The Runner refuses a retry whose (role, harness, model, start_commit, context digest) equals the previous failed attempt after a deterministic failure (`ATTEMPTS_EXHAUSTED` detail `identical_replay`) [SPEC §74].
- Side effects: outbox retries with exponential backoff 1 s × 2ⁿ capped at 5 min, unbounded in count but alerting after 30 min of failure (`SCRunnerOutboxStalled`).
- Worker HTTP: the worker API is idempotent; sc-worker retry policy is plan 27.

## 17. Idempotency behavior

- MCP/API mutations: idempotency keys (§7.2); replay returns the stored response.
- Task creation is atomic with its idempotency row (one transaction). Task ids are never reused.
- Dispatch steps are checkpointed (§14.3); Kubernetes objects are keyed by name; deploy keys by title; PRs by `head = agent/<task>`; statuses are idempotent by (sha, context); labels are set-semantics; alerts by label set.
- Results: one per execution (`results` PK); identical replay → 200, different → 409. A framed result recovered from the log is ingested only if no POSTed result exists.
- Ingestion is split into judge (one transaction persisting verdict, facts and all outbox intents) and apply (outbox). Replays and re-ingestion never re-judge when `facts` exists (review RA-5).
- Dispatch credentials are one resumable unit with explicit resume rules (§14.3, review RA-2).
- Transitions: guarded by `tasks.version` (optimistic lock); a lost race returns `INVALID_STATE`.
- Webhooks deduplicated by `X-Forgejo-Delivery`.
- Migrations: versioned, checksummed, applied once.

## 18. Bootstrap behavior

- The Runner is deployed by Flux through the `strange-company` HelmRelease (plan 54) after P11 (images exist). Prerequisites rendered elsewhere: namespaces, PriorityClasses, VAPs (plan 37), default-deny NetworkPolicies (plan 36), `sc-pg` Cluster (plan 12), SOPS Secrets (plan 35), `sc-runner` Forgejo account + token (plan 16), Woodpecker token (plan 20).
- First start: wait for `sc-pg` → acquire Lease → `pg_advisory_lock(7453271)` → apply migrations → unlock → load policy and company context → verify the organization-level webhooks declared by plan 16 (CD-18; the Runner never creates hooks, drift is reported through the projection-health metric) → ensure Hermes credentials Secret (ADR-0401, unless disabled) → serve. Until all prerequisites exist the pod is unready; there is no partial mode.
- A fresh install must not depend on hand-created Secrets (L-4): every Secret the Runner reads is rendered from SOPS in Git or created by forge-init with a documented name; a missing or empty file is a startup error naming the file.

## 19. Upgrade behavior

- Deployment strategy `Recreate` (single writer during migrations; MVP has one replica).
- Migrations are **forward-only**, embedded in the binary (`src/runner/migrations/NNNN_*.sql`), each in its own transaction, recorded with a checksum; a changed checksum of an applied migration is a startup error. Schema changes follow expand/contract: release N adds (nullable columns, new tables); release N+1 may drop only what release N stopped using. Therefore binary N−1 runs against schema N, and rollback = redeploy the previous image (no down-migrations).
- In-flight Jobs survive Runner upgrades: tokens are validated against the DB + signing key, both unchanged. Contracts are `v1alpha1`; a breaking worker-API change requires sc-worker images and the Runner to support both versions for one release (packet `schema_version`).
- Upgrade test: the k3d chart upgrade test (plan 54) runs a fake execution across a Runner upgrade and asserts it completes (26-AC-24).

## 20. Recovery behavior

- `sc-pg` PITR (plan 12) restores task state to ≤ 15 min RPO. After restore, the Runner reconciles: executions whose Jobs no longer exist → `JOB_LOST`; Jobs without rows → orphan deletion; deploy keys titled `sc-*` unknown to the DB → deleted by the sweeper; gateway tokens not in the restored ledger are unusable by construction (plan 53). Task states after the restore point are re-derived where Git/Woodpecker evidence exists (evidence poller re-polls pipelines referenced by restored tasks); otherwise tasks resume from their restored state.
- **Id floor after any PITR** (review RA-6): before dispatch resumes, the Runner (startup check, also `agent-runner-failure.md` §I) scans Forgejo refs and PRs of every managed repository (`agent/sc-*`, `sc-archive/sc-*`, `sc-quarantine/sc-*`, PR heads) and the S3 evidence prefixes (`logs/`, `results/`, `context/`) for the highest task number, then sets `task_seq` to that maximum + 100 and records the jump in `id_floor` and as a transition note. Per-task execution counters likewise start above the highest `sc-<n>-x<m>` found. No restored task adopts a PR, branch or evidence object of a lost one. Dispatch stays disabled (`POLICY_UNAVAILABLE`-class refusal `ID_FLOOR_PENDING`) until the scan has completed.
- Evidence objects (S3) are immutable and keyed by task/execution, so a restored DB re-links them; orphan objects are retained (plan 43).
- Total loss: black start (plan 45) restores `sc-pg`, then the Runner starts normally; no in-flight work is assumed recoverable.

## 21. Observability

**Metrics** (`/metrics` on the port named `metrics`; no task/execution ids as labels). Names, types, labels and buckets are exactly plan 38 §21.2 (authoritative catalogue) plus the plan-55 forge metrics; `make lint-metrics` checks the exposition against the catalogue.

| Metric | Type | Labels |
|---|---|---|
| `sc_runner_tasks` | gauge | `state` (non-terminal states) |
| `sc_runner_executions` | gauge | `state` |
| `sc_runner_queue_depth` | gauge | — |
| `sc_runner_queue_wait_seconds` | histogram | `role` |
| `sc_runner_queue_oldest_seconds` | gauge | — |
| `sc_runner_execution_duration_seconds` | histogram | `role`, `harness`, `outcome` |
| `sc_runner_results_total` | counter | `role`, `harness`, `outcome`, `failure_class` |
| `sc_runner_transitions_total` | counter | `from`, `to` |
| `sc_runner_needs_human_total` | counter | `reason` |
| `sc_runner_dispatch_total` / `sc_runner_dispatch_rejected_total` | counter | `role` / `code` |
| `sc_runner_violations_total` | counter | `code` |
| `sc_runner_policy_valid` | gauge | — |
| `sc_runner_policy_revision_info` | gauge (=1) | `revision` (12-hex prefix) |
| `sc_runner_policy_commit_resolved` | gauge | — |
| `sc_runner_deploy_keys_active` / `sc_runner_deploy_keys_orphaned` | gauge | — |
| `sc_runner_outbox_pending` / `sc_runner_outbox_oldest_seconds` | gauge | `kind` |
| `sc_runner_dependency_up` | gauge | `dep` |
| `sc_runner_leader` | gauge | — |
| `sc_runner_log_archive_failures_total` | counter | — |
| `sc_runner_mcp_requests_total` | counter | `tool`, `code` |
| `sc_runner_api_requests_total` | counter | `route`, `method`, `code` |
| `sc_runner_admin_requests_total` | counter | `route` |
| `sc_runner_executions_stuck` | gauge | `state` |
| `sc_runner_task_duration_seconds` | histogram | `verification_mode`, `final_state` |
| `sc_runner_retries_total` | counter | `role`, `failure_class` |
| `sc_runner_tdd_compliance_ratio` | gauge | — |
| `sc_runner_merges_without_provenance_total` | counter | — |
| `sc_runner_reconcile_last_success_timestamp_seconds` | gauge | — |
| `sc_runner_alert_posts_total` | counter | `alertname`, `result` |
| `sc_runner_orphan_jobs_deleted_total` | counter | — |
| `sc_runner_unexplained_ref_changes_total` | counter | — |
| `sc_runner_quarantined_refs` | gauge | — |
| `sc_runner_hermes_credential_rotation_failures_total` | counter | — |
| `sc_tasks_stuck` (plan 29 name) | gauge | `state` |
| `sc_task_events_rejected_total` / `sc_task_invariant_violations` / `sc_task_commands_rejected_total` / `sc_runner_human_transitions_total` (plan 29) | counter / gauge / counter / counter | `event`,`code` / — / — / — |
| `sc_verification_runs_total` (plan 22 name) | counter | `verification_phase` (`red`, `green`, `judge_validate`, `judge`), `outcome` (`red_valid`, `red_invalid`, `already_satisfied`, `partially_satisfied`, `baseline_failing`, `inconclusive`, `tamper_detected`, `green_pass`, `green_fail`, `judge_valid`, `judge_invalid`, `judging_pass`, `judging_fail`, `infra_error`, `timeout`, `evidence_rejected`) |
| `sc_verification_duration_seconds` (plan 22 name) | histogram | `verification_phase` |
| `sc_forge_status_forged_total` (plan 55; watcher runs in the Runner, CD-07) | counter | `context` |
| `sc_merge_audits_total` (plan 55) | counter | `outcome` (`verified`, `bypass`, `human_non_task`, `direct_push`, `status_forged`) |
| `sc_forge_projection_last_resync_timestamp_seconds` (plan 55) | gauge | — |
| `sc_context_sections_withheld_total` / `sc_context_compile_failures_total` / `sc_context_compile_seconds` / `sc_context_bytes` (plan 25) | counter / counter / histogram / histogram | `reason` / `reason` / `role` / `role` |

**Logs:** JSON to stdout, fields `ts`, `level`, `msg`, `task_id`, `execution_id`, `trace_id`; no request bodies.
**Traces:** span names are those of `contracts/telemetry.schema.yaml` and plan 40 §7.1: `sc.task` (zero-duration anchor; `tasks.trace_id`/`anchor_span_id` persisted at creation), `sc.task.transition`, `sc.execution` (`executions.span_id` allocated at dispatch; traceparent delivered in the packet and the gateway token), `sc.execution.dispatch`, `sc.execution.ingest`, `sc.verification`, `sc.task.completed`, `sc.runner.api`, `sc.runner.mcp`; attributes per plan 40 (never prompts) [SPEC §93, §94].
**Alerts** (IDs for plan 42's catalog): `SCRunnerDown` (critical, `up==0` 5 m), `SCRunnerNotReady` (warning, 10 m), `SCRunnerDatabaseDown` (critical), `SCRunnerPolicyInvalid` (critical), `SCRunnerPolicyCommitUnknown` (warning), `SCRunnerDependencyDown` (warning, 10 m, per dep), `SCRunnerOutboxStalled` (warning, oldest > 30 m), `SCRunnerOrphanDeployKeys` (critical, > 0 for 10 m), `SCRunnerOrphanJobs` (warning), `SCExecutionInfraFailuresHigh` (warning, > 3 infra start failures / h), `SCRunnerLogArchiveFailing` (warning, 30 m), `SCRunnerUnexplainedRefChange` (warning), `SCRunnerQueueBacklog` (warning, oldest QUEUED > 2 h), `SCRunnerAdminAccess` (info, break-glass audit), `SCWorkerPolicyViolation` (warning), `SCWorkerQuarantined` (warning), `SCTaskNeedsHuman` (warning, notification; SPEC §97), `SCTaskStuck` (warning), `SCHermesCredentialRotationFailed` (warning).

Dashboards: plan 39 uses the gauges/histograms above for queue depth, active workers, durations, red/green transitions, retries, failures, Needs Human [SPEC §92].

## 22. Testing strategy

All deterministic, no paid model calls [SPEC §104].

- **Unit (Go):** forbidden-field detection; validation order; every error code path; policy loader (each invariant has a failing fixture); revision digest stability; Job builder golden files per (role, profile set); result-ingestion rules with a fake Git oracle; state-machine integration via plan 29 library; scrubber corpus (tokens, PEM, provider keys); idempotency (replay, conflict).
- **Contract (F02):** every MCP output produced in tests validates against `mcp-surface.schema.yaml`; every API response against `runner-api.openapi.yaml`; generated `tools/list` equals `x-mcp-tools`.
- **DB:** migrations up from empty and from every previous release snapshot (testcontainers PostgreSQL at the CNPG major version); N−1 binary smoke against schema N.
- **envtest/k3d:** Job builder output admitted by the plan-37 VAPs and PSA restricted; RBAC denial tests (Runner SA cannot create pods directly, cannot touch other namespaces).
- **k3d integration (C02/C03):** Runner + sc-pg (CNPG) + Forgejo (test instance) + fake Gateway + fake harness scenarios of plan 28; Woodpecker replaced by a stub evidence source until C04.
- **Fault injection:** failpoints (build tag `failpoints`) after each dispatch checkpoint (§14.3 steps 2–6), between judge and apply (§14.4), and inside task creation (§14.1 step 6); each run must end with exactly one Job, one key whose fingerprint matches the Secret, zero unrevoked orphan jtis, and one verdict (reviews RA-2, RA-3, RA-5).
- **Coverage matrix** (review RA-m1): a unit test enumerates every `Violation.code` of `mcp-surface.schema.yaml`, every execution failure code in §15 and plan 27 §14.15, and every `HarnessVerdict`, and fails if a value maps to no fake scenario (plan 28) or named unit test.
- **Forgejo stub:** a recorded-fixture stub implementing the §7.3 calls lets GATE-C01 run without C03; the same ACs re-run against the real Forgejo at C03 (26-AC-44).
- **Live (C10):** fake end-to-end flows on the cluster (plans 21, 46).

## 23. Acceptance criteria

### GATE-F04

- [ ] **26-AC-1** `make policy-check` validates every file under `policy/` against its schema by `kind` and exits 0 on the committed policy; each fixture in `src/policy/testdata/invalid/` (one per invariant of §9.2 step 3 and each schema `x-invalid-examples`) makes it exit non-zero with the fixture's expected error code printed.
- [ ] **26-AC-2** The revision digest of `src/policy/testdata/valid/` equals the value recorded in `testdata/valid.digest`; renaming a file or changing one byte changes it; reordering directory entries does not.
- [ ] **26-AC-3** Loader unit test: loading an invalid revision after a valid one leaves `Snapshot().Revision` equal to the valid digest, `DispatchEnabled() == false` and `LastError()` non-empty; loading a valid revision afterwards re-enables dispatch.
- [ ] **26-AC-4** A repository policy that loosens a platform limit (fixture `repo-policy-loosen.yaml`) is rejected by the merge function with `POLICY_LOOSENING` (shared with plan 22's tests).
- [ ] **26-AC-5** The loader rejects a role referencing a harness that does not list the role in `supportedRoles`, a profile missing from the `constrained` set, a production harness with `ignoresRepoConfig: false`, and `disableWorkerTimeout` on a production harness (4 fixtures, 4 distinct errors).

### GATE-C01

- [ ] **26-AC-10** Fresh `sc-pg`: the Runner starts, `schema_migrations` contains every embedded migration, and `GET /readyz` returns 200 `{"ready":true,...}`; starting a second Runner pod concurrently against the same DB applies migrations exactly once (advisory lock; asserted by row count).
GATE-C01 runs against the Forgejo stub of §22 (review RA-11); the same task-creation ACs re-run live at C03 (26-AC-44).

- [ ] **26-AC-11** `POST /v1/tasks` (via port-forward, admin token, Forgejo stub) with the example body from `mcp-surface.schema.yaml` returns 201, a `task_id` matching `^sc-[0-9]+$`, and `GET /v1/tasks/{id}/transitions` shows `∅→SPECIFIED` and `SPECIFIED→TEST_AUTHORING` with actor values recorded.
- [ ] **26-AC-12** Repeating the same request with the same `Idempotency-Key` returns 200 with an identical body and `idempotent_replay: true`; changing one byte of the body with the same key returns 409 `IDEMPOTENCY_CONFLICT`; the tasks table row count is unchanged.
- [ ] **26-AC-13** For each field in the §7.1 forbidden set, an `agent_start` containing it returns `FORBIDDEN_FIELD` with `field` equal to the JSON pointer; an unknown non-forbidden field returns `VALIDATION_FAILED`; no Job is created (`kubectl -n agent-jobs get jobs -l app.kubernetes.io/managed-by=agent-runner` count unchanged).
- [ ] **26-AC-14** `agent_start` with harness `nonexistent` returns `HARNESS_NOT_ALLOWED` with `details.allowed` listing the role's harnesses; with `fake-red-valid` on a repository not in `testRepositories` also returns `HARNESS_NOT_ALLOWED`; no Job is created.
- [ ] **26-AC-15** `agent_start` role `implementer` on a task in `TEST_AUTHORING` returns `GATE_NOT_SATISFIED` with `details.task_state = TEST_AUTHORING` [SPEC §52, §106].
- [ ] **26-AC-16** The admin API is not reachable on the pod IP: `kubectl -n strange-company exec deploy/agent-runner -- …` is not used; instead a test pod in `monitoring` gets connection refused on `<podIP>:8080`, while port-forward to 8080 succeeds with the admin token and returns 401 without it.
- [ ] **26-AC-50** Admin identity (CD-30): a request with a valid per-operator credential and an `X-SC-Actor: human:mallory` header is rejected with 400 `VALIDATION_FAILED` and writes no transition; the same request without the header succeeds and the resulting `transitions.actor` equals the credential's registry identity (`human:<login>`) with `credential_kid` = its kid, and the `admin_audit` row has the same actor and kid; with the break-glass credential the actor is `breakglass:runner-admin`; after `POST /v1/admin/credentials/{kid}:revoke`, the next request with that credential gets 401 even though the registry file still lists it.
- [ ] **26-AC-17** With a deliberately invalid `policy/harnesses.yaml` rolled out in a test cluster: `GET /v1/policy/status` returns `status: degraded` with the previous revision, `agent_start` returns `POLICY_UNAVAILABLE`, `agent_status` still returns 200, and `sc_runner_policy_valid` is 0 within 60 s (the alert path is asserted at C11 by plan 42's catalogue test, review RA-11).
- [ ] **26-AC-18** `agent_list` with `updated_since` returns `NOT_IMPLEMENTED` while `features.updatedSinceFilter` is false (26-R-5).
- [ ] **26-AC-19** `GET /metrics` contains no label named `task_id`, `execution_id` or `jti` (grep on exposition output returns nothing).
- [ ] **26-AC-33** Base-ref restriction (RA-1): `agent_start` with `task.base_ref` = `sc-quarantine/sc-1-x1`, = `agent/sc-1`, = a tag, and = a 40-hex commit 30 commits behind `main` each returns `BASE_REF_NOT_ALLOWED`; `target_branch: feature/x` (unprotected) returns `BASE_REF_NOT_ALLOWED`; the tasks table row count is unchanged.
- [ ] **26-AC-34** Atomic create-and-dispatch (RA-3): `agent_start` with `task` and a disabled harness returns `HARNESS_NOT_ALLOWED` and the tasks count is unchanged; with failpoint `create_before_commit` the request fails, and a retry with the same key creates exactly one task and one execution.
- [ ] **26-AC-36** `POST /v1/admin/gateway-tokens:verify` returns a token whose `exp − iat ≤ 600`, whose `ledger.gateway_tokens` row has `task_id = 'sc-0'`, and whose `revoked_at` is set within 60 s after `expires_at`.
- [ ] **26-AC-20** The rendered Runner Role/RoleBindings equal the §13 table exactly (helm-unittest snapshot, plan 54), and `kubectl auth can-i --as=system:serviceaccount:strange-company:agent-runner create pods -n agent-jobs` → `no`; `… create jobs -n default` → `no`; `… get secrets -n strange-company` → `no`.

### GATE-C02

- [ ] **26-AC-21** Golden test: the Job and Secret built for each (role, standard/constrained profile) equal the committed golden YAML; the Job for profile `implementation` contains exactly the §13 security fields (`yq` assertions on `runAsNonRoot`, `allowPrivilegeEscalation`, `capabilities.drop`, `seccompProfile`, `automountServiceAccountToken: false`, no `hostPath`).
- [ ] **26-AC-22** On k3d with plan-37 admission: a `fake-success` tester execution on `sc-e2e/fake-target` reaches `SUCCEEDED`; during the run `kubectl -n agent-jobs get secret <execution-id> -o jsonpath='{.metadata.ownerReferences[0].kind}'` is `Job`; after completion the Job and Secret are deleted within 120 s and `logs/<task>/<execution>/worker.log.gz` exists in the evidence bucket with the recorded sha256.
- [ ] **26-AC-23** With `maxWorkers: 1`, two `agent_start` calls on different tasks yield one `STARTING/RUNNING` and one `QUEUED` execution (`queue_position: 1`); the second starts only after the first is terminal.
- [ ] **26-AC-24** Runner restart mid-execution: delete the Runner pod while a `fake-timeout` (long-running) execution is RUNNING; after restart the execution is still tracked, completes as `TIMED_OUT`, its deploy key is deleted, and no duplicate Job exists.
- [ ] **26-AC-25** `agent_cancel` on a RUNNING execution: within 90 s the execution is `CANCELLED`, the gateway token row has `revoked_at` set, the deploy key is gone from `GET /repos/{o}/{r}/keys`, and the pod log is archived.
- [ ] **26-AC-26** Every C02-relevant fake scenario of plan 28 (`failure`, `timeout`, `deadline-exceeded`, `blocked`, `push`, `push-no-result`, `malformed-result`, `protected-path-edit`, `push-other-branch`, `secret-in-handoff`, `tester-touches-implementation`, `large-diff`, `not-descendant`, `wrong-commit-claim`, `ignore-cancel`) yields the execution state, failure class and violation code listed in plan 28's scenario table (table-driven e2e test, exit 0).
- [ ] **26-AC-27** After `fake-secret-in-handoff`, `GET /v1/tasks/{id}/result` shows `handoff_redacted: true`, and neither the archived log nor the stored result contains the issued gateway token value (byte search returns no match).
- [ ] **26-AC-29** Two-container isolation (TDD-14): in a running `fake-success` pod, `kubectl exec -c harness -- ls /var/run/sc/secrets` fails (no such directory), `ls /var/run/sc/harness-secrets` lists exactly `egress-token` and `gateway-token`, and the pod spec has `shareProcessNamespace` unset/false; `fake-forge-then-push` (harness attempts `git push` of a tag and a `POST /v1/worker/result`) ends `SUCCEEDED` with no new tag on the repository and no 409 at the Runner (RA-4).
- [ ] **26-AC-38** Failpoint runs (§22) at each dispatch checkpoint and between judge and apply each end with exactly one Job, one deploy key whose fingerprint equals the Secret's public key, zero unrevoked jtis of discarded material, and an unchanged persisted verdict (RA-2, RA-5).
- [ ] **26-AC-39** Heavy-slot queueing (RA-7): with a fixture CI pod holding the heavy slot for 15 min (exclusive mode), a dispatched execution stays `QUEUED` (no Job object exists), then starts and completes without `START_TIMEOUT`/`TIMED_OUT`; with the Woodpecker API blocked the execution stays `QUEUED` and `sc_runner_dependency_up{dep="woodpecker"} = 0`.
- [ ] **26-AC-45** Framed-result recovery (RA-13): with the Runner scaled to 0 for 5 min while a `fake-push` execution finishes, after scale-up the execution is `SUCCEEDED` (result `source = log_recovery`), not `RESULT_MISSING`.
- [ ] **26-AC-46** Coverage matrix test (§22) passes: no unmapped violation code, execution failure code or harness verdict.
- [ ] **26-AC-35** Contract: every task token minted in the C01 test run decodes to claims that validate against `contracts/task-token.schema.yaml`; a `token_use: egress` token is rejected on the worker listener with 401 (RA-8).
- [ ] **26-AC-49** Blackout (GC-20): with a test blackout window starting in 10 min and the `tests` profile active, an `agent_start` stays `QUEUED` until the window ends and then starts; no execution is `RUNNING` during the window.
- [ ] **26-AC-28** Admission independence: submitting the golden Job with `serviceAccountName: default`, with `privileged: true`, and with an image not in the registry, using the Runner's SA identity (`--as`), is each denied by admission (plan 37) — the Runner is not the only defense [SPEC §85, §105].

### GATE-C03 (adapter mechanics; semantics in plan 55)

- [ ] **26-AC-48** Release provenance (SEC-11): on a fixture `promote/*` PR whose provenance names a non-existent pipeline, `strange-company/release-provenance` = `failure`; for a genuine fixture release it is `success`; `GET /v1/ci/push-records?repo=…&ref=refs/tags/v0.0.1` returns the pusher of the fixture tag with `pusher_kind` correct.

- [ ] **26-AC-40** Contract test against a Forgejo container at the pinned version: every call in the §7.3 table succeeds with the documented path and the `sc-runner` token scopes (verify paths at implementation; the test is the re-verification).
- [ ] **26-AC-41** Deploy-key pusher attribution: push to `agent/sc-1` with a deploy key and as a human user; the received push webhooks show `pusher` = repository owner for the deploy-key push and the human login for the human push. If not, the fail-closed attribution path is enabled by config and `fake-push-other-branch` still yields `REF_VIOLATION`.
- [ ] **26-AC-42** Deploy keys: during an execution `GET /repos/{o}/{r}/keys` lists exactly one key titled `sc-<execution-id>` with `read_only` matching the role; within 60 s after any terminal state (and immediately on result receipt) the key is absent; killing the Runner during this window still results in deletion within 60 s of restart (sweeper; RA-m2).
- [ ] **26-AC-44** The GATE-C01 task-creation ACs (26-AC-11, 26-AC-12, 26-AC-33, 26-AC-34) pass against the real Forgejo test instance.
- [ ] **26-AC-43** A webhook with an invalid `X-Forgejo-Signature` gets 401 and is not processed; a duplicate delivery id gets 204 and is processed once.

### GATE-C08 (Runner side; Hermes side in plan 24)

- [ ] **26-AC-30** An MCP client using protocol revision 2025-11-25 (with `initialize`) and one using 2026-07-28 (stateless) both list exactly the six tools; the published `inputSchema` for each is byte-equal (canonical JSON) to the corresponding `$defs` entry.
- [ ] **26-AC-31** MCP calls without a bearer, with a wrong bearer, and with the admin token each return 401; the worker task token is rejected on `:8081` and the Hermes token on `:8082` (cross-listener credential test).
- [ ] **26-AC-32** No MCP tool name other than the six exists (`tools/list` count = 6) and calling `agent_transition` returns a JSON-RPC method/tool-not-found error.
- [ ] **26-AC-37** `POST /v1/admin/hermes-credentials:revoke` (RA-10): afterwards `agent_list` with the last issued MCP bearer returns 401 for 10 minutes, Secret `hermes/hermes-managed-env` is not re-rendered (resourceVersion unchanged over 10 min), and `hermes_state.hermes_disabled = true`; `:enable` restores a working MCP call within 2 min.

## 24. Explicit non-goals

- No UI; the human surfaces are Hermes, Forgejo, Grafana (master plan §15).
- No Authentik-backed human login to the Runner API in MVP (ADR-0400).
- No autonomous merge (ADR-0019), no cross-repository task DAGs, no worker-to-worker messaging [SPEC §127].
- No active-active multi-replica processing (Lease leader only).
- No direct provider calls; no cost computation (plans 32/53).
- RED/GREEN classification logic (plan 22), state-machine semantics (plan 29), PR/label content (plan 55) are consumed, not defined here.

## 25. Open questions

1. **Emergency-stop level names** — resolved: `none|workers|all` plus `dispatchPaused` (plan 33, ADR-0452).
2. **Deploy-key pusher attribution in Forgejo webhooks** (blocking: no). Assumption verified by 26-AC-41; fail-closed fallback defined. Owner: 26/55.
3. **Runner writing a Secret and patching a Deployment in `hermes`** (ADR-0401) expands Runner authority slightly. Proposed resolution: accept (name-restricted except `create`); the alternative (operator-delivered SOPS Hermes gateway token) conflicts with plan 53's Runner-rotated orchestrator token. Blocking: no. Owner: 26/24/53.
4. **Gateway admin API** — resolved: none exists; coordination through `ledger.emergency_stop_*` and the read-only status endpoint (ADR-0452).
5. **Woodpecker evidence timeout values** live in plan 22; the Runner only executes them. Blocking: no.
6. **Who writes `spent` on cost adjustments** — resolved by plans 32/33: the Runner inserts the adjustment row only; the Gateway leader applies it.
7. **Two-container worker pod** (review TDD-14; blocking: no). Requires plan 37 to admit exactly the `sc-worker` + `harness` container pair (same digest, credentials volume only in `sc-worker`, heavy-slot sum check) and plan 36 to accept `token_use: egress` tokens (`aud: [sc-egress-proxy]`). Owner: 37/36.
8. **Egress-token revocation** (review RA-m14; blocking: no). The proxy has no revocation list in MVP; a revoked egress token remains usable for package fetches until `exp` (≤ deadline + 5 min). Accepted residual risk unless plan 36 adds a jti deny-list fed by the Runner.
