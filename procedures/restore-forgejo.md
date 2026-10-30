# Procedure: Forgejo restore

**Owning plan:** plans/16-forgejo.md (with 12 for CNPG mechanics, 13 for Longhorn, 17 for the GitHub mirror, 44 for bundles)
**SPEC references:** §30, §31, §32, §108, §109, §113 ("Forgejo restore"), §124
**Severity / urgency:** outage (whole instance) · degraded (single project, Multi-Attach)
**Expected duration:** triage 15 min; Multi-Attach 15–30 min; single project 20–60 min; whole instance 1.5–3 h (RTO target 4 h; `strange-company` ≤ 2 h)
**Requires break-glass:** cluster break-glass only if the operator kubeconfig is unavailable (`break-glass-access.md` §Cluster). Forgejo break-glass (`sc-breakglass`) only if `sc-admin` is unusable.

Alert names are the catalogue names in `contracts/alert-catalog.yaml` (CD-01).

## When to use
- `SCForgejoDown`, `SCForgejoSSHDown`, `SCForgejoPodStuckPending`, `SCForgejoSSHHostKeyMismatch`, `SCFluxSourceFailing` caused by Forgejo.
- `SCRestoreTestReposFailed` / `SCRestoreTestReposFailed` for a repository, or a human report of a corrupted or deleted repository.
- Logical damage in Forgejo data (deleted issues/PRs/users, bad admin action) needing point-in-time recovery.
- An upgrade whose migration failed (§Upgrade rollback).
- forge-init stopped with `FORGE_MAIN_DIVERGED` (§Diverged).

Do **not** use for: Authentik login problems with Forgejo otherwise healthy (`authentik-outage.md`); GitHub mirror problems with Forgejo healthy (`github-mirror-failure.md`); total cluster loss (`total-cluster-loss.md`, plan 45).

## Preconditions and required material
- Toolbox: `./bootstrap shell` or `./bootstrap exec -- <cmd>` with the operator kubeconfig (`SC-BOOTSTRAP-KUBECONFIG`).
- `SC-FORGEJO-ADMIN` (`sc-admin` password): after `forge finalize` the valid value is the **bundle** one (`./bootstrap exec -- sc-bootstrap bundle open --show SC-FORGEJO-ADMIN`); the SOPS file only holds the seed value used when Forgejo starts on an empty DB (AUTH-9, plan 16 §14.3). For routine admin API calls prefer a short-lived token: `./bootstrap exec -- kubectl -n forgejo exec deploy/forgejo -- forgejo admin user generate-access-token --username sc-admin --token-name sc-op-<ts> --scopes write:admin,write:repository` (delete it at the end).
- For whole-instance restore: S3 reachable (`probe_success{probe="object-storage"}` = 1), the `forgejo-pg` backups (plan 12) and Longhorn backups of `forgejo-data` (plan 13) and bundles in `<prefix>-repo-backups` (plan 44).
- For repository data from bundles: the backup age identity `SC-AGE-BACKUP` and read access to `repo-backups`, as listed in `restore-project-repository.md` §Preconditions.
- A local clone of GitHub `main` (`git clone https://github.com/<GITHUB_MIRROR_REPO>.git`) for `strange-company` ref reconciliation.

## Safety checks before acting
1. **Never** force-push, delete a repository, or regenerate `SECRET_KEY` or the SSH host key during any step. Both come from Git (ADR-0252). A Forgejo started with a different `SECRET_KEY` cannot decrypt push-mirror credentials and 2FA secrets.
2. **Never** force-detach a Longhorn volume while the old pod might still be running on a reachable node (risk of corruption).
3. Before any destructive DB action, take an on-demand backup of the current state (plan 12 `restore-cnpg.md` step 1) so the "bad" state stays recoverable.
4. Pause autonomous work so no worker pushes during restore: `./bootstrap exec -- sc-bootstrap estop set --level workers --reason "forgejo restore"` (plan 33 CLI). Record the time.
5. Blast radius: Flux keeps the last applied state while Forgejo is down (no new deploys). The Runner, Woodpecker and Renovate stall. Nothing is lost except in-flight webhooks and mirror syncs.

## Steps

### §Triage (always first; read-only)
- **T1** `./bootstrap exec -- kubectl -n forgejo get pods,pvc,hr -o wide` and `./bootstrap exec -- kubectl -n forgejo describe pod -l app.kubernetes.io/name=forgejo | tail -40`.
  Expected: one `forgejo` pod. Branch by what you see:
  - Pod `ContainerCreating` with event `Multi-Attach error` or `FailedAttachVolume` → §Multi-Attach.
  - Pod `CrashLoopBackOff` with DB errors (`dial tcp … 5432`, `password authentication failed`) → check `./bootstrap exec -- kubectl -n forgejo get cluster forgejo-pg`; if not healthy → plan 12 `cnpg-primary-loss.md`, then return here at T3.
  - Pod `CrashLoopBackOff` with migration errors right after a version change → §Upgrade rollback.
  - Pod Running and Ready but SSH probe failing → T2.
  - HelmRelease `UpgradeFailed` → `./bootstrap exec -- flux -n forgejo get hr forgejo` and `kubectl -n forgejo describe hr forgejo`; a values/schema error is fixed by a revert PR (Forgejo HTTP may still be up).
- **T2** SSH: `./bootstrap exec -- kubectl run sshcheck --rm -i --restart=Never -n flux-system --image=<toolbox digest> -- ssh-keyscan -t ed25519 -p 22 forgejo-ssh.forgejo.svc.cluster.local`.
  Expected: one `ssh-ed25519` line equal to the public half of `SC-FORGEJO-SSH-HOST-KEY`. Different key → `rotate-forge-credentials.md` §E (host key) — the Secret mount is wrong, do not "accept" the new key. No answer → check `kubectl -n forgejo logs deploy/forgejo | grep -i ssh` for `SSH_SERVER_HOST_KEYS` errors and Service endpoints `kubectl -n forgejo get endpoints forgejo-ssh`.
- **T3** Health: `./bootstrap exec -- kubectl -n forgejo exec deploy/forgejo -- curl -fsS http://localhost:3000/api/healthz`. Expected `"status":"pass"`. Record which component check fails (`database`, `cache`).
- **T4** Data check (if the instance is up but data is suspected wrong): list affected repositories and the earliest time the damage existed (target time `T` for PITR = just before it, UTC).

### §Multi-Attach (lesson L-5; ~15–30 min)
- **MA1** Identify the volume: `./bootstrap exec -- kubectl -n forgejo get pvc forgejo-data -o jsonpath='{.spec.volumeName}'` → `<pv>`. `./bootstrap exec -- kubectl get volumeattachments -o wide | grep <pv>` → note the node `N_old`.
- **MA2** Confirm no old Forgejo pod is running: `./bootstrap exec -- kubectl get pods -A -o wide --field-selector spec.nodeName=<N_old> | grep forgejo`. Expected: none, or only `Terminating`.
  - If `N_old` is NotReady/unreachable: confirm through the provider console or `ssh` that the node is really down or the kubelet is stopped (`break-glass-access.md` §Cluster). Only then continue.
  - If an old pod is still `Running`: delete it normally (`kubectl -n forgejo delete pod <old>`) and wait 2 min. `Recreate` should prevent this; if it happens, record it.
- **MA3** Wait for Longhorn to detach (up to 6 min): `./bootstrap exec -- kubectl -n longhorn-system get volumes.longhorn.io <pv> -o jsonpath='{.status.state}'` → `detached`. The new pod then attaches on its own; done when T3 passes.
- **MA4** Still attached after 6 min with the old node confirmed down: `./bootstrap exec -- kubectl delete volumeattachment <va-name>`. Then delete the stuck pod so it is recreated: `./bootstrap exec -- kubectl -n forgejo delete pod -l app.kubernetes.io/name=forgejo`. Expected: pod Running within 5 min and T3 passes.
- **MA5** If the Longhorn volume is `faulted`: go to plan 13 `restore-longhorn.md`, then §Whole instance steps W4 (rehydrate if the volume comes back empty) and W5.

### §Single project (repository damaged or deleted; SPEC §31; no Longhorn restore)
- **SP1** Follow `restore-project-repository.md` (plan 44) for the Git data. It locates the chain under `s3://<prefix>-repo-backups/git/weekly/<org>/<repo>/<chain-id>/` (age-encrypted `…-full|inc-<ts>.bundle.age` objects plus `manifest-<seq>.json`; monthly fulls under `git/monthly/`), decrypts with `SC-AGE-BACKUP` into tmpfs, rebuilds a scratch mirror at the chosen point, takes a safety bundle of the current repository, re-creates the repository if absent (`$FJ POST /orgs/<org>/repos` with settings from `forge.yaml`, `$FJ` = `./bootstrap exec -- sc-bootstrap forge api`), and pushes (`--mirror` only into an empty repository; otherwise fast-forward per branch). Expected: that procedure's verification passes.
- **SP2** Diverged branches reported by that procedure (Forgejo head not an ancestor of the backup head): stop and decide with the repository owner; a non-fast-forward correction of a protected branch is a human-reviewed action recorded in the evidence.
- **SP3** `strange-company` only: GitHub holds `main`, `release/*` and tags (plan 17). Prefer it when newer than the bundle: `git fetch https://github.com/<repo>.git main` and fast-forward per SP2's rules.
- **SP4** Metadata (issues/PRs) lost with a deleted repository: optional advanced path (plan 16 §20.3): scratch PITR of `forgejo-pg` into `dr-tests` per `restore-cnpg.md` §Scratch, scratch Forgejo with `OFFLINE_MODE=true`, `[mirror] ENABLED=false`, `[cron] ENABLED=false`, webhooks blocked, then `forgejo dump-repo` / `forgejo restore-repo` (verify both commands on the running version). Skip if the owner accepts metadata loss; record the decision.
- **SP5** Re-apply forge configuration for the repository: `./bootstrap exec -- sc-bootstrap forge apply --dry-run` then without `--dry-run` (deploy keys, protection, webhooks). Expected: diff only for the restored repository.

### §Whole instance (DB PITR + repository data; RTO ≤ 4 h)
- **W1** Suspend Flux for Forgejo so it does not fight the restore: `./bootstrap exec -- flux -n flux-system suspend kustomization forgejo` and `./bootstrap exec -- flux -n forgejo suspend hr forgejo`. Scale down: `./bootstrap exec -- kubectl -n forgejo scale deploy/forgejo --replicas=0`. Expected: no Forgejo pod.
- **W2** Record the `SECRET_KEY` hash for later comparison: `./bootstrap exec -- sc-bootstrap forge key-hash --source git` (prints sha256 of the decrypted `SECRET_KEY` from `forgejo-app-secrets.sops.yaml`; never the value). Store the hash in the evidence file only.
- **W3** Restore `forgejo-pg` to target `T` (or "latest" after disk/node loss) following plan 12 `restore-cnpg.md` (verify side by side as `forgejo-pg-pitr`, then re-create by name with `cnpg-bootstrap-recovery`, `SOURCE_GEN=N`, `GEN=N+1`). Because Forgejo is down, use the restore-cnpg "direct apply plus follow-up commit" path. Expected: `./bootstrap exec -- kubectl -n forgejo get cluster forgejo-pg -o jsonpath='{.status.phase}'` = `Cluster in healthy state`, and archiving into `g<N+1>` (`ContinuousArchiving=True`).
- **W4** Repository data:
  - PVC healthy and newer than `T` → keep it (repositories are append-mostly; W5 reconciles).
  - PVC lost or corrupt → restore `forgejo-data` from the latest Longhorn backup before `T` via plan 13 `restore-longhorn.md` (restore to a PVC named `forgejo-data`). If no Longhorn backup is usable: re-create the stable-named empty volume through plan 13 (`restore-longhorn.md` §Empty stable volume; the chart has `persistence.create: false` and never creates it, DR-22), then **rehydrate from bundles** before starting Forgejo (STO-9): with the HelmRelease still suspended, `./bootstrap exec -- kubectl -n forgejo create job --from=cronjob/forgejo-rehydrate forgejo-rehydrate-<ts>` (plan 16 §20.1; the CronJob is suspended by default and exists only as a template). It rebuilds every `<owner>/<name>.git` present in the restored DB from its newest bundle chain and writes a ref report. Expected: Job `Complete`, report shows every DB repository `restored` or `no-backup` (the latter listed for the evidence). After W5 starts Forgejo, run `forgejo admin regenerate hooks` and `forgejo admin regenerate keys` (verify subcommands) via `kubectl exec` before `doctor check`, so server-side hooks (protection, webhooks) are back.
- **W5** Start Forgejo: `./bootstrap exec -- flux -n forgejo resume hr forgejo` and `./bootstrap exec -- flux -n flux-system resume kustomization forgejo`. Wait for T3 to pass (≤ 15 min). Then reconcile Git data with the DB: `./bootstrap exec -- kubectl -n forgejo exec deploy/forgejo -- forgejo doctor check --all` (verify subcommand); record warnings. For each repository whose newest bundle-chain manifest (`refs` map) is newer than disk: restore the chain to a scratch mirror per `restore-project-repository.md` and fast-forward push (SP2 rules). Missing repositories: SP1.
- **W6** `strange-company`: run `./bootstrap exec -- sc-bootstrap mirror compare-refs`. For each of `main`, `release/*`: if Forgejo is an ancestor of GitHub, fast-forward Forgejo from GitHub (`git push <forgejo> <github sha>:refs/heads/<b>` as `sc-admin`; protection stage `steady` requires a temporary push-allowlist entry for `sc-admin`: `sc-bootstrap forge apply --stage bootstrap`, then back to `steady` in W8). If diverged → §Diverged.
- **W7 (step R9 — credentials encrypted with `SECRET_KEY`)** Verify `SECRET_KEY`: `./bootstrap exec -- sc-bootstrap forge key-hash --source pod` (sha256 of the value in the rendered `app.ini`) equals the W2 hash. Mismatch → stop: the Secret mount is wrong; fix the SOPS Secret delivery (Flux) before anything else. Then check the push mirror: `./bootstrap exec -- sc-bootstrap mirror status`. If `last_error` reports authentication/decrypt failure → `github-mirror-failure.md` §D (Recreate). Users whose 2FA secrets cannot be decrypted re-enrol (only possible if the key changed historically; record).
- **W8** Re-run forge-init in recover mode: `./bootstrap exec -- sc-bootstrap forge init --recover`. Expected: every step `verified` or `updated`, none `failed`; tokens that no longer validate are re-minted and their SOPS files change → commit and push through a PR (`FORGE_OUTPUTS_REQUIRE_PR` path) and merge after review. Then `./bootstrap exec -- sc-bootstrap forge apply` (stage `steady`).
- **W9** Flux: `./bootstrap exec -- flux reconcile source git flux-system -n flux-system` → Ready at Forgejo `main`. Agent branches lost between `T` and the incident are treated by the Runner as infrastructure failures (plan 26).
- **W10** Resume workers: `./bootstrap exec -- sc-bootstrap estop set --level none`.

### §Diverged (`FORGE_MAIN_DIVERGED` at forge-init, or Forgejo `main` diverged from GitHub `main`)
- **D1** `./bootstrap exec -- sc-bootstrap mirror compare-refs` and `git log --oneline --graph <forgejo main> <github main> -20`. Identify the commits unique to each side and their authors/merge PRs.
- **D2** Decide which side is authoritative (normally Forgejo: it is the only place merges happen after control transfer). Unique GitHub commits mean someone bypassed the read-only mirror (`github-mirror-failure.md` §C) or Forgejo was restored older than GitHub (then GitHub is authoritative: fast-forward Forgejo if possible).
- **D3** Never force-push either side automatically. Bring missing commits in by a normal PR on Forgejo (cherry-pick or merge branch `restore/<date>`), reviewed by a human. GitHub cannot be force-pushed (ruleset `sc-history`); after the PR merges the mirror fast-forwards GitHub.
- **D4** Record the decision and the commit lists in the evidence.

### §Upgrade rollback (failed Forgejo upgrade; plan 16 §19)
- **U1** Find the pre-upgrade backup evidence: `evidence/backups/forgejo/<ts>.json` named in the upgrade PR (`/backup-evidence <ts>`). It names the CNPG backup, the Longhorn backup and the bundle set.
- **U2** Revert the version PR on Forgejo if Forgejo is still serving; otherwise prepare the revert commit locally and apply it after W5 via `forge-init --recover` push rules (sc-admin temporary allowlist), recording it.
- **U3** Run §Whole instance with `T` = the CNPG backup's `stoppedAt` time (W3) and the named Longhorn backup (W4), then the pre-upgrade image (from the reverted values). Forgejo migrations are forward-only, so restoring the DB is mandatory; Helm rollback alone is not enough.

## Verification
1. T3 healthz `pass` in-cluster, and `./bootstrap exec -- curl -fsS https://git.<BASE_DOMAIN>/api/healthz` = 200.
2. SSH: T2 shows the pinned key; `./bootstrap exec -- flux get sources git -n flux-system` → `flux-system` Ready at Forgejo `main`.
3. `./bootstrap exec -- sc-bootstrap forge verify` exits 0 (no drift).
4. `./bootstrap exec -- sc-bootstrap mirror verify` exits 0 (`strange-company` mirrored, `main` equal on both sides).
5. For a single-project restore: `restore-project-repository.md` verification passes (`git ls-remote` heads equal to, or descendants of, the manifest `refs`).
6. `./bootstrap exec -- kubectl -n forgejo get cluster forgejo-pg -o jsonpath='{.status.conditions[?(@.type=="ContinuousArchiving")].status}'` = `True`, and a new base backup completes within 24 h (or trigger one per plan 12).
7. No `SCForgejo*`, `SCForgeConfigDrift` or `SCGitHubMirror*` alert firing 30 min after completion.

## Rollback / abort
- Before W3's re-creation step, abort by resuming the HelmRelease and scaling back to 1: no data changed.
- After W3, the previous DB state remains recoverable from generation `g<N>` (on-demand backup taken in the safety checks). Restoring "back" is another run of W3 with that target.
- Single-project pushes are fast-forward only and therefore never destroy refs; abort leaves the repository with at least the refs it had.

## Evidence to record
Write `s3://<prefix>-evidence/restores/forgejo/<UTC ts>.json` (and attach to the incident issue): trigger alert/report, chosen target time `T`, backups used (CNPG backup name, Longhorn backup name, bundle chain IDs and manifest sequence numbers), `SECRET_KEY` hash comparison result, `compare-refs` output before and after, forge-init `--recover` journal record, every manual decision (diverged refs, metadata loss acceptance), start/end times (measured RTO), and the commit SHA of any follow-up PR.

## Follow-ups
- If the cause was L-5 (Multi-Attach), add the occurrence to the plan 13 issue tracking detach lag.
- Run the plan 44 Forgejo restore test on demand (RT-7 `rt-app-forgejo` and `rt-repo-bundle`, plan 44: `./bootstrap exec -- kubectl -n dr-tests create job --from=cronjob/<cronjob> <name>-manual-<ts>` with the CronJob names in plan 44 §RT) within 7 days to re-establish confidence.
- Review RPO actually achieved against SPEC §108 and report gaps to plan 44.
