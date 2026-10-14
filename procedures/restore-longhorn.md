# Procedure: Longhorn restore

**Owning plan:** plans/13-longhorn.md (restore tests: plan 44)
**SPEC references:** §27, §108, §109, §113 ("Longhorn restore")
**Severity / urgency:** outage for the consumer (Forgejo or Hermes) during the replacement
**Expected duration:** 20–90 min (dominated by backup download: ≈ 20 GiB at 50 MiB/s ≈ 7 min plus rebuild); RTO target ≤ 8 h [SPEC §108]
**Requires break-glass:** cluster administration if the normal kubeconfig is unavailable

## When to use
- `SCLonghornVolumeFaulted` with no salvageable replica (after `longhorn-replica-loss.md` §Salvage), or the node holding the only replica is lost (self-hosting-minimum).
- Logical damage inside `forgejo-data` or `hermes-data` that cannot be fixed by the application (for single Forgejo repositories use `restore-project-repository.md` instead; it is faster and does not touch the volume).
- Plan 16 or 48 upgrade rollback ("restore the pre-upgrade Longhorn backup").
- A PVC was deleted by mistake: §Rebind.
- A Longhorn chart upgrade failed: §Upgrade failure.
- File-level recovery (one lost file): §Scratch restore.

## Preconditions and required material
- `./bootstrap shell` at a checkout of `strange-company` `main` (kubectl, flux, jq, yq).
- Backup target available: `kubectl -n longhorn-system get backuptargets.longhorn.io default -o jsonpath='{.status.available}'` = `true`. If not, see `object-storage-outage.md` first.
- Volume names and consumers:

| Volume | Consumer namespace / workload | Layer holding the triplet | Consumer layer |
|---|---|---|---|
| `forgejo-data` | `forgejo` / `deploy/forgejo` | `longhorn-config` | `forgejo` |
| `hermes-data` | `hermes` / Hermes Deployment (plan 24) | `strange-company` | `strange-company` |
| `dr-canary` | `dr-tests` / `dr-canary-writer` CronJob | `backups` | `backups` |

## Safety checks before acting
1. Confirm which backup to restore and its time: a restore loses everything written after `backupCreatedAt`. For Forgejo, repositories pushed after that time are recovered afterwards from bundles and GitHub (step 10).
2. If the damaged volume is still readable, take a forensic backup first (step 4). Do not skip it unless the volume is faulted.
3. Suspend Flux for the affected layers **before** deleting anything. Otherwise Flux re-creates an empty `Volume` with the same name (`ssa: IfNotPresent` creates when absent) and races the restore.
4. Do not delete Longhorn `Backup` objects or anything in the `longhorn-backups` bucket.

## Steps

### §Replace (restore an important volume in place)
1. **Pick the backup:**
   ```bash
   kubectl -n longhorn-system get backups.longhorn.io -l backup-volume=<volume> \
     --sort-by=.status.backupCreatedAt \
     -o custom-columns=NAME:.metadata.name,CREATED:.status.backupCreatedAt,STATE:.status.state,URL:.status.url
   ```
   Expected: at least one `Completed` row. Note the `URL` of the chosen backup (verify field name; the backup URL has the form `s3://<bucket>@<region>/?backup=<backup-name>&volume=<volume>`).
   If the list is empty but the bucket has backups (fresh cluster, black start): wait for the backup target poll (5 min) or `kubectl -n longhorn-system annotate backuptargets.longhorn.io default sc/sync="$(date +%s)" --overwrite` and re-list.
2. **Suspend Flux:** `flux suspend kustomization <triplet-layer> <consumer-layer> -n flux-system`.
3. **Stop the consumer:** `kubectl -n <ns> scale deploy/<workload> --replicas=0`. Expected: no pod uses the PVC (command in `longhorn-replica-loss.md` §Stuck attachment step 1) and the volume becomes `detached`.
4. **Forensic backup (if the volume is readable):** apply a Longhorn `Snapshot` and then a `Backup` for it (CR kinds `snapshots.longhorn.io` and `backups.longhorn.io`, verify field names at the pinned version):
   ```bash
   kubectl -n longhorn-system apply -f - <<EOF
   apiVersion: longhorn.io/v1beta2
   kind: Snapshot
   metadata: {name: <volume>-forensic-$(date -u +%Y%m%d%H%M), namespace: longhorn-system}
   spec: {volume: <volume>, createSnapshot: true}
   EOF
   ```
   then a `Backup` with `spec.snapshotName` set to that snapshot. Expected: backup state `Completed`. Detached volumes are attached automatically for the backup (`allowRecurringJobWhileVolumeDetached` covers recurring jobs; for manual backups, verify, otherwise attach in maintenance mode through the UI).
5. **Delete the damaged triplet:**
   ```bash
   kubectl -n <ns> delete pvc <volume>
   kubectl delete pv <volume>
   kubectl -n longhorn-system delete volumes.longhorn.io <volume>
   ```
   Expected: all three gone within 2 minutes (`kubectl -n longhorn-system get volumes.longhorn.io <volume>` → NotFound).
6. **Create the restored triplet** with the same names, labels and sizes as in Git, plus `fromBackup`. Render the component used by black start (plan 13 §9.7) so the manifests are identical to Git:
   ```bash
   export LONGHORN_<VOLUME>_BACKUP_URL='<URL from step 1>'
   sc-gitops render --cluster <cluster-name> --component longhorn-volume-recovery --out out/
   yq 'select(.metadata.name == "<volume>")' out/<cluster-name>/<triplet-layer>.yaml | kubectl apply --server-side --field-manager=kustomize-controller -f -
   ```
   (If `sc-gitops` cannot render a single component at the pinned version, copy the triplet from `gitops/platform/longhorn/volumes/<volume>.yaml`, add `spec.fromBackup: <URL>` to the `Volume`, and apply it.)
   Expected: the `Volume` exists with `spec.fromBackup` set and starts restoring (`status.restoreRequired: true`, verify field), PV and PVC exist, PVC `Bound`.
7. **Wait for the restore:** poll `kubectl -n longhorn-system get volumes.longhorn.io <volume> -o jsonpath='{.status.state}/{.status.restoreRequired}/{.status.robustness}'` every minute.
   Expected: `detached/false/…` within (size / 50 MiB/s) + 10 min. At small-ha Longhorn then rebuilds the second replica after the first attach.
   If the restore fails (`status.conditions` Restore=False): check backup target errors (`kubectl -n longhorn-system describe backupvolumes.longhorn.io <volume>`), choose the previous backup and repeat from step 5.
8. **Resume Flux:** `flux resume kustomization <triplet-layer> <consumer-layer> -n flux-system`. Expected: both Ready; the `Volume` is not modified by Flux (it already exists).
9. **Consumer checks:** Forgejo `/api/healthz` passes and `forgejo doctor check --all` (verify) reports no repository errors; Hermes starts and its health endpoint answers.
10. **Forgejo only — bring repositories forward to the present:** for every repository whose newest bundle (plan 44) is newer than the restored backup, fetch the missing refs (fast-forward only) as in `restore-project-repository.md` §Fast-forward an existing repository. For `strange-company`, fast-forward from GitHub (`main`, `release/*`, tags).

### §Empty stable volume (recreate empty stable volume; no usable backup)
Used by `restore-forgejo.md` (then bundle rehydrate, plan 44 §9.2.6 / plan 16 §20.1) and `hermes-failure.md` (Hermes reset). Data on the old volume is given up deliberately, so record that decision first.
1. **Suspend Flux and stop the consumer:** steps 2–3 of §Replace. Expected: the volume is `detached` (or `faulted`/missing).
2. **Delete what remains of the triplet:** step 5 of §Replace (skip objects that no longer exist). If the volume is still readable and might be needed later, take the forensic backup of §Replace step 4 first.
3. **Make sure no recovery component would restore it:** `gitops/clusters/<cluster-name>/kustomization.yaml` must not list `longhorn-volume-recovery-forgejo` / `-hermes` for this volume (plan 13 §9.7). If it does (an unfinished recovery), remove it by PR or direct apply first.
4. **Recreate empty from Git:** `flux resume kustomization <triplet-layer> -n flux-system`, then `flux reconcile kustomization <triplet-layer> --with-source`. Because the `Volume` is applied with `ssa: IfNotPresent`, Flux creates it fresh and empty (size, replicas and labels from Git), together with its PV and PVC.
   Expected within 5 min: `kubectl -n longhorn-system get volumes.longhorn.io <volume> -o jsonpath='{.status.state}/{.status.robustness}'` = `detached/unknown` or `detached/healthy`; `kubectl -n <ns> get pvc <volume> -o jsonpath='{.status.phase}'` = `Bound`. The filesystem is created on first attach.
5. **Repopulate before the consumer starts:**
   - `forgejo-data`: keep the `forgejo` layer suspended and run the bundle rehydrate Job (`restore-forgejo.md`; plan 44 §9.2.6). Expected: the rehydrate result is `passed`.
   - `hermes-data`: nothing to repopulate (Hermes data is non-authoritative, SPEC §43; managed configuration comes from Git).
6. **Resume the consumer layer:** `flux resume kustomization <consumer-layer> -n flux-system`. Expected: the consumer is Ready, and the next `backup-12h` run produces a backup of the new volume.

### §Scratch restore (file-level recovery, no production change)
1. Create a scratch volume from the chosen backup (name must start with `rt-`; the restore-tester VAP also allows humans):
   `Volume rt-<volume>-<date>` with `fromBackup: <URL>`, `numberOfReplicas: 1`, then PV/PVC `rt-<volume>-<date>` in `dr-tests` with class `longhorn-scratch`.
2. Mount it read-only in a pod (`busybox`-free restricted pod using the `restore-tester` image), then `kubectl -n dr-tests cp rt-pod:/restore/<path> ./<path>`.
3. Delete the pod, PVC, PV and `Volume`.

### §Revert to snapshot (logical damage within the last 24 h)
Local snapshots (`snap-6h`, retain 4) are faster than a backup restore but require attaching the volume in maintenance mode, which Longhorn exposes through its UI (port-forward per plan 13 §7.4) (verify a CR-based method at the pinned version). If the UI path is not available, use §Replace with the newest backup taken before the damage.

### §Rebind (PVC deleted by mistake; PV `Released`, data intact)
1. `kubectl get pv <volume> -o jsonpath='{.status.phase}'` = `Released`.
2. Clear the old claim reference:
   `kubectl patch pv <volume> --type json -p '[{"op":"remove","path":"/spec/claimRef/uid"},{"op":"remove","path":"/spec/claimRef/resourceVersion"}]'`
3. Re-create the PVC from Git: `flux reconcile kustomization <triplet-layer>` (or apply the PVC manifest).
Expected: PV `Bound` to the new PVC; the consumer starts with its data.

### §Upgrade failure (Longhorn chart)
1. Do not roll back the chart (13-R-4, ADR-0201). `kubectl -n longhorn-system get pods -o custom-columns=NAME:.metadata.name,IMAGE:.spec.containers[0].image,STATUS:.status.phase` shows which components run which version.
2. Read the pre-upgrade checker and manager logs; compare with the Longhorn release notes for the target version.
3. Fix forward (a corrected values PR, or the next patch release). Volumes keep serving I/O through running engines during a stalled manager upgrade.
4. If the managers cannot run at either version and volumes become unavailable: escalate. The last resort is a fresh Longhorn installation plus §Replace for every important volume from the pre-upgrade backups required by plan 13 §19; this is a planned outage decided by a human.

## Verification
- `kubectl -n longhorn-system get volumes.longhorn.io <volume> -o jsonpath='{.status.robustness}'` = `healthy` (after rebuild at small-ha).
- Consumer health checks pass (step 9).
- A backup of the restored volume completes at the next `backup-12h` run (or trigger one) and `SCLonghornBackupStale` stays quiet.
- Run `kubectl -n dr-tests create job --from=cronjob/restore-test-longhorn rt-manual-$(date +%s)`; expect `passed`.

## Rollback / abort
- Before step 5 nothing has changed: resume the layers and scale the consumer up.
- After step 5, if the restore fails with every available backup: Forgejo can start on an empty volume and rebuild all repositories from bundles and GitHub (`restore-forgejo.md`, plan 16 §20.1); Hermes can start empty (its data is non-authoritative, SPEC §43).

## Evidence to record
- Volume, chosen backup name and `backupCreatedAt`, forensic backup name, timestamps of steps 3 and 9 (consumer downtime), restore duration, restore-test result id.

## Follow-ups
- After a replacement, the next `backup-12h` runs (retain 14) prune this volume's pre-restore backups within 7 days. If they may be needed for forensics or rollback, raise the job's retain to 60 by PR for 30 days before the next run (plan 13 §9.7, DR-31a).
- Root-cause the fault (disk, node, upgrade).
- For Forgejo, verify mirror and bundle jobs resumed (`SCGitHubMirrorFailing`, `SCRepoBundleBackupStale` quiet).
