# Procedure: Failed Flux handoff

**Owning plan:** plans/19-seed-handoff.md (with plans/18-flux.md, plans/16-forgejo.md, plans/50-bootstrap-orchestrator.md)
**SPEC references:** §34, §35, §113 ("failed Flux handoff"), §8 (conservative around destructive operations)
**Severity / urgency:** degraded during bootstrap (no production yet). §E and §F are outage-class.
**Expected duration:** 15 min (§B, §D) to 2 h (§E, §F)
**Requires break-glass:** no for §A–§D. §E may, and §F does, use `SC-BREAK-GLASS-FORGEJO` and `SC-BREAK-GLASS-CLUSTER` (plan 15 `break-glass-access.md`).

## When to use
Pick the section from the journal error code (`./bootstrap status --json | jq '.phases[] | select(.result=="failed")'`) or the alert:

| Symptom / code | Section |
|---|---|
| `SEED_SNAPSHOT_CONFLICT`, `SEED_ARTIFACT_MISMATCH` | §A |
| `TRANSFER_FORGE_SHA_MISMATCH`, `TRANSFER_PRECONDITION:forge-outputs-scope` | §B |
| `TRANSFER_PRECONDITION:gitrepository-ready`, `TRANSFER_NOT_READY:<object>`, `TRANSFER_ROLLED_BACK` after a T3/T4 timeout | §C |
| `TRANSFER_BUCKET_REFERENCED`; alert `SCFluxSeedSourcePresent` | §D |
| `TRANSFER_UID_CHANGED`, `TRANSFER_HELM_REVISION_CHANGED` | §E (**stop and read fully**) |
| Forgejo lost after transfer and the operator chooses a temporary source while restoring it | §F |

## Preconditions and required material
- Toolbox (`./bootstrap shell`), the bootstrap kubeconfig, a clean clone at the seed commit `S`.
- The journal (`.bootstrap/` locally, or restored from `s3://<prefix>-tofu-state/<install>/journal` per plan 50).
- `transfer-pre-<S>.json` (written at T1 and uploaded to `s3://<prefix>-bootstrap-seed/<cluster>/reports/` before T2; its sha256 is in the journal) for §C and §E.
- §F only: the recovery bundle and passphrase, a GitHub clone, and the break-glass credentials.

## Safety checks before acting
1. **Never delete** a PVC, Namespace, CNPG `Cluster` or Longhorn volume in this procedure (they are prune-protected, ADR-0304). Never `helm uninstall` a seeded release.
2. **Never force-push** to Forgejo or GitHub.
3. Confirm no `./bootstrap` run is active: `./bootstrap status` shows no lock holder. Otherwise wait, or follow `bootstrap-resume.md`.
4. Record `./bootstrap exec -- flux get all -A > handoff-state-before.txt` before any change (evidence).
5. Rollback (`./bootstrap handoff-rollback`) is safe only while `Bucket/flux-system` exists: `./bootstrap exec -- kubectl -n flux-system get bucket flux-system` returns it. If it does not exist, rollback is impossible; use §D/§F.

## Steps

### §A Snapshot conflict or artifact mismatch (possible tampering)
A1. Do not resume the root. With `SEED_ARTIFACT_MISMATCH` it is still `suspend: true` and nothing has been applied. Confirm: `./bootstrap exec -- kubectl -n flux-system get kustomization flux-system -o jsonpath='{.spec.suspend}'` → `true`.

A2. List the object versions under the prefix: `./bootstrap exec -- aws s3api list-object-versions --bucket <prefix>-bootstrap-seed --prefix <cluster>/<S>/` (or the plan 14 tool). Expected: one version per key, written by this machine during U5. **More versions, or a writer you do not recognize** (provider access logs, plan 14) means a possible compromise. Stop, and follow `credential-compromise.md` (plan 35) for `SC-OBJECT-STORAGE` (`seed-write`, `seed-read`).

A3. If A2 shows a benign cause (an interrupted earlier upload by this operator with a *different* commit under the same SHA is impossible, so the usual benign cause is a manual upload during troubleshooting): never overwrite. Create a new commit `S2` (e.g. an empty commit `git commit --allow-empty -m "chore(bootstrap): reseed [<cluster>]"`) and run `./bootstrap --from-phase seed`. The new prefix `<cluster>/<S2>/` is uploaded fresh, and the Bucket prefix is repointed.
- Expected: `artifact-integrity: pass`, then `SEED COMPLETE <S2>`.
- If the mismatch recurs on a fresh prefix, the object store or the network path is altering content. Stop and open an incident.

### §B Forgejo `main` is not the expected commit
B1. `./bootstrap exec -- sc-bootstrap mirror compare-refs` (plan 17) and `git log --oneline S..<forgejo main>`.

B2. Classify, and act on the matching case:
- **(i) Forgejo `main` is a descendant of `F` with extra commits** (someone pushed). Inspect them. If they are legitimate, re-seed at the new head: fetch it locally, check `git status --porcelain` is empty, and run `./bootstrap --from-phase seed`, then `./bootstrap --from-phase flux-bootstrap`. If they are not legitimate, stop and treat it as an incident (only `sc-admin` can push in protection stage `bootstrap`).
- **(ii) Forgejo `main` is an ancestor of `F`** (the forge-init outputs push did not land). Re-run `./bootstrap --from-phase forge-init` (idempotent; plan 16 S16 fast-forwards).
- **(iii) Diverged.** Stop. Do not reset either side. Follow `restore-forgejo.md` §Diverged.
- **(iv) `forge-outputs-scope`:** `S..F` touches paths outside the forge-init output set. Find the commit (`git diff --name-only S F`). If it was a manual commit, move it to a normal PR after transfer: reset local `main` to the forge-init outputs commit, fast-forward Forgejo with `./bootstrap --from-phase forge-init`, then retry.

B3. Retry the transfer: `./bootstrap --from-phase flux-bootstrap`. Expected: T0 passes.

### §C GitRepository not Ready, or layers not Ready after the switch
C1. `./bootstrap exec -- kubectl -n flux-system get gitrepository flux-system -o yaml` and read `status.conditions`:

| Message contains | Cause | Fix |
|---|---|---|
| `knownhosts: key mismatch` / `ssh: handshake failed` | `known_hosts` in the source Secret ≠ Forgejo host key | Compare `ssh-keygen -lf` of `forgejo/forgejo-ssh-host-keys` with the `known_hosts` line in `flux-system/flux-system` (both decrypted with `sops -d` in the toolbox). Fix the committed file by a new commit, then re-seed (§B (i) flow). This is an N3-type defect; file an issue |
| `unable to authenticate` / `permission denied` | the deploy key is not registered, or is not the committed one | `./bootstrap exec -- sc-bootstrap forge verify`, then re-run `./bootstrap --from-phase forge-init` |
| `no such host` / timeout | NetworkPolicy from `flux-system` to `forgejo-ssh:22` missing (plan 36), or Forgejo SSH down | `./bootstrap exec -- kubectl -n forgejo get endpoints forgejo-ssh`; plan 36 rule |
| `repository not found` | wrong URL or repository | compare `.spec.url` with plan 18 §8.4 |

C2. If the journal shows `TRANSFER_ROLLED_BACK`, the cluster is already back on the Bucket. Confirm: `./bootstrap exec -- kubectl -n flux-system get kustomization flux-system -o jsonpath='{.spec.sourceRef.kind}'` → `Bucket`, and every seed layer is Ready at the Bucket revision. Fix the cause from C1 or C3, then retry with `./bootstrap --from-phase flux-bootstrap`.

C3. If the GitRepository is Ready but layers are not: `./bootstrap exec -- flux get kustomizations -n flux-system --status-selector ready=false`. A `BuildFailed` that did not occur under the Bucket means the steady path differs from the seed path. That is a plan 18 `seed-steady-identity` defect: run `./bootstrap exec -- make gitops-validate` on `F` and file it.

C4. If the system is **not** rolled back and you want it back on the Bucket while you investigate: `./bootstrap handoff-rollback`.
- Expected: `ROLLBACK COMPLETE`, every seed layer Ready at Bucket revision `R` within 20 min, and the UID check against `transfer-pre-<S>.json` passes, and every non-seed Kustomization is suspended with label `strange.company/suspended-by: handoff-rollback` (it is resumed by the next transfer's T0(f)).
- If the rollback itself fails twice, stop. Leave the cluster as is (Flux keeps the last applied state) and escalate with the evidence.

### §D Bucket still referenced, or still present after transfer
D1. `./bootstrap exec -- kubectl get ks -A -o json | jq -r '.items[] | select(.spec.sourceRef.kind=="Bucket") | .metadata.namespace+"/"+.metadata.name'`. Expected: empty.

D2. If a layer still references the Bucket, its object was not re-applied from Git. Check `flux get kustomization flux-system` (root): it must be Ready at `main@sha1:<F>`. Force it: `./bootstrap exec -- flux reconcile kustomization flux-system --with-source`. Re-run D1.

D3. When D1 is empty, delete the seed source (idempotent): `./bootstrap exec -- kubectl -n flux-system delete bucket flux-system --ignore-not-found` and `./bootstrap exec -- kubectl -n flux-system delete secret seed-bucket-credentials --ignore-not-found`. Then let the journal record T6 by resuming with `./bootstrap`: `flux-bootstrap` is still in progress (between T2 and T7), so the resume continues from the recorded step and completes T6–T7. Phases that already finished after the switch are frozen and are not re-run (plan 19 §17).

D4. A Bucket is needed again after T6 (Forgejo unusable during bootstrap)? Do **not** recreate it by hand. Use §F, which records the break-glass use.

### §E UID changed or Helm revision changed (possible recreation)
**Stop and assess before touching anything.** A recreated PVC or CNPG `Cluster` means possible data loss. During a first install the data is only bootstrap data, but during black start it is restored data.

E1. Read the report: `.bootstrap/runs/<run>/control-transfer-source-switch-*.json`, fields `uid_comparison.changed[]` and `helm_releases[]` with `revision_before != revision_after`.

E2. For each changed object, determine its kind:
- **Stateless objects** (Deployment, ConfigMap, Service, RBAC): Flux recreated them, and no data is at risk. Record it and continue to E4.
- **HelmRelease revision change only:** `./bootstrap exec -- helm history <release> -n <ns>`. An `upgrade` with identical chart and values means the values rendering differs between seed and steady paths (plan 18 defect). The data is unaffected if the pods are healthy.
- **PVC, CNPG `Cluster`, Longhorn volume, Namespace:** check whether data survived:
  - `kubectl -n forgejo exec deploy/forgejo -- ls /data/git/repositories/strange-company` shows the repository;
  - `kubectl -n forgejo get cluster forgejo-pg -o jsonpath='{.status.phase}'`.
  If the data is lost on a fresh install: complete with a fresh seed on a destroyed and reprovisioned cluster (`safe-destruction.md`, then `fresh-install.md`). Never "repair" in place. In black start: follow `total-cluster-loss.md` (plan 45) from the restore step.

E3. Rollback is automatic once (T2–T5). Do not run `handoff-rollback` again until E2 is understood.

E4. Before any retry, file the defect with the report attached. A retry is allowed only after a fix commit makes `make e2e-handoff` A2/A3 pass for that object class (plan 19 §22).

### §F Emergency temporary source from GitHub while Forgejo is restored (break-glass)
Use only if all of these hold: Forgejo is lost after transfer, the platform needs a configuration change before Forgejo can be restored, and the change cannot wait.

F1. Announce break-glass use (`break-glass-access.md`, cluster target). Record the start time.

F2. From a fresh clone of GitHub `main` (`G`), create a local commit with the urgent change if needed (`G'`). Run `./bootstrap exec -- sc-bootstrap seed --emergency-source --commit <G'>`. It:
- **first suspends every non-seed Kustomization** (field manager `kubectl`, label `strange.company/suspended-by: handoff-rollback`) and lists them in an `emergency` report (`control-transfer-report` `mode: emergency`, `suspended_kustomizations`);
- uploads `<cluster>/<G'>/`, creates `Bucket/flux-system` (labeled `strange.company/emergency: "true"`) and the `seed-bucket-credentials` Secret from the bundle;
- re-applies the root with the seed-mode source, path and substitutions **and `prune: false`** (SEED-14). Without `prune: false` the root would garbage-collect every post-transfer layer, and `MirrorPrune` would uninstall Authentik, Woodpecker, the Runner and monitoring.

Expected: the root is Ready at the Bucket revision, every post-transfer workload keeps running (suspended, not deleted), and `SCFluxSeedSourcePresent` fires after 2 h (intended reminder). This path is tested by the k3d case N7 (plan 19 §22); do not use it on a version where N7 has not passed.

F3. Restore Forgejo (`restore-forgejo.md`). Push `G'` to Forgejo `main` (fast-forward only, as `sc-breakglass` under the recovery push allowlist, ADR-0554). Then transfer back: `./bootstrap --from-phase flux-bootstrap --resume-after-emergency` (the only way to re-run a frozen `flux-bootstrap`). T0(f) resumes the suspended Kustomizations first. Expected: `SOURCE SWITCH COMPLETE <G'>`, `prune: true` restored on the root, and no suspended Kustomization left (`flux get all -A --status-selector suspended=true` prints nothing).

F4. Verify with §D (Bucket gone) and the Verification section below. Close the break-glass record.

## Verification
- `./bootstrap exec -- sc-bootstrap verify control-transfer` → G1–G7 `pass`.
- `./bootstrap exec -- flux get kustomizations -n flux-system` → all seed layers Ready at `main@sha1:<Forgejo main>`, source `GitRepository`.
- `./bootstrap exec -- kubectl get buckets.source.toolkit.fluxcd.io -A` → none.
- The report `source-switch` has `result: pass`, `uid_comparison.changed: []` (except stateless objects accepted in §E2, listed in the evidence).

## Rollback / abort
- Before T6: `./bootstrap handoff-rollback` (idempotent; returns to the Bucket without deleting).
- After T6: no rollback; forward fixes only (§C, §D, §F).
- Aborting the whole install: `safe-destruction.md`. It retains the seed snapshots, recovery data and the GitHub mirror.

## Evidence to record
- The journal error code and the run ID; `handoff-state-before.txt` and after.
- The `transfer-pre-<S>.json` and source-switch reports.
- For §A: the object version listing and the access-log excerpt.
- For §E: the changed-object list, data checks and the decision taken.
- For §F: break-glass start and end times, commits `G'`, and the alert history.

## Follow-ups
- Every §A, §C (C3), §E occurrence is a defect: add a negative case to `make e2e-handoff` (plan 19 §22) reproducing it.
- After §F, open a PR that brings any emergency change in `G'` through normal review if it was not already on Forgejo `main`, and run `rotate-storage-credentials.md` for `seed-read` (the credential was placed in the cluster again).
