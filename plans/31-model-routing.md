# 31 — Model Routing

**Status:** Stage-0 plan (draft)
**DAG nodes:** C05 (co-owner with plans 53 and 32); holds GATE-C10 items (owner plans 21/46) for checks that need fake-harness Jobs. Proposes checks for C08 (owner plans 24/26) and R01 (owner plan 28).
**Related plans:** [53 model gateway](53-model-gateway.md), [32 cost accounting](32-cost-accounting.md), [33 budget enforcement](33-budget-enforcement.md), [26 agent runner](26-agent-runner.md), 21 (TDD phases, generations), 22 (`RepositoryPolicy`), 24 (Hermes), 25 (role definitions), 27 (worker input), 28 (harness registry and adapters), 29 (attempt counters, failure classes), 35 (secrets), 36 (network policy), 42 (alerts)
**Contracts:** owns `contracts/model-routing.schema.yaml` (including `$defs/RepositoryRoutingOverride` and `$defs/RoutingDecision`); consumes `harness.schema.yaml` (28), `role.schema.yaml` (25), `pricing.schema.yaml` (32), `budget-policy.schema.yaml` (33), `gateway-token.schema.yaml` (53), `mcp-surface.schema.yaml` (26)
**ADRs:** relies on ADR-0004 (Gateway), ADR-0017 (policy as GitOps config; base-revision repository policy), ADR-0003 (Hermes untrusted), ADR-0450, ADR-0451; introduces none (routing decisions here are plan-local)

> Every section below is mandatory (SPEC §121). Requirements carry SPEC trace tags.

## 1. Purpose

Model routing turns a dispatch request — (role, harness, requested tier, optional explicit model) plus the task's attempt history — into **one concrete model**: provider, API family and provider model string. It also yields the exact Gateway-token allowlist that confines the worker to that model and, optionally, one auxiliary model for harness background calls.

Routing is a **pure, deterministic function** evaluated by the Agent Runner at dispatch. Its inputs are Git-delivered policy (`policy/model-routing.yaml`, `policy/harnesses.yaml`, `policy/roles/*.yaml`, `policy/pricing/*.yaml`, `policy/budgets.yaml`), the base-revision repository override, the task's attempt counters, the Gateway-reported provider status and budget headroom read from `sc-pg`. The Model Gateway (plan 53) does not route. It enforces the allowlist the Runner minted into the token, so a harness, a prompt-injected model or Hermes cannot choose a different model.

The plan defines: the four capability tiers and their ordered candidate lists; harness compatibility; role default tiers (where the tester may be stronger than the implementer); the escalation ladder on merit failures; same-tier provider fallback on provider-attributable infrastructure failures; per-repository tighten-only overrides; the Hermes (orchestrator) resolution; subscription independence; and what each resolution records.

## 2. Requirements derived from SPEC.md

1. Model names are data, never architectural constants. Routing uses logical capability tiers `clerical`, `bargain`, `standard`, `frontier`. [SPEC §75]
2. Several providers are supported (Anthropic, OpenAI, DeepSeek, Z.ai/GLM, Google, others) behind one routing abstraction. [SPEC §75]
3. Intelligence is allocated by ambiguity: frontier reasoning for ambiguity, a strong standard model for clear specifications, a cheap capable model for bounded implementation, deterministic systems for verification. Test and specification authoring may use a stronger model than implementation. [SPEC §76, §41]
4. Model use relies on automatable API credentials. Consumer subscriptions are never an infrastructure dependency. [SPEC §77]
5. Harness and model are separate concepts. No vendor's harness defines the abstract agent. [SPEC §63, §64]
6. Role determines the default model tier. Role and harness are independent. [SPEC §65]
7. Hermes chooses approved model/harness profiles but never arbitrary values. Trusted policy resolves details. [SPEC §4.6, §62]
8. A failing cheap implementation may escalate to a more capable model. Retries use evidence, are bounded, and never replay identical work. [SPEC §74, §4.8]
9. Model routing is version-controlled policy. [SPEC §4.7, §39]
10. The model, harness and tier of every execution are recorded on the task record. [SPEC §73, §78]
11. Routing is testable deterministically without paid model calls. [SPEC §104]

Derived requirements:

- **31-R-1** Resolution is a pure function of its inputs (§14.1). The same inputs always produce the same `RoutingDecision` or the same error code. No randomness, round-robin, latency-based or "cheapest-now" selection.
- **31-R-2** A candidate is used only if it is enabled, compatible with the harness, permitted by the repository override, **priced** (an effective `priced` entry exists; plan 32), its provider is reported available by the Gateway, and its provider has budget headroom. Otherwise the next candidate in the tier's order is tried.
- **31-R-3** The Gateway-token allowlist contains exactly the resolved primary model and at most one auxiliary model. Nothing else is callable by the worker (53 `SC_MODEL_NOT_ALLOWED`).
- **31-R-4** Escalation never lowers the tier for the same (task, role) within a test generation, and is bounded: when the ladder is exhausted no further execution of that role is resolvable (`ATTEMPTS_EXHAUSTED`, detail `escalation_ladder_exhausted`).
- **31-R-5** Infrastructure-class failures never advance the escalation ladder. Merit-class failures never trigger provider fallback.
- **31-R-6** A repository override can only remove candidates or lower ceilings. It cannot add providers, models or tiers (by construction of `$defs/RepositoryRoutingOverride`).
- **31-R-7** Only `credential.kind: api-key` providers exist (schema constant). Subscription and OAuth authentication modes are rejected at four independent layers (§13.2).
- **31-R-8** Every execution stores its `RoutingDecision` (requested tier, effective tier, tier source, provider, family, model, auxiliary, candidate index, fallback flag, routing revision, pricing version). The gateway token echoes the requested and effective tier.
- **31-R-9** Two declarations of a role's tiers (RoleDefinition, plan 25, and `ModelRouting.roles`) must agree, or the policy set is rejected (§9.3).

## 3. Inputs

| Input | Source | Delivery |
|---|---|---|
| `ModelRouting` | `policy/model-routing.yaml` (this plan) | Flux-generated policy ConfigMap (D-17), F04 loader |
| `HarnessRegistry` (`apiFamilies`, `adapter`, `stage`, `supportedRoles`, `enabled`) | `policy/harnesses.yaml` (plan 28) | same |
| `RoleDefinition` (`defaultModelTier`, `allowedModelTiers`, `accounting` phases) | `policy/roles/*.yaml` (plan 25) | same |
| `PricingTable`s (priced/unpriced at an instant) | `policy/pricing/*.yaml` (plan 32) | same |
| `BudgetPolicy` (`providers`, `dispatch.minimumHeadroomUsd`) | `policy/budgets.yaml` (plan 33) | same |
| `RepositoryRoutingOverride` | `spec.modelRouting` of `.strange-company/policy.yaml` at the task's **base commit** (plan 22 `RepositoryPolicy`, ADR-0017) | Forgejo contents API, read by the Runner at task creation and each dispatch |
| Dispatch request: `role`, `harness`, `model_tier`, optional `model` | Hermes `agent_start` (`mcp-surface.schema.yaml`, plan 26) | MCP |
| Attempt history: merit-failure count per (task, role, generation); last execution's failure class and code; fallbacks used on the task | Runner tables (plans 26, 29) | in-process |
| Provider status (`available`, `reason`, `updated_at`) | `ledger.provider_status`, written by the Gateway leader every 30 s (plan 53 §6) | `sc-pg` SELECT |
| Shared-scope headroom (`provider-day`, `provider-month`) | `ledger.budget_scopes` (plan 33 §7.2) | `sc-pg` SELECT |
| Provider-attributable failure evidence | `ledger.cost_records` and `ledger.denials` of the previous execution (plans 32, 53) | `sc-pg` SELECT |
| Wall clock `now` | Runner | — |

## 4. Outputs

- A `RoutingDecision` (`model-routing.schema.yaml#/$defs/RoutingDecision`) stored in `runner.executions.routing` (jsonb) and projected into the scalar columns `model_tier` (= `tier_effective`), `provider`, `model` (plan 26 §6).
- The gateway-token claims `tier_requested`, `tier_effective` and `allowed_models[]` (`gateway-token.schema.yaml`, minted by plan 26).
- The worker-input `gateway` block (`worker-input.schema.yaml`, plan 27; review RA-m11): `provider`, `api_family`, `model` (= `api_model`), `auxiliary_model` (= the auxiliary's `api_model` or null), `allowed_models`, and `base_urls` (Gateway route `/p/<provider>` per family); the adapter env hint comes from `ModelRouting.harnesses.<h>.auxiliary.envHint`.
- The Hermes orchestrator allowlist and pinned model (§14.6), consumed by the Runner's Hermes-credential rotation (ADR-0401, plan 26) and by plan 24's managed scope.
- Errors with deterministic codes and details (§7.3).
- Prometheus metrics and two alerts (§21).

## 5. Dependencies

**Hard (DAG):** C05 depends on C01. Routing is a library inside the Runner (F04 policy-lib loads the file; this plan's resolver is a pure package) and needs `runner.executions` and the plan 29 attempt counters. Its tests need plan 53's `ledger.provider_status` and plan 33's `ledger.budget_scopes` (both C05/C06 migrations; until C06 the headroom input is treated as "unlimited" in tests only).

**Soft / runtime:**
- Plan 28 harness registry: `apiFamilies` per harness; `adapter` sets the base URL and the auxiliary env var from §4.
- Plan 25 role definitions: tiers must agree (§9.3).
- Plan 29: merit-failure counter per (task, role, generation) and failure classes (master plan §7).
- Plan 22: the `RepositoryPolicy.spec.modelRouting` field embeds `$defs/RepositoryRoutingOverride` (assumed interface).
- Plan 53: `ledger.provider_status` freshness (≤ 30 s writes) and token allowlist enforcement.
- Plan 33: shared-scope headroom and `dispatch.minimumHeadroomUsd`.

## 6. Owned state

| State | Location | Writer | Notes |
|---|---|---|---|
| Routing policy | Git `policy/model-routing.yaml` | humans via PR (`policy/**` is a protected path, ADR-0008) | desired state; revision = policy commit (plan 26 §9.2) |
| Routing decisions | `runner.executions.routing` (+ scalar columns) in `sc-pg` | Runner (`runner_app`) | immutable once the execution leaves `QUEUED`; semantics owned here, storage by plan 26 |
| Escalation and fallback events | `runner.task_events` (plan 26/29 event table) with kinds `routing_escalated`, `routing_fallback_applied`, `routing_re_resolved` | Runner | append-only |

No other durable state. The resolver holds no cache beyond the immutable policy snapshot.

## 7. External interfaces

### 7.1 Policy file `policy/model-routing.yaml` (`kind: ModelRouting`)

Schema: `contracts/model-routing.schema.yaml`. Shipped default content: §9.2.

### 7.2 Resolution API (in-process library used by the Runner)

```text
Resolve(req ResolveRequest) -> (RoutingDecision, AllowedModels, error)

ResolveRequest:
  role, harness, tier_requested, explicit_model?      # from agent_start (validated input)
  task_id, repository, generation                      # task context
  merit_failures (int), last_execution {failure_class, failure_code, execution_id}?
  fallbacks_used (int), excluded_providers (set)       # computed by the Runner (§14.4)
  override (RepositoryRoutingOverride, already merged) # base-revision repo policy
  snapshot (policy set incl. routing, harnesses, roles, pricing, budgets; revision)
  provider_status (map provider -> {available, reason, updated_at})
  headroom (map provider -> {day_micro_usd, month_micro_usd})
  now
```

The Runner calls it at `agent_start` (plan 26 §14.2 step 7) and re-validates at dispatcher step 1 (§14.5).

### 7.3 Errors (Runner error codes; MCP and REST)

| Code | When | `details` |
|---|---|---|
| `MODEL_TIER_NOT_ALLOWED` | `tier_requested` ∉ the role's effective allowed tiers (§14.3), or > the repository cap | `{allowed_tiers, repository_max_tier}` |
| `MODEL_NOT_ALLOWED` | explicit `model` given but not an eligible candidate of the effective tier | `{effective_tier, eligible:[model_id]}` |
| `HARNESS_NOT_ALLOWED` | harness has no API family in common with any model of any allowed tier (static) | `{harness, api_families}` |
| `ROUTING_UNRESOLVABLE` | no eligible candidate for a reason other than budget alone | `{role, harness, tier_effective, rejected:[{model_id, reason}]}` |
| `BUDGET_EXCEEDED` | every otherwise-eligible candidate was rejected only for `provider_budget_headroom` | `{tier_effective, providers:[...]}` |
| `ATTEMPTS_EXHAUSTED` | ladder exhausted (31-R-4) | `{detail: "escalation_ladder_exhausted", merit_failures, ladder}` |
| `DEPENDENCY_UNAVAILABLE` | every provider's status row is older than `routing.providerStatusMaxAgeSeconds` (Gateway leader not reporting) | `{dependency: "model-gateway"}` |

`reason` values for rejected candidates (closed enum, also a metric label): `disabled`, `provider_disabled`, `stage_mismatch`, `family_incompatible`, `harness_provider_constraint`, `repository_denied`, `unpriced`, `no_provider_budget`, `provider_unavailable`, `provider_status_stale`, `provider_budget_headroom`, `infra_excluded`.

**Interface request to plan 26:** `ROUTING_UNRESOLVABLE` is not yet in `mcp-surface.schema.yaml`'s error enum. Add it (HTTP 409 on REST, like other gate failures). Until then the Runner maps it to `MODEL_TIER_NOT_ALLOWED` with `details.routing_unresolvable: true`. That fallback is not acceptable at C05 (31-AC-9 checks the code).

## 8. Internal interfaces

| Component | Responsibility |
|---|---|
| `routing.Loader` (part of F04) | schema validation + §9.3 cross-file invariants; computes the static compatibility matrix (harness × role × tier → candidate list) once per policy revision |
| `routing.Resolver` | pure function of §7.2 |
| Runner `dispatch` | gathers inputs, calls `Resolve`, records the decision, mints the token with `AllowedModels` (plan 26 §14.3 step 5) |
| Runner `history` | computes `merit_failures`, `excluded_providers`, `fallbacks_used` from Runner tables and the ledger (§14.3, §14.4) |
| Runner `hermes credentials` | calls `ResolveOrchestrator` (§14.6) on every policy revision change and at rotation |

## 9. Configuration

### 9.1 Knobs

| Knob | Source of truth | Default | Changed by |
|---|---|---|---|
| Providers, models, tiers, harness constraints, roles, ladders, orchestrator, fallback | `policy/model-routing.yaml` | §9.2 | human PR |
| Repository restrictions | `.strange-company/policy.yaml` `spec.modelRouting` in the managed repository | absent (no restriction) | human PR in that repository (protected path) |
| `routing.providerStatusMaxAgeSeconds` | Runner config (plan 26 §9.1, chart values) | `120` | PR |
| queue timeout for QUEUED re-resolution | plan 29 `VerificationPolicy.spec.retry.infrastructure.queueTimeout` (not a routing knob) | `2h` | PR (plan 29) |

### 9.2 Shipped default policy (`policy/model-routing.yaml`)

Concrete model strings are **not** fixed in Stage 0 (SPEC §75, master plan §6.3). Each slot below is filled at implementation with this **selection rule**: the newest generally-available model of the named class from that provider whose price can be verified from the official pricing page (plan 32 §9.4) and whose usage fixtures pass (53-AC-17). Model ids follow `<provider>/<apiModel>`.

| Tier | Ordered candidates (class) | Intended use [SPEC §76] |
|---|---|---|
| `clerical` | `anthropic/<small fast class>` (e.g. Haiku-class), `deepseek/<flash/chat class>` | auxiliary background calls, summaries, formatting |
| `bargain` | `anthropic/<small fast class>`, `deepseek/<flash/chat class>` | bounded implementation against a RED test |
| `standard` | `anthropic/<balanced class>` (e.g. Sonnet-class), `openai/<general GPT class>` (provider disabled by default) | clear specifications: tests, review, research |
| `frontier` | `anthropic/<largest reasoning class>` (e.g. Opus-class), `openai/<frontier reasoning class>` (disabled by default) | ambiguity: planning, last ladder rung |

`fake/fake-model` (and `fake/fake-model-b` on the second test-only provider `fake-b`, used by fallback tests) is appended to every tier. Stage separation (§14.2) guarantees production harnesses can never resolve to it.

| Role | `defaultTier` | `allowedTiers` | `ladder` |
|---|---|---|---|
| researcher | standard | bargain, standard, frontier | null |
| planner | frontier | standard, frontier | null |
| tester | **standard** | bargain, standard, frontier | `tests`: standard ×2 → frontier ×1 |
| implementer | **bargain** | bargain, standard, frontier | `implementation`: bargain ×2 → standard ×2 → frontier ×1 |
| reviewer | standard | standard, frontier | null |

The tester defaults one tier above the implementer: a correct test makes implementation mechanical [SPEC §76]. The implementation ladder admits 2 + 2 + 1 = 5 merit attempts, which matches SPEC §74's "approximately five autonomous attempts".

**Request allowlists** (`requestPolicy` per provider family, review COST-1): the shipped values are those of the contract's `default` example (Anthropic: Messages members incl. `thinking`, tool type `custom`, betas `claude-code-*`, `interleaved-thinking-*`, `fine-grained-tool-streaming-*`, `token-efficient-tools-*`, pinned `service_tier: standard_only`; Responses: tool types `function`, `custom`, `local_shell`, pinned `store: false`, `service_tier: default`; Chat: tool type `function` only). They are re-captured from each pinned harness and Hermes before release (53-AC-29); each model also declares `outputCapCoversReasoning` (and `maxReasoningTokens` if false; VS-8).

Other shipped values: `harnesses.claude: {providers: [anthropic], auxiliary: {tier: clerical, envHint: <per pinned Claude Code version, VS-R1>}}`; `orchestrator: {tier: standard, apiFamilies: [openai-chat-completions, anthropic-messages], maxConcurrentRequests: 16, auxiliaryTier: clerical}`; `infrastructureFallback: {enabled: true, maxFallbacksPerTask: 1}`. Fallback is enabled because it only acts for harnesses that can speak to more than one provider (the Claude Code harness is constrained to `anthropic`, so it never falls back).

Providers shipped: `anthropic` (enabled, `forbiddenKeyPrefixes: [sk-ant-oat]`, `headerPolicy.anthropicBetaDenyPrefixes: [oauth-]`), `openai` (disabled), `deepseek` (enabled), `zai` (disabled), `fake` and `fake-b` (`stage: test-only`, enabled while `modelGateway.fakeUpstream.enabled`). A provider without a key in `SC-MODEL-API-KEYS` is reported unavailable by the Gateway and skipped (§14.2). It does not break routing.

### 9.2a C10 test overlay for Hermes (requested by plans 24/46, review RA-12)

File `test/e2e/policy-overlays/c10/model-routing.patch.yaml` (JSON merge patch on the shipped `ModelRouting`), applied and reverted by plan 46's C10 setup together with its Hermes chart values. It is never part of the shipped `policy/` tree:

```yaml
spec:
  models:
    fake/fake-hermes-watch: {provider: fake, apiModel: fake-hermes-watch, apiFamily: fake, maxOutputTokens: 4096,
                             contextWindowTokens: 32000, outputCapCoversReasoning: true,
                             responseModelMatch: {exact: [fake-hermes-watch]}, enabled: true}
  tiers:
    clerical: {candidates: [fake/fake-hermes-watch, <shipped clerical candidates>]}   # rendered by sc-e2e from the shipped list
  orchestrator: {tier: clerical, apiFamilies: [fake], providers: [fake], auxiliaryTier: clerical, maxConcurrentRequests: 4}
```

With it, §14.6 resolves both the orchestrator primary and its auxiliary to `fake/fake-hermes-watch` (the `providers: [fake]` filter leaves only fake models, and that one is first). The Runner re-mints the orchestrator token with `allowed_models = [{provider: fake, api_family: fake, model: fake-hermes-watch, purpose: primary}]`. Production harnesses are unaffected: the Claude Code harness is constrained to `anthropic`, and stage separation keeps every production harness off `fake`. The model is priced by `policy/pricing/fake.yaml` (plan 32 §9.4a). `make policy-check` on the shipped tree rejects an `orchestrator.providers` list that contains only `stage: test-only` providers, so the overlay cannot be merged into `policy/` by mistake.

### 9.3 Load-time invariants added by this plan (F04 loader; any violation rejects the policy set, LKG kept, alerts `SCRunnerPolicyInvalid` and `SCGatewayPolicyInvalid`)

In addition to those listed in the schema description:

1. **Role tier agreement (31-R-9):** for each role, `RoleDefinition.defaultModelTier == ModelRouting.roles.<r>.defaultTier` and the two allowed-tier sets are equal. Error `ROLE_TIER_MISMATCH <role>`.
2. **Ladder anchoring:** rung 0's tier equals the role's `defaultTier`. Every rung tier is in `allowedTiers` (schema description) and rungs are strictly ascending.
3. **Static resolvability:** for every enabled `stage: production` harness and every role in its `supportedRoles`, the role's `defaultTier` has at least one candidate that passes the *static* filters of §14.2 (filters 1–5). This ensures an enabled harness/role pair cannot fail every dispatch. Error `TIER_STATICALLY_EMPTY <harness>/<role>/<tier>`.
4. **Orchestrator resolvability:** `orchestrator.tier` has at least one static candidate with an `apiFamily` in `orchestrator.apiFamilies` and a provider in `orchestrator.providers` (if set) whose stage is `production` **or** `test-only` if no production provider is enabled (the fake-only installation used before real keys exist).
5. **Budget coverage (warning, not rejection):** every enabled production provider referenced by a tier has an entry in `BudgetPolicy.providers`. A missing entry makes its candidates ineligible (`no_provider_budget`); `make policy-check` prints a warning.
6. **Intelligence-allocation lint (warning):** `roles.tester.defaultTier < roles.implementer.defaultTier` prints a warning citing SPEC §76.
7. **Allowlist sanity (review COST-1):** every family's `requestPolicy.bodyMembers` contains `model` and the family's `outputCapField`; no `anthropicBeta` entry matches the prefixes `oauth-`, `mcp-client-`, `files-api-`, `code-execution-`, `web-fetch-`, `web-search-`, `computer-use-` or `context-1m-` unless every model of that provider/family declares a `contextWindowTokens` and price card covering it (window-enlarging betas need a separate model id); `toolTypes` contains none of the provider-hosted types `web_search*`, `web_fetch*`, `code_execution*`, `file_search`, `code_interpreter`, `image_generation`, `computer*`, `mcp`, `retrieval`.
8. **Provider hosts are egress-forbidden (review COST-18):** every `providers.*.upstream.baseUrl` host of a `stage: production` provider is matched by `EgressPolicy.forbiddenHosts` (`contracts/egress-policy.schema.yaml`, plan 36), so no egress-proxy delegation can open a direct path to a model provider.

## 10. Secrets

None consumed or produced. Routing references `credential.secretKey` names only. The keys themselves (`SC-MODEL-API-KEYS`) are read by the Gateway alone (plan 53). The resolver never sees a key, a token value or a fingerprint.

## 11. Storage

`runner.executions.routing` jsonb (≈ 600 bytes per execution) and three event kinds in the task event table. At ≈ 20 executions/day this is < 5 MB/year. Retention follows task records (plan 43).

## 12. Resource requirements

In-process in the Runner (plan 26 §12). A resolution evaluates at most 4 tiers × 8 candidates with two indexed `sc-pg` reads (provider status, headroom). Budget: < 5 ms p99 excluding DB, < 20 ms including DB. Policy-load matrix computation: < 50 ms. No additional pods.

## 13. Security boundary

### 13.1 Trust

- Hermes is untrusted for enforcement (ADR-0003). Its `model_tier` and `model` inputs are *requests*, which the resolver validates against policy. Hermes cannot name a provider, an API family, a base URL or a key.
- The worker cannot influence routing. Its only lever is to fail, and the failure class is decided by the Runner and the Gateway ledger, never by the worker's claim (plan 26 §14.4 step 11 uses `ledger.denials` and verification evidence).
- Enforcement point: the Gateway rejects any (provider, family, model) not in the token allowlist (`SC_MODEL_NOT_ALLOWED`), any family the provider does not declare, and any unpriced model. A compromised harness configuration therefore cannot reach a more expensive model.
- `policy/**` and `.strange-company/**` are protected paths (ADR-0008), so no agent task can change the routing that judges it.

### 13.2 Subscription independence [SPEC §77] — four independent layers

| Layer | Mechanism | Test |
|---|---|---|
| Contract | `credential.kind` is the constant `api-key`; an OAuth or subscription credential cannot be expressed | `model-routing.schema.yaml` invalid example 1 (31-AC-1) |
| Gateway | keys matching `forbiddenKeyPrefixes` (Anthropic OAuth `sk-ant-oat`, VS-7) make the provider unavailable; `anthropic-beta: oauth-*` is denied `SC_REQUEST_FEATURE_DENIED`; callers authenticate only with gateway tokens | 53-AC-19, 53-AC-5 |
| Network | workers and Hermes have no egress except to the Gateway (plan 36), so consumer endpoints (claude.ai, chatgpt.com) are unreachable even with a smuggled session | 53-AC-21 |
| Harness | adapters (plan 28) force API-key mode (Claude Code: the gateway token in `ANTHROPIC_API_KEY` delivered per `tokenDelivery`; Codex: API-key login mode (verify the exact config key, VS-R2)), `$HOME` is an emptyDir, and no human credential file is mounted | plan 28 golden Job test; 31-AC-12 |

Bootstrap preflight (plan 50) additionally rejects a model key in `bootstrap.env` that matches a provider's `forbiddenKeyPrefixes` (request to plan 50).

## 14. Happy path

### 14.1 When resolution runs

1. `agent_start` (plan 26 §14.2 step 7): full resolution, validated and recorded with the QUEUED execution.
2. Dispatcher step 1 (QUEUED → STARTING): **re-validation** of the recorded decision against the current snapshot, provider status and headroom (§14.5).
3. Policy revision change: `ResolveOrchestrator` for Hermes (§14.6).

### 14.2 Candidate eligibility (applied to each model id in tier order)

Static filters (depend only on policy and override; precomputed per revision):

1. Model `enabled` and its provider `enabled` (else `disabled` / `provider_disabled`).
2. **Stage separation:** harness `stage: production` ⇒ provider `stage: production`; harness `stage: test-only` (adapter `fake`) ⇒ provider `stage: test-only` (else `stage_mismatch`).
3. `model.apiFamily ∈ harness.apiFamilies` (else `family_incompatible`).
4. If `ModelRouting.harnesses.<harness>.providers` is set, the provider is in it (else `harness_provider_constraint`).
5. Repository override (merged, base revision): provider in `allowedProviders` if set, provider ∉ `deniedProviders`, model ∉ `deniedModels` (else `repository_denied`).

Dynamic filters (evaluated at `now`):

6. An effective `priced` pricing entry exists for (provider, `apiModel`, `standard`) at `now` (plan 32 §9.2) (else `unpriced`).
7. `BudgetPolicy.providers` has an entry for the provider (else `no_provider_budget`).
8. `ledger.provider_status[provider].available = true` and `updated_at ≥ now − providerStatusMaxAgeSeconds` (else `provider_unavailable` / `provider_status_stale`).
9. Provider ∉ `excluded_providers` (§14.4) (else `infra_excluded`).
10. Provider headroom: for both `provider-day:<p>:<today>` and `provider-month:<p>:<this month>`, `limit − spent − reserved ≥ dispatch.minimumHeadroomUsd[tier_effective]` (plan 33 §14.1). A missing row counts as `spent = reserved = 0` (else `provider_budget_headroom`).

The first candidate passing all ten filters is selected. Its 0-based position in the tier list is `candidate_index`.

### 14.3 Tier determination

```text
allowed      = RoleDefinition.allowedModelTiers            (== ModelRouting.roles.<r>.allowedTiers, §9.3)
cap          = min(override.maxTier, override.roleMaxTier[role])     (absent = frontier)
requested    = agent_start.model_tier
require requested ∈ allowed and requested ≤ cap           else MODEL_TIER_NOT_ALLOWED
source       = (requested == roles.<r>.defaultTier) ? role_default : requested

if roles.<r>.ladder == null:   effective = requested
else:
  rungs = escalationLadders[ladder]          # [(tier_0 = defaultTier, n_0), (tier_1, n_1), ...]
  M     = attempts.merit_by_generation["<role>:g<n>"]     # plan 29 counter (CD-09)
  k     = smallest i with M < n_0 + ... + n_i
  if no such i: ATTEMPTS_EXHAUSTED (escalation_ladder_exhausted)
  if k == 0:  effective = requested
  else:       effective = max(requested, tier_k); if tier_k > requested: source = escalated
if effective > cap: effective = cap; source = repository_capped
```

Tier order: `clerical < bargain < standard < frontier`.

**Merit-failure count M** is consumed, not defined, here (CD-09: plan 29 is authoritative for failure events and counters). `M = attempts.merit_by_generation["<role>:g<current_generation>"]` from the task record (plan 29 §16.5), passed to the resolver as `merit_failures_at_resolution`. Plan 29 defines which transitions increment it (for orientation only: RED/judge classified invalid for the tester; GREEN/judging failed and review `changes_requested` for the implementer/producer; worker failures with a merit reason for the other roles) and starts a new generation's counter at 0. Routing adds no merit events of its own.

Infrastructure, policy and budget failures never increment M (31-R-5). An explicit tier request lower than an escalated tier is overridden by escalation (`effective = max`), so Hermes cannot de-escalate a failing task back to a cheap model and replay it [SPEC §74]. Plan 26 additionally refuses an identical replay.

### 14.4 Same-tier infrastructure fallback

Applies when `infrastructureFallback.enabled` and the **immediately preceding** execution of this role on this task ended with `failure_class = infrastructure` and was **provider-attributable**.

**Provider-attributable** (deterministic, computed by the Runner from the ledger, never from the worker's claim):

- the worker result reason is `model_unavailable` (worker-result enum, plan 27), **or** the execution's ledger contains, among its last 3 cost records and denials ordered by time, at least one of:
  - a cost record with `termination ∈ {upstream_unreachable, upstream_timeout, upstream_stream_error, idle_timeout}`;
  - a cost record with `http_status ∈ {429, 500, 502, 503, 504, 529}`;
  - a denial with code `SC_PROVIDER_UNAVAILABLE`;
- **and** no cost record with `termination = completed` and a 2xx status follows that failure.

Then `excluded_providers = {provider of the failed execution}`, extended by the providers of any consecutive provider-attributable infrastructure failures of the same role just before it. The exclusion lasts for this one resolution only, and a later resolution recomputes it. Resolution proceeds in the **same effective tier** with those providers filtered out (filter 9).

- If a different candidate is selected: `fallback_applied = true`, event `routing_fallback_applied`, and `fallbacks_used` for the task increments.
- If `fallbacks_used ≥ maxFallbacksPerTask`, or no other candidate is eligible, the exclusion is dropped and the original provider is used again. The retry is then bounded only by plan 29's consecutive-infrastructure limit (default 3).
- Fallback never changes the tier and never consumes a merit attempt.

### 14.5 Selection, auxiliary model, allowlist, recording

1. **Explicit model.** If `agent_start.model` is given, it must be the `model_id` of an eligible candidate of the effective tier, and is then selected even if it is not first. Otherwise the call fails with `MODEL_NOT_ALLOWED` (details list the eligible ids). This holds also when escalation is in effect: Hermes can choose among eligible models of the escalated tier, never below it.
2. **Auxiliary.** If `ModelRouting.harnesses.<harness>.auxiliary` is set, the auxiliary model is the first candidate of `auxiliary.tier` that passes filters 1–8 **and** has the same provider and API family as the primary. None found → `auxiliary = null`. The adapter then points the auxiliary env var at the primary model, so background calls stay inside the allowlist.
3. **Allowlist.** `allowed_models = [{provider, api_family, model: apiModel, purpose: primary}] + [auxiliary entry, if any]`.
4. **Record.** `RoutingDecision` fields: `role`, `harness`, `tier_requested`, `tier_effective`, `tier_source`, `escalation_rung` (the ladder rung index k of §14.3, recorded even when the tier is capped or was explicitly requested; review COST-20), `merit_failures_at_resolution` (M), `provider`, `api_family`, `model_id`, `api_model`, `auxiliary`, `candidate_index`, `fallback_applied`, `routing_revision` (the policy commit), `pricing_version_at_dispatch` (the `version` of the primary's effective entry). Events `routing_escalated` (when `source = escalated` and the tier differs from the previous execution of the role) and `routing_fallback_applied` are appended.
5. **Re-validation at dispatcher step 1.** Re-run filters 1–10 for the recorded primary (and auxiliary) under the current snapshot. If it still passes, keep the decision unchanged. If it fails, re-resolve with the same request and history. A new decision is recorded with event `routing_re_resolved` (old and new `model_id`, reason) and the token is minted from the new one. If re-resolution fails:
   - only for lack of **`task:`** headroom → the execution ends at once `FAILED`, class `budget`, code `BUDGET_TASK_EXHAUSTED` (task headroom never returns by waiting; plan 29 T106 → `NEEDS_HUMAN`);
   - for any other reason (shared-scope headroom, provider unavailable, unpriced) → the execution stays `QUEUED` and is retried on every dispatcher cycle, up to plan 29's `spec.retry.infrastructure.queueTimeout` (default 2 h). It then ends `FAILED` with `BUDGET_SHARED_EXHAUSTED` (class `budget`, refunded; T105 → `BLOCKED`) if shared headroom was the only reason, else `QUEUE_TIMEOUT` (class `infrastructure`, refunded; plan 29).

### 14.6 Orchestrator (Hermes) resolution

`ResolveOrchestrator(snapshot, provider_status, now)`:

1. Candidates = `tiers[orchestrator.tier].candidates`, filtered by `apiFamily ∈ orchestrator.apiFamilies`, provider ∈ `orchestrator.providers` (if set), filters 1, 6, 7, 8 above. Stage: `production` providers. `test-only` providers are allowed only if no production provider is enabled and eligible (the pre-key installation; plan 24 reports this state in Hermes' status), or if `orchestrator.providers` names only `test-only` providers (the C10 overlay of §9.2a, an explicit opt-in that the shipped policy may not contain).
2. Primary = first eligible. If `orchestrator.auxiliaryTier` is set, the auxiliary = first candidate of that tier passing the same filters with the **same provider and family** as the primary (none → the primary is reused for the auxiliary slots). Allowlist = `[primary]` plus `[auxiliary]` with `purpose: auxiliary` when it differs (review COST-6). Plan 24 pins every Hermes auxiliary slot (compression, summarization, vision) and the `sc-task-watch` job to this auxiliary model.
3. The Runner (ADR-0401) compares the result with the current Hermes token's allowlist after every valid policy load and at the daily rotation check. On change it mints a new orchestrator token (`tier_requested = tier_effective = orchestrator.tier`, `max_concurrent_requests = orchestrator.maxConcurrentRequests`, `budget_scopes: [orchestration]`). It writes the token and the managed-scope model pins (`provider`, base URL `/p/<provider>`, primary `apiModel`, auxiliary `apiModel`) to Hermes' managed Secret (plan 24) and revokes the old jti after `hermes.overlapHours` (24 h). The managed-scope model pin is updated in the same write, so Hermes never calls a model outside its allowlist for longer than one restart.
4. No eligible candidate → Hermes keeps its current token until it expires. Alert `SCRoutingTierEmpty{tier=<orchestrator tier>, consumer="orchestrator"}` fires.

### 14.7 Worked example (shipped defaults, Claude Code harness `claude`)

| Execution | Event before it | M | tier_requested | tier_effective (source) | Model |
|---|---|---|---|---|---|
| sc-184-x1 tester | — | 0 | standard | standard (role_default) | `anthropic/<balanced>` |
| sc-184-x2 implementer | RED verified | 0 | bargain | bargain (role_default) | `anthropic/<small fast>` (auxiliary: same) |
| sc-184-x3 implementer | GREEN failed (merit) | 1 | bargain | bargain (role_default) | same |
| sc-184-x4 implementer | GREEN failed | 2 | bargain | standard (escalated) | `anthropic/<balanced>` |
| sc-184-x5 implementer | pod evicted (infra, not attributable) | 2 | bargain | standard (escalated) | same; no fallback |
| sc-184-x6 implementer | GREEN failed | 3 | bargain | standard (escalated) | same |
| sc-184-x7 implementer | GREEN failed | 4 | bargain | frontier (escalated) | `anthropic/<largest>` |
| — | GREEN failed | 5 | — | `ATTEMPTS_EXHAUSTED` | task → NEEDS_HUMAN (plan 29) |

With harness `opencode` (families `openai-chat-completions`, `anthropic-messages`, no provider constraint), if x2 fails with three consecutive `529` responses from the first bargain provider, x3 resolves to the next eligible bargain candidate on a different provider with `fallback_applied = true`.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Routing policy invalid (schema or §9.3) | loader error; `SCRunnerPolicyInvalid`, `SCGatewayPolicyInvalid` | LKG kept; if none, no dispatch | none (fail closed) | fix PR; `pricing-update.md` for price-related causes |
| A tier has no eligible candidate for a production harness/role (keys missing, all unpriced, providers down) | per-minute evaluation of the static+dynamic filters for each (enabled production harness, role, default tier) → `sc_routing_tier_eligible_candidates == 0` → **`SCRoutingTierEmpty`** (critical) | dispatches of that pair fail `ROUTING_UNRESOLVABLE` | none | `gateway-failure.md` §provider; `pricing-update.md`; `rotate-model-credentials.md` |
| Individual dispatch unresolvable | `sc_routing_resolutions_total{result="unresolvable"}` increase → **`SCRoutingUnresolvable`** (warning) | Hermes receives `ROUTING_UNRESOLVABLE` with per-candidate reasons | none | inspect details; fix policy or override |
| Provider status stale (Gateway leader not writing) | `max(time() - sc_gateway_provider_status_updated_timestamp_seconds) > 120` | dispatch refused `DEPENDENCY_UNAVAILABLE` | none | `gateway-failure.md` |
| Price expires mid-queue | re-validation at dispatcher step 1 | re-resolved to the next candidate, or FAILED after timeout | `routing_re_resolved` event | `pricing-update.md` |
| Provider answers with a different model | Gateway `SCModelMismatch` (plan 53 §14.2) | possibly mispriced; routing intent violated | row flagged | `gateway-failure.md` §mismatch |
| Repository override names an unknown provider/model | task creation validation | task creation fails `VALIDATION_FAILED` (`details.routing_override_unknown_reference`) | none | fix repository policy |
| Repository override leaves tester, implementer or reviewer with no static candidate at its default tier (capped) | task creation | `ROUTING_UNRESOLVABLE` at task creation | none | fix override |
| Ladder exhausted | resolver | no further attempts for the role | `ATTEMPTS_EXHAUSTED` → plan 29 → NEEDS_HUMAN + `SCTaskNeedsHuman` | `stuck-task-recovery.md` |
| Role tier declarations disagree (plan 25 vs this file) | loader `ROLE_TIER_MISMATCH` | policy set rejected | LKG | fix PR |
| Harness auxiliary env var renamed by a CLI upgrade | R01 smoke: background calls denied `SC_MODEL_NOT_ALLOWED`, or calls to the primary only | harness errors or unexpected spend | none | plan 28 upgrade procedure |

## 16. Retry behavior

The resolver does not retry. It is a pure function, and transient DB read errors surface as `DEPENDENCY_UNAVAILABLE` to the caller (plan 26 decides dispatcher retry: next 2 s cycle). Task-level retries are plan 29's. This plan only defines how a retry's model is chosen: escalation on merit (§14.3), fallback on provider-attributable infrastructure (§14.4), otherwise the same decision as the previous attempt. Plan 26 additionally rejects an identical replay after a deterministic failure.

## 17. Idempotency behavior

- `Resolve` is deterministic (31-R-1). An idempotent `agent_start` replay returns the stored decision and does not re-resolve.
- The decision is recorded once per execution. Re-validation either keeps it or records a new decision with an explicit event. The execution's final decision is the one the token was minted from (the token row stores `claims_sha256`, ADR-0451).
- `ResolveOrchestrator` re-mints only when the allowlist actually changes (comparison by `(provider, api_family, model)` set).

## 18. Bootstrap behavior

- The policy file ships in Git (§9.2) and reaches the Runner and Gateway through Flux at control transfer (C01 prerequisites).
- Before real provider keys exist, or when only some are present, providers without keys are reported unavailable by the Gateway, and production harness dispatch fails `ROUTING_UNRESOLVABLE` with reason `provider_unavailable`. Fake flows (C10) use `stage: test-only` providers and fake harnesses and are unaffected.
- `bootstrap verify` (plan 50) checks that each enabled production provider has `available = true` in `ledger.provider_status` **if** the operator supplied its key, and prints the effective tier → model table (`GET /v1/policy/status` → routing summary, request to plan 26).

## 19. Upgrade behavior

- **New provider or model:** a PR adds the provider, model and tier position, plus a priced entry in `policy/pricing/<provider>.yaml` (plan 32), a budget entry (plan 33), recorded usage fixtures and fresh smoke evidence (53-AC-17). `make policy-check` and `make check-provider-evidence` gate the merge.
- **Model retirement:** set `enabled: false` (or remove it from tiers) in a PR. QUEUED executions re-resolve at dispatcher step 1. Running executions keep their token until terminal.
- **Schema evolution:** additive optional fields stay in `v1alpha1`. Breaking changes bump to `v1alpha2`, and F04 converts during a transition release (master plan §6.2).
- **Version-sensitive third-party behavior this plan depends on:**

| ID | Behavior | Assumption | Verified by |
|---|---|---|---|
| VS-R1 | Claude Code's environment variable for its background/small model | Older CLIs read `ANTHROPIC_SMALL_FAST_MODEL`, newer ones a per-class default such as `ANTHROPIC_DEFAULT_HAIKU_MODEL` (verify for the pinned version); `envHint` is set per pinned version | 31-AC-12 (R01 smoke: zero `SC_MODEL_NOT_ALLOWED` denials for the execution) |
| VS-R2 | Codex can be forced into API-key mode with a custom provider base URL | config key for forced API-key login (verify) | plan 28 R01 smoke (when Codex is enabled) |
| VS-R3 | Anthropic OAuth access tokens start with `sk-ant-oat` | = plan 53 VS-7 | 53-AC-19 |

## 20. Recovery behavior

- Routing has no state of its own beyond Git and `runner.executions`. After an `sc-pg` PITR (plan 12), decisions of executions newer than the recovery point are lost with those executions. Plan 26's reconciliation marks their Jobs `JOB_LOST`. Merit counters are re-derived from the restored task history, so escalation can at worst repeat one rung (safe, bounded by plan 29 attempt limits).
- Policy recovery = Git recovery (Forgejo or the GitHub mirror).

## 21. Observability

**Metrics** (Runner, prefix `sc_routing_`; labels bounded by policy: role ≤ 5, tier 4, provider ≤ 8, model = routing model ids ≤ 32, reason = §7.3 enum. No task or repository labels.)

| Metric | Type | Labels |
|---|---|---|
| `sc_routing_resolutions_total` | counter | role, tier_requested, tier_effective, tier_source, provider, result (`ok`, `unresolvable`, `budget`, `exhausted`, `tier_not_allowed`, `model_not_allowed`) |
| `sc_routing_escalations_total` | counter | role, from_tier, to_tier |
| `sc_routing_fallbacks_total` | counter | role, from_provider, to_provider |
| `sc_routing_candidate_rejections_total` | counter | tier, model, reason |
| `sc_routing_tier_eligible_candidates` | gauge (per-minute evaluation) | harness, role, tier (production harnesses and their roles' default tiers only) |
| `sc_routing_re_resolutions_total` | counter | reason |

`sc_routing_unpriced_candidates{tier}` is exported by the Gateway (plan 32 §21).

**Logs:** one structured line per resolution, `msg=routing_resolved`, containing task id, execution id, role, harness, tiers, source, model id, candidate index, fallback flag and the rejected-candidate reasons. No content.

**Traces** (`contracts/telemetry.schema.yaml`): span **`sc.routing.resolve`**, a child of the Runner's `sc.execution.dispatch`, with attributes `sc.role`, `sc.harness`, `sc.routing.requested_tier`, `sc.routing.effective_tier`, `sc.routing.tier_source`, `sc.routing.candidate_index`, `sc.routing.fallback` (= `fallback_applied`), `sc.routing.result` (the `sc_routing_resolutions_total` `result` value), `gen_ai.provider.name` and `gen_ai.request.model`. The dispatch span also keeps `sc.model_tier` (= `tier_effective`).

**Dashboards** (plan 39, SPEC §92 "escalation frequency", "cheap-tier success"): fed by plan 32's queries over `runner.executions.routing` (Q6, Q7, escalation frequency panel) and by `sc_routing_escalations_total`.

**Alerts introduced** (IDs for plan 42, runbook `procedures/gateway-failure.md` §routing):
- `SCRoutingTierEmpty` (critical, `min by (harness, role, tier) (sc_routing_tier_eligible_candidates) == 0` for 5m);
- `SCRoutingUnresolvable` (warning, `increase(sc_routing_resolutions_total{result="unresolvable"}[15m]) > 0`).

## 22. Testing strategy

All deterministic, no paid model calls [SPEC §104].

- **Contract (F02):** `model-routing.schema.yaml` meta-validation, examples valid, `x-invalid-examples` invalid.
- **Loader unit tests:** one negative fixture per §9.3 invariant and per schema-description invariant (`testdata/policy/routing-invalid/*.yaml`, each with an expected error string).
- **Resolver golden tests:** `testdata/routing/cases.yaml`, ≥ 50 cases, each `{inputs, expected decision | expected error + details}`. Required coverage:
  - every filter's rejection reason;
  - stage separation in both directions;
  - explicit model (eligible, ineligible, below the escalated tier);
  - each ladder boundary (M = n₀−1, n₀, …, total);
  - a requested tier above rung k (no de-escalation);
  - repository cap below the requested tier (error) and below the escalated tier (`repository_capped`);
  - fallback: attributable vs not attributable, `maxFallbacksPerTask` reached, no alternative provider;
  - auxiliary: found, none, different provider;
  - stale provider status;
  - headroom exactly equal to the minimum (eligible) and 1 µUSD below (rejected);
  - orchestrator resolution including the test-only-only installation.
- **Property tests:**
  - determinism (100 random permutations of map iteration order give identical output);
  - tighten-only: for random overrides, eligible(override) ⊆ eligible(none);
  - escalation monotonicity in M;
  - `allowed_models` ⊆ the eligible set and has length ≤ 2.
- **Integration (k3d, `make e2e-k3d`):** Runner + Gateway + fake upstream + fake harnesses with a routing test policy containing two test-only providers (`fake`, `fake-b`).
- **Real-provider smoke (opt-in):** covered by 53-AC-17 / 53-AC-23. Routing adds only 31-AC-12 at R01.

## 23. Acceptance criteria

### GATE-C05

- **31-AC-1** `make contracts` passes for `model-routing.schema.yaml`: meta-schema valid, both `examples` validate, all five `x-invalid-examples` fail (including the `oauth-subscription` credential kind, a family without `requestPolicy`, and a model without `maxReasoningTokens` whose cap does not cover reasoning).
- **31-AC-2** `make policy-check` exits 0 on the shipped `policy/` tree. For each file in `testdata/policy/routing-invalid/` it exits non-zero and prints the expected invariant id (`ROLE_TIER_MISMATCH`, `TIER_STATICALLY_EMPTY`, ladder anchoring, stage mixing, duplicate `secretKey`, cluster-internal production `baseUrl`, unknown tier reference, a forbidden beta or provider-hosted tool type in `requestPolicy` (invariant 7), a production provider host missing from `forbiddenHosts` (invariant 8)).
- **31-AC-3** `go test ./... -run 'TestRouting(Golden|Property)'` passes. The golden file has ≥ 50 cases and covers every `reason` enum value (a test asserts set coverage).
- **31-AC-9** Unresolvable: with every bargain candidate disabled in a test policy, `agent_start` (implementer, bargain) returns `ROUTING_UNRESOLVABLE` whose `details.rejected` lists every candidate with reason `disabled`, and `sc_routing_resolutions_total{result="unresolvable"}` increments by 1.
- **31-AC-10** Unpriced and unavailable candidates are skipped: marking the first bargain candidate `status: unpriced` (test pricing), or setting its provider key to empty (Gateway reports `available=false` within 40 s), makes the next resolution select `candidate_index = 1`.

### GATE-C10 (moved from GATE-C05: these need fake-harness Jobs (C02), GREEN evidence (C04) or base-revision reads from Forgejo (C03), which C05 does not depend on; review COST-10 / GC-01)

- **31-AC-4** Harness compatibility (k3d): `agent_start` with harness `fake-success` (test-only) for role implementer resolves to a `fake` provider model. The stored `runner.executions.routing` validates against `model-routing.schema.yaml#/$defs/RoutingDecision`, and the minted token's `allowed_models` equals `[{provider: fake, api_family: fake, model: fake-model, purpose: primary}]` (decoded from the per-execution Secret in the test namespace).
- **31-AC-5** Allowlist enforcement: the fake harness scenario `fake-model-switch` (requests a second model mid-execution, plan 28) gets `SC_MODEL_NOT_ALLOWED` for the second model, and `SELECT count(*) FROM ledger.denials WHERE execution_id=$x AND code='SC_MODEL_NOT_ALLOWED'` = 1.
- **31-AC-6** Escalation ladder (k3d, fake upstream): a task driven by `fake-green-after-retry` configured to fail GREEN 4 times yields implementer executions whose `routing->>'tier_effective'` sequence is `bargain, bargain, standard, standard, frontier` under the shipped ladder `bargain×2, standard×2, frontier×1` (run with both the `e2e-test` policy and the **shipped** policy, review COST-8). The next `agent_start` for the implementer returns `ATTEMPTS_EXHAUSTED` with `details.detail = "escalation_ladder_exhausted"`.
- **31-AC-7** Infrastructure does not escalate: a sequence (merit, infra-evicted, merit) yields M = 2 at the fourth resolution (`merit_failures_at_resolution` = 2), not 3.
- **31-AC-8** Provider fallback (k3d): with test providers `fake` (first) and `fake-b`, and `fake` scripted to return `529` on every request (`fake-529` scenario), the second implementer execution resolves to `fake-b/fake-model-b` with `fallback_applied = true` and one `routing_fallback_applied` event. With `maxFallbacksPerTask: 0` it resolves to `fake` again.
- **31-AC-11** Repository override (k3d fixture repository whose base-revision `.strange-company/policy.yaml` sets `modelRouting.deniedProviders: [fake]`): resolution selects `fake-b`. An override with `maxTier: bargain` makes `agent_start` with `model_tier: standard` fail `MODEL_TIER_NOT_ALLOWED`. A change of the override on the agent branch only (not the base) has no effect (ADR-0017).

- **31-AC-14** C10 Hermes overlay (§9.2a): after `sc-e2e` applies the overlay, within 120 s the newest non-revoked `orchestrator` token row decodes to `allowed_models = [{provider: fake, api_family: fake, model: fake-hermes-watch, purpose: primary}]`; a Hermes watcher run produces `ledger.cost_records` rows with `model_requested='fake-hermes-watch'` and zero `SC_MODEL_NOT_ALLOWED` denials. After the overlay is reverted, the orchestrator token returns to the shipped allowlist within 120 s. `make policy-check` on a `policy/` tree containing the overlay's `orchestrator` block fails.

### Proposed contribution to GATE-C08 (owner plans 24/26)

- **31-AC-13** After a policy PR changes `orchestrator.tier`'s first eligible candidate, within 120 s of the Runner loading the revision a new `ledger.gateway_tokens` row with `token_kind='orchestrator'` exists whose decoded `allowed_models[0].model` equals the new primary. The Hermes managed Secret's model pin equals it. The previous jti has `revoked_at` set within `overlapHours` + 5 min (test config overlap: 1 min). With `auxiliaryTier: clerical`, the token also carries one `purpose: auxiliary` entry, and a scripted Hermes session that triggers context compression and one `sc-task-watch` run produces zero `SC_MODEL_NOT_ALLOWED` denials for the Hermes jti (review COST-6).

### Proposed contribution to GATE-R01 (owner plan 28)

- **31-AC-12** For the pinned Claude Code image, a real-harness smoke task produces zero `ledger.denials` rows with `SC_MODEL_NOT_ALLOWED` and every `ledger.cost_records.model_requested` of the execution ∈ {primary, auxiliary} of its `RoutingDecision` (verifies VS-R1). The job spec contains no volume or env var carrying a human credential file (`yq` assertion on the rendered Job: no `~/.claude`, `~/.codex/auth.json`, `CLAUDE_CODE_OAUTH_TOKEN`).

## 24. Explicit non-goals

- Dynamic routing by live latency, price comparison or provider load; A/B or random routing.
- Translation between API families (ADR-0004). A harness can only use models of a family it speaks.
- Per-repository *expansion* of providers or models (overrides are tighten-only).
- Forcing reviewer and implementer onto different model families. SPEC §48 permits the same family. A future policy flag may add it.
- Routing inside a single execution (a harness choosing models per turn beyond the primary/auxiliary allowlist).
- Google native API routing (post-MVP; plan 53 §7.1).
- Automatic escalation of reviewer, planner or researcher (no default ladders; a ladder can be added by policy PR).

## 25. Open questions

1. **Role tier duplication (plan 25 `RoleDefinition` vs `ModelRouting.roles`).** Blocking: no. Resolution adopted: both must agree (invariant 1, §9.3). A later consolidation may drop one declaration. If plan 25 removes its fields, invariant 1 becomes vacuous. Owner: plans 25/31.
2. **Merit-failure events and generation reset.** Resolved by CD-09: plan 29's `merit_by_generation` counter is consumed as written there (§14.3). Owner: plan 29.
3. **`ROUTING_UNRESOLVABLE` error code** must be added to `mcp-surface.schema.yaml` (plan 26). Blocking: no (31-AC-9 enforces it at C05). Owner: plan 26.
4. **Shipped tier contents need real model strings and prices** at implementation. Blocking: no for C05 (fake providers); yes before enabling a production provider (53-AC-17, plan 32 `make check-pricing`). Owner: plans 31/32 at implementation.
5. **New fake scenarios requested from plans 28 and 53.** The fake harness needs `fake-model-switch`. The fake upstream needs `fake-529` and must serve a second provider under the path prefix `/b/v1` (the `fake-b` provider in the `e2e-test` example of `model-routing.schema.yaml`). Blocking: no. Owner: plans 28/53.
