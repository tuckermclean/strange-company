# 46 — Self-Hosting Threshold

**Status:** Stage-0 plan (draft)
**DAG nodes:** R02 (primary), C10 (co-owner with 21: fake end-to-end suite runner and pass criteria)
**Related plans:** [20](20-woodpecker.md), [21](21-tdd-workflow.md), [22](22-verification-policy.md), [23](23-supply-chain.md), [24](24-hermes.md), [25](25-context.md), [26](26-agent-runner.md), [27](27-worker-contract.md), [28](28-harnesses.md), [29](29-task-state-machine.md), [31](31-model-routing.md), [32](32-cost-accounting.md), [33](33-budget-enforcement.md), [38](38-prometheus.md), [39](39-grafana.md), [40](40-opentelemetry.md), [42](42-alerting-notifications.md), [50](50-bootstrap-orchestrator.md), [52](52-ci-strategy.md), [55](55-forge-workflow.md)
**Contracts:** consumes `mcp-surface`, `runner-api`, `worker-result`, `cost-record`, `task`, `state-machine`, red/green result schemas; defines the threshold evidence bundle layout (§14.4)
**ADRs:** relies on 0003, 0004, 0008, 0019, 0403

> Every section below is mandatory (SPEC §121).

## 1. Purpose

SPEC §117 defines the Self-Hosting Threshold as the point where Strange Company "can meaningfully continue building itself under its own rules", and §118 requires proving it with a genuine test-first task against `strange-company`. This plan turns that into:

1. an **exact definition**: every required platform component and organizational capability mapped to a deterministic check;
2. the **first self-hosted task**, fully specified (objective, ACs, expected RED, implementation scope), with fallbacks, and the expected trajectory from human intent through merge, trusted build and Flux rollout to the new Runner running;
3. the **evidence bundle** proving every SPEC §129 "the system knows" item, with where it is recorded and the query that retrieves it;
4. the **C10 fake end-to-end scenario suite** (with plan 21) and its pass criteria — the orchestration definition of done that must hold before any paid harness is enabled.

## 2. Requirements derived from SPEC.md

1. Required platform: k3s, embedded etcd, CNPG, Longhorn, Authentik, Forgejo, GitHub mirror, Flux, Woodpecker, Prometheus, Grafana, OpenTelemetry Collector, Hermes, Agent Runner [SPEC §117].
2. Required organizational capabilities: task state, acceptance criteria, repository context, structured handoffs, model accounting, budget controls, fake workers, one real coding harness, enforced TDD state machine [SPEC §117].
3. Proof by a genuine test-first task against `strange-company`, recommended "add or improve one deterministic Agent Runner behavior", following human requirement → ACs → tester → RED commit → Woodpecker confirms meaningful failure → RED_VERIFIED → implementer → GREEN verification → reviewer → accepted PR [SPEC §118].
4. Editing its own source is insufficient; it must test-first develop itself [SPEC §118].
5. Throughout, the system knows which models and harnesses were used, what every invocation cost, which test specified the behavior, that it failed first, which implementation made it green, which CI run verified it, which commit shipped; the creative worker never has production authority [SPEC §129].
6. Fake harnesses allow orchestration testing without model spend [SPEC §103]; core orchestration, TDD policy, transitions, security and gates are testable deterministically [SPEC §104]; TDD acceptance tests of SPEC §106 pass.
7. Every milestone has deterministic exit criteria [SPEC §124]; a milestone is complete because evidence passes, not because files exist.

Derived requirements:

- **46-R-1** The threshold is evaluated by one command, `./bootstrap exec -- sc-e2e threshold-check`, which emits a JSON report (one entry per check, `pass|fail`, evidence pointer) and exits 0 only if every check passes. The report is stored in S3 `<prefix>-evidence/threshold/<timestamp>/report.json`.
- **46-R-2** The first self-hosted task's behavior must not exist before R02 (reserved by plan 26: 26-R-5, fallback 26-R-6), so RED is meaningful and ALREADY_SATISFIED cannot occur by accident.
- **46-R-3** The C10 suite makes zero paid model calls (ledger check) and is repeatable: two consecutive runs pass.

## 3. Inputs

- Live cluster at P12 (bootstrap DoD) with C01–C11 and R01 gates passed.
- `sc-e2e` tool (Go, in the toolbox image; plan 02/50 conventions) with suites `c10`, `smoke` (plan 28 §14.3) and `threshold-check`.
- Test fixtures: `test/e2e/fake-target/` with per-scenario commits (plans 21, 28 §14.5); Forgejo org `sc-e2e` and test human account `sc-e2e-human` (non-admin, member of a team allowed to merge in `sc-e2e/*`; created by C10 setup through the Forgejo admin API, plan 16).
- Runner admin token (`SC-RUNNER-ADMIN`, plan 26) for suite setup/assertions; Alertmanager test receiver (plan 42) for notification assertions.

## 4. Outputs

- `threshold/<timestamp>/report.json` and `threshold/<task-id>/bundle.json` in S3 `evidence` (versioned bucket).
- C10 suite JUnit reports in S3 `evidence/e2e/c10/<run-id>/junit.xml` and summary JSON.
- A human-merged PR in `strange-company` implementing the first self-hosted task, and a reviewed PR updating `STATUS.md` with the threshold result and the bundle digest.

## 5. Dependencies

**Hard (DAG):** C10 ← C02, C03, C04, C05, C06, C07, C08, C09. R02 ← R01, C11, P12.

**Interfaces assumed:**
- Plan 29 state names including `AWAITING_MERGE` (after an accepting review) and `DONE` on `pr_merged`; human transitions through `POST /v1/tasks/{id}/transitions`.
- Plan 21: TEST_SPEC_CONFLICT decision payload and generation handling; RED/GREEN semantics owned by 21/22.
- Plan 23: trusted build on `main` produces a signed Runner image with provenance linking image digest → source commit; a promotion PR updates `images.runner` in `gitops/platform/strange-company/base/values.yaml`.
- Plan 32: `ledger.cost_records` columns `task_id`, `execution_id`, `attempt`, `provider`, `model_requested`, `model_reported`, `harness`, `role`, `phase`, `cost_total_micro_usd`, `usage_status`, `token_kind` (names per plan 32; queries below are adjusted at implementation if plan 32 renames them).
- Plan 55: PR body content, statuses `strange-company/tdd-provenance` and `strange-company/review`, merge via Forgejo.

## 6. Owned state

None durable beyond evidence objects in S3 (`threshold/**`, `e2e/**`) and the `STATUS.md` entry (Git). Task, execution, ledger and evidence state are owned by plans 26/29/32/22.

## 7. External interfaces

- `sc-e2e threshold-check [--report <path>]` → JSON report (`{checks: [{id, title, spec, status, evidence}], passed: bool, generated_at, cluster, commit}`).
- `sc-e2e run --suite c10 [--only E2E-xx]` → JUnit + summary JSON.
- `sc-e2e bundle --task sc-<n>` → evidence bundle (§14.4).

## 8. Internal interfaces

`sc-e2e` talks to: the Runner admin API (port-forward, `SC-RUNNER-ADMIN`) and, for Hermes-driven rows, the Hermes gateway API (`SC-HERMES-API`); Forgejo API (`sc-e2e-human` token for merges, read-only otherwise); `sc-pg` read-only role `sc_reporting` (plan 53/12) for ledger and task queries; Prometheus/Alertmanager HTTP APIs; Kubernetes API (cluster break-glass kubeconfig in the toolbox). It never writes to `sc-pg` directly.

## 9. Configuration

### 9.1 Exact threshold definition — platform components

Every check is deterministic and executed by `threshold-check`. "V-xx" refers to plan 50's `./bootstrap verify` checks, re-run fresh.

| ID | Component [SPEC §117] | Check |
|---|---|---|
| TH-P01 | k3s | `kubectl get nodes -o json`: every node `Ready=True`; `kubectl version` server minor equals `versions/k3s.lock` (V-10 class) |
| TH-P02 | embedded etcd | `kubectl -n kube-system get pods -l component=etcd` is not used by k3s; instead `k3s etcd-snapshot list` (via plan 07 helper over SSH, `./bootstrap exec -- sc-bootstrap node exec etcd-snapshot-list`) returns ≥ 1 snapshot < 24 h old in S3 `etcd-snapshots`, and node label `node-role.kubernetes.io/etcd=true` is present on every server |
| TH-P03 | CNPG | `kubectl get clusters.postgresql.cnpg.io -A -o json`: `forgejo-pg`, `authentik-pg`, `woodpecker-pg`, `sc-pg` each `status.phase == "Cluster in healthy state"` and last successful backup < 26 h (plan 12 metrics `cnpg_collector_last_available_backup_timestamp`) |
| TH-P04 | Longhorn | `kubectl -n longhorn-system get volumes.longhorn.io -o json`: every volume `robustness` ∈ {`healthy`} (or `degraded` only when replicas > node count is impossible by profile); `hermes-data` and `forgejo-data` exist |
| TH-P05 | Authentik | `curl -fsS https://auth.<d>/application/o/forgejo/.well-known/openid-configuration` 200 with matching `issuer` (15-AC-6) |
| TH-P06 | Forgejo | `curl -fsS https://git.<d>/api/healthz` 200; clone of `strange-company/strange-company` over internal SSH from the toolbox port-forward succeeds |
| TH-P07 | GitHub mirror | GitHub `main` SHA equals Forgejo `main` SHA, or lag ≤ 1 h per plan 17's freshness metric (`sc_github_mirror_lag_seconds < 3600`) |
| TH-P08 | Flux | `flux get kustomizations -A` and `flux get helmreleases -A`: all Ready; root `GitRepository flux-system` URL is the Forgejo SSH URL and `status.artifact.revision` ends with Forgejo `main` SHA |
| TH-P09 | Woodpecker | Woodpecker API `GET /api/version` 200 and the last trusted pipeline on `strange-company` `main` succeeded |
| TH-P10 | Prometheus | `GET /api/v1/query?query=up` returns `up == 1` for jobs `agent-runner`, `model-gateway`, `kube-state-metrics`, `node-exporter` |
| TH-P11 | Grafana | `GET https://grafana.<d>/api/health` 200 and dashboards `sc-platform`, `sc-organization`, `sc-economics` exist (plan 39 UIDs) |
| TH-P12 | OpenTelemetry Collector | `sc_span_calls_total{span_name="sc.execution"}` increased during the last C10 run (ADR-0551) |
| TH-P13 | Hermes | blackbox `probe_success{probe="hermes-gateway"} == 1` and 24-AC-3 style 401/200 check through port-forward |
| TH-P14 | Agent Runner | `GET /readyz` 200 with `checks.policy == ok`; `sc_runner_leader == 1` |

### 9.2 Exact threshold definition — organizational capabilities

| ID | Capability [SPEC §117] | Check (live, deterministic) |
|---|---|---|
| TH-O01 | task state | `GET /v1/tasks/<first-self-hosted-task>/transitions` lists the full SPEC §118 sequence with actor and reason on every row; every transition's `(from, to)` pair exists in `contracts/state-machine.yaml` |
| TH-O02 | acceptance criteria | the task has ≥ 1 AC with stable `AC-<n>` ids and the tester result's `ac_mapping` covers every AC |
| TH-O03 | repository context | `strange-company` has a valid `.strange-company/repo.yaml` at the task's base commit, and the stored packets of the task's executions contain `repository_context` and `agents_md` sections whose source commit equals `base_commit` |
| TH-O04 | structured handoffs | every execution of the task has a schema-valid handoff in `agent_result` |
| TH-O05 | model accounting | every execution of the task has ≥ 1 `ledger.cost_records` row and no row with `usage_status = 'unknown'` (else the check reports `fail` with the count); `agent_status.cost.complete == true` |
| TH-O06 | budget controls | the last C10 run passed E2E-13 (over-budget and e-stop rows) within the last 7 days, and `policy/budgets.yaml` loads (`GET /v1/policy/status` `ok`) |
| TH-O07 | fake workers | the last C10 run (≤ 7 days) passed in full (§9.4 pass criteria) |
| TH-O08 | one real coding harness | `policy/harnesses.yaml` has `claude` `enabled: true`, `stage: production`; R01 smoke evidence (28-AC-24) exists; the first self-hosted task's tester, implementer and reviewer executions all used harness `claude` |
| TH-O09 | enforced TDD state machine | 26-AC-15 (`GATE_NOT_SATISFIED` for an early implementer) re-run live passes; the PR's `strange-company/tdd-provenance` status is `success` and was posted by `sc-runner`; branch protection on `main` requires it (Forgejo API `GET /repos/strange-company/strange-company/branch_protections/main` lists the three required contexts, ADR-0019) |

The threshold is **reached** when every TH-P and TH-O check passes in one `threshold-check` run executed after the first self-hosted task is `DONE` and its build is running (§14.3 step 12).

### 9.3 The first self-hosted task (primary)

**Behavior:** `agent_list` (and `GET /v1/tasks`) honors `updated_since`. Today the Runner returns `NOT_IMPLEMENTED` whenever the field is present (26-R-5, reserved). This is deterministic, small, observable through the public surface, and it pays for itself: Hermes' watcher (plan 24 §14.3) becomes cheap on a quiet system.

Task definition (as Hermes would submit it; the human confirms the ACs):

```yaml
title: Implement agent_list updated_since filtering
repository: strange-company/strange-company
base_ref: main
verification: {mode: test-first}
objective: >
  Make agent_list (MCP) and GET /v1/tasks honor the updated_since parameter defined in
  contracts/mcp-surface.schema.yaml, instead of returning NOT_IMPLEMENTED.
constraints:
  - Do not change any file under contracts/, policy/, .strange-company/, .woodpecker/ or gitops/.
  - Keep agent_list behavior unchanged when updated_since is absent.
  - Unit tests must run without network access or external services.
acceptance_criteria:
  - {id: AC-1, text: "With updated_since present, agent_list returns only tasks whose updated_at is strictly later than updated_since."}
  - {id: AC-2, text: "Results filtered by updated_since are ordered by updated_at ascending, ties broken by numeric task id ascending."}
  - {id: AC-3, text: "Pagination with updated_since is stable: following next_cursor until it is absent returns every matching task exactly once, even if older tasks are updated meanwhile."}
  - {id: AC-4, text: "updated_since combines with the states, repository and role filters as a logical AND."}
  - {id: AC-5, text: "Without updated_since, agent_list results and ordering are unchanged."}
  - {id: AC-6, text: "With updated_since present, agent_list no longer returns NOT_IMPLEMENTED."}
```

Expected RED: tester writes tests in `src/runner/**/..._test.go` against the service-core list operation with the in-memory task store used by Runner unit tests (plan 26 §22) plus the MCP adapter handler; at base, AC-1..AC-4 and AC-6 tests fail with an assertion on the `NOT_IMPLEMENTED` error (behavior absent, not a compile error), AC-5 passes (regression guard). Woodpecker classification `red_valid`, recorded as `tdd.red_verification.result = expected_failure` (plan 22: ≥ 1 new requirement-specific assertion fails; failures map to ACs).

Expected implementation scope: Runner service core (filter, keyset cursor `(updated_at, task_seq)`), SQL query for the `sc-pg` store (index on `tasks(updated_at, id)` via an expand-only migration), removal of the `features.updatedSinceFilter` gate and its config key (26-R-5 retired). No contract change: `mcp-surface.schema.yaml` already defines the field.

Roles and routing: tester `claude`/`tests`/`standard`; implementer `claude`/`implementation`/`bargain` (ladder escalation per plan 31 on GREEN failure); reviewer `claude`/`reasoning`/`standard`. Budget: the default per-task limit of `policy/budgets.yaml` (plan 33).

### 9.4 Fallbacks

| Situation | Response |
|---|---|
| RED classified `already_satisfied` (behavior unexpectedly present) | Do not manufacture failure (SPEC §51): the task goes `ALREADY_SATISFIED`; a human confirms; switch to **FB-1**. |
| Task reaches NEEDS_HUMAN through bounded retries (model limitation) | Human reviews evidence; one re-run of the full task with tier override `frontier` for the implementer (task-scoped, plan 31 override) is allowed. A second failure → **FB-1**. |
| Platform defect found during the attempt (infrastructure failures) | Fix the defect by a normal human PR; restart the primary task (new task id; the first attempt stays in history). |

**FB-1 (fallback task):** "Reject `orchestrator_notes` containing control characters." Today the Runner accepts them and the context compiler strips them (reserved by 26-R-6). ACs: AC-1 `agent_start` whose `orchestrator_notes` contains a character in U+0000–U+001F other than `\n` (U+000A) and `\t` (U+0009), or U+007F, returns `VALIDATION_FAILED` with `field: /orchestrator_notes`; AC-2 no task or execution row is created in that case; AC-3 notes with only `\n`/`\t` are accepted unchanged; AC-4 the same rule applies to `POST /v1/tasks` and `POST /v1/tasks/{id}/executions`. Same roles, same trajectory.

**FB-2:** if FB-1 is also unusable, a human selects another deterministic Runner behavior and first lands a reviewed PR that reserves it in plan 26 (a `26-R-n` reservation plus a test proving its absence), then repeats §14.

### 9.5 C10 fake end-to-end suite (with plan 21)

Run on the live cluster against `sc-e2e/fake-target` with `modelGateway.fakeUpstream.enabled: true`, only `fake-*` harnesses, and the e2e budget overrides of plans 28/33. Setup (`sc-e2e run --suite c10` step 0): force-reset `sc-e2e/fake-target` `main` to the fixture commit, delete `agent/*`, `scratch/*`, `sc-quarantine/*`, `sc-archive/*` refs; ensure `sc-e2e-human`; apply the **C10 Hermes overlay** (review RA-12): chart values `hermes.orchestrator.provider: fake`, `model: fake-hermes-watch`, `auxiliaryModel: fake-hermes-watch` and the plan 31 test-overlay `ModelRouting.orchestrator` resolving to provider `fake`, model `fake-hermes-watch`, so the Runner re-mints the orchestrator token with `allowed_models = [fake-hermes-watch]` (plan 31 §14.6 re-mint on allowlist change) and Hermes' watcher runs against plan 53's scripted conversation; the overlay is reverted after the run; record `run_start`. Plan 21 owns the TDD semantics of rows E2E-02…E2E-10, which are implemented as plan 21 §22's `tdd-*` cases (including `tdd-red-compile-error-then-stub`, `tdd-tamper-skip`, `tdd-tamper-discovery`, `tdd-tamper-env-detect`, `tdd-exemption-*` with `fake-judge-docs`/`fake-produce-docs`, and `fake-review-request-changes`/`fake-review-reject` review paths); this plan owns the suite runner and pass criteria. **One entry point and one budget** (review TDD-19): C10 = `sc-e2e run --suite c10`, which executes every trajectory file `test/e2e/tdd/*.yaml` of plan 21 (each `tdd-*` case is a gating row, reported under its own name) plus E2E-01 and E2E-11…E2E-19; plan 21's `make e2e-tdd` is an alias of the same command.

| ID | Scenario (fake harnesses, plan 28 §14.4) | Expected end state / assertions | SPEC |
|---|---|---|---|
| E2E-01 | Happy path (plan 21 `tdd-happy-path`): `fake-red-valid` tester → `fake-green` implementer → `fake-review-approve` reviewer → `sc-e2e-human` merges | task `DONE`; transitions in canonical order; `red_commit` ≠ `implementation_commit`, the latter descends from the former; RED classification `red_valid` (`tdd.red_verification.result = expected_failure`), GREEN `green_pass`, pipelines triggered by `sc-runner`; PR body has task id, ACs, test commit, RED evidence, implementation summary, GREEN evidence, known risks, handoff; statuses `tdd-provenance` and `review` = success | §47, §100, §106 #6, §129 |
| E2E-02 | Implementer requested while `TEST_AUTHORING` | `GATE_NOT_SATISFIED`, no Job | §52, §106 #1 |
| E2E-03 | `fake-red-broken-test`, then `fake-red-valid` | `TEST_NEEDS_WORK`, then `RED_VERIFIED` of the new generation | §50, §106 #2 |
| E2E-04 | `fake-already-satisfied` | `ALREADY_SATISFIED`; implementer refused | §51, §106 #3 |
| E2E-05 | `fake-test-tamper` implementer | execution policy violation `TEST_TAMPER`; task `NEEDS_HUMAN`; merge of the PR via API by `sc-e2e-human` is rejected (required status not success) | §53, §102, §106 #4/#5 |
| E2E-06 | `fake-test-delete` implementer | `TEST_TAMPER`; `NEEDS_HUMAN` | §106 #4 |
| E2E-07 | `fake-protected-path-edit` | `PROTECTED_PATH_VIOLATION`; no Woodpecker pipeline created for the head SHA | ADR-0008 |
| E2E-08 | `fake-test-spec-conflict` → `fake-adjudicate-test-invalid` reviewer (`purpose: adjudication`) → `sc-e2e-human` comments `/sc accept-test-change` (ADR-0350/0355) → tester re-authors with `fake-red-valid-v2` (`continue` at red(g1)) → implementer `fake-green`; variant with `fake-adjudicate-test-valid` → implementer re-dispatched | `TEST_SPEC_CONFLICT` reached; after decision, new RED generation, then `GREEN_VERIFIED` | §53, §70, §106 #7 |
| E2E-09 | `fake-green-after-retry` | attempt 1 GREEN failure; attempt 2 `GREEN_VERIFIED`; task `test_commit`/`red_verification` unchanged across attempts; attempt-2 packet contains `prior_attempt_evidence` | §74, §106 #8 |
| E2E-10 | Verification-first task with a disallowed exemption, then an allowed one (plan 22 class) | `EXEMPTION_NOT_ALLOWED`; the allowed one follows the judge-first path to its terminal state | §46, §106 #9 |
| E2E-11 | Infrastructure failures: `fake-failure`, `fake-timeout`, `fake-deadline-exceeded`, `fake-malformed-result`, `fake-malformed-handoff`, `fake-malformed-stream`, `fake-push-no-result`; `fake-claim-missing` (merit) | each row's execution state/class/code per plan 28; three consecutive infra failures on one task → `NEEDS_HUMAN` + `SCTaskNeedsHuman` delivered to the test receiver | §4.8, §74, §97 |
| E2E-12 | Escapes: `fake-push-other-branch`, `fake-secret-in-handoff`, `fake-not-descendant`, `fake-wrong-commit-claim`, `fake-large-diff`, `fake-tester-touches-implementation` | violation codes per plan 28; `main` unchanged; quarantine done; credentials revoked | §86, §87, §105 |
| E2E-13 | Budgets: `fake-over-budget`; `fake-spend-loop` with e-stop `workers` set mid-run, then released and the task resumed by a human transition | `BUDGET_TASK_EXHAUSTED` (plan 33 test overlay `maxTaskCostUsd: 0.01`) → `NEEDS_HUMAN` + notification; e-stop → `BLOCKED (emergency_stop)` → human resume → continues | §81, §97 |
| E2E-14 | Cancellation: `agent_cancel` on `fake-timeout` and on `fake-ignore-cancel` | both `CANCELLED` within bounds; logs archived; keys deleted | §4.5 |
| E2E-15 | Hermes-driven happy path (scripted fake upstream, plan 24 §22): Hermes dispatches tester, implementer, reviewer with fake harnesses on its own | same end state as E2E-01 except merge; transitions with actor `hermes`; orchestrator ledger rows present | §4.6, §60 |
| E2E-16 | `fake-model-switch` | one `SC_MODEL_NOT_ALLOWED` denial; execution SUCCEEDED | §78 |
| E2E-18 | Hermes watcher (moved from 24-AC-12, review RA-11/RA-12): with Hermes' watcher model pointed at plan 53's scripted conversation scenario `fake-hermes-watch`, a task reaching `RED_VERIFIED` gets its implementer dispatched by Hermes within 15 min | transition `RED_VERIFIED → IMPLEMENTING` with actor `hermes`; orchestrator ledger rows have zero `SC_MODEL_NOT_ALLOWED` denials | §4.6, §60 |
| E2E-19 | Context proposal (moved from 25-AC-10): `fake-success` researcher on a task whose objective contains `[fake:context-proposal]` | exactly one open issue labelled `sc/context-proposal` in `sc-e2e/fake-target`; a Runner restart and outbox replay creates no second issue | §43 |
| E2E-17 | Merge policy: force-push to the PR head after review | `tdd-provenance`/`review` statuses return to `pending`; merge refused | ADR-0019 |

**C10 pass criteria (all must hold for one run, and for two consecutive runs):** every row passes; `SELECT count(*) FROM ledger.cost_records WHERE started_at >= :run_start AND provider NOT IN ('fake','fake-b')` = 0 (46-R-3); after the run no deploy key titled `sc-*` exists on `sc-e2e/fake-target`, no Job/Secret with `app.kubernetes.io/managed-by=agent-runner` remains in `agent-jobs` (within 10 min), no execution is non-terminal; total wall time ≤ 180 min (the single C10 budget, shared with plan 21); the run's JUnit and summary are stored in S3.

## 10. Secrets

Consumes (toolbox-side, never stored by `sc-e2e`): `SC-RUNNER-ADMIN` (from the recovery bundle or operator vault, plan 35), `SC-HERMES-API` (Hermes Secret, cluster break-glass), the `sc-e2e-human` Forgejo token (test account; created at C10 setup, stored only in the operator's toolbox session, revoked at the end of the run). The `sc-e2e-human` token is a test credential of class `SC-FORGEJO-BOT-TOKENS` (instance `sc-e2e-human`) — requested addition to plan 35's register. No new class.

## 11. Storage

S3 `evidence/threshold/**` and `evidence/e2e/**` (retention: threshold bundles kept indefinitely; e2e runs 90 days, plan 43). Nothing else.

## 12. Resource requirements

C10 runs one execution at a time on self-hosting-minimum (`maxWorkers: 1`), so it adds at most one worker's profile to cluster load plus fake upstream traffic. `sc-e2e` runs in the operator's toolbox.

## 13. Security boundary

- The suite uses the Runner admin API (break-glass, audited by `SCRunnerAdminAccess`) only for setup, human-equivalent transitions and assertions; dispatches use the same API surfaces Hermes uses.
- `sc-e2e-human` has merge rights only in org `sc-e2e`; it is not an admin and cannot bypass required statuses.
- The first self-hosted task's workers hold no production authority: write deploy key on one repository, gateway token bound to the task budget, no registry/signing/deployment credentials (SPEC §129 last sentence); TH-O09 and the bundle (§14.4 K9) record this.

## 14. Happy path

### 14.1 Preconditions

P12, C01–C11 passed; R01 passed (Claude Code enabled); a C10 run passed ≤ 7 days ago. `modelGateway.fakeUpstream.enabled` stays **true** in production after R02 (review RA-m12): the fake provider is `stage: test-only`, usable only by `fake-*` harnesses on `spec.testRepositories` with a tiny budget, and it keeps C10 regression runs and the procedures' post-recovery fake checks working (request to plan 53 to change its default).

### 14.2 Trajectory of the first self-hosted task

| # | Actor | Action | Resulting state / record |
|---|---|---|---|
| 1 | human | In the Hermes dashboard: "Implement `updated_since` for `agent_list`" | Hermes session |
| 2 | Hermes (`sc-intake`) | drafts the §9.3 definition; human confirms ACs | — |
| 3 | Hermes → Runner | `agent_start` with `task` + role `tester`, harness `claude`, profile `tests`, tier `standard` | task `sc-N`: `∅→SPECIFIED→TEST_AUTHORING` (actor `hermes`); execution `sc-N-x1` QUEUED |
| 4 | Runner | dispatch: context packet (plan 25), deploy key, tokens, Job | execution RUNNING |
| 5 | tester worker | writes tests, `sc-worker` commits `test(sc-N): …`, pushes `agent/sc-N`, posts result | execution SUCCEEDED; task `RED_VERIFYING`; PR opened by `sc-runner` (plan 55) |
| 6 | Runner → Woodpecker | triggers RED pipeline for the red commit with base-revision config (ADR-0008) | pipeline #R |
| 7 | Woodpecker / Runner | `sc-verify` red-result classification `red_valid`; Runner validates evidence | task `RED_VERIFIED` (actor `ci:woodpecker/R`) |
| 8 | Hermes (`sc-task-watch`) | sees `next_allowed_roles: [implementer]`; dispatches implementer (`claude`, `implementation`, `bargain`) | task `IMPLEMENTING`; `sc-N-x2` |
| 9 | implementer worker | continues from the red commit; commits `feat(sc-N): …`; pushes | task `GREEN_VERIFYING`; pipeline #G triggered |
| 10 | Woodpecker / Runner | green-result `success` | task `GREEN_VERIFIED` (actor `ci:woodpecker/G`); Hermes dispatches reviewer → `REVIEW` |
| 11 | reviewer worker → Runner | verdict `approve`, five checks `pass`; Runner re-derives SPEC §102 facts | statuses `tdd-provenance` and `review` = success; task `AWAITING_MERGE` |
| 12 | human | reviews the PR (evidence in body) and merges in Forgejo | task `DONE` (`pr_merged`, plan 29) |
| 13 | trusted CI (plan 23) | builds, scans, signs the Runner image from the merge commit; attaches provenance | image digest D |
| 14 | plan 23 promotion → human | promotion PR sets `images.runner: …@D`; CI; human merge | Git desired state |
| 15 | Flux | reconciles layer `strange-company`; Runner `Recreate`; migrations (expand-only) | Runner pod `imageID` = D, Ready |
| 16 | operator | `sc-e2e threshold-check`; `sc-e2e bundle --task sc-N`; MCP `agent_list` with `updated_since` returns a filtered list (no `NOT_IMPLEMENTED`) | threshold report + bundle in S3 |
| 17 | human | PR updating `STATUS.md` (threshold reached, report and bundle digests) | Git |

### 14.3 Live verification after rollout

`sc-e2e threshold-check` also performs the behavioral check: create two fake-target tasks via the admin API at times t1 < t2, then `agent_list {updated_since: t1}` returns the second and not the first, ordered by `updated_at`.

### 14.4 Evidence bundle (SPEC §129 "the system knows")

`sc-e2e bundle --task sc-N` writes `bundle.json` (each item: `value`, `source`, `query`, `digest` of the raw query result):

| Key | SPEC §129 item | Where recorded | Query |
|---|---|---|---|
| K1 | which models were used | `ledger.cost_records` (plan 32) | `SELECT DISTINCT provider, model_requested, model_reported FROM ledger.cost_records WHERE task_id = 'sc-N'` |
| K2 | which harnesses were used | `runner.executions` | `SELECT id, role, harness, image_digest, attempt FROM runner.executions WHERE task_id = 'sc-N' ORDER BY id` |
| K3 | what every invocation cost | `ledger.cost_records` | `SELECT execution_id, provider_request_id, cost_total_micro_usd, usage_status, pricing_version FROM ledger.cost_records WHERE task_id = 'sc-N' ORDER BY started_at` (+ per-phase totals per plan 32) |
| K4 | what test specified the behavior | `runner.tasks.red_commit`, tester result `ac_mapping`, protected test manifest (plan 22) | `SELECT red_commit FROM runner.tasks WHERE id='sc-N'`; `GET /v1/tasks/sc-N/result?execution_id=<tester>`; Forgejo `GET /repos/strange-company/strange-company/git/commits/<red_commit>` |
| K5 | that the test failed first | RED evidence + transition order | `SELECT classification, pipeline, commit, sha256, recorded_at FROM runner.evidence WHERE task_id='sc-N' AND kind='red'`; transitions: `RED_VERIFIED.at` < implementer execution `started_at` |
| K6 | which implementation made it green | `runner.tasks.implementation_commit`, GREEN evidence | `SELECT implementation_commit FROM runner.tasks WHERE id='sc-N'`; `git merge-base --is-ancestor <red_commit> <implementation_commit>` = true |
| K7 | which CI run independently verified it | `runner.evidence` (pipeline numbers, creator `sc-runner`), S3 evidence objects | `SELECT kind, pipeline, object_key, sha256 FROM runner.evidence WHERE task_id='sc-N' AND kind IN ('red','green')`; Woodpecker `GET /api/repos/<id>/pipelines/<n>` shows `author: sc-runner` and the expected commit |
| K8 | which commit ultimately shipped | Forgejo PR merge commit → plan 23 provenance → promotion commit → Flux → pod | `GET /repos/.../pulls/<pr>` `merge_commit_sha`; provenance attestation for digest D names that commit; `kubectl -n strange-company get helmrelease strange-company -o jsonpath='{.status.lastAppliedRevision}'` ⊇ promotion commit; `kubectl -n strange-company get pod -l app.kubernetes.io/name=agent-runner -o jsonpath='{..imageID}'` ends with D |
| K9 | creative worker never had production authority | per-execution credentials and RBAC | `SELECT jti, kind, revoked_at FROM runner.tokens WHERE execution_id LIKE 'sc-N-x%'` (all revoked); deploy keys deleted (`runner.deploy_keys.deleted_at` not null); Job specs archived in context/log evidence show SA `agent-worker`; plan 37 RBAC assertion output |
| K10 | TDD provenance record (SPEC §56) | `runner.tasks`, plan 29 `tdd` fields | `GET /v1/tasks/sc-N` + transitions export |

The bundle's sha256 is recorded in `STATUS.md` (step 17). Squash merges do not affect K4–K7 because provenance is held in task history (SPEC §56).

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| A TH check fails | `threshold-check` report | threshold not reached | none | fix the owning subsystem; re-run |
| C10 row fails | JUnit | C10 gate red; R01 blocked | none | owning plan's procedure; `tdd-task-recovery.md` |
| C10 makes a paid call | 46-R-3 query | gate red | none | investigate routing stage separation (plan 31) |
| First task ALREADY_SATISFIED | RED classification | primary unusable | — | FB-1 (§9.4) |
| First task exhausts retries | NEEDS_HUMAN | — | — | §9.4; `tdd-task-recovery.md` |
| Promotion or rollout fails | Flux HelmRelease not Ready; Runner CrashLoop | threshold blocked; old Runner rolled back | helm remediation rollback | `agent-runner-failure.md` §Rollback |
| Evidence incomplete (`usage_status unknown`) | TH-O05 | threshold not reached | — | plan 53 usage parsing fix; re-run the task (new id) |

## 16. Retry behavior

`threshold-check` and the bundle are read-only and re-runnable at will. The C10 suite reruns failed rows individually (`--only`) for diagnosis, but the gate requires two consecutive full green runs. The first self-hosted task follows the task retry policy (plan 29); task-level reattempts follow §9.4.

## 17. Idempotency behavior

`threshold-check` and `bundle` are side-effect free except for writing new, timestamped S3 objects. C10 setup is idempotent (force-reset fixture refs; ensure account). Creating tasks generates new ids each run by design.

## 18. Bootstrap behavior

N/A — the threshold is evaluated only after bootstrap (P12) and the Phase-3/4 gates; nothing here participates in bootstrap. `./bootstrap verify` check V-36 (plan 50) covers the platform list presence; this plan's TH-P checks are stricter and run separately.

## 19. Upgrade behavior

After the threshold, `threshold-check` and the C10 suite are regression gates: plan 48 runs C10 after every Runner, Gateway, Hermes or harness upgrade, and a failing C10 blocks promotion (request to plans 48/23).

## 20. Recovery behavior

After black start (plan 45), the threshold is re-established by: `threshold-check` (all TH-P/TH-O pass against restored history; TH-O checks reference the original first self-hosted task, whose records live in restored `sc-pg` and S3) and a fresh C10 run.

## 21. Observability

- `sc-e2e` emits JUnit and JSON to S3. C10 is operator-run (it needs the admin token), so it is deliberately **not** a check CronJob (ADR-0553); the S3 report and the `STATUS.md` entry are the record.
- Alerts: none new; failures are surfaced by the run itself.

## 22. Testing strategy

- `sc-e2e` unit tests (report schema, query builders, bundle digesting) in `make test`.
- The suite is itself the integration test of Phase 3. A dry-run mode (`--dry-run`) validates connectivity and fixtures without dispatching.
- No paid calls anywhere in C10 or `threshold-check`; the only paid execution is the first self-hosted task and the R01 smoke (both outside default CI).

## 23. Acceptance criteria

### GATE-C10 (with plan 21)

- [ ] **46-AC-1** `./bootstrap exec -- sc-e2e run --suite c10` exits 0 twice in a row on the live cluster; both JUnit files list E2E-01, E2E-11…E2E-19 and every `tdd-*` trajectory of `test/e2e/tdd/` as passed and are stored in S3 `evidence/e2e/c10/`.
- [ ] **46-AC-2** For both runs: `SELECT count(*) FROM ledger.cost_records WHERE started_at >= :run_start AND provider NOT IN ('fake','fake-b')` = 0, and `SELECT count(*) FROM ledger.cost_records WHERE started_at >= :run_start` ≥ the number of executions in the run.
- [ ] **46-AC-3** After each run: `GET /repos/sc-e2e/fake-target/keys` contains no `sc-*` title; `kubectl -n agent-jobs get jobs,secrets -l app.kubernetes.io/managed-by=agent-runner --no-headers | wc -l` = 0 within 10 min; `SELECT count(*) FROM runner.executions WHERE state NOT IN ('SUCCEEDED','FAILED','CANCELLED','TIMED_OUT')` = 0.
- [ ] **46-AC-4** The test receiver recorded `SCTaskNeedsHuman` deliveries for E2E-05, E2E-11 and E2E-13 tasks (distinct `subject` labels, ADR-0555) during the run.
- [ ] **46-AC-6** (moved from 24-AC-8; review RA-11) Emergency stop vs Hermes: with level `all`, a Hermes chat turn returns `finish_reason: "error"` and `ledger.denials` gains a row with `code = 'SC_EMERGENCY_STOP'` for the Hermes jti; with level `workers`, a chat turn succeeds and `agent_start` returns `EMERGENCY_STOP`.
- [ ] **46-AC-7** (moved from 25-AC-10 and 24-AC-12) E2E-18 and E2E-19 pass in the C10 runs of 46-AC-1.
- [ ] **46-AC-5** E2E-01's task has, in `GET /v1/tasks/<id>`, non-null `red_commit`, `implementation_commit` and RED/GREEN evidence digests, and `git merge-base --is-ancestor red_commit implementation_commit` is true.

### GATE-R02

- [ ] **46-AC-10** The first self-hosted task (§9.3, or FB-1 with a recorded justification) reached `DONE` with the transition sequence of §14.2 (`GET /v1/tasks/<id>/transitions` contains, in order, `SPECIFIED`, `TEST_AUTHORING`, `RED_VERIFYING`, `RED_VERIFIED`, `IMPLEMENTING`, `GREEN_VERIFYING`, `GREEN_VERIFIED`, `REVIEW`, `AWAITING_MERGE`, `DONE`; retries may insert additional rows between them).
- [ ] **46-AC-11** All of the task's worker executions used harness `claude` and every `ledger.cost_records` row of the task has `usage_status = 'reported'`.
- [ ] **46-AC-12** The RED pipeline was created by `sc-runner` for the red commit and classified `red_valid` (`tdd.red_verification.result = expected_failure`); at least one failing assertion maps to AC-1 (red-result failure list ↔ tester `ac_mapping`).
- [ ] **46-AC-13** The running Runner pod's `imageID` digest equals the digest built by trusted CI from the PR's merge commit (provenance attestation verified with `cosign verify-attestation`), and the HelmRelease `lastAppliedRevision` includes the promotion commit.
- [ ] **46-AC-14** Behavioral check (§14.3) passes on the live Runner: `agent_list {updated_since: t1}` returns exactly the task updated after `t1` (no `NOT_IMPLEMENTED`).
- [ ] **46-AC-17** (moved from 24-AC-13; review GC-01) `hermes-data` has a Longhorn RecurringJob of type backup (daily) and at least one completed backup is listed in Longhorn within 26 h of C08.
- [ ] **46-AC-18** Memory budget under threshold load (review GC-13): `./bootstrap verify --only V-30` (plan 50/51) passes with Runner, Gateway, egress-proxy and Hermes running and a C10 run inside the measurement window; the result is recorded for SI-12.
- [ ] **46-AC-19** (Runner property from plan 26 §20, placed at R02 because it needs the P10 restore drill; review RA-6, RA-11) PITR id floor: create task A after a backup point, restore `sc-pg` to that point, create task B: B's number is greater than A's, no PR of A is adopted, and `id_floor` has one row.
- [ ] **46-AC-15** `sc-e2e threshold-check` exits 0 with every TH-P01…TH-P14 and TH-O01…TH-O09 `pass`; the report and `sc-e2e bundle` output (K1–K10 all non-empty) are stored in S3, and their sha256 values appear in `STATUS.md` on `main` via a human-merged PR.
- [ ] **46-AC-16** A C10 run executed after the new Runner rollout still passes (regression), on the production cluster (the fake upstream remains enabled, §14.1).

## 24. Explicit non-goals

- Proving small-HA (H01–H04) — sequenced after R02 (master plan §13).
- Autonomous merge (ADR-0019); the human merges the threshold PR.
- Performance or cost targets for the first task beyond the configured budgets.
- Evaluating second or third real harnesses.

## 25. Open questions

1. **`AWAITING_MERGE` state name** (blocking: no). Taken from plan 29 (draft on disk); if plan 29 renames it, 46-AC-10 follows.
2. **Ledger column names** (blocking: no). Queries in §14.4 use plan 32's names as read at writing time; `sc-e2e` uses the `sc_reporting` views if plan 32 provides them.
3. **Test merge account** (blocking: no). `sc-e2e-human` needs a register entry (plan 35) and a Forgejo team definition (plan 16/55).
4. **C10 runtime** (blocking: no). Timeout scenarios use the `tiny` profile (7-minute harness timeout); if the 150-minute bound is exceeded, plan 26 may add a test-only profile for `sc-e2e/*` repositories.
