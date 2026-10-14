# ADR-0550: Central monitoring layers and a privileged `monitoring-node` namespace

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 38

## Context
The seed-set components (cert-manager, CNPG, Longhorn, Traefik, cloudflared, Forgejo; ADR-0005) are reconciled before the Prometheus Operator CRDs exist. A ServiceMonitor or PrometheusRule placed inside a seed-set layer would fail to apply during seed and block control transfer. *(Amended 2026-10-14, SEED-23: plan 18 installs the Prometheus Operator CRDs in the seed-set `crds` layer, so this failure mode no longer exists; the central placement is kept because it keeps one owner per scrape object and rule, lets `make lint-metrics` check them in one place, and keeps component layers free of monitoring objects.)* node-exporter (and, at H03, the log agent) needs `hostNetwork`, `hostPID` and `hostPath`. Those violate PSA `baseline`, while the master plan sets `monitoring` to `baseline` and anticipates a split.

## Decision
- Every scrape object (ServiceMonitor, PodMonitor, Probe) and every PrometheusRule lives in **two central Flux Kustomizations**: `monitoring-scrape` (`gitops/platform/monitoring/scrape/`, plan 38) and `monitoring-rules` (`gitops/platform/monitoring/rules/`, plan 42). Both `dependsOn: monitoring-stack`, which installs the Prometheus Operator CRDs through the separate `prometheus-operator-crds` HelmRelease. Component plans own their *metrics endpoints* (port, path, auth). Plans 38 and 42 own the objects that consume them.
- A new namespace **`monitoring-node`** (PSA `privileged`, tier `platform`) holds only DaemonSets that need host access: `node-exporter` (MVP) and `otel-agent` (H03, plan 41). `monitoring` stays `baseline` (target: `restricted` where the charts allow it).

## Consequences
- Seed never depends on monitoring CRDs, and monitoring is never a dependency of control transfer.
- Adding a component's metrics means a PR touching `gitops/platform/monitoring/scrape/`. The `make lint-metrics` job checks that every catalogued job has exactly one scrape object.
- Plans 36, 37 and 18 and ADR-0015's namespace list must include `monitoring-node` (reported for consolidation).

## Alternatives rejected
- Monitors co-located with each component: breaks the seed for seed-set components, or forces the CRDs into the seed set.
- node-exporter in `monitoring` with PSA `privileged`: widens the privileged scope to Grafana, the Collector and Prometheus.

## Verification
P09 gate (plan 38): `kubectl get ns monitoring-node -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}'` = `privileged`; `monitoring` = `baseline`; the seed-to-Flux transfer test passes with `monitoring-*` Kustomizations absent.

## Amendment (2026-10-14, consolidation)
Plan 18 §8.3 is the layer authority. The layers named here map to plan 18's: `monitoring-crds` ≙ `crds` (in the seed set, so the Prometheus Operator CRDs exist from seed), `monitoring-stack` ≙ `monitoring`, and `monitoring-scrape` + `monitoring-rules` ≙ one layer `monitoring-config` with two paths (`gitops/platform/monitoring/scrape/`, `gitops/platform/monitoring/rules/`). The decision (scrape objects and rules only in the central paths, never in component layers; privileged `monitoring-node`) is unchanged.
