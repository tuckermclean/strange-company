# Procedure: Rotate Forge credentials

**Owning plan:** plans/16-forgejo.md (§10), with plans/17-github-mirror.md (§G) and plans/15-authentik.md; policy, schedule and the index of all rotations: plan 35 `rotate-secrets.md`
**SPEC references:** §86, §87, §99, §113 ("rotate Forge credentials")
**Severity / urgency:** routine (schedule) · urgent (suspected compromise: follow `credential-compromise.md` first, plan 35)
**Expected duration:** per section 15–45 min, plus PR review and Flux reconcile time
**Requires break-glass:** no, except §F uses the bundle (recovery passphrase) and §A/§F update bundle entries.

| Section | Class | Consumers | Downtime |
|---|---|---|---|
| §A | `SC-FORGEJO-ADMIN` (`sc-admin`) | `forge-init --recover`, break-glass-class repairs, chart init (seed value only) | none |
| §B | `SC-FORGEJO-BOT-TOKENS` (`sc-runner`, `sc-renovate`, `sc-backup`, `sc-gc`, `sc-ci-config`, `platform-probes`, `sc-release`, `sc-registry-reader`), `SC-RUNNER-WEBHOOK`, `SC-FORGEJO-METRICS-TOKEN` | Runner, Renovate, backup and retention CronJobs, Woodpecker config extension, probes, release pipeline, kubelet pulls, Prometheus | none for tokens (overlap); webhook/metrics ≤ 1 reconcile interval |
| §C | `SC-FLUX-DEPLOY-KEY` | Flux source-controller | none (overlap) |
| §D | `SC-WOODPECKER-OAUTH` | Woodpecker server | Woodpecker logins and token refresh, ≤ 10 min |
| §E | `SC-FORGEJO-SSH-HOST-KEY` | every SSH client: Flux, Runner, workers, Woodpecker, CI, backup jobs | none if phases are followed; workers paused during phase 2 |
| §F | `SC-BREAK-GLASS-FORGEJO` (`sc-breakglass`) | humans (emergency) | none |
| §G | `SC-GITHUB-MIRROR`, `SC-GITHUB-PROBE`, `SC-GITHUB-MIRROR-READ`, `SC-GITHUB-PROJECT-MIRROR`, `SC-GITHUB-IMPORT` | Forgejo push mirror, `platform-probes`, release gate, RT-6 restore test | mirror gap < 1 min |
| §H | `SC-FORGEJO-APP-SECRETS` (`INTERNAL_TOKEN`, `JWT_SECRET`, `LFS_JWT_SECRET`; **not** `SECRET_KEY`) | Forgejo | restart; sessions/JWTs invalidated |

Worker deploy keys (`SC-WORKER-DEPLOY-KEY`) are ephemeral and never rotated by hand; orphans are handled by `agent-runner-failure.md` (plan 26).

## When to use
- Scheduled rotation per plan 35 (`secret-classes.yaml` `rotation.maxAge`), or `SCGitHubMirrorTokenExpiring` (§G).
- `SCForgeBotTokenInvalid` (§B), `SCForgejoSSHHostKeyMismatch` caused by a planned key change (§E), `FORGE_BREAKGLASS_MISMATCH` from forge-init (§F).
- Suspected exposure of any credential above (after `credential-compromise.md` containment).
- Personnel change for human-known credentials (§A, §F).

## Preconditions and required material
- Toolbox (`./bootstrap shell`) with the operator kubeconfig; `sc-admin` password from SOPS (`./bootstrap exec -- sops -d gitops/clusters/<cluster>/forgejo/forgejo-admin.sops.yaml`).
- Forgejo healthy (`SCForgejoDown` not firing) and Flux reconciling (`SCFluxSourceFailing` not firing). If Forgejo is down, restore it first (`restore-forgejo.md`).
- A human able to review and merge PRs on `strange-company` (protection stage `steady` requires PRs for every SOPS change).
- Recovery passphrase for sections that update the bundle (§A, §F, §G).

## Safety checks before acting
1. Never delete the old credential before the new one is verified in use (every section has an explicit "finalize" step).
2. Never rotate `SECRET_KEY` with this procedure (ADR-0252). It is not rotatable in place.
3. For §E, never accept a changed host key on a client "to make it work"; follow the phases.
4. One credential class per change window, so failures are attributable.
5. Pause workers for §D and §E: `./bootstrap exec -- sc-bootstrap estop set --scope workers --reason "forge credential rotation"` (plan 33 CLI). Clear it at the end.

## Steps

### §A `sc-admin` password
After `forge finalize` the valid `sc-admin` password exists only in the recovery bundle; the SOPS file holds only the **seed** value that creates the account on an empty DB (chart `passwordMode: initialOnlyNoReset`, plan 16 §14.3, AUTH-9).
1. Mint a short-lived admin token: `./bootstrap exec -- kubectl -n forgejo exec deploy/forgejo -- forgejo admin user generate-access-token --username sc-admin --token-name sc-op-<ts> --scopes write:admin` (verify flags).
2. `./bootstrap exec -- sc-bootstrap forge admin-password rotate --token-from-stdin`: stages a new random password, sets it with `PATCH /api/v1/admin/users/sc-admin`, and verifies it by API basic auth (does not update `last_login`).
3. `./bootstrap rotate refresh-bundle` (plan 35/50). Expected: bundle manifest fingerprint for `SC-FORGEJO-ADMIN` changed.
4. Delete the token from step 1 (`DELETE /api/v1/users/sc-admin/tokens/sc-op-<ts>`). Expected: `sc_forge_unexpected_admin_tokens` = 0 within 5 min.
5. Seed value: rotate it only if it leaked. One PR replaces `gitops/clusters/<cluster>/forgejo/forgejo-admin.sops.yaml`; no restart is needed, because the chart does not reset existing passwords. The old password fails: `curl -s -o /dev/null -w '%{http_code}' -u sc-admin:<old> http://127.0.0.1:13000/api/v1/user` = 401 (through port-forward).

### §B Bot tokens, webhook HMAC, metrics token
Bot token (`<bot>` ∈ `sc-runner`, `sc-renovate`, `sc-backup`, `sc-gc`, `sc-ci-config`, `platform-probes`; `sc-release` and `sc-registry-reader` follow the same steps, but their files are `secrets/ci/<cluster>/sc-release-forgejo.sops.yaml` (applied to Woodpecker with `./bootstrap exec -- sc-bootstrap ci-secrets apply`, plan 23) and `secrets/<ns>/sc-registry-pull.sops.yaml` in every consuming namespace):
1. `./bootstrap exec -- sc-bootstrap forge apply --rotate <bot>`. It mints `<bot>-<UTC timestamp>` with the declared scopes (plan 16 §7.4) **without** deleting the old token, writes the SOPS file of §4.4 (plan 16) on branch `rotate/<bot>-<ts>`, and pushes it. Expected output: new token name and the PR URL.
2. Review and merge the PR. `./bootstrap exec -- flux reconcile kustomization <consumer layer> --with-source` (Runner: `strange-company`; Renovate: `renovate`; backup and `sc-gc`: `forgejo-config`; `sc-ci-config`: `woodpecker`; probes: `platform-probes`).
3. Restart long-running consumers so they read the new Secret: `./bootstrap exec -- kubectl -n strange-company rollout restart deploy/agent-runner` (sc-runner), `./bootstrap exec -- kubectl -n monitoring rollout restart deploy/platform-probes` (platform-probes), `./bootstrap exec -- kubectl -n woodpecker rollout restart deploy/sc-ci-config` (sc-ci-config). CronJob consumers pick it up at their next run; trigger one: `./bootstrap exec -- kubectl -n forgejo create job --from=cronjob/repo-backup repo-backup-rotate-<ts>` (name per plan 44).
4. Verify the consumer works with the new token: Runner `sc_runner_reconcile_last_success_timestamp_seconds` advances and its log has no `401`; probes `sc_forge_bot_token_valid{bot}` = 1; the manual backup Job succeeds.
5. Finalize: `./bootstrap exec -- sc-bootstrap forge apply --rotate <bot> --finalize` deletes every older token with the `<bot>-` prefix. Expected: `GET /api/v1/users/<bot>/tokens` (as the bot, via the tool) lists exactly one token.
Webhook HMAC (`SC-RUNNER-WEBHOOK`, owner plan 26, CD-08):
1. `sc-bootstrap forge apply --rotate webhook-secret` writes the new secret into SOPS `gitops/clusters/<cluster>/secrets/strange-company/agent-runner-webhook.sops.yaml` (Secret `strange-company/agent-runner-webhook`) as key `secret`, moving the current value to `secret_previous` (assumption on plan 26: the Runner accepts either during rotation). PR, merge, Runner restart.
2. `sc-bootstrap forge apply` then updates the org webhooks to the new secret. Verify a delivery succeeds (`Forgejo admin → hooks → recent deliveries` 2xx, or Runner log `webhook verified`).
3. Finalize: a PR removes `secret_previous`. If plan 26 does not support two secrets, accept that deliveries fail between steps 1 and 2 (≤ 1 reconcile interval); the Runner's polling fallback covers them (plan 16 F16).
Metrics token (`SC-FORGEJO-METRICS-TOKEN`): one PR changes both SOPS copies (`forgejo` and `monitoring`) to the same new value; restart Forgejo (§A step 3) so `[metrics] TOKEN` updates. Expected: `up{job="forgejo"}` returns to 1 within 2 scrape intervals.

### §C Flux read-only deploy key
1. `./bootstrap exec -- sc-bootstrap forge apply --rotate flux-deploy-key`: generates ed25519 in the toolbox, adds it to `strange-company/strange-company` as read-only deploy key `flux-readonly-<ts>` (the old `flux-readonly*` key stays), and rewrites SOPS `gitops/clusters/<cluster>/flux-system/flux-source-auth.sops.yaml` (Secret `flux-system/flux-system`, CD-08; `identity`, `identity.pub`, unchanged `known_hosts`) on branch `rotate/flux-key-<ts>`.
2. PR, merge. Flux fetches this commit with the **old** key, then applies the Secret with the new key.
3. `./bootstrap exec -- flux reconcile source git flux-system -n flux-system`. Expected: Ready, artifact revision = Forgejo `main`. Confirm the new key was used: `./bootstrap exec -- kubectl -n flux-system get secret flux-system -o jsonpath='{.data.identity\.pub}' | base64 -d | ssh-keygen -lf -` equals the new fingerprint.
4. Finalize: `sc-bootstrap forge apply --rotate flux-deploy-key --finalize` deletes the old deploy key. Re-run step 3: still Ready. 16-AC-13's checks (exactly one `flux-readonly*` key, `read_only: true`, push rejected) pass with the new key.
5. Update `config/forge-init.yaml` deploy-key fingerprint in the same or a follow-up PR (the finalize step prints the diff).

### §D Woodpecker OAuth application secret
1. Pause workers (safety check 5). Announce: Woodpecker logins fail for up to 10 min.
2. `./bootstrap exec -- sc-bootstrap forge apply --rotate woodpecker-oauth`: as `sc-woodpecker`, regenerates the client secret (`PATCH /api/v1/user/applications/oauth2/{id}`; the old secret stops working immediately), and writes SOPS `woodpecker/woodpecker-forgejo-oauth` on branch `rotate/woodpecker-oauth-<ts>`.
3. Merge the PR promptly (pre-arranged reviewer). `./bootstrap exec -- flux reconcile kustomization woodpecker --with-source`; `./bootstrap exec -- kubectl -n woodpecker rollout restart deploy/woodpecker-server`.
4. **Re-authorize repositories (mandatory, plan 20):** the forge OAuth tokens Woodpecker stored for `sc-woodpecker` were issued to the old client secret and cannot be refreshed. Run `./bootstrap ci-init` (plan 20): W02 signs in again as `sc-woodpecker` (fresh password set through the admin API, headless OAuth, password discarded), and W03 repairs every repository activation (webhook and repository token). Expected: every step `verified` or `updated`; Woodpecker lists every managed repository as active.
5. Verify: `https://ci.<d>` login via Forgejo works (humans re-login once); a Runner-triggered no-op pipeline (`./bootstrap exec -- sc-bootstrap ci smoke`, plan 20) clones, runs and posts its status on Forgejo.
6. Clear the worker pause.

### §E Forgejo SSH host key (phased; consequences for every SSH client)
Clients that pin the host key: Flux (`flux-system/flux-system` key `known_hosts`, file `flux-source-auth.sops.yaml`), ConfigMap `forgejo-known-hosts` in `strange-company` (Runner), `agent-jobs` (workers), `woodpecker`, `ci-jobs`, and the backup jobs in `forgejo`; plus `platform-probes` (`sc_forgejo_ssh_hostkey_match`) and `config/forge-init.yaml`. Forgejo's built-in SSH server presents one ed25519 key, so the overlap is on the **client** side.
- **Phase 1 — clients accept both keys.** `./bootstrap exec -- sc-bootstrap forge hostkey stage`: generates the new ed25519 key into a staged SOPS file (not yet mounted), and on branch `rotate/ssh-hostkey-<ts>-p1` adds the new public key as a second `known_hosts` line to every pinned location above. PR, merge, Flux reconcile all layers. Verify every client still works with the old key: `flux get sources git` Ready; Runner clone smoke (`sc-bootstrap forge clone-test --as runner`); a fake-harness execution clones (plan 28). Restart the Runner so it reloads `known_hosts`.
- **Phase 2 — server switches key.** Pause workers (safety check 5). PR `…-p2` replaces SOPS `SC-FORGEJO-SSH-HOST-KEY` with the staged key and updates the probe's expected fingerprint and `config/forge-init.yaml`. Merge; Flux applies; `./bootstrap exec -- kubectl -n forgejo rollout restart deploy/forgejo`. Verify: `ssh-keyscan -t ed25519 -p 22 forgejo-ssh.forgejo.svc.cluster.local` (from a toolbox pod) returns the new key; `flux reconcile source git flux-system` Ready; clone smoke tests pass; `sc_forgejo_ssh_hostkey_match` = 1. Clear the worker pause.
- **Phase 3 — remove the old key.** PR `…-p3` removes the old `known_hosts` line everywhere. Merge, reconcile, restart the Runner. Verify again as in phase 2.
- Black start uses the SOPS key from Git, i.e. the new key from phase 2 onward. Old keys remain only in Git history (encrypted).
- Never skip phase 1: Flux would fail with `knownhosts: key mismatch` and stop reconciling until fixed.

### §F Forgejo break-glass password (`sc-breakglass`)
1. `./bootstrap exec -- sc-bootstrap bundle stage-rotation SC-BREAK-GLASS-FORGEJO` (≥ 32 random characters, local staging only).
2. `./bootstrap exec -- sc-bootstrap forge breakglass set-password --from-staging`: with a short-lived `sc-admin` token minted as in §A step 1 (deleted afterwards), `PATCH /api/v1/admin/users/sc-breakglass` with `{password, must_change_password: false, login_name: "sc-breakglass", source_id: 0}`.
3. Verify by **API basic auth** (does not update `last_login`, no page): `sc-bootstrap forge breakglass verify --from-staging` → `GET /api/v1/user` 200, `is_admin: true`.
4. `./bootstrap rotate refresh-bundle`. Then `./bootstrap exec -- sc-bootstrap breakglass drill --only forgejo` (this one pages by design; acknowledge it in the rotation record per `break-glass-access.md` §After use step 2, never silence it).
5. Also used for `FORGE_BREAKGLASS_MISMATCH`: if forge-init cannot verify the bundle password, this section re-aligns the account with the bundle; record why they diverged.

### §G GitHub mirror token (`SC-GITHUB-MIRROR`) and probe token (`SC-GITHUB-PROBE`) — plan 17 §14.5
- **G.1** As the mirror machine user on GitHub, create a fine-grained PAT with the same settings as the old one (only `<GITHUB_MIRROR_REPO>`; Contents R/W, Workflows R/W, Metadata R; expiry ≤ 180 days). Put it in `bootstrap.env` `GITHUB_MIRROR_TOKEN`. Do not revoke the old one yet.
- **G.2** `./bootstrap exec -- sc-bootstrap mirror check-github` (PF-16a–e with the new token). Expected: pass; prints the expiry.
- **G.3** `./bootstrap exec -- sc-bootstrap mirror recreate --token-from env`. Expected: mirror deleted and re-created, `sync` succeeded, `verify` passed (GitHub `main` == Forgejo `main`). The gap is < 1 min; the post-create sync covers pushes in the gap.
- **G.4** PR updating `gitops/clusters/<cluster>/config/github-mirror.yaml` `mirrorTokenExpiresAt` (the tool prints the new value). For the **probe token**: create the new read-only PAT (Contents R, Metadata R, Administration R), set `GITHUB_PROBE_TOKEN`, run `./bootstrap exec -- sc-bootstrap secrets rotate SC-GITHUB-PROBE --write-sops` (rewrites `secrets/github/platform-probes-github.sops.yaml`) in the same PR; after merge restart `deploy/platform-probes`.
- **G.4b** The probe token is also the release gate's Woodpecker secret `sc_github_probe_token`: update `secrets/ci/<cluster>/github-probe.sops.yaml` in the same PR and, after merge, `./bootstrap exec -- sc-bootstrap ci-secrets apply` (plan 23).
- **G.5** `./bootstrap rotate refresh-bundle` (bundle holds `SC-GITHUB-MIRROR`).
- **G.6** Revoke the old PATs in GitHub. Then `./bootstrap exec -- sc-bootstrap mirror sync` succeeds, and after ≤ 10 min `sc_github_mirror_last_attempt_success` = 1, `sc_github_api_up` = 1, `SCGitHubMirrorTokenExpiring` resolved.
- **G.7** `SC-GITHUB-MIRROR-READ` (RT-6 restore test): create the new read-only PAT (Contents R, Metadata R), `./bootstrap exec -- sc-bootstrap secrets rotate SC-GITHUB-MIRROR-READ --write-sops` (rewrites `secrets/dr-tests/github-mirror-read.sops.yaml`), PR, merge, then run RT-6 on demand (`./bootstrap exec -- kubectl -n dr-tests create job --from=cronjob/restore-test-github-black-start rt6-rotate-<ts>`) and revoke the old PAT after it passes.
- **G.8** `SC-GITHUB-PROJECT-MIRROR` (CD-43): create the new fine-grained PAT (selected mirrored repositories only; Contents R/W, Metadata R; expiry ≤ 180 days), then for each `projects/*` repository with a `pushMirrors` entry run `./bootstrap exec -- sc-bootstrap forge apply --rotate-mirror projects/<repo>` (the token is passed at invocation, never written to Git), confirm `sc_github_mirror_last_attempt_success{repository="projects/<repo>"}` = 1, then revoke the old PAT. It is not in the recovery bundle. `SC-GITHUB-IMPORT` is per-use: revoke it in GitHub after the import; nothing to rotate.
- On compromise: revoke first (G.6 before G.3), accept the mirror gap, then G.1–G.5; afterwards run `github-mirror-failure.md` §C checks (compare refs) to confirm nothing was pushed with the leaked token.

### §H Forgejo `INTERNAL_TOKEN`, `JWT_SECRET`, `LFS_JWT_SECRET`
1. `./bootstrap exec -- sc-bootstrap secrets rotate SC-FORGEJO-APP-SECRETS --keys INTERNAL_TOKEN,JWT_SECRET,LFS_JWT_SECRET --write-sops` (keeps `SECRET_KEY` byte-identical; the tool refuses otherwise). PR, merge, Flux reconcile, `rollout restart deploy/forgejo`.
2. Verify: healthz `pass`; `sc-bootstrap forge key-hash --source pod` equals `--source git` (SECRET_KEY unchanged); OAuth logins work (users re-login; JWT-signed sessions invalidated).

## Verification
- §A: API basic auth with the new password succeeds; old password 401; bundle manifest fingerprint for `SC-FORGEJO-ADMIN` updated; no `sc-op-*` token left.
- §B: one token per bot; `sc_forge_bot_token_valid{bot}` = 1 for all bots; no `SCForgeBotTokenInvalid`.
- §C: exactly one `flux-readonly*` deploy key, `read_only: true`; `flux-system` GitRepository Ready.
- §D: `./bootstrap ci-init` reports all repositories active; Woodpecker login works; Runner-triggered pipeline succeeds.
- §E: `sc_forgejo_ssh_hostkey_match` = 1; all SSH clients succeed; `known_hosts` everywhere contains only the new key after phase 3.
- §F: API verification 200; drill `forgejo: passed`.
- §G: `mirror verify` exit 0; `sc_github_mirror_last_attempt_success` = 1; old PATs revoked (`GET /user` with the old token = 401).
- `./bootstrap exec -- sc-bootstrap forge verify` exits 0 (no drift) after every section.

## Rollback / abort
- §A/§B/§C/§G/§H: until finalize, the old credential still works. Revert the PR (or recreate the mirror with the old token) to roll back.
- §D: the old client secret is invalid immediately after step 2. Roll forward only (re-run §D, including the `ci-init` re-authorization).
- §E: before phase 2, revert phase 1 PR. After phase 2, roll back by a PR restoring the old key Secret (clients still accept it until phase 3).
- §F: re-apply the old value from the previous bundle version if the new value cannot be sealed into the bundle; never leave bundle and Forgejo disagreeing.

## Evidence to record
Per rotation, `s3://<prefix>-evidence/rotations/<class>/<UTC ts>.json`: class, reason (schedule / compromise / expiry), PR SHAs, old and new fingerprints (sha256 prefixes only, never values), verification results, bundle version (if updated), duration. Plan 35's `SCRecoveryBundleOutdated` check reads bundle currency from the manifest.

## Follow-ups
- Update the next-due date in plan 35's rotation calendar (`rotate-secrets.md`).
- For compromise-triggered rotations, complete `credential-compromise.md` (audit Forgejo branch-protection and deploy-key changes; compare `main` with GitHub and bundles).
