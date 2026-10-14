# ADR-0552: The OpenTelemetry Collector (contrib) is the log agent; worker, CI and Hermes logs are excluded from Loki

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 41

## Context
H03 adds Loki and needs a node log agent. Candidates are Grafana Alloy, Promtail (deprecated upstream), Fluent Bit, or the OTel Collector. SPEC §94 forbids general telemetry capturing prompts, completions, source code or secret-bearing output. Worker containers print harness streams (stream-json), which contain exactly that content, and Hermes logs may contain conversation text.

## Decision
- The log agent is the **OpenTelemetry Collector contrib distribution as a DaemonSet `otel-agent` in `monitoring-node`**. It uses the `filelog` receiver on `/var/log/pods` and the k3s audit log, the same `redaction` and `transform` privacy processors as plan 40, and exports to Loki's native OTLP endpoint (verify on the selected Loki version).
- **Excluded at the source** (`filelog.exclude`): every container log in `agent-jobs`, `ci-jobs` and `hermes`. Worker output reaches durable storage only through the Runner's scrubbed persistence to S3 `evidence` (plan 27). CI logs stay in Woodpecker. Hermes is observed through the Runner's MCP spans and the blackbox probe.

## Consequences
One telemetry technology and one privacy configuration to review. The Collector contrib image is larger than Alloy (acceptable).

## Alternatives rejected
- Grafana Alloy: capable, but it would need a second implementation of the redaction allowlist in a different configuration language.
- Shipping worker logs to Loki with redaction: redaction of free text is best-effort and cannot meet SPEC §94's "SHALL NOT capture by default".

## Verification
H03 gate (plan 41): a fake worker printing a canary prompt string produces zero matches in Loki (`{namespace="agent-jobs"}` returns no streams), and a canary secret string printed by a platform test pod is stored as `[REDACTED]`.
