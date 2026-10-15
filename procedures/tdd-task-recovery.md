# Procedure: TDD task and pipeline recovery

**Owning plan:** plans/21-tdd-workflow.md (with plans/22-verification-policy.md and plans/29-task-state-machine.md)
**SPEC references:** §50–§56, §74, §97, §106, §113 ("TDD pipeline recovery")
**Severity / urgency:** degraded (single task) to outage (verification machinery down for all tasks)
**Expected duration:** 10–30 min per task; 1–2 h for a CI-wide failure
**Requires break-glass:** no for the Forgejo `/sc` path; cluster kubeconfig and `SC-RUNNER-ADMIN` (your per-operator `scadm_<kid>` credential; the break-glass credential from the recovery bundle only as fallback) only for the admin-API and evidence-inspection steps

## When to use

- Alerts: `SCTaskNeedsHuman` / `SCTaskRetriesExhausted` with `reason` ∈ {`infrastructure_exhausted`, `retries_exhausted`, `policy_violation`, `review_rejected`, `test_spec_conflict`, `already_satisfied`, `needs_investigation`, `external_push`, `worker_blocked`, `approval_required`}; `SCTddVerificationStalled`; `SCTddPipelineFailure`; `SCNeedsHumanBacklog`.
- A task in `TEST_NEEDS_WORK`, `ALREADY_SATISFIED`, `NEEDS_INVESTIGATION`, `TEST_SPEC_CONFLICT` or `JUDGE_AWAITING_APPROVAL` that needs a human decision.
- A suspected wrong RED/GREEN classification.

Not for: tasks stuck without an alert reason or invariant violations (`stuck-task-recovery.md`); merges without provenance (`merge-without-provenance.md`); worker credential leaks (`quarantine-worker.md`); Woodpecker down (`woodpecker-failure.md`).

## Preconditions and required material

- A Forgejo account in team `Owners` or `engineers` (plan 29 §7.3) — for `/sc` commands on the task's tracking issue or PR.
- For inspection and admin steps: toolbox shell `./bootstrap shell`, then `sc-env`. Then:
  ```sh
  read -rs SC_RUNNER_ADMIN; export SC_RUNNER_ADMIN   # your per-operator credential scadm_<kid>.<secret> (CD-30); the break-glass credential only if yours is unavailable
  kubectl -n strange-company port-forward deploy/agent-runner 18080:8080 >/tmp/pf-runner.log 2>&1 &
  RUNNER=http://127.0.0.1:18080
  AUTH=(-H "Authorization: Bearer ${SC_RUNNER_ADMIN}" -H "X-SC-Reason: <incident or task id and why>")   # no X-SC-Actor: identity comes from the credential (CD-30); a request carrying it gets 400
  T=sc-<n>     # the task
  psql_ro() { kubectl -n strange-company exec -i "$(kubectl -n strange-company get pod -l cnpg.io/cluster=sc-pg,role=primary -o name)" -c postgres -- psql -d strange_company -At -c "$1"; }   # read-only queries only
  ```
- Evidence bucket read access for `aws s3` (object-storage credential per plan 14; bucket `<prefix>-evidence`).

## Safety checks before acting

1. Read the task first: `curl -fsS "${AUTH[@]}" $RUNNER/v1/tasks/$T | jq '{state, human_state, needs_human, resume_state, attempts, branch, pr_url}'` and `curl -fsS "${AUTH[@]}" $RUNNER/v1/tasks/$T/transitions | jq -r '.transitions[-5:][] | "\(.at) \(.actor) \(.from)->\(.to) \(.reason)"'`. Expected: the state and reason match the alert. If not, stop and use `stuck-task-recovery.md`.
2. **Never** edit a protected acceptance test on the agent branch yourself to "make it pass", never push to `agent/<task>` without intending to re-verify (it moves the task to NEEDS_HUMAN `external_push`), and never change task state in SQL.
3. A `policy_violation` may be a security event. If the violation code is `SECRET_DETECTED`, `REF_VIOLATION` or `PROTECTED_PATH_VIOLATION`, run `quarantine-worker.md` first.
4. Blast radius: one task, except §A/§B (all verifying tasks).

## Steps

### A. Verification stalled or failing (`SCTddVerificationStalled`, `SCTddPipelineFailure`, reason `infrastructure_exhausted`)

1. List affected tasks: `curl -fsS "${AUTH[@]}" "$RUNNER/v1/tasks?state=RED_VERIFYING&state=GREEN_VERIFYING&state=JUDGE_VALIDATING&state=JUDGING&state=NEEDS_HUMAN" | jq -r '.tasks[] | "\(.task_id) \(.state)"'`.
   Expected: a list. If more than one task is affected, treat it as systemic (step 2); else go to step 3.
2. Systemic: check Woodpecker (`kubectl -n woodpecker get pods`, alert `SCWoodpeckerDown`/`SCWoodpeckerNoAgents`) and `sc-ci-config` (plan 20). If any is down, follow `woodpecker-failure.md`, then return here. If the repair needs a merge while Woodpecker, `sc-ci-config`, the egress-proxy, the heavy-slot advertiser or the Runner is broken, land it with `procedures/emergency-merge.md`. Check the Runner log for evidence outcomes: `kubectl -n strange-company logs deploy/agent-runner --since=2h | grep -E 'EVIDENCE_|verification_infra_failed' | tail -20`.
   - `EVIDENCE_COMMIT_MISMATCH` repeatedly → someone/something pushes to agent branches; investigate pushes (Forgejo push webhooks) before continuing.
   - `EVIDENCE_SCHEMA_INVALID` for all tasks after an `sc-verify` upgrade → roll back `policy/ci.yaml` `spec.verify.installerImage` by PR (plan 20); evidence never degrades to unclassified.
3. Find the verification's pipeline: `curl -fsS "${AUTH[@]}" $RUNNER/v1/tasks/$T | jq '.evidence'` and the Woodpecker UI `https://ci.<domain>` → repository → pipeline. Expected: a failed or killed `verify` pipeline. If the failure is in the toolchain image (pull error, missing tool), fix `.strange-company/validation.yaml` `verification.image` by a **human PR** (protected path) — the task keeps using its base revision, so it must be re-specified (step 5) or cancelled.
4. When CI is healthy again and the task is `NEEDS_HUMAN` with `infrastructure_exhausted`: comment on the PR or tracking issue
   `/sc retry CI repaired (woodpecker agent restored at <time>)`.
   Expected reply: `Accepted: retry → T066, NEEDS_HUMAN → <resume state>`; the pipeline is re-triggered for verifying resume states. If the reply is `Rejected: GATE_NOT_SATISFIED`, the head moved: use `/sc reverify red` (unimplemented candidate) or `/sc reverify green`.
5. If the base revision's validation is itself broken (wrong command), the task cannot verify. Fix `validation.yaml` on `main` by human PR, then `/sc respec <reason>` with the same criteria in a ```` ```yaml ```` block (`acceptanceCriteria: …`) to start a new generation from a fresh task base, or `/sc cancel` and let Hermes create a new task.

### B. RED says the baseline is broken (`needs_investigation` with reason `baseline_failing`)

1. Fetch the classified evidence: `aws s3 cp s3://<prefix>-evidence/evidence/$T/red/<pipeline>.json - | jq '.observed.tests[] | select(.status_base != "passed" and .status_base != "absent" and .status_base != "skipped")'`.
   Expected: pre-existing tests failing at the base commit.
2. Check whether `main` is red: the latest `push/ci` pipeline on `main` in Woodpecker. If `main` is red, fix `main` first (normal PR). Flaky tests: quarantine via plan 52 `quarantine-check` process.
3. After `main` is green the task's base is still the old commit. Choose: `/sc respec` (new generation from the same base — only if the base failure was flaky) or `/sc cancel` and have Hermes create a fresh task on the new `main`.

### C. Tests already pass (`already_satisfied`) or only partly (`partially_satisfied`)

1. Inspect `classification.ac_results` of the red evidence (as B.1). Ask Hermes for its investigation (it may have dispatched a researcher).
2. Decide:
   - behavior genuinely exists → `/sc done behavior already present; tests kept on sc-archive tag` (the tests are not merged; if they are valuable, open a normal human PR with them);
   - tests are too weak → comment `/sc strengthen-tests <reason>` (optional ```` ```yaml ```` `guidance:`); then Hermes dispatches a tester (plan 29 T016/T015). The previously declared tests must stay in the next RED (plan 22 R12) — no failure is manufactured without a human decision (SPEC §51);
   - criteria wrong → `/sc respec` with corrected criteria.

### D. Retries exhausted (`retries_exhausted`)

1. Read the last failures: `curl -fsS "${AUTH[@]}" $RUNNER/v1/tasks/$T/result | jq '{execution_state, failure_class, failure_code, summary, violations}'` and the latest green evidence `classification.reasons`.
2. If the failures differ and progress was made → `/sc retry grant=2 <reason>`; Hermes will escalate the tier (plan 31). If the same test fails identically every time → likely a test/spec problem: escalate a conflict decision with Hermes, or `/sc respec`; or `/sc cancel`.

### E. Test tamper or other policy violation at ingestion (`policy_violation`, code `TEST_TAMPER`, `ROLE_DIFF_VIOLATION`)

1. Get the violation: `curl -fsS "${AUTH[@]}" $RUNNER/v1/tasks/$T/result | jq '.violations'`. Expected: sub-code (e.g. `TEST_TAMPER` path `…_test.go`). The branch was already reset and the offending head tagged `sc-quarantine/<execution-id>`.
2. Inspect the quarantined diff in Forgejo (compare `<red commit>...sc-quarantine/<execution-id>`).
3. If the implementer legitimately needed a test change, the correct path is a conflict: `/sc retry <reason>` and tell Hermes to instruct the implementer to report `test_spec_conflict` instead of editing tests. If it looks like a deliberate bypass, keep the task in NEEDS_HUMAN and file a security issue; consider a different harness/model.

### F. Reviewer rejected as bypass (`review_rejected`)

1. Read the review comment on the PR and the reviewer's `checks` (`agent_result` via Hermes, or `$RUNNER/v1/tasks/$T/result?execution_id=<reviewer execution>`).
2. If you agree: `/sc cancel <reason>` (or `/sc retry` after instructing Hermes). If you disagree after reading the diff and evidence: `/sc retry <reason>` and Hermes dispatches a new review (reviews ≤ 3).

### G. TEST_SPEC_CONFLICT awaiting a human (`test_spec_conflict`)

1. On the PR read the conflict (`conflict.ac_ids`, `tests`, explanation) and the adjudication (`conflict_decision`).
2. Decide:
   - test is wrong → `/sc accept-test-change conflict=<conflict id> digest=<12 hex from the Runner's conflict comment> <reason>` with optional
     ```yaml
     guidance: "Restrict the body test to requests with GetBody set."
     ```
     Expected reply: `Accepted: accept-test-change → T083`; a wrong conflict id or digest gets `Rejected: PAYLOAD_INVALID`. Hermes then dispatches a tester; a new generation and a new RED follow.
   - test is right → `/sc reject-conflict <reason>` (→ RED_VERIFIED).
   - criterion ambiguous → `/sc respec` with the corrected criteria.

### H. Exemption or judge approval (`approval_required`)

1. Read the judge artifact on the PR (`verification/<task>/judge.yaml`) or the criteria (judge source `acceptance-criteria`), and the exemption class/justification in the tracking issue.
2. `/sc approve-judge <reason>`, `/sc reject-judge` with `findings:` list, or `/sc deny-exemption <reason>` (task continues test-first).

### I. External push (`external_push`) or worker blocked (`worker_blocked`)

1. External push: identify the pusher and the commits (Forgejo branch activity; `git log <last accepted commit>..agent/<task>`). Commits are judged by SHA, never by trailer (plan 22 G2/F10).
   - Intentional human fix: comment `/sc accept-external-commit <sha> [<sha>…] <reason>` listing **exactly** the external commits. Expected: `Accepted: accept-external-commit → T072`; the commits are recorded as `human_commit` provenance, GREEN re-runs, and a new final review is needed (Hermes dispatches it). `/sc reverify green` alone would classify the commits as foreign (`tamper_detected`).
   - Unexplained push (including a commit that merely carries a copied `SC-Execution` trailer): follow `quarantine-worker.md` §unexplained-ref; then `/sc respec` or `/sc cancel` — do not accept the commit.
2. Worker blocked: answer the handoff questions to Hermes (dashboard), then `/sc retry <answer summary>`.

### J. Evidence forgery attempt (`policy_violation` from `EVIDENCE_AMBIGUOUS`, `SCTddProvenanceBypass` from the pipeline)

1. Treat as a security incident: run `quarantine-worker.md` for the last producing execution; read the `sc-verify` step log in Woodpecker (two `::sc-evidence-begin` blocks).
2. Do not retry automatically. Cancel the task (`/sc cancel`) unless the test code that printed the block is identified and removed by a new tester generation (`/sc respec`).

### K. Evidence or manifest object missing (merge facts F4/F7 fail with `evidence_missing`)

1. Check versioning: `aws s3api list-object-versions --bucket <prefix>-evidence --prefix evidence/$T/`. If a delete marker hides it, restore with `aws s3api delete-object --bucket … --key … --version-id <delete-marker-id>`. Expected: the object returns and its sha256 matches `sc-pg` (`psql_ro "select sha256 from runner.evidence where task_id='$T'"`).
2. If unrecoverable: `/sc reverify red` is only possible before implementation; otherwise `/sc reverify green` re-creates GREEN evidence, and RED provenance must be re-established by a new generation (`/sc respec`) — record the gap in the task issue.

### L. Suspected wrong classification

1. Download the classified document and re-run offline: `sc-verify classify --policy policy/verification.yaml --doc red.json --facts facts.json` (facts from `aws s3 cp …` and the task record). Expected: same outcome. If the policy rule is wrong, fix `policy/verification.yaml` by PR (new revision), then `/sc reverify red|green` — the new classification records the new revision; old evidence is kept.

## Verification

- `curl -fsS "${AUTH[@]}" $RUNNER/v1/tasks/$T | jq -r .state` shows the intended state, and the last transition's actor is `human:<your operator identity>` with the command's transition id.
- `sum(sc_needs_human)` decreased by one; `SCTaskNeedsHuman{subject="<task>"}` resolved in Alertmanager within 10 min.
- For §A: `sum(increase(sc_verification_runs_total{outcome=~"infra_error|timeout|evidence_rejected"}[30m])) == 0` after the repair.

## Rollback / abort

Human transitions are recorded and not reversible by deletion; correct a wrong decision with another command (e.g. `/sc block`, then the right command). Pushing to protected branches, editing evidence objects or SQL edits are never part of the rollback.

## Evidence to record

In the tracking issue: the alert, the decision taken and why, the command comment link, evidence digests consulted, and (for §J/§K) the incident reference.

## Follow-ups

- Recurrent `infrastructure_exhausted` → capacity/CI review (plans 20, 51).
- Recurrent conflicts on one repository → improve criteria guidance in Hermes skills (plan 24) or repository test guidance (`AGENTS.md`, human PR).
- Any §J occurrence → security review (plan 34) and an update of the residual-risk record (SI-07).
