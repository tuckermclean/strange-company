# ADR-0304: Prune protection for data-bearing objects and no automatic rollback for stateful applications

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 18

## Context
Flux applies Git with `prune: true`, so removing a manifest from Git deletes the object. For most objects that is the GitOps contract. For a Namespace, a PVC, a CNPG `Cluster`, a barman `ObjectStore` or a plain-manifest CRD, deletion destroys data or cascades into data loss. A one-line mistake in a PR (a renamed directory, a dropped overlay entry) would then become an outage with restore-from-backup recovery.

Separately, helm-controller can roll back a failed upgrade automatically. Forgejo, Authentik, Woodpecker, Grafana and the `strange-company` chart run forward-only database migrations at startup. A Helm rollback restores the old image against a migrated schema, which can corrupt state or crash-loop.

## Decision
1. **Prune protection.** Every rendered object of a *protected kind* carries `kustomize.toolkit.fluxcd.io/prune: disabled`. The protected kinds are `Namespace`, `PersistentVolumeClaim`, `PersistentVolume`, `longhorn.io/Volume`, `postgresql.cnpg.io/Cluster`, `barmancloud.cnpg.io/ObjectStore`, `CustomResourceDefinition` (when applied outside Helm) and `longhorn.io/BackupTarget`. F11 check `prune-protection` fails on any violation. Deleting such an object is a deliberate human `kubectl delete` through break-glass, after the manifest has left Git (plans 15, 47, 49).
2. **Destructive-diff PR check.** `make gitops-diff` renders base and head for every cluster and fails when a removed or renamed object is of a protected kind or is a `stateful-app` HelmRelease. The only exception is a head commit trailer `GitOps-Allow-Destroy: <kind>/<namespace>/<name>` for each such object, which the human merger reviews.
3. **Remediation classes** (amended at consolidation: `controller-no-rollback` for Longhorn, per ADR-0201). Each HelmRelease carries label `strange.company/remediation-class`:
   - `controller` (cert-manager, CNPG operator, Traefik, cloudflared, kube-prometheus-stack, OTel, kured, Loki, Tempo): upgrade remediation `retries: 3`, `strategy: rollback`.
   - `controller-no-rollback` (Longhorn only; ADR-0201, Longhorn cannot be downgraded): upgrade remediation `retries: 0`, `remediateLastFailure: false`.
   - `stateful-app` (Forgejo, Authentik, Woodpecker, Grafana, `strange-company`): upgrade remediation `retries: 0`, `remediateLastFailure: false`. A failed upgrade stays failed, the alert fires, and plan 48's procedure decides between roll-forward and restore.
   - Install remediation `retries: 3` is allowed for both classes, because charts never own data PVCs (those are standalone, prune-protected manifests).
   F11 check `remediation-class` enforces that the fields match the label.

## Consequences
- An accidental removal leaves the object orphaned but intact. The Kustomization inventory drops it, and the `gitops-diff` check normally catches it before merge.
- Decommissioning a component takes two steps (a Git removal, then a break-glass delete). Plan 49 and plan 47 document this.
- A failed stateful upgrade needs a human. That is intended: SPEC §8 requires conservatism around destructive operations.

## Alternatives rejected
- `prune: false` on whole layers: Git would stop explaining production, and leftovers would accumulate silently.
- Relying on Helm rollback for stateful apps: unsafe with forward-only migrations.
- Kubernetes finalizers or an admission webhook that blocks deletion: more machinery, and it also blocks legitimate break-glass deletion.

## Verification
F11 (18-AC-2 negative fixture: a PVC without the annotation). `make gitops-diff` negative fixture (a removed CNPG `Cluster` without the trailer fails; with the trailer it passes). P06: 18-AC-13 (a no-op commit causes no Helm revision change).
