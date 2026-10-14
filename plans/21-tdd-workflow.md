# 21 — TDD Workflow

**Status:** Stage-0 plan (draft)
**DAG nodes:** C04 woodpecker-adapter-red-green-evidence (co-owner with 22 and 20: the RED/GREEN workflow end to end), C10 fake-end-to-end-tdd-flows (co-owner with 46: the SPEC §106 acceptance suite). Contributes golden trajectories to F05 (plan 29) and fixtures to F06 (plan 22).
**Related plans:** [22 verification policy](22-verification-policy.md), [29 state machine](29-task-state-machine.md), [30 handoffs](30-handoffs.md), [55 forge workflow](55-forge-workflow.md), [20 Woodpecker](20-woodpecker.md), [25 context](25-context.md), [26 Runner](26-agent-runner.md), [27 worker contract](27-worker-contract.md), [28 harnesses](28-harnesses.md), [31 routing](31-model-routing.md), [32 cost accounting](32-cost-accounting.md), [46 self-hosting threshold](46-self-hosting-threshold.md), [52 CI strategy](52-ci-strategy.md)
**Contracts:** owns `acceptance-criteria.schema.yaml`; consumes `task`, `state-machine` (29), `red-result`, `green-result`, `test-manifest`, `verification` (22), `worker-input`, `worker-result` (27), `handoff` (30), `context` (25), `cost-record`/`gateway-token` phase enum (32/53)
**ADRs:** relies on 0008, 0019, 0302, 0350, 0351, 0352, 0353; introduces [ADR-0355](../adr/0355-test-spec-conflict-human-approval.md) (corrected acceptance tests need human approval by default)

> Every section below is mandatory (SPEC §121).

## 1. Purpose

This plan describes the **end-to-end test-first flow** [SPEC §47–§56, §129] as one coherent procedure that the state machine (plan 29), verification policy (plan 22), Runner (plan 26), CI (plan 20) and forge workflow (plan 55) implement together: how acceptance criteria are written and frozen, what the tester receives and produces, how RED is proven, how the implementation gate opens, how GREEN and review work, how a disputed test is corrected, how provenance survives retries and squash merges, how TDD costs are tagged, and the deterministic **SPEC §106 acceptance suite** that proves the organization enforces all of it.

## 2. Requirements derived from SPEC.md

1. Acceptance criteria exist before test authoring, with stable IDs; tests map back to them [SPEC §44, §45].
2. A separate tester execution precedes implementation; it receives specification, criteria, repository context and current behavior, never a proposed implementation; tester and implementer are independent execution contexts [SPEC §48, §66].
3. The red commit contains only tests, fixtures, minimal test infrastructure and verification artifacts, with a subject like `test(sc-184): …` [SPEC §49].
4. Trusted CI proves RED against pre-implementation code; implementation is blocked until RED_VERIFIED [SPEC §50].
5. Already-passing tests lead to investigation, never an artificial failure [SPEC §51].
6. Implementation starts from the red commit or a descendant; Git topology proves ordering [SPEC §52].
7. Acceptance tests are protected; an invalid test leads to TEST_SPEC_CONFLICT and a distinct reviewable action [SPEC §53, §70].
8. GREEN reruns task tests, relevant existing tests and required validation [SPEC §54].
9. The reviewer receives task, criteria, red commit and evidence, implementation diff, green evidence and handoff, and judges legitimacy [SPEC §55].
10. TDD provenance is durable through squash merges [SPEC §56].
11. Cost accounting distinguishes specification, test-authoring, red-verification, implementation, green-retry and review cost [SPEC §82].
12. CI tests the organization itself (SPEC §106 list) with deterministic fake workers [SPEC §103, §104, §106].
13. The self-hosted task follows human requirement → criteria → tester → RED → Woodpecker → RED_VERIFIED → implementer → GREEN → reviewer → accepted PR [SPEC §118].

Derived requirements:

- **21-R-1** Acceptance criteria are immutable once frozen; any change is a new version through a human `respec` (plan 29 T070).
- **21-R-2** Every criterion of the frozen version is covered by ≥ 1 manifest test (plan 22 R6).
- **21-R-3** A test or judge correction always creates a new generation with a new red commit and a new RED verification; the previous generation's provenance is retained unchanged.
- **21-R-4** Every SPEC §106 item has a named deterministic test at library level (F05/F06) and live (C10).

## 3. Inputs

| Input | Source |
|---|---|
| Human intent | Hermes dashboard conversation or a Forgejo issue (plan 55 §14.1) |
| Task definition with criteria | Hermes `agent_start` with `task` (`mcp-surface.schema.yaml` `TaskDefinition`) |
| Repository policy and validation at base | `.strange-company/policy.yaml`, `validation.yaml` (plan 22) |
| Worker results and handoffs | plan 27 / plan 30 |
| Evidence | plan 22 classified documents |
| Human decisions | `/sc` commands (plan 29 §7.3) |

## 4. Outputs

- Red commits, implementation commits and PRs on `agent/<task-id>` (plan 55).
- Task records with complete TDD provenance (plan 29 `task.schema.yaml` `tdd`).
- Cost records tagged with TDD phases (plan 32).
- The SPEC §106 suite (`make test-tdd-library`, `sc-e2e run --suite c10`) and its reports.

## 5. Dependencies

**Hard (DAG):** C04 ← C01, P08, F06. C10 ← C02–C09.

**Interfaces assumed:**
- **Plan 25:** tester projection forbids `implementation_diff`, `implementer_handoff`, `green_evidence`, `conflict_report`, `reviewer_findings`; the tester receives `conflict_decision` (shape §14.6) and `prior_attempt_evidence` (same role, same generation).
- **Plan 27:** `role_output.ac_mapping` for testers; `conflict` for `test_spec_conflict`; **request**: `ReviewerOutput` gains optional `purpose` (`final_review`, `test_review`, `investigation`, `adjudication`) and `conflict_decision {verdict: test_valid|test_invalid|spec_ambiguous, ac_ids, rationale ≤ 4000, proposed_test_change ≤ 4000}`, required when the Runner dispatched the reviewer in `TEST_SPEC_CONFLICT` (the packet carries the purpose). Branch topology rows "tester re-authoring" use `continue` from the previous red commit after the Runner reset (plan 29 §14.6).
- **Plan 28:** fake scenarios listed in §22.3, including new ones requested there.
- **Plan 32:** phase enum `specification, test_authoring, red_verification, implementation, green_retry, review, orchestration` (`cost-record.schema.yaml`).
- **Plan 55:** PR lifecycle, statuses, archive tags.

## 6. Owned state

N/A as a separate store — this plan owns no table. Acceptance-criteria versions (`runner.task_ac_versions`), generations, conflicts and reviews are owned by plan 29's schema; evidence documents by plan 22. This plan owns the **semantics** of criteria (§9.3) and the workflow rules that tie them together.

## 7. External interfaces

- `acceptance-criteria.schema.yaml` (owned): validation of `TaskDefinition.acceptance_criteria` and of `/sc respec` payloads.
- Test entry points: `make test-tdd-library` (F05/F06 trajectories, no cluster) and the single live C10 entry point owned by plan 46, `sc-e2e run --suite c10`, which executes every trajectory file `test/e2e/tdd/*.yaml` of §22 plus plan 46's E2E rows, with one time budget (≤ 150 min, plan 46) (TDD-19).

## 8. Internal interfaces

| From → to | Interface |
|---|---|
| Hermes → Runner | `agent_start` (tester/implementer/reviewer/auxiliary), `agent_cancel` |
| Runner → worker | packet with role projection (plan 25), branch topology (plan 27) |
| Worker → Runner | result + handoff (plans 27, 30) |
| Runner → Woodpecker | intent + trigger (plan 20), evidence pull |
| Runner → F06 | classification (plan 22) |
| Runner → F05 | events (plan 29) |
| Runner → Forgejo | PR, statuses, labels, archive tags (plan 55) |
| Human → Runner | `/sc` commands (plan 29 §7.3) |

## 9. Configuration

### 9.1 Knobs used by this flow

All in `policy/verification.yaml` (plans 22/29) and per-repository `.strange-company/policy.yaml`: `retry.*`, `testSpecConflict.*`, `red.*`, `green.*`, `stubPolicy.*`, `testProtection.*`. Role behavior (instructions, projections, `accounting` phases) in `policy/roles/*.yaml` (plan 25).

### 9.2 Phase tags (SPEC §82; aligned with plan 32 and `cost-record.schema.yaml`)

| Phase | Assigned to (gateway token claim `phase`, set at dispatch) |
|---|---|
| `specification` | planner and researcher executions (auxiliary in any state, and research producers) |
| `test_authoring` | every tester execution: first RED attempt, retries after `TEST_NEEDS_WORK`, test strengthening, corrected tests after a conflict, verification-first judge authoring |
| `red_verification` | reserved for model spend during RED classification; **none in MVP** (classification is deterministic; CI compute is not a ledger item) |
| `implementation` | implementer/producer executions of a task with no merit failure of the implementer role yet |
| `green_retry` | implementer/producer executions after ≥ 1 implementer merit failure on the task (GREEN/judging failed, review changes requested, merit timeout-free failures) — including the retry after a rejected conflict |
| `review` | reviewer executions: final review, test review, investigation, conflict adjudication |
| `orchestration` | Hermes (not task-attributed; plan 32) |

The Runner computes the phase in the dispatch transaction from the task record (`attempts.merit_by_generation` summed over generations for the implementer role). Role files' `accounting.firstAttemptPhase/retryPhase` must agree with this table (F04 loader invariant shared with plan 25).

### 9.3 Acceptance-criteria semantics (SPEC §45)

- **IDs:** `AC-<n>` (`^AC-[1-9][0-9]{0,2}$`), unique within the task across versions; never reused for a different meaning; removed IDs are retired; a criterion whose text changes keeps its ID with `revision + 1` and its tests are re-mapped by the next tester.
- **Kinds:** `change` (behavior absent at base; must fail at RED) and `preservation` (existing behavior that must not regress; must pass at RED). A test-first version needs ≥ 1 `change`.
- **Immutability:** a version is `draft` until the first tester/judge-author dispatch, then `frozen` forever. Hermes cannot revise criteria (the MCP surface has no operation for it); a human revises with `/sc respec` (new version, `change_reason` ≥ 20 chars, Runner-computed `changes`). Hermes's alternative is to cancel and create a new task.
- **Testable-text rules (`ac-lint`, deterministic, run at creation and at respec):**

| Rule | Check | Severity |
|---|---|---|
| AC-L1 | text 10–2000 chars, ≥ 4 words | error |
| AC-L2 | none of the vague phrases `TBD`, `TODO`, `works well`, `as expected`, `user-friendly`, `is intuitive`, `and so on` (schema) | error |
| AC-L3 | ≤ 1 sentence-final period followed by another sentence (one behavior per criterion) | warning |
| AC-L4 | no reference to implementation identifiers inside backticks unless they already exist at base (checked against the repository tree) | warning |
| AC-L5 | ids contiguous from AC-1 in version 1; unique | error |
| AC-L6 | ≥ 1 `change` criterion for test-first | error |

Errors reject task creation (`VALIDATION_FAILED`, `field` = JSON pointer); warnings are stored on the version and shown in the PR body.
- **Mapping:** the tester declares `ac_mapping` (test names per AC) in its result; the Runner normalizes names to test IDs (exact or `::`-suffix match against the RED report) and records the mapping in the manifest. Test files should carry the token `AC-n` (role instructions); its absence is a warning (`ac_tag_missing`).
- **Coverage check:** at RED, every criterion of the frozen version has ≥ 1 mapped manifest test (plan 22 R6); change criteria fail, preservation criteria pass (R9/R10). At review, the reviewer's check `all_ac_covered` confirms semantic coverage.

## 10. Secrets

N/A — the workflow introduces no secret. Workers use the per-execution credentials of plan 26 (`SC-WORKER-DEPLOY-KEY`, `SC-WORKER-TASK-TOKEN`, `SC-GATEWAY-TOKEN`); verification pipelines get none (ADR-0008).

## 11. Storage

N/A beyond plans 22 and 29: criteria versions and provenance in `sc-pg`; evidence and manifests in S3 `evidence`; commits in Forgejo (archive tags keep red commits reachable after squash, plan 55).

## 12. Resource requirements

No component of its own. Typical per-task load: 1 tester, 1–5 implementer and 1 reviewer execution (profiles `tests`, `implementation`, `reasoning`; plan 26), 1 RED pipeline (2 suite runs), 1–5 GREEN pipelines. C10 suite: ≈ 30 fake tasks, sequential at `maxWorkers: 1`, within plan 46's ≤ 150 min budget on the self-hosting-minimum node.

## 13. Security boundary

- The tester and implementer are separate executions with separate credentials and context projections; the tester never sees implementation material (plan 25 forbid list, including research or task outputs produced after the red commit; plan 27 fetches no archive tags). Because a harness sharing the deploy key could still fetch `sc-archive/*`, plans 26/27 are asked to move the deploy key into a `git` sidecar the harness cannot read (TDD-14; SI proposed) [SPEC §48].
- No worker can change the judge of its own work: protected paths (policy, CI config, validation), manifest blobs, discovery configuration, base-revision policy (ADR-0008, ADR-0017).
- Hermes cannot approve corrected tests, exemptions or criteria revisions (plan 29 29-R-2).
- The reviewer is read-only (read deploy key) and its verdict alone never merges: a human merges (ADR-0019).

## 14. Happy path

### 14.1 Specification (SPECIFIED)

1. A human states intent to Hermes (or files a Forgejo issue and points Hermes at it; plan 55 §14.1).
2. Hermes writes the objective, constraints and acceptance criteria (version 1, `kind` per criterion), chooses `verification.mode` (`test-first` unless an exemption class applies) and calls `agent_start` with the embedded `task` and role `tester` (or first a `planner`/`researcher` as auxiliary execution when ambiguity is high, SPEC §76).
3. The Runner validates criteria (`acceptance-criteria.schema.yaml` + `ac-lint`), repository policy and validation (plan 22), freezes the base commit (T001) and, in the same transaction, dispatches the tester (T010), freezing the criteria.

### 14.2 Test authoring (TEST_AUTHORING)

- **Tester inputs** (context projection, plan 25): company rules, repository context, `AGENTS.md`, architecture, decisions, role instructions, task spec, acceptance criteria (scoped), validation rules (test paths, suites, stub policy), context files named by Hermes at the base commit (current behavior), research reports, prior attempt evidence (same generation, tester role), conflict decision (corrected-test generations), orchestrator notes. **Never**: implementation diffs, implementer handoffs, green evidence, the implementer's conflict report, reviewer findings on implementations.
- **Tester output:** exactly one commit on `agent/<task-id>` with subject `test(<task-id>): <summary>` and trailers `SC-Task`, `SC-Execution`, `SC-Role: tester`, `SC-Base` (plan 27), touching only testPaths, fixturePaths, testInfraPaths and declared stubs in stubPaths (ADR-0352), plus `role_output {kind: tester, test_files, ac_mapping}` and a handoff (plan 30). If the tester believes the behavior already exists, it still commits the tests and reports `already_satisfied`; CI decides [SPEC §51].
- **Ingestion** (plan 26 step 6): `ROLE_DIFF_VIOLATION` for any other path; protected paths → `PROTECTED_PATH_VIOLATION`.

### 14.3 RED (RED_VERIFYING → RED_VERIFIED)

The Runner registers an intent and triggers the `verify` workflow at the candidate red commit (plan 20, ADR-0302); `sc-verify` runs the suites at base and head and emits observations; the Runner classifies with rules R0–R11 (plan 22 §14.3), stores evidence, builds the manifest and fires T020–T023. On `red_valid` the Runner opens the **draft PR** (plan 55) and posts `strange-company/tdd-provenance = pending` ("RED verified; implementation pending").

### 14.4 Implementation gate and implementation (IMPLEMENTING)

- **Gate** (SPEC §52; plan 29 T030 guards): the current generation has a red commit with `red_valid` evidence and a manifest; the dispatch's `start_commit` is the red commit (first attempt) or the last accepted implementer commit, which descends from it. The Runner enforces this before dispatch; ingestion re-checks `NOT_DESCENDANT_OF_START`; GREEN re-checks G1; merge re-checks F6 — **four independent checks** of ordering through Git ancestry.
- **Implementer inputs:** requirement, criteria, red test diff, red evidence (failing tests and messages), tester handoff, architecture, constraints, prior attempt evidence and reviewer findings (retries), conflict decision (after a rejected conflict).
- **Implementer output:** one commit (`feat(<task-id>): …` or `fix(…)`), `role_output {kind: implementer, changed_files, ac_addressed}`, handoff. Ingestion runs plan 22 §14.6 tamper checks.

### 14.5 GREEN (GREEN_VERIFYING → GREEN_VERIFIED)

The Runner triggers the `verify` workflow at the implementation head with `SC_PROTECTED_TESTS` = manifest test IDs; `sc-verify` runs the full suites and required checks; the Runner classifies with G0–G8 (plan 22 §14.7). `green_pass` → `GREEN_VERIFIED`, PR marked ready; `green_fail` → back to `RED_VERIFIED` with the evidence for the next attempt (phase `green_retry`), or `NEEDS_HUMAN` when attempts are exhausted; `tamper_detected` → `NEEDS_HUMAN`.

### 14.6 TEST_SPEC_CONFLICT (SPEC §53, §70; ADR-0355)

1. The implementer returns `state: test_spec_conflict` with `conflict {ac_ids, tests, explanation}` and pushes nothing (a pushed commit is archived and dropped). Task → `TEST_SPEC_CONFLICT` (T080).
2. Hermes dispatches a **read-only reviewer** with purpose `adjudication` (the first adjudication is decisive; a later one can only tighten; disagreement forces human approval — plan 29 §14.6, TDD-20). Its packet holds the conflict report, the criteria, the red diff and evidence, and the implementer's diff so far. It returns `conflict_decision.verdict`:
   - `test_valid` — the test correctly encodes the criterion; implementation must satisfy it;
   - `test_invalid` — the test contradicts or over-specifies the criterion; it gives `proposed_test_change`;
   - `spec_ambiguous` — the criterion itself is ambiguous.
3. `test_valid` → Hermes re-dispatches an implementer (T031) with the decision (`conflict_decision` section). The conflict closes as rejected.
4. `test_invalid` → by default (`resolutionApproval: human`) the Runner notifies a human (`SCTaskNeedsHuman`, reason `test_spec_conflict`; projection *Needs Human*). The human reads the conflict, decision and diff in the PR/issue and comments `/sc accept-test-change` (optional `guidance:`) or `/sc reject-conflict`. This comment is the **distinct reviewable action** of SPEC §53, recorded with the human's identity.
5. After approval Hermes dispatches a tester (T013): new generation; the Runner tags the old head `sc-archive/<task>/g<n>`, recreates `agent/<task>` at the previous red commit; the tester's corrected test is a new commit on top of it (the correction is visible as a `red(g)..red(g+1)` diff); a new RED verification and a new manifest (with `supersedes`) follow; implementation restarts from the new red commit. Old generation rows, evidence and manifest remain.
6. `spec_ambiguous` → a human revises the criteria (`/sc respec`, new version, new generation from base) or cancels.

`conflict_decision` context section shape (plan 25 interface): `{conflict_id, verdict, ac_ids, rationale, proposed_test_change, test_change_approved, decided_by, decided_at, guidance}`; authoritative when `decided_by` is `human:*`.

### 14.7 Review (REVIEW → AWAITING_MERGE)

- **Reviewer inputs** [SPEC §55]: original task and criteria (with versions), red commit and red test diff, classified red evidence, implementation diff (`red..head`), classified green evidence including integrity warnings, tester and implementer handoffs, prior conflict decisions, the manifest.
- **Checks** (all five required; `worker-result` `ReviewerOutput.checks`): `red_failed_for_right_reason`, `acceptance_tests_intact`, `green_not_by_bypass`, `all_ac_covered`, `no_out_of_scope_changes`.
- **Verdict mapping** (worker-result verdict → plan 29 event):

| `verdict` | checks | Mapped verdict | Event |
|---|---|---|---|
| `approve` | all five `pass`, `reviewed_commit == head` | `accept` | `review_accepted` |
| `approve` | any not `pass` | `changes_requested` (inconsistent approvals fail closed) | `review_changes_requested` |
| `request_changes` | any | `changes_requested` | `review_changes_requested` |
| `reject` | any | `reject_bypass` | `review_rejected` |

- Accept → `strange-company/review = success` and (if every merge fact holds) `strange-company/tdd-provenance = success` → `AWAITING_MERGE`; a human merges (ADR-0019) → DONE (plan 55 §14.7).

### 14.8 Provenance retention (SPEC §56)

- **Across retries:** every attempt is a new execution row; the generation keeps `test_executions`, `implementation_executions` and every `verifications` row (RED and each GREEN), append-only. The `tdd` top-level fields name the current/final ones.
- **Across conflicts:** a corrected test opens a new generation; the old generation becomes `superseded` with its red commit, evidence digests, manifest digest and archive tag intact.
- **Across squash merges:** the record keeps commit SHAs; `sc-archive/<task>/merged` (and `g<n>`, `cancelled`) tags keep the red and implementation commits reachable in Forgejo after the agent branch is deleted; tag protection allows only `sc-runner` (plan 55). The PR body's provenance section (plan 55 §14.3) also survives as Forgejo PR history.

### 14.9 Generations and branch topology (authoritative; plan 27 executes it)

A **generation** is one version of the task's tests (or judge). Generation 1 starts at the base commit when the first tester is dispatched. A new generation starts only through an approved conflict (`test_change_approved`), a human `respec`, or an exemption denial. Its number is stored on every execution, verification and manifest.

| Dispatch | Runner preparation | Branch mode / start commit |
|---|---|---|
| first tester of generation 1 | none (branch absent) | `create` at `base_commit` |
| tester retry after `TEST_NEEDS_WORK` (no valid RED yet in the generation) | none | `continue` at current branch head (the previous tester commit) |
| tester after `ALREADY_SATISFIED`/`NEEDS_INVESTIGATION` (requires `/sc strengthen-tests`, plan 29 T016) | none (same generation; plan 22 R12 keeps every previously declared test) | `continue` at current branch head |
| tester strengthening after valid RED, before any implementer dispatch (T012) | new generation `strengthened`: tag `sc-archive/<task>/g<g>` (head already equals red(g)) | `continue` at red(g); plan 22 R12 superset rule |
| tester of generation g+1 after an approved conflict | tag head `sc-archive/<task>/g<g>`, delete `agent/<task>`, recreate it at generation g's red commit (Forgejo `POST /repos/{o}/{r}/branches` with `old_ref_name` = the SHA; verify at C03) | `continue` at red(g) |
| tester of generation g+1 after `respec` or exemption denial | tag head `sc-archive/<task>/g<g>`, delete `agent/<task>` | `create` at `base_commit` |
| first implementer of a generation | none | `continue` at red(g) |
| implementer retry (green_fail, changes requested, rejected conflict) | none | `continue` at the last **accepted** implementer commit (or red(g)) |
| after a policy violation | head quarantined (`sc-quarantine/<execution-id>`), branch reset to the last accepted commit; for `SECRET_DETECTED` the commit is dropped without a tag | — (NEEDS_HUMAN) |
| after `RESULT_MISSING` with a pushed commit | the unreported commit is archived (`sc-archive/<task>/x<n>`) and the branch reset to the execution's start commit before the requeue | per row above |
| reviewer, adjudicator | none | `readonly` at the current head |

Generation bookkeeping (plan 29 §6.1, TDD-7): the manifest of a generation opened by strengthening records `supersedes.reason: strengthened` (approved conflicts: `test_change_approved`; respecification: `respec`); every tester candidate is an append-only `generation_candidates` row (`candidate_test_commit` is only a pointer to the latest); every accepted implementer retry appends to `implementation_commits`, and the set-once `implementation_commit` is written when the final review accepts the head.

### 14.10 Verification-first variant

As plan 29 §14.8 / ADR-0353: criteria → (judge authoring by a tester, or criteria as judge) → judge validation (CI proves it fails before production) → approval (auto or one human) → production → judging → review → merge or DONE. Phase tags as §9.2.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Criteria fail `ac-lint` | creation | task refused | `VALIDATION_FAILED` to Hermes | — |
| Tester touches non-test code | ingestion `ROLE_DIFF_VIOLATION` | policy failure | NEEDS_HUMAN, quarantine | `tdd-task-recovery.md` |
| Tests only fail to compile | RED R3/R7 | TEST_NEEDS_WORK | tester retry with evidence (stub guidance) | — |
| Tests already pass | RED R10 | ALREADY_SATISFIED | notification; Hermes investigates | `tdd-task-recovery.md` §already-satisfied |
| Base branch red | RED R2 | NEEDS_INVESTIGATION | notification | `tdd-task-recovery.md` §baseline |
| Implementer tampers with tests | ingestion `TEST_TAMPER` | policy failure | NEEDS_HUMAN, quarantine | `tdd-task-recovery.md` §tamper |
| Implementer cannot satisfy a test | `test_spec_conflict` result | TEST_SPEC_CONFLICT | adjudication | `tdd-task-recovery.md` §conflict |
| GREEN keeps failing | merit counters | NEEDS_HUMAN after 5 | escalation ladder (plan 31) on the way | `tdd-task-recovery.md` §exhausted |
| Reviewer suspects bypass | `reject` | NEEDS_HUMAN | statuses failure | `tdd-task-recovery.md` §review-reject |
| Verification infrastructure fails | plan 22 §14.10 | stays verifying, retrigger ×2 | then NEEDS_HUMAN | `tdd-task-recovery.md` §pipeline |
| Evidence forgery attempt | `EVIDENCE_AMBIGUOUS` | NEEDS_HUMAN | `SCTddProvenanceBypass` | `tdd-task-recovery.md` §forgery |
| Merge without provenance | plan 55 | NEEDS_HUMAN | critical alert | `merge-without-provenance.md` |

## 16. Retry behavior

Per plan 29 §16: tester attempts ≤ 3 (`maxTesterAttempts`), implementer attempts ≤ 5 matching the plan 31 ladder (bargain ×2, standard ×2, frontier ×1), infrastructure failures refunded and requeued (≤ 3 consecutive, ≤ 2 consecutive timeouts), policy violations never retried, identical replays refused. Every retry carries the previous evidence (red/green reasons with failing test IDs and messages, reviewer findings, handoffs) in `prior_attempt_evidence`.

## 17. Idempotency behavior

Each step is idempotent by construction elsewhere: dispatch (MCP idempotency keys), result ingestion (one result per execution), evidence (pipeline-keyed), manifests (per generation, digest-checked), human commands (comment id), PR and status operations (plan 55). Re-running `sc-e2e run --suite c10` creates new tasks each time (task IDs are never reused) and cleans up only its own branches via cancel.

## 18. Bootstrap behavior

- Before P08 the platform's own work follows the **bootstrap discipline** of plan 52 (`tdd-discipline-check` re-runs the RED commit of human PRs in CI; ADR-0018). That is human-process evidence, not task provenance.
- From C04 the RED/GREEN path is live with fakes on `sc-e2e/fake-target`; from C10 the whole §106 suite passes on the cluster; R02 (plan 46) runs the first real test-first task on `strange-company`.
- `sc-e2e/fake-target` (a small Go module with `validation.yaml`, `policy.yaml` allowing all six exemption classes and `testPaths: ["**/*_test.go"]`) is created by `make e2e-fixtures` through the Forgejo API as `sc-admin` during C03 setup; its fixture commits (per scenario) live in `test/e2e/fake-target/` of `strange-company`.

## 19. Upgrade behavior

Workflow changes arrive as policy (plans 22/29) or code changes; in-flight tasks keep their frozen criteria, generations and evidence; a policy revision change applies to the next classification (evidence records the revision). A change to the phase table requires plan 32's views to accept both old and new tags for 90 days.

## 20. Recovery behavior

Per plans 29 §20 and 22 §20. A task whose provenance cannot be re-established after a restore (missing evidence objects) is re-verified at the same commits (`/sc reverify red|green`) — same commits produce equivalent evidence; the procedure records the gap.

## 21. Observability

Grafana "TDD" dashboard (plan 39) panels sourced from plan 29/22 metrics: tasks by state; red/green transition rates (`sc_task_transitions_total`); RED outcome mix (`sc_verification_runs_total{verification_phase="red"}`); time from SPECIFIED to RED_VERIFIED and RED_VERIFIED to GREEN_VERIFIED (`sc_task_duration_seconds` + sc-pg views); retries by class; Needs Human; TDD compliance ratio (`sc_tdd_compliance_ratio`, SPEC §92); spend by phase (plan 32 `sc_model_spend_micro_usd_total{phase}`). Alerts: plan 29/22 sets.

## 22. Testing strategy

### 22.1 Levels

| Level | Where | What |
|---|---|---|
| Library (F05) | `make test-tdd-library`, no cluster | trajectories below as event sequences with fake facts against `contracts/state-machine.yaml` |
| Library (F06) | same | classification fixtures for each trajectory's evidence |
| Live (C04) | cluster + `sc-e2e/fake-target` | RED and GREEN paths with real Woodpecker pipelines and real `sc-verify` |
| Live (C10) | `sc-e2e run --suite c10` | full suite with fake harnesses; no paid model calls [SPEC §104] |

### 22.2 SPEC §106 acceptance suite (deterministic; each item = named test)

| SPEC §106 item | Test name | Fake scenario(s) (plan 28) | Expected trajectory / assertion |
|---|---|---|---|
| implementer cannot start before RED_VERIFIED | `tdd-gate-implementer-before-red` | `fake-red-valid` (tester), `fake-success` (implementer) | after T010: `agent_start implementer` → `GATE_NOT_SATISFIED` (TEST_AUTHORING); again in RED_VERIFYING → `GATE_NOT_SATISFIED`; after RED_VERIFIED → accepted. No implementer Job exists before RED_VERIFIED (`kubectl get jobs -l strange.company/role=implementer` empty) |
| broken tests do not count as valid red evidence | `tdd-red-broken-test`, `tdd-red-compile-error-then-stub` | `fake-red-broken-test`; `fake-red-compile-error` then `fake-red-with-stub` | SPECIFIED→TEST_AUTHORING→RED_VERIFYING→**TEST_NEEDS_WORK** (reason `build_failed`/`error_class_failure`); implementer refused; second tester with stub → RED_VERIFIED |
| already-passing test produces investigation state | `tdd-already-satisfied` | `fake-already-satisfied` | …→RED_VERIFYING→**ALREADY_SATISFIED**; `SCTaskNeedsHuman{reason="already_satisfied"}` posted; no implementer dispatch possible |
| implementation cannot silently delete acceptance tests | `tdd-tamper-delete` | `fake-red-valid`, `fake-test-delete` | …→IMPLEMENTING→**NEEDS_HUMAN** (`policy_violation`), violation `TEST_TAMPER/test_file_deleted`, branch head reset to the red commit, tag `sc-quarantine/<execution-id>` exists |
| weakened acceptance tests are detected where policy permits | `tdd-tamper-weaken`, `tdd-tamper-golden`, `tdd-tamper-helper`, `tdd-tamper-monkeypatch`, `tdd-tamper-nested-module`, `tdd-phase-conditional`, `tdd-tamper-skip`, `tdd-tamper-discovery`, `tdd-tamper-env-detect` | `fake-test-tamper` (changes an expected value), `fake-golden-update` (rewrites a pre-existing golden file), `fake-test-helper-weaken` (makes a pre-existing assertion helper a no-op), `fake-new-test-monkeypatch` (adds a test file whose `init`/module code patches production), `fake-nested-module-hide` (adds a nested `go.mod`), `fake-phase-conditional-test` (tester writes a test that fails only while the `SC-STUB` marker exists), `fake-test-skip`, `fake-discovery-config-edit`, `fake-test-env-detection` | each → NEEDS_HUMAN (ingestion) or `tamper_detected` at GREEN with sub-code/reason `manifest_file_changed`, `existing_test_modified` (golden, helper), `protected_pass_depends_on_new_test_files` (monkeypatch), `discovery_config_changed` (nested module), `red_not_reproducible` (phase-conditional), `skip_marker_added`, `discovery_config_changed`, `test_env_detection` |
| valid RED → implementation → GREEN path succeeds | `tdd-happy-path` | `fake-red-valid`, `fake-green`, `fake-review-approve` | T001,T010,T014,T020,T030,T032,T033,T040,T041; PR ready with both statuses success; test human merges → T050 DONE; branch deleted; `sc-archive/<task>/merged` exists |
| TEST_SPEC_CONFLICT is supported | `tdd-test-spec-conflict` | `fake-red-valid`, `fake-test-spec-conflict`, `fake-adjudicate-test-invalid`, `fake-red-valid-v2`, `fake-green` | …IMPLEMENTING→**TEST_SPEC_CONFLICT**; adjudication; Hermes tester dispatch refused until test human `/sc accept-test-change`; then TEST_AUTHORING (generation 2 from red g1) →RED_VERIFIED→…→GREEN_VERIFIED; generation 1 `superseded` with its evidence digests intact. Variant `tdd-conflict-rejected`: `fake-adjudicate-test-valid` → implementer re-dispatched (T031) |
| TDD provenance survives retry | `tdd-provenance-survives-retry` | `fake-red-valid`, `fake-green-after-retry`, `fake-review-approve` | first GREEN → RED_VERIFIED (merit 1, phase of next = `green_retry`); second → GREEN_VERIFIED; record keeps both green verifications and the unchanged `test_commit`; after a **squash** merge, `tdd` fields still resolve and `git cat-file -e <red sha>` succeeds in Forgejo via the archive tag |
| exemption requires approved policy | `tdd-exemption-not-in-policy`, `tdd-exemption-auto-docs`, `tdd-exemption-human-approval`, `tdd-exemption-no-self` | `fake-judge-docs`, `fake-produce-docs` | unknown/disallowed class → `EXEMPTION_NOT_ALLOWED`; `documentation-only` → JUDGE_AUTHORING→JUDGE_VALIDATING→JUDGE_APPROVED (auto)→PRODUCING→JUDGING→GREEN_VERIFIED; human class → JUDGE_AWAITING_APPROVAL until `/sc approve-judge`; a worker result with an extra `verification` field → `RESULT_SCHEMA_INVALID` |

Additional organizational tests in the same suite: `tdd-strengthen-after-blocked-refused` (implementer `blocked` → tester dispatch from RED_VERIFIED refused, TDD-5), `tdd-already-satisfied-needs-human` (tester dispatch from ALREADY_SATISFIED refused until `/sc strengthen-tests`, TDD-6), `tdd-forged-trailer-commit` (test human pushes a commit with a copied `SC-Execution` trailer → `/sc reverify green` yields `tamper_detected` `foreign_commit`; `/sc accept-external-commit` then GREEN and a new review, TDD-9), `tdd-second-pr-other-branch` (a second PR from `agent/<task>` into `release/x` gets `tdd-provenance = failure`, TDD-11), `tdd-unapproved-test-change` (generation 2 edits a test of an AC outside the approved conflict → `red_invalid`, TDD-8), `tdd-protected-path-edit` (`fake-protected-path-edit` → `PROTECTED_PATH_VIOLATION`), `tdd-pipeline-config-edit` (edit `.woodpecker/verify.yaml` on the branch → violation; evidence still from base config), `tdd-foreign-pipeline-ignored` (human-created pipeline on the branch → no transition), `tdd-merge-without-provenance` (C09: admin-merge with checks bypassed on a test repository → T052 NEEDS_HUMAN, `SCTddProvenanceBypass`), `tdd-human-command-authz` (plan 29 29-AC-30), `tdd-exhaustion` (5 GREEN failures → NEEDS_HUMAN `retries_exhausted`).

**New fake scenarios requested from plan 28** (harness schema enum): `golden-update`, `test-helper-weaken`, `new-test-monkeypatch`, `nested-module-hide`, `phase-conditional-test`, `green`, `red-compile-error`, `red-with-stub`, `red-valid-v2`, `test-delete`, `test-skip`, `discovery-config-edit`, `test-env-detection`, `review-approve`, `review-request-changes`, `review-reject`, `adjudicate-test-valid`, `adjudicate-test-invalid`, `judge-docs`, `produce-docs`. Each applies a scripted fixture commit from `test/e2e/fake-target/<scenario>/` and returns a fixed result.

### 22.3 Determinism

Fake harnesses make no model calls (the Gateway's fake route records zero-cost ledger rows, plan 53). Every expected trajectory is a committed YAML file `test/e2e/tdd/<test>.yaml` (`events`, `expected_states`, `expected_reasons`) consumed by both the library runner and the live runner, so the library and the cluster are held to the same trajectory.

## 23. Acceptance criteria

### GATE-C04 (with plans 20 and 22)

- [ ] **21-AC-1** `tdd-gate-implementer-before-red` passes live: the implementer `agent_start` returns `GATE_NOT_SATISFIED` with `details.task_state` = `TEST_AUTHORING` and later `RED_VERIFYING`, and is accepted only after `RED_VERIFIED` [SPEC §52, §106].
- [ ] **21-AC-2** `tdd-red-broken-test` and `tdd-already-satisfied` pass live with real Woodpecker pipelines; the stored red-result `classification.outcome` values are `red_invalid` and `already_satisfied`.
- [ ] **21-AC-3** RED evidence for `fake-red-valid` shows `observed.base_sha` = task base commit, `observed.head_sha` = red commit, and the pipeline's `config_revision` = base commit (plan 20 20-AC-28).

### GATE-C10 (with plan 46)

- [ ] **21-AC-4** Phase tags: for the `tdd-provenance-survives-retry` task, `SELECT phase, count(*) FROM ledger.cost_records WHERE task_id=$1 GROUP BY phase` (fake route rows) returns `test_authoring`, `implementation`, `green_retry` (1 execution each) and `review`; no `red_verification` rows. (moved from C04: needs the C05 ledger, GC-01)

- [ ] **21-AC-10** `sc-e2e run --suite c10` on the cluster exits 0; the plan 46 suite report lists every row of §22.2 and the additional tests with `observed_trajectory == expected_trajectory`.
- [ ] **21-AC-11** For `tdd-happy-path`, `GET /v1/tasks/{id}?view=record` validates against `task.schema.yaml`, and `tdd` holds `test_execution`, `test_commit`, `red_verification.result = expected_failure`, `implementation_execution`, `implementation_commit`, `green_verification.result = success` [SPEC §56].
- [ ] **21-AC-12** For `tdd-provenance-survives-retry` after a squash merge: the task record still lists the red commit and both GREEN verifications, and `GET /repos/{o}/{r}/git/commits/<red sha>` returns 200 while `agent/<task>` no longer exists.
- [ ] **21-AC-13** For `tdd-test-spec-conflict` (approved scope `AC-2`): the PR body shows the scoped diff `red(g1)..red(g2)` limited to AC-2's test files, and plan 22 R13 passes; the transition list contains T080, T082, T083 (actor `human:<test user>`), T013, T020; `tdd.generations[0].status = superseded`, `tdd.generations[1].start_commit = generations[0].test_commit`, and both manifests exist in S3 with their recorded digests.
- [ ] **21-AC-14** No paid model call is made during `sc-e2e run --suite c10`: the ledger rows created by the run all have `provider = fake` (plan 53).

### GATE-F05 contributions (owned by plan 29)

- [ ] **21-AC-20** `make test-tdd-library` replays every `test/e2e/tdd/*.yaml` trajectory against the library with fake facts and passes.

## 24. Explicit non-goals

- Automatic merge of accepted PRs (ADR-0019).
- Test-quality scoring beyond plan 22's rules (mutation testing post-MVP).
- Live collaboration between tester and implementer (SPEC §127): they communicate only through Git, evidence, handoffs and decisions.
- Multi-repository tasks.

## 25. Open questions

1. **Reviewer `purpose` and `conflict_decision` fields** in `worker-result.schema.yaml` (plan 27). Blocking: no (needed at C10). Resolution: plan 27 adds them as optional fields with an `if purpose == adjudication then required` rule. Owner: 27.
2. **New fake scenarios** (§22.2) in `harness.schema.yaml` (plan 28). Blocking: no (C10). Owner: 28.
3. **Plan 31's tester merit event "review rejected tests as insufficient"** is not adopted: reviews judge implementations; a test review in RED_VERIFIED is auxiliary and does not count as a tester merit failure. Blocking: no. Owner: 31/29.
4. **Researcher producer phase:** research producers are tagged `specification` (role file accounting) rather than `implementation`. Blocking: no; revisit with plan 32 if research tasks become a large share of spend. Owner: 32.
