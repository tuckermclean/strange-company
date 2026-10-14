# 32 — Cost Accounting

**Status:** Stage-0 plan (draft)
**DAG nodes:** C05 (co-owner with plans 53 and 31)
**Related plans:** [53 model gateway](53-model-gateway.md), [31 model routing](31-model-routing.md), [33 budget enforcement](33-budget-enforcement.md), 21 (TDD phases), 26 (Runner), 29 (state machine, outcomes), 39 (Grafana), 38/42 (Prometheus, alerts), 43 (retention), 55 (PRs)
**Contracts:** owns `contracts/cost-record.schema.yaml` and `contracts/pricing.schema.yaml`; consumes `model-routing.schema.yaml` and `gateway-token.schema.yaml`
**ADRs:** relies on ADR-0004, ADR-0006, ADR-0017; introduces ADR-0450 (integer micro-USD; reservation-based pre-authorization, shared with plan 33)

> Every section below is mandatory (SPEC §121). Requirements carry SPEC trace tags.

## 1. Purpose

Define what one ledger row means, how money is computed from provider-reported usage and versioned price cards, and how rows aggregate into the answers SPEC §80 and §82 require. The deliverables are:

- the cost-record contract;
- the pricing contract, including time-of-day windows, long-context tiers, cache TTL variants and explicit `unpriced`;
- per-provider usage normalization that keeps cache categories disjoint;
- the ledger and rollup schema in `sc-pg`;
- the query sketches for every SPEC §80 question;
- the low-cardinality Prometheus export;
- the data sources for the economics dashboards that plan 39 builds.

The runtime that writes rows is the Model Gateway (plan 53). Budget semantics are in plan 33.

## 2. Requirements derived from SPEC.md

1. Each billable invocation is attributable to provider, model, harness, role, execution, attempt, task, PR (where applicable), repository and project. The unit is one provider request. [SPEC §78]
2. Each row retains the §79 usage, pricing, cost and outcome fields. Historical pricing is retained so old reports stay reproducible. [SPEC §79]
3. Aggregation follows invocation → execution → attempt → TDD phase → task → PR → repository → project → day/month, and answers the ten §80 questions. [SPEC §80]
4. Spend is distinguished by specification, test authoring, red verification, implementation, green retry and review. [SPEC §82]
5. Exact accounting lives in CNPG. Prometheus carries only low-cardinality aggregates. [SPEC §90, §91]
6. Economics dashboards show spend today, spend this month, model/provider/harness/repo spend, cost per accepted task, cost per green task, cheap-tier success, escalation frequency, test-authoring vs implementation spend and estimated routing savings. [SPEC §92]
7. Exact accounting records may be retained longer than traces. [SPEC §107]

Derived requirements:

- **32-R-1** Money is integer micro-USD in storage and contracts. YAML configuration uses decimal strings. Floats never appear anywhere (ADR-0450).
- **32-R-2** `unpriced` is distinct from 0, and a total containing an unpriced, unknown or estimated row reports `cost_complete = false` (L-3).
- **32-R-3** After normalization, `input_tokens`, `cached_input_tokens` and `cache_write_tokens` are disjoint, and `output_tokens` and `reasoning_tokens` are disjoint. No token is charged twice (L-3).
- **32-R-4** Pricing supports time-of-day windows. A malformed schedule is rejected at load (L-3).
- **32-R-5** Every row stores the pricing version, the content digest of the exact entry used, and the applied rates. The entry document is snapshotted in the DB, so a report can be reproduced after the Git file changes.
- **32-R-6** Each provider dialect's normalization mapping is covered by recorded-fixture tests and by an opt-in real-provider re-verification (plan 53 §22.5).

**SPEC interpretations (recorded for spec-issues):**

- **Orchestration spend (Hermes)** is not attributable to a task. It is attributed to a pseudo-scope: `token_kind=orchestrator`, `phase=orchestration`, `project=_platform`, with task, execution and attempt null.
- **Outcome fields** (`worker_success` … `human_accepted`) are not known when the row is written. Stored rows are immutable and carry only `provider_success`; the outcome fields are joined at read time (`record_view: enriched`).
- **PR attribution** is by task. A PR maps 1:1 to a task through the `agent/<task-id>` branch (plan 55), and a task's PR number is joined at read time.
- **Project:** each repository belongs to exactly one project, declared in its repository context (plan 25). The project is carried as a token claim at dispatch.

## 3. Inputs

| Input | Source |
|---|---|
| Normalized usage, reported model, ids, timings, termination | Gateway usage tap (plan 53 §14) |
| `PricingTable` files `policy/pricing/<provider>.yaml` | Git → Flux ConfigMap (D-17) |
| Attribution claims (task, execution, attempt, worker, role, harness, phase, repository, project, tiers) | gateway token (`contracts/gateway-token.schema.yaml`) |
| Phase definitions | plan 21. Mapping: tester → `test_authoring`; implementer with no prior GREEN failure on the task → `implementation`; implementer after ≥1 failed GREEN or merit failure → `green_retry`; reviewer → `review`; planner/researcher → `specification`; Hermes → `orchestration`; `red_verification` is reserved for any model spend during RED classification (none expected in MVP) |
| Execution, task, PR and outcome state | Runner tables (plans 26, 29, 55) via the view `runner.v_execution_outcomes` (§8) |

## 4. Outputs

- Immutable rows in `ledger.cost_records` (one per forwarded request), plus `ledger.pricing_snapshots`.
- Reporting views `ledger.v_cost_records_enriched`, `ledger.v_execution_cost`, `ledger.v_task_cost`, the materialized rollup `ledger.mv_daily_spend`, and the question views of §14.5 (`ledger.v_pr_cost_by_phase`, `v_cost_per_green_task`, `v_tier_success`, `v_escalation`, `v_cost_per_accepted_task`, `v_harness_performance`, `v_failed_work_spend`, `v_routing_savings`).
- The SQL function `ledger.reprice(record_id uuid, pricing_digest text) RETURNS bigint`, used for what-if and savings estimates.
- Prometheus metrics `sc_model_spend_micro_usd_total`, `sc_model_tokens_total`, and the DB-derived gauges `sc_ledger_*` (§21).
- Read API responses: the Runner exposes per-task and per-execution cost with `cost_complete` (plan 26 `GET /tasks/{id}` → `cost` object, and MCP `agent_status`). The JSON shape is in §7.2.

## 5. Dependencies

**Hard:** C01 (`sc-pg`, the migration mechanism, Runner tables), plan 53 (writer), plan 31 (model ids), plan 21 (phase tags).

**Soft:**

- plan 29 provides `runner.v_execution_outcomes` (§8). Until then the enriched view returns null outcomes.
- plan 39 builds the dashboards from §21.
- plan 43 owns retention jobs.
- plan 35 creates the `sc_reporting` role.

## 6. Owned state

| State | Location | Writer | Notes |
|---|---|---|---|
| Invocation ledger | `sc-pg` `ledger.cost_records` | Gateway role `sc_gateway`, INSERT only | System of record for model spend. Immutable (trigger `cost_records_immutable` raises on UPDATE/DELETE, except in the retention function `ledger.prune_usage_raw()` / `ledger.prune_cost_records(before date)` owned by the migration role) |
| Pricing snapshots | `ledger.pricing_snapshots(pricing_digest PK, provider, model, service_tier, version, entry jsonb, first_seen_at)` | Gateway | Never deleted while referenced |
| Rollup | `ledger.mv_daily_spend` | Gateway leader (REFRESH CONCURRENTLY every 5 min) | Derived, rebuildable |
| Price cards | Git `policy/pricing/*.yaml` | humans via PR | Desired state |

Backup and PITR come with `sc-pg` (plan 12; RPO ≤ 15 min).

## 7. External interfaces

### 7.1 `cost-record` contract

`contracts/cost-record.schema.yaml`. The table columns mirror the contract: scalar columns for every top-level field, `usage_*` columns for usage, `pricing_*` columns for the applied rates, `cost_*` columns for cost, `usage_raw jsonb`, and `budget_scope_keys text[]`. Indexes:

- `(task_id)`;
- `(execution_id)`;
- `(started_at)` BRIN;
- `(provider, model_requested, started_at)`;
- `UNIQUE(reservation_id)` — deliberately **no foreign key** to `ledger.reservations`, because settled reservations are pruned after 30 days while cost records are kept ≥ 3 years (review COST-22; 53-AC-34);
- `UNIQUE(gateway_request_id)`.

Mapping to SPEC §79 field names:

| SPEC §79 | Contract |
|---|---|
| `task_id`, `execution_id`, `attempt`, `worker_id`, `repository`, `role`, `harness`, `provider`, `provider_request_id`, `started_at`, `finished_at`, `latency_ms` | same names |
| `model` | `model_requested` (and `model_reported`) |
| `usage.*` | same names (disjoint semantics, §8) plus `cache_write_1h_tokens` and `estimated_fields` |
| `pricing.currency` | `pricing.currency` |
| `pricing.*_per_million` | `pricing.*_per_million_micro_usd` (integer) |
| `pricing.pricing_effective_at`, `pricing.pricing_version` | same, plus `pricing_digest`, `time_window`, `long_context_threshold` |
| `cost.input_usd`, `cache_usd`, `output_usd`, `total_usd` | `cost.input_micro_usd`, `cache_micro_usd` (= read + write), `output_micro_usd + reasoning_micro_usd`, `total_micro_usd` |
| `outcome.*` | same names; only `provider_success` stored, the rest enriched |

### 7.2 Aggregate shape (used by the Runner read API, MCP `agent_status`, and reports)

```json
{"scope": "task:sc-184",
 "total_cost_micro_usd": 2318400,
 "budget_charge_micro_usd": 2410000,
 "requests": 312,
 "tokens": {"input": 120000, "cached_input": 3400000, "cache_write": 91000, "output": 88000, "reasoning": 0},
 "unpriced_rows": 0, "estimated_rows": 1, "unknown_rows": 0,
 "cost_complete": false,
 "by_phase": {"test_authoring": 610000, "implementation": 1500000, "green_retry": 0, "review": 208400}}
```

- **`cost_complete`** is true iff every row in scope has `usage_status ∈ {reported, not_billable}` and `pricing_status ∈ {priced, not_applicable}`.
- **`total_cost_micro_usd`** sums `cost.total_micro_usd` over priced rows. When unpriced rows exist it is a **floor**. When estimated rows exist it includes their worst-case completion.
- **`budget_charge_micro_usd`** is what budgets actually counted.

Human-facing renderings must show an incomplete total as `≥ $2.32 (incomplete: 1 estimated)` or `≈ …`. A bare figure is never shown for an incomplete total (L-3).

### 7.3 Grafana data source

Grafana's PostgreSQL data source (plan 39) logs in as `grafana_ro`, a CNPG managed role (plan 12) that is a **member of the NOLOGIN group role `sc_reporting`** (`GRANT sc_reporting TO grafana_ro`, granted by the Runner's migrations). `sc_reporting` has `SELECT` only on the views in §4 and §14.5 and on `ledger.mv_daily_spend`. It has no access to `gateway_tokens`, `reservations`, `denials` or any `runner.*` table: the question views are owned by the migration role and defined with `security_invoker = false`, so they read `runner.*` with the owner's rights (39-R-4).

The data source's NetworkPolicy allows ingress `monitoring` → `sc-pg:5432` for the Grafana pod only (plan 36). Plan 39 owns the dashboard JSON. This plan owns the queries in §14.4 and §21.

## 8. Internal interfaces — usage normalization per provider dialect

Normalization produces the disjoint categories below. Each dialect is a compiled-in parser, selected by `ModelRouting.providers.<p>.apiFamilies.<f>.usageDialect`. **All field semantics marked (verify) are re-verified by recorded fixtures (53-AC-2) and real-provider smoke (53-AC-17) before a provider is enabled in production.**

| Dialect | Source of terminal usage | `input_tokens` | `cached_input_tokens` | `cache_write_tokens` (`_1h`) | `output_tokens` | `reasoning_tokens` | Reported model |
|---|---|---|---|---|---|---|---|
| `anthropic` | non-stream `usage`; SSE `message_start.message.usage` for the **input-side** fields only, then `message_delta.usage` (cumulative; take max per field). Output fields are taken **only** from `message_delta`; the placeholder `output_tokens` in `message_start` is ignored (review COST-11) | `input_tokens` (already excludes cache reads and writes) | `cache_read_input_tokens` | `cache_creation_input_tokens` (`cache_creation.ephemeral_1h_input_tokens`, verify) | `output_tokens` (includes thinking) | `null` (not reported separately) | `model` |
| `openai-chat` | non-stream `usage`; SSE final chunk with `usage` (requires `include_usage`) | `prompt_tokens − prompt_tokens_details.cached_tokens` | `prompt_tokens_details.cached_tokens` (absent → 0) | 0 | `completion_tokens − completion_tokens_details.reasoning_tokens` | `completion_tokens_details.reasoning_tokens` (absent → 0) | `model` |
| `openai-responses` | non-stream `usage`; SSE `response.completed` / `response.incomplete` / `response.failed` → `response.usage` | `input_tokens − input_tokens_details.cached_tokens` | `input_tokens_details.cached_tokens` | 0 | `output_tokens − output_tokens_details.reasoning_tokens` | `output_tokens_details.reasoning_tokens` | `response.model` |
| `deepseek-chat` (verify) | as openai-chat | `prompt_cache_miss_tokens` | `prompt_cache_hit_tokens` | 0 | `completion_tokens − reasoning_tokens` | `completion_tokens_details.reasoning_tokens` (absent → 0) | `model` |
| `zai-chat` (verify) | as openai-chat | `prompt_tokens − prompt_tokens_details.cached_tokens` | `prompt_tokens_details.cached_tokens` | 0 | `completion_tokens` (reasoning split not assumed) | `null` | `model` |
| `google` (post-MVP) | `usageMetadata` | `promptTokenCount − cachedContentTokenCount` (+ `toolUsePromptTokenCount`) | `cachedContentTokenCount` | 0 | `candidatesTokenCount` | `thoughtsTokenCount` (separate, NOT inside candidates) | `modelVersion` |

**Service-tier normalization (review COST-13).** The reported tier is mapped to the pricing enum before price selection; unmapped values make the row `unpriced` (charged at the reservation) and increment `sc_gateway_usage_inconsistent_total`:

| Dialect | Reported → pricing `serviceTier` |
|---|---|
| `anthropic` | `standard`, `standard_only` → `standard`; `priority` → `priority` (verify, VS-11) |
| `openai-chat`, `openai-responses` | `default`, `auto` (resolved to default) → `standard`; `flex` → `flex`; `priority`, `scale` → `priority` (verify) |
| `deepseek-chat`, `zai-chat` | absent → `standard` |

Pinned parameters (plan 53 §14.3 mutation 3) keep requests on `standard`; a reported `priority` despite the pin is a mismatch-like event, priced at the priority entry if one exists, else unpriced.

**Consistency checks per dialect.** A failure makes the row `usage_status: estimated` with the conservative interpretation and increments `sc_gateway_usage_inconsistent_total{provider}`:

- every derived value is ≥ 0. A subtraction underflow means the provider semantics changed, so the row treats `cached=0` and charges all prompt tokens at the full input rate;
- `deepseek-chat`: `prompt_cache_hit_tokens + prompt_cache_miss_tokens == prompt_tokens`;
- `anthropic`: `message_delta` cumulative values are ≥ the `message_start` values.

**Why disjointness matters** (L-3). OpenAI-style `prompt_tokens` *includes* cached tokens, while Anthropic's `input_tokens` *excludes* them. Passing raw fields through a single formula would double-charge one family or under-charge the other. The table above is the only place where these semantics live. Contract tests (§22) pin it.

**Outcome view expected from plan 29:** `runner.v_execution_outcomes(execution_id, task_id, worker_success bool, red_verified bool, green_verified bool, ci_passed bool, review_accepted bool, human_accepted bool, failure_class text, final_state text)`. Definitions:

- `worker_success` = execution `SUCCEEDED`;
- `red_verified` / `green_verified` = this execution's commit received RED_VERIFIED / GREEN_VERIFIED evidence;
- `ci_passed` = the green pipeline for this execution's commit succeeded;
- `review_accepted` = the task's latest review execution approved;
- `human_accepted` = the task's PR is merged (plan 55).

Also expected: `runner.tasks(task_id, repository, project, state, verification_mode, pr_number, created_at, closed_at)`, `runner.executions(execution_id, task_id, attempt, role, harness, phase, state, failure_class, routing jsonb /* model-routing $defs/RoutingDecision */, started_at, finished_at)`, `runner.repositories(repository, project, primary_language)`. Plan 26/29 own the exact names. This plan's views adapt to them and fail CI if they are missing.

## 9. Configuration

### 9.1 Pricing tables

- Location: `policy/pricing/<provider>.yaml`, one `PricingTable` per provider.
- Schema: `contracts/pricing.schema.yaml`.
- Changed by: humans via PR (procedure `pricing-update.md`). `policy/**` is a protected path, so agent tasks cannot change prices (ADR-0008).

### 9.2 Price selection algorithm

Deterministic. Inputs are `(provider, model, service_tier, instant)`.

1. Select entries with matching provider, `model`, and `serviceTier` (default `standard`) where `effectiveFrom ≤ instant < effectiveTo` (null = ∞). Zero entries → **no price** (`SC_PRICE_UNKNOWN` pre-flight; `unpriced` at settlement). More than one entry cannot happen, because the loader rejects overlap.
2. If the entry is `status: unpriced` → no price.
3. Choose the rate set:
   - If `longContextTiers` is present and `input + cached_input + cache_write > aboveInputTokens`, take the highest matching tier's rates.
   - Else, if `timeWindows` is present, evaluate the window instant (`request_start` = Gateway `started_at`; `request_end` = `finished_at`). Take UTC weekday and `HH:MM`. The first window with `day ∈ days` and `from ≤ HH:MM < to` wins. Otherwise use the base `rates`.
   - Else use the base `rates`.
4. The `instant` used for step 1 is always `started_at`, so a request never straddles two versions. Only the time-window instant follows `windowBasis`. The default and recommended value is `request_start`; DeepSeek's billing basis is marked (verify) in the procedure.
5. If usage has `cache_write_1h_tokens > 0` and the rate set lacks `cacheWrite1hPerMillion`, the row is **unpriced** (not silently priced at the 5-minute rate).

### 9.3 Cost computation

All values are integers. `r_x` is the rate in micro-USD per million tokens, parsed exactly from the decimal string. The 6-fractional-digit cap makes it an integer.

```
input_micro       = ceil(input_tokens × r_input / 1e6)
cache_read_micro  = ceil(cached_input_tokens × r_cached / 1e6)
cache_write_micro = ceil((cache_write_tokens − cache_write_1h_tokens) × r_cw / 1e6)
                  + ceil(cache_write_1h_tokens × r_cw1h / 1e6)
output_micro      = ceil(output_tokens × r_out / 1e6)
reasoning_micro   = ceil(reasoning_tokens × (r_reason ?? r_out) / 1e6)     (null tokens → 0)
cache_micro       = cache_read_micro + cache_write_micro
total_micro       = input + cache + output + reasoning
```

- Rounding is `ceil` per component (error < 1 µUSD per component; conservative direction).
- Overflow: tokens ≤ 9.2e8 per category times a rate ≤ 9.999999999e9 µUSD/M fits in int64. The Gateway rejects usage values > 1e9 as inconsistent.
- SQL `ledger.reprice()` and Go share test vectors (`testdata/pricing-vectors.yaml`), and both must produce identical results.

### 9.4 Shipped price cards

The repository ships pricing tables for every provider and model in the default `policy/model-routing.yaml`. They are filled at implementation time from the official pricing pages, with `source.url` and `source.retrievedAt`.

The repository ships **no guessed numbers**. A model whose rate cannot be verified ships as `status: unpriced`, and routing skips it (plan 31). Operators with negotiated rates override through a PR. The CI check `make check-pricing` fails if:

- any routed, enabled production model has no effective priced entry, or
- any `source.retrievedAt` is > 120 days old (warning at 90 days).

### 9.4a Test-only price cards (e2e)

Shipped with the repository and loaded in every installation, because the test-only providers `fake` and `fake-b` (plan 31 §9.2) must be priced for C05/C10 and `bootstrap verify` (unpriced → `SC_PRICE_UNKNOWN`):

- `policy/pricing/fake.yaml` (`contracts/pricing.schema.yaml` example `fake`): `fake-model` at the deterministic CI rates (input `1`, cached `0.1`, cache write `1.25`, output `2`, reasoning `4` USD/M), `fake-model-large` at 5× those rates (so escalation-cost tests see a difference), and every scenario model of the fake upstream catalogue (plan 53 §22.3: `fake-anthropic-stream-cache`, `fake-chat-no-usage`, `fake-hang`, `fake-slow`, `fake-500`, `fake-429`, `fake-529`, `fake-mismatch`, `fake-midstream-error`, `fake-huge`, `fake-hermes-watch`) at the `fake-model` rates. Version `ci-1`, `effectiveFrom: 2020-01-01T00:00:00Z`, `source.url: https://strange.company/fake-provider`.
- `policy/pricing/fake-b.yaml`: `fake-model-b` at the `fake-model` rates.
- `make check-pricing` skips the `source.retrievedAt` freshness rule for providers with `stage: test-only` and fails if any model routable by a test-only provider lacks a priced entry. These cards never affect production spend: production harnesses cannot resolve to test-only providers (plan 31 §14.2 filter 2).

### 9.5 Reporting knobs

Reporting knobs live in the chart values `ledger.reporting.*` (not in `policy/`), because they do not affect enforcement.

| Key | Default | Meaning |
|---|---|---|
| `savingsBaselineTier` | `standard` | tier used as the counterfactual for routing savings (§14.4 Q-S) |
| `rollupRefreshSeconds` | 300 | rollup refresh interval |
| `usageRawRetentionDays` | 90 | after this, `usage_raw` is set null by `ledger.prune_usage_raw()` |

## 10. Secrets

None owned. This plan uses the `sc-pg` credentials `sc_gateway` (write) and `sc_reporting` (read-only Grafana). Both are sub-credentials of `SC-PG-APP` and are requested from plan 35 (see plan 53 §10). The ledger never stores keys or tokens; `token_jti` is an identifier, not a credential.

## 11. Storage

Estimates:

- Row size ≈ 1.2 KB with `usage_raw` and ≈ 0.8 KB after pruning.
- Self-hosting-minimum load: ≈ 20 executions/day × ≈ 200 requests ≈ 4 000 rows/day ≈ 5 MB/day ≈ 1.8 GB/year before pruning, ≈ 1.2 GB/year after.
- `mv_daily_spend` is negligible.

Retention:

- `cost_records`: 3 years minimum (SPEC §107 allows long retention). Pruning older rows requires an explicit operator call to `ledger.prune_cost_records(before)` under plan 43, and is never automatic in MVP.
- `pricing_snapshots`: forever.

`sc-pg` sizing (plan 12) must include 2 GB/year of ledger growth. The alert `SCCnpgDatabaseLarge` (plan 12/42) covers exhaustion.

## 12. Resource requirements

Runs inside the Gateway (plan 53 §12) and `sc-pg`. The rollup refresh costs < 1 s CPU per 5 min at the estimated volume. No additional pods.

## 13. Security boundary

- The ledger contains no prompt or completion content. `usage_raw` stores only the provider usage object, and the Gateway extracts it by key path (it never stores the response body).
- Write access is limited to `sc_gateway`, INSERT only on `cost_records`. Read access is limited to `sc_runner` and `sc_reporting`.
- `sc_reporting` cannot read `ledger.gateway_tokens` (claims digests) or `ledger.denials`.
- Repository names and task IDs appear in the ledger. They are not secrets, but they are not exported as metric labels (§21).

## 14. Happy path

### 14.1 Write path (per request; executed by plan 53)

1. The usage tap yields observed usage.
2. Normalization (§8) is applied.
3. Price selection (§9.2) and cost computation (§9.3) run.
4. The settlement transaction inserts the row and the snapshot.

### 14.2 Aggregation hierarchy and views

Each level is a `GROUP BY` over `v_cost_records_enriched`. Every level reports `cost_complete` with the §7.2 shape.

| SPEC §80 level | Key | View / expression |
|---|---|---|
| API invocation | `record_id` | `ledger.v_cost_records_enriched` |
| worker execution | `execution_id` | `ledger.v_execution_cost` |
| attempt | `(task_id, role, attempt)` | `v_execution_cost` grouped by these |
| TDD phase | `(task_id, phase)` | `v_task_cost.by_phase` |
| task | `task_id` | `ledger.v_task_cost` |
| PR | `runner.tasks.pr_number` (1:1 with task) | `v_task_cost` joined to the PR |
| repository | `repository` | `mv_daily_spend` / views |
| project | `project` | same |
| day / month | `date_trunc('day'|'month', started_at AT TIME ZONE 'UTC')` | `mv_daily_spend` |

`ledger.mv_daily_spend` holds:

- the key `(day, token_kind, provider, model_requested, harness, role, phase, tier_effective, repository, project)`;
- the totals `requests`, `input_tokens`, `cached_input_tokens`, `cache_write_tokens`, `output_tokens`, `reasoning_tokens`, `total_cost_micro_usd`, `budget_charge_micro_usd`;
- the counts `unpriced_rows`, `estimated_rows`, `unknown_rows`.

Its unique index on the key enables `REFRESH MATERIALIZED VIEW CONCURRENTLY`.

Completeness expression, reused in every view:

```sql
bool_and(usage_status IN ('reported','not_billable') AND pricing_status IN ('priced','not_applicable')) AS cost_complete
```

### 14.3 Enriched view sketch

```sql
CREATE VIEW ledger.v_cost_records_enriched AS
SELECT c.*, 'enriched' AS record_view_e, t.pr_number AS pr_e,
       o.worker_success, o.red_verified, o.green_verified, o.ci_passed, o.review_accepted, o.human_accepted,
       e.routing->>'tier_requested' AS routing_tier_requested, e.failure_class, e.state AS execution_state,
       r.primary_language
FROM ledger.cost_records c
LEFT JOIN runner.executions e USING (execution_id)
LEFT JOIN runner.tasks t ON t.task_id = c.task_id
LEFT JOIN runner.v_execution_outcomes o USING (execution_id)
LEFT JOIN runner.repositories r ON r.repository = c.repository;
```

### 14.4 Queries answering each SPEC §80 question (sketches; `C` = `ledger.v_cost_records_enriched`)

**Q1. What did this PR cost?**

```sql
SELECT t.pr_number, sum(c.cost_total_micro_usd) cost, sum(c.budget_charge_micro_usd) charged,
       bool_and(<complete>) cost_complete
FROM C c JOIN runner.tasks t USING (task_id)
WHERE t.repository = $repo AND t.pr_number = $pr GROUP BY t.pr_number;
```

**Q2. How much was spent authoring its tests?** Same as Q1 with `AND c.phase = 'test_authoring'`.

**Q3. How much was spent implementing it?** Same as Q1 with `AND c.phase IN ('implementation','green_retry')`, also reported split by phase.

**Q4. How much did review cost?** Same as Q1 with `AND c.phase = 'review'`.

**Q5. Which model has the lowest cost per green task?** A green task is a task with ≥1 implementer execution where `green_verified`. The task is credited to the model of its green-producing execution. The numerator is all implementation-phase spend of those tasks.

```sql
WITH green AS (
  SELECT DISTINCT ON (task_id) task_id, routing->>'model_id' AS model_id
  FROM runner.executions e JOIN runner.v_execution_outcomes o USING (execution_id)
  WHERE e.role='implementer' AND o.green_verified ORDER BY task_id, e.finished_at)
SELECT g.model_id, count(DISTINCT g.task_id) green_tasks,
       sum(c.cost_total_micro_usd)/count(DISTINCT g.task_id) AS cost_per_green_task,
       bool_and(<complete>) cost_complete
FROM green g JOIN C c ON c.task_id=g.task_id AND c.phase IN ('implementation','green_retry')
WHERE c.started_at >= $from GROUP BY g.model_id ORDER BY cost_per_green_task;
```

**Q6. How often does the bargain tier succeed?** Success is defined per producing role (review COST-20): tester → its commit was `red_verified`; implementer → `green_verified`; researcher/planner → `worker_success`. The reviewer is excluded: its output is a judgement, not a product that can succeed or fail on its own. Executions not yet judged (outcome null) are excluded.

```sql
SELECT e.role,
       count(*) FILTER (WHERE CASE e.role WHEN 'tester' THEN o.red_verified
                                          WHEN 'implementer' THEN o.green_verified
                                          ELSE o.worker_success END)::float / count(*) AS success_rate,
       count(*) n
FROM runner.executions e JOIN runner.v_execution_outcomes o USING (execution_id)
WHERE e.routing->>'tier_effective'='bargain' AND e.role <> 'reviewer'
  AND e.state IN ('SUCCEEDED','FAILED','TIMED_OUT')
  AND (e.failure_class IS NULL OR e.failure_class='merit')   -- infra/budget failures excluded
  AND CASE e.role WHEN 'tester' THEN o.red_verified WHEN 'implementer' THEN o.green_verified
                  ELSE o.worker_success END IS NOT NULL
GROUP BY e.role;
```

**Q7. How much does escalation cost?** Escalation cost is the spend of escalated executions (`escalation_rung > 0`, which also counts escalations that were capped by a repository override or that Hermes requested explicitly; review COST-20) plus the spend of the failed lower-rung attempts of the same task and role that preceded them.

```sql
WITH esc_tasks AS (SELECT DISTINCT task_id, role FROM runner.executions WHERE (routing->>'escalation_rung')::int > 0)
SELECT sum(c.cost_total_micro_usd) FILTER (WHERE (e.routing->>'escalation_rung')::int > 0) AS escalated_spend,
       sum(c.cost_total_micro_usd) FILTER (WHERE (e.routing->>'escalation_rung')::int = 0 AND e.failure_class='merit') AS failed_pre_escalation_spend,
       count(DISTINCT et.task_id) tasks_escalated
FROM esc_tasks et JOIN runner.executions e USING (task_id, role) JOIN C c USING (execution_id)
WHERE c.started_at >= $from;
```

**Q8. Is a stronger model (e.g. Sonnet) cheaper per accepted task than repeated cheaper attempts?** Compare cost per accepted task (`human_accepted`), grouped by the **starting** tier/model of the implementer ladder. The starting point is the task's first implementer execution's `routing->>'model_id'`, and all implementation-phase spend is included.

```sql
WITH accepted AS (SELECT DISTINCT task_id FROM runner.v_execution_outcomes WHERE human_accepted),   -- one row per task (review COST-9)
first_impl AS (SELECT DISTINCT ON (task_id) task_id, routing->>'model_id' start_model, routing->>'tier_effective' start_tier
               FROM runner.executions WHERE role='implementer' ORDER BY task_id, attempt),
impl_cost AS (SELECT task_id, sum(cost_total_micro_usd) cost, bool_and(<complete>) complete
              FROM C WHERE phase IN ('implementation','green_retry') GROUP BY task_id),
impl_attempts AS (SELECT task_id, count(*) attempts FROM runner.executions WHERE role='implementer' GROUP BY task_id)
SELECT f.start_tier, f.start_model, count(*) accepted_tasks,
       sum(ic.cost)/count(*) cost_per_accepted_task, avg(ia.attempts) avg_impl_attempts, bool_and(ic.complete) cost_complete
FROM first_impl f JOIN accepted a USING (task_id) JOIN impl_cost ic USING (task_id) JOIN impl_attempts ia USING (task_id)
GROUP BY 1,2;
```

Every join is 1:1 per task, so no cost row is multiplied.

**Q9. Which harness performs best for this repository/language?**

```sql
SELECT c.repository, c.primary_language, c.harness,
       count(DISTINCT c.task_id) FILTER (WHERE c.green_verified) green_tasks,
       count(DISTINCT c.execution_id) executions,
       sum(c.cost_total_micro_usd)/NULLIF(count(DISTINCT c.task_id) FILTER (WHERE c.green_verified),0) cost_per_green_task
FROM C c WHERE c.token_kind='execution' AND c.role='implementer'
GROUP BY 1,2,3;
```

**Q10. How much money was spent on failed work?** Failed work = spend of executions whose **output did not survive** (review COST-9), broken down by reason:
- `worker_failed`: the execution itself ended `FAILED`/`TIMED_OUT`/`CANCELLED`;
- `red_invalid`: a tester execution that succeeded but whose commit was not `red_verified` (RED invalid / `TEST_NEEDS_WORK`);
- `green_failed`: an implementer execution that succeeded but whose commit was not `green_verified`;
- `review_rejected`: an implementer execution whose commit received review `changes_requested`;
- `task_abandoned`: any execution of a task that ended `CANCELLED` or whose PR was closed unmerged.

Outcome booleans are null while unjudged (plan 29 view contract), and unjudged executions are excluded. An execution is counted under its first matching reason in the order above.

```sql
SELECT reason, sum(c.cost_total_micro_usd) spend, count(DISTINCT c.execution_id) executions, bool_and(<complete>) cost_complete
FROM (SELECT c.*, CASE
        WHEN c.execution_state IN ('FAILED','TIMED_OUT','CANCELLED')                    THEN 'worker_failed'
        WHEN c.role='tester'      AND c.red_verified   = false                          THEN 'red_invalid'
        WHEN c.role='implementer' AND c.green_verified = false                          THEN 'green_failed'
        WHEN c.role='implementer' AND c.review_accepted = false                         THEN 'review_rejected'
        WHEN t.state='CANCELLED' OR (t.closed_at IS NOT NULL AND NOT coalesce(c.human_accepted,false)) THEN 'task_abandoned'
      END AS reason
      FROM C c JOIN runner.tasks t USING (task_id) WHERE c.started_at >= $from AND c.token_kind='execution') c
WHERE reason IS NOT NULL GROUP BY reason;
```

**Q-S. Estimated routing savings** (SPEC §92). For each implementer green task whose green-producing execution ran below `savingsBaselineTier`:

- the counterfactual is `ledger.reprice(record, digest_of(baseline tier primary candidate))`, summed over that execution's rows;
- the actual is all implementation-phase spend of the task;
- savings = Σ(counterfactual − actual), which can be negative.

The assumption (the baseline model would use the same tokens and succeed in one attempt) is printed on the dashboard panel.

### 14.5 Question views (consumed by Grafana, plan 39 §7.4)

Each §14.4 query is wrapped as a view in schema `ledger`, created by the Runner's migrations (CD-04), owned by the migration role, `WITH (security_invoker = false)`, and granted `SELECT` to `sc_reporting` only. Weekly views bucket by `date_trunc('week', <anchor> AT TIME ZONE 'UTC')`; the anchor is the green-producing or accepting execution's `finished_at`. Every view carries `cost_complete` where it sums money.

| View | Source query | Columns |
|---|---|---|
| `ledger.v_pr_cost_by_phase` | Q1–Q4 | `repository, pr_number, task_id, phase, cost_micro_usd, charged_micro_usd, cost_complete` |
| `ledger.v_cost_per_green_task` | Q5 | `week, model_id, green_tasks, cost_per_green_task_micro_usd, cost_complete` |
| `ledger.v_tier_success` | Q6 (all tiers, not only bargain) | `week, tier, role, n, success_rate` |
| `ledger.v_escalation` | Q7 + escalation frequency | `week, escalation_ratio, escalated_spend_micro_usd, failed_pre_escalation_spend_micro_usd, tasks_escalated` |
| `ledger.v_cost_per_accepted_task` | Q8 | `week, start_tier, start_model, accepted_tasks, cost_per_accepted_task_micro_usd, avg_impl_attempts, cost_complete` |
| `ledger.v_harness_performance` | Q9 | `repository, primary_language, harness, green_tasks, executions, cost_per_green_task_micro_usd, cost_complete` |
| `ledger.v_failed_work_spend` | Q10 (daily, wide) | `day, failed_executions_micro_usd` (= `worker_failed`), `cancelled_tasks_micro_usd` (= `task_abandoned`), `infra_failures_micro_usd`, plus `red_invalid_micro_usd, green_failed_micro_usd, review_rejected_micro_usd, total_failed_micro_usd, cost_complete` (the first three keep plan 39's columns) |
| `ledger.v_routing_savings` | Q-S | `week, tasks, savings_micro_usd, baseline_tier` |

The Q-S view reads the baseline digest from a one-row table `ledger.reporting_settings(savings_baseline_tier, baseline_pricing_digest)` that the Gateway leader updates from `ledger.reporting.savingsBaselineTier` and the routing policy at each policy load.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Provider changes usage semantics | dialect consistency checks; `SCModelUsageUnknown`; `sc_gateway_usage_inconsistent_total` → **`SCUsageNormalizationInconsistent`** (warning) | rows `estimated` (conservative) | none | `gateway-failure.md` §usage |
| Price card missing or expired for a routed model | `make check-pricing` in CI; runtime gauge `sc_routing_unpriced_candidates > 0` → **`SCPricingMissingForRoutedModel`** (critical) | requests denied (`SC_PRICE_UNKNOWN`); routing skips the model | none | `pricing-update.md` |
| Entry expiring with no successor | `sc_pricing_entry_expiry_timestamp_seconds - time() < 7d` → **`SCPricingEntryExpiringSoon`** (warning) | none yet | none | `pricing-update.md` |
| Malformed pricing pushed | CI schema + loader rejects; at runtime `SCGatewayPolicyInvalid` | last-known-good kept | none | `pricing-update.md` |
| Provider price changed but card not updated | monthly reconciliation (§20) differs by > 3 % | ledger inaccurate | none | `pricing-update.md` §reconcile |
| Rollup refresh fails | `sc_ledger_rollup_age_seconds > 900` → **`SCLedgerRollupStale`** (warning) | dashboards stale; exact views still correct | retried every cycle | `gateway-failure.md` §ledger |
| Outcome view missing (plan 29 not deployed) | enriched view returns null outcomes; CI contract test | Q5–Q10 return empty | none | — |

## 16. Retry behavior

Pricing and normalization are pure functions and have no retries. Writes follow plan 53 §16 (settlement retry, then sweeper). Rollup refresh is retried on the next 5-minute cycle.

## 17. Idempotency behavior

- `cost_records` has `UNIQUE(reservation_id)`, so at most one row exists per reservation.
- The pricing snapshot is insert-if-absent by digest.
- Rollup refresh is idempotent.
- `reprice()` is a pure function (`IMMUTABLE` over snapshot data).

## 18. Bootstrap behavior

- The **Runner** creates the `ledger` schema, tables, trigger, views and grants in its in-process, forward-only migrations at startup (CD-04). The Gateway never migrates; it waits for the required schema version (plan 53 §6).
- The pricing ConfigMap must validate before the Gateway becomes ready.
- At first deploy the `fake` provider's pricing (`policy/pricing/fake.yaml`) exists, so C05/C10 fake flows are priced.
- No historical data is needed.

## 19. Upgrade behavior

- Ledger columns are added additively (expand/contract). New usage categories (e.g. a future provider "audio tokens") are added as new nullable columns, a new rate field (optional in the pricing schema), a dialect update and fixtures. Existing rows remain valid.
- A pricing schema change that is breaking bumps to `v1alpha2` with a converter in F04 (master plan §6.2).
- Version-sensitive provider semantics are listed in plan 53 §19 (VS-1…VS-4) and re-verified by 53-AC-17.

## 20. Recovery behavior

- **Ledger:** restored with `sc-pg` PITR. Rows after the recovery point are lost. Lost spend is reconstructed only approximately, from provider consoles. The operator records it as a `ledger.cost_adjustments` row (append-only: `adjustment_id, period, provider, delta_micro_usd, reason, created_by, created_at, applied_at`, written by the Runner admin API, plan 26, and applied to budget scopes by `ledger.apply_cost_adjustment()`, plan 33 §20). Adjustments appear as a separate line in reports and count towards provider and global month scopes (plan 33 §20).
- **Monthly reconciliation** (procedure `pricing-update.md` §Reconcile): compare `SELECT provider, sum(cost_total_micro_usd) FROM ledger.cost_records WHERE started_at in month` with the provider invoice or console. A difference > 3 % (or > $5) opens an issue labelled `ledger-discrepancy`.
- `mv_daily_spend` is rebuilt with `REFRESH MATERIALIZED VIEW`.

## 21. Observability

### 21.1 Prometheus (low cardinality; no task, execution, repository or jti labels — [SPEC §91])

| Metric | Type | Labels | Source |
|---|---|---|---|
| `sc_model_spend_micro_usd_total` | counter | provider, model, harness, role, phase, tier, exactness (`exact`, `estimated`, `unpriced`) | Gateway at settlement (`budget_charge_micro_usd`) |
| `sc_model_cost_micro_usd_total` | counter | provider, model, harness, role, phase, tier | Gateway (priced `cost.total` only) |
| `sc_model_tokens_total` | counter | provider, model, kind (`input`, `cached_input`, `cache_write`, `output`, `reasoning`) | Gateway |
| `sc_ledger_spend_micro_usd` | gauge | period (`today`, `month`), provider | Gateway leader, from the DB every 60 s (survives restarts). Sums `budget_charge_micro_usd` (what budgets counted, conservative for incomplete rows), matching `sc_model_spend_micro_usd_total` (review COST-17) |
| `sc_ledger_spend_by_repository_micro_usd` | gauge | period (`month`), repository | leader, from DB. Bounded by managed repos; top 20 by spend, rest folded into `_other` |
| `sc_ledger_incomplete_rows` | gauge | period (`today`, `month`), kind (`unpriced`, `estimated`, `unknown`) | leader |
| `sc_ledger_rollup_age_seconds` | gauge | — | leader |
| `sc_pricing_entry_expiry_timestamp_seconds` | gauge | provider, model | Gateway (entries with non-null effectiveTo and no successor) |
| `sc_routing_unpriced_candidates` | gauge | tier | Gateway |

### 21.2 Economics dashboard data sources (plan 39 builds; SPEC §92)

| Panel | Source | Query |
|---|---|---|
| Spend today / this month | Prometheus | `sum(sc_ledger_spend_micro_usd{period="today"})/1e6` (+ `_month`); annotate with `sum(sc_ledger_incomplete_rows{period="today"}) > 0` as "incomplete" |
| Model / provider spend (time series) | Prometheus | `sum by (provider, model) (increase(sc_model_spend_micro_usd_total[1h]))/1e6` |
| Harness spend | Prometheus | `sum by (harness) (increase(sc_model_spend_micro_usd_total[$__range]))/1e6` |
| Repo spend | PostgreSQL `sc-ledger` | `SELECT repository, sum(total_cost_micro_usd)/1e6 FROM ledger.mv_daily_spend WHERE day >= $from GROUP BY 1` |
| Cost per accepted task / per green task | PostgreSQL | Q8 and Q5 aggregates (all models combined, and per model) |
| Cheap-tier success | PostgreSQL | Q6 |
| Escalation frequency | PostgreSQL | `count(*) FILTER (WHERE routing->>'tier_source'='escalated')::float / count(*)` over implementer executions per week |
| Test-authoring vs implementation spend | Prometheus | `sum by (phase) (increase(sc_model_spend_micro_usd_total{phase=~"test_authoring|implementation|green_retry|review"}[$__range]))` |
| Estimated routing savings | PostgreSQL | Q-S |
| Ledger completeness | Prometheus | `sc_ledger_incomplete_rows` |

**Alerts introduced:** `SCPricingMissingForRoutedModel`, `SCPricingEntryExpiringSoon`, `SCUsageNormalizationInconsistent`, `SCLedgerRollupStale` (IDs for plan 42).

## 22. Testing strategy

- **Unit.** Pricing vectors (`testdata/pricing-vectors.yaml`, ≥ 40 cases) cover:
  - each window boundary (`from` inclusive, `to` exclusive, `24:00`, every weekday, the Sunday→Monday transition);
  - both `windowBasis` values;
  - the long-context threshold ±1;
  - 1h cache writes with and without the rate;
  - reasoning with and without a separate rate;
  - unpriced status;
  - an effective-interval boundary;
  - ceil rounding;
  - overflow guard.

  The same vectors run against the Go pricer and against SQL `ledger.reprice()` in a test Postgres. Results must be identical.
- **Loader negative tests.** Every `x-invalid-examples` entry in `pricing.schema.yaml`, plus semantic rejects: overlapping windows, overlapping effective intervals, duplicate versions, `from ≥ to`, and decreasing long-context thresholds.
- **Normalization contract tests.** Recorded fixtures (plan 53 §22.2), one `expected.yaml` per fixture, with the dialect table in §8 as the oracle. A test asserts the **double-charge regression**: for an OpenAI chat fixture with `prompt_tokens=1000, cached_tokens=800`, the result is `input=200, cached=800`, and cost = 200×r_in + 800×r_cached (not 1000×r_in + 800×r_cached).
- **Ledger DB tests.**
  - The immutability trigger rejects UPDATE/DELETE by `sc_gateway` and by `sc_runner`.
  - `sc_reporting` cannot `SELECT ledger.gateway_tokens`.
  - Every view compiles against the plan 26/29 schema (CI fails if a referenced column is missing).
- **Aggregation tests.** A synthetic dataset (`testdata/ledger-seed.sql`: 3 tasks, 12 executions, ladders, an escalation, a failure, an unpriced row and an estimated row) has golden answers for Q1–Q10 and Q-S, plus `cost_complete` flags.
- **Real-provider re-verification.** Opt-in `make smoke-providers` (plan 53 §22.5).

## 23. Acceptance criteria

### GATE-C05

- **32-AC-1** `make contracts` passes for `cost-record.schema.yaml` and `pricing.schema.yaml`: meta-schema valid, all `examples` validate, all `x-invalid-examples` fail.
- **32-AC-2** The policy loader rejects each semantic-invalid pricing case of §22 with a non-zero exit and an error naming the file and entry (`go test ./... -run TestPricingLoaderRejects` passes). In k3d, pushing a policy ConfigMap with an overlapping window leaves `sc_gateway_policy_info{pricing_rev}` unchanged and increments `sc_gateway_policy_load_errors_total{file=~".*pricing.*"}`.
- **32-AC-3** The pricing vectors pass identically in Go and SQL (`make test-pricing-vectors` exit 0). They include a DeepSeek-style case: same usage at Mon 02:00 UTC = 2× the cost at Mon 05:00 UTC.
- **32-AC-4** The normalization fixture suite passes for the dialects `anthropic`, `openai-chat`, `openai-responses`, `deepseek-chat`, `zai-chat`. The double-charge regression test passes.
- **32-AC-5** In k3d, after the plan 53 fixture suite, `SELECT count(*) FROM ledger.cost_records WHERE pricing_status='priced' AND cost_total_micro_usd <> cost_input_micro_usd+cost_cache_micro_usd+cost_output_micro_usd+cost_reasoning_micro_usd` = 0, and every row validates against `cost-record.schema.yaml` when exported with `ledger.export_cost_record(record_id)` (JSON).
- **32-AC-6** Immutability: `UPDATE ledger.cost_records SET cost_total_micro_usd=0` as `sc_gateway` and as `sc_runner` fails with a permission or trigger error.
- **32-AC-7** Historical reproducibility: change the fake model's price card (new version effective now), make a request, then:
  - `SELECT DISTINCT pricing_version FROM ledger.cost_records WHERE model_requested='fake-model'` returns both versions;
  - for every row, `ledger.reprice(record_id, pricing_digest) = cost_total_micro_usd`;
  - the old version's snapshot is still present after the Git entry is removed.
- **32-AC-8** Golden aggregation: loading `testdata/ledger-seed.sql` makes Q1–Q10 and Q-S return the golden results (`make test-ledger-queries`). The golden values in `testdata/ledger-golden.yaml` are **hand-computed and committed independently of the SQL** (review COST-9), with the arithmetic shown per value. The seed includes an accepted task with ≥ 3 implementer executions (Q8 must report its implementation spend once, not ×3), a succeeded implementer execution with a failed GREEN, a tester execution with RED invalid, a review `changes_requested`, and a PR closed unmerged (each appears exactly once in Q10 under its reason). The task containing the unpriced row reports `cost_complete=false`.
- **32-AC-9** The Runner read API `GET /tasks/sc-184` (seed data) returns a `cost` object matching §7.2, with `cost_complete=false` for the incomplete task (plan 26 contract test).
- **32-AC-10** Prometheus label hygiene: `promtool` plus a test scrape of the Gateway shows no metric with a label named `task_id`, `execution_id`, `jti` or `repository`, except `sc_ledger_spend_by_repository_micro_usd` (≤ 21 series).
- **32-AC-11** `sc_reporting` can `SELECT` from the four views and `mv_daily_spend`, and gets `permission denied` on `ledger.gateway_tokens`, `ledger.reservations` and `ledger.denials`.
- **32-AC-12** `make check-pricing` fails on a routing file containing an enabled production model with no effective priced entry, and passes on the shipped policy.
- **32-AC-13** Question views: with `testdata/ledger-seed.sql` loaded, each §14.5 view returns the same rows as its §14.4 query (`make test-ledger-queries` compares both). Connected as `grafana_ro`, `SELECT * FROM ledger.v_cost_per_green_task` succeeds, while `SELECT 1 FROM runner.executions LIMIT 1` and `SELECT 1 FROM ledger.reservations LIMIT 1` fail with `permission denied`; `SELECT pg_has_role('grafana_ro','sc_reporting','member')` is true.
- **32-AC-14** Test-only pricing: every model id of the fake upstream catalogue and of the `e2e-test` routing example has an effective priced entry (`make check-pricing` on the shipped tree), and a C05 fake request to `fake-b/fake-model-b` yields a row with `pricing_status='priced'`.

## 24. Explicit non-goals

- Automatic invoice ingestion from provider billing APIs (most providers have none per request; L-3). Reconciliation is manual and monthly.
- Currency other than USD.
- Charging for CI compute or cluster infrastructure (SPEC §80 is model spend).
- Per-request cost from harness self-reports. Those are an informational cross-check only (plan 28).
- Retroactive mutation of ledger rows after price corrections (use `reprice()` for reports and `cost_adjustments` for budget-relevant corrections).

## 25. Open questions

1. **Billing basis for time-windowed rates (request start vs completion) for DeepSeek.** Blocking: no. Proposed: `windowBasis: request_start` by default; the procedure verifies against the provider docs when the card is written. Owner: plan 32 at implementation.
2. **`cost_adjustments` admin endpoint.** Resolved: plan 26 provides `POST /v1/admin/ledger/adjustments` (loopback admin listener, `SC-RUNNER-ADMIN`). It inserts the row and calls `ledger.apply_cost_adjustment()` in the same transaction (`SECURITY DEFINER`, plan 33 §20), which updates the budget `spent` columns. Owner: plans 26/33.
3. **Outcome view names** depend on plans 26/29. Blocking: no; the CI contract test catches drift. Owner: plans 29/32.
4. **"Estimated routing savings" is a counterfactual metric** whose assumption may mislead. Blocking: no. Proposed: label it on the dashboard; revisit after 3 months of data. Owner: plan 39/32.
5. **Outcome booleans must be null while unjudged** (Q6/Q10). `runner.v_execution_outcomes` (plan 29) must return null, not false, for `red_verified`, `green_verified` and `review_accepted` until the corresponding verification or review has concluded. Blocking: no. Owner: plan 29.
6. **Specification cost excludes Hermes** (review COST-9): acceptance criteria written by Hermes are charged to `orchestration` without a task. Recorded as a SPEC interpretation (spec-issues, lead). Blocking: no. Owner: plans 32/33.
