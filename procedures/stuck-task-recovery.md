# Procedure: Stuck task recovery

**Owning plan:** plans/29-task-state-machine.md
**SPEC references:** §72–§74, §96, §113 ("stuck task recovery")
**Severity / urgency:** degraded (one or a few tasks)
**Expected duration:** 10–45 min
**Requires break-glass:** cluster kubeconfig and `SC-RUNNER-ADMIN` (your per-operator `scadm_<kid>` credential; the break-glass credential from the recovery bundle only as fallback) for inspection and admin-API steps; the SQL step in §E additionally needs the CNPG superuser path of `agent-runner-failure.md` and is break-glass

## When to use

- `SCTaskStuck` (a task beyond its state's `stall.alertAfter`), `SCExecutionStuck`, `SCNeedsHumanBacklog` (tasks waiting > 72 h for a human), `SCTaskEventRejected` (an evidence or result event matched no transition), `SCTaskInvariantViolation`.
- A task whose `needs_human.reason` is `stalled` (watchdog escalation, plan 29 T140).
- After an `sc-pg` point-in-time restore (§F).

Not for: decisions on TDD outcomes (`tdd-task-recovery.md`), Runner down (`agent-runner-failure.md`), budget stops (`budget-emergency-stop.md`).

## Preconditions and required material

- Toolbox shell: `./bootstrap shell`, then `sc-env`.
  ```sh
  read -rs SC_RUNNER_ADMIN; export SC_RUNNER_ADMIN   # your per-operator credential scadm_<kid>.<secret> (CD-30); the break-glass credential only if yours is unavailable
  kubectl -n strange-company port-forward deploy/agent-runner 18080:8080 >/tmp/pf-runner.log 2>&1 &
  RUNNER=http://127.0.0.1:18080
  AUTH=(-H "Authorization: Bearer ${SC_RUNNER_ADMIN}" -H "X-SC-Reason: <incident or task id and why>")   # no X-SC-Actor: identity comes from the credential (CD-30); a request carrying it gets 400
  psql_ro() { kubectl -n strange-company exec -i "$(kubectl -n strange-company get pod -l cnpg.io/cluster=sc-pg,role=primary -o name)" -c postgres -- psql -d strange_company -At -c "$1"; }
  ```
- A Forgejo account in team `Owners` or `engineers` for `/sc` commands.

## Safety checks before acting

1. Runner healthy: `kubectl -n strange-company get deploy agent-runner` shows `1/1` ready and `SCRunnerDown` is not firing. If the Runner is down, use `agent-runner-failure.md` first — a down Runner makes every task look stuck. A Runner fix that must merge while the Runner cannot post its statuses lands through `procedures/emergency-merge.md`.
2. Policy loaded: `curl -fsS "${AUTH[@]}" $RUNNER/v1/policy/status | jq -r .status` = `ok`. If `degraded`, fix policy first (dispatch is disabled, tasks waiting on Hermes cannot move).
3. Emergency stop: `curl -fsS "${AUTH[@]}" $RUNNER/v1/admin/emergency-stop | jq -r .level` = `none`. If not, tasks are `BLOCKED` by design (`budget-emergency-stop.md`).
4. Never update `runner.tasks`/`runner.transitions` by SQL except §E, and never delete Jobs, Secrets or deploy keys by hand while an execution is non-terminal (use the quarantine API).

## Steps

### A. Identify stuck tasks

1. `psql_ro "select id, state, state_entered_at, claimed_by from runner.tasks where state not in ('DONE','CANCELLED') order by state_entered_at limit 50"`.
   Expected: rows; compare `now() - state_entered_at` with the state's thresholds (`contracts/state-machine.yaml` `stall`).
2. For each candidate `T`: `curl -fsS "${AUTH[@]}" $RUNNER/v1/tasks/$T | jq '{state, current_execution, attempts, last_transition}'` and `curl -fsS "${AUTH[@]}" $RUNNER/v1/tasks/$T/executions | jq '.[-1]'`.
3. Classify by the table and go to the section:

| Observation | Section |
|---|---|
| execution-required state (`TEST_AUTHORING`, `IMPLEMENTING`, `REVIEW`, `JUDGE_AUTHORING`, `PRODUCING`), execution `QUEUED` for long | B |
| same, execution `STARTING`/`RUNNING` past its deadline (`SCExecutionStuck`) | C |
| same, **no** non-terminal execution | D |
| verifying state | `tdd-task-recovery.md` §A |
| dispatch-ready state waiting on Hermes (`RED_VERIFIED`, `TEST_NEEDS_WORK`, `GREEN_VERIFIED`, `CHANGES_REQUESTED`, `TEST_SPEC_CONFLICT`, `JUDGE_APPROVED`) | G |
| `NEEDS_HUMAN` / `BLOCKED` for days (`SCNeedsHumanBacklog`) | H |
| `SCTaskEventRejected` / `SCTaskInvariantViolation` | E |

### B. Execution queued for long

1. `curl -fsS "${AUTH[@]}" "$RUNNER/v1/tasks?state=IMPLEMENTING&state=TEST_AUTHORING&state=REVIEW" | jq '.tasks | length'` and Prometheus `sc_queue_depth`. Expected: queue depth > 0 because another execution holds the single worker slot (self-hosting-minimum `maxWorkers: 1`).
2. If the slot holder is itself stuck → section C on that execution. If no execution is running and the queue is non-empty for > 10 min → the dispatcher is wedged: `kubectl -n strange-company rollout restart deploy/agent-runner` (the dispatcher resumes from checkpoints; plan 26 §14.3). Expected: the queued execution reaches `STARTING` within 2 min.

### C. Execution past its deadline

1. `kubectl -n agent-jobs get jobs,pods -l strange.company/task-id=$T`. Expected: the Job is gone (then the watcher should have finalized it) or still running.
2. Quarantine-stop it through the API (revokes credentials first, preserves evidence):
   `curl -fsS -X POST "${AUTH[@]}" -H 'Idempotency-Key: stuck-'$T'-1' -H 'Content-Type: application/json' -d '{"reason":"execution past deadline, stuck-task-recovery C"}' $RUNNER/v1/executions/<execution-id>/quarantine | jq '.steps'`.
   Expected: every step `done` or `already_done`. The execution ends `FAILED` (infrastructure); the task follows T100/T101 (requeue or NEEDS_HUMAN).

### D. Execution-required state without an execution (crash window)

1. `psql_ro "select id, state, failure_code from runner.executions where task_id='$T' order by queued_at desc limit 3"`. Expected: the latest execution is terminal.
2. Check whether its terminal event was rejected: `psql_ro "select at, event, reason_code, detail from runner.rejected_events where task_id='$T' order by at desc limit 5"`.
3. If a result was stored but not applied, restart the Runner (the outbox replays the event). If still stuck after 5 min, escalate the task: comment `/sc escalate execution finished without task transition (stuck-task-recovery D)`, then decide with `/sc retry` (resumes the dispatch-ready state recorded from the execution's `return_state`).

### E. Rejected events and invariant violations

1. `psql_ro "select at, actor, event, reason_code, detail from runner.rejected_events where task_id='$T' order by at desc limit 10"` and the Runner log line `task_invariant_violation`.
2. A rejected `red_classified`/`green_classified` usually means the head moved after the trigger: run `tdd-task-recovery.md` §I (external push).
3. An invariant violation that the Runner cannot repair (e.g. `history[-1].to_state != state` after a manual DB change) is break-glass:
   a. Stop dispatch for the task: `/sc block invariant violation under investigation`.
   b. Take a CNPG backup on demand (plan 12 procedure `restore-cnpg.md` §on-demand backup).
   c. Using the superuser path of `agent-runner-failure.md`, insert a corrective `transitions` row via the Runner's repair function `select runner.repair_task_state('$T', '<target state>', 'human:<you>', '<reason>')` (plan 26 migration; it inserts a `machine_migration`-style transition and fixes `tasks.state` atomically). The target may only be `NEEDS_HUMAN`, `BLOCKED`, `CANCELLED` or the state the last valid transition left (TDD-25); the function refuses anything else. Continue from there with `/sc` commands. Never `UPDATE` rows directly.
   d. `/sc unblock` and verify the invariant job reports 0 (`sc_task_invariant_violations == 0` after its next run).

### F. After an `sc-pg` point-in-time restore

1. List tasks whose `updated_at` is within the RPO window before the restore point: `psql_ro "select id, state from runner.tasks where updated_at > '<restore point>'::timestamptz - interval '15 minutes'"`.
2. For each: compare with Forgejo (PR merged? branch head?) and Woodpecker (pipelines after the restore point). The Runner re-polls pipelines and re-discovers merges itself (plan 29 §20) within 10 min; wait, then re-check.
3. Tasks the Runner moved to `NEEDS_HUMAN` with `stalled` and transition reason `recovery_gap`: inspect, then `/sc reverify red|green` (re-derive evidence at the same commits) or `/sc retry`.
4. Re-ingest result objects newer than the restore point if handoffs are missing (`aws s3 ls s3://<prefix>-evidence/results/$T/`), via the admin re-ingest API of plan 26 (`POST /v1/executions/{id}/reingest`, request to plan 26) — if unavailable, record the gap in the task issue.

### G. Waiting on Hermes

1. Check Hermes: `SCHermesDown`? Use `hermes-failure.md`. Ask Hermes in the dashboard about the task.
2. If Hermes is healthy but not acting (e.g. it lost the conversation), either instruct it in the dashboard ("continue sc-<n>: dispatch implementer") or let the watchdog escalate (72 h → NEEDS_HUMAN `stalled`), or `/sc escalate` now.

### H. Long-waiting NEEDS_HUMAN / BLOCKED

1. `psql_ro "select id, needs_human->>'reason', state_entered_at from runner.tasks where state in ('NEEDS_HUMAN','BLOCKED') order by state_entered_at"`.
2. For each, take the decision through `tdd-task-recovery.md` (by reason) or close it: `/sc cancel <reason>` or `/sc done <reason>`.

## Verification

- `psql_ro "select count(*) from runner.tasks where state not in ('DONE','CANCELLED') and state_entered_at < now() - interval '72 hours' and state not in ('NEEDS_HUMAN','BLOCKED','AWAITING_MERGE','SPECIFIED')"` returns 0.
- `sum(sc_tasks_stuck) == 0` in Prometheus 15 min later; `SCTaskStuck`/`SCExecutionStuck` resolved.
- Every task touched has a new transition row with actor `human:<your operator identity>` (from the credential) or a normal Runner/CI actor.

## Rollback / abort

`/sc block` stops further automatic action on a task at any time. The repair function in §E is the only state-changing SQL; if it was applied wrongly, apply it again with the correct state (history keeps both rows).

## Evidence to record

Task ids, alert names, sections applied, quarantine reports, any repair-function calls with before/after state, in the task's tracking issue and (for §E/§F) in the incident log.

## Follow-ups

- Repeated D or E cases → bug in Runner event handling; open an issue with the rejected event rows.
- Repeated B → capacity (plan 51) or a stuck dispatcher (plan 26).
