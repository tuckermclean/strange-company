# Procedure: Manage human access (join, leave, lost MFA device, lost device)

**Owning plan:** plans/15-authentik.md (§14.5, review AUTH-10)
**SPEC references:** §21 (human identity, human/machine separation), §22, §113 ("add procedures discovered to be missing")
**Severity / urgency:** routine (join, leave) · urgent (lost device, suspected account compromise: start within 1 h)
**Expected duration:** 10–20 min per person
**Requires break-glass:** no, unless the affected person is the only `sc-admins` member and lost their MFA device (then `break-glass-access.md` §Authentik Option A)

## When to use
- A person joins and needs access to Forgejo, Grafana, Hermes and Woodpecker (§Join).
- A person leaves, changes role, or their account must be disabled (§Leave).
- A person lost their TOTP/WebAuthn device but still controls their password (§Lost MFA device).
- A laptop or phone holding sessions, PATs or MFA factors is lost or stolen (§Lost device). For the operator's laptop also follow `credential-compromise.md#operator-laptop` (plan 35); this procedure covers the identity part.

## Preconditions and required material
- Toolbox (`./bootstrap shell`) with the operator kubeconfig.
- An Authentik admin session (`sc-admins` member) or, if none is available, `sc-breakglass` (`break-glass-access.md` §Authentik).
- The `sc-bootstrap access` commands (plan 50 CLI): `access join|leave|reset-mfa|revoke-sessions <user>`. Each command is check-then-act, prints the planned changes, asks for typed confirmation `<verb> <user>`, and writes `s3://<prefix>-evidence/access/<UTC ts>-<user>.json`.
- Role → group mapping: engineer → `sc-engineers`; viewer → `sc-viewers`; administrator → `sc-admins` (requires a second administrator's approval recorded in the evidence).

## Safety checks before acting
1. Never remove the last active `sc-admins` member; `access leave` refuses it (`ACCESS_LAST_ADMIN`). Add the successor first.
2. Never touch machine accounts (`sc-*`, `akadmin`, `sc-audit`, `synthetic-verifier`) with this procedure; they have their own lifecycle (plans 15, 16, 35).
3. For §Lost device and suspected compromise, revoke first and investigate afterwards.
4. Usernames follow Authentik `username` = Forgejo login (`nickname` mapping, plan 16); usernames starting with `sc-`, `admin` or `root` are refused by the Forgejo access policy.

## Steps

### §Join
1. `./bootstrap exec -- sc-bootstrap access join <user> --email <e> --name "<n>" --role engineer|viewer|admin`. Expected: Authentik user created in path `sc/humans` with the role group; a one-time recovery link (30 min) printed to the terminal only. If the user exists: `ACCESS_EXISTS`, no change.
2. Send the link to the person over a separate channel. At first login the `sc-recovery` flow sets the password and forces MFA enrolment (plan 15 §9.2).
3. The person logs in to `https://git.<d>` once; Forgejo auto-registers the account and maps teams from groups. Expected: `$FJ GET /orgs/strange-company/teams/<team>/members` (`$FJ` = `./bootstrap exec -- sc-bootstrap forge api`) lists the user.
4. If the person needs Git over HTTPS, they create their own Forgejo PAT; PATs keep working while Authentik is down.
5. **Runner admin credential (operators only, CD-30):** `access join … --runner-admin` generates the per-operator `SC-RUNNER-ADMIN` credential locally, shows it **once** to the person, and opens a PR that adds only its key id and hash to `gitops/clusters/<c>/secrets/strange-company/runner-admin-credentials.sops.yaml`. After the PR merges, verify with the person's credential: `GET /v1/admin/whoami` (through the Runner admin port-forward, plan 26) returns their operator identity and key id. The identity comes from the credential; no actor header is sent.

### §Leave
1. `./bootstrap exec -- sc-bootstrap access leave <user>`. It performs, in order, and reports each result:
   - Authentik: `PATCH /api/v3/core/users/<pk>/` `{is_active: false}`; delete all sessions (`/core/authenticated_sessions/`), tokens (`/core/tokens/?user=<pk>`), consents and MFA devices (`/authenticators/admin/…`) (verify endpoints on the pinned version).
   - Forgejo: `PATCH /api/v1/admin/users/<u>` `{prohibit_login: true}`; delete every PAT and OAuth2 grant of the user (admin API); remove team memberships.
   - Grafana: delete the user (`DELETE /api/admin/users/<id>`); this also ends Grafana sessions.
   - Hermes: invalidate the user's dashboard sessions (plan 24 admin endpoint).
   - Woodpecker: delete the user's token and the user (`DELETE /api/users/<login>`, plan 20), after confirming no repository is activated with that user (activations belong to `sc-woodpecker`).
   - Runner admin credential (CD-30), first and immediately: `POST /v1/admin/credentials/{kid}:revoke` for each of the person's key ids (expected: a later `GET /v1/admin/whoami` with that credential returns 401); then a PR deletes the hash entries from `runner-admin-credentials.sops.yaml`.
2. Expected within 5 min (15-AC-33): the user's former Forgejo PAT returns 401; a pre-existing Forgejo session cookie is redirected to login (verify Forgejo behavior); an Authentik login attempt is denied.
3. Reassign anything the person owned: open issues and PRs (Forgejo), Grafana dashboards they created manually (dashboards in Git are unaffected).

### §Lost MFA device
1. Confirm the person's identity out of band (video call or a second channel known before the request).
2. `./bootstrap exec -- sc-bootstrap access reset-mfa <user>`: deletes the user's authenticators and sessions. If the person is the only `sc-admins` member and cannot log in, an operator performs this through `sc-breakglass` (`break-glass-access.md` §Authentik Option A); that is a break-glass use with its §After use steps.
3. The person logs in; the MFA stage forces enrolment of a new device. Expected: `GET /api/v3/authenticators/admin/all/?user=<pk>` lists exactly the new device.

### §Lost device
1. `./bootstrap exec -- sc-bootstrap access revoke-sessions <user> --all`: Authentik sessions, tokens and MFA devices; Forgejo PATs, OAuth2 grants and sessions; Grafana and Hermes sessions; Woodpecker token; the person's Runner admin credentials (`:revoke`, CD-30; re-issue with `access join --reactivate --runner-admin`). The account itself stays active.
2. Continue with §Lost MFA device step 3 (re-enrolment) and ask the person to rotate their password through `sc-recovery` (a new recovery link from `access join --resend <user>`).
3. If the device held operator material (kubeconfig, `bootstrap.env`, bundle copy): follow `credential-compromise.md#operator-laptop` (plan 35) and `break-glass-access.md` §Cluster step 5 to re-issue the operator certificate.

## Verification
- §Join: the person logs in to Forgejo, Grafana and (if `sc-engineers`/`sc-admins`) Hermes with MFA; group and team membership match the role.
- §Join: `GET /v1/admin/whoami` returns the operator identity for each new Runner admin credential.
- §Leave: the three checks of §Leave step 2 pass, the revoked Runner admin key ids return 401; `GET /api/v3/core/users/?username=<u>` shows `is_active: false`.
- §Lost MFA device / §Lost device: the old device no longer completes a login (no authenticator listed); old PATs return 401.

## Rollback / abort
- §Join: `access leave <user>`.
- §Leave: re-activate with `access join --reactivate <user>` (restores `is_active`, `prohibit_login: false` and groups); deleted PATs, grants and MFA devices are not restored and must be recreated by the person.
- §Lost MFA device / §Lost device: none needed; the person re-enrols.

## Evidence to record
The evidence object written by each `sc-bootstrap access` command (actor, target user, steps and results, timestamps), plus for administrator joins the approving administrator, and for break-glass use the `break-glass-access.md` §After use object.

## Follow-ups
- Quarterly: compare `sc-admins` membership with the intended administrator list and record the review in the drill evidence (plan 15 §14.4.5).
- If a revoked token was used after revocation (Forgejo or Authentik logs), open a security incident.
