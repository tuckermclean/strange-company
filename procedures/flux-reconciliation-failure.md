# Procedure: Flux reconciliation failure

**Owning plan:** plans/18-flux.md
**SPEC references:** §4.2, §33, §96 ("Flux failure"), §113
**Severity / urgency:** degraded. Running workloads keep their last applied state, but Git changes stop applying. Outage if the failure blocks a fix for another outage.
**Expected duration:** 10 min (§B revert, §J) to 2 h (§H, §I)
**Requires break-glass:** no for diagnosis. `SC-BREAK-GLASS-CLUSTER` for §F deletions and for the emergency path in §B. `SC-BREAK-GLASS-FORGEJO` when Forgejo itself is the problem (§A).

## When to use

| Alert (catalogue name) | Section |
|---|---|
| `SCFluxSourceFailing` / `SCFluxSourceNotReady` | §A |
| `SCFluxReconciliationFailing` | reason `BuildFailed` → §B; `DecryptionFailed` → §C; `HealthCheckFailed`/`DependencyNotReady` → §D; HelmRelease `UpgradeFailed`/`InstallFailed` → §E |
| `SCFluxReconcileStalled` | §D (stuck object → §G) |
| A stateful object missing from a Kustomization inventory after a merge, or `make gitops-diff` was bypassed | §F |
| An object stuck `Terminating` | §G |
| `SCFluxControllerDown` | §H |
| `no matches for kind` errors after a Flux version bump | §I |
| `SCFluxSuspendedTooLong` | §J |
| `SCFluxSeedSourcePresent` | not here: `failed-flux-handoff.md` §D |

## Preconditions and required material
- Toolbox (`./bootstrap shell`) with an admin kubeconfig: the operator's OIDC kubeconfig if available, otherwise the break-glass kubeconfig (`break-glass-access.md`).
- A clone of Forgejo `strange-company` at current `main`. If Forgejo is down, use GitHub `main` (read-only).
- Ability to open and merge a PR on Forgejo, or break-glass for the emergency path in §B.

## Safety checks before acting
1. **Git is the source of truth.** A fix is a commit, never `kubectl edit` of a Flux-managed object (helm-controller drift detection reverts it, and SPEC §4.2 forbids unexplained state). Allowed imperative actions: `flux suspend/resume`, `flux reconcile`, deleting stuck finalizers (§G), break-glass deletion of orphaned objects (§F), and re-applying the committed `flux-system` manifests (§H).
2. **Never delete** a PVC, Namespace, CNPG `Cluster`, `ObjectStore` or Longhorn object to "unstick" a reconcile, unless §F or §G explicitly says to and the data is confirmed backed up (plan 44: last backup < 24 h, `./bootstrap exec -- sc-bootstrap backup status`).
3. Record the current state: `./bootstrap exec -- flux get all -A > flux-before.txt`.
4. Suspending a layer stops it **and every layer that depends on it** from updating. Check `gitops/layers/*.yaml` `dependsOn` before suspending. Never suspend `flux-system` (the root) except in §H.

## Steps

### §A Source not ready (Forgejo unreachable, deploy key rejected, chart registry unreachable)
A1. `./bootstrap exec -- flux get sources all -A --status-selector ready=false`. Note the kind and the message.

A2. **GitRepository `flux-system`:**
- `connection refused` / timeout: `./bootstrap exec -- kubectl -n forgejo get pods,endpoints`. If Forgejo is down, follow `restore-forgejo.md` §Triage. Flux needs nothing else; it resumes when Forgejo returns.
- `knownhosts: key mismatch`: the Forgejo host key changed. **Do not** update `known_hosts` to the new key blindly. Follow `rotate-forge-credentials.md` §E (plan 16 F8).
- `unable to authenticate`: compare the deploy-key fingerprint with Forgejo: `./bootstrap exec -- sc-bootstrap forge verify`. If the key is missing, run `./bootstrap exec -- sc-bootstrap forge apply` (re-registers the read-only key from Git).

A3. **OCIRepository / HelmRepository:** registry outage or a removed digest. `./bootstrap exec -- flux get source oci -A`. If the digest no longer exists upstream (the nightly `locks-verify-online` also fails), open a PR pinning a new digest (Renovate or manual). Running workloads are unaffected meanwhile.

A4. Expected: the source turns `Ready True` within one interval after the cause is fixed. Force with `./bootstrap exec -- flux reconcile source git flux-system`.

### §B Render error (`BuildFailed`: bad kustomization, undefined variable, invalid YAML)
B1. `./bootstrap exec -- flux get kustomization <layer> -n flux-system`. The message names the file or variable.

B2. Reproduce offline at the failing revision: `git checkout <sha> && ./bootstrap exec -- make gitops-validate`. It should fail the same way. If it passes offline but fails live, that is a plan 18 F11 gap: file it with both outputs.

B3. **Fix by revert:** `git revert <offending commit>`, open a PR, CI (`ci/woodpecker/pr/ci`), human merge. Expected: the layer is Ready at the new `main` within 3 min of merge.

B4. **Emergency path when CI cannot run** (for example the bad commit broke the `woodpecker` layer):
1. Suspend only the broken layer: `./bootstrap exec -- flux suspend kustomization <layer> -n flux-system`.
2. If Woodpecker is still running, merge the revert PR normally.
3. If merging is impossible (Woodpecker, `sc-ci-config` or the Runner's statuses are themselves broken), land the reviewed revert with **`procedures/emergency-merge.md`** (plan 55; GC-02): `forge apply --stage bootstrap` with `sc-breakglass`, fast-forward push of the reviewed commit, `forge apply --stage steady`, merge audit with an always-notify alert. Do not change branch protection any other way.
4. Then `flux resume kustomization <layer> -n flux-system`. Record the emergency merge.

### §C SOPS decryption failure (`DecryptionFailed`)
C1. Message `no key could decrypt` / `failed to get the data key`. Check that `flux-system/sops-age` exists: `./bootstrap exec -- kubectl -n flux-system get secret sops-age`.
- **Missing:** re-create it from the recovery bundle (`rotate-age-key.md` §restore, plan 35). It is the only out-of-band Secret.
- **Present:** the file was encrypted without the cluster recipient. Check with `./bootstrap exec -- sops filestatus <file>` and inspect the `sops.age[].recipient` entries. Re-encrypt with `sops updatekeys` using `.sops.yaml`, open a PR, merge.

C2. `make secrets-scan` should have caught a recipient mismatch in CI. File a plan 35/01 gap.

C3. Follow `sops-decryption-failure.md` (plan 35) for anything not covered here.

### §D Health check timeout, dependency not ready, or stalled reconcile
D1. `./bootstrap exec -- flux tree kustomization <layer> -n flux-system` shows the objects. Find the not-ready ones: `./bootstrap exec -- kubectl get <kind> <name> -n <ns> -o yaml` and read `status.conditions`, Pod events and logs.

D2. Typical causes and fixes:

| Cause | Fix |
|---|---|
| Pod `ImagePullBackOff` | pinned digest wrong or registry down: §A3 or a PR |
| Admission denial in events | plan 37 rule or component values; PR |
| Pending (resources) | plan 51 quota or PriorityClass; check `kubectl describe pod`. On the 8 GiB constrained profile see ADR-0020 |
| CNPG `Cluster` not Ready | plan 12 procedures (`cnpg-primary-loss.md`) |
| Longhorn volume degraded | plan 13 procedures |
| Multi-Attach (Forgejo/Hermes) | `restore-forgejo.md` §Multi-Attach (lesson L-5) |

D3. A dependent layer showing `DependencyNotReady` resolves automatically once its dependency is Ready (`--requeue-dependency=15s`). Do not suspend it.

D4. A layer that stays `Ready Unknown` for 60 min (`SCFluxReconcileStalled`) usually waits on a deletion (§G) or on a hung health check. Run `./bootstrap exec -- flux reconcile kustomization <layer> -n flux-system`. If it is still stuck after 10 min, inspect kustomize-controller logs: `./bootstrap exec -- kubectl -n flux-system logs deploy/kustomize-controller --since=30m | grep <layer>`.

### §E HelmRelease install or upgrade failure
E1. `./bootstrap exec -- flux get helmreleases -A --status-selector ready=false` and `./bootstrap exec -- kubectl -n <ns> get hr <name> -o jsonpath='{.metadata.labels.strange\.company/remediation-class}'`.

E2. **Class `controller`:** helm-controller has rolled back (up to 3 retries). Read `./bootstrap exec -- helm history <release> -n <ns>`. Fix forward with a PR (pin the previous chart version, or fix the values). After merge, if retries were exhausted: `./bootstrap exec -- flux reconcile helmrelease <name> -n <ns> --force`.

E3. **Class `stateful-app`** (Forgejo, Authentik, Woodpecker, Grafana, `strange-company`): there is **no** automatic rollback (ADR-0304), because the schema may already be migrated.
- E3a. Determine whether the migration ran: the application logs (`kubectl -n <ns> logs <pod> --previous`) show migration messages.
- E3b. If **no migration ran** (the failure happened before the app started, e.g. a values error): revert the PR as in §B. The HelmRelease upgrades back cleanly.
- E3c. If a **migration ran**: do not revert the image version. Either fix forward (a PR with the corrected values or a patch release), or restore: plan 48's upgrade-rollback procedure (component-specific, e.g. `restore-forgejo.md` §Upgrade rollback: PITR to the pre-upgrade backup plus a version revert). The backup-first gate record (plan 48) identifies the restore point.

E4. Expected: HelmRelease `Ready True`, `helm history` shows a new `deployed` revision, and the component's gate checks pass (run its plan's live AC subset).

### §F Stateful manifest removed from Git (object orphaned by prune protection)
F1. Identify the object: `./bootstrap exec -- sc-gitops diff --base <before-merge sha> --head <merge sha>` lists removed objects. Protected objects stay in the cluster, annotated `kustomize.toolkit.fluxcd.io/prune: disabled`, but they are no longer in any inventory.

F2. **If the removal was a mistake:** revert the PR (§B3). Flux re-adopts the object: SSA with the same field manager, so no recreation. Check with `kubectl get <kind> <name> -o jsonpath='{.metadata.uid}'` before and after; the UIDs must be equal.

F3. **If the removal was intended** (the component is decommissioned): confirm a backup exists (safety check 2), then delete with break-glass: `./bootstrap exec -- kubectl delete <kind> <name> -n <ns>`. Record it. The PR should have carried `GitOps-Allow-Destroy: <kind>/<ns>/<name>` (plan 18 §8.6). If it did not, file a process gap.

### §G Object stuck `Terminating`
G1. `./bootstrap exec -- kubectl get <kind> <name> -n <ns> -o jsonpath='{.metadata.finalizers}'`.

G2. Identify the finalizer owner (CNPG, Longhorn, Kubernetes namespace controller, Helm). Fix the owner first: an operator that is not running cannot remove its finalizer. Restarting the owner usually completes the deletion.

G3. Only if the owner is gone for good **and** the object holds no data you need: remove the finalizer with `./bootstrap exec -- kubectl patch <kind> <name> -n <ns> --type json -p '[{"op":"remove","path":"/metadata/finalizers"}]'`. Never do this for a Longhorn volume or a CNPG `Cluster` without the plan 13/12 procedure. Their finalizers protect storage cleanup.

G4. For a Namespace stuck terminating: `./bootstrap exec -- kubectl get ns <ns> -o json | jq .status.conditions` names the blocking resources. Resolve those resources; never force-remove the namespace finalizer.

### §H Flux controller crash-loop or OOM; re-apply controllers
H1. `./bootstrap exec -- kubectl -n flux-system get pods` and `… describe pod`. If the reason is `OOMKilled`, raise the memory limit in `gitops/clusters/<c>/flux-system/kustomization.yaml` (plan 18 §12) by PR. With all controllers down, also apply that file directly (H3), because Flux cannot apply its own fix.

H2. Check the Flux version matches the lock: `./bootstrap exec -- flux version` vs `versions/platform.lock.yaml`.

H3. Re-apply the committed controllers (allowed imperative step; Git remains the source):
```sh
./bootstrap exec -- sh -c 'sc-gitops render --cluster <cluster> --flux-system --exclude-sops \
  | kubectl apply --server-side --field-manager=kubectl --force-conflicts -f -'
```
Run it from a checkout of the current Forgejo `main` (or GitHub `main` if Forgejo is down; they must be equal, which you check with `sc-bootstrap mirror compare-refs`). The render includes `gotk-sync.yaml` and the digest/annotation patches, so this also restores the root `Kustomization` and the `GitRepository`. It deliberately **excludes** the SOPS-encrypted `flux-source-auth.sops.yaml`: never `kubectl apply -k` that directory, which would write ciphertext over the deploy key (SEED-16). If Secret `flux-system/flux-system` itself is lost, restore it with `./bootstrap exec -- sh -c 'sops -d gitops/clusters/<cluster>/flux-system/flux-source-auth.sops.yaml | kubectl apply --server-side --field-manager=kubectl -f -'` (the recovery bundle supplies the age identity in tmpfs, `/run/sc/secrets/`).
- Expected: the four controllers are Available within 5 min, and the root is Ready at `main@sha1:<sha>` within 10 min.
- If `sops-age` is missing, see §C1.

H4. If the controllers stay broken after H3 at the pinned version, do **not** re-run plan 19's seed against a live cluster: the root would be pointed at a Bucket, and the transfer checks assume a fresh install. Escalate with controller logs (`kubectl -n flux-system logs deploy/<controller> --previous`). The last resort is plan 45's rebuild path.

### §I Flux upgrade broke an API version
I1. Symptom: after a merge that changed `gotk-components.yaml`, layers fail with `no matches for kind "<Kind>" in version "<group>/<v>"`.

I2. Revert the Flux bump PR (§B3). The F11 check `flux-upgrade-isolation` guarantees that the PR changed nothing else, so the revert is clean. Flux downgrades its controllers and CRDs. CRDs keep stored versions; verify with `kubectl get crd <crd> -o jsonpath='{.status.storedVersions}'`. A downgrade that removes a stored version must not be applied; see plan 48 in that case.

I3. Migrate the manifests to the new API version in a separate PR (`make gitops-validate` with `flux-api-versions`), then re-apply the Flux bump.

### §J Suspended object forgotten
J1. `./bootstrap exec -- flux get all -A --status-selector suspended=true`.

J2. For each object, find who suspended it and why. Look for an incident record or a break-glass record. The `kubectl` audit log (`/var/lib/rancher/k3s/server/logs/audit.log` on servers, plan 08) shows the actor.

J3. If the reason is resolved: `./bootstrap exec -- flux resume <kind> <name> -n <ns>`. Expected: Ready within one interval, and the alert resolves within 15 min. If the reason is not resolved: record an owner and an expected resume time in the incident.

## Verification
- `./bootstrap exec -- flux get all -A --status-selector ready=false` prints nothing.
- `./bootstrap exec -- kubectl -n flux-system get gitrepository flux-system -o jsonpath='{.status.artifact.revision}'` equals `main@sha1:<Forgejo main SHA>`.
- `./bootstrap exec -- flux get all -A --status-selector suspended=true` prints nothing (unless intentionally suspended with a recorded owner).
- Prometheus `ALERTS{alertname=~"SCFlux.*",alertstate="firing"}` is empty within 15 min.
- For §E/§F: the component's own gate checks pass (its plan's AC list).

## Rollback / abort
- Revert PRs are themselves revertible.
- `flux suspend` is undone with `flux resume`.
- §H re-apply is idempotent.
- §F deletion and §G finalizer removal are **not** reversible. That is why they require the safety checks and break-glass.

## Evidence to record
- `flux-before.txt` and `flux-after.txt`; the failing reason and message; the offending and fixing commit SHAs and PR numbers.
- Any break-glass use (start, end, actor, commands), and any imperative command run (copy from the shell history).
- For §E3c: the restore point used and the data-loss window.

## Follow-ups
- Every §B and §I occurrence that passed `make gitops-validate` is an F11 gap. Add a negative fixture (plan 18 §22).
- Every §F occurrence without a `GitOps-Allow-Destroy` trailer is a `gitops-diff` gap or a bypass. Investigate.
- After §H with OOM: update plan 18 §12 sizes and plan 51's memory budget.
