# Procedure: Woodpecker failure (trusted CI)

**Owning plan:** plans/20-woodpecker.md (CI lanes: plans/52-ci-strategy.md)
**SPEC references:** §36, §50, §54, §96 ("Woodpecker failure"), §113
**Severity / urgency:** outage for engineering flow. Merges and RED/GREEN verification stop. Running production is unaffected, because CI never deploys.
**Expected duration:** 10 min (restarts) to 4 h (§L rebuild; RTO per SPEC §108)
**Requires break-glass:** no for §A–§K. `SC-BREAK-GLASS-CLUSTER` for §L's deletion of `woodpecker-pg`.

## When to use

| Alert / symptom | Section |
|---|---|
| `SCWoodpeckerDown`; `https://ci.<d>/healthz` fails | §A |
| `SCWoodpeckerNoAgents`, `SCWoodpeckerAgentUnavailable`, `SCWoodpeckerBuildAgentUnavailable` | §B |
| Pipelines error immediately ("config", "extension", "signature"); `SCCIConfigExtensionDown`, `SCCIConfigSignatureFailures`, `SCCIRepoPolicyViolation`; step pods rejected by admission | §C |
| `SCCnpgClusterDown{cluster="woodpecker-pg"}`; server logs show DB errors | §D |
| `SCWoodpeckerBacklog`; RED/GREEN waits long | §E |
| `SCCIWorkspaceFilesystemHigh`, `SCCIWorkspaceFilesystemCritical`, `SCCIMaintFailed`; steps `Pending` on PVC | §F |
| Clone steps fail `authentication failed`; statuses missing on Forgejo; login loop | §G |
| A PR has no `ci/woodpecker/pr/ci` status after 10 min | §H |
| `SCTddPipelineFailure`, `SCTddProvenanceBypass`; evidence codes `EVIDENCE_*` | §I |
| `SCRunnerDependencyDown{dep="woodpecker"}` with 401; suspected token or agent-secret leak; unknown agent; **`SCCIBuildLaneAgentMismatch`** (sev1: `sc-ci-config` has already deleted the release secrets) | §J |
| Woodpecker HelmRelease `UpgradeFailed` after a version bump | §K |
| `woodpecker-pg` unrecoverable by PITR | §L |
| `SCCINightlyFailing`, `SCCIOffclusterLaneFailing`, `SCCIOffclusterLaneStale` | §CI lanes |

## Preconditions and required material
- Toolbox (`./bootstrap shell`) with an admin kubeconfig.
- Forgejo admin credential (`SC-FORGEJO-ADMIN`, recovery bundle) for `sc-bootstrap ci login` / `ci verify` (ephemeral `SC-WOODPECKER-ADMIN`).
- `$WP` = `./bootstrap exec -- sc-bootstrap ci api` (authenticated Woodpecker API client, plan 20 §23).

## Safety checks before acting
1. **Do not** relax the trust model to get CI green:
   - never set `WOODPECKER_CONFIG_EXTENSION_EXCLUSIVE=false`;
   - never enable repository `trusted.*` flags;
   - never add privileged plugins;
   - never mount Secrets into `ci-jobs`.
   A CI outage is preferable to untrusted evidence (ADR-0301).
2. **Do not** merge PRs without the required checks. Branch protection blocks it. When the CI outage itself prevents merging the fix (Woodpecker, `sc-ci-config`, heavy-slot advertiser, egress-proxy), the only allowed path is `procedures/emergency-merge.md` (plan 55; GC-02).
3. **Do not** hand-create RED/GREEN evidence. Tasks wait in `*_VERIFYING` and time out into retries or `NEEDS_HUMAN` by plan 22's rules. That is correct behavior.
4. Pause new autonomous work if the outage will exceed 1 h, so retry budgets are not burned on infrastructure failures: `./bootstrap exec -- sc-bootstrap runner dispatch pause` (plan 26; reversible).
5. Record `$WP GET /api/agents`, `kubectl -n woodpecker get pods,hr` and `kubectl -n ci-jobs get pods,pvc` before acting.

## Steps

### §A Server down
A1. `./bootstrap exec -- kubectl -n woodpecker get pods -l app.kubernetes.io/name=woodpecker-server` and `… logs <pod> --previous | tail -100`.

A2. Classify by the log:
- DB connection errors → §D.
- `migration` errors after an upgrade → §K.
- OOMKilled → raise the limit by PR (plan 20 §12) and apply.
- `forge` config errors (OAuth client) → check that Secret `woodpecker/woodpecker-forgejo-oauth` exists and decrypts (`kubectl -n woodpecker get secret woodpecker-forgejo-oauth`). If missing, check Flux (`flux-reconciliation-failure.md` §C).

A3. After the cause is fixed: `./bootstrap exec -- kubectl -n woodpecker rollout restart statefulset/woodpecker-server`. Expected: `https://ci.<d>/healthz` 200 within 3 min, and the agents reconnect (§B1 check).

A4. Running pipelines at the time of the crash end as `killed` or `error`. The Runner re-triggers verification per plan 22 (`EVIDENCE_PIPELINE_LOST`). Humans re-run PR pipelines from the UI (the "restart" button).

### §B Agents not connected
B1. `$WP GET /api/agents`. Expected: two agents with `last_contact` < 60 s, labels `sc-lane=ci` and `sc-lane=build`.

B2. `./bootstrap exec -- kubectl -n woodpecker get deploy woodpecker-agent woodpecker-agent-build` and the agent pod logs:
- `rpc error … unauthenticated` → the agent's registered token is invalid (deleted agent row, or a token file that does not match). Each agent mounts its own token (`woodpecker-agent-token-ci` / `-build`). Re-register with `./bootstrap exec -- sc-bootstrap ci init --only W07,W10` and restart that agent after Flux reconciles (§J3).
- `connection refused :9000` → server down (§A), or a NetworkPolicy from the agents to the server on 9000 is missing (plan 36).
- `forbidden: … cannot create resource "pods" in "ci-jobs"` → the RBAC Role is missing (plan 20 §13.2). Check that the `woodpecker` layer is Ready.

B3. Restart: `./bootstrap exec -- kubectl -n woodpecker rollout restart deploy/woodpecker-agent deploy/woodpecker-agent-build`. Expected: B1 holds within 2 min.

B4. Stale agent rows (agents restarted with an `emptyDir` ID): remove rows with `last_contact` older than 24 h: `$WP DELETE /api/agents/{id}` (admin). This is cosmetic.

### §C Pipelines do not start, or error at configuration
C1. **Extension down:** `./bootstrap exec -- kubectl -n woodpecker get deploy sc-ci-config` and `… logs deploy/sc-ci-config --since=30m`. Restart: `kubectl -n woodpecker rollout restart deploy/sc-ci-config`. Expected: `/readyz` 200, and the next push creates a running pipeline.

C2. **Signature failures** (`401` in the `sc-ci-config` log, `sc_ci_config_signature_failures_total` rising):
- Compare `curl -s http://woodpecker-server.woodpecker.svc:8000/api/signature/public-key` (port-forward) with the fingerprint in `sc_ci_config_signing_key_info`. If they differ, restart `sc-ci-config`; it refetches at start.
- If they match: compare clocks. `kubectl -n woodpecker exec deploy/sc-ci-config -- date -u` and the node time should be within 60 s. Node time sync is plan 08's.
- A changed key without a DB rebuild is unexpected. Treat it as a possible server compromise (§J).

C3. **Repository policy violation** (`REPO_POLICY_VIOLATION`, `SCCIRepoPolicyViolation`):
- `$WP GET /api/repos/lookup/<owner>/<repo>` shows the offending field (`trusted.*` true, or a timeout above the limit).
- Find who changed it in the server log (settings changes are logged with the user).
- Revert it: `./bootstrap exec -- sc-bootstrap ci init --only W04` re-applies `repoSettings`.
- If the change was not made by an operator, treat it as an incident (§J).

C4. **Dropped workflows** (`SCCIUntrustedConstructDropped`): these are expected when a PR tries forbidden constructs. Review the PR. If the PR's intent is legitimate (for example a new workflow that needs a secret), it must be restructured as a protected-mode workflow (plan 23 lane) through a human-reviewed PR. Nothing to repair.

C5. **Admission rejects step pods** (pipeline error `violates PodSecurity` or `ValidatingAdmissionPolicy … denied`):
- Read the denial reason in the step error or `kubectl get events -n ci-jobs`.
- If it follows a Woodpecker upgrade (new pod-spec fields, e.g. an added init container), see §K.
- Otherwise fix `policy/ci.yaml` (`stepSecurityContext`, lane resources) or plan 37's rule by PR. Never loosen PSA on `ci-jobs`.

C6. **Configuration fetch errors** (`sc-ci-config` log `forgejo … 401/404`): the `sc-ci-config` bot token is invalid. Check `SCForgeBotTokenInvalid`, then rotate it with `./bootstrap exec -- sc-bootstrap forge apply --rotate sc-ci-config` (plan 16) and commit by PR.

### §D Database down
D1. `./bootstrap exec -- kubectl -n woodpecker get cluster woodpecker-pg` and follow plan 12's procedures (`cnpg-primary-loss.md`, `cnpg-replica-loss.md`).

D2. After the DB is healthy, restart the server (§A3). Expected: healthz 200, and the agents reconnect.

D3. If PITR is needed (corruption, bad migration): `restore-cnpg.md` (plan 12) to the latest healthy point. Then restart the server. Pipelines after the restore point are lost. The Runner re-polls and re-triggers verification (plan 20 §20.1).

### §E Backlog or slow CI
E1. `$WP GET /api/queue/info` (verify the endpoint) and `kubectl -n ci-jobs get pods -o wide`. Note the pending and running workflows.

E2. Typical causes and actions:

| Cause | Action |
|---|---|
| Step pods `Pending` with `Insufficient memory` / `Insufficient strange.company/heavy-slot` | expected when one job runs at self-hosting-minimum. Check the running pipeline isn't hung (E3) |
| A pipeline hung (no log output for 20 min) | cancel it: `$WP POST /api/repos/{id}/pipelines/{n}/cancel` |
| A burst of PR pushes | `cancel_previous_pipeline_events: [pull_request]` already cancels superseded PR runs. Nothing to do |
| The node is CPU-starved by workers | plan 51 priorities. CI runs at `ci` priority above `agent-worker`. Check `kubectl top pods -A` |

E3. Do not raise `CI_MAX_CONCURRENT` above the profile value as a quick fix. It is SPEC §89 policy (plan 51). A change goes through a PR to the profile.

E4. If verification latency exceeds plan 22's timeouts repeatedly, pause dispatch (safety check 4) until the queue drains.

### §F Workspace storage, orphans, maintenance job
F1. `./bootstrap exec -- kubectl -n ci-jobs get pvc` and `df -h /var/lib/sc/ci-workspace` on the node (`./bootstrap exec -- ssh <node> df -h /var/lib/sc/ci-workspace`, plan 08 SSH access).

F2. Run the sweeper now: `./bootstrap exec -- kubectl -n woodpecker create job --from=cronjob/sc-ci-maint sc-ci-maint-manual-$(date +%s)`. Expected: the Job completes, and its log lists the deleted orphans and pruned logs.

F3. If the filesystem is still above 80% with no running pipelines: list the directories under `/var/lib/sc/ci-workspace` whose PVC no longer exists (`kubectl get pv -o json | jq -r '.items[].spec.local.path // .items[].spec.hostPath.path'` vs `ls`). Delete only directories with no PV, on the node, as root, after recording their names. This is safe: workspaces are disposable (ADR-0305).

F4. `SCCIMaintFailed`: read the Job log. A 401 means `SC-WOODPECKER-MAINT` is invalid → §J2 (rotate `maint`). Drift findings → §C3.

### §G Forge token or login broken
G1. Symptoms: all clone steps fail with `authentication failed`; Forgejo shows no Woodpecker statuses; or human login loops back to `/login`.

G2. `sc-woodpecker`'s stored Forgejo OAuth token is expired or revoked (refresh failed, e.g. after a Forgejo DB restore or an OAuth app secret rotation). Re-establish it: `./bootstrap exec -- sc-bootstrap ci login sc-woodpecker`. It sets a fresh password, runs the headless OAuth flow and updates the stored forge token. Expected: the next pipeline's clone step succeeds.

G3. Human login loop: check the OAuth app (`$FJ GET /api/v1/user/applications/oauth2` as `sc-woodpecker`, plan 16 16-AC-15). The redirect URI must be `https://ci.<d>/authorize`, and the client ID must equal `WOODPECKER_FORGEJO_CLIENT`. If the client secret was regenerated, update the SOPS Secret by PR (`rotate-secrets.md#sc-woodpecker-oauth`) and restart the server.

G4. Users outside the orgs get `org_access_denied`. That is intended (`WOODPECKER_ORGS`).

### §H Webhooks not delivered
H1. Forgejo: repository Settings → Webhooks → the Woodpecker hook → recent deliveries. Or `$FJ GET /api/v1/repos/<o>/<r>/hooks/<id>` plus the delivery history in the admin UI.

H2. Typical causes:
- Delivery error `host not allowed`: Forgejo `[webhook] ALLOWED_HOST_LIST` must include `woodpecker-server.woodpecker.svc.cluster.local` (plan 16 interface).
- `connection refused`: server down (§A), or a NetworkPolicy from `forgejo` to `woodpecker:8000` is missing (plan 36).
- `401`/`404`: the hook was created by an older activation (after §L). Re-run `./bootstrap exec -- sc-bootstrap ci init --only W03,W08` (re-activate, clean up stale hooks).

H3. Re-trigger the PR's pipeline by pushing an empty commit or using Forgejo "redeliver". RED/GREEN verification does not depend on webhooks (API-triggered).

### §I Verification evidence failures
I1. Get the code from the task's history (`GET /v1/tasks/{id}/transitions`, plan 26) or the Runner log line `verification`.

I2. Follow `tdd-task-recovery.md` (plan 21) for task-level actions. The Woodpecker-side checks are:

| Code | Woodpecker-side check |
|---|---|
| `EVIDENCE_PIPELINE_LOST` | §A/§D happened; nothing else |
| `EVIDENCE_COMMIT_MISMATCH` | the agent branch moved after the trigger. Check the Runner's ref audit (`SCRunnerUnexplainedRefChange`) and whether a second execution pushed |
| `EVIDENCE_MISSING`/`TRUNCATED` | open the `sc-verify` step log in the UI. Was the step killed (timeout, OOM)? Compare `exit_code` and the pod events |
| `EVIDENCE_IDENTITY_MISMATCH:I5` | the served configuration differs from the base revision's `verify.yaml`: check `sc-ci-config`'s log line for that pipeline (`config_sha256`, `config_revision`) |
| `EVIDENCE_AMBIGUOUS` | **security event**: the code under test printed a second evidence block. Do not retry automatically. The Runner has classified it as `policy`. Hand it to the reviewer or human with the step log (`SCTddProvenanceBypass`) |

I3. Never re-run a verification pipeline manually from the UI expecting it to count. Only Runner-created pipelines with intents count (ADR-0302).

### §J Credential rotation or compromise
J1. **`SC-WOODPECKER-RUNNER`** (Runner gets 401, or suspected leak): `./bootstrap exec -- sc-bootstrap ci rotate runner`. It signs in as `sc-runner`, calls `DELETE /api/user/token` (invalidates all `sc-runner` tokens), writes the new SOPS file, and opens a PR. After merge, Flux updates `strange-company/agent-runner-woodpecker`, and the Runner reloads it within 60 s. Expected: `SCRunnerDependencyDown` clears. On compromise, also follow `credential-compromise.md` §ci-and-supply-chain: mark evidence from pipelines created after the exposure for re-verification (plan 22).

J2. **`SC-WOODPECKER-MAINT`:** `./bootstrap exec -- sc-bootstrap ci rotate maint` (same flow for `sc-woodpecker`). Then run §F2 to confirm.

J3. **`SC-WOODPECKER-AGENT`** (rogue agent seen in `/api/agents`, `BUILD_LANE_AGENT_MISMATCH` from `sc-ci-config`, or a leaked per-agent token):
1. Immediately delete the affected agent's row (`$WP DELETE /api/agents/{id}`). Its token stops working, and the other lane keeps running.
2. Re-register it and commit the new token by PR: `./bootstrap exec -- sc-bootstrap ci init --only W07,W10` (`rotate-secrets.md#sc-woodpecker-agent`). After merge, restart that agent.
3. Remove every agent row not matching the two Deployments (`$WP DELETE /api/agents/{id}`). If the server-only system secret may also be exposed, rotate it by PR and restart the server.
4. If a rogue agent ran any `release` workflow (`$WP GET /api/repos/{id}/pipelines?event=tag`, and check the agent per workflow), rotate `SC-RELEASE-SIGNING` and `SC-REGISTRY-PUSH` (plan 23).

J4. **`SC-CI-INTENTS`:** `./bootstrap exec -- sc-bootstrap ci rotate intents`. It writes both SOPS copies with dual-accept (old and new) for 1 h, then removes the old value in a follow-up PR.

### §K Upgrade failure
K1. The HelmRelease is class `stateful-app`, so there is no automatic rollback (ADR-0304). Follow `flux-reconciliation-failure.md` §E3.

K2. If the new version changed behavior that plan 20 depends on, the post-upgrade check shows it. Run `./bootstrap exec -- sc-bootstrap ci verify --post-upgrade` and read which of the listed ACs failed (extension payload, agent env names, workspace PVC, status context, API paths). Revert the version PR if the migration did not run (§E3b there). Otherwise fix forward, or restore `woodpecker-pg` to the pre-upgrade backup (the backup-first record) and revert.

### §L Database unrecoverable: rebuild
L1. Confirm that PITR is impossible (plan 12 `restore-cnpg.md` exhausted). Pause dispatch (safety check 4).

L2. Follow plan 20 §20.2:
1. With break-glass, delete `woodpecker-pg` (prune-protected).
2. Commit a generation bump for `CNPG_WOODPECKER_GEN` (plan 12) by PR. CI is down, so land the reviewed commit with `procedures/emergency-merge.md` (plan 55; GC-02), which records the action and raises an always-notify alert.
3. Flux creates an empty cluster, and the server initializes.

L3. `./bootstrap ci-init --recover`. Expected:
- W01–W04 recreate users, activations and settings;
- W08 removes stale Forgejo hooks;
- W05/W06 open a PR with the new tokens (`CI_OUTPUTS_REQUIRE_PR`).
Merging that PR uses `procedures/emergency-merge.md` if no CI is available yet. Otherwise use the normal path.

L4. Plan 23: `./bootstrap exec -- sc-bootstrap ci-secrets apply` re-creates the release secrets and crons.

L5. Resume dispatch: `./bootstrap exec -- sc-bootstrap runner dispatch resume`.

### CI lanes
- **`SCCINightlyFailing`:**
  - Open the `nightly` pipeline (`$WP GET /api/repos/{id}/pipelines?event=cron`).
  - `test-flaky-scan` failure: fix the flaky test or quarantine it by PR (plan 52 §9.3). Quarantine is never allowed for acceptance tests.
  - `locks-verify-online` failure: a pinned artifact vanished or changed upstream. Open a Renovate or manual PR.
  - Expired quarantine: renew (≤ 14 days) or remove by PR.
- **`SCCIOffclusterLaneFailing`:** open the failed GitHub Actions `e2e` run (`gh run list --workflow e2e`). Fix forward by PR on Forgejo. Releases stay blocked until a green run exists for the release commit (plan 52 §8.5).
- **`SCCIOffclusterLaneStale`:** check, in order:
  1. the mirror is current (`github-mirror-failure.md`);
  2. Actions is enabled on the GitHub repository and minutes remain (GitHub billing page);
  3. the workflow is not disabled (GitHub disables scheduled workflows after 60 days without repository activity; re-enable it).

## Verification
- `curl -fsS https://ci.<d>/healthz` returns 200. `$WP GET /api/agents` shows two fresh agents.
- A test PR on `sc-e2e/ci-probe` (or the next real PR) gets `ci/woodpecker/pr/ci` within 30 min (queue permitting).
- `fake-red-valid` on `sc-e2e/fake-target` reaches `RED_VERIFIED` (plan 20 20-AC-28) when the Runner exists (C04+).
- `ALERTS{alertname=~"SCWoodpecker.*|SCCI.*",alertstate="firing"}` is empty.
- Dispatch resumed if it was paused.

## Rollback / abort
- Restarts and `ci-init` steps are idempotent.
- Token rotations take effect at PR merge. Until then the old token keeps working, except when it was revoked first (compromise case).
- §F3 deletions and §L deletion are irreversible by design (disposable workspaces, unrecoverable DB).

## Evidence to record
- Alert(s), start and end times, sections executed, commands run (shell history).
- The affected pipelines (repository, number), the tasks affected (IDs and codes), and whether dispatch was paused.
- Every PR used (rotation, settings, generation bump), and every break-glass use.

## Follow-ups
- §C3, §I `EVIDENCE_AMBIGUOUS` and §J compromises: incident review (plan 34 process).
- Any Woodpecker behavior change found in §K: update plan 20 §7.2/§9.4 and the `make e2e-woodpecker` assertions.
- Repeated §E backlogs: plan 51 capacity review (heavy-slot, `-race` lever in plan 52 §12).
