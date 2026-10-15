# Procedure: Restore a single project repository

**Owning plan:** plans/44-backup.md (Forgejo side: plans/16-forgejo.md §20.3)
**SPEC references:** §31 ("recovery of a single project SHOULD not require restoring an entire Longhorn volume"), §108 (project repositories RPO ≤ 1 h, RTO ≤ 4 h), §109
**Severity / urgency:** degraded (one project affected)
**Expected duration:** 15–45 min
**Requires break-glass:** no, if a human Forgejo admin account is available; otherwise `SC-BREAK-GLASS-FORGEJO` (plan 15/16)

## When to use
- A repository was deleted, corrupted (`git fsck` errors, Forgejo reports broken refs), or had history destroyed by a force-push.
- `rt-repo-bundle` passed recently for that repository (check §Preconditions) and Forgejo itself is healthy. For a whole-Forgejo failure use `restore-forgejo.md`; for `strange-company` itself prefer the GitHub mirror for `main`, `release/*` and tags (step 9 variant), and use bundles for everything else.

## Preconditions and required material
- `./bootstrap shell` (aws, age, git, kubectl, jq).
- The backup age identity `SC-AGE-BACKUP`: from the cluster (`kubectl -n dr-tests get secret backup-age -o jsonpath='{.data.identity}' | base64 -d > /tmp/sc/backup-age.txt`, tmpfs only) or, if the cluster is unavailable, from the recovery bundle (plan 35 unlock).
- Read access to `repo-backups`: the `dr-tests-reader` key (`dr-tests/s3-dr-tests-reader`) or `admin`.
- A Forgejo account allowed to create and push to the repository (org owner or site admin).
- Freshness: `aws --endpoint-url "$S3_ENDPOINT" s3 ls "s3://${S3_BUCKET_PREFIX}-repo-backups/git/weekly/<org>/<repo>/" ` lists at least one chain.

Work directory: `mkdir -p /tmp/sc/restore && cd /tmp/sc/restore` (tmpfs inside the toolbox; nothing is written to the repository checkout).

## Safety checks before acting
1. **Never force-push over a repository that still has history newer than the backup** without saving it first: step 2 takes a safety bundle.
2. Decide the target point: the newest backup (default) or the newest backup before a destructive event (`created_at` ≤ the event time).
3. Pushing into an existing repository uses fast-forward only, unless the operator explicitly chooses §Replace history and records why.
4. Delete the decrypted identity and plaintext bundles at the end (step 11).

## Steps
1. **Find the chain and point:**
   ```bash
   B="s3://${S3_BUCKET_PREFIX}-repo-backups/git/weekly/<org>/<repo>"
   aws --endpoint-url "$S3_ENDPOINT" s3 ls "$B/" | sort          # chains c-<timestamp>/
   CH=<chain-id>
   aws --endpoint-url "$S3_ENDPOINT" s3 ls "$B/$CH/" | sort        # 0000-full…, 0001-inc…, manifest-0000.json…
   ```
   Pick the highest manifest `seq` whose `created_at` ≤ the target time:
   `aws … s3 cp "$B/$CH/manifest-<seq>.json" - | jq '{seq, created_at, refs_count: (.refs|length)}'`.
   For points older than 56 days use `git/monthly/<org>/<repo>/<YYYY-MM>/` (a single full bundle).
   Expected: a manifest exists for the chosen `seq`. Objects without a manifest are incomplete uploads; ignore them.
2. **Safety bundle of the current repository (if it still exists in Forgejo):**
   `git clone --mirror https://git.<domain>/<org>/<repo>.git current.git && git -C current.git bundle create ../pre-restore-<repo>-$(date -u +%Y%m%dT%H%M%SZ).bundle --all`
   Keep it until the restore is verified. If the repository is gone, skip.
3. **Download and check every bundle 0000…<seq>:**
   ```bash
   for s in $(seq -w 0000 <seq>); do
     M=$(aws … s3 cp "$B/$CH/manifest-$s.json" -)
     K=$(echo "$M" | jq -r .bundle_key); H=$(echo "$M" | jq -r .bundle_sha256)
     aws … s3 cp "s3://${S3_BUCKET_PREFIX}-repo-backups/$K" "$s.bundle.age"
     echo "$H  $s.bundle.age" | sha256sum -c -
   done
   ```
   Expected: every line `OK`. If a checksum fails: stop, use the previous `seq` or chain, and report per `restore-test-failure.md` §Repos `RT_CHECKSUM_MISMATCH`.
4. **Decrypt:** `for f in *.bundle.age; do age -d -i /tmp/sc/backup-age.txt -o "${f%.age}" "$f"; done`. Expected: no error.
5. **Rebuild the mirror:**
   ```bash
   git clone --mirror 0000.bundle restored.git     # the full bundle (0000-full)
   for s in $(seq -w 0001 <seq>); do
     git -C restored.git bundle verify ../$s.bundle && git -C restored.git fetch ../$s.bundle '+refs/*:refs/*'
   done
   ```
   Then apply recorded deletions in order: for each manifest, `jq -r '.deleted_refs[]?'` → `git -C restored.git update-ref -d <ref>`.
   Expected: no errors.
6. **Verify:** `git -C restored.git fsck --full --strict` succeeds, and `git -C restored.git for-each-ref --format='%(refname) %(objectname)' | sort` equals `jq -r '.refs | to_entries[] | "\(.key) \(.value)"' manifest-<seq>.json | sort` (`diff` prints nothing).
7. **Prepare the target in Forgejo:**
   - Repository deleted: recreate it with the same owner and name (`POST /api/v1/orgs/<org>/repos` with `{"name":"<repo>","private":true}` or the Forgejo UI); repository settings per `forge.yaml` defaults are re-applied by `forge apply` (plan 16). Expected: an empty repository.
   - Repository exists: go to step 8b.
8. **Push:**
   - **8a (empty target):** `git -C restored.git push --mirror https://git.<domain>/<org>/<repo>.git` (exclude `refs/pull/*`, which Forgejo manages: `git -C restored.git push https://… '+refs/heads/*:refs/heads/*' '+refs/tags/*:refs/tags/*'`).
   - **8b (existing target, §Fast-forward an existing repository):** for each branch and tag, push without force: `git -C restored.git push https://git.<domain>/<org>/<repo>.git 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'`. Rejected non-fast-forward updates mean the live ref has diverged; list them and decide per ref with the project owner (keep live, or §Replace history).
   - **§Replace history (explicit decision only):** `git push --force` for the named refs. Protected branches reject this unless an admin temporarily allows it; record the change in the incident and restore protection immediately.
   Expected: pushes succeed; protected-branch pushes may require the admin account.
8a. **LFS objects (DR-B1):** if the repository uses LFS (`.gitattributes` with `filter=lfs` at any restored ref), list the needed oids (`git -C restored.git lfs ls-files --all --long`), download each from `s3://${S3_BUCKET_PREFIX}-repo-backups/lfs/<oid>` (age-decrypt with the same identity) into `restored.git/lfs/objects/<oid[0:2]>/<oid[2:4]>/<oid>`, then `git -C restored.git lfs push --all https://git.<domain>/<org>/<repo>.git`. Expected: `git lfs fsck` on a fresh clone passes. A missing oid is reported in the evidence (that object is lost).
9. **`strange-company` variant:** fast-forward `main`, `release/*` and tags from the GitHub mirror first (`git fetch https://github.com/<owner>/strange-company.git 'refs/heads/main:refs/heads/main' …` into `restored.git`, fast-forward only), then push as in 8b. GitHub is near-zero RPO for these refs; the bundle supplies agent branches and PR refs.
10. **Metadata (optional; issues and PRs lost with a deleted repository):** the newest weekly export `repo-backups/meta/<org>/<repo>/<ts>.json.age` decrypts with the same identity and contains issues, PRs, comments, labels, milestones and releases as JSON. Re-importing it is manual (no automated importer in MVP); for full fidelity use the scratch-instance path in plan 16 §20.3 (`forgejo dump-repo`/`restore-repo`), which is advanced and optional.
11. **Clean up:** `shred -u /tmp/sc/backup-age.txt; rm -rf /tmp/sc/restore` (keep the safety bundle from step 2 elsewhere until verification passes).

## Verification
- `git ls-remote https://git.<domain>/<org>/<repo>.git | sort` lists the same heads and tags as `restored.git` (for 8a), or the fast-forwarded set (for 8b).
- A fresh `git clone` of the repository passes `git fsck`.
- Woodpecker runs CI on the default branch without errors caused by missing history (optional).
- At the next `repo-bundle-backup` run a new manifest appears for the repository (`SCRepoBundleBackupStale` quiet).

## Rollback / abort
- Before step 8 nothing in Forgejo has changed.
- After 8a/8b: restore the pre-restore state from the safety bundle of step 2 (same push method).

## Evidence to record
- Repository, chain id and `seq` used, target time, refs pushed or rejected, any forced refs with the owner's approval, and timing (RTO evidence for SPEC §108).

## Follow-ups
- Find out how the repository was lost (Forgejo audit log, `SCForgeConfigDrift`, agent `POLICY_VIOLATION` quarantine records).
- If the Runner had tasks on that repository, check them with `stuck-task-recovery.md` (plan 29).
