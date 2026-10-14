# 29 — Task State Machine

**Status:** Stage-0 plan (draft)
**DAG nodes:** F05 state-machine-lib (primary), C01 runner-core (co-owner with 26: task tables, transition engine, watchdog). Consumed by C02–C04, C09, C10.
**Related plans:** [21 TDD workflow](21-tdd-workflow.md), [22 verification policy](22-verification-policy.md), [26 Agent Runner](26-agent-runner.md), [25 context](25-context.md), [27 worker contract](27-worker-contract.md), [30 handoffs](30-handoffs.md), [31 model routing](31-model-routing.md), [32 cost accounting](32-cost-accounting.md), [33 budgets](33-budget-enforcement.md), [38 Prometheus](38-prometheus.md), [42 alerting](42-alerting-notifications.md), [55 forge workflow](55-forge-workflow.md)
**Contracts:** owns `contracts/state-machine.yaml`, `contracts/state-machine.schema.yaml`, `contracts/task.schema.yaml`; owns the `spec.retry`, `spec.watchdog`, `spec.testSpecConflict` and `spec.humanAuthority` sections of `contracts/verification.schema.yaml` (file owned by 22); consumes `acceptance-criteria` (21), `red-result`/`green-result`/`test-manifest` (22), `worker-result` (27), `handoff` (30), `mcp-surface`/`runner-api` (26), `notification` (42)
**ADRs:** relies on 0003, 0007, 0008, 0017, 0019; introduces [ADR-0350](../adr/0350-human-commands-in-forgejo.md) (human-only transitions through authenticated `/sc` commands) and [ADR-0353](../adr/0353-verification-first-judge-states.md) (judge-first states and a single human approval gate)

> Every section below is mandatory (SPEC §121). Requirements carry SPEC trace tags.

## 1. Purpose

This plan defines the **authoritative state machine** for Strange Company tasks [SPEC §72]: every task state, every allowed transition, who may cause it, what must be true first, what happens as a consequence, and what is recorded. The machine is the first of the five TDD enforcement layers [SPEC §58] and the place where the test-first invariant [SPEC §4.4, §47] becomes mechanical: an implementer cannot be dispatched before trusted CI has recorded valid RED, and nothing but a deterministic event or an authenticated human decision can move a task.

Deliverables:

- `contracts/state-machine.yaml`: the machine-readable transition table (24 states, 94 transitions, execution machine, human projection), loaded by the Runner's state-machine library (F05) and by every test that exercises it.
- `contracts/state-machine.schema.yaml`: its schema and loader invariants.
- `contracts/task.schema.yaml`: the durable task record (SPEC §73 + §56 + history).
- The retry policy, watchdog, atomic-claim and idempotency rules the Runner implements around the table.

## 2. Requirements derived from SPEC.md

1. Test-first lifecycle `SPECIFIED → TEST_AUTHORING → RED_VERIFYING → RED_VERIFIED → IMPLEMENTING → GREEN_VERIFYING → GREEN_VERIFIED → REVIEW → DONE` is enforced by durable task state, not by instructions [SPEC §4.4, §47, §58].
2. States equivalent to Test Needs Work, Already Satisfied / Needs Investigation and Test Spec Conflict exist [SPEC §51, §53, §72].
3. Implementation is not dispatched before the test commit exists and RED_VERIFIED is recorded; implementation starts from the red commit or a descendant [SPEC §52].
4. Verification-first work has a machine-visible mode and an equivalent judge-first path [SPEC §46, §57].
5. Execution states `QUEUED, STARTING, RUNNING, SUCCEEDED, FAILED, CANCELLED, TIMED_OUT` exist and are coupled to task states [SPEC §72].
6. The task record holds the SPEC §73 fields and TDD provenance that survives retries and squash [SPEC §56, §73].
7. Every transition records timestamp, actor, old state, new state, reason [SPEC §73].
8. Claims are atomic [SPEC §73].
9. Retries are bounded (≈5 autonomous attempts), use evidence, may escalate model tier, never replay identical deterministic failures; exhaustion → Needs Human with notification [SPEC §4.8, §74, §97].
10. Human-facing states `Backlog, Ready, In Progress, Review, Done, Needs Human, Blocked` are a projection; the Strange Company database is authoritative [SPEC §71, §101].
11. Workers do not freely exempt their tasks [SPEC §46]; Hermes is not trusted for enforcement (ADR-0003).
12. Orchestration, TDD policy and state transitions are testable deterministically without model calls [SPEC §104, §106].

Derived requirements:

- **29-R-1** Workers are never actors. A worker's claim reaches the machine only as a Runner event after ingestion (plan 26 §14.4).
- **29-R-2** Exits from `BLOCKED` and `NEEDS_HUMAN` are human-only (lesson L-4). So are: entering `BLOCKED` on purpose (the Runner itself parks tasks in `BLOCKED` only for an emergency stop or a shared budget window, plan 33 §14.5), approving a corrected acceptance test (default policy), rejecting a conflict without an adjudication, approving an exemption/judge that needs approval, revising acceptance criteria after creation, declaring a task done without merge, and re-running verification by hand.
- **29-R-3** A human transition never lands in a state that requires an active worker execution; humans decide, Hermes dispatches (a human does not pick harness or model).
- **29-R-4** Every transition records the platform policy revision and commit and the state-machine revision in force, and the evidence digests it relied on.
- **29-R-5** The machine fails closed: an event with no matching transition is rejected and recorded; an unknown guard/effect identifier or a failed invariant is a load error that disables dispatch.
- **29-R-6** Added states, with justification (master plan §7 allows additions):
  - `AWAITING_MERGE`: after an accepting review the task waits on a human merge (ADR-0019). The waiting party, stall threshold and permitted events differ from `REVIEW` (where a reviewer execution is active), so one state cannot carry both without ambiguous watchdog and projection rules.
  - `JUDGE_AUTHORING`, `JUDGE_VALIDATING`, `JUDGE_AWAITING_APPROVAL`, `JUDGE_APPROVED`, `PRODUCING`, `JUDGING`: the verification-first path (ADR-0353). Master plan §7 names four. `JUDGE_VALIDATING` (CI proves a deterministic judge fails on base) and `JUDGE_AWAITING_APPROVAL` (the single human gate) are added because they have different actors (`ci` vs `human`) and different stall handling. `TEST_NEEDS_WORK`, `ALREADY_SATISFIED`, `NEEDS_INVESTIGATION`, `TEST_SPEC_CONFLICT`, `GREEN_VERIFIED`, `REVIEW`, `CHANGES_REQUESTED`, `AWAITING_MERGE` and the terminal/held states are shared by both modes; in verification-first mode "test" in their names means "judge artifact".

## 3. Inputs

| Input | Source | Contract |
|---|---|---|
| Transition table | `contracts/state-machine.yaml`, embedded in the Runner binary at build time and checksum-recorded | `state-machine.schema.yaml` |
| Retry, watchdog, conflict and human-authority policy | `policy/verification.yaml` (`spec.retry`, `spec.watchdog`, `spec.testSpecConflict`, `spec.humanAuthority`) merged tighten-only with base-revision `.strange-company/policy.yaml` | `verification.schema.yaml`, `repo-policy.schema.yaml` |
| Hermes requests | MCP `agent_start`, `agent_cancel` (plan 26 §7.1) | `mcp-surface.schema.yaml` |
| Human decisions | `/sc` commands in Forgejo comments (§7.3) and the loopback admin API `POST /v1/tasks/{id}/transitions` | `runner-api.openapi.yaml` `HumanTransitionBody` |
| Execution outcomes | Runner result ingestion and Job watcher (plan 26 §14.4, §15) | `worker-result.schema.yaml` |
| Verification evidence | Runner evidence poller + verifier library (plans 22, 26) | `red-result`, `green-result` |
| Forge events | Forgejo webhooks + 5-minute resync (plans 26, 55) | upstream payloads |
| Clock | Runner watchdog (every 30 s) | — |

## 4. Outputs

- Rows in `sc-pg` schema `runner` (§6): task state, transitions, generations, verifications, conflicts, reviews, rejected events.
- Decisions returned to the Runner core: target state, effects to enqueue (outbox), counters to update, failure class, notification reason.
- `next_allowed_roles` and `human_state` for `agent_status` (MCP).
- Prometheus metrics and Runner-posted alerts (§21).
- The view `runner.v_execution_outcomes` for plan 32 (§7.6).

## 5. Dependencies

**Hard (DAG):**
- F05 ← F02 (contract tooling validates the table against its schema and runs the invariant checker).
- C01 ← F04, F05, F13, P11 (plan 26). This plan's tables and engine ship inside C01.

**Soft / runtime:**
- Plan 22 verifier library (F06) supplies `G_VERIFICATION_OUTCOME`, `G_TEST_MANIFEST_INTACT`, `G_RED_SCOPE_VALID` facts. Until F06 exists, F05 tests use a fake facts provider.
- Plan 26 supplies the facts provider (Git facts via Forgejo, dispatch prerequisites, budget), the outbox and the Job watcher.
- Plan 55 consumes effects `E_ENSURE_PR`, `E_POST_STATUS`, `E_ARCHIVE_BRANCH`, `E_CLOSE_TRACKING`, `E_QUARANTINE_HEAD` and supplies forge events.
- Plan 42 registers alert names and the extended NEEDS_HUMAN reason enum (§21).

Interfaces assumed from other plans:
- Plan 26: one Runner leader writes task state (Lease); idempotency keys for MCP/API; webhook delivery dedup; outbox with `dedup_key`; the admin API `humanTransition` accepts `decision.command` (§7.4).
- Plan 27: worker `failure.reason` values are those of `worker-result.schema.yaml`; implementer/producer results with `state: test_spec_conflict` carry `conflict`.
- Plan 25: retries receive `prior_attempt_evidence`; conflict decisions reach testers as `conflict_decision` and implementers as `reviewer_findings`/`conflict_decision`.
- Plan 31: the routing ladder takes `merit_failures_at_resolution` = merit failures of the role in the current generation (§16.5).

## 6. Owned state

System of record: CNPG `sc-pg`, database `strange_company`, schema `runner` (physical DDL and migrations owned by plan 26; column semantics owned here). Only `agent-runner` (role `runner_app`) writes. Humans change task state only through §7.3/§7.4, never by SQL, except the documented break-glass steps in `procedures/stuck-task-recovery.md`.

### 6.1 Tables (logical)

| Table | Key | Contents |
|---|---|---|
| `tasks` | `id` | Flattened `task.schema.yaml` top level: identity, repository/project, source, base ref/commit, target branch, parent, verification decision (jsonb), `state`, `state_entered_at`, `human_state`, `resume_state`, `needs_human` (jsonb), `blocked` (jsonb), `claimed_by`, `attempts` (jsonb), `current_generation`, `branch`, `pull_request` (jsonb), `tracking_issue`, `statuses` (jsonb), `latest_handoff`, `policy` (jsonb), `state_machine_revision`, `trace_id`, `version`, `next_watchdog_at`, timestamps |
| `task_ac_versions` | (`task_id`, `version`) | `acceptance-criteria.schema.yaml` document, `status`, `digest`, `frozen_at` (replaces plan 26's `acceptance_criteria` table) |
| `task_generations` | (`task_id`, `generation`) | `Generation` of `task.schema.yaml`: kind, status, start commit, AC version, candidate and verified test commit, manifest digest/key, judge approval, implementation commit, archive tag |
| `verifications` | `id` (uuid) | task, generation, phase (`red`,`green`,`judge_validate`,`judge`), expected SHA, pipeline number, `state` (`requested`,`running`,`classified`,`infra_failed`,`superseded`), result, evidence id, `retriggers`, `deadline_at` |
| `task_conflicts` | `conflict_id` (`sc-184-c1`) | `Conflict` of `task.schema.yaml` |
| `task_reviews` | `execution_id` | `Review` of `task.schema.yaml` (purpose, mapped verdict, checks, reviewed commit, findings digest) |
| `transitions` | `id`; UNIQUE (`task_id`,`seq`), UNIQUE (`task_id`,`event_key`) | `TransitionRecord` of `task.schema.yaml` (append-only) |
| `rejected_events` | `id` | task, at, actor, event, `event_key`, `reason_code` (`INVALID_STATE`, `GATE_NOT_SATISFIED`, `GUARD:<id>`), detail; 400-day retention |

Additions to plan 26's `executions` table: `kind` (`producing`,`reviewing`,`auxiliary`), `generation`, `return_state`, `fingerprint`, `failure_signature`, `refunded` (bool).

Constraints that make the rules physical:

```sql
-- one active execution per task (SPEC §73 atomic claims; plan 26 ALREADY_RUNNING)
CREATE UNIQUE INDEX executions_one_active ON runner.executions (task_id)
  WHERE state IN ('QUEUED','STARTING','RUNNING');
-- one pending verification per task
CREATE UNIQUE INDEX verifications_one_pending ON runner.verifications (task_id)
  WHERE state IN ('requested','running');
-- transitions are idempotent per event and ordered per task
ALTER TABLE runner.transitions ADD CONSTRAINT transitions_event_key UNIQUE (task_id, event_key);
ALTER TABLE runner.transitions ADD CONSTRAINT transitions_seq UNIQUE (task_id, seq);
-- history is append-only
REVOKE UPDATE, DELETE ON runner.transitions, runner.task_ac_versions FROM runner_app;
```

Columns fall into three classes (TDD-7). **Set-once** (NULL → value once; value → other value rejected by trigger): `task_generations.test_commit`, `test_manifest_*`, `judge_approval`, `implementation_commit` (written once, when the final review accepts the head — T041/T042), `archive_tag`, `superseded_reason`, and `verifications.commit`, `pipeline`, `result`, `evidence_id`. **Append-only child rows**: `runner.generation_candidates (task_id, generation, seq, commit, execution_id, red_result)` and `runner.generation_implementation_commits (task_id, generation, seq, commit, execution_id)` — every implementer retry adds a row; per-execution commits also live on `executions.result_commit`. **Current pointers** (freely updatable, derived from the latest child row): `candidate_test_commit`, the task-level `tdd.implementation_commit` shown before review, and status columns (`status`, `state`, `retriggers`). Any re-RED after a valid RED opens a **new generation** (`superseded_reason: strengthened`, start = red(g)), so a generation's manifest is written exactly once.

### 6.2 Record invariants

Checked by the engine on every transition and by the nightly consistency job `sc-runner-consistency` (plan 26 CronJob, reports `sc_task_invariant_violations`):

1. `tasks.state` is a state of the loaded machine revision; `history[-1].to_state == state`; `seq` contiguous.
2. `needs_human` present ⇔ `state = NEEDS_HUMAN`; `resume_state` present when `state ∈ {NEEDS_HUMAN, BLOCKED}` and never terminal, held or execution-required.
3. `claimed_by` = `execution:<id>` ⇔ a non-terminal execution exists; `ci:woodpecker/<n>` (or `ci:pending/<uuid>` before the pipeline number is known) ⇔ a pending verification exists; else null.
4. In a state with `activeExecution: required` a non-terminal execution exists, except for at most `spec.watchdog.groups.noExecution.alertAfter` (crash window; watchdog alerts).
5. `tdd.current_generation` equals the highest `task_generations.generation` with `status = active`.

## 7. External interfaces

### 7.1 State-machine library (F05)

A pure Go package used by the Runner, by CI contract tests and by the C10 fake flows. No I/O.

| Function | Semantics |
|---|---|
| `Load(table []byte, guards GuardRegistry, effects EffectRegistry) (*Machine, error)` | Schema validation + §8.2 invariants. Unknown guard/effect identifier → error. |
| `Fire(m, snapshot TaskSnapshot, ev Event, facts Facts) (Decision, error)` | §8.1. Returns either `Applied{transition_id, from, to, effects[], failure_class, counter deltas, reason_code}` or `Rejected{code, failed_guards[]}`. Deterministic for equal inputs. |
| `NextAllowedRoles(m, snapshot, facts) []Role` | Roles for which a dispatch event would currently fire (MCP `next_allowed_roles`). |
| `Project(m, snapshot) (HumanState, label)` | Human projection (§14.13). |
| `Revision(m) int` | `metadata.revision`. |

`Facts` is an interface the Runner implements (Git facts from Forgejo, evidence classification from plan 22, policy values, counters). Tests use a table-driven fake.

### 7.2 Mapping of Runner inputs to events

| Source | Condition | Event |
|---|---|---|
| `agent_start` with `task` | creation (RA-3: every plan 26 §14.2 dispatch check and every `Fire` guard is first evaluated against the would-be task; only if all pass are the task, criteria, transitions, execution and idempotency row inserted, all in the single §8.3 transaction — on any failure nothing is created and the idempotency key stays unused) | `task_created`, then (verification-first, judge source `acceptance-criteria`) `judge_bound`, then the dispatch event below, in **one** DB transaction |
| `agent_start` role `tester` | — | `dispatch_tester` |
| `agent_start` role `implementer` | test-first | `dispatch_implementer` |
| `agent_start` role `implementer` or `researcher` | verification-first and role ∈ class `producerRoles` and state ∈ {`JUDGE_APPROVED`,`CHANGES_REQUESTED`,`TEST_SPEC_CONFLICT`} | `dispatch_producer` |
| `agent_start` role `reviewer` | state `GREEN_VERIFIED` | `dispatch_reviewer` |
| `agent_start` any other role/state | — | `dispatch_auxiliary` |
| `agent_cancel` disposition `cancelled` / `needs_human` | — | `cancel_requested` / `escalate` |
| Result ingestion, execution `SUCCEEDED` | tester/judge author `completed` or `already_satisfied` | `tester_result_accepted` |
| " | implementer `completed` (test-first) | `implementer_result_accepted` |
| " | producer `completed` (verification-first) | `producer_result_accepted` |
| " | implementer/producer `test_spec_conflict` | `implementer_reported_conflict` |
| " | producing role `blocked` | `execution_blocked` |
| " | reviewer in `REVIEW` | `review_accepted` / `review_changes_requested` / `review_rejected` (verdict mapping plan 21 §14.7) |
| " | reviewer in `TEST_SPEC_CONFLICT` | `adjudication_recorded` |
| " | any auxiliary execution (any terminal state) | `auxiliary_result_recorded` |
| Execution `FAILED`/`TIMED_OUT`, or `CANCELLED` by emergency stop | producing or reviewing execution | `execution_failed` (class and code per §15.2) |
| Evidence poller | classified RED / GREEN / judge-validate / judging evidence | `red_classified` / `green_classified` / `judge_classified` / `judging_classified` |
| Evidence poller | pipeline error, timeout, not created, evidence rejected | `verification_infra_failed` |
| Runner dispatch refusal | `BUDGET_EXCEEDED` on the `task:` scope → `dispatch_refused` (T132 → NEEDS_HUMAN `budget_exceeded`); `ATTEMPTS_EXHAUSTED` other than `identical_replay` → `dispatch_refused` (T133 → NEEDS_HUMAN `retries_exhausted`); shared-scope `BUDGET_EXCEEDED` and `EMERGENCY_STOP` refusals leave the state unchanged (plan 33 §14.5) | `dispatch_refused` |
| Evidence poller | `EVIDENCE_AMBIGUOUS` (plan 20 §8.5, class policy) | `verification_evidence_forged` |
| Forgejo `pull_request` closed+merged | task PR | `pr_merged` |
| Forgejo `pull_request` closed, not merged, sender not machine account | task PR | `pr_closed_by_human` (actor `human:<login>`) |
| Forgejo `push` / resync | `agent/<task>` moved to a SHA that is not (a) the accepted commit of an execution of this task, (b) a Runner reset/archive operation | `head_changed_externally` |
| Forgejo `issue_comment` | authorized `/sc` command (§7.3) | command event |
| Watchdog | `now − state_entered_at > escalateAfter` | `stall_timeout` |

Pushes that occur while a producing execution is active are judged by ingestion (`BRANCH_MISMATCH` → policy) and do not raise `head_changed_externally`.

MCP error mapping when `Fire` rejects a dispatch (plan 26 §14.2 step 8): only `G_NO_ACTIVE_EXECUTION` failed → `ALREADY_RUNNING`; only `G_ATTEMPTS_REMAIN` / `G_REVIEWS_REMAIN` / `G_AUXILIARY_REMAIN` failed → `ATTEMPTS_EXHAUSTED`; `G_NOT_IDENTICAL_REPLAY` failed → `ATTEMPTS_EXHAUSTED` with `details.allowed = ["identical_replay"]`; any other → `GATE_NOT_SATISFIED` with `details.task_state`. `agent_cancel` on `BLOCKED`/`NEEDS_HUMAN` from Hermes → `INVALID_STATE`; on a terminal task → `TASK_TERMINAL`.

### 7.3 Human command channel (`/sc` commands; ADR-0350)

Human-only transitions are requested by commenting on the task's **tracking issue or PR** in Forgejo. Forgejo authenticates the human (Authentik SSO, plan 15); the Runner authorizes.

**Syntax.** The comment's first line is `/sc <command> [args]`. The rest of the comment is the free-text reason (≥ 10 characters after trimming, required). An optional fenced ```` ```yaml ```` block carries a structured payload. Only the `issue_comment` event with action `created` is processed; edits and deletions are ignored.

**Authorization (all must hold, else the Runner replies with a rejection comment and records `rejected_events`):**
1. Webhook HMAC valid and delivery not seen before (plan 26).
2. The sender is a **human account** (CD-15): `sender.login` (verify the payload field at C03) is not in `spec.humanAuthority.machineAccounts` (default `[sc-runner, sc-woodpecker, sc-renovate, sc-backup, sc-admin, sc-ci-config]`), does not start with `sc-` (reserved for machine accounts, plan 16), and the Forgejo user record is an individual account, not a bot or organization (`GET /api/v1/users/{username}`; verify the field at C03).
3. The sender is a member of one of `spec.humanAuthority.forgejoTeams` (default `[Owners, engineers]`) of the repository's organization, checked live with `GET /api/v1/orgs/{org}/teams` and `GET /api/v1/teams/{id}/members/{username}` as `sc-runner` (verify paths at C03; 29-AC-31 re-verifies).
4. The comment is on the issue or PR linked to the task (`tracking_issue` or `pull_request`). Exception: `/sc accept-external-commit` may also be posted on a **non-task** PR to adopt foreign commits there (plan 55 §7.2, SEC-2); it then changes no task state and only records the adoption.
5. At most `spec.humanAuthority.maxCommandsPerTaskPerHour` (default 10) accepted commands per task per hour.

Hermes holds **no** Forgejo account (CD-15) and workers hold only deploy keys (ADR-0007), so neither can author comments.

**Commands:**

| Command | Event | From states (table) | Payload |
|---|---|---|---|
| `/sc status` | — (read-only reply) | any | — |
| `/sc block` | `block` | any non-terminal (T063, T064) | — |
| `/sc unblock` | `unblock` | `BLOCKED` (T065) | — |
| `/sc retry [grant=N]` | `retry` | `NEEDS_HUMAN` (T066) | `N` 1–20, default `spec.retry.humanGrant` |
| `/sc reverify red` / `/sc reverify green` | `reverify_red` / `reverify_green` | T067 / T068, T069 | — |
| `/sc respec` | `respec` | T070 | `acceptanceCriteria: [{id, text, kind}]` + reason (becomes `change_reason`) |
| `/sc done` | `declare_done` | T071 | — |
| `/sc cancel` | `cancel_requested` | T060, T061 | — |
| `/sc escalate` | `escalate` | T062 | — |
| `/sc accept-test-change` | `approve_test_change` | `TEST_SPEC_CONFLICT` (T083) | optional `guidance:` text for the tester (≤ 4000 chars) |
| `/sc strengthen-tests` | `strengthen_tests` | `ALREADY_SATISFIED`, `NEEDS_INVESTIGATION` (T016; enables T015/T131) | optional `guidance:` |
| `/sc accept-external-commit <sha> [<sha>…]` | `accept_external_commit` | `NEEDS_HUMAN` (T072, T073) | the listed SHAs must be exactly the commits in `branch head..` not produced by accepted executions; recorded as `human_commit` provenance; GREEN and a new final review follow |
| `/sc reject-conflict` | `reject_conflict` | `TEST_SPEC_CONFLICT` (T084, T085) | — |
| `/sc approve-judge` | `approve_judge` | `JUDGE_AWAITING_APPROVAL` (T121) | — |
| `/sc reject-judge` | `reject_judge` | `JUDGE_AWAITING_APPROVAL` (T122) | `findings:` list |
| `/sc deny-exemption` | `deny_exemption` | T123 | — |

The Runner replies on the same thread within 30 s: `Accepted: <command> → <transition id>, <from> → <to>` or `Rejected: <code>` (`NOT_AUTHORIZED`, `INVALID_STATE`, `GATE_NOT_SATISFIED`, `PAYLOAD_INVALID`, `RATE_LIMITED`). Accepted commands record `actor = human:<login>`, `event_key = comment:<comment id>`, and the payload in `transitions.decision`. Each accepted human transition also posts the informational alert `SCHumanTaskDecision` (silent class; audit trail).

### 7.4 Admin API equivalence (break-glass)

`POST /v1/tasks/{id}/transitions` (loopback, `SC-RUNNER-ADMIN`; plan 26) is the same set of human events. Per CD-30 the actor is derived from the credential, never from a header: a per-operator credential `scadm_<kid>.<secret>` yields `human:<operator identity>`, the break-glass credential yields `breakglass:runner-admin`; a request carrying `X-SC-Actor` is rejected with 400, and `X-SC-Reason` is required and stored as the transition reason. The credential key id is recorded on the transition row (`credential_kid`). Human-only transitions accept both actors; `breakglass:runner-admin` transitions additionally raise `SCRunnerAdminAccess`. The body's `decision` object MUST carry `command` (one of the §7.3 command names) and optional `args`/`payload`; `expected_from` must equal the current state; `to` must equal the state the machine would reach (for `unblock`/`retry`, the recorded `resume_state`; for self-transitions the current state). Mismatch → 409 `INVALID_STATE`. Admin API use raises `SCRunnerAdminAccess` (plan 26). Interface change for plan 26: document `decision.command` as required.

### 7.5 Notifications

`E_NOTIFY_HUMAN:<reason>` posts `SCTaskNeedsHuman` (or `SCTaskRetriesExhausted` when the reason is `retries_exhausted`) to Alertmanager per `notification.schema.yaml` with `subject = <task-id>` (ADR-0555). Reasons are the `NeedsHumanReason` enum of `task.schema.yaml`. Plan 42 must add `worker_blocked`, `merged_without_provenance`, `external_push`, `stalled`, `approval_required` to `notification.schema.yaml` (cross-plan item). Notifications are also posted for states that project to *Needs Human* without being `NEEDS_HUMAN` (`ALREADY_SATISFIED`, `NEEDS_INVESTIGATION`, `JUDGE_AWAITING_APPROVAL`, conflicts awaiting human approval). The alert resolves when the task leaves the triggering state.

### 7.6 Outcome view for plan 32

```sql
-- Outcome booleans stay NULL until the outcome has been judged (plan 32: unknown is not false).
CREATE VIEW runner.v_execution_outcomes AS
SELECT e.id AS execution_id, e.task_id,
       CASE WHEN e.state IN ('QUEUED','STARTING','RUNNING') THEN NULL
            ELSE e.state = 'SUCCEEDED' END                                   AS worker_success,
       (SELECT bool_or(v.result IN ('red_valid','judge_valid')) FROM runner.verifications v
         WHERE v.task_id=e.task_id AND v.commit=e.result_commit AND v.phase IN ('red','judge_validate')
           AND v.state='classified')                                         AS red_verified,
       (SELECT bool_or(v.result IN ('green_pass','judging_pass')) FROM runner.verifications v
         WHERE v.task_id=e.task_id AND v.commit=e.result_commit AND v.phase IN ('green','judge')
           AND v.state='classified')                                         AS green_verified,
       (SELECT bool_or(v.state='classified') FROM runner.verifications v
         WHERE v.task_id=e.task_id AND v.commit=e.result_commit AND v.phase IN ('green','judge'))
                                                                             AS ci_passed,
       (SELECT r.verdict='accept' FROM runner.task_reviews r WHERE r.task_id=e.task_id
         AND r.purpose='final_review' ORDER BY r.at DESC LIMIT 1)            AS review_accepted,
       CASE WHEN t.state IN ('DONE','CANCELLED') THEN (t.pull_request->>'state') = 'merged' AND t.state='DONE'
            ELSE NULL END                                                    AS human_accepted,
       e.failure_class, t.state AS final_state
FROM runner.executions e JOIN runner.tasks t ON t.id = e.task_id;
```

(`ci_passed` is true when the GREEN/judging pipeline produced classified evidence for that commit regardless of verdict; `green_verified` is the verdict.)

### 7.7 Needs-human view for Grafana (plan 39, with plan 26)

```sql
CREATE VIEW reporting.v_tasks_needing_human WITH (security_invoker = false) AS
SELECT t.id AS task_id, t.title, t.repository,
       COALESCE(t.needs_human->>'reason',
                CASE t.state WHEN 'JUDGE_AWAITING_APPROVAL' THEN 'approval_required'
                             WHEN 'ALREADY_SATISFIED' THEN 'already_satisfied'
                             WHEN 'NEEDS_INVESTIGATION' THEN 'needs_investigation'
                             ELSE 'test_spec_conflict' END)                 AS reason,
       t.state_entered_at AS entered_at,
       t.tracking_issue->>'url' AS task_url,
       t.trace_id
FROM runner.tasks t
WHERE t.human_state = 'Needs Human';
```

It lists every task whose projection is *Needs Human* (plan 29 §14.13), not only `NEEDS_HUMAN`. Columns are exactly those plan 39 consumes; the role `sc_reporting` gets `SELECT` only (plan 26 owns grants).

## 8. Internal interfaces

### 8.1 Evaluation rule

For event `ev` with actor `a` (class `c`) on task snapshot `t` (mode `m`, state `s`):

1. Candidates = transitions with `s ∈ from` (or `$none` for creation), `event == ev`, `c ∈ actors`, `m ∈ modes`, ordered by `id`.
2. For each candidate, evaluate guards in listed order; the first candidate whose guards all pass **fires**. Guard evaluation reads only the snapshot and `Facts` (no side effects).
3. If none fires: `Rejected`. The rejection is stored in `rejected_events`; for Hermes calls it maps to an MCP error (§7.2); for CI and Runner events it raises `SCTaskEventRejected` when the event was evidence or a result (fail closed: the evidence is kept, the state does not change, the watchdog eventually escalates).
4. Resolve pseudo-targets: `$same` = `s`; `$return` = the execution's recorded `return_state` (the state from which it was dispatched; must be in `s.returnTargets`); `$resume` = `t.resume_state` (§14.9).
5. Apply in one DB transaction (§8.3): update task row and counters, insert transition row, apply DB-local effects, insert outbox rows for external effects (PR, status, labels, alerts, pipeline triggers, branch operations).

### 8.2 Loader invariants (F05 `Load`, CI `make state-machine-check`)

Schema validity plus every invariant listed in `state-machine.schema.yaml` `description`, and:

- every state with `stall.escalateTo` has a `stall_timeout` transition to that state;
- for each (state, event, actor class, mode) the candidate list is non-empty only for events listed in the state's mapping table (§7.2), and every verification state has a candidate for every outcome of its phase (`red_classified`: `red_valid`, `red_invalid`, `already_satisfied`, `partially_satisfied`, `baseline_failing`, `inconclusive`; `green_classified`: `green_pass`, `green_fail`, `tamper_detected`; `judge_classified`: `judge_valid`, `judge_invalid`, `already_satisfied`, `partially_satisfied`, `baseline_failing`, `inconclusive`; `judging_classified`: `judging_pass`, `judging_fail`, `tamper_detected`) — completeness, so no outcome is silently dropped;
- every `execution_failed` source state covers all four failure classes;
- reachability and termination are computed per mode, with `$return` expanded to `returnTargets` and `$resume` to every non-held, non-terminal, non-execution-required state of the mode.

### 8.3 Atomic claims and the transition transaction

```sql
BEGIN;  -- READ COMMITTED
SELECT state, version FROM runner.tasks WHERE id = $task FOR UPDATE;   -- serializes events per task
-- Go: Fire(...) on the locked snapshot
UPDATE runner.tasks
   SET state=$to, state_entered_at=now(), version=version+1, claimed_by=$claim, attempts=$attempts, ...
 WHERE id=$task AND version=$expected_version;                         -- optimistic check for API callers
INSERT INTO runner.transitions (task_id, seq, event_key, ...) VALUES ($task, $seq, $event_key, ...);
INSERT INTO runner.executions (...) ...;      -- E_CREATE_EXECUTION; executions_one_active enforces one claim
INSERT INTO runner.outbox (dedup_key, ...) ... ON CONFLICT (dedup_key) DO NOTHING;
COMMIT;
```

- A unique violation on `transitions_event_key` means the event was already applied: the transaction rolls back and the caller receives the stored outcome (idempotent replay).
- A unique violation on `executions_one_active` → `ALREADY_RUNNING`.
- Background scanners claim work without contention: the dispatcher `SELECT … FROM runner.executions WHERE state='QUEUED' ORDER BY queued_at FOR UPDATE SKIP LOCKED LIMIT 1`; the watchdog and evidence poller `SELECT id FROM runner.tasks WHERE next_watchdog_at <= now() ORDER BY next_watchdog_at FOR UPDATE SKIP LOCKED LIMIT 50`. A row locked by an in-flight transition is simply skipped this round.
- API callers that pass `expected_from` (admin API) or the MCP layer get `INVALID_STATE` when `version` changed between read and write.

## 9. Configuration

| Key (`policy/verification.yaml`, kind `VerificationPolicy`) | Default | Repository may | Notes |
|---|---|---|---|
| `spec.retry.maxAutonomousAttempts` | 5 | lower | implementation attempts (implementer/producer) per task, SPEC §74; Hermes `max_attempts` may also lower per task. Equals the plan 31 implementer ladder 2+2+1 |
| `spec.retry.maxTesterAttempts` | 3 | lower | tester / judge-author attempts per task |
| `spec.retry.maxReviewExecutions` | 3 | lower | |
| `spec.retry.maxAuxiliaryExecutions` | 8 | lower | researcher/planner/adjudication/test review |
| `spec.retry.maxBlockedResults` | 2 | lower | |
| `spec.retry.infrastructure.maxConsecutive` | 3 | lower | L-4 |
| `spec.retry.infrastructure.maxTotal` | 8 | lower | |
| `spec.retry.infrastructure.queueTimeout` | 2h | shorten | RA-7 |
| `spec.retry.infrastructure.maxConsecutiveTimeouts` | 2 | lower | consecutive `TIMED_OUT` executions (infrastructure class) before `NEEDS_HUMAN`; requested by plan 33 Q2 |
| `spec.retry.infrastructure.backoffSeconds` | `[60, 300, 900]` | raise | delay before requeue n |
| `spec.retry.policy.maxViolations` | 0 | — (fixed 0) | any violation → NEEDS_HUMAN |
| `spec.retry.identicalReplay` | `forbid` | — (fixed) | §16.4 |
| `spec.retry.humanGrant` | 3 | — | default `/sc retry` grant |
| `spec.testSpecConflict.maxConflictsPerTask` | 2 | lower | |
| `spec.testSpecConflict.maxGenerations` | 3 | lower | |
| `spec.testSpecConflict.resolutionApproval` | `human` | tighten only (`adjudicator` → `human`) | ADR-0355 |
| `spec.watchdog.groups.<g>.{alertAfter, escalateAfter}` | from `state-machine.yaml` (`specified` 168h/–, `noExecution` 30m/–, `verification` 1h/4h, `hermesWait` 24h/72h, `investigation` 72h/–, `awaitingMerge` 168h/–, `needsHuman` 72h/–, `approval` 72h/–) | shorten | |
| `spec.humanAuthority.forgejoTeams` | `[Owners, engineers]` | narrow | |
| `spec.humanAuthority.machineAccounts` | `[sc-runner, sc-woodpecker, sc-renovate, sc-backup, sc-admin, sc-ci-config]` | extend | every `sc-*` login is refused regardless (CD-15) |
| `spec.humanAuthority.maxCommandsPerTaskPerHour` | 10 | lower | |

Retry policy lives **only** at `spec.retry` of `policy/verification.yaml` (kind `VerificationPolicy`, schema `verification.schema.yaml#/$defs/RetryPolicy`); there is no `policy/retry.yaml` and no `RetryPolicy` kind (CD-11).

`contracts/state-machine.yaml` itself is code-adjacent, not tunable policy: it is compiled into the Runner (`go:embed`) and changes only through a reviewed PR that bumps `metadata.revision` (§19). `policy/**` and `contracts/**` are protected paths (ADR-0008), so no agent task can change the machine or its policy.

## 10. Secrets

N/A for new classes. This subsystem holds no secret. It relies on `SC-RUNNER-WEBHOOK` (authenticity of Forgejo events carrying human commands), `SC-FORGEJO-BOT-TOKENS` (`sc-runner` team-membership checks and replies) and `SC-RUNNER-ADMIN` (admin API), all defined by plans 26/16/35. Task records, transitions and decision payloads never contain secret values; decision payloads pass the same secret scrubber as worker results before storage (plan 26 §14.4 step 7).

## 11. Storage

`sc-pg` only. Estimates at MVP: ≤ 50 transitions per task, ~1 KiB each; ≤ 20 tasks/day ⇒ < 400 MiB/year including generations and verifications. Retention: tasks, transitions, generations, verifications, conflicts and reviews are kept indefinitely (exact provenance; SPEC §56, §107); `rejected_events` 400 days. Evidence documents live in S3 `evidence` (plan 22) and are referenced by digest.

## 12. Resource requirements

N/A as a separate workload — the library runs inside the Runner process (plan 26 §12). Budget: `Fire` ≤ 1 ms CPU per event excluding `Facts` I/O; the machine table occupies < 1 MiB of memory. The watchdog scan adds ≤ 50 rows per 30 s pass.

## 13. Security boundary

- **Actor classes and their authentication.** `runner`: internal code paths only. `ci`: an evidence document the Runner fetched itself from a pipeline it created (ADR-0008; `G_EVIDENCE_AUTHENTIC`). `hermes`: the MCP bearer (`SC-RUNNER-HERMES`), only through `agent_start`/`agent_cancel`. `human`: an authorized `/sc` comment (§7.3) or the loopback admin API. The `worker` class does not exist; the `worker:<id>` actor pattern in `mcp-surface.schema.yaml` `Transition.actor` is never produced by this plan (cross-plan note to 26: remove it).
- **What Hermes can and cannot do.** Hermes can create tasks, dispatch roles where a guard allows, cancel, and escalate to `NEEDS_HUMAN`. It cannot: leave `BLOCKED`/`NEEDS_HUMAN`, approve a corrected test, approve an exemption or judge, revise criteria, declare done, re-run verification, or enter `BLOCKED`. A prompt-injected Hermes can waste attempts (bounded) but cannot weaken a test, skip RED or produce a merge status.
- **Human authority** is bounded by team membership and is audited (`SCHumanTaskDecision`, transition rows). The break-glass Forgejo admin (`sc-admin`) is excluded from `/sc` by default; break-glass changes go through the admin API, which raises `SCRunnerAdminAccess`.
- **Fail closed:** missing facts (Forgejo down, evidence unparsable) make guards fail; the event is rejected and retried by its source, never assumed.

## 14. Happy path

### 14.1 Canonical test-first trajectory (SPEC §129)

| # | Event (actor) | Transition | State after | Key effects |
|---|---|---|---|---|
| 1 | `task_created` (hermes) | T001 | `SPECIFIED` | base commit frozen, AC v1, tracking issue |
| 2 | `dispatch_tester` (hermes) | T010 | `TEST_AUTHORING` | AC v1 frozen, generation 1, execution `x1` QUEUED |
| 3 | `tester_result_accepted` (runner) | T014 | `RED_VERIFYING` | candidate red commit, RED pipeline triggered |
| 4 | `red_classified=red_valid` (ci) | T020 | `RED_VERIFIED` | manifest frozen, draft PR, statuses pending |
| 5 | `dispatch_implementer` (hermes) | T030 | `IMPLEMENTING` | execution `x2` from the red commit |
| 6 | `implementer_result_accepted` (runner) | T032 | `GREEN_VERIFYING` | GREEN pipeline triggered |
| 7 | `green_classified=green_pass` (ci) | T033 | `GREEN_VERIFIED` | PR ready |
| 8 | `dispatch_reviewer` (hermes) | T040 | `REVIEW` | execution `x3` |
| 9 | `review_accepted` (runner) | T041 | `AWAITING_MERGE` | both statuses success |
| 10 | `pr_merged` (runner) | T050 | `DONE` | facts re-verified, branch archived/deleted, issue closed |

**Base commit rule (TDD-10, RA-1; `G_BASE_COMMIT_RESOLVED`).** `target_branch` (default `main`) must be a protected branch of the merged policy. `base_ref` must be that branch (base = its tip) or a 40-hex SHA that is an ancestor of its tip, at most `spec.task.maxBaseLag` behind (default 20 commits and 24 h, plan 22 §9.1), whose `.strange-company/{repo,policy,validation}.yaml` blobs equal the tip's. Tags, `agent/*`, `sc-archive/*`, `sc-quarantine/*` and other refs are refused (`BASE_REF_NOT_ALLOWED`, plan 26), so Hermes cannot select an older, looser policy revision. Merge fact F12 (plan 22) re-checks the policy blobs at merge.

### 14.2 Tester retries (TEST_NEEDS_WORK)

**Strengthening after valid RED** (T012) is allowed only while no implementer has ever been dispatched in the generation (`G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION`); it opens a new generation (`strengthened`, start = red(g)) and the new RED must be a superset of the old manifest (plan 22 R12). After any implementer dispatch, test changes need TEST_SPEC_CONFLICT and a human (TDD-5).

RED evidence classified `red_invalid` (T021) counts one merit failure. Hermes re-dispatches a tester (T011) with `prior_attempt_evidence` (the red-result reasons) in context; the new tester commit continues on the branch head. The red range of the generation is `(generation start, head]`; every commit in it must be a tester commit. Exhaustion → `NEEDS_HUMAN` through T103 when attempts run out at dispatch time is impossible (`G_ATTEMPTS_REMAIN` refuses the dispatch → Hermes gets `ATTEMPTS_EXHAUSTED` and escalates; the watchdog escalates after 72 h otherwise).

### 14.3 GREEN retries

`green_fail` (T034) returns to `RED_VERIFIED`, merit + 1; the next implementer continues from the current head (phase `green_retry`, plan 32) with the green-result in `prior_attempt_evidence`. `green_fail` with attempts exhausted → T035.

### 14.4 Review outcomes

Accept → `AWAITING_MERGE` (T041) or `DONE` for non-mergeable verification-first work (T042); changes requested → `CHANGES_REQUESTED` (T044, merit) → implementer again (T030); reject_bypass → `NEEDS_HUMAN` (T046, policy). An accepting review that cannot be applied (head moved, provenance incomplete) fails closed to `NEEDS_HUMAN` (T043).

### 14.5 ALREADY_SATISFIED and NEEDS_INVESTIGATION (SPEC §51)

Entered from RED (T022, T023) or judge validation (T119, T120). Both notify a human and project to *Needs Human*, while Hermes may still dispatch a researcher or read-only reviewer to investigate (T090). Re-authoring the tests needs a human `/sc strengthen-tests` first (T016), after which Hermes dispatches the tester (T015); the previously declared tests must stay in the next RED (plan 22 R12), so the earlier passing tests remain as preservation evidence (TDD-6). Humans may `respec` (T070), `done` (T071; tests are not merged — they stay on the archive tag), `reverify red` (T067, e.g. after fixing a broken baseline on main) or `cancel`. No artificial failure is ever manufactured.

### 14.6 TEST_SPEC_CONFLICT (SPEC §53, §70)

1. Implementer result `test_spec_conflict` → T080 (conflict recorded; a pushed commit, if any, is archived and the branch reset to the execution's start commit). The attempt stays consumed.
2. Hermes dispatches a read-only reviewer as adjudicator (T090, `dispatch_auxiliary`, purpose `adjudication`); its result → T082 records `test_valid` / `test_invalid` / `spec_ambiguous` (mapping in plan 21 §14.6). The **first** adjudication of a conflict is decisive; later adjudications are recorded but can only tighten (`test_invalid` → `test_valid`); if two adjudications disagree, human approval is required regardless of `resolutionApproval` (TDD-20). The reviewer's `role_output.purpose` must equal the packet purpose (plan 26 ingestion, `RESULT_IDENTITY_MISMATCH`).
3. `test_valid` → Hermes dispatches the implementer again (T031) with the decision as context.
4. `test_invalid` → with `resolutionApproval: human` (default) the Runner notifies (`test_spec_conflict`); a human comments `/sc accept-test-change` (T083) or `/sc reject-conflict` (T084). With `adjudicator`, the adjudication itself sets approval.
5. Approved → Hermes dispatches a tester (T013): **new generation** starting at the previous generation's red commit. The Runner tags the old head `sc-archive/<task>/g<n>`, deletes `agent/<task>` and recreates it at the previous red commit through the Forgejo API, so the tester runs in branch mode `continue` from that commit (plan 27 topology row "tester re-authoring" changes accordingly); implementation commits of the old generation are thereby dropped from the branch; the corrected test is a new red commit; a new RED verification and a new manifest follow. The old generation's provenance (red commit, evidence, manifest, conflict) stays in the record.
6. `spec_ambiguous` → humans `respec` (T070).

### 14.7 Merge and DONE

Defined in plan 55 §14.7. The machine requires `G_MERGE_PROVENANCE_VERIFIED` at `pr_merged` (T050); otherwise `NEEDS_HUMAN` with `merged_without_provenance` (T051), and any merge before `AWAITING_MERGE` is always a bypass (T052).

### 14.8 Verification-first path (SPEC §57; ADR-0353)

1. Creation validates `exemption_request` against merged policy (plan 22 §9.2). Disallowed → the request fails with `EXEMPTION_NOT_ALLOWED`; there is no fallback mode switch at creation.
2. Judge source `acceptance-criteria` (e.g. `research`): `judge_bound` → `JUDGE_APPROVED` when approval is `auto` (T002) or `JUDGE_AWAITING_APPROVAL` when `human` (T003).
3. Judge source `artifact`: tester dispatch → `JUDGE_AUTHORING` (T110); deterministic judge → `JUDGE_VALIDATING` (T114) → CI proves well-formed and failing on base → `JUDGE_APPROVED` (auto, T116) or `JUDGE_AWAITING_APPROVAL` (T117); rubric judge → `JUDGE_AWAITING_APPROVAL` directly (T115).
4. Human `/sc approve-judge` (T121) approves exemption and judge in one decision; `/sc reject-judge` → `TEST_NEEDS_WORK`; `/sc deny-exemption` → `SPECIFIED` in test-first mode (T123; tightening).
5. Producer dispatch (T124) → `PRODUCING` → `JUDGING` (T126) → `GREEN_VERIFIED` (T127) → `REVIEW` → `AWAITING_MERGE`/`DONE`.

### 14.9 Resume rules (`$resume`)

`E_RECORD_RESUME_STATE` stores, at entry to `NEEDS_HUMAN` or `BLOCKED`:
- if the task was in an execution-required state: the execution's `return_state` (the dispatch-ready state it came from);
- if it was in a verification state: that verification state (resume re-triggers the pipeline at the same SHA);
- otherwise: the current state.
Entering `BLOCKED` from `NEEDS_HUMAN` keeps the existing `resume_state`. `G_RESUME_STATE_VALID` re-checks the target's preconditions at resume (e.g. resuming into `RED_VERIFIED` requires `G_RED_EVIDENCE_VALID`; into a verification state requires the head to still equal the recorded SHA, else the human must use `reverify`).

### 14.10 Cancellation

`cancel_requested` (T060/T061) and `pr_closed_by_human` (T054): cancel the active execution (gateway token revoked first, then task token and deploy key; plan 26 §14.6), mark the pending verification superseded and request pipeline cancellation, close the PR with a comment, tag `sc-archive/<task>/cancelled`, delete the branch, close the issue with `resolution/cancelled`. Cancelling never consumes an attempt and never deletes provenance. Cancelled executions record `CANCELLED` with no failure class.

### 14.11 Parent and child tasks (no DAG scheduling in MVP)

- `parent_task_id` is set only at creation, must reference an existing non-terminal task in the same project, and depth ≤ 3 (cycles are impossible: the parent exists first and the field is immutable).
- The Runner never dispatches anything because of a parent/child relationship [SPEC §128 "task DAG scheduling" is post-MVP]. Hermes coordinates.
- `G_CHILDREN_TERMINAL` guards the non-merge completions (T042, T071). A mergeable parent can still be merged (merge is a human act); open children are listed in the PR body.
- Cancelling a parent does not cascade; `agent_status` of the child shows `parent_task_id`, and the watchdog's normal thresholds apply.

### 14.12 Execution machine coupling

| Task state class | Execution rule |
|---|---|
| `activeExecution: required` (`TEST_AUTHORING`, `IMPLEMENTING`, `REVIEW`, `JUDGE_AUTHORING`, `PRODUCING`) | exactly one producing/reviewing execution `QUEUED/STARTING/RUNNING`; its terminal outcome produces the next event |
| `verification` (`*_VERIFYING`, `JUDGE_VALIDATING`, `JUDGING`) | no execution; exactly one pending verification |
| `optional` | at most one auxiliary execution; producing dispatch requires none active |
| `forbidden` (`AWAITING_MERGE`, `NEEDS_HUMAN`, `BLOCKED`, `JUDGE_AWAITING_APPROVAL`, terminal) | entering cancels any active execution (`E_CANCEL_ACTIVE_EXECUTION`) |

Execution transitions are listed in `state-machine.yaml` `execution.transitions`; the Runner (plan 26) drives them. `QUEUED → STARTING` uses `FOR UPDATE SKIP LOCKED` (§8.3).

### 14.13 Human projection (labels, plan 55)

Many-to-one, first matching entry by `priority`: `NEEDS_HUMAN`, `JUDGE_AWAITING_APPROVAL`, `ALREADY_SATISFIED`, `NEEDS_INVESTIGATION`, and `TEST_SPEC_CONFLICT` awaiting human approval → **Needs Human**; `BLOCKED` → **Blocked**; `DONE`, `CANCELLED` → **Done** (with `resolution/*` label); `REVIEW`, `AWAITING_MERGE` → **Review**; `SPECIFIED` with no execution yet → **Ready**; every other non-terminal state → **In Progress**. **Backlog** is never produced by the Runner: it is a human label on Forgejo issues that have no task yet (plan 55 §14.1).

## 15. Failure modes

### 15.1 Table

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Event with no matching transition | `Fire` returns `Rejected` | state unchanged | `rejected_events` row; MCP error to Hermes; `SCTaskEventRejected` (warning) for evidence/result events | `stuck-task-recovery.md` |
| Lost race between two events on one task | `FOR UPDATE` serializes; optimistic `version` mismatch on API path | second caller gets `INVALID_STATE` | caller re-reads | — |
| Duplicate event (webhook redelivery, result replay) | `transitions_event_key` unique violation | none | stored outcome returned | — |
| Runner crash mid-transaction | Postgres rollback | no partial transition | event re-delivered by source (outbox, webhook, poller, MCP retry) | `agent-runner-failure.md` |
| Runner crash after commit, before effects | outbox rows pending | effects delayed | outbox replay on restart | — |
| Task in execution-required state without execution | watchdog (`noExecution` group, 30 m) | task stalls | `SCTaskStuck`; procedure decides `escalate` | `stuck-task-recovery.md` |
| Verification never returns | watchdog `verification` group (alert 1 h, escalate 4 h); pipeline deadline (plan 22) | stall | `verification_infra_failed` retrigger ≤ `maxInfraRetriggers`; then `NEEDS_HUMAN` (T025) or `stall_timeout` (T140) | `tdd-task-recovery.md` |
| Hermes stops acting | watchdog `hermesWait` (alert 24 h, escalate 72 h) | stall | `stall_timeout` → `NEEDS_HUMAN` (`stalled`) | `stuck-task-recovery.md` |
| Invalid state-machine table in a Runner build | `make state-machine-check` (CI) and `Load` at startup | build fails / Runner unready | none (fail closed) | fix by PR |
| Task row references unknown state after upgrade | startup scan | Runner unready | migration must map (§19) | `agent-runner-failure.md` |
| Forged human command (non-member, machine account) | §7.3 authorization | none | rejection comment, `rejected_events`, `SCTaskCommandRejected` (info) | — |
| Merge before `AWAITING_MERGE` or with failed facts | `pr_merged` → T051/T052/T053 | unverified code on protected branch | `NEEDS_HUMAN`, `SCTddProvenanceBypass` (critical) | `merge-without-provenance.md` |
| External push to agent branch | `head_changed_externally` → T055/T056 | provenance invalid | statuses pending, `NEEDS_HUMAN` (`external_push`) | `tdd-task-recovery.md` |
| Record invariant violation (§6.2) | consistency CronJob | inconsistent provenance | `SCTaskInvariantViolation` (critical); dispatch for that task refused | `stuck-task-recovery.md` |

### 15.2 Failure classification (authoritative)

| Source | Code / worker reason | Class | Execution state |
|---|---|---|---|
| Runner dispatch | `QUEUE_TIMEOUT` (QUEUED longer than `spec.retry.infrastructure.queueTimeout` in `policy/verification.yaml` — **the single authoritative definition, default 2 h**; plan 26's `queue.maxQueueAge` and plan 51's "6 h" must read this value — waiting for a worker slot or heavy-slot units; the Job is not unsuspended while waiting, so neither the Job deadline nor `START_TIMEOUT` runs during the wait — RA-7, plan 26) | infrastructure (refunded) |
| Runner dispatch | `START_TIMEOUT`, `IMAGE_PULL`, `JOB_LOST`, `RESULT_MISSING`, `DEPENDENCY_UNAVAILABLE` | infrastructure | FAILED (`START_TIMEOUT` also covers a STARTING pod that never fetches its packet; STARTING never ends `TIMED_OUT`) |
| Runner ingestion | `RESULT_SCHEMA_INVALID` | infrastructure | FAILED |
| Runner ingestion | `DIFF_TOO_LARGE`, `PROTECTED_PATH_VIOLATION`, `ROLE_DIFF_VIOLATION`, `TEST_TAMPER`, `DELETION_NOT_ALLOWED`, `SECRET_DETECTED`, `REF_VIOLATION`, `PERMISSION_DENIAL`, `RESULT_IDENTITY_MISMATCH`, `BRANCH_MISMATCH`, `COMMIT_MISMATCH`, `NOT_DESCENDANT_OF_START`, `MULTIPLE_COMMITS`, `COMMIT_NOT_FOUND`, `STATE_NOT_ALLOWED_FOR_ROLE` | policy | FAILED |
| Pod deadline after RUNNING, worker `harness_timeout` | `TIMED_OUT` | infrastructure (plan 26), capped by `maxConsecutiveTimeouts` = 2 | TIMED_OUT |
| Worker `failure.reason` | all reasons and details of plan 27 §14.15 (adopted as authoritative): infrastructure for transport/harness/clone/commit/stream failures; **merit** for `HARNESS_GAVE_UP` (`harness_error` with `max_turns:`/`claim_failed:`), `OUTPUT_CONTRACT_VIOLATED` (`malformed_harness_output` `claim:`) and `NO_CHANGES`; policy for `DIFF_PRECHECK_FAILED`, `SECRET_DETECTED`; `branch_exists`/`branch_moved`/`push_rejected` infrastructure unless the ref audit upgrades to `REF_VIOLATION` | as listed | FAILED |
| Worker `budget_denied`/`emergency_stop` claim **without** a matching `ledger.denials` row (plan 26 step 11) | `BUDGET_CLAIM_UNSUPPORTED` | infrastructure (requeued like other infrastructure failures; repeated claims hit `maxConsecutive`) | FAILED |
| Gateway `SC_BUDGET_CONTENDED` surfaced after SDK retries (plan 33) | `BUDGET_CONTENDED` | **infrastructure**, refunded (contention is not exhaustion; T100 requeue) | FAILED |
| Worker `failure.reason` `budget_denied`, classified from `ledger.denials` (plan 33 §14.5) | `execution:`/`task:` scope, request cap, undefined budget | budget, codes `BUDGET_EXECUTION_EXHAUSTED`, `BUDGET_TASK_EXHAUSTED`, `BUDGET_REQUEST_CAP`, `BUDGET_UNDEFINED` → T106 | FAILED |
| Worker `failure.reason` `budget_denied` on a shared scope (provider, global, orchestration) | — | budget, `BUDGET_SHARED_EXHAUSTED` → T105 (`BLOCKED`) | FAILED |
| Worker `emergency_stop`, Runner e-stop cancellation (level ≥ `workers`; levels `none`/`workers`/`all`, plan 33) | — | budget, `EMERGENCY_STOP` → T105 (`BLOCKED`) | FAILED (running) / CANCELLED (queued) |
| Worker `failure.reason` | `cancelled` (task-level cancel) | none (no event) | CANCELLED |

Timeouts stay infrastructure-class (plans 26, 31, 33: no escalation, no merit count) but are not replayed indefinitely: the first `TIMED_OUT` is requeued once (T100); a second consecutive one → `NEEDS_HUMAN` (`infrastructure_exhausted`, T101). The budget code is taken from the Gateway's `ledger.denials` row for the execution (plan 33), not from worker text.

## 16. Retry behavior

### 16.1 Counters (`attempts` in `task.schema.yaml`)

- `autonomous_used`: implementation attempts — incremented at every implementer/producer dispatch (T030, T031, T124, T125); `tester_used`: incremented at every tester/judge-author dispatch (T010–T013, T110–T113). Both are **refunded** (decremented, `executions.refunded = true`) when that execution ends with class infrastructure or with code `EMERGENCY_STOP`/`BUDGET_SHARED_EXHAUSTED`. Infrastructure noise therefore never burns merit attempts (L-4).
- `autonomous_max`: `min(spec.retry.maxAutonomousAttempts, repository value, task.max_attempts from Hermes)` at creation; `tester_max` likewise from `maxTesterAttempts`; raised only by human grants. Plan 33's `perTask.maxAttempts` (15, counting the task's executions that reached STARTING, excluding refunded ones and emergency-stop ones; COST-8) is an independent cost ceiling; the smallest bound wins and its refusal (`ATTEMPTS_EXHAUSTED` at dispatch) is handled like ours.
- `merit_by_generation["<role>:g<n>"]`: merit failures of a role in a generation — the `M` of plan 31's escalation ladder. Merit events: tester → RED/judge classified invalid (T021, T118); implementer/producer → GREEN/judging failed (T034, T035, T128, T129) and review `changes_requested` (T044, T045); reviewer/researcher/planner → worker failure with a merit reason (T102). The counter of a new generation starts at 0 (generations start only at an approved conflict or respec).
- `consecutive_timeouts`: consecutive `TIMED_OUT` executions; reset by any other terminal outcome.
- **Authority (CD-09):** the merit events listed above are the complete set. Plan 31's escalation ladder consumes `merit_by_generation["<role>:g<n>"]` as `M` and defines no merit events of its own; in particular a review that criticizes tests is not a tester merit event (a test review in `RED_VERIFIED` is auxiliary).
- `by_class`: one increment per failed execution or failed verification judgment by class.
- `consecutive_infrastructure`: reset to 0 by any execution that ends SUCCEEDED or with a non-infrastructure class.
- `reviews_used`, `auxiliary_used`, `blocked_results`, `conflicts_raised`: own caps.

### 16.2 Who retries

- **Infrastructure** failures of executions are requeued automatically by the Runner (T100) after `backoffSeconds[n]` with the same role, harness, model and profile, up to `maxConsecutive`/`maxTotal`; then `NEEDS_HUMAN` (`infrastructure_exhausted`). Verification infrastructure failures re-trigger the pipeline (T024) up to the phase's `maxInfraRetriggers` (plan 22).
- **Merit** failures return the task to a dispatch-ready state; **Hermes** decides the next attempt (SPEC §4.6: Hermes manages retries and escalation) within the guards.
- **Policy** failures: none (`maxViolations: 0`).
- **Timeouts**: infrastructure class, one automatic requeue, then `NEEDS_HUMAN` (`maxConsecutiveTimeouts: 2`).
- **Budget**: zero automatic retries (plan 33). Shared-window stops and the emergency stop → `BLOCKED` (`reason_code` `budget_window_exhausted` / `emergency_stop`) with a stored `resume_state`, no attempt consumed; a human unblocks after the stop is lifted (T105). Task-attributable budget exhaustion → `NEEDS_HUMAN` (`budget_exceeded`, T106).

### 16.3 Retries carry evidence

Every dispatch after a failure of the same role in the same generation receives `prior_attempt_evidence` (plan 25): the failing red-result/green-result reasons and failing test IDs, reviewer findings, the previous handoff, and the failure code. Testers never receive implementation material through it (role projection, plan 25).

### 16.4 Identical replay (forbidden; SPEC §74)

- **Dispatch fingerprint** `F = sha256(canonical JSON{role, harness, resolved model id, profile, start_commit, ac_version, sha256(orchestrator_notes), sorted context_refs, sorted failure_signatures delivered in prior_attempt_evidence})`.
- **Failure signature** `S` of a merit or policy failure: RED/GREEN → `sha256(sorted [(test_id, class, normalized first message line)])` where normalization strips digits, hex runs ≥ 7, absolute paths and durations; review → `sha256(sorted normalized blocker/major finding texts)`; timeout → `"TIMED_OUT:" + profile`; ingestion violation → the violation code set.
- A dispatch is an **identical replay** and is refused (`G_NOT_IDENTICAL_REPLAY` fails) if either:
  1. `F` equals the fingerprint of an earlier execution of the same role in the same generation that ended in a merit or policy failure; or
  2. the last two merit failures of the role in the generation have equal `S`, and the new dispatch has the same (harness, model, profile, `sha256(orchestrator_notes)`) as the last one.
- Hermes resolves a refusal by changing something meaningful: a higher tier or different model (escalation), a different harness, new orchestrator notes, or a human decision. Infrastructure requeues (T100) are exempt: their failure was not deterministic.

### 16.5 Escalation

On merit failure, the next implementer's routing resolution receives `merit_failures_at_resolution` = merit failures of the role in the current generation (plan 31 ladder). Hermes may request a higher tier explicitly; repository policy may cap tiers (plan 31 `RepositoryRoutingOverride`).

### 16.6 Exhaustion

`G_ATTEMPTS_EXHAUSTED` is evaluated after counting the failure: the failing role's counter ≥ its maximum, or the plan 31 ladder is exhausted. With the defaults: implementer attempts at M = 0, 1 (bargain), 2, 3 (standard), 4 (frontier); the fifth GREEN failure → `NEEDS_HUMAN` with `retries_exhausted` and `SCTaskRetriesExhausted` notification [SPEC §4.8, §74, §97]. Testers get 3 attempts. `/sc retry grant=N` raises the failing role's maximum by N and resumes.

## 17. Idempotency behavior

Every event carries an `event_key` stored in `transitions` (unique per task):

| Source | `event_key` |
|---|---|
| MCP | `mcp:<idempotency_key>` (creation and first dispatch share the key; seq differs) |
| Admin API | `api:<Idempotency-Key>` |
| Result ingestion | `result:<execution_id>` |
| Execution failure | `result:<execution_id>:<terminal state>` |
| Evidence | `evidence:<phase>:<pipeline number>`; infra retrigger `verify-infra:<verification id>:<retrigger n>` |
| Forgejo webhook | `forgejo:<X-Forgejo-Delivery>`; resync-detected events `forgejo:resync:<kind>:<sha>` |
| Human comment | `comment:<comment id>` |
| Watchdog | `watchdog:<state>:<state_entered_at unix>` |
| Internal chained events | `internal:<parent event_key>:<event>` |

Replaying an applied event returns the recorded outcome; replaying a rejected event re-evaluates (state may have changed). Effects are outbox rows keyed by `<task>:<seq>:<effect>`; external effects are themselves idempotent (plan 26 §17, plan 55 §17).

## 18. Bootstrap behavior

The table is embedded in the Runner binary; at startup the Runner `Load`s it (failure → unready, no partial mode), records `state_machine_revision` in `sc_runner_state_machine_revision` and refuses to start if any non-terminal task row holds a state absent from the table (§19). No task exists at bootstrap; the first tasks are C10 fake flows on `sc-e2e/fake-target`. Policy sections (§9) are loaded with the rest of `policy/verification.yaml` (plan 26 loader); a missing `spec.retry` section is a validation error (fail closed), not a default.

## 19. Upgrade behavior

- A change to `contracts/state-machine.yaml` is a PR that bumps `metadata.revision`, updates golden trajectories (§22) and passes `make state-machine-check`.
- **Additive** changes (new states, transitions, guards) need no data migration.
- **Removing or renaming** a state requires a forward-only migration in the same release that moves every task in that state to a declared successor and inserts a transition row (`actor = runner`, `event = machine_migration`, `transition_id = M<revision>`, reason naming the PR). `machine_migration` is reserved and not part of the table. Without such a migration the Runner refuses to start (§18).
- In-flight tasks keep their history; each transition records the revision under which it fired.
- `v1alpha1` task schema changes follow expand/contract (plan 26 §19).

## 20. Recovery behavior

- After any `sc-pg` PITR, dispatch stays disabled until plan 26 has advanced `task_seq` beyond every `sc-<n>` seen in Forgejo refs, PRs and evidence prefixes (RA-6, plan 26 §20), so no task id is reused.
- `sc-pg` PITR (plan 12) restores task state to ≤ 15 min RPO. After restore the Runner reconciles (plan 26 §20) and this plan adds: tasks in verification states re-poll their pipeline by number (evidence pulled again and classified; duplicate evidence keys are idempotent); tasks in execution-required states whose execution vanished get `execution_failed` (infrastructure, `JOB_LOST`) and follow T100/T101; PRs merged after the restore point are discovered by the 5-minute resync and replayed as `pr_merged`.
- Evidence objects in S3 are immutable; a restored record re-links them by digest.
- Lost transitions after the restore point that cannot be re-derived from Git/Woodpecker/Forgejo are recorded as a `machine_migration`-style note `recovery_gap` on affected tasks and those tasks are moved to `NEEDS_HUMAN` (`stalled`) for human review (procedure `stuck-task-recovery.md` §recovery).

## 21. Observability

**Metrics** (names per plan 38 §21.2; emitted by the Runner; no task IDs as labels):
`sc_tasks{state}`, `sc_tasks_stuck{state}` (tasks beyond their state's `stall.alertAfter`; for execution-required states, time without an active execution), `sc_task_transitions_total{from,to}`, `sc_task_duration_seconds{verification_mode,final_state}`, `sc_retries_total{role,failure_class}`, `sc_needs_human`, `sc_needs_human_entries_total{reason}`, `sc_tdd_compliance_ratio`. New: `sc_task_events_rejected_total{event,code}`, `sc_human_commands_total{command,result}`, `sc_task_invariant_violations`, `sc_runner_state_machine_revision` (gauge, value = revision).

**Alerts** (plan 42 catalogue): existing `SCTaskNeedsHuman`, `SCTaskRetriesExhausted`, `SCNeedsHumanBacklog`, `SCTddVerificationStalled` (extend its matcher to `RED_VERIFYING|GREEN_VERIFYING|JUDGE_VALIDATING|JUDGING`), `SCTddProvenanceBypass`. **New** (registered by plan 42): `SCTaskStuck` (warning; `sum(sc_tasks_stuck{state!~"RED_VERIFYING|GREEN_VERIFYING|JUDGE_VALIDATING|JUDGING|NEEDS_HUMAN"}) > 0` for 10m; runbook `stuck-task-recovery.md`), `SCTaskEventRejected` (warning; `increase(sc_task_events_rejected_total{event=~".*_classified|.*_result_.*"}[15m]) > 0`), `SCTaskInvariantViolation` (critical; `sc_task_invariant_violations > 0`), `SCHumanTaskDecision` (info, runner-post, audit), `SCTaskCommandRejected` (info, runner-post).

**Traces:** each transition is a span event `sc.task.transition` on the task span with attributes `from`, `to`, `event`, `transition_id`, `actor_class` (plan 40).

**Logs:** one JSON line per transition and per rejection (`task_id`, `event`, `transition_id`, `from`, `to`, `actor`, `event_key`).

## 22. Testing strategy

All deterministic, no model calls [SPEC §104].

- **Table checks (F02/F05):** schema validation; every §8.2 invariant has a negative fixture under `src/runner/statemachine/testdata/invalid/` (one per invariant) that `Load` rejects with a named error.
- **Unit (table-driven):** for every transition id, a fixture `(state, mode, event, actor, facts) → expected transition/effects`; for every guard, a fixture where only that guard fails → `Rejected` with that guard named. Coverage gate: 100% of transition ids and guard ids exercised (`make state-machine-coverage`).
- **Property tests:** 10 000 seeded random walks per mode with random events/actors/facts: no walk leaves the declared state set, no non-human actor ever leaves `BLOCKED`/`NEEDS_HUMAN`, no `IMPLEMENTING` entry without `G_RED_EVIDENCE_VALID` true, `autonomous_used ≤ autonomous_max` at every dispatch, history replay reproduces the final state.
- **Golden trajectories:** the plan 21 §22 acceptance-suite trajectories (`tdd-happy-path`, `red-broken-test`, `already-satisfied`, `test-spec-conflict`, `green-after-retry`, `test-tamper`, `exemption-*`, …) as event sequences with expected state sequences; run against the library (F05) and later end-to-end (C10).
- **DB tests (C01):** concurrent `Fire` on one task from 20 goroutines → exactly one transition per distinct event key, contiguous `seq`; two dispatches racing → one `ALREADY_RUNNING`; dispatcher `SKIP LOCKED` with two Runner instances in test → no execution claimed twice.
- **Human channel (C03/C10):** Forgejo test instance: authorized member command accepted; non-member, machine account, edited comment and wrong-issue comment rejected.

## 23. Acceptance criteria

### GATE-F05

- [ ] **29-AC-11** Bypass trajectories (TDD-5/6/9/13/20) on the library: `tdd-strengthen-after-blocked-refused` (implementer dispatched then `blocked` → `dispatch_tester` from RED_VERIFIED rejected by `G_NO_IMPLEMENTER_DISPATCHED_IN_GENERATION`); `dispatch_tester` from ALREADY_SATISFIED rejected until `/sc strengthen-tests`; `review_accepted` with a truncated reviewer diff and no `inspected_paths` → T043; a second adjudication `test_invalid` after a first `test_valid` does not set `test_change_approved`; `accept_external_commit` by `hermes` rejected.

- [ ] **29-AC-1** `make state-machine-check` exits 0 on `contracts/state-machine.yaml`: schema-valid, every §8.2 invariant holds, reachability report shows 0 unreachable states and 0 non-terminating states for both modes.
- [ ] **29-AC-2** Each fixture in `testdata/invalid/` (≥ 1 per invariant in `state-machine.schema.yaml` and §8.2, including "runner actor leaves NEEDS_HUMAN", "human transition targets IMPLEMENTING", "unknown guard id", "missing outcome coverage for red_classified") makes `Load` fail with the fixture's expected error code.
- [ ] **29-AC-3** `go test ./src/runner/statemachine/... -run TestTransitionTable` passes and `make state-machine-coverage` reports 94/94 transition ids and 100% guard ids exercised.
- [ ] **29-AC-4** Property test (`-run TestRandomWalks -seed 1..10000`) passes with zero violations of the five properties in §22.
- [ ] **29-AC-5** `Fire(dispatch_implementer)` from every state other than `RED_VERIFIED`, `CHANGES_REQUESTED` and `TEST_SPEC_CONFLICT` (with rejected conflict) returns `Rejected{GATE_NOT_SATISFIED}`; from `RED_VERIFIED` with `G_RED_EVIDENCE_VALID=false` also rejected [SPEC §52, §106].
- [ ] **29-AC-6** For every transition whose `from` contains `BLOCKED` or `NEEDS_HUMAN` and whose `to` is not `$same`, firing it with actor classes `runner`, `ci` and `hermes` returns `Rejected` (L-4).
- [ ] **29-AC-7** Retry accounting: the golden sequence "tester ok, GREEN fail ×5" ends in `NEEDS_HUMAN` with `retries_exhausted` after the 5th implementer dispatch, with `merit_by_generation["implementer:g1"] = 5`; the same sequence with two (non-consecutive) infrastructure failures interleaved ends identically and shows `autonomous_used = 5`, `by_class.infrastructure = 2` (refunds applied); two consecutive `TIMED_OUT` implementer executions end in `NEEDS_HUMAN` (`infrastructure_exhausted`); an `EMERGENCY_STOP` failure ends in `BLOCKED` with `blocked.reason_code = emergency_stop` and no attempt consumed.
- [ ] **29-AC-8** Identical replay: a dispatch with fingerprint equal to a merit-failed execution is rejected with `G_NOT_IDENTICAL_REPLAY`; the same dispatch with a different tier is accepted; two consecutive equal failure signatures followed by an unchanged (harness, model, profile, notes) dispatch is rejected.
- [ ] **29-AC-9** `Project()` returns the human state listed in §14.13 for all 24 states (table test) and never `Backlog`.
- [ ] **29-AC-10** `task.schema.yaml` validates the golden task records produced by the trajectory tests (records serialized after each step), and every `x-invalid-examples` entry fails.

### GATE-C01 (with plan 26)

- [ ] **29-AC-20** Migrations create the §6.1 tables and constraints; `psql -c "\d runner.executions"` shows index `executions_one_active` with predicate `state IN ('QUEUED','STARTING','RUNNING')`; an `UPDATE runner.transitions …` as `runner_app` fails with permission denied.
- [ ] **29-AC-21** Concurrency test against `sc-pg` (testcontainers at the CNPG Postgres major): 20 concurrent deliveries of the same `red_classified` event produce exactly 1 transition row; 2 concurrent `agent_start implementer` calls on a `RED_VERIFIED` task produce 1 execution and 1 `ALREADY_RUNNING`.
- [ ] **29-AC-22** `GET /v1/tasks/{id}?view=record` for a task driven through the happy path with the fake Facts provider validates against `task.schema.yaml`, and its `history` lists T001, T010, T014, T020, T030, T032, T033, T040, T041, T050 in order with `policy_revision` and `state_machine_revision` set on every row.
- [ ] **29-AC-23** Watchdog: with `spec.watchdog.groups.hermesWait = {alertAfter: 1m, escalateAfter: 2m}` in a test policy, a task left in `RED_VERIFIED` shows `sc_tasks_stuck{state="RED_VERIFIED"} = 1` after 1 min and reaches `NEEDS_HUMAN` with `needs_human.reason = stalled` within 2.5 min.
- [ ] **29-AC-24** Runner start with a task row in state `OBSOLETE_STATE` (seeded) fails readiness with log `unknown task state OBSOLETE_STATE`; after the corresponding migration fixture runs, readiness is 200.
- [ ] **29-AC-25** `agent_cancel` with `disposition: needs_human` on a `NEEDS_HUMAN` task returns `INVALID_STATE` (library + DB test with a fake execution row).
- [ ] **29-AC-26** RA-3: `agent_start` with an embedded task and a disabled harness returns `HARNESS_NOT_ALLOWED` and `SELECT count(*) FROM runner.tasks` is unchanged; with a fault injected between validation and commit, a retry with the same idempotency key creates exactly one task and one execution.
- [ ] **29-AC-27** Set-once provenance: `UPDATE runner.task_generations SET test_commit = <other sha> WHERE test_commit IS NOT NULL` fails in the trigger; a strengthening after RED_VERIFIED creates generation 2 with `superseded_reason = strengthened` on generation 1.

### GATE-C02 (contribution; needs real executions)

- [ ] **29-AC-28** On an `IMPLEMENTING` task with a running fake execution, `agent_cancel` `needs_human` cancels the execution (gateway token revoked) and records `escalate` with reason code `manual_escalation`.
- [ ] **29-AC-29** RA-7: with one worker slot held by a 15-minute fake CI pod and `queueTimeout: 30m`, a queued execution starts afterwards without `START_TIMEOUT`; with `queueTimeout: 1m` it ends `FAILED` `QUEUE_TIMEOUT` (infrastructure, refunded).

### GATE-C03 (human channel; with plans 26, 55)

- [ ] **29-AC-30** On the Forgejo test instance: `/sc block test reason here` by a member of `engineers` moves the task to `BLOCKED` and the Runner reply comment contains `T063`; the same comment by `sc-renovate` and by a user in `viewers` only are rejected with `NOT_AUTHORIZED` and the state is unchanged; editing an accepted comment has no effect.
- [ ] **29-AC-31** Re-verification of third-party behavior: the `issue_comment` webhook payload contains `sender.login`, `comment.id`, `issue.number` and `action: created` at the pinned Forgejo version, and `GET /api/v1/teams/{id}/members/{user}` returns 200/404 for member/non-member (test fails loudly if a field or path differs).

## 24. Explicit non-goals

- Task DAG scheduling, automatic child dispatch, cross-repository dependencies [SPEC §128].
- Autonomous merge (ADR-0019) and automatic resolution of `NEEDS_HUMAN`.
- A UI for transitions: humans use Forgejo comments, the Hermes dashboard (read via Hermes) and the admin API.
- Editing history: transitions are append-only; mistakes are corrected by new transitions.
- Prometheus as a task ledger: per-task data lives in `sc-pg` [SPEC §90].

## 25. Open questions

1. **Human channel location** (blocking: no). ADR-0350 puts human commands in Forgejo comments. An alternative is a Hermes dashboard action, but Hermes is untrusted for enforcement, so it cannot carry human authority. Resolution: Forgejo comments plus the admin API. Owner: 29/55.
2. **Timeout classification** (blocking: no). Deadline timeouts are infrastructure-class (plans 26/31/33) with a consecutive cap of 2 (§15.2). A stricter merit classification was considered and rejected to keep escalation semantics consistent. Owner: 29.
3. **Notification reason enum** (blocking: no). Five reasons are added (§7.5); plan 42 must extend `notification.schema.yaml`. Owner: 42.
4. **`worker:<execution-id>` actor pattern** in `mcp-surface.schema.yaml` contradicts 29-R-1 (blocking: no). Resolution: plan 26 removes it. Owner: 26.
5. **Plan 26's `acceptance_criteria` table** is superseded by `task_ac_versions` (versioned criteria; SPEC §45 revisions). Blocking: no. Owner: 26/29.
6. **Hermes without Forgejo credentials** — resolved by CD-15: Hermes holds no Forgejo account; `/sc` commands are accepted only from human accounts (§7.3 rule 2).
7. **Break-glass repair and re-ingest APIs** (blocking: no). The repair function may only target `NEEDS_HUMAN`, `BLOCKED`, `CANCELLED` or the state the last valid transition left (TDD-25); every other change goes through `/sc` commands. `procedures/stuck-task-recovery.md` relies on a migration-provided SQL function `runner.repair_task_state(task, state, actor, reason)` (inserts a `machine_migration`-style transition and sets the state atomically; the only sanctioned state-changing SQL) and an admin endpoint `POST /v1/executions/{id}/reingest` (re-ingests a stored result object after a PITR). Owner: 26.
