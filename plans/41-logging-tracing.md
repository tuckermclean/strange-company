# 41 — Logging and tracing (MVP logs without Loki; Loki and Tempo at small-ha)

**Status:** Stage-0 plan (draft)
**DAG nodes:** H03 (primary: Loki + Tempo + log agent); contributes to P09 (MVP log handling and bounds), C11 (worker-log persistence checks, with plans 26/27), H04 (telemetry stack under node loss)
**Related plans:** 07 (k3s audit log), 08 (journald, kubelet log rotation), 11 (register S-21, S-28), 14 (`telemetry` bucket, lifecycle), 15 (break-glass evidence), 26/27 (worker log archive and scrubbing), 35, 36, 38 (scrape), 39 (datasources), 40 (Collector, privacy processors, trace export), 42 (alerts), 43 (retention R-02, R-03, R-04, R-23, R-24, R-33), 47 (HA expansion), 51 (capacity)
**Contracts:** consumes `telemetry.schema.yaml` (`LogRecord`, secret value patterns), `alert-catalog.yaml`
**ADRs:** ADR-0552 (OTel Collector contrib is the log agent; worker, CI and Hermes logs excluded from Loki) — binding; ADR-0550 (`monitoring-node`), ADR-0551

## 1. Purpose

Define where logs and traces live at each stage. In MVP there is no Loki and no Tempo [SPEC §95, §127]: component logs stay in the kubelet's rotated files and are read with `kubectl logs`; worker logs are scrubbed and persisted by the Runner to S3 `evidence`; node logs are bounded by journald; the Kubernetes audit log is bounded on disk. At small-ha (H03) Loki and Tempo are added on S3 `telemetry`, fed by an OpenTelemetry Collector DaemonSet with the same privacy rules as plan 40 — unless a deterministic measurement rule (§9.6) justifies deferring them, which SPEC §95 explicitly allows.

## 2. Requirements derived from SPEC.md

1. Loki and Tempo are not required for the minimum bootstrap [SPEC §95, §127].
2. They SHOULD be added by or during `small-ha` unless measurements justify a different decision [SPEC §95, §119 PHASE 6].
3. Persistent logs and traces SHOULD use object storage, not VPS root disks [SPEC §95, §29].
4. Telemetry SHALL NOT capture prompts, completions, source code, environment variables or secret-bearing command output by default [SPEC §94].
5. Retention for Loki data, Tempo data, worker logs and model telemetry is intentional; root disks must not fill [SPEC §107].
6. Logs/traces RPO is best effort / ≤ 24 h and RTO non-critical [SPEC §108].
7. Break-glass use SHOULD produce audit evidence where practical [SPEC §22].
- **41-R-1** Every log on a node has a size cap: kubelet container logs (10 MiB × 3), journald (1 GiB, 14 d), k3s audit log (50 MiB × 6, 7 d), the log agent's checkpoint directory (≤ 64 MiB).
- **41-R-2** Worker (`agent-jobs`), CI (`ci-jobs`) and Hermes (`hermes`) container logs never enter Loki (ADR-0552). Worker output reaches durable storage only through the Runner's scrubbed archive.
- **41-R-3** Loki stream labels are a closed, low-cardinality set (§7.3). Pod names, task IDs, trace IDs and execution IDs are never labels; they may be structured metadata.
- **41-R-4** Loki and Tempo hold no data that is the system of record for anything; their loss is acceptable (S-28).
- **41-R-5** The H03 decision (deploy, partially deploy or defer) is computed from recorded measurements by a stated rule and committed with its evidence.

## 3. Inputs

- Node baseline settings (plan 08): journald `SystemMaxUse=1G`, `SystemKeepFree=5G`, `MaxRetentionSec=14day`; kubelet `containerLogMaxSize: 10Mi`, `containerLogMaxFiles: 3`.
- k3s audit configuration (plan 07 §9.1): `/var/lib/rancher/k3s/server/logs/audit.log`, JSON, Metadata level, `maxsize 50`, `maxbackup 5`, `maxage 7`.
- Worker log archive (plans 26 §14.5 and 27): `s3://<prefix>-evidence/logs/<task>/<execution>/worker.log.gz`.
- Bucket `<prefix>-telemetry` and credential `SC-OBJECT-STORAGE/telemetry` (plan 14/35), lifecycle on `loki/` and `tempo/` (plan 14/43).
- Privacy patterns: `telemetry.schema.yaml` `x-secret-value-patterns`; plan 40's processor configuration.
- Measurements for §9.6 (Prometheus at small-ha before H03).

## 4. Outputs

- **MVP:** no new workloads. Documented access paths (§7.2), the bounds of 41-R-1 asserted by gates, and the worker-log requirements on plans 26/27.
- **H03 (small-ha overlay only):**
  - HelmRelease `loki` (namespace `monitoring`), Service `loki.monitoring.svc:3100`;
  - HelmRelease `tempo` (namespace `monitoring`), Services `tempo.monitoring.svc:3200` (query) and `tempo.monitoring.svc:4317` (OTLP from the Collector only);
  - DaemonSet `otel-agent` (namespace `monitoring-node`, ADR-0550/0552);
  - plan 40's `otlp/tempo` exporter enabled; plan 39's `sc-loki` and `sc-tempo` datasources enabled;
  - the decision record `gitops/clusters/<cluster>/config/telemetry-decision.yaml` (§9.6);
  - alerts `SCLokiDown`, `SCTempoDown`, `SCLogAgentDown`, `SCLogIngestionErrors`, `SCTelemetryStorageWriteFailing`, `SCLokiCompactorFailing`, `SCTempoCompactorFailing` (plan 42 catalogue).

## 5. Dependencies

**Hard (DAG):** H03 depends on H01 (three nodes; capacity) and P09 (monitoring). It is sequenced before H04 (drills).
**Soft/runtime:** plan 14 (bucket and credential), plan 40 (Collector exporter), plan 38 (scrape jobs `loki`, `tempo`, `otel-agent`), plan 39 (datasources), plan 36 (NetworkPolicy), plan 51 (memory budget at small-ha), plan 43 (lifecycle and compactor retention).

## 6. Owned state

| State | Location | Writer | Criticality |
|---|---|---|---|
| Loki chunks and index (H03) | S3 `<prefix>-telemetry/loki/` (S-28) | Loki | best effort; 30 d |
| Tempo blocks (H03) | S3 `<prefix>-telemetry/tempo/` (S-28) | Tempo | best effort; 14 d |
| Loki/Tempo WAL | `emptyDir` (sizeLimit 5 GiB / 3 GiB) | Loki, Tempo | ephemeral; lost on pod deletion (≤ 2 h of unflushed data) |
| Log agent read offsets | hostPath `/var/lib/otel-agent` (≤ 64 MiB, `file_storage` extension) | otel-agent | ephemeral; loss causes re-reading of up to the kubelet's 30 MiB per container (duplicates, never loss) |
| Telemetry decision record | Git `gitops/clusters/<cluster>/config/telemetry-decision.yaml` | `./bootstrap expand` (plan 47) via PR | authoritative for the H03 choice |

Not owned here: worker logs (Runner, S-21), Woodpecker logs (`woodpecker-pg`, S-23), journald and kubelet files (node baseline, plan 08), the audit log file (plan 07).

**Decision on WAL medium:** `emptyDir`, not local-path. Logs and traces are best effort [SPEC §108], and `emptyDir` lets a pod reschedule to any node after a node loss without operator action, whereas a local-path PV would pin it (plan 11 S-28 lists "local-path WAL"; this plan narrows that to `emptyDir` with the same disk budget, §25 Q-41-4).

## 7. External interfaces

### 7.1 Endpoints (H03)

| Service | Port | Purpose | Allowed clients (plan 36) |
|---|---|---|---|
| `loki.monitoring.svc` | 3100 | push (`/otlp/v1/logs`, `/loki/api/v1/push`), query, `/metrics` | `otel-agent` (push), Grafana (query), Prometheus (metrics) |
| `tempo.monitoring.svc` | 4317 | OTLP gRPC ingest | `otel-collector` only |
| `tempo.monitoring.svc` | 3200 | query, `/metrics` | Grafana, Prometheus |

No public exposure; no multi-tenancy (`auth_enabled: false`); access control is NetworkPolicy.

### 7.2 MVP log access paths (no Loki)

| Logs | Where | Bound | How a human reads them |
|---|---|---|---|
| Platform container logs | node `/var/log/pods/…` | 10 MiB × 3 per container (plan 08) | `./bootstrap exec -- kubectl -n <ns> logs <pod> [--previous]` |
| Worker logs | S3 `evidence/logs/<task>/<execution>/worker.log.gz`, scrubbed (plan 27 `src/scrub`), indexed in `sc-pg` | 10 MiB per execution (`logs.maxBytes`, plan 26); 90 d (plan 43 R-04) | Hermes `agent_logs` / Runner `GET /tasks/{id}/logs` (tail ≤ 64 KiB); full object via the toolbox with the evidence credential |
| CI pipeline logs | `woodpecker-pg` (S-23) | 90 d / newest 50 per repo (plan 43 R-19) | Woodpecker UI (`ci.<d>`) |
| k3s and node services | journald on each node | 1 GiB / 14 d (plan 08) | `ssh` (operator key) `journalctl -u k3s` |
| Kubernetes audit | `/var/lib/rancher/k3s/server/logs/audit.log*` on each server | 300 MiB / 7 d (plan 07) | `ssh` + `jq`; break-glass use also raises `SCK3sBreakGlassAuditSeen` from plan 07's node counter |
| Kubernetes events | etcd, 1 h TTL (k3s default, verify) | — | `kubectl get events -A` |
| Runner/Gateway structured logs | container stdout (JSON, `LogRecord` contract) with `trace_id` | as platform logs | `kubectl logs`, `jq 'select(.task_id=="sc-184")'` |

### 7.3 Loki labels and metadata (H03; normative)

Stream labels (only these): `job` (`pods`, `k8s-audit`, `k8s-events`), `namespace`, `app` (`app.kubernetes.io/name` or the container name if absent), `container`, `node`, `stream` (`stdout`/`stderr`), `level` (when parsed: `debug|info|warn|error`). Bounded by the number of platform workloads (≈ 60 apps × 1–3 nodes).

Structured metadata (not indexed): `pod`, `trace_id`, `span_id`, `task_id`, `execution_id` (only from Runner/Gateway JSON lines), `audit_verb`, `audit_user`, `audit_resource`.

Loki OTLP ingestion is configured so that only the label set above becomes index labels (`limits_config.otlp_config` resource-attribute mapping; verify the exact key on the pinned Loki ≥ 3.x) and every other attribute becomes structured metadata or is dropped by the agent's allowlist.

## 8. Internal interfaces

### 8.1 Log agent `otel-agent` (H03; ADR-0552)

DaemonSet in `monitoring-node` (PSA privileged, ADR-0550), image: the same digest-pinned contrib image as plan 40, chart `open-telemetry/opentelemetry-collector` with `mode: daemonset`.

```yaml
extensions:
  file_storage/offsets: {directory: /var/lib/otel-agent, timeout: 2s, compaction: {on_start: true, directory: /var/lib/otel-agent}}
receivers:
  filelog/pods:
    include: [/var/log/pods/*/*/*.log]
    exclude:                                     # ADR-0552: never collected
      - /var/log/pods/agent-jobs_*/*/*.log
      - /var/log/pods/ci-jobs_*/*/*.log
      - /var/log/pods/hermes_*/*/*.log
    start_at: end
    include_file_path: true
    storage: file_storage/offsets
    operators:
      - {type: container, id: container-parser}  # CRI format; sets k8s.namespace.name, k8s.pod.name, k8s.container.name (verify operator name)
      - {type: json_parser, if: 'body matches "^\\{"', parse_to: attributes, on_error: send_quiet}
  filelog/audit:                                 # server nodes only (nodeSelector via a second DaemonSet values block)
    include: [/var/lib/rancher/k3s/server/logs/audit.log]
    start_at: end
    storage: file_storage/offsets
    operators: [{type: json_parser, parse_to: attributes}]
processors:
  memory_limiter: {check_interval: 1s, limit_mib: 160, spike_limit_mib: 32}
  filter/annotated_exclude:                      # pods annotated strange.company/logs: exclude
    logs: {log_record: ['resource.attributes["k8s.pod.annotations.strange.company/logs"] == "exclude"']}
  transform/audit_allowlist:                     # keep only Metadata-level fields
    log_statements:
      - context: log
        conditions: ['attributes["kind"] == "Event"']
        statements:
          - keep_keys(attributes, ["verb", "user", "objectRef", "responseStatus", "sourceIPs", "stage", "userAgent"])
          - set(body, "")                        # no request/response objects (they are absent at Metadata level anyway)
  transform/secrets:                             # value patterns from telemetry.schema.yaml x-secret-value-patterns (generated)
    log_statements:
      - context: log
        statements:
          - replace_all_patterns(attributes, "value", "<pattern>", "[REDACTED]")   # one statement per pattern, generated
          - replace_pattern(body, "<pattern>", "[REDACTED]")                       # one statement per pattern, generated
          - truncate_all(attributes, 1024)
  redaction/log_attributes:                      # attribute-key allowlist (LogRecord keys + k8s/resource keys above)
    allow_all_keys: false
    allowed_keys: ${file:/etc/otelcol/allowlist/log-keys.yaml}
  batch: {send_batch_size: 1000, timeout: 5s}
connectors:
  count/log_redactions:                          # → sc_log_redactions_total
    logs: {sc_log_redactions: {conditions: ['IsMatch(body, "\\[REDACTED\\]")']}}
exporters:
  otlphttp/loki:
    endpoint: http://loki.monitoring.svc:3100/otlp       # Loki native OTLP (verify path on the pinned Loki)
    sending_queue: {queue_size: 1000}
    retry_on_failure: {max_elapsed_time: 300s}
  prometheus: {endpoint: 0.0.0.0:8889}
service:
  extensions: [file_storage/offsets]
  pipelines:
    logs:     {receivers: [filelog/pods, filelog/audit], processors: [memory_limiter, filter/annotated_exclude, transform/audit_allowlist, transform/secrets, redaction/log_attributes, batch], exporters: [otlphttp/loki, count/log_redactions]}
    metrics:  {receivers: [count/log_redactions], exporters: [prometheus]}
```

Mounts: `/var/log/pods` (read-only hostPath), `/var/lib/rancher/k3s/server/logs` (read-only, servers), `/var/lib/otel-agent` (hostPath, `DirectoryOrCreate`). Runs as root only to read root-owned logs (`runAsUser: 0`, all capabilities dropped except `DAC_READ_SEARCH`, read-only root filesystem). This is why it lives in `monitoring-node`.

Kubernetes events (`job=k8s-events`): the gateway Collector (plan 40) gains, in the small-ha overlay only, a `k8sobjects` receiver watching `events` of type `Warning`, exported to Loki (RBAC: `list/watch events` cluster-wide, read-only). Normal events are not stored.

### 8.2 Loki (H03)

| Item | Decision |
|---|---|
| Chart / mode | Grafana's `loki` Helm chart, **SingleBinary** mode (`deploymentMode: SingleBinary`, verify key), `replicas: 1`. Selection rule: latest Loki 3.x minor supported by the chart at H03 time; requires native OTLP ingestion. |
| Schema / index | `schema_config` v13, `store: tsdb`, `object_store: s3`, period 24h. |
| Storage | S3 `<prefix>-telemetry`, path prefix `loki/` (verify prefix support in the pinned Loki object-store client; fallback: a dedicated bucket `<prefix>-telemetry-loki` added by plan 14). Credential `SC-OBJECT-STORAGE/telemetry`. |
| WAL / ingester | `emptyDir` 5 GiB; `chunk_idle_period: 30m`, `max_chunk_age: 2h`, `flush_on_shutdown: true`, `replication_factor: 1`. |
| Retention | compactor `retention_enabled: true`, `retention_period: 720h` (30 d, plan 43 R-02), `delete_request_store: s3`, `compaction_interval: 10m`; bucket lifecycle `loki/` 35 d as backstop (plan 14). |
| Limits | `ingestion_rate_mb: 4`, `ingestion_burst_size_mb: 8`, `max_line_size: 16KB` with `max_line_size_truncate: true`, `per_stream_rate_limit: 1MB`, `max_label_names_per_series: 10`, `reject_old_samples_max_age: 168h`, `max_query_lookback: 720h`, `max_query_series: 5000`. |
| Ruler | disabled (no Loki-rule alerts in the catalogue; the schema keeps `loki-rule` for future use, routed only to Alertmanager). |
| Caches | in-process only (no memcached). |

### 8.3 Tempo (H03)

| Item | Decision |
|---|---|
| Chart / mode | Grafana's `tempo` Helm chart (monolithic), `replicas: 1`. Selection rule: latest Tempo 2.x minor at H03 time. |
| Receivers | `otlp` gRPC on 4317 only (from `otel-collector`). |
| Storage | `trace.backend: s3`, bucket `<prefix>-telemetry`, `prefix: tempo/` (verify); WAL `emptyDir` 3 GiB. |
| Retention | `compactor.compaction.block_retention: 336h` (14 d, plan 43 R-03); lifecycle `tempo/` 21 d backstop. |
| Metrics generator | disabled (span metrics come from the plan 40 Collector). |
| Search | TraceQL enabled; `max_bytes_per_trace: 5MB`. |

### 8.4 Correlation

- Grafana `sc-tempo` → `sc-loki`: `tracesToLogsV2` maps `trace_id` structured metadata (Runner and Gateway logs) to the span's trace (plan 39 §7.2).
- Task → trace: plan 39's task tables link `trace_id` (from the `reporting` views) to Tempo. Traces of tasks created before H03 do not exist (ADR-0551); their identifiers do.

## 9. Configuration

### 9.1 Configuration sources

| Key (Git path) | Default | Changed by |
|---|---|---|
| `gitops/platform/loki/{base,overlays/small-ha}/values.yaml` | §8.2 | PR |
| `gitops/platform/tempo/{base,overlays/small-ha}/values.yaml` | §8.3 | PR |
| `gitops/platform/otel-agent/{base,overlays/small-ha}/values.yaml` | §8.1 | PR |
| `gitops/platform/otel-agent/log-keys.yaml`, `secret-patterns.yaml` (generated from `telemetry.schema.yaml`) | — | `make render-otel-allowlist` |
| Layer membership | `loki`, `tempo` layers exist only in `gitops/profiles/small-ha/` (plan 18 §8.3 #25); `otel-agent` joins the `monitoring` layer in the small-ha overlay | `sc-gitops set-profile` (plan 47) |
| `gitops/clusters/<c>/config/telemetry-decision.yaml` | absent before H03 | expand PR (§9.6) |

### 9.2 MVP bounds (owned by plans 07/08; asserted here)

kubelet: `containerLogMaxSize: 10Mi`, `containerLogMaxFiles: 3`. journald: `SystemMaxUse=1G`, `SystemKeepFree=5G`, `MaxRetentionSec=14day` (plan 08 §9 values). Audit: `audit-log-maxsize=50`, `audit-log-maxbackup=5`, `audit-log-maxage=7`.

### 9.3 Worker logs (requirements on plans 26/27, restated)

1. Worker stdout is framed and scrubbed by `sc-worker` before it reaches the pod log (plan 27 §13.4); the Runner scrubs again before upload (plan 26 step 7).
2. The Runner uploads the full pod log at terminal state, then deletes the Job; the object key is `logs/<task>/<execution>/worker.log.gz`; the index row records the sha256 and `logs_status` (`archived|lost|expired`).
3. Prompt and completion text is never in worker logs unless the harness prints it to stdout. Claude Code's stream-json does print assistant text and tool I/O. That content is therefore stored **only** in the evidence object (an artifact of the execution, access-controlled, 90 d), never in general telemetry or Loki (ADR-0552). This is a deliberate exception to "general telemetry" (SPEC §94 governs telemetry; worker logs are evidence) and is recorded in §25 Q-41-1.

### 9.4 Privacy filters (H03; same patterns as plan 40)

- Exclusions at the source (`filelog.exclude`) for `agent-jobs`, `ci-jobs`, `hermes`; opt-out annotation `strange.company/logs: exclude` for any other pod.
- Secret value patterns replaced in body and attributes (`[REDACTED]`), counted in `sc_log_redactions_total`.
- Attribute allowlist (`redaction/log_attributes`): the `LogRecord` keys of `telemetry.schema.yaml` plus `k8s.*` resource keys and the audit fields.
- Audit records: Metadata level only (plan 07 policy); the agent keeps only verb, user, objectRef, responseStatus, sourceIPs, stage, userAgent.
- Lines > 16 KiB are truncated by Loki.

### 9.5 Cost (H03 estimate, recorded and re-measured by §9.6)

Inputs (steady state, 3 nodes, workers excluded): platform logs ≈ 300 MiB/day raw → ≈ 40 MiB/day stored (Loki chunk compression ≈ 7–10×) → ≈ 1.2 GiB over 30 d; Tempo ≈ 15 MiB/day (plan 40 §9.2) → ≈ 0.2 GiB over 14 d. At a typical European S3 price of 0.01–0.02 EUR/GiB-month (OVH Standard class, verify current pricing) storage is < 0.05 EUR/month; PUT requests (≈ 2 000/day Loki flushes + compaction, ≈ 500/day Tempo) are free or < 0.10 EUR/month on providers that charge them. Memory is the binding cost (§12).

### 9.6 Measurement-based decision rule (H03)

**Sequencing (GC-06).** The H01 platform PR of plan 47 must **not** include the `loki`, `tempo` or `otel-agent` components; H01/H02 gates never wait for them. After H02 has passed and at least 3 days of small-ha Prometheus data exist, `./bootstrap expand` opens a third PR `bootstrap/expand-<run-id>-telemetry` (plan 47 / plan 50 §14.10 must adopt it) that carries the decision record and, per the outcome, the components. `sc-bootstrap telemetry-decide` runs at that point. It reads Prometheus over the preceding 7 days (minimum 3 days of data) and computes:

| Input | Query / source |
|---|---|
| `M_free` (bytes, per node) | `min_over_time(node_memory_MemAvailable_bytes[7d])` − (largest worker profile + one CI job, if that node may host them; plan 51) |
| `R_logs` (bytes/day) | `sum(increase(kubelet_container_log_filesystem_used_bytes{namespace!~"agent-jobs|ci-jobs|hermes"}[1d]))` (verify metric; fallback: daily growth of `/var/log/pods` measured by `sc-node-facts`) |
| `R_spans` (spans/day) | `sum(increase(otelcol_receiver_accepted_spans_total{job="otel-collector"}[1d]))` |
| `C` (EUR/month) | `(R_logs × 0.15 × 30 + R_spans × 1.5 KiB × 14) / 2^30 × S3_PRICE_EUR_PER_GIB` with `S3_PRICE_EUR_PER_GIB` from `infra/catalog/prices/` (plan 03) |
| `B` | `MAX_MONTHLY_COST` (plan 50) |

Rule:
1. **Deploy Loki, Tempo and the agent** iff every node satisfies `M_free ≥ 1.5 GiB` (the §12 total plus 25 % headroom) **and** `C ≤ max(0.05 × B, 2 EUR)`.
2. Else **deploy Tempo only** (with the gateway Collector export) iff every node satisfies `M_free ≥ 0.6 GiB` and Tempo's share of `C` ≤ the same bound. Logs stay MVP-style.
3. Else **defer**: the expand PR records the measurements, and a spec-issues entry is proposed ("SPEC §95 SHOULD deferred by measurement", with the numbers). Re-evaluation every 90 days or after any node resize, through a reminder issue that the expand PR opens with a due date (plan 47); no alert is defined for it.

The decision and its inputs are written to `telemetry-decision.yaml` (`kind: TelemetryDecision`: `decidedAt`, `inputs`, `outcome: deploy|tempo-only|defer`, `rule: 41-9.6-v1`) in the expand PR, so the outcome is reviewable and Git-explainable. The small-ha profile overlay includes the `loki`/`tempo`/`otel-agent` components according to `outcome` (plan 18 component selection).

## 10. Secrets

| Class | Use | Notes |
|---|---|---|
| `SC-OBJECT-STORAGE/telemetry` (plan 14/35, exists) | Loki and Tempo read/write `<prefix>-telemetry` | SOPS `gitops/clusters/<c>/secrets/monitoring/s3-telemetry.sops.yaml`; mounted only by `loki` and `tempo`; not bundled; rotation per `rotate-storage-credentials.md` (both pods restart through a checksum annotation; ingestion pauses ≤ 2 min, WAL buffers) |
| `SC-OBJECT-STORAGE/evidence` (exists) | worker log archive (Runner, MVP) | plan 26 |

No new secret class.

## 11. Storage

- MVP: nothing new. Node disk use is bounded by 41-R-1 (plan 11 §12 budget lines "OS, journald, audit" and "Pod logs").
- H03: S3 `telemetry` (≈ 1.5 GiB steady state, §9.5); per-node `emptyDir` for WAL (Loki 5 GiB, Tempo 3 GiB, on whichever node hosts the pod) and the agent's offsets (≤ 64 MiB). Plan 11's small-ha disk budget must reserve 8 GiB on each node (any node may host the pods).

## 12. Resource requirements (H03, small-ha)

| Workload | Requests (cpu / mem) | Limit (mem) | Replicas |
|---|---|---|---|
| loki (SingleBinary) | 150m / 384 MiB | 1 GiB | 1 |
| tempo (monolithic) | 100m / 256 MiB | 768 MiB | 1 |
| otel-agent | 30m / 96 MiB | 192 MiB (`limit_mib: 160`) | DaemonSet (3) |
| **Total** | ≈ 340m / ≈ 930 MiB (agent ×3 included) | | |

PriorityClass `platform`. Nothing in MVP.

## 13. Security boundary

- `loki`, `tempo` in `monitoring` (PSA baseline; pods restricted-compatible, non-root). `otel-agent` in `monitoring-node` (privileged namespace; the pod itself drops all capabilities except `DAC_READ_SEARCH`, mounts read-only host paths, runs no shell).
- RBAC: `otel-agent` needs no Kubernetes API access (file paths carry namespace and pod names; the `container` operator parses them). The gateway Collector's `k8sobjects` receiver gets `list/watch events` cluster-wide (read-only).
- NetworkPolicy (plan 36): `otel-agent` egress to `loki:3100` only (+ DNS); Loki ingress from `otel-agent`, Grafana, Prometheus; Tempo ingress 4317 from `otel-collector`, 3200 from Grafana and Prometheus; Loki and Tempo egress to the S3 endpoint (443) and DNS.
- Content: §9.4. Loki holds platform logs that may contain repository names, user names and request paths, never prompts, code or secrets by construction of the exclusions and filters. Grafana access to Loki follows Grafana roles (all `sc-*` groups may read logs; there is no per-namespace restriction in MVP of H03).

## 14. Happy path

**MVP:** an operator debugging Forgejo runs `./bootstrap exec -- kubectl -n forgejo logs deploy/forgejo`. A human investigating task `sc-184` calls `agent_logs` (tail) or downloads the archived object. Disk use stays within the caps.

**H03:**
1. `./bootstrap expand` reaches H03; `telemetry-decide` outputs `deploy`; the expand PR adds the decision record and the small-ha layers.
2. Flux reconciles `loki` and `tempo`, the `otel-agent` DaemonSet, and the Collector's Tempo exporter.
3. Within 5 min Grafana's `sc-loki` shows `{job="pods", namespace="forgejo"}` streams and `{job="k8s-audit"}`; a fake execution's trace is retrievable in Tempo.
4. The compactors enforce 30 d / 14 d; bucket lifecycles back them up.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Loki down | `SCLokiDown` | logs not ingested; agent queues 1000 batches, then drops | kubelet restart; WAL replay (emptyDir survives container restart) | `monitoring-failure.md` §Loki |
| Tempo down | `SCTempoDown` | traces dropped after the Collector queue fills | restart | `monitoring-failure.md` §Tempo |
| Agent pod down on a node | `SCLogAgentDown` | that node's logs not collected (still on disk, re-read from offsets on recovery within kubelet retention) | DaemonSet restart | `monitoring-failure.md` §Agent |
| S3 telemetry writes failing | `SCTelemetryStorageWriteFailing`; `SCObjectStorageUnreachable` | WAL grows to its `emptyDir` limit, then the pod is evicted and unflushed data is lost (best effort) | flush retries | `object-storage-outage.md` |
| Ingestion rate limit hit | `SCLogIngestionErrors` (`loki_discarded_samples_total`) | lines dropped | none | raise limits by PR or fix the noisy component |
| Compactor not running | `SCLokiCompactorFailing`, `SCTempoCompactorFailing` | retention not enforced; bucket lifecycle backstop applies at 35 d / 21 d | — | `monitoring-failure.md` §Retention |
| Secret printed by a platform pod | `sc_log_redactions_total` increase (dashboard), canary tests | value replaced by `[REDACTED]` | redaction | `credential-compromise.md` if a real secret appeared (it reached the node's log file even if not Loki) |
| Worker log archive failing (MVP and H03) | `SCRunnerLogArchiveFailing` (plan 26) | worker logs lost after Job TTL | Runner retries | `object-storage-outage.md` |
| journald or audit log exceeding caps | `SCNodeJournalOversize` (plan 43), `SCNodeRootDiskLow` | disk pressure | journald vacuum by size | `disk-pressure.md` |
| Node hosting Loki/Tempo lost (small-ha) | `SCNodeDown`, `SCLokiDown` | ≤ 2 h of unflushed data lost | pods reschedule (emptyDir) | none beyond `lost-node.md` |

## 16. Retry behavior

Agent → Loki: sending queue 1000, retry ≤ 5 min, then drop. Collector → Tempo: plan 40. Loki/Tempo → S3: their own flush retries. Worker log upload: plan 26 (every 60 s until the Job TTL). Nothing blocks a workload.

## 17. Idempotency behavior

Logs re-read after an offset loss produce duplicates (Loki deduplicates identical lines with identical timestamps in the same stream). Re-applying configuration is declarative. `telemetry-decide` is a pure function of its inputs; re-running it on the same window yields the same outcome.

## 18. Bootstrap behavior

Self-hosting-minimum installs nothing from this plan. `./bootstrap expand --profile small-ha` (plan 47) runs H01, H02, then the §9.6 decision, then enables H03 components through the profile PR. Black start of a small-ha installation reapplies the recorded decision; Loki and Tempo start empty or with whatever remains in S3 (the bucket survives).

## 19. Upgrade behavior

Renovate PRs for the Loki, Tempo and contrib charts/images. CI renders and validates configs (`loki -verify-config`, `tempo -config.verify` (verify flags), `otelcol-contrib validate`) and runs the k3d privacy suite (§22) with the new images. Loki schema changes are introduced as a new `schema_config` period starting the next day, never by rewriting old periods.

## 20. Recovery behavior

- Loki/Tempo data is best effort [SPEC §108]: no backup beyond S3 durability. After total loss, black start redeploys them pointing at the same bucket; data within retention remains queryable if the bucket survived.
- Worker logs are recovered with the `evidence` bucket (they were never in the cluster).
- Lost audit logs before H03 (node loss in MVP) are accepted; break-glass use is additionally recorded by plan 15's probe metrics and Alertmanager notifications.

## 21. Observability

- Metrics: Loki (`loki_distributor_bytes_received_total`, `loki_ingester_chunks_flush_failures_total`, `loki_discarded_samples_total`, `loki_boltdb_shipper_compact_tables_operation_last_successful_run_timestamp_seconds` (verify for tsdb)), Tempo (`tempo_distributor_spans_received_total`, `tempodb_backend_request_duration_seconds`, `tempodb_compaction_errors_total`), agent (`otelcol_receiver_accepted_log_records_total`, `otelcol_exporter_send_failed_log_records_total`, `sc_log_redactions_total`). Scraped by plan 38 (`job` `loki`, `tempo`, `otel-agent`).
- Alerts (plan 42): `SCLokiDown`, `SCTempoDown`, `SCLogAgentDown`, `SCLogIngestionErrors`, `SCTelemetryStorageWriteFailing`, `SCLokiCompactorFailing`, `SCTempoCompactorFailing`.
- Dashboard: plan 39 `sc-monitoring-health` gains Loki/Tempo panels in the small-ha overlay.

## 22. Testing strategy

All deterministic, no paid model calls.

- **Render (F11/CI):** small-ha overlay renders Loki/Tempo/agent; self-hosting-minimum renders none of them (`kustomize build gitops/profiles/self-hosting-minimum | grep -c 'name: loki'` = 0).
- **Generated allowlist drift:** as plan 40.
- **k3d privacy suite (`make e2e-logging`):** Loki SingleBinary on the SeaweedFS `s3-emulator` (CD-12) + the agent DaemonSet + test pods:
  1. a pod in namespace `agent-jobs` prints `SC-CANARY-PROMPT-5d1e` → `logcli query '{namespace="agent-jobs"}'` returns no stream, and `logcli query '{job="pods"} |= "SC-CANARY-PROMPT-5d1e"'` returns nothing;
  2. same for `ci-jobs` and `hermes`;
  3. a pod in `apps` prints `token=ghp_` followed by 36 alphanumerics → stored line contains `[REDACTED]` and not the token; `sc_log_redactions_total` increased;
  4. a pod annotated `strange.company/logs: exclude` → no stream;
  5. label cardinality: `logcli series '{job="pods"}' | jq` shows no `pod` label;
  6. an audit line for user `sc-breakglass` appears under `{job="k8s-audit"}` with `audit_user` metadata and no `requestObject`.
- **Decision rule unit tests:** fixture inputs for each outcome (`deploy`, `tempo-only`, `defer`) and the boundary values.
- **Live (H03):** §23.

## 23. Acceptance criteria

### GATE-P09 (contribution: MVP log handling)

- **41-AC-1** On the self-hosting-minimum node: `./bootstrap exec -- kubectl get pods -A -l 'app.kubernetes.io/name in (loki,tempo)'` returns nothing, and `kubectl get --raw /api/v1/nodes/node-01/proxy/configz | jq '.kubeletconfig | {containerLogMaxSize, containerLogMaxFiles}'` = `{"containerLogMaxSize":"10Mi","containerLogMaxFiles":3}`.
- **41-AC-2** On the node (`./bootstrap exec -- ssh node-01`): `systemd-analyze cat-config systemd/journald.conf | grep -E '^SystemMaxUse=1G'` matches, and `ls /var/lib/rancher/k3s/server/logs/` shows `audit.log` with the kube-apiserver flags `audit-log-maxsize=50`, `audit-log-maxbackup=5` present in `/etc/rancher/k3s/config.yaml`.

### GATE-C11 (contribution; plans 26/27 own the mechanism)

- **41-AC-3** After the C10 fake suite: every terminal execution has `logs_status = 'archived'` and an object at `evidence/logs/<task>/<execution>/worker.log.gz` whose sha256 matches the index row; a `fake-success` run whose harness prints a canary secret equal to its gateway token yields an archived log containing `«redacted` and not the token (plan 27 scrubber).

### GATE-H03

- **41-AC-4** `gitops/clusters/<c>/config/telemetry-decision.yaml` exists, validates (`kind: TelemetryDecision`), and its `outcome` follows from its recorded `inputs` by the §9.6 rule (`sc-bootstrap telemetry-decide --verify <file>` exit 0).
- **41-AC-5a** The expand platform PR of H01 contains none of `loki`, `tempo`, `otel-agent` (`git show <H01 merge> --stat | grep -c -E 'loki|tempo|otel-agent'` = 0), and the telemetry PR is a separate merge after H02.
- **41-AC-5** With outcome `deploy`: `flux get helmreleases -n monitoring loki tempo` both Ready; `kubectl -n monitoring-node get ds otel-agent` has `numberAvailable` = node count.
- **41-AC-5b** With outcome `tempo-only`: Tempo Ready, no `loki` HelmRelease, no `otel-agent` DaemonSet; 41-AC-8 (Tempo part) and 41-AC-9 (trace part) pass; the Loki/agent ACs are `n/a` and recorded so in the gate evidence.
- **41-AC-5c** With outcome `defer`: neither Loki nor Tempo exists, the decision record carries the measured inputs, and the lead has added the spec-issues entry ("SPEC §95 SHOULD deferred by measurement"); 41-AC-4 plus this check is the whole gate (41-AC-6..10 `n/a`).
- **41-AC-6** (outcomes `deploy`/`tempo-only`, per component) Objects appear under `s3://<prefix>-telemetry/loki/` and `…/tempo/` within 2 h (`aws s3 ls --recursive … | head`).
- **41-AC-7** `make e2e-logging` scenarios 1–6 pass in k3d, and on the live cluster `logcli query --since=24h '{namespace=~"agent-jobs|ci-jobs|hermes"}'` returns no streams.
- **41-AC-8** Loki runtime config shows `retention_period: 720h` and Tempo shows `block_retention: 336h` (`curl loki:3100/config | grep retention_period`; `curl tempo:3200/status/config | grep block_retention`).
- **41-AC-9** Trace path: after one fake execution, Tempo returns the trace by the task's `trace_id` (40-AC-13), and Grafana's trace-to-logs link from its `sc.execution` span opens Loki lines from `agent-runner` with the same `trace_id`.
- **41-AC-10** Alerts `SCLokiDown`, `SCTempoDown`, `SCLogAgentDown`, `SCLogIngestionErrors`, `SCTelemetryStorageWriteFailing`, `SCLokiCompactorFailing`, `SCTempoCompactorFailing` are loaded (42-AC-19 method).

### GATE-H04 (contribution)

- **41-AC-11** Drill: draining the node that hosts Loki and Tempo reschedules both within 5 min on another node (no PV pinning), and `{job="pods"}` ingestion resumes (`count_over_time` > 0 for the last 5 min).

## 24. Explicit non-goals

- Loki or Tempo in MVP (SPEC §127 "full Loki/Tempo retention" is a non-goal).
- Shipping worker, CI or Hermes container logs to Loki (ADR-0552).
- Loki/Tempo high availability or multi-tenancy.
- Log-based alerting in H03 (the `loki-rule` source kind is reserved; break-glass detection is metric-based).
- journald collection into Loki (k3s and node service logs stay on the node, bounded).
- Long-term log archives beyond 30 days.

## 25. Open questions

| # | Question | Blocking? | Proposed resolution | Owner |
|---|---|---|---|---|
| Q-41-1 | Worker logs archived in `evidence` contain harness stream content (assistant text, tool I/O), which is model content. | no | Interpretation: SPEC §94 restricts *general telemetry*; the archived worker log is execution evidence with restricted access and 90-day retention, never ingested into Loki, metrics or spans. Recorded for the security review (plan 34). | 34 / 26 / 27 |
| Q-41-2 | Loki and Tempo object-store path-prefix support on the pinned versions. | no | Verify at H03; fallback is dedicated buckets `<prefix>-telemetry-loki` and `<prefix>-telemetry-tempo` (plan 14 adds them; lifecycle per plan 43). | 41 / 14 |
| Q-41-3 | `kubelet_container_log_filesystem_used_bytes` availability for §9.6. | no | Verify; fallback is `sc-node-facts` measuring `/var/log/pods` growth (plan 07/08 textfile collector). | 41 / 08 |
| Q-41-4 | Plan 11 S-28 says Loki/Tempo WAL on local-path; this plan uses `emptyDir`. | no | Use `emptyDir` (reschedulable, best-effort data); plan 11 updates S-28 and its small-ha disk budget (8 GiB per node). | 11 |
| Q-41-5 | A "defer" outcome leaves SPEC §95's SHOULD unmet at small-ha. | no | Allowed by SPEC §95 ("unless measurements justify"); the expand PR proposes a spec-issues entry with the measured numbers. | 41 / lead |
