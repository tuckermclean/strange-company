# ADR-0202: Repository-level backup as age-encrypted, incremental git bundle chains written without delete rights

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 44

## Context
SPEC §31 says storage-layer backup alone is insufficient: important repositories need repository-level offsite protection, and recovering one project should not require restoring a whole Longhorn volume. SPEC §108 sets RPO ≤ 1 h and RTO ≤ 4 h for project repositories. Candidate mechanisms are remote mirrors, `git bundle`, Forgejo repository export, and object-storage archives.

## Decision
- A CronJob `repo-bundle-backup` (namespace `forgejo`) runs every 30 minutes (`7,37 * * * *`), each run bounded to 10 minutes; weekly full chains, metadata exports and attachment copies run in a separate CronJob `repo-bundle-full` (revised after blind review DR-B11, so the RPO arithmetic holds: ≤ 60 min while no alert fires). LFS objects are copied content-addressed to `lfs/<oid>` (DR-B1). It keeps a mirror cache of every repository readable by the read-only bot `sc-backup`, and uploads:
  - a **full** `git bundle --all` once a week per repository (a new *chain*), and
  - **incremental** bundles (`<changed refs> --not <previous ref tips>`) only when refs changed,
  - with a plaintext **manifest** per bundle (refs, prerequisites, ciphertext sha256), uploaded last as the completion marker.
- Bundles and weekly metadata exports (issues, PRs, comments, labels, releases as JSON) are **encrypted with age** to the public key of `SC-AGE-BACKUP`. The private key lives in `dr-tests` (restore tests) and in the recovery bundle.
- Uploads use `If-None-Match: *` with the `repo-backups` credential, which can put but not delete (ADR-0204). Retention is enforced only by bucket lifecycle: weekly chains 56 d, monthly fulls (`git/monthly/`) 400 d.
- Restore = full + ordered incrementals + recorded ref deletions, then `fsck` and a ref comparison with the manifest. It needs only S3, the age key, `git` and a Forgejo account to push to.

## Consequences
- Single-project recovery takes minutes and never touches Longhorn.
- An attacker with read access to the bucket learns repository names and ref ids, not code.
- One more secret class (`SC-AGE-BACKUP`) and one more check CronJob.
- `strange-company` is protected three ways: GitHub mirror, bundles, Longhorn backups.

## Alternatives rejected
- Forgejo pull mirrors on a second forge: a second stateful service, and no point-in-time history.
- `forgejo dump`: whole-instance, large, needs quiescing, and duplicates CNPG + Longhorn backups.
- Unencrypted bundles: code would be readable by anyone with bucket read access.
- Full bundles every run: upload volume grows with repository size × 48 per day.

## Verification
Plan 44 44-AC-2 (fresh, encrypted bundle), 44-AC-3 (no delete, no overwrite), 44-AC-5 (restore and ref comparison per repository), `make e2e-dr` (force-push, branch deletion and corruption cases).
