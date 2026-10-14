# 20 — Woodpecker (trusted CI)

**Status:** Stage-0 plan (draft)
**DAG nodes:** P08 (owner; co-owner 52), C04 (co-owner with 21 and 22: Woodpecker trigger and evidence retrieval end to end with fakes), P12 (contributes the Woodpecker checks; owners 50 and 19)
**Related plans:** [01 repository](01-repository.md), [12 CNPG](12-cnpg.md), [15 Authentik](15-authentik.md), [16 Forgejo](16-forgejo.md), [17 GitHub mirror](17-github-mirror.md), [18 Flux](18-flux.md), [21 TDD workflow](21-tdd-workflow.md), [22 verification policy](22-verification-policy.md), [23 supply chain](23-supply-chain.md), [26 Agent Runner](26-agent-runner.md), [35 secrets](35-secrets.md), [36 network policy](36-network-policy.md), [37 admission](37-admission-policy.md), [38 Prometheus](38-prometheus.md), [42 alerting](42-alerting-notifications.md), [43 retention](43-retention-gc.md), [44 backup](44-backup.md), [45 recovery](45-recovery.md), [48 upgrades](48-upgrade.md), [50 bootstrap](50-bootstrap-orchestrator.md), [51 capacity](51-capacity-and-scheduling.md), [52 CI strategy](52-ci-strategy.md), [55 forge workflow](55-forge-workflow.md)
**Contracts:** consumes `red-result.schema.yaml` and `green-result.schema.yaml` (plan 22; the evidence documents carried by §8.5), `secret-classes.yaml` (35), `alert-catalog.yaml` (42), `bootstrap-journal.schema.yaml` (50), `runner-api.openapi.yaml` (26; adds the two intent endpoints in §7.3). Owns `contracts/ci-policy.schema.yaml` (kind `CIPolicy`, §9.2; the `tddDiscipline` section is plan 52's).
**ADRs:** relies on ADR-0006, 0007, 0008, 0013, 0014, 0015, 0017, 0018, 0019, 0553. Introduces [ADR-0301](../adr/0301-woodpecker-trust-model-exclusive-config-extension.md) (trust model), [ADR-0302](../adr/0302-verification-trigger-intents-and-log-evidence.md) (verification trigger and evidence transport), [ADR-0305](../adr/0305-ci-workspace-bounded-local-volume.md) (CI workspace storage).
**Procedures:** [`woodpecker-failure.md`](../procedures/woodpecker-failure.md).

> Every section below is mandatory (SPEC §121). Requirements carry SPEC trace tags.

## 1. Purpose

Woodpecker is the trusted CI [SPEC §36]. This plan defines:
- the deployment: server, two agents and the Kubernetes backend, PostgreSQL on `woodpecker-pg`, and pipeline pods in `ci-jobs` (and `ci-build` for plan 23's release lane);
- Forgejo OAuth integration and human login (Forgejo, which delegates to Authentik);
- the **trust model** (ADR-0301): every pipeline configuration is served by an exclusive config extension `sc-ci-config` from a trusted revision, so untrusted refs never get secrets, the build lane, privileged pods or their own YAML;
- the **ADR-0008 verification mechanism** (ADR-0302): how the Runner triggers a RED or GREEN pipeline for an exact commit, how the trusted `verify` workflow is selected, how evidence leaves the pipeline and how the Runner proves the pipeline's identity;
- concurrency, resources, timeouts, cleanup, metrics, alerts, failure handling, bootstrap (`ci-init`), upgrade and recovery.

Plan 52 owns *which* Make targets run on which event. Plan 22 owns the semantics of RED/GREEN and the `sc-verify` tool. Plan 23 owns the build lane contents, signing and release secrets.

## 2. Requirements derived from SPEC.md

1. Woodpecker provides trusted CI for formatting, lint, unit tests, integration tests, TDD RED/GREEN verification, schema, Helm and Kubernetes validation, security checks, image builds, SBOM and release artifacts [SPEC §36]. Agent-local validation is advisory [SPEC §36].
2. Trusted CI runs the new tests against pre-implementation code and records `RED_VERIFIED` [SPEC §50]. It reruns task tests, relevant tests and all repository-required validation for `GREEN_VERIFIED` [SPEC §54, §118, §129].
3. Woodpecker has its own database on CNPG [SPEC §25, ADR-0006]. Its state is recoverable [SPEC §108].
4. Woodpecker is a Prometheus target and dashboard subject, with a Woodpecker failure alert [SPEC §91, §92, §96].
5. Workers never receive CI, registry-push, signing or deployment credentials [SPEC §37, §38, §86]. CI does not deploy production [SPEC §33].
6. CI uses disposable workspaces where practical [SPEC §28]. Longhorn does not hold CI scratch [SPEC §27]. Root disks do not fill [SPEC §107].
7. CI is protected and bounded: CI has lower scheduling priority than the platform [SPEC §89], and `maximumConcurrentCIJobs` is 1 at self-hosting-minimum [SPEC §11, §89].
8. Human identity comes from Authentik, and machine identity uses scoped credentials [SPEC §21].
9. Default CI does not need paid model APIs [SPEC §104].
10. A procedure exists for Woodpecker failure [SPEC §113].
11. **20-R-1** No pipeline executes YAML read from the commit under test unless that commit is on a protected ref (ADR-0301).
12. **20-R-2** No step pod for an untrusted pipeline receives any Woodpecker secret, a ServiceAccount token, a hostPath, a privileged security context or a pod in `ci-build` (ADR-0301).
13. **20-R-3** Evidence is accepted only from a pipeline that the Runner created, against a registered intent, at the expected commit, with the configuration served for that intent (ADR-0302).
14. **20-R-4** Every step pod complies with PSA `restricted` in `ci-jobs` and uses only the `ci-workspace` PVC of its own workflow (ADR-0305).
15. **20-R-5** All Woodpecker and plugin images and the chart are digest- or version-pinned (`versions/platform.lock.yaml`, plan 01).

## 3. Inputs

| Input | Source | Notes |
|---|---|---|
| Woodpecker chart version and image digests (server, agent, `plugin-git`, permission-init image) | `versions/platform.lock.yaml` (plan 01) | Selection rule §9.1 |
| OAuth app client ID and secret (`SC-WOODPECKER-OAUTH`) | forge-init S13 (plan 16) → SOPS Secret `woodpecker/woodpecker-forgejo-oauth` | Keys `WOODPECKER_FORGEJO_CLIENT`, `WOODPECKER_FORGEJO_SECRET` |
| Agent credentials (`SC-WOODPECKER-AGENT`, one instance per agent) | per-agent tokens minted by `ci-init` W07 (`POST /api/agents`, verify), SOPS `gitops/clusters/<c>/secrets/woodpecker/agent-token-{ci,build}.sops.yaml`; the pre-seed shared value (`woodpecker-agent-secret.sops.yaml`) is mounted only by the server, never by an agent (§9.3) | |
| `woodpecker-pg` app Secret `woodpecker-pg-app` | plan 12 | `uri` key |
| Forgejo read token for `sc-ci-config` (bot `sc-ci-config`, class `SC-FORGEJO-BOT-TOKENS`) | plan 16 `forge.yaml` bot + forge-init S10 | Secret `woodpecker/forgejo-sc-ci-config` (assumed, §5) |
| Cluster variables `BASE_DOMAIN`, `SC_SERVICE_CIDR`, `SC_OPERATOR_FORGEJO_USER`, `CI_MAX_CONCURRENT` | `cluster-vars` (plans 18, 50) | `CI_MAX_CONCURRENT` = profile `maximumConcurrentCIJobs` (plan 51) |
| CI policy `policy/ci.yaml` (`CIPolicy`) | Git, protected path | §9.2 |
| `ci-workspace` StorageClass and filesystem | plans 08, 11 (ADR-0305) | |
| PriorityClass `ci`, ResourceQuota and LimitRange for `ci-jobs` | plan 51 | |
| Admission rules for `ci-jobs`/`ci-build` | plan 37 | §13.4 lists what is assumed |
| NetworkPolicies | plan 36 | §13.3 |

## 4. Outputs

1. A running Woodpecker at `https://ci.<BASE_DOMAIN>` (Service `woodpecker/woodpecker-server`, ports 8000 HTTP, 9000 gRPC, 9001 metrics).
2. `sc-ci-config` (Go, `src/cmd/sc-ci-config`): the exclusive configuration extension (ADR-0301), Service `woodpecker/sc-ci-config` port 8080, metrics 9464.
3. `sc-ci-maint` (Go subcommand of `sc-ci-config`, CronJob in `woodpecker`): orphan cleanup, log pruning, drift checks (§8.7).
4. Agents `woodpecker-agent` (lane `ci`, pods in `ci-jobs`) and `woodpecker-agent-build` (lane `build`, pods in `ci-build`; plan 23 owns what runs there).
5. Woodpecker users and credentials created by `ci-init` (§14.2): Woodpecker users `sc-admin` (admin), `sc-woodpecker` (repository activator), `sc-runner` (verification trigger). Persisted tokens:
   - `SC-WOODPECKER-RUNNER` (Secret `strange-company/agent-runner-woodpecker`);
   - `SC-WOODPECKER-MAINT` (new, Secret `woodpecker/sc-ci-maint-token`);
   - `SC-CI-INTENTS` (new, Secrets `woodpecker/sc-ci-config-intents` and `strange-company/agent-runner-ci-intents`).
6. Activated repositories with policy-conformant settings, and the `nightly` cron on `strange-company`.
7. The verification workflow contract (`.woodpecker/verify.yaml` shape, §8.4) and the evidence framing (§8.5).
8. Metrics, alerts (§21) and the `ci-init` journal record (§14.2 W12).

## 5. Dependencies

**Hard (DAG):**
- P08 depends on P06 (Flux sources Forgejo, which delivers the `woodpecker-db` and `woodpecker` layers; forge-init outputs exist).
- C04 depends on C01 (Runner core), P08 and F06 (verifier library, plan 22).

**Soft and runtime:**
- `woodpecker-pg` (plan 12), Forgejo HTTP API and Git (plan 16), Traefik and cert-manager for `ci.<BASE_DOMAIN>` (plans 09, 10), Prometheus at P09 (plan 38), the Runner from C01 (plan 26).
- Woodpecker login does not depend on Authentik directly. Forgejo does (P07), so human login to Woodpecker works once Forgejo SSO works. Bots log in with local Forgejo credentials.

**Interfaces assumed from other plans (they must honor these):**
- **Plan 16:**
  - `forge.yaml` gains bot `sc-ci-config` (token scopes `read:repository`, team `sc-bots-read` in both orgs, Secret `woodpecker/forgejo-sc-ci-config`).
  - Forgejo `[webhook] ALLOWED_HOST_LIST` includes `woodpecker-server.woodpecker.svc.cluster.local`.
  - Tag protection on `v*` in `strange-company` allows only `sc-admin`, the release identity (plan 23) and humans in `engineers` with approval. Deploy keys cannot push protected tags (verify Forgejo tag-protection semantics for deploy keys).
  - Forgejo `[oauth2] REFRESH_TOKEN_EXPIRATION_TIME` stays at least 720 h (verify the default).
- **Plan 26 (Runner):**
  - exposes `GET /v1/ci/intents` and `POST /v1/ci/intents/{intent_id}/served` on the hooks listener `:8083`, authenticated by bearer `SC-CI-INTENTS` (§7.3);
  - records intents before triggering (ADR-0302);
  - validates pipeline identity as in §8.6.
- **Plan 22:**
  - `sc-verify` is a static Go binary published in the image `sc-verify`;
  - `.strange-company/validation.yaml` (`contracts/repo-validation.schema.yaml`) declares `spec.verification.image` (toolchain image digest), `spec.verification.suites[]` (RED/GREEN test suites) and `spec.verification.checks[]` (GREEN-only required checks); `sc-verify` runs them by phase;
  - `sc-verify run` emits exactly one framed evidence block (§8.5) and exits 0 iff it emitted one;
  - plan 22 owns the verification timeout values (the Runner cancels pipelines on expiry).
- **Plan 23:** owns `ci-build` (PSA level, admission exception, quota), `.woodpecker/release.yaml`, the release secrets (Woodpecker secrets with events `[tag]` and image filter `sc-release`; maintenance-only secrets such as `sc_ci_ovh_readonly` may use `[cron]`), and the release gate on the off-cluster lane (ADR-0303).
- **Plan 51:**
  - PriorityClass `ci` (below `application`, above `agent-worker`);
  - `CI_MAX_CONCURRENT` per profile (1 at self-hosting-minimum, proposed 2 at small-ha);
  - the node extended resource **`strange.company/heavy-slot`** (CD-05, plan 51 §8.4, ADR-0156), the only heavy-execution mutual-exclusion mechanism: ci-lane step and service containers carry 1 unit, build-lane containers 8 units (LimitRange defaults in `ci-jobs`/`ci-build`, plan 51; `sc-ci-config` also forces the value, §8.2 step 6). In exclusive mode one build workflow excludes all other heavy execution; the ci lane is bounded by `CI_MAX_CONCURRENT`. This replaces the `ci-slot` proposed earlier.
- **Plans 08/11/37:** ADR-0305 (`ci-workspace` filesystem, StorageClass, admission rule).
- **Plan 09:** in-cluster resolution of `git.<BASE_DOMAIN>` and `ci.<BASE_DOMAIN>` to the Traefik ClusterIP (CoreDNS rewrite), so in-cluster clients do not hairpin through Cloudflare. Without it, clones go through the public edge, which works but is slower.
- **Plan 50:** CLI `./bootstrap ci-init` (run automatically after `sso-init` and before `verify`) and `./bootstrap exec -- sc-bootstrap ci login|verify|rotate|recover`.
- **Plan 17:** the read-only `SC-GITHUB-PROBE` token gains `Actions: read` (ADR-0303).

## 6. Owned state

| State | Location | System of record | Writers | Backup |
|---|---|---|---|---|
| Users (with forge OAuth access/refresh tokens), repositories, settings, secrets (plan 23), crons, pipelines, step state, logs, agents, server signing key | `woodpecker-pg` (S-12/S-23, plan 11) | Woodpecker | Woodpecker server only | CNPG WAL + daily base backup (plan 12) |
| Desired CI policy | `policy/ci.yaml` (Git) | Git | humans by PR | Git |
| Verification intents and served-config attestations | `sc-pg` (plan 26 tables) | Runner | Runner (served attestation via the extension's POST) | plan 12 |
| RED/GREEN evidence documents | S3 `evidence` + digests in `sc-pg` | Runner | Runner | plan 14 versioning |
| CI workspaces | `ci-workspace` PVCs (ephemeral) | none (disposable) | Woodpecker agents | none |

Woodpecker pipeline logs are **not** authoritative evidence. They are retained for 90 days (§8.7), and anything that matters is copied by the Runner.

## 7. External interfaces

### 7.1 Human
- Web UI `https://ci.<BASE_DOMAIN>`. Login: Woodpecker → Forgejo OAuth (`/login/oauth/authorize`) → Forgejo session, which comes from Authentik OIDC for humans (plan 15/16).
- Registration is open only to members of the Forgejo orgs `strange-company` and `projects` (`WOODPECKER_OPEN=true`, `WOODPECKER_ORGS=strange-company,projects`).
- Woodpecker admins: `sc-admin` and `${SC_OPERATOR_FORGEJO_USER}`.

### 7.2 Machine (Woodpecker API, base `http://woodpecker-server.woodpecker.svc:8000/api`)

All paths are as of Woodpecker 3.x (verify each at P08; `make e2e-woodpecker` re-verifies on every version bump).

| Caller | Operation | Endpoint |
|---|---|---|
| Runner (`sc-runner`) | resolve repository ID | `GET /api/repos/lookup/{owner}/{name}` |
| Runner | create verification pipeline | `POST /api/repos/{repo_id}/pipelines` body `{"branch": "agent/<task-id>", "variables": {...}}` (`PipelineOptions`: `branch`, `variables`, `message`; no commit field) |
| Runner | read pipeline and steps | `GET /api/repos/{repo_id}/pipelines/{number}` (`workflows[].children[]` = steps with `id`, `name`, `state`, `exit_code`) |
| Runner | read step log | `GET /api/repos/{repo_id}/logs/{number}/{step_id}` → array of `{id, step_id, time, line, data (base64), type}` |
| Runner | cancel on deadline | `POST /api/repos/{repo_id}/pipelines/{number}/cancel` |
| Woodpecker server → `sc-ci-config` | configuration extension | `POST http://sc-ci-config.woodpecker.svc.cluster.local:8080/v1/config`, ed25519 HTTP-signed. Payload `{repo, pipeline, netrc?}`. Response `{"configs":[{"name","data"}]}` |
| `sc-ci-config` → server | signature public key | `GET /api/signature/public-key` (PEM) |
| `ci-init`/`sc-ci-maint` | repos, settings, crons, tokens, log deletion | `GET /api/user/repos?all=true`, `POST /api/repos?forge_remote_id=<id>`, `PATCH /api/repos/{id}`, `POST /api/repos/{id}/cron`, `POST /api/user/token`, `DELETE /api/repos/{id}/logs/{number}` |
| Forgejo → server | webhooks | `POST http://woodpecker-server.woodpecker.svc.cluster.local:8000/api/hook` (registered on activation, host from `WOODPECKER_EXPERT_WEBHOOK_HOST`) |
| Prometheus | metrics | `http://woodpecker-server.woodpecker.svc:9001/metrics` (unauthenticated dedicated listener, NetworkPolicy-restricted; this replaces the bearer-token option in plan 38 §21) |

Commit statuses posted to Forgejo use context `ci/woodpecker/<event>/<workflow>`. The default `WOODPECKER_STATUS_CONTEXT=ci/woodpecker` and the format `<context>/<event>/<workflow>` with `pull_request` rendered as `pr` are to be verified. The **required context** for protected branches is `ci/woodpecker/pr/ci` (consumed by plans 16 and 55).

### 7.3 Runner intent endpoints (implemented by plan 26; contract owned by plan 26's `runner-api.openapi.yaml`)

- `GET /v1/ci/intents?repo=<owner/name>&branch=<branch>&commit=<40hex>`
  - 200 with `{"intent_id","task_id","execution_id","phase","subject","red_sha","protected_tests","baseline","base_sha","expected_sha","nonce","expires_at"}` when an unexpired intent matches all three keys (`subject` ∈ {`tests`,`judge`}; `red_sha` is the generation's red commit (GREEN only, for the isolation run and RED replay, plan 22); `protected_tests` is a list of `{kind, id}` with kind `change`|`preservation`|`other`, empty except for GREEN; `baseline` ∈ {`run`,`skip`}; plans 21/22);
  - 404 otherwise.
- `POST /v1/ci/intents/{intent_id}/served` with `{"config_revision","config_sha256","workflow_names":[...],"served_at"}` returns 204. It is idempotent; a second call with different content returns 409.
- Auth: `Authorization: Bearer <SC-CI-INTENTS>`. NetworkPolicy allows only `sc-ci-config` pods to reach `agent-runner:8083` on these paths. Other hooks traffic (Forgejo webhooks) is authenticated by HMAC (plan 26).

### 7.4 Pipeline files consumed (all under `.woodpecker/`, a protected path)

| File | Workflow | Lane | Events (`when`) | Owner of content |
|---|---|---|---|---|
| `ci.yaml` | `ci` | ci | `pull_request`; `push` to `main`, `release/*`; `manual` | plan 52 |
| `verify.yaml` | `verify` | ci | `manual` (served only for a registered intent) | this plan (shape) / plan 22 (command) |
| `nightly.yaml` | `nightly` | ci | `cron` (`nightly`) | plan 52 |
| `release.yaml` | `release` | build | `cron`, `tag` | plan 23 |

## 8. Internal interfaces

### 8.1 Components

| Component | Kind | Namespace | Image | Replicas |
|---|---|---|---|---|
| `woodpecker-server` | StatefulSet from the chart (persistence disabled) | `woodpecker` | `docker.io/woodpeckerci/woodpecker-server:v3.<m>.<p>@sha256:…` | 1 (both profiles; Woodpecker server is not horizontally scalable, verify) |
| `woodpecker-agent` | Deployment | `woodpecker` | `woodpeckerci/woodpecker-agent@sha256:…` | 1 |
| `woodpecker-agent-build` | Deployment (second HelmRelease of the chart's agent component, server disabled; verify chart support) | `woodpecker` | same image | 1 |
| `sc-ci-config` | Deployment | `woodpecker` | internal image `sc-ci-config@sha256:…` (plan 23) | 1 (2 at small-ha, stateless) |
| `sc-ci-maint` | CronJob `17 * * * *` | `woodpecker` | same image as `sc-ci-config` | — |
| `woodpecker-pg` | CNPG `Cluster` | `woodpecker` | plan 12 | 1 / 3 |

Before P11 (internal registry and supply chain), `sc-ci-config` comes from the public release registry like the toolbox (plan 50 OQ-50-2, plan 23).

### 8.2 Configuration extension `sc-ci-config` (ADR-0301)

Request handling, in order. Any failure returns non-2xx, so Woodpecker creates **no** runnable pipeline.

1. **Signature.** Verify the HTTP message signature (ed25519, RFC 9421 style as implemented by Woodpecker; verify the header set) against the public key fetched from `GET /api/signature/public-key`. The key is cached and refetched every 10 minutes and on verification failure (once).
   - Reject with 401 if the signature is missing or invalid, or if its `created` parameter is older than 300 s.
   - Metric `sc_ci_config_signature_failures_total`.
   - On a key change the info metric `sc_ci_config_signing_key_info{fingerprint}` changes value.
2. **Repository policy.** From `repo` in the payload:
   - the owner must be in `CIPolicy.spec.managedOwners`;
   - `trusted.network`, `trusted.volumes` and `trusted.security` must all be false (v3 field shape; verify);
   - `timeout` must be ≤ `repoSettings.maxTimeoutMinutes`.
   Any violation returns 403 `REPO_POLICY_VIOLATION` (metric `sc_ci_config_repo_policy_violations_total{check}`).
3. **Trust classification** (`pipeline.event`, `pipeline.branch`, `pipeline.ref`, `pipeline.commit`):

   **Protected branches** are `main` plus the exact names in `CIPolicy.spec.releaseBranches` (empty in MVP; a release branch is added by PR to `policy/ci.yaml` and `forge.yaml` together). Glob matching on branch **names** is never trusted, and a branch counts only if Forgejo also reports an active protection rule covering it (`GET /repos/{o}/{r}/branch_protections`, cached 60 s). A worker that creates `release/x` with its deploy key therefore gains nothing (SEC-1).

   | Event | Condition | Trust | Config revision |
   |---|---|---|---|
   | `push` | branch is a protected branch (above), and the Forgejo branch head is `commit` or has `commit` as an ancestor | protected | `commit` |
   | `tag` | `ref` matches the **anchored regex** `releaseTagPattern` (`^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$`, plan 23), and `commit` is an ancestor of (or equal to) the head of `main` or of a listed release branch (Forgejo `GET /repos/{o}/{r}/compare/{commit}...{branch}`: `branch` ahead and 0 behind, verify response shape) | protected | the **head of the branch that contains the tag** (`main`, or the listed release branch), never the tag commit itself (SEC-1). The release workflow checks out and builds the tag commit; its pipeline definition comes from reviewed branch state. **Pusher check (SEC-1, plan 26):** before classifying a tag as protected, `sc-ci-config` calls the Runner's `GET /v1/ci/push-records?repo=<repo>&ref=refs/tags/<tag>` (port 8083, bearer `SC-CI-INTENTS`). The tag is untrusted unless exactly one record exists, its `after` equals `commit`, and the pusher (login and `pusher_kind`) is listed in **`security/release-pushers.yaml`** (owned by plan 23; the single source of the allow-list, read by `sc-ci-config` from the default-branch head; no list is kept in this plan or in `policy/ci.yaml`). A `deploy_key` pusher, a missing record or an unreachable Runner all yield `untrusted`, failing closed. The served `release.yaml` repeats the same call as its first step, before any `from_secret` value is used; a failure there aborts the workflow |
   | `cron` | branch is a protected branch | protected | `commit` (the branch head) |
   | `manual` | branch matches `agent/sc-*` **and** the Runner returns an intent for `(repo, branch, commit)` | untrusted (verification) | intent `base_sha`, but only if `base_sha` is an ancestor of `commit` **and of the head of the intent's `target_branch`** (field `VerificationIntent.target_branch` in `runner-api.openapi.yaml`, added by plan 26; a protected branch; TDD-10); otherwise 409 `INTENT_BASE_INVALID` |
   | anything else | (all `pull_request`, unprotected `push`, unmatched `tag`, `manual` without intent, `pull_request_closed`, `deployment`, `release`) | untrusted | head of the repository's default branch at request time |

   **Runner independence.** Only the verification row calls the Runner. Every other row works without it, so `sc-ci-config` (P08) serves ordinary CI before the Runner exists (C01) and while it is down; if the intent lookup fails, that manual pipeline gets the untrusted default config (no `verify` workflow), and the Runner retries per plan 22. The Runner↔`sc-ci-config` calls are therefore not a bootstrap cycle.

4. **Fetch.** Read every `.woodpecker/*.yaml` at the config revision through the Forgejo API (`GET /repos/{o}/{r}/contents/.woodpecker?ref=<rev>`, then the raw files) using the `sc-ci-config` bot token. For verification trust, select only `verify.yaml`, or the platform default verify workflow (§8.4) if absent. For other untrusted trust, drop `verify.yaml`.
5. **Filter** (untrusted only). Drop a workflow if it contains any of the following (metric `sc_ci_config_dropped_workflows_total{reason}`):
   - `from_secret` anywhere (`secret_reference`);
   - a `labels` entry other than `sc-lane: ci` (`lane`);
   - `privileged` (`privileged`);
   - step or workflow `volumes` (`volumes`);
   - `backend_options.kubernetes` keys outside `{resources}` (`backend_options`);
   - `services` with ports below 1024 (`service_port`);
   - an image without `@sha256:` or outside `imageRegistries` (`image`).

   In **protected** mode, workflows are not dropped, but the image rule is still enforced. A violation returns 422 so the push fails visibly.
6. **Normalize** (all modes):
   - Set `labels: {sc-lane: <lane>}`. Untrusted is always `ci`. Protected keeps a declared `build`, otherwise `ci`.
   - Insert an explicit `clone` step using `CIPolicy.spec.cloneImage` with `settings: {depth: 0, tags: true}` (full history, needed by `tdd-discipline-check` and `sc-verify`; verify the `plugin-git` setting names).
   - Merge into every step's `backend_options.kubernetes`:
     - `securityContext` from `stepSecurityContext`;
     - `resources` from the lane defaults. A step may request less, never more than `lanes.<lane>.max`. `limits["strange.company/heavy-slot"]` is **always overwritten** with `lanes.<lane>.heavySlotUnits` (ci 1, build 8; CD-05), so a step cannot request 0 and escape the slot.
   - Build lane only (CD-06): workflows must use sequential steps (no `depends_on`) and no `services`; a protected-mode build workflow that violates this gets 422 `BUILD_LANE_SHAPE`.
   - For verification, inject `environment` `SC_PHASE`, `SC_TASK_ID`, `SC_EXPECTED_SHA`, `SC_BASE_SHA`, `SC_INTENT_ID`, `SC_EVIDENCE_NONCE`, `SC_SUBJECT` (`tests`\|`judge`), `SC_RED_SHA` (GREEN only, else empty), `SC_PROTECTED_TESTS` (newline-separated `<kind>\t<test id>` lines, kind `change`/`preservation`/`other`, ≤ 64 KiB, GREEN only, else empty) and `SC_BASELINE` (`run`\|`skip`) from the intent (plans 21/22). A `SC_PROTECTED_TESTS` value above 64 KiB makes the extension return 422 `INTENT_TOO_LARGE`.
7. **Attest.**
   - For verification: `POST /v1/ci/intents/{id}/served` with the sha256 of the canonical (sorted-key YAML→JSON) served configuration and the config revision. If the attestation fails, return 503 (the pipeline errors and the Runner retries).
   - For all requests: emit a structured log line `{repo, event, branch, commit, trust, config_revision, config_sha256, workflows}` (no file contents).
8. **Respond.** Return 200 `{"configs":[…]}`. Never return 204: in exclusive mode the meaning of "keep existing" is not something to rely on.

Limits: request body ≤ 1 MiB; total served configuration ≤ 256 KiB; upstream calls time out after 5 s each; overall handler deadline 20 s (Woodpecker's extension timeout, verify).

### 8.3 Lanes and agents

| Lane | Agent | `WOODPECKER_AGENT_LABELS` | Pod namespace | Max workflows | Who can reach it |
|---|---|---|---|---|---|
| `ci` | `woodpecker-agent` | `sc-lane=ci` | `ci-jobs` | `${CI_MAX_CONCURRENT}` | every served workflow (forced label in untrusted mode) |
| `build` | `woodpecker-agent-build` | `sc-lane=build` | `ci-build` | 1 | protected-mode workflows that declare `sc-lane: build` |

`WOODPECKER_DEFAULT_WORKFLOW_LABELS=sc-lane=ci` is a second guard: a workflow with no labels never reaches the build agent.

### 8.4 Verification workflow (`verify`)

Required shape, validated by `sc-ci-config` (422 `VERIFY_SHAPE`) and by `make lint` (`woodpecker-cli lint` plus `sc-ci-config lint --verify`):

```yaml
# .woodpecker/verify.yaml (strange-company; illustrative)
when:
  - event: manual
labels: {sc-lane: ci}
steps:
  - name: sc-verify-install            # copies the pinned static verifier into the workspace root
    image: <registry>/strange-company/sc-verify@sha256:<digest from CIPolicy.spec.verify.installerImage>
    commands: ["sc-verify install --dest /woodpecker/.sc/bin"]
  - name: sc-verify                     # the only step whose log is read for evidence
    image: <toolchain image from .strange-company/validation.yaml spec.verification.image>   # strange-company: the `ci` image
    commands: ["/woodpecker/.sc/bin/sc-verify run --validation .strange-company/validation.yaml --emit log"]
```

Rules:
- There are exactly two steps, named `sc-verify-install` and `sc-verify`, in that order.
- The first step's image equals `verify.installerImage`.
- The second step's commands are exactly `["/woodpecker/.sc/bin/sc-verify run --validation .strange-company/validation.yaml --emit log"]`.
- There are no `services`, `depends_on` cycles or extra steps.
- The workspace root `/woodpecker` is the Woodpecker default (verify).

`sc-verify` reads `SC_*` from the environment, runs every `spec.verification.suites[]` entry whose `phases` contains `SC_PHASE` (and, for GREEN, every `spec.verification.checks[]` entry) as child processes (argv, no shell; plan 22 §14) with stdout and stderr captured to a file (not the step log), and prints only:
- a header;
- a tail of at most 64 KiB of scrubbed output;
- the framed evidence block (§8.5).

**Platform default.** If the base revision has no `.woodpecker/verify.yaml` (project repositories), `sc-ci-config` serves `CIPolicy.spec.verify.defaultWorkflow`, the same shape with the toolchain image from the base revision's `validation.yaml`. A repository without a valid `validation.yaml` gets 422, and the Runner records an infrastructure failure (plan 22 decides whether the task can be test-first at all).

### 8.5 Evidence framing and limits (ADR-0302)

```text
::sc-evidence-begin v1 intent=<intent_id> nonce=<32 lowercase hex> phase=<red|green> sha=<40 hex> bytes=<decoded length> sha256=<64 hex of decoded bytes>::
<base64 lines, 76 characters, standard alphabet, padded>
::sc-evidence-end v1 intent=<intent_id>::
```

- The decoded content is UTF-8 JSON, a `red-result` or `green-result` document (plan 22), of at most **262144 bytes**. The block is therefore at most about 360 KiB.
- Markers must be at the start of a log line. A line matching `^::sc-evidence-` that is not part of the well-formed block makes the log ambiguous.
- `sc-verify` keeps the `sc-verify` step log under 1 MiB in total. The Runner reads at most **8 MiB** of that step's log (`EVIDENCE_LOG_TOO_LARGE` above that).
- Parser outcomes (the Runner, F06 library):

  | Outcome | Meaning |
  |---|---|
  | `ok` | Exactly one begin/end pair with the intent's `intent` and `nonce`, and length and sha256 match |
  | `EVIDENCE_MISSING` | No block |
  | `EVIDENCE_AMBIGUOUS` | More than one begin marker, or a begin marker with a different nonce or intent |
  | `EVIDENCE_TRUNCATED` | No end marker |
  | `EVIDENCE_CORRUPT` | Bad base64, or length or sha256 mismatch |
  | `EVIDENCE_SCHEMA_INVALID` | The document does not validate |

  Every non-`ok` outcome is failure class `infrastructure`, except `EVIDENCE_AMBIGUOUS`, which is class `policy` (a possible forgery attempt; alert `SCTddProvenanceBypass` per plan 42 catalogue semantics).

### 8.6 Pipeline identity checks (Runner, before parsing; ADR-0302)

All must hold. Otherwise `EVIDENCE_IDENTITY_MISMATCH:<check>`, and the evidence is discarded.

| # | Check |
|---|---|
| I1 | `(repo_id, number)` equals the pair recorded on the intent from the create response |
| I2 | `pipeline.event == "manual"` and `pipeline.branch == "agent/<task-id>"` |
| I3 | `pipeline.commit == intent.expected_sha` (otherwise `EVIDENCE_COMMIT_MISMATCH`: cancel the pipeline and retry per plan 22) |
| I4 | `pipeline.sender == "sc-runner"` (verify the field for manual events; if Woodpecker does not set it, I1 alone suffices because only the Runner records pipeline numbers) |
| I5 | The intent has a `served` attestation, and its `config_sha256` equals the Runner's own computation over the expected verify workflow at `base_sha` (same canonicalization library as `sc-ci-config`) |
| I6 | The pipeline has a workflow `verify` with steps `sc-verify-install` and `sc-verify` in state `success` with `exit_code == 0`, and no other workflows ran |
| I7 | The pipeline is terminal (`success`) and finished before the intent's verification deadline (plan 22) |

The Runner records the transition with actor `ci:woodpecker/<number>` (contract `mcp-surface.schema.yaml`).

### 8.7 `sc-ci-maint` (hourly CronJob)

Steps, each idempotent. Exit 1 if any step fails (check-cronjob pattern, ADR-0553, check id `ci-maint`).

1. **Kubernetes GC** (RBAC only): in `ci-jobs` and `ci-build`, delete Pods, Services and PVCs carrying the Woodpecker pod labels whose age exceeds `maxPipelineTimeoutMinutes + 15m` (default 105 min). Count them in the job log and record them in `out` (stdout JSON).
2. **Log retention:** for every activated repository, delete logs of pipelines finished more than `retention.logDays` (default 30; plan 43 R-19) ago with `DELETE /api/repos/{id}/logs/{number}` (verify the endpoint), at most 500 per run. This uses `SC-WOODPECKER-MAINT`. If the selected Woodpecker version provides native log retention, use that instead and drop this step and the token (Q-20-04).
3. **Drift report:** compare every activated repository's settings (`GET /api/repos/{id}`) and crons with `CIPolicy.spec.repoSettings` and the declared crons. Mismatch → exit 1, with the differences in the log.
4. **Stale agent report:** agents with `last_contact` older than 24 h are listed (admin-only removal happens in the procedure).
5. **Build-lane attribution (per-agent tokens; SEC-8).** Runs **every 60 seconds inside `sc-ci-config`** (a background loop of the long-running Deployment, so the result is a scrapeable metric; also part of the hourly `sc-ci-maint` report). For every pipeline of the last 30 min with a workflow labeled `sc-lane=build`, the workflow's `agent_id` (verify the field at P08; 20-AC-4 records it) must equal the registered ID of `woodpecker-agent-build`, and no workflow labeled `sc-lane=ci` may have run on that agent. It also checks that `GET /api/agents` lists exactly the two registered agents. The loop exports the gauge **`sc_ci_build_lane_agent_mismatch`** (0 or 1; no labels). On a mismatch, an agent declared a lane it does not own, which is a sev1 compromise:
   - `sc-ci-config` **deletes the release secrets** of every activated repository (`DELETE /api/repos/{id}/secrets/{name}` for the plan 23 secret names, as `sc-woodpecker` with `SC-WOODPECKER-MAINT`, mounted read-only into `sc-ci-config` for this purpose; verify repository-admin rights suffice), so no further release workflow can receive them;
   - it sets the gauge to 1 and logs `BUILD_LANE_AGENT_MISMATCH`. `SCCIBuildLaneAgentMismatch` (`max(sc_ci_build_lane_agent_mismatch) == 1`, for 0s; category `security`, critical, page; plan 42 catalogue) fires within one scrape interval; runbook `woodpecker-failure.md` §J3. The gauge stays 1 until an operator clears it with `sc-ci-config agent-audit --ack <incident>` after the compromise procedure; plan 23 re-applies the secrets only then.
   The preventive control is the network: the server's gRPC port 9000 is reachable only from the two agent pods and is never routed by any ingress (20-AC-21).

## 9. Configuration

### 9.1 Version selection rule

- Latest stable Woodpecker **3.x** minor at implementation time, latest patch (3.19 was current on 2026-10-13). Official Helm chart at the version shipping that app version (`oci://ghcr.io/woodpecker-ci/helm/woodpecker`, verify the location). Image digests from `docker.io/woodpeckerci/*`.
- `plugin-git` latest 2.x that is compatible (verify).
- `woodpecker-cli` in the `ci` image (plan 02) at the same version as the server (`make locks-check` cross-file rule, plan 01).
- A major upgrade (v4) is human-initiated (§19).

### 9.2 `policy/ci.yaml` (kind `CIPolicy`; contract `contracts/ci-policy.schema.yaml`)

```yaml
apiVersion: strange.company/v1alpha1
kind: CIPolicy
metadata: {name: platform}
spec:
  managedOwners: [strange-company, projects, sc-e2e]
  protectedBranches: [main]                # plus releaseBranches; never globbed (SEC-1)
  releaseTagPattern: '^v[0-9]+\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$'   # anchored regex (plan 23; SEC-12)
  releaseBranches: []                      # exact names; empty in MVP (SEC-1)
  imageRegistries: ["git.${BASE_DOMAIN}/strange-company/", "ghcr.io/<owner>/", "docker.io/woodpeckerci/"]
  cloneImage: docker.io/woodpeckerci/plugin-git:<v>@sha256:<64hex>
  stepSecurityContext:
    runAsNonRoot: true
    runAsUser: 65532
    runAsGroup: 65532
    fsGroup: 65532
    allowPrivilegeEscalation: false
    privileged: false
    seccompProfile: {type: RuntimeDefault}
    capabilities: {drop: [ALL]}
  lanes:
    ci:
      defaults: {requests: {cpu: 500m, memory: 1Gi}, limits: {memory: 3Gi, ephemeral-storage: 4Gi}}
      max:      {requests: {cpu: "2", memory: 3Gi}, limits: {memory: 3Gi, ephemeral-storage: 6Gi}}
      constrained: {limits: {memory: 2Gi}}      # applied when the cluster runs the constrained profile set (ADR-0020)
      heavySlotUnits: 1                         # CD-05, plan 51 §8.4
      workspacePvcMax: 8Gi                      # CD-06; agent VOLUME_SIZE 6Gi
    build:
      fromPlan: "23"                            # defaults and max set by plan 23
      heavySlotUnits: 8
      workspacePvcMax: 10Gi                     # CD-06
      sequentialStepsOnly: true                 # CD-06: no depends_on, no services
  repoSettings: {timeoutMinutes: 45, maxTimeoutMinutes: 90, requireApproval: forks, allowPullRequests: true, cancelPreviousEvents: [pull_request]}
  crons:
    - {repo: strange-company/strange-company, name: nightly, branch: main, schedule: "0 0 2 * * *"}
  verify:
    installerImage: <registry>/strange-company/sc-verify@sha256:<64hex>
    defaultWorkflow: {stepImageFrom: validation.yaml}
  retention: {logDays: 30}
  offclusterLane: {provider: github-actions, workflow: e2e, maxWaitMinutes: 90}   # ADR-0303; read by plan 23's release gate
```

It is delivered to `sc-ci-config` and `sc-ci-maint` as ConfigMap `woodpecker/sc-ci-policy`, by the same Flux-generated mechanism as other `policy/**` files (ADR-0017). `make policy-check` validates it.

### 9.3 Server environment (chart values; changed only by PR)

| Variable | Value | Why |
|---|---|---|
| `WOODPECKER_HOST` | `https://ci.${BASE_DOMAIN}` | OAuth redirect and links |
| `WOODPECKER_FORGEJO` / `_URL` / `_CLIENT` / `_SECRET` | `true` / `https://git.${BASE_DOMAIN}` / from Secret | native Forgejo forge (verify names; fallback `WOODPECKER_GITEA_*`) |
| `WOODPECKER_EXPERT_WEBHOOK_HOST` | `http://woodpecker-server.woodpecker.svc.cluster.local:8000` | webhooks stay in-cluster (verify) |
| `WOODPECKER_OPEN` / `WOODPECKER_ORGS` / `WOODPECKER_REPO_OWNERS` | `true` / `strange-company,projects` / `strange-company,projects,sc-e2e` | registration and activation bounded to managed orgs |
| `WOODPECKER_ADMIN` | `sc-admin,${SC_OPERATOR_FORGEJO_USER}` | |
| `WOODPECKER_DISABLE_USER_AGENT_REGISTRATION` | `true` | no user-registered agents |
| `WOODPECKER_AGENT_SECRET_FILE` | `/var/run/secrets/woodpecker/agent-secret` (server only) | system-wide secret; with per-agent tokens the server accepts the registered agents' tokens. Leave it unset if the pinned version then refuses unregistered agents (verify, 20-AC-4); otherwise keep the pre-seed value, which is mounted by no agent |
| `WOODPECKER_DATABASE_DRIVER` / `_DATASOURCE_FILE` / `_MAX_CONNECTIONS` | `postgres` / `/var/run/secrets/woodpecker-pg/uri` (with `sslmode=require`) / `20` | CNPG |
| `WOODPECKER_CONFIG_EXTENSION_ENDPOINT` | `http://sc-ci-config.woodpecker.svc.cluster.local:8080/v1/config` | ADR-0301 |
| `WOODPECKER_CONFIG_EXTENSION_EXCLUSIVE` / `_NETRC` | `true` / `false` | ADR-0301 |
| `WOODPECKER_EXTENSIONS_ALLOWED_HOSTS` | `${SC_SERVICE_CIDR}` | the default `external` would block the in-cluster extension |
| `WOODPECKER_DEFAULT_PIPELINE_TIMEOUT` / `_MAX_PIPELINE_TIMEOUT` | `45` / `90` | §16 |
| `WOODPECKER_DEFAULT_APPROVAL_MODE` | `forks` | |
| `WOODPECKER_DEFAULT_CANCEL_PREVIOUS_PIPELINE_EVENTS` | `pull_request` | main pushes and manual verification runs are never auto-cancelled |
| `WOODPECKER_DEFAULT_WORKFLOW_LABELS` | `sc-lane=ci` | §8.3 |
| `WOODPECKER_DEFAULT_CLONE_PLUGIN` / `WOODPECKER_PLUGINS_TRUSTED_CLONE` | `CIPolicy.spec.cloneImage` (digest) | only the pinned clone image receives netrc |
| `WOODPECKER_PLUGINS_PRIVILEGED` | empty | no privileged plugins |
| `WOODPECKER_AUTHENTICATE_PUBLIC_REPOS` | `true` | |
| `WOODPECKER_LOG_STORE` | `database` | logs in `woodpecker-pg` |
| `WOODPECKER_METRICS_SERVER_ADDR` | `:9001` | §21 |
| `WOODPECKER_SERVER_ADDR` / `WOODPECKER_GRPC_ADDR` | `:8000` / `:9000` | |
| `WOODPECKER_LOG_LEVEL` | `info` | |

### 9.4 Agent environment (Kubernetes backend; names verified against the 3.19 docs on 2026-10-13)

| Variable | `woodpecker-agent` | `woodpecker-agent-build` |
|---|---|---|
| `WOODPECKER_SERVER` | `woodpecker-server.woodpecker.svc.cluster.local:9000` | same |
| `WOODPECKER_BACKEND` | `kubernetes` | `kubernetes` |
| `WOODPECKER_MAX_WORKFLOWS` | `${CI_MAX_CONCURRENT}` | `1` |
| `WOODPECKER_AGENT_LABELS` | `sc-lane=ci` | `sc-lane=build` |
| `WOODPECKER_AGENT_SECRET_FILE` | per-agent token `woodpecker/woodpecker-agent-token-ci` | per-agent token `woodpecker/woodpecker-agent-token-build` |
| `WOODPECKER_HEALTHCHECK_ADDR` | `:3000` | `:3000` |
| `WOODPECKER_BACKEND_K8S_NAMESPACE` | `ci-jobs` | `ci-build` |
| `…_VOLUME_SIZE` / `…_STORAGE_CLASS` / `…_STORAGE_RWX` | `6Gi` / `ci-workspace` / `false` | `10Gi` / `ci-workspace` / `false` |
| `…_POD_LABELS` | `{"strange.company/ci-lane":"ci","app.kubernetes.io/managed-by":"woodpecker"}` | lane `build` |
| `…_POD_LABELS_ALLOW_FROM_STEP`, `…_POD_ANNOTATIONS_ALLOW_FROM_STEP`, `…_POD_NODE_SELECTOR_ALLOW_FROM_STEP`, `…_POD_AFFINITY_ALLOW_FROM_STEP`, `…_SERVICE_ACCOUNT_NAME_ALLOW_FROM_STEP`, `…_RUNTIME_CLASS_ALLOW_FROM_STEP` | `false` | `false` |
| `…_POD_TOLERATIONS_ALLOW_FROM_STEP` | `false` (the default is `true`) | `false` |
| `…_SECCTX_NONROOT` | `true` | plan 23 |
| `…_PRIORITY_CLASS` | `ci` | `ci` |
| `…_PULL_SECRET_NAMES` | `sc-registry-pull` (`SC-REGISTRY-PULL`, plan 23) | same |
| `…_PERMISSION_INIT_IMAGE` | digest-pinned busybox (the default `busybox:stable-musl` is floating); 20-AC-8 verifies whether this init container is added at all under `SECCTX_NONROOT` | same |

## 10. Secrets

| Class | Object | Consumer | Notes |
|---|---|---|---|
| `SC-WOODPECKER-OAUTH` | `woodpecker/woodpecker-forgejo-oauth` | server | Created by forge-init S13 (plan 16). Plan 35's register names creator "P08" and path `secrets/woodpecker/…`, while plan 16 writes `secrets/forge/woodpecker-oauth.sops.yaml` (Q-20-05). |
| `SC-WOODPECKER-AGENT` (instances `ci`, `build`; plus the server-only system value) | `woodpecker/woodpecker-agent-token-ci`, `woodpecker/woodpecker-agent-token-build` (SOPS `secrets/woodpecker/agent-token-{ci,build}.sops.yaml`); `woodpecker/woodpecker-agent-secret` (server only) | one agent each; server | **per-agent tokens** (plans 23/34/35 request): each agent is registered by `ci-init` W07 and authenticates with its own token, so a leaked ci-lane token can be revoked alone (`DELETE /api/agents/{id}`) and every workflow is attributable to an agent ID (§8.7 step 5). Rotation: `woodpecker-failure.md` §J3 |
| `SC-WOODPECKER-SIGNING` (registered by plan 35) | ed25519 server signing key generated by Woodpecker on first start, stored in `woodpecker-pg` (verify storage at P08) | server signs extension requests; `sc-ci-config` verifies with the public key only | rotation on compromise only: `procedures/rotate-secrets.md#sc-woodpecker-signing`; recovery with the `woodpecker-pg` restore, or regenerated by a DB rebuild (§20.2), after which `sc-ci-config` refetches the public key |
| `SC-PG-APP` (`woodpecker-pg`) | `woodpecker/woodpecker-pg-app` | server | CNPG-generated |
| `SC-WOODPECKER-RUNNER` | `strange-company/agent-runner-woodpecker` (SOPS at `gitops/clusters/<c>/secrets/strange-company/woodpecker-runner-token.sops.yaml`) | Runner | minted by `ci-init` W05 at P08, before its consumer exists (C01) |
| `SC-WOODPECKER-ADMIN` | process memory only | `ci-init`, `sc-bootstrap ci verify|recover`, plan 23 `ci-secrets apply` | ephemeral, the token of Woodpecker admin `sc-admin` |
| **`SC-WOODPECKER-MAINT` (new)** | `woodpecker/sc-ci-maint-token` (SOPS at `gitops/clusters/<c>/secrets/woodpecker/sc-ci-maint-token.sops.yaml`) | `sc-ci-maint`; `sc-ci-config` (agent audit only, §8.7 step 5) | the Woodpecker personal token of `sc-woodpecker` (repository-admin rights through Forgejo permissions; **not** a Woodpecker instance admin) |
| **`SC-CI-INTENTS` (new)** | `woodpecker/sc-ci-config-intents` and `strange-company/agent-runner-ci-intents` (same value, two SOPS files) | `sc-ci-config` (client), Runner (server) | 32 random bytes, base64url |
| `SC-FORGEJO-BOT-TOKENS` (bot `sc-ci-config`, new instance) | `woodpecker/forgejo-sc-ci-config` | `sc-ci-config` | `read:repository` only |
| Woodpecker secrets for release (`SC-RELEASE-SIGNING`, `SC-REGISTRY-PUSH`, …) | `woodpecker-pg` | build lane | owned by plan 23; publishing secrets use events `[tag]` only, image filter `sc-release`; maintenance secrets (`sc_ci_ovh_readonly`) use `[cron]` |

New classes for plan 35 to register:
- **`SC-WOODPECKER-MAINT`**
  - creator: `ci-init` W06 (session as `sc-woodpecker`, `POST /api/user/token`);
  - consumer: `sc-ci-maint`;
  - storage: SOPS in Git (two recipients);
  - rotation: P90D or on suspicion, by `sc-bootstrap ci rotate maint` (session login, `DELETE /api/user/token`, which resets the user hash and invalidates the old token, then commit the new SOPS file by PR);
  - recovery: regenerate;
  - blast radius: delete logs, and change non-admin settings and crons of managed repositories (drift is detected by `sc-ci-maint` and by `sc-ci-config` policy checks).
- **`SC-CI-INTENTS`**
  - creator: `ci-init` W07b;
  - consumers: `sc-ci-config`, the Runner;
  - storage: SOPS in Git;
  - rotation: P1Y, with dual-accept for 1 h (the Runner accepts the old and new values listed in its Secret);
  - recovery: from Git;
  - blast radius: read intent metadata and post false "served" attestations. The latter are caught by I5 because the Runner recomputes the digest.

Forbidden in all CI pods: every class above, except the release classes in the build lane. `ci-jobs` pods carry no Secret volumes or `secretKeyRef` env from any of these. Admission rule A3 (§13.4) and 20-AC-20 enforce this.

## 11. Storage

- `woodpecker-pg` (plan 12): 4Gi PGDATA at self-hosting-minimum, `local-path`. Resolved at consolidation (Q-20-03): `retention.logDays` = 30. At about 30 pipelines/day × ≤ 3 MiB that is ≈ 2.7 GiB, so plan 12 keeps `woodpecker-pg` at 8Gi preferred / 4Gi constrained. Alert `SCCnpgDatabaseLarge` (plan 12) covers it.
- `ci-workspace` PVCs: 6Gi request per ci workflow and 10Gi per build workflow, on the bounded filesystem (ADR-0305). Deleted at workflow end; the sweeper catches leftovers.
- Container writable layers: bounded by `ephemeral-storage` limits (§9.2), enforced by kubelet eviction.
- Server and agents: no PVCs. The agent ID config file lives in an `emptyDir`, so an agent restart registers a new agent row (cosmetic; §8.7 step 4).

## 12. Resource requirements

| Component | self-hosting-minimum req (cpu / mem) / mem limit | small-ha |
|---|---|---|
| `woodpecker-server` | 50m / 160Mi / 512Mi | 100m / 192Mi / 768Mi |
| `woodpecker-agent` | 10m / 48Mi / 192Mi | same |
| `woodpecker-agent-build` | 10m / 48Mi / 192Mi | same |
| `sc-ci-config` | 10m / 32Mi / 128Mi | ×2 replicas |
| `sc-ci-maint` (hourly, < 1 min) | 10m / 32Mi / 128Mi | same |
| `woodpecker-pg` | plan 12 (proposed 128Mi / 384Mi) | 3 instances |
| CI step pod (ci lane, one at a time) | 500m / 1Gi / 3Gi (constrained: 2Gi); ephemeral-storage 4Gi | same; up to `CI_MAX_CONCURRENT` concurrently |

The steady-state footprint, excluding step pods, is about 300 MiB requested. The plan 50 memory measurement excludes `ci-jobs` from the platform working set and accounts for one CI job of 3 GiB (ADR-0020 budget).

## 13. Security boundary

### 13.1 Trust levels
- **Trusted:** the Woodpecker server (it holds the OAuth secret, users' forge tokens, the agent secret and its signing key), the agents (they hold the agent secret and create pods), `sc-ci-config`, `sc-ci-maint`.
- **Untrusted:** every step pod in `ci-jobs`. It runs agent- or human-authored code with no credentials beyond the clone step's netrc (clone image only).
- **Partially trusted:** step pods in `ci-build` run reviewed code from protected refs with release secrets (plan 23).

### 13.2 RBAC
- `woodpecker-server`, `sc-ci-config`: ServiceAccounts with `automountServiceAccountToken: false`, no RBAC.
- `woodpecker-agent`: Role `woodpecker-agent` in `ci-jobs`:
  - pods: create, delete, get, list, watch;
  - pods/log: get, list, watch;
  - services: create, delete, get, list;
  - persistentvolumeclaims: create, delete, get, list;
  - secrets: create, delete, get, **only if** the backend needs them for registry auth (verify at P08; omitted otherwise).
  `woodpecker-agent-build` has the same Role in `ci-build`.
- `sc-ci-maint`: Role in `ci-jobs` and `ci-build` with get, list and delete on pods, services and persistentvolumeclaims.
- The `default` ServiceAccount in `ci-jobs` and `ci-build` has `automountServiceAccountToken: false` (namespace manifests, plan 18 `namespaces` layer). Woodpecker cannot pick another ServiceAccount (`SERVICE_ACCOUNT_NAME_ALLOW_FROM_STEP=false`).

### 13.3 NetworkPolicy (plan 36 renders; assumed rules)

| Namespace | Direction | Peer | Port |
|---|---|---|---|
| `woodpecker` | ingress | `ingress` (Traefik) → server | 8000 |
| | ingress | `forgejo` pods → server (webhooks) | 8000 |
| | ingress | `strange-company` (Runner) → server | 8000 |
| | ingress | agents → server | 9000 |
| | ingress | server → `sc-ci-config` (no other source) | 8080 |
| | ingress | `monitoring` (Prometheus) → server 9001, `sc-ci-config` 9464 | |
| | egress | server → `woodpecker-pg` 5432, Forgejo HTTP 3000 (and Traefik for the public URL), `sc-ci-config` 8080, DNS | |
| | egress | agents → kube-apiserver 6443, server 9000 | |
| | egress | `sc-ci-config` → Forgejo 3000, Runner 8083, server 8000 | |
| | egress | `sc-ci-maint` → kube-apiserver, server 8000 | |
| `ci-jobs` | egress | DNS; Forgejo 3000 and 2222 (plan 36 FJ-10); Traefik 8443 (in-cluster `git.`, I-10); packages and registries through the egress proxy `egress-proxy.strange-company.svc:3129` (plan 36 CI-2, ADR-0150). Direct internet 443/80 (CI-3) exists only in `ci-egress-direct` mode, between P08 and C02, before the proxy is deployed | |
| | egress (denied) | kube-apiserver, all CNPG clusters, Runner, Gateway, Alertmanager, Longhorn, `flux-system` | |
| | ingress | only from `ci-jobs` pods (workflow services) | |

Workers (`agent-jobs`) and Hermes have no path to `woodpecker` or `ci-jobs` [SPEC §88].

### 13.4 Admission (plan 37 implements; assumed rules for `ci-jobs`)
- **A1** PSA `restricted` enforce, audit and warn.
- **A2** Pods are created only by `system:serviceaccount:woodpecker:woodpecker-agent` (VAP on `request.userInfo`).
- **A3** No `secretKeyRef` or `secret` volumes, except the image-pull Secret `sc-registry-pull` referenced through `imagePullSecrets`.
- **A4** No projected ServiceAccount token volume.
- **A5** Volumes limited to `emptyDir`, `configMap` (none expected) and PVCs of class `ci-workspace` (ADR-0305).
- **A6** Memory limit ≤ `lanes.ci.max`.
- **A7** Image digest-pinned.
- **A8** PriorityClass `ci`.

`ci-build` rules are plan 23's.

### 13.5 Residual risks
- **R-20-1** `sc-woodpecker`'s forge OAuth token (stored in `woodpecker-pg`, used for clone netrc and statuses) carries the user's full Forgejo rights. `sc-woodpecker` is in team `sc-automation` (repo admin, plan 16) because activation creates webhooks. A compromise of the clone image or of the server exposes it. Mitigations: only the digest-pinned clone image receives netrc; `SCForgeConfigDrift` (plan 16) catches protection changes. Recorded for plan 34.
- **R-20-2** Code under test can read `SC_EVIDENCE_NONCE` and attempt evidence forgery (ADR-0302 residual risk).
- **R-20-3** A repository admin in Woodpecker can create crons on unprotected branches or change approval settings. A cron on an unprotected branch is classified untrusted (§8.2), and drift is reported by `sc-ci-maint`.

## 14. Happy path

### 14.1 Deployment (Flux)
Layer `woodpecker-db` (plan 18 #16) brings `woodpecker-pg` Ready. Layer `woodpecker` (#17) then applies the Secrets, `sc-ci-policy`, the `sc-ci-config` Deployment, the server HelmRelease and the `sc-ci-maint` CronJob. The server migrates its schema on first start, generates its signing key (`SC-WOODPECKER-SIGNING`) and becomes Ready (`GET /healthz` 200). Layer `woodpecker-agents` (plan 18 #17a) holds both agent HelmReleases. It becomes Ready only after `ci-init` W07/W10 have committed the per-agent tokens; the agents then connect over gRPC.

### 14.2 `ci-init` (bootstrap step; plan 50 invokes after `sso-init`, before `verify`)

`./bootstrap ci-init` runs `sc-bootstrap ci init` in the toolbox. Each step is check-then-act and journaled.

| Step | Action | Check (idempotency) | Failure |
|---|---|---|---|
| W00 | Wait for the `woodpecker` HelmRelease Ready, `GET https://ci.<d>/healthz` 200, and `sc-ci-config` `/readyz` 200 (port-forward). Timeout 15 m | — | `CI_UNHEALTHY` (retryable) |
| W01 | Woodpecker sign-in as `sc-admin` (headless OAuth, §14.3) → ephemeral `SC-WOODPECKER-ADMIN` | `GET /api/user` returns `admin: true` | `CI_LOGIN_FAILED:sc-admin` |
| W02 | Only if W03 finds an inactive repository or W06 needs a new token: sign in as `sc-woodpecker` (set a fresh random Forgejo password through the admin API, then headless OAuth; the password is discarded afterwards). The first sign-in creates the Woodpecker user (registration is open to org members) | `sc-admin` sees user `sc-woodpecker` in `GET /api/users` | `CI_LOGIN_FAILED:sc-woodpecker` |
| W03 | As `sc-woodpecker`, activate every repository of `managedOwners` that is not yet active (`POST /api/repos?forge_remote_id=`) | repo `active: true` | `CI_ACTIVATION_FAILED:<repo>` |
| W04 | As `sc-admin`, `PATCH /api/repos/{id}` to `repoSettings`, with all `trusted.*` false | fields equal | |
| W05 | `SC-WOODPECKER-RUNNER`: if the local SOPS file exists and `GET /api/user` with it returns `sc-runner`, keep it. Otherwise set a fresh Forgejo password for `sc-runner`, sign in, and `POST /api/user/token` | token valid | |
| W06 | `SC-WOODPECKER-MAINT`: same as W05 for `sc-woodpecker` | token valid | |
| W07 | Per-agent tokens (`SC-WOODPECKER-AGENT` instances): as `sc-admin`, for each of `woodpecker-agent` and `woodpecker-agent-build`, if `GET /api/agents` lists an agent with that name and the local SOPS token file exists, keep it; otherwise `POST /api/agents {"name": <agent>}` (verify the endpoint and the returned `token` field) and delete any older agent with the same name | both agents registered, token files exist | `CI_AGENT_REGISTRATION_FAILED` |
| W07b | `SC-CI-INTENTS`: generate if no SOPS file exists | file exists | |
| W08 | Forgejo hooks created by activation point at `http://woodpecker-server.woodpecker.svc.cluster.local:8000/api/hook` (Forgejo `GET /repos/{o}/{r}/hooks` as `sc-admin`) | host equal | `CI_WEBHOOK_HOST` (fatal; fix `WOODPECKER_EXPERT_WEBHOOK_HOST`) |
| W09 | Crons per `CIPolicy.spec.crons` (`GET/POST/PATCH /api/repos/{id}/cron`) | equal | |
| W10 | Write the SOPS outputs (W05–W07b), regenerate `gitops/clusters/<c>/cluster-config/kustomization.yaml` (`sc-gitops gen cluster-config`, plan 18 §8.1, so layer `cluster-config` delivers the new Secrets), commit `bootstrap(<cluster>): ci-init outputs` (plan 01 §6.4 form with `SC-Install-Uid`/`SC-Run-Id` trailers; BOOT-20) only if changed, and fast-forward push to Forgejo `main` as `sc-admin` (protection stage `bootstrap`, plan 16). If protection is `steady`: open PR `bootstrap/ci-init-<run>` and stop with `CI_OUTPUTS_REQUIRE_PR` | remote `main` == local HEAD | |
| W11 | Cutover proof (V-21): wait for layer `woodpecker-agents` Ready at the W10 commit (both agents connected with their tokens, timeout 10 m), then wait for the `ci` pipeline triggered by W10's push on `main` to succeed (timeout 45 m). If W10 pushed nothing, trigger `POST /api/repos/{id}/pipelines {"branch":"main"}` as `sc-admin` (manual event, untrusted mode, default-branch config; `ci.yaml` includes `manual`) | the pipeline for the current `main` SHA is `success` and the Forgejo status `ci/woodpecker/push/ci` (or `manual/ci`) is `success` | `CI_CUTOVER_FAILED` |
| W12 | Journal record `ci-init`: `{result, steps[], woodpecker_version, repos_activated, tokens: [{class, user, outcome}], cutover: {sha, pipeline_number, duration_s}}` | — | — |

Re-running `ci-init` on a configured installation makes zero mutating API calls and no commit (20-AC-26).

### 14.3 Headless OAuth sign-in (`sc-bootstrap ci login <user>`)

This is a cookie-jar HTTP client in the toolbox, against the public URLs. Redirect URIs are validated by Forgejo, so the flow must use `https://ci.<d>/authorize`.
1. `GET https://ci.<d>/authorize`. Follow to `https://git.<d>/login/oauth/authorize?client_id=…&state=…`, which redirects to `/user/login`.
2. `GET /user/login`, extract `_csrf`, then `POST /user/login` with `user_name`, `password` and `_csrf`.
3. Follow back to `/login/oauth/authorize`. If a consent page is shown, `POST /login/oauth/grant` with `client_id`, `state`, `redirect_uri`, `granted=true` and `_csrf` (verify the form fields at the pinned Forgejo version).
4. Follow to `https://ci.<d>/authorize?code=…&state=…`. Woodpecker sets the `user_sess` cookie.
5. `POST https://ci.<d>/api/user/token` with the session returns the personal token.

The flow never prints credentials. Each request is logged with method, host, path and status only. Every step is re-verified by 20-AC-5 on each Forgejo or Woodpecker version bump.

### 14.4 Ordinary pipelines
- **PR opened** → Forgejo webhook → Woodpecker → `sc-ci-config` (untrusted; config from `main` head; `ci` workflow, forced `sc-lane: ci`) → agent → step pods in `ci-jobs` (clone, `deps`, `verify`, `tdd-discipline` per plan 52) → status `ci/woodpecker/pr/ci` on the PR head.
- **Merge to `main`** → push event (protected; config from the commit) → `ci` workflow → `ci/woodpecker/push/ci`.
- **Nightly cron** → `nightly` workflow.
- **Tag `v*`** → protected only if reachable from a protected branch → `release` on the build lane (plan 23).

### 14.5 Verification (RED/GREEN; ADR-0302)
1. The Runner (plan 26/22) has completed the protected-path diff check and finds `agent/<task>` head == `expected_sha` (Forgejo API).
2. It records the intent in `sc-pg`.
3. It calls `POST /api/repos/{id}/pipelines` with `branch=agent/<task>` and `variables {SC_TASK_ID, SC_PHASE, SC_EXPECTED_SHA, SC_INTENT_ID}`, then records the `number`.
4. Woodpecker resolves the branch head and calls `sc-ci-config`. The extension finds the intent, fetches `verify.yaml` at `base_sha`, validates its shape, normalizes it, injects the `SC_*` environment, posts `served`, and returns the configuration.
5. The ci agent runs the clone, `sc-verify-install` and `sc-verify` steps.
6. The Runner's evidence poller (every 10 s) waits for a terminal state, runs I1–I7, fetches the `sc-verify` step log, parses the block, validates it, stores it in S3 `evidence`, records the digest, and transitions the task with actor `ci:woodpecker/<number>`.

Target latency with an idle queue, from trigger to evidence recorded: ≤ 10 min for `strange-company` RED (`make test` scoped by plan 22), ≤ 20 min for GREEN (`make verify`).

## 15. Failure modes

| # | Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|---|
| F1 | Server down or crash-looping | `SCWoodpeckerDown` (blackbox, 5 m) | no pipelines, no UI; verification waits | kubelet restart; Runner retries; plan 22 timeout → infra failure | `woodpecker-failure.md` §A |
| F2 | No agent connected, or one lane's agent unavailable | `SCWoodpeckerNoAgents`; `SCWoodpeckerAgentUnavailable` (ci agent); `SCWoodpeckerBuildAgentUnavailable` (build agent) | pipelines pending | kubelet restart | §B |
| F3 | `woodpecker-pg` down | server healthz fails; `SCCnpgClusterDown{cluster="woodpecker-pg"}` | as F1 | CNPG failover (small-ha) | §D, `cnpg-primary-loss.md` |
| F4 | `sc-ci-config` down | `SCCIConfigExtensionDown`; pipelines error "config extension" | **no pipelines run** (fail closed) | Deployment restart | §C |
| F5 | Signature verification failing (key rotated by a DB rebuild, clock skew) | `SCCIConfigSignatureFailures` | as F4 | refetch the key once per failure burst | §C |
| F6 | Repository settings violate policy (someone enabled `trusted.*`) | `SCCIRepoPolicyViolation` (critical) | pipelines refused for that repository | none (fail closed) | §C step "repo policy" |
| F7 | Untrusted configuration contained forbidden constructs | `SCCIUntrustedConstructDropped` (warning) | workflow silently absent from the PR's pipeline | dropped | §C; review the PR |
| F8 | Forgejo webhooks not delivered | PR has no `ci/woodpecker/pr/ci` status after 10 m (humans); Forgejo hook delivery history | PR blocked; verification unaffected (API-triggered) | Forgejo retries | §H |
| F9 | `sc-woodpecker` forge token expired or revoked (refresh failure) | clone steps fail `authentication failed`; statuses missing; `sc-ci-maint` repo listing 401 | all pipelines fail at clone | none | §G (`sc-bootstrap ci login sc-woodpecker`) |
| F10 | Step pod rejected by admission | pipeline error with `admission webhook … denied` / `violates PodSecurity` | that pipeline fails | none | §C step "admission"; fix `CIPolicy` or plan 37 rule by PR |
| F11 | Workspace PVC Pending (StorageClass missing, filesystem full) | step stuck Pending; `SCCIWorkspaceFilesystemHigh` / `SCCIWorkspaceFilesystemCritical` | pipeline times out | sweeper removes orphans | §F |
| F12 | Backlog | `SCWoodpeckerBacklog` | slow CI and verification | none | §E |
| F13 | Pipeline hangs | repository timeout (45 m) or the Runner's verification deadline → cancel | — | Woodpecker kills; the Runner cancels | — |
| F14 | Orphan pods or PVCs after an agent crash | `sc-ci-maint` | disk and quota use | swept at the next hourly run | §F |
| F15 | Evidence missing, ambiguous, corrupt or identity mismatch | Runner outcomes (§8.5, §8.6); `SCTddPipelineFailure`; `SCTddProvenanceBypass` for ambiguous | task stays in `*_VERIFYING`, then retry or `NEEDS_HUMAN` per plan 22 | Runner retry per failure class | `tdd-task-recovery.md` (plan 21), §I |
| F16 | Pipeline commit ≠ expected (branch moved) | I3 | evidence discarded | cancel, then re-trigger once per plan 22 | — |
| F17 | `sc-runner` Woodpecker token invalid | Runner 401; `SCRunnerDependencyDown{dep="woodpecker"}` | no verification | none | §J |
| F18 | Agent secret leaked or rogue agent | unknown agent in `GET /api/agents`; `woodpecker_worker_count` above expected | rogue agent could receive pipelines | none | §J + `credential-compromise.md` |
| F19 | Upgrade migration fails | HelmRelease `UpgradeFailed` (`stateful-app` class, no rollback) | CI down | none | §K, plan 48 |
| F20 | `woodpecker-pg` lost beyond PITR | restore impossible | history lost; Runner evidence intact in S3 | none | §L |
| F21 | Log retention not running | `SCCIMaintFailed` (check-cronjob) | DB growth | — | §F |

## 16. Retry behavior

- Woodpecker retries nothing inside a pipeline. Plan 52 forbids step retries for gating work, and `sc-ci-config` rejects a `failure: ignore` or retry construct on the `verify` workflow.
- The Runner's trigger call retries 3 times (0.5, 1, 2 s) on 5xx or connection errors. Pipeline creation is not idempotent, so after an ambiguous failure the Runner first lists pipelines for the branch created in the last 2 minutes by `sc-runner` before creating another. Verification-level retries (budget, class) are plan 22's.
- `sc-ci-config` makes 2 attempts per upstream call (Forgejo, Runner) within the 20 s deadline. Woodpecker does not retry extension calls (verify); a failed call is an errored pipeline, and the user or Runner re-triggers.
- `ci-init` API calls: 5 attempts with exponential backoff (1–16 s) on 5xx and 429. The phase is resumable (plan 50).
- `sc-ci-maint` is not retried within a run. The next hourly run retries, and `failAfter` (2 h) gives the alert.

## 17. Idempotency behavior

- Flux applies are server-side apply no-ops when unchanged (plan 18).
- `ci-init` is check-then-act per step (§14.2). Tokens are re-minted only when invalid, and a second run makes zero mutations.
- The extension is a pure function of `(payload, Git at revision, CIPolicy, intent)`. The same input yields byte-identical configuration and the same `config_sha256` (unit-tested).
- `POST …/served` is idempotent for identical content.
- `sc-ci-maint` deletions are delete-if-exists. Log deletion of an already-deleted log returns 404 and is treated as success.

## 18. Bootstrap behavior

- Woodpecker is **not** in the seed set (ADR-0005). It needs forge-init credentials and is reconciled by Flux after control transfer (P06), together with `woodpecker-pg`.
- `ci-init` runs as part of `./bootstrap` after `sso-init` (plan 15) and before `verify` (plan 50). W11 makes Woodpecker the trusted CI (ADR-0018 cutover). From that commit on, GitHub Actions is advisory except for the off-cluster e2e lane (ADR-0303).
- Between P06 and P08 (minutes within one `./bootstrap` run), Forgejo has no CI. Only bootstrap commits may land on `main` in that window (forge-init and ci-init outputs, protection stage `bootstrap`).
- P12 (`sc-bootstrap forge finalize`, plan 16) adds `ci/woodpecker/pr/ci` to the required status contexts.
- **Black start:** `woodpecker-pg` is restored by plan 45's recovery overlay. Tokens in Git stay valid (the users' hashes are in the restored DB), the signing key is restored, and repositories remain activated. `ci-init --recover` verifies (expect `verified` outcomes), re-runs W08 and W11, and re-mints only invalid tokens. With the DB unrecoverable, see §20.2.

## 19. Upgrade behavior

- Renovate (plan 23) proposes patch and minor bumps: the chart version, all three image digests and `woodpecker-cli` in the `ci` image (one PR, `locks-check` cross-file rule).
- The PR passes tier V (`make lint` runs `woodpecker-cli lint` over `.woodpecker/*.yaml` with the new CLI). Merge requires a backup-first record for `woodpecker-pg` (plan 48 mechanism, same as Forgejo's).
- HelmRelease remediation class `stateful-app` (ADR-0304): no automatic rollback.
- After Flux applies the upgrade, the operator runs `./bootstrap exec -- sc-bootstrap ci verify --post-upgrade`. It re-runs 20-AC-1, 4, 5, 6, 8, 12, 17 and 26 against the live system, plus the off-cluster `make e2e-woodpecker` result for the merge commit (ADR-0303 lane).
- Version-sensitive behaviors re-verified on every bump:
  - extension request payload and response handling, and signature header format;
  - exclusive mode;
  - the agent environment names in §9.4;
  - the workspace PVC behavior;
  - the status-context format;
  - API paths in §7.2;
  - manual pipeline `sender`;
  - the log entry format;
  - the OAuth login flow.
- **Major version (v4):** human-initiated, with a dedicated plan addendum. Pipeline syntax changes go through `woodpecker-cli lint` with the new version on a branch before the server bump.

## 20. Recovery behavior

### 20.1 Point-in-time (RPO ≤ 15 min, RTO ≤ 4 h)
1. CNPG PITR of `woodpecker-pg` (plan 12 `restore-cnpg.md`).
2. Restart the server. Pipelines that were running are marked killed or errored by Woodpecker on start (verify).
3. The Runner re-polls the pipelines referenced by `*_VERIFYING` tasks. Missing or killed pipelines give `EVIDENCE_PIPELINE_LOST` (infrastructure), and plan 22 re-triggers.

### 20.2 Database unrecoverable
1. Delete the `woodpecker-pg` Cluster through break-glass (prune-protected, ADR-0304), and let Flux recreate it empty with a generation bump (plan 12).
2. The server initializes a new schema and a new signing key. `sc-ci-config` refetches the key automatically.
3. `./bootstrap ci-init --recover`:
   - W01–W03 recreate the users and activations;
   - W08 removes Forgejo hooks whose URL token no longer validates (list hooks with host `woodpecker-server…`; delete those not created by the current activation);
   - W05/W06 re-mint tokens. They are committed by **PR** (`CI_OUTPUTS_REQUIRE_PR`), because protection is `steady`. The Runner's token update lands when the PR merges.
4. Plan 23 re-applies the release secrets (`ci-secrets apply`).
5. Loss: pipeline history and logs only. Evidence and provenance live in `sc-pg` and S3.

### 20.3 Lost node (small-ha)
The server and agents reschedule. Running workflows on the lost node fail (their RWO workspace PV is gone), and the sweeper deletes the PVCs bound to the dead node after the timeout.

### 20.4 After an OAuth app secret rotation (`SC-WOODPECKER-OAUTH`)
Rotating the client secret (`procedures/rotate-secrets.md#sc-woodpecker-oauth`) can invalidate the refresh tokens Woodpecker holds for users. The rotation procedure therefore ends with:
1. `./bootstrap exec -- sc-bootstrap ci login sc-woodpecker`, which refreshes the activator's forge token;
2. `./bootstrap exec -- sc-bootstrap ci init --only W03,W08`, which re-authorizes every activated repository (repairs or recreates its Forgejo hook with the current token);
3. one `ci` pipeline per managed owner (the W11 check) to prove clone, configuration and status posting work.
Humans simply log in again.

## 21. Observability

**Metrics** (scraped by plan 38):
- Server `:9001` (`job="woodpecker"`): `woodpecker_worker_count`, `woodpecker_pending_steps`, `woodpecker_running_steps`, `woodpecker_waiting_steps`, `woodpecker_pipeline_count`, `woodpecker_pipeline_time`, `woodpecker_repo_count`, `woodpecker_user_count` (verify the names and labels at P08; 20-AC-25).
- `sc-ci-config` `:9464` (`job="sc-ci-config"`):
  - `sc_ci_config_requests_total{event, trust, outcome}`;
  - `sc_ci_config_request_duration_seconds` (histogram);
  - `sc_ci_config_dropped_workflows_total{reason}`;
  - `sc_ci_config_signature_failures_total`;
  - `sc_ci_config_repo_policy_violations_total{check}`;
  - `sc_ci_config_intent_lookups_total{outcome}` (`found`, `not_found`, `error`, `base_invalid`);
  - `sc_ci_config_signing_key_info{fingerprint}`;
  - `sc_ci_build_lane_agent_mismatch` (gauge 0/1, §8.7 step 5).

  All label values are bounded enums. There are no repository or task labels (SPEC §91). Plan 38 catalogues them.
- Node: `node_filesystem_avail_bytes{mountpoint="/var/lib/sc/ci-workspace"}` (node-exporter).
- KSM: Deployments in `woodpecker`, and the `sc-ci-maint` CronJob.

**Logs:** server and agent JSON to stdout (collected by plan 41 at H03). `ci-jobs` pod logs are excluded from Loki (ADR-0552) and live only in Woodpecker. `sc-ci-config` logs one line per request (§8.2 step 7) and never logs file contents or tokens.

**Dashboard (plan 39, "Woodpecker"):**
- agents connected;
- pending, running and waiting steps;
- pipeline duration by workflow (`ci`, `verify`, `nightly`);
- extension requests by trust and outcome;
- dropped workflows;
- workspace filesystem usage;
- queue wait p95.

**Alerts:** existing catalogue entries `SCWoodpeckerDown`, `SCWoodpeckerNoAgents`, `SCWoodpeckerBacklog` (plan 42, signal owner this plan). New IDs for plan 42 to register:

| alertname | Expression (sketch) | for | severity |
|---|---|---|---|
| `SCWoodpeckerAgentUnavailable` | `kube_deployment_status_replicas_available{namespace="woodpecker",deployment="woodpecker-agent"} < 1` | 10m | critical |
| `SCWoodpeckerBuildAgentUnavailable` | same for `deployment="woodpecker-agent-build"` | 10m | warning |
| `SCCIConfigExtensionDown` | `up{job="sc-ci-config"} == 0` or `kube_deployment_status_replicas_available{deployment="sc-ci-config"} < 1` | 5m | critical |
| `SCCIConfigSignatureFailures` | `increase(sc_ci_config_signature_failures_total[15m]) > 3` | 0m | warning |
| `SCCIRepoPolicyViolation` | `increase(sc_ci_config_repo_policy_violations_total[15m]) > 0` | 0m | critical |
| `SCCIUntrustedConstructDropped` | `increase(sc_ci_config_dropped_workflows_total[1h]) > 0` | 0m | warning |
| `SCCIWorkspaceFilesystemHigh` | `1 - node_filesystem_avail_bytes{mountpoint="/var/lib/sc/ci-workspace"} / node_filesystem_size_bytes{…} > 0.8` | 10m | warning |
| `SCCIWorkspaceFilesystemCritical` | same `> 0.95` | 10m | critical |
| `SCCIBuildLaneAgentMismatch` | `max(sc_ci_build_lane_agent_mismatch) == 1` | 0s | critical, category `security`, page |
| `SCCIMaintFailed` | check-cronjob `ci-maint`, `failAfter: 2h`, `maxAge: 3h` (ADR-0553); also fires on `BUILD_LANE_AGENT_MISMATCH` (§8.7 step 5) | — | warning |

Runbook for all of them: `procedures/woodpecker-failure.md`.

## 22. Testing strategy

All tests are deterministic and make no model calls.

- **Unit (`make test`):**
  - `sc-ci-config`: the trust table of §8.2 (one table-driven case per row plus negative cases: a tag on an unreachable commit, a cron on an agent branch, a manual pipeline without an intent, an intent with an invalid base); the filter (one fixture per drop reason); normalization golden files; canonicalization stability; signature verification against fixtures captured from a real Woodpecker 3.x server (recorded headers, body and public key), plus tampered-body and expired-`created` fixtures; 204 never returned.
  - Evidence parser: golden blocks; fuzzing (`go test -fuzz` in nightly, corpus committed) over arbitrary logs; each outcome of §8.5.
  - Identity checks I1–I7 against a fake Woodpecker API (recorded JSON).
  - `ci-init` planner against fake Woodpecker and Forgejo APIs: fresh, configured (zero mutations) and token-invalid states.
- **Contract:** `policy/ci.yaml` against `ci-policy.schema.yaml` once added (F02); intent endpoint payloads against `runner-api.openapi.yaml`; evidence documents against plan 22 schemas.
- **Integration (tier E, off-cluster lane, `make e2e-woodpecker`; target to be added to plan 01's catalogue):** k3d with Forgejo (pinned), Woodpecker (pinned chart), `sc-ci-config`, the `ci-workspace` StorageClass (local-path), and a fake Runner intents server. It runs:
  - the headless OAuth login;
  - activation;
  - a PR with each forbidden construct;
  - a tag on an unreachable commit;
  - manual runs with and without an intent;
  - a verify run producing a block that the F06 parser accepts;
  - an extension outage (no pipeline runs);
  - the API path and field checks of §7.2;
  - the status-context format.
- **Live:** P08, C04 and P12 gates below.

## 23. Acceptance criteria

`$WP` is `./bootstrap exec -- sc-bootstrap ci api` (authenticated Woodpecker API client; uses an ephemeral `sc-admin` token unless `--as` is given). `$FJ` is plan 16's Forgejo client.

### GATE-P08

- [ ] **20-AC-1** Server healthy and pinned: `kubectl -n woodpecker get hr woodpecker -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'` = `True`; `curl -fsS https://ci.<d>/healthz` returns 200; every container image in namespace `woodpecker` matches `@sha256:[0-9a-f]{64}$`.
- [ ] **20-AC-2** Database on CNPG: the server pod's env has `WOODPECKER_DATABASE_DRIVER=postgres` and the datasource file's host is `woodpecker-pg-rw.woodpecker.svc`; `kubectl -n woodpecker get cluster woodpecker-pg -o jsonpath='{.status.phase}'` = `Cluster in healthy state`; `kubectl -n woodpecker get pvc -l app.kubernetes.io/name=woodpecker` returns no server PVC.
- [ ] **20-AC-4** Agents: `$WP GET /api/agents` lists exactly two non-stale agents (`last_contact` < 60 s), named `woodpecker-agent` and `woodpecker-agent-build`, with labels `sc-lane=ci` and `sc-lane=build`, each authenticated by its own registered token (the two decrypted token files differ, and neither equals the server's system secret). Rogue-agent test (SEC-8): a third agent pod started with the ci token, name `rogue` and label `sc-lane=build` either fails to connect (recorded), or, if it connects, `sc_ci_build_lane_agent_mismatch` on `sc-ci-config:9464` equals 1 within 2 minutes (the audit loop sees a third agent in `GET /api/agents`), and the fixture repository's release-named test secrets are gone. The test then deletes the pod and acknowledges the gauge. After `DELETE /api/agents/{id}` of a test agent, its token is rejected within 60 s. The ci agent's env shows `WOODPECKER_MAX_WORKFLOWS` = `CI_MAX_CONCURRENT` from `cluster-vars` (1 at self-hosting-minimum). Agent audit (SEC-8): `curl -s http://sc-ci-config.woodpecker.svc:9464/metrics | grep -c '^sc_ci_build_lane_agent_mismatch 0'` = 1; on the `sc-e2e/ci-probe` fixture, a forced attribution mismatch (test flag `SC_TEST_FAKE_AGENT_ID`) sets the gauge to 1 within 2 minutes, deletes the fixture repository's release-named test secrets, and fires `SCCIBuildLaneAgentMismatch` within 10 minutes (checked at P12 if P09 is not yet present). The `agent_id` field name found in `GET /api/repos/{id}/pipelines/{n}` is recorded in the evidence.
- [ ] **20-AC-5** Extension wiring: the server env contains `WOODPECKER_CONFIG_EXTENSION_EXCLUSIVE=true`, `WOODPECKER_CONFIG_EXTENSION_NETRC=false` and the endpoint `http://sc-ci-config.woodpecker.svc.cluster.local:8080/v1/config`. `curl http://woodpecker-server.woodpecker.svc:8000/api/signature/public-key` (port-forward) returns an ed25519 PEM. `sc_ci_config_signing_key_info` has exactly one series whose fingerprint equals the PEM's SHA-256. The headless OAuth login (§14.3) succeeded for `sc-admin`, `sc-woodpecker` and `sc-runner` (journal W01, W02, W05 outcomes).
- [ ] **20-AC-6** Repository settings: for `strange-company/strange-company`, `$WP GET /api/repos/lookup/strange-company/strange-company` shows `active: true`, `timeout: 45`, all `trusted.*` false and `require_approval: forks` (verify field names). The Forgejo hook list for the repository contains exactly one Woodpecker hook, with host `woodpecker-server.woodpecker.svc.cluster.local:8000`.
- [ ] **20-AC-7** Trusted CI cutover (V-21): for the SHA of Forgejo `main` after `ci-init`, `$FJ GET /repos/strange-company/strange-company/commits/<sha>/statuses` contains `ci/woodpecker/push/ci` (or `ci/woodpecker/manual/ci`) with state `success`, and the corresponding Woodpecker pipeline ran step `verify` (`make verify`) with exit code 0.
- [ ] **20-AC-8** Step pods are restricted. During the 20-AC-7 pipeline, a 1-s poll of `kubectl -n ci-jobs get pods -o json` shows for every pod and container:
  - `runAsNonRoot: true`, `allowPrivilegeEscalation: false`, `capabilities.drop` ⊇ `[ALL]`, `seccompProfile.type: RuntimeDefault`;
  - no `privileged: true`;
  - no volume with a `serviceAccountToken` projection;
  - no Secret volume or `secretKeyRef`;
  - `priorityClassName: ci`.
  No init container runs as uid 0. `kubectl get ns ci-jobs -o jsonpath='{.metadata.labels.pod-security\.kubernetes\.io/enforce}'` = `restricted`.
- [ ] **20-AC-9** Workspace storage (ADR-0305): the 20-AC-7 workflow's PVC has `storageClassName: ci-workspace` and its PV's `spec.local.path` or `hostPath.path` begins with `/var/lib/sc/ci-workspace/`. After the pipeline, `kubectl -n ci-jobs get pvc` returns none within 2 minutes.
- [ ] **20-AC-10** Disk isolation: a test pipeline (manual, `ci-disk-probe` workflow present on `main` only in the test fixture repository `sc-e2e/ci-probe`) writes until it fails. Its step fails with `No space left on device`. `df --output=used / ` on the node increases by < 1 GiB during the test. `SCCIWorkspaceFilesystemHigh` is evaluated at P12 (20-AC-35).
- [ ] **20-AC-11** Sweeper: a PVC with the Woodpecker pod labels, created in `ci-jobs` by impersonating the agent ServiceAccount, is deleted by `kubectl -n woodpecker create job --from=cronjob/sc-ci-maint sweep-test -- sc-ci-config maint --max-age 2m` run 3 minutes after the PVC's creation. The Job exits 0 and its log lists the PVC. A PVC younger than `--max-age` in the same run is left in place. (The production threshold of 105 min is asserted by the CronJob spec's args.)
- [ ] **20-AC-12** No secrets for untrusted refs: a PR from branch `test/secret-probe` in `sc-e2e/ci-probe` adds a workflow using `from_secret: sc_registry_push_token`. The pipeline's workflow list (`$WP GET /api/repos/{id}/pipelines/{n}`) does not contain it, and `sc_ci_config_dropped_workflows_total{reason="secret_reference"}` increased by 1.
- [ ] **20-AC-13** No build lane for untrusted refs: a PR adding `labels: {sc-lane: build}` to a workflow results in no pod created in `ci-build` during the pipeline (`kubectl get events -n ci-build` has no `Scheduled` event in the window), and either the workflow is dropped (`reason="lane"`) or it ran in `ci-jobs`.
- [ ] **20-AC-14** No privileged constructs: PR fixtures with `privileged: true`, `backend_options.kubernetes.serviceAccountName: default`, a step `volumes:` entry and a tag-only image each produce a dropped workflow with the matching `reason`. No pod in `ci-jobs` ever has `privileged: true` (20-AC-8 poll applied).
- [ ] **20-AC-15** PRs cannot change their own CI: a PR that replaces `.woodpecker/ci.yaml` with a single `exit 0` step yields a pipeline whose served `config_sha256` (from the `sc-ci-config` log line for that pipeline) equals the sha256 of the canonical configuration at `main` head, and the step `verify` appears in the pipeline.
- [ ] **20-AC-16** Tags are trusted only when reachable: a tag `v0.0.0-probe` pushed to a commit that exists only on `agent/probe` (using a test write deploy key) starts no `release` workflow and creates no pod in `ci-build`. The same tag name on a `main` commit is classified `protected` (`sc_ci_config_requests_total{event="tag",trust="protected"}` +1; the release workflow itself is plan 23's test and runs in its dry-run fixture). **Worker escape (SEC-1):** with a test write deploy key, push branch `release/x` carrying its own `.woodpecker/release.yaml` and then tag `v9.9.9` on it. Both pipelines are classified `untrusted` (the branch is not in `releaseBranches` and the tag is not reachable from `main`), no pod is created in `ci-build`, and no Woodpecker secret is injected (`sc_ci_config_requests_total{event=~"push|tag",trust="protected"}` unchanged). The tag pushed with the deploy key appears in `GET /v1/ci/push-records` with `pusher_kind: deploy_key` and is refused by both the classification and the release workflow's first step, even when it points at a `main` commit.
- [ ] **20-AC-17** Signed requests only: `curl -X POST http://sc-ci-config.woodpecker.svc:8080/v1/config -d @fixture.json` from a debug pod in `woodpecker` returns 401, and `sc_ci_config_signature_failures_total` increments. Replaying a captured valid request after 301 s returns 401.
- [ ] **20-AC-18** Fail closed: with `sc-ci-config` scaled to 0 (`flux suspend kustomization woodpecker` and then `kubectl scale --replicas=0`), a PR push results in no pod in `ci-jobs` within 5 minutes, and the pipeline state is `error` or `failure`. After `flux resume`, the next push runs normally.
- [ ] **20-AC-19** Repository policy enforced: after `$WP PATCH /api/repos/{id}` sets `trusted.security=true` on `sc-e2e/ci-probe`, the next pipeline errors and `sc_ci_config_repo_policy_violations_total{check="trusted"}` increments. `sc-ci-maint` (next run) exits 1 naming the drift. The test reverts the setting.
- [ ] **20-AC-20** Secret scoping: every repository and org secret listed by `$WP GET /api/repos/{id}/secrets` and `/api/orgs/{id}/secrets` has `events` ⊆ `[cron, tag]`, every publishing secret (`sc_cosign_*`, `sc_registry_push_token`, `sc_ghcr_*`, plan 23) has `events == [tag]`, and each has a non-empty `images` list (an empty list passes at P08, before plan 23 adds secrets). No Pod spec in `ci-jobs` observed in 20-AC-8 references a Secret.
- [ ] **20-AC-21** Network isolation: a pod created in `ci-jobs` by impersonating the agent ServiceAccount (`kubectl --as=system:serviceaccount:woodpecker:woodpecker-agent run netprobe …` with the step security context and the Woodpecker pod labels) cannot connect to `kubernetes.default.svc:443`, `woodpecker-pg-rw.woodpecker:5432`, `sc-pg-rw.strange-company:5432`, `agent-runner.strange-company:8083` or `model-gateway.strange-company:8080` (each `nc -z -w 5` fails), and can connect to `forgejo-http.forgejo:3000`. From the same pod, every node IP (`SC_NODE_IP_*`) on ports 22, 2379, 2380, 2381, 6443, 9100 and 10250 times out, and `woodpecker-server.woodpecker:9000` (gRPC) times out (SEC-5, SEC-8). `kubectl get ingressroutes,ingresses -A -o json` contains no route to port 9000.
- [ ] **20-AC-22** Concurrency bound: with `CI_MAX_CONCURRENT=1`, triggering two PR pipelines and a manual nightly at once leads to a 1-s poll of running step pods (`status.phase=Running` in `ci-jobs` ∪ `ci-build` with a `strange.company/heavy-slot` request) never exceeding 1 over the whole run, and all three pipelines finish `success`.
- [ ] **20-AC-23** Timeouts: `$WP GET /api/repos/lookup/sc-e2e/ci-probe` shows `timeout: 45`. A fixture workflow `sleep 3600` on `sc-e2e/ci-probe` (repository timeout patched to 5 for the test) ends in state `killed` or `failure` within 7 minutes, and its pods and PVC are gone within 2 more minutes.
- [ ] **20-AC-24** Version re-verification: the off-cluster lane's `make e2e-woodpecker` result for the commit pinning the deployed version is `success` (GitHub check run, ADR-0303). Its JUnit is attached to the P08 evidence.
- [ ] **20-AC-25** Metrics: `curl -s http://woodpecker-server.woodpecker.svc:9001/metrics | grep -c '^woodpecker_worker_count'` ≥ 1 and the same for `woodpecker_pending_steps`; `curl -s http://sc-ci-config.woodpecker.svc:9464/metrics | grep -c '^sc_ci_config_requests_total'` ≥ 1. The metric names and label sets found are recorded in the P08 evidence and diffed against §21 (any rename fails this AC until §21 and plan 42's catalogue are updated).
- [ ] **20-AC-26** `ci-init` idempotent: a second `./bootstrap ci-init` exits 0 with every step `verified` or `skipped`. The toolbox `--audit-requests` log contains no POST, PATCH, PUT or DELETE against Woodpecker or Forgejo other than W01's sign-in requests for the ephemeral admin token (`POST /user/login`, `POST /login/oauth/grant` when consent is shown, `POST /api/user/token` as `sc-admin`, which signs with the existing hash and does not rotate). No password reset or `POST /api/user/token` happens for `sc-runner` or `sc-woodpecker`, and no new commit exists.
- [ ] **20-AC-27** Token hygiene: the decrypted `SC-WOODPECKER-RUNNER` returns login `sc-runner` from `GET /api/user`, and `SC-WOODPECKER-MAINT` returns `sc-woodpecker` with `admin: false`. `git grep -l` over the repository finds no plaintext token prefix (the first 12 characters of each, matched by sha256 in the test harness). No Kubernetes Secret holds an `sc-admin` Woodpecker token (`SC-WOODPECKER-ADMIN` is process-only).
- [ ] **20-AC-38** Secret injection mechanism (SEC-3; re-verified at every Woodpecker bump): on `sc-e2e/ci-probe`, a repository secret `sc_probe_secret` (events `[tag]`, image filter = the probe image) is used by a protected tag pipeline running a step that sleeps 60 s. During the step, `kubectl -n ci-build get pod <step> -o json` shows whether the value appears inline (`env[].value`) or by reference (`valueFrom.secretKeyRef`). The result (`inline` or `reference`) is recorded in `versions/compat.yaml` key `woodpecker.secretInjection`; plan 23 consumes it (if `inline`, release credentials must be mounted from a Kubernetes Secret only into the `sc-release` pod). The secret is deleted after the test.
- [ ] **20-AC-39** Clone credentials stay in the clone step (SEC-20): a fixture workflow step that runs after `clone` executes `grep -r -c <first 12 chars of sc-woodpecker's forge token> /woodpecker $HOME; env | grep -c -i netrc` (the token prefix is supplied by the harness as a sha256, compared in-step) and reports 0 matches; the step's pod spec has no `CI_NETRC_*` variables.

### GATE-C04 (co-owned with plans 21 and 22; these items cover the Woodpecker trigger and evidence path)

C04 depends on C01, P08 and F06 only, so these ACs do not dispatch worker Jobs (C02). The harness `sc-e2e c04` (plan 21) creates the task through the Runner test API, **pushes the fixture commits itself** to `agent/<task>` on `sc-e2e/fake-target` (the same commits the `fake-red-valid` / `fake-green-after-retry` scenarios produce; plan 28 fixtures), and moves the task to `RED_VERIFYING`/`GREEN_VERIFYING` through the Runner's test hook. The full worker-driven flows are C10 (GC-01).

- [ ] **20-AC-28** RED end to end with fakes: a task on `sc-e2e/fake-target` driven by `sc-e2e c04` with the `fake-red-valid` fixture commit reaches `RED_VERIFIED`. `GET /v1/tasks/{id}/transitions` shows `RED_VERIFYING→RED_VERIFIED` with actor `ci:woodpecker/<n>`. The intent row has `served.config_sha256` equal to the Runner's recomputation. The evidence object exists in S3 `evidence` with a sha256 equal to the digest in `sc-pg`, and validates against `red-result.schema.yaml`.
- [ ] **20-AC-29** GREEN end to end: the `fake-green-after-retry` fixture commits, then `GREEN_VERIFIED` with evidence validating against `green-result.schema.yaml`. Pipeline `n` for GREEN has `commit` = the implementation commit, and `verify` ran from the task's `base_sha` (`served.config_revision == base_sha`).
- [ ] **20-AC-30** Branch race: a test hook pushes a new commit to `agent/<task>` after the Runner's head check and before pipeline creation (fault hook `SC_TEST_FAULT=after:intent-recorded`). The Runner records `EVIDENCE_COMMIT_MISMATCH`, cancels the pipeline (`$WP GET …/pipelines/{n}` state `killed`), and the task does not transition on that pipeline.
- [ ] **20-AC-31** Foreign pipelines ignored: a manual pipeline created by a human admin on `agent/<task>` (with the same variables) runs no `verify` workflow (the served configuration's workflow names exclude `verify`; `sc_ci_config_intent_lookups_total{outcome="not_found"}` +1), and the task's transition history is unchanged.
- [ ] **20-AC-32** Forgery attempt detected: a fixture test on `sc-e2e/fake-target` that writes a second `::sc-evidence-begin …` block with the injected nonce to `/proc/<sc-verify pid>/fd/1` yields `EVIDENCE_AMBIGUOUS` (failure class `policy`). The task does not reach `RED_VERIFIED` from that pipeline, and `SCTddProvenanceBypass` is posted (or the Runner metric that feeds it increments, if P09 rules are not loaded yet).
- [ ] **20-AC-33** Unit-level evidence outcomes: `go test ./src/internal/verify/evidence/... -run TestOutcomes` passes, with one fixture per outcome in §8.5 (`ok`, `MISSING`, `AMBIGUOUS`, `TRUNCATED`, `CORRUPT`, `SCHEMA_INVALID`, `LOG_TOO_LARGE`).
- [ ] **20-AC-34** Latency: over 10 consecutive `sc-e2e c04` RED tasks (fixture `fake-red-valid`) with an idle queue, trigger-to-evidence-recorded p95 ≤ 10 min (from Runner metrics `sc_verification_duration_seconds` or transition timestamps).

### GATE-P12 (Woodpecker contributions; owners 50 and 19)

- [ ] **20-AC-3** Human login (moved from GATE-P08 to GATE-P12 because it needs plan 15's synthetic OIDC user, delivered at P07; GC-01): `curl -s -o /dev/null -w '%{http_code} %{redirect_url}' https://ci.<d>/authorize` returns 30x to `https://git.<d>/login/oauth/authorize?client_id=<WOODPECKER_FORGEJO_CLIENT>`. The plan 15 synthetic OIDC user in team `engineers` completes the flow and `GET /api/user` returns its login. A Forgejo user outside both orgs is redirected with `error=org_access_denied` or `registration_closed`.
- [ ] **20-AC-35** Alerts present: Prometheus `GET /api/v1/rules` contains `SCWoodpeckerDown`, `SCWoodpeckerNoAgents`, `SCWoodpeckerBacklog`, `SCWoodpeckerAgentUnavailable`, `SCWoodpeckerBuildAgentUnavailable`, `SCCIConfigExtensionDown`, `SCCIConfigSignatureFailures`, `SCCIRepoPolicyViolation`, `SCCIUntrustedConstructDropped`, `SCCIWorkspaceFilesystemHigh`, `SCCIWorkspaceFilesystemCritical`, `SCCIMaintFailed`, `SCCIBuildLaneAgentMismatch`. None is firing (`ALERTS{alertname=~"SCWoodpecker.*|SCCI.*",alertstate="firing"}` empty). Scaling `sc-ci-config` to 0 for 6 minutes fires `SCCIConfigExtensionDown` (Alertmanager API), and it resolves after restore.
- [ ] **20-AC-36** Required context: `$FJ GET /repos/strange-company/strange-company/branch_protections/main` has `enable_status_check: true` and `status_check_contexts` containing `ci/woodpecker/pr/ci` (16-AC-32). A test PR whose `make verify` fails cannot be merged by a non-admin (`POST /repos/…/pulls/{n}/merge` returns 405 or 409).
- [ ] **20-AC-37** Prometheus `up{job=~"woodpecker|sc-ci-config"} == 1`, and the Grafana "Woodpecker" dashboard exists (plan 39 API check).

## 24. Explicit non-goals

- Forgejo Actions (plan 16), or any second CI engine [SPEC §127].
- Running tier-E (k3d, QEMU) tests inside the production cluster (ADR-0303).
- Privileged plugins, Docker-in-Docker, or socket mounts in any lane.
- Woodpecker server high availability (single replica at both profiles).
- Per-pipeline approval gates (`require_approval` beyond `forks`).
- Woodpecker as a deployment tool. CI never applies manifests to the cluster [SPEC §33].
- User-registered agents.

## 25. Open questions

| # | Question | Blocking? | Proposed resolution | Owner |
|---|---|---|---|---|
| Q-20-01 | `policy/ci.yaml` needed a contract. | no | **Resolved at consolidation:** `contracts/ci-policy.schema.yaml` written. | — |
| Q-20-02 | Woodpecker's manual-pipeline API cannot target a commit (verified in `server/model/pipeline.go`, 2026-10-13). | no | Resolved by ADR-0302 (intent plus commit check). If a later Woodpecker adds `commit` to `PipelineOptions`, the Runner may also pass it; the checks stay. | 20 |
| Q-20-03 | `woodpecker-pg` size versus log retention. | no | **Resolved:** retention is 30 days and `woodpecker-pg` stays 8Gi/4Gi (plans 12, 43). Measure at P12 with `pg_database_size`. | 12 / 43 |
| Q-20-04 | Native log retention in the selected Woodpecker version. | no | If present, use it and drop `SC-WOODPECKER-MAINT`'s log-deletion use. The token stays for drift reads unless those move to `SC-WOODPECKER-ADMIN` operator runs. | 20 |
| Q-20-05 | `SC-WOODPECKER-OAUTH` creator and path differ between plan 16 (forge-init S13, `secrets/forge/woodpecker-oauth.sops.yaml`) and `secret-classes.yaml` (P08, `secrets/woodpecker/…`). | no | Keep plan 16's creator (P05) and object name `woodpecker/woodpecker-forgejo-oauth`. Plan 35 aligns the register path. | 35 / 16 |
| Q-20-06 | ADR-0305 contradicted plan 11 SA-4 ("no PVC in `ci-jobs`"). | no | **Resolved by CD-06:** plans 11/13/43 adopted `ci-workspace` (ADR-0203); filesystem 12 GiB / 24 GiB; PVC caps 8Gi (`ci-jobs`) and 10Gi (`ci-build`). Plan 37 enforces the caps. | — |
| Q-20-07 | Forgejo tag protection versus deploy keys. If deploy keys can push protected tags, an agent can create `v*` tags on `main` commits (reviewed code, so harmless but noisy) or on unreachable commits (classified untrusted by §8.2). | no | Rely on the §8.2 reachability rule. Plan 16 verifies tag-protection semantics and adds them if effective. | 16 / 55 |
| Q-20-08 | `sc-woodpecker` holds admin team membership (R-20-1). | no | Accept for MVP. Post-MVP: a Forgejo change to allow webhook creation without repository admin, or a webhook broker. | 34 |
| Q-20-09 | Woodpecker does not bind an agent's labels to its token: a holder of the ci-lane token could start an agent declaring `sc-lane=build`. | no | Per-agent tokens give individual revocation and attribution; `sc-ci-maint` step 5 detects a build-lane workflow run by any agent other than the registered build agent. Post-MVP: org-scoped or server-side label binding if Woodpecker adds it. | 20 / 34 |
| Q-20-10 | Concurrent pipelines in `ci-jobs` can reach each other's service pods (plan 36 CI-1 allows intra-namespace traffic), e.g. an untrusted PR and a GREEN verification with a database service at `CI_MAX_CONCURRENT=2` (SEC-25). | no | Plan 36 restricts CI-1 to the same workflow if Woodpecker sets a per-workflow pod label (verify at P08; record the label in 20-AC-8 evidence); otherwise accepted residual risk recorded in plan 34. Verification pipelines carry no secrets, which bounds the impact. | 36 / 34 |
