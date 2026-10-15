# Procedure: Credential compromise (contain, rotate, audit)

**Owning plan:** plans/35-secrets.md (with plan 34 §14.6 incident flow)
**SPEC references:** §22, §37, §38, §84–§88, §98, §99, §111, §113
**Severity / urgency:** outage-class urgency (sev1: act within 15 minutes; sev2: within 4 hours; sev3: next working day). Severity per class is in `contracts/secret-classes.yaml` (`compromise.severity`).
**Expected duration:** containment 5–30 min; full rotation and audit 1–8 h depending on the family
**Requires break-glass:** usually yes — `SC-RUNNER-ADMIN` (emergency stop), `SC-K3S-ADMIN-KUBECONFIG`, and the relevant `SC-BREAK-GLASS-*`

## When to use

- Alerts: `SCSecretScanHit`, `SCWorkerPolicyViolation` with `SECRET_DETECTED`, `SCProvenanceStatusForged`, `SCTddProvenanceBypass`, `SCUnsignedImageRunning`, `SCAnomalousSpend`, `SCModelMismatch`, `SCForgeConfigDrift` (unexplained), `SCBreakGlassUsed` (unexplained), `SCAdmissionDenialSpike` (unexplained), `SCRunnerUnexplainedRefChange`.
- A provider, GitHub, Cloudflare or object-storage security notice; a lost or stolen device; a secret seen in a log, chat, ticket or screenshot; a departing custodian who may retain material.

## Preconditions and required material

- Toolbox (`./bootstrap shell`), recovery passphrase, admin kubeconfig.
- `SC-RUNNER-ADMIN` (bundle) for the emergency stop.
- Access to the issuers (provider console, Cloudflare, GitHub, model provider consoles).
- An incident issue opened in Forgejo (label `security-incident`), or, if Forgejo is suspect, a local incident log file under `/blackstart/<cluster>/incidents/`.

## Safety checks before acting

1. **Identify the class** from the evidence (`make secrets-scan` output names a pattern; the register maps it). If unknown, treat it as the most powerful candidate class.
2. **Do not destroy evidence**: do not delete pods, branches, logs or Secrets before recording them (Step 1). Quarantine instead of delete.
3. **Revoke first, replace second** (35-R-7 does not apply to compromise): accept the expected outage of the consumer.
4. **Do not push fixes to `main` by bypassing protection**; use the PR path. Revocation at the issuer is immediate and does not need Git.
5. If host or cluster compromise is suspected (`SC-K3S-*`, `SC-BREAK-GLASS-CLUSTER`, `SC-SSH-OPERATOR` with unexplained logins), prefer **rebuild from Git** (§cluster-and-node) over cleaning in place.

## Steps

### 0. Common opening (every family)

1. Record: time, detecting alert or report, suspected class/instance, exposure window start (first possible leak), where it leaked.
2. Contain spending and autonomous change: if the class can spend money or change code (`SC-MODEL-API-KEYS`, `SC-GATEWAY-TOKEN`, `SC-RUNNER-SIGNING`, `SC-RUNNER-HERMES`, `SC-FORGEJO-BOT-TOKENS`, `SC-WOODPECKER-*`, anything cluster-admin), engage the emergency stop: `./bootstrap exec -- sc-runner-admin emergency-stop --level workers --reason "incident <id>"` (`budget-emergency-stop.md`; level `all` if Hermes is suspect). *Expected:* `effective_level: workers`; running executions cancelled.
3. Snapshot evidence: `./bootstrap exec -- sc-bootstrap evidence collect --incident <id>` (audit-log excerpt for the window, Forgejo audit/events, Runner transitions and executions in the window, Gateway ledger summary by token, Authentik events) → `s3://<prefix>-evidence/incidents/<id>/`.

### cluster-and-node

Classes: `SC-K3S-TOKEN`, `SC-K3S-SERVER-CA`, `SC-K3S-ENCRYPTION-CONFIG`, `SC-K3S-ADMIN-KUBECONFIG`, `SC-SSH-OPERATOR`, `SC-LAB-SSH`, `SC-INTERNAL-CA`, `SC-PG-APP`, `SC-PG-DR-MARKER`; also unexplained admission denials.
1. List nodes and compare with the inventory: `kubectl get nodes -o wide` vs `gitops/clusters/<cluster>/inventory.yaml`. Unknown node → `kubectl delete node <x>`, block its IP in the provider firewall, and treat as host compromise.
2. Review the API audit log for the window (`sc-bootstrap evidence collect` output): requests by `system:admin`, impersonation (`impersonatedUser`), Secret reads, RoleBinding changes, VAP/binding deletions.
3. Kubeconfig/client CA (`SC-K3S-ADMIN-KUBECONFIG`): system:masters client certificates cannot be revoked individually; rotate the client CA (`k3s certificate rotate-ca`, plan 07, verify), re-fetch the kubeconfig (`rotate-secrets.md#sc-k3s-admin-kubeconfig`).
4. SSH keys: replace `authorized_keys` via the break-glass key (`rotate-secrets.md#sc-ssh-operator`); review `journalctl -u ssh` on each node for unexplained logins; if any, go to step 6.
5. k3s token / encryption keys: `rotate-secrets.md#sc-k3s-token`, `#sc-k3s-encryption-config`; treat every in-cluster Secret as exposed if `SC-K3S-ENCRYPTION-CONFIG` **and** an etcd snapshot or etcd data leaked → rotate every class whose storage includes `k8s-secret`.
6. **Host or CA compromise:** rebuild: `total-cluster-loss.md` with full rotation (§recovery-material), restore data from backups taken **before** the exposure window start, never restore an etcd snapshot taken after it.
7. PG credentials: `rotate-secrets.md#sc-pg-app`; review `pg` logs (CNPG pod logs) for foreign client addresses.

### infrastructure-and-dns

Classes: `SC-INFRA-PROVIDER`, `SC-CI-OVH-READONLY`, `SC-DNS-API`, `SC-DNS-TUNNEL`, `SC-ACME-ACCOUNT`, `SC-TOFU-ENCRYPTION`, `SC-BREAK-GLASS-INFRA`.
1. Revoke at the issuer now (provider console: delete the consumer key; Cloudflare: roll the token / delete the tunnel token; ACME: deactivate account).
2. Provider: compare the account's resource inventory and billing with OpenTofu state (`./bootstrap plan` must show no unexpected resources; plan 03 guard). Delete unknown resources only after recording them.
3. DNS: export the zone and diff against the expected record set (`./bootstrap exec -- sc-bootstrap dns diff`); check Certificate Transparency logs for unexpected certificates for `<BASE_DOMAIN>`.
4. Tunnel: list connectors (`cloudflared tunnel info`); remove unknown ones; rotate (`rotate-secrets.md#sc-dns-tunnel`).
5. Issue replacements with the rotation sections (`rotate-secrets.md`), seal the bundle.
6. OpenTofu passphrase: `rotate-secrets.md#sc-tofu-encryption`, and rotate `SC-OBJECT-STORAGE/tofu-state`.

### object-storage

Classes: `SC-OBJECT-STORAGE/*`, `SC-AGE-BACKUP`.
1. Revoke the key at the provider (or detach its policy). For `admin`, revoke every instance (they may have been re-issued by the attacker): list all keys of the account and delete unknown ones.
2. Check integrity: for the affected buckets, list object versions created or deleted in the window (`./bootstrap exec -- sc-bootstrap buckets audit --since <window start>`); deleted current versions are restorable from noncurrent versions (versioning) — restore them; Object Lock (where enabled) prevents deletion of locked versions.
3. Backups altered or encrypted by an attacker: run the restore tests now (`kubectl -n dr-tests create job --from=cronjob/<rt-…>`), and keep backup generations from before the window (ADR-0554 generation bump if PG paths are tainted).
4. Rotate with `rotate-storage-credentials.md`; for `SC-AGE-BACKUP`, `rotate-secrets.md#sc-age-backup` and re-encrypt retained repository bundles.
5. If `longhorn-backups` leaked, treat `SC-GITHUB-MIRROR` as exposed (its token may be inside the `forgejo-data` volume, plan 17 §13.2) and continue at §identity-and-forge step 5.

### recovery-material

Classes: `SC-AGE-CLUSTER`, `SC-AGE-RECOVERY`, `SC-RECOVERY-PASSPHRASE`, the bundle itself, `SC-BREAK-GLASS-CLUSTER`.
1. Decide what leaked: cluster key only; recovery key; passphrase; a bundle copy **with** its passphrase (= every bundled class).
2. Cluster key only: `rotate-age-key.md#cluster-key`, then rotate every tier-1 value (`gitops/clusters/<cluster>/**`) family by family (this file's other sections and `rotate-secrets.md`). Off-cluster authority (tier 2) and release credentials (tier 1b, encrypted to `SC-AGE-CI`) are unaffected (ADR-0500). `SC-AGE-CI` leaked (bundle and passphrase, or a toolbox tmpfs dump): `rotate-age-key.md#ci-key`, then every tier-1b value (`signing-key-rotation.md#emergency-revocation`, registry push, GHCR, `sc-release` token).
3. Recovery key: `rotate-age-key.md#recovery-key`, then rotate every tier-2 value (`secrets/offcluster/<cluster>/**`) and every tier-1 value.
4. Passphrase only (no bundle copy obtained): `rotate-secrets.md#sc-recovery-passphrase`; old bundle versions in S3 remain readable with the old passphrase, so if S3 `recovery` may also be exposed, continue with step 5.
5. Bundle + passphrase: rotate **every class with `inRecoveryBundle: true`**, then the passphrase, then `#attest`.
6. Both age keys and the passphrase lost (not leaked): the installation cannot be black-started from existing secrets; plan a fresh install with new credentials and data restore from backups (record as a disaster, plan 45).
7. Cluster break-glass key: `rotate-break-glass.md#cluster`; review sshd logs for `sc-breakglass`.

### identity-and-forge

Classes: `SC-AUTHENTIK-*`, `SC-OIDC-CLIENTS`, `SC-BREAK-GLASS-AUTHENTIK`, `SC-BREAK-GLASS-FORGEJO`, `SC-FORGEJO-*`, `SC-FLUX-DEPLOY-KEY`, `SC-GITHUB-MIRROR`, `SC-GITHUB-PROBE`, `SC-GITHUB-MIRROR-READ`; alerts `SCProvenanceStatusForged`, `SCTddProvenanceBypass`, `SCForgeConfigDrift`.
1. Revoke: delete the token (Forgejo user settings or admin API), delete the deploy key, deactivate the account, or roll the OAuth client secret.
2. Forgejo integrity: compare branch protection, webhooks, deploy keys, teams and admins with `forge/forge.yaml` (`./bootstrap exec -- sc-bootstrap forge diff`); revert drift with `forge apply` after review.
3. Merge-gate integrity: list `strange-company/*` statuses posted in the window and their creators (`sc-bootstrap forge statuses --since <start>`); any creator other than `sc-runner` → the Runner re-posts `failure`; list PRs merged in the window and re-verify their provenance (`./bootstrap exec -- sc-runner-admin provenance reverify --since <start>`); revert merges that fail.
4. Code integrity: compare `main` and `release/*` heads with the GitHub mirror and the latest S3 repository bundles (`./bootstrap exec -- sc-bootstrap mirror compare-refs --tags`); a divergence not explained by merged PRs is an incident of its own (`github-mirror-failure.md`).
5. GitHub mirror token: revoke in GitHub; compare GitHub refs (including tags) with Forgejo; rotate (`rotate-forge-credentials.md` §G); force a mirror push only after Forgejo's refs are verified.
6. Authentik: revoke all sessions and tokens (Admin → Sessions); review events (logins, `model_updated`, `policy_execution`) since the window start; rotate per `rotate-secrets.md` / `rotate-break-glass.md`.
7. Replace credentials with `rotate-forge-credentials.md` (plan 16) or `rotate-secrets.md`.

### control-plane-tokens

Classes: `SC-RUNNER-SIGNING`, `SC-RUNNER-HERMES`, `SC-HERMES-API`, `SC-RUNNER-ADMIN`, `SC-RUNNER-WEBHOOK`, `SC-CI-INTENTS`.
1. Emergency stop is already engaged (step 0.2).
2. `SC-RUNNER-SIGNING`: `rotate-secrets.md#sc-runner-signing` with `--emergency` (no overlap); then list `runner.tokens` and `ledger.gateway_tokens` jtis issued in the window that do not belong to known executions (`./bootstrap exec -- sc-runner-admin tokens audit --since <start>`); spending by unknown jtis = §model-provider-keys step 3.
3. `SC-RUNNER-HERMES`/`SC-HERMES-API`: force rotation without overlap; review tasks started by `actor=hermes` in the window; cancel suspicious tasks; review Hermes sessions.
4. `SC-RUNNER-ADMIN`: rotate (`rotate-secrets.md#sc-runner-admin`); review transitions with actor `human:*` and `SCRunnerAdminAccess` alerts in the window.
5. `SC-RUNNER-WEBHOOK`, `SC-CI-INTENTS`: rotate; for intents, re-verify evidence accepted in the window (the Runner re-polls pipelines).
6. Lift the emergency stop only after rotation is verified (`budget-emergency-stop.md` §lift).

### ephemeral-execution-credentials

Classes: `SC-WORKER-DEPLOY-KEY`, `SC-WORKER-TASK-TOKEN`, `SC-GATEWAY-TOKEN/worker`; Runner `SECRET_DETECTED`.
1. Cancel the execution (`agent_cancel` or `./bootstrap exec -- sc-runner-admin executions cancel <id>`): revokes the task and gateway tokens and deletes the deploy key (the Runner does this automatically on `SECRET_DETECTED`).
2. Confirm: `GET /repos/{o}/{r}/keys` has no `sc-<execution-id>` key; the Gateway rejects the jti (`SC_TOKEN_REVOKED`).
3. Quarantine refs pushed with the key: `quarantine-worker.md` (plan 26/27).
4. If the secret appeared in a PR, log or handoff stored outside the Runner's scrubbed copies, remove it there (PR comment edit; Woodpecker log deletion) and record the locations.

### model-provider-keys

Classes: `SC-MODEL-API-KEYS`, `SC-MODEL-USAGE-READ`, `SC-GATEWAY-TOKEN` (Hermes instance); alerts `SCAnomalousSpend`, `SCModelMismatch`.
1. Emergency stop level `all` if spend is ongoing.
2. Revoke the key in the provider console; set a provider-side spend cap if available.
3. Compare provider usage for the window with the Gateway ledger (`./bootstrap exec -- sc-bootstrap ledger reconcile --provider <p> --since <start>`): usage absent from the ledger = the key was used outside Strange Company.
4. Rotate with `rotate-model-credentials.md` (plan 53).
5. `SC-MODEL-USAGE-READ` leaked: revoke it in the provider organisation console; read the provider's organisation audit log for the window (new API keys, member, limit or workspace changes) and undo them; confirm the provider-side hard monthly limit (plan 33 backstop) is unchanged; re-issue with the least scope (`rotate-secrets.md#sc-model-usage-read`). If it was an admin-type key, also rotate every `SC-MODEL-API-KEYS` instance of that provider.

### ci-and-supply-chain

Classes: `SC-WOODPECKER-*`, `SC-CI-INTENTS` (see above), `SC-RELEASE-SIGNING`, `SC-REGISTRY-PUSH`, `SC-REGISTRY-PULL`, `SC-GHCR-PUSH`, `SC-RENOVATE`, `SC-FORGEJO-BOT-TOKENS/sc-release`; alerts `SCUnsignedImageRunning`, release self-check refusals (exit 13).
1. Signing key: `signing-key-rotation.md#emergency-revocation`.
2. Registry push: delete the token; list packages/tags pushed in the window (`sc-release audit-registry --since <start>`); verify their signatures; delete unsigned or unexpected tags (record digests first).
3. Woodpecker agent secret / user tokens: rotate; remove unknown agents (Woodpecker admin → Agents); review pipelines run in the window on the build lane (`sc-lane=build`).
4. Renovate / `sc-release` PR token: delete; close PRs opened in the window for re-review.
5. GHCR: revoke; delete unexpected GHCR versions; confirm `toolbox/image.lock` digests still verify.

### observability-and-notification

Classes: `SC-FORGEJO-METRICS-TOKEN`, `SC-WOODPECKER-METRICS-TOKEN`, `SC-GRAFANA-ADMIN`, `SC-BREAK-GLASS-GRAFANA`, `SC-NOTIFY-TRANSPORT`, `SC-AUTHENTIK-AUDIT-TOKEN`.
1. Rotate per `rotate-secrets.md` / `rotate-break-glass.md#grafana`.
2. Notification transport: rotate at the provider; send a test notification; check that no alerts were suppressed (Alertmanager silences created in the window: `amtool silence query --expired`).

### operator-laptop

1. From another trusted machine: revoke the operator's identity sessions and tokens with `manage-human-access.md` §Lost device (plan 15; Authentik sessions, tokens and authenticators; Forgejo PATs, OAuth grants and `prohibit_login`; Woodpecker and Grafana sessions); rotate `SC-SSH-OPERATOR` using `SC-BREAK-GLASS-CLUSTER` (`rotate-secrets.md#sc-ssh-operator`, `--via-break-glass`). Rebuild the operator's `bootstrap.env` from the bundle (`sc-bootstrap bundle export-env`, plan 50) on the new machine; re-pin node host keys (review DR-29).
2. Treat `bootstrap.env` values as leaked unless the disk was encrypted and the device locked: rotate `SC-INFRA-PROVIDER`, `SC-DNS-API`, `SC-OBJECT-STORAGE/admin`, `SC-GITHUB-MIRROR`, `SC-NOTIFY-TRANSPORT`, `SC-MODEL-API-KEYS`.
3. `/blackstart/<cluster>/kubeconfig` leaked → §cluster-and-node step 3. `recovery-identity.txt` present on the device → §recovery-material step 3. The bundle file alone is safe while the passphrase is not known to the attacker.

## Verification

- Every class named in the incident shows a new generation (`./bootstrap exec -- sc-bootstrap secrets status`).
- Old values are rejected at their issuers (each section's revocation check).
- `recovery-bundle status` = `currency: ok`.
- The triggering alert is resolved and no related alert fired in the following 24 h.
- Emergency stop lifted (if engaged) and a fake execution completes.

## Rollback / abort

Revocations are not rolled back. If a rotation step fails, continue forward with the class's rotation procedure; keep the emergency stop engaged until all money- or code-capable credentials are replaced.

## Evidence to record

Incident issue: timeline, exposure window, classes and instances, revocation timestamps and confirmations, audit findings (statuses, merges, refs, resources, spend), evidence object keys under `incidents/<id>/`, new generations and bundle version.

## Follow-ups

- Post-incident PR: a test or control that would have prevented or detected this earlier; update plan 34's residual-risk register if a risk was realised.
- Re-run the break-glass drill if break-glass credentials were involved.
