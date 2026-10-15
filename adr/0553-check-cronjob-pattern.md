# ADR-0553: DR and backup invariants are exposed as "check CronJobs" read through kube-state-metrics (no Pushgateway)

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 42

## Context
Several black-start-relevant invariants are evaluated by short-lived Jobs, not long-running exporters: restore tests, repository bundles, recovery-bundle currency, recovery-key attestation and the GitHub black-start test. Prometheus cannot scrape a finished Job, and a Pushgateway adds a stateful component whose stale values are a known hazard.

## Decision
- Each such invariant is a CronJob labeled `strange.company/check: "<check-id>"` whose Job **exits 0 if and only if the invariant holds** and writes its structured evidence to S3 or `sc-pg` (plan 44 `restore-test-result`).
- Alerts derive from kube-state-metrics: **failed** when `kube_cronjob_status_last_schedule_time - kube_cronjob_status_last_successful_time > 0` for longer than `failAfter`; **stale** when `time() - kube_cronjob_status_last_successful_time > maxAge`, or when that series is absent. The alert catalogue's `source.kind: check-cronjob` carries these parameters, and the rule renderer generates both expressions.
- Long-running components with native metrics (CNPG, Longhorn, k3s `ETCDSnapshotFile` objects through KSM custom-resource state) use those metrics instead.

## Consequences
No new exporter or Pushgateway. Each check's logic lives in its owning plan (44, 35, 15, 17). The required KSM metric is version-sensitive and re-verified at P10.

## Alternatives rejected
- Pushgateway: a stateful component, and it keeps stale "success" values after the pusher dies.
- A custom DR-status exporter: new software duplicating KSM.

## Verification
P10 gate (plan 42): a check CronJob whose Job exits 1 raises its `...Failed` alert within `failAfter + 2m` (promtool unit test plus a live k3d test). Suspending a check CronJob past `maxAge` raises the stale alert.
