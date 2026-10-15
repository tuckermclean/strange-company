# Procedure: Authentik restore

**Owning plan:** plans/15-authentik.md (with 12 for CNPG mechanics)
**SPEC references:** §21, §22, §26, §108, §109, §113 ("Authentik restore")
**Severity / urgency:** outage (human SSO unavailable; machines unaffected)
**Expected duration:** blueprint repair 15–30 min; PITR restore ≈ 45 min at self-hosting-minimum (RTO target 4 h)
**Requires break-glass:** cluster administration (operator kubeconfig, or `break-glass-access.md` §Cluster). Authentik admin access after restore uses `sc-breakglass` (`break-glass-access.md` §Authentik). **No step needs a working Authentik login** (SPEC §22).

Alert names are the catalogue names in `contracts/alert-catalog.yaml` (CD-01).

## When to use
- Authentik data is wrong or lost: deleted users or groups, wrecked flows or providers made through the admin UI, a bad migration, `authentik-pg` lost or corrupt.
- `SCAuthentikDown` persisting after `authentik-outage.md` triage showed a database or data problem.
- `SCAuthentikBlueprintFailed` / Kustomization `authentik-config` not Ready after a blueprint PR (§Blueprint).
- A failed Authentik upgrade (§Upgrade rollback).
- `sso-init` failed with `AUTHENTIK_BOOTSTRAP_AUTH_FAILED` during bootstrap (§Bootstrap recovery).

For a plain outage (pod crash, worker down) start with `authentik-outage.md`.

## Preconditions and required material
- Toolbox (`./bootstrap shell`) with the operator kubeconfig.
- Recovery bundle + passphrase (for `SC-BREAK-GLASS-AUTHENTIK`; `SC-AUTHENTIK-SECRET-KEY` is SOPS-only in Git): `./bootstrap exec -- sc-bootstrap bundle open --show SC-BREAK-GLASS-AUTHENTIK` (plan 35 CLI; prints to the terminal only).
- S3 reachable; `authentik-pg` base backups and WAL present (plan 12).
- Target time `T` (UTC) just before the damage, for PITR.

## Safety checks before acting
1. Do **not** change `AUTHENTIK_SECRET_KEY` (ADR-0250; SOPS-only in Git). Restoring with a different key invalidates every session and token signature.
2. Do **not** recreate the `authentik-bootstrap` Secret outside §Bootstrap recovery. A restored database already has `akadmin` (inactive), so no bootstrap credential is needed.
3. Take an on-demand backup of the current state before deleting the cluster (plan 12 `restore-cnpg.md` step 1).
4. Warn users: logins to Forgejo, Grafana and Hermes fail during the restore. Existing sessions keep working. Machines (Flux, Runner, Woodpecker, bots) are unaffected.
5. Blast radius: Authentik only. Forgejo and Grafana keep local break-glass logins (15-AC-23).
6. **Identity-outage change path (AUTH-6, plan 15 §20).** With Authentik broken, no OIDC human can log in to Forgejo to approve a merge, and a `sc-breakglass` merge would be self-review through a break-glass identity. Every Git change this procedure needs is therefore: pushed as a branch with the operator's Forgejo **PAT** (works without Authentik) and opened as a PR; **applied with cluster admin** (`kustomize build` of the PR branch's layer path in the toolbox, `kubectl apply --server-side`) while the affected Flux Kustomizations are suspended; merged through the normal path once Authentik is back (if that merge is blocked because a merge-gate component such as the Runner or Woodpecker is also broken, use `procedures/emergency-merge.md`, plan 55); and recorded with its drift window in the evidence.
7. Edge down (Cloudflare or Traefik, AUTH-11): run `./bootstrap edge-bypass` (plan 09) before any browser step or public-URL check; every URL below then works unchanged.

## Steps

### §Blueprint (bad configuration PR; data intact)
1. `./bootstrap exec -- kubectl -n flux-system get kustomization authentik-config -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}'` and `./bootstrap exec -- kubectl -n authentik logs job/authentik-config-check`. Identify the failing blueprint instance.
2. Show its log: `./bootstrap exec -- kubectl -n authentik exec deploy/authentik-worker -- ak apply_blueprint /blueprints/mounted/cm-authentik-blueprints-sc/<file>.yaml` (verify command name on the running version). Expected: the error naming the object and field.
3. Fix by a **revert PR** on Forgejo (preferred), merge, `./bootstrap exec -- flux reconcile kustomization authentik --with-source`. Admin-UI fixes are overwritten on the next apply and are not a fix.
4. If logins are broken meanwhile and admins need the UI: log in as `sc-breakglass` (`break-glass-access.md` §Authentik).
5. Done when every SC blueprint instance is `successful` (Verification 2).

### §PITR (restore data to time T, or latest after disk loss)
1. Suspend and scale down: `./bootstrap exec -- flux -n flux-system suspend kustomization authentik-db authentik-upgrade-gate authentik` (AUTH-3: the CNPG `Cluster` lives in `authentik-db`), `./bootstrap exec -- flux -n authentik suspend hr authentik`, `./bootstrap exec -- kubectl -n authentik scale deploy/authentik-server deploy/authentik-worker --replicas=0`. Expected: no Authentik pods.
2. Record the `SECRET_KEY` hash from Git: `./bootstrap exec -- sc-bootstrap authentik key-hash --source git` (sha256 only; the key is SOPS-only in Git, there is no bundle copy). If decryption fails, stop and follow `sops-decryption-failure.md` (plan 35).
3. Restore `authentik-pg` following plan 12 `restore-cnpg.md`: verification cluster `authentik-pg-pitr` at `T`; inspect it (`./bootstrap exec -- kubectl cnpg psql authentik-pg-pitr -n authentik -- -d authentik -c "select username, is_active, last_login from authentik_core_user order by last_login desc nulls last limit 20;"`), confirm the damage is absent; then re-create `authentik-pg` by name with `cnpg-bootstrap-recovery`, `SOURCE_GEN=N`, `GEN=N+1` (the change goes through the identity-outage change path, safety check 6: branch `restore/authentik-<date>` pushed with a PAT, PR opened, rendered `authentik-db` layer applied with `kubectl apply --server-side`; the PR is merged after Authentik is back). If no backup can be used and `authentik-pg` must start **empty**, run `./bootstrap exec -- sc-bootstrap sso-init --from A0` before step 4 so the bootstrap Secret exists before Authentik's first start on the empty DB (AUTH-1). Expected: `Cluster in healthy state`, `ContinuousArchiving=True` into `g<N+1>`.
4. Resume: `./bootstrap exec -- flux -n authentik resume hr authentik`; the Kustomizations stay suspended until the PR is merged (then `./bootstrap exec -- flux -n flux-system resume kustomization authentik-db authentik-upgrade-gate authentik`, which must report no diff). Wait for `./bootstrap exec -- kubectl -n authentik exec deploy/authentik-server -- ak healthcheck` exit 0 (≤ 15 min).
5. Verify the key in the pod: `./bootstrap exec -- sc-bootstrap authentik key-hash --source pod` equals step 2.
6. Force blueprint re-apply for every SC file: `./bootstrap exec -- kubectl -n authentik exec deploy/authentik-worker -- sh -c 'for f in /blueprints/mounted/cm-authentik-blueprints-sc/*.yaml; do ak apply_blueprint "$f" || exit 1; done'`. Expected: exit 0. This restores groups, flows, providers and policies to Git state even if `T` predates a blueprint change.
7. Re-run `./bootstrap exec -- kubectl -n flux-system annotate kustomization authentik-config reconcile.fluxcd.io/requestedAt="$(date -u +%FT%TZ)" --overwrite` so the check Job re-runs.
8. Check break-glass and bootstrap state: `akadmin` must be inactive and `sc-breakglass` active and superuser. Run `./bootstrap exec -- sc-bootstrap sso-init` (idempotent; it verifies via the audit token and repairs `sc-breakglass`/`akadmin` through the admin channel `kubectl exec … ak shell`, which is audited; AUTH-5). If it reports `sc-breakglass` missing (T predates its creation), it re-creates it with the bundle password (A4).
9. Users created or MFA devices enrolled after `T` are lost. Recreate users through the admin UI (as an `sc-admins` human, or `sc-breakglass`); affected users re-enrol MFA on next login (`not_configured_action: configure`).

### §Upgrade rollback
1. Find the pre-upgrade backup evidence referenced by the upgrade PR (backup-first gate, plan 15 §19).
2. Revert the version PR: push the revert branch with a PAT and apply it through the identity-outage change path (safety check 6) if Authentik is down; merge it once Authentik is back.
3. Run §PITR with `T` = the pre-upgrade backup's `stoppedAt`. Authentik migrations are forward-only; Helm rollback alone is not sufficient.

### §Bootstrap recovery (during first install only: `AUTHENTIK_BOOTSTRAP_AUTH_FAILED` or lost bootstrap credential before `sso-init` A9)
1. Confirm this is a first install: `./bootstrap status --json | jq '.phases["sso-init"].result'` is not `succeeded`, and no human users exist yet.
2. If `/run/sc/secrets/authentik-bootstrap*` exists in the operator session and the Secret exists in-cluster, compare: `./bootstrap exec -- sc-bootstrap sso-init --check-bootstrap-secret`. A mismatch means Authentik started before the Secret; continue to step 3.
3. Default (deterministic): nothing depends on this database yet, so start Authentik over. `./bootstrap exec -- flux -n authentik suspend hr authentik`; scale both Deployments to 0; `./bootstrap exec -- kubectl -n authentik delete cluster authentik-pg` and its PVCs (`kubectl -n authentik delete pvc -l cnpg.io/cluster=authentik-pg`); delete the Secret `authentik-bootstrap` and `/run/sc/secrets/authentik-bootstrap*`. Bump `CNPG_AUTHENTIK_GEN` by one in `cluster-vars` (a fresh backup path, plan 12 §9.4) through a commit pushed under the bootstrap protection stage (forge-init S16 rules). Run `./bootstrap exec -- sc-bootstrap sso-init --from A0` (plan 50): A0 recreates the Secret, then the tool resumes the HelmRelease and the `authentik` Kustomization; Flux recreates `authentik-pg` and Authentik starts with the Secret present; A1–A10 follow. Expected: `sso-init` reaches A10 `succeeded`.
4. Only if step 3 is impossible (humans already enrolled): mint a recovery key through cluster admin, `./bootstrap exec -- kubectl -n authentik exec deploy/authentik-worker -- ak create_recovery_key 1 akadmin` (verify argument order and unit), log in with the printed URL, create an API token for `akadmin` with intent `api` in the admin UI, put it into `/run/sc/secrets/authentik-bootstrap-token`, and re-run `./bootstrap exec -- sc-bootstrap sso-init`, which resumes at A2 and later tears the token down (A9). Record the manual token creation.

## Verification
1. `./bootstrap exec -- curl -fsS https://auth.<BASE_DOMAIN>/-/health/ready/` returns 200/204.
2. `./bootstrap exec -- sc-bootstrap authentik blueprints` (audit token, `GET /api/v3/managed/blueprints/?label=sc`) lists ≥ 10 instances, all `successful`.
3. OIDC discovery for `forgejo`, `grafana`, `hermes-dashboard` returns the expected issuer (15-AC-6).
4. A human `sc-engineers` user logs in to Forgejo via Authentik (or the synthetic login probe `sc_oidc_discovery_ok{app}` = 1 for all apps and the k3d-style headless login run by `./bootstrap exec -- sc-bootstrap authentik login-test`).
5. `sc_breakglass_account_ready{system="authentik"}` = 1 for both `sc-breakglass` and `akadmin` (inactive) — no `SCAuthentikBootstrapAdminActive`, `SCBreakGlassAccountNotReady`.
6. `authentik-pg`: `ContinuousArchiving=True`; a base backup completes (trigger per plan 12 if needed).
7. Hash comparison of step 2/5 recorded equal.

## Rollback / abort
- Before §PITR step 3's re-creation, abort by resuming the Kustomizations and the HelmRelease and scaling up; nothing changed.
- After an identity-outage apply whose PR is then rejected in review: restore the reviewed state by resuming the Kustomizations (Flux reapplies `main`), and run §PITR again if data was affected.
- After re-creation, the pre-restore state is in `g<N>` (on-demand backup). Restore "back" by running §PITR again with that target.
- A blueprint revert is itself revertible by PR.

## Evidence to record
`s3://<prefix>-evidence/restores/authentik/<UTC ts>.json`: trigger, target `T`, backup and generation used, secret-key hash comparison results (hashes only), blueprint instance status before/after, `sso-init` journal record, list of users/devices lost after `T`, the identity-outage drift window (start, end, PR) if used, break-glass uses during the procedure (each raises `SCBreakGlassUsed`; reference this evidence when silencing), measured RTO.

## Follow-ups
- Trigger the plan 44 Authentik restore test on demand within 7 days.
- If the cause was an admin-UI change to a blueprint-managed object, add the object to blueprints or tighten admin rights (plan 34 review).
- If break-glass was used, complete `break-glass-access.md` §After use.
