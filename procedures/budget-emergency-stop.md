# Procedure: Budget emergency stop, budget exhaustion and resumption

**Owning plan:** plans/33-budget-enforcement.md (runtime: plans/53-model-gateway.md, plans/26-agent-runner.md; design: ADR-0452)
**SPEC references:** §81, §96, §97, §113 ("budget emergency stop"), §4.8
**Severity / urgency:** degraded (spend incident) — act within minutes for `SCAnomalousSpend` / `SCBudgetExhausted`; routine for planned freezes
**Expected duration:** engage ≤ 5 min; diagnose 15–60 min; release and resume 10–30 min
**Requires break-glass:** yes for paths 1 and 3: the cluster kubeconfig (`SC-K3S-ADMIN-KUBECONFIG`, or `SC-BREAK-GLASS-CLUSTER` if that is unavailable) for port-forward or ConfigMap writes, and `SC-RUNNER-ADMIN` for the Runner admin API. Path 2 needs only a Forgejo account allowed to merge into `strange-company`.

## When to use

- Alerts: `SCAnomalousSpend`, `SCBudgetExhausted`, `SCBudgetHourlyCapReached`, `SCBudgetThresholdReached` (when the trend is unexpected), `SCEmergencyStopActive` (someone engaged a stop: verify and track it), `SCEmergencyStopNotApplied`, `SCTaskNeedsHuman` with `reason=budget_exceeded`, `SCGatewayReservationOverrun` with a large value, `SCModelMismatch` showing a much more expensive model.
- Suspected credential compromise of a provider key or a gateway token (then also follow `procedures/rotate-model-credentials.md` §C).
- A prompt-injected or misbehaving Hermes or worker generating unexpected traffic.
- Planned freezes: provider invoice review, price-card uncertainty, maintenance.
- After any `sc-pg` point-in-time restore (section "Re-assert after restore").

## Preconditions and required material

- A working toolbox: `./bootstrap shell`. Every command below runs **inside** that shell unless marked `./bootstrap exec`. The shell provides `kubectl`, `curl`, `jq` and `yq`, and sets up the kubeconfig and SSH tunnel on demand with `sc-env` (plan 50).
- `SC-RUNNER-ADMIN` from the recovery bundle (`procedures/break-glass-access.md` §"open the recovery bundle"). Paste it only into the shell's environment:
  `read -rs SC_RUNNER_ADMIN; export SC_RUNNER_ADMIN`.
- Your operator name for the audit actor: `export SC_ACTOR="human:<your-name>"`.
- Helper port-forwards (keep the shell open):

  ```sh
  sc-env                                                     # kubeconfig + tunnel (plan 50)
  kubectl -n strange-company port-forward deploy/agent-runner 18080:8080 >/tmp/pf-runner.log 2>&1 &
  kubectl -n monitoring port-forward svc/prometheus-operated 19091:9090 >/tmp/pf-prom.log 2>&1 &
  RUNNER=http://127.0.0.1:18080; PROM=http://127.0.0.1:19091
  AUTH=(-H "Authorization: Bearer ${SC_RUNNER_ADMIN}" -H "X-SC-Actor: ${SC_ACTOR}")
  gw_status() { for p in $(kubectl -n strange-company get pods -l app.kubernetes.io/name=model-gateway -o name); do
      kubectl -n strange-company port-forward "$p" 19090:9090 >/dev/null 2>&1 & pf=$!; sleep 2
      echo "== $p"; curl -fsS http://127.0.0.1:19090/sc/v1/status | jq '{estop, policy: .policy}'; kill $pf; done; }
  promq() { curl -fsS --get "$PROM/api/v1/query" --data-urlencode "query=$1" | jq -r '.data.result[] | "\(.metric) \(.value[1])"'; }
  ```

  The pod label `app.kubernetes.io/name=model-gateway` is fixed by plan 54 (verify at implementation). If `curl $RUNNER/readyz` fails, the Runner is unavailable: use path 3.

## Safety checks before acting

1. **Decide the level.**
   - `workers`: stops every worker's model spend and cancels worker Jobs. Hermes keeps working and can explain the situation to humans.
   - `all`: also stops Hermes' model calls. Use it when Hermes itself is the spender (`sc_model_spend_micro_usd_total{role="orchestrator"}` dominates), is suspected compromised, or the cause is unknown and spend is still rising.
2. **What a stop does NOT do.** It does not shut down Forgejo, Woodpecker, Flux, CNPG, backups, monitoring, the Runner's read APIs or the Hermes process. **Do not** scale down or delete those to "stop spending". That breaks recovery and evidence without stopping spend any faster.
3. **What a stop interrupts.**
   - Every running worker execution becomes `FAILED` (class `budget`, code `EMERGENCY_STOP`), and every queued one `CANCELLED`.
   - Their tasks move to `BLOCKED`. Partial work on `agent/<task-id>` branches stays, but is not judged further.
   - Nothing resumes by itself when the stop is lifted.
4. **Do not edit the ledger or task tables with SQL.** Read-only queries are fine. All state changes go through the APIs below.
5. **Do not create the ConfigMap through Git or Flux.** It must stay outside GitOps (ADR-0452).

## Steps

### A. Engage the stop (pick the first path that works)

**A1. Path 1 — Runner admin API (preferred).**

```sh
curl -fsS -X PUT "$RUNNER/v1/admin/emergency-stop" "${AUTH[@]}" \
  -H "Idempotency-Key: estop-$(date -u +%Y%m%dT%H%M%SZ)" -H 'Content-Type: application/json' \
  -d '{"level":"workers","reason":"<incident id + one line why>"}' | jq .
```

- Expected: HTTP 200, `.effective_level == "workers"` (or `all`), `.sources.db == "workers"`. Idempotent: re-sending with the same `Idempotency-Key` is a no-op, and setting the level that is already current appends no event (plan 33 §17). A new key with a different level appends one audit event.
- Different result:
  - 401 → wrong `SC_RUNNER_ADMIN` or missing actor header. Re-check it, otherwise use A3.
  - Connection refused or 5xx → use A3 immediately, then diagnose the Runner (`procedures/agent-runner-failure.md`).

**A2. Path 2 — Git (planned freezes, or when you also need the stop to survive database restores).**

1. On a branch of `strange-company`, edit `policy/budgets.yaml`:

   ```yaml
   emergencyStop:
     level: workers
     reason: "<incident id + why>"
   ```

2. Commit, push, open a PR. CI (`make policy-check`) must pass. Merge it (human).
3. Speed up Flux from the shell: `flux reconcile source git flux-system && flux reconcile kustomization <policy kustomization name> --with-source`. The name is in plan 18. List them with `flux get kustomizations`.
4. Expected: within 15 min of the merge, `gw_status` shows `.estop.sources.git == "workers"` on every replica.
   - Not reached → check `flux get kustomizations` for errors, and `SCGatewayPolicyInvalid` (an invalid policy keeps the old level).
   - While waiting, engage A1 or A3 as well.

**A3. Path 3 — Kubernetes ConfigMap (Runner or DB broken, or admin token unavailable).**

```sh
kubectl -n strange-company create configmap sc-emergency-stop \
  --from-literal=level=workers --from-literal=reason="<incident id + why>" \
  --dry-run=client -o yaml | kubectl apply -f -
kubectl -n strange-company annotate configmap sc-emergency-stop --overwrite \
  strange.company/set-by="${SC_ACTOR}" strange.company/set-at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

- Expected: `gw_status` shows `.estop.sources.k8s.level == "workers"` and `.estop.effective_level` ≥ `workers` within 10 s. This step is idempotent: re-applying changes nothing.
- Different result:
  - `Forbidden` → your kubeconfig is not cluster-admin; use `SC-BREAK-GLASS-CLUSTER` (`procedures/break-glass-access.md`).
  - Gateway status unreachable → go to A4.

**A4. Last resort — the Gateway itself does not enforce** (traffic continues while `effective_level` ≥ `workers`, or the Gateway is unreachable but provider consoles show traffic):

1. `flux suspend helmrelease -n strange-company strange-company`. This stops Flux from restoring the replicas. Record that you did it.
2. `kubectl -n strange-company scale deploy/model-gateway --replicas=0`. All model traffic now fails closed, Hermes included.
3. If provider consoles still show traffic after 5 min, the traffic does not come through the Gateway, so a provider key is in use elsewhere. Revoke the key in the provider console and follow `procedures/rotate-model-credentials.md` §C.

### B. Verify the stop is effective

1. `promq 'sc_emergency_stop_level{source="effective"}'` → `1` (workers) or `2` (all) on **every** Gateway replica. `promq 'sc_runner_emergency_stop_level{source="effective"}'` → the same value.
   - If they differ for more than 2 min, `SCEmergencyStopNotApplied` fires. Go to section D.
2. Within 2 min: `promq 'sum(increase(sc_model_spend_micro_usd_total{role!="orchestrator"}[2m]))'` → `0` (with `all`: drop the `role` filter).
   - Not zero after 5 min → go to A4.
3. Within 90 s: `curl -fsS "$RUNNER/v1/tasks?state=BLOCKED&blocked_reason=emergency_stop" "${AUTH[@]}" | jq '.items[].task_id'` lists the interrupted tasks. `kubectl -n agent-jobs get jobs -l app.kubernetes.io/managed-by=agent-runner` → no running Jobs (completed ones may linger until log capture, ≤ 60 s).
4. Core services are still up: `flux get kustomizations` all Ready; `curl -fsS "$RUNNER/readyz"`; Forgejo web reachable.
5. `SCEmergencyStopActive` was delivered to your notification transport. If not, see `procedures/notification-failure.md`. The stop is in effect regardless.

### C. Diagnose the cause (read-only)

1. Top spenders in the last hour:

   ```sh
   promq 'topk(10, sum by (provider, model, harness, role, phase) (increase(sc_model_spend_micro_usd_total[1h])))'
   ```

2. Per task and execution (ledger, read-only):

   ```sh
   kubectl -n strange-company exec -i sc-pg-1 -c postgres -- psql -d strange_company -At -c \
    "SELECT coalesce(task_id,'(hermes)'), coalesce(execution_id,'-'), model_requested, count(*), sum(budget_charge_micro_usd)/1e6
       FROM ledger.cost_records WHERE started_at > now() - interval '2 hours'
      GROUP BY 1,2,3 ORDER BY 5 DESC LIMIT 20;"
   ```

   The CNPG pod name and the local `psql` access are verified at implementation (plan 12). If `sc-pg-1` is not primary, use the pod labelled `role=primary`.
3. Denials: `SELECT code, scope_key, count(*) FROM ledger.denials WHERE at > now()-interval '2 hours' GROUP BY 1,2 ORDER BY 3 DESC;`
4. Classify the cause and record it:
   - (a) legitimate load above the budget;
   - (b) a runaway loop in one task;
   - (c) Hermes behavior;
   - (d) a price-card or usage-parsing error (`SCModelUsageUnknown`, `SCGatewayReservationOverrun`; then `procedures/pricing-update.md` / `procedures/gateway-failure.md`);
   - (e) credential misuse (then `procedures/rotate-model-credentials.md` §C).
5. For (b): cancel that task (`curl -fsS -X POST "$RUNNER/v1/tasks/<id>/cancel" "${AUTH[@]}" -H 'Content-Type: application/json' -d '{"reason":"runaway spend"}'`) before releasing.

### D. Stop not applied (`SCEmergencyStopNotApplied`)

1. Compare levels and sources: `curl -fsS "$RUNNER/v1/admin/emergency-stop" "${AUTH[@]}" | jq .` and `gw_status`.
2. If the Runner reports a lower level, its sources are stale:
   - a DB problem → `procedures/agent-runner-failure.md`;
   - an informer problem → the Runner logs `estop source` errors.

   The Gateway still denies spend, so this only wastes Jobs. If dispatch continues, use path 3 and restart the Runner (`kubectl -n strange-company rollout restart deploy/agent-runner`).
3. If one Gateway replica reports a lower level, delete that pod (`kubectl -n strange-company delete pod <pod>`). The replacement reads all sources at startup (33-AC-18).

### E. Budget exhausted without a stop (`SCBudgetExhausted`, `SCBudgetHourlyCapReached`)

This is enforcement working as designed. Requests in the exhausted scope are denied, and tasks hit by it are `BLOCKED` with `blocked_reason=budget_window_exhausted`.

1. Identify the scope from the alert labels (`scope_kind`, `provider`).
2. Decide:
   - wait for the period to roll over (UTC hour, day or month); or
   - raise the limit with a PR on `policy/budgets.yaml` (the human merge is the approval; record the reason in the PR); or
   - engage a stop (section A) if the spend was unexpected.
3. Resume the blocked tasks only after headroom exists (section G).

### E2. Requests contended, not exhausted (`SC_BUDGET_CONTENDED`)

`SC_BUDGET_CONTENDED` (429) means headroom exists on settled spend but is temporarily held by in-flight reservations; SDKs retry and tasks keep their state (plan 33 §14.2). Act only if `sum(increase(sc_budget_denials_total{code="SC_BUDGET_CONTENDED"}[1h])) > 0` persists for hours: lower `perExecution.maxConcurrentRequests`, or raise the contended limit (usually `global.hourlyUsd`), by PR. Do not engage a stop for contention.

### F. Task budget exhausted (`SCTaskNeedsHuman`, reason `budget_exceeded`)

This also fires when a dispatch is refused because the `task:` scope lacks the minimum headroom for the requested tier (plan 33 §14.5); the task is then in `NEEDS_HUMAN` without a failed execution.

1. Inspect the task: `curl -fsS "$RUNNER/v1/tasks/<id>" "${AUTH[@]}" | jq '{state, cost, attempts}'`.
2. Choose one:
   - cancel: `POST /v1/tasks/<id>/cancel`;
   - ask Hermes to decompose the task;
   - raise this task's limit:

     ```sh
     curl -fsS -X PUT "$RUNNER/v1/admin/budget-scopes/task:<id>" "${AUTH[@]}" -H 'Content-Type: application/json' \
       -d '{"limit_usd":"<new limit>","reason":"<why>"}' | jq .
     ```

     Expected: 200 and `limit_source` = `override:<uuid>`. This endpoint is plan 33 §14.11. If plan 26 has not implemented it yet, the only option is a policy PR raising `perTask.maxCostUsd`.
3. Return the task to work with the human `retry` command from `NEEDS_HUMAN` (plan 29 §7.3, T066): the same call as section G step 4 with `"expected_from":"NEEDS_HUMAN"` and `"decision":{"command":"retry"}` (optionally `"args":{"grant":N}`), `to` = the recorded `resume_state`.

### G. Release the stop and resume work

1. Confirm the cause is handled (section C conclusion recorded).
2. Clear **every** source that is set. The effective level is the maximum of all sources.
   - Path 1: `curl -fsS -X PUT "$RUNNER/v1/admin/emergency-stop" "${AUTH[@]}" -H "Idempotency-Key: estop-release-$(date -u +%Y%m%dT%H%M%SZ)" -H 'Content-Type: application/json' -d '{"level":"none","reason":"<resolution>"}' | jq .`
   - Path 3: `kubectl -n strange-company delete configmap sc-emergency-stop`. Expected: `configmap "sc-emergency-stop" deleted`, or NotFound if it was never set. NotFound is fine.
   - Path 2: PR setting `emergencyStop: {level: none}`, merged.
   - A4 was used: `kubectl -n strange-company scale deploy/model-gateway --replicas=2`, then `flux resume helmrelease -n strange-company strange-company`.
3. Expected: `gw_status` shows `.estop.effective_level == "none"` on every replica, and `promq 'sc_emergency_stop_level{source="effective"}'` = 0. `SCEmergencyStopActive` resolves.
   - If still ≥ 1, read `.estop.sources` to find the remaining source.
   - `degraded == "workers"` means the Gateway cannot read the Kubernetes API: see `procedures/gateway-failure.md` §estop.
4. Resume tasks deliberately, one at a time or in a small batch:

   ```sh
   curl -fsS "$RUNNER/v1/tasks?state=BLOCKED&blocked_reason=emergency_stop" "${AUTH[@]}" | jq -r '.items[] | "\(.task_id) \(.resume_state)"'
   # for each task you choose to resume:
   curl -fsS -X POST "$RUNNER/v1/tasks/<id>/transitions" "${AUTH[@]}" -H 'Content-Type: application/json' \
     -H "Idempotency-Key: resume-<id>-$(date -u +%Y%m%d)" -d '{"expected_from":"BLOCKED","to":"<resume_state>","decision":{"command":"unblock"},"reason":"resume after emergency stop <incident>"}'
   ```

   The body follows plan 29 §7.3: `decision.command` is required, `expected_from` must be the current state and `to` must equal the recorded `resume_state` (T105). Expected: 200 and the task's state equals `resume_state`. Hermes may now dispatch it again. Use `blocked_reason=budget_window_exhausted` for section E tasks.
   - 409 → the state machine (plan 29) refused the transition. Leave the task `BLOCKED` and open a follow-up.

### Re-assert after restore (before and after any `sc-pg` PITR)

A point-in-time restore discards emergency-stop events written after the recovery point, **including a path-1 stop engaged to protect the restore itself** (review COST-14). Memory and Alertmanager history are not authoritative (SPEC §4.1), so the stop that protects a restore lives outside the database.

1. **Before** starting any `sc-pg` PITR (`procedures/restore-cnpg.md`): engage the stop with **A3** (ConfigMap) at level `workers` or `all`, or keep an existing **A2** (Git) stop. Never rely on A1 for a restore. Expected: `gw_status` shows `.estop.sources.k8s.level` (or `.git`) ≥ `workers`.
2. **After** the restore, compare the DB source with the source of step 1: `curl -fsS "$RUNNER/v1/admin/emergency-stop" "${AUTH[@]}" | jq '.sources'`. If the stop must continue after the ConfigMap is removed, first re-engage it through A1 at the same level, then delete the ConfigMap; otherwise release per section G.
3. During black-start recovery the Git source already holds `workers` (plan 33 §20, ADR-0554). Do not release it until the recovery finalize PR.

## Verification

- Stop engaged: `sc_emergency_stop_level{source="effective"} ≥ 1` on every Gateway replica, `sc_runner_emergency_stop_level{source="effective"}` equal to it, zero worker spend increase over 5 min, no running worker Jobs, all Flux Kustomizations Ready.
- Stop released: `effective = 0` everywhere, `.estop.sources` all `none`, and a test dispatch, or the next Hermes dispatch, succeeds.
- Resumed tasks: each listed task left `BLOCKED` through a transition whose actor is your `SC_ACTOR` (`GET /v1/tasks/<id>/transitions`).

## Rollback / abort

- An emergency stop is itself the safe state. To abort an accidental stop, release it (section G) and resume the interrupted tasks (G4). The work they lost is bounded to the running executions, and their branches keep any pushed commits.
- If A4 was used, restore the replicas and resume the HelmRelease before anything else. A suspended HelmRelease blocks all chart upgrades.

## Evidence to record

In the incident issue (Forgejo, label `incident/budget`):
- alert(s) and time;
- level, path(s) and actor;
- the outputs of B1–B3 and C1–C3, with no prompt content;
- cause classification;
- spend during the incident (`sum(budget_charge_micro_usd)` for the window);
- release time;
- the list of resumed and cancelled tasks.

`SCRunnerAdminAccess` events and `ledger.emergency_stop_events` rows are the system-side audit trail.

## Follow-ups

- Tune `policy/budgets.yaml` (limits, `anomaly.*`, `inputBytesPerTokenEstimate`) by PR if the incident showed a gap.
- Credential exposure → `procedures/rotate-model-credentials.md`. Pricing errors → `procedures/pricing-update.md`. Gateway defects → `procedures/gateway-failure.md`.
- Path 1 used → consider rotating `SC-RUNNER-ADMIN` (`procedures/rotate-secrets.md`).
