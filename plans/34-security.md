# 34 — Security architecture and threat model

**Status:** Stage-0 plan (draft)
**DAG nodes:** none owned. This plan is cross-cutting: its acceptance criteria are evaluated inside **GATE-F12** (37/36), **GATE-C02** (26/27/37), **GATE-C09** (55/22) and **GATE-P12** (50/19). Consolidation should add `"34"` to those nodes' `plans` lists in `plans/dag.yaml`.
**Related plans:** every plan; principally [07](07-k3s.md), [08](08-node-security.md), [09](09-networking.md), [15](15-authentik.md), [16](16-forgejo.md), [17](17-github-mirror.md), [20](20-woodpecker.md), [22](22-verification-policy.md), [23](23-supply-chain.md), [24](24-hermes.md), [25](25-context.md), [26](26-agent-runner.md), [27](27-worker-contract.md), [28](28-harnesses.md), [33](33-budget-enforcement.md), [35](35-secrets.md), [36](36-network-policy.md), [37](37-admission-policy.md), [40](40-opentelemetry.md), [42](42-alerting-notifications.md), [53](53-model-gateway.md), [55](55-forge-workflow.md)
**Contracts:** consumes `secret-classes.yaml` (35), `alert-catalog.yaml` (42), `harness.schema.yaml` (28), `gateway-token.schema.yaml` (53), `worker-input`/`worker-result` (27)
**ADRs:** relies on ADR-0003, 0004, 0007, 0008, 0010, 0011, 0012, 0013, 0014, 0016, 0017, 0019, 0251 (plan 15), 0253 (plan 17), 0500–0503 (this group)

> Every section below is mandatory (SPEC §121).

## 1. Purpose

This plan is the system-wide security architecture of Strange Company: what is protected (assets), from whom (actors and threats), where the trust boundaries are and what enforces each one, how every SPEC §105 requirement maps to a mechanism and a deterministic test, how human and machine identity stay separate, what the audit trail is, which alerts signal a security event, how incidents are handled, and which residual risks are knowingly accepted and by whom. Subsystem plans implement the controls; this plan makes sure they compose into **defense in depth** [SPEC §85] and that the central promise of SPEC §129 holds: *at no point does the creative implementation worker possess production authority.*

## 2. Requirements derived from SPEC.md

1. Models propose; deterministic systems decide [SPEC §4.3]. Authority decreases toward production [SPEC §4.9]. Coding agents never deploy production [SPEC §4.2].
2. Hermes has no general Kubernetes workload-creation privileges [SPEC §4.6, §59] and cannot specify image, namespace, ServiceAccount, Secrets, volumes, host paths, privileged mode, security context, raw YAML or resources [SPEC §62].
3. Network policy stays enabled; inter-node traffic is encrypted; agent namespaces have explicit NetworkPolicy; control interfaces are not public without justification [SPEC §18].
4. Human and machine identity are separate; automation uses scoped machine credentials [SPEC §21]. Break-glass access exists, lives outside the cluster, is tested and audited [SPEC §22].
5. Supply chain: traceability, pinned dependencies, immutable references, scanning, SBOM, provenance, signing, verification policy, automated update PRs; stochastic workers never receive release-signing keys [SPEC §37]. Workers never receive Docker/containerd sockets, privileged DinD, registry-push or deployment credentials [SPEC §38].
6. Worker security context, zero-right worker ServiceAccounts, admission defense in depth, scoped worker credentials, Forge credential limits, default-deny network policy [SPEC §84–§88].
7. Telemetry privacy [SPEC §94]. Secrets encrypted in Git; every secret class owned [SPEC §98, §99].
8. CI verifies the SPEC §105 escape list.
9. An independent fresh-context security review is performed [SPEC §132 item 44].

Derived requirements:

- **34-R-1 Two independent controls.** Every threat in §14.3 is mitigated by at least two controls owned by **different** components (e.g. Runner policy + admission, admission + RBAC, NetworkPolicy + credential scope), or it appears in the residual-risk register (§14.8) with an owner.
- **34-R-2 Repository content is hostile.** Anything that originates in a repository, issue, PR, dependency, test output, model output or handoff is untrusted input, including in `strange-company` itself. No component executes it with authority beyond the untrusted zones (lesson L-2).
- **34-R-3 Enforcement never lives in a prompt.** A control that exists only in instructions to a model is not counted as a control [SPEC §4.7, §58].
- **34-R-4 Every SPEC §105 item has a named deterministic test** in k3d (F12) and a live counterpart (C02) (§22.1).
- **34-R-5 Every security alert routes to a human** through Alertmanager (ADR-0009) with `category: security`.
- **34-R-6 Untrusted zones hold no reusable credential.** Every credential reachable from `agent-jobs`, `ci-jobs` or `hermes` is either per-execution and revocable, or scoped so that its worst use is bounded by budget, a single repository's unprotected refs, or read-only access.

## 3. Inputs

SPEC; the master plan; every subsystem plan's §10 (Secrets) and §13 (Security boundary); the secret register; the alert catalogue; ADRs; `plans/reference/prior-implementation-lessons.md` (L-2 hostile repository content, L-4 empty secrets, L-6 GitHub credential scope).

## 4. Outputs

- This threat model, trust-zone map and residual-risk register.
- `test/security/traceability.yaml`: SPEC §105 item → test IDs (consumed by 34-AC-1).
- Security-alert requirements for plan 42 (§21).
- Requirements placed on other plans (§5 table).
- The independent review record `plans/reviews/review-security.md` (written by the fresh-context reviewer; format §22.4).

## 5. Dependencies

No hard DAG dependency of its own. Requirements this plan places on other plans (each is an interface assumption the named plan must honor):

| Plan | Requirement |
|---|---|
| 26 | Job builder exactly per §13 of plan 26; per-execution credentials only; secret scan of results/logs with plan 35's pattern set; ref audit; Runner never calls Forgejo branch-protection endpoints; **status-integrity watcher** (§14.4 T-STATUS). |
| 27/28 | Production harnesses run with repository config ignored (`capabilities.ignoresRepoConfig: true`, no project hooks, no repo `CLAUDE.md`-style auto-loading), stdin closed (L-2), non-root uid 65532, read-only root FS. |
| 36 | Default-deny per namespace; worker egress only DNS, Forgejo SSH, Runner 8082, Gateway 8080 and `egress-proxy` (ADR-0150: per-task FQDN allow-list from `policy/egress.yaml`, provider APIs always forbidden); no worker path to the Kubernetes API, CNPG, Longhorn, Flux, Prometheus, Hermes or provider APIs; Hermes egress only Runner 8081 and Gateway 8080 (+ Authentik for dashboard OIDC). |
| 37 | All policies of plan 37 §13.2; RBAC table plan 37 §13.3. |
| 22 | Protected paths include `security/**`, `.woodpecker/**`, `.strange-company/**`, `policy/**`, `contracts/**`, `AGENTS.md`, `gitops/**`, `infra/**`, `toolbox/**`, `versions/**`, `.sops.yaml`, `secrets/**`, `.github/**` for autonomous tasks. |
| 55 | Required status contexts; status-integrity semantics (T-STATUS); protected tags `*` (plan 17 §13.3). |
| 16 | Protected-tag rule; branch protection `applyToAdmins: true`; forge drift probe (`SCForgeConfigDrift`); `DISABLE_GIT_HOOKS=true`. |
| 20/23 | Verify pool without secrets; build pool only for protected refs; release credentials only in the `sc-release` plugin step (ADR-0502). |
| 40/41/53 | Telemetry privacy pipeline; Gateway never logs bodies; worker/CI/Hermes logs excluded from Loki (ADR-0552). |
| 08 | Host firewall allowlist; SSH hardening; break-glass node user. |
| 42 | Catalogue entries for every alert in §21 with `category: security`. |

## 6. Owned state

The residual-risk register (§14.8) and `test/security/traceability.yaml`, both in Git. No runtime state.

## 7. External interfaces

- **Security review checklist** (§22.4) used by the fresh-context reviewer and at every change to a trust boundary (PRs touching `gitops/platform/admission/**`, `gitops/platform/netpol/**`, `policy/harnesses.yaml`, `security/**`, `.sops.yaml`, RBAC templates of plan 54 require the label `security-review` and a reviewer from the Authentik group `sc-admins`; plan 55 enforces via CODEOWNERS-equivalent required review, verify Forgejo support).
- **Incident handling** entry points: `procedures/credential-compromise.md` (plan 35), `procedures/break-glass-access.md` (plan 15), `procedures/quarantine-worker.md` (plan 26/27), `procedures/budget-emergency-stop.md` (plan 33), `procedures/signing-key-rotation.md#emergency-revocation` (plan 23/35).

## 8. Internal interfaces

N/A — this plan has no runtime components; it composes the interfaces of §5.

## 9. Configuration

N/A as a component. Security-relevant configuration lives with its owners: `gitops/platform/admission/params.yaml` (37), NetworkPolicies (36), `policy/**` (26/22/31/33), `.sops.yaml` (35), `security/**` (35, 23), `forge/forge.yaml` (16). All are protected paths changed only by human-reviewed PR.

## 10. Secrets

No class owned. Credential-leak analysis per class is in §14.5; class metadata is in `contracts/secret-classes.yaml`.

## 11. Storage

**Audit trail sources and retention** (what proves who did what):

| Source | Records | Location | Retention | Owner |
|---|---|---|---|---|
| Kubernetes API audit log | every mutating request (Metadata level; never Secret bodies), impersonation, VAP audit annotations | `/var/lib/rancher/k3s/server/logs/audit.log` on each server; Loki job `k8s-audit` at small-ha | 6 × 50 MiB / 7 d on node; 30 d in Loki (plan 41) | 07, 41 |
| Node auth | sshd/sudo journal; `sc-breakglass` PAM textfile timestamp | journald (plan 08 retention), node-exporter textfile | ≥ 30 d journald (plan 08) | 08, 15 |
| Authentik events | logins, admin actions, model changes, token use | `authentik-pg` (+ backups) | 365 d | 15 |
| Forgejo | pushes, PRs, reviews, statuses (with creator), deploy-key and protection changes, admin actions | `forgejo-pg` (+ PITR backups), access log | indefinite (DB); logs per plan 16 | 16 |
| Git history | every change to desired state, policy, secrets ciphertexts; commit trailers `SC-Task`, `SC-Execution` | Forgejo + GitHub mirror + S3 bundles | indefinite | 16, 17, 44 |
| Runner | task transitions (timestamp, actor, old/new state, reason, policy revision), executions, violations, admin-API calls | `sc-pg` schema `runner` | indefinite (tasks, transitions); 400 d execution events | 26 |
| Gateway ledger | every model request with token jti, model requested/served | `sc-pg` schema `ledger` | indefinite | 53, 32 |
| Evidence | RED/GREEN documents, scrubbed worker logs, quarantined artefacts, check results, break-glass drill evidence | S3 `<prefix>-evidence` (versioned) | plan 43 (≥ 400 d for quarantine and drills) | 26, 22, 43 |
| Woodpecker | pipelines, who triggered, logs | `woodpecker-pg` | plan 43 | 20 |
| Supply chain | signatures, SBOM and provenance attestations per digest | Forgejo OCI registry (S3 `forgejo-objects`) | life of the image + 400 d | 23 |
| Alerts | notification history | external transport; Alertmanager memory | transport-defined | 42 |
| Recovery material | bundle versions, generation ledger, attestations | S3 `<prefix>-recovery` | never auto-deleted | 35 |
| External | provider, Cloudflare, GitHub audit logs | third parties | provider-defined | 03/09/17 |

## 12. Resource requirements

N/A — no workloads.

## 13. Security boundary

### 13.1 Assets

| ID | Asset | Why it matters | Primary store |
|---|---|---|---|
| A1 | Approved desired state (`main` of `strange-company`) | it *is* production (SPEC §4.2) | Forgejo, GitHub mirror |
| A2 | Merge gate integrity (statuses, reviews, branch protection) | decides what reaches A1 | Forgejo |
| A3 | Release artefacts and signing key | decides what runs | Forgejo registry; `SC-RELEASE-SIGNING` |
| A4 | Recovery material (bundle, age keys, passphrase, off-cluster credentials) | black start | operator, S3 `recovery` |
| A5 | Cluster control (cluster-admin, node root, Flux) | everything | nodes, `flux-system` |
| A6 | Money (provider keys, budgets) | direct financial loss | Gateway, provider accounts |
| A7 | Source code and data confidentiality (private repos, DBs, backups) | IP and personal data | Forgejo, CNPG, S3 |
| A8 | Task state and TDD provenance | trust in "test-first" claims | `sc-pg`, evidence |
| A9 | Human identity (Authentik) | gate to every human surface | Authentik |
| A10 | Availability of the platform | the factory works | cluster |

### 13.2 Actors and threats

| ID | Actor / threat | Capability assumed |
|---|---|---|
| T-INJ | Hostile repository content / prompt injection (L-2): text or files in repositories, issues, dependencies, test output or handoffs that steer a harness or Hermes | full control of model behaviour inside one execution or one Hermes turn |
| T-MIS | Misaligned or simply wrong model output | same as T-INJ, without intent |
| T-WRK | Compromised worker: arbitrary code execution inside a worker pod | everything the pod can reach |
| T-HER | Compromised Hermes (or Hermes PVC, or its dashboard session) | everything Hermes' credentials and network allow |
| T-CI | Malicious code executed by CI (tests or implementation under test; ADR-0008, SI-07) | code execution in a verify-pool step pod |
| T-SUP | Compromised dependency or image (upstream image, Helm chart, Go module, npm package of a harness CLI, base image) | code execution wherever that artefact runs, including trusted builds |
| T-LAP | Stolen or compromised operator laptop | `/blackstart/`, `bootstrap.env`, SSH key, browser sessions |
| T-TOK | Any single token class leaked | the class's scope (§14.5) |
| T-OPS | Operator error | any action the operator can take |
| T-PRV | Provider compromise or coercion (VPS provider, Cloudflare, object storage, GitHub, model providers) | read/modify data held by that provider |
| T-NET | Network attacker on the public Internet or the provider network | traffic observation and injection outside the cluster |

### 13.3 Trust zones and their enforcement

| Zone | Contents | Trust | Enforcement mechanisms | Residual |
|---|---|---|---|---|
| **Z-EDGE** | Cloudflare edge + `cloudflared` (tunnel) or Traefik host ports 80/443 (direct) | untrusted inbound | no open HTTP ports in tunnel mode (ADR-0010); host firewall (plan 08); TLS (plan 10); only listed hostnames (`auth.`, `git.`, `ci.`, `grafana.`, `hermes.`); Authentik OIDC / forward-auth; Runner, Gateway, Prometheus, Alertmanager (except forward-auth), Longhorn UI, Flux never public | R-8, R-13 |
| **Z-HOST** | nodes, k3s, containerd, etcd | root of trust | firewall allowlist (6443/2379/2380/10250/51820 peers only; SSH restricted, plan 08); SSH keys only; break-glass node user with PAM audit (plan 15; plan 08 must create the user, group, sudoers and PAM hook, review SEC-9); pod-CIDR access to node ports limited to {6443, 10250, 9100, 2381} by plan 08 and tested from every untrusted namespace by plan 36 (review SEC-5); k3s secrets-encryption; audit log; WireGuard between nodes (ADR-0011); unattended security updates and monthly reboot (plan 08) | R-2, R-25 |
| **Z-ADMIN** | `system:masters`, Flux controllers, break-glass | full | no human RBAC in MVP; admin kubeconfig only via SSH tunnel; Flux reads Git through a read-only deploy key (ADR-0016) and applies only reviewed `main`; drift detection; break-glass alerts | R-3 |
| **Z-PLAT** | `flux-system`, `cert-manager`, `cnpg-system`, `longhorn-system`, `ingress`, `monitoring`, `monitoring-node`, `forgejo`, `authentik`, `woodpecker`, `renovate`, `dr-tests` | trusted (reviewed upstream software) | PSA per plan 37 §13.1 (+ compensating VAPs where privileged); default-deny NetworkPolicy (36); digest-pinned images (F11, `sc-images-digest-pinned`); RBAC from pinned charts; secrets via SOPS | R-7, R-15 |
| **Z-CTRL** | `strange-company`: Runner, Gateway, `sc-pg` | trusted control (deterministic code) | PSA restricted; RBAC minimal (plan 26 §13, 53 §13); `sc-runner-scope` VAP; images built only by the build pool, signed, promoted by human-merged PR; loopback admin API; no public ingress | R-18 |
| **Z-HERMES** | `hermes`: Hermes Agent + PVC | **authenticated-untrusted** | no RBAC (`sc-zero-rights-subjects`); `sc-untrusted-sa-no-mutation`; NetworkPolicy to Runner MCP + Gateway only; MCP surface is narrow and schema-validated with forbidden-field rejection (plan 26 §7.1); gateway token with orchestration budget; managed scope pins operator config (ADR-0003, L-1) | R-16 |
| **Z-AGENT** | `agent-jobs`: worker pods | **untrusted** | Runner Job builder; PSA restricted; `sc-agent-jobs-*` VAPs; RBAC none; default-deny egress except DNS/Forgejo SSH/Runner/Gateway/registries; per-execution credentials (deploy key on one repo, task token, gateway token) revoked at end; ResourceQuota/LimitRange/deadlines; Runner judges results (protected paths, diff rules, secret scan, ref audit) | R-2, R-9, R-17 |
| **Z-CIV** | `ci-jobs`: verify-pool step pods (all events except protected-ref builds) | **untrusted for execution; trusted only as a sandbox whose output the Runner validates** | PSA restricted; `sc-ci-jobs-pod`; no secrets of any kind; Runner-triggered verification with base-revision config and pulled evidence (ADR-0008); NetworkPolicy (36) | R-1 |
| **Z-CIB** | `ci-build`: build-pool step pods | trusted **only** for pipelines on protected refs (`main`, `release/*`, protected tags `v*`) | routing by the Woodpecker config extension `sc-ci-config` (plan 20/23); PSA baseline + `sc-ci-build-pod`; release credentials are Woodpecker secrets (events `[cron, tag]`, image filter `sc-release`) referenced only by protected configurations, and admission allows release-secret env only on the `sc-release` image without entrypoint override (`sc-ci-build-release-steps`); `sc-release` verifies the commit is on a protected branch via the Forgejo API before using any credential (ADR-0502) | R-14, R-15 |
| **Z-APPS** | `apps` | trusted as reviewed code | PSA restricted, default-deny, GitOps only | — |
| **Off-cluster** | operator machine, GitHub, object storage, providers | operator: trusted; providers: trusted for availability, partially for confidentiality | SOPS two-tier (35-R-3); passphrase-encrypted bundle; encrypted OpenTofu state; repo bundles encrypted with `SC-AGE-BACKUP`; GitHub ruleset `sc-mirror-only` (plan 17) | R-7, R-10, R-11, R-19 |

### 13.4 STRIDE per zone (condensed)

| Zone | Spoofing | Tampering | Repudiation | Information disclosure | Denial of service | Elevation of privilege |
|---|---|---|---|---|---|---|
| Z-EDGE | Authentik OIDC + MFA; break-glass accounts MFA-exempt but bundle-only | TLS end to end (Cloudflare→Traefik internal TLS) | Authentik/Forgejo logs | Cloudflare sees plaintext (R-8) | Cloudflare; Traefik limits | no admin surfaces public |
| Z-HOST | SSH keys only; no passwords | file integrity via Ansible `--check` drift (V-11) | journald, audit log | secrets-encryption at rest; snapshots encrypted | resource reservations, PriorityClasses (51) | kernel hardening, sysctls (08); seccomp everywhere |
| Z-ADMIN | client certs via SSH tunnel; no public 6443 | Flux reverts drift; Git is source | audit log; Git history | `sops-age` readable only by Flux/admin | — | break-glass only, audited |
| Z-PLAT | per-service credentials (SOPS) | GitOps + drift detection | Git + Flux events | per-namespace Secrets; netpol | quotas for agents/CI protect platform (51) | PSA + VAP; pinned charts |
| Z-CTRL | Hermes bearer (constant-time), JWT (EdDSA, kid, aud), HMAC webhooks | state changes only through state machine; optimistic locks; outbox | transitions table (actor), admin calls alert | no secrets in DB/logs (26-R-3) | rate limits on MCP; budgets | `sc-runner-scope`; no ClusterRole |
| Z-HERMES | MCP bearer per Hermes; dashboard OIDC | cannot write policy or state except via MCP | every MCP call attributed `actor=hermes` | holds conversation history (non-authoritative) | MCP rate limit 10/s; orchestration budget | zero RBAC; no egress beyond Runner/Gateway |
| Z-AGENT | task token bound to execution; deploy key bound to repo | cannot push protected branches; ref audit; protected paths | commit trailers; execution records | only its own repo + its own Secret; logs scrubbed | deadlines, quotas, single slot | PSA/VAP/RBAC; no SA token |
| Z-CIV | Runner checks pipeline creator, SHA, repo, config | evidence forgery possible (R-1) | Woodpecker records | no secrets mounted | CI quota, heavy slot (51) | PSA restricted, no token |
| Z-CIB | protected-ref routing; plugin self-check | build inputs pinned; SBOM; provenance | signed provenance with pipeline number | release creds only in plugin pod | — | baseline + VAP; userns where verified |

### 13.5 Human versus machine identity [SPEC §21]

| Identity | Authenticates with | Can reach | Cannot |
|---|---|---|---|
| Human engineer/admin | Authentik OIDC + MFA (password + TOTP/WebAuthn) | Forgejo, Grafana, Hermes dashboard, Woodpecker (Forgejo OAuth), Alertmanager (forward-auth, `sc-admins`) | Kubernetes API (no RBAC); Runner admin API (needs port-forward = break-glass) |
| Human operator (break-glass) | `SC-BREAK-GLASS-*`, `SC-RUNNER-ADMIN`, `SC-K3S-ADMIN-KUBECONFIG` via SSH tunnel | everything | — (every use alerts) |
| Hermes | `SC-RUNNER-HERMES`, `SC-GATEWAY-TOKEN/hermes` | Runner MCP, Gateway | Kubernetes, Forgejo, providers, CNPG |
| Worker | `SC-WORKER-TASK-TOKEN`, `SC-GATEWAY-TOKEN/worker`, `SC-WORKER-DEPLOY-KEY` | its packet/result, Gateway within scope, clone/push one repo | everything else |
| Runner | SA token; `sc-runner` Forgejo token; `SC-WOODPECKER-RUNNER`; `SC-RUNNER-SIGNING` | `agent-jobs` Jobs/Secrets, Forgejo API, Woodpecker API | other namespaces; branch protection (by code policy, R-4) |
| Gateway | SA token (one ConfigMap); `SC-MODEL-API-KEYS` | providers, `sc-pg` ledger | Kubernetes writes |
| Flux | read-only deploy key; `sops-age` | Git read; cluster apply | Git write |
| Bots (`sc-runner`, `sc-renovate`, `sc-backup`, `sc-release`, `sc-registry-reader`, `sc-woodpecker`) | scoped Forgejo tokens; random passwords discarded at creation (plan 16 S09) | per scope | interactive login (password unknown); merge to protected branches |
| Authentik `sc-audit` | API token, `service_account` type | read users/events | any flow login |

Rules: no credential is shared between a human and a machine; no automation uses a human's token (operator-run toolbox actions that open PRs use the operator's own credentials and are human actions); machine accounts are named `sc-*` and Authentik forbids `sc-*` usernames for humans (plan 15).

## 14. Happy path

### 14.1 The secured canonical flow (SPEC §129)

1. Human intent → Hermes (Authentik-protected dashboard). Hermes calls `agent_start` (bearer). **Controls:** MCP schema validation and forbidden-field rejection; policy resolves image/SA/resources; budget pre-check.
2. Runner creates Job + per-execution Secret. **Controls:** Job builder (plan 26) → PSA → `sc-agent-jobs-*` VAPs → RBAC (only the Runner may) → quota.
3. Worker clones over SSH with its deploy key (one repo), calls models through the Gateway with its token (allowed models, budget), pushes `agent/<task>`. **Controls:** NetworkPolicy; branch protection rejects deploy-key pushes to protected branches; Gateway budget/e-stop; harness ignores repo config.
4. Result → Runner judges Git facts, protected paths, test tamper, secret scan, ref audit; revokes credentials; archives scrubbed logs.
5. Runner triggers RED/GREEN pipelines (verify pool, no secrets, base-revision config) and pulls evidence (ADR-0008).
6. Runner posts `strange-company/tdd-provenance` and `strange-company/review` after re-deriving facts (ADR-0019). Human reviews and merges.
7. A human tags a release (`v*`, protected tag on `main`) → build lane → after the off-cluster gate, images are built, scanned, SBOM'd, signed and attested one at a time; promotion PR by `sc-release` (plan 23). Human merges the digest bump.
8. Flux (read-only) applies the merged digest; the Runner verifies harness signatures before dispatch.

At no step does a worker, Hermes or CI code hold a credential that can change `main`, sign an artefact, push to the registry or deploy.

### 14.2 Defense in depth for the worker boundary (SPEC §85)

| Layer | Owner | Independent of |
|---|---|---|
| L1 Narrow MCP + policy resolution | 26 | Hermes |
| L2 Job builder (pure function, golden tests) | 26 | Hermes |
| L3 Pod Security Admission `restricted` | 37 (k8s) | Runner |
| L4 VAP `sc-agent-jobs-*`, `sc-untrusted-sa-no-mutation`, `sc-zero-rights-subjects` | 37 (k8s) | Runner, PSA |
| L5 RBAC (Runner limited; worker none) | 37/54 (k8s) | Runner code |
| L6 NetworkPolicy default-deny + allow-list | 36 (k8s) | admission |
| L7 Credential design (per-execution, scoped, revocable; no API tokens to workers) | 26/35/53 | admission |
| L8 Result judgement (protected paths, ref audit, secret scan, diff rules) | 26/22 | worker claims |
| L9 Trusted CI evidence + merge policy + human merge | 20/22/55 | worker, Runner |

### 14.3 Threat → controls → residual

| Threat | Main attack paths | Controls (≥ 2 independent, 34-R-1) | Residual |
|---|---|---|---|
| T-INJ | repo files instruct the harness to exfiltrate, push elsewhere, edit tests or CI config, call tools; issue text instructs Hermes to start costly work | harness ignores repo config (28); egress-proxy per-task allow-list (36, ADR-0150); per-execution deploy key on one repo (ADR-0007); protected paths + test-tamper detection (22); secret scan (26/35); budgets (33); human merge (ADR-0019); Hermes not trusted for enforcement (ADR-0003); handoffs labelled untrusted in context packets (25) | exfiltration of the repository's own content via allowed endpoints (R-9); budget waste (R-16) |
| T-MIS | weakened tests, skipped tests, wrong code | RED-before-GREEN state machine (29); test manifest and tamper detection (22); review role + human merge | quality, not security |
| T-WRK | container escape; reach other namespaces; steal other executions' credentials; persist | PSA restricted + VAP (37); RuntimeDefault seccomp, no caps, read-only root FS, non-root; no SA token; NetworkPolicy; per-execution Secret names enforced (`ADM-15`); deadlines; no PVCs | kernel exploits (R-2) |
| T-HER | start/cancel arbitrary tasks; spend; social-engineer humans; read conversation history | narrow MCP; Runner policy and gates; orchestration budget and rate limits; no RBAC; netpol; managed scope; human merge | junk tasks/PRs and budget burn within limits (R-16) |
| T-CI | forge test results; attack Woodpecker agent; exfiltrate secrets | no secrets in verify pool (ADR-0008); Runner verifies pipeline identity/SHA/config; protected paths; PSA/VAP; netpol; review sees evidence and diff | evidence forgery by code under test (R-1) |
| T-SUP | malicious upstream image/chart/module; malicious package fetched during trusted build | digest pins; Renovate PRs, no automerge, human review (23); vuln scan gate (23); SBOM + provenance; upstream signature verification where published (23); least privilege per component; netpol limits egress | malicious code that passes review and scanning (R-14) |
| T-LAP | plaintext `bootstrap.env`, admin kubeconfig, SSH key, leftover `recovery-identity.txt` | bundle passphrase-encrypted; kubeconfig usable only through SSH tunnel (needs SSH key); PF-23 warns on recovery identity; fresh-install requires full-disk encryption and moving `bootstrap.env` secrets into the bundle after transfer (35, 50) | full compromise if the laptop is unlocked and the SSH key is unprotected (R-11) |
| T-TOK | see §14.5 | per-class scope, TTL, revocation, alerts | per class |
| T-OPS | merging a dangerous PR; disabling a policy; deleting recovery data; misconfiguring protection | PR + CI + review for every change; `security-review` label on boundary changes (§7); prune protection and destroy guards (49); forge drift probe (16); `sc-namespace-guard`; no automated deletion of recovery data (35-R-10) | a determined admin can do anything (by design) |
| T-PRV | S3 provider reads backups; Cloudflare reads traffic; GitHub/provider coercion; model provider sees code | client-side encryption for state (06), bundle (35), repo bundles (44); SOPS ciphertexts on GitHub; etcd snapshots need the k3s token; direct-ingress alternative | DB and Longhorn backups readable by the S3 provider (R-7); Cloudflare plaintext (R-8); model providers see prompts and code (R-10) |
| T-NET | sniff/inject between nodes or to providers | WireGuard between nodes (ADR-0011); TLS to every external endpoint; Gateway verifies TLS (53); SSH host-key pinning (Flux known_hosts, workers) | — |

### 14.4 Specific attack scenarios and their answers

| ID | Scenario | Answer |
|---|---|---|
| T-STATUS | Any holder of a Forgejo token with `write:repository` (`sc-renovate`, `sc-release`, `sc-runner`) posts `strange-company/tdd-provenance=success` on a PR head | Forgejo cannot restrict who posts a status context. **Control (CD-07; watcher in the Runner, owned by plan 55):** every `strange-company/*` status context whose Forgejo `creator` is not `sc-runner` is detected; the Runner reposts the correct status, raises `SCProvenanceStatusForged` (catalogue, critical) and opens a merge audit. Merge-time defence: `SCTddProvenanceBypass` (catalogue) after the fact. Residual R-5. |
| T-TAG | A worker deploy key pushes a tag, which the push mirror copies to GitHub (Forgejo always mirrors tags, plan 17 §13.3) | Forgejo protected-tag rule `*` (plan 16/17; deploy-key coverage is version-sensitive, 17-AC-4); Runner ref audit treats any tag created in the execution window as `POLICY_VIOLATION` and deletes it; the next mirror sync prunes it from GitHub; black start never uses tags unless the operator selects one that is an ancestor of `main` (plan 45); build pool and `sc-release` accept only protected `v*` tags whose commit is on `main` (ADR-0502). Residual R-6. |
| T-BP | `sc-runner` (repo admin, needed for deploy keys) edits branch protection | Runner code never calls protection endpoints (plan 26 test); forge drift probe every 5 min (`SCForgeConfigDrift`, critical); token stored only in `strange-company`. Residual R-4. |
| T-HOOK | Repository content executes during clone (git hooks, filters, LFS) in a worker or CI | Forgejo server-side hooks disabled; `sc-worker` clones with `core.hooksPath=/dev/null`, `protocol.allow` limited to `ssh`, no submodule recursion by default (plan 27, interface); CI clone plugin settings (plan 20). Hooks in repo files cannot run because no component executes them outside the sandbox. |
| T-PROXY | A worker uses the Gateway as an open proxy | upstream host only from policy; absolute-URI and Host overrides rejected; redirects not followed (53 §13) |
| T-ADMNS | A compromised Runner creates privileged pods in its own namespace | RBAC has no pod create anywhere; `sc-runner-scope`; PSA restricted in `strange-company` |
| T-KEYS | Theft of `flux-system/sops-age` (readable by every upstream operator with cluster-wide Secret read, R-22) | yields tier-1 secrets only (in-cluster credentials); off-cluster authority is in tier 2 and release credentials in tier 1b under `SC-AGE-CI`, neither encrypted to the cluster key (35-R-3, review SEC-4); 37-AC-30 pins the list of readers; rotation procedure |
| T-RELEASE-LANE | A prompt-injected worker pushes `release/x` or a `v*` tag with its own release config and Containerfiles to make the build lane sign worker-authored content (review SEC-1) | Forgejo rejects deploy-key creation of `release/*` and protected-tag pushes (23-AC-20, P11 fails otherwise); `sc-ci-config` serves tag release config from `main` and requires reachability from `main` (plan 20); `sc-release` self-check requires a protected tag **pushed by an allowed identity**; Runner `release-provenance` status on promotion PRs (SEC-11); human merge |
| T-KEYCOPY | Additional in-cluster copies of the cluster age key (proposed `dr-tests/blackstart-age` for plan 44 RT-6) | rejected (plan 35 §10.3): the only in-cluster copy stays `flux-system/sops-age`; decryptability is proven by Flux readiness + `secrets-lint` S-13 and by the quarterly offline attestation |
| T-DEP-BUILD | A malicious npm/Go dependency executes during the trusted build | build step pod has no credentials (signing happens in a separate plugin pod); egress limited; the output image is still signed → caught only by review/scan (R-14) |

### 14.5 Leaked-token analysis (T-TOK)

| Class (group) | If leaked, the holder can | Bounded by | First response |
|---|---|---|---|
| Worker credentials (`SC-WORKER-TASK-TOKEN`, `SC-WORKER-DEPLOY-KEY`, `SC-GATEWAY-TOKEN/worker`) | post one result; push unprotected refs of one repo; spend within the execution budget | TTL ≤ deadline + 15 min; revocation at end; budget; ref audit | cancel execution (revokes all) |
| `SC-GATEWAY-TOKEN/hermes`, `SC-RUNNER-HERMES`, `SC-HERMES-API` | act as Hermes: spend orchestration budget; start/cancel tasks | budgets, rate limits, human merge; 30-day rotation | force rotation; e-stop `all` if spend anomalous |
| `SC-RUNNER-SIGNING` | mint task tokens; gateway tokens additionally need DB registration (ADR-0451) | DB registration; JWKS | e-stop `workers`; rotate without overlap |
| `SC-RUNNER-ADMIN` | human-only transitions, e-stop, quarantine | needs port-forward (cluster credentials) | rotate; review `SCRunnerAdminAccess` |
| `SC-MODEL-API-KEYS` | spend money directly at the provider | provider-side limits only | e-stop; revoke at provider |
| `SC-FORGEJO-BOT-TOKENS/sc-runner` | PRs, statuses, labels, deploy keys, protection edits | drift probe; human merge still required | delete token; re-verify statuses |
| `SC-RENOVATE`, `SC-FORGEJO-BOT-TOKENS/sc-release` | branches/PRs; status forging (T-STATUS) | human merge; status watcher | delete token; close PRs |
| `SC-RELEASE-SIGNING` | sign arbitrary images | deployment still needs a human-merged digest PR | revoke key (`trusted-keys.yaml`), re-sign |
| `SC-REGISTRY-PUSH` | push/overwrite tags in the registry | deployments use digests + signatures | delete token; audit packages |
| `SC-AGE-CLUSTER` | decrypt tier-1 secrets from Git history | tier-2 separation | rotate key and every tier-1 value |
| `SC-AGE-RECOVERY` + `SC-RECOVERY-PASSPHRASE` | everything off-cluster | — | full rotation (`credential-compromise.md#recovery-material`) |
| `SC-K3S-ADMIN-KUBECONFIG`, `SC-BREAK-GLASS-CLUSTER`, `SC-SSH-OPERATOR` | cluster/node admin | API only through SSH; firewall | rotate client CA / keys; treat nodes as suspect |
| `SC-INFRA-PROVIDER`, `SC-DNS-API`, `SC-OBJECT-STORAGE/admin` | create/destroy paid resources; hijack DNS; read/delete backups | versioning/Object Lock on buckets; provider audit | revoke at provider |
| `SC-GITHUB-MIRROR` | overwrite tags / create refs on the GitHub seed | GitHub ruleset blocks history rewrite on `main`/`release/*` (plan 17) | revoke; compare refs; force mirror |
| `SC-WOODPECKER-AGENT` (per-agent `ci`/`build`, plan 20) | register a rogue agent declaring `sc-lane=build` and receive release pipelines (R-21) | per-agent revocation; `sc-ci-maint` mismatch detection | delete the agent registration; rotate release credentials |

Per-class detail (creator, storage, rotation, compromise actions) is authoritative in the register (plan 35 §10.2).

### 14.6 Incident response

Every security incident follows: **detect** (alert, §21) → **contain** (emergency stop `workers`/`all` via `SC-RUNNER-ADMIN`, revoke the credential at its issuer, quarantine the execution or branch) → **eradicate** (rotate per register; rebuild nodes from Git when host compromise is suspected) → **recover** (restore from backups if data was altered; plan 45) → **record** (evidence objects in S3 `evidence/incidents/<id>/`, timeline in an issue labelled `security-incident`) → **learn** (a PR adding a test or control; residual-risk register update). Procedures: `credential-compromise.md` (credential-centric), `break-glass-access.md`, `quarantine-worker.md`, `budget-emergency-stop.md`, `signing-key-rotation.md#emergency-revocation`, `total-cluster-loss.md`.

### 14.7 Planned hardening (post-verification, non-blocking)

- **H-1** Worker and CI pods with `hostUsers: false` (user namespaces) once 23-AC-6 shows the pinned k3s/containerd/kernel support it; plan 26 builder + 37 param (`requireUserNamespaces`) flip together. Reduces R-2.
- **H-2** Admission-time signature verification (post-MVP; plan 23 §14.7).
- **H-3** FQDN-based egress for workers if plan 36's approximation proves too broad (Cilium revisit, ADR-0011).

### 14.8 Accepted residual-risk register

| ID | Risk | Why accepted | Compensating controls | Owner plan | Review trigger |
|---|---|---|---|---|---|
| R-1 | Code under test forges its own RED/GREEN output (SI-07) | inherent to running untrusted code | protected paths, base-revision config, immutable test manifest, structured classification, reviewer sees evidence + diff, human merge (ADR-0008) | 22 | any verifier change |
| R-2 | Kernel/container-runtime escape from worker or CI pods | no VM isolation in MVP | seccomp RuntimeDefault, non-root, no caps, read-only root, no SA token, node updates (08), H-1 | 37 | H-1 verification; CVE in runc/containerd/kernel |
| R-3 | Flux controllers hold cluster-admin | upstream design | read-only Git source; only reviewed `main`; drift detection | 18 | Flux upgrade |
| R-4 | `sc-runner` is repo admin and could change branch protection | Forgejo needs admin for deploy keys (R-16-1) | code never calls protection endpoints; drift probe every 5 min (critical) | 16/55 | Forgejo adds deploy-key scope |
| R-5 | Any `write:repository` token can post `strange-company/*` statuses | Forgejo has no per-context status ACL | T-STATUS watcher, `SCProvenanceStatusForged`, `SCTddProvenanceBypass`, human merge | 55/26 | Forgejo feature change |
| R-6 | A rogue tag pushed by a worker deploy key reaches the private GitHub mirror for up to one mirror interval | Forgejo always mirrors tags; protected-tag coverage of deploy keys is version-sensitive | protected tags `*`; Runner ref audit deletes the tag; next sync prunes GitHub; black start ignores tags; `sc-release` accepts only protected tags on `main` | 17/26 | 17-AC-4 result |
| R-7 | S3 provider can read CNPG and Longhorn backups (private code, DB contents, and the release credentials stored as Woodpecker secrets in `woodpecker-pg`, review SEC-3) and etcd snapshots (Pod specs are not covered by secrets-encryption; relevant only if 23-AC-3 shows inlined secret env, which triggers the Secret-mount switch) | barman-cloud and Longhorn rely on server-side encryption | scoped credentials; versioning; repo bundles client-side encrypted (`SC-AGE-BACKUP`); state and bundle encrypted | 14/44 | provider change; post-MVP client-side encryption |
| R-8 | Cloudflare terminates public TLS and sees plaintext in tunnel mode | default edge for minimal exposure (ADR-0010) | direct-ingress alternative; internal TLS behind cloudflared | 09 | operator preference |
| R-9 | Workers **and untrusted CI**, by default, can exfiltrate repository content: the default `agent-base` profile includes writable endpoints (npm, crates.io publish, storage.googleapis.com), `ci-base` allows pushes to public registries with attacker credentials, and DNS tunnelling through CoreDNS works from every namespace (review SEC-17). SPEC §105 "bypass network policy" is read as "cannot reach unapproved services", not "cannot exfiltrate" | workers legitimately hold the repository | egress-proxy allow-lists; no other repositories; no secrets; read-only mirrors considered post-MVP | 36 | per-repo confidentiality policy |
| R-10 | Model providers receive prompts and code | inherent to using hosted models | provider allowlist per repository (31); no secrets in prompts (scrubbing) | 31/53 | — |
| R-11 | Operator laptop compromise while unlocked exposes `bootstrap.env` and the admin kubeconfig | operator must hold authority | full-disk encryption required; bundle-held credentials; SSH-only API access; PF-23; rotation procedures | 50 | — |
| R-12 | Grafana break-glass credential stored in-cluster | Grafana rebuilt from Git (ADR-0251) | Grafana has no write path to production; `SCGrafanaLocalLogin` | 15 | — |
| R-13 | Cloudflare tunnel token theft lets an attacker attach a connector | token needed in-cluster | rotation; connector inventory check (09) | 09 | — |
| R-14 | Malicious dependency executes in a trusted build and its output is signed | builds must fetch dependencies | pins, Renovate review, vuln gate, SBOM, provenance, human-merged promotion | 23 | supply-chain incident |
| R-15 | Woodpecker server compromise can schedule arbitrary pipelines on the build pool | Woodpecker is the trusted CI (SPEC §36) | release credentials only in the `sc-release` plugin with protected-ref self-check; VAP `sc-ci-build-release-steps`; human-merged promotion | 20/23 | — |
| R-16 | Prompt-injected Hermes creates junk tasks/PRs and burns orchestration budget | Hermes is an LLM by design | budgets, rate limits, human merge, e-stop | 24/33 | — |
| R-17 | Deploy keys cannot be restricted to `agent/*` (SI-06) | Forgejo limitation | branch protection for protected refs; ref audit + quarantine | 26 | Forgejo feature |
| R-18 | Runner admin API reachable by anyone who can port-forward | equals cluster-admin already | loopback only; `SCRunnerAdminAccess` | 26 | — |
| R-19 | Single operator holds all recovery material | MVP single-operator assumption | attestation every 90 d; bundle in S3 + local + operator copies | 35 | team growth |
| R-20 | NetworkPolicy cannot express FQDNs; FQDN control depends on the `egress-proxy` (ADR-0150), a critical-path component that fails closed | ADR-0011 (no Cilium in MVP) | proxy default-deny, DNS-rebinding defence, provider APIs forbidden; Gateway as only model egress | 36 | H-3 |
| R-21 | Woodpecker does not bind agent labels to agent tokens (plan 20 Q-20-09): a leaked ci-lane agent token can register an agent that claims `sc-lane=build` and receives protected release workflows with their secrets | Woodpecker limitation | per-agent tokens (`SC-WOODPECKER-AGENT/ci`, `/build`) allow individual revocation and attribution; `sc-ci-maint` detects a build-lane workflow run by any agent other than the registered build agent (`BUILD_LANE_AGENT_MISMATCH`), which must page as security alert `SCCIBuildLaneAgentMismatch` (critical; plan 42 to add) rather than `SCCIMaintFailed` (review SEC-8); the preventive control is that Woodpecker gRPC 9000 is reachable only from the two agent pods and never routed by ingress (plans 36/20 test it). Detection plus network prevention | 20 | Woodpecker adds server-side label binding |
| R-22 | Upstream operators with cluster-wide Secret read (cert-manager, CNPG, Longhorn, Flux, possibly prometheus-operator) can read every in-cluster Secret, including `flux-system/sops-age` (review SEC-4) | upstream chart design | tier 1b/2 not encrypted to the cluster key; 37-AC-30 allowlist of readers; RBAC diff at every upstream upgrade (§19) | 18/37 | upstream RBAC change |
| R-23 | `sc-woodpecker`'s repo-admin Forgejo OAuth token is held by Woodpecker and injected into clone steps (plan 20 R-20-1, review SEC-20) | Woodpecker design | only the digest-pinned clone image receives netrc; plan 20 adds a P08 AC that the token is absent from the workspace and later steps' environment | 20 | Woodpecker change |
| R-24 | Release credentials stored as Woodpecker secrets live in `woodpecker-pg` (encryption at rest unverified) and its backups (review SEC-3) | Woodpecker secret store | events `[tag]`, image filter, exclusive config extension, ADM-46; 23-AC-3 decides Secret-mount fallback | 23/20 | 23-AC-3 result |
| R-25 | Pod-to-node traffic is stopped by kube-router NetworkPolicy and, after plan 08's change, by the host firewall's port list; until plan 08 narrows the pod-CIDR accept, NetworkPolicy is the single control (review SEC-5) | interim | plan 36 tests every node IP port from every untrusted namespace; plan 08 drops 22/2379/2380 from the pod-CIDR accept | 08/36 | plan 08 change merged |
| R-26 | `dr-tests` is credential-rich (backup age key, read token over all repositories, mirror read token, S3 readers) and processes restored, attacker-influenced data with Internet egress (review SEC-23) | restore tests need these | route `dr-tests` through the egress proxy with an S3 + api.github.com profile (plan 36); no cluster RBAC beyond `rt-*` (ADM-60) | 36/44 | — |
| R-27 | Concurrent CI pipelines in `ci-jobs` can reach each other (plan 36 CI-1, review SEC-25) | Woodpecker pod labels per pipeline unverified | restrict CI-1 to the same pipeline label if Woodpecker sets one (verify) | 36/20 | — |
| R-28 | Forgejo `sc-admin` is a machine account that a human operator can also use (dual use; SPEC §21, SI-46, review AUTH-9) | bootstrap and forge automation need a site-admin identity before SSO exists | the seed password is retired at `forge finalize`; the steady value lives only in the bundle; human administration uses personal accounts and break-glass a distinct audited identity; interactive `sc-admin` logins are treated as break-glass-class events (plan 15 probe) | 15/16 | Forgejo scoped admin tokens |

## 15. Failure modes

| Failure (security event) | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|
| Worker policy violation (protected path, foreign ref, test tamper, secret in result) | Runner (`SCWorkerPolicyViolation`) | execution rejected | revoke credentials, quarantine branch | `quarantine-worker.md` |
| Admission denial spike | `SCAdmissionDenialSpike` | blocked attempts or misconfiguration | none | `credential-compromise.md#cluster-and-node` if unexplained; otherwise fix PR |
| Break-glass used | `SCBreakGlassUsed`, `SCK3sBreakGlassAuditSeen`, `SCGrafanaLocalLogin` | — | notify | `break-glass-access.md` |
| Orphaned deploy key | `SCRunnerOrphanDeployKeys` | standing write credential | sweeper retries | `quarantine-worker.md` §manual |
| Forged status | `SCProvenanceStatusForged` (plan 55) | merge gate attacked | Runner reposts the correct status and opens a merge audit | `credential-compromise.md#identity-and-forge` |
| Provenance bypass at merge | `SCTddProvenanceBypass` | unverified code on `main` | none | `credential-compromise.md` + revert PR |
| Secret in Git history | `SCSecretScanHit` | credential exposure | none | `credential-compromise.md` |
| Forge config drift | `SCForgeConfigDrift` (plan 16) | merge policy weakened | none | `forge apply` after review |
| Unsigned or unverified internal image running | `SCUnsignedImageRunning` (plan 23) | supply-chain breach | none | `signing-key-rotation.md` |
| Model mismatch / anomalous spend | `SCModelMismatch`, `SCAnomalousSpend` | money, policy bypass | e-stop thresholds (33) | `budget-emergency-stop.md` |
| Telemetry leak | `SCTelemetryForbiddenAttributeDropped` | content leak attempt | redaction | plan 40 procedure |

## 16. Retry behavior

N/A — security controls are deterministic gates; retries belong to the components (e.g. Runner infra retries never retry policy violations: 0 automatic retries, plan 26 §16).

## 17. Idempotency behavior

N/A at this level; every contributing control states its own idempotency (plans 26, 35, 37).

## 18. Bootstrap behavior

Security controls are active before the workloads they protect: PSA defaults in k3s from configure (07); `policies` layer (VAP + NetworkPolicy) at seed step 3 before platform workloads (19); recovery bundle confirmed before paid resources (50/35); branch protection `bootstrap` stage at forge-init, `steady` stage at P12 (16); break-glass drill before P12 (15).

## 19. Upgrade behavior

Any PR touching a trust boundary (paths listed in §7) carries the `security-review` label and requires an `sc-admins` approval; upgrades of components with cluster-wide RBAC (Flux, CNPG, Longhorn, cert-manager, kube-prometheus-stack) include an RBAC diff in the PR description generated by `sc-gitops rbac-diff` (plan 18/48 interface).

## 20. Recovery behavior

After a suspected host or cluster compromise, the preferred recovery is a rebuild from Git (black start, plan 45) with full credential rotation (`credential-compromise.md#recovery-material`), not in-place cleaning. etcd snapshots taken after the suspected compromise time are not restored.

## 21. Observability

Security alerts (all `category: security` in the catalogue; plan 42 owns rendering). All names below are catalogue `alertname`s (CD-01).

| Alert | Status | Signal owner | Severity |
|---|---|---|---|
| `SCBreakGlassUsed`, `SCK3sBreakGlassAuditSeen`, `SCGrafanaLocalLogin`, `SCBreakGlassTestOverdue` | catalogue | 15/41/39 | critical / warning |
| `SCRunnerAdminAccess` | plan 26 | 26 | info (notify) |
| `SCWorkerPolicyViolation`, `SCRunnerUnexplainedRefChange` | catalogue | 22/26 | warning |
| `SCRunnerOrphanDeployKeys` | catalogue | 26 | critical |
| `SCTddProvenanceBypass` | catalogue | 55 | critical |
| `SCProvenanceStatusForged` | catalogue (CD-07) | 55 | critical |
| `SCAdmissionDenialSpike`, `SCAdmissionPolicyError`, `SCAdmissionPolicyMissing`, `SCPodSecurityViolation` | catalogue | 37 | warning / critical |
| `SCSecretScanHit`, `SCSopsDecryptionFailing`, `SCSecretRotationOverdue` | catalogue | 35 | critical / critical / warning |
| `SCRecoveryBundleOutdated`, `SCRecoveryBundleVerifyFailed`, `SCRecoveryKeyAttestationOverdue` | catalogue | 35 | warning |
| `SCUnsignedImageRunning`, `SCVulnerabilityCriticalDeployed`, `SCRenovateStale` | catalogue | 23 | critical / warning / warning |
| `SCTelemetryForbiddenAttributeDropped` | catalogue | 40 | warning |
| `SCModelMismatch`, `SCAnomalousSpend` | catalogue | 53/33 | warning |
| `SCForgeConfigDrift` | plan 16 | 16 | critical (protection) |

## 22. Testing strategy

### 22.1 SPEC §105 traceability

`test/security/traceability.yaml` encodes this table; 34-AC-1 checks it mechanically.

| SPEC §105 item | Mechanisms (layers of §14.2) | k3d test (F12) | Live test (C02) |
|---|---|---|---|
| run privileged | L2 builder; L3 PSA; L4 `ADM-10` | E1, E2, E3 | E1 |
| run as root where forbidden | L2; L3; L4 `ADM-07` | E4, E5 | E4 |
| mount hostPath | L2; L3; L4 `ADM-14`, `ADM-06` | E6, E7 | E6 |
| mount Kubernetes service tokens | L2; L4 `ADM-05`, `ADM-14`; L5 (SA has no rights anyway) | E8, E9 | E8 |
| choose arbitrary ServiceAccounts | L1 forbidden fields (26-AC-13); L4 `ADM-04`, `ADM-38` | E10, E11, E12 | E10 |
| choose arbitrary Secrets | L1; L4 `ADM-13`, `ADM-15`, `ADM-32`; L5 | E13, E14, E15, E16 | E13 |
| choose arbitrary images | L1; L2 (registry only); L4 `ADM-11`, `ADM-09`, `ADM-36`, `ADM-51` | E17, E18, E19 | E17 |
| exceed resource profiles | L1 profile allowlist; L4 `ADM-16..18`, `ADM-24/25`; quota/LimitRange (51) | E20–E24 | E20, E22 |
| enter infrastructure namespaces | L5 RBAC; L4 `ADM-41`, `-42`, `-40`, `-37`, `-21`, `-01` | E25–E30 | E25, E28 |
| bypass network policy | L6 (plan 36 suite); L5 (`ADM-41` blocks NetworkPolicy edits); host firewall port list (plan 08) | E31, E32 + 36-AC-2 including pod → **every node IP** on {22, 2379, 2380, 2381, 6443, 9100, 10250} from `agent-jobs`, `ci-jobs`, `ci-build`, `hermes` (review SEC-5) | 36-AC-11 (worker matrix incl. node-IP ports), V-12 |
| obtain signing/deployment credentials | L7 (no such credential in `agent-jobs`); L5; L4 `ADM-44/46`; ADR-0502; release-lane trust (T-RELEASE-LANE) | E33–E36, E47 (23-AC-20), E48 (23-AC-3), 37-AC-30 (review SEC-21) | E33, 23-AC-20 |

### 22.2 Other security tests (owned elsewhere, required here)

- Protected-path, pipeline-config edit and test-tamper rejection (ADR-0008, C04/22).
- Status forgery (T-STATUS) and provenance bypass (C09, 55).
- Hermes isolation: no route to Kubernetes API/providers/CNPG; forged MCP credential rejected (ADR-0003, C08).
- Gateway open-proxy and token tests (53-AC-13 et al.).
- Telemetry privacy canaries (53-AC-14, ADR-0552 H03 test).
- External port scan (V-11, V-17).
- Break-glass drill (15).
- Secret scanning and lint (35-AC-2..4).
- Supply-chain verification (23-AC-*).

### 22.3 Determinism

All tests above run without paid model APIs (fake harnesses, fake providers) [SPEC §104].

### 22.4 Independent review

A fresh-context reviewer (SPEC §132 item 44) reads SPEC, the master plan and plans 34–37, 23, 26, 15, 16, 20, 53 and writes `plans/reviews/review-security.md` with: findings (`id`, `severity` ∈ {blocking, major, minor}, `affected plans`, `recommendation`), and a final line `blocking findings: <n>`. The checklist: every trust boundary in §13.3 has its enforcement implemented by a named artefact; every threat in §14.3 meets 34-R-1; every credential reachable from an untrusted zone meets 34-R-6; every residual risk has an owner.

## 23. Acceptance criteria

### GATE-F12 (contributed)

- [ ] **34-AC-1** `make security-traceability` exits 0: every SPEC §105 item in `test/security/traceability.yaml` lists ≥ 1 F12 test ID and ≥ 1 live test ID, every listed ID exists as a test case name in `test/admission/` or plan 36's suite, and the latest F12 JUnit report shows each as passed.
- [ ] **34-AC-2** `yq` over `contracts/alert-catalog.yaml` finds every alert name of §21 marked catalogue or new, each with `category: security` (new names are added by plan 42 at consolidation; until then the check lists the missing names and fails).

### GATE-C02 (contributed)

- [ ] **34-AC-3** During a live fake execution, `kubectl -n agent-jobs get secret <execution-id> -o json | jq '.data | keys'` returns exactly `["deploy-key","gateway-token","known_hosts","task-token"]`; the Forgejo deploy key for the execution has `read_only` matching the role; the gateway token's `allowed_models` equals the routing result; after completion all three are revoked or deleted (26-AC-25 shared).
- [ ] **34-AC-4** From a worker pod (fake harness debug mode), `curl -sS --max-time 5 https://kubernetes.default.svc` and a TCP connect to `sc-pg-rw.strange-company.svc:5432` both fail, and `ls /var/run/secrets/kubernetes.io` returns "No such file or directory".

### GATE-C09 (contributed)

- [ ] **34-AC-5** T-STATUS: posting `strange-company/tdd-provenance=success` on a test PR head with the `sc-renovate` token results, within 6 minutes, in the Runner reposting the correct status on the same SHA and context, and the Runner metric `sc_forge_status_forged_total` incrementing (plan 55 owns the watcher; review SEC-22).

### GATE-C11 (contributed)

- [ ] **34-AC-9** Repeating the 34-AC-5 action makes `SCProvenanceStatusForged` active in Alertmanager within 5 minutes (the rule is loaded at C11).

### GATE-P12 (contributed)

- [ ] **34-AC-6** `plans/reviews/review-security.md` exists and ends with `blocking findings: 0` (review SEC-15).
- [ ] **34-AC-7** `./bootstrap verify --only V-11,V-12,V-13,V-17,V-26` passes (exposed surface, NetworkPolicy, admission, edge, image signatures).
- [ ] **34-AC-8** The residual-risk register (§14.8) has an owner plan for every row (`grep -c` of rows equals rows with a two-digit owner), and every owner plan file references its R-ID (`grep -l 'R-<n>' plans/<owner>-*.md`).

## 24. Explicit non-goals

- Multi-tenant isolation between untrusted customers (single-organization platform).
- VM or gVisor/Kata isolation of workers in MVP (R-2; revisit with H-1 data).
- Formal compliance frameworks (SOC 2, ISO 27001) and DLP.
- Protecting against a malicious operator holding break-glass credentials.
- Intrusion detection agents (Falco, Tetragon) in MVP.

## 25. Open questions

1. **Status-integrity watcher** (T-STATUS): resolved by CD-07 — one watcher in the Runner owned by plan 55, alert `SCProvenanceStatusForged`. Blocking: no. Owner: 55.
2. **DAG ownership** (blocking: no): plan 34 owns no node; add `"34"` to F12, C02, C09 and P12 `plans` lists. Owner: lead.
3. **Plaintext `bootstrap.env` after transfer** (blocking: no): require plan 50 to support running every post-transfer command from bundle-held credentials so the operator can delete `bootstrap.env` secrets (R-11). Owner: 50.
4. **CODEOWNERS-style required review** for `security-review` paths depends on Forgejo support for protected file patterns / code owners (verify). Fallback: the Runner refuses autonomous changes to these paths (protected paths, plan 22) and human PRs rely on the `sc-admins` reviewer convention. Owner: 55/16.
