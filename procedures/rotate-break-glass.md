# Procedure: Rotate break-glass credentials

**Owning plan:** plans/35-secrets.md (policy, schedule, bundle); mechanics plan 15 (`break-glass-access.md` §Rotate, ADR-0251) and plan 16 (`rotate-forge-credentials.md` §F)
**SPEC references:** §22, §99, §111, §113 (rotate break-glass credentials)
**Severity / urgency:** routine (yearly) · urgent after use when the credential was exposed · urgent on compromise (start at `credential-compromise.md`)
**Expected duration:** 15–30 min per system; ≤ 2 h for all five
**Requires break-glass:** the current credential of the system being rotated, or an `sc-admins` human session; `SC-K3S-ADMIN-KUBECONFIG` for the cluster key

## When to use

- Yearly schedule: `SCSecretRotationDue`/`Overdue` for `SC-BREAK-GLASS-*` or `SC-GRAFANA-ADMIN`.
- After any break-glass use where `break-glass-access.md` §After use step 3 says "rotate" (credential displayed to anyone else, written outside `/run/sc/secrets/bg/`, used from an untrusted machine, Authentik recovery-key path used).
- Custodian change (anyone who knew the passphrase leaves).
- Compromise: `credential-compromise.md` sends you here after containment.

Systems and classes: `#cluster` (`SC-BREAK-GLASS-CLUSTER`), `#authentik` (`SC-BREAK-GLASS-AUTHENTIK`), `#forgejo` (`SC-BREAK-GLASS-FORGEJO`), `#grafana` (`SC-BREAK-GLASS-GRAFANA` = `SC-GRAFANA-ADMIN/admin`), `#infrastructure` (`SC-BREAK-GLASS-INFRA` and the bundled infrastructure toolkit).

## Preconditions and required material

- Toolbox; recovery passphrase; recovery bundle reachable (`recovery-bundle status` = ok).
- For `#cluster`: SSH access through the operator key (`SC-SSH-OPERATOR`) as a fallback while the break-glass key changes.
- For `#infrastructure`: provider console access with MFA.

## Safety checks before acting

1. Run the drill for the system first: `./bootstrap exec -- sc-bootstrap breakglass drill --only <system>`. If it fails, the current credential is broken: use `break-glass-access.md` §Repair before rotating (you must never rotate away the last working path).
2. Keep at least one independent admin path alive during the rotation: Authentik → an `sc-admins` human session; Forgejo → `sc-admin` automation account; cluster → `SC-SSH-OPERATOR`; Grafana → Git (SOPS); infrastructure → console.
3. The new bundle must be sealed and confirmed before the old credential is discarded (old bundle versions keep the old value, which must stop working).

## Steps

Common pattern (each section adds system specifics):
1. Stage: `./bootstrap exec -- sc-bootstrap bundle stage-rotation <CLASS>` — new value in local staging only.
2. Apply (system section).
3. Seal: `./bootstrap rotate refresh-bundle` (`reason.trigger: rotation`, `rotated_classes: [<CLASS>]`); re-enter the passphrase to confirm the S3 copy.
4. Verify: `./bootstrap exec -- sc-bootstrap breakglass drill --only <system>` → `passed` with the new value. This raises `SCBreakGlassUsed`; silence it with comment `rotation: <issue>` (`break-glass-access.md` §After use step 2).
5. Discard the staged old value; ledger entry written by the tool; `tier-2` file `secrets/offcluster/<cluster>/break-glass-<system>.sops.yaml` updated by PR (the tool opens it).

### cluster

1. Stage `SC-BREAK-GLASS-CLUSTER` (new ed25519 pair).
2. PR replacing `infra/ansible/files/breakglass.pub` with the new public key; merge.
3. `./bootstrap exec -- sc-bootstrap configure --tags breakglass` on every node (Ansible installs the new key and removes the old one in the same run).
4. Seal, then verify: `ssh -i <staged key> sc-breakglass@<node> sudo k3s kubectl get nodes` works on every node; the old key is refused (`Permission denied (publickey)`).
5. `sc_breakglass_ssh_key_present == 1` on every node (no `SCBreakGlassAccountNotReady`).

### authentik

1. Stage `SC-BREAK-GLASS-AUTHENTIK` (≥ 32 random characters).
2. Apply per `break-glass-access.md` §Rotate (Authentik): Admin → Users → `sc-breakglass` → Set password, as an `sc-admins` human, or `sc-bootstrap sso-init --rotate-breakglass`.
3. Seal; verify with the drill (password login through the flow executor).
4. Revoke existing `sc-breakglass` sessions (Admin → Users → `sc-breakglass` → Sessions → delete all).

### forgejo

1. Stage `SC-BREAK-GLASS-FORGEJO`.
2. Apply per `rotate-forge-credentials.md` §F (admin API as `sc-admin`).
3. Seal; verify with the drill (web form login); the old password fails.

### grafana

1. Stage `SC-BREAK-GLASS-GRAFANA`.
2. `./bootstrap rotate secret SC-GRAFANA-ADMIN/admin --value-from-staging` → PR replacing the SOPS file `gitops/clusters/<cluster>/secrets/monitoring/grafana-admin.sops.yaml`; merge; Grafana restarts and resets the admin password (plan 39).
3. Seal; verify (`GET /api/user` with basic auth via the drill); `SCGrafanaLocalLogin` fires for the drill and is silenced with the rotation comment.

### infrastructure

1. Provider console: change the console password and regenerate MFA recovery codes; store them offline (`SC-BREAK-GLASS-INFRA` is operator-held only).
2. Rotate the bundled toolkit credentials whose schedule is due or which were exposed, each with its own procedure: `SC-INFRA-PROVIDER`, `SC-DNS-API`, `SC-DNS-TUNNEL` (`rotate-secrets.md`), `SC-OBJECT-STORAGE/admin` (`rotate-storage-credentials.md`), `SC-TOFU-ENCRYPTION` (on compromise only).
3. Seal; verify with the drill (`--only infrastructure`: provider `/me`, Cloudflare token verify, S3 `ListBuckets`, `tofu state list`).

## Verification

- `./bootstrap exec -- sc-bootstrap breakglass drill` passes for every rotated system with the new values.
- `recovery-bundle status`: new `bundle_version`, `currency: ok`; the inventory lists the rotated classes at generation N+1.
- No `SCBreakGlassAccountNotReady`; `SCBreakGlassTestOverdue` resolved (the drill wrote evidence).

## Rollback / abort

- Before step 2 (apply): discard the staging (`sc-bootstrap bundle stage-rotation --abort`).
- After apply but before sealing: the old value no longer works and the new value exists only in staging; **seal immediately** (`refresh-bundle`). If sealing fails (S3 down), the staging area keeps the value encrypted under the passphrase; retry; do not close the toolbox session before the local bundle file is written.
- After sealing: no rollback; rotate again if needed.

## Evidence to record

Rotation issue with: system, class, reason (schedule, after-use, custodian change, compromise), drill evidence key, new `bundle_version`, PR URLs, and who knows the new credential.

## Follow-ups

- If the rotation followed a use, close the break-glass incident with the root cause.
- Review whether the break-glass path that was used can be avoided next time (`break-glass-access.md` §After use step 4).
