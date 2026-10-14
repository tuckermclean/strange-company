# ADR-0551: In MVP, traces are converted to span metrics and not stored; trace IDs are recorded on task records

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 40

## Context
SPEC §93 requires OpenTelemetry-compatible telemetry with a task → execution → invocation hierarchy. SPEC §95 and §127 make Tempo a small-ha (H03) concern ("full Loki/Tempo retention" is an MVP non-goal). A bounded in-memory trace buffer is lost on every Collector restart and has no query UI.

## Decision
- MVP: the OpenTelemetry Collector receives OTLP spans from the Runner, Gateway and `sc-worker`, applies the privacy pipeline, and feeds them only to the **spanmetrics connector**. The resulting low-cardinality RED metrics are exported to Prometheus. Spans themselves are **not stored**. The `debug` exporter exists in config, disabled by default.
- Every task and execution record in `sc-pg` stores its `trace_id` and anchor/execution `span_id` (plan 29 task schema; plan 26). At H03, Tempo stores traces, and Grafana links task rows to traces by `trace_id`. Correlation therefore works retroactively for all traces emitted after H03, and the identifiers already exist on MVP-era records.
- Exact accounting never depends on traces (SPEC §93).

## Consequences
No trace storage cost or memory on the 1-node profile. Investigation in MVP uses the task history (`sc-pg`), Runner-persisted worker logs (S3 `evidence`) and span metrics.

## Alternatives rejected
- Tempo in MVP: about 0.4 GiB more memory on a node that is already tight (SI-01), for a non-required capability.
- In-memory/debug-exporter storage: unqueryable and lost on restart. It is false comfort.

## Verification
P09 gate (plan 40): a fake-harness execution produces `sc_span_calls_total{span_name="sc.execution"}` increments and the task record carries a 32-hex `trace_id`. H03 gate (plan 41): the same `trace_id` is retrievable from Tempo.
