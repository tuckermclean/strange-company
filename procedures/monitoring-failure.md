# Procedure: Monitoring failure

**Owning plan:** plans/38-prometheus.md (with plans 39, 40, 41, 42)
**SPEC references:** §90–§96, §107, §108, §113 ("monitoring failure")
**Severity / urgency:** degraded (monitoring is best effort and never on the request path), **outage of detection** when Prometheus or Alertmanager is down — act within 1 h, because until then no alert reaches a human.
**Expected duration:** 10–45 min per section
**Requires break-glass:** no (operator kubeconfig through the toolbox). Use `break-glass-access.md` §Cluster only if the operator kubeconfig is unusable.

Monitoring, Grafana, the OpenTelemetry Collector, Loki and Tempo hold no authoritative data (plan 11 S-24…S-29). The fix for a corrupted store is always "recreate it empty"; never restore metrics or logs from anywhere.

## When to use

| Alert / symptom | Section |
|---|---|
| External heartbeat service reports "no ping" (dead-man's switch) | §A Triage |
| `SCMonitoringTargetDown`, `SCPlatformProbesDown` | §B Scrape targets |
| Prometheus pod CrashLoop / OOM (`SCPlatformPodOOMKilled{namespace="monitoring"}`), TSDB errors in logs | §C Prometheus |
| `SCPrometheusCardinalityHigh` | §D Cardinality |
| `SCPrometheusStorageNearCap`, `SCPrometheusRetentionShort` | §E Storage and retention |
| Alertmanager pod down, `AlertmanagerFailedReload`, `AlertmanagerMembersInconsistent` (upstream) | §F Alertmanager (then `notification-failure.md` for delivery problems) |
| `SCGrafanaDown`, Grafana panels erroring, datasource health failing | §G Grafana |
| `SCOTelCollectorDown`, `SCOTelCollectorRefusingData`, `SCTelemetryForbiddenAttributeDropped` | §H OpenTelemetry Collector |
| `SCLokiDown`, `SCTempoDown`, `SCLogAgentDown`, `SCLogIngestionErrors`, `SCTelemetryStorageWriteFailing`, `SCLokiCompactorFailing`, `SCTempoCompactorFailing` (small-ha) | §I Loki / Tempo / agent |
| `SCNodeFactsStale` | §J Node textfile collector |
| `SCPlatformJobFailed` in `monitoring` (e.g. `silence-audit`) | §K Monitoring jobs |
| `SCTelemetryDebugCaptureActive` lingering, debug objects past retention | §L Debug capture |

## Preconditions and required material

- Toolbox: `./bootstrap shell` (kubectl, flux, promtool, amtool, logcli are in the toolbox, plan 02).
- Operator kubeconfig (`/blackstart/<cluster>/kubeconfig`, SSH tunnel per plan 50).
- Aliases used below (define in the toolbox shell):
  ```sh
  PQ() { kubectl -n monitoring exec sts/prometheus-kps-prometheus -c prometheus -- promtool query instant http://localhost:9090 "$1"; }
  ```

## Safety checks before acting

1. Monitoring components are restartable without impact on the platform. Do **not** restart platform components (Forgejo, CNPG, Longhorn) to "fix" a monitoring symptom.
2. Never delete PVCs other than the Prometheus/Alertmanager ones named in this procedure.
3. Never add a scrape config, rule or Alertmanager route by `kubectl edit`; changes go through a PR (Flux reverts manual edits). Emergency exception: `flux suspend kustomization monitoring-config` to stop a broken rule rollout — always resume within 24 h (`SCFluxSuspendedTooLong` fires after 2 h).
4. If the whole node is down (self-hosting-minimum), this is not a monitoring problem: go to `lost-node.md`.

## Steps

### §A Triage (heartbeat lapsed or "nothing works")
1. Is the cluster reachable? `kubectl get nodes`. **Expected:** nodes `Ready`. If the API is unreachable → `lost-node.md` / `restore-etcd.md`.
2. `kubectl -n monitoring get pods -o wide; kubectl -n monitoring-node get pods -o wide`. **Expected:** all `Running`, restarts not increasing.
3. Is Watchdog firing and reaching Alertmanager? `PQ 'ALERTS{alertname="Watchdog"}'` → one series. Then `kubectl -n monitoring port-forward svc/kps-alertmanager 9093 &` and `amtool --alertmanager.url=http://127.0.0.1:9093 alert query alertname=Watchdog` → one alert.
4. Is the heartbeat being delivered? `PQ 'increase(alertmanager_notifications_total{receiver_name="heartbeat"}[10m])'` ≥ 8 and `PQ 'increase(alertmanager_notifications_failed_total{receiver_name="heartbeat"}[10m])'` = 0.
   - Prometheus down → §C. Alertmanager down → §F. Delivery failing → `notification-failure.md` §Heartbeat. Egress broken (all external deliveries fail, `probe_success{probe="github"} == 0`) → §A.5.
5. **Egress:** from a debug pod in `monitoring`: `kubectl -n monitoring run egress-test --rm -i --restart=Never --image=<curl image digest> -- curl -sS -m 10 -o /dev/null -w '%{http_code}' https://github.com/` → `200`/`301`. Otherwise check node firewall/DNS (`edge-ingress-failure.md`, plan 08/09).
6. Record the start time; continue at the identified section.

### §B Scrape targets
1. `PQ 'up == 0'` lists the failing jobs.
2. For each job: `kubectl -n monitoring get servicemonitor,podmonitor,probe -l strange.company/monitoring=true` contains its object (plan 38 §7.2). Missing → the `monitoring-config` layer failed: `flux get kustomizations monitoring-config`; fix via PR.
3. Target pod running? If the component is down, that is the component's incident (its procedure).
4. NetworkPolicy: `kubectl -n <target-ns> get networkpolicy` must allow Prometheus on the metrics port (plan 36). A NetworkPolicy change is a PR.
5. Auth: `forgejo` job down with HTTP 401/403 in `PQ 'scrape_samples_scraped{job="forgejo"}'`/Prometheus targets page → the `SC-FORGEJO-METRICS-TOKEN` copies differ: compare `sha256sum` of both decrypted SOPS copies (`forgejo` and `monitoring`); re-sync by PR (`rotate-secrets.md#sc-forgejo-metrics-token`).
6. **Expected result:** `PQ 'count(up == 0)'` empty within two scrape intervals after the fix.

### §C Prometheus
1. `kubectl -n monitoring logs sts/prometheus-kps-prometheus -c prometheus --tail=200`.
2. **OOMKilled:** check `PQ`-independent: `kubectl -n monitoring describe pod prometheus-kps-prometheus-0 | grep -A3 'Last State'`. Then §D (cardinality is the usual cause). Temporary relief only via PR raising the memory limit (plan 38 §12).
3. **TSDB corruption** (log lines `opening storage failed`, `corruption`, `invalid magic number`): data loss is accepted [SPEC §108].
   ```sh
   kubectl -n monitoring scale prometheus kps-prometheus --replicas=0 2>/dev/null || kubectl -n monitoring patch prometheus kps-prometheus --type merge -p '{"spec":{"replicas":0}}'
   kubectl -n monitoring delete pvc prometheus-kps-prometheus-db-prometheus-kps-prometheus-0
   kubectl -n monitoring patch prometheus kps-prometheus --type merge -p '{"spec":{"replicas":1}}'   # 2 at small-ha
   ```
   The operator object is Flux-managed; the replica patch is reverted by Flux at the next reconcile, which is the intended end state. **Expected:** pod `Running` within 5 min, `up{job="prometheus"} == 1`.
4. **Pinned to a lost node (small-ha):** the replica's local-path PV binds it to the dead node. If the node will not return within 1 h: delete that replica's PVC and pod; it reschedules empty on another node.
5. Rule evaluation failures (`PrometheusRuleFailures`): `PQ 'increase(prometheus_rule_evaluation_failures_total[30m]) > 0'` names the group; fix the rule by PR (plan 42 promtool tests must catch it next time).

### §D Cardinality
1. Top metric names: `PQ 'topk(10, count by (__name__) ({__name__=~".+"}))'`.
2. Top jobs: `PQ 'topk(10, count by (job) ({__name__=~".+"}))'`.
3. For an `sc_*` metric: find the offending label (`PQ 'count by (<label>) (<metric>)'`). A task/execution/commit identifier as a label is a defect of the emitter (plan 38 §21.1): open an issue against the owning plan; meanwhile add a `metricRelabelings` drop rule for that metric to its ServiceMonitor by PR.
4. Kube-native churn (`agent-jobs` pods): acceptable within plan 38 §7.6 bounds; if exceeded, add a relabel drop for `container_*` series in `agent-jobs` older detail.
5. **Expected:** `PQ 'max(prometheus_tsdb_head_series)'` back under 250 000 within 2 h (head compaction).

### §E Storage and retention
1. `PQ 'max(prometheus_tsdb_storage_blocks_bytes + prometheus_tsdb_wal_storage_size_bytes)'` vs `retentionSize` (plan 38 §9).
2. Effective retention: `PQ '(time() - min(prometheus_tsdb_lowest_timestamp_seconds)) / 86400'`. If < 8 days (`SCPrometheusRetentionShort`), the plan 33 anomaly baseline is incomplete: either reduce series (§D) or raise `retentionSize` within the plan 51 disk budget by PR. Never raise it beyond 80 % of the allocation.

### §F Alertmanager
1. `kubectl -n monitoring get pods -l app.kubernetes.io/name=alertmanager`; logs of the failing pod.
2. Config reload failing: `kubectl -n monitoring logs <am-pod> -c config-reloader --tail=50`. The last good config stays active. Validate the rendered config locally: `amtool check-config gitops/clusters/<c>/monitoring/alertmanager.yaml` (and `make render-alerts`); fix by PR.
3. Pod down on a lost node (small-ha): the surviving replica keeps delivering; delete the dead replica's PVC only if the node will not return.
4. After recovery: the Runner re-posts Needs Human alerts within 5 min, Prometheus re-sends rule alerts within 30 s. Silences may be lost: re-create them from the `silence-audit` evidence or memory.
5. Delivery problems with a healthy Alertmanager → `notification-failure.md`.

### §G Grafana
1. `kubectl -n monitoring get pods -l app.kubernetes.io/name=grafana`; `kubectl -n monitoring logs deploy/grafana -c grafana --tail=100`.
2. Grafana is stateless: `kubectl -n monitoring delete pod -l app.kubernetes.io/name=grafana`. **Expected:** dashboards back within 3 min (39-AC-8).
3. Datasource errors: log in (OIDC, or the break-glass form login) and use Connections → Data sources → <name> → Test (HTTP basic auth is disabled, plan 39 §9). Grafana has no Alertmanager datasource; the `sc-alerts` panels read `sc-pg` (`reporting.v_tasks_needing_human`). `sc-pg` failing → check `sc-pg` health (`cnpg-primary-loss.md`), then the `grafana_ro` password copies (`strange-company/sc-pg-grafana-ro` vs `monitoring/grafana-datasource-sc-pg`, compare sha256 of decrypted values); re-sync by PR.
4. OIDC login broken but Grafana up → `authentik-outage.md`; use the break-glass login (`break-glass-access.md` §Grafana) meanwhile.

### §H OpenTelemetry Collector
1. `kubectl -n monitoring logs deploy/otel-collector --tail=200`.
2. Refusing data: `PQ 'rate(otelcol_receiver_refused_spans_total[5m])'` and `PQ 'otelcol_process_memory_rss'`. Memory limiter active → an emitter is flooding: `PQ 'topk(5, sum by (service_name) (rate(sc_span_calls_total[5m])))'`; fix the emitter; Collector restart is safe (no state).
3. `SCTelemetryForbiddenAttributeDropped`: the redaction processor removed content. **Do not** relax the allowlist. Identify the emitter: temporarily (≤ 1 h) enable the debug exporter only in a test cluster, never in production; in production use `PQ 'sum by (service_name) (increase(sc_span_redactions_total[1h]))'` and the emitter's contract tests. Treat any secret-shaped value as exposed inside the emitter process: `credential-compromise.md`.
4. Work is never blocked by the Collector; there is no urgency beyond restoring visibility.

### §I Loki / Tempo / log agent (small-ha only)
1. `kubectl -n monitoring get pods -l 'app.kubernetes.io/name in (loki,tempo)'`; `kubectl -n monitoring-node get ds otel-agent`.
2. S3 write errors (`SCTelemetryStorageWriteFailing`): check `SCObjectStorageUnreachable` first (`object-storage-outage.md`); then the `SC-OBJECT-STORAGE/telemetry` credential (`rotate-storage-credentials.md`).
3. Ingestion limits (`SCLogIngestionErrors`): `logcli` top streams `logcli series --analyze-labels '{job="pods"}'`; fix the noisy component or raise limits by PR (plan 41 §8.2).
4. Compactor failing: logs of the Loki/Tempo pod for `compactor`/`retention`; the bucket lifecycle backstop (35 d / 21 d) still applies.
5. Pod pinned or stuck: Loki and Tempo use `emptyDir`; `kubectl delete pod` reschedules them (≤ 2 h of unflushed data lost — accepted).

### §J Node textfile collector (`SCNodeFactsStale`)
1. `./bootstrap exec -- ssh <node> systemctl status sc-node-facts.timer sc-node-facts.service`.
2. Run once: `systemctl start sc-node-facts.service`; check `/var/lib/node-exporter/textfile/*.prom` mtime. If the script fails, re-run `./bootstrap configure --tags baseline` (plan 07/08).

### §K Monitoring jobs
`kubectl -n monitoring get jobs`; logs of the failed Job. `silence-audit` failing means a silence violates policy (`notification-failure.md` §Silences) or the Alertmanager API is unreachable (§F).

### §L Debug capture
1. Active grants: `ls policy/telemetry-debug/` on `main`; expired grants are ignored automatically. To end capture early, revert the grant by PR (protected path, human merge).
2. Objects past retention: `aws s3 ls s3://<prefix>-evidence/telemetry-debug/ --recursive` (toolbox, evidence credential); delete objects older than their grant's `retentionDays` and investigate the sweeper (plan 40 §9.5).

## Verification

- `PQ 'count(up == 0)'` → empty (excluding jobs whose component is intentionally absent).
- `PQ 'ALERTS{alertname="Watchdog"}'` → 1 series; heartbeat service shows a ping within the last 2 min.
- `./bootstrap verify --only V-22,V-23,V-24` → `pass`.
- At small-ha: 2 Prometheus and 2 Alertmanager pods on different nodes.

## Rollback / abort

All steps are restarts, PVC recreation of non-authoritative stores, or PR reverts. Abort by stopping; Flux returns every object to its Git state. If a suspended Kustomization was used, `flux resume kustomization <name>`.

## Evidence to record

Issue in Forgejo (`ops` label) with: alert names and start/end times, root cause, commands run, whether data was discarded (TSDB, WAL), and the PR numbers of any fix.

## Follow-ups

- Recurring OOM or cardinality → plan 38 sizing review.
- Any forbidden-attribute redaction → fix the emitter and add a contract test case.
- Heartbeat gaps not explained by an outage → `notification-failure.md` §Heartbeat review with the heartbeat provider.
