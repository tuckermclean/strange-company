# Procedure: Break-glass access

**Owning plan:** plans/15-authentik.md (§14.4, ADR-0251); rotation policy and bundle mechanics plan 35
**SPEC references:** §22, §99, §111, §113 ("rotate break-glass credentials" mechanics)
**Severity / urgency:** emergency access during an outage (degraded or outage); drills are routine
**Expected duration:** access 5–15 min; drill ≤ 30 min; rotation 30–60 min
**Requires break-glass:** yes — this procedure is the break-glass procedure. Every use is audited and pages (`SCBreakGlassUsed`).

Alert names are the catalogue names in `contracts/alert-catalog.yaml` (CD-01).

## When to use
| Situation | Section |
|---|---|
| Operator kubeconfig lost; kube-apiserver unreachable remotely; RBAC broken | §Cluster |
| Human admins locked out of Authentik (MFA lost, flow broken, IdP misconfigured) | §Authentik |
| Forgejo admin action needed while Authentik is down or no `sc-admins` human is available | §Forgejo |
| Grafana needed during an incident while Authentik is down | §Grafana |
| Nodes unreachable, SSH broken, provider/DNS/S3/OpenTofu actions needed outside the cluster | §Infrastructure |
| After **any** break-glass use (mandatory) | §After use |
| `SCBreakGlassAccountNotReady`: account deleted, deactivated, de-privileged, or SSH key missing | §Repair |
| `SCBreakGlassTestOverdue`, or the quarterly schedule | §Drill |
| Rotation (schedule, suspected exposure, personnel change, after use when the credential was displayed to anyone else) | §Rotate |

Not for routine administration. Routine access is the operator kubeconfig and Authentik SSO.

## Preconditions and required material
- The recovery bundle (local `/blackstart/<cluster>/` copy or `s3://<prefix>-recovery/<cluster>/bundles/`, plan 35) and the recovery passphrase (`SC-RECOVERY-PASSPHRASE`, held only by the operator). Alternative source for `SC-BREAK-GLASS-{CLUSTER,AUTHENTIK,FORGEJO}`: `secrets/offcluster/<cluster>/break-glass-*.sops.yaml` in a Git clone, decryptable only with the offline recovery age identity (`./bootstrap exec -- sc-bootstrap bundle open --from-offcluster <CLASS>`, plan 35 CLI).
- The toolbox (`./bootstrap shell`). The bundle is unlocked **locally** only: `./bootstrap exec -- sc-bootstrap bundle open --show <CLASS>` prints a single credential to the terminal; `--extract <CLASS> --to /run/sc/secrets/bg/` writes it to a tmpfs path inside the toolbox container that is deleted when the shell exits.
- Never copy a break-glass credential into the cluster, Git, a chat, a ticket or a log.

## Safety checks before acting
1. Confirm the normal path is really unavailable (record why). Break-glass use is an incident.
2. Open an incident issue in Forgejo (if Forgejo is up) or a local incident note, with the start time. Every action below goes into it.
3. Expect `SCBreakGlassUsed` to page within 5 min. **Never silence it** (plan 42); it expires 24 h after the last use.
4. Use the minimum system necessary; do not chain into other systems "while you are in".

## Steps

### §Cluster (node SSH + node-local kubeconfig)
1. `./bootstrap exec -- sc-bootstrap bundle open --extract SC-BREAK-GLASS-CLUSTER --to /run/sc/secrets/bg/` → key at `/run/sc/secrets/bg/sc-breakglass_ed25519` (mode 0600).
2. Node address: `./bootstrap status --json | jq -r '.inventory.nodes[] | "\(.name) \(.public_ip)"'` (plan 50 journal; also in OpenTofu outputs).
3. `./bootstrap exec -- ssh -i /run/sc/secrets/bg/sc-breakglass_ed25519 -o IdentitiesOnly=yes sc-breakglass@<node-ip>`. Expected: shell on the node. Host-key prompt: compare with the fingerprint recorded in the journal (`.inventory.nodes[].ssh_host_key_fingerprint`); mismatch → stop (possible MITM or reinstalled node) and go to §Infrastructure.
   - `Permission denied (publickey)` → key removed or wrong node: §Repair from another node, or §Infrastructure (provider console).
   - Connection refused/timeout → firewall or node down: §Infrastructure.
4. `sudo k3s kubectl get nodes -o wide`. Expected: node list. Perform only the actions the incident needs, all as `sudo k3s kubectl …` (node-local admin kubeconfig `/etc/rancher/k3s/k3s.yaml`).
5. To restore normal operator access, never copy `k3s.yaml` off the node (AUTH-7): from the toolbox run `./bootstrap exec -- sc-bootstrap kubeconfig refresh --via-ssh sc-breakglass@<node-ip> --identity /run/sc/secrets/bg/sc-breakglass_ed25519` (plans 07/50), which issues a new short-lived operator certificate `CN=sc-operator:<name>` on the node and writes the operator kubeconfig. Routine access then no longer uses `system:admin`, so the audit log keeps attributing break-glass correctly.
6. Exit; `./bootstrap exec -- sh -c 'shred -u /run/sc/secrets/bg/*'`.
Evidence automatically produced: sshd journal (`Accepted publickey for sc-breakglass`), the PAM textfile timestamp (→ `SCBreakGlassUsed{system="cluster"}`), kube-apiserver audit log entries for `system:admin` (→ `SCK3sBreakGlassAuditSeen`, plan 07).

### §Authentik
Option A — password (Authentik up, admins locked out):
1. `./bootstrap exec -- sc-bootstrap bundle open --show SC-BREAK-GLASS-AUTHENTIK`.
2. Browse `https://auth.<BASE_DOMAIN>/if/flow/sc-authentication/` (or the login page), user `sc-breakglass`. Edge down (Cloudflare or Traefik unreachable from outside, AUTH-11): run `./bootstrap edge-bypass` (plan 09) first; it port-forwards Traefik and adds temporary `/etc/hosts` entries, so the same URL works with the real certificate and brand `sc`. MFA is not requested (ADR-0251). Expected: admin interface at `/if/admin/`.
   - "Invalid credentials" → the password in the bundle is not current (rotation not completed): Option B.
3. Fix the lockout (e.g. reset a human admin's MFA: Directory → Users → user → "Reset MFA"/delete authenticators; repair a flow by re-applying blueprints: `restore-authentik.md` §Blueprint). Log out.
Option B — recovery key (password lost, or Authentik login flow itself broken):
1. Cluster access (operator kubeconfig or §Cluster).
2. `./bootstrap exec -- kubectl -n authentik exec deploy/authentik-worker -- ak create_recovery_key 1 sc-breakglass` (verify: first argument is the validity duration in the running version). Expected: a URL `https://auth.<d>/recovery/use-token/<token>/`.
3. Open it once (through `./bootstrap edge-bypass` if the edge is down). Expected: authenticated admin session. Then set a new password for `sc-breakglass` and record that §Rotate is required (the bundle is now stale).
4. Delete leftover recovery tokens: Admin → Tokens, filter intent `recovery`, delete all.
Evidence: Authentik events `login` (user `sc-breakglass`) or `model_created` (recovery token), kube-apiserver audit of the `exec`.

### §Forgejo
1. `./bootstrap exec -- sc-bootstrap bundle open --show SC-BREAK-GLASS-FORGEJO`.
2. Browse `https://git.<BASE_DOMAIN>/user/login`, user `sc-breakglass` (local login form; works with Authentik down). Expected: site-admin access (`/-/admin`).
   - Public edge down: `./bootstrap edge-bypass` (plan 09) and the normal URL, or `./bootstrap exec -- kubectl -n forgejo port-forward svc/forgejo-http 13000:3000` and `http://127.0.0.1:13000/user/login`.
   - "Username or password is incorrect" → `rotate-forge-credentials.md` §F (reset via `sc-admin`).
3. Do only the necessary admin actions. Do not change branch protection in the UI. A merge-gate component that is broken (Runner, Woodpecker, `sc-ci-config`, egress-proxy) is handled by `procedures/emergency-merge.md` (plan 55, protection stage `recovery`), and the black-start recovery push by ADR-0554 (`forge apply --stage bootstrap`); never by a `sc-breakglass` UI merge.
4. Log out.
Evidence: Forgejo `last_login` of `sc-breakglass` (→ `SCBreakGlassUsed{system="forgejo"}` via `platform-probes`), router log `POST /user/login`.

### §Grafana
1. `./bootstrap exec -- sc-bootstrap bundle open --show SC-BREAK-GLASS-GRAFANA`.
2. Browse `https://grafana.<BASE_DOMAIN>/login?disableAutoLogin=true` (verify parameter), user `sc-breakglass`. Expected: Admin role. The login **form** is the only local path; API basic auth is disabled (plan 39, AUTH-8). Port-forward fallback: `./bootstrap exec -- kubectl -n monitoring port-forward svc/grafana 13001:80`.
3. Read-only use is expected (dashboards, Explore). Grafana holds no production write path.
Evidence: Grafana server log `Successful Login` for `sc-breakglass`; `SCGrafanaLocalLogin` (catalogue alert on `grafana_api_login_post_total`, plan 39) pages.

### §Infrastructure (provider, DNS/tunnel, object storage, OpenTofu)
1. `./bootstrap exec -- sc-bootstrap bundle open --extract SC-INFRA-PROVIDER,SC-DNS-API,SC-DNS-TUNNEL,SC-OBJECT-STORAGE/admin,SC-TOFU-ENCRYPTION --to /run/sc/secrets/bg/` → files `infra-provider.env`, `dns-api.env`, `dns-tunnel.env`, `object-storage-admin.env`, `tofu-passphrase`. `SC-BREAK-GLASS-INFRA` itself is the provider-account console login with MFA recovery codes, held offline by the operator only (not in the bundle); use it for the provider console and rescue mode.
2. Use them only through the toolbox: `./bootstrap exec -- sh -c 'set -a; . /run/sc/secrets/bg/infra-provider.env; tofu -chdir=infra/tofu/<module> plan'`, provider console/rescue mode (operator's own provider login), or `./bootstrap recover` (plan 45).
3. Prefer read-only actions first (`GET /me`-class calls, `ListBuckets`, `tofu state list`).
4. `shred -u /run/sc/secrets/bg/*` when done.
Evidence: provider audit logs (OVH API logs/console history, Cloudflare audit log, S3 access logs); the incident note lists every command run.

### §After use (mandatory after any of the above)
1. Write `s3://<prefix>-evidence/breakglass-use/<UTC ts>-<system>.json`: `{system, account, operator, reason, start, end, actions[], incident_ref, credential_displayed_to: [..]}` and attach the source evidence before it rotates away (AUTH-24): for cluster use the sshd journal excerpt and the matching `audit.log` lines from the node (plan 07 keeps them 7 days), for Authentik the event IDs, for Forgejo the router-log lines. Upload with `./bootstrap exec -- sc-bootstrap evidence put breakglass-use <file> --attach <excerpt>…`.
2. Acknowledge, do not silence (AUTH-8): record in the incident issue `alert: SCBreakGlassUsed{system=<system>}; evidence: <object key>`, and link the issue from the evidence object. Do **not** create an Alertmanager silence: plan 42 forbids silencing break-glass alerts, and any matching silence raises `SCBreakGlassAlertSilenced`. The alert resolves by itself 24 h after the last use.
3. Decide rotation: rotate (§Rotate) if the credential was displayed to anyone other than the operator, written to any file outside `/run/sc/secrets/bg/`, used from an untrusted machine, or if Option B of §Authentik was used. Otherwise record "no rotation needed" with the reason.
4. Close the incident with a cause and a follow-up item that removes the need for break-glass next time.

### §Repair (`SCBreakGlassAccountNotReady`)
1. Which account: `sc_breakglass_account_ready == 0` labels `{system, account}`, or `sc_breakglass_ssh_key_present == 0` (cluster).
2. Cluster key missing: run the node baseline for the key only: `./bootstrap exec -- sc-bootstrap configure --tags breakglass` (plan 08; re-installs `infra/ansible/files/breakglass.pub` and the PAM hook). Expected: `sc_breakglass_ssh_key_present` = 1 within 15 min.
3. Authentik `sc-breakglass` missing/inactive/not superuser: `./bootstrap exec -- sc-bootstrap sso-init` (A4 through the admin channel: `kubectl exec … ak shell` with a versioned script, audited by the `pods/exec` audit entry and Authentik events; it re-creates or re-activates the account and restores group membership, and sets the password from the bundle only when creating; AUTH-5). If it exists but the password is unknown → §Rotate (Authentik).
4. Authentik `akadmin` active (`SCAuthentikBootstrapAdminActive`): `./bootstrap exec -- sc-bootstrap sso-init` (A9 teardown through the same admin channel). Investigate who re-activated it (Authentik events `model_updated` on `akadmin`); treat as a security incident.
5. Forgejo `sc-breakglass` missing/not admin: `./bootstrap exec -- sc-bootstrap forge init --only S03`; if present but unverifiable → `rotate-forge-credentials.md` §F.
6. Expected: `SCBreakGlassAccountNotReady` resolves within 30 min. Repairs are themselves security-relevant; record them in the incident note.

### §Drill (quarterly; ≤ 30 min)
1. Announce the drill in the operator channel (alerts will page).
2. `./bootstrap exec -- sc-bootstrap breakglass drill [--phase P07|full]` (`P07`: first drill, before monitoring exists — cluster, Authentik, Forgejo, infrastructure, detection checked on source metrics; `full`, the default from P09: all five systems plus the Alertmanager checks). It prompts for the passphrase, unlocks the bundle in memory and, per system: cluster (SSH + `k3s kubectl get nodes`), Authentik (password login through the flow executor), Forgejo (web form login), Grafana (login **form**, so `SCGrafanaLocalLogin` fires), infrastructure (read-only calls: provider `/me`-class, Cloudflare token verify, S3 `ListBuckets`, `tofu state list` on the state module). It waits ≤ 10 min for `SCBreakGlassUsed` for cluster, Authentik and Forgejo.
2b. Infrastructure attestation (AUTH-23): the tool asks the operator to log in to the provider console with `SC-BREAK-GLASS-INFRA` (MFA) and to type the number of remaining MFA recovery codes; both are recorded in the evidence. Once a year, also run `--only authentik-recovery-key` (Option B end to end).
3. Expected output: every system `passed`, alert latencies ≤ 300 s, evidence written to `s3://<prefix>-evidence/breakglass-drills/<UTC ts>.json`, ConfigMap `monitoring/breakglass-drill-status` `lastSuccess` updated, which the probe exports as `sc_breakglass_drill_last_success_timestamp_seconds`; `SCBreakGlassTestOverdue` fires when it is older than 100 days.
4. Any `failed` system → §Repair (or §Rotate if the credential is wrong), then rerun the drill for that system: `sc-bootstrap breakglass drill --only <system>`.
5. Acknowledge the drill-generated alerts in the drill issue per §After use step 2 (no silence); they resolve 24 h after the drill.

### §Rotate (mechanics; policy and schedule plan 35 `rotate-break-glass.md`)
1. Generate a new value: `./bootstrap exec -- sc-bootstrap bundle stage-rotation <CLASS>` (32+ random chars for passwords; new ed25519 pair for the cluster key). The new value lives only in the local staging area.
2. Apply it:
   - `SC-BREAK-GLASS-AUTHENTIK`: as an `sc-admins` human (or current `sc-breakglass`), Admin → Users → `sc-breakglass` → Set password; or `./bootstrap exec -- sc-bootstrap sso-init --rotate-breakglass` (uses the admin channel via `ak shell` on the worker, verify).
   - `SC-BREAK-GLASS-FORGEJO`: `rotate-forge-credentials.md` §F.
   - `SC-BREAK-GLASS-GRAFANA`: PR replacing SOPS `SC-GRAFANA-ADMIN` with the staged value (Grafana restarts and resets the admin password).
   - `SC-BREAK-GLASS-CLUSTER`: PR replacing `infra/ansible/files/breakglass.pub`, then `./bootstrap exec -- sc-bootstrap configure --tags breakglass` on every node. The old key is removed by the same run.
   - `SC-BREAK-GLASS-INFRA`: change the provider-account console password and regenerate MFA recovery codes in the provider console; store them offline (not in the bundle). The bundled infrastructure toolkit (`SC-INFRA-PROVIDER`, `SC-DNS-API`, `SC-DNS-TUNNEL`, `SC-OBJECT-STORAGE/admin`, `SC-TOFU-ENCRYPTION`) rotates with its own procedures (`rotate-storage-credentials.md`, plan 35).
3. `./bootstrap rotate refresh-bundle` (plan 35): seals a new bundle version locally and to S3 `recovery`, and rewrites `secrets/offcluster/<cluster>/break-glass-*.sops.yaml` (recovery recipient only) for a PR; the passphrase prompt is mandatory.
4. Verify with `./bootstrap exec -- sc-bootstrap breakglass drill --only <system>`. Expected: `passed` with the new value.
5. Only after step 4 passes: discard the staged old value. Old bundle versions remain in S3 (versioned) and still contain the old credential, which no longer works.

## Verification
- Access sections: the intended action succeeded, `SCBreakGlassUsed` fired for the system used, and §After use evidence exists.
- §Repair: `SCBreakGlassAccountNotReady` resolved; `sc_breakglass_account_ready` = 1 for all `{system, account}`.
- §Drill: evidence object with all five systems `passed`; `SCBreakGlassTestOverdue` and `SCBreakGlassTestOverdue` not firing.
- §Rotate: drill for the rotated system passes with the new value; bundle manifest fingerprint changed (`./bootstrap exec -- sc-bootstrap bundle manifest | jq '.entries[] | select(.class=="<CLASS>")'`).

## Rollback / abort
- Access: log out / exit; nothing to roll back except actions taken, which follow their own procedures.
- Rotation: until step 5 the old value is still staged; if the new value cannot be applied everywhere, re-apply the old value and re-seal the bundle (`refresh-bundle`), so bundle and system agree again. Never leave the bundle and the system disagreeing.

## Evidence to record
The §After use object for every use; the drill evidence objects; for rotations, the bundle version number and manifest fingerprint change, the PR links, and the drill result. Authentik event IDs, Forgejo login timestamps and node audit-log excerpts are referenced by time range, not copied.

## Follow-ups
- Any break-glass use needs a post-incident review item explaining why the normal path failed.
- If the drill exposed a gap (alert latency > 5 min, missing detection), open an issue against plan 15/42.
- Keep `procedures/rotate-break-glass.md` (plan 35) consistent with §Rotate.
