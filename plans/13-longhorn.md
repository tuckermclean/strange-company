# 13 — Longhorn

**Status:** Stage-0 plan (draft)
**DAG nodes:** P04 (co-owned: Longhorn healthy in the seed, StorageClasses, `forgejo-data` volume), P09 (13-AC-23), P10 (co-owned: backup target, recurring jobs, restore test), C08 (13-AC-24, `hermes-data`), H02 (co-owned: replicas 1→2 including existing volumes)
**Related plans:** [07](07-k3s.md), [08](08-node-security.md), [11](11-storage.md), [14](14-object-storage.md), [15](15-authentik.md), [16](16-forgejo.md), [18](18-flux.md), [19](19-seed-handoff.md), [24](24-hermes.md), [36](36-network-policy.md), [37](37-admission-policy.md), [38](38-prometheus.md), [42](42-alerting-notifications.md), [43](43-retention-gc.md), [44](44-backup.md), [45](45-recovery.md), [47](47-ha-expansion.md), [48](48-upgrade.md), [51](51-capacity-and-scheduling.md)
**Contracts:** produces `restore-test-result` documents through plan 44 (`rt-longhorn-canary`, `rt-longhorn-volume`); consumes `cluster-profile.schema.yaml` (`storage.longhorn.replicas`)
**ADRs:** ADR-0203 (plan 11: StorageClass policy, stable-named volumes); introduces ADR-0201 (Longhorn scope and settings: V1 engine, non-default class, replica policy, backup target as CR, no automatic Helm rollback)
**Procedures:** owns `procedures/longhorn-replica-loss.md`, `procedures/restore-longhorn.md`

## 1. Purpose

Longhorn V1 provides replicated block storage for the few stateful applications that cannot replicate themselves: Forgejo repository storage and Hermes `/opt/data`, plus a DR canary volume (SPEC §27). This plan defines the install (seed set), node prerequisites, StorageClasses and settings, stable-named volumes, the replica path from 1 to 2 for new **and existing** volumes, recurring snapshots and backups to the `longhorn-backups` bucket, restore, the RWO Multi-Attach detach-lag failure mode (lesson L-5), UI access, resource cost, and upgrade constraints.

## 2. Requirements derived from SPEC.md

1. Longhorn **V1** provides replicated persistent block storage for applications that cannot replicate themselves. [SPEC §20, §27]
2. `replicas: 1` at bootstrap, `replicas: 2` at small-ha. [SPEC §11, §12, §27, §119 PHASE 6]
3. Appropriate uses: Forgejo repository storage, application upload directories, ordinary stateful apps. Not CNPG PGDATA, agent workspaces, CI scratch or reconstructible caches. [SPEC §27, §25, §28]
4. Forgejo Git repository storage uses Longhorn. [SPEC §30]
5. Critical Longhorn data: RPO ≤ 24 h, RTO ≤ 8 h. [SPEC §108]
6. Repeatable restore testing exists for important Longhorn data, and failure is alertable. [SPEC §109, §96]
7. Degraded Longhorn raises an alert. [SPEC §96]
8. Longhorn snapshots have intentional cleanup. [SPEC §107]
9. Workers have no Longhorn admin and no network path to Longhorn management. [SPEC §86, §88]
10. Infrastructure control interfaces are not exposed publicly without justification. [SPEC §18]
11. Longhorn is part of the seed. [SPEC §34]
12. Procedures exist for Longhorn replica loss and Longhorn restore. [SPEC §113]

Derived:
- **13-R-1** `longhorn` is **not** the default StorageClass (ADR-0203). Every Longhorn PVC names its class.
- **13-R-2** Important volumes are **stable-named**: Longhorn `Volume`, PV and PVC share one name (`forgejo-data`, `hermes-data`, `dr-canary`), so backups, restores and recovery overlays address them deterministically.
- **13-R-3** The replica count of every important volume equals `LONGHORN_REPLICAS` within 30 minutes of a profile change, including volumes that already exist (§9.6).
- **13-R-4** Longhorn is never rolled back automatically by Helm. Longhorn does not support downgrades (verify), so a failed upgrade stays failed and is handled by procedure.
- **13-R-5** A Longhorn `Volume` or PV holding register data is never pruned by Flux and never deleted by automation.

## 3. Inputs

- `cluster-vars` (plan 18 §9.2; additions requested): `LONGHORN_REPLICAS` (`1`|`2`), `STORAGE_PROFILE` (`preferred`|`constrained`), `S3_ENDPOINT`, `S3_REGION`, `S3_BUCKET_PREFIX`, `NODE_COUNT`.
- SOPS Secret `longhorn-system/longhorn-backup-s3` (`SC-OBJECT-STORAGE/longhorn-backups`, plan 14 §10).
- Node prerequisites delivered by plan 08's baseline role (§9.1).
- In recovery mode: the resolved backup URL per volume in `gitops/clusters/<cluster>/config/recovery.yaml` (plan 45, ADR-0554).

## 4. Outputs

- StorageClasses `longhorn` and `longhorn-scratch` (plan 11 §9.1).
- Stable-named volumes `forgejo-data` (ns `forgejo`), `hermes-data` (ns `hermes`), `dr-canary` (ns `dr-tests`).
- Backups under `s3://<prefix>-longhorn-backups/backupstore/` (Longhorn-native layout).
- Metrics (ServiceMonitor owned by plan 38) and alert signals (§21).
- Component `gitops/components/longhorn-volume-recovery` used by the recovery overlay (§9.7).

## 5. Dependencies

- **Hard:** P03 (k3s, node baseline including iSCSI), F11/F12 (tree renders, policies admit Longhorn in `longhorn-system` with PSA `privileged`), P01 (bucket and credential) before the backup target is configured.
- **Seed ordering** (plan 18 §8.3): layer `longhorn` (HelmRelease) → layer `longhorn-config` (StorageClasses, `BackupTarget`, `RecurringJob`s, the stable-named triplets of `forgejo-data` **and** `hermes-data`) → layer `forgejo`. Every important stable volume lives only in `longhorn-config` (ADR-0203, STO-5; plan 18 §8.3 row 8): the Hermes layer and the `strange-company` chart never render the `hermes-data` PV, PVC or `Volume` (plan 24, plan 54); they only mount the PVC. The `hermes` namespace exists from the `namespaces` layer, so the triplet can be created during seed. `dr-canary` is a test fixture, not register data, and stays in the `backups` layer (plan 44).
- **Soft:** plan 36 (NetworkPolicy for `longhorn-system`), plan 38 (scrape and KSM CRS), plan 51 (memory budget, PriorityClass `platform-critical`), plan 08 (reboot and drain procedure).
- **Runtime:** object storage for backups only. Volumes keep working during an outage.

## 6. Owned state

| State | Location | Writers |
|---|---|---|
| Replica data (register S-47) | `/var/lib/longhorn` on each node's root filesystem | Longhorn engine and replica processes |
| Longhorn metadata (S-46): `Volume`, `Replica`, `Engine`, `Setting`, `RecurringJob`, `BackupTarget`, `BackupVolume`, `Backup`, `Snapshot` CRs | etcd | longhorn-manager; desired settings and jobs from Git |
| Backups | `s3://<prefix>-longhorn-backups/backupstore/` | longhorn-manager with `longhorn-backups` credential |

The data *inside* volumes belongs to the applications (plans 16, 24, 44).

## 7. External interfaces

### 7.1 Which PVCs use Longhorn (complete list)

| PVC (namespace/name) | Register | StorageClass | Size preferred / constrained (small-ha) | Replicas | Recurring group | Backup max age (alert) |
|---|---|---|---|---|---|---|
| `forgejo/forgejo-data` | S-01, S-02, S-07 | `longhorn` | 20Gi / 12Gi (50Gi at small-ha, plan 16) | `LONGHORN_REPLICAS` | `critical` | 26 h |
| `hermes/hermes-data` | S-30 | `longhorn` | 5Gi / 3Gi | `LONGHORN_REPLICAS` | `critical` | 26 h |
| `dr-tests/dr-canary` | probe | `longhorn` | 1Gi | `LONGHORN_REPLICAS` | `critical` | 26 h |
| `dr-tests/rt-*` (transient restore targets) | — | `longhorn-scratch` | ≤ source size | 1 | none | — |

Nothing else may use `longhorn` or `longhorn-scratch` (plan 11 §9.2; F11 check and `storage-audit` SA-3). Authentik has no media volume (plan 15). Woodpecker, Prometheus, Alertmanager and CNPG use `local-path` or no PVC.

### 7.2 StorageClasses (rendered in layer `longhorn-config`)

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: longhorn
  annotations:
    storageclass.kubernetes.io/is-default-class: "false"
    kustomize.toolkit.fluxcd.io/force: enabled     # parameters are immutable; replace on change
provisioner: driver.longhorn.io
reclaimPolicy: Retain
allowVolumeExpansion: true
volumeBindingMode: Immediate
parameters:
  numberOfReplicas: "${LONGHORN_REPLICAS}"
  staleReplicaTimeout: "30"
  dataLocality: best-effort
  fsType: ext4
  dataEngine: v1
---
# longhorn-scratch: identical except reclaimPolicy: Delete, numberOfReplicas: "1", dataLocality: disabled
```

The chart's own default class is disabled (`persistence.defaultClass: false`), so the chart does not render a competing `longhorn` class.

### 7.3 Stable-named volume triplet (ADR-0203)

```yaml
apiVersion: longhorn.io/v1beta2                 # verify served version
kind: Volume
metadata:
  name: forgejo-data
  namespace: longhorn-system
  labels:
    strange.company/state-id: S-01
    strange.company/backup-max-age-seconds: "93600"
    recurring-job-group.longhorn.io/critical: enabled      # verify label form
  annotations:
    kustomize.toolkit.fluxcd.io/ssa: IfNotPresent
    kustomize.toolkit.fluxcd.io/prune: disabled
spec:
  size: "21474836480"
  numberOfReplicas: ${LONGHORN_REPLICAS}
  dataLocality: best-effort
  accessMode: rwo
  frontend: blockdev
  dataEngine: v1
---
apiVersion: v1
kind: PersistentVolume
metadata:
  name: forgejo-data
  annotations: {kustomize.toolkit.fluxcd.io/prune: disabled}
spec:
  capacity: {storage: 20Gi}
  accessModes: [ReadWriteOnce]
  persistentVolumeReclaimPolicy: Retain
  storageClassName: longhorn
  volumeMode: Filesystem
  csi: {driver: driver.longhorn.io, fsType: ext4, volumeHandle: forgejo-data}
  claimRef: {namespace: forgejo, name: forgejo-data}
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: forgejo-data
  namespace: forgejo
  labels: {strange.company/state-id: S-01}
  annotations: {kustomize.toolkit.fluxcd.io/prune: disabled}
spec:
  storageClassName: longhorn
  volumeName: forgejo-data
  accessModes: [ReadWriteOnce]
  resources: {requests: {storage: 20Gi}}
```

`ssa: IfNotPresent` on the `Volume` means Flux creates it once and never fights longhorn-manager over its spec (plan 11 §17). Changes to a live volume (replica count, size) therefore go through the mechanisms in §9.6 and §19, not through Flux patches. The consuming chart must **not** create the PVC (Forgejo: `persistence.create: false`, §25 Q-13-3).

### 7.4 Human access

- **The Longhorn UI is not exposed** through ingress, consistent with plan 15 (§121 table: "Longhorn UI — no"). Its powers (delete volumes, detach, change replicas) are destructive, and every procedure in this plan works through `kubectl` on Longhorn CRs.
- Optional UI access for an operator: `./bootstrap shell`, then `kubectl -n longhorn-system port-forward svc/longhorn-frontend 8080:80`, which needs the toolbox to publish the port to the host (assumed plan 50/02 option `./bootstrap shell --publish 8080`, §25 Q-13-4). No forward-auth route is created. If a future plan exposes it, it must be behind Authentik forward-auth restricted to `sc-admins` (plan 15) and requires an ADR.

## 8. Internal interfaces

- CSI: kubelet ↔ `longhorn-csi-plugin` (DaemonSet) ↔ longhorn-manager; sidecars attacher, provisioner, resizer, snapshotter.
- longhorn-manager ↔ instance-manager (engine and replica processes) on each node; replica traffic between nodes at small-ha travels over the pod network, which flannel WireGuard encrypts (ADR-0011).
- longhorn-manager ↔ S3 for backups (`BackupTarget default`).
- Prometheus → `longhorn-backend:9500/metrics` (plan 38).

## 9. Configuration

### 9.1 Node prerequisites (delivered by plan 08, checked here)

| Prerequisite | Requirement | Check |
|---|---|---|
| `open-iscsi` | installed; `iscsid` enabled and active; kernel module `iscsi_tcp` loadable | `systemctl is-active iscsid` = `active` |
| `multipathd` | disabled, **or** `/etc/multipath.conf` blacklists `devnode "^sd[a-z0-9]+"`, so it does not claim Longhorn block devices (known Longhorn issue) | `multipath -t` shows the blacklist, or `systemctl is-enabled multipathd` ≠ `enabled` |
| Filesystem | `/var/lib/longhorn` on the root filesystem, ext4 or xfs (plan 08 baseline) | `findmnt -T /var/lib/longhorn -o FSTYPE` ∈ {ext4, xfs} |
| Mount propagation | k3s default (shared) | Longhorn environment check passes |
| `nfs-common` | **not installed**: no RWX volumes and no NFS backup target. Longhorn's environment check may warn about it; that warning is expected | — |
| `cryptsetup`, `dm_crypt` | not required (no encrypted volumes in MVP) | — |
| Supported OS | plan 07/08 matrix (Debian 13, Ubuntu 24.04) | plan 08 |

13-AC-4 runs `longhornctl check preflight` (verify the tool name and flags at the pinned Longhorn version; fallback is the chart's environment-check output) from the toolbox.

### 9.2 Install (seed set, layer `longhorn`)

| Item | Decision |
|---|---|
| Chart | `longhorn` from `https://charts.longhorn.io` as `HelmRelease longhorn` in `longhorn-system`, release name `longhorn`. **Selection rule:** latest Longhorn minor that (a) supports the pinned k3s Kubernetes minor per Longhorn's support matrix, (b) is at patch ≥ `.2`, and (c) has a supported upgrade path from the installed minor (one minor at a time). |
| Images | digest-pinned through chart values `image.longhorn.*.tag` + digest overrides (F11 `images-pinned`, plan 18) |
| Remediation | `upgrade.remediation.retries: 0`, `remediateLastFailure: false`; `install.remediation.retries: 3`. This deviates from plan 18's `controller` class (§25 Q-13-1). |
| Namespace | `longhorn-system`, PSA `privileged` (master plan §5), tier `platform-critical` |
| `priorityClass` | `platform-critical` for manager, driver, CSI and instance managers |
| CSI sidecar replicas | `csi.attacherReplicaCount`, `provisionerReplicaCount`, `resizerReplicaCount`, `snapshotterReplicaCount`: `1` at self-hosting-minimum, `2` at small-ha |
| UI | `longhornUI.replicas: 1` (preferred), `0` on `constrained` (scaled up on demand by procedure) |
| `preUpgradeChecker.jobEnabled` | `true` (chart refuses an unsupported upgrade path, verify) |
| `persistence.defaultClass` | `false` |
| `enablePSP` | `false` |
| `metrics.serviceMonitor.enabled` | `false` (plan 38 owns the ServiceMonitor, ADR-0550) |

### 9.3 Settings (chart `defaultSettings`; values identical in Git for both profiles unless noted)

| Setting | Value | Reason |
|---|---|---|
| `defaultDataPath` | `/var/lib/longhorn` | plan 11 disk budget |
| `defaultReplicaCount` | `${LONGHORN_REPLICAS}` | 13-R-3 (new volumes) |
| `replicaSoftAntiAffinity` | `false` | replicas of one volume never share a node |
| `replicaZoneSoftAntiAffinity` | `true` | no zones on VPS |
| `replicaAutoBalance` | `best-effort` | spreads replicas after nodes are added (H01/H02) |
| `storageOverProvisioningPercentage` | `100` | no thin-provisioning overcommit beyond actual capacity |
| `storageMinimalAvailablePercentage` | `15` | stop scheduling replicas before the root disk is critically full (plan 11 §12) |
| `storageReservedPercentageForDefaultDisk` | `30` | leaves room for OS, images, PGDATA on the shared root filesystem |
| `nodeDownPodDeletionPolicy` | `delete-both-statefulset-and-deployment-pod` | a pod on a dead node is deleted so its volume can attach elsewhere (plan 16 F3 depends on it) |
| `nodeDrainPolicy` | `allow-if-replica-is-stopped` (1 node) / `block-if-contains-last-replica` (small-ha) | a single-node drain for reboot (plan 08) must not hang; at small-ha a drain must not remove the last replica (verify value names) |
| `autoDeletePodWhenVolumeDetachedUnexpectedly` | `true` | a pod whose volume detached is restarted instead of serving I/O errors |
| `allowRecurringJobWhileVolumeDetached` | `true` | `dr-canary` is detached most of the time but must be backed up daily |
| `autoCleanupRecurringJobBackupSnapshot` | `true` | snapshots created by backup jobs are removed after upload (plan 43 R-13) |
| `snapshotMaxCount` | `20` | hard cap per volume (verify setting availability) |
| `snapshotDataIntegrity` | `fast-check` with `snapshotDataIntegrityCronjob: "0 4 * * 6"` | weekly checksum of snapshot data; a corrupt replica is rebuilt |
| `backupCompressionMethod` | `lz4` | |
| `backupConcurrentLimit` / `restoreConcurrentLimit` | `2` / `2` | bounded I/O and egress |
| `concurrentAutomaticEngineUpgradePerNodeLimit` | `1` | engine upgraded automatically after a manager upgrade, one volume at a time |
| `guaranteedInstanceManagerCPU` | `5` (percent of node CPU) | default 12 % reserves too much on 4 vCPU (plan 51 budget) |
| `upgradeChecker` | `false` | no outbound version checks |
| `allowCollectingLonghornUsageMetrics` | `false` | no telemetry to the vendor |
| `v1DataEngine` / `v2DataEngine` | `true` / `false` | V1 only (SPEC §20) |
| `deletingConfirmationFlag` | `false` | an accidental chart uninstall is refused (verify behavior) |
| `createDefaultDiskLabeledNodes` | `false` | every node gets the default disk |
| `priorityClass` | `platform-critical` | |

Whether `defaultSettings` changes are applied to an existing installation (rather than only at first install) is version-sensitive (verify). 13-AC-14 checks the live `settings.longhorn.io` values after a change. If the chart does not sync them, the same values are rendered as `Setting` CRs in layer `longhorn-config`.

### 9.4 Backup target (layer `longhorn-config`)

```yaml
apiVersion: longhorn.io/v1beta2
kind: BackupTarget
metadata:
  name: default
  namespace: longhorn-system
  annotations: {kustomize.toolkit.fluxcd.io/prune: disabled}
spec:
  backupTargetURL: s3://${S3_BUCKET_PREFIX}-longhorn-backups@${S3_REGION}/
  credentialSecret: longhorn-backup-s3
  pollInterval: 300s
```

Secret `longhorn-backup-s3` keys: `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_ENDPOINTS` (`https://<S3 endpoint>`), `VIRTUAL_HOSTED_STYLE` (`"false"` when `S3_FORCE_PATH_STYLE=true`), and `AWS_CERT` only if `S3_CA_BUNDLE` is set. The `BackupTarget` CR replaces the older `backup-target` setting in recent Longhorn minors (verify at the pinned version; fallback is the `backupTarget`/`backupTargetCredentialSecret` settings).

### 9.5 Recurring jobs (layer `longhorn-config`)

| RecurringJob | Task | Cron (UTC) | Retain | Groups | Purpose |
|---|---|---|---|---|---|
| `snap-6h` | `snapshot` | `0 */6 * * *` | 4 | `critical` | fast local rollback window of 24 h (plan 16) |
| `backup-12h` | `backup` | `30 3,15 * * *` | 14 | `critical` | RPO ≤ 24 h with one missed run tolerated before `SCLonghornBackupStale` (26 h) fires (STO-22) |
| `backup-weekly` | `backup` | `0 4 * * 0` | 4 | `critical` | 4 weeks of history |
| `trim-weekly` | `filesystem-trim` | `0 5 * * 0` | 0 | `critical` | returns freed blocks to the node disk |
| `snapshot-cleanup-daily` | `snapshot-cleanup` | `0 6 * * *` | 0 | `critical` | removes system snapshots |

All jobs have `concurrency: 1`. Volumes join through their `recurring-job-group.longhorn.io/critical: enabled` label. Volumes with no recurring-job label would fall into Longhorn's `default` group, which no job here uses, so `rt-*` scratch volumes get nothing. The `backup-12h` slot (03:30) comes after the `dr-canary-writer` run (03:00, plan 44) and before the weekly Longhorn restore test (Wednesday 04:00, plan 44).

### 9.6 Replica count 1 → 2 (13-R-3)

1. **New volumes:** StorageClass `longhorn` parameter `numberOfReplicas` and setting `defaultReplicaCount` come from `${LONGHORN_REPLICAS}`. StorageClass parameters are immutable, so the class carries `kustomize.toolkit.fluxcd.io/force: enabled` and Flux recreates it. Existing PVs are unaffected by class recreation.
2. **Existing volumes:** the post-transfer layer `backups` (plan 18 row 20, not part of the seed set) contains a Job named `longhorn-replica-sync-r${LONGHORN_REPLICAS}` (ServiceAccount `longhorn-replica-sync`, RBAC `get,list,patch` on `volumes.longhorn.io` in `longhorn-system` only, image `restore-tester`, command `sc-restore-test longhorn-replica-sync --replicas ${LONGHORN_REPLICAS} --selector strange.company/state-id`). Because the name contains the target count, a profile change creates a new Job. The Job is deliberately **not** in the seed layer `longhorn-config` (STO-1): its image is an internal image, and nothing in the seed set may depend on an image that the installation has not yet published; at `-r1` it is a no-op anyway, so the seed loses nothing. Its image reference is the GHCR digest from `versions/internal-images.lock.yaml` (plan 44 §9.3). F11 check `seed-no-internal-images` (requested from plan 18) fails if any seed-set layer references an image in the Forgejo registry. It:
   - patches `spec.numberOfReplicas` on every `Volume` with a `strange.company/state-id` label whose StorageClass is `longhorn`;
   - waits (timeout 25 min per volume) for `status.robustness=healthy` and for `count(replicas in running state) == N` on distinct nodes;
   - exits non-zero on timeout. Flux reports the layer unhealthy, and the H02 gate fails.
   `ttlSecondsAfterFinished: 86400`. After deletion Flux recreates the Job, which then runs again as an idempotent no-op, so it doubles as a daily reconcile.
3. **Locality:** `dataLocality: best-effort` keeps one replica on the node running the consumer pod, which reduces I/O latency for Forgejo. After a pod moves, Longhorn rebuilds a local replica and then removes the surplus.
4. **Order at H02** (plan 47): nodes 2 and 3 join and become Longhorn-schedulable (`nodes.longhorn.io` `Schedulable=True`) → profile commit sets `LONGHORN_REPLICAS=2` → Flux recreates the class and runs the sync Job → `replicaAutoBalance` spreads replicas.
5. **2 → 1** (node removal, plan 47): the same Job with `-r1` removes surplus replicas. Longhorn chooses which replica to remove; the procedure removes the node's disk scheduling first (`nodes.longhorn.io` `allowScheduling=false`, eviction requested) so replicas leave that node before it goes.

### 9.7 Restore and recovery overlay

- **Restore into a new volume** (restore test, or partial restore): create a `Volume` with `spec.fromBackup: <backup URL>` (form `s3://<bucket>@<region>/?backup=<backup-name>&volume=<volume-name>`, verify), `numberOfReplicas: 1`, then a PV/PVC with `longhorn-scratch`. The volume reports `status.restoreRequired` until the restore completes (verify field); it can be attached only after that.
- **Replace an important volume** (`restore-longhorn.md`): scale the consumer to 0 → rename-free swap: delete the PVC, PV and Longhorn `Volume` of the *damaged* volume **only after** a fresh backup of it exists if it is still readable (forensics) → apply the recovery component that renders the same triplet with `fromBackup` → wait for restore → scale up.
- **Recovery overlay** (plan 45, ADR-0554; DR-5): one component **per volume**, `gitops/components/longhorn-volume-recovery-forgejo` and `gitops/components/longhorn-volume-recovery-hermes`, each patching its `Volume` with `spec.fromBackup: ${LONGHORN_FORGEJO_DATA_BACKUP_URL}` or `${LONGHORN_HERMES_DATA_BACKUP_URL}`. `./bootstrap recover` resolves the newest completed backup per volume from the backup-store listing (`backupstore/volumes/**/backups/backup_*.cfg`, the same parser as `backup.LastSuccess`, plan 14 §7.3), writes the URL into **`vars/cluster-vars.yaml`** (the only file Flux `substituteFrom` reads; `config/recovery.yaml` records it as evidence), and adds only the components of volumes that have a backup. A volume without a backup gets no component and is created empty, which the recovery report states explicitly (for Forgejo, the bundle rehydrate of plan 44 §9.2.6 follows). The `Volume.spec.size` in Git must be ≥ the backed-up volume's size, or the restore fails (STO-19): F11 check `triplet-size-consistency` (requested from plan 18) requires PVC request == PV capacity == `Volume.spec.size` for every triplet, and the recovery resolver aborts with `LONGHORN_BACKUP_LARGER_THAN_VOLUME` if a backup's size exceeds `Volume.spec.size`. Because the `Volume` is `ssa: IfNotPresent`, the patch only matters at creation, which is exactly the recovery case. The finalize PR removes the components.
- **Pre-recovery backups (DR-31a):** after a recovery, `backup-12h` (retain 14) would prune the pre-recovery backups of the same volume within 7 days, removing the forensic and rollback source. The recovery commit therefore raises `backup-12h` retain to 60 (30 days) through the same per-volume components, and the finalize PR keeps that value for 30 days in a follow-up `RecurringJob` patch that a dated PR removes (plan 45 schedules it).
- **Black start without a usable Longhorn backup:** Forgejo starts on an empty volume and repositories are restored from bundles and GitHub (plans 16 §20, 44). Outside black start, the same outcome (an empty stable volume, then rehydrate) is `restore-longhorn.md` §Empty stable volume.

### 9.8 Configuration ownership

All values above live in `gitops/platform/longhorn/` (HelmRelease values ConfigMap, StorageClasses, `BackupTarget`, `RecurringJob`s); the replica-sync Job lives in `gitops/platform/backups/longhorn-replica-sync/` (layer `backups`, STO-1) and are changed by PR. Profile-dependent values come only from `cluster-vars`.

## 10. Secrets

| Class | Object | Notes |
|---|---|---|
| `SC-OBJECT-STORAGE/longhorn-backups` | `longhorn-system/longhorn-backup-s3` (SOPS, both recipients) | L G P D on B5; no version deletion (plan 14). Rotation per `rotate-storage-credentials.md`; Longhorn re-reads the Secret on the next backup-target poll (verify; otherwise restart longhorn-manager). |
| (no Longhorn admin credential) | — | Longhorn has no API authentication. Its authority is Kubernetes RBAC on `longhorn.io` CRs plus network access to `longhorn-backend`/`longhorn-frontend`. Only cluster admins (break-glass, plan 15) and the `longhorn-replica-sync` and `restore-tester` ServiceAccounts (narrow RBAC) hold rights on these CRs. Workers have none [SPEC §86]. |

## 11. Storage

All replica data is on the node root filesystem under `/var/lib/longhorn` (plan 11 §12.1 data part: `G` + `H` + ≈ 15 % snapshot overhead, bounded by the data ceiling of 16 GiB on a 100 GiB disk and 81 GiB on 200 GiB; STO-3). Snapshot overhead is bounded by `snap-6h` retain 4, backup-snapshot cleanup and `snapshotMaxCount`. `storageMinimalAvailablePercentage: 15` stops new replica scheduling before the disk is critical; existing volumes keep working.

## 12. Resource requirements

Estimates per node (planning figures; plan 51 records measured values at P12):

| Component | Requests | Limits | Count (minimum / small-ha) |
|---|---|---|---|
| longhorn-manager (DaemonSet) | 50m / 128Mi | 512Mi | 1 / 3 |
| instance-manager (engine + replica processes, 3 volumes) | 5 % CPU (`guaranteedInstanceManagerCPU`) / 256Mi | none set by Longhorn (verify) | 1 / 3 |
| longhorn-csi-plugin (DaemonSet) | 10m / 32Mi | 128Mi | 1 / 3 |
| CSI sidecars ×4 | 10m / 24Mi each | 64Mi each | 4 / 8 |
| longhorn-driver-deployer | 10m / 32Mi | 64Mi | 1 |
| longhorn-ui | 10m / 32Mi | 64Mi | 1 / 1 (0 on constrained) |
| engine-image DaemonSet | 10m / 16Mi | 64Mi | 1 / 3 |
| **Steady-state working set** | | | ≈ 0.6–0.9 GiB on one node |

These go into the ADR-0020 memory-budget gate. Restore-test volumes add one transient replica (≤ source size on disk) for the duration of a test.

## 13. Security boundary

- Namespace `longhorn-system` is PSA `privileged`, which Longhorn requires (host mounts, iSCSI, device access). It is isolated by NetworkPolicy (plan 36): ingress to `longhorn-backend:9500` from `monitoring` (Prometheus) and from longhorn components; ingress to `longhorn-frontend:80` from `longhorn-system` only (port-forward goes through the API server); webhooks and conversion from the API server; egress to the S3 endpoint (443), the API server, DNS and peer nodes. **No path from `agent-jobs`, `ci-jobs` or `hermes`** [SPEC §88].
- RBAC: no human role except cluster-admin (break-glass) can modify `longhorn.io` CRs. `restore-tester` (plan 44) may create, get and delete `volumes.longhorn.io` whose name matches `rt-*`. RBAC cannot restrict by name prefix on create, so a ValidatingAdmissionPolicy enforces it (`dr-tests` restore-tester may only create or delete Longhorn `Volume`s named `rt-*` with `numberOfReplicas: 1`; plan 37 renders it).
- VAP (plan 37): no PVC in `agent-jobs`, and only `ci-workspace` PVCs in `ci-jobs`/`ci-build` (plan 11 SA-3/SA-4, ADR-0305), so workers and CI pods cannot mount Longhorn volumes.
- Backups are protected against cluster-side destruction by plan 14 (no version deletion, Object Lock where available). They are not client-side encrypted (SI-G3-03 applies equally to Longhorn backups); Forgejo repositories are additionally covered by age-encrypted bundles.

## 14. Happy path

1. P03: plan 08 baseline installs and enables `open-iscsi` and the multipath blacklist.
2. P04 seed: layer `longhorn` installs the chart; manager, driver, CSI and instance managers become Ready; `nodes.longhorn.io/<node>` reports `Ready` and `Schedulable`.
3. Layer `longhorn-config`: StorageClasses, `BackupTarget default` (becomes `available` once the credential works), recurring jobs and the `forgejo-data` triplet; only upstream images, all digest-pinned (the replica-sync Job arrives later in layer `backups`). The Forgejo layer then mounts `forgejo-data`.
4. Post-transfer: Hermes mounts the already existing `hermes-data` (created empty by `longhorn-config` at seed), and the `backups` layer (`dr-canary`, plan 44) arrives.
5. Daily: snapshots every 6 h; backup at 03:30; weekly backup, trim and integrity check; weekly restore tests by plan 44.
6. H02: `LONGHORN_REPLICAS=2`, replica sync, auto-balance; every important volume is healthy with replicas on two distinct nodes.

## 15. Failure modes

| Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| One replica fails (small-ha) | `SCLonghornVolumeDegraded` (`longhorn_volume_robustness == 2`, 10m) | redundancy reduced; I/O continues | Longhorn rebuilds a replica on another schedulable node | `longhorn-replica-loss.md` |
| Only replica fails or its node is lost (replicas = 1) | `SCLonghornVolumeFaulted` (`== 3`), consumer down | data unavailable | none | `restore-longhorn.md` (from backup) |
| All replicas faulted (small-ha) | `SCLonghornVolumeFaulted` | data unavailable | Longhorn tries auto-salvage (verify setting `autoSalvage`, default true) | `longhorn-replica-loss.md` §Salvage, else `restore-longhorn.md` |
| **RWO Multi-Attach / detach lag (L-5)**: after a rollout or node loss, the new pod stays `ContainerCreating` with `FailedAttachVolume … Multi-Attach error` or "volume is still attached to another node" | `SCLonghornVolumeAttachStuck` (new: `longhorn_volume_state == 4 or longhorn_volume_state == 5` for 10m, warning; 20m critical) plus plan 16 `SCForgejoPodStuckPending` | consumer down | Kubernetes force-detaches after 6 min only if the old node is NotReady; Longhorn deletes pods on down nodes (`nodeDownPodDeletionPolicy`). **Never automatic force-detach on a reachable node** | `longhorn-replica-loss.md` §Stuck attachment |
| Node disk below `storageMinimalAvailablePercentage` | `SCLonghornNodeStorageLow` (80 %), node `Schedulable=False` | no new replicas or rebuilds on that node | none | `disk-pressure.md` (plan 08); plan 43 retention review |
| Volume nearly full | `SCLonghornVolumeNearlyFull` (85 %) | application writes fail at 100 % | none | expand by PR (§19) |
| Backup target unavailable (S3 outage, credential) | `SCLonghornBackupTargetUnavailable` (new; KSM CRS `sc_longhorn_backup_target_available == 0` for 15m), then `SCLonghornBackupStale` | backups stop; snapshots continue | next poll and schedule retry | `object-storage-outage.md` |
| Backup stale for a volume | `SCLonghornBackupStale` (age > the volume's `backup-max-age-seconds`) | RPO breach | none | `restore-test-failure.md` §Longhorn backup |
| Restore test fails | `SCRestoreTestLonghornFailed` | backup unproven | none | `restore-test-failure.md` |
| longhorn-manager or instance-manager crash | `SCLonghornManagerDown` (new: `kube_daemonset_status_number_unavailable{namespace="longhorn-system"} > 0` for 10m) | attach/detach and rebuild stall; an instance-manager crash detaches the volumes on that node, and pods restart (`autoDeletePodWhenVolumeDetachedUnexpectedly`) | kubelet restart | `longhorn-replica-loss.md` §Manager |
| `iscsid` missing or stopped after OS update | attach fails with iSCSI errors; plan 08 baseline check | new attachments fail | none | plan 08 node baseline re-run; `longhorn-replica-loss.md` §Node prerequisites |
| `multipathd` claims Longhorn devices | mount fails `already mounted or mount point busy` (Longhorn known issue) | attach fails | none | same section; fix blacklist |
| Replica-sync Job times out at H02 | Job failed; layer `longhorn-config` not Ready (`SCFluxReconciliationFailing`) | volumes keep the old replica count | none | `longhorn-replica-loss.md` §Replica count |
| Snapshot accumulation | `retention-audit` RA-13 (plan 43): > 12 snapshots on a volume | disk growth | `snapshotMaxCount: 20` hard cap | plan 43 |
| PVC accidentally deleted | PV `Released` (Retain), consumer Pending | consumer down; data intact | none | `restore-longhorn.md` §Rebind |
| Longhorn `Volume` CR deleted by a human | Volume gone, replicas deleted by Longhorn | data lost locally | none (prune disabled stops Flux, not humans) | `restore-longhorn.md` (from backup) |
| Upgrade fails mid-way | HelmRelease `UpgradeFailed`; no rollback (13-R-4) | managers on mixed versions | none | `restore-longhorn.md` §Upgrade failure; plan 48 |
| Snapshot data corruption | `fast-check` integrity marks a replica failed, then rebuild | none if another healthy replica exists | rebuild | `longhorn-replica-loss.md` |

### 15.1 Stuck attachment (L-5): why and how it is recovered

With `strategy: Recreate` and one RWO volume, the new pod is created only after the old pod is gone. The Kubernetes VolumeAttachment and Longhorn's attachment tickets (`volumeattachments.longhorn.io`) can still hold the volume on the old node for a while after the old pod terminated. On the same node this usually resolves within a minute. On a different node (rescheduling after node loss or drain) the attach-detach controller reports Multi-Attach until the old attachment is released. If the old node is unreachable, release only happens after the 6-minute force-detach timeout, or immediately if the node is tainted `node.kubernetes.io/out-of-service` (non-graceful node shutdown, GA since Kubernetes 1.28).

Recovery, in order (detailed in `longhorn-replica-loss.md` §Stuck attachment):
1. Verify that no running pod anywhere mounts the PVC.
2. If the old node is NotReady and **confirmed powered off**: taint it `node.kubernetes.io/out-of-service=nodeshutdown:NoExecute`. The volume detaches and the new pod attaches.
3. If the old node is Ready but the attachment is stale: delete the stale Kubernetes `VolumeAttachment` for the PV on the old node. The CSI attacher detaches, and the new pod proceeds after its next retry (delete the pending pod to retry at once).
4. Never edit Longhorn `Engine`/`Replica` objects, and never force-delete a pod whose node is reachable and whose containers may still be running. Writing from two nodes corrupts the filesystem.

Prevention: `Recreate` strategy (plan 16), `nodeDownPodDeletionPolicy`, and the P10 drill 13-AC-12.

## 16. Retry behavior

- Longhorn rebuilds replicas automatically with its own backoff. Concurrent rebuilds per node are limited (setting `concurrentReplicaRebuildPerNodeLimit: 2`, verify name).
- Recurring backup failures are retried at the next schedule. There is no extra loop; `SCLonghornBackupStale` covers persistence.
- The replica-sync Job: `backoffLimit: 2`, `activeDeadlineSeconds: 3600`.
- Restore tests do not retry (plan 44).

## 17. Idempotency behavior

- HelmRelease, StorageClasses (force-replaced only when parameters change), `BackupTarget`, `RecurringJob`s: declarative.
- Stable-named `Volume`s: `ssa: IfNotPresent`. Re-applying is a no-op, and a deleted `Volume` is re-created empty by Flux. That is why `restore-longhorn.md` suspends the layer Kustomization before replacing a volume (otherwise Flux would race the restore with an empty volume).
- Replica-sync Job: patching to the current value and waiting for health are no-ops.

## 18. Bootstrap behavior

- Seed: layer `longhorn` (timeout 15m) → `longhorn-config` (5m) → `forgejo` (plan 18). The `BackupTarget` may be `available=false` during seed if object storage is slow; layer health does not depend on it (health checks cover the HelmRelease, StorageClasses and the `forgejo-data` Volume `status.state` in {`detached`, `attached`}).
- `forgejo-data` is created empty and formatted on first attach (ext4).
- Black start: the recovery component sets `fromBackup` on `forgejo-data` and `hermes-data` (§9.7). Forgejo's layer waits for the Volume to finish restoring (health expression `status.restoreRequired == false`, verify field), so the RTO includes the download time (measured by `rt-longhorn-volume` as `restore_duration_seconds`).

## 19. Upgrade behavior

- **Path:** one Longhorn minor at a time (e.g. 1.9.x → 1.10.x); patches within a minor are free. Renovate proposes chart bumps (plan 23); plan 48 sequences them with k3s upgrades so the Kubernetes minor always stays inside Longhorn's support matrix.
- **No downgrade, no automatic rollback** (13-R-4, ADR-0201). A failed upgrade is fixed forward or restored (`restore-longhorn.md` §Upgrade failure).
- **Pre-upgrade gate** (plan 48 mechanism, required evidence): every important volume `robustness=healthy`; an on-demand backup of each (`kubectl -n longhorn-system create -f backup-<volume>.yaml` via the procedure, or `sc-bootstrap backup now --component longhorn` if plan 16's helper exists) completed within 2 h; `rt-longhorn-volume` passed within 8 d; the chart's pre-upgrade checker passes.
- **Engine upgrade:** automatic after the manager upgrade (`concurrentAutomaticEngineUpgradePerNodeLimit: 1`). Live engine upgrade of single-replica volumes may be unsupported (verify); then the engine upgrades on the next detach, which happens at the consumer's next restart. Plan 48 schedules a Forgejo and Hermes restart in the maintenance window. Old engine images are deleted after `kubectl get engineimages` shows `refCount=0`.
- **Volume expansion:** PR increases PVC `spec.resources.requests.storage` (and the PV/`Volume` sizes in Git for documentation). Online expansion through the CSI resizer is supported for V1 volumes in current Longhorn (verify). If not supported at the pinned version, the procedure scales the consumer to 0 first.
- **Kubernetes upgrades** (k3s, plan 07/48): drain with `nodeDrainPolicy` honored; on one node, Longhorn volumes detach during the drain.

## 20. Recovery behavior

| Situation | Path | RTO expectation |
|---|---|---|
| Replica loss, small-ha | automatic rebuild | minutes to an hour (data size / disk throughput) |
| Volume faulted or node disk lost, replicas = 1 | `restore-longhorn.md` from the newest backup (≤ 24 h old) | download time: ≈ 20 GiB at 50 MiB/s ≈ 7 min, plus attach; well within 8 h [SPEC §108] |
| Logical damage inside a volume (bad Forgejo upgrade) | revert to a Longhorn snapshot (≤ 6 h old, local) or restore from backup (plan 16 §19 rollback) | ≤ 1 h |
| Single Forgejo repository | **not** a volume restore: bundles (plan 44, `restore-project-repository.md`) | ≤ 1 h |
| Total cluster loss | recovery overlay `fromBackup` (§9.7, plan 45) | inside the black-start budget |

## 21. Observability

- **Metrics:** Longhorn manager `/metrics` (ServiceMonitor `longhorn`, plan 38): `longhorn_volume_robustness`, `longhorn_volume_state`, `longhorn_volume_actual_size_bytes`, `longhorn_volume_capacity_bytes`, `longhorn_node_status`, `longhorn_node_storage_usage_bytes`, `longhorn_node_storage_capacity_bytes`, `longhorn_instance_manager_*` (verify names at the pinned version; 13-AC-10).
- **KSM custom-resource state** (plan 38 adds): `sc_longhorn_backup_info{volume,state}` with `status.backupCreatedAt` (exists in plan 38), `sc_longhorn_backup_target_available` from `backuptargets.longhorn.io` `status.available`, and `sc_longhorn_backup_max_age_seconds{volume}` from the Volume label `strange.company/backup-max-age-seconds` (used by the catalogue's `SCLonghornBackupStale`).
- **Catalogue alerts consumed** (plan 42): `SCLonghornVolumeDegraded`, `SCLonghornVolumeFaulted`, `SCLonghornVolumeNearlyFull`, `SCLonghornNodeStorageLow`, `SCLonghornBackupStale`, `SCRestoreTestLonghornFailed`.
- **New alerts for plan 42:**

| alertname | Expression (sketch) | For | Severity / class | Runbook |
|---|---|---|---|---|
| `SCLonghornVolumeAttachStuck` | `longhorn_volume_state == 4 or longhorn_volume_state == 5` | 10m (warning), 20m (critical variant `SCLonghornVolumeAttachStuckCritical`) | warning/notify; critical/page | `longhorn-replica-loss.md` |
| `SCLonghornBackupTargetUnavailable` | `sc_longhorn_backup_target_available == 0` | 15m | warning / notify, `alwaysNotify: failed-backup` | `object-storage-outage.md` |
| `SCLonghornManagerDown` | `kube_daemonset_status_number_unavailable{namespace="longhorn-system"} > 0 or kube_deployment_status_replicas_available{namespace="longhorn-system",deployment=~"longhorn-driver-deployer|csi-.*"} == 0` | 10m | critical / page | `longhorn-replica-loss.md` |

- A replica count different from `LONGHORN_REPLICAS` on an important volume is detected by `storage-audit` SA-7 (plan 11), which raises `SCStorageAuditFailed`; no separate alert.
- **Grafana:** upstream Longhorn dashboard (plan 39) plus a panel per important volume showing last backup age and snapshot count.

## 22. Testing strategy

- **F11 offline:** render both profiles. Assert: `longhorn` class not default, `reclaimPolicy: Retain`, `numberOfReplicas` equals `LONGHORN_REPLICAS`; every Longhorn `Volume` has `ssa: IfNotPresent` and `prune: disabled`; every PV of class `longhorn` has `persistentVolumeReclaimPolicy: Retain` and `prune: disabled`; the HelmRelease has `upgrade.remediation.retries: 0`; images pinned; no Ingress or IngressRoute in `longhorn-system`.
- **k3d:** Longhorn cannot run meaningfully in k3d (iSCSI, host devices). Only the replica-sync Job logic is tested in k3d against Longhorn CRDs installed without the manager, plus the VAP for `rt-*` volumes.
- **Lab VMs** (`make e2e-lab-longhorn`; also 13-AC-25: the recovery overlay end to end, i.e. render `gitops/clusters/<cluster>` with fixture URLs in `cluster-vars`, apply it, and see `forgejo-data` restore with `fromBackup` and the fixture checksum match, DR-5; plan 05 QEMU VMs, the same VMs as plan 07's lab, deterministic and free): install Longhorn on 1 VM, create `forgejo-data`, write data, back up to the S3 emulator (plan 14), restore into `rt-*` and compare checksums; join 2 VMs, run the replica-sync Job with `-r2`, assert replicas on distinct nodes; power off the node holding the consumer pod and run the §15.1 recovery steps; upgrade from the previous Longhorn minor to the pinned one with a volume attached.
- **Live:** the gates below.

## 23. Acceptance criteria

### GATE-P04
- **13-AC-1** `./bootstrap exec -- kubectl -n longhorn-system get nodes.longhorn.io -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.status.conditions[?(@.type=="Ready")].status}{" "}{.status.conditions[?(@.type=="Schedulable")].status}{"\n"}{end}'` prints `True True` for every node, and `kubectl -n longhorn-system get ds,deploy -o json | jq '[.items[] | select((.status.numberUnavailable // 0) > 0 or (.status.unavailableReplicas // 0) > 0)] | length'` prints `0`.
- **13-AC-2** `kubectl get sc longhorn -o jsonpath='{.parameters.numberOfReplicas}|{.parameters.dataLocality}|{.reclaimPolicy}|{.allowVolumeExpansion}'` prints `1|best-effort|Retain|true` on self-hosting-minimum. `kubectl get sc -o json | jq '[.items[] | select(.metadata.annotations["storageclass.kubernetes.io/is-default-class"]=="true") | .metadata.name]'` prints `["local-path"]`.
- **13-AC-3** `kubectl -n longhorn-system get volumes.longhorn.io forgejo-data -o jsonpath='{.status.state}|{.status.robustness}|{.spec.numberOfReplicas}'` prints `attached|healthy|1`, and `kubectl get pv forgejo-data -o jsonpath='{.spec.csi.volumeHandle}|{.spec.claimRef.namespace}/{.spec.claimRef.name}'` prints `forgejo-data|forgejo/forgejo-data`.
- **13-AC-4** Node prerequisites: on every node `systemctl is-active iscsid` prints `active`, and the multipath condition of §9.1 holds (Ansible check task `longhorn-prereqs` reports `ok`). `longhornctl check preflight` (verify) reports no error (an NFS warning is allowed).
- **13-AC-5** UI not public: `kubectl get ingress,ingressroutes.traefik.io -n longhorn-system -o name` prints nothing, and no Traefik router rule references `longhorn-frontend` (`kubectl get ingressroutes.traefik.io -A -o json | jq '[.. | .services? // empty | .[] | select(.name=="longhorn-frontend")] | length'` = `0`).
- **13-AC-6** Network isolation: from a test pod in `agent-jobs`, `curl -m 5 http://longhorn-backend.longhorn-system:9500/v1` times out. (The Prometheus clause moved to 13-AC-23 at P09, GC-01.)
- **13-AC-7** The HelmRelease `longhorn` has `spec.upgrade.remediation.retries == 0` and every Longhorn image in the rendered manifests is digest-pinned (F11 report section `longhorn`).

### GATE-P09
- **13-AC-23** Prometheus target `longhorn` is `up == 1` (`count(up{job="longhorn"} == 1) >= 1`).

### GATE-P10
- **13-AC-8** `kubectl -n longhorn-system get backuptargets.longhorn.io default -o jsonpath='{.status.available}'` prints `true`, and `kubectl -n longhorn-system get recurringjobs.longhorn.io -o name` lists exactly `snap-6h`, `backup-12h`, `backup-weekly`, `trim-weekly`, `snapshot-cleanup-daily`.
- **13-AC-9** For each of `forgejo-data` and `dr-canary` (`hermes-data`: 13-AC-24 at C08, GC-01): `kubectl -n longhorn-system get backups.longhorn.io -l backup-volume=<v> -o json | jq '[.items[] | select(.status.state=="Completed")] | length'` ≥ 1 (verify label key), and the newest `status.backupCreatedAt` is < 26 h old.
- **13-AC-10** Every Longhorn alert expression in §21 and the catalogue returns at least one series in Prometheus (`count(longhorn_volume_robustness) >= 3`, `count(sc_longhorn_backup_target_available) == 1`, `count(sc_longhorn_backup_max_age_seconds) >= 3`).
- **13-AC-11** `rt-longhorn-canary` and `rt-longhorn-volume` (plan 44) have each produced a `passed` result (44-AC-6).
- **13-AC-12** Multi-Attach drill: `kubectl -n forgejo rollout restart deploy/forgejo` repeated 3 times; each time the new pod is Ready within 300 s (no `FailedAttachVolume` event older than 120 s). The drill result is recorded as `evidence/restore-tests/longhorn-attach-drill/<ts>.json`.
- **13-AC-13** Snapshot bound: 7 days after P10, `kubectl -n longhorn-system get snapshots.longhorn.io -o json | jq 'group_by(.spec.volume) | map(length) | max'` ≤ 12.
- **13-AC-14** Settings applied: `kubectl -n longhorn-system get settings.longhorn.io node-down-pod-deletion-policy replica-soft-anti-affinity default-replica-count -o jsonpath='{range .items[*]}{.metadata.name}={.value}{"\n"}{end}'` prints the §9.3 values.
- **13-AC-25** `make e2e-lab-longhorn` passes the recovery-overlay case (DR-5): with `LONGHORN_FORGEJO_DATA_BACKUP_URL` set in a fixture `cluster-vars` and only the `longhorn-volume-recovery-forgejo` component added, the rendered `forgejo-data` restores from the emulator backup and its fixture file checksum matches; a fixture backup larger than `Volume.spec.size` makes the resolver exit with `LONGHORN_BACKUP_LARGER_THAN_VOLUME`.

### GATE-C08
- **13-AC-24** `hermes-data` has a `Completed` backup younger than 26 h, using the 13-AC-9 command. (The triplet exists from P04; the AC stays at C08 because only a running Hermes puts data on it worth proving.)

### GATE-H02
- **13-AC-15** After the profile commit, Job `longhorn-system/longhorn-replica-sync-r2` has `status.succeeded == 1`, and for `forgejo-data`, `hermes-data` and `dr-canary`: `spec.numberOfReplicas == 2`, `status.robustness == healthy`, and `kubectl -n longhorn-system get replicas.longhorn.io -l longhornvolume=<v> -o jsonpath='{range .items[*]}{.spec.nodeID}{"\n"}{end}' | sort -u | wc -l` = `2`.
- **13-AC-16** `kubectl get sc longhorn -o jsonpath='{.parameters.numberOfReplicas}'` = `2`, and a newly created test PVC on `longhorn` gets 2 replicas (then deleted).
- **13-AC-17** Node-loss drill: power off the node running the Forgejo pod (provider console or `poweroff`). Within 15 minutes the Forgejo pod runs on another node with `forgejo-data` attached, and `git ls-remote` of `strange-company` returns the pre-drill `main`. After the node returns, `forgejo-data` is `healthy` with 2 replicas within 60 minutes.
- **13-AC-18** `nodeDrainPolicy` = `block-if-contains-last-replica` at small-ha, and `kubectl drain` of a node holding one of two healthy replicas succeeds.

## 24. Explicit non-goals

- Longhorn V2 (SPDK) data engine.
- RWX volumes and the NFS share manager.
- Longhorn encrypted volumes (client-side backup encryption would come with them; revisit with SI-G3-03).
- Longhorn for PGDATA, agent workspaces, CI scratch, caches or Prometheus [SPEC §27].
- Exposing the Longhorn UI (plan 15 decision).
- Longhorn system backup (`SystemBackup`) as a recovery mechanism: Git plus the recovery overlay recreate configuration; only volume backups are needed.
- Cross-cluster disaster-recovery volumes (`standby` volumes).

## 25. Open questions

- **Q-13-1** (blocking: no) Plan 18 puts Longhorn in remediation class `controller` (`strategy: rollback`). Longhorn does not support downgrade (verify), so an automatic Helm rollback after a partially applied upgrade is unsafe. Proposed: Longhorn uses `stateful-app`-like remediation (`retries: 0`), recorded in ADR-0201. Owner: plan 18.
- **Q-13-2** (blocking: no) Plan 18 §8.6 protected kinds lack `PersistentVolume` and `longhorn.io/Volume`. Proposed: add both, because deleting either loses data. Owner: plan 18.
- **Q-13-3** (blocking: no) Plan 16's Forgejo values set `persistence.create: true`, which conflicts with the stable-named, prune-protected PVC (ADR-0203, plan 18 §8.6). Proposed: `persistence.create: false` with `claimName: forgejo-data`. Owner: plan 16.
- **Q-13-4** (blocking: no) Optional UI access requires `./bootstrap shell` to publish a port. Proposed: plan 50/02 add `--publish <port>`. If not added, the UI is simply unavailable; all procedures use `kubectl`. Owner: plan 50.
- **Q-13-5** (blocking: no) Version-sensitive facts to re-verify at the pinned version: `BackupTarget` CR vs setting, `fromBackup` URL form, `restoreRequired` field, recurring-job label form, setting names (`nodeDrainPolicy` values, `snapshotMaxCount`, `concurrentReplicaRebuildPerNodeLimit`), live engine upgrade for single-replica volumes, `defaultSettings` sync behavior. All are covered by the lab suite (§22) and 13-AC-8, -9, -14. Owner: P04/P10 implementation.
- **Q-13-6** (blocking: no) `cluster-vars` additions `LONGHORN_REPLICAS`, `STORAGE_PROFILE`, `CNPG_INSTANCES` (plan 11) and per-volume `LONGHORN_<VOLUME>_BACKUP_URL` (recovery only) must be added to plan 18 §9.2. Owner: plan 18.
