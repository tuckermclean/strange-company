# 42 — Alerting and human notifications

**Status:** Stage-0 plan (draft)
**DAG nodes:** P09 (primary, with 38, 39, 40), C06 (co-owner with 33: Runner-posted notifications, budget stop), C11 (primary, with 39), P10 (contributes: backup and restore-test alerts), H03 (contributes: telemetry-stack alerts), H04 (contributes: HA Alertmanager), P12 (contributes: verify check V-24)
**Related plans:** 06, 07, 08, 11, 12, 13, 14, 15, 16, 17, 18, 20, 26, 29, 31, 32, 33, 35, 36, 38 (signals), 39 (alert panels), 40, 41, 43, 44, 45, 50 (preflight PF-17/PF-18, verify V-24, report), 51
**Contracts:** owns `contracts/alert-catalog.yaml` + `alert-catalog.schema.yaml`, `contracts/notification.schema.yaml`; consumes the metric catalogue (plan 38 §21.2)
**ADRs:** ADR-0009 (binding), ADR-0550, ADR-0553, ADR-0555 (introduced by the G9 group)

## 1. Purpose

Dashboards alone are insufficient [SPEC §96]. This plan turns every condition SPEC §96 lists, plus the platform essentials that keep the system recoverable, into **machine-checked alerts** with one owner, one route and one runbook. It routes the human-required ones to an **actual notification** [SPEC §97] through one hub, Alertmanager (ADR-0009). It defines:

- the alert catalogue (`contracts/alert-catalog.yaml`, §23.5 renders it as a table) and how it is rendered into PrometheusRules;
- the Alertmanager routing tree, inhibition, grouping and repeat intervals, including the always-notify classes;
- the transports and their secrets;
- the Runner → Alertmanager API path for organizational events;
- the external dead-man's switch and the behavior when none is configured;
- the definition of *black-start-threatening*;
- notification testing at P09 and later gates.

## 2. Requirements derived from SPEC.md

1. Alerts exist for at least: node failure, disk pressure, etcd degradation, CNPG failure, failed backups, failed restore tests, degraded Longhorn, Forgejo failure, Authentik failure, GitHub mirror failure, Flux failure, Woodpecker failure, Agent Runner failure, task exhaustion, TDD pipeline failure, budget thresholds, anomalous API spend [SPEC §96]. Mapping in §2.1.
2. Human-required conditions result in actual notifications. At least one mechanism is configurable (email, Matrix, ntfy, Slack, Discord, generic webhook) [SPEC §97].
3. Notifications include at least: `Needs Human`, budget stop, failed backup, critical platform outage, failed mirror, black-start-threatening condition [SPEC §97].
4. Budget exhaustion fails safely and produces visible human notification [SPEC §81].
5. Restore-test failure is visible and alertable [SPEC §109]. Mirror failure is visible [SPEC §32].
6. Telemetry, including notification text, captures no prompts, completions, code, environment variables or secret-bearing output [SPEC §94].
7. Task IDs and similar identifiers do not become metric labels [SPEC §91].
8. Break-glass use produces audit evidence where practical [SPEC §22].

Derived requirements:
- **42-R-1** Every alert the platform can raise has exactly one catalogue entry. Rendered PrometheusRules equal the catalogue (no hand-written rule exists outside it, except upstream kube-prometheus-stack groups listed in `spec.upstreamRules`).
- **42-R-2** An alert whose `alwaysNotify` is not `none` is never routed to the `silent` class and is never the target of an inhibit rule. Only a human silence (audited, §9.4) can mute it.
- **42-R-3** Every catalogue alert has a runbook path that exists in the repository and a `promtool` unit test (rule alerts) or a k3d test (posted alerts).
- **42-R-4** Exactly one hub: Grafana alerting is disabled (plan 39), Loki/Tempo rulers send only to Alertmanager (plan 41), and no component sends human notifications directly.
- **42-R-5** A transport is mandatory at bootstrap and is tested by preflight (PF-17) before any paid resource is created.
- **42-R-6** Total outage (cluster, node, Prometheus or Alertmanager down; no egress) is detected outside the cluster by the dead-man's switch. If none is configured, that absence is itself alerted and reported.
- **42-R-7** At small-ha the alerting path survives the loss of any one node.

### 2.1 SPEC §96 coverage

| SPEC §96 condition | Catalogue alerts (§23.5 has every field) |
|---|---|
| node failure | `SCNodeDown`, `SCNodeMemoryPressure`, `SCNodeOOMKill`, `SCNodeClockSkew` |
| disk pressure | `SCNodeDiskPressure`, `SCNodeRootDiskLow`, `SCNodeRootDiskCritical`, `SCLonghornNodeStorageLow`, `SCCnpgDatabaseLarge`, `SCCnpgWalReadyBacklog*` |
| etcd degradation | `SCEtcdNoLeader`, `SCEtcdMemberMissing`, `SCEtcdDbSizeHigh`, `SCEtcdSlowDisk`, `SCEtcdSnapshotUploadFailing`, `SCEtcdSnapshotMissing` |
| CNPG failure | `SCCnpgClusterDown`, `SCCnpgClusterDegraded`, `SCCnpgInstanceNotReady`, `SCCnpgReplicationLagHigh`, `SCCnpgSyncReplicationDegraded`, `SCCnpgOperatorDown` |
| failed backups | `SCCnpgWalArchiveFailing`, `SCCnpgWalArchiveBacklog`, `SCCnpgBaseBackupStale`, `SCCnpgBackupFailed`, `SCCnpgPluginDown`, `SCLonghornBackupStale`, `SCRepoBundleBackupStale`, `SCEtcdSnapshotUploadFailing`, `SCOffsiteBackupFailing24h` |
| failed restore tests | `SCRestoreTestCnpgFailed`, `SCRestoreTestReposFailed`, `SCRestoreTestLonghornFailed`, `SCRestoreTestEtcdFailed`, `SCRestoreTestAppsFailed`, `SCTofuStateRestoreTestFailed`, `SCGitHubBlackStartTestFailed`, `SCRecoveryBundleVerifyFailed`, `SCRecoveryDrillOverdue` |
| degraded Longhorn | `SCLonghornVolumeDegraded`, `SCLonghornVolumeFaulted`, `SCLonghornVolumeNearlyFull` |
| Forgejo failure | `SCForgejoDown`, `SCForgejoSSHDown`, `SCForgejoPodStuckPending(Critical)`, `SCForgejoSSHHostKeyMismatch`, `SCForgeBotTokenInvalid`, `SCForgejoObjectStorageErrors` |
| Authentik failure | `SCAuthentikDown`, `SCAuthentikWorkerDown`, `SCOIDCLoginBroken`, `SCAuthentikTasksFailing`, `SCAuthentikBlueprintFailed` |
| GitHub mirror failure | `SCProjectMirrorFailing` (project mirrors, CD-43), `SCGitHubMirrorFailing`, `SCGitHubMirrorStale`, `SCGitHubMirrorRPOBreach`, `SCGitHubMirrorDiverged`, `SCGitHubMirrorNotConfigured`, `SCGitHubMirrorSignalMissing` |
| Flux failure | `SCFluxReconciliationFailing`, `SCFluxSourceFailing`, `SCFluxSourceNotReady`, `SCFluxControllerDown`, `SCFluxReconcileStalled`, `SCFluxSuspendedTooLong` |
| Woodpecker failure | `SCWoodpeckerDown`, `SCWoodpeckerNoAgents`, `SCWoodpeckerBacklog` |
| Agent Runner failure | `SCRunnerDown`, `SCRunnerNotReady`, `SCAgentRunnerReconcileStalled`, `SCRunnerDatabaseDown`, `SCRunnerPolicyInvalid`, `SCRunnerOutboxStalled`, `SCExecutionStuck` |
| task exhaustion | `SCTaskRetriesExhausted`, `SCTaskNeedsHuman`, `SCNeedsHumanBacklog`, `SCTaskStuck` |
| TDD pipeline failure | `SCTddPipelineFailure`, `SCTddVerificationStalled`, `SCTddProvenanceBypass`, `SCProvenanceStatusForged` (CD-07), `SCProtectedBranchDirectPush`, `SCWorkerPolicyViolation`, `SCTaskEventRejected`, `SCTaskInvariantViolation` |
| budget thresholds | `SCBudgetThresholdReached`, `SCBudgetExhausted`, `SCBudgetHourlyCapReached`, `SCBudgetCostIncomplete`, `SCBudgetMetricsStale`, `SCEmergencyStopActive`, `SCEmergencyStopNotApplied` |
| anomalous API spend | `SCAnomalousSpend`, `SCGatewayBoundViolation`, `SCLedgerProviderDivergence`, `SCLedgerProviderUndercount`, `SCModelMismatch` |

Platform essentials named in the assignment: certificate expiry (`SCCertificateExpiringSoon`, `SCCertificateExpiryImminent`, `SCK3sCertificateExpiringSoon`), Watchdog, Gateway down (`SCModelGatewayDown`), orphaned deploy keys (`SCRunnerOrphanDeployKeys`), break-glass used (`SCBreakGlassUsed`, `SCK3sBreakGlassAuditSeen`, `SCGrafanaLocalLogin`), model mismatch (`SCModelMismatch`), emergency stop active (`SCEmergencyStopActive`), WAL backlog (`SCCnpgWalArchiveBacklog`, `SCCnpgWalReadyBacklogHigh/Critical`), object storage unreachable (`SCObjectStorageUnreachable`, `SCObjectStorageCanaryFailing`), etcd snapshot upload failing (`SCEtcdSnapshotUploadFailing`).

## 3. Inputs

| Input | Source | Notes |
|---|---|---|
| Alert catalogue | `contracts/alert-catalog.yaml` | authoritative; changed by PR |
| Metric names | plan 38 §21.2 metric catalogue + emitter plans | `make lint-alerts` checks every metric referenced in an `expr` against `observability/metric-catalog.yaml` plus the upstream exporter allowlist |
| Notification transport | `bootstrap.env` `NOTIFY_TRANSPORT`, `NOTIFY_WEBHOOK_URL`, `NOTIFY_NTFY_TOKEN`, `NOTIFY_SMTP_*` (plan 50 §9.2) | secret parts → `SC-NOTIFY-TRANSPORT` |
| Heartbeat URL | `bootstrap.env` `HEARTBEAT_URL` (optional, warned) | `SC-NOTIFY-TRANSPORT` instance `heartbeat` |
| Public URLs | `BASE_DOMAIN` (runbook and dashboard links), `GITHUB_MIRROR_REPO` (fallback runbook links) | |
| Profile | cluster config | selects `profiles` per alert |
| Check CronJobs | plans 06, 11, 14, 35, 44, 45, 42 (§7.6) | ADR-0553 |
| Organizational events | Runner (plan 26) per `notification.schema.yaml` | |

## 4. Outputs

- **Rendered rules** (committed, regenerated by `make render-alerts`; CI fails on diff): `gitops/platform/monitoring/rules/sc-<category>.yaml`, one `PrometheusRule` per category (group name `sc-<category>`, or `ruleGroup` where set, e.g. `flux-alerts` for plan 18's 18-AC-14), labels `strange.company/monitoring: "true"`, and `gitops/platform/monitoring/rules/sc-recording.yaml` (§8.3).
- **Rule unit tests:** `gitops/platform/monitoring/rules/tests/<alertname>.test.yaml` (one per rule-based alert; hand-written, existence lint-enforced).
- **Alertmanager configuration:** `gitops/clusters/<cluster>/monitoring/alertmanager.yaml`, rendered per installation by `sc-alertgen --cluster <cluster>` from the catalogue plus `gitops/clusters/<cluster>/config/notifications.yaml`, delivered by a kustomize `secretGenerator` as Secret `monitoring/alertmanager-sc-config` (key `alertmanager.yaml`, `disableNameSuffixHash: true`) and referenced by `alertmanager.alertmanagerSpec.configSecret` (verify key on the pinned chart). It contains no secret values (every credential is a `*_file` reference, §9.2).
- **Notification templates:** `gitops/platform/monitoring/alertmanager/templates/sc.tmpl`, mounted from ConfigMap `alertmanager-sc-templates`.
- **Static info rule:** `sc_notification_heartbeat_configured` (recording rule `vector(1)` or `vector(0)`, rendered from `notifications.yaml`).
- **`silence-audit`** CronJob (namespace `monitoring`, §9.4).
- **Verify check V-24** registered with plan 50; preflight checks PF-17 and PF-18.

## 5. Dependencies

**Hard (DAG):** P09 depends on P06 (GitOps from Forgejo) and P07 (Authentik, for the forward-auth Alertmanager UI). C06 depends on C05 and P09. C11 depends on C06 and P09.

**Soft/runtime:**
- Plan 38: Prometheus, Alertmanager (`kps-alertmanager`, 1 replica minimum / 2 small-ha), KSM (CronJob and ConfigMap series), blackbox probes.
- Plan 26: posts organizational alerts (§7.4), exposes `sc_runner_*`.
- Plan 33: emits `sc_budget_*`, `sc_emergency_stop_level` and `sc_emergency_stop_active` (plan 33 §21; expressions of the budget alerts are normative there and copied verbatim into the catalogue; the anomaly rule's thresholds are rendered from `policy/budgets.yaml`, and `make check-rules` fails on drift).
- Plan 36: NetworkPolicy — ingress to Alertmanager 9093 from Prometheus, the Runner (`strange-company/agent-runner`), the `silence-audit` CronJobs, and (H03) Loki/Tempo rulers; **not** from Grafana (plan 39 has no Alertmanager datasource, AUTH-8); egress from Alertmanager to the transport endpoint (`0.0.0.0/0:443` and SMTP port) and to the heartbeat URL.
- Plan 15: Authentik forward-auth on `alertmanager.<BASE_DOMAIN>` (human UI); `platform-probes` metrics.
- Plan 50: preflight PF-17/18, verify V-24, report lines.
- Plan 44 (and 06, 11, 14, 35, 45): check CronJobs named in the catalogue.

## 6. Owned state

| State | Location | Writer | Notes |
|---|---|---|---|
| Alert catalogue, templates, routing intent | Git (`contracts/`, `gitops/platform/monitoring/`) | PR + human merge | authoritative |
| Per-installation notification config | Git `gitops/clusters/<c>/config/notifications.yaml` (non-secret) + SOPS `gitops/clusters/<c>/secrets/monitoring/alertmanager-transports.sops.yaml` | bootstrap (render), then PR | |
| Active alerts | Alertmanager memory | Prometheus, Runner, bootstrap/operator test posts | ephemeral; re-sent by Prometheus every `evaluation_interval` and re-posted by the Runner every 5 min |
| Silences, notification log | Alertmanager local-path PVC (plan 38 §11; register S-25) | humans; Alertmanager | reconstructible; loss may cause one duplicate notification |
| Runner notification state | `sc-pg` table `notification_state` (plan 26) | Runner | which task alerts were posted, so a restart can resolve stale ones |

## 7. External interfaces

### 7.1 Alert labels and annotations (contract)

Every Strange Company rule carries labels `alertname`, `severity` (`critical|warning|info`), `category`, `notify_class` (`page|notify|silent|heartbeat`), `always_notify` (`none` or a §7.2 reason), `source` (`sc` for rules, `upstream` for chart rules, or the poster for posted alerts). Rules may keep label dimensions from their `expr` (`namespace`, `cluster`, `node`, `provider`, `dep`, …); none of them is a task, execution or commit identifier (enforced by the plan 38 label vocabulary and `make lint-alerts`).

Annotations rendered for every rule: `summary` (catalogue text), `description` (catalogue-provided Go template over labels and `$value`, never over free text), `runbook_url` = `https://git.<BASE_DOMAIN>/strange-company/strange-company/src/branch/main/<runbook>`, `runbook_mirror_url` = `https://github.com/<GITHUB_MIRROR_REPO>/blob/main/<runbook>` (readable when Forgejo is down), `dashboard_url` (category → Grafana dashboard UID, plan 39 §7.3), `catalogue_ref` = `contracts/alert-catalog.yaml#<alertname>`.

### 7.2 Notification classes and always-notify reasons

| Class | `external` | group_wait / group_interval / repeat_interval | send_resolved | Used for |
|---|---|---|---|---|
| `page` | yes, high priority | 30s / 5m / 4h | yes | every `critical`; schema forces `severity: critical ⇒ page` |
| `notify` | yes, normal priority | 2m / 10m / 24h | yes | warnings that a human must act on |
| `silent` | no (Alertmanager UI and Grafana "Firing alerts" panel only) | 5m / 30m / 7d | no | info and housekeeping |
| `heartbeat` | yes, to the dead-man's switch only | 0s / 1m / 1m | no | `Watchdog` only |

**Always-notify reasons** (SPEC §97). An alert whose `alwaysNotify` is one of these is `page` or `notify` (schema-enforced), never inhibited (lint-enforced), and its silences are audited (§9.4):

| SPEC §97 item | Reason | Alerts (examples; full list in §23.5) |
|---|---|---|
| `Needs Human` | `needs-human` | `SCTaskNeedsHuman`, `SCTaskRetriesExhausted` (Runner-posted), `SCTddProvenanceBypass`, `SCRunnerNotificationPostFailing` |
| budget stop | `budget-stop` | `SCBudgetExhausted`, `SCEmergencyStopActive`, `SCEmergencyStopNotApplied`, `SCAnomalousSpend`, `SCLedgerWriteFailing`, `SCEmergencyStopSourceUnavailable` |
| failed backup | `failed-backup` | `SCCnpgWalArchiveFailing`, `SCCnpgWalArchiveBacklog`, `SCCnpgBaseBackupStale`, `SCCnpgBackupFailed`, `SCLonghornBackupStale`, `SCRepoBundleBackupStale`, `SCEtcdSnapshotUploadFailing`, `SCRestoreTest*Failed` |
| critical platform outage | `critical-outage` | `SCNodeDown`, `SCEtcdNoLeader`, `SCCnpgClusterDown`, `SCForgejoDown`, `SCAuthentikDown`, `SCWoodpeckerDown`, `SCRunnerDown`, `SCModelGatewayDown`, `SCEdgeDown`, `SCFluxSourceFailing`, `SCAlertmanagerNotificationsFailing` |
| failed mirror | `failed-mirror` | `SCGitHubMirrorFailing`, `SCGitHubMirrorStale`, `SCGitHubMirrorSignalMissing`, `SCGitHubMirrorTokenExpiring` |
| black-start-threatening condition | `black-start-threat` | §7.5 |
| (Stage-0 addition, AUTH-8) break-glass use and its detection | `security-audit` | `SCBreakGlassUsed`, `SCGrafanaLocalLogin`, `SCK3sBreakGlassAuditSeen`, `SCBreakGlassAlertSilenced`, `SCCIBuildLaneAgentMismatch` |

### 7.3 Alertmanager routing tree (rendered; normative structure)

Baseline: Alertmanager v0.34.x and Prometheus/promtool v3.15.x (kube-prometheus-stack 92.x; `plans/compatibility-baseline.yaml`, CD-29). Version-sensitive here: `time_intervals`/`mute_time_intervals` syntax, `*_file` integration fields (42-AC-5) and the `receiver_name` label on notification metrics (42-AC-6), all re-verified on that line.

```yaml
global:
  resolve_timeout: 5m
templates: [/etc/alertmanager/config/sc.tmpl]          # mounted ConfigMap
route:
  receiver: silent                                    # default: nothing leaves Alertmanager
  group_by: [alertname, category]                      # never subject/task_id/pod/instance (schema)
  group_wait: 5m
  group_interval: 30m
  repeat_interval: 7d
  routes:
    - {matchers: ['alertname="Watchdog"'], receiver: heartbeat, group_wait: 0s, group_interval: 1m, repeat_interval: 1m, continue: false}
    # self-hosting-minimum only (GC-20): the four delivery routes below are rendered a first time with the extra
    # matcher 'always_notify=~"|none"' (no reason, or upstream without the label) and
    # mute_time_intervals: [sc-node-maintenance]; always-notify alerts never match those copies (42-R-2)
    - {matchers: ['notify_class="page"'],   receiver: human-page,   group_wait: 30s, group_interval: 5m,  repeat_interval: 4h}
    - {matchers: ['notify_class="notify"'], receiver: human-notify, group_wait: 2m,  group_interval: 10m, repeat_interval: 24h}
    # upstream kube-prometheus-stack rules carry no notify_class: severityFallback
    - {matchers: ['source="upstream"', 'severity="critical"'], receiver: human-page,   group_wait: 30s, group_interval: 5m,  repeat_interval: 4h}
    - {matchers: ['source="upstream"', 'severity="warning"'],  receiver: human-notify, group_wait: 2m,  group_interval: 10m, repeat_interval: 24h}
    # safety net: anything carrying an always-notify reason that matched nothing above
    - {matchers: ['always_notify=~".+"', 'always_notify!="none"'], receiver: human-page, group_wait: 30s, group_interval: 5m, repeat_interval: 4h}
inhibit_rules: <rendered from spec.routing.inhibitRules>
time_intervals:          # self-hosting-minimum only; rendered from cluster-vars SC_MAINTENANCE_WINDOW (= plan 08 node_maintenance_window)
  - name: sc-node-maintenance
    time_intervals: [{weekdays: [sunday], times: [{start_time: "04:00", end_time: "06:00"}], location: UTC}]
receivers:
  - name: silent                                      # no integrations
  - name: heartbeat
    webhook_configs: [{url_file: /etc/alertmanager/secrets/alertmanager-transports/heartbeat-url, send_resolved: false, max_alerts: 1}]
  - name: human-page     # one integration per configured transport, high priority (§9.2)
  - name: human-notify   # same transports, normal priority
```

- Grouping: `[alertname, category]`. Runner task alerts differ only by the `subject` label, so a burst of NEEDS_HUMAN entries is one message per `group_interval` that lists every task (ADR-0555).
- Repeat: a firing `page` alert repeats every 4 h until resolved or silenced; `notify` every 24 h. `SCNeedsHumanBacklog` (72 h) is the extra reminder for long-waiting human decisions.
- **Inhibition** (from the catalogue): `notifications-failing-stops-heartbeat`; `node-down-inhibits-node-symptoms` (`equal: [node]`); `forgejo-down-inhibits-forgejo-symptoms`; `edge-down-inhibits-public-endpoints`; the upstream `critical`→`warning`→`info` same-alert rules; `InfoInhibitor`. Every target matcher list includes `always_notify="none"` except the Watchdog rule. **Deliberately not adopted:** plan 12's request to inhibit CNPG backup alerts while object storage is down. Those are `failed-backup` alerts, which SPEC §97 requires to notify. They are grouped instead, so an S3 outage produces one `SCObjectStorageUnreachable` page plus one grouped `failed-backup` message per alertname.
- **Maintenance mute (GC-20).** On self-hosting-minimum the weekly `sc-maintenance` reboot (plan 08 §9.9) causes 3–6 min of total downtime and would page for edge, Forgejo and Runner symptoms. Alerts without an always-notify reason are muted (not silenced) during `sc-node-maintenance`; if a symptom still fires after the window ends it is delivered then. Always-notify alerts are never muted, so a failed backup, a lost mirror or a node that does not come back still notifies. small-ha renders no mute interval (kured drains one node at a time). Pausing dispatch before the reboot is plan 08/26's part of GC-20.
- Upstream chart alerts: `defaultRules` per `spec.upstreamRules` (groups kept or disabled, alerts disabled with `replacedBy`), with added label `source: upstream` (plan 38 §7.1).

### 7.4 Runner → Alertmanager API path (organizational events)

Contract: `contracts/notification.schema.yaml`. Only alertnames with `source.kind: runner-post` (`SCTaskNeedsHuman`, `SCTaskRetriesExhausted`) may be posted by the Runner; `bootstrap`/`operator` may post `SCNotificationTest` only.

1. **Trigger.** A task enters `NEEDS_HUMAN` (plan 29). The Runner computes `alertname` = `SCTaskRetriesExhausted` if the transition reason is `retries_exhausted`, otherwise `SCTaskNeedsHuman`, and `reason` from the bounded enum. It writes an outbox intent `alert.post` in the same transaction as the transition (plan 26 outbox).
2. **Payload.** Labels: `alertname`, `severity: warning`, `category: task`, `notify_class: notify`, `always_notify: needs-human`, `source: runner`, `subject: <task_id>`, `reason`. Annotations: `summary`, `description` (built from structured fields only: state, reason, attempt, AC IDs; never handoff text, prompts or logs), `task_id`, `task_title` (≤ 200 chars, from the task record, which humans authored), `repository`, `attempt`, `state`, `task_url` (Forgejo issue or PR), `runbook_url`, `event_id`. `startsAt` = transition timestamp; `endsAt` = now + 6 h.
3. **Fan-out.** The Runner resolves the headless Service `alertmanager-operated.monitoring.svc` and POSTs `/api/v2/alerts` to **every** replica (Alertmanager replicas gossip notification logs and silences, not alerts). Success = HTTP 200 from at least one replica; each attempt increments `sc_runner_alert_posts_total{alertname, result}`.
4. **Refresh.** Every 5 min the Runner re-posts the identical label set for every task still in `NEEDS_HUMAN`, extending `endsAt`. This restores alerts after an Alertmanager restart.
5. **Resolve.** When the task leaves `NEEDS_HUMAN`, the Runner posts the same labels with `endsAt = now`. Alertmanager sends the resolved message (`send_resolved: true`).
6. **Restart reconciliation.** On startup the Runner reads `notification_state` and the set of tasks in `NEEDS_HUMAN` and posts firing or resolved updates so the two agree.
7. **Retries.** Per cycle: up to 5 attempts per replica, backoff 1, 2, 4, 8, 16 s. If every post fails for 10 min, Prometheus raises `SCRunnerNotificationPostFailing` (page, `needs-human`), which reaches the human through Prometheus's own path.
8. **Runner down.** Posted alerts persist until `endsAt` (≤ 6 h); `SCRunnerDown` pages. A Runner outage never resolves a Needs Human notification early, and after a restart step 6 re-posts.

Budget stop is **rule-based**, not posted: the Gateway exposes `sc_budget_exhausted` and `sc_emergency_stop_level` (plan 33 §14.4, §21), and `SCBudgetExhausted` / `SCEmergencyStopActive` fire within one evaluation interval (≤ 30 s) plus `group_wait` (30 s / 2 m). This is a clarification of ADR-0009, which listed budget stop among Runner-posted events; a gauge already exists, so posting would duplicate it (§25 Q-42-3).

### 7.5 Black-start-threatening conditions (definition)

A condition is **black-start-threatening** when, if total cluster loss happened while it persisted, the plan 45 black-start procedure would fail, lose data beyond the SPEC §108 RPO targets, or need material that is not available. The catalogue marks such alerts `blackStartThreat: true` and `alwaysNotify: black-start-threat` (schema enforces the pairing). Classes:

| Class | Why it threatens black start | Alerts |
|---|---|---|
| Recovery seed (GitHub) | black start clones GitHub `main` (SPEC §111) | `SCGitHubMirrorRPOBreach`, `SCGitHubMirrorDiverged`, `SCGitHubMirrorNotConfigured`, `SCGitHubRepoSettingsDrift`, `SCGitHubMirrorRecoveryModeTooLong`, `SCGitHubBlackStartTestFailed` |
| Off-cluster backups | data restored after total loss comes only from S3 | `SCOffsiteBackupFailing24h`, `SCObjectStorageUnreachable`, `SCObjectStorageCanaryFailing`, `SCObjectStorageConfigDrift`, `SCOciReferencedImageMissing` |
| OpenTofu state | provision/recover needs decryptable, versioned state (SPEC §15) | `SCTofuStateRestoreTestFailed` (includes overdue), `SCObjectStorageConfigDrift` (includes versioning of `tofu-state`) |
| Recovery secrets | the bundle, the passphrase and the offline age key unlock everything (ADR-0012) | `SCRecoveryBundleOutdated`, `SCRecoveryBundleVerifyFailed`, `SCRecoveryKeyAttestationOverdue` |
| Break-glass | needed when Authentik/Forgejo are down during recovery (SPEC §22) | `SCBreakGlassAccountNotReady`, `SCBreakGlassTestOverdue` |
| Proven procedure | an undrilled procedure is unproven (SPEC §109) | `SCRecoveryDrillOverdue` |
| Outage detection | without the external heartbeat, total loss is detected only by a human | `SCDeadMansSwitchNotConfigured` |

### 7.6 Check CronJobs consumed (ADR-0553)

Rendered expression for a `check-cronjob` alert with namespace `N`, CronJob `C`, `failAfter = F`, `maxAge = M` (KSM series):

```promql
(   (kube_cronjob_status_last_schedule_time{namespace="N",cronjob="C"}
     - kube_cronjob_status_last_successful_time{namespace="N",cronjob="C"}) > 0
  and (time() - kube_cronjob_status_last_schedule_time{namespace="N",cronjob="C"}) > F )   # latest run failed or hung
or (time() - kube_cronjob_status_last_successful_time{namespace="N",cronjob="C"}) > M     # stale
or ((time() - kube_cronjob_created{namespace="N",cronjob="C"}) > M
     unless on (namespace, cronjob) kube_cronjob_status_last_successful_time{namespace="N",cronjob="C"})  # never succeeded
or absent(kube_cronjob_created{namespace="N",cronjob="C"})                                # CronJob deleted
```
with `for: 5m` (`for: 1h` on the `absent` branch is folded in by the renderer as a separate guard). `kube_cronjob_status_last_successful_time` is version-sensitive in KSM (verify; 42-AC-12 re-verifies). Owners of the CronJobs: `repo-bundle-backup` (44, ns `forgejo`), `restore-test-*` and `rt-*` (44), `check-tofu-*` (06 via 44 framework), `recovery-bundle-currency`, `restore-test-recovery-bundle`, `recovery-key-attestation` (35), `objstore-canary` (14), `storage-audit` (11), `rt-drill-freshness` (45), `silence-audit` and `silence-audit-breakglass` (42), `dr-marker-heartbeat` (44).

### 7.7 Human surfaces

- Alertmanager UI at `https://alertmanager.<BASE_DOMAIN>` behind Authentik forward-auth (plan 15 §9.3), group `sc-admins`. Break-glass path: `./bootstrap exec -- kubectl -n monitoring port-forward svc/kps-alertmanager 9093`.
- `amtool` (in the toolbox) for silences: `./bootstrap exec -- amtool --alertmanager.url=http://127.0.0.1:9093 silence add …` over the port-forward.
- Grafana "Alerts" dashboard (plan 39) has **no** Alertmanager datasource (AUTH-8, §9.4): it shows `ALERTS` series from Prometheus and Needs Human tasks from `sc-pg` (`reporting.v_tasks_needing_human`).

## 8. Internal interfaces

### 8.1 `sc-alertgen` (Go, `src/cmd/sc-alertgen`, run in the toolbox and in CI)

- `sc-alertgen render rules` → `gitops/platform/monitoring/rules/*.yaml` (deterministic ordering; byte-stable output).
- `sc-alertgen render alertmanager --cluster <c>` → `gitops/clusters/<c>/monitoring/alertmanager.yaml`.
- `sc-alertgen lint` → all checks of §22 (`make lint-alerts`).
- `sc-alertgen test-routes` → for every catalogue entry, builds the label set the rule would emit and runs `amtool config routes test` against the rendered config; asserts the receiver per §7.3.
- `sc-alertgen post-test --category <c> --class <page|notify> --nonce <N>` → posts an `SCNotificationTest` (used by V-24 and `notification-failure.md`).

### 8.2 Rule rendering

`prometheus-rule` → one rule `{alert, expr, for, labels, annotations}`. `check-cronjob` → §7.6 expression. `runner-post`/`external-post` → no rule (posted), but the catalogue entry still drives routing tests. Alerts whose `profiles` exclude the installation's profile are rendered into `sc-<category>-small-ha.yaml`, which only the small-ha profile overlay includes.

### 8.3 Recording rules (owned here; required by plan 38 §21.3 and the catalogue)

| Record | Expression (sketch) |
|---|---|
| `sc:cnpg_ready_instances` | `max by (namespace, cluster) (sc_cnpg_cluster_ready_instances)` |
| `sc:cnpg_desired_instances` | `max by (namespace, cluster) (sc_cnpg_cluster_instances)` |
| `sc:cnpg_primaries` | `sum by (namespace, cluster) (1 - cnpg_pg_replication_in_recovery)` |
| `sc:cnpg_last_backup_success_timestamp_seconds` | `max by (cluster) (sc_cnpg_backup_stopped_timestamp_seconds{phase="completed"})` or the plugin's last-available-backup metric (verify; plan 12 §21) |
| `sc:cnpg_first_recoverability_point_timestamp_seconds` | plugin metric (verify) or KSM CRS of `Cluster.status.firstRecoverabilityPoint` |
| `sc:cnpg_cluster_created_timestamp_seconds` | `max by (namespace, cluster) (kube_customresource_created{customresource_kind="Cluster"})` (verify KSM CRS name) |
| `sc:cnpg_storage_nominal_bytes` | KSM CRS of `Cluster.spec.storage.size` (dashboard only) |
| `sc:cnpg_data_ceiling_bytes` | `vector(<CNPG_DATA_CEILING_BYTES>)` rendered from `cluster-vars` = the profile's CNPG data ceiling `P` of plan 11 §12 (used by `SCCnpgDatabaseLarge`: sum of all clusters on a node > 80 %, STO-3) |
| `sc:longhorn_last_backup_success_timestamp_seconds` | `max by (volume) (sc_longhorn_backup_info{state="Completed"})` |
| `sc:etcd_last_s3_snapshot_timestamp_seconds` | `max(sc_etcd_snapshot_last_success_timestamp_seconds{location="s3"})` (plan 07 textfile; KSM `sc_etcd_snapshot_info` fallback) |
| `sc:model_spend_micro_usd:increase15m` | `sum(increase(sc_model_spend_micro_usd_total[15m]))`, evaluated every 1 m (plan 33 §14.8) |
| `sc:longhorn_expected_replicas` | `vector(<LONGHORN_REPLICAS>)` rendered from `cluster-vars` (1 / 2) |
| `sc:etcd_expected_members` | `count(kube_node_labels{label_strange_company_node_role="server"})` |
| `sc:backup_last_success_timestamp_seconds` | union with labels `stream_kind ∈ {cnpg, longhorn, repo-bundle, etcd}` and `stream` = cluster/volume/`all`: `label_replace(sc:cnpg_last_backup_success_timestamp_seconds, …) or label_replace(sc:longhorn_last_backup_success_timestamp_seconds, …) or label_replace(kube_cronjob_status_last_successful_time{namespace="forgejo",cronjob="repo-bundle-backup"}, …) or label_replace(sc:etcd_last_s3_snapshot_timestamp_seconds, …)` |
| `sc:host_firewall_ruleset_mismatch` | `max by (instance) (sc_host_firewall_ruleset_info) unless on (instance, sha256) sc_host_firewall_expected_info` → 1 (plan 08) |
| `namespace_tier:container_memory_working_set_bytes:sum` | `sum by (tier) (container_memory_working_set_bytes{container!=""} * on (namespace) group_left (tier) label_replace(kube_namespace_labels, "tier", "$1", "label_strange_company_tier", "(.*)"))` |
| `sc_notification_heartbeat_configured` | `vector(1)` or `vector(0)` from `notifications.yaml` |

## 9. Configuration

### 9.1 `notifications.yaml` (`kind: NotificationConfig`, per installation)

```yaml
apiVersion: strange.company/v1alpha1
kind: NotificationConfig
metadata: {name: prod}
spec:
  transports:                      # ≥ 1; rendered from bootstrap.env, extended by PR
    - kind: ntfy                   # email | webhook | ntfy | slack | discord | matrix-webhook
      name: primary
  email: {smarthost: "smtp.example.org:587", from: "sc@example.org", to: ["ops@example.org"], requireTLS: true}
  heartbeat: {configured: true, expectedPeriodMinutes: 5, graceMinutes: 10}
  links: {baseDomain: example.org, githubRepo: example/strange-company}
```

| Key | Default | Changed by |
|---|---|---|
| `spec.transports[]` | one, from `NOTIFY_TRANSPORT` | bootstrap; PR (a second transport is recommended for small-ha) |
| `spec.heartbeat.configured` | `true` iff `HEARTBEAT_URL` set | bootstrap; PR |
| Notification class timings, inhibit rules, groupBy | catalogue `spec` | PR to the contract |
| Per-alert `for`, thresholds | catalogue entries | PR (with a promtool test change) |
| `silence-audit.maxSilenceHours` | `72` | PR |
| Runner `alertmanager.url` | `http://alertmanager-operated.monitoring.svc:9093` (headless; fan-out §7.4) | plan 26 chart values |

### 9.2 Transports (SPEC §97)

Alertmanager has native integrations for email, Slack, Discord and generic webhooks (verify each `*_file` field on the pinned Alertmanager; 42-AC-5). ntfy and Matrix are reached through the generic webhook:

| `NOTIFY_TRANSPORT` | Alertmanager integration | Secret keys in `monitoring/alertmanager-transports` | Priority mapping (page / notify) |
|---|---|---|---|
| `email` | `email_configs` (`smarthost`, `from`, `to`, `auth_username`, `auth_password_file`, `require_tls: true`) | `smtp-password` | subject prefix `[PAGE]` / `[SC]`; header `X-Priority: 1` / `3` |
| `webhook` | `webhook_configs` (`url_file`, `max_alerts: 50`) | `webhook-url-page`, `webhook-url-notify` | separate URLs (the operator may point both at one endpoint) |
| `ntfy` | `webhook_configs` to `https://<ntfy>/<topic>?tpl=1&t=<title tmpl>&m=<message tmpl>&p=<5\|3>` (ntfy templating, verify on the operator's ntfy version; without it the JSON body is delivered raw); bearer token via `http_config.authorization.credentials_file` | `ntfy-url-page`, `ntfy-url-notify`, `ntfy-token` | `p=5` / `p=3` |
| `slack` | `slack_configs` (`api_url_file`) | `slack-url` | `:rotating_light:` prefix / none |
| `discord` | `discord_configs` (`webhook_url_file`) | `discord-url` | `@here` in page content / none |
| `matrix-webhook` | `webhook_configs` to an operator-run Matrix bridge (e.g. a hookshot generic webhook) | `webhook-url-page`, `webhook-url-notify` | bridge-dependent |
| heartbeat | `webhook_configs` (`url_file`) | `heartbeat-url` | — |

`./bootstrap` renders the SOPS file from `bootstrap.env` (`NOTIFY_WEBHOOK_URL` → both URL keys; ntfy page/notify URLs get `p=5`/`p=3` appended). Alertmanager mounts it through `alertmanagerSpec.secrets: [alertmanager-transports]` at `/etc/alertmanager/secrets/alertmanager-transports/<key>`.

**Fallback if a `*_file` field is unsupported** on the pinned version: `sc-alertgen` renders the whole `alertmanager.yaml` into the SOPS Secret instead (the routing tree then lives encrypted in Git, which is less reviewable). 42-AC-5 decides which mode is used and records it.

### 9.3 Templates

`sc.tmpl` defines `sc.title` = `[{{ .Status | toUpper }}{{ if eq .Status "firing" }}:{{ .Alerts.Firing | len }}{{ end }}] {{ .CommonLabels.alertname }} ({{ .CommonLabels.severity }}, {{ .CommonLabels.category }})` and `sc.text`, which lists per alert the summary, `description`, `task_id` and `task_url` when present, `runbook_url`, `runbook_mirror_url` and the start time. Templates reference only labels and annotations defined in §7.1 and the notification contract. No template ranges over arbitrary labels. Messages are truncated to 4000 characters per transport.

### 9.4 Silence policy

Silences are human actions (Alertmanager UI or `amtool`). Policy: a silence that matches any alert with `always_notify != "none"` must have a comment containing an issue reference (`#<n>` or a Forgejo URL) and must end within `maxSilenceHours` (72 h). The `silence-audit` CronJob (namespace `monitoring`, every 30 min, image `sc-alertgen` digest-pinned, read-only Alertmanager API access through NetworkPolicy) exits 1 when an active silence violates the policy → `SCSilencePolicyViolation`. It never deletes silences.

**Break-glass alerts (`always_notify="security-audit"`) may not be silenced at all.** A second CronJob `silence-audit-breakglass` (same image, every 5 min) exits 1 when any active silence's matchers match a `security-audit` alert → `SCBreakGlassAlertSilenced` (critical, page). Because that alert is itself `security-audit`, silencing it is detected by the same check. The Alertmanager API is unauthenticated in-cluster; the compensating controls are (a) NetworkPolicy limits port 9093 to Prometheus, the Runner and the audit CronJobs (trusted platform code), (b) Grafana has no Alertmanager datasource, (c) human silences go through `kubectl port-forward` with the operator kubeconfig, and (d) the audit above. `break-glass-access.md` §After use records evidence by issue reference, never by silencing.

### 9.5 Dead-man's switch

- `HEARTBEAT_URL` points at an external heartbeat service chosen by the operator (healthchecks.io-style "ping URL", Uptime Kuma push monitor, a cloud provider's heartbeat check). Requirements: accepts HTTP POST with any body; alerts the operator when no ping arrives for `expectedPeriodMinutes + graceMinutes` (default 5 + 10 min); its alert channel does **not** depend on the cluster (e.g. the service's own email or SMS).
- `Watchdog` (always firing) routes to `heartbeat` with `repeat_interval: 1m`, so a healthy Alertmanager posts about once a minute.
- The ping stops when: the node or cluster is down; Prometheus is down (Watchdog stops being sent, and Alertmanager resolves it after `resolve_timeout`); Alertmanager is down; egress is broken; or `SCAlertmanagerNotificationsFailing` fires (inhibit rule `notifications-failing-stops-heartbeat`), which covers "Alertmanager runs but cannot deliver human notifications".
- **No heartbeat configured:** PF-18 warns; `sc_notification_heartbeat_configured` is `0`, so `SCDeadMansSwitchNotConfigured` fires after 1 h (`notify`, `black-start-threat`) and repeats every 24 h; the control-transfer report prints `heartbeat: ABSENT — total-outage detection is not configured` (plan 50 §14.11); V-24 reports `warn`, not `pass`. Detection of total outage then depends on a human noticing. This is accepted only as an explicit, repeatedly notified operator choice.
- Planned downtime (`./bootstrap destroy`, maintenance): the operator pauses the external check first (`safe-destruction.md`, plan 49).

## 10. Secrets

| Class | Use | Notes |
|---|---|---|
| `SC-NOTIFY-TRANSPORT` (plan 35 register; owner 42) | instances `receiver` (keys per §9.2) and `heartbeat` (`heartbeat-url`) | Creator: operator via `bootstrap.env`; rendered by `sc-bootstrap` into `gitops/clusters/<c>/secrets/monitoring/alertmanager-transports.sops.yaml` (two recipients). Consumer: Alertmanager only. Rotation: `rotate-secrets.md#sc-notify-transport` — update `bootstrap.env`, `./bootstrap rotate refresh-bundle` is **not** needed (not bundled), open a PR replacing the SOPS file, merge; the operator reloads config within 1 min; send a test (`notification-failure.md` §Test). Recovery: from Git. |

The Runner posts unauthenticated to the in-cluster Alertmanager API; access control is NetworkPolicy (only the Runner, Prometheus, the `silence-audit` CronJobs and the H03 rulers reach port 9093; §9.4 lists the compensating controls for silence creation). No new secret class is introduced by this plan.

## 11. Storage

None beyond plan 38's Alertmanager PVC (1 GiB local-path per replica, retention `120h`). Rendered rules and configuration are Git. `notification_state` lives in `sc-pg` (plan 26).

## 12. Resource requirements

| Workload | Requests | Limits | Replicas (min / small-ha) |
|---|---|---|---|
| Alertmanager (plan 38) | 20m / 64 MiB | 128 MiB | 1 / 2 |
| `silence-audit` CronJob | 10m / 16 MiB | 64 MiB, `activeDeadlineSeconds: 120` | — |
| Rule evaluation cost (Prometheus) | ≈ 180 rules at 30 s; budgeted inside plan 38's 1 GiB request | — | — |

PriorityClass `platform` for both.

## 13. Security boundary

- Alertmanager (namespace `monitoring`, PSA baseline per ADR-0550; target restricted) holds transport credentials only. It has no Kubernetes write rights.
- Ingress to 9093 only from the listed pods (plan 36). The UI is public only through Authentik forward-auth (`sc-admins`).
- The Runner may post **only** the runner-post alertnames. Alertmanager cannot enforce that, so: (a) the Runner's client validates every payload against `notification.schema.yaml` before sending (unit-tested); (b) NetworkPolicy keeps every other workload, including Hermes and workers, away from 9093; (c) `make lint-alerts` checks that no runner-post alertname is also a Prometheus rule. A forged posted alert can at worst create noise; it cannot silence or resolve rule-based alerts, because rule alerts are re-sent by Prometheus every 30 s.
- Content: notifications contain catalogue text, label values and the structured annotations of §7.1. They never contain prompts, completions, code, command output or secrets [SPEC §94]. The Runner builds descriptions from enum fields; `task_title` is human-authored text and is length-capped.
- Transport credentials never appear in Git plaintext, logs or the rendered config (file references only).

## 14. Happy path

1. **Bootstrap (P01 onward):** PF-17 sends one test message through the configured transport from the toolbox (no cluster yet) and passes on the transport's success response (HTTP 2xx / SMTP 250). Typing the nonce is optional (confirmation kind `notification-nonce`, plan 50) and never blocks an unattended run (BOOT-15). PF-18 pings `HEARTBEAT_URL`.
2. **Plan phase:** `sc-bootstrap` renders `notifications.yaml`, the SOPS transport Secret and `alertmanager.yaml` into the cluster directory.
3. **After control transfer:** Flux applies the monitoring layers (plan 38/18). Alertmanager loads `alertmanager-sc-config`; Prometheus loads the `sc-*` PrometheusRules. Watchdog starts pinging the heartbeat within 1 min.
4. **Steady state:** rules evaluate every 30 s. Alerts route per §7.3; humans receive grouped messages with runbook links; resolved messages follow.
5. **Organizational events (C06):** the Runner posts and refreshes NEEDS_HUMAN alerts (§7.4); budget rules fire from Gateway metrics.
6. **Verify (P12):** V-24 posts one `SCNotificationTest` per always-notify reason plus one `page`-class test; it passes on Alertmanager's recorded successful deliveries (automatic evidence). Human receipt is acknowledged afterwards with the optional `./bootstrap verify --ack-notifications` (types the nonces; recorded as `notification-nonce` confirmations), which is never part of the unattended gate (CD-19, BOOT-15).

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Transport rejects or is unreachable | `SCAlertmanagerNotificationsFailing` (page); inhibits Watchdog → external heartbeat lapses | humans not notified through the primary path | Alertmanager retries each notification until the next group interval | `notification-failure.md` |
| Transport credential revoked | same | same | none | `notification-failure.md` §Credential, `rotate-secrets.md#sc-notify-transport` |
| Alertmanager pod down (minimum) | external heartbeat lapses; upstream `alertmanager.rules` alerts cannot be delivered | no notifications | kubelet restart | `monitoring-failure.md` §Alertmanager |
| One Alertmanager replica down (small-ha) | upstream `AlertmanagerMembersInconsistent`, `KubePodNotReady` | none (Prometheus and the Runner post to both) | — | `monitoring-failure.md` |
| Prometheus down | heartbeat lapses (Watchdog resolves) | no rule-based alerts | kubelet restart | `monitoring-failure.md` |
| Invalid rendered config | CI `amtool check-config`; at runtime the operator's config-reloader rejects it, old config stays; upstream `AlertmanagerFailedReload` | new changes not applied | config-reloader keeps last good | revert PR |
| Rule with a typo'd metric (never fires) | `make lint-alerts` metric-existence check; C11/P09 ACs query every referenced metric | silent gap | CI blocks merge | — |
| Runner cannot post | `SCRunnerNotificationPostFailing` (page, needs-human) | Needs Human not delivered by the posted path | Runner retries every refresh | `notification-failure.md` §Runner |
| Runner down | `SCRunnerDown` | posted alerts persist ≤ 6 h, then expire | Runner re-posts on restart | `agent-runner-failure.md` |
| Notification storm (many alerts) | message count | noise | grouping by `[alertname, category]`; inhibition for non-always-notify symptoms | tune via PR |
| Long-lived silence hides an always-notify alert | `SCSilencePolicyViolation` | missed condition | none | `notification-failure.md` §Silences |
| Heartbeat service itself down | heartbeat service's own status | false "outage" alert or missed outage | none | operator chooses a second heartbeat service (optional) |
| Total egress loss | heartbeat lapses | external notifications impossible | none | `monitoring-failure.md` §Egress |
| KSM CronJob metrics missing (version change) | `make e2e-k3d` check-cronjob test; `absent` branch fires | false "stale" alerts | none | re-verify KSM metric names (plan 38) |

## 16. Retry behavior

Prometheus re-sends firing alerts every evaluation interval. Alertmanager retries failed notifications with backoff until the next `group_interval`, then re-sends in the next cycle. The Runner retries per §7.4 step 7. `sc-alertgen post-test` retries 3 times (2 s apart) per replica. No component retries forever without an alert: failures surface as `SCAlertmanagerNotificationsFailing` or `SCRunnerNotificationPostFailing`.

## 17. Idempotency behavior

- Rendering is deterministic: the same catalogue and config always produce byte-identical files; CI enforces regenerate-equals-committed.
- Alertmanager identifies an alert by its label set; re-posting is idempotent, and re-sending a firing rule alert extends it.
- Test posts carry a fresh `subject` (`test-<nonce>`), so repeated tests never merge with each other or with real alerts.

## 18. Bootstrap behavior

- Before the cluster exists, notification capability is proven from the toolbox (PF-17/18).
- Monitoring and alerting are **not** in the seed set (ADR-0005, ADR-0550). During seed and transfer, failures are reported by `sc-bootstrap` on the console and in the journal (plan 50).
- From P09, every alert gated at P09 must be loaded. Alerts gated later (C06, C11) are rendered and loaded from P09 too (ADR-0550: all rules are central), but they cannot fire before their emitter exists: level comparisons on absent series yield no alert, and the few `absent()`-based alerts (`SCRunnerDown`, `SCModelGatewayDown`) carry the guard `and on() count(kube_deployment_created{namespace="strange-company",deployment="<name>"}) > 0`, so they arm only once the Deployment exists.
- Black start (plan 45): alerting returns with the monitoring layers after the source switch. Until then the recovery operator watches the console. Workers stay paused, so no posted alert is expected.

## 19. Upgrade behavior

- kube-prometheus-stack upgrades (plan 38 §19) can change upstream rule groups and alert names: CI diffs the rendered upstream alert list against `spec.upstreamRules` and fails on a new group not classified as kept or disabled.
- Alertmanager upgrades: CI runs `amtool check-config` and `sc-alertgen test-routes` with the new image (pinned `amtool` version equals the image).
- Catalogue changes go through PR with promtool tests; a removed alertname must also be removed from runbooks' "When to use" lists (lint).

## 20. Recovery behavior

- Alertmanager state (silences, notification log) is reconstructible and not backed up. After loss: active alerts re-arrive within one evaluation interval (rules) or 5 min (Runner); humans re-create silences; at most one duplicate notification per group.
- After black start, alert history before the incident is gone (Prometheus is best effort, SPEC §108). Recovery evidence lives in the plan 45 report, not in alert history.

## 21. Observability

- Metrics: Alertmanager's own (`alertmanager_notifications_total`, `alertmanager_notifications_failed_total{integration, receiver_name (verify)}`, `alertmanager_alerts`, `alertmanager_silences`), Prometheus rule-evaluation metrics (`prometheus_rule_evaluation_failures_total`), and `sc_runner_alert_posts_total`.
- Dashboards: plan 39 "Alerts & notifications" (firing by class, delivery failures, silences, heartbeat delivery).
- Alerts about alerting: `Watchdog`, `SCDeadMansSwitchNotConfigured`, `SCAlertmanagerNotificationsFailing`, `SCRunnerNotificationPostFailing`, `SCSilencePolicyViolation`, plus upstream `alertmanager.rules` and `prometheus` groups (`PrometheusRuleFailures`, `AlertmanagerFailedReload`, `PrometheusNotConnectedToAlertmanagers`).
- Logs: Alertmanager logs to stdout (kubectl logs; Loki at H03).

## 22. Testing strategy

All deterministic; no paid model calls [SPEC §104].

- **Contract (F02):** `alert-catalog.schema.yaml` and `notification.schema.yaml` meta-validate, examples validate, invalid examples fail; the catalogue validates against its schema.
- **`make lint-alerts` (`sc-alertgen lint`, every PR touching `contracts/alert-catalog.yaml`, `gitops/platform/monitoring/**` or `procedures/**`):** unique alertnames; unique aliases that are not alertnames; runbook files exist; every metric in every `expr` is in `observability/metric-catalog.yaml` or the upstream exporter allowlist; no inhibit target can match an always-notify alert; rendered rules equal committed files; every rule alert has `tests/<alertname>.test.yaml`; no runner-post alertname is rendered as a rule; `group_by` excludes identity labels; every `alertname` or alias mentioned in `plans/**` and `procedures/**` (backticked, CamelCase, `SC…`) resolves to a catalogue entry.
- **`promtool test rules`:** each rule alert has at least one firing and one non-firing case, including the `for` boundary. Check-cronjob alerts have four cases (latest failed, stale, never succeeded, CronJob deleted).
- **Route tests:** `sc-alertgen test-routes` against the rendered config for each transport kind (golden configs for `email`, `webhook`, `ntfy`, `slack`, `discord`).
- **k3d (`make e2e-alerting`, plan 52):** kube-prometheus-stack (minimum values) + a `notification-sink` test pod (HTTP server recording every request; digest-pinned test image) configured as the webhook transport and as the heartbeat URL. Scenarios: (a) Watchdog reaches the heartbeat sink at least twice within 3 min; (b) scaling Alertmanager to 0 stops heartbeat requests (no request for 3 min); (c) a sink that returns 500 for the human receiver raises `SCAlertmanagerNotificationsFailing` and the heartbeat sink stops receiving within 15 min; (d) a fake Runner posts `SCTaskNeedsHuman` for `sc-1` and, 60 s later, `sc-2`: the sink records two deliveries, and the second message lists both tasks (ADR-0555); (e) resolve → resolved message; (f) a check CronJob whose Job exits 1 raises its alert within `failAfter + 2m`; suspending it past a test-shortened `maxAge` raises the stale branch; (g) a silence without an issue reference on an always-notify alert raises `SCSilencePolicyViolation`. (h) any silence matching a `security-audit` alert raises `SCBreakGlassAlertSilenced`.
- **Live (P09/C06/C11/P12):** §23 commands.

## 23. Acceptance criteria

`PQ` = `./bootstrap exec -- kubectl -n monitoring exec sts/prometheus-kps-prometheus -c prometheus -- promtool query instant http://localhost:9090 '<expr>'`. `AM` = `./bootstrap exec -- amtool --alertmanager.url=http://127.0.0.1:9093` over `kubectl -n monitoring port-forward svc/kps-alertmanager 9093`.

### GATE-P09

- **42-AC-1** `make lint-alerts` exits 0 on the P09 merge commit, and a fixture catalogue with an inhibit rule targeting `always_notify="black-start-threat"` makes it exit non-zero (RED proof recorded in the PR).
- **42-AC-2** `make render-alerts && git diff --exit-code gitops/platform/monitoring/rules gitops/clusters/<c>/monitoring` exits 0.
- **42-AC-3** `promtool test rules gitops/platform/monitoring/rules/tests/*.test.yaml` exits 0 and the number of test files equals the number of rule-based (`prometheus-rule` + `check-cronjob`) catalogue alerts.
- **42-AC-4** `sc-alertgen test-routes --cluster <c>` exits 0: every `page` alert resolves to `human-page`, every `notify` to `human-notify`, every `silent` to `silent`, `Watchdog` to `heartbeat`, and every alert with `alwaysNotify != none` resolves to a receiver with at least one integration.
- **42-AC-5** Re-verification of transport file references: `./bootstrap exec -- amtool check-config gitops/clusters/<c>/monitoring/alertmanager.yaml` with the pinned `amtool` exits 0 and the config contains no literal URL or password (`grep -E 'https?://[^ ]*(token|key|hooks)|auth_password:' … ` returns nothing). The mode used (file references or encrypted fallback, §9.2) is recorded in the verify evidence.
- **42-AC-6** `PQ 'count(ALERTS{alertname="Watchdog",alertstate="firing"})'` = 1 (the upstream `general.rules` Watchdog is disabled via `disableAlerts`, GC-12), and `PQ 'increase(alertmanager_notifications_total{integration="webhook",receiver_name="heartbeat"}[5m]) >= 4'` returns a series (verify label name; fallback: the external service shows a ping within 2 min, operator-confirmed).
- **42-AC-7** Every P09-gated rule alert of the installation's profile is loaded: `curl -s localhost:9090/api/v1/rules | jq '[.data.groups[].rules[] | select(.type=="alerting") | .name]'` (inside the Prometheus pod) contains each such catalogue alertname (script compares with `yq` output of the catalogue filtered by `gate == "P09"` and profile).
- **42-AC-8** V-24 synthetic notifications: `./bootstrap verify --only V-24` posts eight `SCNotificationTest` alerts (seven always-notify reasons as `notify`, one `page`), and within 5 min `PQ 'sum(increase(alertmanager_notifications_total{receiver_name=~"human-.*"}[10m]))'` ≥ 2 (one per class group) with `alertmanager_notifications_failed_total` unchanged. No human input is required; the optional nonce acknowledgement is not part of the gate.
- **42-AC-9** Heartbeat absent path: in the k3d suite with `heartbeat.configured: false`, `SCDeadMansSwitchNotConfigured` fires after the (test-shortened) `for`, and the rendered report line contains `ABSENT — total-outage detection is not configured`.
- **42-AC-10** k3d scenarios (a)–(c) of §22 pass (`make e2e-alerting` exit 0).
- **42-AC-22** `make e2e-alerting` scenario (h): a silence matching `alertname="SCBreakGlassUsed"` (any comment, any duration) makes `SCBreakGlassAlertSilenced` deliver to the test receiver within 10 min; a matching silence on `SCBreakGlassAlertSilenced` itself is also reported.
- **42-AC-23** (GATE-P09, render unit test, GC-20) The self-hosting-minimum render contains `time_intervals` `sc-node-maintenance` equal to `NODE_MAINTENANCE_WINDOW`, and every route that carries `mute_time_intervals` also carries the matcher `always_notify=~"|none"`; `amtool config routes test always_notify=failed-backup notify_class=notify` resolves to an unmuted route; the small-ha render contains no `time_intervals`.
- **42-AC-11** `kubectl -n monitoring get secret alertmanager-sc-config -o jsonpath='{.data.alertmanager\.yaml}' | base64 -d | yq '.route.group_by'` = `[alertname, category]`.

### GATE-P10

- **42-AC-12** k3d scenario (f) passes for a representative check CronJob, which re-verifies `kube_cronjob_status_last_successful_time` on the pinned KSM.
- **42-AC-13** Live: `PQ 'count(ALERTS{category=~"backup|restore-test|black-start-threat",alertstate="firing"})'` returns empty after all P10 backups and restore tests have run once, and for each check CronJob in the catalogue `kubectl get cronjob -n <ns> <name>` exists.

### GATE-C06

- **42-AC-14** k3d scenario (d) passes (two deliveries for two tasks; one grouped message), and the posted payloads validate against `notification.schema.yaml` (the sink stores them; the test validates).
- Note: 42-AC-15 moved to GATE-C11 (it needs the fake harness, the Forgejo and Woodpecker adapters and Hermes, which C06 does not depend on; GC-01).
- **42-AC-16** Live: setting a fake provider budget to exhaustion (plan 33 test fixture) makes `ALERTS{alertname="SCBudgetExhausted",alertstate="firing"}` present within 2 min and Alertmanager records a `human-page` delivery.

### GATE-C11

- **42-AC-15** Live (after the C10 suite): forcing a fake task into `NEEDS_HUMAN` (fake harness `fake-test-spec-conflict`, plan 21) produces, within 5 min, `AM alert query alertname=SCTaskNeedsHuman subject=<task>` returning one alert with annotation `task_id=<task>`; deleting the Alertmanager pod and waiting 6 min shows the alert again (Runner refresh); moving the task out of NEEDS_HUMAN resolves it within 5 min.
- **42-AC-17** For every C11-gated catalogue alert, every metric name in its `expr` exists: a script extracts metric names and runs `PQ 'count(<metric>)'` for each; none returns empty, except metrics listed in an allowlist of "event-only" counters that legitimately have no series until the first event (the script requires each such counter to be present with value 0 after a Runner/Gateway restart, i.e. pre-initialized; plan 26/53 requirement).
- **42-AC-18** `PQ 'count(ALERTS{alertstate="firing",category=~"runner|gateway|task|tdd|budget|spend"})'` is empty after the C10 fake end-to-end suite completes on the live cluster with an unexhausted budget.

### GATE-H03

- **42-AC-19** On small-ha, `SCLokiDown`, `SCTempoDown`, `SCLogAgentDown`, `SCLogIngestionErrors` and `SCTelemetryStorageWriteFailing` are loaded (42-AC-7 method with `gate == "H03"`), and the Loki and Tempo rulers (if enabled by plan 41) list Alertmanager as their only notifier.

### GATE-H04

- **42-AC-20** With two Alertmanager replicas, the Runner's post reaches both (`AM --alertmanager.url=<each pod IP> alert query subject=<task>` returns the alert on both), and deleting one pod during an active NEEDS_HUMAN still yields exactly one delivery per group interval at the test receiver.

### GATE-P12 (contribution)

- **42-AC-21** `verify.json` contains V-24 with `status: pass` (or `warn` only for an absent heartbeat, which must also appear in the report).

### 23.5 Catalogue rendered as a table (generated from `contracts/alert-catalog.yaml`; the contract wins on any difference)

| alertname | category | severity / class | alwaysNotify | source / `for` | expr sketch | gate | profiles | signal | runbook |
|---|---|---|---|---|---|---|---|---|---|
| `Watchdog` | monitoring | info / heartbeat | none | rule / 0s | `vector(1)` | P09 | both | 38 | `monitoring-failure.md` |
| `SCDeadMansSwitchNotConfigured` | monitoring | warning / notify | black-start-threat | rule / 1h | `sc_notification_heartbeat_configured == 0` | P09 | both | 42 | `notification-failure.md` |
| `SCAlertmanagerNotificationsFailing` | monitoring | critical / page | critical-outage | rule / 10m | `sum by (integration) (rate(alertmanager_notifications_failed_total[10m])) > 0` | P09 | both | 38 | `notification-failure.md` |
| `SCRunnerNotificationPostFailing` | monitoring | critical / page | needs-human | rule / 10m | `sum(increase(sc_runner_alert_posts_total{result="failure"}[10m])) > 0 and sum(increase(sc_runner_alert_posts_total{result="success"}[10m])) == 0` | C11 | both | 26 | `notification-failure.md` |
| `SCSilencePolicyViolation` | monitoring | warning / notify | none | check-cronjob / fail 30m, stale 2h | `monitoring/silence-audit` (ADR-0553) | P09 | both | 42 | `notification-failure.md` |
| `SCBreakGlassAlertSilenced` | security | critical / page | security-audit | check-cronjob / fail 15m, stale 30m | `monitoring/silence-audit-breakglass` (ADR-0553) | P09 | both | 42 | `break-glass-access.md` |
| `SCPrometheusCardinalityHigh` | monitoring | warning / notify | none | rule / 30m | `max(prometheus_tsdb_head_series) > 250000` | P09 | both | 38 | `monitoring-failure.md` |
| `SCPrometheusStorageNearCap` | monitoring | info / silent | none | rule / 1h | `max(prometheus_tsdb_storage_blocks_bytes + prometheus_tsdb_wal_storage_size_bytes) / max(sc_prometheus_retention_size_bytes) > 0.9` | P09 | both | 38 | `monitoring-failure.md` |
| `SCPrometheusRetentionShort` | monitoring | warning / notify | none | rule / 1h | `(time() - min(prometheus_tsdb_lowest_timestamp_seconds)) < 8 * 86400 and on() (time() - min(kube_persistentvolumeclaim_created{namespace="monitoring",persistentvolumeclaim=~"prometheus-kps-prometheus-db-.*"})) > 9 * 86400` | P09 | both | 38 | `monitoring-failure.md` |
| `SCMonitoringTargetDown` | monitoring | warning / notify | none | rule / 10m | `up{job=~"cnpg\|longhorn\|forgejo\|authentik\|flux\|woodpecker\|agent-runner\|model-gateway\|otel-collector\|grafana\|blackbox\|cert-manager\|traefik\|cloudflared\|kube-etcd\|kube-state-metrics\|node-exporter"} == 0` | P09 | both | 38 | `monitoring-failure.md` |
| `SCOTelCollectorDown` | telemetry | warning / notify | none | rule / 10m | `absent(up{job="otel-collector"} == 1)` | P09 | both | 40 | `monitoring-failure.md` |
| `SCOTelCollectorRefusingData` | telemetry | warning / notify | none | rule / 15m | `sum(rate(otelcol_receiver_refused_spans_total[10m])) + sum(rate(otelcol_exporter_send_failed_spans_total[10m])) > 0` | P09 | both | 40 | `monitoring-failure.md` |
| `SCTelemetryForbiddenAttributeDropped` | security | warning / notify | none | rule / 0s | `sum(increase(sc_span_redactions_total[1h])) > 0` | P09 | both | 40 | `credential-compromise.md` |
| `SCTelemetryDebugCaptureActive` | security | warning / notify | none | rule / 0s | `max(sc_telemetry_debug_grants_active) > 0` | C11 | both | 40 | `monitoring-failure.md` |
| `SCGrafanaDown` | monitoring | warning / notify | none | rule / 10m | `probe_success{probe="grafana"} == 0` | P09 | both | 39 | `monitoring-failure.md` |
| `SCLokiDown` | telemetry | warning / notify | none | rule / 15m | `absent(up{job="loki"} == 1)` | H03 | small-ha | 41 | `monitoring-failure.md` |
| `SCTempoDown` | telemetry | warning / notify | none | rule / 15m | `absent(up{job="tempo"} == 1)` | H03 | small-ha | 41 | `monitoring-failure.md` |
| `SCLogAgentDown` | telemetry | warning / notify | none | rule / 15m | `kube_daemonset_status_number_available{namespace="monitoring-node",daemonset="otel-agent"} < kube_daemonset_status_desired_number_scheduled{namespace="monitoring-node",daemonset="otel-agent"}` | H03 | small-ha | 41 | `monitoring-failure.md` |
| `SCLogIngestionErrors` | telemetry | warning / notify | none | rule / 30m | `sum(rate(loki_discarded_samples_total[15m])) > 0` | H03 | small-ha | 41 | `monitoring-failure.md` |
| `SCTelemetryStorageWriteFailing` | telemetry | warning / notify | none | rule / 30m | `sum(rate(loki_ingester_chunks_flush_failures_total[15m])) > 0 or sum(rate(tempodb_backend_request_duration_seconds_count{status_code=~"5..\|error"}[15m])) > 0` | H03 | small-ha | 41 | `monitoring-failure.md` |
| `SCLokiCompactorFailing` | telemetry | warning / notify | none | rule / 1h | `time() - max(loki_boltdb_shipper_compact_tables_operation_last_successful_run_timestamp_seconds) > 10800 or absent(loki_boltdb_shipper_compact_tables_operation_last_successful_run_timestamp_seconds)` | H03 | small-ha | 41 | `monitoring-failure.md` |
| `SCTempoCompactorFailing` | telemetry | warning / notify | none | rule / 1h | `sum(increase(tempodb_compaction_errors_total[1h])) > 0 or (time() - max(tempodb_blocklist_poll_last_successful_timestamp_seconds) > 10800)` | H03 | small-ha | 41 | `monitoring-failure.md` |
| `SCNodeDown` (aka `NodeDown`, `KubeNodeNotReady`) | node | critical / page | critical-outage | rule / 5m | `max by (node) (kube_node_status_condition{condition="Ready",status="true"} == 0) or max by (node) (label_replace(up{job="node-exporter"}, "node", "$1", "instance", "(.*)") == 0)` | P09 | both | 38 | `lost-node.md` |
| `SCNodeDiskPressure` | node | critical / page | critical-outage | rule / 2m | `kube_node_status_condition{condition="DiskPressure",status="true"} == 1` | P09 | both | 38 | `disk-pressure.md` |
| `SCNodeRootDiskLow` | node | warning / notify | none | rule / 10m | `node_filesystem_avail_bytes{mountpoint="/",fstype!~"tmpfs\|overlay"} / node_filesystem_size_bytes{mountpoint="/",fstype!~"tmpfs\|overlay"} < 0.25` | P09 | both | 38 | `disk-pressure.md` |
| `SCNodeRootDiskCritical` | node | critical / page | critical-outage | rule / 5m | `node_filesystem_avail_bytes{mountpoint="/",fstype!~"tmpfs\|overlay"} / node_filesystem_size_bytes{mountpoint="/",fstype!~"tmpfs\|overlay"} < 0.15` | P09 | both | 38 | `disk-pressure.md` |
| `SCNodeRootDiskFillingUp` | node | warning / notify | none | rule / 1h | `predict_linear(node_filesystem_avail_bytes{mountpoint="/",fstype!~"tmpfs\|overlay"}[6h], 24 * 3600) < 0.10 * node_filesystem_size_bytes{mountpoint="/",fstype!~"tmpfs\|overlay"}` | P09 | both | 43 | `disk-pressure.md` |
| `SCNodeJournalOversize` | node | warning / notify | none | rule / 1h | `sc_node_journald_bytes > 1.1 * 1073741824` | P09 | both | 43 | `disk-pressure.md` |
| `SCRetentionEtcdSnapshotsExcess` | node | warning / notify | none | rule / 6h | `sc_etcd_snapshot_count{location="local"} > 38` | P09 | both | 43 | `disk-pressure.md` |
| `SCEtcdSnapshotDirLarge` | node | warning / notify | none | rule / 1h | `sc_etcd_snapshot_dir_bytes > 2 * 1073741824 or (time() - sc_etcd_snapshot_prune_last_run_timestamp_seconds) > 2 * 86400` | P09 | both | 08 | `disk-pressure.md` |
| `SCK3sUpgradeIncomplete` | node | warning / notify | none | rule / 6h | `count(count by (version) (sc_k3s_version_info)) > 1` | P09 | both | 48 | `upgrade-k3s.md` |
| `SCUpgradeGateFailed` | gitops | warning / notify | none | rule / 0s | `max(kube_job_status_failed{job_name=~"upgrade-gate-.*"}) > 0` | P09 | both | 48 | `upgrade-platform-component.md` |
| `SCNodeMemoryPressure` | node | critical / page | critical-outage | rule / 2m | `kube_node_status_condition{condition="MemoryPressure",status="true"} == 1` | P09 | both | 38 | `lost-node.md` |
| `SCNodeOOMKill` | node | warning / notify | none | rule / 0s | `increase(node_vmstat_oom_kill[15m]) > 0` | P09 | both | 38 | `lost-node.md` |
| `SCPlatformPodOOMKilled` | node | warning / notify | none | rule / 0s | `max by (namespace, pod, container) (kube_pod_container_status_last_terminated_reason{reason="OOMKilled",namespace!~"agent-jobs\|ci-jobs\|apps"}) * on (namespace, pod, container) group_left increase(kube_pod_container_status_restarts_total[15m]) > 0` | P09 | both | 51 | `monitoring-failure.md` |
| `SCPlatformJobFailed` | node | warning / notify | none | rule / 15m | `kube_job_status_failed{namespace!~"agent-jobs\|ci-jobs\|apps"} > 0` | P09 | both | 38 | `monitoring-failure.md` |
| `SCPlatformPodEvicted` | node | warning / notify | none | rule / 0s | `sum by (namespace) (increase(kube_pod_status_reason{reason="Evicted",namespace!~"agent-jobs\|ci-jobs\|apps\|dr-tests\|renovate"}[15m])) > 0` | P09 | both | 51 | `disk-pressure.md` |
| `SCWorkerPendingCapacity` | runner | warning / notify | none | rule / 0s | `max(time() - kube_pod_created{namespace="agent-jobs"} * on (pod) group_left kube_pod_status_phase{namespace="agent-jobs",phase="Pending"}) > 1800` | C11 | both | 51 | `stuck-task-recovery.md` |
| `SCHeavySlotAdvertiserStale` | node | warning / notify | none | check-cronjob / fail 15m, stale 20m | `kube-system/heavy-slot-advertiser` (ADR-0553) | P09 | both | 51 | `stuck-task-recovery.md` |
| `SCHeavySlotMissing` | node | warning / notify | none | rule / 10m | `count(kube_node_status_condition{condition="Ready",status="true"} == 1) > count(kube_node_status_capacity{resource="strange_company_heavy_slot"} > 0)` | P09 | both | 51 | `stuck-task-recovery.md` |
| `SCHeavySlotDeadlock` | ci | warning / notify | none | rule / 0s | `max(time() - kube_pod_created{namespace="ci-build"} * on (pod) group_left kube_pod_status_phase{namespace="ci-build",phase="Pending"}) > 3600` | P09 | both | 51 | `woodpecker-failure.md` |
| `SCNodeFactsStale` | monitoring | warning / notify | none | rule / 10m | `time() - max by (instance) (sc_node_facts_last_run_timestamp_seconds) > 1800` | P09 | both | 07 | `monitoring-failure.md` |
| `SCNodeMaintenanceStale` | node | warning / notify | none | rule / 1h | `time() - max by (instance) (sc_node_maintenance_last_run_timestamp_seconds) > 35 * 86400` | P09 | both | 08 | `upgrade-k3s.md` |
| `SCNodeClockSkew` | node | warning / notify | none | rule / 15m | `abs(node_timex_offset_seconds) > 0.05 or node_timex_sync_status == 0` | P09 | both | 08 | `lost-node.md` |
| `SCNodeRebootRequiredStale` | node | warning / notify | none | rule / 1h | `sc_node_reboot_required == 1 and (time() - sc_node_reboot_required_since_timestamp_seconds) > 7 * 86400` | P09 | both | 08 | `upgrade-platform-component.md` |
| `SCHostFirewallNotLoaded` | security | critical / page | none | rule / 10m | `sc_host_firewall_loaded == 0 or sc:host_firewall_ruleset_mismatch == 1` | P09 | both | 08 | `add-node.md` |
| `SCNodeSwapEnabled` | node | warning / notify | none | rule / 15m | `sc_node_swap_bytes > 0` | P09 | both | 08 | `lost-node.md` |
| `SCNodeUpdatesFailing` | node | warning / notify | none | rule / 24h | `sc_node_unattended_upgrades_last_status != 0` | P09 | both | 08 | `upgrade-platform-component.md` |
| `SCK3sServerConfigDrift` (aka `SCK3sConfigDrift`) | node | warning / notify | none | rule / 15m | `count(count by (sha256) (sc_k3s_cluster_config_info{role="server"})) > 1` | H04 | small-ha | 07 | `upgrade-k3s.md` |
| `SCK3sVersionDrift` | node | warning / notify | none | rule / 2h | `count(count by (version) (sc_k3s_version_info)) > 1` | H04 | small-ha | 07 | `upgrade-k3s.md` |
| `SCEtcdNoLeader` | etcd | critical / page | critical-outage | rule / 1m | `etcd_server_has_leader{job="kube-etcd"} == 0` | P09 | both | 07 | `restore-etcd.md` |
| `SCEtcdMemberMissing` | etcd | critical / page | critical-outage | rule / 5m | `count(up{job="kube-etcd"} == 1) < on() sc:etcd_expected_members` | H04 | small-ha | 07 | `lost-etcd-member.md` |
| `SCEtcdDbSizeHigh` | etcd | warning / notify | none | rule / 15m | `etcd_mvcc_db_total_size_in_bytes{job="kube-etcd"} / etcd_server_quota_backend_bytes{job="kube-etcd"} > 0.8` | P09 | both | 07 | `restore-etcd.md` |
| `SCEtcdSlowDisk` | etcd | warning / notify | none | rule / 10m | `histogram_quantile(0.99, sum by (instance, le) (rate(etcd_disk_wal_fsync_duration_seconds_bucket{job="kube-etcd"}[5m]))) > 0.5` | P09 | both | 07 | `disk-pressure.md` |
| `SCEtcdSnapshotUploadFailing` | backup | warning / notify | failed-backup | rule / 15m | `time() - sc:etcd_last_s3_snapshot_timestamp_seconds > 46800 or absent(sc:etcd_last_s3_snapshot_timestamp_seconds)` | P10 | both | 07 | `restore-etcd.md` |
| `SCEtcdSnapshotMissing` | backup | warning / notify | failed-backup | rule / 15m | `time() - max by (instance) (sc_etcd_snapshot_last_success_timestamp_seconds{location="local"}) > 46800` | P10 | both | 07 | `restore-etcd.md` |
| `SCCnpgClusterDown` (aka `CNPGClusterNotHealthy`, `CNPGNoPrimary`, `CNPGClusterNotReady`) | database | critical / page | critical-outage | rule / 3m | `sc:cnpg_ready_instances == 0 or sc:cnpg_primaries == 0` | P09 | both | 12 | `cnpg-primary-loss.md` |
| `SCCnpgClusterDegraded` (aka `CNPGReplicaCountBelowSpec`) | database | warning / notify | none | rule / 10m | `sc:cnpg_ready_instances > 0 and sc:cnpg_ready_instances < sc:cnpg_desired_instances` | H04 | small-ha | 12 | `cnpg-replica-loss.md` |
| `SCCnpgReplicationLagHigh` (aka `CNPGReplicationLagHigh`) | database | warning / notify | none | rule / 10m | `max by (cluster) (cnpg_pg_replication_lag) > 300` | H04 | small-ha | 12 | `cnpg-replica-loss.md` |
| `SCCnpgWalArchiveFailing` (aka `CNPGWALArchivingFailing`) | backup | critical / page | failed-backup | rule / 5m | `max by (cluster) (cnpg_pg_stat_archiver_last_failed_time) > max by (cluster) (cnpg_pg_stat_archiver_last_archived_time)` | P10 | both | 12 | `restore-cnpg.md` |
| `SCCnpgWalArchiveBacklog` | backup | critical / page | failed-backup | rule / 2m | `max by (cluster) (cnpg_pg_stat_archiver_seconds_since_last_archival) > 900` | P10 | both | 12 | `restore-cnpg.md` |
| `SCCnpgBaseBackupStale` (aka `CNPGBaseBackupStale`) | backup | warning / notify | failed-backup | rule / 15m | `time() - sc:cnpg_last_backup_success_timestamp_seconds > 93600` | P10 | both | 12 | `restore-cnpg.md` |
| `SCCnpgBackupFailed` (aka `CNPGBaseBackupFailed`) | backup | warning / notify | failed-backup | rule / 0s | `sum by (namespace) (sc_cnpg_backup_info{phase="failed"}) > 0` | P10 | both | 12 | `restore-cnpg.md` |
| `SCCnpgInstanceNotReady` (aka `CNPGInstanceNotReady`) | database | warning / notify | none | rule / 5m | `kube_pod_status_ready{condition="true",namespace=~"forgejo\|authentik\|woodpecker\|strange-company",pod=~".+-pg-[0-9]+"} == 0` | P09 | both | 12 | `cnpg-primary-loss.md` |
| `SCCnpgSyncReplicationDegraded` (aka `CNPGSyncReplicationDegraded`) | database | warning / notify | none | rule / 15m | `cnpg_collector_sync_replicas{value="observed"} < cnpg_collector_sync_replicas{value="expected"}` | H04 | small-ha | 12 | `cnpg-replica-loss.md` |
| `SCCnpgWalReadyBacklogHigh` (aka `CNPGWALArchiveBacklogHigh`) | backup | warning / notify | failed-backup | rule / 10m | `max by (namespace, pod) (cnpg_collector_pg_wal_archive_status{value="ready"}) > 256` | P10 | both | 12 | `object-storage-outage.md` |
| `SCCnpgWalReadyBacklogCritical` (aka `CNPGWALArchiveBacklogCritical`) | backup | critical / page | failed-backup | rule / 5m | `max by (namespace, pod) (cnpg_collector_pg_wal_archive_status{value="ready"}) > 1024` | P10 | both | 12 | `object-storage-outage.md` |
| `SCCnpgRecoveryWindowShort` (aka `CNPGRecoveryWindowShort`) | backup | warning / notify | failed-backup | rule / 1h | `(time() - sc:cnpg_first_recoverability_point_timestamp_seconds < 7 * 86400) and (time() - sc:cnpg_cluster_created_timestamp_seconds > 8 * 86400)` | P10 | both | 12 | `restore-cnpg.md` |
| `SCCnpgDatabaseLarge` (aka `CNPGDatabaseLarge`, `CNPGStorageHigh`) | storage | warning / notify | none | rule / 1h | `sum by (node) (sum by (namespace, pod) (cnpg_pg_database_size_bytes) * on (namespace, pod) group_left (node) kube_pod_info) > 0.8 * scalar(sc:cnpg_data_ceiling_bytes)` | P09 | both | 12 | `disk-pressure.md` |
| `SCCnpgOperatorDown` (aka `CNPGOperatorDown`) | database | critical / page | critical-outage | rule / 10m | `kube_deployment_status_replicas_available{namespace="cnpg-system",deployment=~".*cloudnative-pg.*\|cnpg.*"} == 0` | P09 | both | 12 | `cnpg-primary-loss.md` |
| `SCCnpgPluginDown` (aka `CNPGPluginDown`) | backup | critical / page | failed-backup | rule / 10m | `kube_deployment_status_replicas_available{namespace="cnpg-system",deployment=~".*barman-cloud.*"} == 0` | P10 | both | 12 | `cnpg-primary-loss.md` |
| `SCLonghornVolumeDegraded` | storage | warning / notify | none | rule / 10m | `longhorn_volume_robustness == 2` | P09 | both | 13 | `longhorn-replica-loss.md` |
| `SCLonghornReplicaCountBelowProfile` | storage | warning / notify | none | rule / 30m | `min(sc_longhorn_volume_number_of_replicas{state_id!=""}) < on() sc:longhorn_expected_replicas` | H04 | small-ha | 47 | `longhorn-replica-loss.md` |
| `SCLonghornVolumeFaulted` | storage | critical / page | critical-outage | rule / 1m | `longhorn_volume_robustness == 3` | P09 | both | 13 | `restore-longhorn.md` |
| `SCLonghornVolumeNearlyFull` (aka `LonghornVolumeUsageHigh`, `ForgejoPVCUsageHigh`) | storage | warning / notify | none | rule / 30m | `longhorn_volume_actual_size_bytes / longhorn_volume_capacity_bytes > 0.85` | P09 | both | 13 | `restore-longhorn.md` |
| `SCLonghornNodeStorageLow` (aka `LonghornDiskUsageHigh`) | storage | warning / notify | none | rule / 30m | `longhorn_node_storage_usage_bytes / longhorn_node_storage_capacity_bytes > 0.8` | P09 | both | 13 | `disk-pressure.md` |
| `SCLonghornManagerDown` | storage | warning / notify | none | rule / 10m | `max(kube_daemonset_status_number_unavailable{namespace="longhorn-system"}) > 0` | P09 | both | 13 | `longhorn-replica-loss.md` |
| `SCLonghornVolumeAttachStuck` | storage | warning / notify | none | rule / 10m | `longhorn_volume_state == 4 or longhorn_volume_state == 5` | P09 | both | 13 | `longhorn-replica-loss.md` |
| `SCLonghornVolumeAttachStuckCritical` | storage | critical / page | critical-outage | rule / 20m | `longhorn_volume_state == 4 or longhorn_volume_state == 5` | P09 | both | 13 | `longhorn-replica-loss.md` |
| `SCLonghornBackupTargetUnavailable` | backup | warning / notify | failed-backup | rule / 15m | `max(sc_longhorn_backup_target_available) == 0` | P10 | both | 13 | `object-storage-outage.md` |
| `SCLonghornBackupStale` | backup | warning / notify | failed-backup | rule / 15m | `time() - sc:longhorn_last_backup_success_timestamp_seconds > on (volume) group_left sc_longhorn_backup_max_age_seconds` | P10 | both | 13 | `restore-longhorn.md` |
| `SCOffsiteBackupFailing24h` | black-start-threat | critical / page | black-start-threat | rule / 15m | `time() - sc:backup_last_success_timestamp_seconds > 86400` | P10 | both | 44 | `total-cluster-loss.md` |
| `SCRepoBundleBackupStale` | backup | warning / notify | failed-backup | check-cronjob / fail 30m, stale 1h | `forgejo/repo-bundle-backup` (ADR-0553) | P10 | both | 44 | `restore-test-failure.md` |
| `SCRestoreTestCnpgFailed` | restore-test | warning / notify | failed-backup | check-cronjob / fail 3h, stale 8d | `dr-tests/restore-test-cnpg` (ADR-0553) | P10 | both | 44 | `restore-test-failure.md` |
| `SCRestoreTestReposFailed` (aka `RepoBundleVerifyFailed`) | restore-test | warning / notify | failed-backup | check-cronjob / fail 2h, stale 8d | `dr-tests/restore-test-repos` (ADR-0553) | P10 | both | 44 | `restore-test-failure.md` |
| `SCRestoreTestLonghornFailed` | restore-test | warning / notify | failed-backup | check-cronjob / fail 2h, stale 8d | `dr-tests/restore-test-longhorn` (ADR-0553) | P10 | both | 44 | `restore-test-failure.md` |
| `SCRestoreTestEtcdFailed` | restore-test | warning / notify | failed-backup | check-cronjob / fail 1h, stale 8d | `dr-tests/restore-test-etcd` (ADR-0553) | P10 | both | 44 | `restore-test-failure.md` |
| `SCRestoreTestAppsFailed` | restore-test | warning / notify | failed-backup | check-cronjob / fail 4h, stale 35d | `dr-tests/restore-test-apps` (ADR-0553) | P10 | both | 44 | `restore-test-failure.md` |
| `SCDrCanaryWriterFailed` | restore-test | warning / notify | none | check-cronjob / fail 1h, stale 26h | `dr-tests/dr-canary-writer` (ADR-0553) | P10 | both | 44 | `restore-test-failure.md` |
| `SCDrMarkerHeartbeatFailed` | backup | warning / notify | failed-backup | check-cronjob / fail 15m, stale 30m | `dr-tests/dr-marker-heartbeat` (ADR-0553) | P10 | both | 44 | `restore-test-failure.md` |
| `SCTofuStateRestoreTestFailed` (aka `TofuStateRestoreTestFailed`, `TofuStateRestoreTestOverdue`, `SCRestoreTestTofuStateFailed`, `SCTofuStateRestoreTestOverdue`) | black-start-threat | critical / page | black-start-threat | check-cronjob / fail 1h, stale 8d | `dr-tests/restore-test-tofu-state` (ADR-0553) | P10 | both | 06 | `recover-opentofu-state.md` |
| `SCTofuStateLockStale` (aka `TofuStateLockStale`) | backup | warning / notify | none | check-cronjob / fail 1h, stale 26h | `dr-tests/check-tofu-lock-age` (ADR-0553) | P10 | both | 06 | `recover-opentofu-state.md` |
| `SCGitHubBlackStartTestFailed` | black-start-threat | warning / notify | black-start-threat | check-cronjob / fail 1h, stale 8d | `dr-tests/restore-test-github-black-start` (ADR-0553) | P10 | both | 44 | `github-black-start.md` |
| `SCRecoveryBundleOutdated` | black-start-threat | warning / notify | black-start-threat | check-cronjob / fail 1h, stale 26h | `dr-tests/recovery-bundle-currency` (ADR-0553) | P10 | both | 35 | `rotate-secrets.md` |
| `SCRecoveryBundleVerifyFailed` | black-start-threat | warning / notify | black-start-threat | check-cronjob / fail 1h, stale 8d | `dr-tests/restore-test-recovery-bundle` (ADR-0553) | P10 | both | 35 | `rotate-secrets.md` |
| `SCRecoveryKeyAttestationOverdue` | black-start-threat | warning / notify | black-start-threat | check-cronjob / fail 1h, stale 26h | `dr-tests/recovery-key-attestation` (ADR-0553) | P10 | both | 35 | `rotate-age-key.md` |
| `SCSecretRotationOverdue` | security | warning / notify | none | check-cronjob / fail 1h, stale 26h | `dr-tests/secret-rotation-age` (ADR-0553) | P10 | both | 35 | `rotate-secrets.md` |
| `SCSecretRotationDue` | security | info / silent | none | rule / 1h | `max(sc_secret_rotation_due) > 0` | P10 | both | 35 | `rotate-secrets.md` |
| `SCSecretScanHit` | security | critical / page | none | check-cronjob / fail 1h, stale 26h | `dr-tests/secret-scan-history` (ADR-0553) | P10 | both | 35 | `credential-compromise.md` |
| `SCSopsDecryptionFailing` | security | critical / page | critical-outage | rule / 10m | `max by (exported_namespace, name) (gotk_resource_info{customresource_kind="Kustomization",ready="False",reason="DecryptionFailed"}) == 1` | P09 | both | 35 | `sops-decryption-failure.md` |
| `SCAdmissionPolicyError` | security | critical / page | none | rule / 0s | `sum(increase(apiserver_validating_admission_policy_check_total{error_type!=""}[10m])) > 0` | P09 | both | 37 | `credential-compromise.md` |
| `SCAdmissionPolicyMissing` | security | critical / page | none | rule / 10m | `count(kube_customresource_info{customresource_kind="ValidatingAdmissionPolicyBinding",name=~"sc-.*"}) < on() max(sc_admission_expected_bindings)` | P09 | both | 37 | `credential-compromise.md` |
| `SCAdmissionDenialSpike` | security | warning / notify | none | rule / 0s | `sum by (policy) (increase(apiserver_validating_admission_policy_check_total{enforcement_action="deny"}[15m])) > 5` | P09 | both | 37 | `tdd-task-recovery.md` |
| `SCAdmissionAuditHits` | security | info / silent | none | rule / 0s | `sum by (policy) (increase(apiserver_validating_admission_policy_check_total{enforcement_action="warn"}[1h])) > 0` | P09 | both | 37 | `credential-compromise.md` |
| `SCPodSecurityViolation` | security | warning / notify | none | rule / 0s | `sum by (policy_level) (increase(pod_security_evaluations_total{decision="deny",mode="enforce"}[15m])) > 0` | P09 | both | 37 | `credential-compromise.md` |
| `SCNetworkPolicyControllerDown` | security | critical / page | critical-outage | check-cronjob / fail 15m, stale 30m | `monitoring/netpol-canary` (ADR-0553) | P09 | both | 36 | `lost-node.md` |
| `SCEgressProxyDown` | ci | critical / page | none | rule / 5m | `sum(up{job="egress-proxy"}) == 0 and on() count(kube_deployment_created{deployment="egress-proxy"}) > 0` | C11 | both | 36 | `agent-runner-failure.md` |
| `SCEgressDeniedSpike` | security | warning / notify | none | rule / 0s | `sum(increase(sc_egress_requests_total{decision="deny"}[15m])) > 200` | C11 | both | 36 | `credential-compromise.md` |
| `SCEgressPolicyLoadFailed` | security | warning / notify | none | rule / 0s | `sum(increase(sc_egress_policy_load_errors_total[10m])) > 0` | C11 | both | 36 | `agent-runner-failure.md` |
| `SCBreakGlassTestOverdue` (aka `BreakGlassDrillOverdue`) | security | warning / notify | black-start-threat | rule / 1h | `time() - sc_breakglass_drill_last_success_timestamp_seconds > 8640000 or absent(sc_breakglass_drill_last_success_timestamp_seconds)` | P10 | both | 15 | `break-glass-access.md` |
| `SCRecoveryDrillOverdue` | black-start-threat | warning / notify | black-start-threat | check-cronjob / fail 1h, stale 26h | `dr-tests/rt-drill-freshness` (ADR-0553) | P10 | both | 45 | `recovery-drill.md` |
| `SCBootstrapCapsuleCheckFailed` | black-start-threat | warning / notify | black-start-threat | check-cronjob / fail 1h, stale 26h | `dr-tests/restore-test-bootstrap-capsule-light` (ADR-0553) | H04 | both | 44 | `github-black-start.md` |
| `SCBootstrapCapsuleFullCheckFailed` | black-start-threat | warning / notify | black-start-threat | check-cronjob / fail 3h, stale 8d | `dr-tests/restore-test-bootstrap-capsule-full` (ADR-0553) | H04 | both | 44 | `github-black-start.md` |
| `SCHADrillOverdue` | black-start-threat | warning / notify | black-start-threat | rule / 1h | `time() - sc_ha_drill_last_success_timestamp_seconds > 100 * 86400` | H04 | small-ha | 47 | `expand-to-small-ha.md` |
| `SCObjectStorageUnreachable` (aka `ObjectStorageDown`) | black-start-threat | critical / page | black-start-threat | rule / 10m | `probe_success{probe="object-storage"} == 0` | P09 | both | 14 | `object-storage-outage.md` |
| `SCObjectStorageCanaryFailing` | black-start-threat | critical / page | black-start-threat | check-cronjob / fail 30m, stale 45m | `dr-tests/objstore-canary` (ADR-0553) | P10 | both | 14 | `object-storage-outage.md` |
| `SCObjectStorageConfigDrift` (aka `TofuStateVersioningDisabled`) | black-start-threat | critical / page | black-start-threat | check-cronjob / fail 1h, stale 26h | `dr-tests/objstore-audit` (ADR-0553) | P10 | both | 14 | `object-storage-outage.md` |
| `SCObjectStorageProtectionWeak` | object-storage | warning / notify | none | rule / 1h | `max(sc_objstore_protection_weak) == 1` | P10 | both | 14 | `object-storage-outage.md` |
| `SCStorageAuditFailed` | storage | warning / notify | none | check-cronjob / fail 2h, stale 36h | `dr-tests/storage-audit` (ADR-0553) | P10 | both | 11 | `disk-pressure.md` |
| `SCRetentionAuditFailed` | storage | warning / notify | none | check-cronjob / fail 1h, stale 36h | `dr-tests/retention-audit` (ADR-0553) | P10 | both | 43 | `disk-pressure.md` |
| `SCCrRetentionFailed` | storage | warning / notify | none | check-cronjob / fail 1h, stale 36h | `dr-tests/cr-retention` (ADR-0553) | P10 | both | 43 | `disk-pressure.md` |
| `SCOciReferencedImageMissing` | black-start-threat | critical / page | black-start-threat | check-cronjob / fail 15m, stale 36h | `forgejo/oci-retention-audit` (ADR-0553) | P10 | both | 43 | `restore-forgejo.md` |
| `SCOciRetentionFailed` | storage | warning / notify | none | check-cronjob / fail 1h, stale 8d | `forgejo/oci-retention` (ADR-0553) | P10 | both | 43 | `disk-pressure.md` |
| `SCRetentionHermesSessionsOverdue` | runner | warning / notify | none | check-cronjob / fail 1h, stale 36h | `hermes/hermes-session-prune` (ADR-0553) | P10 | both | 43 | `hermes-failure.md` |
| `SCRetentionTelemetryOverdue` | storage | warning / notify | none | rule / 1h | `max(sc_ledger_usage_raw_oldest_seconds) > 92 * 86400 or max(sc_ledger_denials_oldest_seconds) > 92 * 86400 or max(sc_ledger_reservations_oldest_closed_seconds) > 32 * 86400` | C11 | both | 43 | `gateway-failure.md` |
| `SCRetentionRunnerSweeperStalled` | runner | warning / notify | none | rule / 1h | `max(sc_runner_retention_oldest_seconds{table=~"idempotency_keys\|webhook_deliveries"}) > 8 * 86400 or max(sc_runner_retention_oldest_seconds{table="execution_events"}) > 401 * 86400` | C11 | both | 43 | `agent-runner-failure.md` |
| `SCForgejoDown` (aka `ForgejoDown`) | forge | critical / page | critical-outage | rule / 3m | `probe_success{probe="forgejo-http"} == 0` | P09 | both | 16 | `restore-forgejo.md` |
| `SCForgejoSSHDown` (aka `ForgejoSSHDown`) | forge | critical / page | critical-outage | rule / 5m | `probe_success{probe="forgejo-ssh"} == 0` | P09 | both | 16 | `restore-forgejo.md` |
| `SCForgejoPublicEndpointDown` | ingress | warning / notify | none | rule / 5m | `probe_success{probe="public-git"} == 0` | P09 | both | 09 | `edge-ingress-failure.md` |
| `SCForgejoPodStuckPending` (aka `ForgejoPodStuckPending`) | forge | warning / notify | none | rule / 10m | `max(kube_pod_status_phase{namespace="forgejo",pod=~"forgejo-[a-z0-9]+-[a-z0-9]+",phase="Pending"}) == 1 or max(kube_pod_container_status_waiting_reason{namespace="forgejo",container="forgejo",reason="ContainerCreating"}) == 1` | P09 | both | 16 | `restore-forgejo.md` |
| `SCForgejoPodStuckPendingCritical` | forge | critical / page | critical-outage | rule / 20m | `max(kube_pod_status_phase{namespace="forgejo",pod=~"forgejo-[a-z0-9]+-[a-z0-9]+",phase="Pending"}) == 1 or max(kube_pod_container_status_waiting_reason{namespace="forgejo",container="forgejo",reason="ContainerCreating"}) == 1` | P09 | both | 16 | `restore-forgejo.md` |
| `SCForgejoObjectStorageErrors` (aka `ForgejoObjectStorageErrors`) | forge | warning / notify | none | rule / 10m | `sum(rate(traefik_service_requests_total{exported_service=~"forgejo-forgejo-http.*",code=~"5.."}[10m])) > 0 and on() max(probe_success{probe="forgejo-http"}) == 1` | P09 | both | 16 | `object-storage-outage.md` |
| `SCForgejoSSHHostKeyMismatch` (aka `ForgejoSSHHostKeyMismatch`) | forge | critical / page | critical-outage | rule / 0s | `min(sc_forgejo_ssh_hostkey_match) == 0` | P09 | both | 16 | `rotate-forge-credentials.md` |
| `SCForgeBranchProtectionDrift` | security | critical / page | none | rule / 10m | `max(sc_forge_config_drift{object=~"branch_protection.*"}) == 1` | P09 | both | 16 | `credential-compromise.md` |
| `SCForgeConfigDrift` (aka `ForgeConfigDrift`) | forge | warning / notify | none | rule / 10m | `max by (object) (sc_forge_config_drift{object!~"branch_protection.*"}) == 1` | P09 | both | 16 | `rotate-forge-credentials.md` |
| `SCForgeBotTokenInvalid` (aka `ForgeBotTokenInvalid`) | forge | critical / page | critical-outage | rule / 15m | `min by (bot) (sc_forge_bot_token_valid) == 0` | P09 | both | 16 | `rotate-forge-credentials.md` |
| `SCForgeUnexpectedAdminToken` | security | critical / page | security-audit | rule / 15m | `max(sc_forge_unexpected_admin_tokens) > 0` | P09 | both | 16 | `credential-compromise.md` |
| `SCRunnerOrphanDeployKeys` (aka `SCForgejoOrphanedDeployKeys`) | security | critical / page | none | rule / 10m | `max(sc_runner_deploy_keys_orphaned) > 0` | C11 | both | 26 | `quarantine-worker.md` |
| `SCAuthentikDown` (aka `AuthentikDown`) | identity | critical / page | critical-outage | rule / 5m | `probe_success{probe="authentik"} == 0` | P09 | both | 15 | `authentik-outage.md` |
| `SCAuthentikWorkerDown` (aka `AuthentikWorkerDown`) | identity | warning / notify | none | rule / 10m | `kube_deployment_status_replicas_available{namespace="authentik",deployment=~".*worker.*"} == 0` | P09 | both | 15 | `authentik-outage.md` |
| `SCAuthentikTasksFailing` (aka `AuthentikTasksFailing`) | identity | warning / notify | none | rule / 30m | `sum(authentik_system_tasks{status="error"}) > 0` | P09 | both | 15 | `authentik-outage.md` |
| `SCAuthentikBlueprintFailed` (aka `AuthentikBlueprintFailed`) | identity | warning / notify | none | rule / 15m | `max by (instance) (sc_authentik_blueprint_status{status!="successful"}) == 1` | P09 | both | 15 | `restore-authentik.md` |
| `SCAuthentikBootstrapAdminActive` (aka `AuthentikBootstrapAdminActive`) | security | critical / page | none | rule / 30m | `min(sc_breakglass_account_ready{system="authentik",account="akadmin"}) == 0 or max(sc_authentik_bootstrap_token_present) == 1` | P09 | both | 15 | `break-glass-access.md` |
| `SCAuthentikUpstreamFlowOpen` | security | critical / page | security-audit | rule / 15m | `max(sc_authentik_upstream_flow_open) == 1` | P09 | both | 15 | `credential-compromise.md` |
| `SCAuthentikSigningCertExpiring` | identity | warning / notify | none | rule / 1h | `min(sc_authentik_signing_cert_expiry_timestamp_seconds) - time() < 30 * 86400` | P09 | both | 15 | `authentik-outage.md` |
| `SCOIDCLoginBroken` (aka `OIDCLoginBroken`) | identity | critical / page | critical-outage | rule / 10m | `min by (app) (sc_oidc_discovery_ok) == 0` | P09 | both | 15 | `authentik-outage.md` |
| `SCBreakGlassAccountNotReady` (aka `BreakGlassAccountNotReady`) | black-start-threat | critical / page | black-start-threat | rule / 30m | `min by (system, account) (sc_breakglass_account_ready) == 0 or min by (account) (sc_breakglass_ssh_key_present) == 0` | P09 | both | 15 | `break-glass-access.md` |
| `SCBreakGlassAuditDown` (aka `BreakGlassAuditDown`) | security | warning / notify | none | rule / 30m | `min(sc_breakglass_probe_success) == 0` | P09 | both | 15 | `break-glass-access.md` |
| `SCPlatformProbesDown` (aka `PlatformProbesDown`) | monitoring | warning / notify | none | rule / 15m | `absent(up{job="platform-probes"} == 1)` | P09 | both | 15 | `monitoring-failure.md` |
| `SCBreakGlassUsed` (aka `BreakGlassUsed`) | security | critical / page | security-audit | rule / 0s | `(time() - max by (system, account) (sc_breakglass_last_login_timestamp_seconds)) < 86400 or (time() - max by (account) (label_replace(sc_breakglass_ssh_last_login_timestamp_seconds, "system", "cluster", "", ""))) < 86400` | P09 | both | 15 | `break-glass-access.md` |
| `SCGrafanaLocalLogin` | security | critical / page | security-audit | rule / 0s | `sum(increase(grafana_api_login_post_total{job="grafana"}[5m])) > 0` | P09 | both | 39 | `break-glass-access.md` |
| `SCK3sBreakGlassAuditSeen` (aka `SCClusterBreakGlassUsed`) | security | critical / page | security-audit | rule / 0s | `sum(increase(sc_k3s_audit_breakglass_requests_total[10m])) > 0` | P09 | both | 07 | `break-glass-access.md` |
| `SCGitHubMirrorFailing` | mirror | warning / notify | failed-mirror | rule / 15m | `sc_github_mirror_last_attempt_success{repository="strange-company"} == 0 and on() sc_github_mirror_expected == 1` | P09 | both | 17 | `github-mirror-failure.md` |
| `SCProjectMirrorFailing` | mirror | warning / notify | none | rule / 1h | `sc_github_mirror_last_attempt_success{repository=~"projects/.+"} == 0` | P09 | both | 17 | `github-mirror-failure.md` |
| `SCGitHubMirrorRPOBreach` | black-start-threat | critical / page | black-start-threat | rule / 5m | `sc_github_mirror_lag_seconds{repository="strange-company"} > 3600` | P09 | both | 17 | `github-mirror-failure.md` |
| `SCGitHubMirrorSignalMissing` | mirror | warning / notify | failed-mirror | rule / 30m | `absent(sc_github_mirror_last_attempt_success{repository="strange-company"}) or sc_github_api_up == 0` | P09 | both | 17 | `github-mirror-failure.md` |
| `SCGitHubMirrorStale` (aka `GitHubMirrorStale`) | mirror | warning / notify | failed-mirror | rule / 5m | `sc_github_mirror_lag_seconds{repository="strange-company"} > 900` | P09 | both | 17 | `github-mirror-failure.md` |
| `SCGitHubMirrorDiverged` | black-start-threat | critical / page | black-start-threat | rule / 5m | `sc_github_mirror_diverged{repository="strange-company"} == 1` | P09 | both | 17 | `github-mirror-failure.md` |
| `SCGitHubMirrorNotConfigured` | black-start-threat | critical / page | black-start-threat | rule / 15m | `sc_github_mirror_configured == 0 and on() sc_github_mirror_expected == 1` | P09 | both | 17 | `github-mirror-failure.md` |
| `SCGitHubRepoSettingsDrift` | black-start-threat | critical / page | black-start-threat | rule / 30m | `min by (kind) (sc_github_repo_setting_ok{kind=~"visibility\|history_protection\|mirror_only"}) == 0` | P09 | both | 17 | `github-mirror-failure.md` |
| `SCGitHubRepoSettingsDriftMinor` | mirror | warning / notify | none | rule / 30m | `min by (kind) (sc_github_repo_setting_ok{kind!~"visibility\|history_protection\|mirror_only"}) == 0` | P09 | both | 17 | `github-mirror-failure.md` |
| `SCGitHubMirrorTokenExpiring` | mirror | warning / notify | failed-mirror | rule / 1h | `min(sc_github_mirror_token_expiry_timestamp_seconds) - time() < 21 * 86400` | P09 | both | 17 | `github-mirror-failure.md` |
| `SCGitHubMirrorTokenExpiryImminent` | mirror | critical / page | failed-mirror | rule / 1h | `min(sc_github_mirror_token_expiry_timestamp_seconds) - time() < 3 * 86400` | P09 | both | 17 | `github-mirror-failure.md` |
| `SCGitHubMirrorRecoveryModeTooLong` (aka `SCRecoveryModeActive`) | black-start-threat | warning / notify | black-start-threat | rule / 7d | `max(sc_github_mirror_expected) == 0` | P12 | both | 17 | `github-mirror-failure.md` |
| `SCFluxReconciliationFailing` (aka `FluxReconciliationFailed`, `FluxKustomizationNotReady`) | gitops | critical / page | none | rule / 15m | `max by (customresource_kind, exported_namespace, name) (gotk_resource_info{customresource_kind=~"Kustomization\|HelmRelease",ready="False",suspended="false"}) == 1` | P09 | both | 18 | `flux-reconciliation-failure.md` |
| `SCFluxSourceFailing` (aka `FluxSourceNotReady`) | gitops | critical / page | critical-outage | rule / 10m | `max by (exported_namespace, name) (gotk_resource_info{customresource_kind="GitRepository",ready="False",suspended="false"}) == 1` | P09 | both | 18 | `flux-reconciliation-failure.md` |
| `SCFluxSuspendedTooLong` (aka `FluxResourceSuspended`) | gitops | warning / notify | none | rule / 2h | `max by (customresource_kind, exported_namespace, name) (gotk_resource_info{suspended="true"}) == 1` | P09 | both | 18 | `flux-reconciliation-failure.md` |
| `SCFluxControllerDown` (aka `FluxControllerDown`) | gitops | critical / page | critical-outage | rule / 5m | `kube_deployment_status_replicas_available{namespace="flux-system"} < 1` | P09 | both | 18 | `flux-reconciliation-failure.md` |
| `SCFluxSourceNotReady` | gitops | warning / notify | none | rule / 10m | `max by (customresource_kind, exported_namespace, name) (gotk_resource_info{customresource_kind=~"OCIRepository\|HelmRepository\|Bucket",ready="False",suspended="false"}) == 1` | P09 | both | 18 | `flux-reconciliation-failure.md` |
| `SCFluxSeedSourcePresent` (aka `FluxSeedSourcePresent`) | gitops | warning / notify | none | rule / 2h | `max(gotk_resource_info{customresource_kind="Bucket",exported_namespace="flux-system"}) == 1` | P09 | both | 18 | `failed-flux-handoff.md` |
| `SCFluxReconcileStalled` (aka `FluxReconcileStalled`) | gitops | warning / notify | none | rule / 60m | `max by (customresource_kind, exported_namespace, name) (gotk_resource_info{customresource_kind=~"Kustomization\|HelmRelease",ready="Unknown",suspended="false"}) == 1` | P09 | both | 18 | `flux-reconciliation-failure.md` |
| `SCWoodpeckerDown` | ci | critical / page | critical-outage | rule / 5m | `probe_success{probe="woodpecker"} == 0` | P09 | both | 20 | `woodpecker-failure.md` |
| `SCWoodpeckerNoAgents` | ci | critical / page | critical-outage | rule / 10m | `max(woodpecker_worker_count{job="woodpecker"}) == 0` | P09 | both | 20 | `woodpecker-failure.md` |
| `SCWoodpeckerBacklog` | ci | warning / notify | none | rule / 30m | `max(woodpecker_pending_steps{job="woodpecker"}) > 5` | P09 | both | 20 | `woodpecker-failure.md` |
| `SCWoodpeckerAgentUnavailable` | ci | critical / page | critical-outage | rule / 10m | `kube_deployment_status_replicas_available{namespace="woodpecker",deployment="woodpecker-agent"} < 1` | P09 | both | 20 | `woodpecker-failure.md` |
| `SCWoodpeckerBuildAgentUnavailable` | ci | warning / notify | none | rule / 10m | `kube_deployment_status_replicas_available{namespace="woodpecker",deployment="woodpecker-agent-build"} < 1` | P09 | both | 20 | `woodpecker-failure.md` |
| `SCCIConfigExtensionDown` | ci | critical / page | critical-outage | rule / 5m | `up{job="sc-ci-config"} == 0 or kube_deployment_status_replicas_available{deployment="sc-ci-config"} < 1` | P09 | both | 20 | `woodpecker-failure.md` |
| `SCCIConfigSignatureFailures` | ci | warning / notify | none | rule / 0s | `increase(sc_ci_config_signature_failures_total[15m]) > 3` | P09 | both | 20 | `woodpecker-failure.md` |
| `SCCIRepoPolicyViolation` | security | critical / page | none | rule / 0s | `increase(sc_ci_config_repo_policy_violations_total[15m]) > 0` | P09 | both | 20 | `woodpecker-failure.md` |
| `SCCIBuildLaneAgentMismatch` | security | critical / page | security-audit | rule / 0s | `max(sc_ci_build_lane_agent_mismatch) == 1` | P09 | both | 20 | `woodpecker-failure.md` |
| `SCCIUntrustedConstructDropped` | ci | warning / notify | none | rule / 0s | `increase(sc_ci_config_dropped_workflows_total[1h]) > 0` | P09 | both | 20 | `woodpecker-failure.md` |
| `SCCIWorkspaceFilesystemHigh` | ci | warning / notify | none | rule / 10m | `1 - node_filesystem_avail_bytes{mountpoint="/var/lib/sc/ci-workspace"} / node_filesystem_size_bytes{mountpoint="/var/lib/sc/ci-workspace"} > 0.8` | P09 | both | 20 | `woodpecker-failure.md` |
| `SCCIWorkspaceFilesystemCritical` | ci | critical / page | none | rule / 10m | `1 - node_filesystem_avail_bytes{mountpoint="/var/lib/sc/ci-workspace"} / node_filesystem_size_bytes{mountpoint="/var/lib/sc/ci-workspace"} > 0.95` | P09 | both | 20 | `woodpecker-failure.md` |
| `SCCIMaintFailed` (aka `SCCIMaintStale`, `SCRetentionWoodpeckerPurgeFailed`) | ci | warning / notify | none | check-cronjob / fail 2h, stale 3h | `woodpecker/ci-maint` (ADR-0553) | P09 | both | 20 | `woodpecker-failure.md` |
| `SCCINightlyFailing` | ci | warning / notify | none | rule / 0s | `max(sc_ci_nightly_last_conclusion{conclusion="failure"}) == 1` | P09 | both | 52 | `woodpecker-failure.md` |
| `SCCIOffclusterLaneFailing` | ci | warning / notify | none | rule / 0s | `max(sc_ci_offcluster_lane_last_run_conclusion{conclusion="failure"}) == 1` | P09 | both | 52 | `woodpecker-failure.md` |
| `SCCIOffclusterLaneStale` | ci | warning / notify | none | rule / 1h | `time() - max(sc_ci_offcluster_lane_last_success_timestamp_seconds) > 8 * 86400` | P09 | both | 52 | `woodpecker-failure.md` |
| `SCEdgeDown` | ingress | critical / page | critical-outage | rule / 5m | `count(probe_success{probe_kind="public"} == 0) == count(probe_success{probe_kind="public"})` | P09 | both | 09 | `edge-ingress-failure.md` |
| `SCTraefikDown` | ingress | critical / page | critical-outage | rule / 3m | `sum(up{job="traefik"}) == 0` | P09 | both | 09 | `edge-ingress-failure.md` |
| `SCCloudflaredTunnelDown` | ingress | critical / page | critical-outage | rule / 5m | `sum(cloudflared_tunnel_ha_connections{job="cloudflared"}) == 0 or (absent(up{job="cloudflared"} == 1) and on() count(kube_deployment_created{namespace="ingress",deployment="cloudflared"}) > 0)` | P09 | both | 09 | `edge-ingress-failure.md` |
| `SCCoreDNSDown` | node | critical / page | critical-outage | rule / 2m | `sum(up{job="coredns"}) == 0` | P09 | both | 09 | `lost-node.md` |
| `SCDNSRewriteBroken` | ingress | warning / notify | none | rule / 15m | `probe_success{probe="dns-rewrite"} == 0` | P09 | both | 09 | `edge-ingress-failure.md` |
| `SCWireGuardPeerStale` | node | critical / page | critical-outage | rule / 10m | `time() - sc_flannel_wg_latest_handshake_seconds > 600` | H04 | small-ha | 09 | `lost-node.md` |
| `SCCertificateExpiringSoon` | certificate | warning / notify | none | rule / 1h | `certmanager_certificate_expiration_timestamp_seconds - time() < 14 * 86400` | P09 | both | 10 | `certificate-failure.md` |
| `SCCertificateExpiryImminent` | certificate | critical / page | critical-outage | rule / 10m | `(certmanager_certificate_expiration_timestamp_seconds - time() < 3 * 86400) or (probe_ssl_earliest_cert_expiry - time() < 3 * 86400)` | P09 | both | 10 | `certificate-failure.md` |
| `SCCertificateNotReady` | certificate | warning / notify | none | rule / 30m | `certmanager_certificate_ready_status{condition="False"} == 1` | P09 | both | 10 | `certificate-failure.md` |
| `SCK3sCertificateExpiringSoon` | certificate | warning / notify | none | rule / 1h | `min by (instance) (sc_k3s_cert_expiry_timestamp_seconds) - time() < 30 * 86400` | P09 | both | 07 | `certificate-failure.md` |
| `SCCertManagerDown` (aka `SCCertManagerWebhookDown`) | certificate | warning / notify | none | rule / 15m | `absent(up{job="cert-manager"} == 1)` | P09 | both | 10 | `certificate-failure.md` |
| `SCCnpgCertificateExpiringSoon` | certificate | critical / page | critical-outage | rule / 1h | `min by (cluster) (sc_cnpg_certificate_expiration_timestamp_seconds) - time() < 5 * 86400` | P09 | both | 10 | `certificate-failure.md` |
| `SCInternalCAExpiringSoon` | certificate | warning / notify | none | rule / 1h | `min(sc_internal_ca_expiry_timestamp_seconds) - time() < 365 * 86400` | P09 | both | 10 | `certificate-failure.md` |
| `SCRunnerDown` (aka `SCAgentRunnerDown`) | runner | critical / page | critical-outage | rule / 5m | `absent(up{job="agent-runner"} == 1) and on() count(kube_deployment_created{namespace="strange-company",deployment="agent-runner"}) > 0` | C11 | both | 26 | `agent-runner-failure.md` |
| `SCRunnerNotReady` | runner | warning / notify | none | rule / 10m | `probe_success{probe="agent-runner"} == 0` | C11 | both | 26 | `agent-runner-failure.md` |
| `SCRunnerDatabaseDown` | runner | critical / page | critical-outage | rule / 2m | `max(sc_runner_dependency_up{dep="sc-pg"}) == 0` | C11 | both | 26 | `agent-runner-failure.md` |
| `SCRunnerDependencyDown` | runner | warning / notify | none | rule / 10m | `max by (dep) (sc_runner_dependency_up{dep!="sc-pg"}) == 0` | C11 | both | 26 | `agent-runner-failure.md` |
| `SCRunnerOutboxStalled` | runner | warning / notify | none | rule / 0s | `max by (kind) (sc_runner_outbox_oldest_seconds) > 1800` | C11 | both | 26 | `agent-runner-failure.md` |
| `SCRunnerOrphanJobs` | runner | warning / notify | none | rule / 0s | `sum(increase(sc_runner_orphan_jobs_deleted_total[1h])) > 0` | C11 | both | 26 | `agent-runner-failure.md` |
| `SCRunnerLogArchiveFailing` | runner | warning / notify | none | rule / 30m | `sum(increase(sc_runner_log_archive_failures_total[30m])) > 0` | C11 | both | 26 | `object-storage-outage.md` |
| `SCRunnerUnexplainedRefChange` | security | warning / notify | none | rule / 0s | `sum(increase(sc_runner_unexplained_ref_changes_total[1h])) > 0` | C11 | both | 26 | `quarantine-worker.md` |
| `SCRunnerQueueBacklog` | runner | warning / notify | none | rule / 0s | `max(sc_runner_queue_oldest_seconds) > 7200` | C11 | both | 26 | `stuck-task-recovery.md` |
| `SCRunnerAdminAccess` | security | info / silent | none | rule / 0s | `sum(increase(sc_runner_admin_requests_total[10m])) > 0` | C11 | both | 26 | `break-glass-access.md` |
| `SCRunnerPolicyCommitUnknown` | runner | warning / notify | none | rule / 5m | `min(sc_runner_policy_commit_resolved) == 0` | C11 | both | 26 | `agent-runner-failure.md` |
| `SCWorkerQuarantined` | security | warning / notify | none | rule / 0s | `max(sc_runner_quarantined_refs) > 0` | C11 | both | 26 | `quarantine-worker.md` |
| `SCHermesCredentialRotationFailed` | runner | warning / notify | none | rule / 0s | `sum(increase(sc_runner_hermes_credential_rotation_failures_total[2h])) > 0` | C11 | both | 26 | `hermes-failure.md` |
| `SCAgentRunnerReconcileStalled` | runner | critical / page | critical-outage | rule / 2m | `time() - max(sc_runner_reconcile_last_success_timestamp_seconds) > 300` | C11 | both | 26 | `agent-runner-failure.md` |
| `SCRunnerPolicyInvalid` (aka `SCAgentRunnerPolicyLoadFailed`) | runner | critical / page | critical-outage | rule / 1m | `min(sc_runner_policy_valid) == 0` | C11 | both | 26 | `agent-runner-failure.md` |
| `SCExecutionStuck` | runner | warning / notify | none | rule / 10m | `sum(sc_runner_executions_stuck) > 0` | C11 | both | 26 | `stuck-task-recovery.md` |
| `SCExecutionInfraFailuresHigh` (aka `SCRunnerDispatchFailures`) | runner | warning / notify | none | rule / 0s | `sum(increase(sc_runner_results_total{failure_class="infrastructure"}[1h])) > 3` | C11 | both | 26 | `agent-runner-failure.md` |
| `SCHermesDown` | runner | critical / page | critical-outage | rule / 5m | `probe_success{probe="hermes-gateway"} == 0` | C11 | both | 24 | `hermes-failure.md` |
| `SCHermesRestartLoop` | runner | warning / notify | none | rule / 0s | `sum(increase(kube_pod_container_status_restarts_total{namespace="hermes"}[30m])) > 3` | C11 | both | 24 | `hermes-failure.md` |
| `SCHermesMcpSilent` | runner | warning / notify | none | rule / 0s | `sum(increase(sc_runner_mcp_requests_total[2h])) == 0 and on() sum(sc_runner_tasks) > 0 and on() max(probe_success{probe="hermes-gateway"}) == 1` | C11 | both | 24 | `hermes-failure.md` |
| `SCModelGatewayDown` (aka `GatewayDown`) | gateway | critical / page | critical-outage | rule / 3m | `absent(up{job="model-gateway"} == 1) and on() count(kube_deployment_created{namespace="strange-company",deployment="model-gateway"}) > 0` | C11 | both | 53 | `gateway-failure.md` |
| `SCModelProviderErrorsHigh` (aka `GatewayUpstreamErrorRateHigh`) | gateway | warning / notify | none | rule / 15m | `(sum by (provider) (rate(sc_gateway_upstream_responses_total{status_class!="2xx"}[15m])) / sum by (provider) (rate(sc_gateway_upstream_responses_total[15m])) > 0.20) and sum by (provider) (increase(sc_gateway_upstream_responses_total[15m])) >= 20` | C11 | both | 53 | `gateway-failure.md` |
| `SCModelMismatch` (aka `ModelMismatch`) | gateway | warning / notify | none | rule / 0s | `sum by (provider, model) (increase(sc_gateway_model_mismatch_total[15m])) > 0` | C11 | both | 53 | `gateway-failure.md` |
| `SCModelUsageUnknown` (aka `GatewayUsageUnknownHigh`) | gateway | warning / notify | none | rule / 0s | `sum by (provider) (increase(sc_gateway_usage_status_total{usage_status="unknown"}[1h])) / sum by (provider) (increase(sc_gateway_usage_status_total[1h])) > 0.05` | C11 | both | 53 | `gateway-failure.md` |
| `SCModelUnpricedDenied` | gateway | warning / notify | none | rule / 0s | `sum(increase(sc_gateway_denials_total{code="SC_PRICE_UNKNOWN"}[1h])) > 0` | C11 | both | 32 | `pricing-update.md` |
| `SCLedgerWriteFailing` (aka `GatewayLedgerWriteFailing`) | gateway | critical / page | budget-stop | rule / 0s | `sum(increase(sc_gateway_settlement_failures_total[5m])) > 0` | C11 | both | 53 | `gateway-failure.md` |
| `SCGatewayNotReady` (aka `GatewayNotReady`) | gateway | warning / notify | none | rule / 5m | `probe_success{probe="model-gateway"} == 0` | C11 | both | 53 | `gateway-failure.md` |
| `SCGatewayPolicyInvalid` (aka `GatewayPolicyInvalid`) | gateway | critical / page | critical-outage | rule / 0s | `sum(increase(sc_gateway_policy_load_errors_total[10m])) > 0` | C11 | both | 53 | `gateway-failure.md` |
| `SCModelProviderUnavailable` (aka `ModelProviderUnavailable`) | gateway | warning / notify | none | rule / 10m | `min by (provider) (sc_gateway_provider_available) == 0` | C11 | both | 53 | `rotate-model-credentials.md` |
| `SCModelCredentialRejected` (aka `ModelCredentialRejected`) | gateway | critical / page | critical-outage | rule / 0s | `sum by (provider) (increase(sc_gateway_upstream_responses_total{code=~"401\|403"}[10m])) > 0` | C11 | both | 53 | `rotate-model-credentials.md` |
| `SCEmergencyStopSourceUnavailable` (aka `GatewayEmergencyStopSourceUnavailable`, `SCGatewayEmergencyStopSourceUnavailable`) | budget | warning / notify | budget-stop | rule / 1m | `max(sc_emergency_stop_level{source="degraded"}) >= 1 or (time() - max(sc_emergency_stop_k8s_last_read_ok_timestamp_seconds)) > 60` | C11 | both | 33 | `gateway-failure.md` |
| `SCGatewayLatencyOverheadHigh` (aka `GatewayLatencyOverheadHigh`) | gateway | warning / notify | none | rule / 15m | `histogram_quantile(0.99, sum by (le) (rate(sc_gateway_overhead_seconds_bucket[15m]))) > 0.05` | C11 | both | 53 | `gateway-failure.md` |
| `SCPricingMissingForRoutedModel` (aka `PricingMissingForRoutedModel`) | gateway | critical / page | none | rule / 5m | `max by (tier) (sc_routing_unpriced_candidates) > 0` | C11 | both | 32 | `pricing-update.md` |
| `SCPricingEntryExpiringSoon` (aka `PricingEntryExpiringSoon`) | gateway | warning / notify | none | rule / 1h | `min by (provider, model) (sc_pricing_entry_expiry_timestamp_seconds) - time() < 7 * 86400` | C11 | both | 32 | `pricing-update.md` |
| `SCUsageNormalizationInconsistent` (aka `UsageNormalizationInconsistent`) | gateway | warning / notify | none | rule / 0s | `sum by (provider) (increase(sc_gateway_usage_inconsistent_total[1h])) > 0` | C11 | both | 32 | `gateway-failure.md` |
| `SCLedgerRollupStale` (aka `LedgerRollupStale`) | gateway | warning / notify | none | rule / 10m | `max(sc_ledger_rollup_age_seconds) > 900` | C11 | both | 32 | `gateway-failure.md` |
| `SCRoutingTierEmpty` | gateway | critical / page | none | rule / 5m | `min by (harness, role, tier) (sc_routing_tier_eligible_candidates) == 0` | C11 | both | 31 | `gateway-failure.md` |
| `SCRoutingUnresolvable` | gateway | warning / notify | none | rule / 0s | `sum(increase(sc_routing_resolutions_total{result="unresolvable"}[15m])) > 0` | C11 | both | 31 | `gateway-failure.md` |
| `SCTaskNeedsHuman` | task | warning / notify | needs-human | runner-post by runner / ttl 6h | API post (§7.4) | C06 | both | 26 | `tdd-task-recovery.md` |
| `SCTaskRetriesExhausted` | task | warning / notify | needs-human | runner-post by runner / ttl 6h | API post (§7.4) | C06 | both | 26 | `tdd-task-recovery.md` |
| `SCNeedsHumanBacklog` | task | warning / notify | none | rule / 72h | `max(sc_runner_tasks{state="NEEDS_HUMAN"}) > 0` | C11 | both | 26 | `tdd-task-recovery.md` |
| `SCTddPipelineFailure` | tdd | warning / notify | none | rule / 0s | `sum(increase(sc_verification_runs_total{outcome=~"infra_error\|timeout\|evidence_rejected"}[1h])) >= 3` | C11 | both | 22 | `tdd-task-recovery.md` |
| `SCTddVerificationStalled` | tdd | warning / notify | none | rule / 10m | `sum(sc_tasks_stuck{state=~"RED_VERIFYING\|GREEN_VERIFYING\|JUDGE_VALIDATING\|JUDGING"}) > 0` | C11 | both | 26 | `tdd-task-recovery.md` |
| `SCTaskStuck` | task | warning / notify | none | rule / 10m | `sum(sc_tasks_stuck{state!~"RED_VERIFYING\|GREEN_VERIFYING\|JUDGE_VALIDATING\|JUDGING\|NEEDS_HUMAN"}) > 0` | C11 | both | 29 | `stuck-task-recovery.md` |
| `SCTaskEventRejected` | task | warning / notify | none | rule / 0s | `sum(increase(sc_task_events_rejected_total{event=~".*_classified\|.*_result_.*"}[15m])) > 0` | C11 | both | 29 | `stuck-task-recovery.md` |
| `SCTaskInvariantViolation` | task | critical / page | needs-human | rule / 0s | `max(sc_task_invariant_violations) > 0` | C11 | both | 29 | `stuck-task-recovery.md` |
| `SCHumanTaskDecision` | task | info / silent | none | rule / 0s | `sum(increase(sc_runner_human_transitions_total[10m])) > 0` | C11 | both | 29 | `tdd-task-recovery.md` |
| `SCTaskCommandRejected` | security | info / silent | none | rule / 0s | `sum(increase(sc_task_commands_rejected_total[10m])) > 0` | C11 | both | 29 | `stuck-task-recovery.md` |
| `SCContextSecretDetected` | security | warning / notify | none | rule / 0s | `sum(increase(sc_context_sections_withheld_total{reason="secret_detected"}[1h])) > 0` | C11 | both | 25 | `quarantine-worker.md` |
| `SCContextEvidenceMissing` | tdd | warning / notify | none | rule / 0s | `sum(increase(sc_context_sections_withheld_total{reason="source_unreadable"}[1h])) > 0` | C11 | both | 25 | `tdd-task-recovery.md` |
| `SCContextCompileFailures` | runner | warning / notify | none | rule / 0s | `sum(increase(sc_context_compile_failures_total[1h])) > 3` | C11 | both | 25 | `agent-runner-failure.md` |
| `SCTddProvenanceBypass` | security | critical / page | needs-human | rule / 0s | `sum(increase(sc_runner_merges_without_provenance_total[1h])) > 0` | C11 | both | 55 | `merge-without-provenance.md` |
| `SCProvenanceStatusForged` (aka `SCStatusForgeryDetected`) | security | critical / page | needs-human | rule / 0s | `sum(increase(sc_forge_status_forged_total[15m])) > 0` | C11 | both | 55 | `merge-without-provenance.md` |
| `SCProtectedBranchDirectPush` | security | warning / notify | none | rule / 0s | `sum(increase(sc_merge_audits_total{outcome="direct_push"}[1h])) > 0` | C11 | both | 55 | `merge-without-provenance.md` |
| `SCForgeProjectionStale` | forge | warning / notify | none | rule / 5m | `time() - max(sc_forge_projection_last_resync_timestamp_seconds) > 1800` | C11 | both | 55 | `agent-runner-failure.md` |
| `SCRepoPolicyRejected` | tdd | info / silent | none | rule / 0s | `sum(increase(sc_runner_dispatch_rejected_total{code="REPO_POLICY_LOOSENS_PLATFORM"}[1h])) > 0` | C11 | both | 22 | `tdd-task-recovery.md` |
| `SCUnsignedImageRunning` | security | critical / page | none | check-cronjob / fail 1h, stale 26h | `renovate/supply-chain-signatures` (ADR-0553) | P10 | both | 23 | `credential-compromise.md` |
| `SCVulnerabilityCriticalDeployed` | security | warning / notify | none | check-cronjob / fail 1h, stale 26h | `renovate/supply-chain-vulns` (ADR-0553) | P10 | both | 23 | `credential-compromise.md` |
| `SCRenovateStale` | ci | warning / notify | none | check-cronjob / fail 2h, stale 26h | `renovate/renovate` (ADR-0553) | P10 | both | 23 | `rotate-secrets.md` |
| `SCWorkerPolicyViolation` (aka `SCPolicyViolationDetected`) | security | warning / notify | none | rule / 0s | `sum by (code) (increase(sc_runner_violations_total[1h])) > 0` | C11 | both | 22 | `quarantine-worker.md` |
| `SCBudgetThresholdReached` | budget | warning / notify | none | rule / 0s | `max by (scope_kind, provider, threshold) (sc_budget_threshold_reached{scope_kind!="global_hour"}) == 1 unless on (scope_kind, provider) (sc_budget_exhausted == 1)` | C11 | both | 33 | `budget-emergency-stop.md` |
| `SCBudgetExhausted` | budget | critical / page | budget-stop | rule / 0s | `max by (scope_kind, provider) (sc_budget_exhausted{scope_kind!="global_hour"}) == 1` | C11 | both | 33 | `budget-emergency-stop.md` |
| `SCBudgetCostIncomplete` | budget | warning / notify | none | rule / 15m | `max by (scope_kind, provider) (sc_budget_cost_incomplete) == 1 and on() sum(sc_ledger_incomplete_rows{period="month",kind=~"unknown\|unpriced"}) > 0` | C11 | both | 33 | `budget-emergency-stop.md` |
| `SCEmergencyStopActive` | budget | warning / notify | budget-stop | rule / 0s | `max(sc_emergency_stop_level{source="effective"}) >= 1` | C11 | both | 33 | `budget-emergency-stop.md` |
| `SCEmergencyStopNotApplied` | budget | critical / page | budget-stop | rule / 2m | `max(sc_runner_emergency_stop_level{source="effective"}) != max(sc_emergency_stop_level{source="effective"})` | C11 | both | 33 | `budget-emergency-stop.md` |
| `SCBudgetHourlyCapReached` | budget | warning / notify | none | rule / 0s | `max(sc_budget_exhausted{scope_kind="global_hour"}) == 1` | C11 | both | 33 | `budget-emergency-stop.md` |
| `SCBudgetDispatchRefused` | budget | warning / notify | none | rule / 15m | `sum(increase(sc_runner_dispatch_refusals_total{code="BUDGET_EXCEEDED"}[15m])) > 0` | C11 | both | 33 | `budget-emergency-stop.md` |
| `SCBudgetMetricsStale` | budget | warning / notify | none | rule / 5m | `time() - max(sc_budget_metrics_updated_timestamp_seconds) > 300` | C11 | both | 33 | `gateway-failure.md` |
| `SCAnomalousSpend` (aka `AnomalousModelSpend`) | spend | critical / page | budget-stop | rule / 0s | `(sc:model_spend_micro_usd:increase15m > 4 * quantile_over_time(0.95, sc:model_spend_micro_usd:increase15m[7d] offset 15m) and sc:model_spend_micro_usd:increase15m > 500000) or sc:model_spend_micro_usd:increase15m > 8000000` | C11 | both | 33 | `budget-emergency-stop.md` |
| `SCGatewayBoundViolation` (aka `SCGatewayReservationOverrun`, `GatewayReservationOverrun`) | spend | critical / page | budget-stop | rule / 0s | `sum by (provider, model) (increase(sc_gateway_bound_violations_total[15m])) > 0` | C11 | both | 53 | `gateway-failure.md` |
| `SCLedgerProviderDivergence` | spend | warning / notify | none | check-cronjob / fail 1h, stale 26h | `strange-company/ledger-reconcile` (ADR-0553) | C11 | both | 32 | `pricing-update.md` |
| `SCLedgerProviderUndercount` | spend | critical / page | budget-stop | rule / 0s | `max by (provider) (sc_ledger_provider_divergence_ratio) > 0.2` | C11 | both | 32 | `pricing-update.md` |
| `SCGatewayPolicyDenialsRepeated` | budget | warning / notify | none | rule / 0s | `max by (sc_code) (increase(sc_runner_gateway_policy_denials_total[1h])) >= 3` | C11 | both | 33 | `gateway-failure.md` |
| `SCNotificationTest` | notification-test | info / notify | none | external-post by bootstrap,operator / ttl 15m | API post (§7.4) | P09 | both | 42 | `notification-failure.md` |

### 23.6 Name mapping for consolidation (generated from catalogue `aliases`)

The catalogue is the single naming authority. Consolidation renames every reference below to the catalogue name; until then `make lint-alerts` resolves aliases.

| Name used elsewhere | Catalogue alertname | Referenced in |
|---|---|---|
| `NodeDown` | `SCNodeDown` | — (former catalogue name) |
| `KubeNodeNotReady` | `SCNodeDown` | — (former catalogue name) |
| `SCK3sConfigDrift` | `SCK3sServerConfigDrift` | — (former catalogue name) |
| `CNPGClusterNotHealthy` | `SCCnpgClusterDown` | — (former catalogue name) |
| `CNPGNoPrimary` | `SCCnpgClusterDown` | — (former catalogue name) |
| `CNPGClusterNotReady` | `SCCnpgClusterDown` | — (former catalogue name) |
| `CNPGReplicaCountBelowSpec` | `SCCnpgClusterDegraded` | — (former catalogue name) |
| `CNPGReplicationLagHigh` | `SCCnpgReplicationLagHigh` | — (former catalogue name) |
| `CNPGWALArchivingFailing` | `SCCnpgWalArchiveFailing` | — (former catalogue name) |
| `CNPGBaseBackupStale` | `SCCnpgBaseBackupStale` | — (former catalogue name) |
| `CNPGBaseBackupFailed` | `SCCnpgBackupFailed` | — (former catalogue name) |
| `CNPGInstanceNotReady` | `SCCnpgInstanceNotReady` | — (former catalogue name) |
| `CNPGSyncReplicationDegraded` | `SCCnpgSyncReplicationDegraded` | — (former catalogue name) |
| `CNPGWALArchiveBacklogHigh` | `SCCnpgWalReadyBacklogHigh` | — (former catalogue name) |
| `CNPGWALArchiveBacklogCritical` | `SCCnpgWalReadyBacklogCritical` | — (former catalogue name) |
| `CNPGRecoveryWindowShort` | `SCCnpgRecoveryWindowShort` | — (former catalogue name) |
| `CNPGDatabaseLarge` | `SCCnpgDatabaseLarge` | — (former catalogue name) |
| `CNPGStorageHigh` | `SCCnpgDatabaseLarge` | — (former catalogue name) |
| `CNPGOperatorDown` | `SCCnpgOperatorDown` | — (former catalogue name) |
| `CNPGPluginDown` | `SCCnpgPluginDown` | — (former catalogue name) |
| `LonghornVolumeUsageHigh` | `SCLonghornVolumeNearlyFull` | — (former catalogue name) |
| `ForgejoPVCUsageHigh` | `SCLonghornVolumeNearlyFull` | — (former catalogue name) |
| `LonghornDiskUsageHigh` | `SCLonghornNodeStorageLow` | — (former catalogue name) |
| `RepoBundleVerifyFailed` | `SCRestoreTestReposFailed` | — (former catalogue name) |
| `TofuStateRestoreTestFailed` | `SCTofuStateRestoreTestFailed` | 06 |
| `TofuStateRestoreTestOverdue` | `SCTofuStateRestoreTestFailed` | — (former catalogue name) |
| `SCRestoreTestTofuStateFailed` | `SCTofuStateRestoreTestFailed` | — (former catalogue name) |
| `SCTofuStateRestoreTestOverdue` | `SCTofuStateRestoreTestFailed` | — (former catalogue name) |
| `TofuStateLockStale` | `SCTofuStateLockStale` | — (former catalogue name) |
| `BreakGlassDrillOverdue` | `SCBreakGlassTestOverdue` | — (former catalogue name) |
| `ObjectStorageDown` | `SCObjectStorageUnreachable` | — (former catalogue name) |
| `TofuStateVersioningDisabled` | `SCObjectStorageConfigDrift` | 06 |
| `ForgejoDown` | `SCForgejoDown` | — (former catalogue name) |
| `ForgejoSSHDown` | `SCForgejoSSHDown` | — (former catalogue name) |
| `ForgejoPodStuckPending` | `SCForgejoPodStuckPending` | — (former catalogue name) |
| `ForgejoObjectStorageErrors` | `SCForgejoObjectStorageErrors` | — (former catalogue name) |
| `ForgejoSSHHostKeyMismatch` | `SCForgejoSSHHostKeyMismatch` | — (former catalogue name) |
| `ForgeConfigDrift` | `SCForgeConfigDrift` | — (former catalogue name) |
| `ForgeBotTokenInvalid` | `SCForgeBotTokenInvalid` | — (former catalogue name) |
| `SCForgejoOrphanedDeployKeys` | `SCRunnerOrphanDeployKeys` | — (former catalogue name) |
| `AuthentikDown` | `SCAuthentikDown` | — (former catalogue name) |
| `AuthentikWorkerDown` | `SCAuthentikWorkerDown` | — (former catalogue name) |
| `AuthentikTasksFailing` | `SCAuthentikTasksFailing` | — (former catalogue name) |
| `AuthentikBlueprintFailed` | `SCAuthentikBlueprintFailed` | — (former catalogue name) |
| `AuthentikBootstrapAdminActive` | `SCAuthentikBootstrapAdminActive` | — (former catalogue name) |
| `OIDCLoginBroken` | `SCOIDCLoginBroken` | — (former catalogue name) |
| `BreakGlassAccountNotReady` | `SCBreakGlassAccountNotReady` | — (former catalogue name) |
| `BreakGlassAuditDown` | `SCBreakGlassAuditDown` | — (former catalogue name) |
| `PlatformProbesDown` | `SCPlatformProbesDown` | — (former catalogue name) |
| `BreakGlassUsed` | `SCBreakGlassUsed` | — (former catalogue name) |
| `SCClusterBreakGlassUsed` | `SCK3sBreakGlassAuditSeen` | 07 |
| `GitHubMirrorStale` | `SCGitHubMirrorStale` | — (former catalogue name) |
| `SCRecoveryModeActive` | `SCGitHubMirrorRecoveryModeTooLong` | — (former catalogue name) |
| `FluxReconciliationFailed` | `SCFluxReconciliationFailing` | — (former catalogue name) |
| `FluxKustomizationNotReady` | `SCFluxReconciliationFailing` | — (former catalogue name) |
| `FluxSourceNotReady` | `SCFluxSourceFailing` | — (former catalogue name) |
| `FluxResourceSuspended` | `SCFluxSuspendedTooLong` | — (former catalogue name) |
| `FluxControllerDown` | `SCFluxControllerDown` | — (former catalogue name) |
| `FluxSeedSourcePresent` | `SCFluxSeedSourcePresent` | — (former catalogue name) |
| `FluxReconcileStalled` | `SCFluxReconcileStalled` | — (former catalogue name) |
| `SCCIMaintStale` | `SCCIMaintFailed` | — (former catalogue name) |
| `SCRetentionWoodpeckerPurgeFailed` | `SCCIMaintFailed` | — (former catalogue name) |
| `SCCertManagerWebhookDown` | `SCCertManagerDown` | — (former catalogue name) |
| `SCAgentRunnerDown` | `SCRunnerDown` | — (former catalogue name) |
| `SCAgentRunnerPolicyLoadFailed` | `SCRunnerPolicyInvalid` | — (former catalogue name) |
| `SCRunnerDispatchFailures` | `SCExecutionInfraFailuresHigh` | — (former catalogue name) |
| `GatewayDown` | `SCModelGatewayDown` | — (former catalogue name) |
| `GatewayUpstreamErrorRateHigh` | `SCModelProviderErrorsHigh` | — (former catalogue name) |
| `ModelMismatch` | `SCModelMismatch` | — (former catalogue name) |
| `GatewayUsageUnknownHigh` | `SCModelUsageUnknown` | — (former catalogue name) |
| `GatewayLedgerWriteFailing` | `SCLedgerWriteFailing` | — (former catalogue name) |
| `GatewayNotReady` | `SCGatewayNotReady` | — (former catalogue name) |
| `GatewayPolicyInvalid` | `SCGatewayPolicyInvalid` | — (former catalogue name) |
| `ModelProviderUnavailable` | `SCModelProviderUnavailable` | — (former catalogue name) |
| `ModelCredentialRejected` | `SCModelCredentialRejected` | — (former catalogue name) |
| `GatewayEmergencyStopSourceUnavailable` | `SCEmergencyStopSourceUnavailable` | — (former catalogue name) |
| `SCGatewayEmergencyStopSourceUnavailable` | `SCEmergencyStopSourceUnavailable` | — (former catalogue name) |
| `GatewayLatencyOverheadHigh` | `SCGatewayLatencyOverheadHigh` | — (former catalogue name) |
| `PricingMissingForRoutedModel` | `SCPricingMissingForRoutedModel` | — (former catalogue name) |
| `PricingEntryExpiringSoon` | `SCPricingEntryExpiringSoon` | — (former catalogue name) |
| `UsageNormalizationInconsistent` | `SCUsageNormalizationInconsistent` | — (former catalogue name) |
| `LedgerRollupStale` | `SCLedgerRollupStale` | — (former catalogue name) |
| `SCStatusForgeryDetected` | `SCProvenanceStatusForged` | 55 |
| `SCPolicyViolationDetected` | `SCWorkerPolicyViolation` | — (former catalogue name) |
| `AnomalousModelSpend` | `SCAnomalousSpend` | 33 |
| `SCGatewayReservationOverrun` | `SCGatewayBoundViolation` | 33 |
| `GatewayReservationOverrun` | `SCGatewayBoundViolation` | — (former catalogue name) |

## 24. Explicit non-goals

- Grafana alerting or Grafana contact points (ADR-0009).
- On-call scheduling, escalation chains or acknowledgement workflows (PagerDuty-class features). Operators who need them point the webhook transport at such a service.
- Per-user notification preferences.
- Delivering task content (handoffs, logs, diffs) in notifications. Notifications link to Forgejo and Hermes instead.
- An in-cluster ntfy or Matrix relay. Those transports go through the generic webhook to an operator-provided endpoint.

## 25. Open questions

| # | Question | Blocking? | Proposed resolution | Owner |
|---|---|---|---|---|
| Q-42-1 | Several plans introduced alert names without the `SC` prefix (plans 06, 12, 15, 16, 18, 32, 53). The catalogue keeps one name per alert and records the others as `aliases`. | no | Consolidation renames references in those plans to catalogue names (lint resolves aliases meanwhile). 16-AC-30 and 18-AC-14 must list catalogue names; 18-AC-14's group `flux-alerts` is honored through `ruleGroup`. | lead |
| Q-42-2 | Plan 06 wanted `sc_tofu_state_*` metrics "exported through the plan 44 mechanism". ADR-0553 has no metric export from Jobs. | no | Mapped onto check CronJobs: plan 44's `restore-test-tofu-state` (fails on a failed or > 35-day-old result) → `SCTofuStateRestoreTestFailed`; versioning → plan 14's `objstore-audit` → `SCObjectStorageConfigDrift`; lock age → `check-tofu-lock-age` (plan 06/44 add it) → `SCTofuStateLockStale`. | 06 / 44 / 14 |
| Q-42-3 | ADR-0009 lists budget stop among Runner-posted events; this plan makes it rule-based (gauge exists). | no | Keep rule-based; the latency is ≤ 1 min plus `group_wait`. Record as an ADR-0009 clarification at consolidation. | 42 / 33 |
| Q-42-4 | Does the pinned Alertmanager expose `receiver_name` on notification metrics (needed to exclude the heartbeat receiver from `SCAlertmanagerNotificationsFailing`)? | no | Assume yes (verify, 42-AC-6). Otherwise give the heartbeat its own integration type (`webhook` vs. `email` etc. is not always possible) or accept that a failing heartbeat URL also pages. | 42 |
| Q-42-5 | Plans 12 and 14 asked to inhibit CNPG/Longhorn/repo/etcd backup alerts while object storage is down. | no | Declined (§7.3): `failed-backup` must always notify (SPEC §97); grouping limits the noise to one message per alertname. | 42 / 12 / 14 |
| Q-42-7 | Plan 29 asked for `SCHumanTaskDecision` and `SCTaskCommandRejected` as Runner-posted info alerts. | no | Rendered as silent-class Prometheus rules on counters `sc_runner_human_transitions_total` and `sc_task_commands_rejected_total` (plan 26/29 export them): the transitions table is the audit record, and the notification contract stays limited to human-actionable posts. | 29 / 26 |
| Q-42-9 | AUTH-8 asked to restrict which pods may create silences. Alertmanager has no per-endpoint authorization and NetworkPolicy works per port. | no | Accepted residual: only trusted platform pods reach 9093 (§9.4), and any silence on a `security-audit` alert pages through `SCBreakGlassAlertSilenced`. An authenticating proxy in front of Alertmanager is post-MVP. | 42 / 34 |
| Q-42-10 | Gate-level DAG ordering (GC-01): 38-AC-4 / 39-AC-3 need Woodpecker (P08) at P09; 42-AC-15 / C11 ACs run after the C10 suite; 42-AC-20 (H04) needs the Runner. | no | Lead adds edges P09→P08 (P09 depends on P08), C11→C10, H04→R02 (or the C phase) in `dag.yaml`. 42-AC-15 is already moved to GATE-C11. | lead |
| Q-42-8 | Plans 12, 15, 16, 18, 26, 29, 33, 53 name some metrics differently from plan 38 §21.2 (e.g. `sc_tasks_stuck` in plan 29 vs `sc_runner_tasks_stuck`). | no | The catalogue uses the **emitter plan's** name where the emitter defines one (plan 29's `sc_tasks_stuck`, `sc_task_events_rejected_total`, `sc_task_invariant_violations`; plan 33's `sc_budget_*`, `sc_emergency_stop_level`); plan 38 §21.2 is updated to match. `make lint-alerts` checks every referenced metric against the generated metric catalogue. | lead |
| Q-42-6 | Event-only counters need pre-initialized zero series for 42-AC-17. | no | Plans 26, 32, 53 pre-initialize every bounded label combination of their counters at startup (standard Prometheus client practice). | 26 / 32 / 53 |
