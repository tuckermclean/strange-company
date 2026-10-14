# ADR-0200: CNPG backs up through the Barman Cloud CNPG-I plugin into generation-qualified paths

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 12

## Context
SPEC §26 requires base backups, WAL archiving, PITR and a repeatable restore test for every CNPG cluster (ADR-0006: four clusters). CNPG's in-tree `barmanObjectStore` is deprecated in favor of the Barman Cloud CNPG-I plugin, and its removal is announced for a later CNPG minor (verify at implementation). A cluster restored from an object-store path must not archive back into that same path: barman refuses a non-empty WAL path (verify), and mixing timelines would make the pre-recovery history unusable for a second attempt.

## Decision
- Every cluster uses the **Barman Cloud plugin** (`barman-cloud.cloudnative-pg.io`) as WAL archiver and for `ScheduledBackup` (`method: plugin`). The in-tree `barmanObjectStore` is never used (F11: `grep barmanObjectStore gitops/` must be empty).
- One `ObjectStore` per cluster with `destinationPath: s3://<prefix>-pg-backups/<cluster>/` and the per-cluster credential `SC-OBJECT-STORAGE/pg-backups-<cluster>` (plan 14).
- The plugin `serverName` is **generation-qualified**: `<cluster>-g<N>`, with `N` from `cluster-vars` (`CNPG_<ID>_GEN`, starting at 1). Every recovery restores **from** `g<N>` and archives **into** `g<N+1>`. The recovery commit (plan 45, ADR-0554, via the per-cluster `cnpg-recovery-*-<id>` components) and the PITR procedure (`restore-cnpg.md`) increment `N`.
- Retention `30d` in the `ObjectStore`; object versions behind it are protected by bucket versioning, a 14-day noncurrent window and Object Lock where available (ADR-0204).
- Scratch restore clusters (restore tests, PITR verification) have **no** plugin entry, so they can never write to the bucket.

## Consequences
- PITR from any generation stays possible while its objects are retained; a failed recovery attempt can be retried from the untouched source generation.
- Black start and PITR both need a Git change (the generation bump). That is intended: the recovered state is explained by Git.
- ADR-0554 (revised) uses this ADR's `serverName <cluster>-g<N>`. Black start selects the recovery bootstrap through the per-cluster components `gitops/components/cnpg-recovery-{latest,target}-<id>` (Flux `spec.patches` on one db layer each, plan 45); single-cluster PITR swaps the platform-level component by PR (plan 12 §9.5).

## Alternatives rejected
- In-tree `barmanObjectStore`: deprecated, scheduled for removal.
- Volume-snapshot backups: `local-path` cannot snapshot, and snapshots are not off-cluster.
- Reusing the same path after recovery: refused by barman (verify) and interleaves timelines.

## Verification
Plan 12 12-AC-3, 12-AC-9, 12-AC-12 (`make e2e-cnpg`: PITR to time; "latest" with generation bump archives into `g2`); plan 44 44-AC-4 (weekly scratch PITR per cluster) and 44-AC-15 (no generation change on scale-out).
