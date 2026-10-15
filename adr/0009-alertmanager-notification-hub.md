# ADR-0009: Alertmanager is the single human-notification hub; external dead-man's switch for total outage

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13

## Context
SPEC §96–§97 require alerts for platform and organizational conditions, and real notifications for `Needs Human`, budget stop, failed backup, critical outage, failed mirror and black-start-threatening conditions. Two independent notification paths, one for the Runner and one for the platform, would drift apart.

## Decision
- All human notifications route through **Alertmanager** (kube-prometheus-stack). Platform conditions come from Prometheus rules. Organizational events (`Needs Human`, budget stop, task exhaustion) are posted by the Runner straight to the Alertmanager v2 API, with stable labels (`alertname`, `severity`, `category`, `task_id` as an *annotation* rather than a label for grouping). They are *also* exposed as low-cardinality metrics for Prometheus rules.
- Transports configured from `bootstrap.env` → SOPS: at least one of email (SMTP), generic webhook, ntfy (webhook), Slack, Discord, Matrix (via webhook bridge). A transport is mandatory at bootstrap. Preflight sends a test notification.
- **Dead-man's switch:** the always-firing `Watchdog` alert is routed to an *external* heartbeat receiver configured by the operator (any healthchecks-style URL). Silence from it means total outage. If the operator configures no external heartbeat, the bootstrap report states that total-outage detection is absent.

## Consequences
One routing tree, one silencing model, one place to test. Alertmanager failure is detected externally by the dead-man's switch.

## Alternatives rejected
- A Runner-native notifier: a duplicate routing and silencing implementation.
- Grafana alerting: a second rule engine.

## Verification
P09 and C06 gates: a synthetic alert of each required category reaches the test receiver; stopping Alertmanager causes the external heartbeat check to fail (simulated by a test receiver in CI).

## Amendments (Stage-0 consolidation)
- **2026-10-13, plan 42.** Budget-stop and emergency-stop notifications are **Prometheus rules** over the Runner/Gateway gauges, not Runner-posted alerts, because the gauges already exist. That leaves one firing path per condition. The Runner still posts `Needs Human` and task-exhaustion events through the Alertmanager API, to every replica (`contracts/notification.schema.yaml`). Failed-backup alerts are never inhibited (SPEC §97).
