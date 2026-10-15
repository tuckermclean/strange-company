# Procedure: Rotate secrets (index and per-class rotations)

**Owning plan:** plans/35-secrets.md
**SPEC references:** §98, §99, §111, §113 (rotate model / Forge / storage / age / break-glass credentials), §22
**Severity / urgency:** routine (scheduled) · degraded (rotation overdue) · outage when used for a compromise (then follow `credential-compromise.md` first)
**Expected duration:** 10–30 min per tier-1 class (mostly CI and Flux wait); 30–60 min for bundled classes
**Requires break-glass:** no for tier-1 classes (normal PR flow); yes (`SC-K3S-ADMIN-KUBECONFIG` via `./bootstrap shell`) for out-of-band Secrets (`sops-age`) and node-level classes

## When to use

- `SCSecretRotationDue` (info, 14 days ahead) or `SCSecretRotationOverdue` (warning) names a class.
- `SCRecoveryBundleOutdated` or `SCRecoveryBundleVerifyFailed` → [§bundle-refresh](#bundle-refresh).
- An operator change (custodian leaves), a provider-forced rotation, or the annual schedule.
- A suspected compromise: **start with `credential-compromise.md`** (revoke first), then return here for the replacement.

Specialised procedures (this file links to them):

| Class family | Procedure |
|---|---|
| `SC-AGE-CLUSTER`, `SC-AGE-RECOVERY` | `rotate-age-key.md` |
| `SC-OBJECT-STORAGE` (all instances) | `rotate-storage-credentials.md` |
| `SC-BREAK-GLASS-*`, `SC-GRAFANA-ADMIN` | `rotate-break-glass.md` |
| `SC-FORGEJO-ADMIN`, `SC-FORGEJO-APP-SECRETS`, `SC-FORGEJO-SSH-HOST-KEY`, `SC-FORGEJO-BOT-TOKENS`, `SC-FLUX-DEPLOY-KEY`, `SC-GITHUB-MIRROR`, `SC-GITHUB-PROBE`, `SC-GITHUB-MIRROR-READ`, `SC-GITHUB-PROJECT-MIRROR`, `SC-GITHUB-IMPORT`, `SC-RENOVATE`, `SC-WOODPECKER-OAUTH` | `rotate-forge-credentials.md` (plan 16/17) |
| `SC-MODEL-API-KEYS`, `SC-GATEWAY-TOKEN` | `rotate-model-credentials.md` (plan 53) |
| `SC-RELEASE-SIGNING` | `signing-key-rotation.md` |

## Preconditions and required material

- A clone of `strange-company` at `origin/main`, the toolbox (`./bootstrap shell`), and your own Forgejo account with approve/merge rights (the tool opens rotation PRs as `sc-admin`; your approval and merge are the human action, SPEC §21).
- The recovery passphrase (every rotation that changes a bundled class, and every rotation that must decrypt a tier-1 file).
- For tier-2 classes: access to the issuer (provider console, Cloudflare, GitHub).
- Check current state first: `./bootstrap exec -- sc-bootstrap secrets status` (generation, `rotated_at`, max age, due date per class) and `./bootstrap exec -- sc-bootstrap recovery-bundle status`.

## Safety checks before acting

1. `./bootstrap status` shows no run in progress (exit 0, no lock). A rotation while a bootstrap run holds the lock exits 6.
2. `flux get kustomizations -A` (in `./bootstrap shell`): every Kustomization Ready. Do not rotate a tier-1 class while Flux is failing; you could not tell a rotation failure from the existing one.
3. For bundled classes, the bundle must be current: `recovery-bundle status` reports `currency: ok`. If not, run [§bundle-refresh](#bundle-refresh) first.
4. Never delete or revoke the old value before the new one is verified (35-R-7), unless `credential-compromise.md` told you to.
5. Never edit a SOPS file by hand outside `sops`/`sc-bootstrap`; never commit plaintext. `make secrets-lint` and `make secrets-scan` must pass locally before pushing (`./bootstrap exec -- make secrets-lint secrets-scan`).

## Steps

### Generic flow G-1: tier-1 SOPS secret (rotated by PR)

Applies to every class whose authoritative storage is `sops-git` under `gitops/clusters/<cluster>/`.

1. `./bootstrap rotate secret <CLASS>[/<instance>]` — prompts for the passphrase, generates the new value (or prompts for it when the issuer creates it), writes the SOPS file(s), bumps `strange.company/secret-generation` on the Secret and `strange.company/secret-generations` on the consuming workloads listed in the class section, and pushes branch `rotate/<class>-<UTC timestamp>` and opens the PR **as `sc-admin`** (credentials from the bundle), so that you can approve and merge it yourself (no self-approval deadlock; plan 35 §14.5.1).
   *Expected:* exit 75 ("waiting on human") with the PR URL. *If exit 9:* lint/scan failed; read the named check; do not bypass.
2. Wait for Woodpecker `ci` on the PR to be green; get one approval; merge.
3. Wait for Flux: `flux get kustomizations -A --status-selector ready=false` returns nothing within 10 min; `kubectl -n <ns> get secret <name> -o jsonpath='{.metadata.annotations.strange\.company/secret-generation}'` equals the new generation.
4. Re-run the same command: `./bootstrap rotate secret <CLASS>` resumes, runs the class's verification probe, revokes the old value at the issuer (if any), and writes the ledger entry. *Expected:* exit 0, `rotated: <CLASS> generation N+1`.
5. If the class is bundled (`bundleUpdateRequired`), the command seals a new bundle before exiting; confirm with `recovery-bundle status` (`currency: ok`).

*Idempotent:* re-running at any point resumes from the journal entry `rotation/<class>/<instance>`.

### Generic flow G-2: bundled or off-cluster secret (tier 2)

1. `./bootstrap rotate secret <CLASS>[/<instance>]` — the tool stages a sealed candidate bundle containing **both** old and new values before contacting the issuer.
2. Create the new credential at the issuer when prompted (console or API; the tool performs API-capable issuers itself). Old and new are valid at once (overlap).
3. The tool updates node files (Ansible re-run of the affected role) or in-cluster copies (tier-1 PR, as in G-1) where the class has them, verifies use of the new value, revokes the old value, writes `secrets/offcluster/<cluster>/…` (PR), seals the final bundle, uploads it, re-opens the S3 copy and updates the pointer, inventory and ledger.
4. *Expected:* exit 0; `recovery-bundle status` shows the new `bundle_version` and `currency: ok`.

### Generic flow G-3: issuer-automatic

The issuing software rotates (k3s certificates, cert-manager CA, Runner-managed Hermes credentials). The procedure only forces or verifies it, as described per class.

### Generic flow G-4: ephemeral (per-use)

No rotation exists; containment = revoke the current value (cancel the execution, delete the key). Use the class section.

### bundle-refresh

Re-seal the bundle after a bundled value changed outside the tool, or when a check reports it outdated or broken.

1. `./bootstrap exec -- sc-bootstrap recovery-bundle status --json` and read `currency.mismatches[]` (class, ledger generation, inventory generation) and `integrity` (pointer/object/header findings).
2. If `integrity` is not `ok` but a valid older version exists (`bundles/` listing): `./bootstrap rotate refresh-bundle --from-version <N>` re-seals from that version plus the current `bootstrap.env` values.
3. Otherwise `./bootstrap rotate refresh-bundle`. *Expected:* exit 0, new `bundle_version`.
4. Trigger the checks: `kubectl -n dr-tests create job --from=cronjob/recovery-bundle-currency rbc-manual-$(date +%s)` and the same for `restore-test-recovery-bundle`; both complete with exit 0; the alerts resolve within one evaluation interval.

### recovery-bundle

Entry point used by `bootstrap-verify.md` (V-32), `safe-destruction.md` and `recover-opentofu-state.md`.

1. **S3 copy missing or digest differs, local copy intact:** `./bootstrap rotate refresh-bundle` re-seals the current contents as version N+1 and uploads it (pointer, inventory and ledger updated). *Expected:* `./bootstrap verify --only V-32` passes.
2. **New passphrase wanted (or required after a recovery):** `./bootstrap rotate recovery-bundle` — see [§sc-recovery-passphrase](#sc-recovery-passphrase).
3. **Contents outdated or integrity failure:** [§bundle-refresh](#bundle-refresh).

### Per-class sections

#### sc-k3s-token
Flow G-2 (node-level). On compromise or custodian change only.
1. Take a pre-rotation etcd snapshot: `./bootstrap exec -- sc-bootstrap etcd snapshot --name pre-token-rotation` (plan 07).
2. `./bootstrap rotate secret SC-K3S-TOKEN` → runs `k3s token rotate --new-token <new>` on one server (k3s ≥ 1.28, verify), re-renders the cluster file on every server and restarts servers one at a time (plan 07 §19).
3. The bundle keeps `token_previous` for 30 days (snapshots taken before rotation need it).
*Verify:* `kubectl get nodes` all Ready; a test `k3s etcd-snapshot` succeeds; `recovery-bundle status` lists `SC-K3S-TOKEN` generation N+1.

#### sc-k3s-server-ca
Do **not** rotate in place on compromise; follow `credential-compromise.md#cluster-and-node` (rebuild from Git). Planned CA rotation (rare): plan 07 `k3s certificate rotate-ca` procedure, then re-fetch `SC-K3S-ADMIN-KUBECONFIG` ([§sc-k3s-admin-kubeconfig](#sc-k3s-admin-kubeconfig)).

#### sc-k3s-encryption-config
Annual. `./bootstrap exec -- sc-bootstrap k3s secrets-encrypt-rotate` (plan 07 §19.4: pre-snapshot, `k3s secrets-encrypt rotate-keys` on one server, wait for `k3s secrets-encrypt status` to show re-encryption finished, rolling restart). Ledger entry written by the tool. *Verify:* `k3s secrets-encrypt status` = `Enabled`, hash match on all servers.

#### sc-k3s-admin-kubeconfig
G-3. Per-operator 30-day certificate (`CN=sc-operator:<name>`, plans 07/50, AUTH-7); refreshed automatically when < 7 days remain: `./bootstrap exec -- sc-bootstrap kubeconfig refresh` (CSR in tmpfs, signed over SSH by the k3s client CA, written to `/blackstart/<cluster>/kubeconfig`). Revoke all operator certificates at once (compromise or custodian change): bump `k3s_operator_generation` (PR, plan 07) and run `./bootstrap configure --tags k3s`, then refresh. `k3s.yaml` never leaves the node. *Verify:* `./bootstrap exec -- kubectl auth whoami` prints `sc-operator:<name>`; an old-generation certificate gets 403.

#### sc-ssh-operator
Annual or on custodian change. 1. Generate a new key pair locally (ed25519; hardware-backed preferred). 2. Set `SSH_PRIVATE_KEY_PATH` to it **and** keep the old key loadable — or, if the old key is lost (operator machine loss, review DR-29), use the cluster break-glass key from the bundle (`--via-break-glass`) for this one run. 3. `./bootstrap rotate secret SC-SSH-OPERATOR --new-public-key <path.pub>` → Ansible adds the new key to `authorized_keys` on every node, tests login with the new key, then removes the old one. *Verify:* `ssh -i <new> <admin>@<node> true` succeeds on every node; the old key is refused.

#### sc-lab-ssh
G-4: generated and destroyed per CI lab run; nothing to rotate. If a lab key leaks, destroy the lab VMs (`make lab-destroy`).

#### sc-infra-provider
G-2. 1. In the provider console create a new application key/secret and consumer key with the same access rules (plan 04). 2. Update `bootstrap.env`. 3. `./bootstrap rotate secret SC-INFRA-PROVIDER` (verifies with `/me`, re-runs `./bootstrap plan` read-only, seals the bundle). 4. Revoke the old consumer key in the console. *Verify:* `./bootstrap plan` exits 0 with "no changes".

#### sc-ci-ovh-readonly
1. Create a GET-only OVH credential. 2. `./bootstrap rotate secret SC-CI-OVH-READONLY` writes `secrets/ci/<cluster>/ci-ovh-readonly.sops.yaml` (PR) and applies the Woodpecker secret (`sc-bootstrap ci-secrets apply`). 3. Revoke the old credential. *Verify:* the next `provider-contract` cron pipeline succeeds.

#### sc-dns-tunnel
G-1 + bundle. `./bootstrap rotate secret SC-DNS-TUNNEL` rotates the tunnel token through the Cloudflare API (`SC-DNS-API`), writes the SOPS file, PR, merge; cloudflared rolls (< 1 min). Then removes stale connectors. *Verify:* `SCEdgeDown` not firing; every public hostname answers (`./bootstrap verify --only V-17,V-35`).

#### sc-dns-api
G-2 (toolbox only, never in the cluster; review AUTH-13). 1. Create a new provider token with the same permission set (plan 09/10: tunnel edit + zone DNS edit). 2. Update `bootstrap.env`. 3. `./bootstrap rotate secret SC-DNS-API` (verifies `/user/tokens/verify`, writes `secrets/offcluster/<cluster>/dns-api.sops.yaml`, seals the bundle). 4. Roll the old token. *Verify:* `./bootstrap exec -- sc-bootstrap dns diff` exits 0.

#### sc-dns-acme
G-1. 1. Create a new zone- (or challenge-zone-) scoped DNS Edit token. 2. `./bootstrap rotate secret SC-DNS-ACME` → PR for `cert-manager/dns-api-token`; merge. 3. Roll the old token. *Verify:* a test `Certificate` renewal succeeds (`cmctl renew` on a test certificate, plan 10).

#### sc-tofu-encryption
On compromise only. `./bootstrap rotate tofu-passphrase` (plan 06 §20.3: re-encrypt state with the fallback key provider, then drop the old passphrase). *Verify:* `./bootstrap state verify` exits 0 and the raw state object decrypts only with the new passphrase.

#### sc-recovery-passphrase
`./bootstrap rotate recovery-bundle` — prompts old passphrase once and the new one twice (policy: ≥ 20 characters or ≥ 5 words), seals a new version with `reason.trigger: passphrase-change`, uploads, and asks you to re-enter the new passphrase once more against the downloaded copy (plan 50 confirm step). Record the new passphrase offline **before** confirming. Old bundle versions remain under the old passphrase; if the old passphrase may be compromised, follow `credential-compromise.md#recovery-material` (rotate the bundled classes).

#### sc-age-backup
Yearly. `./bootstrap rotate secret SC-AGE-BACKUP` generates a new identity, adds it as `current.agekey` while keeping previous identities in the same SOPS Secret, updates the public recipient ConfigMap `forgejo/backup-age-recipients` (PR), seals the bundle. Previous identities are removed only when the last backup encrypted to them has expired (365 d, plan 44). *Verify:* next `rt-repo-bundle` restore test passes.

#### sc-internal-ca
On compromise or before the root expires: G-1 with plan 10's root rotation. `./bootstrap rotate secret SC-INTERNAL-CA` generates a new root and opens PR 1 that publishes **both** public roots in the `sc-internal-ca` ConfigMaps (trust overlap); after Flux applies and consumers reload, PR 2 switches `secrets/cert-manager/sc-internal-ca-root.sops.yaml` to the new key; cert-manager re-issues every internal certificate; PR 3 removes the old public root. *Verify:* `kubectl get certificates -A` all Ready; internal clients connect (V-16).

#### sc-acme-account
On compromise: deactivate the ACME account at the CA (plan 10), delete `cert-manager/acme-account-key`; cert-manager registers a new account. *Verify:* ClusterIssuer Ready.

#### sc-authentik-bootstrap
G-4: single use. If `SCAuthentikBootstrapAdminActive` fires, re-run `./bootstrap sso-init` (plan 15 A9 tears the bootstrap token down and deactivates `akadmin`). *Verify:* the alert resolves.

#### sc-authentik-secret-key
Never rotated routinely; rotated only on compromise (plan 15 15-R-3). G-1 with `--emergency`. Effect: all Authentik sessions are invalidated (forced re-login); OIDC links to Forgejo/Grafana/Hermes are unaffected because subjects use `sub_mode: user_uuid` (verify on the pinned version that no database field is encrypted with this key before rotating; review AUTH-18). *Verify:* Authentik pods Ready; an OIDC login to Forgejo succeeds.

#### sc-authentik-audit-token
G-1: both SOPS copies (`authentik` and `monitoring`) change in the same PR. *Verify:* `sc_breakglass_probe_success == 1` within 5 min; `authentik-config-check` Job succeeds.

#### sc-synthetic-user
G-2 (bundle + recovery-only SOPS; never in the cluster). `./bootstrap rotate secret SC-SYNTHETIC-USER` generates a new password and TOTP seed, sets them through the admin channel (`sso-init` A5b path, plan 15), writes `secrets/offcluster/<cluster>/synthetic-user.sops.yaml` (PR as `sc-admin`) and seals the bundle (`refresh-bundle`). *Verify:* `./bootstrap verify --only V-20` passes with the new credentials.

#### sc-oidc-clients
G-1, one instance per run (`SC-OIDC-CLIENTS/forgejo|grafana|hermes-dashboard`): the Authentik side and the consumer side change in one PR; login to that application is unavailable for < 1 reconcile interval. *Verify:* OIDC login to the application succeeds (V-20).

#### sc-forgejo-metrics-token
G-1: both copies (`forgejo`, `monitoring`) in one PR. *Verify:* `up{job="forgejo"} == 1` within 2 min.

#### sc-woodpecker-metrics-token
G-1: both copies (`woodpecker`, `monitoring`) in one PR. *Verify:* `up{job="woodpecker"} == 1`.

#### sc-runner-webhook
G-1 (Runner hot-reloads; it re-sets the secret on managed hooks on its next 5-min resync). Deliveries signed with the old key during the switch are rejected and recovered by the resync. *Verify:* `sc_runner_dependency_up{dep="forgejo"} == 1` and no rise in webhook 401s after 10 min.

#### sc-woodpecker-oauth
See `rotate-forge-credentials.md` §D (plan 16).

#### sc-woodpecker-agent
Per instance (`SC-WOODPECKER-AGENT/ci` or `/build`): `./bootstrap exec -- sc-bootstrap ci init --only W07 --rotate <agent>` registers a new agent token (plan 20 W07), writes `secrets/woodpecker/agent-token-<instance>.sops.yaml` (PR), and after Flux applies and the agent reconnects, deletes the old registration. Only that lane pauses (< 5 min). *Verify:* Woodpecker admin lists exactly `woodpecker-agent` and `woodpecker-agent-build`; a pipeline runs on the rotated lane; the next `sc-ci-maint` run reports no `BUILD_LANE_AGENT_MISMATCH`.

#### sc-woodpecker-runner
G-1 via `sc-bootstrap ci-init` token step (plan 20 W05). *Verify:* the Runner's `sc_runner_dependency_up{dep="woodpecker"} == 1`.

#### sc-woodpecker-maint
G-1 via `sc-bootstrap ci-init`. *Verify:* the next `sc-ci-maint` run succeeds.

#### sc-woodpecker-signing
On compromise only. Regenerate the Woodpecker server signing key with the plan 20 mechanism (verify: key stored in `woodpecker-pg`), restart the server, then `kubectl -n woodpecker rollout restart deploy/sc-ci-config` so it refetches `/api/signature/public-key`. *Verify:* a push pipeline is served (`sc_ci_config_signature_failures_total` does not increase).

#### sc-woodpecker-admin
G-4: minted per command and revoked at command end. If a process died holding one, revoke all tokens of the Woodpecker admin user in the Woodpecker UI.

#### sc-woodpecker-user-tokens
G-3 (OAuth refresh). Forced: revoke the OAuth grants of the user in Forgejo (Settings → Applications), then re-run `./bootstrap ci-init` (W01/W02 sign in again). *Verify:* `ci` pipelines clone successfully.

#### sc-ci-intents
G-1: both copies (`woodpecker/sc-ci-config-intents`, `strange-company/agent-runner-ci-intents`) in one PR. Verification pauses until both pods reload (< 5 min). *Verify:* a fake RED verification (C04 test) completes.

#### sc-runner-signing
Three PRs, ≥ 24 h apart (plan 35 §14.5.4):
1. `./bootstrap rotate secret SC-RUNNER-SIGNING --stage add` → PR 1 adds the new public key to `sc-runner-jwks` and the new private key as `next.pem`. Merge; confirm the Gateway lists two kids (`/readyz` reasons empty, metric `sc_gateway_jwks_keys == 2`).
2. After ≥ 24 h: `--stage switch` → PR 2 makes the Runner sign with the new kid.
3. After ≥ 24 h: `--stage retire` → PR 3 removes the old kid.
Emergency (`--emergency`): one PR replacing key and JWKS; running executions fail as infrastructure failures and are revoked; engage emergency stop `workers` first (`budget-emergency-stop.md`).
*Verify:* a fake execution completes after each stage.

#### sc-runner-hermes
G-3: the Runner rotates every 30 days with a 24 h overlap (ADR-0401). Forced: `./bootstrap exec -- sc-runner-admin hermes-credentials rotate [--no-overlap]` through the admin API (port-forward, `SC-RUNNER-ADMIN`). *Verify:* Hermes can call `agent_status`; `SCHermesCredentialRotationFailed` not firing.

#### sc-model-usage-read
Flow G-1, per provider instance (`anthropic`, `openai`). 1. In the provider organisation console, create a new key with the least scope that still reaches the usage/cost endpoints (read-only where offered). 2. `./bootstrap rotate secret SC-MODEL-USAGE-READ --instance <p>` writes `gitops/clusters/<c>/secrets/strange-company/model-usage-read.sops.yaml` (PR, merge, Flux). 3. Trigger `kubectl -n strange-company create job --from=cronjob/ledger-reconcile lr-rotate` and confirm it exits 0. 4. Revoke the old key in the console. *Verify:* the provider console shows one active usage key for Strange Company; `recovery-bundle status` is not affected (not bundled).

#### sc-hermes-api
Same as [§sc-runner-hermes](#sc-runner-hermes) (same Secret, same rotation timer).

#### sc-runner-admin
CD-30: per-operator instances `operator:<login>` are issued and revoked with `sc-bootstrap access join|leave` (365-day expiry; re-join before expiry); the `breakglass` instance is rotated with `rotate-break-glass.md#runner-admin`. Revoke any single credential with `POST /v1/admin/credentials/{kid}:revoke`. The cluster holds hashes only (`secrets/strange-company/runner-admin-credentials.sops.yaml` → Secret `agent-runner-admin`).

#### sc-worker-deploy-key
G-4. Containment: `credential-compromise.md#ephemeral-execution-credentials`.

#### sc-worker-task-token
G-4. Containment: cancel the execution (`agent_cancel` or the admin API) — revokes the jti.

#### sc-pg-app
G-1, one instance per run. The SOPS file changes the role password; CNPG reconciles the role (verify per plan 12); the application restarts via the bumped annotation (< 2 min). *Verify:* the application's readiness probe passes; `psql` with the old password fails (test pod in the namespace).

#### sc-pg-dr-marker
G-1: both copies (cluster namespace and `dr-tests`) in one PR. *Verify:* the next `rt-cnpg-*` restore test passes.

#### sc-notify-transport
G-1 after rotating at the transport provider. *Verify:* `./bootstrap exec -- sc-bootstrap notify test` delivers a test notification (`SCNotificationTest`).

#### sc-registry-push
G-1 into `secrets/ci/<cluster>/registry-push.sops.yaml` then `./bootstrap exec -- sc-bootstrap ci-secrets apply` (Woodpecker secret). Delete the old token in Forgejo (`sc-release` user settings) after the next release dry run (`sc-release publish --dry-run`) succeeds.

#### sc-registry-pull
G-1: one SOPS file per consuming namespace, all in one PR (the tool writes them). *Verify:* `kubectl -n strange-company rollout restart deploy/agent-runner` pulls successfully (or a test pod pulls an internal image in each namespace).

#### sc-ghcr-push
1. Create a new GitHub token (write:packages on the toolbox package). 2. `./bootstrap rotate secret SC-GHCR-PUSH` (SOPS under `secrets/ci/`, PR, `ci-secrets apply`). 3. Revoke the old token. *Verify:* `sc-release publish --dry-run --target ghcr` authenticates.

#### sc-renovate
See `rotate-forge-credentials.md` §B (forgejo instance, plan 16) and replace the `github-com` instance by G-1. *Verify:* `kubectl -n renovate create job --from=cronjob/renovate rn-rot` completes.

## Verification

- `./bootstrap exec -- sc-bootstrap secrets status` shows the class with the new generation and `rotated_at` today.
- `recovery-bundle status` reports `currency: ok` (bundled classes).
- The class-specific check above passes.
- `SCSecretRotationOverdue`/`Due` for that class resolve after the next `secret-rotation-age` run (or trigger it: `kubectl -n dr-tests create job --from=cronjob/secret-rotation-age sra-manual-$(date +%s)`).

## Rollback / abort

- Before the PR merges: close the PR; the tool's journal keeps the staged candidate; re-run with `--abort` to discard it (the issuer-side new value, if created, is revoked by `--abort`).
- After merge but before old-value revocation: revert the PR; Flux restores the old value; re-run with `--abort`.
- After revocation: there is no rollback; complete the rotation forward.
- A sealed bundle version is never deleted; if it is wrong, seal a new one (`refresh-bundle`).

## Evidence to record

The PR URL, the tool's final JSON (`--json`), the ledger entry (class, generation, actor, reason), the new `bundle_version` (bundled classes) and the verification output, attached to the rotation issue (label `secret-rotation`).

## Follow-ups

- If the rotation was compromise-driven, complete `credential-compromise.md` (audit and evidence).
- If any step needed manual work not described here, open a PR improving this procedure.
