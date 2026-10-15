# ADR-0004: All billable model traffic crosses a Strange Company Model Gateway

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §78–§82 require exact per-request accounting attributable to task, execution, attempt, role, harness, provider and model, plus *preventive* circuit breakers and an emergency stop that keeps core services running. SPEC §86 says workers receive only what their task needs, and §88 restricts worker egress to approved model APIs. Harness self-reported cost is unreliable: opencode drops usage events in containers, and Codex reports no cost (L-3). Accounting from harness output alone would leave the budget blind.

## Decision
- A Strange Company–owned **Model Gateway** (`model-gateway`, namespace `strange-company`, Go) is the only network path from workers and Hermes to model providers.
- It is a **protocol-native pass-through**. Each upstream API (Anthropic Messages, OpenAI Responses or Chat Completions, OpenAI-compatible APIs such as DeepSeek or GLM, Google) gets its own route that preserves the provider wire format. The Gateway does not translate between providers. Harnesses are pointed at it through their standard base-URL settings (for example `ANTHROPIC_BASE_URL`, OpenAI `base_url`, OpenCode provider `baseURL`).
- Callers authenticate with a short-lived **gateway token** (`SC-GATEWAY-TOKEN`) minted by the Runner per execution, or a long-lived revocable token for Hermes. The token's claims bind task, execution, attempt, role, harness, the allowed provider and model set, and budget scope. The Gateway swaps the token for the real provider key (`SC-MODEL-API-KEYS`), which workers never see.
- **Per request**, the Gateway: authenticates; checks the emergency stop and budgets *before* forwarding (estimated worst-case cost from `max_tokens` and the price table, denying on unknown price); forwards; streams the response through; parses the provider-reported usage (including streaming terminal events); and writes one immutable `cost-record` row to `sc-pg` with the pricing version used. A request whose usage cannot be parsed is recorded as `usage_status: unknown` and counts against the budget at its pre-authorized worst case. It is never recorded as zero.
- It validates that the provider-reported model matches the requested and allowed model, and raises an alert on mismatch (L-4).

## Consequences
- Accounting no longer depends on harness behavior. Harness-reported usage is kept only as a cross-check.
- One critical-path component is added. Gateway downtime stops all autonomous model spending; that is fail-safe, and the Gateway has its own SLO and alerts (plan 53).
- Prompts and completions pass through the Gateway. It must not log or persist bodies by default (SPEC §94); verbose capture needs an explicit, time-limited debug flag.
- Provider-specific streaming parsers are a maintenance cost. They are contract-tested against recorded fixtures.

## Alternatives rejected
- LiteLLM proxy: mature, but its translation layer, Python runtime and own database schema add surface. Its attribution model (virtual keys with metadata) would need the same token minting anyway. Revisit if the protocol-parsing burden grows.
- Harness self-reporting only: blind for opencode and Codex cost (L-3), and it cannot prevent overspend.
- Provider-side project or budget features: they are not uniform across providers and not request-granular.

## Verification
C05 and C06 gates: a fake provider upstream returns recorded usage fixtures (streaming and non-streaming) and every request yields exactly one ledger row; an unknown price is denied; an exhausted budget is denied before the upstream is contacted; emergency stop returns a deterministic error within one request; workers cannot reach provider endpoints directly (NetworkPolicy test).

## Amendments (Stage-0 review fixes)
- **2026-10-14, COST-1 (cost-accounting review).** The Gateway is an **allowlisted, token-priced request surface**, not a general pass-through. Per provider and API family, `contracts/model-routing.schema.yaml` lists the request features (fields, tool types, content-block kinds) that may be forwarded. Everything else is denied before the provider is contacted. That includes provider-hosted tools that fetch URLs, MCP connectors, URL or file sources, and provider-side conversation or state stores. An approved model API is therefore approved only as token-priced inference, which keeps SPEC §88 egress control and SPEC §78 accounting exact (SI-37). Verification adds 53-AC-28/29: each denied feature class is rejected with `SC_REQUEST_FEATURE_DENIED`, and no upstream request is made.
- **2026-10-14, COST-3.** Contention between concurrent in-flight reservations is not exhaustion. The Gateway waits up to 30 s, then returns a retryable `SC_BUDGET_CONTENDED`, which is classed as `infrastructure` and refunded (plan 29).
