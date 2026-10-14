# Procedure: Notification failure

**Owning plan:** plans/42-alerting-notifications.md
**SPEC references:** §96, §97, §81, §113 (procedure added by Stage 0: human notifications are SPEC-required, so their failure needs its own runbook)
**Severity / urgency:** **outage of human notification** — a Needs Human, budget stop, failed backup or black-start threat may currently be unseen. Act immediately; while it lasts, check Alertmanager manually every hour.
**Expected duration:** 15–60 min
**Requires break-glass:** no

## When to use

| Alert / symptom | Section |
|---|---|
| `SCAlertmanagerNotificationsFailing` (arrives through the heartbeat lapse, a second transport, or a human noticing) | §A Transport delivery |
| External heartbeat service alarms while the cluster is healthy | §B Heartbeat |
| `SCDeadMansSwitchNotConfigured` | §B.4 Configure a heartbeat |
| `SCRunnerNotificationPostFailing`; Needs Human tasks without a message | §C Runner posting |
| `SCSilencePolicyViolation` | §D Silences |
| A human reports "I did not get notified about X" | §E Missed notification |
| Preflight `PF-17`/`PF-18` failed (no cluster yet) | §F Bootstrap-time tests |
| Planned test (P09 V-24, after a transport change, quarterly) | §G Test notifications |

## Preconditions and required material

- Toolbox (`./bootstrap shell`), operator kubeconfig.
- `kubectl -n monitoring port-forward svc/kps-alertmanager 9093 &`, then `AM="amtool --alertmanager.url=http://127.0.0.1:9093"`.
- `PQ` alias from `monitoring-failure.md`.
- Access to the transport provider's console (SMTP account, ntfy server, Slack/Discord webhook settings) and to the heartbeat service.

## Safety checks before acting

1. Before changing anything, **read the active alerts** so nothing is lost while you work: `$AM alert query -o extended` and handle every `always_notify!="none"` alert by its runbook.
2. Do not silence `SCAlertmanagerNotificationsFailing` or `Watchdog`.
3. Never put a transport secret in Git plaintext, an issue or a chat; it goes into `bootstrap.env` and the SOPS file only.
4. Do not edit the Alertmanager Secret in the cluster; change Git (`gitops/clusters/<c>/secrets/monitoring/alertmanager-transports.sops.yaml`, `config/notifications.yaml`) and let Flux apply.

## Steps

### §A Transport delivery
1. Which integration fails: `PQ 'sum by (integration, receiver_name) (rate(alertmanager_notifications_failed_total[10m]))'`.
2. Error text: `kubectl -n monitoring logs <alertmanager-pod> -c alertmanager --since=30m | grep -i 'notify\|error' | tail -20` (URLs are secrets: do not copy log lines containing them into the issue).
3. Classify:
   | Error | Cause | Action |
   |---|---|---|
   | `401`/`403`, `535 authentication failed` | credential revoked/expired | §A.4 rotate |
   | `404` (webhook/Slack/Discord URL) | webhook deleted at the provider | §A.4 with a new URL |
   | `429` | rate limited | reduce noise (check inhibition and grouping for a storm), wait |
   | timeout / connection refused / DNS | egress or provider outage | `monitoring-failure.md` §A.5; provider status page |
   | `x509` | TLS interception or wrong SMTP port | check `smarthost`/`requireTLS` in `notifications.yaml` |
4. **Rotate / replace the transport credential:** update `bootstrap.env` (`NOTIFY_*`), then `./bootstrap exec -- sc-bootstrap notifications render --cluster <c>` (re-renders the SOPS Secret from `bootstrap.env`), open a PR, merge. **Expected:** within 2 min after reconcile, `alertmanager_notifications_failed_total` stops increasing.
5. If the primary transport will be down for long: add a second transport to `spec.transports[]` in `notifications.yaml` by PR (plan 42 §9.1).
6. Send a test (§G) and confirm receipt.

### §B Heartbeat
1. Cluster healthy? (`monitoring-failure.md` §A steps 1–4). If Watchdog is delivered (`increase(alertmanager_notifications_total{receiver_name="heartbeat"}[10m]) ≥ 8`) but the service shows no ping: the heartbeat URL is wrong or the service rejects the POST → check `heartbeat-url` (compare the sha256 of the decrypted value with the URL in the service console).
2. If deliveries fail (`…failed_total{receiver_name="heartbeat"}` increasing): same classification as §A.3.
3. If the heartbeat lapsed because `SCAlertmanagerNotificationsFailing` inhibits Watchdog: fix §A first; the heartbeat resumes automatically.
4. **No heartbeat configured** (`SCDeadMansSwitchNotConfigured`): create a check at a heartbeat service (period 5 min, grace 10 min, alert channel independent of the cluster), set `HEARTBEAT_URL` in `bootstrap.env`, run `sc-bootstrap notifications render`, PR, merge. **Expected:** `sc_notification_heartbeat_configured == 1` and the service shows pings within 5 min of reconcile.

### §C Runner posting
1. `PQ 'sum by (result) (increase(sc_runner_alert_posts_total[30m]))'`.
2. Runner logs: `kubectl -n strange-company logs deploy/agent-runner --since=30m | jq 'select(.event|test("alert"))'`.
3. Common causes: NetworkPolicy (Runner → `alertmanager-operated:9093`), Alertmanager down (`monitoring-failure.md` §F), payload rejected (HTTP 400: a schema bug — open an issue against plan 26 with the `event` code; the payload must validate against `contracts/notification.schema.yaml`).
4. Meanwhile list tasks needing a human directly: Hermes `agent_list` with state `NEEDS_HUMAN`, or `GET /v1/tasks?state=NEEDS_HUMAN` on the Runner (port-forward, admin token), and handle them.
5. **Expected after the fix:** within one refresh cycle (5 min) `$AM alert query alertname=SCTaskNeedsHuman` shows one alert per task in `NEEDS_HUMAN`.

### §D Silences
1. `$AM silence query -o extended`.
2. For each silence matching an always-notify alert: it must reference an issue (`#<n>` or Forgejo URL) in its comment and end within 72 h. Otherwise expire it (`$AM silence expire <id>`) or re-create it correctly (`$AM silence add --duration=24h --comment="#<issue> <reason>" alertname=<x>`).
3. **Expected:** the next `silence-audit` run (≤ 30 min) succeeds and `SCSilencePolicyViolation` resolves.
4. Break-glass alerts (`SCBreakGlassUsed`, `SCGrafanaLocalLogin`, `SCK3sBreakGlassAuditSeen`) may **never** be silenced, not even with an issue reference: expire any such silence immediately. The `silence-audit-breakglass` CronJob pages `SCBreakGlassAlertSilenced` within 30 min (plan 42 §9.4). Acknowledge break-glass pages; record the use per `break-glass-access.md` §After use.

### §E Missed notification
1. Was the alert firing? `PQ 'ALERTS{alertname="<name>"}[<window>]'` (range query) or Alertmanager history (`$AM alert query alertname=<name>`).
2. Was it routed externally? `./bootstrap exec -- sc-alertgen test-routes --cluster <c> --alert <name>` shows the receiver. `silent` for an alert a human needs → PR changing its `notifyClass` in the catalogue.
3. Was it inhibited or silenced? `$AM alert query --inhibited --silenced alertname=<name>`.
4. Was the delivery attempted and successful? §A.1 for the time window.
5. Record findings and open the catalogue PR if routing was wrong.

### §F Bootstrap-time tests (`PF-17`, `PF-18`)
1. `./bootstrap preflight` prints the failing check and the transport error class.
2. Fix `bootstrap.env` values (URL, SMTP host/port/credentials, ntfy token) and rerun preflight. PF-17 requires the operator to type the 6-character nonce from the received message (interactive) or a successful provider response (non-interactive).

### §G Test notifications
1. `./bootstrap exec -- sc-alertgen post-test --category backup --class notify --nonce $(head -c3 /dev/urandom | od -An -tx1 | tr -d ' \n' | tr a-f A-F)` (and `--class page`).
2. **Expected:** a message titled `[FIRING:1] SCNotificationTest (info, backup)` arrives within `group_wait` (2 min notify / 30 s page) on every configured transport, showing the nonce; a resolved message follows after 15 min.
3. For the full P09 matrix run `./bootstrap verify --only V-24`.

## Verification

- `PQ 'sum(increase(alertmanager_notifications_failed_total[15m]))'` = 0.
- A §G test message received on every transport; heartbeat service shows a ping within 2 min.
- `ALERTS{alertname=~"SCAlertmanagerNotificationsFailing|SCRunnerNotificationPostFailing|SCSilencePolicyViolation"}` not firing.

## Rollback / abort

Transport changes are Git PRs; revert the PR to return to the previous transport. Silences can be re-created. Nothing in this procedure affects platform workloads.

## Evidence to record

Issue (`ops` label): window of lost notifications, alerts that fired during it (from `ALERTS` history) and how each was handled, root cause, PRs. For a credential compromise, follow `credential-compromise.md#observability-and-notification`.

## Follow-ups

- If the single transport was the cause, add a second transport (recommended for small-ha).
- Review whether any always-notify alert fired during the gap and confirm it was handled.
