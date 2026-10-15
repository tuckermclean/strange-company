# Blind review: model spend (Gateway, budgets, routing, accounting, emergency stop)

Reviewer: blind adversarial reviewer, area `BUD`. Date: 2026-10-14.

## 1. Scope read

Only `plans/reviews/consolidation-decisions.md` was read from `plans/reviews/` (binding). No other review or closure report was used for findings.

- SPEC.md §75–§82, §86, §88.
- Plans 53 (all), 33 (all), 32 (§1–§14, §20 recovery/reconciliation, §21 skim), 31 (grep-level: fallback, auxiliary, orchestrator, quarantine), 29 §15.2 (failure classification), 28 (grep: base URL, telemetry, model pinning, retries), 26 (grep: dispatch, concurrency, classification), 24 (grep: Hermes credentials, auxiliary, watcher), 35/36/45/50 (grep: model keys, egress proxy policy, fencing).
- Contracts: model-routing (Model, requestPolicy, inputBound, examples), gateway-token (invariants), gateway-status, budget-policy (invariants, examples), cost-record and pricing (skim via plan 32).
- ADR-0004, 0450, 0451, 0452.
- Procedures: budget-emergency-stop (all), gateway-failure (all), pricing-update (all).

## 2. Verdict

**PASS WITH FIXES.** The reservation design is sound: atomic worst-case admission in one transaction, hard `perRequest` ceiling, fail-closed on DB loss, and a three-source stop whose Kubernetes path needs neither the Runner, the DB nor Flux. No blocker found. There are six majors, mostly places where the "provable ceiling" rests on a mechanism that is named but not specified, or on a third-party fact whose planned verification would not detect the failure.

## 3. Findings (by severity)

| ID | Sev | Location | One-line |
|---|---|---|---|
| BUD-B1 | major | plan 53 §7.1 (`count_tokens` row), §7.3, §14.1 | `count_tokens` is an un-inspected, un-reserved, un-throttled pass-through that bypasses the allowlist, e-stop and model checks |
| BUD-B2 | major | plan 53 §14.1 steps 1/3, §9, §12; plan 33 §9.1 | Request body is buffered (up to 32 MiB) before authentication; 128 in-flight × 32 MiB vs a 256 Mi limit lets any pod in `agent-jobs` OOM both replicas |
| BUD-B3 | major | plan 53 §8 `request inspector`, §14.3, §14.4 | JSON inspection semantics are unspecified; parser differentials (duplicate keys, case, numeric forms, nesting) can defeat the cap, model and content checks |
| BUD-B4 | major | plan 53 §22.5, §23 53-AC-17, VS-8 | `cap_covers_reasoning` evidence cannot be produced by the specified smoke (one 16-token request) |
| BUD-B5 | major | plan 53 §14.1 step 6, 53-AC-36; model-routing `imageTokensUpperBound` | Image token bound is per family, unverified by the corpus, and a violation quarantines the whole model |
| BUD-B6 | major | plan 33 §24, ADR-0450 alternatives, plan 32 §20, pricing-update §E | No independent out-of-band spend backstop; price or billing drift is detected only by a monthly manual comparison |
| BUD-B7 | minor | plan 53 §22.5, plan 32 §9 | Operator-run smoke/fixture/corpus calls use raw keys outside the ledger and budgets |
| BUD-B8 | minor | plan 33 §7.1, §9.1 | Fixed UTC hour/day windows allow 2× the cap in minutes; no automatic trip on anomaly |
| BUD-B9 | minor | budget-emergency-stop A1–A4; plan 33 §14.6 | The stop procedure assumes kubectl and the k3s API; key revocation is the last step and waits 5 min |
| BUD-B10 | minor | plan 33 §14.6, §13 | Fail-safe direction not stated for a missing/NULL/unknown DB e-stop row |
| BUD-B11 | minor | plan 53 §14.4 | Any non-2xx before body is `not_billable` / 0 even for 5xx/504 from a CDN in front of a request the provider processed |
| BUD-B12 | minor | plan 53 §15, plan 33 §14.2 | Reservation expiry (start+total+600 s ≈ 70 min) orphans headroom and phantom-charges worst case after a crash |
| BUD-B13 | minor | plan 32 §2, §14.4; plan 33 §25 Q7 | Hermes spend is untied to tasks: §80 Q1–Q4 and Q10 under-report by design |
| BUD-B14 | minor | plan 32 §14.4 Q8 | Q8 compares tasks, not matched work; selection bias makes the answer unreliable |
| BUD-B15 | minor | plan 29 §15.2; plans 26/27/33 | Gateway denial codes `SC_MODEL_QUARANTINED`, `SC_PRICE_UNKNOWN`, `SC_MODEL_NOT_ALLOWED`, `SC_REQUEST_FEATURE_DENIED`, `SC_MAX_TOKENS_EXCEEDS_POLICY`, `SC_PROVIDER_UNAVAILABLE` have no execution classification |
| BUD-B16 | minor | plan 53 §14.1 step 6, VS-12 | `contextWindowTokens` and the "1h cache write only if present" rule are per-model facts verified per family |
| BUD-B17 | minor | `contracts/budget-policy.schema.yaml` examples; `gateway-token` examples | Example values contradict shipped defaults and invariant 10 (copy-paste hazard) |

### BUD-B1 (major): `count_tokens` bypasses the inspector, the e-stop and the model allowlist

**Where.** Plan 53 §7.1 row `/p/{provider}/v1/messages/count_tokens`: "no (pass-through, counted in metrics, no reservation, no cost record)". No other text in plans, contracts, ADRs or procedures mentions it (grep for `count_tokens` finds only that row and the §14.1 alternatives paragraph).

**Scenario.** Pipeline §14.1 is written for billable routes. The route is not said to run authn step 3, inspect step 4 (allowlist, always-denied list), the e-stop check (which is in the reservation transaction, step 7, that this route skips), the `allowed_models` check or any per-token rate limit. Consequences:
1. A hostile worker (it holds a valid gateway token and has no internet) sends `count_tokens` with an image `source.type: url` or `mcp_servers`. The provider fetches that URL to count it: an exfiltration and SSRF channel from a pod with no egress, exactly the thing COST-1/SPEC §88 closes for billable routes (53-AC-28 only tests the billable routes).
2. During an emergency stop, `count_tokens` keeps working: "stopped" workers still reach the provider with the real key. The provider's separate count_tokens rate limit (shared across the whole key) can be exhausted by one worker, degrading every harness, including Hermes.
3. Claude Code does call this endpoint, so denying the route outright is not available.

**Fix.** Plan 53 §7.1/§14.1: define `count_tokens` as a *non-billable inspected* route: same steps 1–5 (authn including revocation lookup, route family, `allowed_models`, the full allowlist and always-denied list, body-size cap), plus the e-stop check (deny at the same levels as billable routes) and a per-token rate limit (new policy key, e.g. `perExecution.maxCountTokensPerMinute`) with a `ledger.denials` / metric row. Add 53-AC cases: a `count_tokens` request with a URL image, with `mcp_servers`, with a revoked token, with a model outside `allowed_models`, and during e-stop each returns the documented denial with zero upstream contact.

### BUD-B2 (major): pre-authentication body buffering can OOM both replicas

**Where.** Plan 53 §14.1 step 1 ("read the body up to `maxRequestBodyBytes`") precedes step 3 (authenticate). `perRequest.maxRequestBodyBytes` ships as 33 554 432 (plan 33 §9.1). `limits.maxInflightPerReplica` is 128 and the pod limit is 256 Mi (§9, §12). §12's memory budget counts only the 8 pooled non-streaming response parse buffers.

**Scenario.** Any pod in `agent-jobs` (hostile repository code, no token needed) opens ~10 concurrent connections that each send a 30 MiB body slowly or fast. Both replicas hit the 256 Mi limit, are OOMKilled and restart in a loop. Fail-safe for spend, but Hermes and every worker lose model access (the Gateway is the only path), and `gateway-failure.md` §down offers only "raise memory". Every in-flight request killed by the OOM leaves an `open` reservation that the sweeper settles **at worst case** up to ~70 min later (BUD-B12), so the attacker also drains `global-hour`/`global-day` with phantom charges, blocking the legitimate workload even after the pods recover. The plan's own 128 × 32 MiB = 4 GiB arithmetic shows the configuration cannot hold.

**Fix.** Plan 53 §14.1: authenticate (JWS verify only, no DB) from headers before reading the body; reject on failure with zero body read. Read the body through a global byte semaphore (`limits.maxBufferedBodyBytes`, default well under the memory limit, e.g. 96 MiB) and a per-token byte allowance; excess gets 429 `SC_GATEWAY_OVERLOADED`. Set a default `maxRequestBodyBytes` that matches the largest legitimate request (the 53-AC-35 replay gives the number; 32 MiB is 30× a 100 KB Claude Code request). Add an AC: 20 concurrent 30 MiB unauthenticated bodies leave the Gateway Ready with no OOM, and a legitimate request still succeeds.

### BUD-B3 (major): the inspector's parsing rules are not specified, and the hard ceiling depends on them

**Where.** Plan 53 §8 (`request inspector`), §14.3 ("raw JSON pass-through"), §14.1 step 4. The text says unknown members are denied and the body is re-emitted byte-identical, but never says how keys are matched.

**Scenarios (each is a way the Gateway's view of a request differs from the provider's).**
- Duplicate keys: `{"max_tokens":100, ..., "max_tokens":900000}` or two `model` members. Go decoders keep the last; other parsers keep the first or reject. If the provider's winner differs from the Gateway's, the cap, the priced model or `tools` are not what was admitted. Reservation = $0.1, billed cap = 900 000.
- Case folding: Go's `encoding/json` matches struct fields case-insensitively. `"Max_Tokens": 100` could be read as the cap while the provider ignores it and uses its default (OpenAI chat routes have no mandatory cap), so the "enforced" cap is not enforced.
- Number forms and types: `1e3`, `100.5`, `"100"`, `-1`, `null`, huge integers; alias handling where one alias is `null`.
- Escapes: `"max_tokens"`; invalid UTF-8, BOM, lone surrogates, trailing bytes after the object, `Content-Encoding: gzip` (the body-size/bytes bound would then be the compressed size and the `bytes` input bound under-counts; §7.2 does not strip or deny `content-encoding`).
- Nesting: §8 lists "content-block `type`/`source`" parsing but not depth. Images, `source.type: url` and `file_id` inside Anthropic `tool_result.content[]`, Responses `function_call_output.output[]` or `system` block arrays are not in 53-AC-28's fixtures (all top-level-message examples). A URL image missed in a nested block bypasses the always-denied list; a base64 image missed is merely over-counted.
- Usage tap: the dialect parser must read terminal usage only from the top-level `usage` of the specific terminal event. Model output is attacker-influenced; plan 32 §8 says "by key path" but there is no test where the model's text contains literal `"usage":{...}` or a second `usage` object.

**Fix.** Plan 53 §8/§14.3: specify one strict tokenizing JSON reader for inspection: case-sensitive member names; **reject duplicate member names at every depth of inspected objects**; reject non-UTF-8, BOM, trailing data and non-integer/non-positive cap values; reject any `content-encoding` other than identity and `transfer-encoding` tricks; inspect nested content arrays to a fixed depth (e.g. 8) and deny deeper nesting. Mutation (§14.3) must operate on the same token stream it validated. Add 53-AC cases for each bullet above plus the "usage in model text" fixture (and a Go fuzz target comparing the inspector's view against two reference parsers, one first-wins and one last-wins; any disagreement is a deny).

### BUD-B4 (major): `cap_covers_reasoning` cannot be established by the specified smoke test

**Where.** Plan 53 §22.5 (smoke: one request, `max_tokens` 16, per provider), VS-8, 53-AC-17 (`cap_covers_reasoning` must equal the model's `outputCapCoversReasoning`).

**Scenario.** The ceiling (CD-28) holds only if the enforced output cap also bounds thinking/reasoning tokens. The check that `outputCapCoversReasoning: true` is correct requires a request whose unconstrained reasoning would exceed the cap, then observing `reasoning + output ≤ cap`. A minimal prompt with `max_tokens: 16` usually produces a short or no reasoning phase (or a provider error "max_tokens too small for thinking budget"). The evidence file then says `cap_covers_reasoning: true` vacuously. Nothing in the plans defines how the field is computed. Detection afterwards is only the post-hoc `BOUND_VIOLATION` (one request past the ceiling, then quarantine). Providers have changed this behaviour between model generations (e.g. DeepSeek's reasoning-model docs historically described the cap as excluding chain-of-thought), so it is exactly the kind of fact to prove, not assume.

**Fix.** Plan 53 §22.5 and 53-AC-17: define the probe: for every enabled reasoning-capable model, send a prompt that forces long reasoning (documented fixture), with thinking/reasoning enabled at its highest setting and `max_tokens` set to a small cap (e.g. 256) that is above the provider's minimum, and require `output + reasoning ≤ cap`; for models marked `false`, require that `reasoningCapField` is honoured the same way. `make check-provider-evidence` must verify the probe request is the reasoning fixture (hash in the evidence file), not any 16-token request. Add: any model whose smoke does not exercise reasoning cannot have `outputCapCoversReasoning: true`.

### BUD-B5 (major): the image bound is unproven, per family not per model, and its failure mode is a model-wide halt

**Where.** model-routing `inputBound.imageTokensUpperBound` (example 1600 for Anthropic); plan 53 §14.1 step 6; 53-AC-36 corpus list (40 items, no image); VS-13.

**Scenario.** The "provable" input bound for image requests is one declared number per family. Newer, higher-resolution models can charge several times more per image than older ones; the number is a third-party fact, verified by no AC. When Claude Code reads a screenshot or image file in a repository (which a hostile repo can plant), the reported input exceeds the bound, the Gateway records `BOUND_VIOLATION`, pages critical and **quarantines the model for every worker and for Hermes until a human acknowledges** (§14.4a). One image can therefore both exceed the ceiling and halt a tier. Without images in the corpus the first sight of the error is production.

**Fix.** Plan 53/model-routing: move `imageTokensUpperBound` to the `Model` object (per model, sized from the provider's documented maximum image tokens at the model's maximum image resolution, with the formula cited), add images (smallest, maximal documented size, many images, nested in tool_result) to 53-AC-36 for every enabled family/model that accepts images, and deny images for models without a value. Consider scoping quarantine of an image-driven violation to "deny images for this model" rather than the whole model.

### BUD-B6 (major): no independent backstop against the ledger being wrong

**Where.** Plan 33 §24 ("provider-side spend caps ... not relied upon"), ADR-0450 alternatives ("provider-side spend limits: rejected" as the *enforcement* mechanism), plan 32 §20 ("manual and monthly" reconciliation), pricing-update §E, plan 35 register (impact "Money (provider account spend limit)", but no control).

**Scenario.** Every defence in the design is computed from the same price card and parser. Failure modes that all of them share: a provider price rise before the PR merges (pricing-update admits "the ledger under-states cost and budgets are too generous"), an account-level setting (priority tier default, data-residency surcharge, regional endpoint) that changes the rate without any request feature, a new billed dimension, or a cost-attributing bug. Cost-dimension bound violations are measured with the same wrong card, so they do not fire. The only detector is the monthly console comparison on the 3rd of the next month, by a human, with a 3 %/$5 tolerance. Given the operator's history (a $60 loss in 20 minutes), a month of undetected under-charging at 2× is a $600 surprise, with no brake.

**Fix.** Plan 33 §9.3/§24 and plan 50 preflight: require, as an attested bootstrap precondition (like PF-26), a provider-side hard monthly limit or a prepaid balance with auto-reload off for every enabled production provider, sized to ≤ 1.25× that provider's `monthlyUsd`. Plan 32/pricing-update: add a weekly (or daily where a provider exposes an aggregate usage/cost endpoint; verify at implementation) automated or checklist comparison of provider-reported spend to the ledger for the same window, with an alert `SCLedgerProviderDivergence`. Record the residual in ADR-0450.

### BUD-B7 (minor): operator-run calls leave the ledger

`make smoke-providers`, `make smoke-providers --corpus` (a full-window item and ~40 large inputs on the frontier class) and `make record-provider-fixtures` use raw provider keys (plan 53 §22.2/§22.5, 53-AC-36). Whether they go through the Gateway is not stated; the corpus evidence ("reported vs computed input") suggests the Gateway's `usage` parse, but `record-provider-fixtures` needs raw bytes. If they are direct, they hold decrypted provider keys on an operator machine and are unbudgeted. **Fix:** plan 53 §22.5: route them through a Gateway instance with a dedicated `smoke` token kind and execution scope (cap, e.g. $2/run), or state that they are direct, set a `--max-usd` guard and record spend in `ledger.cost_adjustments`. Cost per frontier corpus run should be stated.

### BUD-B8 (minor): fixed windows and no automatic trip

`global-hour` is a fixed UTC hour (`global-hour:YYYY-MM-DDTHH`). $20 can be spent at HH:59 and another $20 at HH+1:00, i.e. 2× the stated hourly cap in two minutes; the same at the UTC day boundary against the day cap ($60 + $60 behind the hourly cap, so bounded at ≤ $40 per two minutes). The incident that motivates this review ($60 in 20 minutes) is within the daily cap by design. `SCAnomalousSpend` only pages. **Fix:** plan 33 §7.2: add a rolling-window scope (e.g. `global-15m`, sum over `cost_records` + reservations in the last 15 min, same transaction) or make the hourly limit a sliding window; optionally let the Gateway leader auto-engage `workers` when 15-minute spend exceeds `anomaly.absoluteWindowCapUsd` (with a human release), noting it in ADR-0452.

### BUD-B9 (minor): stop procedure assumes a healthy kube-apiserver and a 5-minute wait before the universal brake

`budget-emergency-stop.md` A1–A4 need `./bootstrap shell`, `sc-env` (SSH tunnel) and `kubectl`. If the k3s API is down: the Gateway raises `degraded=workers` after 60 s (workers stop), but Hermes (`all`) cannot be stopped and no `kubectl scale`/Flux suspend is possible; the document has no node-level path (SSH to the node and stop the Gateway container, or an iptables DROP for 443 on the node). A4 step 3 only revokes the provider key after "5 min" of continued traffic; the provider-console revoke is the one action that works in every failure combination and should come first when the Gateway is suspected. **Fix:** add A0 "revoke or pause the provider key in the console" (list per provider, with MFA readiness check in preflight) and A5 "node-level kill when the API is down"; require that the operator have rehearsed A1/A3/A0 in the recovery drill and record time-to-stop.

### BUD-B10 (minor): missing DB stop row

Plan 33 §13 lists fail-safe directions for an unparseable ConfigMap (`all`), unreadable k8s (`workers`), invalid Git (LKG), but not for `ledger.emergency_stop_state` returning no row, NULL, or an unknown `level` (e.g. after a bad PITR before the seed event). Say explicitly: treated as `all`, alert `SCEmergencyStopSourceUnavailable{source="db"}`. Add an AC: delete the state row in a test, expect denial.

### BUD-B11 (minor): "non-2xx before body = not billable"

Plan 53 §14.4 charges 0 for any upstream non-2xx before a body stream. A 502/504/524 produced by a CDN or load balancer in front of the provider while the model has already run may still be billed. SDK retries multiply that. **Fix:** treat 5xx without a recognisable provider error envelope and request id as `unknown` (charge reservation), and treat 429/4xx/529 with envelope as `not_billable`; add fixtures for CDN-style bodies.

### BUD-B12 (minor): coarse reservation expiry

`expires_at = started + timeouts.total (3600 s) + 600 s`. A killed pod holds its reservation open for up to 70 minutes: it counts toward the token's `max_concurrent_requests` (the execution may be locked out of its own concurrency), toward `reserved` on the shared scopes (contention), and is then settled at worst case as spent. **Fix:** plan 53 §14.1/§17: owner-replica lease (heartbeat updated every 15 s in the reservation row, or a replica-liveness table); the sweeper expires a reservation whose owner lease lapsed > 60 s, settling at worst case immediately. 53-AC-11 should use production-like timeouts for at least the lease path.

### BUD-B13 (minor): §80 questions the design cannot answer exactly

Hermes calls (acceptance-criteria authoring, decomposition, watcher) have null task/execution (plan 32 §2, plan 33 §25 Q7). So "What did this PR cost?" (Q1), "How much was spent authoring its tests?" (Q2 ignores the AC spec work) and "How much money was spent on failed work?" (Q10 excludes orchestration on abandoned tasks) understate. SPEC §78 says every invocation is attributable to task "where applicable"; here it is applicable and unmet. **Fix:** let the orchestrator token carry an *attribution hint* (`x-sc-task` honoured only for `token_kind: orchestrator`, stored in a separate `attributed_task_id` column, never used for enforcement or trusted for security), and add `specification` phase attribution for Hermes; then include it in Q1–Q4/Q10 with a flag.

### BUD-B14 (minor): Q8 is not answerable as written

Plan 32 Q8 compares cost per accepted task grouped by *starting* tier. Task difficulty drives both the choice and the outcome; easy tasks start cheap and succeed. The answer to "is Sonnet cheaper per accepted task than repeated cheaper-model attempts" needs matched tasks (same repository/size class) or A/B assignment. **Fix:** plan 32/31: add an optional assignment knob (policy-driven random starting tier for eligible tasks) and report Q8 per size class with the sample size; state the limitation on the dashboard.

### BUD-B15 (minor): unclassified denial codes

Plan 29 §15.2 and plan 27 §14.15 map only budget codes, contention and e-stop. `SC_MODEL_QUARANTINED`, `SC_PRICE_UNKNOWN`, `SC_MODEL_NOT_ALLOWED`, `SC_REQUEST_FEATURE_DENIED`, `SC_MAX_TOKENS_EXCEEDS_POLICY`, `SC_PROVIDER_UNAVAILABLE`, `SC_FAMILY_NOT_ALLOWED` have no class. A harness upgrade that sends a new feature, or a quarantine arriving mid-run, would produce an execution whose class is whatever the worker claims. **Fix:** plan 33 §14.5 table and plan 29 §15.2: add class `infrastructure` (code `GATEWAY_POLICY_DENIED`, refunded, count towards the consecutive-infra limit) for these, with a Runner alert when the same code repeats (it indicates a policy bug, not task failure).

### BUD-B16 (minor): per-model facts verified per family

- `contextWindowTokens` is configured per model but VS-12 verifies "provider rejects over-window input" with one fixture per family. A model whose real window is larger than configured admits requests whose real input exceeds `in_bound`: the overshoot is bounded (≈ real/config × the input part) but not "provable".
- The `cacheWrite1h` rate is applied only "if any `cache_control.ttl = 1h` is present", which depends on the inspector finding every nested `cache_control` (see BUD-B3). Safer and cheap: always take `max(…, cacheWrite1h)` when the entry has one.
**Fix:** plan 53 step 6: use `cacheWrite1h` unconditionally when priced; per-model window evidence in the provider smoke file (documented value and an over-window probe where affordable).

### BUD-B17 (minor): contract examples contradict shipped policy

`budget-policy.schema.yaml` examples use `perRequest.maxCostUsd "5.00"`, `maxConcurrentRequests 8`, `global.hourlyUsd "15.00"`; gateway-token examples use `max_concurrent_requests` 8 and 16. Shipped defaults are `3.00`, 4 and `20.00` (plan 33 §9.1), and invariant 10 (`hourlyUsd ≥ 2 × concurrency × worst`) fails for the example ($5 × 8 × 2 = $80 > $15). Align the examples with the shipped file or mark them non-normative.

## 4. Attacks and failures the design withstood

- **Concurrent over-admission.** Row locks on all scope rows in key order plus `spent + reserved + amount ≤ limit` make N parallel requests unable to share headroom; the 64-way test is specified (33-AC-3). Deadlocks avoided by ordering.
- **Unpriced treated as free / unknown usage as zero.** `SC_PRICE_UNKNOWN` before forwarding; unknown, unparseable or oversized usage charged at the reservation; `not_billable` limited to pre-body non-2xx.
- **Client abort / stream truncation / pod kill.** Missing output is charged at `out_cap`; abort after `message_start` ignores the placeholder output count; sweeper settles orphans at worst case; settlement is idempotent with `UNIQUE(reservation_id)`.
- **Harness retries.** Gateway never retries; each SDK retry is a fresh reservation; budget denials are 403 so SDKs stop (VS-9 verified at R01/C08); contention is 429 and refunded.
- **Provider-side features.** Allowlisted body members, tool types, betas and pinned `service_tier`/`store`; URL/file sources, `mcp_servers`, conversation state, `n>1`, batch and background APIs denied; window-enlarging betas require a separate model id.
- **Cache pricing.** Reservation rate is the max over `input`, `cachedInput`, `cacheWrite` (and 1h write when marked), all time windows and long-context tiers, across every allowed model; a 1h write without a 1h rate is `unpriced`, charged at the reservation.
- **Reasoning tokens.** Models without an output cap covering reasoning cannot be routed without an enforced reasoning cap (subject to BUD-B4's verification gap).
- **Model drift.** Reported model vs requested is compared; the more expensive price is used; mismatch alerts.
- **Token forgery and replay.** EdDSA plus DB registration with payload digest; `aud`/`typ` separation; revocation inside the reservation transaction.
- **Worker bypass of the Gateway.** Workers hold no provider key (only the gateway token); the egress proxy denies model-provider hosts; NetworkPolicy has no direct route. Credential-less direct calls cannot bill.
- **Stop when Runner / DB / Flux are broken.** The Kubernetes ConfigMap source needs none of them (33-AC-11); `degraded` raises to `workers` on an unreadable API; DB loss means no spend; Flux cannot adopt or prune the ConfigMap (33-AC-12); stop before PITR is required to use path 2/3.
- **Restore losing the stop.** Procedure forbids path 1 around restores; black-start recovery sets the Git source.
- **No automatic resume.** `BLOCKED` tasks need a human transition; releasing requires clearing all sources.
- **Rate-of-spend defaults.** With the shipped `hourlyUsd` $20 / `dailyUsd` $60 and 1–2 workers, the $60-in-20-minutes class of incident is stopped at ≈ $20–40 (see BUD-B8 for the boundary case).
- **Open-proxy / SSRF on billable routes.** Upstream host from policy only; no redirects; headers stripped; absolute-URI targets refused (53-AC-20). The non-billable `count_tokens` route is the exception (BUD-B1).
- **Accounting privacy.** Bodies are never persisted; canary test 53-AC-14.
