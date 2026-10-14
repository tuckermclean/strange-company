# ADR-0253: GitHub mirror = Forgejo push mirror with a repository-scoped fine-grained PAT, a ref filter, and a GitHub repository that only the mirror can write

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 17

## Context
SPEC §31–§32 require `strange-company` to mirror continuously from Forgejo to GitHub for black start, with failures visible and with Forgejo never depending on GitHub. Lesson L-6 prefers GitHub App installation tokens over PATs for automation. ADR-0018 keeps `.github/workflows/**` on `main`. SI-11 notes that a push mirror force-overwrites GitHub. ADR-0554 notes that a restored, older Forgejo could overwrite a newer GitHub. Agent branches are pushed by worker deploy keys, and Forgejo cannot restrict which refs a deploy key creates (SI-06).

## Decision
- **Mechanism:** a native Forgejo **push mirror** on `strange-company/strange-company` over HTTPS, `sync_on_commit: true`, interval `10m0s`, declared in `forge/forge.yaml` and created by forge-init (step S17) or `forge apply`.
- **Ref filter:** `branch_filter: "main,release/*"`. Forgejo always mirrors tags as well. Agent, Renovate, PR, probe and upstream-sync refs never leave the cluster. Requires a Forgejo version with push-mirror `branch_filter` (upstream since July 2025).
- **Credential:** `SC-GITHUB-MIRROR` is a **fine-grained PAT** of a dedicated machine user, repository access limited to the one repository, permissions Contents R/W + Workflows R/W + Metadata R, expiry ≤ 180 days. A GitHub App token is rejected because installation tokens expire hourly and a push mirror stores a static credential with no refresh hook. The Workflows permission is unavoidable while `.github/workflows/**` is on `main`. Branch filters cannot exclude paths.
- **Read-only GitHub:** private repository; a ruleset with **no bypass** blocks non-fast-forward updates and deletions on `main`, `release/*` and a canary namespace; a second ruleset restricts every ref creation, update and deletion to a team containing only the mirror user. GitHub Actions run with read-only tokens and no secrets. Issues and wiki are disabled. No Strange Company credential holds GitHub administration write.
- **Observability:** Forgejo's `last_update`/`last_error` plus an independent comparison of GitHub `main` with Forgejo `main` (read-only probe token `SC-GITHUB-PROBE` with Contents/Metadata/Administration/Actions read, never the mirror token; the restore test uses a separate `SC-GITHUB-MIRROR-READ`), exported by the `platform-probes` module `github-mirror`.

## Consequences
- GitHub can only ever fast-forward `main` and `release/*`. A restored-older or misconfigured Forgejo produces a visible mirror failure instead of destroying the recovery seed.
- Tags are mirrored unconditionally, including the Runner's `sc-archive/*` and `sc-quarantine/*` tags (plan 55), so those agent commits are reachable on the private GitHub repository (no secret content is ever tagged). A rogue tag pushed by a worker key can appear briefly on the private GitHub repository until the Runner quarantines it and the next sync prunes it (residual risk recorded for plan 34).
- Rulesets on private repositories require a paid GitHub plan (verify). Installations without it run with tier `unavailable-accepted`: weaker GitHub-side guarantees, compensated by divergence alerts and recovery-mode mirror disablement.
- The PAT needs manual rotation at least every 180 days, with an alert 21 days before expiry.
- The rulesets block rewrites, not appends: a leaked mirror PAT can add commits on top of `main`. Black start therefore does not trust GitHub `main` blindly; `recover` compares it with the newest repository-bundle manifest and requires a typed acknowledgement for extra commits (plan 45, review DR-16).
- Whether Forgejo tag protection holds against worker deploy keys is version-sensitive; a negative result fails GATE-P11, because the release lane must not be reachable from a worker key (review SEC-1).

## Alternatives rejected
- GitHub App with an in-cluster token refresher: puts a non-expiring App private key in the cluster and adds a component.
- SSH push mirror with a GitHub write deploy key: the private key exists only in the Forgejo DB, so any DB loss needs a manual GitHub change, and API-based checks still need a token.
- A CronJob running `git push` instead of the native mirror: duplicates Forgejo functionality and needs the credential in a Kubernetes Secret.
- An unfiltered `--mirror` push: leaks every agent branch to GitHub.

## Verification
GATE-P06 (plan 17): 17-AC-1 (mirror configuration), 17-AC-2/3 (commits and tags propagate), 17-AC-4 (no agent ref leaks), 17-AC-7/8 (GitHub settings and history protection enforced), 17-AC-9/10 (token scope, workflows permission). GATE-P09: 17-AC-13..15 (metrics and alerts).
