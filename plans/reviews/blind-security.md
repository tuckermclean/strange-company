# Blind review: security, secrets and admission

Reviewer: blind adversarial review (CD-34). Area prefix `SEC-B`. Date of review: 2026-10-14.

## 1. Scope read

Read in full or in the sections that matter for the boundary being tested:

- `SPEC.md` (§4, §21, §22, §36–§38, §84–§88, §98–§99, §105 and the table of contents), `plans/reviews/consolidation-decisions.md` (CD-01..CD-34).
- Plans: 34 (all), 35 (§1–§4, §10.3, §13, §14.1–§14.7), 36 (§8, §9.4–§9.9, allow-matrix rows W/CI/CB/FJ/SC/HE), 37 (§13 complete, VAP sketches ADM-01..63, RBAC table), 26 (§7.3, §10, §13 incl. Job builder, §14.4), 27 (§1, §10, §11, §13, §14.1–§14.5), 24 (§9, §22–§23), 23 (§14.9, §18, 23-AC-3), 20 (trust model, R-20-1, clone/secret sections, ACs 5, 20, 38, 39), 16 (§7.4 forge.yaml, branch protection, protected tags), 15 (flows, MFA, break-glass), 28 (harness registry, pins), 40 (worker OTel exception), 53 (§13, §14.1 pricing bound), 55 (merge-gate tables), 09 (OIDC entrypoint), 08 (referenced only), `plans/dag.yaml`, `plans/spec-issues.md` (SI-01..58).
- ADRs: 0003, 0004, 0007, 0008, 0012, 0014, 0018, 0150, 0251, 0253, 0301, 0302, 0303, 0305, 0350, 0400, 0401, 0500, 0501, 0502, 0503.
- Contracts: `task-token`, `gateway-token`, `forge-configuration` (protection and tag defs), `repo-policy` (diff limits), `runner-api.openapi.yaml` (route/security inventory), `secret-classes.yaml` (grep for untrusted-zone consumers).
- Procedures: `credential-compromise`, `emergency-merge`, `rotate-break-glass`, `break-glass-access` (grep).

Disclosure: one `grep -r` over `plans/` surfaced a single line from a file under `plans/reviews/` (a closure table row mentioning SEC-3). I did not open that directory and did not use the line; the corresponding plan text (20-AC-38, 23-AC-3, 37 E48) had already been read independently.

Not read in depth (outside the area list): plans 07, 08 (host firewall), 10, 11–14, 17, 18, 19, 21, 22 beyond cited sections, 25, 29–33 beyond cited sections, 38, 39, 41–52, 54.

## 2. Verdict

**FAIL, with one narrow blocker (SEC-B1).** Everything else is PASS WITH FIXES quality. The worker, Hermes, admission, token and secret-tier designs are unusually well specified and withstood most attacks I tried (section 4). The blocker is not a flaw in those mechanisms. It is that the control the whole threat model leans on at the end of every chain ("a human merges") is not enforced by any configured Forgejo mechanism, and several machine accounts hold the rights to approve and merge.

## 3. Findings (ordered by severity)

| ID | Severity | Location | Summary |
|---|---|---|---|
| SEC-B1 | blocker | plan 16 §7.4, `contracts/forge-configuration.schema.yaml` `branchProtection`, plan 34 §14.1 / §14.3 / R-5, plan 20 R-20-1 | "Human merge" is not enforced. Machine accounts can approve and merge. |
| SEC-B2 | major | plan 55 (`humanApprovalsRequired`, `elevatedApprovals`), plan 16 (`recovery` stage), plan 01 elevated paths, plan 35 §14.5.1 | A single operator cannot satisfy the approval rules the design depends on. |
| SEC-B3 | major | plan 26 §14.4 step 6, §7.3; ADR-0008 | The Runner's authoritative changed-path derivation does not specify completeness, rename, mode or gitlink semantics. |
| SEC-B4 | major | plan 20 §8.2 step "explicit clone step", ADR-0301 §4, plan 36 FJ-10 | The CI clone step that holds the repo-admin token does not disable submodule recursion or LFS on untrusted refs. |
| SEC-B5 | minor | plan 36 W-5, plan 40 §13, plan 34 §5 | Untrusted worker pods have a route into the monitoring collector that holds cluster-wide pod read. |
| SEC-B6 | minor | plan 34 AC-3, §13.5, §14.5 | Stale acceptance criterion: a correct implementation fails 34-AC-3 (five Secret keys, not four). |
| SEC-B7 | minor | ADR-0301, plan 20 AC-5/12..19 | The central third-party semantic (exclusive extension) is proven at bump time only. No runtime canary. |
| SEC-B8 | minor | plan 01 / `contracts/repo-policy.schema.yaml` example `elevatedPaths` | Enforcement-adjacent code is missing from the elevated-path list. |
| SEC-B9 | minor | plan 23 §14.9, ADR-0253 | Upstream keyless identity is not bound to a commit or environment, and ADR-0253's "read-only Actions" statement contradicts the publish workflow. |
| SEC-B10 | minor | plan 36 W-1, FJ-10 | Untrusted pods can reach Forgejo's HTTP port (3000) without need. |
| SEC-B11 | minor | plan 27 §13.3 | `sc-worker` file operations in the shared workspace are not specified as symlink-safe. |
| SEC-B12 | minor | `break-glass-access.md:68`, `rotate-break-glass.md#runner-admin`, ADR-0350 | Procedure inconsistencies and one deny-list where an allow-list is safer. |
| SEC-B13 | minor | plan 23 §14.6, plan 36 §9.4 | Renovate: direct Internet egress, package managers over repository content, write token on the whole org. |
| SEC-B14 | minor | plan 34 H-1, ADR-0501 | User namespaces for workers are deferred although the cost is one pod field. |

### SEC-B1 (blocker): human merge is a convention, not a control

**Where.** `plans/16-forgejo.md` §7.4 (`forge.yaml`: `enableMergeWhitelist: false`, no approvals whitelist, `requiredApprovals: 1`; teams `sc-automation(admin: sc-runner, sc-woodpecker)`, `sc-bots-write(write: sc-renovate)`, `sc-release(write: sc-release)`); `contracts/forge-configuration.schema.yaml` (the protection def has `enableMergeWhitelist` and nothing for approvals); `plans/34-security.md` §14.1 closing paragraph, §14.3 T-CI/T-SUP rows, R-5, R-15; `plans/20-woodpecker.md` R-20-1.

**Problem.** Forgejo counts an approval from any user with write access toward `required_approvals` unless an approvals whitelist is set, and lets any user with write access merge unless a merge whitelist is set. Neither whitelist exists in the schema or in `forge.yaml`. Plan 34 states: "At no step does a worker, Hermes or CI code hold a credential that can change `main`". That is false as designed:

- `sc-woodpecker` is in `sc-automation` (repo admin on every repository of the org). Its OAuth token sits in `woodpecker-pg`, and the Woodpecker server and the pinned clone step handle it (R-20-1).
- `sc-renovate` (write on every org repository) lives in the `renovate` pod, which has permanent direct Internet egress and runs package managers over repository content.
- `sc-release` (write on `strange-company`) is a Woodpecker secret.
- `sc-runner` (admin) and `sc-admin` (site admin) are trusted but are also machine accounts.

**Scenario.** An attacker who obtains any one of these tokens (a clone-image bug, a Renovate data-source bug, a `woodpecker-pg` backup read, SEC-B4) opens or reuses a PR authored by a different account, for example any `sc-runner`-authored agent PR. The attacker posts `strange-company/tdd-provenance` and `strange-company/review` as `success` (the plan accepts that any `write:repository` token can, R-5), submits an `APPROVED` review as the compromised bot to satisfy `required_approvals: 1`, and calls the merge endpoint. No human is involved. Flux then applies `main` with cluster-admin. T-STATUS detection (`SCProvenanceStatusForged`, up to 6 minutes) fires after the merge. `34-AC-5` only tests status reposting, not approval or merge.

**Fix.**
1. `contracts/forge-configuration.schema.yaml` and `forge/forge.yaml`: add `enableApprovalsWhitelist: true` with `approvalsWhitelistTeams: [engineers, Owners]` and `enableMergeWhitelist: true` with `mergeWhitelistTeams: [engineers, Owners]` (same human teams) on `main`, `release/*` and every protected pattern of managed repositories. Make these `const: true` for protected patterns, as `applyToAdmins` already is.
2. F11/`forge verify`: fail if any `sc-*` machine account is a member of a whitelisted team, and compare the whitelists in the 5-minute drift probe.
3. Plan 16: demote `sc-woodpecker` from `sc-automation` admin. Use the org-level hooks of CD-18 so Woodpecker no longer needs repo admin to create webhooks. Restrict `sc-renovate` to the repositories it updates, not the whole org team.
4. Plan 34: add an AC to GATE-C09: with the `sc-renovate`, `sc-woodpecker`, `sc-release`, `sc-runner` and `sc-admin` credentials, `POST .../pulls/{n}/reviews` with `APPROVED` does not change `mergeable`, and `POST .../pulls/{n}/merge` returns 403 on `main`. Reword §14.1's closing claim and R-5's compensating control to name the whitelist as the enforcement.

### SEC-B2 (major): a single operator cannot satisfy the approval rules

**Where.** Plan 55 (`humanApprovalsRequired: 1`; `elevatedApprovals: 2` counted only from human accounts, not machine accounts); `plans/16-forgejo.md` §7.4 `recovery` stage (`requiredApprovals: 2`); `plans/01-repository.md` elevated paths and `contracts/repo-policy.schema.yaml` (`src/internal/verify/**`, `statemachine`, `policy`, `sc-verify`, `sc-ci-config`); plan 35 §14.5.1 / open question 8; plan 34 R-19 ("single operator").

**Problem.** The design assumes one human (R-19, plan 35 §24). Forgejo does not let a PR author approve their own PR.
- Operator-authored PRs to `strange-company` (policy, ADRs, `security-review` PRs) can never reach one qualifying approval. The documented workaround (author via `sc-admin`) is stated only for toolbox-generated rotation and finalize PRs, and it blurs the human/machine separation that SPEC §21 and R-28 protect.
- Any self-hosted task that touches an elevated path (the judge code, which is the point of the Self-Hosting Threshold) needs 2 approvals from human accounts. One operator cannot produce them. If the operator uses a second account (`sc-breakglass`, `sc-admin`) to get there, the control is silently defeated; if not, the work never merges.
- The `recovery` protection stage needs 2 approvals, so the "PR-based alternative" in plan 16 Q-16-09 is unusable by one person.

**Fix.** Record the decision explicitly in an ADR (owner plan 55/16) and as a spec issue for operator acceptance. For example: "In single-operator installations the elevated-path rule is 1 human approval plus a mandatory delay (for instance 24 h between the last push and the merge, enforced by a Runner status) plus an explicit `/sc confirm-elevated <sha>` comment; `elevatedApprovals: 2` applies once the `engineers` team has at least 2 human members." Define one sanctioned way for the operator to merge their own non-task PR, and say which identity authors it. Remove or amend the `recovery` stage's 2-approval requirement for single-operator installs. Add an AC with exactly one human Forgejo account that merges (a) an operator-authored PR and (b) an agent PR touching an elevated path (45-AC-16 covers only the finalize PR).

### SEC-B3 (major): changed-path derivation completeness is not specified

**Where.** `plans/26-agent-runner.md` §14.4 step 6 ("changed paths from `compare(start_commit, commit)`"), §7.3 compare row; ADR-0008 item 1; `contracts/repo-policy.schema.yaml` (`maxFilesChanged` up to 2000).

**Problem.** Protected-path, role-diff, test-tamper and deletion checks all rest on one derived list of changed paths, and the worker-side pre-check is advisory (the worker is hostile-adjacent). The plan names a Forgejo endpoint ("verify paths at C03") but does not say that the list must be complete, nor how renames, copies, mode changes (`chmod +x`), type changes (file to symlink) or gitlinks appear. Forgejo list endpoints are paginated and some are capped (`MAX_GIT_DIFF_FILES` style limits). A policy that allows up to 2000 files can sit above a platform cap.

**Scenario.** A hostile harness produces one commit changing the maximum allowed number of files, with a change to `policy/` or `gitops/` sorting after the first page, or a rename of `.woodpecker/ci.yaml` that the API reports under the new path only. A Runner that reads the first page, or only `filename`, accepts it. The commit is protected-path-violating but judged `accepted`.

**Fix.** Plan 26 §14.4 step 6: derive paths with `git diff-tree --no-renames --raw -z` semantics. If only an API is available, require the Runner to page to completion and compare the count with the commit's total; mismatch or truncation is a fail-closed `DIFF_UNVERIFIABLE`. State that both old and new paths are checked (a rename is delete plus add), that mode and type changes count as modifications, and that gitlink changes are denied unless the repository policy allows them. Add a fixture AC (more files than one page, a rename out of a protected prefix, a chmod of a protected script) to 26-AC for GATE-C02, and add `maxFilesChanged <= verified API completeness limit` as a loader invariant.

### SEC-B4 (major): CI clone of untrusted refs does not disable submodules or LFS

**Where.** `plans/20-woodpecker.md` §8.2 step (explicit `clone` step with `settings: {depth: 0, tags: true}`), ADR-0301 §4, plan 36 FJ-10 ("SSH for submodules"), R-20-1, R-23.

**Problem.** The clone step receives the `sc-woodpecker` netrc token (repo admin on all org repositories) and runs on refs pushed by workers and on human PRs. The worker side sets `submodule.recurse=false` and `GIT_LFS_SKIP_SMUDGE=1` (plan 27 §13.3), but the CI clone settings do not. The Woodpecker git plugin recurses into submodules by default (verify). `.gitmodules` and `.gitattributes` are repository-controlled and are not in the protected-path list.

**Scenario.** A prompt-injected worker commits a `.gitmodules` pointing at another private repository on `git.<domain>` (the netrc machine entry matches) or at an attacker host, and a gitlink. CI's clone step, holding the repo-admin token, fetches it into an untrusted workspace, or processes attacker-controlled submodule or LFS content with Git code that has had submodule-handling vulnerabilities. A clone-image compromise yields the `sc-woodpecker` token, which with SEC-B1 is enough to approve and merge.

**Fix.** `sc-ci-config` normalization (ADR-0301 §4) forces clone settings `recursive: false`, `lfs: false`, `submodule_override` empty, and rejects configurations that restore them. Add `.gitmodules` and `.gitattributes` to the denied-for-autonomous paths unless the repository policy opts in. Plan 20: AC where a fixture branch with `.gitmodules` pointing at a second private repository results in no fetch of that repository (Forgejo access log shows no request from `sc-woodpecker` for it). Narrow FJ-10 to port 3000 only (no 2222) once submodules are off.

### SEC-B5 (minor): untrusted worker pods reach the collector that holds cluster-wide pod read

Plan 36 W-5 / plan 40 §7.3: workers (hostile harness shares the pod network) send OTLP to `otel-collector-workers:4318`, which is the `otlp/workers` receiver of the same collector Deployment that uses `k8sattributes` and so holds cluster-wide pod read (plan 36 §9.7). Plan 34 §5 does not list this egress, and Q-40-4 says plan 34 "confirms" it; plan 34 never does. Impact today is span forgery and a parser-bug pivot to a pod with API read. If 23-AC-3 shows Woodpecker inlines secrets into pod specs, that read includes release credentials, and E48 does not test the monitoring service accounts. Fix: run the workers receiver as a separate Deployment with no ServiceAccount token (or relay spans through the Runner), add the egress to plan 34 §5 and the zone table, and add Prometheus, KSM, the OTel collector and the Grafana sidecar service accounts to E48's "cannot read `ci-build` pod specs" list.

### SEC-B6 (minor): 34-AC-3 and §13.5 are stale

`34-AC-3` requires the per-execution Secret's keys to be exactly `["deploy-key","gateway-token","known_hosts","task-token"]`. Plans 26 §13 and 37 (`allowedSecretKeys`) add `egress-token`; a correct implementation fails the AC. §13.5 and §14.5 also omit the egress token from the worker's credentials. Update AC-3 to five keys, add the egress token to §13.5 and §14.5 (bounded by the proxy allow-list, no revocation until `exp`), and add an AC that the `harness` container sees only `gateway-token` and `egress-token`.

### SEC-B7 (minor): no runtime canary for the CI trust model

ADR-0301 rests on `WOODPECKER_CONFIG_EXTENSION_EXCLUSIVE` behaving as documented. 20-AC-5 only checks that the env var is present; the behavioural negatives (20-AC-12..19) run at P08 and at version bumps. A rename or semantic change in a later minor, or a silent fall-through, would leave untrusted refs with their own YAML and secrets. Add a scheduled canary (`sc-ci-maint`, daily, on `sc-e2e/ci-probe`): push an unprotected-branch workflow that references a canary `from_secret` and `sc-lane: build`; the check passes only if no pipeline step runs with it. Alert `SCCITrustModelBroken` (critical, category security).

### SEC-B8 (minor): elevated-path list omits code that enforces security

`elevatedPaths` lists verify, statemachine, policy, `sc-verify` and `sc-ci-config`. It omits the Job builder, `src/internal/scrub`, the secrets-lint and `sc-gitops` (admission-params and F11), `egress-proxy`, the Gateway auth path and `sc-release`. Their output is partly guarded by protected committed files, but the checks themselves run from the head of an untrusted PR in the verify pool. Add these to `elevatedPaths` and to `toolingFrom: base` for the F11/lint targets.

### SEC-B9 (minor): upstream keyless trust

Plan 23 §14.9 trusts `publish.yaml@refs/tags/v*` with a subject regex only. The GitHub mirror PAT has Contents R/W and Workflows R/W (ADR-0253) and can create tags, so a leaked PAT can add a workflow change and a tag and obtain a valid keyless signature for attacker-built images. ADR-0253's statement "GitHub Actions run with read-only tokens and no secrets" is incompatible with a workflow that needs `packages: write` and `id-token: write`. Pinned digests and the black-start bundle-manifest comparison (plan 45) limit the impact, but the design should say so. Fix: run `publish.yaml` in a GitHub Environment with a required human reviewer and pin `environment:` in the trusted subject regex; state the real GitHub posture in ADR-0253.

### SEC-B10 (minor): Forgejo HTTP from untrusted pods

W-1 allows `agent-jobs` to Forgejo 2222 and 3000 ("LFS/raw reads"), but workers use SSH and LFS smudge is disabled. Port 3000 exposes the full web and API surface of the most valuable pre-auth target to hostile code. Allow 2222 only for workers; keep 3000 for `ci-jobs` clone only.

### SEC-B11 (minor): symlink safety of `sc-worker` on the shared workspace

The harness controls `/workspace`; `sc-worker` reads `/workspace/out/claim.json`, shim files and the tree, and writes restored `.git/config`, `.git/hooks` and alternates. Plan 27 does not require `O_NOFOLLOW` or `openat2(RESOLVE_NO_SYMLINKS)`. A symlink at one of those paths can make `sc-worker` read `/var/run/sc/secrets/*` (the scrubber then redacts exact values, so confidentiality largely holds) or delete or overwrite files in `/var/sc/private`. Mostly a denial-of-service issue. Specify no-follow semantics, validate `.git` is a real directory before restore, and add the fixture to 27-AC-10.

### SEC-B12 (minor): procedure and ADR consistency

- `break-glass-access.md:68` says emergency merge uses stage `recovery`; `emergency-merge.md` and plan 16 use stage `bootstrap`.
- `rotate-break-glass.md#runner-admin` rotates the break-glass Runner credential "after every use" through a PR, merge and Flux. That path may be unavailable in exactly the outage that required its use. State a degraded path (rotate by patching `agent-runner-admin` directly with cluster break-glass, then reconcile Git).
- ADR-0350 authorizes `/sc` commands by a deny-list of five machine names plus team membership. Prefer an allow-list: only accounts whose Forgejo login source is the Authentik OIDC source and which pass the `sc-*` username rule.

### SEC-B13 (minor): Renovate blast radius

Renovate has permanent direct Internet egress (plan 36 §9.4), executes package managers over repository content, and `sc-renovate` has write on the whole org (SEC-B1). Route it through a dedicated proxy listener with a Renovate profile, limit the token's team to the repositories it manages, and never give it admin. SEC-B1's whitelists make its compromise survivable.

### SEC-B14 (minor): user namespaces

R-2 (kernel or runtime escape) is the highest-impact accepted risk because workers share the single node with etcd. H-1 (`hostUsers: false`) is deferred to after verification. The pod field is available on the pinned k3s and kernel (23-AC-6 already probes it). Make `requireUserNamespaces: true` the default for `agent-jobs` as soon as 23-AC-6 passes, rather than a post-MVP hardening, and state the fallback.

## 4. Attacks tried that the design withstood

- **Hostile harness reaches the deploy key or Runner token.** Two containers, no shared PID namespace, separate secret mounts, VAP ADM-15a/b pin which container mounts what, `items` limited to two keys. Held.
- **Worker pod shape escape** (privileged, hostPath, SA token, other Secret, extra container, own image, `command` games to read other volumes). PSA plus 20 ADM checks; pods only from the Job controller; Jobs and Secrets only from the Runner; Secret name equals execution id. Held. Positive controls for GC and TTL paths exist.
- **VAP parameter tampering or deletion bricking the cluster.** Cluster-wide policies are parameter-free (ADR-0503); namespace-bound ones fail closed. Held.
- **Forged or cross-used tokens.** Distinct `typ`, `aud`, `token_use`; Gateway requires a DB row and a claims hash, so a stolen signing key alone cannot mint a usable Gateway token. Held. Egress token has no revocation (documented).
- **Hermes escape.** No RBAC, no Forgejo account, Runner and Gateway egress only, Traefik `oidc` entrypoint for `auth.` only (SEC-16), toolsets disabled, managed scope with override tests, containment revoke. Held (toolset names are marked "verify" and covered by 24-AC-2).
- **Status forgery.** Detected and reposted by the Runner watcher (CD-07). Held as detection only; enforcement of the merge itself is SEC-B1.
- **Worker pushes tags, `release/*` or a signing-lane trigger.** Protected tags and `release/*` are fail-closed at P11; `sc-release` self-check; config served from `main`; Runner ref audit with fail-closed attribution. Held.
- **Release credentials via an untrusted ref.** Exclusive extension filters `from_secret`, lane label and backend options; admission ADM-46 allows secret env only on `sc-release` without entrypoint override; fallback to a Secret mount if Woodpecker inlines. Held, subject to SEC-B7.
- **Cluster age key theft.** Tier 2 (off-cluster authority) and tier 1b (release credentials) are not encrypted to the cluster key; upstream operators' Secret read is recorded and tested (37-AC-30). Held.
- **Egress proxy abuse** (IP literals, DNS rebinding, ports other than 443, redirects, cross-listener use). Checked address is the connected address; private, service, pod and node ranges denied; provider hosts forbidden; per-listener NetworkPolicy. Held. Domain fronting and writable registries are accepted exfiltration (R-9, SI-53).
- **Gateway as open proxy or spend bypass.** Upstream from policy, absolute-URI and Host rejected, redirects not followed, provider-hosted tools and URL sources denied, provable worst-case cost, separate DB roles. Held.
- **Runner admin API reached from workers or Hermes.** Loopback only, separate listener per credential type, actor derived from the credential (CD-30). Held.
- **Git hooks, filters and fsmonitor run by `sc-worker`.** Snapshot and restore of `.git` internals, hooks path forced to `/dev/null`, private gitconfig. Held (symlink nit in SEC-B11).
- **Break-glass lock-out.** Bundle-held credentials, node SSH user, Authentik and Forgejo local accounts, detection from source-system timestamps, silence-proof alerting. Held.
- **Bootstrap images before P11.** Keyless upstream signature plus digest pin in Git, with V-26 verification; black-start bundle-manifest comparison. Held, with the SEC-B9 caveats.
