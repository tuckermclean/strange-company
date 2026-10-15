# Procedure: Upgrade a platform component

**Owning plan:** plans/48-upgrade.md
**SPEC references:** §4.2, §33, §37, §116
**Severity / urgency:** routine
**Expected duration:** 15–60 min per component after merge
**Requires break-glass:** no (except §Rollback step that applies Flux manifests directly: cluster admin through the toolbox kubeconfig)

## When to use
- A Renovate (or human) PR upgrades a Flux-delivered component: controller class (cert-manager, CNPG operator, Longhorn, Traefik, cloudflared, kube-prometheus-stack, OTel, kured, Loki, Tempo), stateful-app class (Forgejo, Authentik, Woodpecker, Grafana, Hermes), the `strange-company` chart, Flux itself, or a harness/toolchain image.
- `SCUpgradeGateFailed` → §Gate. A failed HelmRelease after merge → §Controller or §Stateful.
- OS point updates and `SCNodeUpdatesFailing` → §OS.
- PostgreSQL major and Longhorn minor upgrades have extra rules (plan 48 §14.5, §14.6); use this procedure with those sections.

## Preconditions and required material
- Forgejo access to review and merge; `./bootstrap shell` for checks.
- The PR's CI is green (locks-check, render, k3d upgrade test; for `sc`: N−1 migration compatibility; for Hermes: L-1 suite).

## Safety checks before acting
1. One upgrade at a time (plan 48 §9.3 freeze rule): no other upgrade is converging (`flux get kustomizations -A` all Ready at their latest merge), `./bootstrap status` shows no expansion running, and no critical alert, `SCUpgradeGateFailed` or `SCFluxReconciliationFailing` fires.
   - If the component is part of the merge gate itself (Runner, Woodpecker, `sc-ci-config`, egress-proxy, heavy-slot advertiser) and its failure prevents the revert PR from merging, use plan 55's `emergency-merge.md` (GC-02).
2. Read the release notes for breaking changes; required values changes must be in the same PR.
3. Stateful apps: the gate backup will run automatically; make sure backups are currently healthy (`SCCnpgBackupFailed`, `SCLonghornBackupStale` not firing), otherwise the gate will fail anyway.
4. Do not merge PostgreSQL major or Kubernetes minor PRs (`do-not-merge/needs-procedure`) outside a maintenance window.

## Steps
1. **Review and merge** the PR. Note the merge SHA.
2. **Watch reconciliation:** `./bootstrap exec -- flux get kustomizations -A --watch` until the component's layer shows `Applied revision: main@sha1:<merge SHA>`.
   - Stateful-app class: first the `<app>-upgrade-gate` layer runs Job `upgrade-gate-<app>-<version>`: `kubectl -n <ns> get job -l strange.company/upgrade-gate --sort-by=.metadata.creationTimestamp`. Expected: `Complete` within 45 min; ConfigMap `upgrade-gate-<app>` shows the new `version` and `backup_ids`. Then the app layer upgrades.
3. **Check the release:** `./bootstrap exec -- flux get helmreleases -A | grep <component>` → `Ready True`, chart version = new. `kubectl -n <ns> get hr <component> -o jsonpath='{.status.history[0].status}'` = `deployed`.
4. **Run the component's gate checks** (the component plan's acceptance items, e.g. Forgejo 16-AC-1, cert-manager 10-AC-2, Traefik 09-AC-8/10). Expected: all pass within 30 min of merge.
5. **Watch alerts** for 30 min; none new for the component.

### §Gate (`SCUpgradeGateFailed`)
1. `kubectl -n <ns> logs job/upgrade-gate-<app>-<version>`; `kubectl -n <ns> get backups.postgresql.cnpg.io` (and Longhorn backups for Forgejo/Hermes).
2. Fix the backup cause (object storage, credentials, CNPG plugin; see `object-storage-outage.md`, plan 12/13 procedures).
3. Delete the failed Job so Flux recreates it: `kubectl -n <ns> delete job upgrade-gate-<app>-<version>`; `flux reconcile kustomization <app>-upgrade-gate`. Expected: Job completes; the app upgrade proceeds.
4. Nothing in the app changed while the gate was failing.

### §Controller (controller class failed)
1. Flux already rolled back (3 remediation attempts). Confirm: `kubectl -n <ns> get hr <c> -o jsonpath='{.status.history}'` shows the old version `deployed` again.
2. Revert the PR (Forgejo "Revert" button → PR → merge) so Git matches the running version.
3. Investigate in a branch with the k3d upgrade test; re-propose.
4. **Longhorn is the exception:** it cannot be downgraded. If a Longhorn upgrade fails mid-way, do not revert; follow plan 13's upgrade failure guidance and keep volumes attached.

### §Stateful (stateful-app class failed after the gate)
1. The HelmRelease is failed and no automatic rollback happened (by design: the database may already be migrated).
2. Decide with the app's logs (`kubectl -n <ns> logs deploy/<app>`):
   - Migration did not start or failed before changing the schema (logs say so): revert the PR; the old image runs against the unchanged database.
   - Migration changed the schema: **do not** run the old image against it. Restore the database to the gate backup (`restore-cnpg.md` with recovery target = the gate backup's `stoppedAt`, from ConfigMap `upgrade-gate-<app>`; for Forgejo/Hermes also `restore-longhorn.md` to the gate's Longhorn backup), and revert the PR in the same change window.
3. Expected: app healthy on the old version; data as of the gate backup (writes after it are lost; announce to users).

### §OS (point upgrades; `SCNodeUpdatesFailing`)
1. `SCNodeUpdatesFailing`: `./bootstrap exec -- ssh <node> 'sudo journalctl -u unattended-upgrades -n 100; sudo dpkg --audit'`; fix the broken package (`sudo dpkg --configure -a`, `sudo apt-get -f install`).
2. Monthly point upgrade: `./bootstrap configure --tags os-upgrade` (single node: in the maintenance window; it reboots if required). small-ha: nodes one at a time with drain.
3. Expected: `apt list --upgradable` empty on every node afterwards; `./bootstrap verify` passes.

### §Rollback for Flux itself
If a Flux upgrade leaves controllers unable to reconcile (so a revert PR is never applied): `./bootstrap exec -- git show <previous SHA>:gitops/clusters/<c>/flux-system/gotk-components.yaml | kubectl apply --server-side --force-conflicts -f -`, then merge the revert PR; Flux takes over again.

## Verification
- HelmRelease `Ready=True` at the new version; component gate checks pass; no new alerts.
- `./bootstrap verify` passes (run it after stateful-app and `sc` upgrades).

## Rollback / abort
Per class above. Closing the PR before merge aborts with no effect.

## Evidence to record
PR URL, merge SHA, gate Job name and backup IDs, time to Ready, any remediation. A comment on the PR is sufficient for routine upgrades; failures get an incident issue.

## Follow-ups
- After a stateful-app restore, run the app's restore test (plan 44) and check the GitHub mirror (plan 17) for Forgejo.
- Update `versions/compat.yaml` if the component changed its supported Kubernetes range.
