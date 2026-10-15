# ADR-0201: Longhorn V1 scope, settings and upgrade discipline

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 13

## Context
SPEC §27 makes Longhorn V1 the replicated block storage for applications that cannot replicate themselves, with 1 replica at bootstrap and 2 at small-ha, and forbids it for PGDATA, workspaces, CI scratch and caches. The prior implementation observed RWO Multi-Attach detach lag on single-replica `Recreate` Deployments (lesson L-5). Longhorn does not support downgrades (verify), while plan 18 gives controller HelmReleases automatic rollback.

## Decision
- **V1 data engine only.** No V2/SPDK, no RWX/NFS, no encrypted volumes in MVP.
- **Narrow scope:** only `forgejo-data`, `hermes-data` and `dr-canary` use class `longhorn`; restore-test volumes use `longhorn-scratch`. Both classes are non-default (ADR-0203).
- **Replica policy:** `numberOfReplicas` = `LONGHORN_REPLICAS` for new volumes (StorageClass, force-replaced on change) **and** for existing volumes (a name-versioned reconcile Job `longhorn-replica-sync-r<N>` in the post-transfer `backups` layer, never in the seed set, because it uses an internal image, STO-1); hard replica anti-affinity; `dataLocality: best-effort`; `replicaAutoBalance: best-effort`.
- **Failure handling settings:** `nodeDownPodDeletionPolicy: delete-both-statefulset-and-deployment-pod`, `autoDeletePodWhenVolumeDetachedUnexpectedly: true`; a stuck attachment is recovered by procedure (out-of-service taint or stale `VolumeAttachment` deletion), never by automatic force-detach on a reachable node.
- **Backups:** `BackupTarget` CR `default` on bucket `longhorn-backups`; recurring jobs `snap-6h` (retain 4), `backup-12h` (14), `backup-weekly` (4), `trim-weekly`, `snapshot-cleanup-daily`, all in group `critical`.
- **Upgrades:** one minor at a time, no Helm automatic rollback (`upgrade.remediation.retries: 0`), backup-first gate, chart pre-upgrade checker enabled.
- **UI:** not exposed (plan 15); `kubectl` on Longhorn CRs is the operational interface.

## Consequences
- Profile changes reach existing volumes deterministically through Git.
- A failed Longhorn upgrade stays visible and is fixed forward or restored, instead of a risky automatic downgrade. Plan 18's remediation class for Longhorn must change (reported).
- Operators need `kubectl`, not the UI, for every procedure.

## Alternatives rejected
- Longhorn as the default StorageClass: a forgotten class would put caches and PGDATA on replicated storage.
- Patching `Volume.spec.numberOfReplicas` through Flux server-side apply: Flux would contend with longhorn-manager over the volume spec.
- Exposing the UI through Authentik forward-auth: destructive powers with no benefit over `kubectl` (plan 15).

## Verification
Plan 13 13-AC-1…18 (P04, P10, H02), including the Multi-Attach drill (13-AC-12) and the replica-sync Job (13-AC-15).
