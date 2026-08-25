# strange-company

Batteries-optional Helm chart providing the Kubernetes substrate for the Strange
Company autonomous engineering system.

This repository contains **infrastructure packaging only** — no Kanban logic, no
agent orchestration, no model routing. It makes PostgreSQL, Vikunja, a Hermes
Agent, and a Control Plane deployable and reachable. Behavior lands later, in the
control plane application.

## Quick start

```bash
helm install strange-company ./charts/strange-company \
  --namespace strange-company --create-namespace
```

## Batteries-optional

Each dependency is independently switchable between **bundled** and **external**:

| Component    | Bundled by chart                | External via                       |
|--------------|---------------------------------|------------------------------------|
| `postgresql` | StatefulSet + Service + Secret  | `postgresql.external.*`            |
| `vikunja`    | official Vikunja Helm dependency| `vikunja.external.*`               |
| `hermes`     | Deployment + Service + PVC      | `hermes.external.*`                |
| `controlPlane` | Deployment + Service          | n/a (always chart-owned)           |

The control plane receives the same environment contract in every combination.

See [docs/deployment-prompt.md](docs/deployment-prompt.md) for a self-contained
deployment brief (cluster prerequisites, PodSecurity requirements, credentials,
and verification steps),
[docs/USAGE.md](docs/USAGE.md) for external, hybrid, and parent-chart usage,
and [docs/specs/strange-company-helm-chart.md](docs/specs/strange-company-helm-chart.md)
for the build specification.
