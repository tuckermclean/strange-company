# 15 — Authentik identity, SSO and break-glass access

**Status:** Stage-0 plan (draft)
**DAG nodes:** **P07 authentik-sso-break-glass (owner)**; contributes to P09 (alerts, forward-auth, Grafana OIDC, full drill), P10 (Authentik restore test, identity-outage path), P12 (verify), C08 (Hermes dashboard OIDC)
**Related plans:** [07-k3s](07-k3s.md), [08-node-security](08-node-security.md), [09-networking](09-networking.md), [10-tls-certificates](10-tls-certificates.md), [12-cnpg](12-cnpg.md), [16-forgejo](16-forgejo.md), [17-github-mirror](17-github-mirror.md), [18-flux](18-flux.md), [19-seed-handoff](19-seed-handoff.md), [24-hermes](24-hermes.md), [34-security](34-security.md), [35-secrets](35-secrets.md), [36-network-policy](36-network-policy.md), [38-prometheus](38-prometheus.md), [39-grafana](39-grafana.md), [41-logging-tracing](41-logging-tracing.md), [42-alerting-notifications](42-alerting-notifications.md), [44-backup](44-backup.md), [45-recovery](45-recovery.md), [50-bootstrap-orchestrator](50-bootstrap-orchestrator.md)
**Contracts:** consumes `secret-classes.yaml`, `recovery-bundle.schema.yaml` (35), `alert-catalog.yaml` (42), `bootstrap-journal.schema.yaml` (50), `restore-test-result.schema.yaml` (44)
**ADRs:** relies on ADR-0005, 0006, 0009, 0010, 0012, 0014; introduces **ADR-0250** (Authentik deployment: PostgreSQL-only, Git blueprints, ephemeral bootstrap credential) and **ADR-0251** (break-glass design and audit)

## 1. Purpose

Authentik is the primary **human** identity provider [SPEC §21]. This plan defines:

1. The Authentik deployment: the official chart, CNPG `authentik-pg`, no Redis, no Bitnami. It is reconciled by Flux **after** control transfer (master plan §13, SI-05).
2. Declarative configuration through **Authentik blueprints in Git**: groups, flows, the MFA policy, OIDC providers and applications, forward-auth proxy providers, and a read-only audit service account.
3. SSO wiring for Forgejo, Grafana and the Hermes dashboard. Woodpecker logs in through Forgejo OAuth (plan 20). Forward-auth covers selected UIs without native SSO.
4. Strict separation of human and machine identity.
5. The bootstrap admin lifecycle (`sso-init`).
6. **Break-glass access** [SPEC §22] for cluster administration, Authentik, Forgejo, Grafana and bootstrap infrastructure: where each credential lives, how it is used, how use produces audit evidence and alerts, and how it is tested.
7. Authentik failure and restore, guaranteeing that failure of Authentik never makes Authentik unrecoverable.

## 2. Requirements derived from SPEC.md

1. Authentik provides primary human identity [SPEC §21].
2. Native OIDC/OAuth integration wherever supported. Consumers: Forgejo, Grafana, Hermes UI, and future internal applications [SPEC §21].
3. Applications without satisfactory native SSO MAY use Authentik forward auth through ingress [SPEC §21].
4. Human and machine identity remain separate. Automation uses scoped machine credentials [SPEC §21, §86].
5. Failure of Authentik MUST NOT make Authentik unrecoverable [SPEC §22].
6. Controlled break-glass for cluster administration, Authentik, Forgejo, Grafana and bootstrap infrastructure. Break-glass credentials are not used ordinarily, live outside the cluster, are documented, SHOULD be periodically tested, and SHOULD produce audit evidence when used [SPEC §22].
7. Authentik DB on CNPG with PITR [SPEC §25, §26, §108: RPO ≤15 min, RTO ≤4 h]. Cluster `authentik-pg` [ADR-0006].
8. No Bitnami dependencies [SPEC §20].
9. Infrastructure control interfaces are not exposed publicly without justification [SPEC §18].
10. Alerts for Authentik failure [SPEC §96]. Authentik metrics collected [SPEC §91] and dashboarded [SPEC §92].
11. Secret classes Authentik bootstrap admin and break-glass credentials are owned [SPEC §99]. A procedure exists for Authentik restore [SPEC §113]. `rotate-break-glass.md` is plan 35's.

Derived requirements:

- **15-R-1** Authentik version ≥ **2025.10** (Redis removed upstream). No Redis or valkey is deployed in any form.
- **15-R-2** All Authentik configuration that policy depends on is defined by blueprints in Git and re-applies after restore. Admin-UI changes to blueprint-managed objects are overwritten on re-apply. This is intended.
- **15-R-3** `AUTHENTIK_SECRET_KEY` is generated once pre-seed and stored SOPS-only in Git, encrypted to both age recipients (`SC-AUTHENTIK-SECRET-KEY`; no bundle copy per `secret-classes.yaml` — the offline recovery identity decrypts it from the GitHub clone). It is rotated **only on compromise** (`rotate-secrets.md#sc-authentik-secret-key`): all sessions are invalidated, and OIDC links are unaffected because `sub_mode: user_uuid` (verify on the pinned version that no DB column is encrypted with it) (AUTH-18). The chart documents "don't change this after the first install".
- **15-R-4** OIDC `sub` uses `user_uuid` mode, so subjects survive DB restore and do not depend on `SECRET_KEY` hashing. Forgejo account links are therefore stable.
- **15-R-5** Every application has an Authentik access policy (group allowlist). Apps never rely only on their own group mapping to deny access.
- **15-R-6** No machine authenticates through an Authentik user flow. Service accounts exist only for read-only audit probing.
- **15-R-7** Break-glass credentials for Authentik and Forgejo exist **only** outside the cluster: the recovery bundle (authoritative) and `secrets/offcluster/<cluster>/break-glass-*.sops.yaml`, encrypted to the **recovery recipient only**, so the cluster cannot decrypt them. Grafana's is also in Git as cluster-decryptable SOPS because Grafana is rebuilt from Git (§14.4.4).

## 3. Inputs

| Input | Source |
|---|---|
| `BASE_DOMAIN`, `OPERATOR_USERNAME`, `OPERATOR_EMAIL`, `OPERATOR_NAME`, SMTP settings (optional) | `bootstrap.env` → `bootstrap-config.schema.yaml` (plan 50, 42) |
| Pre-seed generated SOPS secrets: `SC-AUTHENTIK-SECRET-KEY`, `SC-OIDC-CLIENTS` (forgejo, grafana, hermes-dashboard), `SC-AUTHENTIK-AUDIT-TOKEN` | plan 50 secret generation |
| Bundle-staged secrets: `SC-BREAK-GLASS-AUTHENTIK`, `SC-BREAK-GLASS-CLUSTER` (SSH key pair), `SC-BREAK-GLASS-GRAFANA`, `SC-BREAK-GLASS-FORGEJO` | plan 50 operator-side staging in `/run/sc/secrets/` (tmpfs, ADR-0101, CD-08) → plan 35 recovery bundle + recovery-recipient-only `secrets/offcluster/<cluster>/` files |
| Ephemeral `SC-AUTHENTIK-BOOTSTRAP` (password + API token) | generated by `sso-init` at the start of `flux-bootstrap` (§18) |
| `authentik-pg` (Cluster, Secret `authentik-pg-app`, Service `authentik-pg-rw`) | plan 12 |
| Traefik IngressRoute CRDs, cert-manager certificate for `auth.<BASE_DOMAIN>` | plans 09, 10 |
| Blueprint files `gitops/platform/authentik/blueprints/*.yaml` | this plan (Git) |

## 4. Outputs

1. Authentik at `https://auth.<BASE_DOMAIN>` (Service `authentik/authentik-server:80→9000`; metrics 9300 on server and worker).
2. OIDC providers + applications `forgejo`, `grafana`, `hermes-dashboard`, with discovery at `https://auth.<d>/application/o/<slug>/.well-known/openid-configuration`.
3. Forward-auth: the embedded outpost serving proxy providers `prometheus` and `alertmanager`, plus the Traefik Middleware `monitoring/authentik-forwardauth`.
4. Secret `forgejo/forgejo-oidc-values` (Forgejo OIDC source values; plan 16 §9.3).
5. Groups `sc-admins`, `sc-engineers`, `sc-viewers`, `sc-breakglass`, `sc-audit`.
6. The `sso-init` outputs (AUTH-3): **no Git commit and no SOPS file**. Its only outputs are (a) the A10 journal record under phase `sso-init` (`result`, per-step outcomes A0–A10 with timestamps, `a0_at`, `operator_mfa: enrolled|deferred`, blueprint instance hashes, `bootstrap_token_sha256`, `teardown: complete|partial`), and (b) the Authentik objects A4/A5 create (`sc-breakglass`, the operator user, the synthetic verification user). `flux-bootstrap.sso-init-a0` is a step of plan 50's `flux-bootstrap` phase that records `a0_at`.
7. The **platform probes** Deployment `monitoring/platform-probes` (§21.2), which hosts break-glass, forge-drift and mirror probes.
8. Prometheus rules for Authentik and break-glass (§21.3).

## 5. Dependencies

**Hard (DAG):** P07 needs P06 (Flux sourcing Forgejo, control transfer done). The Authentik layer is not in the seed set.

**Flux ordering (plan 18 layer table is authoritative for names; AUTH-3):**
`authentik-db` (`authentik-pg` Cluster, ObjectStore, ScheduledBackup) → `authentik-upgrade-gate` (plan 48 gate Job) → `authentik` (SOPS Secrets, blueprint ConfigMap, HelmRelease, IngressRoutes; the Namespace comes from the seed `namespaces` layer and NetworkPolicies from `policies`) → `authentik-config` (readiness Job `authentik-config-check`, `wait: true`) → `authentik-sso-forgejo` (Secret `forgejo/forgejo-oidc-values`). Monitoring layers that consume OIDC depend on `authentik-config`. **`platform-probes`** (Deployment, §21.2) depends on `forgejo` only, never on an Authentik layer (AUTH-4): detection must work while Authentik is broken. Plan 18 must add the rows `authentik-config`, `authentik-sso-forgejo` and `platform-probes` to its layer table.

**Runtime:** `authentik-pg`; DNS/ingress for `auth.<d>`; SMTP optional.

**Assumed interfaces:**
- Plan 19: namespace `authentik` (Namespace object + PSA labels only, no workloads) **is in the seed set**, so the orchestrator can create the ephemeral bootstrap Secret before the source switch (§18). If plan 19 rejects this, the alternative is that the orchestrator creates the namespace with identical labels and Flux adopts it (Namespace only).
- Plan 50: an explicit hook **`sso-init --from A0` inside `flux-bootstrap`, before the source switch** (so the bootstrap Secret exists before Authentik's first start, AUTH-1/AUTH-3), recorded in the journal as step `flux-bootstrap.sso-init-a0` with its timestamp; `verify` fails if the first `authentik-server` pod start (`.status.startTime`) is earlier. `sso-init` A1–A10 run after the `authentik-config` layer is Ready. `sso-init` commits **no** outputs. CLI: `./bootstrap sso-init [--from <step>]`, and `./bootstrap exec -- sc-bootstrap breakglass drill [--phase P07|full] [--only <system>]|status`.
- Plan 08: node user `sc-breakglass` with the authorized key from `SC-BREAK-GLASS-CLUSTER` (public half committed at `infra/ansible/files/breakglass.pub`), NOPASSWD sudo, plus a PAM `pam_exec` session hook that writes `/var/lib/node_exporter/textfile/sc_breakglass.prom` (§14.4.1). Plan 38: node-exporter textfile collector reads that directory.
- Plan 07: kube-apiserver audit logging enabled to `/var/lib/rancher/k3s/server/logs/audit.log`, with a policy logging at least `Metadata` for all requests by `system:admin` and group `system:masters`, rotated per plan 07 (50 MiB × 6, 7 days). **Operator identity (AUTH-7, plans 07/50):** routine operator access uses a distinct client certificate `CN=sc-operator:<name>`, `O=sc:cluster-admins` (≤ 30 days, re-issued over SSH by `sc-bootstrap kubeconfig refresh`); `k3s.yaml` is never copied off a node. `system:admin` is then used only by break-glass (`sudo k3s kubectl` on a node), so `sc_k3s_audit_breakglass_requests_total` counts audit events with `user.username == "system:admin"` (verify that k3s internal components do not use that identity).
- Plan 09/10: in-cluster clients (Forgejo, Grafana, Hermes) reach `https://auth.<BASE_DOMAIN>` through the CoreDNS rewrite to Traefik (pod port 8443) with a certificate they trust; labs and drills with staging or internal-CA certificates mount the test root via plan 10's `trust-test-root` component (AUTH-14), never skip-verify. Plan 09 provides `./bootstrap edge-bypass` (local port-forward of Traefik 443 plus temporary `/etc/hosts` entries for every public host) as the edge-down path to every UI with its real hostname (AUTH-11).
- Plan 39: Grafana `auth.generic_oauth` per §9.4. Plan 24: Hermes dashboard OIDC per §9.5. Plan 38/42: IngressRoutes for `prometheus.<d>` and `alertmanager.<d>` use Middleware `authentik-forwardauth` (§7.2).
- Plan 39: Grafana metric `grafana_api_login_post_total` (local login form), consumed by the catalogue alert `SCGrafanaLocalLogin` (§14.4.4). Plan 07: `sc_k3s_audit_breakglass_requests_total` for `SCK3sBreakGlassAuditSeen`.

## 6. Owned state

| State | Location | Writers | Backup |
|---|---|---|---|
| Users, groups, sessions, tokens, events (audit log), OAuth grants, blueprint instances, MFA devices | CNPG `authentik-pg` (db `authentik`) | Authentik | CNPG WAL + daily base → S3 `pg-backups/authentik-pg/`; PITR (plan 12) |
| Blueprint definitions | Git `gitops/platform/authentik/blueprints/` | humans via PR | Git |
| `AUTHENTIK_SECRET_KEY` | SOPS (both recipients) | plan 50 once | Git (+ GitHub mirror) |
| Break-glass credentials | recovery bundle (plan 35) + recovery-recipient-only `secrets/offcluster/<cluster>/break-glass-*.sops.yaml` | plan 50, rotation procedure | bundle copies (local, S3 `recovery`, operator offline); Git (offcluster files) |
| Break-glass drill status | ConfigMap `monitoring/breakglass-drill-status` (not in Git) + evidence objects S3 `evidence/breakglass-drills/` | drill command | S3 versioning |
| Media | **none**. No custom icons or backgrounds; applications use no uploaded media | — | — |

Authentik event retention: `AUTHENTIK_EVENTS__CONTEXT_PROCESSORS__…` defaults, with the event expiry set by blueprint system settings to **365 days** (`event_retention: days=365`, verify setting name in the chosen version). Login evidence for break-glass is therefore kept for a year in `authentik-pg` and in its backups.

## 7. External interfaces

### 7.1 OIDC applications

| Slug | Consumer | Client type | Redirect URI(s) (strict) | Access policy (any of) | Claims |
|---|---|---|---|---|---|
| `forgejo` | Forgejo (plan 16) | confidential, `client_id: forgejo` | `https://git.<d>/user/oauth2/authentik/callback` | `sc-admins`, `sc-engineers`, `sc-viewers`; deny usernames `sc-*`, `admin`, `root` | openid, email, profile, groups |
| `grafana` | Grafana (plan 39) | confidential, `client_id: grafana` | `https://grafana.<d>/login/generic_oauth` | `sc-admins`, `sc-engineers`, `sc-viewers`, `sc-synthetic` | openid, email, profile, groups |
| `hermes-dashboard` | Hermes dashboard (plan 24) | **public + PKCE** by default (Hermes takes `HERMES_DASHBOARD_OIDC_CLIENT_ID`/`_ISSUER`/`_PUBLIC_URL` per L-1; confidential if plan 24 finds a client-secret setting, verify) | `https://hermes.<d>/<callback path per Hermes docs>` (verify) | `sc-admins`, `sc-engineers` | openid, email, profile, groups Fallback if native dashboard OIDC is absent or cannot enforce the group allowlist at C08: an Authentik forward-auth proxy provider for `hermes.<d>` restricted to `sc-admins`/`sc-engineers` (AUTH-26). |

Common provider settings: `sub_mode: user_uuid`; `issuer_mode: per_provider`; `include_claims_in_id_token: true`; signing key = the Authentik self-signed certificate (RS256); access token validity 10 min; refresh token validity 30 days; authorization flow `default-provider-authorization-implicit-consent`; invalidation flow `default-provider-invalidation-flow` (verify slug availability in the chosen version). Woodpecker has **no** Authentik application. It authenticates against Forgejo (plan 20).

**`groups` claim:** a blueprint-defined scope mapping `sc-scope-groups` (`scope_name: groups`), expression `return {"groups": [g.name for g in request.user.ak_groups.all()]}`. It is attached to every provider in addition to the managed `openid`, `email` and `profile` mappings.

### 7.2 Forward-auth

**Decision.** Exposed UIs:

| UI | Hostname | Exposed? | Access | Justification |
|---|---|---|---|---|
| Prometheus | `prometheus.<d>` | **yes**, forward-auth | `sc-admins`, `sc-engineers` + MFA | Rule, target and TSDB status pages for incident triage. Read-only: `--web.enable-admin-api` and `--web.enable-lifecycle` stay off (plan 38) |
| Alertmanager | `alertmanager.<d>` | **yes**, forward-auth | `sc-admins` only + MFA | Silence management. Restricted to admins. This is **not** the control that protects break-glass alerts: Alertmanager's API is reachable in-cluster and Grafana Admins can silence through the datasource, so break-glass alerts carry an always-notify class and any silence matching them raises `SCBreakGlassAlertSilenced` on a receiver silences cannot match (plan 42, AUTH-8) |
| Longhorn UI | — | **no** | `kubectl port-forward` via `./bootstrap shell` | Destructive powers (delete volumes, detach). SPEC §18 justification is insufficient |
| Flux UI | — | none exists (no Weave GitOps in MVP) | `flux` CLI in toolbox | — |
| Runner API, Model Gateway | — | **no** | in-cluster only | master plan §6.1 |

Mechanism:
- Proxy providers `prometheus` and `alertmanager`: `mode: forward_single`, `external_host: https://<host>`, `access_token_validity: hours=8`, assigned to the **embedded outpost** (`authentik Embedded Outpost`, managed by Authentik; the blueprint sets its `providers` list (verify the managed identifier `goauthentik.io/outposts/embedded`)).
- Traefik `Middleware monitoring/authentik-forwardauth` (owned by this plan, rendered into `monitoring`):
  ```yaml
  spec:
    forwardAuth:
      address: http://authentik-server.authentik.svc.cluster.local/outpost.goauthentik.io/auth/traefik
      trustForwardHeader: true
      authResponseHeaders: [X-authentik-username, X-authentik-groups, X-authentik-email, X-authentik-uid]
  ```
- `IngressRoute authentik/authentik` (this plan): `Host(auth.<d>)` → `authentik-server:80`, `entryPoints: [websecure, oidc]`. `oidc` is plan 09's internal entrypoint `:8444` that routes only `auth.`; in-cluster OIDC clients such as Hermes reach only it (plan 36, SEC-16).
- `IngressRoute authentik/authentik-outpost-callbacks` (this plan): rules `(Host(prometheus.<d>) || Host(alertmanager.<d>)) && PathPrefix(/outpost.goauthentik.io/)` → `authentik-server:80`. It sits in namespace `authentik`, so no cross-namespace service reference is needed.
- Plans 38/42 own `IngressRoute monitoring/prometheus` and `monitoring/alertmanager` with `middlewares: [{name: authentik-forwardauth}]`.
- Adding a new forward-auth consumer requires a blueprint proxy provider + application + policy, a callback rule, and a written justification in the consuming plan.

### 7.3 Human admin surface
`https://auth.<d>/if/admin/` for `sc-admins` (superuser group) and `sc-breakglass`. Normal identity administration (create users, assign groups, reset MFA) happens here. Group *definitions* and app policies are blueprint-managed.

### 7.4 Machine surface
Only `platform-probes` uses the Authentik API, read-only, with service account `sc-audit` (§10). No workload uses Authentik to obtain machine credentials (15-R-6).

## 8. Internal interfaces

- Authentik server and worker ↔ `authentik-pg-rw.authentik.svc:5432` (TLS `sslmode=require`).
- Blueprint discovery: ConfigMap `authentik-blueprints-sc` (kustomize `configMapGenerator` from `gitops/platform/authentik/blueprints/*.yaml`, hash suffix disabled so the chart reference is stable) mounted by the chart under `/blueprints/mounted/cm-authentik-blueprints-sc/`. Only keys ending `.yaml` are applied (chart note, verified). `!Env` tags read values from env injected by `global.envFrom` (Secret `authentik-sc-env`).
- `authentik-config-check` Job (curl image, digest-pinned): waits up to 15 min until each discovery URL in §7.1 returns 200 and `GET /api/v3/managed/blueprints/?label=sc` (token `SC-AUTHENTIK-AUDIT-TOKEN`, read permission on blueprint instances) shows every SC instance `status: successful`. The Job exits 0 when both hold. Kustomization `authentik-config` has `wait: true`, `force: true` (re-creates the Job when its spec changes).

## 9. Configuration

### 9.1 Version selection and HelmRelease values (illustrative)

Baseline: Authentik 2026.8.x with goauthentik chart 2026.8.3, chart and app versions aligned (`plans/compatibility-baseline.yaml`, CD-29/ADR-0022). Evidence: the chart has no Redis keys and its `postgresql` (Bitnami) subchart defaults to `enabled: false`. A newer minor or major is a plan-amendment task, not a silent bump. Verified by 15-AC-2 and 15-AC-34.

```yaml
# HelmRelease authentik/authentik — chart https://charts.goauthentik.io, version-pinned
global:
  image: {digest: "sha256:…"}            # ghcr.io/goauthentik/server
  envFrom: [{secretRef: {name: authentik-sc-env}}]     # OIDC client secrets, audit token (for !Env)
  env:
    - {name: AUTHENTIK_SECRET_KEY, valueFrom: {secretKeyRef: {name: authentik-secret-key, key: secret-key}}}   # key name per register (AUTH-17)
    - {name: AUTHENTIK_POSTGRESQL__HOST, value: authentik-pg-rw.authentik.svc.cluster.local}
    - {name: AUTHENTIK_POSTGRESQL__NAME, value: authentik}
    - {name: AUTHENTIK_POSTGRESQL__USER, valueFrom: {secretKeyRef: {name: authentik-pg-app, key: username}}}
    - {name: AUTHENTIK_POSTGRESQL__PASSWORD, valueFrom: {secretKeyRef: {name: authentik-pg-app, key: password}}}
    - {name: AUTHENTIK_POSTGRESQL__SSLMODE, value: require}
    - {name: AUTHENTIK_BOOTSTRAP_PASSWORD, valueFrom: {secretKeyRef: {name: authentik-bootstrap, key: password, optional: true}}}
    - {name: AUTHENTIK_BOOTSTRAP_TOKEN, valueFrom: {secretKeyRef: {name: authentik-bootstrap, key: token, optional: true}}}
    - {name: AUTHENTIK_BOOTSTRAP_EMAIL, value: "akadmin@<BASE_DOMAIN>"}
    - {name: AUTHENTIK_ERROR_REPORTING__ENABLED, value: "false"}
    - {name: AUTHENTIK_DISABLE_UPDATE_CHECK, value: "true"}
    - {name: AUTHENTIK_DISABLE_STARTUP_ANALYTICS, value: "true"}
    - {name: AUTHENTIK_LOG_LEVEL, value: info}
authentik: {secret_key: ""}                # supplied via env; chart value left empty
postgresql: {enabled: false}               # Bitnami subchart never rendered
serviceAccount: {create: false}            # no Kubernetes outpost integration, no RBAC
geoip: {enabled: false}
blueprints: {configMaps: [authentik-blueprints-sc]}
server:
  replicas: 1                               # small-ha: 2
  priorityClassName: platform
  metrics: {enabled: true, serviceMonitor: {enabled: true}}
  ingress: {enabled: false}                 # IngressRoute authentik/authentik owned by this plan (§7.2; entryPoints websecure + oidc)
  securityContext: {runAsNonRoot: true, seccompProfile: {type: RuntimeDefault}}
  containerSecurityContext: {allowPrivilegeEscalation: false, capabilities: {drop: [ALL]}}
worker:
  replicas: 1
  priorityClassName: platform
  metrics: {enabled: true, serviceMonitor: {enabled: true}}
  securityContext/containerSecurityContext: same as server
prometheus: {rules: {enabled: false}}       # rules owned by this plan/plan 42 (§21.3)
```

`optional: true` on the bootstrap env is load-bearing (§18). Verify that the chart's `global.env` passes `valueFrom` through unchanged (15-AC-3).

### 9.2 Blueprints (Git, `gitops/platform/authentik/blueprints/`)

All files carry `metadata.labels: {sc: "true"}` and use `!Env` only for secrets. Ordering between files uses `authentik_blueprints.metaapplyblueprint` entries (`required: true`) (verify model name).

| File | Contents |
|---|---|
| `05-hardening.yaml` | **AUTH-1.** Upstream flows `default-authentication-flow`, `default-enrollment-flow`, `default-recovery-flow`, `default-source-authentication`, `default-source-enrollment` and `initial-setup` are bound to expression policy `sc-deny-flow` (`return False`, `ak_message("disabled")`) with `policy_engine_mode: all` and `denied_action: message`; where upstream allows deletion of the instance-managed flow without breaking its system blueprint, `state: absent` is used instead (verify per version). The upstream default brand (`authentik-default`) is set `default: false`; brand `sc` (00-system) is `default: true`, so every request, including a port-forward with an unknown Host, uses the SC flows. Applied before every other SC file (`metaapplyblueprint` ordering) |
| `00-system.yaml` | system/brand: brand `sc` with `domain: auth.<BASE_DOMAIN>`, `default: true` (AUTH-1), `flow_authentication: sc-authentication`, `flow_invalidation: default-invalidation-flow`, `flow_recovery: sc-recovery`, `flow_user_settings: default-user-settings-flow`; event retention 365 days; no enrollment flow on the brand (no self-registration) |
| `10-groups.yaml` | groups `sc-admins` (`is_superuser: true`), `sc-engineers`, `sc-viewers`, `sc-breakglass` (`is_superuser: true`), `sc-audit` |
| `20-flows.yaml` | flow `sc-authentication`: stages `sc-identification` (user fields `username`, `email`; password stage embedded `sc-password`; no sources; `show_matched_user: false`) → `sc-mfa-validate` (`device_classes: [totp, webauthn, static]`, `not_configured_action: configure`, `configuration_stages: [default-authenticator-totp-setup, default-authenticator-webauthn-setup]`, `last_auth_threshold: seconds=0`) → `sc-login` (`session_duration: hours=12`, `remember_me_offset: seconds=0`). The MFA stage binding has policy `sc-not-breakglass` (`return not ak_is_group_member(request.user, name="sc-breakglass")`) with `re_evaluate_policies: true`, `evaluate_on_plan: false`. Flow `sc-recovery` (AUTH-1): flow policy `sc-require-flow-token` (`return bool(request.context.get("is_restored") or request.context.get("pending_user"))`, verify context keys) so it cannot run without a recovery token → prompt stage (new password ×2, policy `sc-password-policy`: length ≥ 14, zxcvbn ≥ 3, HIBP check **off** because egress is restricted) → user-write stage (`user_creation_mode: never_create`) → `sc-mfa-validate` (configure mode, same binding as above, so `sc-breakglass` is still exempt) → `sc-login` |
| `30-scope-groups.yaml` | scope mapping `sc-scope-groups` (§7.1) |
| `31-app-forgejo.yaml` | OAuth2 provider + application `forgejo` + expression policy `sc-access-forgejo`: `return any(ak_is_group_member(request.user, name=g) for g in ("sc-admins","sc-engineers","sc-viewers","sc-synthetic")) and not request.user.username.startswith("sc-") and request.user.username not in ("admin","root")`, bound to the application |
| `32-app-grafana.yaml` | provider + app `grafana` + policy `sc-access-grafana` (groups `sc-admins`, `sc-engineers`, `sc-viewers`, `sc-synthetic`) |
| `33-app-hermes.yaml` | provider + app `hermes-dashboard` + policy `sc-access-hermes` (admins, engineers) |
| `40-proxy-prometheus.yaml`, `41-proxy-alertmanager.yaml` | proxy providers + apps + policies (§7.2) |
| `45-outpost.yaml` | embedded outpost `providers: [prometheus, alertmanager]`, config `authentik_host: https://auth.<BASE_DOMAIN>` |
| `55-synthetic.yaml` | group `sc-synthetic` (no superuser, admitted by the `forgejo` policy (Forgejo team `viewers`, plan 16 `groupTeamMap`), admitted by `sc-access-grafana` and mapped to Grafana **Viewer** by the role expression (§9.4, plan 39); not admitted to `hermes-dashboard`). The user itself is created by `sso-init` A5b, because it carries a password and TOTP seed |
| `50-audit.yaml` | user `sc-audit` (`type: service_account`, path `sc/service-accounts`), group `sc-audit`, RBAC role `sc-audit-readonly` with permissions `authentik_core.view_user`, `authentik_events.view_event`, `authentik_blueprints.view_blueprintinstance` assigned to group `sc-audit`; token `sc-audit-token` (`intent: api`, `expiring: false`, `key: !Env AUTHENTIK_SC_AUDIT_TOKEN`) (verify: token model supports `key` in blueprints, which the upstream bootstrap blueprint relies on) |

Blueprints never create human users or the break-glass user. Those carry secrets that must not be in Git (§14.2). The one blueprint-created non-human user is the DR marker `sc-dr-marker` (`50-audit.yaml`: `type: internal`, `is_active: false`, no password, path `sc/markers`), which plan 44's `rt-app-authentik` looks for after a scratch restore.

### 9.3 Knobs

| Key | Default | Owner |
|---|---|---|
| `authentik.version` | per §9.1 rule | 15 |
| `authentik.server.replicas` / `worker.replicas` | 1/1 minimum; 2/1 small-ha | 15, 47 |
| `authentik.mfa.required` | `true` (humans) | 15; changing it is a security-review PR (plan 34) |
| `authentik.forwardAuth.apps` | `[prometheus, alertmanager]` | 15 |
| `authentik.sessionHours` | 12 | 15 |
| `authentik.smtp` | from plan 42 transport if SMTP configured | 42 |

### 9.4 Grafana OIDC (plan 39 applies; normative mapping)
`GF_AUTH_GENERIC_OAUTH_ENABLED=true`, `GF_AUTH_GENERIC_OAUTH_NAME=Authentik`, `CLIENT_ID=grafana`, `CLIENT_SECRET` from `SC-OIDC-CLIENTS/grafana`, `SCOPES="openid email profile groups"`, `AUTH_URL=https://auth.<d>/application/o/authorize/`, `TOKEN_URL=https://auth.<d>/application/o/token/`, `API_URL=https://auth.<d>/application/o/userinfo/`, `LOGIN_ATTRIBUTE_PATH=preferred_username`, `ROLE_ATTRIBUTE_PATH="contains(groups[*], 'sc-admins') && 'Admin' || contains(groups[*], 'sc-engineers') && 'Editor' || contains(groups[*], 'sc-viewers') && 'Viewer' || contains(groups[*], 'sc-synthetic') && 'Viewer'"`, `ROLE_ATTRIBUTE_STRICT=true`, `ALLOW_ASSIGN_GRAFANA_ADMIN=false`, `GF_AUTH_OAUTH_AUTO_LOGIN=true`, `GF_AUTH_DISABLE_LOGIN_FORM=false` (local form reachable at `/login?disableAutoLogin=true`, verify), `GF_AUTH_SIGNOUT_REDIRECT_URL=https://auth.<d>/application/o/grafana/end-session/`, `GF_SECURITY_ADMIN_USER=sc-breakglass`, password from `SC-GRAFANA-ADMIN`.

### 9.5 Hermes dashboard (plan 24 applies)
`HERMES_DASHBOARD_OIDC_ISSUER=https://auth.<d>/application/o/hermes-dashboard/`, `HERMES_DASHBOARD_OIDC_CLIENT_ID=hermes-dashboard`, `HERMES_DASHBOARD_PUBLIC_URL=https://hermes.<d>`. These go through managed scope (ADR-0003).

## 10. Secrets

| Class | Status | Creator | Consumer | Storage | Rotation | Recovery |
|---|---|---|---|---|---|---|
| `SC-AUTHENTIK-BOOTSTRAP` (akadmin password + bootstrap API token) | master §9 | `sso-init` (toolbox, random 40 chars / 64 hex) | Authentik first start (env), `sso-init` | **ephemeral**: imperative Secret `authentik/authentik-bootstrap` + operator-side `/run/sc/secrets/authentik-bootstrap*` (tmpfs, CD-08) until `sso-init` completes, then destroyed (§14.2 A9) | not rotated; single use | not needed after A9; if lost mid-bootstrap see `restore-authentik.md` §Bootstrap recovery |
| `SC-AUTHENTIK-SECRET-KEY` | **new** | plan 50 pre-seed (50 random bytes, base64) | Authentik | SOPS `gitops/clusters/<c>/authentik/authentik-secret-key.sops.yaml` → Secret `authentik/authentik-secret-key` (key `secret-key`); no bundle copy | **never** (15-R-3) | Git |
| `SC-OIDC-CLIENTS` (forgejo, grafana, hermes if confidential) | master §9 | plan 50 pre-seed | Authentik (`!Env` via `authentik-sc-env`), Forgejo (`forgejo-oidc-client`), Grafana | SOPS, same value in two files: Authentik side `gitops/clusters/<c>/authentik/authentik-sc-env.sops.yaml`, consumer side `gitops/clusters/<c>/secrets/<ns>/oidc-<app>.sops.yaml` | PR changes both files in one commit. Authentik re-applies the blueprint, the app restarts, and logins fail between the two applies (< 1 reconcile interval) | Git |
| `SC-AUTHENTIK-AUDIT-TOKEN` | **new** | plan 50 pre-seed (64 hex) | Authentik blueprint (`!Env`), `platform-probes`, `authentik-config-check` | SOPS `gitops/clusters/<c>/authentik/authentik-sc-env.sops.yaml` and `gitops/clusters/<c>/secrets/monitoring/platform-probes-authentik.sops.yaml` | PR | Git |
| `SC-BREAK-GLASS-AUTHENTIK` (`sc-breakglass` password) | master §9 | plan 50 staging; user created by `sso-init` A4 | humans, emergencies | bundle (authoritative) + `secrets/offcluster/<c>/break-glass-authentik.sops.yaml` (recovery recipient only) | plan 35 `rotate-break-glass.md` (mechanics: `break-glass-access.md` §Rotate) | bundle; if lost: recovery key via cluster break-glass |
| `SC-BREAK-GLASS-CLUSTER` (ed25519 SSH key for node user `sc-breakglass`) | master §9 | plan 50 staging; public key committed for plan 08 | humans | bundle (authoritative, private key) + `secrets/offcluster/<c>/break-glass-cluster.sops.yaml` (recovery recipient only); public key on nodes | plan 35 | bundle; if lost: provider console (`SC-BREAK-GLASS-INFRA`) |
| `SC-BREAK-GLASS-FORGEJO` | master §9 | plan 50 / plan 16 S03 | humans | bundle (authoritative) + `secrets/offcluster/<c>/break-glass-forgejo.sops.yaml` (recovery recipient only) | plan 35 | via `sc-admin` API reset |
| `SC-BREAK-GLASS-GRAFANA` | master §9 | plan 50 | Grafana built-in admin (`sc-breakglass`) | bundle **and** SOPS `gitops/clusters/<c>/secrets/monitoring/grafana-admin.sops.yaml` (`SC-GRAFANA-ADMIN`, same value, because Grafana is rebuilt from Git) | PR + bundle update | Git/bundle |
| `SC-BREAK-GLASS-INFRA` (provider account console login with MFA recovery codes) | master §9 | operator | humans | **operator-offline** only (not in the bundle). The rest of the infrastructure toolkit is the separately bundled `SC-INFRA-PROVIDER`, `SC-DNS-API`, `SC-DNS-TUNNEL`, `SC-OBJECT-STORAGE/admin` and `SC-TOFU-ENCRYPTION` | plan 35 | operator |
| `SC-SYNTHETIC-USER` (**new**; password + TOTP seed of `synthetic-verifier`) | this plan | plan 50 pre-seed | toolbox checks (`verify`, `sc-dashcheck`, drills) | bundle + `secrets/offcluster/<c>/synthetic-user.sops.yaml` (recovery recipient only); never a cluster Secret | yearly or on exposure: new seed via the admin channel + `refresh-bundle` | bundle |
| `SC-PG-APP` (`authentik-pg-app`) | master §9 (owner plan 12) | plan 50 pre-seed | Authentik, CNPG bootstrap | SOPS `gitops/clusters/<c>/secrets/authentik/authentik-pg-app.sops.yaml` (12-R-5) | plan 12 | Git |

**Human/machine separation [SPEC §21]:** human accounts authenticate interactively with password + MFA. `sc-audit` is a `service_account` with no password and no flow access (service accounts cannot log in interactively), and holds only view permissions. No other machine identity exists in Authentik. Machines use Forgejo bot tokens, deploy keys, Runner task tokens and gateway tokens (plans 16, 26, 53). Hermes authenticates humans through the dashboard OIDC client only and holds **no** Forgejo account (CD-15).

## 11. Storage

All durable state is in `authentik-pg` (`local-path` PGDATA, plan 12). Authentik pods use `emptyDir` only. **Connection budget:** Authentik ≥ 2025.10 uses roughly 50% more PostgreSQL connections than with Redis (upstream release note), because cache, tasks and websockets now run on PostgreSQL. Requirement for plan 12: `authentik-pg` `max_connections ≥ 150`, and `shared_buffers` sized per plan 12's memory budget.

## 12. Resource requirements

| Pod | minimum req / limit | small-ha |
|---|---|---|
| authentik-server | cpu 100m; mem 448Mi / 1Gi | ×2 replicas, same |
| authentik-worker | cpu 50m; mem 384Mi / 1Gi | ×1 (2 if task backlog alerts) |
| authentik-pg | plan 12 (proposed 256Mi / 768Mi) | ×3 |
| authentik-config-check Job | 10m / 32Mi, runs to completion | same |
| platform-probes | cpu 10m; mem 48Mi / 128Mi | same |

Values are starting points. Plan 51's capacity gate measures the working set (ADR-0020).

## 13. Security boundary

- Namespace `authentik`, PSA `restricted`, tier `platform`. Pods run non-root with capabilities dropped. `serviceAccount.create: false` and `automountServiceAccountToken: false`. Authentik gets **no** Kubernetes API access (the embedded outpost needs none).
- **NetworkPolicy (plan 36 renders):**

  | Dir | Peer | Port |
  |---|---|---|
  | in | `ingress` ns (Traefik) | 9000 |
  | in | `monitoring` (Prometheus) | 9300 |
  | in | `monitoring` (`platform-probes`), `authentik` (config-check Job) | 9000 |
  | out | `authentik-pg` pods | 5432 |
  | out | kube-dns | 53 |
  | out | SMTP host (if configured) | 465/587 |
  | out | `ingress` (Traefik pods) | 8443 (post-DNAT pod port; unconditional, plan 36 I-10, AUTH-20) |
- Traefik Middleware: forward-auth answers are trusted only from the in-cluster address. `trustForwardHeader` applies only to Traefik's own entrypoint headers, and the Cloudflare tunnel sets `X-Forwarded-*` (plan 09).
- **Admin-surface hardening:** superuser membership is limited to `sc-admins` and `sc-breakglass`. `akadmin` is deactivated after `sso-init`. MFA is mandatory for humans (enforced by `configure` on first login) in **every** flow a human can reach: the SC flows enforce it, and every upstream flow is denied (`05-hardening.yaml`, AUTH-1). Upstream flow semantics are re-verified on every Authentik bump (§19, 15-AC-29). `sc-breakglass` is exempt from MFA so that MFA-device loss can't lock out recovery (ADR-0251). The compensating controls are a ≥ 32-char random password held only in the bundle, the `SCBreakGlassUsed` alert, and drills.
- Authentik event log = audit trail for all logins, admin actions and model changes (365 days).

## 14. Happy path

### 14.1 Reconcile (P07, Flux)
1. After control transfer, Flux applies `authentik`: Namespace (already seeded), NetworkPolicies, SOPS Secrets (`authentik-secret-key`, `authentik-sc-env`), blueprint ConfigMap, `authentik-pg` (plan 12), HelmRelease.
2. Authentik starts. Migrations run. System blueprints run, including the upstream bootstrap blueprint, which creates `akadmin` with `AUTHENTIK_BOOTSTRAP_PASSWORD` and token `authentik-bootstrap-token` with `AUTHENTIK_BOOTSTRAP_TOKEN` because the ephemeral Secret exists (§18). SC blueprints run.
3. `authentik-config` (check Job) succeeds → `authentik-sso-forgejo` applies `forgejo/forgejo-oidc-values` → Forgejo upgrades and restarts once with the OIDC source (plan 16 §9.3). The monitoring layer (Grafana with OIDC) reconciles independently.

### 14.2 `sso-init` (bootstrap step owned here; plan 50 invokes)

| Step | Action | Idempotency check | Failure |
|---|---|---|---|
| A0 | (runs inside `flux-bootstrap` before the source switch, **and** before any start of Authentik on an empty `authentik-pg` (black start fresh initdb, §Bootstrap recovery), AUTH-1) generate `SC-AUTHENTIK-BOOTSTRAP`; `kubectl -n authentik create secret generic authentik-bootstrap --from-literal=password=… --from-literal=token=…` labeled `strange.company/ephemeral=bootstrap`. This is a documented third class of imperative Secret (ephemeral, bootstrap-labeled, destroyed at A9/A10), distinct from plan 19's seed Secrets (SEEDFLUX-B10) | Secret exists with label → reuse values from `/run/sc/secrets/` (re-read from the Secret if the operator session was lost) | namespace missing → `SSO_NAMESPACE_MISSING` (plan 19 seed-set issue) |
| A1 | (after the `authentik-config` Kustomization is Ready) port-forward `authentik-server:9000`; wait `GET /-/health/ready/` = 200 (timeout 15 min) | — | `AUTHENTIK_UNHEALTHY` |
| A2 | `GET /api/v3/core/users/me/` with bootstrap token → `is_superuser: true` | — | `AUTHENTIK_BOOTSTRAP_AUTH_FAILED` → `restore-authentik.md` §Bootstrap recovery |
| A3 | verify all SC blueprint instances `status: successful` (`GET /api/v3/managed/blueprints/`) | — | `AUTHENTIK_BLUEPRINT_FAILED` (prints the instance's last log) |
| A4 | ensure `sc-breakglass` (during bootstrap with the bootstrap token; after A9 through the admin channel below): `GET /api/v3/core/users/?username=sc-breakglass` → if absent `POST /api/v3/core/users/` (`username`, `name: "Break-glass"`, `type: internal`, `path: sc/breakglass`, `groups: [<sc-breakglass pk>]`, `is_active: true`), then `POST /api/v3/core/users/{pk}/set_password/` with the staged password. If present, verify group membership and `is_active`; **do not** verify the password by interactive login (that would trigger `SCBreakGlassUsed`). Password verification happens only in drills | membership ok | — |
| A5 | ensure operator human: user `OPERATOR_USERNAME` in `sc-admins`. If newly created, `POST /api/v3/core/users/{pk}/recovery/` (verify) → print the one-time link (expires per `sc-recovery` flow token, default 30 min) to the operator terminal **only**, never logged to the journal | exists | — |
| A5b | ensure the **synthetic verification user** `synthetic-verifier` (path `sc/synthetic`, `type: internal`, group `sc-synthetic` only): password and TOTP seed from `SC-SYNTHETIC-USER` (generated pre-seed, held operator-side: recovery bundle + recovery-recipient-only `secrets/offcluster/<c>/synthetic-user.sops.yaml`, never in the cluster). Used by headless OIDC checks run from the toolbox only: 15-AC-12/13, 20-AC-3, plan 39 `sc-dashcheck` | exists, groups equal | — |
| A6 | verify OIDC discovery for `forgejo`, `grafana`, `hermes-dashboard` = 200 and `issuer` matches | — | `AUTHENTIK_PROVIDER_MISSING` |
| A7 | wait (≤ 20 min) until `GET https://git.<d>/user/oauth2/authentik` redirects to `https://auth.<d>/application/o/authorize/` (Forgejo restarted with the source) | — | `FORGEJO_OIDC_NOT_REGISTERED` → check HelmRelease `forgejo` events |
| A8 | operator confirmation prompt: "log in at https://git.<d> with your Authentik account and enrol MFA; press Enter when done". Then verify via API that the operator user has ≥ 1 MFA device (`GET /api/v3/authenticators/admin/all/?user=<pk>`, verify) | device count ≥ 1 | operator may skip with `--defer-operator-mfa`; the journal records `operator_mfa: deferred` and `verify` (P12) fails until done |
| A9 | **bootstrap credential teardown:** (a) `kubectl -n authentik delete secret authentik-bootstrap`; (b) `kubectl -n authentik rollout restart deploy/authentik-server deploy/authentik-worker` and wait ready (env vars now absent, so the bootstrap blueprint's token condition is false); (c) with the bootstrap token: `PATCH /api/v3/core/tokens/authentik-bootstrap-token/` `{expiring: true, expires: now+10m}`; (d) `PATCH /api/v3/core/users/{akadmin}/` `{is_active: false}`; (e) delete `/run/sc/secrets/authentik-bootstrap*`; (f) after 10 min, verify the token returns 401/403 | Secret absent, akadmin inactive, token rejected | partial → re-runnable through the admin channel; if the token is already rejected, steps c/d are verified via `platform-probes` (akadmin `is_active=false`) |
| A10 | journal record `sso-init` (`result`, step outcomes, operator MFA state, blueprint instance hashes) | — | — |

**Post-bootstrap admin channel (AUTH-5).** After A9 no Authentik write token exists. Every later `sso-init` run (repair of `sc-breakglass` or `akadmin`, `--rotate-breakglass`, re-run after a restore) uses `kubectl -n authentik exec deploy/authentik-worker -- ak shell -c <script>` running the versioned, idempotent script `sso-init-admin.py` shipped in the toolbox (it is passed on stdin, never written into the pod). It needs cluster-admin, so each use is evidenced by the kube-apiserver `exec` audit entry and by Authentik `model_updated`/`model_created` events; nothing about it is audit-free. The script never reads or prints passwords except the staged `sc-breakglass` value it sets, which it receives on stdin.

### 14.3 Normal human login
User opens Forgejo → redirect to Authentik `sc-authentication` (identification + password → MFA validate, or MFA configure on first login) → implicit consent → callback. Forgejo auto-registers with username = `nickname` and maps groups to teams. Grafana and Hermes work the same way. Prometheus/Alertmanager go through forward-auth with the same Authentik session.

### 14.4 Break-glass (SPEC §22)

#### 14.4.0 Inventory

| System | Credential (class) | Lives (outside cluster) | Normal-path failure it covers | How used | Audit evidence | Automated detection | Test |
|---|---|---|---|---|---|---|---|
| Cluster administration | SSH key for node user `sc-breakglass` (`SC-BREAK-GLASS-CLUSTER`) | recovery bundle | operator laptop/kubeconfig lost; kube-apiserver unreachable remotely; RBAC broken | `ssh sc-breakglass@<node>` → `sudo k3s kubectl …` (node-local root kubeconfig `/etc/rancher/k3s/k3s.yaml`) | sshd journal (`Accepted publickey for sc-breakglass`), PAM textfile timestamp, kube-apiserver audit log entries for `system:admin` | `SCBreakGlassUsed{system="cluster"}` from node-exporter textfile metric; `SCK3sBreakGlassAuditSeen` from the kube-apiserver audit counter (plan 07) | quarterly drill: SSH + `k3s kubectl get nodes` |
| Authentik | `sc-breakglass` superuser password (`SC-BREAK-GLASS-AUTHENTIK`); **recovery key** minted on demand via cluster break-glass | bundle | human admins locked out (MFA lost, IdP misconfigured) | web login (MFA-exempt) at `https://auth.<d>/` **or** `kubectl -n authentik exec deploy/authentik-worker -- ak create_recovery_key 1 sc-breakglass` → one-time URL `https://auth.<d>/recovery/use-token/<token>/` (verify: duration argument is in years) | Authentik events `login` (user `sc-breakglass`), `model_created` (recovery token), k8s audit log of the `exec` | `SCBreakGlassUsed{system="authentik"}` from `platform-probes` (`last_login`) | quarterly drill: password login via flow executor |
| Forgejo | `sc-breakglass` local site admin (`SC-BREAK-GLASS-FORGEJO`) | bundle | Authentik down, OIDC misconfigured, all `sc-admins` unavailable | web login at `https://git.<d>/user/login` | Forgejo `last_login` on the user, router log `POST /user/login` | `SCBreakGlassUsed{system="forgejo"}` from `platform-probes` | quarterly drill: web form login |
| Grafana | built-in admin `sc-breakglass` (`SC-BREAK-GLASS-GRAFANA`) | bundle (+ SOPS, §14.4.4) | Authentik down during an incident | `https://grafana.<d>/login?disableAutoLogin=true` (login **form** only; plan 39 disables `[auth.basic]` so API basic auth cannot be used undetected, AUTH-8) | Grafana server log (`Successful Login`, user `sc-breakglass`) | `SCGrafanaLocalLogin` (catalogue; `grafana_api_login_post_total`, plan 39; verify it counts only local-form logins) | quarterly drill: login through the **form** (`SCGrafanaLocalLogin` fires; API basic auth is disabled) |
| Bootstrap infrastructure | `SC-BREAK-GLASS-INFRA` = provider-account console login + MFA recovery codes (operator-offline, **not** bundled), plus the bundled toolkit `SC-INFRA-PROVIDER`, `SC-DNS-API`, `SC-DNS-TUNNEL`, `SC-OBJECT-STORAGE/admin`, `SC-TOFU-ENCRYPTION` (AUTH-16) | operator offline (console); bundle (toolkit) | nodes unreachable; SSH broken; total loss | toolbox: `./bootstrap exec -- tofu …`, provider console/rescue mode, `./bootstrap recover` (plan 45) | provider-side audit logs (OVH API logs / console history, Cloudflare audit log, S3 provider access logs) | none (outside the system); the procedure records usage | quarterly drill: read-only toolkit API calls plus an operator attestation that the console login with MFA works and how many recovery codes remain (AUTH-23) |

**Decision: no offline client-certificate kubeconfig in the bundle.** A long-lived `system:masters` client cert cannot be revoked short of rotating the k3s client CA, and it expires or breaks on CA rotation. SSH + node-local kubeconfig always matches the live CA. When SSH itself is broken, the provider console (infrastructure break-glass) is the next layer. ADR-0251 records this.

#### 14.4.1 Cluster detection (plan 08 implements the node part)
PAM `pam_exec` on sshd session open, for user `sc-breakglass` only, atomically writes:
```text
# /var/lib/node_exporter/textfile/sc_breakglass.prom
sc_breakglass_ssh_last_login_timestamp_seconds{account="sc-breakglass"} <unix time>
sc_breakglass_ssh_key_present{account="sc-breakglass"} 1
```
The key-present line is also refreshed by a systemd timer every 15 min (it checks `authorized_keys` contains the committed fingerprint).

#### 14.4.2 Authentik and Forgejo detection
`platform-probes` (§21.2) polls every 60 s:
- Authentik `GET /api/v3/core/users/?username=<u>` for `u ∈ {sc-breakglass, akadmin}` → `last_login`, `is_active`, groups.
- Forgejo `GET /api/v1/users/<u>` (admin-scoped read token) for `u ∈ {sc-breakglass, sc-admin}` → `last_login`, `is_admin`, `prohibit_login`. Interactive web use of the automation admin `sc-admin` is also treated as a break-glass-class event. API basic auth does not update `last_login` (verify, 15-AC-22).

It exports `sc_breakglass_last_login_timestamp_seconds{system,account}` and `sc_breakglass_account_ready{system,account}` (1 when the account exists, is active and has its admin/superuser flag; for `akadmin`, ready means **inactive**).

#### 14.4.3 Alert semantics
`SCBreakGlassUsed` fires while `time() - last_login < 86400`, so it stays active 24 h after use. Severity `critical`, category `security`, routed to every configured transport (plan 42). Break-glass alerts are **never silenced** (plan 42; any matching silence raises `SCBreakGlassAlertSilenced`): the procedure acknowledges the alert in the incident record with the evidence-object reference and lets it expire after 24 h. Probe downtime up to 24 h cannot hide a login, because the timestamp lives in the source system.

#### 14.4.4 Grafana specifics
Grafana is rebuilt from Git (plan 39), so its built-in admin password must be in Git (SOPS `SC-GRAFANA-ADMIN`) as well as the bundle. Grafana holds no write path to production, so in-cluster storage of this one credential is accepted and recorded (ADR-0251). Detection: the catalogue alert `SCGrafanaLocalLogin` fires on any increase of `grafana_api_login_post_total` (plan 39 exposes it; verify that OAuth logins do not increment it on the pinned Grafana).

#### 14.4.5 Periodic testing
- **Drill phases (AUTH-2).** `--phase P07` (first drill, before monitoring exists): cluster, Authentik, Forgejo and infrastructure only; detection is checked on the source metrics (node textfile `sc_breakglass_ssh_last_login_timestamp_seconds`, `platform-probes` `/metrics`), not on Alertmanager. `--phase full` (default from P09): all five systems, including Grafana through the login **form**, and the Alertmanager checks below. `--only authentik-recovery-key` exercises the recovery-key path (Option B); it runs at least yearly (AUTH-23).
- **Quarterly drill (operator, ≤ 30 min):** `./bootstrap exec -- sc-bootstrap breakglass drill` unlocks the recovery bundle locally (passphrase prompt), then for each system performs the test in §14.4.0, including the operator's attestation of the provider console login (AUTH-23). It waits ≤ 10 min for Alertmanager (via port-forward, `GET /api/v2/alerts?filter=alertname="SCBreakGlassUsed"`) to show `SCBreakGlassUsed` for `cluster`, `authentik` and `forgejo`, which proves the detection path end to end. It writes `s3://<prefix>-evidence/breakglass-drills/<UTC ts>.json` (per-system result, alert latencies) and patches ConfigMap `monitoring/breakglass-drill-status` (`data.lastSuccess: <unix>` only when all systems pass).
- **Continuous non-secret readiness checks (automated):** `sc_breakglass_account_ready` and `sc_breakglass_ssh_key_present` → `SCBreakGlassAccountNotReady`. An automated *login* test is rejected because it would require the credential inside the cluster, which contradicts SPEC §22 "live outside the cluster".
- `SCBreakGlassTestOverdue` fires when the last successful drill is older than 100 days.

### 14.5 Human access lifecycle (AUTH-10)

Procedure `procedures/manage-human-access.md` (owner: this plan) implements these sections. Every step goes through the toolbox (`./bootstrap exec -- sc-bootstrap access <verb> <user>`, plan 50 CLI) and writes `s3://<prefix>-evidence/access/<UTC ts>-<user>.json`.

| Section | Authentik | Forgejo | Grafana | Hermes | Woodpecker |
|---|---|---|---|---|---|
| §Join | create user in path `sc/humans`, groups per role, recovery link printed to the inviter (A5 mechanics); MFA enrolment forced at first login | account auto-created at first OIDC login; teams from groups | role from groups | dashboard access by group | login via Forgejo |
| §Leave | deactivate; delete sessions, tokens, MFA devices (`DELETE /api/v3/core/user_consent/…`, `/authenticators/admin/…`, `/core/tokens/…`) | `PATCH /admin/users/<u>` `{prohibit_login: true}`; delete PATs and OAuth2 grants (admin API); verify an old session cookie is rejected (verify Forgejo behavior) | delete the user (`DELETE /api/admin/users/<id>`) | invalidate sessions (plan 24) | revoke the user's token, remove from orgs |
| §Lost MFA device | admin (or `sc-breakglass`, if the lost device belonged to the only admin) deletes the user's authenticators; next login forces re-enrolment | — | — | — | — |
| §Lost device (laptop) | §Lost MFA device + delete sessions | delete PATs, OAuth2 grants and sessions | delete sessions (restart Grafana if needed) | invalidate sessions | revoke token |

Runner admin access (CD-30): §Join issues the operator's per-operator `SC-RUNNER-ADMIN` credential (generated locally, shown once, hash entry added by PR to `secrets/strange-company/runner-admin-credentials.sops.yaml`, verified with `GET /v1/admin/whoami`); §Leave and §Lost device revoke it immediately with `POST /v1/admin/credentials/{kid}:revoke`, then a PR removes the hash entry. The Runner takes identity only from the credential (no `X-SC-Actor`).

Acceptance: 15-AC-33 (k3d).

## 15. Failure modes

| # | Failure | Detection | Effect | Automated response | Procedure |
|---|---|---|---|---|---|
| A1 | Authentik server down | `SCAuthentikDown`: `up{job=~"authentik-server.*"}==0` or blackbox `probe_success{probe="authentik"}` (plan 38 probe name) fails 5m (critical) | no new human logins anywhere; **existing Forgejo/Grafana sessions keep working**; machines unaffected | kubelet restart | `authentik-outage.md` |
| A2 | Authentik worker down / task backlog | `SCAuthentikWorkerDown` (`up{job=~"authentik-worker.*"}==0` 10m); `SCAuthentikTasksFailing` (metric `authentik_system_tasks{status="error"}` > 0 for 30m, verify name) | blueprints not applied, events delayed | restart | `authentik-outage.md` §Worker |
| A3 | `authentik-pg` down | plan 12 `SCCnpgClusterDown{cluster="authentik-pg"}` + A1 | A1 | CNPG failover (small-ha) | `cnpg-primary-loss.md` → `restore-authentik.md` |
| A4 | Blueprint fails to apply (bad PR) | `authentik-config-check` Job fails → Kustomization `authentik-config` not Ready → `SCFluxReconciliationFailing` (plan 42); `SCAuthentikBlueprintFailed` from the probe (`sc_authentik_blueprint_status{status!="successful"}`) | dependent layers (Forgejo OIDC values, Grafana) not updated; existing config stays | none | revert PR; `restore-authentik.md` §Blueprint |
| A5 | Admin locked out (MFA devices lost, flow misconfigured) | human report | admin UI inaccessible | none | `break-glass-access.md` §Authentik |
| A6 | DB corruption / bad change (deleted users, wrecked flows) | human report, events | identity wrong | none | `restore-authentik.md` (PITR) |
| A7 | `AUTHENTIK_SECRET_KEY` lost/changed | sessions invalidated en masse; `restore-authentik.md` pre-check compares the key hash in the pod with Git | all sessions reset | none | restore the key from Git; with `user_uuid` subjects, OIDC links survive |
| A8 | OIDC client secret mismatch (one side updated) | prevented: `sc-bootstrap secrets rotate SC-OIDC-CLIENTS` always writes both SOPS files and stamps both with annotation `strange.company/value-digest` (sha256 of the plaintext); F11 `secrets-lint` fails a PR whose two digests differ (AUTH-19). At runtime: app login error `invalid_client` reported by users; `SCOIDCLoginBroken` covers discovery only | that app's logins fail | none | fix the PR so both files carry the same value |
| A9 | Forward-auth outage | blackbox on `https://prometheus.<d>` returns 5xx | Prometheus/Alertmanager UIs inaccessible | none | `kubectl port-forward` via toolbox |
| A10 | Bootstrap credential not torn down (A9 incomplete) | probe: `sc_breakglass_account_ready{account="akadmin"}=0` (akadmin active) → `SCAuthentikBootstrapAdminActive`; Secret `authentik-bootstrap` exists → same alert (probe checks via the API that the token `authentik-bootstrap-token` is absent) | standing superuser credential | none | re-run `./bootstrap sso-init` (A9) |
| A11 | Break-glass used | `SCBreakGlassUsed` | — | notify | `break-glass-access.md` §After use |
| A12 | Break-glass account damaged (deleted, deactivated, de-privileged; SSH key removed) | `SCBreakGlassAccountNotReady` | recovery path at risk | none | `break-glass-access.md` §Repair |
| A13 | Probes down | `SCPlatformProbesDown` (`up{job="platform-probes"}==0` 15m); `SCBreakGlassAuditDown` (`sc_breakglass_probe_success==0` 30m) | detection gap ≤ 24 h | restart | §21.2 |
| A14 | Drill overdue | `SCBreakGlassTestOverdue` | untested recovery | notify | `break-glass-access.md` §Drill |
| A15 | Connection exhaustion on `authentik-pg` | CNPG `cnpg_backends_total / max_connections > 0.85` (plan 12 rule) | Authentik 500s | none | raise `max_connections` (plan 12) |

## 16. Retry behavior

- `sso-init`: API calls retry 5× with exponential backoff on 5xx and connection errors. Health waits are bounded (15 min, A7 20 min). A re-run resumes at the first unsuccessful step. After 3 identical failures, plan 50 stops with `NEEDS_OPERATOR`.
- Blueprints: Authentik retries failed instances on its discovery schedule. No external loop.
- `authentik-config-check`: `backoffLimit: 3`, `activeDeadlineSeconds: 900`.
- `platform-probes`: each poll is independent. Failures are counted (`sc_probe_errors_total{probe}`), never retried in a tight loop.

## 17. Idempotency behavior

- Blueprints are declarative (`state: present` for SC objects). Re-applying them is a no-op.
- `sso-init` steps are check-then-act (§14.2). A second run on a completed install verifies through the audit token and the probe metrics and performs no mutations when everything is in state (`already-complete`). When a repair is needed (`sc-breakglass` missing, inactive or without its group; `akadmin` active), it applies it through the admin channel (§14.2, AUTH-5).
- The break-glass password is never re-set by `sso-init` once the account exists (A4), except with the explicit `--rotate-breakglass`.
- The drill is safe to repeat. Its only side effects are evidence objects, a ConfigMap update and expected alerts.

## 18. Bootstrap behavior

1. Pre-seed (plan 50): generate `SC-AUTHENTIK-SECRET-KEY`, `SC-OIDC-CLIENTS`, `SC-AUTHENTIK-AUDIT-TOKEN` into SOPS files; stage `SC-BREAK-GLASS-*` in `/run/sc/secrets/` for the recovery bundle and the offcluster files (plan 35). Commit the public half of `SC-BREAK-GLASS-CLUSTER` for plan 08.
2. Configure (plan 08): node user `sc-breakglass` and PAM hook.
3. Seed (P04): namespace `authentik` only (assumed seed-set member, §5).
4. forge-init (P05). Then **`sso-init` A0** runs as an explicit hook inside `flux-bootstrap`, before the source switch (plan 50): the ephemeral bootstrap Secret exists before Authentik first starts. The journal records the hook time; `verify` fails if the first `authentik-server` pod started earlier.
5. Control transfer (P06) → Flux reconciles Authentik → `sso-init` A1–A10 (P07).
6. `verify` (P12) requires: `sso-init` result `succeeded`, `operator_mfa: enrolled`, no `SCAuthentikBootstrapAdminActive`, the P07 drill (15-AC-24) and the first full drill (15-AC-26b) recorded.

**Black start / restore:** when `authentik-pg` comes back by CNPG recovery, `akadmin` exists and is inactive, the bootstrap Secret is absent, and the optional env stays empty; no bootstrap credential is needed and blueprints re-apply from Git (`restore-authentik.md`). **Empty-database rule (AUTH-1):** whenever Authentik would start on an **empty** `authentik-pg` (black-start fresh initdb, plan 45 S16; any re-initdb), the HelmRelease stays suspended until `sso-init --from A0` has created the bootstrap Secret; only then is it resumed. Even if this rule were skipped, `05-hardening.yaml` denies `initial-setup`, so no internet visitor can claim `akadmin` (15-AC-30).

## 19. Upgrade behavior

- Renovate proposes chart+image bumps. Authentik releases ship DB migrations, which are forward-only.
- **Backup-first gate:** same mechanism as plan 16 §19 (CNPG on-demand backup of `authentik-pg` completed less than 2 h before merge, evidence referenced in the PR).
- Read the release notes for **breaking changes to blueprints, flows or providers** (e.g. redirect-URI matching modes, invalidation flows in 2024.12, Redis removal in 2025.10). Each upgrade PR must pass the k3d integration suite (§22), which applies all SC blueprints against the new version.
- Upgrades skip at most one major `YYYY.N` release at a time, unless upstream documents direct paths.
- Every bump re-runs the flow-hardening checks (15-AC-29, 15-AC-30) in k3d: upstream flow slugs, the default brand and the `initial-setup` behavior are version-sensitive (AUTH-1).
- HelmRelease: `upgrade.remediation.retries: 0`. Rollback is PITR + revert (`restore-authentik.md` §Upgrade rollback).

## 20. Recovery behavior

**Guarantee [SPEC §22]:** Authentik recovery never requires a working Authentik login:
- the DB restores through CNPG (operator uses cluster admin, not Authentik);
- configuration re-applies from Git blueprints;
- `AUTHENTIK_SECRET_KEY` comes from Git (decryptable with the cluster or offline recovery identity);
- admin access after restore comes from the `sc-breakglass` password (bundle) or a recovery key minted through cluster break-glass;
- every consumer has a non-Authentik fallback: Forgejo/Grafana local break-glass, Forgejo PATs for Git over HTTPS (they keep working while Authentik is down), and existing sessions.

**Identity-outage change path (AUTH-6).** Restoring Authentik must not wait for a merge that needs an Authentik login. Decision (option 1): the operator applies the recovery change with cluster admin. (1) Push the change as branch `restore/authentik-<date>` with a personal access token (PATs work without Authentik) and open the PR. (2) Suspend the affected Flux Kustomizations (`authentik-db`, `authentik-upgrade-gate`, `authentik`). (3) `kustomize build` the PR branch's layer paths in the toolbox and `kubectl apply --server-side` them with the operator identity. (4) When Authentik is back, review and merge the PR through the normal path and resume the Kustomizations; Flux then owns the objects again with no diff. (5) The evidence object records the temporary drift window and the PR. A `sc-breakglass` merge in Forgejo is **not** used for this, because it is self-review through a break-glass identity. The same path applies to any other change needed while Authentik is unavailable (e.g. plan 10's tunnel → direct switch).

**Restore sequence** (`restore-authentik.md`): suspend `authentik-db`, `authentik-upgrade-gate`, `authentik` and the HelmRelease → scale to 0 → CNPG PITR to target T as a new `authentik-pg` (plan 12) → verify the `SECRET_KEY` hash → resume → force blueprint re-apply (`kubectl -n authentik exec deploy/authentik-worker -- ak apply_blueprint <path>` for each SC file, verify command name) → check discovery, synthetic logins and Forgejo login → record evidence. RTO target 4 h; expected about 45 min at the minimum profile.

## 21. Observability

### 21.1 Metrics
Authentik server and worker expose `:9300/metrics` (ServiceMonitors from the chart, jobs `authentik-server-metrics` and `authentik-worker-metrics`). Dashboards (plan 39): logins/min, failed logins, flow execution latency, worker task status, DB connections.

### 21.2 Platform probes (component owned by this plan)
- Deployment `monitoring/platform-probes`, 1 replica, PSA restricted-compatible (`runAsUser: 10001`, plan 47), `automountServiceAccountToken: false`. Delivered by Flux Kustomization `platform-probes` (`dependsOn: [forgejo]` only, never an Authentik layer, AUTH-4; `gitops/platform/monitoring/probes/`) right after control transfer (**P06**), so probe metrics are readable (`/metrics`) from P06; each module tolerates an absent dependency by exporting its `*_probe_success`/`*_up` as 0; its ServiceMonitor lives in `monitoring-scrape` (ADR-0550) and alerts fire from P09. Image = the **toolbox image digest** from `toolbox/image.lock` (ADR-0013). Command `sc-bootstrap probes serve --config /etc/probes/config.yaml --listen :9464`, reusing the Go clients from forge-init/sso-init (ADR-0002). Metrics are served on the container port named `metrics` (TCP 9464, `/metrics`, unauthenticated, NetworkPolicy-restricted to Prometheus), per plan 38's interface rule. ServiceMonitor job `platform-probes`.
- Modules (enabled by config as their dependencies come up):

  | Module | Owner plan | Enabled at | Interval | Metrics |
  |---|---|---|---|---|
  | `github-mirror` | 17 | P06 (after transfer; the P06 gate itself uses `sc-bootstrap mirror verify`) | 60 s (GitHub API call every 5 min) | `sc_github_mirror_*`, `sc_github_api_up`, `sc_github_repo_setting_ok{kind}` (plan 17 §21) |
  | `ci-signals` | 17 (signals defined by plan 52 §21) | P08 | 300 s | `sc_ci_offcluster_lane_last_success_timestamp_seconds`, `sc_ci_offcluster_lane_last_run_conclusion{conclusion}` (GitHub Actions workflow `e2e` on `main`, probe token with Actions: read), `sc_ci_nightly_last_conclusion{conclusion}` (Forgejo statuses `ci/woodpecker/cron/nightly`, sc-admin read token), `sc_ci_quarantine_entries` (`policy/test-quarantine.yaml` at the deployed revision) |
  | `forge` | 16 | P06 | 300 s | `sc_forge_config_drift{object}`, `sc_forge_bot_token_valid{bot}` (weekly), `sc_forgejo_ssh_hostkey_match`, `sc_forge_unexpected_admin_tokens` (tokens of `sc-admin` other than the declared `platform-probes` token; AUTH-9) |
  | `breakglass` | 15 | P07 | 60 s | `sc_breakglass_last_login_timestamp_seconds{system,account}`, `sc_breakglass_account_ready{system,account}`, `sc_breakglass_probe_success{system}`, `sc_breakglass_drill_last_success_timestamp_seconds` (from the optional ConfigMap volume `breakglass-drill-status`) |
  | `authentik` | 15 | P07 | 300 s | `sc_authentik_blueprint_status{instance,status}`, `sc_oidc_discovery_ok{app}`, `sc_authentik_bootstrap_token_present`, `sc_authentik_signing_cert_expiry_timestamp_seconds` (§21.4), `sc_authentik_upstream_flow_open{kind}` (1 if any upstream flow is reachable without the deny policy; AUTH-1) |
  | `tls` | 10 | P07 | 300 s | `sc_internal_ca_expiry_timestamp_seconds{generation}` from ConfigMap `monitoring/sc-internal-ca` (public data only; plan 10 §21) |
  | `ha` | 47 | H01 | 300 s | `sc_ha_drill_last_success_timestamp_seconds` from ConfigMap `monitoring/ha-drill-status` (plan 47) |
- Secrets mounted: `platform-probes-forgejo` (plan 16), `platform-probes-authentik` (`SC-AUTHENTIK-AUDIT-TOKEN`), `platform-probes-github` (plan 17).
- ConfigMaps mounted (optional volumes): `breakglass-drill-status`, `github-mirror-config` (plan 17), `sc-internal-ca` (plan 10), `ha-drill-status` (plan 47).
- NetworkPolicy egress: Forgejo 3000, Forgejo SSH 2222, Authentik 9000, `api.github.com:443` (plan 17), DNS.
- **Image-availability caveat:** when the install used a *locally built* toolbox (ADR-0013 fallback), the digest may not be pullable from the cluster. Plan 50 then imports the image into each node's containerd (`k3s ctr images import`) and records the digest actually used in `gitops/clusters/<c>/config/toolbox-image.yaml`, which the Deployment references (§25 Q-15-03).

### 21.3 Alerts (catalogue names, CD-01; plan 42 registers routing)

| alertname | Expr (sketch) | for | severity |
|---|---|---|---|
| `SCAuthentikDown` | `probe_success{probe="authentik"} == 0` (catalogue; plan 38 probe name) | 5m | critical |
| `SCAuthentikWorkerDown` | `up{job=~"authentik-worker.*"}==0` | 10m | warning |
| `SCAuthentikTasksFailing` | worker task error metric > 0 (verify name) | 30m | warning |
| `SCAuthentikBlueprintFailed` | `sc_authentik_blueprint_status{status!="successful"}==1` | 15m | warning |
| `SCAuthentikBootstrapAdminActive` | `sc_breakglass_account_ready{system="authentik",account="akadmin"}==0 or sc_authentik_bootstrap_token_present==1` | 30m | critical |
| `SCOIDCLoginBroken` | `sc_oidc_discovery_ok==0` | 10m | critical |
| `SCBreakGlassUsed` | `(time() - max by (system,account)(sc_breakglass_last_login_timestamp_seconds)) < 86400 or (time() - max by (account)(label_replace(sc_breakglass_ssh_last_login_timestamp_seconds,"system","cluster","",""))) < 86400` | 0m | critical |
| `SCBreakGlassAccountNotReady` | `sc_breakglass_account_ready==0 or sc_breakglass_ssh_key_present==0` | 30m | critical |
| `SCBreakGlassAuditDown` | `sc_breakglass_probe_success==0` | 30m | warning |
| `SCBreakGlassTestOverdue` | `time() - sc_breakglass_drill_last_success_timestamp_seconds > 8640000 or absent(sc_breakglass_drill_last_success_timestamp_seconds)` | 1h | warning |
| `SCPlatformProbesDown` | `up{job="platform-probes"}==0` | 15m | warning |
| `SCAuthentikUpstreamFlowOpen` (**new**, plan 42 to register) | `max(sc_authentik_upstream_flow_open) == 1` | 15m | critical |
| `SCAuthentikSigningCertExpiring` (**new**) | `min(sc_authentik_signing_cert_expiry_timestamp_seconds) - time() < 30*86400` | 1h | warning |
| `SCForgeUnexpectedAdminToken` (**new**, AUTH-9) | `max(sc_forge_unexpected_admin_tokens) > 0` | 15m | critical |
| `SCBreakGlassAlertSilenced` (**new**, plan 42 owns rule and receiver, AUTH-8) | any active silence whose matchers match a break-glass alert | 0m | critical (receiver not silenceable) |

### 21.4 OIDC signing key lifecycle (AUTH-21)
- The signing key of all three OIDC providers is the blueprint-referenced certificate `sc-oidc-signing` (`!Find [authentik_crypto.certificatekeypair, [name, sc-oidc-signing]]`), generated by Authentik at first blueprint apply with a 5-year validity (verify the generator default). `platform-probes` exports `sc_authentik_signing_cert_expiry_timestamp_seconds` from `GET /api/v3/crypto/certificatekeypairs/?name=sc-oidc-signing`.
- Rotation (≥ 30 days before expiry, or on compromise): add `sc-oidc-signing-<yyyy>` by blueprint PR, switch providers to it, keep the old pair for one refresh-token lifetime (30 days) so consumers that cache the JWKS re-fetch (Forgejo, Grafana and Hermes fetch JWKS by `kid`), then remove it.
- After an empty-DB rebuild a new key exists; consumers re-fetch the JWKS on the unknown `kid`. Existing sessions in consumers are unaffected; refresh tokens are invalid and users log in again.

## 22. Testing strategy

- **Blueprint validation (F11/F02, offline):** YAML lint. A k3d job applies all SC blueprints to a real Authentik of the pinned version (`ak apply_blueprint`), asserts `status: successful`, and checks that a second apply produces no changes. No paid APIs.
- **Integration (k3d, `make e2e-authentik`):** Authentik + CNPG (1 instance) + Forgejo. Checks: OIDC login via a headless flow-executor client (test user with a TOTP device seeded through the API using a known secret), group→team mapping, forward-auth on a dummy upstream (`traefik/whoami`, digest-pinned) with an allowed and a denied group, `sso-init` idempotency, bootstrap teardown (token rejected afterwards, akadmin inactive), and break-glass detection (login → metric → rule evaluation via `promtool test rules`).
- **Flow hardening (k3d, every Authentik bump):** 15-AC-29 and 15-AC-30 run in `make e2e-authentik` against the pinned version (AUTH-1).
- **Rule unit tests:** `promtool test rules` for every alert in §21.3 with synthetic series.
- **Restore test (plan 44, monthly):** scratch PITR of `authentik-pg` in `dr-tests`, start a scratch Authentik (no ingress), assert a marker user exists and blueprints apply → `restore-test-result`.
- **Drill:** quarterly on live systems (§14.4.5).

## 23. Acceptance criteria

### GATE-P07 (owner)

- **15-AC-1** Authentik healthy: `kubectl -n authentik exec deploy/authentik-server -- ak healthcheck` exits 0. `curl -fsS https://auth.<d>/-/health/ready/` from outside returns 200 (204 accepted).
- **15-AC-2** No Redis, no Bitnami: `kubectl get pods -A -o jsonpath='{..image}' | tr ' ' '\n' | grep -Eic 'redis|valkey|bitnami'` = 0. `helm template` of the pinned chart with our values renders zero resources from the `postgresql` subchart, and `grep -ci redis` on the rendered output = 0. The deployed `server` version (`GET /api/v3/admin/version/` `version_current`) is `2026.8.x` (baseline).
- **15-AC-3** DB on CNPG: env `AUTHENTIK_POSTGRESQL__HOST` in the server pod = `authentik-pg-rw.authentik.svc.cluster.local`. `kubectl -n authentik get cluster authentik-pg -o jsonpath='{.status.phase}'` = `Cluster in healthy state`. `max_connections` ≥ 150 (`SHOW max_connections` via `kubectl cnpg psql`).
- **15-AC-4** Blueprints applied: `GET /api/v3/managed/blueprints/?label=sc` (audit token) returns ≥ 10 instances, all `status: successful`. A second `ak apply_blueprint` of each file reports no changes.
- **15-AC-5** Groups exist: `sc-admins` (superuser), `sc-engineers`, `sc-viewers`, `sc-breakglass` (superuser), `sc-audit`, verified via `GET /api/v3/core/groups/?name=…`.
- **15-AC-6** OIDC discovery: for `forgejo`, `grafana`, `hermes-dashboard`, `curl -fsS https://auth.<d>/application/o/<slug>/.well-known/openid-configuration | jq -r .issuer` = `https://auth.<d>/application/o/<slug>/`.
- **15-AC-7** `sub_mode` is `user_uuid` for all three providers (`GET /api/v3/providers/oauth2/?name=…`).
- **15-AC-8** Access policy enforced: a test user in no `sc-*` group gets HTTP 403 / "Permission denied" from the Authentik authorize endpoint for `forgejo` (headless flow test). A test user named `sc-test` in `sc-engineers` is also denied for `forgejo`.
- **15-AC-9** MFA enforced for humans: a new test user in `sc-engineers` logging in through the flow executor is presented the `authenticator-totp-setup`/`webauthn` stage before any session is issued (the flow challenge component is the setup stage).
- **15-AC-10** Break-glass MFA exemption: `sc-breakglass` completes `sc-authentication` with password only (drill tooling, 15-AC-24).
- **15-AC-12** Authentik login works end to end for Forgejo: the headless OIDC test with a `sc-engineers` test user (TOTP via known seed) ends with a Forgejo session cookie, and `GET /api/v1/user` shows the auto-registered user, who is a member of team `engineers` of `strange-company` (`GET /api/v1/orgs/strange-company/teams/…/members`).
- **15-AC-15** Machine separation: `GET /api/v3/core/users/?type=service_account` lists exactly `sc-audit` (plus Authentik-managed outpost service accounts, if any, filtered by `path` ≠ `sc/*`). `sc-audit` cannot obtain a session through `sc-authentication` (flow executor returns a denial). Its token gets 403 on `POST /api/v3/core/users/`.
- **15-AC-16** Bootstrap teardown: Secret `authentik/authentik-bootstrap` does not exist; `akadmin` has `is_active: false`; a request with the former bootstrap token (from the `sso-init` test log in k3d; live: the journal records only its sha256, and the toolbox re-tests from the local file before deletion) returns 401/403; the server Deployment env has no resolvable `AUTHENTIK_BOOTSTRAP_TOKEN` (`kubectl exec … printenv AUTHENTIK_BOOTSTRAP_TOKEN` exits 1).
- **15-AC-17** Secret key stable: sha256 of the decrypted `SC-AUTHENTIK-SECRET-KEY` from Git equals sha256 of `printenv AUTHENTIK_SECRET_KEY` in the server pod (hash only), (there is no bundle copy; `secret-classes.yaml` `inRecoveryBundle: false`).
- **15-AC-18** Break-glass inventory complete (AUTH-16): the recovery bundle manifest (plan 35) lists `SC-BREAK-GLASS-CLUSTER`, `-AUTHENTIK`, `-FORGEJO`, `-GRAFANA` and the infrastructure toolkit classes `SC-INFRA-PROVIDER`, `SC-DNS-API`, `SC-DNS-TUNNEL`, `SC-OBJECT-STORAGE/admin`, `SC-TOFU-ENCRYPTION`, and has **no** entry for `SC-BREAK-GLASS-INFRA` (operator-offline). The Authentik and Forgejo break-glass passwords exist only in the bundle and in `secrets/offcluster/<cluster>/break-glass-{authentik,forgejo}.sops.yaml`, whose `sops.age` stanza lists **only** the recovery recipient (`yq '.sops.age | length'` = 1 and the recipient equals the recovery public key). No cluster Secret contains them (scan of decrypted Secrets compares hashes), and no other Git file contains their hash prefix.
- **15-AC-19** Cluster break-glass works: `ssh -i <bundle key> sc-breakglass@<node> sudo k3s kubectl get nodes` lists all nodes Ready. Within 5 min the node textfile metric `sc_breakglass_ssh_last_login_timestamp_seconds` (node-exporter `/metrics`, read through `kubectl get --raw` proxy) shows the login time; from P09 on, `SCBreakGlassUsed{system="cluster"}` is also firing in Alertmanager (re-checked in 15-AC-26b). The kube-apiserver audit log on the node contains a `system:admin` entry for `GET /api/v1/nodes` in that minute. Attribution (AUTH-7): in the same minute, a routine `./bootstrap exec -- kubectl get nodes` produces audit entries with `user.username` = `sc-operator:<name>` and none with `system:admin`, and the node's `sc_k3s_audit_breakglass_requests_total` increments only for the break-glass call.
- **15-AC-20** Authentik and Forgejo break-glass detection: after a drill login, `SCBreakGlassUsed{system="authentik",account="sc-breakglass"}` and `{system="forgejo",account="sc-breakglass"}` are firing within 5 min (at P07: `sc_breakglass_last_login_timestamp_seconds{system,account}` on `platform-probes:9464/metrics` shows the login within 5 min; alert firing re-checked in 15-AC-26b). The Authentik event log has a `login` event for `sc-breakglass`.
- **15-AC-21** Recovery key path: `kubectl -n authentik exec deploy/authentik-worker -- ak create_recovery_key 1 sc-breakglass` prints a URL matching `https://auth.<d>/recovery/use-token/[A-Za-z0-9]+/`. Opening it (headless) yields an authenticated admin session. The token is deleted afterwards (`GET /api/v3/core/tokens/?intent=recovery` is empty).
- **15-AC-22** Automation-API calls do not trigger the break-glass alert: running `forge-init` (plan 16) and `sso-init` again leaves `sc_breakglass_last_login_timestamp_seconds{account=~"sc-admin|sc-breakglass"}` unchanged.
- **15-AC-23** Break-glass with Authentik down: with the `authentik` HelmRelease suspended and both Deployments scaled to 0, the Forgejo break-glass login (web form) succeeds, and a Forgejo PAT of an OIDC-registered human still clones and pushes over HTTPS (AUTH-6). Machine paths are unaffected: Flux reconciles (`flux reconcile source git flux-system` Ready) and a `sc-runner` token API call returns 200. (The Grafana half is 15-AC-23b at P09.)
- **15-AC-24** First drill recorded (`sc-bootstrap breakglass drill --phase P07`, AUTH-2): `s3://<prefix>-evidence/breakglass-drills/` contains an object with `cluster`, `authentik`, `forgejo` and `infrastructure` `passed` (including the console attestation), detection proven on the source metrics within 5 min. ConfigMap `monitoring/breakglass-drill-status` `lastSuccess` is within 24 h, and `sc_breakglass_drill_last_success_timestamp_seconds` on the probe endpoint is within 24 h.
- **15-AC-29** Flow hardening (AUTH-1), in k3d `make e2e-authentik` and live: (i) for each upstream slug `default-authentication-flow`, `default-enrollment-flow`, `default-recovery-flow`, `default-source-authentication`, `default-source-enrollment`, `initial-setup`, `GET /api/v3/flows/executor/<slug>/` returns a denial (access-denied challenge or 404), never an identification stage; (ii) `sc-recovery` executed without a recovery token is denied; (iii) a test user in `sc-engineers` with **no** MFA device cannot obtain a session through any flow (every reachable flow presents a setup or validate stage before `user_login`); (iv) a port-forward to `authentik-server:9000` with `Host: 127.0.0.1` serves brand `sc` (`GET /api/v3/core/brands/current/` returns `brand_uuid` of `sc`); (v) `sc_authentik_upstream_flow_open` = 0 for every kind.
- **15-AC-30** Empty-DB takeover is impossible (AUTH-1): k3d starts Authentik on an empty database **without** `AUTHENTIK_BOOTSTRAP_*` and with the SC blueprints mounted; `GET /if/flow/initial-setup/` and the flow executor for `initial-setup` return a denial, and `akadmin` cannot be given a password through any flow. Live: the journal shows step `flux-bootstrap.sso-init-a0` earlier than the first `authentik-server` pod `.status.startTime`.
- **15-AC-33** Human leave (AUTH-10), k3d: after `sc-bootstrap access leave <test-user>`, within 5 min the user's Forgejo PAT gets 401, a pre-existing Forgejo session cookie is redirected to login, and an Authentik login attempt is denied.
- **15-AC-31** Admin channel (AUTH-5), k3d: deactivate `sc-breakglass` and remove it from `sc-breakglass`; run `./bootstrap exec -- sc-bootstrap sso-init` → `sc_breakglass_account_ready{system="authentik",account="sc-breakglass"}` = 1 and the group restored. Reactivate `akadmin`; run `sso-init` → `akadmin` inactive again. The Authentik event log shows `model_updated` events and the kube-apiserver audit log shows the `pods/exec` request.

### GATE-P09 (contributed)
- **15-AC-25** Required alerts exist: PrometheusRules contain every alertname in §21.3. `promtool test rules` passes for all of them in CI.
- **15-AC-26** Metrics visible: `up{job=~"authentik-(server|worker)-metrics"}` = 1, and `up{job="platform-probes"}` = 1.
- **15-AC-26b** First full drill (`--phase full`, AUTH-2/AUTH-8): all five systems `passed`; Grafana is exercised through the login **form**. Within 5 min `SCBreakGlassUsed` fires for `cluster`, `authentik` and `forgejo`, `SCK3sBreakGlassAuditSeen` fires for the cluster login, and `SCGrafanaLocalLogin` fires for the Grafana login (port-forwarded `amtool alert query`). Creating a test silence matching `alertname="SCBreakGlassUsed"` raises `SCBreakGlassAlertSilenced` at the test receiver within 5 min (plan 42).
- **15-AC-11** Forward-auth (moved from P07, needs Prometheus/Alertmanager, AUTH-2/GC-01): unauthenticated `curl -s -o /dev/null -w '%{http_code}' https://prometheus.<d>/` = 302 to `auth.<d>`. With a session for a `sc-viewers` user = 403. With a `sc-engineers` session = 200. For `alertmanager.<d>`, `sc-engineers` = 403 and `sc-admins` = 200. Longhorn UI has no public route (`curl https://longhorn.<d>` gives NXDOMAIN or 404 from Traefik).
- **15-AC-13** Grafana OIDC role mapping (moved from P07; duplicates 39-AC-6/7): the same test user receives role `Editor` (`GET /api/user` → org role via `/api/user/orgs`). A `sc-viewers` user gets `Viewer`. A user without a group is refused (`role_attribute_strict`).
- **15-AC-23b** Grafana break-glass with Authentik down (moved from 15-AC-23): with Authentik scaled to 0, the Grafana login form at `https://grafana.<d>/login?disableAutoLogin=true` accepts `sc-breakglass`, and `GET /api/user` with basic auth returns 401 (basic auth disabled, AUTH-8).

### GATE-P10 (contributed)
- **15-AC-27** Authentik restore works: the plan 44 restore test `rt-app-authentik` (scratch PITR of `authentik-pg`, scratch Authentik `/-/health/ready/` = 200, blueprint-created marker user `sc-dr-marker` and brand `sc` present) records `outcome: passed` within the last 35 days.
- **15-AC-28** Backup works: the latest `authentik-pg` base backup is `completed` and less than 26 h old. `ContinuousArchiving=True`.
- **15-AC-34** Baseline (verify, CD-29): the rendered chart is `authentik` 2026.8.3 and the server reports `2026.8.x`; the version-sensitive assumptions listed in the baseline entry (blueprint models, flow slugs, metrics port 9300, health path, recovery key units) pass 15-AC-29/30 and Q-15-06 checks against it. A different minor fails the check until the plan is amended.
- **15-AC-32** Identity-outage restore path (AUTH-6), lab: with Authentik scaled to 0 and no human able to log in to Forgejo through OIDC, `restore-authentik.md` §PITR runs end to end using only a PAT (branch push) and cluster admin (suspend + server-side apply); afterwards the PR is merged through the normal path, `flux get kustomizations authentik-db authentik` are Ready with no diff, and the evidence object records the drift window.

### GATE-C08 (contributed; plan 24 owns the gate)
- **15-AC-14** Hermes dashboard OIDC: `GET https://hermes.<d>/` unauthenticated redirects to `https://auth.<d>/application/o/authorize/?client_id=hermes-dashboard…`. A user outside `sc-admins`/`sc-engineers` is refused; if native dashboard OIDC cannot enforce this, the forward-auth fallback (§7.1, AUTH-26) is deployed and the same check passes (moved from P07, AUTH-2).

## 24. Explicit non-goals

- OIDC login for `kubectl` (kube-apiserver OIDC) in MVP. Cluster admin is the operator kubeconfig plus break-glass. Post-MVP option.
- Self-service enrollment, social logins, LDAP/SCIM outposts, RAC/RADIUS providers.
- Using Authentik to issue machine credentials.
- Exposing Longhorn UI publicly.
- An automated in-cluster break-glass login test (contradicts "outside the cluster").
- Authentik HA beyond 2 server replicas.
- Custom media storage.

## 25. Open questions

| ID | Question | Blocking | Proposed resolution | Owner |
|---|---|---|---|---|
| Q-15-01 | Namespace `authentik` must exist before the source switch so the ephemeral bootstrap Secret precedes Authentik's first start. | no | Plan 19 adds the namespace (only) to the seed set. Fallback: the orchestrator creates the namespace with identical labels and Flux adopts it. | 19 |
| Q-15-02 | Upstream bootstrap-blueprint re-apply semantics (whether `AUTHENTIK_BOOTSTRAP_*` is read only on first start) are not fully verified. | no | The design is safe either way: the Secret is ephemeral and env is optional, so after A9 nothing can recreate the token. 15-AC-16 re-verifies. | 15 |
| Q-15-03 | `platform-probes` runs the toolbox image, which is large (OpenTofu, Ansible, etc.). A locally built toolbox may not be pullable by the cluster. | no | MVP: toolbox image (single code path, available before P11). Post-P11: plan 23 may publish a slim `sc-probes` image built from the same Go packages. Local-build installs import into containerd (plan 50). | 15 / 23 / 50 |
| Q-15-04 | Grafana break-glass detection depends on `grafana_api_login_post_total` counting only local-form logins. | no | Catalogue alert `SCGrafanaLocalLogin` (plan 39 signal). If the metric also counts OAuth logins on the pinned version, fall back to a Loki rule at H03 (plan 41) and accept the MVP gap. | 39 / 41 |
| Q-15-05 | Hermes dashboard OIDC client type (public PKCE vs confidential) and callback path are unverified. | no | Plan 24 confirms at C08. The blueprint `33-app-hermes.yaml` is adjusted in that PR. | 24 |
| Q-15-06 | Authentik's API endpoint for admin-generated recovery links and the MFA device listing endpoint for A5/A8 are marked (verify). | no | If unavailable, A5 falls back to printing a one-time password set by `set_password` with "change on first login" enforced through the `sc-recovery` flow. A8 falls back to the operator confirming by prompt. | 15 |
| Q-15-07 | Master plan §9 lists `SC-GRAFANA-ADMIN` and `SC-BREAK-GLASS-GRAFANA` separately. They are the same value here. | no | Plan 35 records `SC-BREAK-GLASS-GRAFANA` as "bundle copy of `SC-GRAFANA-ADMIN`". | 35 |
| Q-15-08 | SPEC §22 says break-glass credentials live outside the cluster; the Grafana admin password is also tier-1 SOPS (cluster-decryptable) because Grafana is rebuilt from Git (AUTH-15). | no (operator acceptance required) | Recorded as a spec issue (Interpretation). Compensating controls: Grafana holds no production write path; `[auth.basic]` disabled (plan 39), so only the form login works and it raises `SCGrafanaLocalLogin`; the Alertmanager datasource is read-only (plan 39/42); silences on break-glass alerts page via `SCBreakGlassAlertSilenced`. Alternative if rejected: Grafana Admin only via OIDC, break-glass = anonymous Viewer on a non-ingress port. | lead / operator |
| Q-15-09 | Cluster break-glass attribution needs a distinct routine operator identity (AUTH-7); plans 07 and 50 own the certificate issuance (`sc-bootstrap kubeconfig refresh`). | no | Plans 07/50 implement; 15-AC-19 verifies attribution. | 07 / 50 |
| Q-15-10 | `procedures/manage-human-access.md` (AUTH-10). | no | Resolved: created (lead approved). | 15 |
| Q-15-12 | The synthetic verification user is a non-human account in a human flow (SPEC §21 separation). | no | Accepted for verification only: read-only Forgejo team (`viewers`), Grafana Viewer, credentials never in the cluster, every login visible in Authentik events; `sso-init` A5b keeps it in `sc-synthetic` only. Plan 35 registers `SC-SYNTHETIC-USER`. | 15 / 35 |
| Q-15-11 | With Cloudflare as both tunnel and authoritative DNS, a Cloudflare account loss leaves only the registrar NS change (AUTH-11); the registrar account is off-cluster recovery material not listed in SPEC §111. | no | Plans 09/10/35 add the registrar path and an operator-offline class; spec issue proposed. Authentik admin with the edge down uses `./bootstrap edge-bypass` (plan 09). | 09 / 10 / 35 |
