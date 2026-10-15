# Procedure: GitHub black start

**Owning plan:** plans/45-recovery.md (with plan 17 for the GitHub guarantees BG-1…BG-6)
**SPEC references:** §6, §31, §32, §111, §112, §113 ("GitHub black start")
**Severity / urgency:** disaster (it is the source half of total cluster loss)
**Expected duration:** 15–30 min for the GitHub part; then `total-cluster-loss.md`
**Requires break-glass:** no for a normal clone; the operator's own GitHub account for repository administration in §B/§C; the recovery bundle for §C (age-encrypted bundles)

This procedure establishes a **trusted source tree** for black start: obtaining `strange-company` from GitHub (or an alternative), proving it is the right content, and handling a GitHub that is unavailable, deleted or untrustworthy. Everything after the clone is `total-cluster-loss.md`.

## When to use

- At step 1 of `total-cluster-loss.md` (always: §A).
- `SCGitHubBlackStartTestFailed` (the weekly assertion set of plan 17 §20.2 failed) — run §A as a dry check without recovering.
- GitHub repository missing, account locked, or GitHub down during a total loss (§C).
- GitHub history rewritten or suspicious content (§B).

## Preconditions and required material

- Git and Docker/Podman on a clean machine.
- `GITHUB_MIRROR_REPO` and a read credential (the mirror token or your own account; the repository is private by default, plan 17).
- The trusted SHA (newest bundle manifest `source_commit`, last control-transfer/recovery report, or a known clone).
- For §C: the recovery bundle and passphrase (it contains `SC-AGE-BACKUP` for repository bundles, plan 44) and `bootstrap.env` S3 credentials.

## Safety checks before acting

1. Do **not** push anything to GitHub from the recovering machine. The recovering cluster pushes only after `forge-reconnect` (ADR-0554).
2. Do not "repair" GitHub history by force-push; the history rules (plan 17 BG-2) must stay.
3. If the GitHub content cannot be trusted (§B), do not seed from it.

## Steps

### §A Normal clone and validation (5–15 min)
1. Clone:
   ```sh
   git clone https://github.com/<GITHUB_MIRROR_REPO>.git strange-company && cd strange-company
   git log -1 --format='%H %cI %s'
   ```
   **Expected:** succeeds within 5 min; HEAD is on `main`.
2. Trust check (DR-16): the anchor is `refs/heads/main` of the newest `repo-backups` manifest of `strange-company/strange-company` (plaintext manifest, ≤ 30 min before the loss; `./bootstrap exec -- sc-restore-test repo-bundle --show-manifest --repo strange-company/strange-company`). `git merge-base --is-ancestor <anchor> HEAD && echo OK`, then `git log --oneline <anchor>..HEAD`. **Expected:** `OK` and no extra commits (recover would otherwise demand `accept-github-commits <n>`). Extra commits you cannot attribute to a merged Forgejo PR, or anchor not an ancestor → §B.
3. Completeness (plan 17 BG-3): these paths exist: `SPEC.md`, `plans/45-recovery.md`, `procedures/total-cluster-loss.md`, `toolbox/Containerfile`, `gitops/clusters/<cluster>/flux-system/`, `gitops/clusters/<cluster>/bootstrap-config.yaml`, `gitops/clusters/<cluster>/config/forge-init.yaml`, `infra/`. `grep -r 'filter=lfs' .gitattributes` returns nothing.
4. Offline render (no cluster): `./bootstrap exec -- make gitops-validate`. **Expected:** exit 0.
5. Secrets are encrypted: `./bootstrap exec -- sh -c 'for f in $(git ls-files "*.sops.yaml"); do yq -e ".sops.age | length >= 2" "$f" >/dev/null || echo "BAD $f"; done'` prints nothing; no `github_pat_`/`ghp_` strings in the tree (`git grep -nE 'github_pat_|ghp_[A-Za-z0-9]{30}'` empty).
6. Decryptability: `./bootstrap exec -- sc-bootstrap recovery-bundle verify --full` (prompts for the passphrase and the offline recovery identity; plain `verify` checks integrity only, DR-22) decrypts every `gitops/clusters/<cluster>/**/*.sops.yaml` with the bundled cluster key and with the recovery identity. **Expected:** `0 failures`.
7. Continue with `total-cluster-loss.md` step 2 from this clone.

### §B Untrusted or rewritten GitHub content
1. Compare with every other source you have: the newest `strange-company` bundle in `repo-backups` (§C.2), operator clones, the last recovery/control-transfer report SHA.
2. Choose the newest source whose history contains the trusted SHA and passes §A steps 3–6.
3. If GitHub's `main` is merely **behind** (mirror lag before the loss) and contains the trusted SHA: it is usable; newer commits come back from the restored Forgejo or bundles during ref reconciliation (`total-cluster-loss.md` step 7).
4. If GitHub contains commits you did not author or history is rewritten: treat as compromise (`credential-compromise.md#identity-and-forge`); seed from the best alternative source (§C) and create a new GitHub repository later (§C.5).

### §C GitHub unavailable, deleted or untrusted — alternative seed
1. **GitHub platform outage** (status page shows an incident): see §D — without a local artifact cache, black start must wait for GitHub (plan 45 S19b).
2. **Repository bundle from object storage** (plan 44 BK-09; newest within ≈ 55 min of the loss):
   ```sh
   ./bootstrap exec -- sh -c '
     aws s3 ls s3://<prefix>-repo-backups/git/weekly/strange-company/strange-company/ --recursive | sort | tail -5'
   ```
   Restore it with plan 44's authoritative algorithm (DR-24), which downloads the full and incremental chain named by the newest manifest, decrypts with `SC-AGE-BACKUP` from the recovery bundle, applies every ref including deletions (`deleted_refs`), runs `git fsck` and compares the result with the manifest:
   ```sh
   ./bootstrap exec -- sc-restore-test repo-bundle --restore-to ./strange-company --repo strange-company/strange-company
   cd strange-company && git checkout main
   ```
   **Expected:** exit 0 (`fsck ok`, refs equal the manifest); `main` contains the anchor (it *is* the anchor).
3. **Operator clone** (any up-to-date clone, e.g. a developer laptop): use it if it contains the trusted SHA and passes §A 3–6.
4. Run `total-cluster-loss.md` from this tree. `recover` warns that the source is not GitHub (`PF-16` cannot pass): accept with the typed confirmation `seed-without-github <cluster>` (records the source in the `RecoveryRecord`).
5. **Re-create the GitHub repository** after the platform is back (repository or account lost): create an empty private repository (plan 17 §14.1 settings and rulesets), set `GITHUB_MIRROR_REPO` if the name changed (a PR to `gitops/clusters/<cluster>/config/github-mirror.yaml` and `bootstrap-config.yaml`), issue a new mirror token (`rotate-forge-credentials.md` §G), and run `forge-reconnect` / `mirror recreate` so Forgejo pushes the full history. Until then `SCGitHubMirrorNotConfigured` fires (expected).

### §D GitHub platform outage (plan 45 S19b)
1. Black start depends on GitHub-hosted artifacts: the toolbox image (GHCR) or, for a local build, tool downloads from GitHub releases; k3s release assets; Flux, CNPG and plugin images on `ghcr.io`; chart repositories on `*.github.io`. A repository bundle (§C.2) replaces only the source tree.
2. If you have a working toolbox image locally (`docker image ls` shows the pinned digest) **and** the required images and charts are reachable from another registry or cache: seed from §C.2 or §C.3 with `seed-without-github <cluster>`; expect pull failures for anything not cached.
3. **From H04:** use the bootstrap capsule (§E). **Before H04, or if the capsule check is failing:** **wait** for GitHub; record the waiting time separately in the incident. This is an accepted, recorded risk (SI-43, CD-33).

### §E Black start from the bootstrap capsule (from H04; plan 45 §9.4)
1. On the clean machine, with the object-storage credentials from `bootstrap.env`: `aws s3 cp s3://<prefix>-recovery/<cluster>/capsule/LATEST.json -` names the capsule id; `aws s3 cp --recursive s3://<prefix>-recovery/<cluster>/capsule/<id>/ ./capsule/`. **Expected:** ≈ 9–10 GiB, ≈ 15 min on 100 Mbit/s.
2. `git clone ./capsule/strange-company.bundle strange-company && cd strange-company`.
3. `./bootstrap recover --from-capsule ../capsule` — it loads the toolbox from the capsule archive, unlocks the recovery bundle, verifies `capsule.json` with the bundle's copy of the trusted signing keys and every file digest, then fast-forwards `main` from the newest `repo-backups` chain (trust anchor). **Expected:** `capsule <id>: signature ok, <n>/<n> digests ok`; then continue with `total-cluster-loss.md` from step 2. Nodes import images from the capsule; charts come from the seed Bucket.
4. Finalize: if GitHub is still down, type `defer-mirror <cluster>`; the mirror stays disabled and `SCGitHubMirrorRecoveryModeTooLong` keeps notifying until §C.5 / `forge-reconnect` after GitHub returns.
5. A signature or digest failure: do not use the capsule; try the previous capsule id (kept ≥ 35 days), otherwise wait (step §D.3).

## Verification

- §A steps 2–6 pass on the chosen source.
- After the platform is recovered: GitHub `main` == Forgejo `main` (`./bootstrap exec -- sc-bootstrap mirror status` shows `in-sync`) and the weekly `restore-test-github-black-start` passes on its next run (or trigger it: `kubectl -n dr-tests create job --from=cronjob/restore-test-github-black-start manual-<ts>`).

## Rollback / abort

Nothing here mutates GitHub or the cluster; abort by deleting the local clone. Choosing a different source later is always possible before seeding.

## Evidence to record

In the recovery incident issue: the chosen source (GitHub / bundle / clone), its HEAD SHA, the trusted SHA and how it was established, and §A check outputs. The `RecoveryRecord` `sourceCommit` field records the SHA automatically.

## Follow-ups

- If §C was used: re-create GitHub (§C.5) and confirm `SCGitHubMirror*` alerts clear.
- If §B found tampering: credential compromise follow-up for every token that could write to GitHub.
