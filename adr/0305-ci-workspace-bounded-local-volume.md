# ADR-0305: Woodpecker workspaces use a bounded node-local StorageClass `ci-workspace`, the only PVCs allowed in CI namespaces

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 20 (affects 08, 11, 37, 43, 51)
**Relation to other plans:** this contradicts plan 11 §9 and SA-4 ("no PVC in `agent-jobs` or `ci-jobs`; emptyDir only") for `ci-jobs` and `ci-build`. Recorded for consolidation. `agent-jobs` is unaffected.

## Context
The Woodpecker Kubernetes backend (3.x, verified against the 3.19 documentation on 2026-10-13) runs every step as its own Pod. It shares the workspace between steps through one **PersistentVolumeClaim per workflow**, configured by `WOODPECKER_BACKEND_K8S_VOLUME_SIZE`, `WOODPECKER_BACKEND_K8S_STORAGE_CLASS` and `WOODPECKER_BACKEND_K8S_STORAGE_RWX`. There is no emptyDir option.

SPEC constraints:
- §27: Longhorn SHOULD NOT hold CI scratch space.
- §28: CI SHOULD use disposable workspaces.
- §107: root disks SHALL NOT fill.
- k3s `local-path` PVCs are plain directories on the root filesystem with no size enforcement, so a runaway build could fill the node disk and take etcd down with it.

## Decision
1. StorageClass **`ci-workspace`**: provisioner **`strange.company/ci-workspace`**, served by a **separate, Flux-managed local-path-provisioner instance** (Deployment `ci-workspace-provisioner` in `kube-system`, its own ConfigMap with `nodePathMap` = `/var/lib/sc/ci-workspace` for every node, digest-pinned upstream image; plan 11 owns the manifests, layer `capacity`). It does not touch the k3s-managed `local-path-config` addon, which k3s re-applies on restart and upgrade (STO-8, amended at review). `volumeBindingMode: WaitForFirstConsumer`, `reclaimPolicy: Delete`. Volumes land in **`/var/lib/sc/ci-workspace`**. That path is a dedicated filesystem plan 08 creates on every node: an LVM logical volume when the provider disk layout allows it, otherwise an ext4 filesystem image file mounted by a systemd mount unit. Its size is `ciWorkspaceFilesystemSize` (default **12 GiB** on self-hosting-minimum, 24 GiB on small-ha; amended from 16 GiB at consolidation because 16 GiB did not fit plan 11's 75 GiB disk budget, SI-G3-05).
2. The Woodpecker agents set `STORAGE_CLASS=ci-workspace`, `STORAGE_RWX=false` and `VOLUME_SIZE` 6Gi (ci lane) / 10Gi (build lane). Build-lane workflows use sequential steps and no service containers (CD-06), so at most one build workspace exists. Steps of one workflow therefore share an RWO volume and are co-scheduled on its node by the PV's node affinity.
3. Admission (plan 37): in `ci-jobs` and `ci-build`, a PVC may be created only by the agent ServiceAccount of that namespace, only with `storageClassName: ci-workspace`, and only with a request ≤ **8Gi in `ci-jobs`** and ≤ **10Gi in `ci-build`** (CD-06). A Pod may mount PVCs only of that class. hostPath stays forbidden.
4. Cleanup:
   - Woodpecker deletes the PVC at workflow end.
   - The `sc-ci-maint` CronJob (plan 20) deletes any PVC in those namespaces older than `maxPipelineTimeout + 15m`.
   - `reclaimPolicy: Delete` removes the directory.
5. Monitoring: `SCCIWorkspaceFilesystemHigh` fires at 80% usage of the mount, `SCCIWorkspaceFilesystemCritical` at 95%. Continuous check: plan 11's `storage-audit` adds SA-8 "every PV of class `ci-workspace` has a path under `/var/lib/sc/ci-workspace/`", re-run in plan 48's k3s upgrade gate. A full workspace filesystem fails CI jobs only. It cannot touch `/` or `/var/lib/rancher`.

## Consequences
- Disk exhaustion by CI is isolated from the root filesystem.
- Plan 08 gains a filesystem to create and check. Plan 11 must amend SA-4 and its storage register. Plan 37 must replace "no PVC in ci-jobs" with rule 3. Plan 43 adds the orphan-PVC sweep to its retention table.
- On self-hosting-minimum nodes, 12 GiB of disk is reserved for CI. Plan 51's disk budget accounts for it.

## Alternatives rejected
- Plain `local-path` on the root filesystem: unbounded, violates SPEC §107.
- A per-StorageClass mapping in the k3s-managed `local-path-config`: k3s re-applies its addon manifests, so the mapping could silently disappear and CI volumes would land on `/` again (STO-8).
- Longhorn: SPEC §27 says CI scratch SHOULD NOT live there, and replica traffic on a single node is wasted.
- Single-step workflows that skip the clone step: still create the PVC, and cloning inside the step would require giving the netrc credential to untrusted step images.

## Verification
P08 gate: 20-AC-9 (a workflow's PVC has class `ci-workspace` and is bound to a PV under `/var/lib/sc/ci-workspace`); 20-AC-10 (a step that writes 20 GiB fails with `No space left on device` while `/` stays below its pre-test usage + 1 GiB); 20-AC-11 (an orphan PVC is removed by the next `sc-ci-maint` run).
