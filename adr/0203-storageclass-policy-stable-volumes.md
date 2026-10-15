# ADR-0203: StorageClass policy and stable-named Longhorn volumes

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 11

## Context
SPEC §24 rejects a universal storage mechanism; §25 and §27 place PGDATA on node-local storage and keep caches and workspaces off Longhorn. k3s ships `local-path` as its default class. Backups, restores and black-start overlays need to address important volumes deterministically, and dynamic PV names (`pvc-<uid>`) change on every re-creation.

## Decision
- Exactly four StorageClasses: `local-path` (k3s, remains default), `longhorn` (non-default, `Retain`), `longhorn-scratch` (non-default, `Delete`, restore tests only) and `ci-workspace` (non-default, `Delete`, bounded CI filesystem, Woodpecker workflow volumes only; ADR-0305). Every PVC names its class explicitly (F11 `pvc-explicit-storageclass`, runtime `storage-audit`).
- Important Longhorn volumes are **stable-named triplets**: Longhorn `Volume`, PV and PVC share one name (`forgejo-data`, `hermes-data`, `dr-canary`). The `Volume` carries `kustomize.toolkit.fluxcd.io/ssa: IfNotPresent` and all three carry `prune: disabled`. Consuming charts reference the PVC and do not create it.
- No workload in `agent-jobs` mounts a PVC. In `ci-jobs`/`ci-build` only `ci-workspace` PVCs exist (ADR-0305).

## Consequences
- Backup volume names, restore manifests and the recovery overlay (`fromBackup`) are static and testable offline.
- Changing a live volume (size, replicas) needs an explicit mechanism (PVC resize, replica-sync Job), not a Flux patch.
- Plan 18's list of prune-protected kinds must include PersistentVolume and `longhorn.io/Volume` (reported).

## Alternatives rejected
- Making `longhorn` the default class: silent replication of caches and PGDATA.
- Dynamic provisioning for Forgejo and Hermes: new PV names after every re-creation break restore automation.

## Verification
Plan 11 11-AC-1…3, `storage-audit` SA-1…SA-7; plan 13 13-AC-2, 13-AC-3.
