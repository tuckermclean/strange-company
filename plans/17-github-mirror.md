# 17 — GitHub mirror (off-cluster recovery seed)

**Status:** Stage-0 plan (draft)
**DAG nodes:** **P06 control-transfer-and-mirror (co-owner with 19 and 18; this plan owns the mirror part, GATE-P06 §23)**. Contributes to P05 (mirror creation step S17 of forge-init, plan 16), P09 (mirror metrics and alerts), P10 (GitHub black-start test assertions), P12 (verify check V-08).
**Related plans:** [06-opentofu-state](06-opentofu-state.md), [11-storage](11-storage.md), [14-object-storage](14-object-storage.md), [15-authentik](15-authentik.md) (hosts the `platform-probes` Deployment), [16-forgejo](16-forgejo.md), [18-flux](18-flux.md), [19-seed-handoff](19-seed-handoff.md), [23-supply-chain](23-supply-chain.md), [26-agent-runner](26-agent-runner.md), [35-secrets](35-secrets.md), [36-network-policy](36-network-policy.md), [38-prometheus](38-prometheus.md), [42-alerting-notifications](42-alerting-notifications.md), [44-backup](44-backup.md), [45-recovery](45-recovery.md), [49-destroy](49-destroy.md), [50-bootstrap-orchestrator](50-bootstrap-orchestrator.md), 52 (deterministic CI strategy), [55-forge-workflow](55-forge-workflow.md)
**Contracts:** owns `contracts/github-mirror-config.schema.yaml` (`GitHubMirrorConfig`) and the `pushMirrors` element of `contracts/forge-configuration.schema.yaml` (file owner 16); consumes `bootstrap-config.schema.yaml` (50: `/githubMirror/*`), `secret-classes.yaml` (35: `SC-GITHUB-MIRROR`, `SC-GITHUB-PROBE`, `SC-GITHUB-MIRROR-READ`), `alert-catalog.yaml` (42), `bootstrap-journal.schema.yaml` (50), `restore-test-result.schema.yaml` (44).
**ADRs:** relies on ADR-0005, 0012, 0016, 0018, 0019, 0553, 0554; introduces **ADR-0253** (GitHub mirror: Forgejo push mirror with a fine-grained PAT, branch filter, read-only GitHub).

## 1. Purpose

SPEC §6 makes Forgejo the primary forge once the platform is alive, and GitHub the place from which the platform is resurrected when Forgejo is not. This plan defines:

1. The **Forgejo → GitHub push mirror** of `strange-company/strange-company`: sync on every push plus a 10-minute interval, filtered to the refs that matter for black start (`main`, `release/*`, tags) so agent, Renovate and PR refs never leave the cluster.
2. The **mirror credential** `SC-GITHUB-MIRROR` (a fine-grained personal access token scoped to one repository), why it is a PAT and not a GitHub App token, and how it is rotated.
3. The **GitHub repository settings** that make GitHub a read-only copy: private visibility, push restricted to the mirror identity, force pushes and deletions of `main`/`release/*` blocked, and an Actions policy that cannot write.
4. **Mirror health observability:** Forgejo push-mirror status (`last_update`, `last_error`) plus an *independent* freshness check that compares GitHub `main` with Forgejo `main`, exported as `sc_github_mirror_*` metrics with alerts.
5. The **independence guarantee**: no Forgejo function depends on GitHub [SPEC §32].
6. The **upstream-updates path** (SI-11): how changes from outside enter now that GitHub is downstream.
7. The **black-start guarantees** the mirror provides. Plan 45 owns the black-start procedure.

## 2. Requirements derived from SPEC.md

1. `strange-company` continuously mirrors from Forgejo to GitHub [SPEC §6, §31, §32].
2. GitHub serves black start, bootstrap, external source backup and total-cluster-loss recovery [SPEC §32, §111, §112].
3. Flux sources Forgejo after bootstrap, never GitHub [SPEC §32, §33].
4. Mirror failure is visible [SPEC §32]. Alerts cover GitHub mirror failure [SPEC §96], and a failed mirror produces a human notification [SPEC §97].
5. Forgejo operation does not depend on GitHub being available [SPEC §32].
6. Control transfer is complete only when the GitHub mirror works [SPEC §35].
7. RPO for `strange-company` committed Git state is ≤ 1 h, preferably near-zero mirror lag; RTO ≤ 2 h [SPEC §108].
8. Repeatable restore testing covers the `strange-company` GitHub black start [SPEC §109]. Restore-test failure is visible and alertable.
9. The GitHub mirror token is an owned secret class with creator, consumer, storage, rotation and recovery [SPEC §99]. A rotation procedure exists ("rotate Forge credentials", SPEC §113).
10. Destroying compute never destroys the GitHub mirror [SPEC §114] (plan 49 implements; §17 here states the guarantee).
11. The GitHub mirror is part of the Self-Hosting Threshold platform and of MVP [SPEC §117, §126].
12. Procedure "GitHub mirror failure" exists [SPEC §113].

Derived requirements:

- **17-R-1** Only `refs/heads/main`, `refs/heads/release/*` and `refs/tags/*` are mirrored. Agent branches (`agent/*`), Renovate branches, `sc-verify/*` probe branches, upstream-sync branches and `refs/pull/*` are never pushed to GitHub. **Exception, by Forgejo design:** every tag is pushed, including the Runner's `sc-archive/*` provenance tags and `sc-quarantine/*` violation tags (plan 55). Those tags make agent commits (never secrets: plan 55 does not tag `SECRET_DETECTED` content) reachable on the private GitHub repository. Accepted (§13.3); the alternative of moving them to a non-tag ref namespace is a plan 55 decision (Q-17-09).
- **17-R-2** GitHub never rewrites or loses history that it already holds on `main` or `release/*`. Non-fast-forward updates and deletions of those branches are rejected **by GitHub**, independently of Forgejo's configuration (defense against a restored-older Forgejo, ADR-0554).
- **17-R-3** After control transfer, GitHub is **read-only for humans**. The only writer is the mirror identity (SI-11).
- **17-R-4** Mirror health is judged by two independent signals: Forgejo's own push-mirror status, and a comparison of the actual GitHub `main` SHA with Forgejo `main`. Either one alone can lie: Forgejo can report success for a mirror that pushes the wrong refs, and a matching SHA says nothing while there are no new commits.
- **17-R-5** `strange-company` does not use Git LFS and contains no file ≥ 50 MiB. A clone from GitHub is therefore complete without any Strange Company–operated service (black start).
- **17-R-6** The mirror credential can write to exactly one GitHub repository and has no administrative permission on it.
- **17-R-7** The in-cluster freshness probe holds no GitHub write credential. It uses a separate read-only token (`SC-GITHUB-PROBE`).

## 3. Inputs

| Input | Source | Used by |
|---|---|---|
| `GITHUB_MIRROR_REPO` (`<owner>/<name>`), `GITHUB_MIRROR_TOKEN`, `GITHUB_MIRROR_ALLOW_PUBLIC` | `bootstrap.env` → `/githubMirror/*` (plan 50 §9.2) | preflight PF-16, forge-init S17, rotation |
| `GITHUB_PROBE_TOKEN` (**new key**, required unless the repository is public) | `bootstrap.env` → `/githubMirror/probeToken` (plan 50 must add) | written by plan 50 into the SOPS file `monitoring/platform-probes-github` |
| `GITHUB_MIRROR_READ_TOKEN` (**new key**, required unless the repository is public; CD-19) | `bootstrap.env` → `/githubMirror/readToken` (plan 50 must add) | written by plan 50 into `secrets/dr-tests/github-mirror-read.sops.yaml` at install-identity (RT-6) |
| `GITHUB_MIRROR_PROTECTION_TIER` (**new key**, `rulesets` \| `unavailable-accepted`, default `rulesets`) | `bootstrap.env` → `/githubMirror/protectionTier` (plan 50 must add) | §9.2 config, settings check |
| Forgejo repo `strange-company/strange-company`, admin credential `SC-FORGEJO-ADMIN` | plan 16 | mirror creation, sync trigger |
| `forge/forge.yaml` `pushMirrors` entry | Git (§7.2) | `forge apply`, drift probe |
| GitHub repository, prepared by the operator per §14.1 | operator, before `./bootstrap` | everything |
| Off-cluster upstream repository (optional) | `gitops/clusters/<c>/config/github-mirror.yaml` `upstream` | §14.6 |

## 4. Outputs

1. A Forgejo push mirror on `strange-company/strange-company` targeting `https://github.com/<GITHUB_MIRROR_REPO>.git`.
2. GitHub `main`, `release/*` and tags equal to Forgejo's, normally within seconds of each push.
3. Non-secret config `gitops/clusters/<cluster>/config/github-mirror.yaml` (kind `GitHubMirrorConfig`, §9.2), rendered as ConfigMap `monitoring/github-mirror-config`.
4. SOPS Secret `gitops/clusters/<cluster>/secrets/github/platform-probes-github.sops.yaml` → Secret `monitoring/platform-probes-github` (key `token`, `SC-GITHUB-PROBE`).
5. The `github-mirror` module of `platform-probes` (plan 15 §21.2) and its metrics (§21).
6. Bootstrap checks: preflight PF-16 sub-checks, forge-init steps M0–M5, the `mirror-verify` check used by plan 19 C8 and plan 50 V-08, and the CLI subcommands in §7.3.
7. Alert definitions (§21.3) for plan 42 to register.
8. The GitHub black-start assertions that plan 44's `restore-test-github-black-start` CronJob executes (§20.2).
9. Procedure `procedures/github-mirror-failure.md`. Token rotation mechanics live in `procedures/rotate-forge-credentials.md` §G.

## 5. Dependencies

**Hard (DAG):** P06 needs P05 (forge-init has created the repository and pushed `main`). The mirror part of GATE-P06 runs after the source switch has passed (plan 19), so a mirror failure never blocks or confuses the switch itself.

**Soft and runtime:**
- GitHub.com availability: needed only by the mirror push, the freshness probe, preflight and black start. Never on any Forgejo, Flux or Runner path (§13.4).
- Plan 15 `platform-probes` Deployment (module host). It exists right after control transfer (P06; Kustomization `platform-probes`, `dependsOn: [forgejo]`); its alerts fire from P09. Before it starts, mirror health is checked only by `sc-bootstrap mirror verify`.
- Plan 42 registers the alerts and routes them. Plan 44 runs the black-start restore test.

**Assumed interfaces (other plans must honor):**
- **Plan 16:** forge-init step S17 calls this plan's steps M0–M5 (§14.2). `forge/forge.yaml` carries a `pushMirrors` list whose element shape is §7.2. `forge apply` creates or recreates the mirror from it using `SC-GITHUB-MIRROR`, which it reads from the operator's `bootstrap.env` or unlocked bundle (never from the cluster). The platform-probes Forgejo token (`sc-admin`, scopes `read:admin,read:repository,read:user`) can list push mirrors.
- **Plan 16:** the Forgejo version selected is ≥ the first release carrying push-mirror `branch_filter` (merged upstream July 2025, Forgejo PR #7823). The LTS in plan 16 §9.1 satisfies this. 17-AC-1 re-verifies.
- **Plan 16:** Forgejo protected-tag rule `*` (whitelist: `sc-admin`, team `Owners`) in `forge.yaml` (§13.3 here explains why). Plan 16 adds the field.
- **Plan 50:** CLI forms in §7.3, new `bootstrap.env` keys `GITHUB_PROBE_TOKEN` and `GITHUB_MIRROR_PROTECTION_TIER`, PF-16 implemented as §14.1.3, and V-08 implemented as `mirror-verify` (§14.3). V-08 uses a probe **tag**, not an `sc-verify/<run-id>` branch, because branches outside the filter are never mirrored (17-R-1).
- **Plan 19:** check C8 = `sc-bootstrap mirror verify` (§14.3). The "probe commit" in plan 19's C8 wording is a probe tag (no commit lands on `main`).
- **Plan 38:** scrape job for these metrics is `platform-probes` (port `metrics` 9464 on `monitoring/platform-probes`), not a separate `forgejo/github-mirror-watch` Service. The metric names and labels in plan 38 §21.2 are kept. §21.1 adds five metrics to that catalogue.
- **Plan 18:** layer `forgejo-config` contains **no** mirror reconciler. The mirror is Forgejo database state, created by forge-init and `forge apply`. Plan 18's layer table should drop "mirror reconciler (17)".
- **Plan 36:** egress from `forgejo` pods to TCP 443 on public addresses (plan 16 §13.2) and from `monitoring/platform-probes` to `api.github.com:443`.
- **Plan 45 / ADR-0554:** in recovery mode Forgejo runs with push mirrors disabled until `forge-reconnect`, which calls §20.3.
- **Plan 49:** destroy never touches GitHub. Its unmirrored-commit guard calls `sc-bootstrap mirror freshness` (§7.3).
- **Plan 52/23:** CI checks `no-lfs`, `max-file-size` (50 MiB) and `no-plaintext-github-token` run on every PR to `strange-company` (17-R-5, 17-AC-12).

## 6. Owned state

| State | Location | Writer | Backup / recovery |
|---|---|---|---|
| Push-mirror record (remote address, filter, interval, `sync_on_commit`, `last_update`, `last_error`, encrypted credential) | Forgejo DB `forgejo-pg` (`push_mirror` table); remote config in the bare repository on the `forgejo-data` PVC | Forgejo, via forge-init / `forge apply` | CNPG PITR (plan 12). Re-creatable from `forge.yaml` + bundle at any time (§20.3) |
| Mirror desired state | Git `forge/forge.yaml` `pushMirrors` | humans via PR | Git |
| `GitHubMirrorConfig` | Git `gitops/clusters/<c>/config/github-mirror.yaml` | bootstrap, rotation PRs | Git |
| The GitHub repository contents (`main`, `release/*`, tags) | GitHub | Forgejo push mirror **only** | GitHub is itself the off-cluster copy. Also covered by hourly bundles in S3 `repo-backups` (plan 44), which carry all refs |
| GitHub repository settings | GitHub | operator only (no Strange Company credential has administration write) | documented checklist §14.1; drift detected (§21) |
| Probe runtime state (last-known GitHub SHA, unsynced-since timestamp) | `platform-probes` memory | probe | none: rebuilt conservatively on restart (§14.4) |

## 7. External interfaces

### 7.1 Forgejo push-mirror API (third-party; verified against Forgejo source 2026-10-13, re-verified by 17-AC-1)

| Call | Use |
|---|---|
| `GET /api/v1/repos/{owner}/{repo}/push_mirrors` | List mirrors. Fields `remote_name`, `remote_address`, `created`, `last_update`, `last_error`, `interval`, `sync_on_commit`, `public_key`, `branch_filter` |
| `POST /api/v1/repos/{owner}/{repo}/push_mirrors` | Create. Body `CreatePushMirrorOption`: `remote_address`, `remote_username`, `remote_password`, `interval`, `sync_on_commit`, `use_ssh`, `branch_filter` |
| `DELETE /api/v1/repos/{owner}/{repo}/push_mirrors/{remote_name}` | Delete (used by rotation and recreation) |
| `POST /api/v1/repos/{owner}/{repo}/push_mirrors-sync` | Trigger an immediate sync of all push mirrors of the repository |

Semantics this plan relies on (from `services/mirror/mirror_push.go`):
- With a non-empty `branch_filter`, Forgejo configures one push refspec per comma-separated entry (`+refs/heads/<entry>:refs/heads/<entry>`; globs allowed) **and always** `+refs/tags/*:refs/tags/*`. With an empty filter it pushes all branches (`--mirror`). Tags are therefore always mirrored.
- The push runs with force and mirror flags. Pruning of remote refs that no longer exist locally is expected to be limited to the configured refspec namespaces (verify: 17-AC-3 measures it; plan 50 OQ-50-3).
- `last_update` is set after **every** attempt, successful or not. `last_error` is cleared at the start of an attempt and set to the error text on failure. **Success ⇔ `last_error == ""` and `last_update` non-null.**
- There is no API to update a mirror's credential or filter. A change is a delete plus a create (§14.5).
- Credential storage: the password is stored encrypted with `SECRET_KEY` in the DB. Whether the bare repository's `config` on the PVC also holds the credential in the remote URL is version-dependent (verify: 17-AC-11 records the result and §13.2 states the consequence).

### 7.2 `forge.yaml` `pushMirrors` element (`contracts/forge-configuration.schema.yaml` `$defs/pushMirror`; the schema rejects an empty filter or one matching `agent/`, `renovate/`, `sc-verify/` or `*`)

```yaml
pushMirrors:
  - name: github                                  # Strange Company identifier; Forgejo assigns remote_name itself
    remoteAddress: https://github.com/${GITHUB_MIRROR_REPO}.git
    branchFilter: "main,release/*"                # 17-R-1; tags are always pushed by Forgejo
    interval: 10m0s                               # = Forgejo [mirror] MIN_INTERVAL (plan 16 §9.2)
    syncOnCommit: true
    useSSH: false
    credential: {class: SC-GITHUB-MIRROR, source: operator}   # never stored in Git or in-cluster Secrets
```

`forge apply` identifies the mirror by `remoteAddress`. A mirror on the repository whose address is not declared is drift (`sc_forge_config_drift{object="push_mirror"}`, plan 16). `forge apply --prune-mirrors` deletes it after confirmation.

### 7.3 CLI (plan 50 owns the surface; forms assumed here)

| Command | Effect | Mutates |
|---|---|---|
| `./bootstrap exec -- sc-bootstrap mirror status [--json]` | Forgejo mirror record, GitHub and Forgejo `main` SHAs, lag, divergence, settings check, token expiry | no |
| `./bootstrap exec -- sc-bootstrap mirror sync` | `POST push_mirrors-sync`, then wait ≤ 5 min for `last_update` to advance; prints the result | triggers a push |
| `./bootstrap exec -- sc-bootstrap mirror verify [--run-id <id>]` | The `mirror-verify` check (§14.3) | creates and deletes a probe tag |
| `./bootstrap exec -- sc-bootstrap mirror check-github` | §14.1.3 checks using `GITHUB_MIRROR_TOKEN` and `GITHUB_PROBE_TOKEN` | no |
| `./bootstrap exec -- sc-bootstrap mirror recreate [--token-from env\|bundle]` | delete + create the mirror from `forge.yaml` with the current token, then `sync` and `verify` | yes |
| `./bootstrap exec -- sc-bootstrap mirror freshness` | exit 0 if GitHub `main` == Forgejo `main`; exit 1 listing unmirrored commits; exit 3 if unknown (plan 49 consumer) | no |
| `./bootstrap exec -- sc-bootstrap mirror compare-refs [--tags]` | full table of `main`, `release/*`, tags on both sides with ancestry relations (used by procedures) | no |
| `./bootstrap exec -- sc-bootstrap mirror delete-github-tag <tag>` | delete one tag on GitHub with the mirror token (rogue-tag and probe-tag cleanup; refuses `main`/`release/*` and any branch) | yes (GitHub tag only) |

All commands reach Forgejo through `kubectl port-forward svc/forgejo-http 13000:3000` (plan 16 §8) and authenticate as `sc-admin`. GitHub calls use `https://api.github.com` with `Accept: application/vnd.github+json` and `X-GitHub-Api-Version` pinned at implementation (verify current version string).

### 7.4 GitHub REST endpoints used (third-party; all marked verify, re-verified by 17-AC-7 and the integration lane)

| Endpoint | Token | Use |
|---|---|---|
| `GET /user` | mirror | Token owner login (`remote_username`); response header `github-authentication-token-expiration` gives the token expiry (verify) |
| `GET /repos/{o}/{r}` | mirror, probe | `private`, `visibility`, `default_branch`, `permissions.push`, `archived` |
| `GET /repos/{o}/{r}/branches/main` | probe | `commit.sha` (with `If-None-Match` ETag) |
| `GET /repos/{o}/{r}/git/matching-refs/tags/sc-mirror-probe/` | mirror, probe | probe tag presence |
| `GET /repos/{o}/{r}/rules/branches/{branch}` | probe | active rules applying to `main`, `release/x`, `sc-canary/protection` (verify readable with Metadata: read) |
| `GET /repos/{o}/{r}/actions/permissions`, `.../actions/permissions/workflow` | probe | Actions policy and default `GITHUB_TOKEN` permission (needs Administration: read; verify) |
| `GET /user/repos?per_page=100` | mirror | count of repositories the token can access (17-R-6) |
| `DELETE /repos/{o}/{r}/git/refs/tags/{tag}` | mirror | probe-tag cleanup when pruning is not propagated |
| `GET /repos/{o}/{r}/actions/workflows/{file}/runs` | probe (Actions: read) | off-cluster e2e lane status (`ci-signals`, plan 52) |

### 7.5 Human interface
GitHub is browsable and clonable by the operator. The repository description states "Read-only mirror. Primary: https://git.<BASE_DOMAIN>/strange-company/strange-company". Issues, Wiki, Projects and Discussions are disabled on GitHub so nobody mistakes it for the forge.

## 8. Internal interfaces

- Forgejo (pod `forgejo`, namespace `forgejo`) → `https://github.com:443` (git smart HTTP push). Forgejo's mirror queue runs the push, bounded by Forgejo `[git.timeout] MIRROR` (default 600 s; plan 16 keeps the default).
- `platform-probes` (`monitoring`) → Forgejo API `forgejo-http.forgejo.svc:3000` (sc-admin probe token, plan 16) and → `api.github.com:443` (probe token).
- `platform-probes` reads ConfigMap `monitoring/github-mirror-config` (mounted, re-read every 5 min).
- `sc-bootstrap mirror …` (toolbox) → Forgejo via port-forward; → GitHub API and `git` over HTTPS with the operator's tokens.

## 9. Configuration

### 9.1 Knobs

| Key | Source of truth | Default | Who changes |
|---|---|---|---|
| Mirror remote, filter, interval, sync-on-commit | `forge/forge.yaml` `pushMirrors` | §7.2 | PR + `forge apply` (operator) |
| Forgejo `[mirror] MIN_INTERVAL` | plan 16 values | `10m` | plan 16 |
| `GitHubMirrorConfig` (§9.2) | `gitops/clusters/<c>/config/github-mirror.yaml` | rendered at bootstrap | PR |
| Freshness poll intervals | `platform-probes` config (plan 15 §21.2) | Forgejo 60 s; GitHub 300 s, plus an extra GitHub poll at most every 60 s when Forgejo `main` changed | PR |
| Alert thresholds | `contracts/alert-catalog.yaml` (plan 42) | §21.3 | PR (plan 42) |

### 9.2 `GitHubMirrorConfig` (non-secret; `contracts/github-mirror-config.schema.yaml`)

```yaml
apiVersion: strange.company/v1alpha1
kind: GitHubMirrorConfig
metadata: {name: strange-company}
spec:
  repository: example-org/strange-company        # GITHUB_MIRROR_REPO
  forgejoRepository: strange-company/strange-company
  expectMirror: true                              # false only while ADR-0554 recovery mode disables mirrors
  branchFilter: "main,release/*"
  allowPublic: false                              # GITHUB_MIRROR_ALLOW_PUBLIC
  protectionTier: rulesets                        # rulesets | unavailable-accepted (§14.1, SI-G4A-01)
  mirrorTokenExpiresAt: "2027-04-10T00:00:00Z"    # from the GitHub expiry header at creation/rotation; null = no expiry
  mirrorTokenOwner: example-org-sc-mirror
  canaryBranch: sc-canary/protection
  ciSignals:                                      # §14.4 step 8 (plan 52 §21)
    offclusterWorkflow: e2e
    offclusterBranch: main
    nightlyContext: ci/woodpecker/cron/nightly
    quarantineFile: policy/test-quarantine.yaml
  upstream:                                       # §14.6, optional
    enabled: false
    url: https://github.com/<upstream-owner>/strange-company.git
    ref: main
```

The probe treats an unknown `kind` or a schema-invalid file as `sc_github_mirror_config_valid 0` (fails closed: every mirror alert stays armed).

## 10. Secrets

| Class | Status | Creator | Consumer | Storage | Rotation | Recovery |
|---|---|---|---|---|---|---|
| `SC-GITHUB-MIRROR` (fine-grained PAT; repository access: only `<GITHUB_MIRROR_REPO>`; permissions: Contents read/write, Workflows read/write, Metadata read (mandatory); nothing else) | master §9; registered in `secret-classes.yaml` (owner 17) | operator in GitHub settings, before bootstrap, on the mirror identity (§14.1) | Forgejo push mirror; `sc-bootstrap` (preflight, mirror create/verify/recreate, plan 49 freshness) | `bootstrap.env` (authoritative), recovery bundle, Forgejo DB (encrypted with `SECRET_KEY`), possibly the bare repo `config` on the PVC (§13.2). **Never** in Git, never in a Kubernetes Secret | every ≤ 180 days and on suspicion: `rotate-forge-credentials.md` §G. Expiry set ≤ 180 days at creation | bundle; else the operator issues a new token in GitHub |
| `SC-GITHUB-PROBE` (fine-grained PAT on the same repository; permissions: Contents read, Metadata read, Administration read, **Actions read**; nothing else) | registered (owner 17) | operator, before bootstrap | `monitoring/platform-probes` (`github-mirror`, `ci-signals` modules), `sc-bootstrap mirror check-github`, the `sc-release gate --offcluster` step (plan 23/52, polls GitHub Actions run of workflow `e2e`) | SOPS `gitops/clusters/<c>/secrets/github/platform-probes-github.sops.yaml` (authoritative, both recipients) → Secret `monitoring/platform-probes-github`; Woodpecker secret `sc_github_probe_token` (events `[tag]`, image filter `sc-release`) applied by `sc-bootstrap ci-secrets apply` from `secrets/ci/<c>/github-probe.sops.yaml`; `bootstrap.env` | ≤ 365 days; `rotate-forge-credentials.md` §G.4 | re-issue in GitHub; non-critical (observability and the release gate wait) |
| `SC-GITHUB-MIRROR-READ` (fine-grained PAT, Contents read + Metadata read on the mirror repository only) | registered (owner 17) | operator, **before bootstrap** (CD-19: collected via `bootstrap.env` `GITHUB_MIRROR_READ_TOKEN` and written to its SOPS file by plan 50 at install-identity, so RT-6 can run inside `verify`) | plan 44 RT-6 `restore-test-github-black-start` (`dr-tests`) | SOPS `gitops/clusters/<c>/secrets/dr-tests/github-mirror-read.sops.yaml` → Secret `dr-tests/github-mirror-read` | ≤ 365 days; `rotate-forge-credentials.md` §G.7 | re-issue; not needed when the mirror is public |

Why the mirror credential is a **PAT** (ADR-0253):
- GitHub App installation tokens expire after one hour. A Forgejo push mirror stores one static credential and has no hook to refresh it, so an App token would break the mirror within an hour of each refresh. Running an in-cluster token refresher that calls delete+create every 50 minutes would add a component with an App private key (a more powerful, non-expiring credential) inside the cluster. L-6 prefers App tokens "where automation needs GitHub write access"; the push mirror is the documented exception.
- A fine-grained PAT can be limited to one repository and to Contents (+ Workflows) permissions, and carries an expiry.
- The **Workflows** permission is required because `.github/workflows/**` exists on `main` (interim and advisory CI, ADR-0018). Without it GitHub rejects any push that touches workflow files (verify the rejection text in 17-AC-10). Branch filters cannot exclude paths. The risk L-6 names (the credential can change the checks that verify the work) is bounded: GitHub Actions is advisory after P08, and the mirror only ever pushes what Forgejo `main` already holds after Woodpecker and human review.
- An SSH push mirror (Forgejo-generated key added as a GitHub write deploy key) was considered and rejected (ADR-0253): its private key exists only in the Forgejo DB, so any Forgejo DB loss requires a manual GitHub change before the mirror works again, and preflight/freshness checks would still need an API token.

`SC-GITHUB-MIRROR` is **excluded** from `platform-probes`, workers, Hermes, CI verification pipelines and every Kubernetes Secret (17-R-7). forge-init and `forge apply` read it from the operator side only.

## 11. Storage

No PVC or bucket of its own. The push-mirror record lives in `forgejo-pg`; the git remote configuration lives in the `forgejo-data` PVC (plan 16). GitHub holds the mirrored copy. Plan 44's hourly bundles in S3 `repo-backups` cover all refs, including those deliberately not mirrored.

Size budget: GitHub recommends repositories stay well below 5 GB and rejects files > 100 MB (verify). 17-R-5 (no LFS, no file ≥ 50 MiB) keeps the mirror inside both limits. `make repo-size-check` (plan 52) fails a PR when the packed repository exceeds 1 GiB, which triggers a review of what is being committed.

## 12. Resource requirements

| Component | self-hosting-minimum | small-ha |
|---|---|---|
| Forgejo mirror push (git subprocess inside the Forgejo pod) | transient: ≈ 50–150 MiB RSS for seconds per push; within plan 16 Forgejo limits | same |
| `platform-probes` `github-mirror` module | within plan 15 budget (cpu 10m, mem 48Mi/128Mi); ≤ 1 GitHub API call/min average, ≤ 300/h worst case (rate limit 5,000/h per token) | same |
| Network | one push per Forgejo push + one no-op negotiation per 10 min (a few KiB) | same |

## 13. Security boundary

### 13.1 Trust levels
- The mirror identity is a dedicated GitHub machine user (recommended `<owner>-sc-mirror`) with **Write** role on the one repository. Its PAT is the only credential that can change GitHub refs.
- The probe token is read-only and lives in-cluster (`monitoring`). A leak exposes repository contents (which are already SOPS-encrypted where sensitive) and settings metadata, nothing else.
- The GitHub repository is **private** unless `GITHUB_MIRROR_ALLOW_PUBLIC=true`. Even when private, everything in it is treated as potentially readable: SOPS files are encrypted to the two age recipients only (ADR-0012), and CI forbids plaintext secrets (17-AC-12).

### 13.2 Credential exposure inside the cluster
`SC-GITHUB-MIRROR` exists in-cluster only inside Forgejo: in `forgejo-pg` (encrypted with `SECRET_KEY`) and, depending on the Forgejo version, in the bare repository's git `config` remote URL on `forgejo-data` (17-AC-11 records which). In the second case it is also inside Longhorn backups in S3 `longhorn-backups` (plan 13), whose credentials are restricted to Longhorn and the operator (plan 14). Accepted because the token can write only to the mirror, GitHub blocks history rewrites on `main`/`release/*` (17-R-2), and rotation is ≤ 180 days. Repository bundles (plan 44) do not contain the repository `config` file.

### 13.3 What can reach GitHub through the mirror
The mirror pushes whatever Forgejo holds under `main`, `release/*` and `refs/tags/*`:
- `main` and `release/*` are protected on Forgejo: deploy keys cannot push, steady-stage pushes require PR + CI + human approval (plan 16 §13.3, ADR-0019).
- **Tags are always mirrored** (Forgejo behavior) and are not covered by branch protection. Legitimate tags include `v*` releases, the Runner's `sc-archive/*` provenance tags and `sc-quarantine/*` policy-violation tags (plan 55); the last two point at agent commits, which therefore become reachable on the private GitHub repository (accepted: no secret content is ever tagged; GitHub `main` stays the only black-start input). A worker deploy key could also push a tag (SI-06). Mitigations, in order: (1) Forgejo protected-tag rules `*`, `v*`, `sc-archive/*`, `sc-quarantine/*` (plan 16 `forge.yaml`; deploy-key pushes run as the repository owner, which no rule whitelists; whether protected tags apply to deploy-key pushes is version-sensitive, verify in 17-AC-4); (2) the Runner's foreign-ref detection treats any tag created during an execution window as `POLICY_VIOLATION` and deletes it (ADR-0007), after which the next mirror sync prunes it from GitHub; (3) black start uses only `main` and `release/*`, never a tag, unless the operator explicitly selects a tag that is an ancestor of `main` (plan 45). Residual risk: a rogue tag may exist on the private GitHub repository for up to one mirror interval after detection. Recorded for plan 34.
- Agent, Renovate, upstream-sync and probe branches never leave Forgejo (17-R-1). Their contents are only in Forgejo and in S3 bundles.

### 13.4 Independence from GitHub [SPEC §32]
- Forgejo `OFFLINE_MODE=true` (plan 16). No Forgejo health check, startup step, login path or webhook targets GitHub. The push mirror runs in Forgejo's background queue; a failed or hanging push affects only that queue item (bounded by `[git.timeout] MIRROR`).
- Flux sources Forgejo over the in-cluster SSH Service (ADR-0016). The Runner, Woodpecker and workers use Forgejo only.
- The only in-cluster GitHub callers are the Forgejo mirror queue and `platform-probes`. Both fail without side effects. 17-AC-6 proves Forgejo operation with GitHub unreachable.

### 13.5 Network
- `forgejo` egress TCP 443 to public addresses (plan 16 §13.2). Plan 36 MAY narrow it to GitHub's published `git` CIDRs from `https://api.github.com/meta`, rendered at bootstrap and refreshed by a Renovate-style PR. If the list is stale the mirror fails visibly (`SCGitHubMirrorFailing`), which is the safe direction.
- `monitoring/platform-probes` egress TCP 443 to `api.github.com` (same approximation).

### 13.6 GitHub-side controls (17-R-2, 17-R-3)
Defined in §14.1.2. Every administrative capability on GitHub stays with the operator's own account, which Strange Company never holds a credential for.

## 14. Happy path

### 14.1 Before bootstrap: GitHub repository preparation (operator; plan 50 `fresh-install.md` includes this list)

#### 14.1.1 Identity and tokens
1. The repository is owned by a GitHub **organization** (recommended) on a plan that offers rulesets for private repositories (GitHub Team or higher at authoring time; verify plan availability). If the operator uses a personal account or the free plan, set `GITHUB_MIRROR_PROTECTION_TIER=unavailable-accepted` and read SI-G4A-01.
2. Create machine user `<owner>-sc-mirror` (one machine account per operator is permitted by GitHub's terms, verify). Grant it **Write** on the repository only. Create team `sc-mirror` containing only that user (rulesets bypass by team, §14.1.2).
3. As the machine user, create fine-grained PAT `strange-company-mirror`: resource owner = the organization; repository access = only this repository; permissions Contents R/W, Workflows R/W, Metadata R; expiry ≤ 180 days. Put it in `GITHUB_MIRROR_TOKEN`. If the organization requires approval of fine-grained PATs, approve it.
4. Create fine-grained PAT `strange-company-probe` (same owner and repository; Contents R, Metadata R, Administration R, Actions R; expiry ≤ 365 days), and a third PAT `strange-company-mirror-read` (Contents R, Metadata R) for the restore test (`SC-GITHUB-MIRROR-READ`); put it in `GITHUB_MIRROR_READ_TOKEN` (CD-19: everything needed before transfer is in `bootstrap.env`). Put the probe token in `GITHUB_PROBE_TOKEN`.

#### 14.1.2 Repository settings ("read-only mirror" baseline)

| Setting | Value | Why |
|---|---|---|
| Visibility | private (unless `GITHUB_MIRROR_ALLOW_PUBLIC=true`) | §13.1 |
| Default branch | `main` | black start clones the default branch |
| Ruleset `sc-history` (target branches `main`, `release/*`, `sc-canary/*`; **no bypass actors**; enforcement active) | rules: block force pushes (non-fast-forward), restrict deletions | 17-R-2. With no bypass, not even the mirror can rewrite or delete these branches |
| Ruleset `sc-mirror-only` (target: all branches and all tags; bypass: team `sc-mirror` (always)) | rules: restrict creations, restrict updates, restrict deletions | 17-R-3: only the mirror identity can create, update or delete any ref |
| Issues, Wiki, Projects, Discussions | disabled | §7.5 |
| Actions | allowed: "actions created by GitHub + allowlisted actions" as plan 52 lists; workflow permissions: **read repository contents**; "Allow GitHub Actions to create and approve pull requests": off; no repository or environment secrets | Actions are advisory after P08 (ADR-0018). A workflow run on the mirror must not be able to write to it |
| Dependabot security updates / version updates | off (Renovate runs on Forgejo, plan 23) | Dependabot branches would be blocked by `sc-mirror-only` and are noise |
| Secret-scanning push protection | off for this repository | A false positive on a test fixture would block the mirror. Secret detection runs in Forgejo CI before merge (plan 23/52). Secret-scanning *alerts* may stay on |
| Description | "Read-only mirror. Primary: https://git.<BASE_DOMAIN>/…" | §7.5 |
| Canary branch | `sc-canary/protection` created once by `sc-bootstrap mirror check-github --init-canary` (as the mirror identity) at the current `main` SHA | 17-AC-8 proves `sc-history` is enforced without touching `main` |

Ruleset semantics (bypass by team, rule names, "restrict updates" blocking ordinary pushes for non-bypass actors) are version-sensitive GitHub behavior (verify; 17-AC-7 and 17-AC-8 re-verify).

**Tier `unavailable-accepted`:** no rulesets are possible. Then the only writers are the repository owner and the machine user, history protection on GitHub is absent, and 17-R-2 is enforced only by Forgejo-side controls plus divergence detection (`SCGitHubMirrorDiverged`) and the ADR-0554 recovery-mode mirror disablement. The settings check reports `kind="history_protection"` as accepted rather than failing.

#### 14.1.3 Preflight PF-16 (plan 50 runs; this plan defines the sub-checks)

| Sub-check | Pass condition | Failure |
|---|---|---|
| PF-16a token valid | `GET /user` 200 with the mirror token; owner login recorded | fail `GITHUB_TOKEN_INVALID` |
| PF-16b repository | `GET /repos/{o}/{r}` 200, `permissions.push: true`, `archived: false` | fail `GITHUB_REPO_UNUSABLE` |
| PF-16c visibility | `private: true`, or `GITHUB_MIRROR_ALLOW_PUBLIC=true` | fail `GITHUB_REPO_PUBLIC` |
| PF-16d scope | `GET /user/repos` with the mirror token returns exactly this repository (verify fine-grained listing semantics) | warn `GITHUB_TOKEN_OVERSCOPED` (fail if > 1 repository with push) |
| PF-16e expiry | expiry header present and ≥ 30 days away, or absent (no expiry) with a warning | warn `GITHUB_TOKEN_EXPIRING` (< 30 d), fail (< 3 d) |
| PF-16f history | **before `forge-initialized`:** GitHub `main` (`git ls-remote`) is empty, or is an ancestor of local `HEAD`. **After** (GitHub is downstream, BOOT-21): local `HEAD` is an ancestor of, or equal to, Forgejo `main` (fall back to GitHub `main` when Forgejo is unreachable); otherwise fail with the hint `git pull` | fail `GITHUB_DIVERGED` |
| PF-16g tags | before `forge-initialized` only: every GitHub tag exists locally with the same target object | fail `GITHUB_TAGS_DIVERGED` (the forced tag push would overwrite them) |
| PF-16h stray branches | GitHub branches outside `main`, `release/*`, `sc-canary/*` | warn `GITHUB_STRAY_BRANCHES` with the list (they are left untouched by the mirror; operator may delete them) |
| PF-16i settings | §14.1.2 checks via the probe token (`check-github`) | warn at preflight (`GITHUB_SETTINGS_INCOMPLETE`); required at GATE-P06 |
| PF-16j probe token | `GET /repos/{o}/{r}` 200 with the probe token, `GET .../actions/permissions` 200, `GET .../actions/runs?per_page=1` 200 (Actions: read) | fail `GITHUB_PROBE_TOKEN_INVALID` (unless repository public and token unset: warn) |

### 14.2 forge-init step S17 (plan 16 calls M0–M5)

| Step | Action | Idempotency check | Failure |
|---|---|---|---|
| M0 | Re-run PF-16a/b/f/g (cheap) against the now-current Forgejo `main` instead of local HEAD. In `recover` with the typed confirmation `seed-without-github` (GitHub platform outage, plan 45 S19b / plan 50), M0–M4 are skipped and the mirror stays disabled until `forge-reconnect` (DR-14) | — | `GITHUB_DIVERGED`, `GITHUB_UNREACHABLE` |
| M1 | `GET push_mirrors`. If a mirror with `remote_address == https://github.com/<repo>.git`, `branch_filter == "main,release/*"`, `sync_on_commit == true`, `interval == "10m0s"` exists → `verified`. A mirror with that address but other settings, or extra mirrors → delete them (they are forge-init's own leftovers) | expected mirror exists exactly once | API error |
| M2 | If absent: `POST push_mirrors` `{remote_address, remote_username: <PF-16a login>, remote_password: <token>, interval: "10m0s", sync_on_commit: true, use_ssh: false, branch_filter: "main,release/*"}` | — | `FORGE_API_ERROR`; a 4xx naming `branch_filter` → `FORGE_MIRROR_FILTER_UNSUPPORTED` (fatal: the selected Forgejo version violates §5) |
| M3 | `POST push_mirrors-sync`; poll `GET push_mirrors` every 5 s until `last_update` > M3 start (timeout 5 min) | — | timeout → `GITHUB_UNREACHABLE` (non-fatal at P05: `mirror: pending`) |
| M4 | Require `last_error == ""`; then GitHub `main` (API, mirror token) == Forgejo `main` | equal | `last_error` text recorded; classify (§15) → `GITHUB_PUSH_REJECTED` / `GITHUB_AUTH_FAILED` |
| M5 | Verify only (SEED-9): `config/github-mirror.yaml` was written **in S15** with `mirrorTokenExpiresAt` and `mirrorTokenOwner` computed from PF-16a/e before S15, so the outputs commit already contains it; M5 checks that the token used in M2 has the same owner and expiry and records `mirror: {id, state, github_main_sha}` in the forge-init result. It never commits | file matches | mismatch (token replaced mid-run) → `GITHUB_AUTH_FAILED`; rerun forge-init |

The mirror is created **after** S16 pushed the outputs commit, so the first sync carries `main` including forge-init outputs, and GitHub's first post-bootstrap `main` already contains the cluster directory that black start needs.

### 14.3 `mirror-verify` (plan 19 C8, plan 50 V-08, GATE-P06)

1. Read Forgejo `main` SHA `F`. Trigger `push_mirrors-sync`; wait ≤ 5 min until `last_update` advances with `last_error == ""`.
2. Assert GitHub `main` == `F` (API, probe token). Else fail `MIRROR_MAIN_MISMATCH`.
3. Create lightweight tag `sc-mirror-probe/<run-id>` at `F` on Forgejo (`POST /repos/.../tags` as `sc-admin`). Tags are always mirrored and leave `main` untouched. Wait (sync-on-commit) ≤ 5 min for the tag on GitHub. Else fail `MIRROR_PROBE_NOT_PROPAGATED`.
4. Delete the tag on Forgejo; trigger sync; wait ≤ 5 min for it to disappear on GitHub. If it does not, record `prune: not-propagated`, delete it on GitHub with the mirror token, and continue (the check still passes; plan 50 OQ-50-3 resolved by measurement).
5. Run `check-github` (§14.1.3 PF-16i, j). Required to pass at P06 and P12.
6. Emit a `mirror-verify` record into the journal: `{github_main_sha, forgejo_main_sha, probe_tag_latency_seconds, prune, settings: {...}, result}`.

### 14.4 Steady state: freshness probe (`platform-probes` module `github-mirror`)

Every 60 s:
1. Forgejo `GET push_mirrors` → find the declared mirror. Export `sc_github_mirror_configured` (exactly one mirror with the declared address, filter and `sync_on_commit`), `sc_github_mirror_last_attempt_timestamp_seconds` (= `last_update`), `sc_github_mirror_last_attempt_success` (`last_error == ""`). When success is observed with a `last_update` newer than the stored value, set `sc_github_mirror_last_success_timestamp_seconds`.
2. Forgejo `GET /repos/.../branches/main` → `F`.

Every 300 s, and additionally (at most once per 60 s) when `F` changed:
3. GitHub `GET /repos/{o}/{r}/branches/main` (ETag) → `G`. On API failure keep the last known `G` and set `sc_github_api_up 0`.
4. If `G == F`: lag 0, diverged 0, clear `unsynced_since`.
5. Else determine ancestry via Forgejo: `GET /repos/.../compare/{G}...{F}` (verify endpoint; fallback: walk `GET /repos/.../commits?sha=F&limit=50` up to 10 pages looking for `G`):
   - `G` is an ancestor of `F` → GitHub is behind. If `unsynced_since` is unset, set it to `now`, **except** on the first evaluation after probe start, where it is set to the committer time of the oldest first-parent commit of `F` not reachable from `G` (conservative: may overstate, never understates). `sc_github_mirror_lag_seconds = now − unsynced_since`.
   - `G` unknown to Forgejo, or not an ancestor of `F` → `sc_github_mirror_diverged 1`; lag continues to grow from `unsynced_since`.

Every 3600 s:
6. Settings check (§14.1.2) with the probe token → `sc_github_repo_setting_ok{kind}` for kinds `visibility`, `default_branch`, `history_protection` (canary branch has an active non-fast-forward rule and deletion rule), `mirror_only` (an active creation/update/deletion restriction applies to `main`), `actions_policy` (default workflow permission is read), `features` (issues and wiki disabled). With tier `unavailable-accepted`, `history_protection` and `mirror_only` export 1 and `sc_github_mirror_protection_accepted_gap 1`.
7. Export `sc_github_mirror_token_expiry_timestamp_seconds` from `mirrorTokenExpiresAt` (absent if null).

Module `ci-signals` (every 300 s, from P08; signals defined by plan 52 §21):
8. GitHub `GET /repos/{o}/{r}/actions/workflows/{offclusterWorkflow}.yaml/runs?branch=main&status=completed&per_page=10` (probe token, Actions: read) → newest completed run and newest successful run whose jobs ran; Forgejo `GET /repos/{fo}/{fr}/commits?sha=main&limit=20` + `GET /repos/{fo}/{fr}/commits/{sha}/statuses` (sc-admin probe token) → newest `nightlyContext` status; the quarantine file is read from the mounted policy ConfigMap (plan 52). Exported as the `sc_ci_*` metrics in §21.1; alerts `SCCINightlyFailing`, `SCCIOffclusterLaneFailing`, `SCCIOffclusterLaneStale` are plan 52's.

### 14.5 Credential rotation (summary; procedure `rotate-forge-credentials.md` §G)
1. Operator creates the new PAT (same settings) and updates `bootstrap.env`.
2. `./bootstrap exec -- sc-bootstrap mirror recreate --token-from env`: delete the mirror, create it with the new token (M2), sync, verify (M3–M4). The gap is < 1 min. Pushes in the gap are caught by the post-create sync.
3. PR updating `mirrorTokenExpiresAt` in `config/github-mirror.yaml`.
4. `./bootstrap rotate refresh-bundle` (plan 35/50) so the bundle holds the new token.
5. Revoke the old PAT in GitHub. Verify the next `mirror sync` still succeeds.

### 14.6 Upstream updates (SI-11)
GitHub is downstream after control transfer, so nothing that lands on GitHub is ever merged back automatically, and anything committed on GitHub by a bypassing administrator is detected as divergence and would be force-overwritten on tags or rejected on `main` (17-R-2).

Changes from an upstream project (for example, the public Strange Company repository this installation was cloned from) enter like any other change:
1. Optional in-forge visibility: with `upstream.enabled: true`, `forge.yaml` declares a Forgejo **pull mirror** repository `strange-company/upstream` (interval 24 h, read-only). Its sync failure affects only that repository (SPEC §32 holds).
2. A human with write access runs, through the toolbox: `git fetch https://git.<d>/strange-company/upstream.git main` (or the upstream URL directly), creates branch `upstream-sync/<YYYYMMDD>` from the merge of upstream `main` into the current `main` (conflicts resolved locally), and pushes it to Forgejo over HTTPS with a personal access token.
3. A PR `upstream-sync/<YYYYMMDD>` → `main` goes through Woodpecker CI and human review like any change (ADR-0019). Protected-path changes (`policy/**`, `.woodpecker/**`, etc.) are reviewed explicitly.
4. After merge, the mirror publishes it to GitHub. The `upstream-sync/*` branch is never mirrored.

No Strange Company component pulls from GitHub automatically.

### 14.7 Black-start role (plan 45 owns the procedure; this plan owns these guarantees)

| ID | Guarantee | Mechanism | Verified by |
|---|---|---|---|
| BG-1 | GitHub `main` contains every commit that has been on Forgejo `main` for longer than the alerting horizon; normally lag is seconds | sync-on-commit + 10 min interval; `SCGitHubMirrorStale` (15 min) and `SCGitHubMirrorRPOBreach` (1 h) | 17-AC-2, 17-AC-14 |
| BG-2 | GitHub history on `main` and `release/*` is never rewritten or deleted, even by a misconfigured or restored-older Forgejo | ruleset `sc-history` (no bypass) + ADR-0554 recovery mode | 17-AC-8 |
| BG-9 | GitHub content is not trusted blindly at black start: the mirror credential can **append** commits (it cannot rewrite), so `recover` compares GitHub `main` with the newest `repo-backups` manifest `refs/heads/main` (plaintext, written by a put-only credential) and requires a typed acknowledgement for any extra commit (DR-16) | plan 45 §13/§14.2 step 1 | plan 45 AC with a fixture extra commit |
| BG-3 | `main` alone is sufficient for black start: SPEC, plans, procedures, `gitops/` including `gitops/clusters/<cluster>/` (forge-init outputs, recovery-relevant config), SOPS files, toolbox Containerfile, `infra/` | everything is committed to `main` before transfer (plan 16 S15/S16); no LFS (17-R-5) | plan 44 black-start test (§20.2) |
| BG-4 | No plaintext secret is on GitHub; every SOPS file decrypts with the offline recovery identity | ADR-0012, CI `no-plaintext-github-token` + plan 23 secret scan | §20.2 assertions 4–5 |
| BG-5 | The mirror credential survives cluster loss | recovery bundle + `bootstrap.env` | plan 35 bundle manifest lists `SC-GITHUB-MIRROR` |
| BG-6 | A recovering installation never pushes to GitHub before refs are reconciled | ADR-0554: mirrors disabled in recovery mode; `forge-reconnect` runs §20.3 (fast-forward only) | ADR-0554 drill |
| BG-7 | Destroying compute leaves GitHub untouched; destroy refuses when GitHub is behind unless explicitly accepted | plan 49 guard via `mirror freshness` | 49-AC-10 |
| BG-8 | The GitHub copy is independent of Strange Company accounts: the operator's own GitHub account administers it | §14.1 | — |

## 15. Failure modes

| # | Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|---|
| M-F1 | GitHub outage or network path down | `last_error` (connection/timeout); `sc_github_api_up 0`; blackbox `probe_success{probe="github"}` (plan 38); `SCGitHubMirrorFailing` after 15 m | GitHub falls behind; Forgejo unaffected | Forgejo retries every 10 min; probe keeps last-known `G` and lag grows | `github-mirror-failure.md` §A |
| M-F2 | Token expired or revoked | `last_error` contains `401`/`Authentication failed`/`Invalid username or password` (classification table in the procedure); `SCGitHubMirrorTokenExpiring` beforehand | as M-F1 | none | §B → `rotate-forge-credentials.md` §G |
| M-F3 | Token lacks Workflows permission and a commit touches `.github/workflows/**` | `last_error` contains `refusing to allow a Personal Access Token to create or update workflow` (verify text) | mirror stuck at the commit before | none | §B (re-issue token with Workflows R/W) |
| M-F4 | GitHub rejects non-fast-forward or deletion on a protected branch (Forgejo `main` rewritten, or Forgejo restored older than GitHub) | `last_error` contains `protected branch`/`Cannot force-push`/`GH013` (verify); `SCGitHubMirrorDiverged` | GitHub keeps the newer history (by design, BG-2); mirror stalled | none | §C Diverged |
| M-F5 | Someone wrote directly to GitHub (admin bypass, tier `unavailable-accepted`) | `SCGitHubMirrorDiverged`; next mirror push to `main` rejected (non-FF) or GitHub-only tag overwritten | GitHub ≠ Forgejo | none | §C |
| M-F6 | Push mirror missing or misconfigured (deleted in UI, filter changed, `sync_on_commit` off) | `sc_github_mirror_configured 0` → `SCGitHubMirrorNotConfigured`; `SCForgeConfigDrift{object="push_mirror"}` (plan 16) | no mirroring | none | §D Recreate |
| M-F7 | Forgejo `SECRET_KEY` mismatch after restore (credential undecryptable) | `last_error` mentions decrypt/cipher, or every push fails auth after a restore | as M-F2 | none | §D Recreate; `restore-forgejo.md` R9 |
| M-F8 | Forgejo queue item lost (LevelDB queue loss, pod restart during push) | lag grows beyond 10 min with no new `last_update` → `SCGitHubMirrorStale` | delayed until next interval | interval sync | none, or §A if persistent |
| M-F9 | GitHub repository renamed, transferred, archived or deleted | `last_error` contains `Repository not found`/`404`/`archived`; settings check fails | no off-cluster seed | none | §E Repository lost (create a new repository, full push from Forgejo; update `GITHUB_MIRROR_REPO`) |
| M-F10 | GitHub repository made public | `sc_github_repo_setting_ok{kind="visibility"} 0` → `SCGitHubRepoSettingsDrift` (critical) | encrypted secrets world-readable; no plaintext secret exposure if BG-4 holds | none | §F Settings drift (+ `credential-compromise.md` review) |
| M-F11 | Rulesets removed or weakened | `sc_github_repo_setting_ok{kind=~"history_protection\|mirror_only"} 0` | BG-2/17-R-3 not enforced | none | §F |
| M-F12 | Rogue tag pushed to Forgejo by a worker key | Runner foreign-ref detection (`POLICY_VIOLATION`); tag visible on GitHub | tag briefly on GitHub | Runner deletes the tag; mirror prunes it | `agent-runner-failure.md` (plan 26); §G Tag cleanup |
| M-F13 | Probe down or GitHub API rate-limited | `SCGitHubMirrorSignalMissing` (absent series 30 m); `SCPlatformProbesDown` (plan 15) | blind spot; mirror itself unaffected | probe backs off on `403`/`429` honoring `Retry-After`/`x-ratelimit-reset` | §H Signal missing |
| M-F14 | Probe token expired | `sc_github_api_up 0` with 401 → `SCGitHubMirrorSignalMissing` eventually, lag frozen at last known | freshness blind; Forgejo status still exported | none | `rotate-forge-credentials.md` §G.4 |
| M-F15 | Push exceeds timeout (huge push) | `last_error` contains `timeout`/`killed` | mirror stalled | none | §A (check repository size; 17-R-5) |
| M-F16 | GitHub secret-scanning push protection enabled by org policy | `last_error` contains `GH009`/`push protection` (verify) | mirror stalled | none | §F (disable for the repository or remove the false positive by PR) |
| M-F17 | Mirror credential leaked | GitHub audit log / secret-scanning alert / unexplained divergence | attacker can create refs and tags; cannot rewrite protected history (BG-2) | none | `credential-compromise.md` (plan 35) + `rotate-forge-credentials.md` §G |

## 16. Retry behavior

- Forgejo retries a failed push at every interval (10 min) and on every new push (sync-on-commit). There is no tight loop.
- `sc-bootstrap mirror sync/verify` waits are bounded (5 min each). forge-init M3/M4 failures are non-fatal at P05 (`mirror: pending`) and fatal at P06; `./bootstrap` resume re-runs them. After 3 identical failures plan 50 stops with `NEEDS_OPERATOR`.
- GitHub API calls from the probe and the CLI: 3 attempts with exponential backoff (2, 4, 8 s) on 5xx and connection errors; on `403` with `x-ratelimit-remaining: 0` or `429`, wait until reset (max 15 min) and export `sc_github_api_up 0` meanwhile.
- Non-fast-forward rejections are never retried with force by any Strange Company tool.

## 17. Idempotency behavior

- M1–M5 are check-then-act. A second forge-init run on a configured mirror performs no mutation (part of 16-AC-17).
- `mirror recreate` is safe to repeat: delete-if-present, create, sync.
- `mirror-verify` creates a uniquely named probe tag and always deletes it (on Forgejo and, if needed, on GitHub), including on failure paths. A leftover `sc-mirror-probe/*` tag from an interrupted run is deleted at the start of the next run.
- The probe is read-only on both sides.

## 18. Bootstrap behavior

1. **Before P05 (Phase 1, ADR-0018):** GitHub is the origin and runs interim CI. The operator prepares GitHub per §14.1 immediately before the first `./bootstrap`, after the last direct push to GitHub.
2. **Preflight:** PF-16a–j.
3. **forge-init (P05):** S08 pushes local `HEAD` to Forgejo; S15/S16 push the outputs commit; S17 = M0–M5 creates the mirror and performs the first sync. GitHub unreachable → `mirror: pending`, P05 still passes.
4. **Control transfer (P06):** after plan 19's source switch passes, `mirror-verify` (§14.3) runs. GATE-P06 requires it (17-AC-2..10).
5. **P06 (after transfer):** `platform-probes` (plan 15) starts with module `github-mirror`; **P08:** module `ci-signals`; **P09:** alerts live (GATE-P09 contributions).
6. **P12:** V-08 = `mirror-verify` again; protection stage `steady` (plan 16 finalize) means `main` only changes by reviewed merges from now on.
7. **Black start:** the operator clones GitHub `main` (BG-3). `./bootstrap recover` uses `SC-GITHUB-MIRROR` from the bundle only after ADR-0554 fencing; Forgejo starts with mirrors disabled; `forge-reconnect` runs §20.3.

## 19. Upgrade behavior

- **Forgejo upgrades** (plan 16 §19) re-run the k3d mirror suite (§22): `branch_filter` refspecs, tags always pushed, pruning within filtered namespaces, `last_update`/`last_error` semantics, credential storage location, sync-on-commit latency. A changed behavior fails the upgrade PR.
- **GitHub API changes:** the pinned `X-GitHub-Api-Version` is bumped by PR. The live lane (`make e2e-github-live`, opt-in, §22) re-verifies endpoints and ruleset behavior against a throwaway repository.
- **Changing the filter or address:** PR to `forge.yaml` → `forge apply` performs delete + create + verify (§14.5 mechanics). Adding a namespace to the filter requires a security review note (agent refs must never match).

## 20. Recovery behavior

### 20.1 Mirror-only recovery
Any mirror defect is fixed by `mirror recreate`: the mirror is stateless apart from its credential, and GitHub keeps its history (BG-2).

### 20.2 GitHub black-start restore test (plan 44 RT-6 `restore-test-github-black-start` in `dr-tests`, weekly; plan 44 owns the test, this plan owns the guarantees it checks)

RT-6 runs with `SC-GITHUB-MIRROR-READ` (read-only, never the mirror or probe token). It must cover these guarantees; plan 44 §RT-6 is the authoritative step list:
1. Clone succeeds and `git fsck --full` passes (BG-3).
2. `refs-in-sync` for `main`, `release/*` and tags against Forgejo (BG-1; lag tolerance 60 min).
3. Black-start files exist on `main` (`bootstrap`, `toolbox/Containerfile`, `toolbox/tools.lock.yaml`, `gitops/clusters/<cluster>/`) and the offline render (`sc-gitops render --cluster <cluster>`) passes (BG-3).
4. Every `*.sops.yaml` lists exactly the two age recipients (structural check, CD-10: no in-cluster copy of any private age key; decryption is proven by plan 35's `restore-test-recovery-bundle` and the operator's quarterly `recovery-bundle verify --full`) (BG-4).
5. No file matches `github_pat_`/`ghp_`, and `.gitattributes` has no `filter=lfs` (BG-4, 17-R-5). Plan 44 adds this step to RT-6 (Q-17-10).
Evidence: `restore-test-result` with `test_id: rt-github-mirror`. Failure raises `SCGitHubBlackStartTestFailed`.

### 20.3 Reconnect after Forgejo restore or black start (`forge-reconnect`, ADR-0554)
1. Keep mirrors disabled (`[mirror] ENABLED=false` in the recovery component; verify key).
2. `mirror compare-refs`. For `main` and each `release/*`: if GitHub is an ancestor of Forgejo → OK. If Forgejo is an ancestor of GitHub (GitHub newer, the usual case after a Forgejo restore) → fast-forward Forgejo from GitHub (`git fetch github; git push forgejo <sha>:refs/heads/<b>` as `sc-admin` under the recovery push allowlist). If diverged → **halt** for the operator (`github-mirror-failure.md` §C).
3. Tags: GitHub-only tags are fetched into Forgejo before re-enabling (otherwise the forced tag push and prune would delete them).
4. Re-enable mirrors (remove the recovery setting by the finalize PR, or `forge apply` while still in recovery when the operator chooses), then `mirror recreate` (the restored DB credential may be stale) and `mirror verify`.

## 21. Observability

### 21.1 Metrics (`platform-probes`, job `platform-probes`; label `repository="strange-company"` only; plan 38 §21.1 vocabulary)

| Metric | Type | Labels | Meaning | In plan 38 catalogue |
|---|---|---|---|---|
| `sc_github_mirror_last_attempt_success` | gauge | `repository` | 1 if the latest push-mirror attempt succeeded (`last_error == ""`) | yes |
| `sc_github_mirror_last_success_timestamp_seconds` | gauge | `repository` | `last_update` of the latest observed successful attempt (absent until one is observed) | yes |
| `sc_github_mirror_lag_seconds` | gauge | `repository` | Age of the oldest Forgejo `main` commit not on GitHub `main` (0 when in sync; definition §14.4) | yes |
| `sc_github_mirror_last_attempt_timestamp_seconds` | gauge | `repository` | Forgejo `last_update` | **add** |
| `sc_github_mirror_configured` | gauge | `repository` | 1 if exactly one mirror matches the declared address, filter and sync-on-commit | **add** |
| `sc_github_mirror_expected` | gauge | `repository` | `spec.expectMirror` | **add** |
| `sc_github_mirror_diverged` | gauge | `repository` | 1 if GitHub `main` is not an ancestor of Forgejo `main` | **add** |
| `sc_github_api_up` | gauge | `repository` | 1 if the last GitHub API call succeeded | **add** |
| `sc_github_repo_setting_ok` | gauge | `repository`, `kind` | per §14.4 step 6 | **add** |
| `sc_github_mirror_token_expiry_timestamp_seconds` | gauge | `repository` | mirror PAT expiry from config | **add** |
| `sc_github_mirror_protection_accepted_gap` | gauge | `repository` | 1 when tier is `unavailable-accepted` | **add** |

| `sc_github_mirror_config_valid` | gauge | `repository` | 1 if `github-mirror-config` validates against its schema | **add** |
| `sc_ci_offcluster_lane_last_success_timestamp_seconds` | gauge | — | `ci-signals`: completion time of the newest successful GitHub Actions run of workflow `e2e` on `main` that ran its jobs (not a schedule-skip) (plan 52 §21) | plan 52 |
| `sc_ci_offcluster_lane_last_run_conclusion` | gauge | `conclusion` | `ci-signals`: 1 for the conclusion of the newest completed `e2e` run on `main` (`success`, `failure`, `cancelled`, `skipped`), 0 for the others | plan 52 |
| `sc_ci_nightly_last_conclusion` | gauge | `conclusion` | `ci-signals`: newest Forgejo commit status `ci/woodpecker/cron/nightly` among the last 20 `main` commits (`success`, `failure`, `error`, `pending`) | plan 52 |
| `sc_ci_quarantine_entries` | gauge | — | `ci-signals`: entries in `policy/test-quarantine.yaml` at the deployed revision | plan 52 |

`kind` values: `visibility`, `default_branch`, `history_protection`, `mirror_only`, `actions_policy`, `features` (bounded set). Plan 38 must allow `repository` on these added `sc_github_*` metrics as well.

### 21.2 Logs and dashboards
- Forgejo logs mirror failures at error level (`SyncPushMirror`/`runPushSync` messages); the push-mirror `last_error` is the canonical text.
- Probe logs (JSON): one line per state change (sync observed, divergence, settings change), never the token.
- Grafana (plan 39) Forgejo row: mirror status, lag, last success age, token days remaining, settings checks.

### 21.3 Alerts (catalogue names; plan 42 registers routing; `signalOwnerPlan: "17"`)

| alertname | Expr (sketch) | for | severity / class | alwaysNotify | Catalogue |
|---|---|---|---|---|---|
| `SCGitHubMirrorFailing` | `sc_github_mirror_last_attempt_success{repository="strange-company"} == 0 and sc_github_mirror_expected == 1` | 15m | warning / notify | failed-mirror | catalogue |
| `SCGitHubMirrorStale` | `sc_github_mirror_lag_seconds{repository="strange-company"} > 900` | 5m | warning / notify | failed-mirror | catalogue |
| `SCGitHubMirrorRPOBreach` | `sc_github_mirror_lag_seconds{repository="strange-company"} > 3600` | 5m | critical / page | black-start-threat | catalogue |
| `SCGitHubMirrorSignalMissing` | `absent(sc_github_mirror_last_attempt_success{repository="strange-company"}) or sc_github_api_up == 0` | 30m | warning / notify | failed-mirror | catalogue |
| `SCGitHubMirrorDiverged` | `sc_github_mirror_diverged{repository="strange-company"} == 1` | 5m | critical / page | black-start-threat | catalogue |
| `SCGitHubMirrorNotConfigured` | `sc_github_mirror_configured == 0 and sc_github_mirror_expected == 1` | 15m | critical / page | black-start-threat | catalogue |
| `SCGitHubRepoSettingsDrift` / `SCGitHubRepoSettingsDriftMinor` | `min by (kind) (sc_github_repo_setting_ok{kind=~"visibility\|history_protection\|mirror_only"}) == 0` / same with `kind!~` | 30m | critical / page; warning / notify | black-start-threat / none | catalogue |
| `SCGitHubMirrorTokenExpiring` / `SCGitHubMirrorTokenExpiryImminent` | `min(sc_github_mirror_token_expiry_timestamp_seconds) - time() < 21*86400` / `< 3*86400` | 1h | warning / critical | failed-mirror | catalogue |
| `SCGitHubMirrorRecoveryModeTooLong` | `sc_github_mirror_expected == 0` | 7d | warning / notify | black-start-threat | catalogue |

The assignment's names "SCGitHubMirrorStale"/"GitHubMirrorFailing" map to `SCGitHubMirrorStale`/`SCGitHubMirrorFailing` (catalogue `SC` prefix convention). Runbook for all: `procedures/github-mirror-failure.md`.

## 22. Testing strategy

All CI lanes are deterministic and need no paid model API (SPEC §104).

- **Unit (Go, probe module and `sc-bootstrap mirror`):** table tests with fake Forgejo and GitHub HTTP servers (recorded fixtures): in sync; GitHub behind by 1 and by 600 commits; diverged (GitHub unknown SHA; GitHub non-ancestor); GitHub API 401/403-rate-limit/500; mirror missing, duplicated, wrong filter; `last_error` classification strings (fixtures captured from real Forgejo/GitHub responses at implementation); lag initialization after restart never underestimates; settings evaluation per tier.
- **Promtool:** unit tests for every alert in §21.3 with synthetic series (including the `expected` guard).
- **Contract (F02):** `GitHubMirrorConfig` example validates; `forge.yaml` `pushMirrors` element validates.
- **Integration (k3d, `make e2e-mirror`, plan 52):** Forgejo (pinned version) + CNPG + SeaweedFS `s3-emulator` (CD-12) + a local smart-HTTP git server (`git http-backend` behind a digest-pinned image) standing in for GitHub, with a pre-receive hook rejecting non-fast-forward on `main`. Asserts: (1) filter pushes `main`, `release/*` and tags only; `agent/x`, `renovate/x`, `sc-verify/x` absent on the target; (2) deleting a `release/x` branch and a tag propagates (prune) and branches outside the filter on the target are untouched; (3) `last_update` advances on failure and `last_error` is set (target returns 401); (4) sync-on-commit latency < 60 s; (5) non-FF rejection appears in `last_error` and does not alter the target; (6) with the target unreachable (NetworkPolicy deny), Forgejo healthz passes, pushes over SSH succeed, Flux-like clone works, and after restoring reachability the target catches up within one interval; (7) whether the credential appears in the bare repo `config` (records, does not fail).
- **Live lane (`make e2e-github-live`, opt-in, needs a throwaway GitHub repository and tokens in the operator's environment; not part of default CI):** rulesets enforcement, Workflows-permission rejection text, PAT expiry header, `/user/repos` scope listing. Run before P06 on each new GitHub API version pin.
- **Live gates:** §23.

## 23. Acceptance criteria

`$FJ` = `./bootstrap exec -- sc-bootstrap forge api` (authenticated Forgejo API helper, plan 50); `$GH` = `./bootstrap exec -- sc-bootstrap mirror gh-api` (GitHub API with the probe token). Repository `strange-company/strange-company`.

### GATE-P05 (contributed)
- **17-AC-1** Mirror configured: `$FJ GET /repos/strange-company/strange-company/push_mirrors | jq` shows exactly one entry with `remote_address == "https://github.com/<GITHUB_MIRROR_REPO>.git"`, `branch_filter == "main,release/*"`, `sync_on_commit == true`, `interval == "10m0s"`. The forge-init journal has `mirror.state ∈ {synced, pending}`.

### GATE-P06 (owner of the mirror part)
- **17-AC-2** Mirror receives commits: `./bootstrap exec -- sc-bootstrap mirror verify` exits 0. Its journal record shows `github_main_sha == forgejo_main_sha` and `result: pass`. `$FJ GET .../push_mirrors` shows `last_error == ""` and `last_update` within the last 15 min.
- **17-AC-3** Tags propagate and prune is measured: the same `mirror-verify` record shows `probe_tag_latency_seconds ≤ 300` and `prune ∈ {propagated, not-propagated}`; afterwards `$GH GET /repos/<repo>/git/matching-refs/tags/sc-mirror-probe/` returns `[]`.
- **17-AC-4** No agent ref leaks: as `sc-admin`, push branch `agent/sc-0-mirror-gate` pointing at `main`; trigger `mirror sync`; after 120 s `$GH GET /repos/<repo>/branches/agent%2Fsc-0-mirror-gate` returns 404. Then delete the branch on Forgejo. A tag push by a test write deploy key (`git push <key> refs/tags/sc-gate-tag`) is rejected by Forgejo's protected-tag rule (exit ≠ 0); if it is accepted, this AC **fails** (SEC-1): the Forgejo version or tag-protection configuration must be fixed before P06 passes; 17-AC-4b re-checks it at P11.
- **17-AC-5** Credential placement: `git grep -c -E 'github_pat_|ghp_' $(git rev-list --all)` on Forgejo `main` history = 0; `kubectl get secrets -A -o json | jq` (toolbox, decoded values hashed and compared with sha256 of `GITHUB_MIRROR_TOKEN`) finds 0 matches; the recovery-bundle manifest lists `SC-GITHUB-MIRROR` with the current fingerprint.
- **17-AC-6** Forgejo independence: `make e2e-mirror` case (6) passed for the deployed Forgejo version (JUnit attached to P06 evidence). Live static check: `app.ini` has `OFFLINE_MODE = true`, and `grep -ci github /data/gitea/conf/app.ini` = 0.
- **17-AC-7** GitHub settings: `./bootstrap exec -- sc-bootstrap mirror check-github` exits 0: `private: true` (or allow-public), `default_branch: main`, issues and wiki disabled, Actions default workflow permission `read`, `rules/branches/main` includes the deletion, non-fast-forward and update-restriction rules (tier `rulesets`), or the tier is `unavailable-accepted` and is recorded in `config/github-mirror.yaml`.
- **17-AC-8** History protection enforced (tier `rulesets`): as the mirror identity, `git push --force https://github.com/<repo>.git <main>~1:refs/heads/sc-canary/protection` exits ≠ 0 with a protected-branch/ruleset message, and `git push https://…/<repo>.git :refs/heads/sc-canary/protection` exits ≠ 0. With the **probe** token, `git push … <main>:refs/heads/sc-gate-x` exits ≠ 0 (no write).
- **17-AC-9** Token scope: `GET /user/repos` with the mirror token returns exactly one repository (`<GITHUB_MIRROR_REPO>`); the token's expiry header (or `mirrorTokenExpiresAt`) is ≤ 180 days after its creation date.
- **17-AC-10** Workflows permission present: as the mirror identity, push a commit that adds `.github/workflows/sc-canary.yml` (a no-op `workflow_dispatch` workflow) to `sc-canary/workflows`: accepted (exit 0). The branch is left in place (under `sc-history`) as evidence.
- **17-AC-11** Credential storage recorded: `kubectl -n forgejo exec deploy/forgejo -- git -C /data/git/repositories/strange-company/strange-company.git config --get-regexp '^remote\..*\.url$'` output is examined by the gate tool for userinfo (`https://[^@/]+@`). Result `credential_in_repo_config: true|false` is written to the P06 evidence. If `true`, the S3 `longhorn-backups` access policy (plan 14) is confirmed to exclude every credential except Longhorn and operator-admin.

### GATE-P09 (contributed)
- **17-AC-12** CI guards: `make no-lfs max-file-size no-plaintext-github-token` pass on `main`; a fixture PR adding a file containing `github_pat_` followed by 82 characters fails `no-plaintext-github-token`.
- **17-AC-13** Metrics visible: PromQL `sc_github_mirror_last_attempt_success{repository="strange-company"}` = 1, `sc_github_mirror_lag_seconds{repository="strange-company"}` = 0, `sc_github_mirror_configured` = 1, `sc_github_mirror_diverged` = 0, `min(sc_github_repo_setting_ok)` = 1, `sc_github_api_up` = 1.
- **17-AC-14** Lag measured live: after a merge to Forgejo `main`, `sc_github_mirror_lag_seconds` returns to 0 within 120 s (sampled by `promql max_over_time(...[5m])` returning a value ≤ 120 in the window after the merge).
- **17-AC-15** Required alerts exist: `kubectl -n monitoring get prometheusrule -o json | jq -r '..|.alert? // empty'` contains every alertname in §21.3, and `promtool test rules` for them passes in CI.

### GATE-P10 (contributed)
- **17-AC-16** GitHub black-start test: the plan 44 `restore-test-github-black-start` CronJob's latest Job succeeded within the last 8 days and its `restore-test-result` (`test_id: rt-github-mirror`) records every RT-6 check, including the guarantees 1–5 of §20.2, as `passed`.

### GATE-P11 (contributed; plan 23 owns the gate)
- **17-AC-4b** Protected tags hold against worker keys (SEC-1): re-run the 17-AC-4 tag push and the 16-AC-18 release-branch creation with a fresh write deploy key on the deployed Forgejo version; both are rejected. Any accepted push **fails** this gate (the release lane must not be reachable from a worker key).

### GATE-P12 (contributed)
- **17-AC-17** V-08 passes in the `./bootstrap verify` run that prints `CONTROL TRANSFER COMPLETE`, and at that time `ALERTS{alertname=~"SCGitHub.*",alertstate="firing"}` returns no series.

## 24. Explicit non-goals

- Mirroring any repository other than `strange-company` to GitHub (project repositories are protected by bundles, plan 44).
- Mirroring agent, Renovate, PR or other non-release branches.
- Bidirectional sync or any automatic GitHub → Forgejo merge.
- Flux, the Runner or workers ever reading GitHub.
- GitHub as a second CI authority after P08 (Actions are advisory, ADR-0018).
- Managing GitHub settings with a Strange Company credential that holds administration write (the operator owns GitHub administration).
- A GitHub App–based mirror (ADR-0253) and an in-cluster token refresher.
- An off-cluster mirror-freshness watchdog (master plan §15; plan 42 decides post-MVP). The external dead-man's switch covers total outage.

## 25. Open questions

| ID | Question | Blocking | Proposed resolution | Owner |
|---|---|---|---|---|
| Q-17-01 | GitHub branch protection and rulesets on **private** repositories require a paid plan (verify). SPEC §32 does not require protection, but 17-R-2/BG-2 depend on it. | no | Recommend an organization on a plan with rulesets. Allow tier `unavailable-accepted` with explicit acknowledgement, divergence detection and ADR-0554 recovery-mode disablement as compensating controls. Recorded as SI-G4A-01 for `spec-issues.md`. | 17 / lead |
| Q-17-02 | Do Forgejo protected tags apply to deploy-key pushes on the selected version? | no | 17-AC-4 measures. If not, rely on Runner quarantine + mirror prune (residual risk §13.3, plan 34). | 16 / 34 |
| Q-17-03 | Does the push mirror prune deletions within filtered namespaces (plan 50 OQ-50-3)? | no | `mirror-verify` measures and cleans up either way; the k3d suite asserts the current behavior and fails on change. | 17 |
| Q-17-04 | Is the PAT also persisted in the bare repository's git config (and hence in Longhorn backups)? | no | 17-AC-11 records it; mitigation in §13.2. | 17 / 14 |
| Q-17-05 | Plan 38 assumed a dedicated `github-mirror-watch` scrape target and plan 18 a "mirror reconciler" in `forgejo-config`. | no | This plan uses the `platform-probes` module (plan 15) and `forge apply`. Plans 38/18 update their tables at consolidation. | 38 / 18 |
| Q-17-06 | Plan 50 V-08 and plan 19 C8 describe a probe branch/commit; branches outside the filter are never mirrored. | no | Use the probe tag in §14.3. Plans 50/19 adjust wording. | 50 / 19 |
| Q-17-07 | Three new `bootstrap.env` keys (`GITHUB_PROBE_TOKEN`, `GITHUB_MIRROR_READ_TOKEN` (CD-19), `GITHUB_MIRROR_PROTECTION_TIER`) and one new secret class (`SC-GITHUB-PROBE`). | no | Plan 50 adds the keys. `SC-GITHUB-PROBE` and `SC-GITHUB-MIRROR-READ` are registered in `secret-classes.yaml` (consolidation). | 50 |
| Q-17-08 | Fine-grained PAT maximum lifetime and whether "no expiration" is allowed are GitHub-policy-dependent. | no | Policy: always set an expiry ≤ 180 days; alert 21 days before. | 17 |
| Q-17-09 | `sc-archive/*` and `sc-quarantine/*` tags (plan 55) are mirrored to GitHub because Forgejo always pushes tags. | no (resolved by CD-17) | Accepted for MVP: private repository, no secret content is ever tagged, archive tags preserve TDD provenance. Revisit only if Forgejo can protect non-tag refs. | 55 / 17 |
| Q-17-10 | Plan 44 RT-6 should add the `github_pat_`/`ghp_`/LFS check (§20.2 item 5) and drop its `dr-tests/blackstart-age` decryption step (CD-10). | no | Plan 44 aligns RT-6. | 44 |

| Q-17-11 | A GitHub **platform** outage (not just repository loss) blocks black start: toolbox, k3s, Flux/CNPG images and charts are fetched from GitHub-hosted locations (DR-14). | no | Spec issue proposed; plan 45 S19b decides between waiting and an object-storage black-start artifact cache. This plan's PF-16/M0 skip with `seed-without-github`. | 45 / 50 |