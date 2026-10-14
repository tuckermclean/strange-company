# 39 — Grafana (dashboards as code)

**Status:** Stage-0 plan (draft)
**DAG nodes:** P09 (co-owner with 38, 40, 42: Grafana, datasources, Platform dashboards), C11 (primary with 42: Strange Company and Economics dashboards), H03 (contributes: Loki and Tempo datasources), P12 (contributes: V-22 dashboards provisioned)
**Related plans:** 12 (`grafana_ro` role), 15 (OIDC mapping §9.4, break-glass), 18 (layers, Flux dashboard), 26/29 (reporting views), 32 (ledger views and economics queries), 33 (budget metrics), 35 (secrets), 36 (NetworkPolicy), 38 (metric catalogue), 40, 41, 42 (alerts panels), 51 (capacity)
**Contracts:** consumes the plan 38 metric catalogue, `cost-record.schema.yaml` (field semantics), plan 32 views; produces no new contract (dashboard JSON is validated by `make lint-dashboards`)
**ADRs:** ADR-0009 (no Grafana alerting), ADR-0550, ADR-0015

## 1. Purpose

Provide the dashboards the Self-Hosting Threshold requires [SPEC §92, §117] — Platform, Strange Company and Economics — as version-controlled code, with Authentik OIDC for humans, a local break-glass admin, and read-only datasources: Prometheus, Alertmanager, the `sc-pg` reporting views through the `grafana_ro` role, and (at H03) Loki and Tempo. Grafana holds no authoritative state: everything it shows is rebuilt from Git and from the systems it reads.

## 2. Requirements derived from SPEC.md

1. Grafana is required by the Self-Hosting Threshold and in MVP [SPEC §20, §117, §126].
2. Platform dashboards: node state, CPU, memory, disk, etcd, CNPG, Longhorn, Forgejo, Authentik, Flux, Woodpecker [SPEC §92].
3. Strange Company dashboards: queue depth, active workers, task duration, red/green transitions, retries, failures, `Needs Human`, TDD compliance [SPEC §92].
4. Economics dashboards: spend today, spend this month, model/provider spend, harness spend, repo spend, cost per accepted task, cost per green task, cheap-tier success, escalation frequency, test-authoring versus implementation spend, estimated routing savings [SPEC §92].
5. Grafana uses Authentik through native OIDC [SPEC §21] and has controlled break-glass access [SPEC §22].
6. Exact accounting comes from CNPG, not Prometheus [SPEC §90]; Prometheus carries no task identifiers [SPEC §91].
7. Dashboards are Git-defined [SPEC §4.1, §4.7]. No Bitnami [SPEC §20].
8. Bootstrap configures Prometheus/Grafana without manual steps [SPEC §130].
- **39-R-1** Every dashboard, folder and datasource is provisioned from Git and is read-only in the UI (`allowUiUpdates: false`, `editable: false`). A UI-created dashboard is scratch and is lost on restart.
- **39-R-2** Grafana state is **ephemeral** (decision §6): no PVC, no backup, no Grafana database to restore.
- **39-R-3** Grafana alerting is disabled; alerts live only in Prometheus/Alertmanager (ADR-0009, plan 42 42-R-4).
- **39-R-4** The `sc-pg` datasource uses role `grafana_ro`, which can read **only** reporting views (no base tables, no token, reservation or denial tables).
- **39-R-5** Every dashboard query is linted: PromQL metric names exist in the plan 38 catalogue (or the upstream exporter allowlist); SQL reads only allowlisted views; datasource UIDs are from the provisioned set.
- **39-R-6** No Grafana plugin is installed from the internet. Only built-in panels and datasources are used.

## 3. Inputs

- Metric catalogue (plan 38 §21.2), recording rules (plan 42 §8.3).
- Reporting views in `sc-pg`: plan 32 `ledger.*` views (§7.4 lists the ones required here) and plan 26/29 `reporting.*` views (§7.4).
- OIDC client `grafana` and mapping (plan 15 §9.4); `SC-OIDC-CLIENTS/grafana`.
- `SC-GRAFANA-ADMIN` (admin password = break-glass value, secret key), `SC-PG-APP/sc-pg-grafana-ro`.
- `BASE_DOMAIN` (`grafana.<d>`), ingress mode (plan 09/10).
- Upstream dashboard JSON: kube-prometheus-stack dashboards (plan 38 `forceDeployDashboards`), CNPG and Flux upstream dashboards (vendored, version-pinned).

## 4. Outputs

- HelmRelease `grafana` (namespace `monitoring`) in the plan 18 `monitoring` layer.
- Provisioned datasources (§7.2), folders and dashboards (§7.3).
- IngressRoute `grafana.<BASE_DOMAIN>` (plan 09 pattern) and a `ServiceMonitor` entry in plan 38's central scrape layer (`job=grafana`).
- `make lint-dashboards` and the `sc-dashcheck` live checker (§22).
- Verify check V-22 (dashboards part) for plan 50.

## 5. Dependencies

**Hard (DAG):** P09 needs P06 (Flux from Forgejo) and P07 (Authentik OIDC). C11 needs C06 (budgets and notifications) and P09.
**Soft/runtime:** plan 38 (Prometheus `kps-prometheus:9090`, Alertmanager `kps-alertmanager:9093`), plan 12 (`sc-pg-r` service, `grafana_ro` managed role), plan 32 (views and grants), plan 26/29 (reporting views), plan 15 (Authentik reachable from the Grafana pod at `https://auth.<d>`), plan 09/10 (ingress and TLS), plan 36 (NetworkPolicy), plan 41 (Loki/Tempo endpoints at H03).

## 6. Owned state

**Decision: Grafana is stateless (ephemeral SQLite on `emptyDir`).** Register item S-27 (plan 11).

| State | Location | Writer | Criticality |
|---|---|---|---|
| Dashboards, folders, datasources | Git (`gitops/platform/grafana/**`) | PR | authoritative |
| Users (auto-created on OIDC login), preferences, stars, ad-hoc dashboards, annotations | SQLite in `emptyDir` (`/var/lib/grafana/grafana.db`) | Grafana | **none**; lost on restart, accepted |
| Sessions | same SQLite | Grafana | lost on restart → users re-login through OIDC |

Rationale: everything valuable is in Git or in the datasources. A PVC would pin Grafana to a node (local-path) or spend Longhorn on a cache [SPEC §27]. With `emptyDir`, Grafana reschedules on any node in seconds at small-ha. Consequences: one replica only (SQLite is not shared, so two replicas would split sessions); no API keys or service accounts persist (none are needed: nothing writes to Grafana); human edits must be exported as JSON and submitted as a PR.

## 7. External interfaces

### 7.1 Deployment

| Item | Decision |
|---|---|
| Chart | The official Grafana Helm chart (`grafana`) from Grafana's published chart repository. **Selection rule:** latest chart whose `appVersion` is the latest Grafana OSS minor at implementation time; the chart repository location is re-verified at implementation (verify: the chart's hosting moved between repositories in the past). Images digest-pinned from `docker.io/grafana/grafana` (OSS) and the chart's sidecar image. No Bitnami (F11 lint). |
| Release | HelmRelease `grafana` in `monitoring`, layer `monitoring` (plan 18 §8.3 #18). Not in the seed set. |
| Replicas | 1 (both profiles). `strategy: RollingUpdate` (no volume to attach). |
| Storage | `persistence.enabled: false`; `emptyDir` `sizeLimit: 256Mi` for `/var/lib/grafana`. |
| Provisioning | Datasources: `datasources.yaml` from Secret `grafana-datasources` (contains `$__file{}` references only). Dashboards: k8s sidecar (`sidecar.dashboards.enabled: true`, `label: grafana_dashboard`, `labelValue: "1"`, `folderAnnotation: grafana_folder`, `searchNamespace: monitoring`, `provider.allowUiUpdates: false`, `provider.foldersFromFilesStructure: false`). Sidecar for datasources disabled (static file). |
| Ingress | `https://grafana.<BASE_DOMAIN>` through Traefik (`websecure`) and the edge of plan 09; `root_url` set accordingly. |
| Metrics | `/metrics` on 3000, scraped by plan 38 (`job=grafana`); NetworkPolicy limits it to Prometheus. |

**`grafana.ini` (normative keys; OIDC keys per plan 15 §9.4):**

```ini
[server]        root_url = https://grafana.<BASE_DOMAIN>/ ; enforce_domain = false
[database]      type = sqlite3 ; path = /var/lib/grafana/grafana.db
[security]      admin_user = sc-breakglass ; admin_password = $__file{/etc/secrets/grafana-admin/admin}
                secret_key = $__file{/etc/secrets/grafana-admin/secret-key} ; cookie_secure = true
                disable_gravatar = true ; disable_initial_admin_creation = false
[users]         allow_sign_up = false ; allow_org_create = false ; auto_assign_org_role = Viewer
[auth]          disable_login_form = false ; oauth_auto_login = true ; signout_redirect_url = https://auth.<d>/application/o/grafana/end-session/
[auth.anonymous] enabled = false
[auth.basic]    enabled = false           ; AUTH-8: no undetected basic-auth Admin API use; break-glass goes through the login form (counted)
[auth.generic_oauth] (plan 15 §9.4 mapping; role_attribute_strict = true; allow_assign_grafana_admin = false)
                ; role expression adds `contains(groups[*], 'sc-synthetic') && 'Viewer'` (synthetic verifier is read-only)
[unified_alerting] enabled = false
[alerting]      enabled = false           ; legacy key, ignored by newer versions (verify)
[analytics]     reporting_enabled = false ; check_for_updates = false ; check_for_plugin_updates = false
[plugins]       plugin_admin_enabled = false ; preinstall_disabled = true (verify key) 
[news]          news_feed_enabled = false
[snapshots]     enabled = false           ; no external snapshot sharing
[dashboards]    default_home_dashboard_path = /tmp/dashboards/platform/sc-platform-overview.json (verify sidecar path)
[log]           mode = console ; level = info
[metrics]       enabled = true
```

### 7.2 Datasources (provisioned; fixed UIDs; `editable: false`)

| UID | Name | Type | Endpoint | Auth | From |
|---|---|---|---|---|---|
| `sc-prometheus` | Prometheus | prometheus (default) | `http://kps-prometheus.monitoring.svc:9090` | none (NetworkPolicy) | P09 |
| (removed) `sc-alertmanager` | — | — | — | — | Not provisioned (AUTH-8): an Alertmanager datasource lets Grafana Admins create silences. Alert state is read from Prometheus `ALERTS` and Alertmanager's own metrics instead. |
| `sc-pg` | sc-pg (reporting, read-only) | grafana-postgresql-datasource (built-in) | `sc-pg-r.strange-company.svc:5432`, database `strange_company`, `sslmode: require`, `maxOpenConns: 3`, `maxIdleConns: 1`, `connMaxLifetime: 14400`, `timescaledb: false` | user `grafana_ro`, password `$__file{/etc/secrets/sc-pg/password}` | C11 (provisioned at P09; panels use it from C11) |
| `sc-loki` | Loki | loki | plan 41 §7.1 (`http://loki.monitoring.svc:3100`) | none | H03 (small-ha overlay) |
| `sc-tempo` | Tempo | tempo (`tracesToLogsV2` → `sc-loki`, `serviceMap` → `sc-prometheus`) | plan 41 §7.1 (`http://tempo.monitoring.svc:3200`) | none | H03 |

Notes:
- `sc-pg-r` is CNPG's "any instance" read Service. It works with 1 instance (the primary) and spreads reads over replicas at 3. (`sc-pg-ro` has no endpoints at 1 instance.)
- `sslmode: require` encrypts but does not verify the CNPG server certificate, because the CNPG CA Secret lives only in `strange-company`. The residual risk is an in-cluster MITM by a compromised pod with network position. It is bounded by NetworkPolicy (only Grafana reaches `sc-pg` from `monitoring`) and by the role being read-only over aggregate views. Upgrade path: plan 10 issues a cert-manager CA bundle into `monitoring` (§25 Q-39-2).
- Plan 32 §7.3 calls this datasource `sc-ledger`. This plan names it `sc-pg` (UID `sc-pg`) because it serves both ledger and Runner reporting views. Plan 32's panel table maps 1:1 (§7.3 Economics).

**`grafana_ro` role (plan 12 managed role; grants owned here, applied by the Runner's in-process migrations (CD-04) as plan 32 does for `sc_reporting`):**
```sql
-- role created by CNPG managed.roles: grafana_ro LOGIN, password from Secret sc-pg-grafana-ro
GRANT sc_reporting TO grafana_ro;                       -- plan 32: SELECT on ledger views + mv_daily_spend
ALTER ROLE grafana_ro SET default_transaction_read_only = on;
ALTER ROLE grafana_ro SET statement_timeout = '10s';
ALTER ROLE grafana_ro CONNECTION LIMIT 5;
GRANT USAGE ON SCHEMA reporting TO sc_reporting;
GRANT SELECT ON ALL TABLES IN SCHEMA reporting TO sc_reporting;   -- views only; schema holds no tables
REVOKE ALL ON SCHEMA runner FROM grafana_ro;            -- no base tables
```
A migration test asserts `grafana_ro` cannot `SELECT` any relation whose `relkind` is `r` (table) or `m` other than `ledger.mv_daily_spend`.

### 7.3 Folders and dashboards (complete list)

Folders (fixed UIDs): `sc-platform` "Platform", `sc-company` "Strange Company", `sc-economics` "Economics", `sc-operations` "Operations", `sc-upstream` "Upstream (vendored)". Dashboard UIDs are stable; every dashboard uses only datasource UIDs from §7.2 (no `${DS_*}` inputs). Default time range 6 h (Economics: 30 d). Variables are listed per dashboard. "P" = Prometheus (`sc-prometheus`), "SQL" = `sc-pg`, "AM" = `sc-alertmanager`.

Units: money panels show USD from micro-USD (`/1e6`) with 2 decimals; any money panel whose scope contains an unpriced, estimated or unknown-usage row shows the "incomplete" overlay required by plan 32 §7.2 (`≥ $x (incomplete)`), driven by `sc_ledger_incomplete_rows` (P) or the `cost_complete` column (SQL).

#### Platform (`sc-platform`)

**`sc-platform-overview` — Platform overview** (home dashboard)

| Panel | Type | Source | Query (sketch) |
|---|---|---|---|
| Nodes Ready / expected | stat | P | `sum(kube_node_status_condition{condition="Ready",status="true"})` / `count(kube_node_info)` |
| Firing alerts by class | stat ×3 | P | `count by (notify_class) (ALERTS{alertstate="firing",alertname!="Watchdog"})` |
| CPU used % per node | timeseries | P | `1 - avg by (instance) (rate(node_cpu_seconds_total{mode="idle"}[5m]))` |
| Memory used % per node | timeseries | P | `1 - node_memory_MemAvailable_bytes / node_memory_MemTotal_bytes` |
| Root disk used % per node | bargauge (thresholds 75/85) | P | `1 - node_filesystem_avail_bytes{mountpoint="/"} / node_filesystem_size_bytes{mountpoint="/"}` |
| etcd leader / members | stat | P | `min(etcd_server_has_leader{job="kube-etcd"})`, `count(up{job="kube-etcd"}==1)` vs `sc:etcd_expected_members` |
| CNPG ready / desired | table | P | `sc:cnpg_ready_instances`, `sc:cnpg_desired_instances` |
| Longhorn volumes by robustness | stat | P | `count by (robustness) (longhorn_volume_robustness)` (1 healthy, 2 degraded, 3 faulted) |
| Service health | state-timeline | P | `probe_success{probe=~"forgejo-http\|forgejo-ssh\|authentik\|woodpecker\|grafana\|agent-runner\|model-gateway\|hermes-gateway\|object-storage"}` |
| Flux not Ready | stat | P | `count(gotk_resource_info{ready!="True",suspended="false"}) or vector(0)` |
| GitHub mirror lag | stat (thresholds 900/3600 s) | P | `sc_github_mirror_lag_seconds{repository="strange-company"}` |
| Last backup age per stream | table | P | `time() - sc:backup_last_success_timestamp_seconds` |
| Earliest certificate expiry | stat | P | `min(certmanager_certificate_expiration_timestamp_seconds) - time()` |

**`sc-nodes` — Nodes** (variable `$node` from `label_values(kube_node_info, node)`)

| Panel | Source | Query (sketch) |
|---|---|---|
| Node conditions (Ready, MemoryPressure, DiskPressure, PIDPressure) | P | `kube_node_status_condition{node=~"$node",status="true"}` |
| CPU by mode | P | `sum by (mode) (rate(node_cpu_seconds_total{instance=~"$node.*"}[5m]))` |
| Load 1/5/15 vs cores | P | `node_load1`, `node_load5`, `node_load15`, `count(node_cpu_seconds_total{mode="idle"})` |
| Memory: available, working set by tier | P | `node_memory_MemAvailable_bytes`; `namespace_tier:container_memory_working_set_bytes:sum` |
| Memory allocatable vs requests vs limits | P | `kube_node_status_allocatable{resource="memory"}`, `sum(kube_pod_container_resource_requests{resource="memory"})`, `…limits…` |
| OOM kills | P | `increase(node_vmstat_oom_kill[1h])`; `kube_pod_container_status_last_terminated_reason{reason="OOMKilled"}` |
| Filesystem used % (/, /var/lib/longhorn) | P | `node_filesystem_*{mountpoint=~"/\|/var/lib/longhorn"}` |
| Disk IO and latency | P | `rate(node_disk_read_bytes_total[5m])`, `rate(node_disk_written_bytes_total[5m])`, `rate(node_disk_io_time_seconds_total[5m])` |
| Network rx/tx (incl. WireGuard `flannel-wg`) | P | `rate(node_network_receive_bytes_total[5m])`, `…transmit…` |
| Clock offset / sync | P | `node_timex_offset_seconds`, `node_timex_sync_status` |
| Reboot required / swap | P | `sc_node_reboot_required`, `sc_node_swap_bytes` |
| Pods per node, restarts | P | `count by (node) (kube_pod_info)`, `sum by (namespace) (increase(kube_pod_container_status_restarts_total[1h]))` |

**`sc-etcd` — etcd**

| Panel | Source | Query (sketch) |
|---|---|---|
| Has leader / leader changes | P | `etcd_server_has_leader`, `increase(etcd_server_leader_changes_seen_total[1h])` |
| Members up vs expected | P | `count(up{job="kube-etcd"}==1)`, `sc:etcd_expected_members` |
| DB size vs quota | P | `etcd_mvcc_db_total_size_in_bytes`, `etcd_server_quota_backend_bytes` |
| WAL fsync / backend commit p99 | P | `histogram_quantile(0.99, sum by (instance, le) (rate(etcd_disk_wal_fsync_duration_seconds_bucket[5m])))`, same for `etcd_disk_backend_commit_duration_seconds_bucket` |
| Proposals failed / pending | P | `rate(etcd_server_proposals_failed_total[5m])`, `etcd_server_proposals_pending` |
| Peer RTT (small-ha) | P | `histogram_quantile(0.99, rate(etcd_network_peer_round_trip_time_seconds_bucket[5m]))` |
| Last snapshot age (local / S3) | P | `time() - max by (location) (sc_etcd_snapshot_last_success_timestamp_seconds)` |

**`sc-cnpg` — PostgreSQL (CNPG)** (variable `$cluster` ∈ `forgejo-pg, authentik-pg, woodpecker-pg, sc-pg`)

| Panel | Source | Query (sketch) |
|---|---|---|
| Ready / desired instances, primary count | P | `sc:cnpg_ready_instances`, `sc:cnpg_desired_instances`, `sc:cnpg_primaries` |
| Replication lag | P | `max by (pod) (cnpg_pg_replication_lag{cluster="$cluster"})` |
| Seconds since last WAL archived | P | `cnpg_pg_stat_archiver_seconds_since_last_archival{cluster="$cluster"}` (threshold 900 s) |
| WAL ready-to-archive backlog | P | `cnpg_collector_pg_wal_archive_status{value="ready",cluster="$cluster"}` (256 / 1024) |
| Last base backup age | P | `time() - sc:cnpg_last_backup_success_timestamp_seconds{cluster="$cluster"}` |
| Recovery window | P | `time() - sc:cnpg_first_recoverability_point_timestamp_seconds` |
| Database size vs nominal | P | `sum by (datname) (cnpg_pg_database_size_bytes{cluster="$cluster"})`, `sc:cnpg_storage_nominal_bytes` |
| Connections vs max | P | `sum(cnpg_backends_total{cluster="$cluster"})`, `cnpg_pg_settings_setting{name="max_connections"}` |
| Transactions / s, deadlocks | P | `rate(cnpg_pg_stat_database_xact_commit[5m])`, `rate(cnpg_pg_stat_database_deadlocks[5m])` |
| Operator and plugin available | P | `kube_deployment_status_replicas_available{namespace="cnpg-system"}` |

Plus the vendored upstream CNPG dashboard `cnpg-upstream` in folder `sc-upstream` (source: the CNPG project's Grafana dashboard JSON at the pinned operator version, datasource rewritten to `sc-prometheus`).

**`sc-longhorn` — Longhorn**

| Panel | Source | Query (sketch) |
|---|---|---|
| Volume state / robustness table | P | `longhorn_volume_state`, `longhorn_volume_robustness` by `volume` |
| Actual size vs capacity | P | `longhorn_volume_actual_size_bytes / longhorn_volume_capacity_bytes` |
| Node storage used vs capacity | P | `longhorn_node_storage_usage_bytes / longhorn_node_storage_capacity_bytes` |
| Last backup age per volume | P | `time() - sc:longhorn_last_backup_success_timestamp_seconds` |
| Replica count per volume | P | `count by (volume) (longhorn_replica_info)` (verify metric) |
| Manager CPU / memory | P | cAdvisor on `namespace="longhorn-system"` |

**`sc-forgejo` — Forgejo**

| Panel | Source | Query (sketch) |
|---|---|---|
| HTTP / SSH health | P | `probe_success{probe=~"forgejo-http\|forgejo-ssh\|public-git"}` |
| Repositories, users, issues, pulls | P | `gitea_repositories`, `gitea_users`, `gitea_issues`, `gitea_pulls` (verify Forgejo keeps `gitea_` names; plan 16 §21) |
| Request rate / 5xx via edge | P | `sum by (code) (rate(traefik_service_requests_total{exported_service=~"forgejo.*"}[5m]))` |
| `forgejo-data` usage | P | `longhorn_volume_actual_size_bytes{volume="forgejo-data"}` / capacity |
| Mirror lag and last success | P | `sc_github_mirror_lag_seconds`, `time() - sc_github_mirror_last_success_timestamp_seconds` |
| Bot tokens valid, config drift, host-key match | P | `sc_forge_bot_token_valid`, `sc_forge_config_drift`, `sc_forgejo_ssh_hostkey_match` |
| Pod restarts / pending | P | `kube_pod_container_status_restarts_total{namespace="forgejo"}`, `kube_pod_status_phase{namespace="forgejo",phase="Pending"}` |
| `forgejo-pg` summary | P | link to `sc-cnpg?var-cluster=forgejo-pg` |

**`sc-authentik` — Authentik and SSO**

| Panel | Source | Query (sketch) |
|---|---|---|
| Live / ready | P | `probe_success{probe=~"authentik\|public-auth"}`, `up{job="authentik"}` |
| OIDC discovery per app | P | `sc_oidc_discovery_ok` by `app` |
| Blueprint status | P | `sc_authentik_blueprint_status` |
| Worker tasks in error | P | `authentik_system_tasks{status="error"}` (verify name) |
| Break-glass readiness / last use | P | `sc_breakglass_account_ready`, `time() - sc_breakglass_last_login_timestamp_seconds` |
| `authentik-pg` connections | P | `sum(cnpg_backends_total{cluster="authentik-pg"})` |

**`sc-flux` — Flux** (panel set handed over by plan 18 §21)

| Panel | Source | Query (sketch) |
|---|---|---|
| Ready / not Ready by kind | P | `count by (customresource_kind, ready) (gotk_resource_info)` |
| Not-ready objects table | P | `gotk_resource_info{ready!="True"}` (kind, namespace, name, suspended) |
| Suspended objects | P | `gotk_resource_info{suspended="true"}` |
| Reconcile duration p95 by kind | P | `histogram_quantile(0.95, sum by (kind, le) (rate(gotk_reconcile_duration_seconds_bucket[10m])))` |
| Controller errors, workqueue depth | P | `rate(controller_runtime_reconcile_errors_total[5m])`, `workqueue_depth{job="flux"}` |
| Seed Bucket present | P | `gotk_resource_info{customresource_kind="Bucket"}` |

The applied revision per layer is **not** a panel: commit SHAs are forbidden as labels (plan 38 §21.1). The dashboard links to `flux get kustomizations` instructions and to Forgejo's commit view instead.

**`sc-woodpecker` — Woodpecker (trusted CI)**

| Panel | Source | Query (sketch) |
|---|---|---|
| Server health | P | `probe_success{probe=~"woodpecker\|public-ci"}` |
| Connected agents | P | `woodpecker_worker_count` (verify) |
| Pending / running steps | P | `woodpecker_pending_steps`, `woodpecker_running_steps` (verify) |
| Pipelines by status (rate) | P | `sum by (status) (rate(woodpecker_pipeline_total[1h]))` (verify) |
| `ci-jobs` pods and resources | P | `count(kube_pod_info{namespace="ci-jobs"})`, cAdvisor working set `namespace="ci-jobs"` |
| RED/GREEN verification runs (link) | P | `sum by (verification_phase, outcome) (increase(sc_verification_runs_total[1h]))` |

**`sc-ingress-certs` — Ingress and certificates**

| Panel | Source | Query (sketch) |
|---|---|---|
| Public endpoint success / latency | P | `probe_success{probe_kind="public"}`, `probe_duration_seconds{probe_kind="public"}` |
| Certificate expiry (cert-manager, edge) | P | `certmanager_certificate_expiration_timestamp_seconds - time()`, `probe_ssl_earliest_cert_expiry - time()` |
| Certificates not Ready | P | `certmanager_certificate_ready_status{condition="False"}` |
| Traefik requests / 5xx by service | P | `sum by (exported_service, code) (rate(traefik_service_requests_total[5m]))` |
| cloudflared connections | P | `cloudflared_tunnel_ha_connections` (verify) |

**`sc-backups-dr` — Backups and disaster recovery**

| Panel | Source | Query (sketch) |
|---|---|---|
| Last success age per backup stream | P | `time() - sc:backup_last_success_timestamp_seconds` (thresholds: cnpg 26 h, repo 2 h, longhorn 26 h, etcd 13 h) |
| WAL archive age per cluster | P | `cnpg_pg_stat_archiver_seconds_since_last_archival` |
| Check CronJobs: last success age | P | `time() - kube_cronjob_status_last_successful_time{cronjob=~"restore-test-.*\|rt-.*\|check-tofu-.*\|recovery-.*\|objstore-canary\|storage-audit"}` |
| Check CronJobs: latest run failed | P | `(kube_cronjob_status_last_schedule_time - kube_cronjob_status_last_successful_time) > 0` |
| GitHub mirror lag / diverged / configured | P | `sc_github_mirror_lag_seconds`, `sc_github_mirror_diverged`, `sc_github_mirror_configured` |
| Object storage probe | P | `probe_success{probe="object-storage"}` |
| Break-glass drill age | P | `time() - sc_breakglass_drill_last_success_timestamp_seconds` |
| Black-start threats firing | P | `ALERTS{always_notify="black-start-threat",alertstate="firing"}` |

**`sc-monitoring-health` — Monitoring health**

| Panel | Source | Query (sketch) |
|---|---|---|
| Scrape targets up/down by job | P | `count by (job) (up == 1)`, `count by (job) (up == 0)` |
| Head series / `sc_*` series | P | `prometheus_tsdb_head_series`, `count({__name__=~"sc_.*"})` |
| TSDB size vs cap, effective retention | P | `prometheus_tsdb_storage_blocks_bytes + prometheus_tsdb_wal_storage_size_bytes`, `sc_prometheus_retention_size_bytes`, `time() - prometheus_tsdb_lowest_timestamp_seconds` |
| Rule evaluation failures | P | `rate(prometheus_rule_evaluation_failures_total[5m])` |
| Alertmanager deliveries / failures by integration | P | `rate(alertmanager_notifications_total[5m])`, `rate(alertmanager_notifications_failed_total[5m])` |
| Heartbeat deliveries | P | `increase(alertmanager_notifications_total{receiver_name="heartbeat"}[10m])` (verify label) |
| OTel Collector accepted / refused / exported spans | P | `otelcol_receiver_accepted_spans_total`, `otelcol_receiver_refused_spans_total`, `otelcol_exporter_sent_spans_total` (plan 40 §21) |
| Redactions, debug grants | P | `increase(sc_span_redactions_total[1h])`, `sc_telemetry_debug_grants_active` |
| Monitoring pods memory | P | cAdvisor working set `namespace=~"monitoring\|monitoring-node"` |

#### Operations (`sc-operations`)

**`sc-alerts` — Alerts and notifications**

| Panel | Source | Query (sketch) |
|---|---|---|
| Firing alerts (table: alertname, severity, category, class, always_notify, since) | P | `ALERTS{alertstate="firing"}` + `ALERTS_FOR_STATE` |
| Tasks waiting for a human (Runner-posted alerts are not Prometheus series) | SQL | `SELECT task_id, title, reason, entered_at, task_url FROM reporting.v_tasks_needing_human` |
| Active silences | P | `alertmanager_silences{state="active"}` (details via `amtool` over port-forward, plan 42 §9.4) |
| Notifications sent / failed per receiver | P | `sum by (receiver_name) (increase(alertmanager_notifications_total[1h]))`, failures likewise |
| Runner alert posts | P | `sum by (alertname, result) (increase(sc_runner_alert_posts_total[1h]))` |

#### Strange Company (`sc-company`)

**`sc-orchestration` — Strange Company: orchestration** (SPEC §92 Strange Company items)

| Panel | SPEC §92 item | Source | Query (sketch) |
|---|---|---|---|
| Queue depth | queue depth | P | `sc_runner_queue_depth` |
| Oldest queued (minutes) | queue depth | P | `sc_runner_queue_oldest_seconds / 60` |
| Queue wait p50 / p95 by role | queue depth | P | `histogram_quantile(0.95, sum by (role, le) (rate(sc_runner_queue_wait_seconds_bucket[1h])))` |
| Active workers (STARTING + RUNNING) vs limit | active workers | P | `sum(sc_runner_executions{state=~"STARTING\|RUNNING"})`; limit line from the `concurrencyLimit` annotation of the dashboard (rendered from the profile: 1 / 2) |
| Tasks by state | — | P | `sum by (state) (sc_runner_tasks)` |
| Needs Human (count; red if > 0) | `Needs Human` | P | `sc_runner_tasks{state="NEEDS_HUMAN"}` |
| Tasks in NEEDS_HUMAN (table with links) | `Needs Human` | SQL | `SELECT task_id, title, repository, reason, entered_at, task_url FROM reporting.v_tasks_needing_human ORDER BY entered_at` |
| Task duration p50 / p95 by verification mode | task duration | P | `histogram_quantile(0.95, sum by (verification_mode, le) (rate(sc_runner_task_duration_seconds_bucket[$__range])))` |
| Execution duration p95 by role | task duration | P | `histogram_quantile(0.95, sum by (role, le) (rate(sc_runner_execution_duration_seconds_bucket[1h])))` |
| RED/GREEN transitions per hour | red/green transitions | P | `sum by (to) (increase(sc_runner_transitions_total{to=~"RED_VERIFIED\|GREEN_VERIFIED\|TEST_NEEDS_WORK\|ALREADY_SATISFIED\|TEST_SPEC_CONFLICT"}[1h]))` |
| Verification outcomes | red/green transitions | P | `sum by (verification_phase, outcome) (increase(sc_verification_runs_total[$__range]))` |
| Verification duration p95 | red/green transitions | P | `histogram_quantile(0.95, sum by (verification_phase, le) (rate(sc_verification_duration_seconds_bucket[1h])))` |
| Retries by role and failure class | retries | P | `sum by (role, failure_class) (increase(sc_runner_retries_total[$__range]))` |
| Failures by class and harness | failures | P | `sum by (failure_class, harness) (increase(sc_runner_results_total{outcome=~"FAILED\|TIMED_OUT"}[$__range]))` |
| Policy violations by code | failures | P | `sum by (code) (increase(sc_runner_violations_total[$__range]))` |
| NEEDS_HUMAN entries by reason | `Needs Human` | P | `sum by (reason) (increase(sc_runner_needs_human_total[$__range]))` |
| Runner health (policy valid, dependencies, outbox age, leader) | — | P | `sc_runner_policy_valid`, `sc_runner_dependency_up`, `sc_runner_outbox_oldest_seconds`, `sc_runner_leader` |
| Hermes MCP calls by tool / code | — | P | `sum by (tool, code) (rate(sc_runner_mcp_requests_total[5m]))` |
| Deploy keys active / orphaned | — | P | `sc_runner_deploy_keys_active`, `sc_runner_deploy_keys_orphaned` |

**`sc-tdd` — Strange Company: TDD compliance**

| Panel | SPEC §92 item | Source | Query (sketch) |
|---|---|---|---|
| TDD compliance (30 d, stat; red if < 1) | TDD compliance | P | `sc_runner_tdd_compliance_ratio` |
| Merges without provenance (counter; red if > 0) | TDD compliance | P | `sc_runner_merges_without_provenance_total` |
| Test-first tasks done, by provenance completeness (table) | TDD compliance | SQL | `SELECT task_id, repository, done_at, provenance_complete, missing_facts FROM reporting.v_tdd_provenance WHERE done_at >= $__timeFrom() ORDER BY done_at DESC` |
| Funnel: SPECIFIED → RED_VERIFIED → GREEN_VERIFIED → DONE | red/green transitions | P | `sum by (to) (increase(sc_runner_transitions_total{to=~"SPECIFIED\|RED_VERIFIED\|GREEN_VERIFIED\|DONE"}[$__range]))` |
| Invalid RED / already satisfied / spec conflicts | red/green transitions | P | `sum by (outcome) (increase(sc_verification_runs_total{outcome=~"red_invalid\|already_satisfied"}[$__range]))`, `increase(sc_runner_transitions_total{to="TEST_SPEC_CONFLICT"}[$__range])` |
| Protected-path and test-tamper violations | TDD compliance | P | `sum by (code) (increase(sc_runner_violations_total{code=~"PROTECTED_PATH_VIOLATION\|TEST_TAMPER"}[$__range]))` |

**`sc-workers` — Workers**

| Panel | Source | Query (sketch) |
|---|---|---|
| Executions by state | P | `sum by (state) (sc_runner_executions)` |
| Outcomes by harness / role | P | `sum by (harness, role, outcome) (increase(sc_runner_results_total[$__range]))` |
| Dispatch rejections by code | P | `sum by (code) (increase(sc_runner_dispatch_rejected_total[$__range]))` |
| `agent-jobs` memory / CPU (namespace aggregate) | P | `sum(container_memory_working_set_bytes{namespace="agent-jobs",container!=""})`, `sum(rate(container_cpu_usage_seconds_total{namespace="agent-jobs"}[5m]))` |
| `agent-jobs` OOM kills | P | `sum(kube_pod_container_status_last_terminated_reason{namespace="agent-jobs",reason="OOMKilled"})` |
| Span RED metrics per span name | P | `sum by (span_name) (rate(sc_span_calls_total[5m]))`, `histogram_quantile(0.95, sum by (span_name, le) (rate(sc_span_duration_seconds_bucket[15m])))` (plan 40) |

Per-execution detail is **not** a Grafana panel (no task labels in Prometheus); humans use `agent_status`/`agent_logs` through Hermes or the Runner read API. At H03 the `sc-tasks-trace` panel links a task's `trace_id` (from `reporting.v_tasks_needing_human` and `reporting.v_task_traces`) to Tempo.

**`sc-gateway` — Model Gateway**

| Panel | Source | Query (sketch) |
|---|---|---|
| Requests by provider / model / result | P | `sum by (provider, model, result) (rate(sc_gateway_requests_total[5m]))` |
| Denials by code | P | `sum by (code) (increase(sc_gateway_denials_total[1h]))` |
| Upstream status classes | P | `sum by (provider, status_class) (rate(sc_gateway_upstream_responses_total[5m]))` |
| Latency p50 / p95 and TTFB | P | `histogram_quantile(0.95, sum by (provider, le) (rate(sc_gateway_request_duration_seconds_bucket[5m])))`, same for `sc_gateway_ttfb_seconds` |
| Overhead p99 | P | `histogram_quantile(0.99, sum by (le) (rate(sc_gateway_overhead_seconds_bucket[15m])))` |
| Usage status share | P | `sum by (usage_status) (increase(sc_gateway_usage_status_total[1h]))` |
| Model mismatches | P | `sum by (provider, model) (increase(sc_gateway_model_mismatch_total[$__range]))` |
| Provider availability, emergency stop level per source | P | `sc_gateway_provider_available`, `sc_emergency_stop_level` by `source`; `sum by (code) (increase(sc_budget_denials_total[1h]))` |

#### Economics (`sc-economics`)

**`sc-economics` — Economics** (default range: 30 d; every SPEC §92 Economics item; queries per plan 32 §14.4/§21.2)

| Panel | SPEC §92 item | Source | Query (sketch) |
|---|---|---|---|
| Spend today (USD; incomplete overlay) | spend today | P | `sum(sc_ledger_spend_micro_usd{period="today"}) / 1e6`; overlay if `sum(sc_ledger_incomplete_rows{period="today"}) > 0` |
| Spend this month | spend this month | P | `sum(sc_ledger_spend_micro_usd{period="month"}) / 1e6`; overlay likewise |
| Budget utilization per scope | (budget) | P | `sc_budget_utilization_ratio` by `scope_kind, provider`; stop status `sc_emergency_stop_level{source="effective"}`; blocked tasks `sc_runner_tasks_blocked` |
| Spend by model and provider (time series) | model/provider spend | P | `sum by (provider, model) (increase(sc_model_spend_micro_usd_total[1h])) / 1e6` |
| Spend by provider this month (bar) | model/provider spend | P | `sum by (provider) (sc_ledger_spend_micro_usd{period="month"}) / 1e6` |
| Harness spend | harness spend | P | `sum by (harness) (increase(sc_model_spend_micro_usd_total[$__range])) / 1e6` |
| Repo spend | repo spend | SQL | `SELECT repository, sum(total_cost_micro_usd)/1e6 AS usd, bool_and(unpriced_rows+estimated_rows+unknown_rows=0) AS complete FROM ledger.mv_daily_spend WHERE day >= $__timeFrom()::date GROUP BY 1 ORDER BY 2 DESC` |
| Cost per accepted task (overall and by starting tier/model) | cost per accepted task | SQL | `SELECT start_tier, start_model, accepted_tasks, cost_per_accepted_task_micro_usd/1e6, cost_complete FROM ledger.v_cost_per_accepted_task WHERE week >= $__timeFrom()` (plan 32 Q8 as a view) |
| Cost per green task by model | cost per green task | SQL | `SELECT model_id, green_tasks, cost_per_green_task_micro_usd/1e6, cost_complete FROM ledger.v_cost_per_green_task WHERE week >= $__timeFrom()` (Q5) |
| Cheap-tier success rate by role | cheap-tier success | SQL | `SELECT week, role, success_rate, n FROM ledger.v_tier_success WHERE tier='bargain' AND week >= $__timeFrom()` (Q6) |
| Escalation frequency (weekly) and escalation cost | escalation frequency | SQL | `SELECT week, escalation_ratio, escalated_spend_micro_usd/1e6, failed_pre_escalation_spend_micro_usd/1e6 FROM ledger.v_escalation WHERE week >= $__timeFrom()` (Q7 + plan 32 §21.2 ratio) |
| Test authoring vs implementation vs review spend | test-authoring vs implementation | P | `sum by (phase) (increase(sc_model_spend_micro_usd_total{phase=~"test_authoring\|implementation\|green_retry\|review\|specification"}[$__range])) / 1e6` |
| Estimated routing savings (with assumption text) | estimated routing savings | SQL | `SELECT week, savings_micro_usd/1e6, tasks FROM ledger.v_routing_savings WHERE week >= $__timeFrom()` (Q-S); a text panel states plan 32's counterfactual assumption |
| Spend on failed work | (§80 Q10) | SQL | `SELECT day, failed_executions_micro_usd/1e6, cancelled_tasks_micro_usd/1e6, infra_failures_micro_usd/1e6 FROM ledger.v_failed_work_spend WHERE day >= $__timeFrom()::date` |
| Tokens by kind and provider | — | P | `sum by (provider, kind) (increase(sc_model_tokens_total[$__range]))` |
| Ledger completeness | — | P | `sc_ledger_incomplete_rows` by `period, kind`; `sc_ledger_rollup_age_seconds` |
| Orchestration (Hermes) spend | — | P | `sum(increase(sc_model_spend_micro_usd_total{phase="orchestration"}[$__range])) / 1e6` |

**`sc-economics-drilldown` — Cost by PR and task** (variables `$repository` from `SELECT DISTINCT repository FROM ledger.mv_daily_spend`, `$pr` free text; SQL only)

| Panel | Source | Query (sketch) |
|---|---|---|
| PR total, by phase (Q1–Q4) | SQL | `SELECT phase, cost_micro_usd/1e6, cost_complete FROM ledger.v_pr_cost_by_phase WHERE repository = $repository AND pr_number = $pr` |
| Executions of the PR's task | SQL | `SELECT execution_id, role, attempt, harness, model_requested, total_cost_micro_usd/1e6, cost_complete FROM ledger.v_execution_cost WHERE task_id = (SELECT task_id FROM ledger.v_pr_cost_by_phase WHERE repository=$repository AND pr_number=$pr LIMIT 1)` |

#### Upstream (`sc-upstream`)

kube-prometheus-stack dashboards (cluster/namespace/pod/workload compute resources, kubelet, API server, CoreDNS, persistent volumes, node exporter, Prometheus, Alertmanager) rendered as ConfigMaps by plan 38 with `grafana_folder: Upstream (vendored)`, the vendored CNPG dashboard, and Flux's upstream "cluster" and "control plane" dashboards (plan 18 §8.8 assets). Upstream dashboards are not linted for metric names (only for datasource UIDs and parseability).

### 7.4 SQL views this plan consumes (interface requirements)

| View | Owner | Columns (minimum) |
|---|---|---|
| `ledger.mv_daily_spend` | 32 (exists) | per plan 32 §14.2 |
| `ledger.v_execution_cost` | 32 (exists) | `execution_id, task_id, role, attempt, harness, model_requested, total_cost_micro_usd, cost_complete` |
| `ledger.v_pr_cost_by_phase` | 32 (**requested**) | `repository, pr_number, task_id, phase, cost_micro_usd, cost_complete` (Q1–Q4) |
| `ledger.v_cost_per_green_task` | 32 (**requested**) | `week, model_id, green_tasks, cost_per_green_task_micro_usd, cost_complete` (Q5) |
| `ledger.v_tier_success` | 32 (**requested**) | `week, tier, role, n, success_rate` (Q6) |
| `ledger.v_escalation` | 32 (**requested**) | `week, escalation_ratio, escalated_spend_micro_usd, failed_pre_escalation_spend_micro_usd, tasks_escalated` (Q7 + ratio) |
| `ledger.v_cost_per_accepted_task` | 32 (**requested**) | `week, start_tier, start_model, accepted_tasks, cost_per_accepted_task_micro_usd, avg_impl_attempts, cost_complete` (Q8) |
| `ledger.v_routing_savings` | 32 (**requested**) | `week, tasks, savings_micro_usd, baseline_tier` (Q-S) |
| `ledger.v_failed_work_spend` | 32 (**requested**) | `day, failed_executions_micro_usd, cancelled_tasks_micro_usd, infra_failures_micro_usd` (Q10) |
| `reporting.v_tasks_needing_human` | 26/29 (**requested**) | `task_id, title, repository, reason, entered_at, task_url, trace_id` |
| `reporting.v_tdd_provenance` | 26/22 (**requested**) | `task_id, repository, done_at, provenance_complete bool, missing_facts text[]` |
| `reporting.v_task_traces` (H03) | 26 (**requested**) | `task_id, trace_id, created_at` |

Plan 32's Q5–Q8 join `runner.*` base tables. Wrapping them as `ledger.v_*` views (owner: the migration role, `security_invoker = false`) lets `grafana_ro` read results without any grant on `runner.*` tables (39-R-4). 39-AC-12 asserts it.

## 8. Internal interfaces

- Grafana → Prometheus / Alertmanager / `sc-pg` / Loki / Tempo (read only).
- Sidecar → Kubernetes API: `list/watch configmaps` in namespace `monitoring` only (Role, not ClusterRole).
- Grafana → Authentik: OIDC authorize/token/userinfo per plan 15 §9.4.

## 9. Configuration

| Key (Git path) | Default | Changed by |
|---|---|---|
| `gitops/platform/grafana/base/values.yaml` (chart values, `grafana.ini` §7.1) | §7.1 | PR |
| `gitops/platform/grafana/datasources/datasources.yaml` (rendered into Secret `grafana-datasources`) | §7.2; Loki/Tempo entries only in the small-ha overlay | PR |
| `gitops/platform/grafana/dashboards/<folder>/<uid>.json` (ConfigMaps via `configMapGenerator`, label `grafana_dashboard: "1"`, annotation `grafana_folder`) | §7.3 | PR (export JSON from a scratch dashboard, then commit) |
| `gitops/platform/grafana/dashboards/upstream/` (vendored JSON + `SOURCE` file with URL and version) | pinned | Renovate PR |
| Dashboard annotation `concurrencyLimit` | rendered from profile (1 / 2) | profile overlay |
| `gitops/platform/grafana/overlays/<profile>/` | replicas 1; resources §12 | PR |

## 10. Secrets

| Class | Object | Notes |
|---|---|---|
| `SC-GRAFANA-ADMIN` (plan 35; owner 39) | Secret `monitoring/grafana-admin` keys `admin` (= `SC-BREAK-GLASS-GRAFANA` value, plan 15 Q-15-07), `secret-key` | SOPS in Git (two recipients) + recovery bundle copy of `admin`. Rotation: `rotate-break-glass.md#grafana` updates both the SOPS file and the bundle. Changing `secret-key` invalidates stored datasource secrets; harmless here because datasource secrets come from files at every start. |
| `SC-OIDC-CLIENTS/grafana` (plan 15) | Secret `monitoring/grafana-oidc-client` key `client-secret` | same value as the Authentik copy (plan 15) |
| `SC-PG-APP` instance `sc-pg-grafana-ro` (plan 12; **request to plan 35**: register this instance with two storage locations) | `strange-company/sc-pg-grafana-ro` (CNPG managed-role password) and an identical SOPS copy `monitoring/grafana-datasource-sc-pg` key `password` | generated at pre-seed by `sc-bootstrap` (plan 50); rotation changes both files in one PR; CNPG updates the role password (verify), Grafana re-reads the file on restart (checksum annotation) |

No other credential exists in Grafana. No Grafana API key or service-account token is created.

## 11. Storage

`emptyDir` only (§6), `sizeLimit: 256Mi`. Dashboards, datasources and configuration are Git. No backup [SPEC §108 does not list Grafana state; plan 11 S-26/S-27].

## 12. Resource requirements

| Workload | Requests (cpu / mem) | Limit (mem) | Replicas |
|---|---|---|---|
| grafana | 50m / 192 MiB | 384 MiB | 1 |
| dashboard sidecar | 10m / 48 MiB | 96 MiB | 1 |
| **Total** | 60m / 240 MiB | | |

PriorityClass `platform`. On 8 GiB `constrained` nodes: 160 MiB request, 320 MiB limit. SQL panels are bounded by `statement_timeout = 10s` and `CONNECTION LIMIT 5`, so Grafana cannot load `sc-pg` noticeably.

## 13. Security boundary

- Namespace `monitoring` (PSA baseline, restricted-compatible pod spec: non-root uid 472, read-only root filesystem except `/var/lib/grafana` and `/tmp`, all capabilities dropped, RuntimeDefault seccomp).
- Human access: OIDC (Authentik groups → roles per plan 15 §9.4). Break-glass: local `sc-breakglass` admin at `/login?disableAutoLogin=true` (verify path on the pinned version), detected by `SCGrafanaLocalLogin` (plan 42; metric `grafana_api_login_post_total` (verify)), which closes plan 15's Q-15-04 gap before H03.
- Grafana has **no write path to production**: datasources are read-only (Prometheus HTTP read API; `grafana_ro` with `default_transaction_read_only`). There is no Alertmanager datasource, so Grafana cannot create silences (AUTH-8). Basic auth is disabled; the only non-OIDC entry is the login form, which `SCGrafanaLocalLogin` counts.
- NetworkPolicy (plan 36): ingress from Traefik (`ingress` namespace) on 3000 and from Prometheus on 3000 (`/metrics`); egress to `kps-prometheus:9090`, `kps-alertmanager:9093`, `sc-pg` (`strange-company`, pod label `cnpg.io/cluster=sc-pg`, 5432), Authentik per plan 15's in-cluster path, DNS, and at H03 Loki/Tempo. No other internet egress (plugins, gravatar, news and update checks are disabled).
- Content: dashboards show aggregates and task IDs/titles from reporting views. They never show prompts, completions, code or secrets, because no datasource contains them [SPEC §94]. Loki at H03 excludes worker, CI and Hermes logs at the source (ADR-0552).

## 14. Happy path

1. P09: Flux reconciles the `monitoring` layer. Grafana starts with an empty SQLite, provisions datasources and loads dashboards from ConfigMaps within 60 s.
2. A human opens `https://grafana.<d>`, is redirected to Authentik, logs in with MFA, and lands on `sc-platform-overview` with the role from their group.
3. C11: Runner and Gateway metrics and the reporting views exist; the Strange Company and Economics dashboards show data.
4. A human wants a new panel: builds it in a scratch dashboard, exports JSON, opens a PR; CI runs `make lint-dashboards`; after merge, Flux delivers it.
5. H03: the small-ha overlay adds the Loki and Tempo datasources; trace links appear.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Grafana pod down | `SCGrafanaDown` (probe) | no dashboards; alerting unaffected (Alertmanager is the hub) | kubelet restart; reschedule anywhere (no PV) | `monitoring-failure.md` §Grafana |
| Authentik down | `SCAuthentikDown` | no OIDC login; existing sessions keep working until restart | none | `break-glass-access.md` §Grafana |
| Break-glass login used | `SCGrafanaLocalLogin` (page) | audit event | notify | `break-glass-access.md` §After use |
| Dashboard JSON invalid or duplicate UID | CI `make lint-dashboards`; at runtime sidecar/Grafana log errors and the dashboard is missing | one dashboard missing | none | revert/fix PR |
| `sc-pg` unreachable or `grafana_ro` password mismatch | SQL panels show errors; `sc-dashcheck` fails | Economics SQL panels empty; Prometheus panels work | none | `monitoring-failure.md` §Grafana datasources |
| Required view missing (plan 32/26 migration lag) | SQL error `relation does not exist`; 39-AC-12 | panel error | none | deploy the migration |
| Prometheus down | panels "No data" | none beyond visibility | — | `monitoring-failure.md` |
| Expensive query | `statement_timeout` cancels after 10 s; Prometheus query timeout 2 m | panel error | — | optimize the view / recording rule via PR |
| Grafana restart | — | preferences, stars and scratch dashboards lost | — | accepted (§6) |
| Upstream dashboard references a metric that no longer exists after an upgrade | `sc-dashcheck` warning for upstream panels | empty panels | — | Renovate PR re-vendors |

## 16. Retry behavior

Grafana retries datasource queries per panel refresh. The sidecar watches ConfigMaps continuously. Flux retries the HelmRelease per plan 18 defaults. No custom retry logic.

## 17. Idempotency behavior

Provisioning is declarative and repeated at every start: the same Git content yields the same datasources, folders and dashboards with the same UIDs. Restarting Grafana is always safe.

## 18. Bootstrap behavior

Not in the seed set. Arrives with the `monitoring` layer after control transfer, before `verify`. OIDC works once the Authentik layer and `authentik-sso-*` steps have run (P07); before that, only the break-glass admin can log in (used by no automated step). `./bootstrap verify` V-22 checks dashboards (§23). Black start: identical; Grafana returns empty-stateful by design.

## 19. Upgrade behavior

- Renovate PRs bump the chart and image. CI renders the chart, runs `make lint-dashboards` and, in k3d, starts the new Grafana with all provisioned dashboards and asserts zero provisioning errors (`GET /api/search` count equals the file count; Grafana log has no `failed to provision`).
- Grafana major upgrades can migrate dashboard schema in memory; committed JSON is not rewritten automatically. A follow-up PR re-exports changed JSON if the lint's `schemaVersion` floor requires it.
- Version-sensitive behaviors re-verified per upgrade: `[unified_alerting] enabled=false` honored; `/login?disableAutoLogin=true` path; `grafana_api_login_post_total` metric; PostgreSQL datasource type name.

## 20. Recovery behavior

Nothing to restore. After any loss Grafana is redeployed by Flux and re-provisions from Git. The break-glass credential is in Git (SOPS) and in the recovery bundle (plan 15 §14.4.4).

## 21. Observability

- Metrics: Grafana `/metrics` (`job=grafana`): `grafana_http_request_duration_seconds`, `grafana_api_login_post_total`, `grafana_api_login_oauth_total`, `grafana_datasource_request_total` (verify names).
- Alerts (plan 42): `SCGrafanaDown`, `SCGrafanaLocalLogin`, `SCOIDCLoginBroken` (app `grafana`).
- Logs: stdout (kubectl logs; Loki at H03 — the Grafana log line with `Successful Login` for `sc-breakglass` is also captured there as secondary evidence).

## 22. Testing strategy

- **`make lint-dashboards` (CI, offline, every PR touching `gitops/platform/grafana/**`):** JSON parses; every dashboard has a stable `uid` matching its file name and a folder annotation from §7.3; UIDs unique; datasource references only §7.2 UIDs; no `__inputs`; `editable: false`; every PromQL target parses (`promtool`'s parser via `sc-alertgen promql-parse`) and every metric name exists in `observability/metric-catalog.yaml`, the recording rules (plan 42 §8.3) or the upstream exporter allowlist; every SQL target parses (`pg_query` parser) and references only relations in the §7.4 allowlist; money panels use unit `currencyUSD` and divide micro-USD by `1e6`; every `count(...)`, `ALERTS{...}` and `increase(...)` stat or table target ends with `or vector(0)` so that "nothing happened" renders 0 instead of no data (GC-11); every panel has an `availableFrom` DAG node and an `alwaysPresent` flag in `dashboards/spec-coverage.yaml`; the §7.3 SPEC-item columns map to at least one panel each (a manifest `dashboards/spec-coverage.yaml` is checked against the SPEC §92 list).
- **Migration test (plan 32/26 test DB):** `grafana_ro` can `SELECT` every §7.4 view and gets `permission denied` on `runner.tasks`, `ledger.cost_records`, `ledger.gateway_tokens`, `ledger.reservations` and `ledger.denials`; `SET default_transaction_read_only` is effective (`INSERT` fails).
- **k3d (`make e2e-grafana`):** Grafana + Prometheus + a fake `/metrics` emitter with the catalogue's `sc_*` families (generated fixture) + a PostgreSQL with the views seeded from plan 32's `testdata/ledger-seed.sql`. `sc-dashcheck --all` runs every panel query through `POST /api/ds/query` (k3d uses a provisioned test-only admin session via the login form) and requires a valid frame for every panel and data for every panel in the k3d fixture (all sources are synthetic there, so data is deterministic). Provisioning errors in the log fail the test.
- **Live (P09/C11):** §23.

## 23. Acceptance criteria

`GF` = `./bootstrap exec -- sc-dashcheck curl https://grafana.<d>`: the checker logs in through OIDC as plan 15's synthetic verification user `synthetic-verifier` in group `sc-synthetic`, mapped to the Grafana **Viewer** role (headless flow of 15-AC-13). Viewer is enough: every check only lists dashboards, runs panel queries (`POST /api/ds/query`) and reads datasource health and frontend settings (verify the health endpoint's permission on the pinned version) and reuses the session cookie. Basic auth is disabled (AUTH-8); break-glass form login is tested only in 39-AC-7.

### GATE-P09

- **39-AC-1** `./bootstrap exec -- flux get helmreleases -n monitoring grafana` → `Ready=True`; `kubectl -n monitoring get pvc -l app.kubernetes.io/name=grafana` returns no PVC.
- **39-AC-2** `GF/api/datasources/uid/sc-prometheus/health` returns `"status":"OK"`, `GF/api/frontend/settings` lists no datasource of type `alertmanager` (readable by Viewer; `GET /api/datasources/uid/…` needs `datasources:read`, which Viewer lacks); `GF/api/datasources/uid/sc-pg/health` returns OK (the role can connect even before C11 views have data).
- **39-AC-3** `GF/api/search?folderUIDs=sc-platform,sc-operations` returns exactly the UIDs `sc-platform-overview, sc-nodes, sc-etcd, sc-cnpg, sc-longhorn, sc-forgejo, sc-authentik, sc-flux, sc-woodpecker, sc-ingress-certs, sc-backups-dr, sc-monitoring-health, sc-alerts`.
- **39-AC-4** `./bootstrap exec -- sc-dashcheck --folder sc-platform --folder sc-operations --available-at P09` exits 0. Semantics (GC-11): every panel query **executes without error and returns a valid frame**; data presence is required only for panels flagged `alwaysPresent: true` in `dashboards/spec-coverage.yaml` (node, etcd, CNPG, Longhorn, Forgejo, Authentik, Flux, Prometheus series). Panels whose source is delivered by a later node (`availableFrom: P10|P08|C04…` in the same manifest, e.g. restore-test and verification panels) are skipped at P09 and checked by 39-AC-16 (GC-01).
- **39-AC-5** `GF/api/frontend/settings | jq .unifiedAlertingEnabled` = `false` (single predicate, GC-22).
- **39-AC-6** OIDC: the plan 15 headless OIDC login test (15-AC-13) succeeds against Grafana, and a user without an `sc-*` group is refused.
- **39-AC-7** Break-glass: an interactive local login (`POST /login` with `sc-breakglass`) succeeds with Authentik scaled to 0, and `ALERTS{alertname="SCGrafanaLocalLogin"}` fires within 2 min.
- **39-AC-8** Statelessness: `kubectl -n monitoring delete pod -l app.kubernetes.io/name=grafana`; within 3 min 39-AC-2 and 39-AC-3 pass again.
- **39-AC-9** `make lint-dashboards` passes on the P09 merge commit; a fixture dashboard referencing metric `sc_runner_task_id_total` or table `runner.tasks` makes it fail (RED proof).

### GATE-P12 (contribution, re-check)

- **39-AC-16** `sc-dashcheck --folder sc-platform --folder sc-operations` (no `--available-at`) exits 0 with the GC-11 semantics after V-25/V-37 restore tests and the Woodpecker cutover have run, covering the panels skipped at P09.

### GATE-C11

- **39-AC-10** `GF/api/search?folderUIDs=sc-company,sc-economics` returns exactly `sc-orchestration, sc-tdd, sc-workers, sc-gateway, sc-economics, sc-economics-drilldown`.
- **39-AC-11** After the C10 fake end-to-end suite on the live cluster: `sc-dashcheck --folder sc-company --folder sc-economics` exits 0 with the GC-11 semantics of 39-AC-4, and every `alwaysPresent` panel there (queue depth, executions, tasks by state, spend gauges, ledger completeness) returns data.
- **39-AC-12** As `grafana_ro`: `SELECT 1 FROM runner.tasks LIMIT 1` → `permission denied`; `SELECT count(*) FROM ledger.v_cost_per_green_task` succeeds; `INSERT INTO ledger.cost_records …` → error (read-only transaction or permission).
- **39-AC-13** "Spend today" consistency: the panel's value (via `/api/ds/query`) equals `SELECT sum(budget_charge_micro_usd) FROM ledger.cost_records WHERE started_at >= date_trunc('day', now() AT TIME ZONE 'UTC')` divided by 1e6, within one Gateway recompute interval (60 s); with an unpriced fixture row present the panel's incomplete overlay is shown (`sc_ledger_incomplete_rows{period="today"} > 0`).

### GATE-H03

- **39-AC-14** On small-ha: `GF/api/datasources/uid/sc-loki/health` and `…/sc-tempo/health` return OK; opening the trace link of a task row in `sc-orchestration` resolves to a Tempo trace whose root span is `sc.task` with matching `sc.task_id` (plan 41 41-AC-* provides the trace).

### GATE-P12 (contribution)

- **39-AC-15** `verify.json` V-22 includes `grafana: 13 platform/operations dashboards provisioned, 0 provisioning errors`.

## 24. Explicit non-goals

- Grafana alerting, contact points, or Grafana-managed recording rules (ADR-0009).
- Persisting Grafana state; HA Grafana.
- Third-party Grafana plugins.
- Per-task or per-execution metric panels (task-level data comes from SQL views or the Runner API).
- Public dashboards, snapshots or anonymous access.

## 25. Open questions

| # | Question | Blocking? | Proposed resolution | Owner |
|---|---|---|---|---|
| Q-39-1 | Plan 32 names the datasource `sc-ledger` and grants only ledger views; this plan needs Runner reporting views too and names it `sc-pg`. | no | Use `sc-pg`; plan 32 wraps Q1–Q10/Q-S as the §7.4 views; plan 26/29 add the `reporting` schema views; `grafana_ro` is a member of `sc_reporting`. | 32 / 26 / 39 |
| Q-39-2 | `sslmode: require` does not verify the CNPG server certificate. | no | Accept for MVP (read-only role, NetworkPolicy). Post-MVP: plan 10 distributes the `sc-pg` CA to `monitoring` (trust-manager or a SOPS copy) and the datasource moves to `verify-full`. | 10 / 39 |
| Q-39-3 | The Grafana chart repository location and the `preinstall_disabled` key are version-sensitive. | no | Selection rule plus the upgrade re-verification list (§19). | 39 |
| Q-39-4 | Several Woodpecker, Longhorn, Authentik and cloudflared metric names are marked (verify). | no | `sc-dashcheck` in k3d (synthetic data for every series) fails on empty panels; the dashboard is corrected in the same PR that pins the component version. | 39 |
| Q-39-5 | The Grafana break-glass password is in the cluster (SOPS → Secret) because Grafana is rebuilt from Git; SPEC §22 says break-glass credentials live outside the cluster (AUTH-15). | no | Keep, as a recorded SPEC interpretation for the lead to add to `spec-issues.md` with the compensating controls now in place: basic auth disabled, no Alertmanager datasource (no silence path), form login counted by `SCGrafanaLocalLogin` (security-audit class, silence-audited). Grafana holds no production write path. Operator acceptance required. | lead / 15 |
| Q-39-6 | Plan 15 (§`55-synthetic.yaml`, Q-15-12, `ROLE_ATTRIBUTE_PATH`) maps `sc-synthetic` to Grafana Editor and does not list it among the `grafana` application's allowed groups. | no | Coordinator decision (cross-group round): **Viewer**, because the synthetic verifier only reads. Plan 15 adds `sc-synthetic` to the `grafana` application groups and `contains(groups[*], 'sc-synthetic') && 'Viewer'` to the role expression, and changes "Editor" to "Viewer" in 55-synthetic and Q-15-12. | 15 |
