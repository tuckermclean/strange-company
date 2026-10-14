# Procedure: GitHub mirror failure

**Owning plan:** plans/17-github-mirror.md
**SPEC references:** §31, §32, §96, §97, §108, §111, §113 ("GitHub mirror failure")
**Severity / urgency:** degraded (mirror behind; RPO at risk). Becomes black-start-threatening at `SCGitHubMirrorRPOBreach` (lag > 1 h) or `SCGitHubMirrorDiverged`.
**Expected duration:** 10–30 min for credential, network or configuration causes; divergence needs a human decision (1–2 h)
**Requires break-glass:** no. The Forgejo admin automation identity (`sc-admin`) is used through `sc-bootstrap`; GitHub settings changes use the operator's own GitHub account.

Forgejo keeps working throughout (SPEC §32). Nothing in this procedure stops or restarts Forgejo.

## When to use
| Alert / symptom | Start at |
|---|---|
| `SCGitHubMirrorFailing` (latest push attempt failed for 15 min) | Step 1 → classification |
| `SCGitHubMirrorStale` (lag > 15 min) or `SCGitHubMirrorRPOBreach` (lag > 1 h) | Step 1 |
| `SCGitHubMirrorDiverged` | §C |
| `SCGitHubMirrorNotConfigured`, `SCForgeConfigDrift{object="push_mirror"}` | §D |
| `SCGitHubRepoSettingsDrift`, `SCGitHubRepoSettingsDriftMinor` | §F |
| `SCGitHubMirrorTokenExpiring`, `SCGitHubMirrorTokenExpiryImminent` | `rotate-forge-credentials.md` §G |
| `SCGitHubMirrorSignalMissing` | §H |
| `SCGitHubMirrorRecoveryModeTooLong` | §I |
| forge-init/preflight errors `GITHUB_DIVERGED`, `GITHUB_TAGS_DIVERGED`, `GITHUB_UNREACHABLE`, `GITHUB_PUSH_REJECTED`, `GITHUB_AUTH_FAILED`, `MIRROR_*` from `mirror verify` | the matching section below |
| Destroy refused with `DESTROY_REFUSED_UNMIRRORED` (plan 49) | Step 1, then rerun destroy |

## Preconditions and required material
- Toolbox (`./bootstrap shell`) with the operator kubeconfig; `bootstrap.env` with `GITHUB_MIRROR_REPO`, `GITHUB_MIRROR_TOKEN`, `GITHUB_PROBE_TOKEN`.
- Access to the operator's GitHub account (repository administration) for §E/§F.
- Forgejo healthy (`SCForgejoDown` not firing). If Forgejo is down, that is the incident: `restore-forgejo.md`.

## Safety checks before acting
1. **Never force-push to GitHub**, never delete GitHub `main` or `release/*`, and never temporarily remove the `sc-history` ruleset to "unstick" the mirror. GitHub's history is the black-start seed (BG-2).
2. Never change the push mirror's `branch_filter` to empty (that would publish every agent branch, 17-R-1).
3. Never paste the mirror token into Forgejo's UI, a ticket or a chat. All operations go through `sc-bootstrap mirror …`, which reads it from `bootstrap.env`.
4. If the installation is in recovery mode (ADR-0554, `expectMirror: false`), do not re-enable the mirror outside `forge-reconnect` (§I).

## Steps
1. **Status.** `./bootstrap exec -- sc-bootstrap mirror status`. Expected fields: mirror present/configured, `last_update`, `last_error`, Forgejo `main`, GitHub `main`, ancestry (`in-sync` | `github-behind` | `diverged`), lag, settings check results, token expiry.
2. **Classify `last_error`** (substring match, case-insensitive; exact texts captured in plan 17 test fixtures; verify on the running versions):

   | `last_error` contains | Cause | Go to |
   |---|---|---|
   | `Could not resolve host`, `Connection timed out`, `connection refused`, `502`, `503`, `504`, `timeout`, `killed` | network/GitHub outage/timeout | §A |
   | `Authentication failed`, `Invalid username or password`, `401`, `403`, `Permission to … denied` | token expired, revoked or lacking permission | §B |
   | `refusing to allow a Personal Access Token to create or update workflow` | token lacks Workflows permission | §B |
   | `protected branch`, `GH013`, `Cannot force-push`, `cannot delete`, `non-fast-forward`, `[remote rejected]` on `main`/`release/*` | GitHub rejected a rewrite or deletion | §C |
   | `Repository not found`, `404`, `archived` | GitHub repository gone, renamed, archived | §E |
   | `GH009`, `push protection`, `secret` | GitHub push protection blocked the push | §F |
   | decrypt/cipher error, or auth failures right after a Forgejo DB restore | stored credential undecryptable (`SECRET_KEY`) | §D |
   | empty, but lag growing | queue item lost or sync-on-commit not firing | §A step A3 |

### §A Network or GitHub outage
- **A1** GitHub status: `./bootstrap exec -- curl -fsS https://www.githubstatus.com/api/v2/status.json | jq -r .status.description` and the blackbox probe `probe_success{probe="github"}` (Prometheus via port-forward). GitHub degraded → wait; Forgejo retries every 10 min. Nothing else to do unless `SCGitHubMirrorRPOBreach` fires (then record the RPO breach for the incident).
- **A2** GitHub fine, cluster egress failing: from the Forgejo pod, `./bootstrap exec -- kubectl -n forgejo exec deploy/forgejo -- curl -sS -o /dev/null -w '%{http_code}\n' https://github.com/` (expect 200/301). Failure → NetworkPolicy or DNS: `./bootstrap exec -- kubectl -n forgejo get networkpolicy` and compare with plan 36's rendered allow for TCP 443 egress; a stale GitHub CIDR list (if plan 36 narrowed egress) is fixed by a PR refreshing it from `https://api.github.com/meta`.
- **A3** Reachable but nothing pushed: trigger `./bootstrap exec -- sc-bootstrap mirror sync`. Expected: `last_update` advances within 5 min, `last_error` empty, lag returns to 0. If `last_update` does not advance at all, check Forgejo's mirror queue: `./bootstrap exec -- kubectl -n forgejo logs deploy/forgejo --since=30m | grep -i -E 'mirror|push_mirror'`; persistent queue stalls → restart Forgejo during a quiet period (`rollout restart`; L-5 caveat in `restore-forgejo.md` §Multi-Attach).
- **A4** Timeouts on large pushes: check repository size (`./bootstrap exec -- kubectl -n forgejo exec deploy/forgejo -- du -sh /data/git/repositories/strange-company/strange-company.git`) and recent large commits; 17-R-5 forbids files ≥ 50 MiB. Removing a large file already merged requires a history decision (never rewrite GitHub history; see §C D3).

### §B Credential (token expired, revoked, under-permissioned)
- **B1** `./bootstrap exec -- sc-bootstrap mirror check-github`. Expected failures pinpoint the cause: `GITHUB_TOKEN_INVALID` (expired/revoked), missing Contents write (`permissions.push: false`), missing Workflows (only visible as the push error above).
- **B2** Issue a new token and recreate the mirror: `rotate-forge-credentials.md` §G (G.1–G.6).
- **B3** If the token was revoked unexpectedly (not by the operator): treat as suspected compromise (`credential-compromise.md`, plan 35); after B2, run §C's comparison to confirm GitHub holds nothing unexpected.

### §C Diverged (GitHub `main`/`release/*` not an ancestor of Forgejo, or GitHub-only commits/tags)
- **C1** `./bootstrap exec -- sc-bootstrap mirror compare-refs`. It lists, for `main`, each `release/*` and each tag: Forgejo SHA, GitHub SHA, relation (`equal`, `github-behind`, `forgejo-behind`, `diverged`, `github-only`, `forgejo-only`).
- **C2** Determine how it happened:
  - `forgejo-behind` on `main` (GitHub newer): Forgejo was restored to an older state, or forge-init pushed an old HEAD. GitHub is authoritative for those commits. Fast-forward Forgejo from GitHub: `./bootstrap exec -- sc-bootstrap forge apply --stage bootstrap` (temporary `sc-admin` push allowlist), `git fetch https://github.com/<repo>.git main` in a toolbox clone, `git push http://sc-admin:<pw>@127.0.0.1:13000/strange-company/strange-company.git <github-sha>:refs/heads/main` (fast-forward only), then `sc-bootstrap forge apply` (back to `steady`). The mirror resumes on the next sync.
  - `diverged` or `github-only` commits: someone wrote to GitHub (tier `unavailable-accepted`, or an administrator bypassed rulesets), or Forgejo history was rewritten. Check GitHub's audit log (organization settings → audit log, filter `repo:<repo>` and `git.push`/`protected_branch`/`repository_ruleset`) and Forgejo's branch-protection events. Treat an unexplained GitHub-side write as a security incident (`credential-compromise.md`).
- **C3** Resolve without force: bring the wanted GitHub-only commits into Forgejo through a normal PR (branch `restore/github-<date>` created from the GitHub commit, merged into `main` by a reviewed PR). After merge, Forgejo `main` descends from GitHub `main`, and the mirror fast-forwards GitHub. Unwanted GitHub-only commits: still merge them with an explicit revert commit on top (the history stays linear on GitHub; content is reverted), because GitHub's ruleset correctly refuses a rewrite.
- **C4** GitHub-only tags: fetch them into Forgejo (`git push <forgejo> refs/tags/<t>`) if they are legitimate; otherwise delete them on GitHub through the operator's account after recording them (tags are not covered by `sc-history`).
- **C5** Verify: `mirror compare-refs` shows only `equal` for `main`/`release/*`; `SCGitHubMirrorDiverged` resolves within 10 min.

### §D Mirror missing, misconfigured, or credential undecryptable
- **D1** `./bootstrap exec -- sc-bootstrap forge apply --dry-run` shows the `pushMirrors` delta (missing mirror, wrong filter, extra mirror).
- **D2** `./bootstrap exec -- sc-bootstrap mirror recreate --token-from env` (or `--token-from bundle` when `bootstrap.env` is not at hand: prompts for the passphrase). Expected: exactly one mirror with `branch_filter "main,release/*"`, `sync_on_commit: true`, interval `10m0s`; sync and verify pass.
- **D3** Investigate who changed it: Forgejo admin audit (repository settings changes are visible in the UI activity/log; `./bootstrap exec -- kubectl -n forgejo logs deploy/forgejo --since=24h | grep -i push_mirror`). An unexplained change is a security incident (R-16-1 in plan 16: repo admins can edit mirrors).

### §E GitHub repository lost, renamed or archived
- **E1** In the operator's GitHub account: if renamed or transferred, note the new `<owner>/<name>`; if archived, unarchive; if deleted, restore it within GitHub's restore window (organization settings → deleted repositories, verify availability) or create a new **empty** private repository.
- **E2** Re-apply §14.1 of plan 17 (machine-user access, rulesets `sc-history` and `sc-mirror-only`, Actions policy, features off, canary branch via `sc-bootstrap mirror check-github --init-canary`).
- **E3** Update `GITHUB_MIRROR_REPO` in `bootstrap.env` (and a PR updating `config/github-mirror.yaml` `repository` and `forge.yaml` `remoteAddress`), then `./bootstrap exec -- sc-bootstrap mirror recreate --token-from env`. A new empty repository receives the full `main`, `release/*` and tags in the first sync.
- **E4** `./bootstrap rotate refresh-bundle` if the token changed. Update the operator's black-start notes (the clone URL changed).

### §F Settings drift (visibility, rulesets, Actions, push protection)
- **F1** `./bootstrap exec -- sc-bootstrap mirror check-github` lists each failing `kind`.
- **F2** Restore the setting in GitHub with the operator account exactly as in plan 17 §14.1.2 (private; rulesets with the specified targets, rules and bypass; Actions read-only; issues/wiki off; secret-scanning push protection off for this repository).
- **F3** `visibility` was public: treat repository contents as disclosed. SOPS files remain protected by age, but review plan 35 `credential-compromise.md` for anything that may have been committed in plaintext (gitleaks report on `main`), and record the exposure window from the GitHub audit log.
- **F4** Push protection blocked a push (`GH009`): identify the flagged file from `last_error`. If it is a real secret, it must be removed and the credential rotated (`credential-compromise.md`); it is already on Forgejo `main`. If it is a false positive, disable push protection for the repository (F2) and record the decision.
- **F5** Verify: `check-github` exit 0; `sc_github_repo_setting_ok` = 1 for all kinds within 1 h (or force a probe cycle by restarting `deploy/platform-probes`).

### §G Rogue tag (pushed by a worker key and mirrored)
- **G1** Only for tags outside `v*`, `sc-archive/*` and `sc-quarantine/*` (those are legitimate and mirrored by design, plan 17 Q-17-09). The Runner reports `POLICY_VIOLATION` (`foreign_ref`) and deletes the tag on Forgejo (plan 26). Trigger `./bootstrap exec -- sc-bootstrap mirror sync`.
- **G2** `./bootstrap exec -- sc-bootstrap mirror compare-refs --tags`. If the tag is still on GitHub (prune not propagated, see plan 17 Q-17-03), delete it with the operator account (or `sc-bootstrap mirror delete-github-tag <tag>`, mirror token) and record it.
- **G3** Record the tag name, target SHA, execution ID and exposure window in the incident.

### §H Signal missing
- **H1** `./bootstrap exec -- kubectl -n monitoring get pods -l app.kubernetes.io/name=platform-probes` and logs. `SCPlatformProbesDown` → restart; crash → fix by PR (plan 15).
- **H2** `sc_github_api_up == 0`: probe token expired or rate-limited. `./bootstrap exec -- kubectl -n monitoring logs deploy/platform-probes | grep -i github | tail -20`. 401 → `rotate-forge-credentials.md` §G.4; 403/429 rate limit → wait for reset; persistent → check for another consumer of the same token.
- **H3** Meanwhile, judge the mirror manually every hour with `sc-bootstrap mirror status`.

### §I Recovery mode lasting too long (ADR-0554)
- **I1** The installation is in recovery mode with mirrors disabled (`expectMirror: false`) for > 7 days. GitHub is not receiving new commits, so the black-start seed ages.
- **I2** Complete plan 45's `forge-reconnect`: `mirror compare-refs` (fast-forward only; divergence → §C), then the finalize PR re-enables mirrors and sets `expectMirror: true`, then `mirror recreate` and `mirror verify`.

## Verification
1. `./bootstrap exec -- sc-bootstrap mirror verify` exits 0 (main equal; probe tag round trip).
2. `sc_github_mirror_last_attempt_success{repository="strange-company"}` = 1, `sc_github_mirror_lag_seconds` = 0, `sc_github_mirror_diverged` = 0, `min(sc_github_repo_setting_ok)` = 1.
3. All `SCGitHubMirror*` and `SCGitHubRepoSettingsDrift` alerts resolved within 15 min.
4. If the incident breached RPO, `./bootstrap exec -- kubectl -n dr-tests create job --from=cronjob/restore-test-github-black-start rt-github-manual-<ts>` succeeds (plan 44).

## Rollback / abort
Every action here is additive or fast-forward. `mirror recreate` can be repeated. Abort leaves Forgejo fully functional and GitHub at its last good state, which the alerts continue to report.

## Evidence to record
Incident issue (Forgejo) with: alert timeline, `mirror status --json` before and after, `last_error` text (token-free), classification, `compare-refs` output (for §C), GitHub audit-log excerpts (for §C/§F), PRs merged, maximum observed lag (RPO impact against SPEC §108), and whether the black-start test was re-run.

## Follow-ups
- RPO breach: report to plan 44/45 owners; check whether the alert thresholds or the 10-minute interval need tuning.
- Divergence or settings drift without a known cause: security review (plan 34) and GitHub access review (who holds admin on the repository).
- Token-related failures: shorten the rotation calendar or improve expiry alerting (plan 35).
