# 38 — Prometheus (metrics, scraping, cardinality, metric catalogue)

**Status:** Stage-0 plan (draft)
**DAG nodes:** P09 (primary, with 39, 40, 42); contributes to C11 (SC metric catalogue live), P12 (metrics visible), H04 (HA Prometheus)
**Related plans:** 07 (k3s), 08 (node baseline), 12 (CNPG), 13 (Longhorn), 15 (Authentik), 16 (Forgejo), 17 (mirror), 18 (Flux), 20 (Woodpecker), 24 (Hermes), 26 (Runner), 32 (cost), 33 (budgets), 35 (secrets), 36 (netpol), 39 (Grafana), 40 (OTel), 42 (alerting), 43 (retention), 44 (backup), 51 (capacity), 53 (Gateway)
**Contracts:** consumes `alert-catalog.yaml` (plan 42), `telemetry.schema.yaml` (plan 40). This plan is authoritative for the **Strange Company metric catalogue** (§21.2).
**ADRs:** ADR-0009, ADR-0014, ADR-0015, ADR-0020; introduces **ADR-0550** (central monitoring layers + `monitoring-node`)

## 1. Purpose

Provide the metrics backbone required by the Self-Hosting Threshold [SPEC §117]. It consists of kube-prometheus-stack (Prometheus Operator, Prometheus, Alertmanager, node-exporter and kube-state-metrics), a blackbox exporter, and a scrape object for every SPEC §91 source. Low-cardinality Strange Company metrics come from the Runner, the Gateway and the cost and budget subsystems. A cardinality policy, enforced in CI, keeps Prometheus from being "abused as a high-cardinality task ledger" [SPEC §90].

## 2. Requirements derived from SPEC.md

1. Prometheus is the metrics system and is required at the Self-Hosting Threshold [SPEC §20, §90, §117, §126].
2. Collect useful metrics from nodes, Kubernetes, etcd, CNPG, Longhorn, Forgejo, Authentik, Flux, Woodpecker, Hermes, Agent Runner, workers and model-cost aggregates [SPEC §91].
3. Task IDs and similarly unbounded identifiers SHOULD NOT become metric labels [SPEC §91]. Exact accounting and task events live in CNPG [SPEC §90].
4. No Bitnami dependencies [SPEC §20].
5. Retention of Prometheus data is intentional, and root disks must not fill [SPEC §107].
6. Historical Prometheus metrics are best effort for RPO/RTO [SPEC §108].
7. Alerting must be possible for every SPEC §96 condition. This plan supplies the signals and plan 42 the rules [SPEC §96].
8. Infrastructure outranks agents in scheduling. Monitoring runs at `platform` priority [SPEC §89].
9. Version pinning and immutable images [SPEC §37, §116].
- **38-R-1** Every scrape object lives in the central path `gitops/platform/monitoring/scrape/`, applied by plan 18 layer `monitoring-config` (ADR-0550 as amended). Component layers never carry Prometheus Operator CRs.
- **38-R-2** Every Strange Company–authored metric is in the catalogue (§21.2) and passes `make lint-metrics` (§22).
- **38-R-3** Prometheus storage is capped by **both** time and size. On local-path, size is the only real cap (local-path does not enforce PVC capacity (verify)).
- **38-R-7** Effective retention is at least **8 days** on every profile (plan 33 §14.8 anomaly baseline). `retentionSize` is sized so that the size cap does not bind before 8 days at the 24 h head-series measurement (38-AC-12); `SCPrometheusRetentionShort` (plan 42) fires otherwise.
- **38-R-4** At small-ha, monitoring survives the loss of any single node. Prometheus and Alertmanager run with 2 replicas each (§12).
- **38-R-5** Short-lived workers are never scraped directly. Worker metrics are Runner aggregates plus kube-native cAdvisor/KSM series (§7.6).
- **38-R-6** Public endpoints are probed *through the edge*, and internal endpoints in-cluster (§7.8).

## 3. Inputs

- Profile (`self-hosting-minimum` | `small-ha`) and node disk class from the cluster config (`gitops/clusters/<cluster>/config/cluster.yaml`, plans 03 and 50).
- Disk budget for monitoring from plan 51: this plan **requests** a 10 GiB local-path allocation per Prometheus replica (8 GiB on 75 GiB nodes), as in §11.
- Metrics endpoints declared by component plans (§7.2 table: port, path, auth).
- `BASE_DOMAIN` and public hostnames (master plan §6.1) for public probes.
- Object-storage endpoint URL (plan 14) for the reachability probe.
- Scrape token `SC-FORGEJO-METRICS-TOKEN` (§10). Woodpecker needs none (dedicated `:9001` listener, plan 20).

## 4. Outputs

- HelmReleases: `prometheus-operator-crds`, `kube-prometheus-stack` (release name `kube-prometheus-stack`, `fullnameOverride: kps`), and `prometheus-blackbox-exporter` twice (`fullnameOverride: blackbox` and `blackbox-public`, §7.8), all in `monitoring`. node-exporter is delivered by the kube-prometheus-stack subchart into `monitoring-node`.
- Services: `kps-prometheus.monitoring:9090`, `kps-alertmanager.monitoring:9093`, `prometheus-operated`, `alertmanager-operated` (exact names re-verified, 38-AC-3).
- Scrape objects under `gitops/platform/monitoring/scrape/` (§7.2).
- The metric catalogue artifact `observability/metric-catalog.yaml`, generated at implementation from §21.2 and consumed by `make lint-metrics` and the Grafana dashboard lint (plan 39).
- Static info metrics rendered from config: `sc_prometheus_retention_size_bytes`, `sc:etcd_expected_members` (rules in plan 42).

## 5. Dependencies

**Hard (DAG):** P06 (Flux sources Forgejo, so monitoring arrives through GitOps after control transfer) and P07 (Authentik, for Grafana OIDC in the same gate). P09 is co-owned with 39, 40 and 42.
**Soft/runtime:**
- Plan 07: k3s flags `etcd-expose-metrics: true` (verify) and the kubelet/apiserver default scrape. Plan 07 must **not** bind controller-manager or scheduler to non-loopback addresses (§7.3).
- Plan 08: host firewall allows TCP 9100 (node-exporter, hostNetwork) and TCP 2381 (etcd metrics) **from the pod CIDR and peer node IPs only**, never from public interfaces.
- Plan 36: NetworkPolicies allowing `monitoring/prometheus` → each metrics port in §7.2, `blackbox` egress to the internet on 443 (public probes and object storage), and Runner → Alertmanager 9093.
- Plan 51: PriorityClass `platform` for every monitoring pod, `platform-critical` for none, and the disk budget above.
- Plans 12, 13, 15, 16, 17, 18, 20, 24, 26, 32, 33, 53: the endpoints and metric names in §7.2 and §21.2.

## 6. Owned state

| State | Location | Writer | Criticality |
|---|---|---|---|
| Prometheus TSDB | local-path PVC `prometheus-kps-prometheus-db-prometheus-kps-prometheus-<n>` (per replica) | Prometheus | Non-critical, best effort [SPEC §108]. Not backed up. |
| Alertmanager silences + notification log | local-path PVC 1 GiB per replica (`alertmanager-kps-alertmanager-db-…`) | Alertmanager | Low. Silences lost on node loss are acceptable. Active alerts are re-sent by Prometheus and re-posted by the Runner (plan 42). |
| Scrape and recording config | Git (`gitops/platform/monitoring/**`) | PR + human merge | Authoritative desired state |

No Strange Company business state is owned here. Exact values (spend, task history) are in `sc-pg` [SPEC §90].

## 7. External interfaces

### 7.1 Chart and version selection

| Component | Chart | Selection rule |
|---|---|---|
| CRDs | `prometheus-community/prometheus-operator-crds` | Same operator version as the stack chart's appVersion |
| Stack | `prometheus-community/kube-prometheus-stack` | Latest chart minor whose Prometheus Operator supports the k3s minor pinned by plan 07. Pinned in the version lock (plan 01). Upgraded only by Renovate PR. |
| Blackbox | `prometheus-community/prometheus-blackbox-exporter` | Latest stable at implementation |
Images are digest-pinned, from upstream registries (quay.io/prometheus, registry.k8s.io/kube-state-metrics). No Bitnami (F11 lint).

Stack values (decisions):
- `crds.enabled: false` (CRDs come from the separate release, so CRD upgrades are explicit).
- `grafana.enabled: false`, `grafana.forceDeployDashboards: true`, `grafana.forceDeployDatasources: false` (verify key names). The stack's upstream dashboards are rendered as ConfigMaps for plan 39's sidecar, and the datasource is provisioned by plan 39.
- `prometheusOperator.admissionWebhooks.enabled: false`. Rule validity is enforced in CI with `promtool` (plan 42 §22). This avoids a fail-closed webhook on the API path and the patch Jobs.
- `prometheus.prometheusSpec.serviceMonitorSelectorNilUsesHelmValues: false`, same for `podMonitor`, `probe`, `rule`. Selectors are explicit: `matchLabels: {strange.company/monitoring: "true"}` on monitors, probes and rules. Monitors outside the central layer are ignored.
- `serviceMonitorNamespaceSelector` / `ruleNamespaceSelector`: `{matchLabels: {kubernetes.io/metadata.name: monitoring}}`. All CRs live in `monitoring`.
- `kubeControllerManager.enabled: false`, `kubeScheduler.enabled: false`, `kubeProxy.enabled: false` (§7.3).
- `kubeEtcd.enabled: true`, `kubeEtcd.endpoints: [<server node IPs>]`, `kubeEtcd.service.port: 2381`, `targetPort: 2381`, scheme `http` (verify).
- `coreDns.enabled: true` (k3s CoreDNS Service `kube-dns` in `kube-system`, port 9153, verify the label selector `k8s-app: kube-dns`).
- `kubelet.serviceMonitor.cAdvisor: true`, `probes: false` (the probes endpoint adds series with little value), `resource: false`.
- `kube-state-metrics`: `metricLabelsAllowlist` limited to `cronjobs=[strange.company/check]`, `namespaces=[strange.company/tier]`. No pod label exposure, so execution IDs never enter via labels. `customResourceState` enabled for the CRDs in §7.5.
- `nodeExporter`/`prometheus-node-exporter`: `namespaceOverride: monitoring-node` (verify key), hostNetwork/hostPID true (upstream default), `--collector.filesystem.mount-points-exclude` default plus `^/var/lib/kubelet/pods/.+` and `^/run/k3s/.+`.
- `defaultRules`: groups and disabled alerts exactly as `contracts/alert-catalog.yaml` `spec.upstreamRules`. `defaultRules.additionalRuleLabels: {source: upstream}` (verify key). `defaultRules.appNamespacesTarget` is set to the platform namespaces regex (plan 42 §8.2), which excludes `agent-jobs`, `ci-jobs` and `apps`.

### 7.2 Scrape inventory (every SPEC §91 source)

All objects carry label `strange.company/monitoring: "true"` and set an explicit `job` via `relabelings: [{targetLabel: job, replacement: <job>}]`. Default `interval: 30s`, `scrapeTimeout: 10s`.

| SPEC §91 source | Object | Target (ns/svc:port path) | Auth | `job` | Interval | Endpoint owner |
|---|---|---|---|---|---|---|
| nodes | node-exporter SM (stack) | monitoring-node/node-exporter:9100 `/metrics` | none (hostNetwork; firewall) | `node-exporter` | 30s | 38 |
| Kubernetes (kubelet, cAdvisor) | stack SM | kube-system kubelet 10250 `/metrics`, `/metrics/cadvisor` | SA token, TLS insecureSkipVerify (k3s kubelet cert) | `kubelet` | 30s | 07 |
| Kubernetes API (+ embedded controller-manager/scheduler registry) | stack SM | default/kubernetes:443 | SA token | `apiserver` | 30s | 07 |
| Kubernetes objects | kube-state-metrics SM | monitoring/kps-kube-state-metrics:8080 | none | `kube-state-metrics` | 30s | 38 |
| etcd | stack SM `kubeEtcd` | server node IPs :2381 `/metrics` | none, http (verify) | `kube-etcd` | 30s | 07 |
| CoreDNS | stack SM | kube-system/kube-dns:9153 | none | `coredns` | 60s | 07 |
| CNPG instances (4 clusters) | PodMonitor `cnpg-instances` | pods `cnpg.io/podRole=instance` in forgejo, authentik, woodpecker, strange-company, port `metrics` 9187 | none (in-cluster) | `cnpg` | 30s | 12 |
| CNPG operator | PodMonitor `cnpg-operator` | cnpg-system operator pod :8080 | none | `cnpg-operator` | 60s | 12 |
| CNPG barman-cloud plugin | PodMonitor (if the plugin exposes metrics, verify) | plugin sidecar port | none | `cnpg-plugin` | 60s | 12 |
| Longhorn | SM | longhorn-system/longhorn-backend:9500 `/metrics` | none | `longhorn` | 60s | 13 |
| Forgejo | SM | forgejo/forgejo-http:3000 `/metrics` | bearer `SC-FORGEJO-METRICS-TOKEN` (`[metrics] ENABLED=true, TOKEN=…`) | `forgejo` | 60s | 16 |
| Authentik | SM | authentik/authentik-server-metrics:9300 `/metrics` (verify) | none | `authentik` | 60s | 15 |
| Flux controllers | PodMonitor | flux-system pods `app in (source-controller,kustomize-controller,helm-controller,notification-controller)` port `http-prom` 8080 | none | `flux` | 30s | 18 |
| Flux object readiness | KSM customResourceState → `gotk_resource_info` | — | — | `kube-state-metrics` | — | 18/38 |
| Woodpecker | SM | woodpecker/woodpecker-server:9001 `/metrics` (dedicated metrics listener, plan 20) | none (NetworkPolicy admits only Prometheus) | `woodpecker` | 60s | 20 |
| Hermes | Probe (blackbox) | hermes/hermes:8642 health (verify path), :9119 dashboard | — | `blackbox` | 60s | 24 |
| Agent Runner | SM | strange-company/agent-runner, port name `metrics` (Runner 8090, CD-02) `/metrics` | none | `agent-runner` | 30s | 26 |
| Model Gateway (model-cost aggregates) | SM | strange-company/model-gateway, port name `metrics` (plan 53: :9090) `/metrics` | none | `model-gateway` | 30s | 53 |
| Workers | Runner aggregates + cAdvisor/KSM | — (§7.6) | — | — | — | 26 |
| Cost/budget aggregates | exposed by Gateway (§21.2) | model-gateway port `metrics` | — | `model-gateway` | 30s | 32/33 |
| GitHub mirror freshness | SM on plan 17's mirror-watch endpoint | `forgejo/github-mirror-watch:9464` (assumed; plan 17 names it) | none | `github-mirror` | 60s | 17 |
| cert-manager | SM | cert-manager/cert-manager:9402 | none | `cert-manager` | 60s | 10 |
| `sc-ci-config` (Woodpecker config extension) | SM | woodpecker/sc-ci-config port `metrics` | none | `sc-ci-config` | 60s | 20 |
| `platform-probes` (break-glass, OIDC, forge drift, mirror, CI lane probes) | SM | monitoring/platform-probes port `metrics` | none | `platform-probes` | 60s | 15/16/17 |
| egress proxy (C01+) | SM | port `metrics` of `egress-proxy` | none | `egress-proxy` | 30s | 36 |
| Traefik | SM | ingress/traefik-metrics:9100 (verify) | none | `traefik` | 30s | 09 |
| cloudflared | PodMonitor | ingress pods `app=cloudflared` port 2000 `/metrics` (verify `--metrics`) | none | `cloudflared` | 30s | 09 |
| OTel Collector | SM | monitoring/otel-collector:8888 (self) and :8889 (spanmetrics) | none | `otel-collector` | 30s | 40 |
| Grafana | SM | monitoring/grafana:3000 `/metrics` | none (NetworkPolicy) | `grafana` | 60s | 39 |
| Prometheus, Alertmanager, operator | stack SMs | — | — | `prometheus`, `alertmanager`, `prometheus-operator` | 30s | 38 |
| Loki, Tempo, otel-agent (H03) | SMs | monitoring/loki:3100, tempo:3200, monitoring-node/otel-agent:8888 | none | `loki`, `tempo`, `otel-agent` | 60s | 41 |

Every Strange Company service exposes metrics on a **dedicated container port named `metrics`, path `/metrics`, unauthenticated**. ServiceMonitors select the port by *name*, so the number is the emitter plan's choice (CD-02: Runner 8090, Gateway 9090, egress-proxy 9090; plan 17 mirror-watch 9464). The port serves no mutating or secret-bearing API. One read-only exception is allowed: the Model Gateway's `GET /sc/v1/status` (effective e-stop and budget status, no secrets) on its 9090 `metrics` listener, which the Runner reads (plan 53, plan 36 row SC-5). NetworkPolicy then admits Prometheus **and** the Runner to that port, and nothing else. NetworkPolicy admits only pods labeled `app.kubernetes.io/name=prometheus` from `monitoring` (plan 36). This is an interface requirement on plans 26, 53, 17 and 54.

### 7.3 k3s control-plane metrics (decision)

k3s runs the apiserver, controller-manager, scheduler and kube-proxy in one process with a **shared metrics registry**. Every component endpoint returns all components' metrics, so scraping several endpoints duplicates series (observed upstream; verify on the pinned k3s version). Decision:
- Scrape **only** the apiserver (`job=apiserver`) and the kubelet. Controller-manager and scheduler series (e.g. `workqueue_*`, `scheduler_schedule_attempts_total`) are expected to appear under `job=apiserver`. 38-AC-6 verifies this. If they are absent, the fallback is plan 07 adding `kube-controller-manager-arg: bind-address=<node-ip>` / `kube-scheduler-arg: bind-address=<node-ip>`, the firewall restricting 10257/10259 to the pod CIDR, and enabling the stack ServiceMonitors with `insecureSkipVerify`. That fallback needs a plan-07 amendment.
- etcd: plan 07 sets `etcd-expose-metrics: true` in `/etc/rancher/k3s/config.yaml` on every server. Expected effect: etcd serves `/metrics` over plain HTTP on `:2381` on the node address (verify; 38-AC-5). Port 2381 is firewall-restricted (plan 08).
- kube-proxy is not scraped (low value, duplicate registry).

### 7.4 Recording rules for kube-native aggregation

Plan 42 owns every rule file. This plan specifies the inputs it guarantees: the `cnpg_*`, `longhorn_*`, `etcd_*`, `kube_*`, `gotk_resource_info`, `sc_*`, `probe_*`, `certmanager_*` and `woodpecker_*` series named in `contracts/alert-catalog.yaml`.

### 7.5 kube-state-metrics custom resource state

KSM `customResourceState` config (in stack values) generates:

| Metric | CRD | Labels (only these) | Value |
|---|---|---|---|
| `gotk_resource_info` | Kustomization, HelmRelease, GitRepository, Bucket, HelmRepository, OCIRepository (flux) | `customresource_kind`, `exported_namespace`, `name`, `ready`, `suspended`, `reason` (Ready condition reason, a bounded Flux enum; needed by `SCSopsDecryptionFailing`) | 1 |
| `sc_cnpg_backup_info` | `backups.postgresql.cnpg.io` | `namespace`, `cluster`, `phase`, `method` | 1 |
| `sc_cnpg_backup_stopped_timestamp_seconds` | same | `namespace`, `cluster`, `phase` | `status.stoppedAt` |
| `sc_cnpg_cluster_instances` | `clusters.postgresql.cnpg.io` | `namespace`, `cluster` | `spec.instances` |
| `sc_cnpg_cluster_ready_instances` | same | `namespace`, `cluster` | `status.readyInstances` |
| `sc_etcd_snapshot_info` | `etcdsnapshotfiles.k3s.cattle.io` (verify CRD on the pinned k3s) | `location_kind` (`s3`/`local`), `ready` | `status.creationTime` as timestamp |
| `sc_longhorn_backup_info` | `backups.longhorn.io` | `volume`, `state` | `status.backupCreatedAt` as timestamp |
| `sc_cnpg_certificate_expiration_timestamp_seconds` | `clusters.postgresql.cnpg.io` | `namespace`, `cluster`, `secret` (`<cluster>-ca`, `-server`, `-replication`) | `status.certificates.expirations[<secret>]` parsed as timestamp (verify field; plan 10 `SCCnpgCertificateExpiringSoon`) |
| `sc_longhorn_volume_number_of_replicas` | `volumes.longhorn.io` | `volume`, `state_id` (from label `strange.company/state-id`, bounded by the register) | `spec.numberOfReplicas` (plan 47 `SCLonghornReplicaCountBelowProfile`) |
| `sc_longhorn_backup_target_available` | `backuptargets.longhorn.io` (verify CRD; Longhorn ≥ 1.6) | `name` | `status.available` (1/0) (plan 13 `SCLonghornBackupTargetUnavailable`) |
| `sc_longhorn_backup_max_age_seconds` | `volumes.longhorn.io` | `volume` | value of annotation `strange.company/backup-max-age-seconds` on important volumes (93600 = 26 h); feeds `SCLonghornBackupStale` |

Flux's upstream example includes a `revision` label. It is **deliberately omitted** here, because commit SHAs are forbidden as labels (§21.1). The CNPG `Backup` and Longhorn `Backup` objects carry per-backup names, and those are **not** exported as labels. Aggregation is by cluster/volume only, which may require KSM `labelsFromPath` restricted to the listed paths. Plan 12 may instead supply the barman-cloud plugin's native last-backup metric. Plan 42's recording rule `sc:cnpg_last_backup_success_timestamp_seconds` accepts either source (§21.3).

### 7.6 Workers (decision)

Workers are Jobs that live minutes. They are **not scraped directly**: they have no stable endpoint, and the scrape interval would miss most of their lifetime. Worker visibility comes from:
1. **Runner aggregates** (§21.2): `sc_runner_executions`, `sc_runner_execution_duration_seconds`, `sc_runner_results_total`, `sc_runner_queue_depth`.
2. **kube-native resource series** for namespace `agent-jobs`: cAdvisor `container_memory_working_set_bytes`, `container_cpu_usage_seconds_total` and KSM `kube_pod_container_resource_limits`. Their `pod` label embeds the execution ID. That is tolerated (kube-native, churn-bounded: about 50 executions/day × ~15 series × 15 days ≈ 11k series), but such series are **never** used in Strange Company recording rules except through namespace-level aggregation (`sum by (namespace)`).
3. **Span metrics** derived by plan 40 from `sc-worker` spans (`sc_span_calls_total{span_name=…, sc_role, sc_harness}`).
Workers do not push to Prometheus. There is no Pushgateway (ADR-0553).

### 7.7 Prometheus API consumers

- Grafana (plan 39) reads Prometheus at `http://kps-prometheus.monitoring.svc:9090`.
- Nothing else reads Prometheus. Prometheus and Alertmanager UIs are not public (master plan §6.1). Human access is by `./bootstrap exec -- kubectl -n monitoring port-forward svc/kps-prometheus 9090` (plan 50 CLI form).

### 7.8 Blackbox probes

Two blackbox exporter releases share the module file: `blackbox` (in-cluster targets, normal cluster DNS) and **`blackbox-public`** (targets with `probe_kind: public` and `external`) with `dnsPolicy: None` and `dnsConfig.nameservers: [1.1.1.1, 9.9.9.9]`, so public hostnames resolve through public DNS and the probe really crosses the edge instead of plan 09's in-cluster DNS rewrite to Traefik. Probes with `probe_kind: public|external` use `prober.url: blackbox-public.monitoring.svc:9115`.

Modules (blackbox config, Git): `http_2xx` (status 200–299, IPv4 preferred, `fail_if_ssl: false`), `http_2xx_tls` (TLS verify on, `fail_if_not_ssl: true`), `http_any_tls` (TLS verify on, `valid_status_codes: [200,301,302,400,401,403,404]`), and `ssh_banner` (tcp, `query_response: [{expect: "^SSH-2.0-"}]`).

| Probe CR | `probe` label | `probe_kind` | Target | Module | Interval |
|---|---|---|---|---|---|
| forgejo-http | forgejo-http | internal | `http://forgejo-http.forgejo.svc:3000/api/healthz` | http_2xx | 30s |
| forgejo-ssh | forgejo-ssh | internal | `forgejo-ssh.forgejo.svc:22` | ssh_banner | 60s |
| authentik | authentik | internal | `http://authentik-server.authentik.svc:9000/-/health/live/` (verify) | http_2xx | 30s |
| woodpecker | woodpecker | internal | `http://woodpecker-server.woodpecker.svc:8000/healthz` (verify) | http_2xx | 30s |
| hermes-gateway | hermes-gateway | internal | `http://hermes.hermes.svc:8642/health` (verify; fallback tcp_connect) | http_2xx | 60s |
| agent-runner | agent-runner | internal | `http://agent-runner.strange-company.svc:8090/readyz` (ops listener, port name `metrics`; 8080 is the loopback-only admin API, GC-03) | http_2xx | 30s |
| model-gateway | model-gateway | internal | `http://model-gateway.strange-company.svc:9090/readyz` (ops listener, port name `metrics`; plan 53 §9 `listen.ops`) | http_2xx | 30s |
| grafana | grafana | internal | `http://grafana.monitoring.svc:3000/api/health` | http_2xx | 60s |
| object-storage | object-storage | internal | `https://<S3 endpoint host>/` (no credentials; reachability + TLS) | http_any_tls | 60s |
| public-git | public-git | public | `https://git.<BASE_DOMAIN>/api/healthz` | http_2xx_tls | 60s |
| public-auth | public-auth | public | `https://auth.<BASE_DOMAIN>/-/health/live/` | http_2xx_tls | 60s |
| public-ci | public-ci | public | `https://ci.<BASE_DOMAIN>/healthz` | http_2xx_tls | 60s |
| public-grafana | public-grafana | public | `https://grafana.<BASE_DOMAIN>/api/health` | http_2xx_tls | 60s |
| public-hermes | public-hermes | public | `https://hermes.<BASE_DOMAIN>/` | http_any_tls | 60s |
| github | github | external | `https://github.com/` | http_any_tls | 300s |

**NetworkPolicy rows these probes need (plan 36, GC-03):** plan 36 MO-8 allows `blackbox` only to Forgejo, Hermes, CoreDNS and the Internet. Required additions: `monitoring/blackbox` → `authentik` server 9000, `woodpecker` server 8000, `monitoring` Grafana 3000, `strange-company` agent-runner port `metrics` (8090) and model-gateway port `metrics` (9090). 38-AC-21 checks every Probe CR target against the plan 36 matrix statically.

Public probes leave through the node's internet egress and return through the edge (Cloudflare Tunnel or direct ingress). They prove the full public path except an *external vantage point*, which the operator's heartbeat service supplies (plan 42 §7.6). The `probe_ssl_earliest_cert_expiry` series from public probes feeds `SCCertificateExpiryImminent` for edge certificates that cert-manager does not manage.

## 8. Internal interfaces

- Prometheus → Alertmanager: `alertmanagers` configured by the stack (service `kps-alertmanager:9093`, v2 API).
- Prometheus Operator watches only namespace `monitoring` for CRs with label `strange.company/monitoring: "true"`.
- Config reloaders: the operator's reloader sidecars, with no manual restarts.

## 9. Configuration

| Key (Git path) | Default (minimum / small-ha) | Changed by |
|---|---|---|
| `gitops/platform/monitoring/stack/values.yaml` `prometheus.prometheusSpec.retention` | `15d` / `15d` (`10d` on 75 GiB nodes) | PR |
| `…retentionSize` | `8GiB` / `8GiB` (`5GiB` on 75 GiB nodes) | PR; must be ≤ 80% of the plan-51 monitoring disk allocation |
| `…storageSpec.volumeClaimTemplate` | `local-path`, `10Gi` (`6Gi` on 75 GiB) | PR |
| `…replicas` | `1` / `2` (podAntiAffinity hard on `kubernetes.io/hostname`) | profile overlay |
| `…scrapeInterval` / `evaluationInterval` | `30s` / `30s` | PR |
| `…walCompression` | `true` | — |
| `…enableFeatures` | none | PR |
| `…externalLabels` | `{installation: <install-id>}` | bootstrap-rendered |
| `alertmanager.alertmanagerSpec.replicas` | `1` / `2` | profile overlay |
| `alertmanager.alertmanagerSpec.retention` | `120h` | — |
| `alertmanager.alertmanagerSpec.storage` | local-path `1Gi` | — |
| `alertmanager.alertmanagerSpec.secrets` | `[alertmanager-transports]` (plan 42) | — |
| `gitops/platform/monitoring/scrape/*.yaml` | §7.2 | PR |
| `gitops/platform/monitoring/blackbox/values.yaml` | §7.8 modules | PR |
| `gitops/clusters/<cluster>/config/monitoring.yaml` (`kind: MonitoringConfig`, rendered at bootstrap) | `objectStorageProbeUrl`, `publicHostnames[]`, `etcdServerIPs[]`, `profile` | bootstrap / `./bootstrap expand` |
| Cardinality budget | `250000` head series (alert `SCPrometheusCardinalityHigh`) | PR |

Flux layering (plan 18 §8.3 names, authoritative): `crds` (seed set; HelmRelease `prometheus-operator-crds`) → `monitoring` (≙ ADR-0550 `monitoring-stack`: `kube-prometheus-stack`, both blackbox exporters, Grafana, OTel Collector) → `monitoring-config` (≙ ADR-0550 `monitoring-scrape` + `monitoring-rules`: paths `gitops/platform/monitoring/scrape/` and `…/rules/`, `dependsOn: monitoring`, `wait: true`). Only `crds` is in the seed set; the monitoring layers are reconciled after control transfer and before `verify` (P09 precedes P12).

## 10. Secrets

| Class | Use here | Notes |
|---|---|---|
| `SC-FORGEJO-METRICS-TOKEN` (registered by plan 35, owner plan 16) | Bearer token Prometheus presents to Forgejo `/metrics` | SOPS copies in `forgejo` and `monitoring` (the Secret must be co-located with the ServiceMonitor). Rotation: both copies in one PR; expected scrape gap ≤ 2 min. |
| `SC-NOTIFY-TRANSPORT` | Mounted into Alertmanager (plan 42) | Defined in plan 42 |

Prometheus's own ServiceAccount token (kubelet and apiserver scrape) is a projected token, not a secret class.

## 11. Storage

- Prometheus: local-path PVC per replica (non-replicated, node-pinned). Size and retention per §9. Longhorn is **not** used: metrics are a reconstructible cache-like signal [SPEC §27, §108].
- local-path does not enforce capacity (verify). `retentionSize` is the enforcing cap, and it covers blocks and WAL in current Prometheus versions (verify; 38-AC-8). Headroom: 8 GiB cap within a 10 GiB budget.
- Sizing basis: target ≤ 120k head series at self-hosting-minimum, ~1.5–2 bytes/sample after compression, 30 s interval: 120k × 2880 × 15 d × 1.7 B ≈ 8.8 GB. The 8 GiB size cap therefore usually binds before 15 days. `SCPrometheusStorageNearCap` (silent) marks it and the Grafana monitoring-health dashboard shows the effective retention. Plan 43 records Prometheus retention as "time 15 d, size cap binding".
- Alertmanager: 1 GiB local-path per replica.

## 12. Resource requirements

PriorityClass `platform` for all. Requests are what plan 51 budgets.

| Workload | Requests (cpu/mem) | Limits (mem) | min replicas | small-ha replicas |
|---|---|---|---|---|
| prometheus (+ config-reloader) | 200m / 1.0 GiB | 2.0 GiB | 1 | 2 |
| alertmanager | 20m / 64 MiB | 128 MiB | 1 | 2 |
| prometheus-operator | 50m / 96 MiB | 256 MiB | 1 | 1 |
| kube-state-metrics | 20m / 96 MiB | 192 MiB | 1 | 1 |
| node-exporter (per node) | 20m / 32 MiB | 64 MiB | DaemonSet | DaemonSet |
| blackbox-exporter (×2: `blackbox`, `blackbox-public`) | 10m / 32 MiB each | 64 MiB | 1 + 1 | 1 + 1 |
| **Total (minimum)** | ≈ 320m / ≈ 1.35 GiB | | | |

CPU limits are not set (avoids throttling), and memory request is < limit for Prometheus only (compaction spikes). On 8 GiB `constrained` nodes (ADR-0020): Prometheus request 768 MiB, limit 1.5 GiB, retentionSize 5 GiB. P12's memory-budget gate measures the real working set.

## 13. Security boundary

- Namespace `monitoring`: PSA `baseline` (enforce), `restricted` audit/warn. `monitoring-node`: PSA `privileged`, holding only node-exporter (and otel-agent at H03) (ADR-0550). Both carry tier label `platform`.
- RBAC: Prometheus ClusterRole read-only on nodes, nodes/metrics, services, endpoints, endpointslices, pods; `nonResourceURLs: [/metrics, /metrics/cadvisor]`. KSM ClusterRole is list/watch on core objects plus the CRDs in §7.5 (read-only). No write rights outside `monitoring`.
- Prometheus cannot read Secrets except those referenced by ServiceMonitors (the operator materializes them). Scrape tokens are read-only metric credentials.
- NetworkPolicy (plan 36): ingress to Prometheus from Grafana only (and the operator/reloader). Ingress to Alertmanager from Prometheus, the Runner (`strange-company/agent-runner`) and the plan 42 `silence-audit` CronJobs (no Grafana, no Authentik; plan 42 §9.4). Egress from Prometheus to the metrics ports in §7.2. Egress from blackbox to in-cluster probe targets plus `0.0.0.0/0:443`.
- Metrics content: metrics carry no secrets, prompts or content. The label vocabulary (§21.1) forbids user, task and commit identifiers.
- Not exposed publicly. The external port scan in the P12 gate (ADR-0010) shows 9100 and 2381 closed from the internet.

## 14. Happy path

1. The `crds` layer already exists from the seed. After control transfer, Flux reconciles `monitoring`. Prometheus, Alertmanager, KSM, node-exporter (in `monitoring-node`) and blackbox become Ready.
2. `monitoring-config` applies all scrape objects. Within 2 scrape intervals, `up == 1` for every job in §7.2 whose component is deployed. Phase-3 jobs (`agent-runner`, `model-gateway`) appear at C01/C05 and are allowed `absent` before then, because their alerts are gated at C11.
3. The plan 42 rules (same `monitoring-config` layer) load. Watchdog fires and reaches the heartbeat receiver.
4. Grafana (plan 39) shows platform dashboards. The Runner and Gateway later expose `sc_*` metrics, and C11 verifies the SC dashboards.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Prometheus pod crash / OOM | `up{job="prometheus"}`, `SCPlatformPodOOMKilled`; at small-ha the other replica alerts. At minimum only the external heartbeat lapses. | No rule evaluation, so no alerts | kubelet restart. Alertmanager keeps sending already-firing alerts until `endsAt`. | `monitoring-failure.md` |
| Prometheus TSDB corruption (unclean shutdown) | Pod CrashLoop with a TSDB error in logs | As above | none | `monitoring-failure.md` §TSDB: delete the PVC, data loss accepted [SPEC §108] |
| Disk cap reached | `SCPrometheusStorageNearCap` | Retention shortens | Size-based deletion | Tune per §11 |
| Cardinality explosion | `SCPrometheusCardinalityHigh`; memory growth | OOM risk | none | `monitoring-failure.md` §cardinality: find the top series (`topk(10, count by (__name__)({__name__=~".+"}))`), add a drop relabel, fix the emitter |
| Alertmanager down | Prometheus `PrometheusNotConnectedToAlertmanagers` (upstream) cannot be delivered. The Watchdog heartbeat stops, and the external monitor alerts the operator. | No notifications | kubelet restart | `notification-failure.md` |
| Node hosting Prometheus lost (minimum) | External heartbeat | Total monitoring loss on the 1-node profile (the node is the cluster) | none | `lost-node.md` → `monitoring-failure.md` |
| Node hosting one Prometheus replica lost (small-ha) | Surviving replica: `SCNodeDown` | Half the HA pair; the pod is pinned by its local-path PV | Pod stays Pending until the node returns | `monitoring-failure.md` §pinned-PV: if the node is not returning, delete the PVC and pod so the replica reschedules empty |
| Scrape token mismatch after rotation | `up{job=~"forgejo|woodpecker"} == 0` → `SCMonitoringTargetDown` | Missing component metrics | none | Re-sync both SOPS copies (§10) |
| Operator down | `up{job="prometheus-operator"}` | Config changes not applied; existing config keeps running | restart | `monitoring-failure.md` |
| k3s shared-registry assumption false (no scheduler metrics) | 38-AC-6 fails | Missing panels | — | Plan-07 fallback (§7.3) |
| etcd metrics not exposed | `up{job="kube-etcd"} == 0` | No etcd alerts | — | Plan 07 flag check |
| Probe false negatives (DNS, egress) | All public probes failing while in-cluster probes pass → `SCEdgeDown` | Possible false page | none | `edge-ingress-failure.md` (plan 09) |

## 16. Retry behavior

Prometheus retries scrapes every interval. Rule evaluation is periodic. The Alertmanager client retries with backoff (built-in). Flux retries reconciliation every `interval: 10m` with `retryInterval: 2m` for the monitoring layers. No custom retry logic is added.

## 17. Idempotency behavior

Everything is declarative Flux state. Re-applying scrape objects is a no-op. Deleting a Prometheus PVC loses only history and the replica comes back empty. The generated metric catalogue is deterministic from §21.2.

## 18. Bootstrap behavior

- Not in the seed set. Reconciled after `flux-bootstrap` (control transfer) and before `verify`. `./bootstrap verify` (plan 50) calls the P09 checks in §23.
- During seed there is no Prometheus. Seed health is judged by Flux readiness and the bootstrap journal (plans 19, 50).
- `MonitoringConfig` is rendered by `./bootstrap` from `bootstrap.env` (object-storage endpoint, `BASE_DOMAIN`, node IPs) and committed as part of the installation config. `./bootstrap expand --profile small-ha` re-renders it (etcd IPs, replicas).

## 19. Upgrade behavior

- Chart upgrades arrive as Renovate PRs. CI renders the chart, runs `promtool check rules` on default plus SC rules, and runs `kubeconform` against the pinned CRDs.
- CRD upgrades go first: the `prometheus-operator-crds` HelmRelease is bumped in the same PR, and Flux orders it by `dependsOn`.
- A major stack upgrade follows the upstream upgrade notes (CRD changes). The plan-48 upgrade procedure includes a pre-upgrade metric/alert snapshot comparison (list of firing alerts before and after).
- Prometheus TSDB format is forward-compatible across minor versions. A downgrade may lose data, which is accepted.

## 20. Recovery behavior

- Prometheus data is not backed up [SPEC §108 "best effort"]. After black start, Prometheus starts empty, and history before the incident is gone.
- Alertmanager silences are lost on PVC loss. The Runner re-posts active organizational alerts within 5 min (plan 42), and Prometheus re-fires rule alerts after `for` elapses.
- Recovery ordering in black start (plan 45): monitoring comes up with the normal post-transfer layers. Recovery verification uses `./bootstrap verify` checks, which do not depend on historical metrics.

## 21. Observability

### 21.1 Cardinality rules (normative for every `sc_*` metric)

1. **Allowed label names** for Strange Company metrics (closed vocabulary): `state`, `from`, `to`, `role`, `harness`, `provider`, `model`, `model_tier`, `phase`, `outcome`, `failure_class`, `reason`, `kind`, `verification_phase`, `verification_mode`, `final_state`, `scope`, `scope_kind`, `window`, `alertname`, `result`, `tool`, `route`, `method`, `code`, `stream_kind`, `stream`, `repository`*, `le`, `quantile`.
   *`repository` is allowed **only** on mirror metrics (`sc_github_mirror_*`), whose value set is the configured mirror list (one repository in MVP), and on `sc_ledger_spend_by_repository_micro_usd` (plan 32: top 20 + `_other`, ≤ 21 series).
   Additional names used by emitter plans and accepted here: `api_family`, `result`, `code`, `status_class`, `streamed`, `token_kind`, `usage_status`, `exactness`, `tier`, `period`, `dep`, `file`, `level`, `stage`.
2. **Forbidden label names:** `task_id`, `task`, `execution_id`, `execution`, `attempt`, `commit`, `sha`, `revision`, `branch`, `pr`, `pull_request`, `user`, `user_id`, `email`, `trace_id`, `span_id`, `request_id`, `pod` (on sc metrics), `job_name`.
3. **Value rules** (exception: the `*_revision_info` / `*_policy_info` gauges of plans 26 and 53 may carry 12-hex revision prefixes, because exactly one series exists at a time and churn equals the number of policy merges): every label value matches `^[A-Za-z0-9_.:/-]{1,64}$`, and must **not** match `^sc-[0-9]+` (task/execution IDs), `^[0-9a-f]{7,64}$` (SHAs), UUID `^[0-9a-f]{8}-[0-9a-f]{4}-`, or contain `@`.
4. **Bounded sets:** `model` values must come from the routing policy (plan 31) or be `other`. The Gateway maps any model not in policy to `other`, so a provider returning arbitrary model strings cannot create series. `provider` comes from the provider registry. `role`, `harness`, `phase`, `state`, `outcome` and `failure_class` are enums defined in the master plan (§7) or below.
5. **Budget:** ≤ 5,000 active `sc_*` series in total (checked by `count({__name__=~"sc_.*"})` in 38-AC-14), and ≤ 250k head series overall.
6. Exact per-task, per-PR and per-repository economics are served from `sc-pg` SQL views (plan 39), never from Prometheus.

### 21.2 Strange Company metric catalogue (authoritative names)

**Reconciliation note (consolidation, G9).** An earlier draft of this table used un-prefixed names (`sc_tasks`, `sc_model_cost_usd_total`, …). Plans 26, 32 and 53, written in parallel, chose `sc_runner_*`, `sc_gateway_*`, `sc_model_*_micro_usd_total` and `sc_ledger_*`. This table now adopts the emitter plans' names wherever they define one, and keeps the remaining entries as **requirements on the emitter** under the emitter's prefix (marked *req*). Plans 39 and 42 and `contracts/alert-catalog.yaml` use exactly these names. Money metrics are integer micro-USD (ADR-0450).

Emitter plans implement exactly these names, types and labels. Histograms use the bucket sets shown.

**Agent Runner (plan 26; task-state semantics plan 29):**

| Metric | Type | Labels | Meaning | Source |
|---|---|---|---|---|
| `sc_runner_tasks` | gauge | `state` | Tasks currently in each **non-terminal** state (terminal `DONE` and `CANCELLED` excluded; `NEEDS_HUMAN` and `BLOCKED` included). Recomputed from `sc-pg` every 30 s. `sc_runner_tasks{state="NEEDS_HUMAN"}` is the SPEC §92 Needs Human panel and the Prometheus-side NEEDS_HUMAN signal (ADR-0555). | 26 |
| `sc_runner_executions` | gauge | `state` | Executions per execution state (`QUEUED`, `STARTING`, `RUNNING`). Active workers = `sum(sc_runner_executions{state=~"STARTING\|RUNNING"})` | 26 |
| `sc_runner_queue_depth` | gauge | — | Executions in `QUEUED` waiting for a concurrency slot | 26 |
| `sc_runner_queue_wait_seconds` | histogram | `role` | QUEUED → STARTING. Buckets 5, 15, 30, 60, 300, 900, 1800, 3600, 7200 | 26 |
| `sc_runner_execution_duration_seconds` | histogram | `role`, `harness`, `outcome` | Job create → terminal. Buckets 30, 60, 120, 300, 600, 900, 1200, 1800, 2700, 3600, 5400 | 26 |
| `sc_runner_results_total` | counter | `role`, `harness`, `outcome` (`SUCCEEDED`/`FAILED`/`CANCELLED`/`TIMED_OUT`), `failure_class` (`none`/`merit`/`infrastructure`/`policy`/`budget`) | Terminal executions | 26 |
| `sc_runner_transitions_total` | counter | `from`, `to` | Every recorded task transition (valid edges only, ≤ ~60 pairs) | 26 |
| `sc_runner_needs_human_total` | counter | `reason` (notification schema `reason` enum) | Entries into `NEEDS_HUMAN` by cause | 26 |
| `sc_runner_dispatch_total` / `sc_runner_dispatch_rejected_total` | counter | `role` / `code` | Dispatch attempts and rejections | 26 |
| `sc_runner_violations_total` | counter | `code` (`PROTECTED_PATH_VIOLATION`, `POLICY_VIOLATION`, `TEST_TAMPER`, `RESULT_SCHEMA_INVALID`, …; bounded by the plan 26 error-code enum) | Detected violations | 26 |
| `sc_runner_policy_valid` | gauge | — | 1 if the current policy loaded and validated | 26 |
| `sc_runner_policy_revision_info` | gauge (=1) | `revision` (12-hex prefix) | Policy revision in force (exception to §21.1 rule 3: one series at a time) | 26 |
| `sc_runner_deploy_keys_active` / `sc_runner_deploy_keys_orphaned` | gauge | — | Live worker deploy keys / keys whose execution is terminal + grace (ADR-0007) | 26 |
| `sc_runner_outbox_pending` / `sc_runner_outbox_oldest_seconds` | gauge | `kind` | Side-effect outbox depth and age | 26 |
| `sc_runner_dependency_up` | gauge | `dep` (`sc-pg`, `forgejo`, `woodpecker`, `gateway`, `alertmanager`, `kubernetes`, `object-storage`) | 1 if the dependency answered its last health call | 26 |
| `sc_runner_leader` | gauge | — | 1 on the elected leader | 26 |
| `sc_runner_log_archive_failures_total` | counter | — | Failed worker-log uploads to S3 `evidence` | 26 |
| `sc_runner_mcp_requests_total` | counter | `tool`, `code` | MCP calls from Hermes (`tool` ∈ the six MCP tools) | 26 |
| `sc_runner_executions_stuck` | gauge | `state` | *req* — executions past their state deadline (`STARTING` > 10 m; `RUNNING` > profile deadline + 5 m) | 26 |
| `sc_tasks_stuck` | gauge | `state` | Tasks in a verifying state > 1 h, or in any non-human state without a transition for `watchdog.taskStallHours` (24 h) (plan 29 name) | 29/26 |
| `sc_task_events_rejected_total` / `sc_task_invariant_violations` / `sc_task_commands_rejected_total` / `sc_runner_human_transitions_total` | counter / gauge / counter / counter | `event` / — / — / — | State-machine health and audit counters (plan 29) | 29/26 |
| `sc_context_sections_withheld_total` / `sc_context_compile_failures_total` | counter | `reason` / — | Context compiler (plan 25) | 25 |
| `sc_runner_task_duration_seconds` | histogram | `verification_mode`, `final_state` | *req* — task creation → terminal. Buckets 600, 1800, 3600, 7200, 14400, 28800, 86400, 172800, 604800 | 26 |
| `sc_runner_retries_total` | counter | `role`, `failure_class` | *req* — new attempts started after a failed attempt | 26 |
| `sc_runner_tdd_compliance_ratio` | gauge | — | *req* — over the trailing 30 days: test-first tasks reaching `DONE` with complete provenance (SPEC §102 facts) ÷ test-first tasks reaching `DONE`; `1` when there are none | 26/22 |
| `sc_runner_merges_without_provenance_total` | counter | — | *req* — merges of test-first PRs detected without complete provenance (plan 55 webhook path in the Runner) | 26/55 |
| `sc_verification_runs_total` | counter | `verification_phase` (`red`/`green`/`judge_validate`/`judge`), `outcome` (`red_valid`, `red_invalid`, `already_satisfied`, `partially_satisfied`, `baseline_failing`, `inconclusive`, `tamper_detected`, `green_pass`, `green_fail`, `judge_valid`, `judge_invalid`, `judging_pass`, `judging_fail`, `infra_error`, `timeout`, `evidence_rejected`) | RED/GREEN/judge verification results (plan 22 §21 names; emitted by the Runner) | 22/26 |
| `sc_verification_duration_seconds` | histogram | `verification_phase` | (plan 22 name) — trigger → evidence ingested. Buckets 30, 60, 120, 300, 600, 1200, 1800, 3600 | 26 |
| `sc_runner_reconcile_last_success_timestamp_seconds` | gauge | — | *req* — last complete reconcile loop | 26 |
| `sc_runner_alert_posts_total` | counter | `alertname`, `result` (`success`/`failure`) | *req* — Alertmanager API posts (plan 42 §7.4) | 26 |
| `sc_runner_api_requests_total` | counter | `route`, `method`, `code` | *req* — internal API (route templates only) | 26 |
| `sc_runner_policy_commit_resolved` | gauge | — | *req* — 1 if the loaded policy's Git commit was resolved (plan 26 §9.2 step 5); feeds `SCRunnerPolicyCommitUnknown` | 26 |
| `sc_runner_queue_oldest_seconds` | gauge | — | *req* — age of the oldest `QUEUED` execution; feeds `SCRunnerQueueBacklog` | 26 |
| `sc_runner_orphan_jobs_deleted_total` | counter | — | *req* — Jobs deleted for lack of an execution row | 26 |
| `sc_runner_unexplained_ref_changes_total` | counter | — | *req* — ref changes without an attributable actor (plan 26 ref audit) | 26 |
| `sc_runner_quarantined_refs` | gauge | — | *req* — quarantined agent refs currently present | 26 |
| `sc_runner_admin_requests_total` | counter | `route` | *req* — loopback admin API calls (human, break-glass class) | 26 |
| `sc_runner_hermes_credential_rotation_failures_total` | counter | — | *req* — failed Hermes credential rotations | 26 |

**Model Gateway (plan 53; cost semantics plan 32; budgets plan 33):**

| Metric | Type | Labels | Meaning | Source |
|---|---|---|---|---|
| `sc_gateway_requests_total` | counter | `provider`, `api_family`, `model`, `result` (`forwarded`/`denied`) | Every request | 53 |
| `sc_gateway_denials_total` | counter | `code` (`SC_BUDGET_EXHAUSTED`, `SC_EMERGENCY_STOP`, `SC_PRICE_UNKNOWN`, `SC_TOKEN_INVALID`, …), `token_kind` | Denied requests by code | 53 |
| `sc_gateway_upstream_responses_total` | counter | `provider`, `api_family`, `status_class`, `code` | Upstream responses | 53 |
| `sc_gateway_request_duration_seconds` / `sc_gateway_ttfb_seconds` | histogram | `provider`, `api_family` (+ `streamed`) | Latency | 53 |
| `sc_gateway_usage_status_total` | counter | `provider`, `api_family`, `usage_status` (`reported`, `estimated`, `unknown`, `not_billable`) | Usage parse outcomes | 53 |
| `sc_gateway_model_mismatch_total` | counter | `provider`, `model` (requested) | Provider-reported model ≠ requested model (L-4) | 53 |
| `sc_gateway_settlement_failures_total` | counter | — | Failed ledger settlement writes | 53 |
| `sc_gateway_provider_available` | gauge | `provider` | 1 if the provider key is present and accepted | 53 |
| `sc_gateway_policy_load_errors_total` | counter | `file` | Policy load failures | 53 |
| `sc_gateway_policy_info` | gauge (=1) | `routing_rev`, `pricing_rev`, `budgets_rev` | Exception to §21.1 rule 3 (one series at a time) | 53 |
| `sc_gateway_overhead_seconds` | histogram | `stage` | Pre-upstream overhead (SLO input) | 53 |
| `sc_gateway_reservation_overrun_micro_usd_total` | counter | `provider` | Actual cost above reservation | 53 |
| `sc_gateway_usage_inconsistent_total` | counter | `provider` | Normalization consistency-check failures | 32 |
| `sc_routing_tier_eligible_candidates` / `sc_routing_resolutions_total` | gauge / counter | `harness`, `role`, `tier` / `result` | Routing health (plan 31 §21) | 31 |
| `sc_model_spend_micro_usd_total` | counter | `provider`, `model`, `harness`, `role`, `phase`, `tier`, `exactness` (`exact`/`estimated`/`unpriced`) | Budget charge at settlement (counter resets on restart; exact values are SQL) | 32 |
| `sc_model_cost_micro_usd_total` | counter | `provider`, `model`, `harness`, `role`, `phase`, `tier` | Priced `cost.total_micro_usd` only | 32 |
| `sc_model_tokens_total` | counter | `provider`, `model`, `kind` (`input`, `cached_input`, `cache_write`, `output`, `reasoning`) | Provider-reported tokens (disjoint, L-3) | 32 |
| `sc_ledger_spend_micro_usd` | gauge | `period` (`today`/`month`), `provider` | Exact spend from the DB, recomputed every 60 s (survives restarts) | 32 |
| `sc_ledger_incomplete_rows` | gauge | `period`, `kind` (`unpriced`/`estimated`/`unknown`) | Rows that make a total a floor | 32 |
| `sc_ledger_rollup_age_seconds` | gauge | — | Age of `ledger.mv_daily_spend` | 32 |
| `sc_ledger_spend_by_repository_micro_usd` | gauge | `period` (`month`), `repository` (top 20 + `_other`) | Exception to the `repository` rule (§21.1), bounded at 21 series | 32 |
| `sc_pricing_entry_expiry_timestamp_seconds` | gauge | `provider`, `model` | Entries with an end date and no successor | 32 |
| `sc_routing_unpriced_candidates` | gauge | `tier` | Routed candidates without an effective price | 32 |
| `sc_budget_limit_micro_usd` / `sc_budget_spent_micro_usd` / `sc_budget_reserved_micro_usd` | gauge | `scope_kind`, `provider` (`""` for global/orchestration) | Current-period limit, spend and open reservations per shared scope | 33 |
| `sc_budget_utilization_ratio` | gauge | `scope_kind`, `provider` | spent / limit | 33 |
| `sc_budget_threshold_reached` | gauge (0/1) | `scope_kind`, `provider`, `threshold` (`50`/`80`) | Warning thresholds | 33 |
| `sc_budget_exhausted` / `sc_budget_cost_incomplete` | gauge (0/1) | `scope_kind`, `provider` | Exhaustion; incomplete cost in period | 33 |
| `sc_budget_metrics_updated_timestamp_seconds` | gauge | — | Freshness of the budget gauges | 33 |
| `sc_budget_denials_total` | counter | `code`, `scope_kind`, `dimension`, `token_kind` | Budget denials | 33 |
| `sc_emergency_stop_level` | gauge (0/1/2) | `source` (`db`, `git`, `k8s`, `degraded`, `effective`) | Emergency-stop level per source (Gateway) | 33 |
| `sc_emergency_stop_active` | gauge (0/1) | `level` (`workers`, `all`) | 1 while the effective level is at least `level` | 33 |
| `sc_emergency_stop_k8s_last_read_ok_timestamp_seconds` / `sc_emergency_stop_cuts_total` | gauge / counter | — / `token_kind` | Kubernetes source freshness; in-flight cuts | 33 |
| `sc_runner_emergency_stop_level` | gauge | `source` | Runner's view of the e-stop sources (`SCEmergencyStopNotApplied`) | 33/26 |
| `sc_runner_dispatch_refusals_total` / `sc_runner_tasks_blocked` | counter / gauge | `code` / `blocked_reason` | Budget/e-stop refusals; blocked tasks | 33/26 |
| `sc_telemetry_debug_grants_active` | gauge | — | Active `TelemetryDebugGrant`s honored by this Gateway/Runner (plan 40) | 40 |

`phase` ∈ `specification`, `test_authoring`, `red_verification`, `implementation`, `green_retry`, `review`, `research`, `orchestration` (Hermes), `other` [SPEC §82].

**GitHub mirror (plan 17):**

| Metric | Type | Labels | Meaning |
|---|---|---|---|
| `sc_github_mirror_last_attempt_success` | gauge | `repository` | 1 if the latest push-mirror sync succeeded |
| `sc_github_mirror_last_success_timestamp_seconds` | gauge | `repository` | Time of the last successful sync |
| `sc_github_mirror_lag_seconds` | gauge | `repository` | Age of the oldest Forgejo `main` commit not present on GitHub `main` (0 when in sync) |

**Other emitters referenced by plan 42's catalogue (names owned by the emitter plan):** `sc_ci_nightly_last_conclusion`, `sc_ci_offcluster_lane_last_run_conclusion`, `sc_ci_offcluster_lane_last_success_timestamp_seconds` (plan 17 probe, plan 52 §21), `sc_ci_config_*` (plan 20 `sc-ci-config`, job `sc-ci-config`), `sc_github_mirror_expected`, `sc_github_mirror_configured`, `sc_github_mirror_diverged`, `sc_github_api_up`, `sc_github_repo_setting_ok`, `sc_github_mirror_token_expiry_timestamp_seconds` (plan 17 §21), `sc_node_*`, `sc_host_firewall_*` (plan 08 §21), `sc_tofu_state_*` (plan 06 §21), `sc_k3s_*`, `sc_etcd_snapshot_last_success_timestamp_seconds`, `sc_node_facts_last_run_timestamp_seconds` (plan 07 §21 textfile collector), `sc_breakglass_*`, `sc_authentik_*`, `sc_oidc_discovery_ok` (plan 15 `platform-probes`), `sc_forgejo_ssh_hostkey_match`, `sc_forge_config_drift`, `sc_forge_bot_token_valid` (plan 16 via `platform-probes`).

**OTel-derived (plan 40):** `sc_span_calls_total`, `sc_span_duration_seconds` (spanmetrics connector, namespace `sc_span`; plan 40 §9.3 pins the exact names), `sc_span_redactions_total` (Collector `count` connector, plan 40).

### 21.3 Recording rules this plan requires plan 42 to define

`sc:cnpg_ready_instances{namespace,cluster}`, `sc:cnpg_desired_instances{namespace,cluster}`, `sc:cnpg_last_backup_success_timestamp_seconds{cluster}`, `sc:longhorn_last_backup_success_timestamp_seconds{volume}`, `sc:etcd_last_s3_snapshot_timestamp_seconds`, `sc:backup_last_success_timestamp_seconds{stream_kind,stream}`, `sc:etcd_expected_members`, and `namespace_tier:container_memory_working_set_bytes:sum{tier}` (dashboard aggregate).

### 21.4 Logs and traces for this subsystem

Prometheus and Alertmanager logs go to stdout (`kubectl logs`). At H03 they go to Loki through otel-agent. No traces.

## 22. Testing strategy

All deterministic, with no paid model calls [SPEC §104].
- **Render tests (F11/CI, offline):** `helm template` of the stack with installation values for both profiles; `kubeconform` with the pinned CRD schemas; F11 Bitnami lint; assertions that `crds.enabled=false`, admission webhooks are off, selectors match `strange.company/monitoring`, and node-exporter renders into `monitoring-node`.
- **`make lint-metrics` (CI, every PR touching `src/` or `gitops/platform/monitoring/`):**
  1. Static: parse `observability/metric-catalog.yaml`. Every metric name starts with `sc_`. Labels are within the §21.1 vocabulary. No forbidden names.
  2. Dynamic: integration tests start the Runner and Gateway against a fake provider (plan 53 fixtures) and the fake harness, drive the scenario set (fake-success, fake-failure, fake-red-valid, fake-green-after-retry), scrape `/metrics`, and assert: (a) every exposed `sc_*` family is in the catalogue with exactly the catalogued labels and type; (b) every label value obeys §21.1 rule 3; (c) total `sc_*` series ≤ 5,000 after 1,000 synthetic tasks. RED proof: a test emitter adding `task_id` makes the job fail.
  3. Scrape-object check: every `job` named in `contracts/alert-catalog.yaml` expressions has exactly one scrape object in `gitops/platform/monitoring/scrape/`.
- **k3d integration (`make e2e-k3d`, plan 52):** install CRDs, the stack (minimum values) and blackbox. Assert targets up for kubelet, apiserver, KSM, node-exporter and blackbox. Run a fake Runner `/metrics` stub to check the ServiceMonitor selection and NetworkPolicy (an unlabeled pod cannot scrape the `metrics` port).
- **Live (P09 gate on the real node):** §23 checks via `./bootstrap verify --check observability` (plan 50 surface) or the equivalent `kubectl`/`promtool query` commands.

## 23. Acceptance criteria

All queries run inside the toolbox: `./bootstrap exec -- kubectl -n monitoring exec sts/prometheus-kps-prometheus -c prometheus -- promtool query instant http://localhost:9090 '<expr>'`, abbreviated below as `PQ '<expr>'`.

### GATE-P09

- **38-AC-1** `./bootstrap exec -- flux get kustomizations -n flux-system crds monitoring monitoring-config` → all `Ready=True` at the Forgejo `main` SHA.
- **38-AC-2** `./bootstrap exec -- kubectl get ns monitoring monitoring-node -o jsonpath='{range .items[*]}{.metadata.name}={.metadata.labels.pod-security\.kubernetes\.io/enforce}{"\n"}{end}'` → `monitoring=baseline`, `monitoring-node=privileged`. `kubectl -n monitoring-node get ds` lists only node-exporter.
- **38-AC-3** `kubectl -n monitoring get svc kps-prometheus kps-alertmanager prometheus-operated alertmanager-operated` → all four exist (re-verifies the chart naming with `fullnameOverride: kps`).
- **38-AC-4** `PQ 'count by (job) (up == 1)'` includes at least: `node-exporter`, `kubelet`, `apiserver`, `kube-state-metrics`, `kube-etcd`, `coredns`, `cnpg`, `cnpg-operator`, `longhorn`, `forgejo`, `authentik`, `flux`, `woodpecker`, `cert-manager`, `traefik`, `cloudflared` (tunnel mode), `otel-collector`, `grafana`, `blackbox`, `prometheus`, `alertmanager`, `prometheus-operator`, `github-mirror`. `PQ 'count(up == 0)'` → empty result.
- **38-AC-5** `PQ 'etcd_server_has_leader{job="kube-etcd"}'` → value `1` for each server. From outside the cluster, `nc -zv <node-public-ip> 2381` and `nc -zv <node-public-ip> 9100` → connection refused or timeout (re-verifies `etcd-expose-metrics` and firewall).
- **38-AC-6** `PQ 'count(scheduler_schedule_attempts_total)'` > 0 and `PQ 'count(workqueue_adds_total{name="deployment"})'` > 0 (re-verifies the k3s shared registry assumption, §7.3). `PQ 'count by (__name__)({__name__="apiserver_request_total"}) '` returns exactly one series family, with no duplicate jobs.
- **38-AC-7** `kubectl -n monitoring get prometheus kps-prometheus -o jsonpath='{.spec.retention} {.spec.retentionSize}'` → `15d 8GiB` (or the 75 GiB-node values per §9).
- **38-AC-8** Size-cap re-verification (k3d, CI): with `retentionSize: 200MiB` and a synthetic series generator (avalanche, test only) running 30 min, `du -sh /prometheus` stays ≤ 260 MiB, which shows `retentionSize` bounds blocks+WAL on the pinned version.
- **38-AC-9** `PQ 'probe_success'` returns one series per Probe in §7.8 that is deployed at P09. All values are `1`, except `hermes-gateway`, `agent-runner` and `model-gateway`, which may be absent before C01/C05/C08.
- **38-AC-21** Static: `make test-netpol-static` (plan 36) evaluates, for every Probe CR in `gitops/platform/monitoring/scrape/`, that its target namespace/port is allowed for the selected prober (`blackbox` or `blackbox-public`) by the rendered NetworkPolicies; the job fails on any unreachable target.
- **38-AC-10** `PQ 'probe_success{probe_kind="public"}'` → all 1, and `PQ 'min(probe_ssl_earliest_cert_expiry{probe_kind="public"}) - time() > 7*86400'` → true.
- **38-AC-11** `PQ 'count(gotk_resource_info)'` > 0, and `PQ 'count(gotk_resource_info{revision!=""})'` → empty (no revision label).
- **38-AC-12** `PQ 'max(prometheus_tsdb_head_series)'` < 150000 on self-hosting-minimum after 24 h of steady state, with the value recorded in the bootstrap verification report.
- **38-AC-13** Scrape-token negative test: `./bootstrap exec -- kubectl -n monitoring run tok-test --rm -i --image=<curl image digest> -- curl -s -o /dev/null -w '%{http_code}' http://forgejo-http.forgejo.svc:3000/metrics` → `401` or `403` (no token). The NetworkPolicy may also make it time out; either is a pass. Prometheus `up{job="forgejo"} == 1`.
- **38-AC-19** `make lint-metrics` passes in CI on the P09 merge commit. The deliberate-violation test (emitter with a `task_id` label) fails the job (RED proof recorded once in the PR).

### GATE-C11

- **38-AC-14** After the C10 fake end-to-end suite has run on the live cluster: `PQ 'count({__name__=~"sc_.*"})'` < 5000, and `PQ 'count({__name__=~"sc_.*", task_id!=""}) or count({__name__=~"sc_.*", execution_id!=""})'` → empty.
- **38-AC-15** `PQ 'count by (__name__) ({__name__=~"sc_runner_tasks|sc_runner_queue_depth|sc_runner_executions|sc_runner_transitions_total|sc_runner_retries_total|sc_runner_tdd_compliance_ratio|sc_model_spend_micro_usd_total|sc_ledger_spend_micro_usd|sc_budget_spent_micro_usd|sc_budget_limit_micro_usd|sc_emergency_stop_level|sc_verification_runs_total"})'` → all 12 names present.
- **38-AC-16** `PQ 'up{job=~"agent-runner|model-gateway"}'` → all 1. A pod in `apps` without the Prometheus label gets a timeout from `curl http://agent-runner.strange-company.svc:<metrics port>/metrics` (NetworkPolicy).
- **38-AC-17** After a fake model invocation with a known fixture cost of 21300 µUSD: `PQ 'sum(increase(sc_model_cost_micro_usd_total[10m]))'` equals the ledger row's `cost_total_micro_usd` (± 1, extrapolation tolerance; SQL check via plan 32's view).

### GATE-P12 (contribution)

- **38-AC-18** `./bootstrap verify` output contains `observability: metrics OK (N jobs up, 0 down)` and the head-series figure.

### GATE-H04 (contribution, HA monitoring)

- **38-AC-20** At small-ha: `kubectl -n monitoring get pods -l app.kubernetes.io/name=prometheus -o wide` shows 2 pods on different nodes, and likewise 2 Alertmanager pods. With the node hosting replica 0 powered off, `SCNodeDown` for that node is delivered to the test receiver within 10 min (drill evidence from plan 45 / 47).

## 24. Explicit non-goals

- Long-term metrics storage (Thanos, Mimir, remote_write to a SaaS). Metrics are best effort [SPEC §108].
- Pushgateway (ADR-0553).
- Per-task, per-PR or per-repository metrics in Prometheus (`sc-pg` serves these).
- Public exposure of the Prometheus or Alertmanager UIs.
- Scraping workers directly.
- Grafana alerting (ADR-0009).

## 25. Open questions

| # | Question | Blocking? | Proposed resolution | Owner |
|---|---|---|---|---|
| 1 | Does the pinned k3s expose controller-manager and scheduler metrics through the apiserver scrape (shared registry)? | no | Assume yes (38-AC-6). Otherwise use the plan-07 fallback with node-IP binding and firewall. | 38 + 07 |
| 2 | Does `etcd-expose-metrics` serve on `:2381` over HTTP on the node IP? | no | Assume yes. Otherwise adjust the `kubeEtcd` port/scheme to observed behavior (38-AC-5). | 07 |
| 3 | Does the CNPG barman-cloud plugin expose last-backup metrics, or is KSM custom-resource state on `Backup` objects needed? | no | Support both behind `sc:cnpg_last_backup_success_timestamp_seconds`. | 12 + 42 |
| 4 | Plan 51's monitoring disk budget is not yet published. This plan requests 10 GiB (Prometheus) + 1 GiB (Alertmanager) per replica, and 2× at small-ha. | no | Plan 51 confirms or reduces the request. `retentionSize` follows as 80% of the allocation. | 51 |
| 5 | `monitoring-node` is a new namespace (ADR-0550) and must be added to master plan §5, ADR-0015, plan 36 (netpol) and plan 37 (PSA labels). | no | Consolidation adds it. | lead |
| 6 | Metric names in §21.2 now follow plans 26, 32 and 53. Entries marked *req* (26) and *assumed* (33) are requirements those plans must adopt. | no | Consolidation adds the *req* metrics to plan 26 §21 and confirms plan 33's `sc_budget_*` / `sc_emergency_stop_active` names. | lead |
| 7 | Layer names differed from plan 18 §8.3. | — | **Resolved:** plan 18 names are used (`crds`, `monitoring`, `monitoring-config`); ADR-0550 carries an amendment mapping its names. | — |
