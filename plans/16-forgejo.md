# 16 — Forgejo (engineering ledger) and forge-init

**Status:** Stage-0 plan (draft)
**DAG nodes:** P04 (co-owned with 19, 12, 13, 09, 10: Forgejo healthy on CNPG + Longhorn in the seed), **P05 forge-init (owner)**; consumed by P06 (plan 19/17/18), P07 (plan 15: OIDC), P08 (plan 20: Woodpecker OAuth app), C03 (plan 55/26: bots, webhooks), P10 (plan 44: backups)
**Related plans:** [12-cnpg](12-cnpg.md), [13-longhorn](13-longhorn.md), [14-object-storage](14-object-storage.md), [15-authentik](15-authentik.md), [17-github-mirror](17-github-mirror.md), [18-flux](18-flux.md), [19-seed-handoff](19-seed-handoff.md), [20-woodpecker](20-woodpecker.md), [23-supply-chain](23-supply-chain.md), [26-agent-runner](26-agent-runner.md), [35-secrets](35-secrets.md), [36-network-policy](36-network-policy.md), [42-alerting-notifications](42-alerting-notifications.md), [44-backup](44-backup.md), [45-recovery](45-recovery.md), [48-upgrade](48-upgrade.md), [50-bootstrap-orchestrator](50-bootstrap-orchestrator.md), [55-forge-workflow](55-forge-workflow.md)
**Contracts:** owns `contracts/forge-configuration.schema.yaml` (`forge/forge.yaml`) and `contracts/forge-init-result.schema.yaml` (forge-init journal record); consumes `bootstrap-journal.schema.yaml` (50), `secret-classes.yaml` (35), `alert-catalog.yaml` (42), `restore-test-result.schema.yaml` (44), `github-mirror-config.schema.yaml` (17).
**ADRs:** relies on ADR-0005, 0006, 0007, 0012, 0013, 0016, 0019; introduces **ADR-0252** (Forgejo durable identity material and the forge desired-state file).

> Every section below is mandatory (SPEC §121).

## 1. Purpose

Forgejo is the engineering ledger [SPEC §30]: the system of record for repositories, branches, commits, PRs, issues, review history, releases and OCI/package metadata. This plan defines:

1. The Forgejo deployment: the official forgejo-helm chart, PostgreSQL on CNPG `forgejo-pg`, Git data on a Longhorn PVC, and bulk objects in S3. It has no Redis, no deployable Bitnami subcharts (a vendored template-only `common` library is allowed, CD-26) and no Forgejo Actions.
2. **forge-init**, the bootstrap phase that makes a freshly seeded Forgejo usable. It creates identities, orgs, the `strange-company` repository and its protection, bot credentials, the Flux deploy key and the Woodpecker OAuth application, then writes the generated credentials back into Git as SOPS files before control transfer.
3. The forge desired-state file `forge/forge.yaml`, which keeps orgs, teams, bots, branch protection and webhooks Git-explainable and re-appliable [SPEC §4.2, §4.7].
4. Forgejo upgrade, restore (whole-instance and single-project), and the Longhorn Multi-Attach failure mode (L-5).

## 2. Requirements derived from SPEC.md

1. Forgejo owns repositories, branches, commits, PRs, issues, review history, releases and supported package/OCI metadata [SPEC §30, §101].
2. Forgejo relational state uses CNPG [SPEC §30, §25]. Cluster `forgejo-pg` [ADR-0006].
3. Git repository storage uses Longhorn [SPEC §30, §27].
4. Large suitable blobs use object storage [SPEC §30, §29]: LFS, attachments, packages/OCI blobs, avatars and repo archives go to S3 `<prefix>-forgejo-objects`.
5. No Bitnami dependencies [SPEC §20] as decided by CD-26 (supersedes CD-20): no Bitnami image, no Bitnami chart repository or registry referenced or fetched, no Bitnami chart deployed as a component. A template-only Bitnami library subchart (`common`) vendored inside the forgejo-helm package is allowed if it is `type: library`. See §9.1 selection rule and §25 Q-16-02.
6. Forgejo is created during bootstrap, `strange-company` is pushed there, and Flux is then bootstrapped against it [SPEC §6, §8 `forge-init`, §34, §35].
7. Bootstrap is idempotent, safely resumable, observable and conservative around destructive operations [SPEC §8]. forge-init never deletes repositories, never force-pushes, and never silently resets a break-glass credential.
8. Forgejo authenticates humans through Authentik OIDC [SPEC §21], and machines use scoped credentials [SPEC §21, §86, §87]. The OIDC part is wired in this plan; the Authentik side is plan 15.
9. Break-glass access to Forgejo exists, lives outside the cluster, and produces audit evidence when used [SPEC §22]. The mechanism is defined in plan 15 §14.4; this plan creates the account.
10. Agent branches follow `agent/<task-id>`, and workers cannot push protected branches [SPEC §100, ADR-0007].
11. Forgejo Issues provide the human backlog. Human states are projected as `status/*` labels [SPEC §101, SI-10; semantics owned by plan 55].
12. `strange-company` continuously mirrors to GitHub [SPEC §31, §32]. The mirror is defined in plan 17 and created by a forge-init step.
13. Repository-level offsite protection exists, and single-project recovery does not require a whole-volume restore [SPEC §31]. Bundles are plan 44; the restore path is §20.3.
14. Forgejo operation does not depend on GitHub [SPEC §32]. No Forgejo health check, startup or request path calls GitHub.
15. Forgejo is a Prometheus target with alerts for Forgejo failure [SPEC §91, §96].
16. RPO/RTO: Forgejo DB ≤15 min / ≤4 h; project repos ≤1 h / ≤4 h; `strange-company` ≤1 h (near-zero mirror lag) / ≤2 h [SPEC §108].
17. The SPEC §124 example Forgejo gate is reproduced in full (§23).

Derived requirements:

- **16-R-1** Forgejo's `SECRET_KEY`, `INTERNAL_TOKEN`, `JWT_SECRET` and `LFS_JWT_SECRET` are generated once, before seed, and stored SOPS-encrypted in Git (`SC-FORGEJO-APP-SECRETS`). Forgejo encrypts push-mirror credentials, 2FA secrets and OAuth2 application secrets with `SECRET_KEY`, so a DB restore without the original key leaves them undecryptable. The key must therefore survive PVC loss.
- **16-R-2** The Forgejo SSH host key is generated before seed and stored SOPS-encrypted in Git (`SC-FORGEJO-SSH-HOST-KEY`). Flux `known_hosts` and worker `known_hosts` then stay valid across PVC loss and black start. forge-init *verifies and records* the fingerprint (ADR-0016; see §25 Q-16-03).
- **16-R-3** Forgejo runs as a single replica with the `Recreate` strategy at both profiles. Level queues and the bleve indexer are local to the PVC, so multi-replica Forgejo is out of scope. HA comes from Longhorn replicas plus rescheduling.
- **16-R-4** Every forge object that policy depends on (orgs, teams, bot membership, branch protection, webhooks, push mirror, org labels) is declared in `forge/forge.yaml` and applied idempotently by `sc-bootstrap forge apply`. Manual drift is detected (`SCForgeConfigDrift`, `SCForgeBranchProtectionDrift`).
- **16-R-5** Forgejo Actions is disabled (`[actions] ENABLED=false`). Woodpecker is the CI [SPEC §36].
- **16-R-6** The SSH listener is cluster-internal by default (ADR-0010). It is reachable from `flux-system`, `agent-jobs`, `strange-company` (Runner), `woodpecker`, `ci-jobs` and `forgejo` (backup CronJobs), and from nowhere else.

## 3. Inputs

| Input | Source | Used by |
|---|---|---|
| `BASE_DOMAIN`, `S3_*`, `INSTALL_PREFIX`, `GITHUB_MIRROR_*`, `OPERATOR_*` | normalized `bootstrap.env` (`contracts/bootstrap-config.schema.yaml`, plan 50) | values, forge-init |
| Pre-seed generated secrets: `SC-FORGEJO-ADMIN`, `SC-FORGEJO-APP-SECRETS`, `SC-FORGEJO-SSH-HOST-KEY`, `SC-FORGEJO-METRICS-TOKEN`, `SC-FLUX-DEPLOY-KEY` (plan 18 file `flux-source-auth.sops.yaml`), `SC-RUNNER-WEBHOOK` (plan 26), `SC-OBJECT-STORAGE` (forgejo-objects credential) | plan 50 secret-generation step → SOPS files committed in the local commit that is seeded | HelmRelease |
| Break-glass password `SC-BREAK-GLASS-FORGEJO` | plan 50 operator-side staging (`/run/sc/secrets/`, tmpfs, ADR-0101/CD-08; sealed into the recovery bundle and the recovery-recipient-only file `secrets/offcluster/<cluster>/break-glass-forgejo.sops.yaml` by plan 35) | forge-init S03 |
| `forge/forge.yaml` | Git (this plan) | forge-init, `forge apply` |
| CNPG `forgejo-pg` (Cluster, `forgejo-pg-app` Secret, `forgejo-pg-rw` Service) | plan 12 | Forgejo DB connection |
| Longhorn StorageClass `longhorn` (replicas per profile) | plan 13 | PVC `forgejo-data` |
| S3 bucket `<prefix>-forgejo-objects` with versioning | plan 14 | storage config |
| Traefik + cert-manager (`git.<BASE_DOMAIN>`) | plans 09, 10 | ingress |
| `sc-runner`, Woodpecker and webhook targets (service DNS names) | plans 26, 20 | webhooks, OAuth app redirect URI |

## 4. Outputs

1. A running Forgejo at `https://git.<BASE_DOMAIN>` (HTTP Service `forgejo/forgejo-http:3000`) and an internal SSH Service `forgejo/forgejo-ssh:22`.
2. The org `strange-company` with repository `strange-company/strange-company`, and the org `projects` for managed project repositories (SPEC §62 example `projects/example`).
3. Bot accounts and tokens (`SC-FORGEJO-BOT-TOKENS`), the Flux read-only deploy key (`SC-FLUX-DEPLOY-KEY`), the Woodpecker OAuth2 app (`SC-WOODPECKER-OAUTH`), Runner webhooks (secret `SC-RUNNER-WEBHOOK`, pre-seeded, owner plan 26), branch and tag protection, and the GitHub push mirror (plan 17).
4. **Committed forge-init outputs.** These are SOPS files plus non-secret config, pushed to `Forgejo/strange-company` `main` **before** the source switch (§14.2 S16):

   | Path (cluster overlay; plan 18 owns the tree, plan 35 owns secret file conventions) | Kind | Namespace | Content |
   |---|---|---|---|
   | `gitops/clusters/<cluster>/secrets/strange-company/forgejo-sc-runner.sops.yaml` | Secret `strange-company/agent-runner-forgejo` | strange-company | `token`, `username` |
   | `gitops/clusters/<cluster>/secrets/renovate/forgejo-sc-renovate.sops.yaml` | Secret `renovate/forgejo-sc-renovate` | renovate | `token` |
   | `gitops/clusters/<cluster>/secrets/forgejo/forgejo-sc-backup.sops.yaml` | Secret `forgejo/forgejo-sc-backup` | forgejo | `token` |
   | `gitops/clusters/<cluster>/secrets/forgejo/forgejo-sc-gc.sops.yaml` | Secret `forgejo/forgejo-sc-gc` | forgejo | `token` (`write:package` only; plan 43 `oci-retention`) |
   | `gitops/clusters/<cluster>/secrets/woodpecker/forgejo-sc-ci-config.sops.yaml` | Secret `woodpecker/forgejo-sc-ci-config` | woodpecker | `token` (`read:repository`; plan 20 config extension) |
   | `gitops/clusters/<cluster>/secrets/monitoring/forgejo-platform-probes.sops.yaml` | Secret `monitoring/platform-probes-forgejo` | monitoring | `token` (sc-admin, scopes `read:admin,read:repository,read:user`) |
   | `gitops/clusters/<cluster>/secrets/forge/woodpecker-oauth.sops.yaml` | Secret `woodpecker/woodpecker-forgejo-oauth` | woodpecker | `WOODPECKER_FORGEJO_CLIENT`, `WOODPECKER_FORGEJO_SECRET` (`SC-WOODPECKER-OAUTH`, owner plan 20, created by S13) |
   | `gitops/clusters/<cluster>/config/forgejo-known-hosts.yaml` | ConfigMap `forgejo-known-hosts` (rendered into `strange-company`, `agent-jobs`, `woodpecker`, `ci-jobs`) | — | `known_hosts` |
   | `gitops/clusters/<cluster>/config/forge-init.yaml` | non-secret record (`ForgeInitRecord`) | — | org, repo and bot IDs, token *names*, deploy-key fingerprint, host-key fingerprint, mirror ID, forge-init run ID |
   | `gitops/clusters/<cluster>/config/github-mirror.yaml` | `GitHubMirrorConfig` (plan 17, `contracts/github-mirror-config.schema.yaml`) | — | mirror token expiry and owner (M5) |

   Not forge-init outputs (pre-seeded by plan 50 before seed, CD-08): the Flux source Secret `flux-system/flux-system` (file `gitops/clusters/<cluster>/flux-system/flux-source-auth.sops.yaml`, plan 18; `identity`, `identity.pub`, `known_hosts`), whose public key S11 registers as the read-only deploy key, and the Runner webhook secret `strange-company/agent-runner-webhook` (`SC-RUNNER-WEBHOOK`, plan 26), which S14 configures on the webhooks. Bots `sc-release` and `sc-registry-reader` are declared in `forge.yaml` but created with their tokens by plan 23's `sc-bootstrap supply-chain init` at P11.

5. A forge-init result document written to the bootstrap journal (plan 50), validated by `contracts/forge-init-result.schema.yaml` (§7.3).
6. Prometheus metrics (`/metrics`, bearer-token protected), ServiceMonitor `forgejo/forgejo`, and alert rules (§21).

## 5. Dependencies

**Hard (DAG):**
- P04 needs P03 (k3s), F11 and F12. Within the seed, Forgejo `dependsOn`: CNPG operator → `forgejo-pg` Ready; Longhorn Ready; cert-manager + Traefik (ingress only, not health); the `forgejo` namespace with NetworkPolicies and PSA labels.
- P05 (forge-init) needs P04.

**Soft and runtime:**
- Plan 15 (P07) adds OIDC after control transfer through `valuesFrom` (§9.3). Forgejo does not depend on Authentik for health, and local accounts keep working when Authentik is down.
- Plan 17 defines the push-mirror parameters. forge-init executes the create step.
- Plan 20 consumes the Woodpecker OAuth app and the `sc-woodpecker` identity.
- Plan 26/55 consume `sc-runner`, the webhooks (activated at C03) and the branch-protection contexts (completed at C09).
- Plan 44 consumes `sc-backup` and runs bundle/backup CronJobs in `forgejo`.
- GitHub is **not** a runtime dependency [SPEC §32]. forge-init touches GitHub only to create the mirror and verify one sync (P06 evidence). If GitHub is unreachable, forge-init records `mirror: pending` and plan 17's procedure applies. It does not block P05.

**Assumed interfaces (other plans must honor):**
- Plan 50: secret generation before seed produces the files in §10 marked "pre-seed". forge-init runs inside the toolbox with an admin kubeconfig and uses `kubectl port-forward` to `forgejo-http:3000` and `forgejo-ssh:22`. It does not depend on public DNS or the tunnel. The CLI surface is `./bootstrap forge-init` and `./bootstrap exec -- sc-bootstrap forge apply|finalize|verify`.
- Plan 19: the seed set contains the `forgejo` layer exactly as steady state, so the Forgejo HelmRelease object is identical across the transfer (ADR-0005). The source switch happens only after S16 has pushed.
- Plan 18: layer paths `gitops/platform/forgejo/` (HelmRelease, base values) and `gitops/clusters/<cluster>/forgejo/` (cluster values, SOPS Secrets).
- Plan 12: `forgejo-pg` creates database `forgejo` owned by role `forgejo`, with Secret `forgejo-pg-app` (`username`, `password`, `host`, `port`, `dbname`, `uri`) and ≥ 50 `max_connections`. It also declares managed role `forgejo_ro` (`LOGIN`, member of `pg_read_all_data`, no other grants; password in SOPS `gitops/clusters/<c>/secrets/forgejo/forgejo-pg-ro.sops.yaml`, a `SC-PG-APP` instance) used only by plan 44 (repository listing for restore tests and `forgejo-rehydrate`); NetworkPolicy admits it only from `dr-tests` restore-tester pods and the rehydrate Job.
- Plan 14: credential `forgejo-objects` scoped read/write to bucket `<prefix>-forgejo-objects` only.
- Plan 09: in tunnel mode, `git.<BASE_DOMAIN>` routes through Traefik. Forgejo SSH is not published.

## 6. Owned state

| State | Location | Writer(s) | Backup (plan 44) |
|---|---|---|---|
| Forgejo relational data (users, orgs, PRs, issues, reviews, labels, statuses, webhooks, tokens (hashed), deploy keys, OAuth2 apps, push mirrors (credentials encrypted with `SECRET_KEY`)) | CNPG `forgejo-pg` | Forgejo only | CNPG WAL + daily base backup → S3 `pg-backups/forgejo-pg/`; PITR |
| Git repositories, LevelDB queues, bleve issue index, generated `app.ini` | Longhorn PVC `forgejo/forgejo-data` | Forgejo | age-encrypted `git bundle` chains every 30 min → S3 `repo-backups/git/weekly/…` (plan 44, ADR-0202); Longhorn recurring backup daily (plan 13) |
| LFS objects, attachments, packages and OCI blobs, avatars, repo-avatars, repo archives | S3 `<prefix>-forgejo-objects` (prefixes `lfs/`, `attachments/`, `packages/`, `avatars/`, `repo-avatars/`, `repo-archive/`) | Forgejo | bucket versioning (plan 14) |
| Durable identity material (app secrets, SSH host key) | Git (SOPS, both recipients; no bundle copy per `secret-classes.yaml`) | plan 50 pre-seed generation; rotation procedure | Git + GitHub mirror |
| Forge desired state (`forge/forge.yaml`) | Git | humans via PR | Git |
| forge-init record | Git (`config/forge-init.yaml`) + journal | forge-init | Git, journal in S3 |

Indexes and queues on the PVC are reconstructible: the bleve index rebuilds, and LevelDB queue loss drops only in-flight async jobs such as webhook deliveries and mirror syncs.

## 7. External interfaces

### 7.1 Human
- Web UI and HTTPS Git at `https://git.<BASE_DOMAIN>`. Humans sign in through **Authentik OIDC** (source name `authentik`). Forgejo **personal access tokens keep working while Authentik is down** and are the supported Git-over-HTTPS path during identity outages (AUTH-6). Local password login is available only to local accounts (`sc-admin`, `sc-breakglass`, bots). OIDC users push over HTTPS with personal access tokens.
- Git over SSH: internal only (`ssh://git@forgejo-ssh.forgejo.svc.cluster.local:22/<org>/<repo>.git`). In direct-ingress mode, `forgejo.ssh.public: true` MAY publish it on node port 2222 (§9.1). The default is off.

### 7.2 Machine

| Consumer | Credential | Transport | Rights |
|---|---|---|---|
| Flux source-controller | `SC-FLUX-DEPLOY-KEY` (read-only deploy key on `strange-company/strange-company`; Secret `flux-system/flux-system`) | SSH, internal Service | clone `strange-company` |
| Agent Runner (`sc-runner`) | API token, scopes `write:repository,write:issue,read:organization,read:user` | HTTP, `http://forgejo-http.forgejo.svc.cluster.local:3000` | PRs, statuses, labels, issues, comments, deploy-key create/delete (needs repo-admin team permission: §13.3) |
| Workers | per-execution deploy key (ADR-0007, created by Runner) | SSH | clone + push one repo |
| Woodpecker | OAuth2 app (client ID/secret) + per-user OAuth tokens | HTTP | per plan 20 |
| Renovate (`sc-renovate`) | API token `write:repository,write:issue,read:user,read:organization` | HTTP | branches `renovate/*`, PRs |
| Backup CronJobs (`sc-backup`) | API token `read:repository,read:organization,read:user,read:package` | HTTP (+ HTTPS clone with token) | read all repos in `strange-company` and `projects`; package listing for `oci-retention-audit` (plan 43) |
| Retention (`sc-gc`) | API token `write:package` only | HTTP | delete unreferenced container package versions (plan 43 `oci-retention`); no repository write |
| Woodpecker config extension (`sc-ci-config`) | API token `read:repository` (team `sc-bots-read`) | HTTP | read `.woodpecker/**` at the trusted revision (plan 20, ADR-0301) |
| Release pipeline (`sc-release`, P11) | API token `write:repository,write:issue`, team `sc-release` on `strange-company/strange-company` only | HTTP | push `promote/*` branches, open promotion PRs, push release images to the registry; never in a push allowlist of a protected branch (plan 23) |
| Registry pulls (`sc-registry-reader`, P11) | API token `read:package` (team `sc-registry-readers`) | HTTPS registry | pull internal images (Secret `sc-registry-pull`, plan 23) |
| Hermes | **none** (CD-15) | — | Hermes holds no Forgejo account; human `/sc` commands are accepted only from human accounts (ADR-0350) |
| Platform probes (plan 15 §14.4, plan 17 §21) | sc-admin token `read:admin,read:repository,read:user` | HTTP | read users' `last_login`, push-mirror status, branch protection |
| Prometheus | `SC-FORGEJO-METRICS-TOKEN` bearer | HTTP `/metrics` | metrics |
| Runner and Woodpecker webhooks (outbound from Forgejo) | HMAC secret `SC-RUNNER-WEBHOOK` (Runner); Woodpecker per-repo hook secret (plan 20) | HTTP to in-cluster Services (Runner `:8083`, CD-03; Woodpecker `:8000`) | — |

### 7.3 forge-init result (journal record; contract `contracts/forge-init-result.schema.yaml`)

snake_case JSON written to the plan-50 journal under phase `forge-init` (the contract is authoritative; it adds `mode` (`init`|`recover`), `started_at`, `finished_at`, per-step `mutations`, `pushed_sha` (final Forgejo `main` after S19) and plan 17's M0–M5 step IDs):

```yaml
phase: forge-init
run_id: fi-20261013T221500Z
result: succeeded            # succeeded | failed | partial
forgejo_version: "15.0.10"   # from GET /api/v1/version
steps:                        # one entry per step S00..S19
  - {id: S07, name: ensure-repo, outcome: created, at: "2026-10-13T22:16:02Z"}   # created|verified|updated|skipped|failed
repo: {full_name: strange-company/strange-company, id: 1, main_sha: "<sha>"}
ssh_host_key_fingerprint: "SHA256:..."
flux_deploy_key: {id: 1, fingerprint: "SHA256:...", read_only: true}
bots: [{username: sc-runner, token_name: sc-runner-20261013T221530Z}]
woodpecker_oauth_client_id: "<uuid>"
mirror: {id: "<remote_name>", state: synced, github_main_sha: "<sha>"}   # synced|pending|failed
outputs_commit: "<sha>"
protection_stage: bootstrap  # bootstrap | steady
errors: []                   # [{code, step, message}]
```

Error codes: `FORGE_UNHEALTHY`, `FORGE_ADMIN_AUTH_FAILED`, `FORGE_BREAKGLASS_MISMATCH`, `FORGE_MAIN_DIVERGED`, `FORGE_HOSTKEY_MISMATCH`, `FORGE_DEPLOY_KEY_CONFLICT`, `FORGE_OUTPUTS_REQUIRE_PR`, `FORGE_MIRROR_FILTER_UNSUPPORTED`, `GITHUB_DIVERGED`, `GITHUB_TAGS_DIVERGED`, `GITHUB_AUTH_FAILED`, `GITHUB_PUSH_REJECTED` (plan 17), `GITHUB_UNREACHABLE` (plan 17, non-fatal), `FORGE_API_ERROR`.

### 7.4 `forge/forge.yaml` (desired state; contract `contracts/forge-configuration.schema.yaml`)

The contract's `examples[0]` is the normative initial content of `forge/forge.yaml`; it is reproduced in condensed form here. `forge apply` refuses a file that fails the schema.

```yaml
apiVersion: strange.company/v1alpha1
kind: ForgeConfiguration
metadata: {name: strange-company}
spec:
  orgs:
    - name: strange-company        # visibility private; labelsRef plan55-status-labels
      teams: [Owners(admin), engineers(write), viewers(read),
              sc-automation(admin: sc-runner, sc-woodpecker), sc-bots-write(write: sc-renovate),
              sc-bots-read(read: sc-backup, sc-ci-config), sc-release(write, strange-company repo only: sc-release),
              sc-registry-readers(read, packages: sc-registry-reader), sc-gc(write, packages: sc-gc)]
    - name: projects               # Owners, engineers, viewers, sc-automation, sc-bots-read
  bots:                            # createdAt: P05 = forge-init S09/S10; P11 = plan 23 supply-chain init
    - {username: sc-runner,          createdAt: P05, tokenScopes: [write:repository, write:issue, read:organization, read:user], secretRef: strange-company/agent-runner-forgejo}
    - {username: sc-renovate,        createdAt: P05, tokenScopes: [write:repository, write:issue, read:organization, read:user], secretRef: renovate/forgejo-sc-renovate}
    - {username: sc-backup,          createdAt: P05, tokenScopes: [read:repository, read:organization, read:user, read:package], secretRef: forgejo/forgejo-sc-backup}
    - {username: sc-gc,              createdAt: P05, tokenScopes: [write:package], secretRef: forgejo/forgejo-sc-gc}
    - {username: sc-ci-config,       createdAt: P05, tokenScopes: [read:repository], secretRef: woodpecker/forgejo-sc-ci-config}
    - {username: sc-release,         createdAt: P11, tokenScopes: [write:repository, write:issue], repositoryScope: [strange-company/strange-company], secretRef: "woodpecker-secret:sc_release_pr_token"}
    - {username: sc-registry-reader, createdAt: P11, tokenScopes: [read:package], secretRef: strange-company/sc-registry-pull}
    - {username: sc-woodpecker,      createdAt: P05, tokenScopes: [], oauthApps: [{name: woodpecker, redirectUris: ["https://ci.${BASE_DOMAIN}/authorize"], confidential: true, secretRef: woodpecker/woodpecker-forgejo-oauth}]}
  adminTokens:
    - {name: platform-probes, scopes: [read:admin, read:repository, read:user], secretRef: monitoring/platform-probes-forgejo}
  repositories:
    - name: strange-company/strange-company
      defaultBranch: main
      private: true
      hasWiki: false
      hasProjects: true
      hasActions: false
      deployKeys: [{name: flux-readonly, readOnly: true, secretRef: flux-system/flux-system}]   # key pre-seeded (plan 18), registered by S11
      branchProtection:
        - branchPattern: main
          stages:
            bootstrap: {pushWhitelistUsers: [sc-admin, sc-breakglass], statusChecks: []}   # sc-breakglass: emergency-merge.md (plan 55, GC-02)
            steady:    {pushWhitelistUsers: [], statusChecks: [ci/woodpecker/pr/ci, strange-company/tdd-provenance, strange-company/review, strange-company/release-provenance]}
            recovery:  {pushWhitelistUsers: [], statusChecks: [ci/woodpecker/pr/ci], requiredApprovals: 2}   # TDD-17/GC-02: only via `forge apply --stage recovery` (emergency-merge.md, plan 55)
          enablePush: true
          enablePushWhitelist: true
          pushWhitelistDeployKeys: false
          requiredApprovals: 1
          dismissStaleApprovals: true
          blockOnRejectedReviews: true
          blockOnOfficialReviewRequests: true
          blockOnOutdatedBranch: false   # plan 55 §25 Q3
          applyToAdmins: true            # (verify) Forgejo `apply_to_admins`
          enableMergeWhitelist: false
        - branchPattern: "release/*"     # same body; steady statusChecks: [ci/woodpecker/pr/ci]
        - branchPattern: "renovate/*"    # TDD-16: only sc-renovate may push (both stages); no required checks
        # added only if 16-AC-34 reports `supported` (SEC-2): "agent/**" (deploy keys allowed, higher priority) and "**" (humans and bots only)
      protectedTags:                     # every matching rule must allow the pusher (verify semantics; 17-AC-4, 55-AC-1)
        - {namePattern: "*",               whitelistUsernames: [sc-admin, sc-runner, sc-release], whitelistTeams: [Owners, engineers]}
        - {namePattern: "v*",              whitelistUsernames: [sc-admin, sc-release], whitelistTeams: [engineers]}   # plan 20/23
        - {namePattern: "sc-archive/*",    whitelistUsernames: [sc-runner]}                                           # plan 55
        - {namePattern: "sc-quarantine/*", whitelistUsernames: [sc-runner]}                                           # plan 55
      pushMirrors:                       # plan 17 §7.2
        - {name: github, remoteAddress: "https://github.com/${GITHUB_MIRROR_REPO}.git", branchFilter: "main,release/*", interval: 10m0s, syncOnCommit: true, useSSH: false, credential: {class: SC-GITHUB-MIRROR, source: operator}}
  packageCleanupRules:                   # plan 43 R-10 (non-container packages; containers are R-09 oci-retention)
    - {owner: strange-company, type: generic, keepCount: 10, keepPattern: "^v?[0-9]+\\.[0-9]+\\.[0-9]+$", removeDays: 30, enabled: true}
    - {owner: strange-company, type: helm,    keepCount: 10, keepPattern: "^v?[0-9]+\\.[0-9]+\\.[0-9]+$", removeDays: 30, enabled: true}
  webhooks:
    - {scope: org/strange-company, name: agent-runner, url: "http://agent-runner.strange-company.svc.cluster.local:8083/v1/hooks/forgejo", events: [push, pull_request, pull_request_review, pull_request_comment, issues, issue_comment, status], contentType: json, secretRef: strange-company/agent-runner-webhook, active: false}
    - {scope: org/projects,        name: agent-runner, <same>}
```

Staging of required status contexts: the list above is the target state. The `forge.yaml` committed at P05 lists only `ci/woodpecker/pr/ci` for `main` (its producer is live at P12, when `forge finalize` applies `steady`); the C09 PR of plan 55 adds `strange-company/tdd-provenance`, `strange-company/review` and `strange-company/release-provenance` (SEC-11), after which no PR merges into `main` without them.

`active: false` until C03: plan 26 flips it to `true` by PR, and `forge apply` applies it. Port 8083 and path `/v1/hooks/forgejo` follow CD-03 and plan 26.

## 8. Internal interfaces

- Forgejo ↔ `forgejo-pg-rw.forgejo.svc:5432` (TLS `SSL_MODE=require`).
- Forgejo ↔ S3 endpoint (HTTPS, Forgejo's `minio` storage client type, which speaks generic S3).
- Init containers (chart) → generated `app.ini` on the PVC from `gitea.config`, `additionalConfigSources` and `additionalConfigFromEnvs`. The chart's `configure-gitea` init container creates the admin user once (`passwordMode: initialOnlyNoReset`, never resets an existing password, AUTH-9) and creates/updates OAuth sources (`gitea.oauth`) idempotently on every pod start.
- `sc-bootstrap forge` (toolbox, Go, plan 50 binary) ↔ Forgejo REST API `/api/v1` through `kubectl port-forward svc/forgejo-http 13000:3000` and SSH keyscan through `kubectl port-forward svc/forgejo-ssh 10022:22`.

## 9. Configuration

### 9.1 Knobs (source of truth: Git; changed by PR)

| Key (cluster values `gitops/clusters/<cluster>/forgejo/values.yaml`) | Default | Notes |
|---|---|---|
| `forgejo.version` (chart version + image digest) | latest **Forgejo LTS** at implementation time, forgejo-helm chart release whose appVersion is that LTS; image `code.forgejo.org/forgejo/forgejo:<v>-rootless@sha256:…` | Selection rule. Verified 2026-10-13: chart appVersion 15.0.10 on main (verify LTS status at implementation).; F11 (CD-26) checks that no chart dependency points at a Bitnami repository or registry, that any vendored Bitnami subchart is `type: library`, and that rendered manifests contain no Bitnami image |
| `forgejo.persistence.size` | `20Gi` (minimum), `50Gi` (small-ha) | Longhorn online expansion |
| `forgejo.persistence.storageClass` | `longhorn` | replicas 1 / 2 per profile (plan 13) |
| `forgejo.ssh.public` | `false` | direct mode only; opens NodePort 2222 and firewall (plan 08) |
| `forgejo.resources` | §12 | |
| `forgejo.repoIndexer` | `false` | code search off to save CPU and disk |
| `forgejo.mailer.enabled` | `true` if plan 42 SMTP transport configured, else `false` | |
| `forgejo.packages.cleanup` | `forge.yaml` `packageCleanupRules` (plan 43 R-10), applied by `forge apply`; Forgejo cron `cleanup_packages` and `archive_cleanup` (§9.2) | plan 43 owns the values |
| `forgejo.oidc.enabled` | delivered post-transfer by plan 15 through `valuesFrom` | §9.3 |

### 9.2 Chart values and `app.ini` (normative content; illustrative YAML)

```yaml
# HelmRelease forgejo/forgejo, chart oci://code.forgejo.org/forgejo-helm/forgejo (version-pinned)
image: {rootless: true}              # PSA restricted; built-in SSH server
replicaCount: 1
strategy: {type: Recreate}
test: {enabled: false}               # chart test pod uses busybox:latest (unpinned)
persistence: {enabled: true, create: false, claimName: forgejo-data}   # stable-named Volume/PV/PVC triplet created by plan 13 (layer longhorn-config, ADR-0203); the chart never creates or deletes it
service:
  http: {type: ClusterIP, port: 3000}
  ssh:  {type: ClusterIP, port: 22}  # → container 2222
podSecurityContext: {fsGroup: 1000, runAsNonRoot: true, seccompProfile: {type: RuntimeDefault}}
containerSecurityContext: {allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}, runAsUser: 1000, runAsGroup: 1000, readOnlyRootFilesystem: false}
priorityClassName: platform-critical
gitea:
  admin: {username: sc-admin, email: "sc-admin@<BASE_DOMAIN>", existingSecret: forgejo-admin, passwordMode: initialOnlyNoReset}   # (verify option name) the SOPS value only creates sc-admin; the steady value lives in the bundle (AUTH-9, §13.3)
  metrics: {enabled: true, serviceMonitor: {enabled: false}}   # scraping is central (ADR-0550, plan 38); avoids double scrape (SEED-5)
  additionalConfigSources:
    - secret: {secretName: forgejo-app-secrets}      # security/oauth2/server secrets as INI sections
  additionalConfigFromEnvs:
    - {name: FORGEJO__database__PASSWD, valueFrom: {secretKeyRef: {name: forgejo-pg-app, key: password}}}
    - {name: FORGEJO__storage__MINIO_ACCESS_KEY_ID, valueFrom: {secretKeyRef: {name: forgejo-objects-s3, key: accessKeyId}}}
    - {name: FORGEJO__storage__MINIO_SECRET_ACCESS_KEY, valueFrom: {secretKeyRef: {name: forgejo-objects-s3, key: secretAccessKey}}}
    - {name: FORGEJO__metrics__TOKEN, valueFrom: {secretKeyRef: {name: forgejo-metrics-token, key: token}}}
  config:
    server:
      DOMAIN: git.<BASE_DOMAIN>
      ROOT_URL: https://git.<BASE_DOMAIN>/
      START_SSH_SERVER: true
      SSH_LISTEN_PORT: 2222
      SSH_PORT: 22
      SSH_DOMAIN: forgejo-ssh.forgejo.svc.cluster.local
      SSH_SERVER_HOST_KEYS: /etc/forgejo/ssh/ssh_host_ed25519_key   # mounted from Secret forgejo-ssh-host-keys (verify key)
      LFS_START_SERVER: true
      OFFLINE_MODE: true             # no gravatar or external fetches (SPEC §32 independence)
    database: {DB_TYPE: postgres, HOST: "forgejo-pg-rw.forgejo.svc.cluster.local:5432", NAME: forgejo, USER: forgejo, SSL_MODE: require}
    storage:                          # default for all storage subsystems
      STORAGE_TYPE: minio
      MINIO_ENDPOINT: <S3 host:port>
      MINIO_BUCKET: <prefix>-forgejo-objects
      MINIO_LOCATION: <S3 region>
      MINIO_USE_SSL: true
      MINIO_BUCKET_LOOKUP: path       # (verify) key name for path-style; required by most non-AWS S3
    "storage.lfs": {MINIO_BASE_PATH: lfs/}
    "storage.attachments": {MINIO_BASE_PATH: attachments/}
    "storage.packages": {MINIO_BASE_PATH: packages/}
    "storage.avatars": {MINIO_BASE_PATH: avatars/}
    "storage.repo-avatars": {MINIO_BASE_PATH: repo-avatars/}
    "storage.repo-archive": {MINIO_BASE_PATH: repo-archive/}
    queue: {TYPE: level, DATADIR: queues/}           # single replica; on PVC
    cache: {ADAPTER: memory}                          # rebuildable
    session: {PROVIDER: db}                           # sessions survive restarts; no Redis
    indexer: {ISSUE_INDEXER_TYPE: bleve, REPO_INDEXER_ENABLED: false}
    actions: {ENABLED: false}
    packages: {ENABLED: true}
    metrics: {ENABLED: true}                          # TOKEN from env
    service:
      DISABLE_REGISTRATION: false
      ALLOW_ONLY_EXTERNAL_REGISTRATION: true          # accounts created only through OIDC
      SHOW_REGISTRATION_BUTTON: false
      REQUIRE_SIGNIN_VIEW: true
      DEFAULT_KEEP_EMAIL_PRIVATE: true
      DEFAULT_ORG_VISIBILITY: private
    oauth2_client:
      ENABLE_AUTO_REGISTRATION: true
      USERNAME: nickname                              # Authentik `nickname` = username (plan 15)
      ACCOUNT_LINKING: login                          # never auto-link to local accounts
      UPDATE_AVATAR: false
      OPENID_CONNECT_SCOPES: "openid email profile groups"
    openid: {ENABLE_OPENID_SIGNIN: false, ENABLE_OPENID_SIGNUP: false}
    webhook:
      ALLOWED_HOST_LIST: "external,agent-runner.strange-company.svc.cluster.local,woodpecker-server.woodpecker.svc.cluster.local"   # Runner :8083 (CD-03) and Woodpecker :8000 hook targets only; (verify) host-list semantics
      DELIVER_TIMEOUT: 10
    repository: {DEFAULT_BRANCH: main, DEFAULT_PRIVATE: private, ENABLE_PUSH_CREATE_USER: false, ENABLE_PUSH_CREATE_ORG: false, MAX_CREATION_LIMIT: 0}   # no personal repositories: every repository lives in a bundled org (STO-21)
    "repository.pull-request": {DEFAULT_MERGE_STYLE: merge}  # plan 55 MAY change
    mirror: {ENABLED: true, DISABLE_NEW_PULL: false, DISABLE_NEW_PUSH: false, MIN_INTERVAL: 10m, DEFAULT_INTERVAL: 8h}
    admin: {DISABLE_REGULAR_ORG_CREATION: true}
    security: {INSTALL_LOCK: true, LOGIN_REMEMBER_DAYS: 14, MIN_PASSWORD_LENGTH: 16}
    log: {LEVEL: Info, "logger.router.MODE": console, "logger.access.MODE": console}
    cron.update_checker: {ENABLED: false}
    "cron.delete_old_actions": {ENABLED: false}
    "cron.archive_cleanup": {ENABLED: true, RUN_AT_START: false, OLDER_THAN: 24h}      # plan 43 R-18 (verify key names)
    "cron.cleanup_packages": {ENABLED: true, OLDER_THAN: 24h}                          # plan 43 R-09 blob GC (verify key names)
    "cron.delete_repo_archives": {ENABLED: true}
extraVolumes: [{name: ssh-host-key, secret: {secretName: forgejo-ssh-host-keys, defaultMode: 0440}}]   # SC-FORGEJO-SSH-HOST-KEY (register name)
extraContainerVolumeMounts: [{name: ssh-host-key, mountPath: /etc/forgejo/ssh, readOnly: true}]
```

`forgejo-app-secrets` contains `security: INTERNAL_TOKEN, SECRET_KEY`, `oauth2: JWT_SECRET`, `server: LFS_JWT_SECRET` in the chart's `additionalConfigSources` INI-section format. Re-verify the exact Secret shape at implementation (AC 16-AC-9).

### 9.3 OIDC source delivery (post-transfer, no change to the seeded HelmRelease object)

The Forgejo HelmRelease carries, from the very first (seed) commit:

```yaml
spec:
  valuesFrom:
    - {kind: Secret, name: forgejo-oidc-values, valuesKey: values.yaml, optional: true}
```

During seed the Secret is absent and the value is skipped. Plan 15's post-transfer Kustomization `authentik-sso-forgejo` (dependsOn `authentik-config`) creates `forgejo/forgejo-oidc-values` containing:

```yaml
gitea:
  oauth:
    - name: authentik
      provider: openidConnect
      existingSecret: forgejo-oidc-client          # keys: key, secret (SC-OIDC-CLIENTS/forgejo)
      autoDiscoverUrl: https://auth.<BASE_DOMAIN>/application/o/forgejo/.well-known/openid-configuration
      scopes: "openid email profile groups"
      groupClaimName: groups
      adminGroup: sc-admins
      groupTeamMap: '{"sc-admins":{"strange-company":["Owners"],"projects":["Owners"]},"sc-engineers":{"strange-company":["engineers"],"projects":["engineers"]},"sc-viewers":{"strange-company":["viewers"],"projects":["viewers"]},"sc-synthetic":{"strange-company":["viewers"],"projects":["viewers"]}}'
      groupTeamMapRemoval: true
      skipLocal2fa: false
```

The chart converts unknown item keys to kebab-case `forgejo admin auth add-oauth|update-oauth` flags (`--group-claim-name`, `--admin-group`, `--group-team-map`, `--group-team-map-removal`) (verify, AC 16-AC-31). helm-controller detects the new values on its next reconcile and upgrades. The pod restarts (once), and the init container registers the source while Authentik is already healthy. This avoids registering an OIDC source whose discovery URL is unreachable at Forgejo start. The decision is recorded in ADR-0252.

## 10. Secrets

| Class | Status | Creator | Consumer | Storage | Rotation | Recovery |
|---|---|---|---|---|---|---|
| `SC-FORGEJO-ADMIN` (`sc-admin` local site admin; automation identity) | master §9 | plan 50, pre-seed | chart init (seed value only, `initialOnlyNoReset`), `forge-init --recover`, break-glass-class repairs (human `forge apply` uses the operator's own admin identity after finalize) | **seed value**: SOPS `gitops/clusters/<c>/forgejo/forgejo-admin.sops.yaml` (both recipients; only creates the account, chart `initialOnlyNoReset`); **steady value**: bundle only, set by `forge finalize` (AUTH-9; plan 35 register to record both) | `rotate-forge-credentials.md` §A (PR changes SOPS → pod restart resets password) | from Git/bundle |
| `SC-BREAK-GLASS-FORGEJO` (`sc-breakglass` local admin) | master §9 (`SC-BREAK-GLASS-*`) | plan 50 operator-side staging (`/run/sc/secrets/`); account created by forge-init S03 | humans in emergencies only | **recovery bundle** (authoritative) + `secrets/offcluster/<cluster>/break-glass-forgejo.sops.yaml` encrypted to the recovery recipient only (the cluster cannot decrypt it); never a cluster Secret | `rotate-break-glass.md` (plan 35), mechanics in `rotate-forge-credentials.md` §F | from bundle; if lost, reset via `sc-admin` API |
| `SC-FORGEJO-BOT-TOKENS` (instances sc-runner, sc-renovate, sc-backup, sc-gc, sc-ci-config, platform-probes; sc-release and sc-registry-reader minted by plan 23 at P11) | master §9 | forge-init S10 (P05) / plan 23 supply-chain init (P11) | Runner, Renovate, backup and retention CronJobs, Woodpecker config extension, platform probes, release pipeline, kubelet pulls | SOPS `gitops/clusters/<c>/secrets/<ns>/forgejo-<instance>.sops.yaml` (§4.4); sc-release as Woodpecker secret (plan 23) | `rotate-forge-credentials.md` §B; token names carry a timestamp; old tokens deleted after the new one is Flux-applied | re-mint via `forge apply --rotate` |
| `SC-FLUX-DEPLOY-KEY` | master §9 (owner plan 18) | plan 50 pre-seed (ed25519); forge-init S11 registers the public key read-only | Flux source-controller | SOPS `gitops/clusters/<c>/flux-system/flux-source-auth.sops.yaml` → Secret `flux-system/flux-system` (CD-08) | §C | re-generate; key is repo-scoped and read-only |
| `SC-WOODPECKER-OAUTH` | master §9 (owner plan 20) | forge-init S13 (P05) | Woodpecker server | SOPS `gitops/clusters/<c>/secrets/forge/woodpecker-oauth.sops.yaml` → Secret `woodpecker/woodpecker-forgejo-oauth` | §D (regenerate via API, then re-authorize repositories with `./bootstrap ci-init`) | re-generate |
| `SC-FORGEJO-APP-SECRETS` (`SECRET_KEY`, `INTERNAL_TOKEN`, `JWT_SECRET`, `LFS_JWT_SECRET`) | **new** (this plan) | plan 50, pre-seed (`forgejo generate secret` equivalent: 64-byte random) | Forgejo | SOPS only, `gitops/clusters/<c>/forgejo/forgejo-app-secrets.sops.yaml` (register: no bundle copy; the offline recovery identity decrypts it from the GitHub clone) | `SECRET_KEY` is **not rotatable** in place (re-encrypting stored secrets is unsupported); other three rotatable by PR (invalidates sessions/JWTs; `rotate-forge-credentials.md` §H) | from Git (GitHub mirror); loss of `SECRET_KEY` = re-create push mirror credentials and 2FA enrollments |
| `SC-FORGEJO-SSH-HOST-KEY` (ed25519) | **new** (this plan) | plan 50, pre-seed | Forgejo SSH server (Secret `forgejo/forgejo-ssh-host-keys`); fingerprint recorded in `forgejo-known-hosts` and the Flux Secret | SOPS only, `gitops/clusters/<c>/forgejo/forgejo-ssh-host-key.sops.yaml` (no bundle copy) | §E: client-side `known_hosts` overlap (add new key to clients, switch server, remove old) | from Git (GitHub mirror) |
| `SC-FORGEJO-METRICS-TOKEN` | **new** (this plan) | plan 50, pre-seed | Forgejo `[metrics] TOKEN`, Prometheus ServiceMonitor | SOPS (Secret `forgejo/forgejo-metrics-token`, copied to `monitoring` by a SOPS file of the same value) | PR | re-generate |
| `SC-RUNNER-WEBHOOK` (HMAC secret for Runner webhooks, CD-08) | owner plan 26 | plan 50 pre-seed (32 bytes) | Forgejo webhook (S14 configures it), Runner verifier | SOPS `gitops/clusters/<c>/secrets/strange-company/agent-runner-webhook.sops.yaml` | `rotate-forge-credentials.md` §B | re-generate |
| `SC-OBJECT-STORAGE` (`forgejo-objects` credential) | master §9 (plan 14) | plan 14 | Forgejo | SOPS | plan 35 `rotate-storage-credentials.md` | plan 14 |
| `SC-PG-APP` (`forgejo-pg-app`) | master §9 (owner plan 12) | plan 50 pre-seed (32 random bytes) | Forgejo, CNPG `bootstrap.*.secret` | SOPS `gitops/clusters/<c>/secrets/forgejo/forgejo-pg-app.sops.yaml` (12-R-5: identical across `initdb` and `recovery`) | plan 12 | from Git |
| `SC-OIDC-CLIENTS` (forgejo client) | master §9 | plan 50 pre-seed generation | Authentik blueprint + Forgejo | SOPS | plan 15 | Git |
| `SC-GITHUB-MIRROR` | master §9 | operator (plan 17) | Forgejo push mirror (DB, encrypted with `SECRET_KEY`) | bundle + Forgejo DB | plan 17 | plan 17 |

Bot accounts have random passwords that forge-init sets and discards (§14.2 S09). They are not stored anywhere. Bots authenticate only with tokens.

## 11. Storage

- **PVC `forgejo-data`** on Longhorn (`ReadWriteOnce`, `longhorn` class, replica count from profile). Contents: `/data/git/repositories`, `/data/gitea/{queues,indexers,conf}`. Volume growth alert at 80% (plan 13 `SCLonghornVolumeNearlyFull`, scoped by label). Longhorn recurring snapshot every 6 h retained 4, and backup daily retained 7 (plan 13/43). Longhorn must not hold CNPG PGDATA (SPEC §27); `forgejo-pg` uses `local-path` (plan 12).
- **S3 `forgejo-objects`**: versioned, with lifecycle for noncurrent versions owned by plan 43. Packages GC (Forgejo `cleanup rules`) per plan 43.
- **No Redis, no memcache:** cache `memory`, session `db`, queue `level`.
- Repository archives (`repo-archive/`) are reconstructible. Forgejo cron `delete_repo_archives` stays enabled at default.

## 12. Resource requirements

| Component | self-hosting-minimum req / limit | small-ha req / limit |
|---|---|---|
| Forgejo container | cpu 200m / — ; mem 384Mi / 1Gi | cpu 300m / — ; mem 512Mi / 1536Mi |
| Init containers | cpu 50m; mem 64Mi / 256Mi | same |
| `forgejo-pg` | plan 12 (proposed 1 instance, 256Mi / 512Mi) | plan 12 (3 instances) |
| PVC | 20Gi Longhorn ×1 replica | 50Gi ×2 replicas |
| S3 | ~1–10 GiB at threshold (OCI images dominate; plan 43 GC) | grows with packages |

CPU limits are not set on platform-critical pods, so throttling cannot stall Git operations (plan 51 decides globally). Memory limits are hard.

## 13. Security boundary

### 13.1 Trust level and placement
Platform tier `platform-critical`, namespace `forgejo`, PSA `restricted` (ADR-0014). The rootless image runs as uid 1000, with no privilege escalation, all capabilities dropped and RuntimeDefault seccomp. The ServiceAccount `forgejo` has `automountServiceAccountToken: false` and no RBAC.

### 13.2 NetworkPolicy (namespace `forgejo`; plan 36 renders)

| Direction | Peer | Port | Why |
|---|---|---|---|
| ingress | `ingress` ns (Traefik pods) | 3000 | web/HTTPS Git |
| ingress | `flux-system` (source-controller), `agent-jobs`, `strange-company` (Runner), `woodpecker`, `ci-jobs`, `forgejo` (backup jobs) | 2222 | SSH clone/push |
| ingress | `strange-company` (Runner), `woodpecker`, `renovate`, `forgejo` (backup), `monitoring` (probes) | 3000 | API |
| ingress | `monitoring` (Prometheus) | 3000 `/metrics` | scrape |
| egress | `forgejo-pg` pods | 5432 | DB |
| egress | S3 endpoint CIDR/FQDN set (plan 36 approximation) | 443 | objects |
| egress | `strange-company` (Runner) 8083 (CD-03), `woodpecker` (server) 8000 | | webhooks |
| egress | `0.0.0.0/0` except cluster/private ranges | 443 | **push mirror to GitHub only** (plan 17); restricted to GitHub CIDRs if plan 36 supports an IP-set approximation |
| egress | `ingress` Traefik pods (CoreDNS rewrite of `auth.<d>`) | 8443 (post-DNAT pod port, unconditional; plan 36 I-10, AUTH-20) | OIDC discovery/token (plan 15) |
| egress | kube-dns | 53 | |

### 13.3 Forge authorization model
- Site admins: `sc-admin` (automation), `sc-breakglass` (emergency), and humans in Authentik `sc-admins` (OIDC `--admin-group`).
- **Branch protection on `main` and `release/*`:** no direct pushes in the steady stage; deploy keys cannot push (`push_whitelist_deploy_keys: false`); one approval; stale approvals dismissed; `apply_to_admins: true` (verify). Required status contexts come from plan 55's list (ADR-0019).
- **Residual risk R-16-1:** `sc-runner` holds `admin` team permission because Forgejo requires repo admin to manage deploy keys (ADR-0007). Repo admins can edit branch protection. Mitigations: the platform probe compares live protection against `forge/forge.yaml` every 5 min and fires `SCForgeBranchProtectionDrift` (critical), and the Runner's code never calls protection endpoints (plan 26 test). Recorded for plan 34/55 (§25 Q-16-04).
- Repository-level custom Git hooks stay disabled (Forgejo default `DISABLE_GIT_HOOKS=true`; ADR-0007).
- `ACCOUNT_LINKING=login` plus the Authentik policy forbidding `sc-*`, `admin` and `root` usernames for Forgejo (plan 15) prevent an OIDC identity from claiming a local admin account.

## 14. Happy path

### 14.1 Seed (P04, part owned here)
1. Flux (Bucket source, plan 19) applies, in `dependsOn` order (plan 18 layer table; one owner per object, SEED-5): `namespaces` (Namespace `forgejo` with PSA and tier labels) → `policies` (NetworkPolicies) → the cluster-configuration layer of plan 18 (SOPS Secrets `forgejo-admin`, `forgejo-app-secrets`, `forgejo-ssh-host-keys`, `forgejo-metrics-token`, `forgejo-objects-s3`, `forgejo-oidc-client`, `forgejo-pg-app`; SEED-1) → `forgejo-db` (`forgejo-pg`) → `longhorn-config` (stable-named PVC `forgejo-data`, plan 13, ADR-0203) → `forgejo-upgrade-gate` → `forgejo`, which contains **only** the HelmRelease (`persistence.create: false`; the chart creates the HTTP and SSH Services; ServiceMonitor disabled).
2. Chart init containers render `app.ini`, run migrations, and create `sc-admin` with the SOPS seed password on an empty DB (`initialOnlyNoReset`; existing passwords are never reset, AUTH-9). The main container starts, and the built-in SSH server listens on 2222 with the mounted host key.
3. Readiness: `GET /api/healthz` returns 200 with `"status":"pass"`. The HelmRelease becomes Ready. Seed gate GATE-P04 items 16-AC-1..9.

### 14.2 forge-init (P05) — ordered, idempotent steps

`./bootstrap forge-init` (plan 50 invokes) runs `sc-bootstrap forge init` in the toolbox. Every step is *check-then-act* and records an outcome in the journal. A re-run resumes at the first non-`succeeded` step but re-verifies all earlier steps cheaply.

| Step | Action | Check (idempotency) | Failure → result |
|---|---|---|---|
| S00 preflight | Open port-forwards (`13000→forgejo-http:3000`, `10022→forgejo-ssh:22`). Load local staging secrets. Plan 17 check: if GitHub `main` of `GITHUB_MIRROR_REPO` exists it must be an ancestor of local `HEAD` (`git merge-base --is-ancestor`) | — | `GITHUB_DIVERGED` (fatal: refuse, see `github-mirror-failure.md` §Diverged); port-forward failure → `FORGE_UNHEALTHY` |
| S01 wait healthy | Poll `GET /api/healthz` every 5 s, timeout 15 min. Also `GET /api/v1/version` | status pass | `FORGE_UNHEALTHY` (retryable) |
| S02 verify `sc-admin` | Basic auth `GET /api/v1/user` → `is_admin: true` | — | `FORGE_ADMIN_AUTH_FAILED` (fatal; before finalize check the seed Secret, after finalize use the bundle value — the chart never resets an existing password) |
| S03 ensure `sc-breakglass` | If absent: `POST /api/v1/admin/users` (`username: sc-breakglass`, `login_name: sc-breakglass`, `source_id: 0`, `must_change_password: false`, `visibility: private`, password from staging), then `PATCH /api/v1/admin/users/sc-breakglass` `{admin: true, login_name: sc-breakglass, source_id: 0}`. If present: verify by **API basic auth** `GET /api/v1/user` (does not update `last_login`, so no break-glass alert) | user exists and auth OK | `FORGE_BREAKGLASS_MISMATCH` (fatal; never reset silently, see `rotate-forge-credentials.md` §F) |
| S04 ensure orgs | `GET /api/v1/orgs/{org}` → 404 → `POST /api/v1/orgs` for `strange-company`, `projects` (visibility private) | 200 | API error |
| S05 ensure teams | per `forge.yaml`: `GET /api/v1/orgs/{org}/teams/search?q=` → create (`POST /orgs/{org}/teams`) or `PATCH /teams/{id}` to desired permission/units/includes_all_repositories | fields equal | |
| S06 ensure org labels | plan 55 label set (`status/backlog`, `status/ready`, `status/in-progress`, `status/review`, `status/done`, `status/needs-human`, `status/blocked`, exclusive scope `status/`) via `/api/v1/orgs/{org}/labels` | name, color and exclusive equal | |
| S07 ensure repo | `GET /api/v1/repos/strange-company/strange-company` → 404 → `POST /api/v1/orgs/strange-company/repos` (`auto_init: false`, `private: true`, `default_branch: main`). Then `PATCH` `has_wiki: false` etc. | exists with settings | |
| S08 initial push | **Authoritative transport for forge-init (NC-5):** HTTP over the run-scoped port-forward (TLS is not needed on loopback) with `GIT_ASKPASS`; S16 and plan 50 `sync-local` use the same transport, never an SSH remote. `git push http://127.0.0.1:13000/strange-company/strange-company.git HEAD:refs/heads/main` with credentials supplied by `GIT_ASKPASS` reading `/run/sc/secrets/forgejo-admin` (never in argv or error output; value registered with the redactor, BOOT-24) and `--tags`. **No force.** If remote `main` exists it must be an ancestor of local HEAD (fast-forward) or equal | remote main == local HEAD (or ancestor → FF push) | `FORGE_MAIN_DIVERGED` (fatal; see `restore-forgejo.md` §Diverged) |
| S09 ensure bots | `sc-runner`, `sc-woodpecker`, `sc-renovate`, `sc-backup`, `sc-gc`, `sc-ci-config` (bots with `createdAt: P05`; `sc-release`, `sc-registry-reader` are skipped until plan 23's P11 init): `POST /api/v1/admin/users` (random 40-char password, `must_change_password: false`, `visibility: private`); team memberships per `forge.yaml` (`PUT /teams/{id}/members/{user}`) | exists and member | |
| S10 ensure tokens | For each bot with scopes: if the SOPS output file exists locally (on a new machine, forge-init first fetches Forgejo `main` when the journal records `pushed_sha` and fast-forwards local `main`, so outputs committed by an earlier run are present, BOOT-7) **and** `GET /api/v1/user` with that token returns the bot's login, keep it. Otherwise set a fresh random password (`PATCH /admin/users/{bot}`), create `POST /api/v1/users/{bot}/tokens` (basic auth as bot) named `<bot>-<UTC timestamp>` with the declared scopes, then delete older tokens with the same prefix. The same applies to the `platform-probes` token of `sc-admin` | token valid | |
| S11 Flux deploy key | The key pair is pre-seeded (plan 50/18) in `flux-system/flux-source-auth.sops.yaml`; S11 decrypts the public half locally and ensures a deploy key with it exists (`GET /repos/.../keys`), `read_only: true`, title `flux-readonly`. Absent → `POST /repos/.../keys` `{title: flux-readonly, key, read_only: true}`. A key titled `flux-readonly` with a different fingerprint → delete and re-add (the Git file is authoritative) | fingerprint equal, read_only true | `FORGE_DEPLOY_KEY_CONFLICT` if a key with that title is not read-only |
| S12 host key | `ssh-keyscan -p 10022 -t ed25519 127.0.0.1` → compare with the public half of `SC-FORGEJO-SSH-HOST-KEY`. Write `known_hosts` lines for `forgejo-ssh.forgejo.svc.cluster.local` and `forgejo-ssh.forgejo.svc` | equal | `FORGE_HOSTKEY_MISMATCH` (fatal) |
| S13 Woodpecker OAuth app | As `sc-woodpecker` (fresh password, basic auth): `GET /api/v1/user/applications/oauth2` → create `{name: woodpecker, redirect_uris: [https://ci.<d>/authorize], confidential_client: true}` or, when the local SOPS secret is absent, `PATCH /user/applications/oauth2/{id}` to regenerate `client_secret` | app exists and local secret present | |
| S14 webhooks | per `forge.yaml` (`active: false` until C03): `GET/POST/PATCH /api/v1/orgs/{org}/hooks`, secret from the pre-seeded `SC-RUNNER-WEBHOOK` file (decrypted locally) | config equal | |
| S15 write outputs | Render §4.4 files, SOPS-encrypt to both recipients (`.sops.yaml`, plan 35), write `config/forge-init.yaml` and `config/github-mirror.yaml` (fields computed from PF-16a/e before S15, plan 17 M5, SEED-9), and regenerate `gitops/clusters/<c>/cluster-config/kustomization.yaml` with `sc-gitops gen cluster-config --cluster <c>` in the same commit, so layer `cluster-config` (plan 18) delivers every new manifest (SEED-1; plan 19 T0(b) accepts the outputs plus that generated file). Commit locally with exactly the subject plan 19 T0(b) requires, `bootstrap(<cluster>): forge-init outputs` and trailers `SC-Install-Uid`, `SC-Run-Id` (BOOT-20) **only if the content changed** | no diff → skip | |
| S16 push outputs | Fast-forward push to Forgejo `main` (as S08). In stage `bootstrap`, `sc-admin` is whitelisted. If protection is already `steady` and a push is needed → `FORGE_OUTPUTS_REQUIRE_PR` (open PR `bootstrap/forge-init-<run>`, stop) | remote main == local HEAD | |
| S17 push mirror | plan 17 §14 steps M1–M4 (create mirror, sync, verify GitHub `main` == Forgejo `main`) | mirror exists with expected config | `GITHUB_UNREACHABLE` → `mirror: pending` (non-fatal for P05; fatal for P06) |
| S18 branch and tag protection (stage bootstrap) | `GET /repos/.../branch_protections` → `POST` or `PATCH` for `main`, `release/*` with the bootstrap stage; `GET /repos/.../tag_protections` → `POST`/`PATCH` for `*`, `v*`, `sc-archive/*`, `sc-quarantine/*` per `forge.yaml` | fields equal | |
| S19 record | journal result (§7.3) including `pushed_sha` = the **final** Forgejo `main` SHA after S16–S18 (the value plan 19 T0(c) compares, SEED-9), and print the summary | — | |

forge-init holds **no** state between runs except Git (SOPS outputs), local staging and the journal.

### 14.3 After forge-init
- P06: the source switch is plan 19's (§8.5 T0–T8). The seed root applies `GitRepository flux-system` and its Secret `flux-system/flux-system` (from `flux-source-auth.sops.yaml`) from the Bucket like every other object; forge-init and the orchestrator apply nothing out of band here (SEED-8).
- **`sc-bootstrap forge finalize`** (plan 50 invokes during P12, just before `CONTROL TRANSFER COMPLETE`): (1) apply branch protection stage `steady`: remove `sc-admin` from the push whitelist and set the required status contexts whose producers are live (Woodpecker context from plan 20 at P12; Runner contexts `strange-company/tdd-provenance` and `strange-company/review` are added at C09 by a PR to `forge.yaml` plus `forge apply`, as plan 55 defines); (2) verify every `forge.yaml` object; (3) record `protection_stage: steady`; (4) **retire the seed `sc-admin` password** (AUTH-9): set a new random password for `sc-admin` through the admin API, store it only in the bundle (`./bootstrap rotate refresh-bundle` runs within the same phase) and delete the API session, so the tier-1 SOPS value no longer authenticates. From then on `sc-admin` is used only by `forge-init --recover`, break-glass-class repairs and short-lived tokens minted with `kubectl -n forgejo exec deploy/forgejo -- forgejo admin user generate-access-token --username sc-admin --token-name sc-op-<ts> --scopes <s>` and deleted after use; human-run `forge apply` authenticates with the operator's own Forgejo admin identity (OIDC user in `sc-admins`, PAT with `write:admin,write:organization,write:repository`). Tool-opened PRs (rotations, restore, finalize) are authored by `sc-admin` through such a short-lived token and approved by a human, so a single operator never needs to self-approve (DR-26).

### 14.4 Steady state
**Emergency merge (TDD-17, GC-02).** When a merge-gate component (Runner, Woodpecker, `sc-ci-config`, egress-proxy) is broken, its fix cannot collect the required statuses. `procedures/emergency-merge.md` (plan 55, authoritative) applies `sc-bootstrap forge apply --stage bootstrap` (push whitelist `sc-admin`, `sc-breakglass`), fast-forward-pushes the reviewed commit as `sc-breakglass`, and returns to `steady`. The `recovery` stage (Runner contexts removed, two approvals, admins still bound) is kept as the PR-based alternative when only the Runner contexts are missing (Q-16-09). Either way the procedure lands the reviewed fix, re-audits every merge made in the window (plan 55 unaudited-range logic), and restores `steady`. `SCForgeBranchProtectionDrift` fires for the window and is annotated with the procedure's evidence.

Routine forge configuration changes: PR to `forge/forge.yaml` → merge → operator runs `./bootstrap exec -- sc-bootstrap forge apply` (prints a diff, then applies). Automating `forge apply` as an in-cluster reconciler is post-MVP (§24). The probe detects drift continuously.

## 15. Failure modes

| # | Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|---|
| F1 | Forgejo pod crashloop / down | `SCForgejoDown`: blackbox `probe_success{probe="forgejo-http"} == 0` for 3m (critical; catalogue) | no Git, PR, Runner or Flux fetches (Flux keeps last applied revision) | kubelet restarts; Flux HelmRelease remediation **disabled for upgrades** (§19) | `restore-forgejo.md` §Triage |
| F2 | `forgejo-pg` unavailable | `SCForgejoDown` + `SCCnpgClusterDown{cluster="forgejo-pg"}` (plan 12); healthz `database` check fails | Forgejo 500s | CNPG failover (small-ha) | plan 12 `cnpg-primary-loss.md` |
| F3 | **Multi-Attach / detach lag (L-5)** after rollout or node loss: new pod stuck `ContainerCreating`, event `FailedAttachVolume … Multi-Attach error` | `SCForgejoPodStuckPending` (10m, warning) / `SCForgejoPodStuckPendingCritical` (20m): `kube_pod_status_phase{namespace="forgejo",phase=~"Pending"} == 1` for 10m, **or** `kube_pod_container_status_waiting_reason{namespace="forgejo",reason="ContainerCreating"}` | Forgejo down | none automatic (never force-detach automatically); `Recreate` strategy prevents two pods by design; Longhorn `nodeDownPodDeletionPolicy=delete-both-statefulset-and-deployment-pod` (plan 13) handles node loss | `restore-forgejo.md` §Multi-Attach |
| F4 | PVC full | `SCLonghornVolumeNearlyFull{volume="forgejo-data"}` (plan 13) and Forgejo push errors | pushes fail | none | expand PVC (PR to size), plan 13 |
| F5 | S3 unreachable | `SCForgejoObjectStorageErrors`: rate of HTTP 500 on `/api/packages|/attachments|*.git/info/lfs` (access log metric) > 0 for 10m; plan 14 `SCObjectStorageUnreachable` | packages, LFS and attachments fail; Git and PRs keep working | none | `object-storage-outage.md` (plan 14) |
| F6 | Repo corruption / accidental deletion | backup verification (plan 44 `SCRestoreTestReposFailed`); human report | one project | none | `restore-forgejo.md` §Single project |
| F7 | `SECRET_KEY` mismatch after restore | push-mirror `last_error` contains decrypt error; 2FA login fails | mirror broken | none | `restore-forgejo.md` step R9, `github-mirror-failure.md` §Credential |
| F8 | SSH host key changed (key not from Secret) | Flux `GitRepository` not Ready with `knownhosts: key mismatch`; probe `sc_forgejo_ssh_hostkey_match == 0` → `SCForgejoSSHHostKeyMismatch` (critical) | Flux, workers and Runner clones fail | none | `rotate-forge-credentials.md` §E |
| F9 | SSH listener down, HTTP up | blackbox TCP probe `forgejo-ssh:22` fails 5m → `SCForgejoSSHDown` (warning; catalogue) | Flux and workers stall | restart by liveness? no (HTTP liveness only); alert | `restore-forgejo.md` §Triage |
| F10 | Bot token revoked or expired | Runner/Renovate/backup 401 → their own alerts; probe checks each token weekly → `SCForgeBotTokenInvalid` | automation stops | none | `rotate-forge-credentials.md` §B |
| F11 | Forge config drift (protection weakened, team changed, webhook edited) | probe compares `forge.yaml` vs API every 5 min → `SCForgeBranchProtectionDrift` (critical, `object=~"branch_protection.*"`) or `SCForgeConfigDrift{object=…}` (warning, other objects) (was: critical for branch protection, warning otherwise) | merge policy weakened | none (no auto-revert in MVP) | `forge apply` after review; incident if unexplained |
| F12 | forge-init interrupted | journal shows `partial` | bootstrap paused | resume with `./bootstrap` (plan 50 resume) | `bootstrap-resume.md` (plan 50) |
| F13 | forge-init `FORGE_MAIN_DIVERGED` | step S08 | refuse to overwrite | none | `restore-forgejo.md` §Diverged |
| F14 | Upgrade migration fails | HelmRelease `UpgradeFailed`, pod crashloop with migration error in logs | Forgejo down | no auto-rollback (`upgrade.remediation.retries: 0`) | §19, plan 48 |
| F15 | OIDC source broken (Authentik down or source misconfigured) | plan 15 `SCAuthentikDown`; synthetic login probe | humans can't sign in; existing sessions, PATs and machines unaffected | none | `authentik-outage.md` |
| F16 | Webhook deliveries failing | Forgejo `/admin/hooks` delivery history; Runner reconciler (plan 26) polls as fallback | delayed Runner reactions | Forgejo retries per queue | plan 26 |
| F18 | Unexpected `sc-admin` token (anything other than the declared `platform-probes` token, AUTH-9) | `sc_forge_unexpected_admin_tokens` → `SCForgeUnexpectedAdminToken` | standing admin credential | none | delete it; `credential-compromise.md` if unexplained |
| F17 | Break-glass account login | `SCBreakGlassUsed{system="forgejo"}` (plan 15) | — | notification | `break-glass-access.md` |

## 16. Retry behavior

- forge-init API calls: up to 5 attempts with exponential backoff (1, 2, 4, 8, 16 s) on 5xx, connection reset or 429; no retry on 4xx except 404-then-create races (409 → re-GET). Healthz wait is bounded at 15 min.
- `git push` in S08/S16: 3 attempts, 10 s apart, on transport errors only. A non-fast-forward rejection is never retried.
- The whole forge-init phase is retried by `./bootstrap` resume, never in an automatic loop. After 3 consecutive failed runs with the same error code, plan 50 stops with `NEEDS_OPERATOR`.
- Forgejo internal: webhook and mirror queues are Forgejo-managed.

## 17. Idempotency behavior

- Every forge-init step is check-then-act (§14.2). Re-running forge-init on a fully configured instance results in **zero** API mutations and **no** new commit (AC 16-AC-17).
- Tokens are never re-minted when the existing token validates. Rotation is explicit (`forge apply --rotate <bot>`).
- Deploy key and host key: verified by fingerprint, never regenerated while a valid local private key exists.
- `git push` is fast-forward only. A diverged remote fails closed.
- The chart's admin and OAuth init steps are idempotent by design (`initialOnlyNoReset` creates `sc-admin` once; `update-oauth`).
- `forge apply` computes a diff and applies only the delta. `--dry-run` prints it.

## 18. Bootstrap behavior

1. **Pre-seed (plan 50):** generate `SC-FORGEJO-ADMIN`, `SC-FORGEJO-APP-SECRETS`, `SC-FORGEJO-SSH-HOST-KEY`, `SC-FORGEJO-METRICS-TOKEN`, `SC-OIDC-CLIENTS`, and stage `SC-BREAK-GLASS-FORGEJO`. Write the SOPS files and commit them locally; the seed snapshot contains them.
2. **Seed (P04):** §14.1.
3. **forge-init (P05):** §14.2.
4. **Control transfer (P06):** plan 19. Mirror verification is part of P06 (plan 17).
5. **SSO (P07):** plan 15 delivers `forgejo-oidc-values`. Forgejo restarts once; L-5 risk is covered by F3.
6. **Finalize (P12):** §14.3.

**Black start (plan 45):** the same pre-seed SOPS files come from the GitHub clone, so the SSH host key and `SECRET_KEY` are the originals. `forgejo-pg` bootstraps by CNPG recovery from S3. The PVC restores from the Longhorn backup or starts empty, and repositories are then restored from bundles or GitHub (§20). forge-init runs in `--recover` mode: steps S03–S14 verify (expect `verified`, not `created`), S08 pushes the GitHub clone's `main` only if Forgejo's `main` is an ancestor, and missing tokens are re-minted.

## 19. Upgrade behavior

- **Selection rule:** latest Forgejo **LTS** minor/patch, through the forgejo-helm chart version that ships it. Renovate (plan 23) proposes patch updates within the LTS line automatically. LTS→LTS majors are human-initiated.
- **Backup-first gate (mandatory):** the upgrade PR may merge only after `./bootstrap exec -- sc-bootstrap backup now --component forgejo` has produced, within the previous 2 hours, (a) a completed CNPG `Backup` for `forgejo-pg`, (b) a completed Longhorn backup of `forgejo-data`, and (c) a fresh bundle set (plan 44). The command writes `evidence/backups/forgejo/<ts>.json`. Plan 52 adds a CI check on PRs touching `gitops/platform/forgejo/**` version fields that requires a PR comment `/backup-evidence <ts>` referencing an existing evidence object; the Runner validates it (plan 48 owns the general mechanism).
- HelmRelease settings: `upgrade.remediation.retries: 0`, `upgrade.remediation.remediateLastFailure: false`, `install.remediation.retries: 3`. Forgejo DB migrations are forward-only, so Helm rollback does not revert the DB.
- **Rollback:** restore `forgejo-pg` to the pre-upgrade PITR target plus the pre-upgrade Longhorn backup, and revert the version PR (`restore-forgejo.md` §Upgrade rollback).
- Version-sensitive behaviors re-verified on every Forgejo version bump by the P04/P05 integration suite (§22): admin CLI flags, `gitea.oauth` key conversion, `apply_to_admins`, `branch_filter`, `ALLOWED_HOST_LIST` globbing, `SSH_SERVER_HOST_KEYS`, `MINIO_BUCKET_LOOKUP`, healthz format, metrics names.

## 20. Recovery behavior

### 20.1 Whole instance (RPO DB ≤15 min, repos ≤1 h; RTO ≤4 h)
1. CNPG PITR of `forgejo-pg` to target T (plan 12: new `Cluster` from `externalClusters` barman source with `recoveryTarget.targetTime`).
2. Restore `forgejo-data` from the latest Longhorn backup taken before T (plan 13), **or**, when no usable Longhorn backup exists, **rehydrate from bundles** (STO-9): with Forgejo scaled to 0, a Job created from the suspended CronJob template `forgejo-rehydrate` (layer `forgejo-config`; toolbox image; mounts `forgejo-data`; `SC-AGE-BACKUP` from a `forgejo/backup-age` copy and the `repo-backups` reader credential, plan 44/35; no heavy-slot) rebuilds every `<owner>/<name>.git` that exists in the restored DB (lowercase path from table `repository`) from its newest bundle chain per `restore-project-repository.md`; then, with Forgejo started, `forgejo admin regenerate hooks`, `forgejo admin regenerate keys` and `forgejo doctor check --all` (verify subcommands) run, and refs are compared with the manifests.
3. Reconcile DB ↔ Git: run `forgejo doctor check --all` (verify) in the pod, then for every repository whose newest bundle-chain manifest is newer than disk, restore the chain to a scratch mirror per `restore-project-repository.md` and fast-forward refs from it (no deletes). For `strange-company`, fetch from the GitHub mirror and fast-forward `main`, `release/*` and tags only.
4. Re-run forge-init `--recover`.
5. Agent branches lost between T and the incident are treated by the Runner as infrastructure failures (plan 26). This is accepted.

### 20.2 `strange-company` only
GitHub holds `main`, `release/*` and tags (plan 17). Hourly bundles hold all refs. Restore = fast-forward push from those sources (no force). RPO ≈ mirror lag for protected refs.

### 20.3 Single project (SPEC §31)
- **Git data:** procedure `restore-project-repository.md` (plan 44). Bundles are age-encrypted incremental chains at `s3://<prefix>-repo-backups/git/weekly/<org>/<repo>/<chain-id>/<seq>-<full|inc>-<ts>.bundle.age` with per-step manifests (ADR-0202; monthly fulls under `git/monthly/`). The procedure decrypts a chain with `SC-AGE-BACKUP`, re-creates the repository if absent (`POST /orgs/{org}/repos`, settings from `forge.yaml`), and pushes with `--mirror` only into an empty repository, otherwise branch-by-branch fast-forward. No Longhorn volume restore.
- **Metadata (issues, PRs):** live in the DB. If the repo was deleted with its metadata, restore via a scratch instance: CNPG PITR into `forgejo-pg-scratch` in namespace `dr-tests`, start a scratch Forgejo (no ingress, `OFFLINE_MODE=true`, `[mirror] ENABLED=false`, `[cron] ENABLED=false`, `[webhook]` deliveries blocked by `ALLOWED_HOST_LIST=loopback` (verify key semantics), and NetworkPolicy egress deny), `forgejo dump-repo` there, and `forgejo restore-repo` into production (verify both commands). This is an advanced path; the procedure marks it optional.

### 20.4 Recovery invariants
- `SECRET_KEY` and the SSH host key come from Git (the GitHub clone, decrypted with the cluster or offline recovery identity), never regenerated during recovery.
- The GitHub mirror is not re-created if the DB restore brought it back. Credential validity is checked by plan 17's probe.

## 21. Observability

**Metrics:** Forgejo `/metrics` (bearer token) via ServiceMonitor `forgejo/forgejo` (job `forgejo`): `gitea_*` collectors (verify prefix: Forgejo keeps `gitea_` names), plus Go/process metrics. Probe metrics (plan 15 §21 `platform-probes`): `sc_forgejo_ssh_hostkey_match`, `sc_forge_config_drift{object}`, `sc_forge_bot_token_valid{bot}`, `sc_forgejo_api_healthz_up`.
**Blackbox (plan 38):** HTTP `https://git.<d>/api/healthz` (external path through ingress) and TCP `forgejo-ssh.forgejo.svc:22`.
**Logs:** console, JSON where supported. Router and access logs at Info (SPEC §94: no request bodies).
**Dashboards (plan 39):** Forgejo panel set: up, request rate/latency, repositories, open PRs, queue length, PVC usage, push-mirror lag (plan 17).

**Alerts (catalogue names, CD-01; plan 42 registers routing):**

| alertname | Expr (sketch) | for | severity |
|---|---|---|---|
| `SCForgejoDown` | `probe_success{probe="forgejo-http"} == 0` | 3m | critical |
| `SCForgejoSSHDown` | `probe_success{probe="forgejo-ssh"} == 0` | 5m | warning |
| `SCForgejoPodStuckPending` / `SCForgejoPodStuckPendingCritical` | §15 F3 | 10m / 20m | warning / critical |
| `SCForgejoObjectStorageErrors` | §15 F5 | 10m | warning |
| `SCForgejoSSHHostKeyMismatch` | `sc_forgejo_ssh_hostkey_match==0` | 0m | critical |
| `SCForgeBranchProtectionDrift` | `max(sc_forge_config_drift{object=~"branch_protection.*"}) == 1` | 10m | critical |
| `SCForgeConfigDrift` | `max by (object) (sc_forge_config_drift{object!~"branch_protection.*"}) == 1` | 10m | warning |
| `SCForgeBotTokenInvalid` | `sc_forge_bot_token_valid==0` | 15m | critical |
| `SCLonghornVolumeNearlyFull` (plan 13; covers `forgejo-data`) | Longhorn volume usage | per catalogue | warning |

## 22. Testing strategy

- **Unit (Go, `sc-bootstrap forge`):** step planner against a fake Forgejo API (recorded fixtures): fresh, fully configured, partially configured, diverged main, host-key mismatch, token invalid. Each must produce the exact mutation set. Zero mutations on a fully configured fixture.
- **Contract:** `forge/forge.yaml` validates against the proposed schema (F02). The forge-init journal record validates.
- **Offline render (F11):** `helm template` of the Forgejo HelmRelease values produces no `bitnami` image and no deployable Bitnami chart object (vendored `type: library` subcharts allowed, CD-26), no `busybox:latest`, `strategy: Recreate`, `replicas: 1`, rootless image by digest, and `valuesFrom` with `optional: true`.
- **Integration (k3d, CI-runnable without paid APIs; plan 52 target `make e2e-forgejo`):** CNPG + a single-node Longhorn substitute (`local-path` in k3d, with Longhorn-specific checks deferred to the live P04 gate) + SeaweedFS `s3-emulator` as S3 (CD-12, ADR-0205) + Forgejo chart. Runs forge-init twice and asserts idempotency. Verifies OIDC source registration against a mock OIDC provider (dex in test only). Verifies push-mirror creation against a local Git HTTP server standing in for GitHub (verifies `branch_filter`). Verifies branch protection rejects deploy-key pushes to `main`.
- **Live gates:** §23 on the real node.
- **Restore tests:** plan 44 runs a monthly scratch restore (`dr-tests`) of `forgejo-pg` + bundle and records `restore-test-result`.

## 23. Acceptance criteria

### GATE-P04 (Forgejo part; co-owned)

- **16-AC-1** HTTP healthy: `kubectl -n forgejo exec deploy/forgejo -- curl -fsS http://localhost:3000/api/healthz` returns JSON with `"status":"pass"`. `curl -fsS https://git.<BASE_DOMAIN>/api/healthz` from outside the cluster returns 200.
- **16-AC-2** DB uses CNPG: `kubectl -n forgejo exec deploy/forgejo -- grep -A4 '^\[database\]' /data/gitea/conf/app.ini` shows `DB_TYPE = postgres` and `HOST = forgejo-pg-rw.forgejo.svc.cluster.local:5432`. `kubectl -n forgejo get cluster forgejo-pg -o jsonpath='{.status.phase}'` = `Cluster in healthy state`.
- **16-AC-3** Git data uses intended storage: `kubectl -n forgejo get pvc forgejo-data -o jsonpath='{.spec.storageClassName}'` = `longhorn`. The Longhorn volume for it has `numberOfReplicas` = profile value (1 / 2).
- **16-AC-4** Bulk objects use S3: after uploading an attachment and pushing an OCI test image (16-AC-21), `aws s3 ls s3://<prefix>-forgejo-objects/attachments/` and `.../packages/` (toolbox `aws`/`mc`) list ≥ 1 object each. `/data/gitea/attachments` on the PVC does not exist or is empty.
- **16-AC-5** No Redis / no Bitnami: `kubectl get pods -A -o jsonpath='{..image}' | tr ' ' '\n' | grep -Eic 'redis|valkey|bitnami'` = 0. `app.ini` has `[cache] ADAPTER = memory`, `[session] PROVIDER = db`, `[queue] TYPE = level`.
- **16-AC-6** Actions disabled: `curl -fsS -H "Authorization: token $T" https://git.<d>/api/v1/repos/strange-company/strange-company` returns `"has_actions": false` (verify field), or `app.ini` `[actions] ENABLED = false`.
- **16-AC-7** PSA/security: `kubectl get ns forgejo -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}'` = `restricted`. The Forgejo pod `runAsNonRoot` is true and the image tag ends `-rootless@sha256:…`.
- **16-AC-8** SSH host key is the pre-generated one: `ssh-keyscan -t ed25519 -p 22 forgejo-ssh.forgejo.svc.cluster.local` (from a pod in `flux-system`'s allowed peer set via toolbox `kubectl run`) equals the public key in `SC-FORGEJO-SSH-HOST-KEY`.
- **16-AC-9** `SECRET_KEY` is the Git one: `forgejo-app-secrets` decrypted `SECRET_KEY` equals the value in the rendered `app.ini` (`grep SECRET_KEY`), compared by sha256 only and never printed.

### GATE-P05 (forge-init; owner)

- **16-AC-10** Repository creation works: `GET /api/v1/repos/strange-company/strange-company` → 200, `private: true`, `default_branch: main`. `GET /api/v1/orgs/projects` → 200.
- **16-AC-11** Push works: Forgejo `main` SHA (`GET /api/v1/repos/strange-company/strange-company/branches/main` `.commit.id`) equals the outputs commit SHA recorded in the journal.
- **16-AC-12** Clone works: `git clone ssh://git@forgejo-ssh.forgejo.svc.cluster.local/strange-company/strange-company.git` in a throwaway pod with the Flux deploy key and `forgejo-known-hosts` succeeds, with `StrictHostKeyChecking=yes`. HEAD equals 16-AC-11's SHA.
- **16-AC-13** Flux deploy key is read-only: `GET /api/v1/repos/strange-company/strange-company/keys` contains exactly one key titled `flux-readonly` with `read_only: true`. A `git push` with it is rejected (exit ≠ 0, stderr contains `read only` or `denied`).
- **16-AC-14** Bots and scopes: for each of `sc-runner`, `sc-renovate`, `sc-backup`, `GET /api/v1/user` with the token from its decrypted Secret returns that login. `sc-backup`'s token gets 403 on `POST /api/v1/repos/strange-company/strange-company/issues`. `sc-runner` is in team `sc-automation` of both orgs.
- **16-AC-15** Woodpecker OAuth app: `GET /api/v1/user/applications/oauth2` as `sc-woodpecker` lists `woodpecker` with redirect URI `https://ci.<d>/authorize`. The client ID equals `WOODPECKER_FORGEJO_CLIENT` in the SOPS output.
- **16-AC-16** Outputs committed before transfer: `git log -1 --format=%s <outputs_commit>` = `bootstrap(<cluster>): forge-init outputs` (plan 19 T0(b) subject) with trailers `SC-Install-Uid` and `SC-Run-Id`; the journal `pushed_sha` equals Forgejo `main` after S19 (SEED-9). Every §4.4 `*.sops.yaml` file in that commit decrypts with the offline recovery identity and contains `sops.age` entries for both recipients. The journal `outputs_commit` is an ancestor of, or equal to, the SHA Flux first reports after the switch (plan 19 check).
- **16-AC-17** Idempotent: a second `./bootstrap forge-init` exits 0 with every step outcome `verified` or `skipped`, makes zero mutating API calls (toolbox `--audit-requests` log contains no POST/PATCH/PUT/DELETE), and creates no new commit.
- **16-AC-18** Branch protection (bootstrap stage): `GET .../branch_protections/main` shows `enable_push_whitelist: true`, `push_whitelist_usernames: ["sc-admin", "sc-breakglass"]`, `push_whitelist_deploy_keys: false`, `required_approvals: 1`, `apply_to_admins: true` (verify field). A push to `main` with a write deploy key created for the test is rejected. Release-lane protection (SEC-1): with the same test write deploy key, `git push <key> <main>:refs/heads/release/sc-probe` (branch creation) and `git push <key> <main>:refs/tags/v0.0.0-probe` are both rejected (exit ≠ 0); `renovate/*` rejects a push by any identity other than `sc-renovate` (TDD-16).
- **16-AC-35** Rehydrate from bundles (STO-9), k3d `make e2e-forgejo`: DB restored, `forgejo-data` empty, three repositories rehydrated by `forgejo-rehydrate`; refs equal the manifests, a push to protected `main` with a write deploy key is rejected, and a push to an unprotected branch fires the Runner webhook (test receiver).
- **16-AC-36** Bitnami rule (CD-26): `Chart.lock`/`Chart.yaml` of the pinned Forgejo chart list no dependency whose `repository` matches `bitnami`; every vendored subchart under `charts/` whose name or source is Bitnami has `type: library`; `helm template` output contains no image reference matching `bitnami`.
- **16-AC-37** `sc-admin` seed password retired (AUTH-9): after `forge finalize`, basic auth with the SOPS seed value returns 401; `GET /api/v1/users/sc-admin/tokens` (admin) lists only the `platform-probes` token; `sc_forge_unexpected_admin_tokens` = 0.
- **16-AC-34** Deploy-key namespace restriction evaluated (SEC-2): on the pinned Forgejo, a fixture repository with rule `**` (push whitelist humans and bots, `push_whitelist_deploy_keys: false`, creation included) and a higher-priority rule `agent/**` (deploy keys allowed) is tested: a write deploy key can create and push `agent/sc-0-probe` and cannot create `scratch/x`, `renovate/x` or `promote/x`. The result is recorded in `versions/compat.yaml` (`forgejo.deploy_key_namespace_protection: supported|unsupported`); if `supported`, the two rules are added to `forge.yaml` for both orgs in the same PR and SI-06 changes from detection to prevention.
- **16-AC-19** Break-glass account exists: `sc-breakglass` passes `GET /api/v1/user` with API basic auth using the staged password, and has `is_admin: true` and `source_id`/`login_source` 0 (local). The staged password is listed in the recovery-bundle manifest (plan 35); in Git it exists only in `secrets/offcluster/<cluster>/break-glass-forgejo.sops.yaml`, whose `sops.age` stanza lists only the recovery recipient.
- **16-AC-20** Webhooks present and inactive: `GET /api/v1/orgs/strange-company/hooks` contains `agent-runner` with `active: false`.
- **16-AC-21** OCI registry works: trusted push path stand-in at P05: `oras push git.<d>/strange-company/gate-probe:p05` with an `sc-admin` token, then `oras pull` succeeds. The test package is deleted afterwards (plan 23 owns real image pushes).
- **16-AC-22** Metrics visible: `curl -H "Authorization: Bearer <metrics token>" http://forgejo-http.forgejo.svc:3000/metrics | grep -c '^gitea_repositories'` ≥ 1, and the same request without a token returns 401. (Prometheus `up{job="forgejo"}==1` is checked at GATE-P09 by plan 38.)
- **16-AC-23** forge-init recorded: the journal record validates against the forge-init result schema, with `result: succeeded`. `config/forge-init.yaml` in Git has `ssh_host_key_fingerprint` equal to `ssh-keygen -lf` of the Secret's public key.

### GATE-P06 (consumed; Forgejo-side checks)
- **16-AC-24** Flux sources Forgejo over SSH: `kubectl -n flux-system get gitrepository flux-system -o jsonpath='{.spec.url}'` starts `ssh://git@forgejo-ssh.forgejo.svc.cluster.local/`, and `.status.artifact.revision` contains Forgejo's `main` SHA.
- **16-AC-25** Mirror receives commit: covered by 17-AC-1..3.

### GATE-P07 (Forgejo-side OIDC checks; plan 15 owns the gate)
- **16-AC-26** Authentik login works: `curl -s -o /dev/null -w '%{http_code} %{redirect_url}' https://git.<d>/user/oauth2/authentik` returns `307` (or `302`) with a redirect to `https://auth.<d>/application/o/authorize/`. The plan 15 synthetic login (15-AC-12) results in a Forgejo session whose user is in team `engineers` of `strange-company`.
- **16-AC-27** Break-glass access works with Authentik down: with Authentik scaled to 0 (`flux suspend hr authentik` + `kubectl scale`), the web login form at `https://git.<d>/user/login` accepts `sc-breakglass`, and within 5 min `sc_breakglass_last_login_timestamp_seconds{system="forgejo"}` on the `platform-probes` endpoint shows the login (alert firing is checked at P09, plan 15 15-AC-26b).
- **16-AC-31** Group→team mapping re-verification: `forgejo admin auth list` shows source `authentik`. A test user in `sc-viewers` only lands in team `viewers` and has no write permission (`PUT /repos/.../contents/x` → 403).

### GATE-P10 (Forgejo-side; plan 44 owns backup machinery)
- **16-AC-28** Backup works: the latest CNPG backup for `forgejo-pg` is `completed` and less than 26 h old. The WAL archive shows `ContinuousArchiving=True`. The latest bundle for `strange-company` exists in `repo-backups` and is less than 2 h old.
- **16-AC-29** Restore works: the plan 44 restore test for Forgejo (RT-7 `rt-app-forgejo`: scratch `forgejo-pg` PITR + scratch Forgejo start, `/api/healthz` pass, every repository name recorded from production at T exists in the scratch DB; bundle chains verified by `rt-repo-bundle`) records `restore-test-result` `outcome: passed` within the last 35 days.
- **16-AC-30** Required alerts exist: `kubectl -n monitoring get prometheusrule -o json | jq -r '..|.alert? // empty'` contains `SCForgejoDown`, `SCForgejoSSHDown`, `SCForgejoPodStuckPending`, `SCForgejoPodStuckPendingCritical`, `SCForgejoSSHHostKeyMismatch`, `SCForgeBranchProtectionDrift`, `SCForgeConfigDrift`, `SCForgeBotTokenInvalid`, `SCForgejoObjectStorageErrors`, `SCLonghornVolumeNearlyFull`.

### GATE-P12 (Forgejo-side; plan 50/19 own)
- **16-AC-32** Finalized protection: `GET .../branch_protections/main` shows `push_whitelist_usernames` = `[]`, `enable_status_check: true`, `status_check_contexts` ⊇ `["ci/woodpecker/pr/ci"]` at P12 and ⊇ `["ci/woodpecker/pr/ci", "strange-company/tdd-provenance", "strange-company/review", "strange-company/release-provenance"]` from C09 (55-AC-10). `GET .../tag_protections` lists `*`, `v*`, `sc-archive/*`, `sc-quarantine/*` with the `forge.yaml` whitelists. `forge verify` exits 0 (no drift).

**SPEC §124 example Forgejo gate mapping:** HTTP healthy → 16-AC-1; Authentik login works → 16-AC-26; break-glass access works → 16-AC-27, 16-AC-19; DB uses CNPG → 16-AC-2; Git data uses intended storage → 16-AC-3 (+ 16-AC-4 objects); repository creation works → 16-AC-10; push works → 16-AC-11; clone works → 16-AC-12; mirror receives commit → 17-AC-1..3; backup works → 16-AC-28; restore works → 16-AC-29; metrics visible → 16-AC-22 (+ plan 38 at P09); required alerts exist → 16-AC-30.

## 24. Explicit non-goals

- Multi-replica Forgejo or a Redis/valkey-backed queue (SPEC §127 spirit; single replica is sufficient).
- Forgejo Actions (Woodpecker is CI).
- Public Git-over-SSH in tunnel mode.
- Code search indexer.
- An in-cluster continuous reconciler for `forge.yaml` (post-MVP; MVP uses `forge apply` + drift alert).
- Forgejo federation, package proxying, pull mirrors of arbitrary upstreams (except plan 17's optional upstream sync).
- Automatic merge (ADR-0019).

## 25. Open questions

| ID | Question | Blocking | Proposed resolution | Owner |
|---|---|---|---|---|
| Q-16-01 | Contracts for `forge.yaml` and the forge-init record. | no | Resolved at consolidation: `contracts/forge-configuration.schema.yaml`, `contracts/forge-init-result.schema.yaml` (validated). | 16 |
| Q-16-02 | forgejo-helm (verified 2026-10-13) declares the Bitnami **`common` library chart**. | no (resolved by CD-26, superseding CD-20) | Allowed: it is a template-only library vendored inside the upstream chart package. F11 enforces `type: library`, no Bitnami repository/registry fetch and no Bitnami image (16-AC-36). | 16 / 18 |
| Q-16-03 | ADR-0016 says forge-init "generates and records" the SSH host key. This plan generates it pre-seed (plan 50) so the seed HelmRelease can mount it and the key survives PVC loss. forge-init verifies and records it. | no | Treat as a refinement of ADR-0016, no change in intent. | lead |
| Q-16-04 | `sc-runner` needs repo-admin to manage deploy keys, which also allows editing branch protection (R-16-1). | no | Detection (`SCForgeBranchProtectionDrift`) + Runner code never calls protection APIs (plan 26 test). Post-MVP: evaluate a Forgejo change or a deploy-key broker. Plan 34 records the residual risk. | 34 / 55 |
| Q-16-05 | Required status contexts cannot be enforced before their producers exist (Woodpecker at P08, Runner at C09). Between P05 and P12, `main` is protected by push restriction + approval only. | no | Staged protection (§14.3). Plan 55 owns the context list and the C09 `forge apply`. | 55 |
| Q-16-06 | Runner webhook target: resolved to `agent-runner.strange-company.svc.cluster.local:8083/v1/hooks/forgejo` (CD-03, plan 26). Plan 26 §… also describes the Runner ensuring **repository-level** hooks itself, while this plan declares **org-level** hooks in `forge.yaml`. | no | **Resolved (CD-18).** One mechanism: org-level hooks from `forge.yaml` (Git-explainable, drift-detected); the Runner only verifies them. Plan 26 to align at C03. | 26 |
| Q-16-07 | Whether Forgejo's admin `sudo` would remove the need to set bot passwords (S10). | no | Keep the password-reset approach. It needs no undocumented behavior. | 16 |
| Q-16-08 | `sc-admin` dual use by automation and the human operator (AUTH-9). | no | Seed password retired at `forge finalize`; steady value bundle-only; short-lived exec-minted tokens; human `forge apply` uses the operator's own admin identity; unexpected tokens alert. Residual: a cluster admin can exec into Forgejo (same privilege; recorded for plan 34). Plan 35 records the two storage values. | 16 / 35 |
| Q-16-09 | Emergency merges: plan 55's `emergency-merge.md` uses stage `bootstrap` with `sc-breakglass` whitelisted (fast-forward push of a reviewed commit); this plan also defines a PR-based `recovery` stage. | no | Plan 55's procedure is authoritative; `recovery` stays as the alternative for "Runner statuses only" outages. Both are audited by the plan 55 merge auditor. | 55 / 16 |
