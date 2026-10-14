# 33 — Budget Enforcement and Emergency Stop

**Status:** Stage-0 plan (draft)
**DAG nodes:** C06 (primary, with plan 42 for notification delivery); holds GATE-C10 items for checks that need worker Jobs, evidence or Hermes. Supplies the reservation semantics exercised at C05 (plans 53/32).
**Related plans:** [53 model gateway](53-model-gateway.md), [32 cost accounting](32-cost-accounting.md), [31 model routing](31-model-routing.md), [26 agent runner](26-agent-runner.md), 24 (Hermes), 28 (harnesses, fake scenarios), 29 (state machine, retry classes), 35 (secrets), 36 (network policy), 18/54 (Flux, chart), 38/39/42 (metrics, dashboards, alerts), 43 (retention), 45 (recovery overlay), 50 (bootstrap preflight)
**Contracts:** owns `contracts/budget-policy.schema.yaml`; consumes `gateway-token.schema.yaml` (53), `cost-record.schema.yaml` (32), `model-routing.schema.yaml` (31), `runner-api.openapi.yaml` and `mcp-surface.schema.yaml` (26), `alert-catalog.yaml` and `notification.schema.yaml` (42), `resource-profile.schema.yaml` (26)
**ADRs:** relies on ADR-0004, ADR-0009, ADR-0017, ADR-0450 (reservations, micro-USD), ADR-0451 (token registry), ADR-0554 (recovery keeps workers paused); introduces **ADR-0452** (three-source emergency stop with a Kubernetes fallback outside Flux)

> Every section below is mandatory (SPEC §121). Requirements carry SPEC trace tags.

## 1. Purpose

Turn the cost ledger into **prevention** [SPEC §81]. This plan defines:

- the limits (per request, execution, task, attempts, tokens, wall time, daily and monthly per provider, global hourly/daily/monthly, Hermes orchestration);
- where each limit is enforced and how enforcement stays exact under concurrency;
- what happens, deterministically, when a limit is exhausted;
- how humans are notified at 50 %, 80 % and 100 %;
- how anomalous spend is detected;
- the **emergency stop**: a way to stop new autonomous spending within seconds, through three independent activation paths, without stopping core services.

The Model Gateway (plan 53) executes the per-request part. The Agent Runner (plan 26) executes the dispatch-time part. Alertmanager (ADR-0009) delivers notifications.

## 2. Requirements derived from SPEC.md

1. Policies support: maximum cost per provider request, execution cost, task cost, attempts, tokens, wall time; daily provider budget; monthly provider budget; global monthly budget; emergency stop. [SPEC §81]
2. Budget exhaustion fails safely and produces a visible human notification. [SPEC §81, §97]
3. New autonomous spending can be stopped without shutting down core services. [SPEC §81]
4. Budget limits are version-controlled policy. [SPEC §4.7]
5. Retries are bounded, and exhaustion leads to an explicit state (`Needs Human`). [SPEC §4.8, §74]
6. Alerts exist for budget thresholds and anomalous API spend. [SPEC §96]
7. Notifications include `Needs Human` and budget stop. [SPEC §97]
8. Hermes is not a policy enforcement point. [SPEC §4.6; ADR-0003]
9. Budget controls are part of MVP and the Self-Hosting Threshold. [SPEC §117, §126]
10. Budget behavior is testable deterministically without paid model calls. [SPEC §104]

Derived requirements:

- **33-R-1** Every limit expressed in money or tokens is enforced **before** a request is forwarded, by an atomic reservation (ADR-0450). No two concurrent requests can both be admitted against the same headroom.
- **33-R-2** Unknown or unpriced usage is charged at its reserved worst case (L-3). A budget is never enforced against a blind floor.
- **33-R-3** Each exhaustion produces exactly one deterministic outcome per §14.5: a Gateway code, a Runner code, an execution failure class `budget` with a specific failure code, a task state (`NEEDS_HUMAN` or `BLOCKED`), and a notification.
- **33-R-4** The emergency stop has three sources (DB via the Runner admin API, Git policy, Kubernetes ConfigMap). The effective level is the **maximum** of all sources. Any single source can stop spending. Releasing requires clearing every source that is set.
- **33-R-5** The Kubernetes source works with the Runner and `sc-pg` down, and is never owned, pruned or overwritten by Flux.
- **33-R-6** An emergency stop takes effect for new requests within 5 s of becoming visible to the Gateway and cuts in-flight streams within 5 s. Running worker Jobs are terminated within 90 s. Hermes, the Runner's read APIs, Forgejo, Woodpecker, Flux, CNPG, monitoring and backups keep running.
- **33-R-7** Lifting an emergency stop or a budget exhaustion never resumes stopped tasks automatically. A human resumes them (L-4: `Blocked → *` is human-only).
- **33-R-8** Budget accounting survives Gateway restarts and rolling upgrades (all state is in `sc-pg`). Losing `sc-pg` means no spend (fail closed).

## 3. Inputs

| Input | Source | Delivery |
|---|---|---|
| `BudgetPolicy` | `policy/budgets.yaml` (this plan; schema `budget-policy.schema.yaml`) | Flux-generated policy ConfigMap (D-17), loaded by the Gateway and the Runner with F04 |
| `ModelRouting` providers, `PricingTable`s | plans 31, 32 | same |
| `ResourceProfiles` (`deadline`) | plan 26 | same |
| Gateway token claims (`budget_scopes`, `token_kind`, `max_concurrent_requests`) | plan 53 / ADR-0451 | per request |
| Emergency-stop DB source | `ledger.emergency_stop_events` and `ledger.emergency_stop_state`, written by the Runner admin API | `sc-pg` |
| Emergency-stop Kubernetes source | ConfigMap `strange-company/sc-emergency-stop`, created by an operator, never in Git | Kubernetes API (Gateway GET every 5 s; Runner informer) |
| Attempt counts, task state | Runner tables (plans 26, 29) | in-process |
| Cost adjustments after PITR | `ledger.cost_adjustments` (plan 32 §20) | `sc-pg` |

## 4. Outputs

- `ledger.budget_scopes`, `ledger.reservations`, `ledger.denials`, `ledger.emergency_stop_events`, `ledger.emergency_stop_state`, `ledger.budget_limit_overrides` (DDL owned here, applied by the Runner's in-process, forward-only migrations at startup; CD-04. The Gateway never migrates.)
- Gateway denials with deterministic codes (plan 53 §7.3). Runner dispatch refusals (`BUDGET_EXCEEDED`, `EMERGENCY_STOP`, `ATTEMPTS_EXHAUSTED`). Execution failure codes and task transitions (§14.5).
- Prometheus metrics `sc_budget_*`, `sc_emergency_stop_*` (§21) and alert definitions (IDs for plan 42).
- Runner-posted `SCTaskNeedsHuman` notifications with reason `budget_exceeded` (plan 26, `notification.schema.yaml`).
- The procedure `procedures/budget-emergency-stop.md`.

## 5. Dependencies

**Hard (DAG):** C06 depends on C05 (Gateway, ledger, reservation path) and P09 (Prometheus, Alertmanager, test receiver).

**Soft / runtime:**
- Plan 26: admin API (`PUT /v1/admin/emergency-stop`, the new `PUT /v1/admin/budget-scopes/{scope_key}`), dispatch pre-check, cancellation, scope-row upserts, ConfigMap informer.
- Plan 29: task states `NEEDS_HUMAN`, `BLOCKED` with a stored `resume_state`; the per-class retry policy (class `budget` gets 0 automatic retries).
- Plan 36: Gateway egress to the kube-apiserver (for the ConfigMap GET).
- Plan 18/54: CI check that no rendered manifest contains ConfigMap `sc-emergency-stop`.
- Plan 42: rendering the rules of §21 and delivering notifications. Plan 38: Prometheus retention ≥ 8 days (anomaly baseline).
- Plan 45: the recovery commit sets the Git e-stop source (§20).

## 6. Owned state

| State | Location | Writers | Notes |
|---|---|---|---|
| Budget policy | Git `policy/budgets.yaml` | humans via PR (`policy/**` is protected, ADR-0008) | desired state |
| Budget scopes (limits, spent, reserved, counts) | `sc-pg` `ledger.budget_scopes` | Gateway (shared rows; spent/reserved columns), Runner (task/execution rows; limit columns) | column and row ownership enforced by grants plus trigger (§7.2) |
| Reservations | `ledger.reservations` | Gateway | `open → settled \| expired` |
| Denials | `ledger.denials` | Gateway (insert only) | 90-day retention (plan 43) |
| Emergency-stop DB source | `ledger.emergency_stop_events` (append-only), `ledger.emergency_stop_state` (single row, maintained by trigger) | Runner admin API | never deleted |
| Task budget overrides | `ledger.budget_limit_overrides` (append-only audit) | Runner admin API | never deleted |
| Emergency-stop Kubernetes source | ConfigMap `strange-company/sc-emergency-stop` | operator (cluster-admin) | deliberately **not** in Git (ADR-0452) |

## 7. External interfaces

### 7.1 Enforcement points

| Limit (policy key) | Enforced by | Where / when | Mechanism |
|---|---|---|---|
| `perRequest.maxCostUsd` | Gateway | before forward | worst-case estimate > cap → `SC_REQUEST_COST_CAP` |
| `perRequest.maxOutputTokens` | Gateway | before forward | requested cap > limit → `SC_MAX_TOKENS_EXCEEDS_POLICY`; absent cap injected (plan 53 §14.3) |
| `perRequest.maxRequestBodyBytes` | Gateway | read | `SC_BODY_TOO_LARGE` |
| `perExecution.maxCostUsd`, `maxTotalTokens`, `maxRequests` | Gateway | reservation | `execution:<id>` scope row (limits set by the Runner at dispatch) |
| `perExecution.maxConcurrentRequests` | Gateway | admission | token claim `max_concurrent_requests`; per-replica slot + open-reservation count per jti |
| `perExecution.maxWallTimeSeconds` | Kubernetes (Job) | runtime | Runner sets `activeDeadlineSeconds = min(profile.deadline, maxWallTimeSeconds)` (§14.9); token `exp` = deadline + 300 s |
| `perTask.maxCostUsd` | Gateway (per request) and Runner (dispatch) | reservation and dispatch | `task:<id>` scope row |
| `perTask.maxAttempts` | Runner | dispatch | count of the task's executions (§14.9) → `ATTEMPTS_EXHAUSTED` |
| `providers.<p>.dailyUsd` / `monthlyUsd` | Gateway (and the Runner/routing pre-check) | reservation and dispatch | `provider-day:<p>:<YYYY-MM-DD>`, `provider-month:<p>:<YYYY-MM>` |
| `global.hourlyUsd` / `dailyUsd` / `monthlyUsd` | Gateway (and the Runner pre-check) | reservation and dispatch | `global-hour:<YYYY-MM-DDTHH>`, `global-day:<YYYY-MM-DD>`, `global-month:<YYYY-MM>` |
| `orchestration.dailyUsd` / `monthlyUsd` | Gateway | reservation | `orchestration-day:<date>`, `orchestration-month:<YYYY-MM>` (orchestrator tokens only) |
| `dispatch.minimumHeadroomUsd.<tier>` | Runner (and routing filter 10, plan 31) | dispatch and dispatcher re-check | every applicable scope must have `limit − spent − reserved ≥ minimum[tier_effective]` |
| `thresholds.warnPercents` | Gateway leader → Prometheus → Alertmanager | every 30 s | §14.4 |
| `anomaly.*` | Prometheus rule (rendered from policy) | every 1 min | §14.8 |
| `emergencyStop.level` (Git source) | Gateway and Runner | continuous | §14.6 |

All calendar periods are UTC (schema constant). A request belongs to the periods containing its Gateway `started_at`. Settlement charges the same scope keys even if the request ends in the next period.

### 7.2 Budget scopes (`ledger.budget_scopes`)

Scope keys (identical to `cost-record.schema.yaml` `budget_scope_keys` pattern):

| Kind | Key | Row created by | Limit source |
|---|---|---|---|
| `execution` | `execution:sc-184-x3` | Runner at dispatch (before the Job exists; plan 53 §8) | `perExecution.*` at dispatch |
| `task` | `task:sc-184` | Runner at first dispatch; limit refreshed at every dispatch the latest human override (§14.11), else `repositories.<repo>.perTask.maxCostUsd` (§9.4), else `perTask.maxCostUsd` |
| `provider_day` / `provider_month` | `provider-day:anthropic:2026-10-13` / `provider-month:anthropic:2026-10` | Gateway, lazily (`INSERT … ON CONFLICT DO NOTHING`) | `providers.<p>.*` of the policy loaded by the replica |
| `global_hour` / `global_day` / `global_month` | `global-hour:2026-10-13T10` / `global-day:2026-10-13` / `global-month:2026-10` | Gateway, lazily | `global.*` |
| `orchestration_day` / `orchestration_month` | `orchestration-day:2026-10-13` / `orchestration-month:2026-10` | Gateway, lazily | `orchestration.*` |

Scopes per token kind:
- `execution` tokens: execution, task, provider-day, provider-month, global-hour, global-day, global-month (7 keys);
- `orchestrator` tokens: orchestration-day, orchestration-month, provider-day, provider-month, global-hour, global-day, global-month.

Global scopes therefore include Hermes spend. Verify-only tokens (task `sc-0`, plan 53 §14.5a) use the execution scope plus provider and global scopes, with no `task:` scope.

DDL sketch (owned here; applied by the Runner's in-process migrations under its advisory lock, CD-04):

```sql
CREATE TYPE ledger.scope_kind AS ENUM ('execution','task','provider_day','provider_month',
  'global_hour','global_day','global_month','orchestration_day','orchestration_month');

CREATE TABLE ledger.budget_scopes (
  scope_key          text PRIMARY KEY,
  scope_kind         ledger.scope_kind NOT NULL,
  provider           text,
  period_start       timestamptz,            -- null for execution/task
  period_end         timestamptz,
  limit_micro_usd    bigint NOT NULL CHECK (limit_micro_usd >= 0),
  limit_tokens       bigint,                 -- execution only
  limit_requests     integer,                -- execution only
  limit_source       text NOT NULL,          -- 'policy:<commit>' | 'override:<uuid>'
  spent_micro_usd    bigint NOT NULL DEFAULT 0,
  reserved_micro_usd bigint NOT NULL DEFAULT 0 CHECK (reserved_micro_usd >= 0),
  spent_tokens       bigint NOT NULL DEFAULT 0,
  reserved_tokens    bigint NOT NULL DEFAULT 0 CHECK (reserved_tokens >= 0),
  request_count      integer NOT NULL DEFAULT 0,
  incomplete_rows    integer NOT NULL DEFAULT 0,   -- unknown/estimated/unpriced charges
  last_denied_at     timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON ledger.budget_scopes (scope_kind, period_start);
```

Grants:
- `sc_gateway`: SELECT, INSERT (shared kinds only, enforced by trigger `budget_scopes_insert_guard`), UPDATE of `spent_*`, `reserved_*`, `request_count`, `incomplete_rows`, `last_denied_at`, `updated_at`; UPDATE of `limit_micro_usd`, `limit_source` on shared kinds only (trigger `budget_scopes_limit_guard` checks `current_user` and `scope_kind`).
- `runner_app`: SELECT, INSERT and UPDATE of `limit_*` on `execution` and `task` rows only (same triggers).
- `sc_reporting`: no access (aggregates come from plan 32 views and metrics).

### 7.3 Error and failure codes

| Layer | Code | Meaning |
|---|---|---|
| Gateway (HTTP 403, plan 53 §7.3) | `SC_BUDGET_EXHAUSTED` | some scope lacks headroom for cost, tokens or requests. `sc_error.scope` = scope key, `sc_error.dimension` ∈ `cost`, `tokens`, `requests` |
| Gateway (HTTP 429) | `SC_BUDGET_CONTENDED` | headroom on settled spend exists but is held by open reservations after the bounded wait (§14.2; dimension `cost_reserved`). Transient, retried by SDKs, never a budget-class failure |
| Gateway | `SC_BUDGET_UNDEFINED` | a required `task`/`execution` row is missing, or the provider has no entry in `BudgetPolicy.providers` |
| Gateway | `SC_REQUEST_COST_CAP` | worst case > `perRequest.maxCostUsd` |
| Gateway | `SC_EMERGENCY_STOP` | effective level covers the token kind |
| Runner (`agent_start`, MCP/REST) | `BUDGET_EXCEEDED` (402) | dispatch pre-check failed. `details: {scope_key, scope_kind, limit_micro_usd, spent_micro_usd, reserved_micro_usd, required_headroom_micro_usd}` |
| Runner | `EMERGENCY_STOP` (503) | effective level ≥ `workers`. `details: {level, sources: {db, git, k8s, degraded}}` |
| Runner | `ATTEMPTS_EXHAUSTED` (409) | attempts ≥ `min(perTask.maxAttempts, plan 29 limit)`, or the escalation ladder is exhausted (plan 31) |
| Execution `failure_code` (class `infrastructure`) | `BUDGET_CONTENDED` | contention surfaced to the worker after SDK retries |
| Execution `failure_code` (class `budget`) | `BUDGET_EXECUTION_EXHAUSTED`, `BUDGET_TASK_EXHAUSTED`, `BUDGET_SHARED_EXHAUSTED`, `BUDGET_REQUEST_CAP`, `BUDGET_UNDEFINED`, `EMERGENCY_STOP` | derived by the Runner from the execution's **last `ledger.denials` row** (never from the worker's claim). The worker-result reasons `budget_denied`/`emergency_stop` only trigger the lookup |

### 7.4 Emergency-stop interfaces

| Path | Interface | Activation | Reversal | Latency to effect (new requests) |
|---|---|---|---|---|
| 1. Runner admin API (primary) | `PUT /v1/admin/emergency-stop` on the loopback admin listener (`runner-api.openapi.yaml`), bearer `SC-RUNNER-ADMIN` `scadm_<kid>.<secret>` (per-operator, actor `human:<login>` derived from the key id, or break-glass `breakglass:runner-admin`; CD-30) + `X-SC-Reason` + `Idempotency-Key` | body `{"level":"workers"\|"all","reason":"…"}` | body `{"level":"none","reason":"…"}` | immediate at the next reservation (the DB is read inside the transaction); in-flight cut ≤ 2 s (DB poll) |
| 2. Git policy (planned, durable) | `policy/budgets.yaml` `spec.emergencyStop: {level, reason}` via PR → CI → human merge → Flux | PR setting `workers`/`all` (reason required by schema) | PR setting `none` | Flux interval + kubelet ConfigMap volume sync + 10 s reload; target ≤ 15 min after merge (verify kubelet sync on the pinned k3s) |
| 3. Kubernetes fallback (works without Runner and DB) | ConfigMap `strange-company/sc-emergency-stop`, `data.level` ∈ {`none`,`workers`,`all`}, `data.reason`; annotations `strange.company/set-by`, `strange.company/set-at` | `kubectl create`/`apply` (cluster-admin) | `kubectl delete configmap sc-emergency-stop` (or `level: none`) | ≤ 5 s (Gateway poll) |

`GET /v1/admin/emergency-stop` and the Gateway `GET :9090/sc/v1/status` both report `{effective_level, sources: {db, git, k8s: {level, last_read_ok_at}, degraded}}`.

**Interface changes requested from plan 26** (its open question 1 defers to this plan):
- The `EmergencyStopLevel` enum is `none | workers | all`. There is no `dispatch` level: a dispatch-only pause is the separate Runner config `runner.dispatchPaused` used by ADR-0554.
- `EmergencyStopState` gains `effective_level` and `sources`.
- The Runner does **not** forward the stop to a Gateway admin endpoint. The Gateway reads `ledger.emergency_stop_state` directly. No Gateway admin port (`:8091`) exists. `gateway_applied` is replaced by `gateway_effective_level`, read from the Gateway status endpoint, which plan 36 must open from the Runner to Gateway `:9090`.
- The authoritative DB source is `ledger.emergency_stop_events`/`_state` (this plan). Plan 26's `emergency_stop` table becomes a view over them.

### 7.5 Notifications (all through Alertmanager, ADR-0009)

| Condition | Alert (catalog name) | Severity / class | `always_notify` |
|---|---|---|---|
| shared scope at a `warnPercents` threshold (50 %, 80 %) | `SCBudgetThresholdReached` | warning / notify | none |
| shared scope exhausted (100 % or a budget denial in the period); excludes `global_hour` | `SCBudgetExhausted` | critical / page | `budget-stop` |
| `global_hour` cap reached | `SCBudgetHourlyCapReached` (new) | warning / notify | none |
| dispatch refused for shared-scope headroom for 15 m | `SCBudgetDispatchRefused` (new, review COST-7) | warning / notify | none |
| task-attributable exhaustion → task `NEEDS_HUMAN` | `SCTaskNeedsHuman` (Runner-posted, `reason: budget_exceeded`) | warning / notify | `needs-human` |
| emergency stop effective ≥ `workers` | `SCEmergencyStopActive` | warning / notify | `budget-stop` |
| Runner and Gateway effective levels disagree | `SCEmergencyStopNotApplied` (new) | critical / page | `budget-stop` |
| anomalous spend | `SCAnomalousSpend` (assignment ID `AnomalousModelSpend`) | critical / page | `budget-stop` |
| incomplete cost inside a budget period | `SCBudgetCostIncomplete` | warning / notify | none |
| budget gauges not refreshed | `SCBudgetMetricsStale` (new) | warning / notify | none |

## 8. Internal interfaces

### 8.1 Tables (DDL sketches; owned here)

```sql
CREATE TABLE ledger.reservations (
  id uuid PRIMARY KEY, gateway_request_id text UNIQUE NOT NULL, token_jti uuid NOT NULL,
  token_kind text NOT NULL, execution_id text, task_id text,
  scope_keys text[] NOT NULL, amount_micro_usd bigint NOT NULL, tokens bigint NOT NULL,
  state text NOT NULL CHECK (state IN ('open','settled','expired')),
  charge_micro_usd bigint, created_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
  settled_at timestamptz);
CREATE INDEX ON ledger.reservations (token_jti) WHERE state = 'open';
CREATE INDEX ON ledger.reservations (expires_at) WHERE state = 'open';

CREATE TABLE ledger.denials (
  id bigserial PRIMARY KEY, at timestamptz NOT NULL, gateway_request_id text NOT NULL,
  token_jti uuid, token_kind text, execution_id text, task_id text,
  provider text, model text, code text NOT NULL, scope_key text, dimension text,
  detail jsonb NOT NULL);  -- {limit, spent, reserved, requested}; never content
CREATE INDEX ON ledger.denials (execution_id, at DESC);

CREATE TABLE ledger.emergency_stop_events (
  id bigserial PRIMARY KEY, level text NOT NULL CHECK (level IN ('none','workers','all')),
  reason text NOT NULL, actor text NOT NULL, idempotency_key text UNIQUE, at timestamptz NOT NULL DEFAULT now());
CREATE TABLE ledger.emergency_stop_state (
  id smallint PRIMARY KEY CHECK (id = 1), level text NOT NULL, event_id bigint NOT NULL, at timestamptz NOT NULL);
-- trigger: AFTER INSERT ON emergency_stop_events → UPSERT emergency_stop_state(1, NEW.level, NEW.id, NEW.at)
-- migration seeds event (level 'none', actor 'migration') so the state row always exists

CREATE TABLE ledger.budget_limit_overrides (
  id uuid PRIMARY KEY, scope_key text NOT NULL, old_limit_micro_usd bigint NOT NULL,
  new_limit_micro_usd bigint NOT NULL, reason text NOT NULL, actor text NOT NULL, at timestamptz NOT NULL);
```

Grants: the Gateway has SELECT on `emergency_stop_state`, `emergency_stop_events`, `gateway_tokens`, `budget_limit_overrides`, and INSERT on `reservations`, `denials`. The Runner has INSERT on `emergency_stop_events` and `budget_limit_overrides`, and SELECT on all of them.

### 8.2 Runner ↔ Gateway contract

There is no RPC. Coordination happens only through `sc-pg` (plan 53 §8) and Kubernetes:
1. The Runner inserts the token row and upserts the `task:`/`execution:` scope rows **before** creating the Job.
2. The Runner revokes the token at every terminal state, at cancellation, and at an emergency stop.
3. The Runner writes the DB e-stop source. The Gateway reads it.
4. The Gateway writes spent/reserved and denials. The Runner reads them for the pre-check and for failure classification.

## 9. Configuration

### 9.1 Shipped `policy/budgets.yaml` (conservative defaults; human PR to change)

```yaml
apiVersion: strange.company/v1alpha1
kind: BudgetPolicy
metadata: {name: default}
spec:
  currency: USD
  calendarTimezone: UTC
  perRequest: {maxCostUsd: "3.00", maxOutputTokens: 32000, maxRequestBodyBytes: 33554432}
  perExecution: {maxCostUsd: "8.00", maxTotalTokens: 10000000, maxRequests: 1000, maxWallTimeSeconds: 3600, maxConcurrentRequests: 4}
  perTask: {maxCostUsd: "20.00", maxAttempts: 15}
  providers:
    anthropic: {dailyUsd: "30.00", monthlyUsd: "250.00"}
    openai:    {dailyUsd: "30.00", monthlyUsd: "250.00"}
    deepseek:  {dailyUsd: "10.00", monthlyUsd: "80.00"}
    zai:       {dailyUsd: "10.00", monthlyUsd: "80.00"}
    fake:      {dailyUsd: "1000.00", monthlyUsd: "10000.00"}
    fake-b:    {dailyUsd: "1000.00", monthlyUsd: "10000.00"}
  global: {hourlyUsd: "20.00", dailyUsd: "60.00", monthlyUsd: "300.00"}
  orchestration: {dailyUsd: "10.00", monthlyUsd: "120.00"}
  thresholds: {warnPercents: [50, 80]}
  dispatch: {minimumHeadroomUsd: {clerical: "0.50", bargain: "0.50", standard: "1.50", frontier: "3.00"}}
  anomaly: {windowMinutes: 15, baselineDays: 7, baselineQuantile: 0.95, multiplier: 4, minimumSpendUsd: "0.50", absoluteWindowCapUsd: "8.00"}
  repositories:
    sc-e2e/fake-target: {perTask: {maxCostUsd: "5.00"}}
  emergencyStop: {level: none}
```

Rationale:
- **Sized against the provable worst case** (plan 53 §14.1 step 6; CD-28). The input bound is capped at the model window, so a large request's worst case is the full-window cost. With illustrative rates (balanced class $3/$15 per M with 5-minute cache write $3.75; largest class $5/$25, write $6.25; small class $1/$5), a request at a full 200 k-token window with the 32 k output cap reserves at most ≈ $0.41 (small), ≈ $1.23 (balanced) and ≈ $2.05 (largest). `perRequest.maxCostUsd: 3.00` and the per-tier `minimumHeadroomUsd` values cover these, and invariant 8 (§9.2) re-checks the real price cards at load, so no full-context request is denied `SC_REQUEST_COST_CAP`. A typical Claude Code request (system prompt and tools ≈ 100 KB, i.e. ≤ ≈ 100 k tokens by the bound) reserves ≈ $0.86 on the balanced class, of which the 32 k output cap is $0.48.
- `maxConcurrentRequests: 4` keeps one execution's peak reservations (4 × ≤ $2.05 = $8.20) within `perExecution.maxCostUsd: 8.00` for all but four concurrent full-window largest-class requests (which then wait, §14.2), and below `global.hourlyUsd: 20.00`; two workers plus Hermes may briefly contend, which §14.2 handles as transient.
- `maxTotalTokens` excludes cache reads (contract description), so a long Claude Code session that re-reads a cached 80 k-token context per turn is not cut off by tokens that cost a tenth of input.
- **Orchestration** is raised to $10/day and $120/month, and plan 31 ships `orchestrator.maxConcurrentRequests: 4`: with worst-case reservations of ≈ $0.9 per Hermes turn (≈ 30 k-token conversation plus the 32 k output cap), a $5 daily scope would start refusing Hermes once ≈ $4 had actually been spent. Actual Hermes spend is typically a few cents per turn.
- `perTask.maxAttempts: 15` ≥ 3 (tests ladder) + 5 (implementation ladder) + 3 (plan 29 review limit) + 2 (consecutive infrastructure requeues) + 2 margin; invariant 9 (§9.2) enforces the sum (review COST-8).
- The global monthly cap is the binding limit. Provider monthly limits may sum above it.
- Fake providers cost nothing real, but they are still budgeted so the code paths are identical.

### 9.1a Per-repository task-budget overrides (`spec.repositories`)

`BudgetPolicy.spec.repositories` maps a managed repository (`owner/name`) to task-level overrides `perTask.maxCostUsd` and/or `perTask.maxAttempts`. They live in platform policy (Git, human-reviewed), so they may raise or lower the default for that repository. Repository-side policy (`.strange-company/`) cannot change budgets (ADR-0017). The shipped entry is `sc-e2e/fake-target` (the C10/plan 46 fixture repository): a $5.00 task limit, the single R01 smoke budget number (plan 28 §14.3 must be raised from $2.00 to match, CD-28): under the provable worst case each balanced-class Claude Code request reserves ≈ $0.86, so a $2.00 task scope would deny requests once ≈ $1.1 had been spent. At fake rates it is ample for C10, and it caps the R01 real-model smoke. The C10 over-budget scenario lowers it to $0.01 in a test overlay (plan 28), never in the shipped file. Precedence for a task's limit: human override (§14.11) > repository override > `perTask`. Invariants: each override `maxCostUsd ≤ global.monthlyUsd`, and the key names a repository listed in the Runner's `forgejo.managedRepositories` (warning only, because test repositories may be absent in some installations).

### 9.2 Load-time invariants (F04; schema description lists the first four)

1. `warnPercents` strictly ascending.
2. `perRequest.maxCostUsd ≤ perExecution.maxCostUsd ≤ perTask.maxCostUsd`.
3. Every `providers` key names a provider in `ModelRouting`.
4. Every `dispatch.minimumHeadroomUsd` value is `≤ perExecution.maxCostUsd`.
5. (added) `global.hourlyUsd ≤ global.dailyUsd ≤ global.monthlyUsd` and `orchestration.dailyUsd ≤ orchestration.monthlyUsd`.
6. (added) `anomaly.absoluteWindowCapUsd ≤ global.hourlyUsd`. A higher cap could never fire before the hourly limit stops spend.
7. (added) `perExecution.maxWallTimeSeconds ≥` the smallest profile deadline in every profile set (otherwise some profile could never run to its deadline). This is a warning, not a rejection.
8. (added, review COST-2, CD-28) For every enabled `stage: production` model that appears in a tier used by any role or by the orchestrator: `worst(model)` = plan 53's provable worst case with `in_bound = contextWindowTokens` and `out_bound = min(perRequest.maxOutputTokens, model.maxOutputTokens)` (plus `maxReasoningTokens` when the cap does not cover reasoning), using the model's current priced entry. Require `worst(model) ≤ perRequest.maxCostUsd` and `worst(model) ≤ dispatch.minimumHeadroomUsd[tier]` for each tier listing it. A violation rejects the policy set and names the model and both amounts. An operator either raises the limits or routes a smaller-window model id.
9. (added, review COST-8) `perTask.maxAttempts` and every `repositories.*.perTask.maxAttempts` ≥ Σ `meritFailuresBeforeEscalation` of the `tests` ladder + Σ of the `implementation` ladder + plan 29's `VerificationPolicy.spec.retry` review limit + its consecutive-infrastructure limit.
10. (added, warning) `global.hourlyUsd ≥ 2 × perExecution.maxConcurrentRequests × max worst(model)`. Below that, a single execution can routinely contend on the hourly scope.

A violation rejects the whole policy set. Last-known-good is kept and `SCGatewayPolicyInvalid` / `SCRunnerPolicyInvalid` fire. With no valid policy ever loaded, the Gateway is not ready, so nothing is spent.

### 9.3 Runtime knobs (chart values, plan 54)

| Key | Default | Notes |
|---|---|---|
| `modelGateway.estop.configMapName` | `sc-emergency-stop` | fixed name; the RBAC `resourceNames` match it |
| `modelGateway.estop.pollInterval` | `5s` | Kubernetes source |
| `modelGateway.estop.dbPollInterval` | `2s` | DB source poll for in-flight cuts (new requests read it transactionally) |
| `modelGateway.estop.staleAfter` | `60s` | Kubernetes source unreadable → degraded level `workers` |
| `modelGateway.budgetMetrics.interval` | `30s` | leader export of §21 gauges |
| `agentRunner.estop.cancelGraceSeconds` | `60` | equals plan 26 `cancel.graceSeconds` |

Bootstrap preflight (plan 50) prints `global.monthlyUsd`, `global.dailyUsd` and the provider limits next to the infrastructure cost estimate, and requires the same confirmation as the paid footprint (SPEC §14 analogue for model spend; request to plan 50).

## 10. Secrets

| Class | Use |
|---|---|
| `SC-RUNNER-ADMIN` (plan 26, CD-30) | bearer `scadm_<kid>.<secret>` for path 1 and the other admin calls (`PUT /v1/admin/emergency-stop`, `PUT /v1/admin/budget-scopes/{key}`, `POST /v1/admin/ledger/adjustments`, `POST /v1/admin/model-quarantine/…:acknowledge`). Per-operator credentials carry the audit identity; the break-glass instance (actor `breakglass:runner-admin`) is in the recovery bundle |
| `SC-K3S-ADMIN-KUBECONFIG` / `SC-BREAK-GLASS-CLUSTER` | port-forward for path 1, and create/delete of the path-3 ConfigMap |
| `SC-PG-APP` (`sc_gateway`, `runner_app`) | DB access per §7.2 grants |

No new secret class is introduced. Neither the ConfigMap nor the policy file contains secrets.

## 11. Storage

All in `sc-pg` (plan 12):
- `budget_scopes`: about 7 rows per execution day plus 2 per execution and 1 per task, roughly 25 k rows/year.
- `reservations`: one per request, about 1.5 M rows/year at 4 000/day. Settled rows older than 30 days are deleted by plan 43's job, because the cost record is the durable record.
- `denials`: kept for 90 days.
- `emergency_stop_events` and `budget_limit_overrides`: tiny, kept forever.

Scope rows of `execution`/`task` kind are retained with their task (plan 43). Shared-period rows are kept 400 days for reporting.

## 12. Resource requirements

No additional pods.
- Gateway: one extra K8s GET every 5 s and one single-row SELECT every 2 s per replica. The reservation transaction locks ≤ 7 rows. Measured budget: p99 ≤ 10 ms of the 50 ms pre-upstream overhead SLO (plan 53 §14.6).
- Runner: one ConfigMap informer and two indexed SELECTs per dispatch.
- Prometheus: two recording rules evaluated each minute. The anomaly baseline over 7 days at 1-minute resolution is ≈ 10 k samples per evaluation.

## 13. Security boundary

- **Who can change limits:** only Git PRs merged by humans (`policy/**` is protected; agents cannot propose effective changes to it). The single runtime exception is the per-task override (§14.11), which needs `SC-RUNNER-ADMIN` + cluster port-forward, is audited and raises `SCRunnerAdminAccess`.
- **Who can engage or lift the emergency stop:** holders of `SC-RUNNER-ADMIN` with port-forward rights (path 1); human PR mergers (path 2); cluster-admin (path 3). Hermes has no MCP tool to read or write it beyond the `EMERGENCY_STOP` error. Workers have no route to the Runner admin listener (loopback) and no Kubernetes API access. A prompt-injected Hermes therefore cannot lift a stop.
- **RBAC for path 3:** no ServiceAccount except Flux controllers and cluster-admin may write ConfigMaps in `strange-company`.
  - The Gateway SA has `get` on `configmaps` with `resourceNames: [sc-emergency-stop]`.
  - The Runner SA has `get`, `list`, `watch` with the same `resourceNames` (a list/watch with `fieldSelector=metadata.name=sc-emergency-stop`).
  - Kubernetes `resourceNames` cannot restrict `create`. Path 3 is therefore deliberately a cluster-admin action.
- **Flux exclusion:** kustomize-controller prunes only objects in its own inventory, and helm-controller manages only chart-rendered objects (verify on the pinned Flux; 33-AC-12). A CI rule (`make lint-gitops`, plans 18/54) fails if any rendered manifest names a ConfigMap `sc-emergency-stop`, so Flux can never adopt it.
- **Fail-safe direction:** an unparseable `data.level` is treated as `all` and alerted. An unreadable Kubernetes source for 60 s raises the effective level to `workers`. An unreadable DB means no spend at all. An invalid Git policy keeps the last-known-good level.
- **Denial responses** use HTTP 403 so SDKs do not auto-retry them (plan 53 §7.3).

## 14. Happy path

### 14.1 Dispatch-time checks (Runner; plan 26 §14.2 step 9 and dispatcher step 1)

After routing has produced `tier_effective` and the provider (plan 31):

1. Effective e-stop level ≥ `workers` → `EMERGENCY_STOP` (checked first, plan 26 step 2).
2. Attempts: `n = count(executions of the task that reached STARTING and are not refunded)`. Refunded = plan 29's refund rule (`executions.refunded = true`: class infrastructure, or code `EMERGENCY_STOP` / `BUDGET_SHARED_EXHAUSTED`; review COST-8). If `n ≥ min(maxAttempts, plan 29 limit)` → `ATTEMPTS_EXHAUSTED`, where `maxAttempts` = `repositories.<repo>.perTask.maxAttempts` if set (§9.4), else `perTask.maxAttempts`.
3. Headroom with `need = minimumHeadroomUsd[tier_effective]`. For each of `task:<id>` (limit = human override, else the repository override of §9.4, else `perTask.maxCostUsd`), `provider-day`, `provider-month`, `global-hour`, `global-day`, `global-month`: `limit − spent − reserved ≥ need`. A missing shared row means `spent = reserved = 0`. The first failing scope → `BUDGET_EXCEEDED` with details.
4. On success, in the dispatch transaction:

   ```sql
   INSERT INTO ledger.budget_scopes(scope_key, scope_kind, limit_micro_usd, limit_source)
     VALUES ('task:'||$t, 'task', $task_limit, $src) ON CONFLICT (scope_key) DO UPDATE SET limit_micro_usd = EXCLUDED.limit_micro_usd, limit_source = EXCLUDED.limit_source;
   INSERT INTO ledger.budget_scopes(scope_key, scope_kind, limit_micro_usd, limit_tokens, limit_requests, limit_source)
     VALUES ('execution:'||$x, 'execution', $exec_cost, $exec_tokens, $exec_requests, $src);
   ```

   Then the token row is inserted (plan 53 §8).

The pre-check is advisory for efficiency: it avoids starting Jobs that cannot spend. The Gateway reservation remains authoritative. A race between the pre-check and the first request is harmless.

### 14.2 Reservation transaction (Gateway, per forwarded request)

```sql
BEGIN;                                              -- READ COMMITTED
SELECT revoked_at, claims_sha256 FROM ledger.gateway_tokens WHERE jti = $jti;      -- (a)
SELECT level FROM ledger.emergency_stop_state WHERE id = 1;                          -- (b)
INSERT INTO ledger.budget_scopes (scope_key, scope_kind, provider, period_start, period_end,
       limit_micro_usd, limit_source)
VALUES <one row per shared key with the replica's policy limit>
ON CONFLICT (scope_key) DO NOTHING;                                                  -- (c)
SELECT * FROM ledger.budget_scopes
 WHERE scope_key = ANY($keys) ORDER BY scope_key FOR UPDATE;                          -- (d)
SELECT count(*) FROM ledger.reservations WHERE token_jti = $jti AND state = 'open';   -- (e)
-- (f) checks in Go, first failure wins, then ROLLBACK and write the denial:
--   token row missing/revoked/digest≠         → SC_TOKEN_REVOKED / SC_TOKEN_INVALID
--   max(db, git, k8s, degraded) covers kind   → SC_EMERGENCY_STOP
--   task/execution row missing                → SC_BUDGET_UNDEFINED
--   (e) ≥ max_concurrent_requests             → SC_CONCURRENCY_LIMIT
--   for each scope in key order:
--     lim = shared ? policy_limit : row.limit_micro_usd
--     row.spent + row.reserved + amount > lim                          → SC_BUDGET_EXHAUSTED(cost)
--     execution: spent_tokens + reserved_tokens + worst_tok > limit_tokens → (tokens)
--     execution: request_count + 1 > limit_requests                     → (requests)
--   classification of a cost shortfall (review COST-3):
--     spent + amount > lim                    → SC_BUDGET_EXHAUSTED (dimension cost)
--     spent + amount <= lim < spent + reserved + amount → CONTENDED (dimension cost_reserved):
--         ROLLBACK, wait for NOTIFY sc_budget_settled on any of $keys (or 2 s), retry the
--         whole transaction; after reservation.contentionWaitSeconds (30 s) → 429 SC_BUDGET_CONTENDED
INSERT INTO ledger.reservations (...) VALUES (..., 'open', ..., now() + total + grace);   -- (g)
UPDATE ledger.budget_scopes
   SET reserved_micro_usd = reserved_micro_usd + $amount,
       reserved_tokens    = reserved_tokens + $worst_tok,
       request_count      = request_count + 1,
       limit_micro_usd    = CASE WHEN scope_kind IN (<shared kinds>) THEN $policy_limit[scope_key] ELSE limit_micro_usd END,
       updated_at = now()
 WHERE scope_key = ANY($keys);                                                       -- (h)
COMMIT;
```

- **Concurrency safety.**
  - Every request of a token locks its `execution:` (or `orchestration-day:`) row, so the open-reservation count (e) is exact across replicas.
  - Every request locks the global rows, so no two requests can be admitted against the same headroom (33-R-1).
  - Locks are always taken in ascending `scope_key` order, so there are no deadlocks.
  - Serialization or deadlock errors are retried ≤ 3 times (plan 53 §16). After that the request is denied `SC_LEDGER_UNAVAILABLE`.
- **Contention is not exhaustion.** Headroom held only by open reservations comes back when those requests settle, usually at a fraction of their reservation. The settlement transaction issues `NOTIFY sc_budget_settled, '<scope_key>'` for each released scope, so waiting requests re-check promptly. A request that is still contended after the wait gets 429 `SC_BUDGET_CONTENDED`, which SDKs retry; it never produces a budget-class failure or a human-only task state (§14.5).
- **Denial rows** are written in a separate autocommit statement after the rollback. `last_denied_at` is set on a scope **only for spent-based denials** (`SC_BUDGET_EXHAUSTED`), never for contention (`SC_BUDGET_CONTENDED`, dimension `cost_reserved`), so `sc_budget_exhausted` and `SCBudgetExhausted` do not fire while utilization is low.
- **Policy skew between replicas** during a rollout: each replica checks shared limits against its own loaded policy. For ≤ one reload interval (10 s) the two replicas may apply the old and the new limit. That is acceptable and recorded.

### 14.3 Settlement and the hard ceiling

Settlement (plan 53 §14.1 step 10) in one transaction:
1. `UPDATE reservations SET state='settled' … WHERE id=$id AND state='open'`. Zero rows → no-op.
2. Lock the same scope rows in key order.
3. `reserved −= amount`, `reserved_tokens −= worst_tok`, `spent += budget_charge`, `spent_tokens += actual_tokens`, `incomplete_rows += (usage_status ∈ {unknown, estimated} or pricing_status = unpriced ? 1 : 0)`.
4. Insert the cost record.

The sweeper's expiry path is identical, with `charge = amount`.

`budget_charge` (plan 32 / cost-record) is the priced actual, or the reservation if usage is unknown or the price is unpriced, or a worst-case completion if usage is estimated.

**The per-request ceiling is hard (CD-28, SPEC §81).** A request is admitted only if its **provable** worst-case cost (plan 53 §14.1 step 6) is ≤ `perRequest.maxCostUsd`, and the reservation equals that worst case. The bound is provable because:
- input tokens are bounded by the family's declared `inputBound` (≤ 1 token per UTF-8 byte for byte-level BPE families, plus per-message and per-request overheads and a documented per-image maximum), capped at the context window the provider enforces; content without a documented bound is denied;
- output is the provider-enforced output cap, which must also bound reasoning or be accompanied by a separately enforced reasoning cap (models without one cannot be routed);
- every rate is the highest that can apply (cache writes included), over every model in the token's allowlist;
- provider-side state, file ids, URL sources and window-enlarging betas are denied, so nothing outside the body can be billed.

Hence `charge ≤ amount` for every request whose provider honours its documented window, caps and price card, and no scope can overshoot: each scope admits only `spent + reserved + amount ≤ limit`, and each request's charge stays within its own reservation.

**Bound violation.** If reported usage nevertheless exceeds the computed bound (a tokenizer, window or cap assumption is false), the row records `bound_violation: true` with the real cost, the scopes are charged the real cost, `SCGatewayBoundViolation` pages, and the (provider, model) is quarantined until a human acknowledges it (plan 53 §14.4a). The worst possible overshoot is therefore the excess of the violating request plus the excess of that model's requests already in flight at that moment; it is an anomaly to investigate, never normal operation. 53-AC-37 and 33-AC-25 pin it.

### 14.4 Thresholds and notifications

Every `budgetMetrics.interval` (30 s) the Gateway leader reads the current-period shared rows and exports, per (`scope_kind`, `provider`):
- `sc_budget_limit_micro_usd`, `sc_budget_spent_micro_usd`, `sc_budget_reserved_micro_usd`;
- `sc_budget_utilization_ratio` = spent / limit (0 when limit = 0 and spent = 0; +Inf is never emitted: limit 0 with any spend exports 1);
- `sc_budget_threshold_reached{threshold="50"|"80"}` = 1 iff utilization ≥ threshold/100;
- `sc_budget_exhausted` = 1 iff spent ≥ limit, or `last_denied_at` lies in the current period;
- `sc_budget_cost_incomplete` = 1 iff the period has rows with `usage_status = unknown` or `pricing_status = unpriced` (`estimated` rows from client disconnects or e-stop cuts are excluded, because they are priced conservatively; review COST-15). The scope column `incomplete_rows` counts only those;
- `sc_budget_metrics_updated_timestamp_seconds`.

`provider` is `""` for global and orchestration scopes. The series count is ≤ 7 kinds × 8 providers.

Alert rules (plan 42 renders them; expressions are normative here):

```text
SCBudgetThresholdReached:  max by (scope_kind, provider, threshold) (sc_budget_threshold_reached{scope_kind!="global_hour"}) == 1
                           unless on (scope_kind, provider) (sc_budget_exhausted == 1)          for: 0s
SCBudgetExhausted:         max by (scope_kind, provider) (sc_budget_exhausted{scope_kind!="global_hour"}) == 1   for: 0s
SCBudgetHourlyCapReached:  max (sc_budget_exhausted{scope_kind="global_hour"}) == 1              for: 0s
SCBudgetCostIncomplete:    max by (scope_kind, provider) (sc_budget_cost_incomplete) == 1        for: 15m
SCBudgetMetricsStale:      time() - max(sc_budget_metrics_updated_timestamp_seconds) > 300        for: 5m
```

Because period rows are keyed by period, a new day or month starts at utilization 0, and the alerts resolve by themselves. Annotations carry the scope key, limit, spent, utilization and the runbook `procedures/budget-emergency-stop.md`. Task-scope thresholds produce no alert (the label would be unbounded). Task exhaustion notifies through `SCTaskNeedsHuman` (§14.5).

### 14.5 Exhaustion handling (decision)

When a worker's request is denied, the harness surfaces the provider-format error, sc-worker reports `failed` with reason `budget_denied` or `emergency_stop` (plan 27), and the Runner classifies from `ledger.denials` (latest row for the execution):

| Trigger (denial code / scope kind) | Execution | Task transition (plan 29) | Notification |
|---|---|---|---|
| `SC_BUDGET_EXHAUSTED` on `execution:` | `FAILED`, class `budget`, `BUDGET_EXECUTION_EXHAUSTED` | → `NEEDS_HUMAN` (`reason: budget_exceeded`) | `SCTaskNeedsHuman` |
| `SC_BUDGET_EXHAUSTED` on `task:` | `FAILED`, `budget`, `BUDGET_TASK_EXHAUSTED` | → `NEEDS_HUMAN` (`budget_exceeded`) | `SCTaskNeedsHuman` |
| `SC_REQUEST_COST_CAP` | `FAILED`, `budget`, `BUDGET_REQUEST_CAP` | → `NEEDS_HUMAN` (`budget_exceeded`) | `SCTaskNeedsHuman` |
| `SC_BUDGET_UNDEFINED` | `FAILED`, `budget`, `BUDGET_UNDEFINED` | → `NEEDS_HUMAN` (`budget_exceeded`) | `SCTaskNeedsHuman` (+ `SCGatewayPolicyInvalid` if policy-caused) |
| `SC_BUDGET_EXHAUSTED` on a shared scope (provider, global, orchestration) | `FAILED`, `budget`, `BUDGET_SHARED_EXHAUSTED` | → `BLOCKED` (`blocked_reason: budget_window_exhausted`, `resume_state` = the state the task had before this dispatch) | `SCBudgetExhausted` / `SCBudgetHourlyCapReached` |
| `SC_EMERGENCY_STOP` or a Runner e-stop cancellation | running: `FAILED`, `budget`, `EMERGENCY_STOP`; queued: `CANCELLED` | → `BLOCKED` (`blocked_reason: emergency_stop`, `resume_state` as above) | `SCEmergencyStopActive` |
| `SC_BUDGET_CONTENDED` (surfaced after SDK retries) | `FAILED`, class **infrastructure**, code `BUDGET_CONTENDED` (refunded, plan 29) | per plan 29's infrastructure rule (requeue; no human-only state) | none (`sc_budget_denials_total{code="SC_BUDGET_CONTENDED"}` visible on the dashboard) |
| Dispatch refusal `BUDGET_EXCEEDED` on the **`task:`** scope (review COST-7) | none created | → `NEEDS_HUMAN` (`budget_exceeded`, plan 29 T132, event `dispatch_refused`): task headroom never returns by waiting | `SCTaskNeedsHuman` |
| Dispatch refusal `BUDGET_EXCEEDED` on a **shared** scope | none created | unchanged (Hermes waits; headroom returns with the next period) | `SCBudgetDispatchRefused` (new, warning: `increase(sc_runner_dispatch_refusals_total{code="BUDGET_EXCEEDED"}[15m]) > 0` for 15 m) and, at 100 %, `SCBudgetExhausted` |
| Dispatch refusal `EMERGENCY_STOP` | none created | unchanged | `SCEmergencyStopActive` |
| Dispatch refusal `ATTEMPTS_EXHAUSTED` | none created | → `NEEDS_HUMAN` (plan 29) | `SCTaskNeedsHuman` |
| QUEUED execution whose dispatcher re-validation (plan 31 §14.5) fails for **`task:`** headroom | `FAILED`, `budget`, `BUDGET_TASK_EXHAUSTED`, immediately | T106 → `NEEDS_HUMAN` (`budget_exceeded`) | `SCTaskNeedsHuman` |
| QUEUED execution whose re-validation fails only for **shared** headroom until plan 29's `spec.retry.infrastructure.queueTimeout` (2 h) | `FAILED`, `budget`, `BUDGET_SHARED_EXHAUSTED` (refunded) | T105 → `BLOCKED` (`budget_window_exhausted`) | `SCBudgetDispatchRefused` / `SCBudgetExhausted` |

Rationale:
- **Task-attributable** limits mean the work is larger or costlier than its bound. A retry would hit the same wall, so a human must decompose the task, raise the task limit (§14.11) or cancel it.
- **Shared-window and e-stop** stops are not the task's fault. `BLOCKED` with a stored resume state keeps the task intact. Leaving `BLOCKED` is a human-only transition (L-4), so lifting a budget or a stop never restarts spending silently (33-R-7). Plan 29's per-class retry policy MUST give class `budget` **0 automatic retries**.
- Budget-class failures never count as merit failures (no escalation, plan 31) and never as infrastructure failures (no fallback).
- **Resume** is a human `POST /v1/tasks/{id}/transitions` with `decision.command: unblock`, `expected_from: BLOCKED` and `to: <resume_state>` (plan 29 §7.3, T105), actor `human:<name>`. The procedure lists all tasks to resume with `GET /v1/tasks?state=BLOCKED&blocked_reason=<reason>` (request to plan 26 for the filter).

### 14.6 Emergency stop: sources and effective level

Levels are totally ordered: `none` (0) < `workers` (1) < `all` (2).

```text
db       = ledger.emergency_stop_state.level        (Gateway: inside every reservation txn + 2 s poll;
                                                     Runner: read at every dispatch + 2 s poll)
git      = BudgetPolicy.spec.emergencyStop.level    (last-known-good policy)
k8s      = ConfigMap sc-emergency-stop data.level   (absent/404 = none; unparseable = all)
degraded = workers  if the k8s source has not been read successfully for > staleAfter (60 s), else none
effective = max(db, git, k8s, degraded)
```

Token kinds covered: `workers` covers `execution` tokens; `all` covers `execution` and `orchestrator` tokens.

**Gateway behavior** on an effective-level increase that covers a token kind:
- new requests of that kind → 403 `SC_EMERGENCY_STOP`;
- **in-flight requests of that kind are cut**: the upstream connection is closed, the client connection is closed, settlement runs with `termination: emergency_stop_cut` and `usage_status` per plan 53 §14.4: `estimated` if partial usage was observed, else `unknown` (review COST-12).

The SDK's retry after the cut receives the 403 and stops. The cut happens within one poll interval of the source change (≤ 2 s for DB, ≤ 5 s for Kubernetes, ≤ 10 s plus kubelet sync for Git). The source change, level and actor annotation are logged (`msg=estop_source_changed`).

**Runner behavior** at an effective level ≥ `workers`. The Runner computes the level with the same formula from its own reads: `degraded` comes from its ConfigMap informer's last successful list/watch, older than 60 s. `SCEmergencyStopNotApplied` detects any divergence from the Gateway's level.
1. `agent_start` → `EMERGENCY_STOP`. Embedded-task creation in `agent_start` is rejected atomically. `POST /v1/tasks` without dispatch (admin) still works.
2. Every `QUEUED` execution → `CANCELLED` (reason `emergency_stop`). Its task → `BLOCKED` (`emergency_stop`).
3. Every `STARTING`/`RUNNING` execution:
   - revoke the gateway token immediately;
   - set `cancel_requested`;
   - after `cancelGraceSeconds` capture logs and delete the Job (plan 26 §14.6);
   - the execution becomes `FAILED`, class `budget`, `EMERGENCY_STOP`; its task → `BLOCKED` (`emergency_stop`).
   - Pushed commits are left on `agent/<task-id>` and are not judged further. Pending RED/GREEN pipelines that were already triggered complete and their evidence is stored (no model spend), but no follow-on dispatch occurs.
4. Retries and the evidence-triggered next dispatch are suppressed while the level is ≥ `workers`.
5. The Runner appends an audit event (`runner.audit_events`, plan 26) whenever it observes a change in any source, including the Kubernetes and Git ones.

**What keeps running** at both levels: Runner API/MCP (`agent_status`, `agent_logs`, `agent_result`, `agent_cancel`, `agent_list`), Forgejo sync and PR updates, the evidence poller, sweepers, Woodpecker CI, Forgejo, Flux, CNPG and backups, Authentik, monitoring and alerting, the GitHub mirror, and the Gateway itself (denying).
- At `workers`, Hermes keeps full model access, so it can explain the situation to the human, and its spend remains bounded by the orchestration budget.
- At `all`, the Hermes process and dashboard stay up, but its model calls are denied, so it can no longer reason autonomously.

### 14.7 Activation and reversal procedures (summary; the full runbook is `procedures/budget-emergency-stop.md`)

| Path | Use when | Activate | Verify | Reverse |
|---|---|---|---|---|
| 1 Runner admin API | default; fastest audited path; Runner and DB healthy | `PUT /v1/admin/emergency-stop {"level":"workers"\|"all","reason":…}` via port-forward with `SC-RUNNER-ADMIN` | response `effective_level`; `sc_emergency_stop_level{source="effective"}`; Gateway `/sc/v1/status` | same call with `"level":"none"` |
| 2 Git | planned freeze; persistence across DB restores; black-start recovery | PR on `policy/budgets.yaml` `spec.emergencyStop` | `GET /v1/policy/status` shows the revision; source `git` level | PR setting `level: none` |
| 3 Kubernetes ConfigMap | Runner or DB broken, Runner unreachable, or the admin token unavailable | `kubectl -n strange-company create configmap sc-emergency-stop --from-literal=level=all --from-literal=reason=…` (+ annotations) | Gateway `/sc/v1/status` source `k8s` | `kubectl -n strange-company delete configmap sc-emergency-stop` |
| (last resort) | Gateway itself misbehaving (forwarding despite a stop) | suspend Flux for the chart, scale `model-gateway` to 0; revoke provider keys in the provider consoles | fake upstream / provider console shows no traffic | reverse in order; rotate keys (procedure `rotate-model-credentials.md`) |

**Reversal semantics:** the effective level drops only when **all** sources are `none` (and the Kubernetes source is readable). After reversal:
1. new dispatches are accepted;
2. `BLOCKED` tasks stay blocked until a human resumes them (§14.5);
3. QUEUED executions that were cancelled are not re-queued.

### 14.8 Anomalous spend detection (`SCAnomalousSpend`, assignment ID `AnomalousModelSpend`)

Detection is separate from prevention. The hard brake is `global.hourlyUsd` (and the other limits). The anomaly alert asks a human to look before a limit is hit.

Recording rule (evaluated every 1 m):

```text
sc:model_spend_micro_usd:increase15m = sum(increase(sc_model_spend_micro_usd_total[15m]))
```

Alert (the policy values are baked into the rule text at render time; shipped values shown):

```text
(
  sc:model_spend_micro_usd:increase15m
    > 4 * quantile_over_time(0.95, sc:model_spend_micro_usd:increase15m[7d] offset 15m)
  and sc:model_spend_micro_usd:increase15m > 500000          # minimumSpendUsd 0.50
)
or sc:model_spend_micro_usd:increase15m > 8000000             # absoluteWindowCapUsd 8.00
for: 0s   severity: critical   notifyClass: page   alwaysNotify: budget-stop
```

- `windowMinutes` → the `[15m]` range and the offset. `baselineDays` → `[7d]`. `baselineQuantile` → `0.95`. `multiplier` → `4`.
- The rule renderer (plan 42 `make render-rules`) reads `policy/budgets.yaml`. `make check-rules` fails if the rendered rule disagrees with the policy, so a policy PR must regenerate the rules in the same PR.
- `sc_model_spend_micro_usd_total` counts `budget_charge_micro_usd` (plan 32 §21), so unknown-usage requests count at worst case.
- Cold start: with less than 7 days of history, the quantile uses what exists. With no history the baseline is 0 and the `minimumSpendUsd` guard prevents trivial alerts.
- Prometheus retention MUST be ≥ 8 days (request to plan 38). Prometheus loss disables detection only; prevention is unaffected.
- Response: the procedure checks the top spenders (`sum by (provider, model, harness, role)(increase(sc_model_spend_micro_usd_total[15m]))`) and the ledger, then decides whether to engage a stop. There is no automatic stop in MVP. An absolute cap that should *prevent* belongs in `global.hourlyUsd`.

### 14.9 Attempts, tokens and wall time

- **Attempts:** §14.1 step 2. Executions ended with `EMERGENCY_STOP` are excluded (operator-caused). Executions that never reached `STARTING` are excluded. Plan 29's per-role and per-class limits and plan 31's ladder bound attempts further. The smallest bound wins.
- **Tokens:** `perExecution.maxTotalTokens` is reserved per request (`worst_tok = in_bound + out_bound + rsn_bound`, plan 53 §14.1) and settled to actual tokens counted as uncached input + cache-write + output + reasoning; cache reads are excluded (contract `maxTotalTokens`, review COST-2).
- **Wall time:** the Runner sets the pod `activeDeadlineSeconds = min(profile.deadline, perExecution.maxWallTimeSeconds)` and the Job deadline per plan 26 (+ startup grace). The gateway token's `exp` = pod deadline + 300 s (≤ 6 h), so a Job that somehow outlives its deadline cannot spend. Deadline expiry is classified `TIMED_OUT` by plan 26 (class `infrastructure`) and counts as an attempt.

### 14.10 Orchestration (Hermes) budget

Hermes' orchestrator token reserves against `orchestration-day`/`-month`, the provider scopes and the global scopes. On exhaustion:
- Hermes calls fail with `SC_BUDGET_EXHAUSTED`. Hermes surfaces backend errors as `finish_reason: "error"` (L-1);
- `SCBudgetExhausted{scope_kind="orchestration_day"}` pages;
- workers continue (their scopes are separate), but no new work arrives because Hermes cannot plan.

The orchestrator token has no `task:` scope. `perRequest` limits apply to Hermes as to workers.

### 14.11 Human task-budget override

`PUT /v1/admin/budget-scopes/{scope_key}` (new; request to plan 26). Admin listener, `SC-RUNNER-ADMIN` credential (actor from the key id, CD-30) and `X-SC-Reason`. Body `{"limit_usd": "<decimal>", "reason": "<≥10 chars>"}`.
- Allowed only for `task:` keys, with `limit_usd ≤ global.monthlyUsd`.
- Writes `ledger.budget_limit_overrides`, updates the row's `limit_micro_usd` with `limit_source = override:<id>`, and raises `SCRunnerAdminAccess`.
- At later dispatches the override wins over `perTask.maxCostUsd` (§14.1 step 4).
- Shared and execution limits can only change through Git.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| `sc-pg` down | Gateway `/readyz` reason `db`; `SCGatewayNotReady` | no reservations → no spend | fail closed | `gateway-failure.md`; `cnpg-primary-loss.md` |
| Hot-row lock contention | `sc_gateway_stage_seconds{stage="reserve"}` p99 > 50 ms → `SCGatewayLatencyOverheadHigh` | slower calls | none | `gateway-failure.md` §latency |
| Bound violation (reported usage above the computed bound) | `SCGatewayBoundViolation` (critical, new) | one request (plus that model's in-flight requests) cost more than reserved; scopes charged the real cost | model quarantined until acknowledged (plan 53 §14.4a) | `gateway-failure.md` §bound |
| Gateway pod dies with open reservations | open past `expires_at` | headroom held until expiry | sweeper settles at worst case | — |
| Budget gauges stale (leader lost, DB slow) | `SCBudgetMetricsStale` | threshold alerts blind; enforcement unaffected | none | `gateway-failure.md` §ledger |
| Reservation contention (concurrent requests of healthy executions) | `sc_budget_denials_total{code="SC_BUDGET_CONTENDED"}`; wait-time histogram `sc_gateway_stage_seconds{stage="reserve"}` | requests wait ≤ 30 s, then 429 and SDK retry | bounded wait (§14.2) | none unless persistent: `budget-emergency-stop.md` §E (raise the limit or lower concurrency by PR) |
| Prometheus or Alertmanager down | `Watchdog` dead-man's switch (ADR-0009) | notifications delayed; enforcement unaffected | none | `monitoring-failure.md` |
| Kubernetes API unreachable from the Gateway | `sc_emergency_stop_k8s_last_read_ok_timestamp_seconds` stale > 60 s → `SCEmergencyStopSourceUnavailable` | effective level ≥ `workers` (degraded): worker spend stops | automatic; clears when reads succeed | `gateway-failure.md` §estop |
| Unparseable ConfigMap level | Gateway log + `sc_emergency_stop_level{source="k8s"} == 2` with annotation `invalid` | `all` | none | `budget-emergency-stop.md` (fix or delete) |
| Runner and Gateway effective levels disagree for > 2 m | `SCEmergencyStopNotApplied` | workers may still be running while denied (wasted Jobs), or a Runner keeps dispatching while the Gateway denies | none | `budget-emergency-stop.md` §diagnose |
| E-stop engaged but the Runner is down | Runner not observing | running Jobs keep running but cannot spend (Gateway denies); they fail on their own or hit their deadline | Gateway enforcement suffices | `budget-emergency-stop.md`, `agent-runner-failure.md` |
| Policy invalid (budgets) | `SCGatewayPolicyInvalid`, `SCRunnerPolicyInvalid` | LKG limits and LKG Git e-stop level stay in force | none | fix PR |
| Shared budget exhausted | `SCBudgetExhausted` | worker requests in scope denied; tasks `BLOCKED` | none | `budget-emergency-stop.md` §exhausted |
| Task budget exhausted | `SCTaskNeedsHuman` (`budget_exceeded`) | task `NEEDS_HUMAN` | none | `budget-emergency-stop.md` §task |
| Anomalous spend | `SCAnomalousSpend` | none yet | none (human decision) | `budget-emergency-stop.md` §anomaly |
| Ledger lost to PITR | restore procedure | spent under-counted for the lost window | none | `pricing-update.md` §reconcile (cost adjustments) |
| DB e-stop events lost to PITR | restore procedure | a stop set after the recovery point vanishes | none | `budget-emergency-stop.md` step "re-assert after restore" |
| Someone lifts one source while another is set | status shows the remaining source | stop stays in effect (by design) | none | `budget-emergency-stop.md` §release |

## 16. Retry behavior

- Gateway: reservation transaction retried ≤ 3 times on serialization or deadlock errors. Settlement retried with backoff for 10 minutes, then the sweeper takes over (plan 53 §16). Kubernetes source polled every 5 s indefinitely.
- Runner: e-stop application (cancellations, revocations) runs through the outbox (`dedup_key = estop:<event_id>:<execution_id>`) with plan 26's backoff until complete.
- Task level: class `budget` gets 0 automatic retries (§14.5). Any re-dispatch is a human-initiated resume or a Hermes `agent_start` after headroom returns. Hermes receives `BUDGET_EXCEEDED`/`EMERGENCY_STOP` until the cause clears.

## 17. Idempotency behavior

- `PUT /v1/admin/emergency-stop` is idempotent by `Idempotency-Key` (unique on `emergency_stop_events.idempotency_key`). Setting the current level again appends no event and returns the current state.
- Cancellation of executions on a stop is idempotent per (event, execution) dedup key. An execution already terminal is skipped.
- Scope-row creation is `INSERT … ON CONFLICT`. Settlement is guarded by `state='open'` (plan 53 §17).
- ConfigMap activation with `kubectl apply` is declarative, and applying it repeatedly is a no-op.
- Git source: re-merging the same level changes nothing.

## 18. Bootstrap behavior

- The Runner's startup migrations (CD-04) create the tables and seed `emergency_stop_events` with `(level 'none', actor 'migration')`.
- `policy/budgets.yaml` ships with the defaults of §9.1. Preflight prints them for operator confirmation (plan 50).
- During bootstrap the only usable provider is `fake` (plan 53 §18). `bootstrap verify` performs one Gateway request with a verify-only token. When an e-stop is active (e.g. black-start recovery, §20), verify accepts a `403 SC_EMERGENCY_STOP` response as proof that the Gateway is healthy and enforcing (request to plan 50).
- The ConfigMap `sc-emergency-stop` does not exist after bootstrap (absent = `none`).

## 19. Upgrade behavior

- Table changes are expand/contract (plan 53 §19). The reservation and settlement SQL must stay compatible across one Gateway version skew (rolling update with two versions live).
- New scope kinds (e.g. per-repository monthly budgets) are additive: a new enum value, a new key pattern in `cost-record.schema.yaml` and token derivation. Existing rows are unaffected.
- A budget-policy change takes effect at the next reload on both components. In-flight reservations keep their amounts.
- Version-sensitive behavior: kubelet ConfigMap-volume propagation delay (path 2 latency) and Flux's inventory-based pruning (path 3 isolation). Both are re-verified by 33-AC-10 and 33-AC-12 on the pinned k3s and Flux versions.

## 20. Recovery behavior

- **`sc-pg` PITR** (plan 12):
  - Scope rows revert to the recovery point, so spend after it is not counted. The operator records lost spend from provider consoles as `ledger.cost_adjustments` (plan 32 §20). The Runner admin endpoint `POST /v1/admin/ledger/adjustments` (plan 26) inserts the adjustment row and, **in the same transaction**, calls `ledger.apply_cost_adjustment(adjustment_id uuid)`. That function is `SECURITY DEFINER`, owned by the migration role and created by the Runner's migrations (CD-04). It locks the matching `provider-month:` and `global-month:` rows of the adjustment's period (plus `provider-day:`/`global-day:` if `period` is a day) in key order, adds `delta_micro_usd` to `spent_micro_usd` (creating missing period rows with the policy limit), and sets `applied_at`. It is idempotent: an adjustment whose `applied_at` is set is a no-op. `runner_app` holds only `EXECUTE` on the function and no `UPDATE` on `spent_*`, so the spent columns stay Gateway- or definer-written (review RA-m9; aligned with plan 26 Q6).
  - E-stop events after the recovery point are lost, and a path-1 stop engaged just before the restore is rolled back by the restore itself. Therefore **before any `sc-pg` PITR the stop is engaged through path 3 (ConfigMap) or path 2 (Git), never path 1**, and after the restore the DB source is compared against that source, not against memory (review COST-14; `budget-emergency-stop.md` §re-assert; `restore-cnpg.md` must say so, plan 12).
  - Open reservations restored by PITR are expired by the sweeper at worst case (over-count, safe).
- **Black start (plan 45, ADR-0554):** the recovery commit MUST set `policy/budgets.yaml` `spec.emergencyStop: {level: workers, reason: "black-start recovery <install-id>"}`. The stop then holds through Git regardless of database state, and the finalize PR resets it to `none` (interface to plan 45, consistent with ADR-0554's "workers stay paused").
- **Kubernetes ConfigMap:** not backed up and not in Git. After total cluster loss it is gone, which is safe because the Git source above covers recovery.

## 21. Observability

**Gateway metrics** (leader-exported gauges from the DB unless noted; no task, execution or jti labels):

| Metric | Type | Labels |
|---|---|---|
| `sc_budget_limit_micro_usd`, `sc_budget_spent_micro_usd`, `sc_budget_reserved_micro_usd` | gauge | scope_kind, provider |
| `sc_budget_utilization_ratio` | gauge | scope_kind, provider |
| `sc_budget_threshold_reached` | gauge (0/1) | scope_kind, provider, threshold |
| `sc_budget_exhausted` | gauge (0/1) | scope_kind, provider |
| `sc_budget_cost_incomplete` | gauge (0/1) | scope_kind, provider |
| `sc_budget_metrics_updated_timestamp_seconds` | gauge | — |
| `sc_budget_denials_total` | counter (every replica) | code, scope_kind, dimension, token_kind |
| `sc_emergency_stop_level` | gauge (every replica; 0/1/2) | source (`db`, `git`, `k8s`, `degraded`, `effective`) |
| `sc_emergency_stop_active` | gauge (0/1) | level (`workers`, `all`) |
| `sc_emergency_stop_k8s_last_read_ok_timestamp_seconds` | gauge | — |
| `sc_gateway_estop_source_up` | gauge (every replica; 0/1) | source (`db`, `git`, `k8s`): 1 iff the source was read successfully within `estop.staleAfter` (`git`: the last-known-good policy is loaded) |
| `sc_ledger_reservations_oldest_closed_seconds` | gauge (leader) | — age of the oldest settled or expired reservation; retention target 30 d (plan 43 R-05) |
| `sc_emergency_stop_cuts_total` | counter | token_kind |

**Runner metrics:**
- `sc_runner_emergency_stop_level{source}` (same encoding);
- `sc_runner_estop_cancellations_total`;
- `sc_runner_dispatch_refusals_total{code}` (`BUDGET_EXCEEDED`, `EMERGENCY_STOP`, `ATTEMPTS_EXHAUSTED`);
- `sc_runner_tasks_blocked{blocked_reason}` (gauge; bounded enum).

**Alert expressions** for `SCEmergencyStopActive` and `SCEmergencyStopNotApplied`:

```text
SCEmergencyStopActive:     max(sc_emergency_stop_level{source="effective"}) >= 1                       for: 0s
SCEmergencyStopNotApplied: max(sc_runner_emergency_stop_level{source="effective"})
                             != max(sc_emergency_stop_level{source="effective"})                       for: 2m
```

Budget alert expressions are in §14.4 and the anomaly alert in §14.8.

**Catalogue alignment (CD-01).** `contracts/alert-catalog.yaml` carries every alert above under these names and with these expressions (`SCBudgetThresholdReached`, `SCBudgetExhausted`, `SCBudgetHourlyCapReached`, `SCBudgetCostIncomplete`, `SCBudgetMetricsStale`, `SCEmergencyStopActive`, `SCEmergencyStopNotApplied`, `SCEmergencyStopSourceUnavailable`, `SCAnomalousSpend`). `SCBudgetDispatchRefused` (§14.5) and `SCGatewayBoundViolation` (critical, page, `budget-stop`; replaces `SCGatewayReservationOverrun`, CD-28) are new in this review round and must be added by plan 42. The catalogue summaries of `SCModelUsageUnknown` and `SCBudgetCostIncomplete` must say that unknown usage is charged at its worst case (a ceiling, not a floor; review COST-15). The catalogue is the naming authority; this section is normative for the signal semantics. Runbooks: `procedures/budget-emergency-stop.md`, and `procedures/gateway-failure.md` for the source-unavailable alert.

**Dashboards** (plan 39, "Economics"):
- spend today and month vs limits: `sc_budget_spent_micro_usd / sc_budget_limit_micro_usd` per scope kind;
- a stop-status stat panel: `sc_emergency_stop_level{source}`;
- denials by code;
- blocked tasks by reason.

**Logs:**
- Gateway: `msg=budget_denied` (code, scope, dimension; no content) and `msg=estop_source_changed`.
- Runner: `msg=estop_applied` (event id, counts of cancelled executions).

**Traces** (names from `contracts/telemetry.schema.yaml`):
- `sc.model.invocation` (plan 53) carries `sc.gateway.denial_code` and `sc.gateway.outcome` (`denied_budget`, `denied_estop`).
- Every observed change of the effective level emits a span **`sc.estop.apply`** with `sc.estop.level` (`none`/`workers`/`all`), `sc.estop.source` (`api` for path 1 through the Runner admin API; `db`, `git` or `k8s` for a change observed in that source; `degraded` for the stale-Kubernetes fallback) and `sc.estop.cancelled_executions`. The Runner emits it when applying the stop; the Gateway emits it when its own level changes.
- Each cancelled execution additionally gets the span event `sc.cancel_requested` on its `sc.execution` span.

## 22. Testing strategy

All deterministic with the fake upstream and fake harnesses [SPEC §104].

- **Contract:** `budget-policy.schema.yaml` meta-validation, examples, and invalid examples.
- **Loader unit tests:** one negative fixture per §9.2 invariant.
- **Reservation unit and DB tests** (testcontainers Postgres):
  - the §14.2 algorithm per check, with exact boundaries (headroom = amount admitted; amount + 1 µUSD denied);
  - lock-order deadlock freedom (two goroutines with overlapping key sets, 10 000 iterations, zero deadlock errors);
  - grant and trigger tests (§7.2): the Gateway cannot change a task limit, and the Runner cannot change spent.
- **Concurrency test** (`make test-budget-concurrency`): 64 goroutines × 1 request against an execution scope with headroom for exactly 10 worst-case reservations. Exactly 10 forwarded, 54 denied, invariant `spent + reserved ≤ limit` sampled every 1 ms, and final `reserved = 0` after settlement.
- **Emergency-stop tests** (k3d): every path, every level, in-flight cut with `fake-slow`, the degraded source, and Flux non-interference.
- **Classification tests:** each §14.5 row driven by a fake harness that makes requests until denied (`fake-spend-loop`, a new plan 28 scenario) or by setting the denial condition directly. Assertions on execution class and code, task state, `resume_state`, and the notification at the test receiver.
- **Rule tests** (`promtool test rules`): threshold, exhaustion, hourly cap, stale metrics, e-stop active and not-applied, and anomaly (baseline/spike/min-guard/absolute cap series).
- **Rendered-rule drift test** (`make check-rules`).

## 23. Acceptance criteria

### GATE-C06

- **33-AC-1** `make contracts` passes for `budget-policy.schema.yaml`: meta-schema valid, both examples valid, all four invalid examples fail. `make policy-check` rejects one fixture per §9.2 invariant with the invariant named in the error, and accepts the shipped `policy/budgets.yaml`.
- **33-AC-2** Scope coverage (k3d, test policy with small limits): for each limit `perExecution.maxCostUsd`, `maxTotalTokens`, `maxRequests`, `perTask.maxCostUsd`, `providers.fake.dailyUsd`, `providers.fake.monthlyUsd`, `global.hourlyUsd`, `global.dailyUsd`, `global.monthlyUsd`, `orchestration.dailyUsd` and `orchestration.monthlyUsd`, a request that would exceed it returns 403 with `x-sc-error-code: SC_BUDGET_EXHAUSTED`. A `ledger.denials` row exists with the expected `scope_key` and `dimension`, and the fake upstream's request count is unchanged.
- **33-AC-3** Concurrency: `make test-budget-concurrency` (k3d, both Gateway replicas behind the Service) forwards exactly 10 of 64 concurrent requests. `SELECT spent_micro_usd + reserved_micro_usd <= limit_micro_usd FROM ledger.budget_scopes WHERE scope_key=$k` is true at every sample. After settlement `reserved_micro_usd = 0` and `count(cost_records) = 10`.
- **33-AC-4** Dispatch pre-check: with `task:` headroom below `minimumHeadroomUsd.bargain`, `agent_start` (implementer, bargain) returns `BUDGET_EXCEEDED` with `details.scope_key = "task:<id>"`. No Job, no `ledger.gateway_tokens` row and no `execution:` scope row are created. With `perTask.maxAttempts: 3` (test policy whose ladders and retry limits sum to ≤ 3, so invariant 9 holds) the fourth non-refunded `agent_start` returns `ATTEMPTS_EXHAUSTED`.
- **33-AC-6** Thresholds: driving `provider-day:fake` spend to 51 %, 81 % and 100 % of its limit produces, at the test receiver, `SCBudgetThresholdReached{threshold="50"}`, then `{threshold="80"}`, then `SCBudgetExhausted` (and no `SCBudgetThresholdReached` once exhausted), each within 2 min of crossing.
- **33-AC-10** E-stop path 2 (Git source): applying a policy ConfigMap revision with `emergencyStop.level: workers` (the k3d stand-in for a merged PR) yields `sc_emergency_stop_level{source="git"} == 1` on both Gateway replicas and denial of execution-token requests within 120 s of the ConfigMap update (re-verifies kubelet propagation on the pinned k3s). Setting `none` reverses it.
- **33-AC-12** Flux non-interference: with the ConfigMap present, running `flux reconcile kustomization <every kustomization> --with-source` and `flux reconcile helmrelease -n strange-company strange-company` leaves its `metadata.resourceVersion` unchanged and the ConfigMap present. `make lint-gitops` fails on a fixture overlay that renders a ConfigMap named `sc-emergency-stop`.
- **33-AC-13** RBAC: `kubectl auth can-i` returns:
  - `no` for `create`, `update` and `delete` on `configmaps` in `strange-company` for SAs `agent-runner`, `model-gateway`, `hermes:*`, `agent-jobs:agent-worker`;
  - `yes` for `get configmaps/sc-emergency-stop` for `model-gateway`;
  - `no` for `get configmaps/other` for `model-gateway`.
- **33-AC-14** Degraded source: removing the Gateway's egress to the kube-apiserver (test NetworkPolicy) makes `sc_emergency_stop_level{source="degraded"} == 1` and execution-token requests fail `SC_EMERGENCY_STOP` within 75 s, while orchestrator tokens still succeed. Restoring egress clears it within 10 s. `promtool test rules` shows `SCEmergencyStopSourceUnavailable` firing on that series.
- **33-AC-15** `promtool test rules` passes for every alert in §14.4, §14.8 and §21:
  - anomaly: a 7-day baseline of $0.20/15m and a $2.00/15m spike fires; a $0.40 spike does not (min guard); $9.00 fires (absolute cap);
  - not-applied fires after 2 m of divergence.
  - `make check-rules` fails when `anomaly.multiplier` in a fixture policy differs from the rendered rule.
- **33-AC-17** Task override: `PUT /v1/admin/budget-scopes/task:sc-<n> {"limit_usd":"40.00","reason":"approved larger refactor"}` returns 200, writes one `ledger.budget_limit_overrides` row, sets `limit_source` to `override:<id>`, and fires `SCRunnerAdminAccess`. The previously refused `agent_start` then succeeds. The same call for a `global-day:` key returns 400.
- **33-AC-18** Persistence: after setting level `workers` via path 1 and deleting both Gateway pods, the first request served by a new pod is denied `SC_EMERGENCY_STOP`.
- **33-AC-19** PITR adjustment: inserting a cost adjustment of +$3.00 for provider `fake` and month `2026-10` through the admin API increases `spent_micro_usd` of `provider-month:fake:2026-10` and `global-month:2026-10` by 3 000 000 in the same transaction (`ledger.apply_cost_adjustment()`), and the adjustment's `applied_at` is set. Calling the function again for the same id changes nothing. `runner_app` gets `permission denied` on a direct `UPDATE ledger.budget_scopes SET spent_micro_usd = …`.
- **33-AC-20** Repository override: with the shipped policy, the first dispatch of a task on `sc-e2e/fake-target` creates `task:<id>` with `limit_micro_usd = 2000000` and `limit_source` naming the repository override; a task on any other repository gets `perTask.maxCostUsd`. A policy whose override exceeds `global.monthlyUsd` is rejected by `make policy-check`.
- **33-AC-21** Telemetry and retention gauges: `sc_gateway_estop_source_up{source="k8s"}` drops to 0 in the 33-AC-14 scenario and returns to 1 afterwards; after a reservation older than 30 d is seeded (test clock), the daily maintenance run deletes it and `sc_ledger_reservations_oldest_closed_seconds < 32*86400`.
- **33-AC-22** Contention (review COST-3): with an execution scope whose limit admits two worst-case reservations only one at a time, two concurrent `fake-slow` requests both end 2xx (the second waits for the first's settlement); `ledger.denials` has no `SC_BUDGET_EXHAUSTED` row, `last_denied_at` stays null and `sc_budget_exhausted` stays 0. Held open past the wait, the second request returns 429 `SC_BUDGET_CONTENDED`, dimension `cost_reserved`.
- **33-AC-23** Shipped defaults vs real request sizes (review COST-2): `make policy-check` on the shipped `policy/` (with the implementation-time price cards) passes invariants 8–10; a fixture price card that makes the largest-class model's full-window worst case exceed `perRequest.maxCostUsd` is rejected with the model and both amounts in the error.
- **33-AC-24** Attempt counting (review COST-8): `make policy-check` rejects a policy with `perTask.maxAttempts` below the ladder + review + infrastructure sum; in the Runner unit tests, a task with 2 refunded infrastructure failures and 5 implementer merit failures under the **shipped** policy still reaches the frontier rung (no `ATTEMPTS_EXHAUSTED` before `escalation_ladder_exhausted`).
- **33-AC-25** Hard ceiling and bound violation (CD-28): (a) 1 000 random requests from the adversarial fixture corpus (plan 53 53-AC-36) through the fake family each have `budget_charge_micro_usd ≤ reserved_micro_usd ≤ perRequest.maxCostUsd` in micro-USD and no `bound_violation`; (b) a policy whose full-window worst case for a model exceeds `perRequest.maxCostUsd` is rejected by `make policy-check` (invariant 8); (c) with `fake-over-bound`, the scope's `spent_micro_usd` includes the full real charge, the row has `bound_violation=true`, and the next request for that model is `SC_MODEL_QUARANTINED`.

### GATE-C10 (moved from GATE-C06; they need fake-harness Jobs (C02), Hermes (C08) or evidence (C04), which C06 does not depend on — review COST-10 / GC-01)

- **33-AC-26** Dispatch refusal notification (review COST-7): with `task:` headroom below `minimumHeadroomUsd[tier]`, `agent_start` returns `BUDGET_EXCEEDED`, the task enters `NEEDS_HUMAN` (`budget_exceeded`) and `SCTaskNeedsHuman` reaches the test receiver within 2 min; with only `provider-day:fake` short, the task state is unchanged and `SCBudgetDispatchRefused` fires after 15 m (`promtool test rules`).
- **33-AC-27** Queued re-validation (review COST-7): an execution QUEUED behind a held worker slot whose task scope is lowered below `minimumHeadroomUsd[tier]` ends `FAILED` `BUDGET_TASK_EXHAUSTED` at the next dispatcher cycle and the task is `NEEDS_HUMAN` via T106 (`runner.transitions` row with that id); with only `provider-day:fake` short and a test `queueTimeout` of 1 m, the execution stays QUEUED until the timeout, then ends `FAILED` `BUDGET_SHARED_EXHAUSTED` (refunded) and the task is `BLOCKED` via T105.
- **33-AC-5** Exhaustion classification (each §14.5 row, k3d, `fake-spend-loop`):
  - execution-scope exhaustion → execution `FAILED`/`budget`/`BUDGET_EXECUTION_EXHAUSTED`, task `NEEDS_HUMAN`, and an `SCTaskNeedsHuman` delivery with `reason=budget_exceeded` at the test receiver within 2 min;
  - provider-day exhaustion → `BUDGET_SHARED_EXHAUSTED`, task `BLOCKED` with `blocked_reason=budget_window_exhausted` and `resume_state` equal to the pre-dispatch state, and an `SCBudgetExhausted` delivery within 2 min.
  - In both cases no further execution of the task starts within 10 min.
- **33-AC-7** E-stop path 1, level `workers`:
  - after `PUT /v1/admin/emergency-stop {"level":"workers"}` the response `effective_level` is `workers`;
  - a new execution-token request is denied `SC_EMERGENCY_STOP` within 5 s;
  - an in-flight `fake-slow` stream ends within 5 s with a cost record `termination=emergency_stop_cut`;
  - every RUNNING execution is `FAILED`/`budget`/`EMERGENCY_STOP` and its pod is gone within 90 s; QUEUED executions are `CANCELLED`; their tasks are `BLOCKED` (`emergency_stop`);
  - an orchestrator-token request still succeeds;
  - Forgejo `GET /api/healthz` = 200, Runner `/readyz` = 200, MCP `agent_status` succeeds, and `flux get kustomizations` shows all Ready;
  - `SCEmergencyStopActive` is delivered to the test receiver within 2 min.
- **33-AC-8** E-stop level `all`: an orchestrator-token request is denied `SC_EMERGENCY_STOP` within 5 s. The Hermes pod remains `Ready` and its dashboard answers 200.
- **33-AC-9** Reversal: after `{"level":"none"}` new requests succeed within 5 s. The tasks blocked in 33-AC-7 are still `BLOCKED` 10 min later. A human transition to their `resume_state` makes them dispatchable again.
- **33-AC-11** E-stop path 3 with the Runner scaled to 0 replicas and Hermes calling: `kubectl create configmap sc-emergency-stop --from-literal=level=all` makes orchestrator-token requests fail `SC_EMERGENCY_STOP` within 10 s. `GET :9090/sc/v1/status` shows `sources.k8s.level = "all"`. `kubectl delete configmap sc-emergency-stop` restores service within 10 s. With `sc-pg` scaled to 0, the Gateway still reports `sources.k8s` (and denies everything with `SC_LEDGER_UNAVAILABLE`).
- **33-AC-16** Wall time: for every profile in both profile sets, the golden Job has `activeDeadlineSeconds = min(profile.deadline, perExecution.maxWallTimeSeconds)`, and the decoded gateway token has `exp − iat ≤ activeDeadlineSeconds + startupGrace + 300`.

## 24. Explicit non-goals

- Automatic emergency stop on anomaly detection (MVP alerts only; hard caps are limits).
- Automatic resume of `BLOCKED` tasks after a window resets or a stop is lifted (L-4).
- Per-repository or per-project calendar budgets (additive later, §19).
- Currencies other than USD; non-UTC budget calendars (schema constants).
- Budgeting CI compute or infrastructure cost (SPEC §14 covers paid infrastructure separately).
- Provider-side spend caps as an enforcement mechanism (operators may set them as defense in depth; not relied upon).
- A Hermes-facing tool to read or set budgets or the emergency stop.

## 25. Open questions

1. **Emergency-stop level names.** Resolved here as `none | workers | all` (assignment and `budget-policy.schema.yaml`). Plan 26 assumed an extra `dispatch` level and a Gateway admin endpoint (`:8091`). Blocking: no. Resolution: plan 26 aligns its OpenAPI enum and drops the forwarding (§7.4). Owner: plans 26/33.
2. **`TIMED_OUT` classification.** A wall-time limit is a SPEC §81 budget limit, but plan 26 classifies deadline expiry as `infrastructure`, which allows up to 3 consecutive automatic retries (each bounded by its own deadline and execution budget). Blocking: no. Proposed: keep plan 26's class for MVP, because `perTask.maxAttempts` and `perTask.maxCostUsd` bound the total. Plan 29 may cap consecutive `TIMED_OUT` at 2. Owner: plan 29.
3. **BLOCKED with `resume_state`** and the human-only exit are assumptions on plan 29's state machine. The `blocked_reason` filter on `GET /v1/tasks` is a request to plan 26. Blocking: no. Owner: plans 29/26.
4. **Alert catalogue alignment.** Resolved by CD-01: the catalogue now carries the §14.4/§14.8/§21 names and expressions. Owner: plan 42.
5. **Bootstrap acknowledgement of model budgets** (§9.3) is a request to plan 50. Blocking: no. Owner: plan 50.
6. **Recovery commit sets the Git e-stop source** (§20) is a request to plan 45 and is consistent with ADR-0554. Blocking: no. Owner: plan 45.
7. **Hermes spend is not task-attributed** (review COST-9, SPEC §78/§82). Acceptance criteria written by Hermes are charged to `orchestration` (`project _platform`), so a task's "specification cost" covers planner/researcher executions only. Blocking: no. Resolution: recorded as a SPEC interpretation (spec-issues, lead); a task-scoped orchestrator sub-scope is post-MVP. Owner: plans 32/33.
8. **Contended requests surfaced to a worker** are class `infrastructure` with code `BUDGET_CONTENDED` and refunded; plans 26 and 29 must add the code to their classification tables (needs other owner). Blocking: no. Owner: plans 26/29.
