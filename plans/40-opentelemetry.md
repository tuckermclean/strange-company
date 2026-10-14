# 40 — OpenTelemetry (collector, span hierarchy, context propagation, telemetry privacy)

**Status:** Stage-0 plan (draft)
**DAG nodes:** P09 (co-owner with 38, 39, 42: Collector and privacy pipeline live); contributes to C11 (span hierarchy from the real Runner, Gateway and fake workers), R01 (harness telemetry settings for the real harness, plan 28), H03 (trace export to Tempo, with plan 41)
**Related plans:** 26 (Runner spans, task/execution IDs, debug grants), 27 (`sc-worker` spans, task packet `traceparent`), 28 (harness environment), 29 (task record `trace_id`), 33, 35, 36 (NetworkPolicy), 38 (span-metric names, scrape), 41 (Tempo, log agent), 42 (alerts), 43 (retention), 53 (Gateway spans)
**Contracts:** owns `contracts/telemetry.schema.yaml`; consumes `worker-input.schema.yaml` (`traceparent`), `gateway-token.schema.yaml` (requested `trace_parent` claim, §7.4), `harness.schema.yaml` (`env`)
**ADRs:** ADR-0551 (MVP traces not stored), ADR-0552 (Collector as log agent, plan 41), ADR-0004, ADR-0550

## 1. Purpose

Give Strange Company distributed execution telemetry [SPEC §90, §93] without turning it into a content leak [SPEC §94]: one OpenTelemetry Collector that receives OTLP spans from the Agent Runner, the Model Gateway and `sc-worker`; a privacy pipeline that enforces an attribute allowlist and secret redaction; span metrics for Prometheus in MVP (spans themselves are not stored, ADR-0551); and trace export to Tempo at small-ha (H03, plan 41). This plan defines the span hierarchy task → execution → model invocation / tool action / test invocation, how trace context flows through the Runner, the task packet and the Gateway, which harness telemetry settings are allowed, and how verbose content capture works under an explicit, time-limited, retention-bounded debug grant.

## 2. Requirements derived from SPEC.md

1. Hermes, the Agent Runner and harnesses SHOULD emit OpenTelemetry-compatible telemetry [SPEC §93].
2. Hierarchy: task └ worker execution ├ model invocation ├ tool action ├ test invocation [SPEC §93].
3. OTel complements, never replaces, the exact accounting ledger [SPEC §93, §90].
4. General telemetry SHALL NOT capture by default: full prompts, full completions, arbitrary source code, environment variables, secret-bearing command output [SPEC §94].
5. Safe default telemetry: provider, model, harness, task, repository, role, tokens, latency, cost, tool name, status, validation result [SPEC §94].
6. Verbose content capture requires explicit debugging configuration **and** retention limits [SPEC §94].
7. The OpenTelemetry Collector is required at the Self-Hosting Threshold and in MVP [SPEC §20, §117, §126]; Tempo is a small-ha concern [SPEC §95, §127].
8. Prometheus is not a high-cardinality ledger [SPEC §90, §91]: span metrics never use task or execution IDs as dimensions.
9. Retention of model telemetry is intentional [SPEC §107].
- **40-R-1** The attribute allowlist in `telemetry.schema.yaml` is the single source. The Collector's `redaction` allowlist is generated from it (`make render-otel-allowlist`), and CI fails on drift.
- **40-R-2** Privacy is enforced twice: at the emitter (contract tests validate every emitted span against the schema) and at the Collector (allowlist, value patterns, length caps). Either layer alone must be sufficient for the canary tests (§22).
- **40-R-3** Telemetry failure never blocks work: exporters are asynchronous, bounded queues drop on overflow, and no request path waits on the Collector.
- **40-R-4** Worker-originated telemetry is untrusted: the Collector overwrites the task and execution identifiers of worker spans from the source pod's labels (anti-spoofing).
- **40-R-5** Captured debug content never passes through the Collector or Prometheus; it goes only to a retention-bounded S3 prefix (§9.5).

## 3. Inputs

- `contracts/telemetry.schema.yaml` (allowlist, forbidden patterns, secret value patterns, span names, debug grant format).
- `policy/telemetry-debug/*.yaml` (`TelemetryDebugGrant`, protected path, human-reviewed PR).
- Task packet `traceparent` (worker-input contract) and the requested gateway-token claim `trace_parent` (§7.4).
- Harness registry `env` (plan 28) for harness telemetry settings.
- Plan 38 metric naming and label vocabulary.

## 4. Outputs

- HelmRelease `otel-collector` (namespace `monitoring`, layer `monitoring`), Services `otel-collector:4317` (platform OTLP/gRPC) and `otel-collector-workers:4318` (worker OTLP/HTTP).
- Span metrics on `otel-collector:8889/metrics`: `sc_span_calls_total`, `sc_span_duration_seconds_bucket|sum|count`, `sc_span_redactions_total` (scraped by plan 38, `job=otel-collector`); Collector self-metrics on `:8888`.
- `gitops/platform/otel-collector/allowlist.yaml` (generated).
- Requirements on emitters (§7): span names, attributes, parenting, identifiers on records.
- At H03: OTLP export to Tempo (plan 41).

## 5. Dependencies

**Hard (DAG):** P09 (with P06/P07 upstream). The Collector is deployable at P09 before any Strange Company service exists; it then carries only test spans.
**Soft/runtime:** plan 38 (Prometheus scrape of 8888/8889), plan 36 (NetworkPolicies §13), plans 26/27/53 (emitters; C01, C02, C05), plan 28 (harness env, R01), plan 41 (Tempo endpoint at H03), plan 14/35 (`SC-OBJECT-STORAGE/telemetry-debug` instance, §10).

## 6. Owned state

None durable. The Collector holds in-memory batches and span-metric accumulators (register S-29, plan 11: ephemeral; spans in flight are lost on restart, and span-metric counters reset, which Prometheus `rate()` tolerates). Debug-capture objects in S3 are owned by their writer (Runner/Gateway) under the §9.5 retention rule; this plan owns the rule.

## 7. External interfaces

### 7.1 Collector endpoints

| Service | Port | Protocol | Receiver | Who may send (NetworkPolicy, plan 36) |
|---|---|---|---|---|
| `otel-collector.monitoring.svc` | 4317 | OTLP/gRPC | `otlp/platform` | pods in `strange-company` (`agent-runner`, `model-gateway`) |
| `otel-collector-workers.monitoring.svc` | 4318 → target 4319 | OTLP/HTTP (protobuf) | `otlp/workers` | pods in `agent-jobs` labeled `app.kubernetes.io/name=agent-worker` (exception to SPEC §88's default egress list, recorded here as explicit platform policy) |
| `otel-collector.monitoring.svc` | 8888 | HTTP | self-metrics | Prometheus |
| `otel-collector.monitoring.svc` | 8889 | HTTP | `prometheus` exporter | Prometheus |
| `otel-collector.monitoring.svc` | 13133 | HTTP | `health_check` extension | kubelet probes |

No TLS inside the cluster (the WireGuard-encrypted pod network covers inter-node traffic, ADR-0011); access control is NetworkPolicy plus the anti-spoofing rule. No authentication on OTLP (telemetry is non-authoritative; a forged span can only distort span metrics, and the worker path cannot impersonate another execution, 40-R-4).

### 7.2 Span hierarchy (normative; names and attributes per `telemetry.schema.yaml`)

```text
trace (one per task; trace_id stored on the task record)
sc.task                              Runner   INTERNAL  zero-duration anchor, created at task creation (sc.task.anchor=true)
├── sc.task.transition               Runner   INTERNAL  one per state transition (from, to, actor_kind, reason_code)
├── sc.execution                     Runner   INTERNAL  QUEUED → terminal; span_id allocated at dispatch (§7.3)
│   ├── sc.execution.dispatch        Runner   INTERNAL  Job/Secret/deploy-key creation
│   │   └── sc.routing.resolve       Runner   INTERNAL  model routing decision (plan 31: requested/effective tier, tier source, candidate index, fallback)
│   ├── sc.worker.run                sc-worker INTERNAL  pod start → exit (parent from packet traceparent)
│   │   ├── sc.worker.clone
│   │   ├── sc.worker.checkout
│   │   ├── sc.harness.session       sc-worker INTERNAL  harness process lifetime
│   │   │   ├── sc.tool.action       sc-worker INTERNAL  one per harness tool call (name + status only)
│   │   │   └── sc.test.invocation   sc-worker INTERNAL  one per local validation command (command_id, result, counts)
│   │   ├── sc.worker.push
│   │   └── sc.worker.report
│   ├── sc.model.invocation          Gateway  SERVER    one per provider request (parent from token claim, §7.4)
│   │   ├── sc.gateway.reserve
│   │   ├── sc.gateway.upstream      Gateway  CLIENT
│   │   └── sc.gateway.settle
│   └── sc.execution.ingest          Runner   INTERNAL  result ingestion and verdict
├── sc.verification                  Runner   INTERNAL  RED/GREEN trigger → evidence ingested (pipeline number, classification)
└── sc.task.completed                Runner   INTERNAL  terminal state reached
separate traces, linked to sc.task by span link when the task is known:
sc.runner.api / sc.runner.mcp        Runner   SERVER    internal API and Hermes MCP calls
sc.estop.apply                       Runner/Gateway INTERNAL  an emergency-stop level change applied from any source (api, db, git, k8s, degraded; plan 33), with cancelled-execution count
sc.model.invocation (token_kind=orchestrator, phase=orchestration)  Gateway  SERVER  Hermes model calls (own trace per request)
```

Mapping to SPEC §93: *task* = `sc.task`; *worker execution* = `sc.execution` (+ `sc.worker.run`); *model invocation* = `sc.model.invocation`; *tool action* = `sc.tool.action`; *test invocation* = `sc.test.invocation` (local) and `sc.verification` (trusted CI). A task's whole history is one trace even though it spans days: every span is created with the stored `trace_id` and an explicit parent, so emitters never hold long-lived spans in memory.

Plan 53 named its spans `gateway.request`, `gateway.reserve`, `gateway.upstream`, `gateway.settle` and plan 26 named `task`, `execution`, `dispatch`, `ingest`, `verify`. The contract names above are authoritative (the schema enum); plans 26 and 53 adopt them at consolidation (§25 Q-40-1). The Gateway's money attribute is `sc.cost.total_micro_usd` (integer, ADR-0450); `gen_ai.system` is replaced by `gen_ai.provider.name`, and `sc.denial_code` by `sc.gateway.denial_code`.

### 7.3 Context propagation

| Hop | Mechanism | Trust |
|---|---|---|
| Task creation → all task spans | Runner generates a random 16-byte `trace_id` and 8-byte anchor `span_id`, persists both on the task row (`tasks.trace_id`, `tasks.anchor_span_id`; plan 29 schema, ADR-0551), emits `sc.task` immediately with start = end = creation time | trusted |
| Later Runner spans | rebuilt as remote-parent `SpanContext{trace_id, anchor_span_id}` from the row | trusted |
| Execution span | `span_id` allocated at QUEUED and persisted on the execution row (`executions.span_id`); the span is emitted when the execution reaches a terminal state, with start = QUEUED timestamp (Go SDK custom `IDGenerator` returning the persisted ID; verify API) | trusted |
| Runner → worker | task packet `traceparent` = `00-<trace_id>-<execution span_id>-01` (worker-input contract); `sc-worker` parents `sc.worker.run` on it | trusted input, untrusted emitter |
| Runner → Gateway | the gateway token (minted per execution) carries `trace_parent` = the same traceparent (**request to plan 53 / `gateway-token.schema.yaml`**). The Gateway parents `sc.model.invocation` on the claim. An incoming `traceparent` header from the harness is recorded only as a span **link**, never as the parent, because the harness is untrusted. The header itself passes through to the provider unchanged (plan 53 §9). | trusted (signed claim) |
| Hermes → Runner (MCP) | `sc.runner.mcp` SERVER span per call; if the call names a task, a span link to that task's anchor | trusted |
| Runner → Woodpecker | not propagated (Woodpecker has no OTel support assumed); the Runner's `sc.verification` span covers trigger → evidence | — |
| Logs | Runner/Gateway JSON log lines carry `trace_id` and `span_id` (telemetry.schema `LogRecord`), so logs and traces join at H03 | — |

`sc-worker` exporter settings (fixed by the Runner's Job builder, plan 26/27): `OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector-workers.monitoring.svc:4318`, `OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf`, `OTEL_TRACES_EXPORTER=otlp`, `OTEL_METRICS_EXPORTER=none`, `OTEL_LOGS_EXPORTER=none`, `OTEL_BSP_MAX_QUEUE_SIZE=2048`, `OTEL_BSP_EXPORT_TIMEOUT=5000`, and a final flush of at most 5 s before exit (never delaying the result post).

### 7.4 Emitter requirements

| Emitter | Must emit | Must never emit |
|---|---|---|
| Runner (26) | `sc.task`, `sc.task.transition`, `sc.task.completed`, `sc.execution` (+ `dispatch`, `ingest`), `sc.verification`, `sc.runner.api`, `sc.runner.mcp`; resource `service.name=agent-runner` | MCP arguments, task objective text, handoff text, evidence bodies, log lines |
| Gateway (53) | `sc.model.invocation` (SERVER) with `gen_ai.operation.name`, `gen_ai.provider.name`, `gen_ai.request.model`, `gen_ai.response.model`, usage tokens, `sc.cost.total_micro_usd`, `sc.cost.status`, `sc.usage.status`, `sc.gateway.outcome`, `sc.token_kind`, `sc.phase`, `sc.task_id`/`sc.execution_id`/`sc.role`/`sc.harness` when the token is an execution token; children `sc.gateway.reserve|upstream|settle` | request or response bodies, headers, tool definitions, system prompts, the token itself |
| `sc-worker` (27) | `sc.worker.*`, `sc.harness.session`, `sc.tool.action` (from the harness's structured stream: tool name, ok/error/denied, timing), `sc.test.invocation` (command ID from `validation.yaml`, exit code, pass/fail/skip counts) | tool inputs or outputs, command lines, file paths beyond the repository-relative path of the validation config, stdout/stderr |
| Hermes (24) | nothing in MVP (upstream Hermes Agent OTel support not assumed; verify at C08). Hermes activity is observed through `sc.runner.mcp` and orchestrator `sc.model.invocation` spans | — |
| Harness native telemetry | disabled in MVP (§9.4) | — |
| `sc-verify`, `sc-bootstrap` | nothing in MVP (`service.name` values are reserved in the schema for later use) | — |

SPEC §93's "Hermes SHOULD emit OTel" is met by the Runner and Gateway spans that represent every Hermes action; recorded as an interpretation in §25 Q-40-3.

## 8. Internal interfaces — Collector configuration (normative sketch)

Distribution: **OpenTelemetry Collector contrib** (ADR-0552), image `otel/opentelemetry-collector-contrib` digest-pinned; Helm chart `open-telemetry/opentelemetry-collector` (`mode: deployment`). Selection rule: latest contrib release supported by the chart's latest minor at implementation time; components used must not be marked "development" stability (re-verified at each bump, 40-AC-9).

```yaml
extensions:
  health_check: {endpoint: 0.0.0.0:13133}
  k8s_observer: {}                       # not used; k8sattributes uses its own watch
receivers:
  otlp/platform: {protocols: {grpc: {endpoint: 0.0.0.0:4317, max_recv_msg_size_mib: 4}}}
  otlp/workers:  {protocols: {http: {endpoint: 0.0.0.0:4319, max_request_body_size: 1048576}}}
processors:
  memory_limiter: {check_interval: 1s, limit_mib: 200, spike_limit_mib: 50}
  k8sattributes/workers:                 # source-pod identity for worker spans (40-R-4)
    auth_type: serviceAccount
    passthrough: false
    filter: {namespace: agent-jobs}
    pod_association: [{sources: [{from: connection}]}]
    extract:
      labels:
        - {tag_name: sc.execution_id.trusted, key: strange.company/execution-id, from: pod}
        - {tag_name: sc.task_id.trusted, key: strange.company/task-id, from: pod}
  transform/workers_identity:
    trace_statements:
      - context: span
        statements:
          - set(attributes["sc.execution_id"], resource.attributes["sc.execution_id.trusted"])
          - set(attributes["sc.task_id"], resource.attributes["sc.task_id.trusted"])
          - delete_key(resource.attributes, "sc.execution_id.trusted")
          - delete_key(resource.attributes, "sc.task_id.trusted")
  filter/workers_unattributed:           # drop worker spans whose source pod is unknown
    error_mode: ignore
    traces: {span: ['attributes["sc.execution_id"] == nil']}
  resource/common:
    attributes:
      - {key: sc.installation, value: "${env:SC_INSTALLATION}", action: upsert}
  redaction:                             # allowlist generated from telemetry.schema.yaml (40-R-1)
    allow_all_keys: false
    allowed_keys: ${file:/etc/otelcol/allowlist/span-keys.yaml}       # (verify file-provider include syntax)
    blocked_values: ${file:/etc/otelcol/allowlist/secret-value-patterns.yaml}
    summary: info                        # adds redaction.* counters to the span (verify attribute names)
  transform/bounds:
    trace_statements:
      - context: span
        statements:
          - truncate_all(attributes, 256)
          - truncate_all(resource.attributes, 256)
          - set(status.message, "redacted") where status.message != nil and not IsMatch(status.message, "^[a-z0-9_.:-]{1,64}$")
      - context: spanevent
        statements:
          - truncate_all(attributes, 256)
  filter/span_names:                     # spans not in the contract's name enum are dropped
    error_mode: ignore
    traces: {span: ['not IsMatch(name, "^sc\\.(task|task\\.transition|task\\.completed|execution|execution\\.dispatch|execution\\.ingest|verification|worker\\.(run|clone|checkout|push|report)|harness\\.session|tool\\.action|test\\.invocation|model\\.invocation|gateway\\.(reserve|upstream|settle)|runner\\.(api|mcp)|routing\\.resolve|estop\\.apply)$")']}
  filter/events:
    error_mode: ignore
    traces: {spanevent: ['not IsMatch(name, "^(sc\\.retry|sc\\.deadline_exceeded|sc\\.cancel_requested|sc\\.policy_violation|exception)$")']}
  transform/metrics_resource:            # keep span-metric cardinality bounded
    metric_statements:
      - context: resource
        statements: ['keep_keys(attributes, ["service.name"])']
  batch: {send_batch_size: 512, send_batch_max_size: 1024, timeout: 5s}
connectors:
  spanmetrics:
    namespace: sc_span
    histogram: {unit: s, explicit: {buckets: [0.005, 0.025, 0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 900, 1800, 3600]}}
    dimensions:                          # no task/execution/commit/request IDs, ever
      - {name: sc.role}
      - {name: sc.harness}
      - {name: sc.phase}
      - {name: sc.gateway.outcome}
      - {name: sc.tool.status}
    exclude_dimensions: [k8s.pod.name, k8s.job.name, service.instance.id]
    resource_metrics_key_attributes: [service.name]
    dimensions_cache_size: 2000
    aggregation_temporality: AGGREGATION_TEMPORALITY_CUMULATIVE
    metrics_flush_interval: 15s
  count/redactions:
    spans:
      sc_span_redactions:
        description: Spans on which the redaction processor removed keys or masked values.
        conditions: ['attributes["redaction.redacted.count"] > 0 or attributes["redaction.masked.count"] > 0']
exporters:
  prometheus: {endpoint: 0.0.0.0:8889, resource_to_telemetry_conversion: {enabled: false}, enable_open_metrics: false}
  debug: {verbosity: basic}              # present, wired only into the disabled `traces/debug` pipeline
  # H03 (small-ha overlay): otlp/tempo: {endpoint: tempo-distributor.monitoring.svc:4317, tls: {insecure: true}, sending_queue: {queue_size: 2000}, retry_on_failure: {max_elapsed_time: 300s}}
service:
  extensions: [health_check]
  telemetry: {metrics: {level: normal, readers: [{pull: {exporter: {prometheus: {host: 0.0.0.0, port: 8888}}}}]}}
  pipelines:
    traces/platform:
      receivers: [otlp/platform]
      processors: [memory_limiter, resource/common, filter/span_names, filter/events, redaction, transform/bounds, batch]
      exporters: [spanmetrics, count/redactions]           # + otlp/tempo at H03
    traces/workers:
      receivers: [otlp/workers]
      processors: [memory_limiter, k8sattributes/workers, transform/workers_identity, filter/workers_unattributed, resource/common, filter/span_names, filter/events, redaction, transform/bounds, batch]
      exporters: [spanmetrics, count/redactions]           # + otlp/tempo at H03
    metrics/spanmetrics:
      receivers: [spanmetrics, count/redactions]
      processors: [memory_limiter, transform/metrics_resource, batch]
      exporters: [prometheus]
```

Notes (all version-sensitive items are re-verified by 40-AC-9 on the pinned contrib version):
- Order matters: `redaction` runs **after** the span-name filter and **before** export, on both pipelines; the `count/redactions` connector counts on the redacted span.
- The Prometheus exporter produces `sc_span_calls_total{service_name, span_name, span_kind, status_code, sc_role, sc_harness, sc_phase, sc_gateway_outcome, sc_tool_status}` and `sc_span_duration_seconds_*` (verify exact suffixes), and `sc_span_redactions_total{service_name}`. Plan 38's label vocabulary admits exactly these labels for `sc_span_*` metrics.
- Cardinality bound: 22 span names × 5 roles × ~4 harnesses × ≤ 9 phases × ≤ 8 outcomes is an upper bound far above reality, because each span name populates only its own attributes. Expected < 600 series; `dimensions_cache_size: 2000` caps it.
- `k8sattributes` needs `get/list/watch` on `pods` (and `replicasets` is not needed) in `agent-jobs` only: a namespaced Role bound to the Collector's ServiceAccount.

## 9. Configuration

### 9.1 Configuration sources

| Key (Git path) | Default | Changed by |
|---|---|---|
| `gitops/platform/otel-collector/base/values.yaml` (chart values + the §8 config) | §8 | PR |
| `gitops/platform/otel-collector/allowlist.yaml` (generated from `telemetry.schema.yaml`) | — | `make render-otel-allowlist` in the same PR as a schema change |
| `gitops/platform/otel-collector/overlays/small-ha/` | adds `otlp/tempo` exporter, `replicas: 2` | PR (H03) |
| Emitter settings (`telemetry.otlpEndpoint`, sampler) | `otel-collector.monitoring.svc:4317`, `parentbased_always_on` | plans 26, 53 chart values |
| `policy/telemetry-debug/*.yaml` | none | human PR (protected path) |

### 9.2 Sampling

MVP: 100 % (spans feed only span metrics; nothing is stored). H03: 100 % to Tempo. Volume estimate: ≈ 20 executions/day × (≈ 200 model invocations + ≈ 300 tool actions + 30 other spans) ≈ 10–12 k spans/day ≈ 15 MB/day in Tempo. Decision rule: if Tempo ingestion exceeds 1 GiB/month, add `probabilistic_sampler` at 25 % for `sc.tool.action` only (model invocations, executions and tasks are always kept).

### 9.3 Span-metric names

`sc_span_calls_total`, `sc_span_duration_seconds` (histogram), `sc_span_redactions_total`. These names are pinned by 40-AC-3; if the pinned connector produces different suffixes, the Collector config adds a `metricstransform` rename rather than changing plan 38/39/42 references.

### 9.4 Harness telemetry settings (plan 28 registry `env`; enforced by the Runner Job builder, plan 26/27)

| Harness | MVP environment (fixed) | Forbidden (Job builder rejects; registry lint fails) |
|---|---|---|
| Claude Code | `CLAUDE_CODE_ENABLE_TELEMETRY=0` (native OTel off), `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` (no Statsig/Sentry/auto-update traffic; it would be blocked by NetworkPolicy anyway), `DISABLE_TELEMETRY=1`, `DISABLE_ERROR_REPORTING=1` (verify each name on the pinned version, 40-AC-10) | `OTEL_LOG_USER_PROMPTS` (any value), `OTEL_LOG_TOOL_DETAILS` (any value), `OTEL_EXPORTER_OTLP_ENDPOINT` pointing anywhere but `otel-collector-workers` |
| Codex | `[otel]` disabled in the generated `config.toml` (`exporter = "none"`), `log_user_prompt = false` (verify keys) | any `[otel]` exporter other than `none` |
| OpenCode / others | no native telemetry configured | `OTEL_*` variables except those the Job builder sets for `sc-worker` |
| `fake` | n/a | n/a |

Why native harness telemetry is off in MVP: tool and test spans come from `sc-worker` (structured stream parsing, no content), model spans and exact usage come from the Gateway (ADR-0004), and native exports carry identifiers (session IDs, account UUIDs) and event payloads that would need their own redaction review. **Opt-in path (post-MVP):** a harness registry flag `telemetry.native: logs` may enable Claude Code with `CLAUDE_CODE_ENABLE_TELEMETRY=1`, `OTEL_LOGS_EXPORTER=otlp`, `OTEL_METRICS_EXPORTER=none`, endpoint `otel-collector-workers`, and **without** the two forbidden variables, which keeps prompt text and tool parameters out of exported events by the harness's documented default (verify). That requires a new receiver pipeline for logs with its own allowlist and a security review (plan 34).

### 9.5 Verbose content capture (debug grants)

- **Grant:** a `TelemetryDebugGrant` file in `policy/telemetry-debug/` (contract `telemetry.schema.yaml`), merged by a human (protected path, ADR-0008). Loader invariants (Runner and Gateway, plan 26/53 policy loaders): `expiresAt − createdAt ≤ 24 h`; `retentionDays ≤ 7`; at most 3 active grants; scope `task`, `execution` or `service`; expired grants ignored. Active grants are exported as `sc_telemetry_debug_grants_active` and raise `SCTelemetryDebugCaptureActive` (plan 42).
- **What is captured, by whom:** the Gateway captures `prompts`/`completions` (request and response bodies, after removing `authorization`, `x-api-key` and cookie headers) for tokens in scope; the Runner captures `tool_io`/`command_output` from the worker's stream log (already scrubbed by plan 27's secret scrubber) for executions in scope; `exception_messages` lets services include exception messages in the captured record.
- **Where:** never in spans, logs or metrics. Objects go to `s3://<prefix>-evidence/telemetry-debug/<grant-name>/<execution-id|service>/<UTC-ts>-<request-id>.json.gz`, written with the Runner's evidence credential and, for the Gateway, a new write-only credential instance `SC-OBJECT-STORAGE/telemetry-debug` (prefix-scoped where the provider supports it; §10).
- **Retention:** bucket lifecycle rule on prefix `telemetry-debug/`: expire current versions after 7 days and noncurrent versions after 1 day (plan 14/43 apply it). A daily Runner sweeper deletes objects older than their grant's `retentionDays`. Object Lock, if enabled on `evidence` (plan 14), must exclude this prefix (otherwise it is a different bucket, `…-telemetry-debug`; plan 14 decides).
- **Access:** reading captured content requires the S3 credential (operator through the toolbox). It is not exposed in Grafana, Hermes or the Runner API.

## 10. Secrets

| Class | Use | Notes |
|---|---|---|
| none for OTLP | — | OTLP is unauthenticated in-cluster (§7.1) |
| **`SC-OBJECT-STORAGE/telemetry-debug`** (**new instance**, for plans 14 and 35 to register) | Gateway writes debug captures | write-only to `<prefix>-evidence/telemetry-debug/` (or bucket `<prefix>-telemetry-debug`); SOPS in `strange-company`; not in the recovery bundle; rotation with the other storage instances (`rotate-storage-credentials.md`); mounted only when at least one grant targets the Gateway (the Gateway reads it lazily) |

## 11. Storage

None in MVP (ADR-0551). Collector queues are in memory. At H03 the `otlp/tempo` exporter uses an in-memory sending queue (2000 batches); a persistent queue is not used (telemetry is best effort, SPEC §108).

## 12. Resource requirements

| Workload | Requests (cpu / mem) | Limit (mem) | Replicas (min / small-ha) |
|---|---|---|---|
| otel-collector | 50m / 128 MiB | 256 MiB (`memory_limiter` 200 MiB) | 1 / 2 (podAntiAffinity preferred) |

PriorityClass `platform`. At small-ha two replicas each export their own span-metric counters; dashboards and alerts use `sum by (…)`, so totals stay correct. Constrained 8 GiB nodes: request 96 MiB, limit 192 MiB, `limit_mib: 150`.

## 13. Security boundary

- Namespace `monitoring` (PSA baseline; the Collector pod is restricted-compatible: non-root 10001, read-only root FS, no capabilities).
- RBAC: ServiceAccount `otel-collector` with a Role in `agent-jobs`: `get, list, watch` on `pods` (for `k8sattributes`); nothing else.
- NetworkPolicy (plan 36): ingress 4317 from `strange-company` pods `app.kubernetes.io/name in (agent-runner, model-gateway)`; ingress 4319 from `agent-jobs` worker pods; ingress 8888/8889/13133 from Prometheus and kubelet; egress to the API server (k8sattributes), DNS, and at H03 Tempo. **No** internet egress.
- The worker exception: `agent-jobs` → `otel-collector-workers:4318` is the only monitoring endpoint workers can reach. It accepts OTLP/HTTP only, bounded to 1 MiB per request and by `memory_limiter`. Spoofed identifiers are overwritten (40-R-4); spans from unknown pods are dropped.
- Privacy layers: (1) emitters only set allowlisted keys (contract tests); (2) span-name and event-name filters; (3) `redaction` with `allow_all_keys: false`, the generated allowlist and the secret value patterns; (4) length caps and status-message coercion; (5) no content in metrics (dimensions are enums). Forbidden content (SPEC §94): prompts, completions, source code, environment variables, command lines and command output, headers, URLs with queries, SQL text, exception messages and stack traces, user e-mails and account IDs (schema `x-forbidden-attribute-patterns`).

## 14. Happy path

1. P09: Flux deploys the Collector; health check passes; Prometheus scrapes 8888/8889 (`up{job="otel-collector"} == 1`).
2. C01/C05: the Runner and Gateway start exporting. A Hermes `agent_start` creates task `sc-N` with a `trace_id`, emits `sc.task`, and dispatches an execution with a traceparent in the packet and in the gateway token.
3. The worker emits its spans to `otel-collector-workers`; the Gateway emits `sc.model.invocation` under the execution; the Runner emits `sc.execution` at the terminal state and `sc.verification` when RED/GREEN evidence arrives.
4. The Collector redacts, bounds and converts spans into `sc_span_*` metrics. Nothing is stored (MVP). Grafana's `sc-workers` dashboard shows span RED metrics.
5. H03: the same spans are also exported to Tempo, and Grafana links task rows to traces by `trace_id`.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Collector down | `SCOTelCollectorDown` | spans lost; span metrics stale; **no effect on work** (40-R-3) | kubelet restart | `monitoring-failure.md` §OTel |
| Collector memory pressure | `SCOTelCollectorRefusingData` (receiver refused spans); `otelcol_processor_refused_spans_total` | spans dropped | `memory_limiter` refuses; emitters drop on full queue | `monitoring-failure.md` §OTel |
| Emitter sends a forbidden attribute or secret-shaped value | `sc_span_redactions_total` → `SCTelemetryForbiddenAttributeDropped` | value removed before export | redaction | `credential-compromise.md` (treat the value as exposed in the emitter's process memory only; fix the emitter) |
| Worker spoofs another execution's ID | k3d test; in production silent | none (identity overwritten) | `transform/workers_identity` | — |
| Spans from an unknown pod | dropped (`filter/workers_unattributed`) | none | — | — |
| Span-metric cardinality creep (new attribute added to dimensions) | `SCPrometheusCardinalityHigh`; plan 38 `lint-metrics` | Prometheus memory | none | revert config PR |
| Debug grant left active | `SCTelemetryDebugCaptureActive` | content captured within scope | loader expires it at `expiresAt` (≤ 24 h) | revert the grant PR |
| Debug objects outlive retention | `telemetry-debug` sweeper failure → its CronJob check (plan 44 framework) | retention breach | lifecycle rule as backstop (7 d) | `monitoring-failure.md` §Debug capture |
| Tempo unavailable (H03) | `SCTempoDown`, `otelcol_exporter_send_failed_spans_total` | traces lost after queue fills | bounded retry 5 min | `monitoring-failure.md` |
| Harness configured with a forbidden telemetry variable | Job builder rejects dispatch (`POLICY_VIOLATION`), registry lint fails in CI | no dispatch | fail closed | fix registry PR |

## 16. Retry behavior

Emitters: OTLP exporter retries with backoff for at most 30 s (`OTEL_EXPORTER_OTLP_TIMEOUT=10000`, SDK retry defaults), then drops. `sc-worker` final flush ≤ 5 s. Collector → Tempo (H03): `retry_on_failure` max 5 min. No retry ever blocks a request or a worker exit.

## 17. Idempotency behavior

Spans are fire-and-forget. Re-exported spans (retries) may duplicate a span, which only affects span-metric counts by the duplicate. Span IDs persisted on records make re-emission after a Runner restart idempotent in Tempo (same IDs).

## 18. Bootstrap behavior

Not in the seed set. Deployed with the `monitoring` layer after control transfer. V-23 (plan 50) checks: Collector Ready, `up{job="otel-collector"} == 1`, and a synthetic span sent by `./bootstrap verify` from a short-lived pod in `monitoring` labelled `app.kubernetes.io/name=sc-verify-otel` (`telemetrygen` image, digest-pinned, span name `sc.runner.api` with allowlisted attributes). Plan 36 must add a matrix row `monitoring` pods `app.kubernetes.io/name=sc-verify-otel` → `otel-collector:4317` (GC-18); MO-10 alone admits only `strange-company` increments `sc_span_calls_total{span_name="sc.runner.api"}` within 60 s.

## 19. Upgrade behavior

- Renovate bumps the contrib image and chart. CI runs `otelcol-contrib validate --config` on the rendered config with the new image and the k3d privacy suite (§22).
- Component stability and config key changes (`redaction` summary attributes, `spanmetrics` naming, `k8sattributes` association, `count` connector) are re-verified by 40-AC-9. A failing check blocks the bump.

## 20. Recovery behavior

Nothing to recover: no state. After black start the Collector returns with the monitoring layer; tasks recovered from `sc-pg` keep their `trace_id`, so post-recovery spans join the original traces (in Tempo, from H03).

## 21. Observability

- Collector self-metrics (`job=otel-collector`, port 8888): `otelcol_receiver_accepted_spans_total`, `otelcol_receiver_refused_spans_total`, `otelcol_processor_refused_spans_total`, `otelcol_exporter_sent_spans_total`, `otelcol_exporter_send_failed_spans_total`, `otelcol_process_memory_rss` (verify names on the pinned version).
- Alerts (plan 42): `SCOTelCollectorDown`, `SCOTelCollectorRefusingData`, `SCTelemetryForbiddenAttributeDropped`, `SCTelemetryDebugCaptureActive`, at H03 `SCTempoDown`, `SCTelemetryStorageWriteFailing`.
- Dashboard: `sc-monitoring-health` (Collector panels), `sc-workers` (span RED metrics) — plan 39.

## 22. Testing strategy

All deterministic, no paid model calls [SPEC §104].

- **Contract tests (emitters; plans 26, 27, 53 run them, this plan defines them):** every span produced in unit/integration tests is captured by an in-memory exporter, serialized to the `SpanRecord` JSON shape and validated against `telemetry.schema.yaml` (allowlisted keys, required attributes per span name, parenting). A test failing on any unknown key is the first privacy layer.
- **Allowlist drift:** `make render-otel-allowlist && git diff --exit-code gitops/platform/otel-collector/allowlist.yaml`.
- **Config validation:** `otelcol-contrib validate --config=<rendered>` with the pinned image.
- **k3d privacy suite (`make e2e-otel`):** the Collector (as configured) plus Prometheus, a fake worker pod in `agent-jobs` with labels `strange.company/execution-id=sc-9-x1`, `strange.company/task-id=sc-9`, and the `debug` exporter temporarily wired (test-only overlay) to a file sink:
  1. **Canary content:** spans whose attributes include `gen_ai.prompt="SC-CANARY-PROMPT-91c2"`, `sc.tool.full_command="curl -H 'Authorization: Bearer SC-CANARY-TOK'"`, `exception.message="SC-CANARY-EXC"` and a value `sk-ant-AAAAAAAAAAAAAAAAAAAAAAAA` on an allowlisted key → the sink contains none of the canary strings; the allowlisted key's value is masked; `sc_span_redactions_total` increased.
  2. **Spoofing:** the fake worker sends `sc.execution_id=sc-1-x1` → the sink shows `sc-9-x1`.
  3. **Unknown pod:** a pod without the labels sends a span → dropped.
  4. **Unknown span name** `sc.secret.dump` → dropped.
  5. **Cardinality:** 1 000 spans with distinct `sc.execution_id`/`sc.task_id` values → `count({__name__=~"sc_span_.*"})` unchanged after the first batch (IDs are not dimensions).
- **Hierarchy test (C10/C11 live or k3d with the fake harness):** with the debug overlay enabled in a test cluster, one `fake-success` execution yields spans whose parent chain is `sc.tool.action → sc.harness.session → sc.worker.run → sc.execution → sc.task`, and `sc.model.invocation → sc.execution`, all with the task's `trace_id`.
- **Harness env test (R01, plan 28 harness image):** the Job builder's golden Job manifest for the `claude` harness contains exactly the §9.4 telemetry variables; a registry fixture adding `OTEL_LOG_USER_PROMPTS=1` fails `make lint-policy`; the real harness CLI (pinned version, pointed at the fake Gateway upstream fixture) run with `CLAUDE_CODE_ENABLE_TELEMETRY=0` produces no connection attempt to any OTLP port (captured by a network namespace sniffer in the test).

## 23. Acceptance criteria

`PQ` as defined in plan 38 §23.

### GATE-P09

- **40-AC-1** `./bootstrap exec -- flux get helmreleases -n monitoring otel-collector` → `Ready=True`; `PQ 'up{job="otel-collector"}'` = 1 for every replica.
- **40-AC-2** V-23: `./bootstrap verify --only V-23` sends one synthetic `sc.runner.api` span and `PQ 'sum(sc_span_calls_total{span_name="sc.runner.api"})'` increases by ≥ 1 within 60 s.
- **40-AC-3** The span-metric names are exactly `sc_span_calls_total` and `sc_span_duration_seconds_bucket|_sum|_count`, and `sc_span_redactions_total` exists after test 1 of the privacy suite (`PQ 'count by (__name__) ({__name__=~"sc_span_.*"})'`).
- **40-AC-4** `make e2e-otel` passes all five privacy-suite scenarios (§22).
- **40-AC-5** `make render-otel-allowlist && git diff --exit-code` passes on the P09 merge commit; adding a key to the schema without regenerating fails CI (RED proof).
- **40-AC-6** NetworkPolicy: from a pod in `apps`, `curl -m 5 http://otel-collector-workers.monitoring.svc:4318/v1/traces` times out; from a labeled worker test pod in `agent-jobs` the same call returns HTTP 200/400 (reachable); from a worker pod, `otel-collector.monitoring.svc:4317` times out.
- **40-AC-7** `kubectl auth can-i --as=system:serviceaccount:monitoring:otel-collector list pods -n agent-jobs` = yes; `… list pods -n strange-company` = no; `… get secrets -n agent-jobs` = no.
- **40-AC-9** Version re-verification (k3d, every contrib bump): `otelcol-contrib components` lists `redaction`, `transform`, `filter`, `k8sattributes`, `spanmetrics`, `count`, `prometheus` with stability ≥ beta (or alpha explicitly accepted in the PR), and the privacy suite passes.

### GATE-C11 (contribution)

- **40-AC-11** After the C10 fake end-to-end suite: `SELECT count(*) FROM tasks WHERE trace_id !~ '^[0-9a-f]{32}$'` = 0 and `SELECT count(*) FROM executions WHERE span_id !~ '^[0-9a-f]{16}$'` = 0 (plan 29/26 columns), and `PQ 'sum by (span_name) (increase(sc_span_calls_total[1h]))'` includes `sc.task`, `sc.execution`, `sc.worker.run`, `sc.tool.action`, `sc.test.invocation`, `sc.model.invocation`, `sc.verification`.
- **40-AC-12** Debug grant lifecycle (test cluster): merging a grant for `execution sc-N-x1` with `capture: [completions]` raises `SCTelemetryDebugCaptureActive`; one fake invocation writes exactly one object under `evidence/telemetry-debug/<grant>/sc-N-x1/`; after `expiresAt` the alert resolves and no new objects appear; the sweeper deletes the object after `retentionDays` (test-shortened); a grant with `retentionDays: 30` or a 48 h window is rejected by the loader (`POLICY_UNAVAILABLE` not triggered; the grant alone is ignored and logged).

### GATE-R01 (contribution; plan 28 owns the gate)

- **40-AC-10** The harness env test of §22 passes for the real harness at its pinned version, including verification that the §9.4 variable names exist in that version's documentation or help output (recorded in the PR).

### GATE-H03 (contribution; plan 41 owns the gate)

- **40-AC-13** On small-ha, after one fake execution, Tempo returns the trace by the task's `trace_id` (`GET /api/traces/<trace_id>` on `tempo:3200`) with the span names of 40-AC-11, and no span attribute key outside the allowlist (`jq` over the response against `allowlist.yaml`).

## 24. Explicit non-goals

- Storing traces in MVP (ADR-0551).
- Using OTel metrics or logs as an accounting source (SPEC §93; the ledger is authoritative).
- Native harness telemetry in MVP (§9.4).
- OTel instrumentation of Forgejo, Woodpecker, Authentik or Hermes internals.
- Tail sampling, OTel Operator, auto-instrumentation injection.
- Authenticated OTLP (in-cluster NetworkPolicy and anti-spoofing suffice for non-authoritative telemetry).

## 25. Open questions

| # | Question | Blocking? | Proposed resolution | Owner |
|---|---|---|---|---|
| Q-40-1 | Plans 26 and 53 use span names (`task`, `execution`, `dispatch`, `ingest`, `verify`; `gateway.request`, `gateway.*`) and attributes (`gen_ai.system`, `sc.cost_micro_usd`, `sc.usage_status`, `sc.denial_code`) that differ from the contract. | no | The contract names win (§7.2); the schema now includes `sc.execution.dispatch|ingest`, `sc.gateway.reserve|upstream|settle`, `sc.cost.total_micro_usd`, `sc.gateway.denial_code`, `sc.token_kind`. Plans 26/53 rename at consolidation. | 26 / 53 / lead |
| Q-40-2 | The gateway token needs a `trace_parent` claim for trusted parenting of model spans. | no | Plan 53 adds the optional claim to `gateway-token.schema.yaml`; without it the Gateway emits a root span with a link (plan 53's current behavior), and 40-AC-11's hierarchy check accepts a link instead of a parent. | 53 |
| Q-40-3 | SPEC §93 says Hermes SHOULD emit OTel; upstream Hermes Agent support is unverified. | no | Interpretation: Hermes actions are represented by `sc.runner.mcp` and orchestrator `sc.model.invocation` spans. Re-evaluate at C08 if Hermes exposes OTel with content capture off. | 24 / 40 |
| Q-40-4 | Workers reaching `otel-collector-workers` is an addition to SPEC §88's default worker egress list. | no | Recorded as explicit platform policy (§13); plan 36 adds the single allow rule; plan 34 security review confirms. The alternative (Runner relays worker spans) is kept as a fallback if review rejects it. | 36 / 34 |
| Q-40-5 | Worker pod labels `strange.company/execution-id` and `strange.company/task-id` must exist on worker pods. | no | Plan 26 Job builder sets both (ADR-0014 already requires the execution label for Secret binding). | 26 |
| Q-40-6 | Debug-capture storage. | — | **Resolved by plan 14 B9:** prefix `telemetry-debug/` in the `evidence` bucket; instance `telemetry-debug` (Gateway writes, Runner sweeper deletes where the provider supports prefix scoping; otherwise the lifecycle rule alone enforces retention). | — |
