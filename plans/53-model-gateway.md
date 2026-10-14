# 53 — Model Gateway

**Status:** Stage-0 plan (draft)
**DAG nodes:** C05 (primary, with plans 32 and 31). Contributes checks proposed for C08 (owner plan 24/26) and R01 (owner plan 28).
**Related plans:** [31 model routing](31-model-routing.md), [32 cost accounting](32-cost-accounting.md), [33 budget enforcement](33-budget-enforcement.md), 24 (Hermes), 26 (Agent Runner), 27 (worker contract), 28 (harnesses), 12 (CNPG), 35 (secrets), 36 (network policy), 38/39/40/42 (observability), 43 (retention), 54 (chart)
**Contracts:** owns `contracts/gateway-token.schema.yaml` and `contracts/gateway-status.schema.yaml`; consumes `model-routing.schema.yaml`, `pricing.schema.yaml`, `budget-policy.schema.yaml`, `cost-record.schema.yaml`, `harness.schema.yaml` (plan 28)
**ADRs:** relies on ADR-0004, ADR-0003, ADR-0006, ADR-0009, ADR-0011, ADR-0014, ADR-0017; introduces ADR-0450 (reservation-based pre-authorization, integer micro-USD) and ADR-0451 (gateway token: Runner-signed JWT plus DB registration)

> Every section below is mandatory (SPEC §121). Requirements carry SPEC trace tags.

## 1. Purpose

The Model Gateway (`model-gateway`, Go, namespace `strange-company`) is the only network path from worker Jobs and Hermes to model providers (ADR-0004). It is a protocol-native pass-through proxy. For every provider request it:

1. authenticates a Strange Company gateway token;
2. authorizes the requested model and checks the emergency stop;
3. pre-authorizes a worst-case cost reservation against every applicable budget scope, atomically;
4. swaps the token for the real provider key and forwards;
5. streams the response through while parsing usage;
6. settles the reservation to the actual cost; and
7. writes exactly one `cost-record`.

The Gateway holds the provider keys (`SC-MODEL-API-KEYS`), so callers never see them. It is where exact accounting [SPEC §78, §79], preventive budgets [SPEC §81] and the spend side of the emergency stop are enforced.

Plan 32 owns pricing and usage normalization, plan 33 owns budget semantics and the emergency stop, and plan 31 owns routing. This plan owns the runtime that executes them.

## 2. Requirements derived from SPEC.md

1. Every billable model invocation is attributable to provider, model, harness, role, execution, attempt, task, PR, repository and project. The accounting unit is one provider request. [SPEC §78]
2. Every invocation retains the SPEC §79 field set, including the pricing version, so historical reports stay reproducible. [SPEC §79]
3. Accounting supports prevention: per-request, execution, task, token, daily/monthly provider and global limits, plus an emergency stop. [SPEC §81]
4. New autonomous spending can be stopped without shutting down core services. [SPEC §81]
5. Workers receive only what their task needs, never provider keys (SI-09). [SPEC §86]
6. Worker egress to model APIs goes only through approved paths. [SPEC §88]
7. No default capture of prompts, completions, source or secrets in telemetry or logs. [SPEC §94]
8. OTel spans for model invocations are children of the execution. [SPEC §93]
9. Model-cost aggregates go to Prometheus without unbounded labels. [SPEC §90, §91]
10. CI needs no paid model APIs; a fake provider upstream exists. [SPEC §104]
11. Only API credentials are used, never consumer subscriptions. [SPEC §77]
12. Alerts cover budget thresholds and anomalous spend. [SPEC §96]

Derived requirements:

- **53-R-1** Exactly one `ledger.reservations` row per forwarded request, and exactly one `ledger.cost_records` row per reservation, written by settlement or by the expiry sweeper. Denied requests produce one `ledger.denials` row and no cost record.
- **53-R-2** The Gateway never forwards a request it has not reserved for. If the DB is unavailable it does not forward (fail closed).
- **53-R-3** The Gateway never forwards to a model without an effective `priced` pricing entry (SC_PRICE_UNKNOWN). "Unpriced" is never treated as zero (L-3).
- **53-R-4** Usage that cannot be parsed is charged at the reserved worst case and recorded `usage_status: unknown`, never as zero (ADR-0004).
- **53-R-5** The provider-reported model is compared with the requested model, and a mismatch is recorded and alerted (L-4).
- **53-R-6** The Gateway performs no upstream retries. Exactly one upstream attempt is made per inbound request (§16).
- **53-R-7** The upstream URL is always taken from policy, never from the request, so the Gateway is not an open proxy.
- **53-R-8** Request and response bodies are never logged or persisted. Body capture does not exist in MVP.
- **53-R-9** Added latency (excluding upstream) is p99 ≤ 50 ms before the first upstream byte, and ≤ 1 ms per streamed chunk.
- **53-R-10** Gateway unavailability means no autonomous spend (fail-safe), and readiness reflects ability to account.
- **53-R-11** The Gateway forwards only an **allowlisted, token-priced request surface** per (provider, family): unknown body members, tool types and beta headers are denied, and features that make the provider fetch arbitrary hosts, bill outside token pricing or bill context absent from the body are always denied (§7.3; review COST-1, SPEC §78, §81, §88).

## 3. Inputs

| Input | Source | Delivery |
|---|---|---|
| Gateway token (JWS) | Runner mints per execution (plan 26); Hermes token minted by the Runner, delivered by plan 24 | Request header `Authorization: Bearer` or `x-api-key` |
| Provider API request | Harness CLI (plan 28) or Hermes | HTTP/1.1 or h2c to Service `model-gateway:8080` |
| `ModelRouting` | `policy/model-routing.yaml` (plan 31) | Flux-generated ConfigMap `sc-policy` mounted at `/etc/strange-company/policy/` (D-17; ConfigMap name per plan 54) |
| `PricingTable`s | `policy/pricing/*.yaml` (plan 32) | same ConfigMap |
| `BudgetPolicy` | `policy/budgets.yaml` (plan 33) | same ConfigMap |
| `HarnessRegistry` | `policy/harnesses.yaml` (plan 28) | same ConfigMap (read for `hermes` reservation checks and loader validation) |
| Runner JWKS (public keys of `SC-RUNNER-SIGNING`) | plan 35 | ConfigMap `sc-runner-jwks` mounted at `/etc/strange-company/jwks/jwks.json` |
| Provider API keys | `SC-MODEL-API-KEYS`, Secret `model-api-keys` (SOPS file `gitops/clusters/<cluster>/secrets/strange-company/model-api-keys.sops.yaml`, plan 35 register) | Secret volume at `/var/run/secrets/strange-company/model-api-keys/<secretKey>` |
| Token registry, e-stop events, budget-scope limits | written by the Runner in `sc-pg` schema `ledger` | PostgreSQL (role `sc_gateway`) |
| Kubernetes e-stop flag | ConfigMap `sc-emergency-stop` in `strange-company`, created by an operator, never in Git | Kubernetes API GET, polled (plan 33 §14.6) |

## 4. Outputs

| Output | Consumer |
|---|---|
| Proxied provider responses (status, allowed headers, body/SSE stream), plus header `x-sc-gateway-request-id` | Harness / Hermes |
| Deterministic denial responses in provider error envelopes (§7.3) | Harness / Hermes; surfaced by sc-worker (plan 27) |
| `ledger.reservations`, `ledger.cost_records`, `ledger.denials`, `ledger.pricing_snapshots` rows; spent/reserved columns of `ledger.budget_scopes` | Plans 32, 33, 26 (dispatch checks, failure classification), Grafana PG data source (plan 39) |
| Prometheus metrics on the container port named `metrics` (9090, CD-02), path `/metrics` (§21) | Prometheus (plan 38), alert rules (plan 42) |
| OTel spans `sc.model.invocation` with children `sc.gateway.reserve`, `sc.gateway.upstream`, `sc.gateway.settle` (`contracts/telemetry.schema.yaml`; no content) | OTel Collector (plan 40) |
| Status document `GET :9090/sc/v1/status` | Operators (procedures), Runner admin API (plan 26) |

## 5. Dependencies

**Hard (DAG):** C05 depends on C01 (Runner core: `sc-pg`, the migration mechanism, token minting, the `ledger` schema migrations being run). Through C01 it also depends on F04 (policy-lib loads and validates the policy files), F13 (chart skeleton) and P11 (signed images).

**Soft / runtime:**

- `sc-pg` (plan 12) must be reachable. Without it the Gateway is not ready (fail closed).
- Kubernetes API, used for the e-stop ConfigMap poll. If it is unreachable the Gateway degrades as described in plan 33 §14.6.
- NetworkPolicy (plan 36):
  - ingress to `:8080` from `agent-jobs` (worker pods), `hermes` and the fake-harness test pods;
  - ingress to the `metrics` port (9090) from `monitoring` and from the Runner (status reads, plan 33 §7.4); policies select the port by name (CD-02);
  - egress to provider APIs on TCP 443, to `sc-pg` on 5432, to the kube-apiserver, to DNS, and to `fake-model-upstream:8080`.
  - The Gateway is the only workload with an internet egress rule towards **model providers**. `agent-jobs` and `hermes` have no internet egress at all. Within `strange-company` the Runner (object storage) and the egress proxy also hold internet egress (plan 36 §9.4, Q-36-04), and the Gateway's other egress is cluster-internal (kube-apiserver, `sc-pg`, DNS, fake upstream). FQDN filtering is not possible with kube-router (ADR-0011). That is why the Gateway pins upstream hosts itself (53-R-7).
- OTel Collector (plan 40): optional; span export failures never affect requests.
- Plan 28 harness configuration: base URLs point at the Gateway (§7.1), and the output cap is configured ≤ `perRequest.maxOutputTokens`.

## 6. Owned state

The Gateway is the **system of record for the invocation ledger** (with plan 32 owning its semantics). Everything lives in database `sc-pg`, schema `ledger`:

| Table | Writer | Readers | Mutability |
|---|---|---|---|
| `ledger.reservations` | Gateway | Gateway, Runner | state transitions `open → settled` or `open → expired` only |
| `ledger.cost_records` | Gateway (insert only) | Runner, `sc_reporting` | immutable: no UPDATE/DELETE grants, and a trigger rejects them. Retention deletion only via the owner function of plan 43. |
| `ledger.denials` | Gateway (insert only) | Runner (failure classification), reporting | immutable; 90-day retention |
| `ledger.pricing_snapshots` | Gateway (insert-if-absent) | reporting | immutable |
| `ledger.budget_scopes` columns `spent_micro_usd`, `reserved_micro_usd`, `spent_tokens`, `reserved_tokens`, `request_count` | Gateway | Runner, reporting | Gateway-only columns |
| `ledger.budget_scopes` columns `limit_*` and the row itself for `task:`/`execution:` scopes | Runner (plan 33) | Gateway | Runner-only columns (column-level grants) |
| `ledger.gateway_tokens` | Runner | Gateway (SELECT) | Runner inserts and sets `revoked_at` |
| `ledger.emergency_stop_events` | Runner admin API | Gateway (SELECT) | append-only |
| `ledger.provider_status` (one row per provider: `provider`, `available`, `reason`, `key_fingerprint`, `updated_at`) | Gateway leader (upsert every 30 s) | Runner (routing eligibility, plan 31 §14.2) | overwritten in place; derived, rebuildable |

DDL is owned by plans 32 (ledger, pricing snapshots) and 33 (scopes, reservations, e-stop, denials). **The Runner owns every `sc-pg` migration, including the `ledger` schema (CD-04).** Migrations run in-process at Runner startup under its Postgres advisory lock and are forward-only (plan 26 §19). **The Gateway never migrates.** It reads `max(version)` from `runner.schema_migrations` (SELECT grant for `sc_gateway`) at startup and every 30 s, and is not ready while that value is below its compiled `requiredSchemaVersion` (readiness reason `schema`).

The Gateway holds no other durable state. In-memory state (loaded policy, keys, e-stop sources, concurrency counters) is reconstructible.

## 7. External interfaces

### 7.1 Routes

The Service is `model-gateway.strange-company.svc.cluster.local`, port 8080 (HTTP/1.1 and h2c). Path scheme: `/p/<provider-id>/v1/<family path>`. The prefix `/p/<provider-id>/v1` is replaced by the provider's `upstream.baseUrl` (plan 31).

| Gateway path | Method | API family | Billable | Upstream example | MVP |
|---|---|---|---|---|---|
| `/p/{provider}/v1/messages` | POST | `anthropic-messages` (non-streaming and SSE) | yes | `https://api.anthropic.com/v1/messages` | yes |
| `/p/{provider}/v1/messages/count_tokens` | POST | `anthropic-messages` | no (pass-through, counted in metrics, no reservation, no cost record) | `.../v1/messages/count_tokens` | yes |
| `/p/{provider}/v1/responses` | POST | `openai-responses` (non-streaming and SSE) | yes | `https://api.openai.com/v1/responses` | yes |
| `/p/{provider}/v1/chat/completions` | POST | `openai-chat-completions` or `fake` (non-streaming and SSE) | yes | `https://api.deepseek.com/v1/chat/completions`, `https://api.z.ai/api/paas/v4/chat/completions` (verify) | yes |
| `/p/{provider}/v1/models` | GET | any | no; answered **locally** from the token's `allowed_models` (no upstream call) | — | yes |
| `/p/{provider}/v1beta/models/{model}:generateContent`, `:streamGenerateContent` | POST | `google-generative` | yes | Google Generative Language API | **post-MVP**: answers 501 `SC_FAMILY_NOT_SUPPORTED` |
| anything else | any | — | — | — | 404 `SC_ROUTE_NOT_FOUND` |

Explicitly not routed: batch APIs, files and uploads, embeddings, realtime/WebSocket upgrades, Responses `GET`/`DELETE`/`cancel` by id, and fine-tuning. Batch and background billing is asynchronous and cannot be settled per request.

The route family must be declared for that provider in `ModelRouting`. Otherwise the request is denied with `SC_FAMILY_NOT_ALLOWED`.

**Decision: Google Gemini is post-MVP.** No MVP harness requires it. Claude Code speaks Anthropic Messages, Codex speaks OpenAI Responses, and OpenCode and Hermes can use OpenAI-compatible Chat Completions. Its usage dialect (`thoughtsTokenCount` reported separately from `candidatesTokenCount`, model in the URL path, `key` query parameter) needs its own parser and fixtures. The path shape is reserved so that adding it is additive. ADR-0004 lists Google among the routes; this is a sequencing deferral, recorded in §25.

Harness base-URL settings (plan 28 configures them; this plan defines the values):

| Harness | Setting | Value |
|---|---|---|
| Claude Code | `ANTHROPIC_BASE_URL` | `http://model-gateway.strange-company.svc.cluster.local:8080/p/anthropic` (Claude Code appends `/v1/messages`) |
| Codex | provider `base_url` (via `SC_CODEX_BASE_URL`), `wire_api = "responses"` | `http://model-gateway.strange-company.svc.cluster.local:8080/p/openai/v1` |
| OpenCode | provider `options.baseURL` | `.../p/<provider>/v1` |
| Hermes (plan 24) | custom OpenAI-compatible or Anthropic base URL in managed scope | `.../p/<provider>/v1` or `.../p/<provider>` |
| Fake harness | `SC_FAKE_GATEWAY_URL` | `.../p/fake/v1` |

### 7.2 Header policy

**Inbound authentication.** The gateway token is accepted from `Authorization: Bearer <jws>` or `x-api-key: <jws>`, whichever a harness SDK sends. Both are removed before forwarding. If both are present and differ, the request is denied with `SC_TOKEN_INVALID`.

**Request headers stripped:** `authorization`, `x-api-key`, `x-goog-api-key`, `cookie`, `proxy-*`, `forwarded`, `x-forwarded-*`, `x-real-ip`, `host`, hop-by-hop headers (RFC 9110 §7.6.1), `openai-organization`, `openai-project`, `anthropic-dangerous-direct-browser-access`, and every `x-sc-*` header.

**`anthropic-beta` is allowlisted** (review COST-1). Every comma-separated item must match an entry of the family's `requestPolicy.anthropicBeta` (exact value, or prefix ending in `*`); anything else, and any item starting with a prefix in `headerPolicy.anthropicBetaDenyPrefixes` (default `oauth-`; subscription auth is rejected [SPEC §77]), is refused with `SC_REQUEST_FEATURE_DENIED`. Betas that enable provider-side tools, MCP connectors, file APIs or a larger context window (e.g. `mcp-client-*`, `files-api-*`, `code-execution-*`, `web-fetch-*`, `context-1m-*`) are therefore unusable unless a PR adds them; a window-enlarging beta additionally needs a separate model id whose `contextWindowTokens` covers it (plan 31 §9.3 invariant 7).

**Other request headers pass through:** `anthropic-version`, `content-type`, `accept`, `user-agent`, `x-stainless-*` and `traceparent`. None of them changes what is billed.

**Injected:** the provider credential per `authScheme` (`x-api-key: <key>` or `Authorization: Bearer <key>`), and `content-length` after any body mutation. `x-sc-*` headers are never sent upstream.

**Response headers passed** (allowlist): `content-type`, `cache-control`, `retry-after`, `retry-after-ms`, `request-id`, `x-request-id`, `anthropic-ratelimit-*`, `x-ratelimit-*`, `openai-processing-ms`, `openai-version`, `x-should-retry`. All others are dropped, including `set-cookie`, organization ids and `alt-svc`. Every response also carries `x-sc-gateway-request-id: gw_<ulid>`.

**Query strings** pass through, except the parameter `key`, which is removed. Claude Code's `?beta=true` is preserved.

### 7.3 Denial and error contract

Denials are returned *in the caller's provider error envelope*, so SDKs and CLIs surface the message. Each denial also carries the header `x-sc-error-code: <CODE>` and writes a `ledger.denials` row (except codes marked †). Every message starts with the code, e.g. `SC_BUDGET_EXHAUSTED: scope task:sc-184 limit 25.000000 USD`.

- Anthropic envelope: `{"type":"error","error":{"type":"<anthropic type>","message":"<CODE>: ..."},"sc_error":{"code":"<CODE>","scope":...}}`.
- OpenAI envelope: `{"error":{"message":"<CODE>: ...","type":"<openai type>","param":null,"code":"<CODE>"}}`.

| Code | HTTP | Anthropic type / OpenAI type | When | Retryable by caller |
|---|---|---|---|---|
| `SC_TOKEN_MISSING`† | 401 | authentication_error / invalid_request_error | no token | no |
| `SC_TOKEN_INVALID`† | 401 | authentication_error | bad signature/typ/aud/iss/claims schema, or the registry digest differs | no |
| `SC_TOKEN_EXPIRED` | 401 | authentication_error | `exp` passed | no |
| `SC_TOKEN_REVOKED` | 401 | authentication_error | registry row missing or revoked | no |
| `SC_ROUTE_NOT_FOUND`† | 404 | not_found_error | unrouted path | no |
| `SC_FAMILY_NOT_SUPPORTED`† | 501 | api_error | Google family (MVP) | no |
| `SC_FAMILY_NOT_ALLOWED` | 403 | permission_error | provider does not declare the family | no |
| `SC_PROVIDER_UNAVAILABLE` | 503 | api_error / server_error | provider disabled, or key missing, empty or forbidden-prefixed | no (operator action) |
| `SC_MODEL_NOT_ALLOWED` | 403 | permission_error | (provider, family, model) not in `allowed_models` | no |
| `SC_BODY_INVALID` | 400 | invalid_request_error | body is not JSON, or `model` is missing | no |
| `SC_BODY_TOO_LARGE` | 413 | request_too_large / invalid_request_error | body > `perRequest.maxRequestBodyBytes` | no |
| `SC_REQUEST_FEATURE_DENIED` | 400 | invalid_request_error | any request feature outside the family's `requestPolicy` allowlist or on the always-denied list (§7.3), named by ADR-0004's COST-1 amendment. `details.feature_class` ∈ {`body_member`, `tool_type`, `beta_header`, `content_source`, `conversation_state`, `pinned_parameter`, `subscription_auth`} and `details.member` names the feature (never its value) | no |
| `SC_MAX_TOKENS_EXCEEDS_POLICY` | 400 | invalid_request_error | requested output cap > `perRequest.maxOutputTokens` | no |
| `SC_PRICE_UNKNOWN` | 403 | permission_error | no effective `priced` entry for (provider, model, service tier) | no |
| `SC_REQUEST_COST_CAP` | 403 | permission_error | worst case > `perRequest.maxCostUsd` | no |
| `SC_BUDGET_UNDEFINED` | 403 | permission_error | a required scope row or provider budget is missing | no |
| `SC_BUDGET_EXHAUSTED` | 403 | permission_error | any scope lacks headroom on **settled** spend: `spent + amount > limit` (or tokens, or `maxRequests`) | no |
| `SC_BUDGET_CONTENDED` | 429 | rate_limit_error | headroom exists on settled spend but is held by open reservations (`spent + amount ≤ limit < spent + reserved + amount`) after the bounded wait of plan 33 §14.2 (review COST-3) | yes (`retry-after: 5`) |
| `SC_EMERGENCY_STOP` | 403 | permission_error | effective e-stop level covers this token kind | no |
| `SC_CONCURRENCY_LIMIT` | 429 | rate_limit_error | per-token in-flight ≥ `max_concurrent_requests` | yes (`retry-after: 1`) |
| `SC_GATEWAY_OVERLOADED`† | 429 | rate_limit_error | replica in-flight ≥ `limits.maxInflightPerReplica` | yes (`retry-after: 2`) |
| `SC_LEDGER_UNAVAILABLE`† | 503 | api_error / server_error | DB unreachable or txn failed | yes (`retry-after: 5`) |
| `SC_UPSTREAM_UNREACHABLE` | 502 | api_error | connect/TLS/DNS failure (cost record `upstream_unreachable`) | caller decides |
| `SC_UPSTREAM_TIMEOUT` | 504 | api_error | response-header timeout (cost record `upstream_timeout`) | caller decides |

Budget-class denials use 403, not 429, because SDKs auto-retry 429 and 5xx. A budget or e-stop denial must stop the harness, not loop it. That harnesses really stop after a 403 is a third-party assumption (VS-9), verified at R01 and C08 (53-AC-33). `SC_BUDGET_CONTENDED` is deliberately 429: contention is transient, and the SDK retry is the desired behaviour.

**Request feature allowlist (review COST-1).** The inspector forwards a request only if every feature is explicitly allowed for the route's (provider, family) in `ModelRouting.providers.<p>.apiFamilies.<f>.requestPolicy`:

1. every top-level body member ∈ `bodyMembers` (unknown members are denied, not ignored);
2. every `tools[].type` ∈ `toolTypes` (`custom` covers Anthropic client tools without `type`). Tool-type matching is exact, so dated variants of provider-hosted tools (`web_search_preview_2025_03_11`, `web_search_20250305`) and Chat-route provider tools (Z.ai `web_search`/`retrieval`) are denied unless listed;
3. every `anthropic-beta` item ∈ `anthropicBeta` (§7.2);
4. every pinned member absent or equal to its pinned value (§14.3 mutation 3).

**Always denied, independent of policy** (`SC_REQUEST_FEATURE_DENIED`), because they make the provider fetch from arbitrary hosts (SPEC §88), bill outside token pricing (SPEC §78, §81) or bill context the body does not contain (review COST-4):

- non-inline content sources anywhere in `messages`/`input`/`system`: `source.type` ∈ {`url`, `file`}, any `file_id`, `image_url.url` / `input_image.image_url` not starting with `data:`, `input_file.file_url`, `input_file.file_id`;
- Anthropic `document` blocks other than `source.type: text` (inline PDFs are token-dense; deferred until a per-page estimate exists);
- provider-side conversation state: Responses `previous_response_id`, `conversation`, `prompt`; Responses `background: true`;
- Anthropic `mcp_servers` and `container`; Chat `web_search_options`; `n > 1`.

The allowlists ship with the values captured from the pinned harnesses (plan 31 §9.2) and are re-verified whenever a harness CLI version changes (53-AC-29, VS-10). The Gateway parses at most the top level, `tools`, and content-block `type`/`source` keys, within the bounded body read; it never interprets message text.

Upstream non-2xx responses are passed through **unchanged**: status, allowed headers and body.

### 7.4 Operational endpoints (container port named `metrics`, 9090 (CD-02); ClusterIP Service `model-gateway-metrics`; not routed by Ingress)

- `GET /healthz`: process liveness only.
- `GET /readyz` (served on this `metrics` port, 9090, which is the target for the blackbox probe of plan 38 and for plan 36's `monitoring` ingress rule; review GC-03): 200 iff all of the following hold: policy loaded and valid, JWKS loaded (≥1 key), DB ping < 1 s within the last 5 s, schema version OK, at least one provider enabled with a valid key. Otherwise 503 with JSON `{"ready":false,"reasons":[...]}`.
- `GET /metrics`: Prometheus.
- `GET /sc/v1/status`: JSON document validated by `contracts/gateway-status.schema.yaml` (review GC-19; consumed by the Runner's e-stop comparison and by procedures), containing:
  - policy revisions and digests (routing, pricing, budgets, harnesses);
  - `last_load_error`;
  - per-provider `{enabled, key_fingerprint (first 12 hex of sha256(key)), key_loaded_at}`;
  - e-stop `{effective_level, sources:{db, git, k8s:{level, last_read_ok_at}, degraded}}` (plan 33 §14.6);
  - open reservation count;
  - build version and image digest.
  - It contains no secrets.

## 8. Internal interfaces

Logical components inside the `model-gateway` binary (Go package boundaries are implementation detail):

| Component | Responsibility | Interfaces |
|---|---|---|
| `policy store` | Loads and validates routing, pricing, budgets and harnesses with the F04 policy-lib. Atomic swap. Keeps last-known-good. Polls the mount every 10 s (content digest). | read-only snapshot per request |
| `key store` | Reads `/var/run/secrets/.../model-api-keys/<secretKey>` every 30 s. Trims whitespace. An empty file or a `forbiddenKeyPrefixes` match marks the provider unavailable (L-4: an empty secret is worse than a missing one). | `Key(provider)` |
| `authn` | Parses and verifies the JWS (EdDSA, `kid` from JWKS, `typ`, `iss`, `aud`, `nbf`/`exp` ± 30 s skew). Validates claims against `gateway-token.schema.yaml`. Computes `claims_sha256` over the payload bytes. | `Principal` |
| `request inspector` | Bounded JSON parse of the body: top-level member names, `model`, `stream`, every output-cap alias of the family, `stream_options`, `n`, `tools[].type`, content-block `type`/`source`/`*_url`/`file_id` keys, `cache_control.ttl`, pinned members. Enforces the `requestPolicy` allowlist and the always-denied list (§7.3). Applies the permitted mutations (§14.3). | `Inspected` |
| `pricer` (plan 32) | Selects the effective entry at an instant; computes worst-case rates and actual cost in integer micro-USD. | `WorstCase()`, `Price()` |
| `reservation store` (plan 33) | One SQL transaction that checks registry, e-stop and scopes, then inserts the reservation. One settlement transaction. | `Reserve()`, `Settle()` |
| `forwarder` | Single upstream attempt, timeouts, header policy, immediate flushing for SSE. | |
| `usage tap` (plan 32 dialects) | Per-dialect incremental parser over the response bytes. Extracts usage, model, ids and terminal events. Never retains content beyond the current SSE event. | `Observed` |
| `e-stop watcher` (plan 33) | Polls the K8s ConfigMap every 5 s, reloads the Git source with policy, reads the DB source inside each reservation txn and polls it every 2 s for cutting in-flight streams (plan 33 §14.6). | effective level |
| `sweeper` / maintenance loop | Leader only (`pg_try_advisory_lock(0x5C6A7E01)`): every 60 s settles expired reservations; every 5 min refreshes rollups (plan 32). Exports shared-scope gauges every 30 s. Upserts `ledger.provider_status` every 30 s (plan 31 §14.2). Daily retention (plan 43 R-05): deletes settled or expired reservations older than 30 d, denials older than 90 d, and runs `ledger.prune_usage_raw()`. Exports `sc_ledger_reservations_oldest_closed_seconds`, `sc_ledger_denials_oldest_seconds`, `sc_ledger_usage_raw_oldest_seconds`. | |

**Runner ↔ Gateway contract** (plan 26 must honor):

1. At dispatch, *before* the Job is created, the Runner:
   - inserts `ledger.gateway_tokens(jti, token_kind, execution_id, task_id, claims_sha256, issued_at, expires_at, revoked_at NULL)`;
   - upserts the `task:<id>` and `execution:<id>` rows in `ledger.budget_scopes` with limits (plan 33 §7.2).
2. When an execution reaches any terminal state, the Runner sets `revoked_at = now()` on its token. Once the revocation commits, further requests with that token fail with `SC_TOKEN_REVOKED`.
3. The Runner never writes Gateway-owned columns.

## 9. Configuration

Policy-level knobs live in the policy files (plans 31, 32, 33). Runtime knobs are Helm values of the `strange-company` chart (plan 54), under `modelGateway.*`, source of truth `gitops/` values in Git. Changes are made by PR (operator).

| Key (`modelGateway.`) | Default | Notes |
|---|---|---|
| `replicas` | 2 | both profiles |
| `listen.api` / `listen.ops` | `:8080` / `:9090` | the ops port is declared as container port name `metrics` (CD-02) |
| `timeouts.connect` | 10s | TCP+TLS to upstream |
| `timeouts.responseHeader` | 900s | non-streaming generation can be long |
| `timeouts.streamIdle` | 300s | maximum gap between upstream bytes on SSE (providers send pings) |
| `timeouts.total` | 3600s | hard cap per request |
| `timeouts.clientWrite` | 60s | slow-client protection |
| `limits.maxInflightPerReplica` | 128 | beyond → 429 `SC_GATEWAY_OVERLOADED` (retryable, `retry-after: 2`) |
| `limits.maxNonStreamingParseBytes` | 16777216 | larger non-streamed responses are passed through, but usage is `unknown` (`response_too_large`) |
| `limits.maxSseEventBytes` | 1048576 | a larger single event is passed through without parsing |
| `reservation.expiryGrace` | 600s | `expires_at = started + timeouts.total + grace` |
| `reservation.contentionWaitSeconds` | 30 | bounded wait before `SC_BUDGET_CONTENDED` (plan 33 §14.2) |
| `captureFeatures` | `false` | test/R01 only: log one line per request with the request's top-level member names, tool types, beta items and content-source types (never values or text), for 53-AC-29. Refused at startup unless `fakeUpstream.enabled` and an explicit `captureFeaturesUntil` timestamp ≤ 24 h ahead are set |
| `estop.configMapName` / `estop.pollInterval` / `estop.staleAfter` | `sc-emergency-stop` / 5s / 60s | plan 33 |
| `db.dsnSecret` | `sc-pg-gateway` | CNPG managed-role credential (SC-PG-APP) |
| `db.maxConns` | 20 | per replica |
| `otel.endpoint` | `otel-collector.monitoring.svc:4317` | plan 40 |
| `logLevel` | `info` | `debug` adds header names and timing, never values or bodies |
| `fakeUpstream.enabled` | `true` in every installation | deploys `fake-model-upstream` (§22.3). It stays on after R02 (review RA-m12): it is reachable only through the test-only providers, which only test-only harnesses on `spec.testRepositories` can resolve (plan 31 §14.2 filter 2), and it is budgeted like any provider, so post-recovery smoke checks (`agent-runner-failure.md`) keep working without paid calls. |

There is deliberately **no** body-capture knob in MVP (§24).

## 10. Secrets

| Class | Use | Notes |
|---|---|---|
| `SC-MODEL-API-KEYS` | consumed (read-only Secret volume) | one key per provider `credential.secretKey`. Rotation: `procedures/rotate-model-credentials.md`. Hot-reloaded, no restart. |
| `SC-GATEWAY-TOKEN` | verified (never stored by the Gateway) | minted by the Runner. Execution tokens are delivered in the per-execution Secret (plan 27). The Hermes token is delivered by plan 24. |
| `SC-RUNNER-SIGNING` | **public half only** (JWKS ConfigMap `sc-runner-jwks`) | Decision (ADR-0451): no dedicated gateway signing key. The verifier holds only public keys, `aud`/`typ` separate token types, and DB registration means a leaked signing key alone cannot mint usable gateway tokens. Plan 35 must publish the JWKS ConfigMap and rotate with ≥ 24 h overlap of two `kid`s. |
| `SC-PG-APP` (sc-pg) | Gateway DB credential | **Request to plan 35:** a distinct CNPG managed role `sc_gateway` (Secret `sc-pg-gateway`), separate from the Runner's role, with the grants in §6. Also a read-only role `sc_reporting` (Secret `sc-pg-reporting`) for Grafana (plan 32/39). Both are sub-credentials of SC-PG-APP for `sc-pg`. |

No new secret class is introduced. Keys are never logged. The key fingerprint is a 12-hex prefix of SHA-256, which does not disclose the key.

## 11. Storage

No PVC. `emptyDir` is not needed (read-only root filesystem; `/tmp` as an `emptyDir` with `sizeLimit: 64Mi` for Go runtime use only). Ledger growth estimates and retention are in plan 32 §11.

## 12. Resource requirements

| Profile | Replicas | CPU req/limit | Memory req/limit | PriorityClass | Spread |
|---|---|---|---|---|---|
| self-hosting-minimum | 2 (same node) | 50m / 1 | 96Mi / 256Mi | platform tier (plan 51 name) | — |
| small-ha | 2 | 50m / 1 | 96Mi / 256Mi | same | `topologySpreadConstraints` `kubernetes.io/hostname`, `maxSkew: 1`, `whenUnsatisfiable: ScheduleAnyway` |

- PodDisruptionBudget: `minAvailable: 1`.
- Rolling update: `maxUnavailable: 0`, `maxSurge: 1`.
- `terminationGracePeriodSeconds: 3700`, so in-flight streams finish up to `timeouts.total` during a rollout.
- On SIGTERM: readiness goes false immediately, no new requests are accepted, and in-flight requests run to completion.
- Memory budget: ~30 MiB base + ≤ 16 MiB per concurrent non-streaming parse (bounded by `maxInflightPerReplica`, so the worst case is capped by the 256Mi limit through the inflight limiter: parse buffers come from a pool capped at 8 concurrent large parses; beyond that, usage is `unknown`).

## 13. Security boundary

- **Trust level:** platform, highest secret sensitivity in `strange-company` (holds every provider key). It is not reachable from the internet (no Ingress; ClusterIP only). Callers (workers, Hermes) are **untrusted-but-authenticated**.
- **Reachable from:** `agent-jobs` pods and `hermes` on 8080; `monitoring` and the Runner on the `metrics` port (9090); fake-harness test pods (`agent-jobs`). The NetworkPolicy is owned by plan 36. The Runner calls only `GET /sc/v1/status` (plan 33 §7.4); it never proxies model traffic.
- **Reaches:** provider HTTPS endpoints (443), `sc-pg` (5432), kube-apiserver (GET of one ConfigMap), DNS, `fake-model-upstream`.
- **RBAC:** ServiceAccount `model-gateway` with a Role in `strange-company` granting `get` on `configmaps` with `resourceNames: [sc-emergency-stop]`, and nothing else. `automountServiceAccountToken: true` for this pod only.
- **PSA restricted:** non-root uid 65532, `readOnlyRootFilesystem`, all capabilities dropped, RuntimeDefault seccomp, distroless image, digest-pinned and signed (plan 23).
- **Open-proxy prevention:** the upstream host comes only from policy. `Host` and absolute-URI requests are rejected. Redirects from upstream are **not followed**; they are returned as-is. TLS verification uses system roots and cannot be disabled for `stage: production` providers.
- **Content privacy:** bodies are never logged, persisted or attached to spans. Parsers hold at most one SSE event or the bounded non-streaming buffer, and zero buffers on release. Panic recovery logs no request data.
- **Threats and mitigations:**

| Threat | Mitigation |
|---|---|
| Stolen worker token | Short exp; revoked at execution end; model allowlist; budget scopes |
| Stolen Hermes token | DB revocation (§8); orchestration budget; rotate (procedure rotate-model-credentials §Hermes token) |
| Forged token | EdDSA plus DB registration digest |
| Token replay across executions | `jti` registry is bound to the execution |
| Prompt-injected harness or hostile repository code (which can read the worker's gateway token) tries provider-side web tools, MCP connectors or URL sources to exfiltrate or to spend outside token pricing | allowlist denies every non-listed feature and always denies URL/file sources, `mcp_servers` and conversation state (§7.3, 53-AC-28) |
| Header smuggling of org/project | stripped |
| Response-splitting via upstream | Go `net/http` header validation; allowlist |

## 14. Happy path

### 14.1 Request pipeline (all steps per inbound request; stage timings recorded in `sc_gateway_stage_seconds`)

1. **Accept.** Enforce `maxInflightPerReplica`, read the body up to `maxRequestBodyBytes` (413 beyond), assign `gw_<ulid>`, start span `sc.model.invocation`.
2. **Route.** Match `/p/{provider}/...` to (provider, family, endpoint). Reject unknown, unsupported or undeclared routes.
3. **Authenticate.** Verify the JWS and claims (§8 `authn`). Token registry membership is checked transactionally in step 7.
4. **Inspect.** Parse the body. Extract `model` (`SC_BODY_INVALID` if missing). Check `(provider, family, model) ∈ allowed_models` (`SC_MODEL_NOT_ALLOWED`). Enforce the family's request allowlist and the always-denied features (§7.3): unknown members, tool types, betas, non-inline sources and provider-side state are denied with zero upstream contact.
5. **Concurrency.** Acquire a per-`jti` slot (in-memory per replica, limit `max_concurrent_requests`). The cross-replica bound is enforced in step 7 by counting open reservations for the `jti`.
6. **Price and estimate** (plan 32 §10): find the effective priced entry at `now` for (provider, model, `service_tier` requested or `standard`), else `SC_PRICE_UNKNOWN`. Then compute:

   ```
   window    = model.contextWindowTokens                       # effective window; no allowed beta enlarges it (§7.2)
   est_input = min(ceil(len(body_bytes_after_mutation) / perRequest.inputBytesPerTokenEstimate), window)
   out_cap   = requested cap (after alias normalisation and injection, §14.3)
   reason    = model.outputCapCoversReasoning ? 0 : model.maxReasoningTokens
   rate_in   = max over candidate rate sets of max(input, cacheWrite)
               (cacheWrite1h instead of cacheWrite only if the bounded parse saw cache_control.ttl = "1h")
   rate_out  = max over candidate rate sets of max(output, reasoning or output)
   worst     = ceil(est_input × rate_in / 1e6) + ceil((out_cap + reason) × rate_out / 1e6)   (micro-USD)
   worst_tok = est_input + out_cap + reason
   ```

   The candidate rate sets are: the base rates, every time window, and **every** long-context tier whose `aboveInputTokens` is below `window`, **taken over every model in the token's `allowed_models`** (so a mismatch to the auxiliary or any other allowed model cannot exceed the reservation; review COST-4). Cache reads are priced at most at the input rate, so `max(input, cacheWrite)` is a true upper bound for every input category; the 2× 1-hour write rate applies only when the request asks for it (review COST-2). Using only the tier selected by `est_input` would under-reserve when the estimate falls just below a threshold that the actual input crosses (plan 33 §14.3). If `worst > perRequest.maxCostUsd` the request is denied with `SC_REQUEST_COST_CAP`.

   **Decision:** input is estimated from bytes, not from a provider count-tokens call. Count-tokens is not uniform across providers, adds a round trip and its own rate limit, and Anthropic's is approximate anyway. A divisor of 2.0 bytes/token over-estimates typical English/code (≈3–4 bytes/token), which is the safe direction. Because the estimate is capped at `window`, and the input the provider bills is bounded by the same window (provider-side state and non-inline sources are denied, §7.3), the input part of `worst` is a hard bound once the body is larger than `2 × window` bytes. The remaining overrun channel is token-dense content below that size; plan 33 §14.3 states the bound.
7. **Reserve.** One SQL transaction (plan 33 §14.2):
   - token registry check (exists, not revoked, digest equal);
   - DB e-stop level;
   - lock the scope rows in key order;
   - check headroom for cost, tokens and requests;
   - insert the reservation (`state=open`, `amount`, `tokens`, `scope_keys`, `expires_at`);
   - increment `reserved_*`.

   Combined with the in-memory Git/K8s e-stop sources, this yields `SC_EMERGENCY_STOP`, `SC_BUDGET_EXHAUSTED`, `SC_BUDGET_UNDEFINED` or `SC_TOKEN_REVOKED`. A shortfall caused only by open reservations is **contention**, not exhaustion: the request waits up to `reservation.contentionWaitSeconds` (30 s), re-trying when a settlement on one of its scopes commits (`LISTEN sc_budget_settled`), and only then answers `SC_BUDGET_CONTENDED` (429; plan 33 §14.2). On DB error the result is `SC_LEDGER_UNAVAILABLE` and nothing is forwarded.
8. **Forward.** Build the upstream request to `baseUrl + rest`. Apply the header policy. Inject the key. Make a single attempt.
9. **Stream through.** Copy upstream bytes to the client as they arrive, flushing per write for SSE (`FlushInterval: -1` semantics). Tee every chunk into the usage tap.
10. **Settle.** On upstream EOF, error or timeout, client disconnect, or e-stop cut: derive `usage_status` and usage (§14.4), price (plan 32 §10), compute `budget_charge`, then run the settlement transaction: reservation `open→settled`, scopes `reserved -= amount`, `spent += charge`, insert the cost record, and insert the pricing snapshot if absent. If the settlement transaction fails, retry with backoff (1, 2, 4 … 60 s) **until `expires_at − 60 s`**, so a short DB outage keeps the exact usage (review COST-21). On SIGTERM the pod blocks shutdown on outstanding settlements within `terminationGracePeriodSeconds`. If the process dies first, the sweeper settles at worst case (`termination: reservation_expired`, `usage_status: unknown`).
11. **Emit.** Metrics, span attributes (§21), and the structured log line `gateway_request` (no content).

### 14.2 Model-mismatch detection

The reported model comes from:

- Anthropic: `message.model` (JSON) or `message_start.message.model` (SSE);
- Chat Completions: `model` of the response or of any chunk;
- Responses: `response.model`.

It is compared with the requested model's `responseModelMatch` (exact list plus prefixes, plan 31). On mismatch:

- the response is still delivered (it is already streaming and the money is already spent);
- the row gets `model_mismatch=true`;
- pricing uses the **more expensive** of the requested and the reported model's priced entries (per component), if the reported model is a known routed model; otherwise the row is `unpriced` and the budget is charged the reservation (review COST-4);
- `sc_gateway_model_mismatch_total{provider,model}` is incremented, which fires alert **`SCModelMismatch`** (warning; plan 42).

The Runner surfaces the flag on the execution (plan 26). An absent reported model is not a mismatch; it is recorded as null.

### 14.3 Permitted body mutations (the only three)

1. **Output-cap injection.** The inspector reads **every** output-cap alias of the family (Chat: `max_tokens`, `max_completion_tokens`; Responses: `max_output_tokens`; Anthropic: `max_tokens`). Any alias > `perRequest.maxOutputTokens` (or > `model.maxOutputTokens`) is denied (`SC_MAX_TOKENS_EXCEEDS_POLICY`), not clamped. A value carried in a non-policy alias is moved to the policy `outputCapField` and the alias is removed, so the upstream receives exactly one cap (review COST-5). If no alias is present, inject `min(perRequest.maxOutputTokens, model.maxOutputTokens)` into `outputCapField`. Rationale: the reservation must be bounded by a value the provider enforces.
2. **Stream usage injection** (`openai-chat-completions` and `fake`, `injectStreamUsage: true`). If `stream: true` and `stream_options.include_usage` is not `true`, set it. Without it, OpenAI-compatible streams report no usage and the ledger goes blind (prior-implementation evidence). This adds one final chunk with empty `choices`, which the OpenAI SDKs handle (verify per harness in R01 smoke).

3. **Pinned parameters** (`requestPolicy.pinnedParameters`, review COST-4/COST-13). A pinned member that is absent is injected with its pinned value (e.g. Anthropic `service_tier: standard_only`, Responses `store: false` and `service_tier: default`; exact provider values verified, VS-11). A present member with another value is denied (feature class `pinned_parameter`). This keeps every request on a priced service tier and prevents provider-side storage that later requests could bill against.

Mutation rewrites only the touched members. Every other member is re-emitted byte-identical (raw JSON pass-through), preserving numeric precision and key order.

### 14.4 Usage status derivation

| Situation | usage_status | Charge |
|---|---|---|
| Terminal usage observed (Anthropic `message_delta` with usage + `message_stop`; Chat final usage chunk / non-stream `usage`; Responses `response.completed`/`incomplete` with usage) | `reported` | priced actual |
| Upstream non-2xx response before any body stream (4xx/5xx with error JSON) | `not_billable` | 0 |
| Connect/TLS/DNS failure before the request was sent | `not_billable` (`upstream_unreachable`) | 0 |
| Request fully sent, no response headers before `responseHeader` timeout | `unknown` (`upstream_timeout`) | reservation |
| Stream ended (error event, idle timeout, client disconnect, e-stop cut, total timeout) after **partial** usage (e.g. Anthropic `message_start` input usage) | `estimated`: reported components kept, missing output = `out_cap`, listed in `estimated_fields` | priced estimate (worst case for the missing parts) |
| No usage at all observed on a 2xx response (truncated, parse failure, oversized, e-stop cut before `message_start`) | `unknown` | reservation |
| **Default rule:** any other termination after request bytes were written with no usage observed (e.g. connection reset before response headers) | `unknown` | reservation |

For `unknown`, `usage` holds the reservation's estimate (every field in `estimated_fields`) and `cost` is that estimate priced at the reservation rates (cost-record description, review COST-12). For Anthropic, output components are taken **only** from `message_delta`; the `message_start` output count is never treated as a reported output component (review COST-11), so an abort after `message_start` charges `out_cap` for output.

**Client disconnect:** the Gateway cancels the upstream request immediately (closes the upstream connection). Providers may still bill generated tokens, which is why missing output is charged at worst case.

### 14.5 Hermes path

Hermes (plan 24) uses an `orchestrator` token: `sub: orchestrator:hermes`, `harness: hermes`, `role: orchestrator`, `phase: orchestration`, `budget_scopes: [orchestration]`, `max_concurrent_requests` from `ModelRouting.orchestrator`, and an allowlist from plan 31 §14.6. Its requests reserve against:

- `orchestration-day:<date>`;
- `orchestration-month:<YYYY-MM>`;
- the provider scopes;
- the global scopes.

Emergency stop level `all` denies it, while `workers` does not (plan 33).

Its allowlist holds the primary and, when `ModelRouting.orchestrator.auxiliaryTier` is set, one `purpose: auxiliary` model for Hermes' compression, summarization and watcher slots (plan 31 §14.6, review COST-6). The token lives ≤ 31 days (30-day rotation plus 24 h overlap; review SEC-13) and is revocable through the registry. Rotation is described in procedure `rotate-model-credentials.md` §B; delivery into the Hermes managed scope is owned by plan 24. Hermes rows have null task, execution and attempt, `worker_id: hermes` and project `_platform` (SPEC interpretation, plan 32 §2).

### 14.5a Verify-only tokens (bootstrap verify, smoke checks)

Minted by `POST /v1/admin/gateway-tokens:verify` (plan 26; review RA-8): `token_kind: execution`, `task_id: sc-0`, `execution_id: sc-0-x<n>`, `attempt: 1`, `role: tester`, `harness: fake-success`, `phase: specification`, `repository: strange-company/strange-company`, `project: _platform`, `budget_scopes: [execution:sc-0-x<n>]` (no `task:` scope, because no task row exists), `exp − iat ≤ 600`. `allowed_models` names the test-only `fake` model unless the request names an enabled production provider for a one-request smoke. The Gateway accepts the missing `task:` scope only for `task_id = sc-0` (schema invariant), reserves against the execution scope (limit `max_usd`, default $0.05) plus the provider and global scopes, and denies it at e-stop level ≥ `workers` like any execution token.

### 14.6 High availability and fail-safe

Both replicas are stateless. All coordination happens in `sc-pg` (row locks, advisory-lock leader).

- **Gateway fully down:** no autonomous model spend at all. This is fail-safe. Workers fail their model calls with connection errors, which the Runner classifies as `infrastructure` (plan 29), and Hermes reports errors to the human.
- **DB down:** the Gateway is not ready and returns 503 `SC_LEDGER_UNAVAILABLE` (also fail-safe).
- **SLO:** monthly availability of "≥1 ready replica" ≥ 99.5 %.
- **Latency overhead SLO:** p99 of `sc_gateway_overhead_seconds{stage="pre_upstream"}` ≤ 50 ms.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| All Gateway pods down | `up{job="model-gateway"}` = 0; `kube_deployment_status_replicas_available{deployment="model-gateway"}`=0 → **`SCModelGatewayDown`** (critical, 2m) | no model spend; workers fail `infrastructure` | Deployment restarts; Runner defers dispatch while Gateway unready (plan 26 reads `/readyz` via Service endpoints count) | `gateway-failure.md` |
| `sc-pg` unreachable | readiness reason `db`; **`SCGatewayNotReady`** (critical, 5m) | 503 for all requests | none; fail closed | `gateway-failure.md`, `cnpg-primary-loss.md` |
| Settlement write fails repeatedly | `sc_gateway_settlement_failures_total` increase → **`SCLedgerWriteFailing`** (critical) | rows delayed; reservations stay open | in-memory retry 10 min, then sweeper worst-case settlement | `gateway-failure.md` §ledger |
| Pod killed mid-request | open reservations past `expires_at` | caller sees disconnect; row settled at worst case later | sweeper (`reservation_expired`) | — |
| Invalid policy pushed | load error → `sc_gateway_policy_load_errors_total`; **`SCGatewayPolicyInvalid`** (critical) | last-known-good kept; if none, not ready | none | `pricing-update.md` / fix PR |
| Provider key missing, empty or OAuth-prefixed | `sc_gateway_provider_available{provider}`=0 → **`SCModelProviderUnavailable`** (warning) | requests to that provider 503 `SC_PROVIDER_UNAVAILABLE`; routing skips it (plan 31) | none | `rotate-model-credentials.md` |
| Provider rejects key (401/403 upstream) | `sc_gateway_upstream_responses_total{status_class="4xx",code="401"\|"403"}` → **`SCModelCredentialRejected`** (critical) | calls fail | none | `rotate-model-credentials.md` |
| Provider outage / 5xx / 529 / 429 storm | **`SCModelProviderErrorsHigh`** (>20 % non-2xx over 15 m, ≥ 20 requests) | executions fail `infrastructure`; optional same-tier fallback (plan 31) | none at Gateway (no retries) | `gateway-failure.md` §upstream |
| Usage parse failures (provider format change) | `sc_gateway_usage_status_total{usage_status="unknown"}` share > 5 % over 1 h → **`SCModelUsageUnknown`** (warning) | ledger over-charges at worst case; `cost_complete=false` | none | `gateway-failure.md` §usage; re-record fixtures |
| Reservation contention (headroom held by open reservations) | `sc_budget_denials_total{code="SC_BUDGET_CONTENDED"}` | caller retries after `retry-after`; no task state change | bounded wait, then 429 | `budget-emergency-stop.md` §E (only if persistent) |
| Request uses a feature outside the allowlist | `sc_gateway_denials_total{code="SC_REQUEST_FEATURE_DENIED"}` | that request fails; a harness upgrade that needs a new feature fails its R01/C08 capture check (53-AC-29) | none | update `requestPolicy` by PR after review of what the feature does |
| Reservation underestimate (actual > reserved) | `sc_gateway_reservation_overrun_micro_usd_total` increase → **`SCGatewayReservationOverrun`** (warning) | scope may exceed limit by the overrun | none; next requests denied | plan 33 §14.3 |
| Model mismatch | §14.2 → **`SCModelMismatch`** | possibly mispriced or unexpected model | row flagged | `gateway-failure.md` §mismatch |
| JWKS missing or rotated without overlap | `SC_TOKEN_INVALID` spike; readiness reason `jwks` | all tokens rejected | none | `gateway-failure.md` §tokens |
| Kubernetes API unreachable | e-stop K8s source stale > 60 s (`sc_gateway_estop_source_up{source="k8s"}` = 0) → **`SCEmergencyStopSourceUnavailable`** | effective level raised to `workers` (plan 33 §14.6) | automatic | `gateway-failure.md` §estop |
| Latency overhead regression | **`SCGatewayLatencyOverheadHigh`** (p99 pre-upstream > 50 ms for 15 m) | slower calls | none | `gateway-failure.md` |
| Slow or stuck client | `timeouts.clientWrite` | stream aborted; `client_disconnect` | upstream cancelled; estimated settlement | — |

## 16. Retry behavior

**The Gateway performs no upstream retries, including on connect errors.** Reasons:

1. Harness SDKs already retry 429/5xx and connection errors (Anthropic and OpenAI SDKs default to 2 retries). Gateway retries would multiply cost and latency (2×3 = up to 9 upstream attempts per logical call), and hide provider health from the harness and the Runner's failure classification.
2. A retried generation can be billed twice. The caller is the only party that knows whether a retry is worth its cost.
3. One upstream attempt per reservation keeps "one request = one ledger row" exact [SPEC §78].

The Gateway itself retries only internal, idempotent operations:

- the DB reservation transaction on serialization failure or deadlock (≤ 3 attempts, 10–50 ms jitter);
- settlement (§14.1 step 10);
- policy and key reloads.

## 17. Idempotency behavior

- Settlement is idempotent: `UPDATE ledger.reservations SET state='settled' ... WHERE id=$1 AND state='open'`. If 0 rows are updated, settlement is a no-op. `cost_records.reservation_id` is `UNIQUE`, so the sweeper and a late settlement cannot both write.
- `pricing_snapshots` is keyed by digest (`INSERT ... ON CONFLICT DO NOTHING`).
- Policy and key reloads are content-digest based, so a reload with unchanged content is a no-op.
- The Gateway does not deduplicate inbound requests (a provider request is not idempotent). Each inbound request is a distinct ledger row.

## 18. Bootstrap behavior

The Gateway is installed by Flux with the `strange-company` chart after control transfer (C01 prerequisites). It becomes ready only when all of the following hold:

1. the Runner has applied the migrations in-process (CD-04) and `runner.schema_migrations` reaches the Gateway's `requiredSchemaVersion`;
2. the policy ConfigMap is present and valid;
3. the `sc-runner-jwks` ConfigMap exists;
4. at least one provider key is non-empty (the `fake` provider's key counts: plan 35 seeds the `fake` key as a random value).

Provider keys come from `bootstrap.env` → SOPS Secret `model-api-keys` (plans 50, 35). Bootstrap verification (plan 50 `verify`) calls `GET /readyz` and performs one fake-provider request through the Gateway using a verify-only execution token minted by the Runner's admin API (plan 26). No real provider is called during bootstrap. Pricing tables for the default routing ship in Git (plan 32 §9.4).

## 19. Upgrade behavior

- Rolling update with `maxUnavailable: 0`. In-flight streams on old pods finish within the grace period. Old and new versions share the DB.
- Ledger schema migrations are forward-only and expand/contract, and they ship in the **Runner** (CD-04): the Runner release carrying an additive migration ships first, and the Gateway version that uses it ships after. The Gateway stays unready below its `requiredSchemaVersion`, and tolerates newer additive versions.
- Provider wire changes (new usage fields, new SSE event types) are handled by:
  1. recorded-fixture contract tests updated in the same PR;
  2. the opt-in real-provider smoke `make smoke-providers` (§22.4) run before bumping harness CLI versions (plan 28) or enabling a new provider.
- Version-sensitive behaviours this plan depends on:

| ID | Behaviour | Verified by |
|---|---|---|
| VS-1 | Anthropic SSE usage carried in `message_start` and `message_delta` | 53-AC-17 |
| VS-2 | OpenAI Chat `stream_options.include_usage` final chunk | 53-AC-17 |
| VS-3 | Responses `response.completed.response.usage` | 53-AC-17 |
| VS-4 | DeepSeek `prompt_cache_hit_tokens`/`prompt_cache_miss_tokens` | 53-AC-17 |
| VS-5 | Claude Code honours `ANTHROPIC_BASE_URL` with http and sends the API key as `x-api-key` | 53-AC-23 |
| VS-6 | Codex Responses base_url override | 53-AC-23 |
| VS-7 | the Anthropic OAuth token prefix `sk-ant-oat` | 53-AC-23 |
| VS-8 | per model: whether the output cap bounds reasoning/thinking tokens (`outputCapCoversReasoning`) | fixture + 53-AC-17 field `cap_covers_reasoning` |
| VS-9 | harnesses stop calling after a 403 budget/e-stop denial (no internal retry loop) | 53-AC-33 |
| VS-10 | the pinned harnesses' request members, tool types and betas lie inside the shipped allowlists | 53-AC-29 |
| VS-11 | provider values for pinned parameters (`standard_only`, `default`, `store: false`) and the reported service-tier strings mapped by plan 32 §8 | fixtures + 53-AC-17 |

## 20. Recovery behavior

- **Stateless pods:** recovery is a reschedule.
- **Ledger:** recovered with `sc-pg` PITR (plan 12, RPO ≤ 15 min). After a PITR, ledger rows newer than the recovery point are lost. Provider invoices (plan 32 §20) are the reconciliation source, and budgets for the affected period are corrected by an operator-recorded `ledger.cost_adjustments` row (plan 32 §20), which plan 33 §20 counts against the month scopes.
- **Open reservations restored by PITR** whose requests completed on the lost timeline are expired by the sweeper at worst case. That is safe (over-count).
- **Keys:** come from Git (SOPS) plus the recovery bundle. Provider keys are not black-start material beyond what `bootstrap.env` holds (plan 35).

## 21. Observability

**Metrics** (prefix `sc_gateway_` unless noted; label sets are bounded by policy: `provider` ≤ 8, `model` = routing model ids ≤ 32, `api_family` ≤ 5, `harness` ≤ 10, `role` ≤ 6, `phase` 7, `tier` 4. **No task, execution, repository or jti labels.**)

| Metric | Type | Labels |
|---|---|---|
| `sc_gateway_requests_total` | counter | provider, api_family, model, result (`forwarded`, `denied`) |
| `sc_gateway_denials_total` | counter | code, token_kind |
| `sc_gateway_upstream_responses_total` | counter | provider, api_family, status_class, code (status code only for 401/403/429/529, else `other`) |
| `sc_gateway_request_duration_seconds` | histogram | provider, api_family, streamed |
| `sc_gateway_ttfb_seconds` | histogram | provider, api_family |
| `sc_gateway_stage_seconds` | histogram | stage (`authn`, `inspect`, `reserve`, `pre_upstream`, `settle`) |
| `sc_gateway_overhead_seconds` | histogram | stage (`pre_upstream`) — alias used by SLO |
| `sc_gateway_inflight_requests` | gauge | provider |
| `sc_gateway_usage_status_total` | counter | provider, api_family, usage_status |
| `sc_gateway_model_mismatch_total` | counter | provider, model |
| `sc_gateway_settlement_failures_total` | counter | — |
| `sc_gateway_reservations_open` | gauge | — |
| `sc_gateway_reservations_expired_total` | counter | — |
| `sc_gateway_reservation_overrun_micro_usd_total` | counter | provider |
| `sc_gateway_provider_available` | gauge | provider |
| `sc_gateway_policy_load_errors_total` | counter | file |
| `sc_gateway_policy_info` | gauge (=1) | routing_rev, pricing_rev, budgets_rev (12-hex prefixes; one series at a time) |
| `sc_gateway_estop_source_up` | gauge (0/1) | source (`db`, `git`, `k8s`): 1 iff the source was read successfully within `estop.staleAfter` (plan 33 §21) |
| `sc_ledger_reservations_oldest_closed_seconds`, `sc_ledger_denials_oldest_seconds`, `sc_ledger_usage_raw_oldest_seconds` | gauge (leader) | — (plan 43 retention checks) |
| `sc_model_spend_micro_usd_total`, `sc_model_tokens_total` | counter | defined in plan 32 §21 |
| `sc_budget_*`, `sc_emergency_stop_*` | gauge | defined in plan 33 §21 |

**Logs:** one JSON line per request, `msg=gateway_request`. Fields: gateway_request_id, jti (first 8 chars), task_id, execution_id, provider, model_requested, model_reported, status, termination, usage_status, tokens, budget_charge_micro_usd, latency. No headers' values, bodies or keys.

**Traces** (span and attribute names from `contracts/telemetry.schema.yaml`, plan 40): span **`sc.model.invocation`** (kind SERVER) with child spans **`sc.gateway.reserve`**, **`sc.gateway.upstream`** (CLIENT) and **`sc.gateway.settle`**. Attributes:

- `gen_ai.operation.name`, `gen_ai.provider.name`, `gen_ai.request.model`, `gen_ai.response.model`;
- `gen_ai.usage.input_tokens`, `gen_ai.usage.output_tokens`, `sc.usage.cached_input_tokens`, `sc.usage.cache_write_tokens`, `sc.usage.reasoning_tokens`;
- `sc.task_id`, `sc.execution_id`, `sc.role`, `sc.harness`, `sc.phase`, `sc.token_kind`;
- `sc.gateway.outcome`, `sc.gateway.denial_code`, `sc.cost.total_micro_usd`, `sc.cost.status`, `sc.usage.status`, `sc.model.mismatch`, `sc.stream`, `sc.cost_record_id`.

Mapping to the telemetry enums: `sc.usage.status` carries the ledger `usage_status` unchanged (`reported`, `estimated`, `unknown`, `not_billable`). `pricing_status` `priced` → `priced` and `unpriced` → `unpriced`, and a row whose usage is `unknown` → `unknown_usage`.

**Parent context.** An incoming `traceparent` header is honoured. Otherwise the parent is the token claim `trace_parent` (W3C traceparent of the execution span, minted by the Runner, `gateway-token.schema.yaml`), so every invocation nests under its execution [SPEC §93]. Orchestrator tokens carry no `trace_parent`, and Hermes calls start a root span. There is no content, no prompt and no completion [SPEC §94].

**Alerts** (catalogue names, `contracts/alert-catalog.yaml`, CD-01): `SCModelGatewayDown`, `SCGatewayNotReady`, `SCLedgerWriteFailing`, `SCGatewayPolicyInvalid`, `SCModelProviderUnavailable`, `SCModelCredentialRejected`, `SCModelProviderErrorsHigh`, `SCModelUsageUnknown`, `SCGatewayReservationOverrun`, `SCModelMismatch`, `SCEmergencyStopSourceUnavailable`, `SCGatewayLatencyOverheadHigh`. Budget and e-stop alerts are in plan 33. Runbook: `procedures/gateway-failure.md` (credential alerts: `procedures/rotate-model-credentials.md`).

## 22. Testing strategy

Everything in CI runs without paid model calls [SPEC §104].

### 22.1 Unit (Go, `make test`)

- JWS verification matrix: alg confusion (`none`, HS256 with the public key), wrong `typ`/`aud`/`iss`, skew boundaries, unknown `kid`, claims-schema violations, digest mismatch.
- Header policy: table tests for strip, pass, deny and inject.
- Body mutation preserves unknown members byte-identical (golden files, including large numbers and unicode).
- Worst-case estimator and the pricer (plan 32 unit vectors).
- Usage-status derivation table (§14.4), one case per row.
- Error envelope rendering per family (golden JSON).

### 22.2 Contract tests against recorded provider fixtures

Fixtures live under `testdata/provider-fixtures/<provider>/<family>/<scenario>/` (final path per plan 01). Each fixture has `request.json`, `response.http` (raw status, headers, body or SSE) and `expected.yaml` (normalized usage, usage_status, model_reported, provider ids, termination). The required scenarios for each of `anthropic/anthropic-messages`, `openai/openai-responses`, `openai/openai-chat-completions`, `deepseek/openai-chat-completions` and `zai/openai-chat-completions` are:

- non-stream ok;
- stream ok;
- stream with cache read and write (Anthropic 5m and 1h);
- reasoning tokens;
- 400 error;
- 401;
- 429 with `retry-after`;
- 500;
- 529 / overloaded (Anthropic);
- mid-stream `error` event;
- stream truncated without a terminal event;
- missing usage;
- model alias in response (dated snapshot);
- mismatched model.

Fixtures are captured once by `make record-provider-fixtures` (opt-in, needs real keys, never in CI). Text content is replaced by fixed lorem strings, and the structure, usage and ids are preserved. A test asserts that no fixture contains strings matching common key patterns (`sk-`, `sk-ant-`).

### 22.3 Fake provider upstream (`fake-model-upstream`)

A Go binary and image in the same module, deployed:

- in k3d for CI;
- in-cluster (`strange-company` ns, Deployment `fake-model-upstream`, 1 replica, 10m/32Mi) while `modelGateway.fakeUpstream.enabled`.

It serves `/v1/messages`, `/v1/responses` and `/v1/chat/completions` by replaying fixtures. The scenario is selected by the request `model` (catalog: `fake-model` = chat ok with usage; `fake-anthropic-stream-cache`, `fake-chat-no-usage`, `fake-hang` (idle forever), `fake-slow` (2 s between chunks), `fake-500`, `fake-429`, `fake-mismatch`, `fake-midstream-error`, `fake-huge` (> 16 MiB), …), so no Gateway-stripped header is needed. It records the received request headers and bodies in memory for assertions at `GET /_fake/requests` (test use only). Fixture scenarios for the always-denied and non-allowlisted features (§7.3) exist only as **request** fixtures: the Gateway must deny them before the fake is contacted.

Additional behaviours (requested by plans 24 and 31):

- **Scripted tool calls.** When the last user message contains the marker `[fake:tool <name> <json-args>]`, the upstream answers with exactly one tool call to `<name>` with those arguments, in the wire format of the route: an OpenAI Chat Completions `tool_calls` entry (finish reason `tool_calls`) or an Anthropic Messages `tool_use` block (stop reason `tool_use`), streaming or not, with normal usage. Without a marker it answers text. Plan 24's 24-AC-4/24-AC-12 depend on it.
- **`fake-529` scenario:** every request answers HTTP 529 with an Anthropic-style `overloaded_error` body (or 503 on chat routes). Used by plan 31's fallback test (31-AC-8).
- **Scripted conversation `fake-hermes-watch`** (review RA-12), selected by request model. Deterministic rules over the whole conversation, not only the last user message: if no tool result is present yet → one tool call `agent_list` with `{"states":["RED_VERIFIED"]}`; if the latest tool result lists a task in state `RED_VERIFIED` → one tool call `agent_start` with `{"task_id":"<first such id>","role":"implementer","harness":"fake-success","profile":"implementation","model_tier":"bargain","idempotency_key":"watch-<task id>"}`; otherwise a text answer. Tool results are recognised in both wire formats (Chat `role: tool`, Anthropic `tool_result` blocks). Plan 24 points the watcher at this model in the C10 overlay.
- **Second provider prefix:** the same server also serves every route under `/b/v1/...`, so that the test-only provider `fake-b` (`upstream.baseUrl: …/b/v1`, `model-routing.schema.yaml` example `e2e-test`) is a distinct upstream. `/_fake/requests` records the prefix of each request.

### 22.4 Integration (`make e2e-k3d`, deterministic)

The real Gateway, PostgreSQL (CNPG or plain Postgres in k3d), fake upstream and a test token minter using a test signing key. This exercises every C05 acceptance criterion below, plus the concurrency test of plan 33.

### 22.5 Real-provider smoke (opt-in, `make smoke-providers`, never a CI gate)

For each enabled provider:

- one minimal request (max output 16 tokens), non-streaming and streaming;
- asserts usage normalization invariants and that `model_reported` matches;
- writes `evidence/provider-smoke/<date>-<provider>.json` (no content).

It is run before R01, monthly via `pricing-update.md`, and on any harness CLI bump.

## 23. Acceptance criteria

### GATE-C05

- **53-AC-1** `kubectl -n strange-company get deploy model-gateway -o jsonpath='{.status.readyReplicas}'` returns `2`. `GET /readyz` on each pod returns 200. `kubectl get pdb model-gateway -o jsonpath='{.spec.minAvailable}'` returns `1`.
- **53-AC-2** For each fixture family (anthropic-messages, openai-responses, openai-chat-completions, fake), a streaming and a non-streaming request through the Gateway to `fake-model-upstream` returns a byte-identical body (SSE event sequence equal, after removing the Gateway-injected final usage chunk for chat streams where the caller did not request usage) and yields exactly one `ledger.cost_records` row (`SELECT count(*) ... WHERE gateway_request_id=$id` = 1) whose normalized usage equals `expected.yaml`.
- **53-AC-3** Over the full fixture suite (≥ 60 requests), `SELECT count(*) FROM ledger.reservations` = `SELECT count(*) FROM ledger.cost_records` and `SELECT count(*) FROM ledger.reservations WHERE state='open'` = 0 after 10 s.
- **53-AC-4** `fake-model-upstream` `GET /_fake/requests` shows that no forwarded request carried the gateway token, `x-sc-*` headers, `cookie` or `openai-organization`, and that each carried the configured fake key in the configured auth header.
- **53-AC-5** A request with each denial condition in §7.3 (one test per code) returns the documented HTTP status, `x-sc-error-code`, and an envelope that the official Anthropic and OpenAI SDK test clients parse as an API error with the code in the message. The fake upstream receives zero requests for those tests.
- **53-AC-6** A request for a model with `status: unpriced`, or with no effective entry, is denied `SC_PRICE_UNKNOWN` before any upstream contact (fake upstream request count unchanged).
- **53-AC-7** Model mismatch: `fake-mismatch` produces a row with `model_mismatch=true`, `sc_gateway_model_mismatch_total` increases by 1, and the `SCModelMismatch` rule evaluates firing in `promtool test rules`.
- **53-AC-8** Streaming without usage: a chat stream to `fake-chat-no-usage` with `stream_options` absent reaches the upstream with `stream_options.include_usage=true` (asserted from `/_fake/requests`), and the row is `usage_status=reported`.
- **53-AC-9** Truncated stream: `fake-midstream-error` after `message_start` yields `usage_status=estimated` with `estimated_fields` containing `output_tokens`. A 2xx with no usage yields `unknown` with `budget_charge_micro_usd = reserved_micro_usd`.
- **53-AC-10** Client disconnect after the first chunk: the upstream connection is closed within 1 s (the fake upstream records cancellation), and exactly one row exists with `termination=client_disconnect`.
- **53-AC-11** Pod kill mid-stream (`kubectl delete pod --grace-period=0 --force` during `fake-hang`): within `timeouts.total + expiryGrace + 120 s` (test config: total=30 s, grace=10 s) the reservation is `expired` and one row exists with `termination=reservation_expired`, `usage_status=unknown`.
- **53-AC-12** No upstream retry: `fake-500` and `fake-429` each result in exactly one upstream request (`/_fake/requests` count = 1) and one `not_billable` row.
- **53-AC-13** Token revocation: after `UPDATE ledger.gateway_tokens SET revoked_at=now()` for a jti, the next request with it returns 401 `SC_TOKEN_REVOKED`. A token signed by a non-JWKS key returns `SC_TOKEN_INVALID`. A Runner task token (different `aud`) returns `SC_TOKEN_INVALID`.
- **53-AC-14** Privacy: run the fixture suite with a body containing the marker string `SC-CANARY-7f3a`, then grep the Gateway pod logs (`kubectl logs`), the OTel collector debug exporter output and `pg_dump --schema=ledger` for the canary. Expected: 0 matches.
- **53-AC-15** Overhead: the k3d benchmark (200 sequential non-streaming requests to `fake-model`, 0 ms upstream delay) reports p99 of `sc_gateway_overhead_seconds{stage="pre_upstream"}` ≤ 50 ms. A `fake-slow` stream delivers each chunk to the client within 5 ms of the upstream emitting it (timestamps in the fake upstream vs the test client).
- **53-AC-16** Provider-hosted tools: an Anthropic request with `tools:[{"type":"web_search_20250305",...}]` and a Responses request with `tools:[{"type":"web_search"}]` are denied `SC_REQUEST_FEATURE_DENIED` with no upstream contact.
- **53-AC-28** Allowlist (review COST-1), one request fixture each, every one denied with the listed code, `details.member` naming the feature, and zero requests at the fake upstream (`/_fake/requests` count unchanged): Responses `tools:[{"type":"web_search_preview_2025_03_11"}]`; Chat `tools:[{"type":"web_search"}]` and `tools:[{"type":"retrieval"}]`; Chat `web_search_options`; Anthropic `mcp_servers` with `anthropic-beta: mcp-client-2025-04-04` (feature class `beta_header` or `body_member`); Anthropic image block with `source.type: url`; Anthropic `document` with `source.type: url` and with `source.type: base64` PDF; Responses `input_file.file_url` and `input_image.image_url` = `https://…`; Responses `previous_response_id`; top-level unknown member `foo`; `anthropic-beta: context-1m-2025-08-07`; Responses `store: true` (feature class `pinned_parameter`). A body with an inline `data:` image is forwarded.
- **53-AC-30** Contention (review COST-3): with an execution scope whose limit admits two worst-case reservations only sequentially, two concurrent requests to `fake-slow` both end 2xx; the second waited ≤ `contentionWaitSeconds`; no `SC_BUDGET_EXHAUSTED` row exists and `last_denied_at` of the scope stays null. With the first request held open past the wait (`fake-hang`, test wait 2 s), the second returns 429 `SC_BUDGET_CONTENDED` with `retry-after: 5`.
- **53-AC-31** Estimator (review COST-2): `go test ./... -run TestWorstCaseEstimator` passes golden vectors, including: a body of 4 × `window` bytes reserves exactly `window` input tokens; a request without `cache_control.ttl:"1h"` is priced at `max(input, cacheWrite)`, with it at `cacheWrite1h`; a model with `outputCapCoversReasoning: false` adds `maxReasoningTokens`; a token whose `allowed_models` contains a pricier auxiliary uses the pricier rates. The request-size replay against the real harness is 53-AC-35 at GATE-R01, because its fixtures only exist after R01 captures them.
- **53-AC-32** Cap aliases (review COST-5): a Chat request with `max_tokens: 100, max_completion_tokens: 900000` is denied `SC_MAX_TOKENS_EXCEEDS_POLICY`; a Chat request to a family whose `outputCapField` is `max_completion_tokens` carrying only `max_tokens: 100` reaches the fake upstream with `max_completion_tokens: 100` and no `max_tokens` member.
- **53-AC-34** Settlement durability (review COST-21/22): with `sc-pg` unreachable for 3 min (k3d NetworkPolicy) during and after a completed request, the row written after recovery has `usage_status=reported` (not `reservation_expired`). A reservation older than 30 d removed by retention leaves its cost record intact (`SELECT count(*) FROM ledger.cost_records WHERE reservation_id=$r` = 1; there is no FK from `cost_records.reservation_id`).
- **53-AC-17** Real-provider re-verification evidence exists and validates for every provider enabled in the production `policy/model-routing.yaml` **whose key is present in the installation** (review COST-10: GATE-C05 needs no paid calls when no keys are configured; the check is mandatory for every enabled provider at GATE-R01). The file `evidence/provider-smoke/*-<provider>.json` is ≤ 31 days old, has `normalization_invariants_ok: true`, `model_match: true`, `cap_covers_reasoning` equal to each model's `outputCapCoversReasoning`, and `service_tier_mapped: true`. Checked by `make check-provider-evidence` (deterministic file check).
- **53-AC-18** Fail closed on DB loss: with `sc-pg` scaled to 0 in k3d, `/readyz` returns 503 with reason `db` within 10 s, a request returns 503 `SC_LEDGER_UNAVAILABLE`, and the fake upstream receives 0 requests.
- **53-AC-19** Key hygiene: setting the fake provider key file to the empty string makes `sc_gateway_provider_available{provider="fake"}` = 0 within 40 s and requests return `SC_PROVIDER_UNAVAILABLE`. A key starting `sk-ant-oat` for provider `anthropic` produces the same. `/sc/v1/status` shows a 12-hex fingerprint and never the key.
- **53-AC-20** Open-proxy prevention: requests with an absolute-URI target, a `Host` header naming another host, or a path outside §7.1 never cause an upstream connection to any host other than the configured `baseUrl` (fake upstream plus a canary listener receive 0 connections).
- **53-AC-21** NetworkPolicy (with plan 36, k3d): from a pod in `agent-jobs`, a TCP connect to `api.anthropic.com:443` (or a stand-in external IP in k3d) fails, and a connect to `model-gateway.strange-company:8080` succeeds. From `hermes`, the same results.
- **53-AC-22** `GET /p/fake/v1/models` returns only the token's `allowed_models` and makes no upstream call.
- **53-AC-25** Fake upstream script: a chat request whose last user message contains `[fake:tool agent_list {}]` returns one `tool_calls` entry named `agent_list` (chat route) or one `tool_use` block (messages route); `fake-529` returns 529 on the messages route; a request to `/p/fake-b/v1/chat/completions` is recorded under prefix `/b/v1`.
- **53-AC-26** Trace parenting: a request with no `traceparent` header and a token whose `trace_parent` is `00-<t>-<s>-01` produces an `sc.model.invocation` span (collector debug exporter in k3d) with trace id `<t>` and parent span id `<s>`, and children `sc.gateway.reserve`, `sc.gateway.upstream`, `sc.gateway.settle`; every span validates against `telemetry.schema.yaml` `SpanRecord`.
- **53-AC-27** Schema gate (CD-04): with `runner.schema_migrations` one version below `requiredSchemaVersion`, `/readyz` returns 503 with reason `schema` and the Gateway issues no DDL (`pg_stat_statements`/statement log shows no `CREATE`/`ALTER` from role `sc_gateway`).

### Proposed contributions to GATE-C08 (owner: plans 24/26) and GATE-R01 (owner: plan 28)

- **53-AC-23** (R01) With the pinned Claude Code (and Codex, when enabled) image, a real-harness smoke task completes with `ANTHROPIC_BASE_URL` pointing at the Gateway. Every request in `ledger.cost_records` for the execution has `usage_status=reported`, `model_mismatch=false`, and the harness-reported usage total (plan 28 cross-check) is within ±2 % of the ledger token totals.
- **53-AC-24** (C08) A Hermes chat turn produces ≥ 1 row with `token_kind=orchestrator`, `harness=hermes`. After the Hermes token's jti is revoked, Hermes' next model call fails, and Hermes surfaces the error.
- **53-AC-29** (C08 for Hermes, R01 for each real harness; VS-10) Request capture: with the Gateway in `captureFeatures` test mode (records member names, tool types, betas and source types only, never values), a full scripted Hermes session (including one context compression) and the R01 smoke task produce zero `SC_REQUEST_FEATURE_DENIED` and `SC_MODEL_NOT_ALLOWED` denials, and every captured feature lies inside the shipped `requestPolicy`.
- **53-AC-35** (R01; review COST-2) Replay: the request-byte sizes and concurrency recorded during the R01 Claude Code smoke (≥ 200 requests, captured by `captureFeatures` mode, sizes only) are replayed through the estimator with the **shipped** policy and price cards: zero `SC_REQUEST_COST_CAP`, and the peak sum of concurrent reservations of one execution ≤ `global.hourlyUsd`. The size file is committed as `testdata/estimator/claude-code-r01-sizes.yaml` and becomes a regression input for every later policy change (`make test-estimator-replay`).
- **53-AC-33** (R01, C08; VS-9) After a forced `SC_BUDGET_EXHAUSTED` (task scope set to its spent value), the harness (Claude Code at R01; Hermes at C08) makes ≤ 1 further request with the same token within 60 s (`ledger.denials` count for the jti ≤ 2).

## 24. Explicit non-goals

- Translation between provider APIs (ADR-0004).
- Response caching, semantic caching, load-balancing across keys, and key pools.
- Request or response body capture, even for debugging (MVP). A future debug capture requires an ADR, an execution-scoped time-limited flag (≤ 24 h) and a retention limit [SPEC §94].
- Google native API (post-MVP, §7.1), embeddings, batch, files, realtime/WebSocket, background Responses.
- Upstream retries and hedging.
- Public exposure, or human UI.
- Enforcing provider-side rate limits (passed through).

## 25. Open questions

1. **Internal TLS for the Gateway listener.** Gateway tokens and prompts cross the pod network in cleartext inside the node. Inter-node traffic is WireGuard-encrypted (ADR-0011). Blocking: no. Proposed: accept for MVP and revisit with plan 10 when a mesh or cert-manager-issued pod certs are introduced. Owner: plans 10/34.
2. **Google native API deferral vs ADR-0004 wording.** Blocking: no. Proposed: post-MVP; ADR-0004's route list is architectural. Owner: plan 53.
3. **DeepSeek/Z.ai `/v1` path compatibility and usage fields (VS-4, Z.ai `prompt_tokens_details.cached_tokens`)** are marked verify. Blocking: no for C05 (the fake upstream is used); yes before enabling each provider in production (53-AC-17 enforces this). Owner: plan 53 at implementation.
4. **Verify-only token.** Resolved (review RA-8): `POST /v1/admin/gateway-tokens:verify` exists in `runner-api.openapi.yaml`. Its tokens are `execution` tokens for the synthetic task `sc-0` whose shape `gateway-token.schema.yaml` now defines (budget scope `execution:sc-0-x<n>` only, lifetime ≤ 600 s; §14.5a).
5. **ADR-0004 wording.** Resolved: ADR-0004's COST-1 amendment describes the allowlisted, token-priced request surface; plan 53 uses its denial code `SC_REQUEST_FEATURE_DENIED`.
6. **Inline PDF documents are denied** until a per-page token estimate exists. Blocking: no. Harness capture (53-AC-29) shows whether the pinned harnesses send them. Owner: plan 53.
