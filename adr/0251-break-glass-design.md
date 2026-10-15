# ADR-0251: Break-glass access: bundle-held credentials, SSH for cluster administration, MFA-exempt identity accounts, detection from source-system timestamps

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plan:** 15

## Context
SPEC §22 requires controlled break-glass access for cluster administration, Authentik, Forgejo, Grafana and bootstrap infrastructure. The credentials are not used ordinarily, live outside the cluster, are documented, SHOULD be tested periodically and SHOULD produce audit evidence. Failure of Authentik must not make Authentik unrecoverable.

## Decision
- **Storage:** `SC-BREAK-GLASS-CLUSTER`, `-AUTHENTIK`, `-FORGEJO` and `-GRAFANA` live in the passphrase-encrypted recovery bundle (ADR-0012), with a second copy of the first three in `secrets/offcluster/<cluster>/break-glass-*.sops.yaml` encrypted to the **recovery recipient only**, so the cluster can never decrypt them. `SC-BREAK-GLASS-INFRA` (provider console login and MFA recovery codes) is held offline by the operator; the bundled infrastructure toolkit (provider, DNS, object-storage admin, OpenTofu passphrase) carries its own classes. Grafana's admin password is also SOPS-encrypted in Git because Grafana is rebuilt from Git and holds no write path to production. This is the single accepted in-cluster copy.
- **Cluster administration:** a node user `sc-breakglass` with an ed25519 key from the bundle and passwordless sudo, used as `ssh sc-breakglass@<node> sudo k3s kubectl …` against the node-local admin kubeconfig. There is **no** offline client-certificate kubeconfig: a long-lived `system:masters` certificate cannot be revoked without rotating the cluster CA and breaks on CA rotation. If SSH is broken, the provider console (infrastructure break-glass) is the next layer.
- **Authentik:** local superuser `sc-breakglass` (group `sc-breakglass`), exempt from MFA so that loss of MFA devices cannot lock out recovery. Compensating controls: a ≥ 32-character random password held only in the bundle, a page-level alert on every use, and quarterly drills. A one-time recovery key can also be minted with `ak create_recovery_key` through cluster break-glass.
- **Forgejo:** local site admin `sc-breakglass` (no OIDC link), usable while Authentik is down.
- **Detection:** use is detected from timestamps kept by the source system (sshd PAM hook writing a node-exporter textfile; Authentik and Forgejo `last_login` read by `platform-probes`), so a probe outage shorter than 24 h cannot hide a login. The alert (`SCBreakGlassUsed`) stays active for 24 h after use. Cluster use additionally raises `SCK3sBreakGlassAuditSeen` from the kube-apiserver audit counter, and Grafana local-form logins raise `SCGrafanaLocalLogin`. Automation (`forge-init`, `sso-init`) authenticates through APIs that do not update `last_login`, so automation does not trigger the alert.
- **Attribution (AUTH-7).** Routine operator access uses a distinct short-lived client certificate (`CN=sc-operator:<name>`, plans 07/50); `k3s.yaml` never leaves a node. `system:admin` is thereby reserved for break-glass, and the audit counter behind `SCK3sBreakGlassAuditSeen` counts only that identity.
- **Silencing (AUTH-8).** Break-glass alerts carry an always-notify class; any silence matching them raises `SCBreakGlassAlertSilenced` on a receiver that silences cannot match. Break-glass alerts are never silenced; they are acknowledged in the incident record with the evidence reference and expire 24 h after use. Grafana API basic auth is disabled, so the in-cluster Grafana password works only through the login form, which raises `SCGrafanaLocalLogin`.
- **Testing:** a quarterly operator drill (`sc-bootstrap breakglass drill`) unlocks the bundle locally, exercises every credential, waits for the detection alerts and writes evidence to S3 `evidence/breakglass-drills/`. The first drill runs at P07 in `--phase P07` (no Grafana, metric-level detection); the first full drill runs at P09. The drill also records an operator attestation of the provider console login (`SC-BREAK-GLASS-INFRA`), and the recovery-key path runs at least yearly. Automated in-cluster *login* tests are rejected because they would put the credentials inside the cluster. Only non-secret readiness (account exists, active, privileged; key present) is checked continuously.

## Consequences
- The operator must keep the recovery passphrase. Without it, break-glass for Authentik and Forgejo is unavailable (cluster break-glass via the provider console remains).
- The in-cluster Grafana copy deviates from SPEC §22 "live outside the cluster" and is registered as a spec issue for operator acceptance (review AUTH-15), with the compensating controls above.
- Grafana break-glass detection relies on `grafana_api_login_post_total` counting only local-form logins (verify); otherwise a Loki rule at H03 closes the gap.
- Silencing `SCBreakGlassUsed` is restricted to Alertmanager admins, because silences could hide break-glass use.

## Alternatives rejected
- Offline admin kubeconfig in the bundle: not revocable, CA-coupled.
- MFA on the break-glass account: an MFA device is a second off-cluster object that can be lost, which reintroduces the lockout break-glass exists to solve.
- Storing break-glass passwords in SOPS in Git: they would then be present in the cluster and on GitHub (encrypted), contradicting "live outside the cluster".

## Verification
GATE-P07 (plan 15): 15-AC-18 (bundle-only storage), 15-AC-19 (cluster break-glass and audit evidence), 15-AC-20 (Authentik/Forgejo detection), 15-AC-21 (recovery key), 15-AC-22 (automation does not alert), 15-AC-23 (break-glass works with Authentik down), 15-AC-24 (first drill recorded).
