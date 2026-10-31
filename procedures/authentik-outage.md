# Procedure: Authentik outage

**Owning plan:** plans/15-authentik.md
**SPEC references:** §21, §22, §96, §97
**Severity / urgency:** outage for human SSO (new logins to Forgejo, Grafana, Hermes, Prometheus and Alertmanager UIs fail). Machine identity is unaffected.
**Expected duration:** 10–45 min triage and repair; escalate to `restore-authentik.md` if data is damaged
**Requires break-glass:** usually no. Forgejo/Grafana break-glass only if humans must act in those UIs during the outage (`break-glass-access.md`).

Alert names are the catalogue names in `contracts/alert-catalog.yaml` (CD-01).

## When to use
- `SCAuthentikDown`: health endpoint failing 5 min.
- `SCAuthentikWorkerDown`, `SCAuthentikTasksFailing` (§Worker).
- `SCOIDCLoginBroken` (`sc_oidc_discovery_ok == 0`), or users report `invalid_client`/redirect errors (§OIDC).
- Forward-auth on `prometheus.<d>`/`alertmanager.<d>` returning 5xx (§Forward-auth).
- `SCCnpgClusterDown{cluster="authentik-pg"}` / `SCCnpgClusterDown` for `authentik` namespace (go to plan 12 first, then return here).

## Preconditions and required material
- Toolbox with the operator kubeconfig (`./bootstrap shell`). If the kubeconfig is unavailable: `break-glass-access.md` §Cluster.
- No Authentik credential is required for any step here.

## Safety checks before acting
1. Confirm scope: are machines unaffected? `./bootstrap exec -- flux get sources git -n flux-system` (Ready) and `./bootstrap exec -- kubectl -n strange-company logs deploy/agent-runner --since=10m | grep -c ' 401 '` (expect 0). If machines also fail, the problem is wider (Forgejo, network): go to the relevant procedure.
2. Do not delete `authentik-pg`, PVCs or Secrets in this procedure. Do not change `AUTHENTIK_SECRET_KEY`.
3. Do not "fix" blueprint-managed objects in the admin UI; changes are overwritten and hide the real cause.
4. Tell users: existing sessions keep working; Forgejo PATs keep working for Git over HTTPS; break-glass logins exist for emergencies only.
5. Any Git change needed while Authentik is down (e.g. a values fix) uses the identity-outage change path of `restore-authentik.md` safety check 6 (PAT push + cluster-admin apply with the Kustomization suspended), never a `sc-breakglass` merge (AUTH-6).

## Steps
1. **Pods and events.** `./bootstrap exec -- kubectl -n authentik get pods,deploy,hr -o wide` and `./bootstrap exec -- kubectl -n authentik get events --sort-by=.lastTimestamp | tail -30`.
   Expected: `authentik-server` and `authentik-worker` Running/Ready. Branch:
   - `CrashLoopBackOff` → step 2.
   - `Pending` (insufficient memory, PriorityClass preemption) → `./bootstrap exec -- kubectl describe pod …`; check node memory (`SCNodeMemoryPressure`) and plan 51 capacity; free capacity by pausing workers (`./bootstrap exec -- sc-bootstrap estop set --level workers --reason authentik-capacity`), never by lowering Authentik limits ad hoc.
   - HelmRelease `UpgradeFailed` → §Upgrade.
   - All Running but health failing → step 3.
2. **Crash cause.** `./bootstrap exec -- kubectl -n authentik logs deploy/authentik-server --previous --tail=100`.
   - `connection refused`/`could not connect to server` on 5432, or `too many connections` → `./bootstrap exec -- kubectl -n authentik get cluster authentik-pg -o jsonpath='{.status.phase}'`. Not healthy → plan 12 `cnpg-primary-loss.md`. `too many connections` → check `cnpg_backends_total` vs `max_connections` (failure A15); raise `max_connections` by PR (plan 12) and restart: `./bootstrap exec -- kubectl -n authentik rollout restart deploy/authentik-server deploy/authentik-worker`.
   - Migration errors after a version change → §Upgrade.
   - `SECRET_KEY`-related errors or mass `invalid signature` → compare hashes: `./bootstrap exec -- sc-bootstrap authentik key-hash --source git` vs `--source pod`. Mismatch → the SOPS Secret delivery is wrong; check `./bootstrap exec -- flux get kustomization authentik` for decryption errors (`sops-decryption-failure.md`, plan 35).
3. **Health from inside.** `./bootstrap exec -- kubectl -n authentik exec deploy/authentik-server -- ak healthcheck` (exit 0 expected) and `./bootstrap exec -- kubectl -n authentik exec deploy/authentik-server -- curl -fsS http://localhost:9000/-/health/ready/`.
   - Healthy inside, failing from outside → edge problem: `./bootstrap exec -- curl -sv https://auth.<BASE_DOMAIN>/-/health/live/`; check `SCEdgeDown`, Traefik IngressRoute `authentik` (`kubectl -n authentik get ingressroute`), certificate (`kubectl -n authentik get certificate`) → `certificate-failure.md` / `edge-ingress-failure.md` (plans 10/09). Meanwhile admins reach Authentik and every other UI with `./bootstrap edge-bypass` (plan 09: local Traefik port-forward plus temporary `/etc/hosts` entries, real hostnames and certificates, brand `sc`; AUTH-11). Never use a plain port-forward to `authentik-server` for logins.
4. **Restart once** if steps 1–3 show no specific cause: `./bootstrap exec -- kubectl -n authentik rollout restart deploy/authentik-server deploy/authentik-worker`; wait ≤ 10 min for Ready. Expected: `SCAuthentikDown` resolves within 10 min. If it does not, collect logs (Evidence) and go to `restore-authentik.md` if the database content is suspected, otherwise escalate.

### §Worker
1. `./bootstrap exec -- kubectl -n authentik logs deploy/authentik-worker --tail=200`. Look for task failures (`blueprints_discovery`, `outpost` tasks).
2. Check task status in the UI only if an admin can log in; otherwise `./bootstrap exec -- kubectl -n authentik exec deploy/authentik-worker -- ak shell -c "from authentik.events.models import SystemTask; print([(t.name, t.status) for t in SystemTask.objects.all()])"` (verify model name for the running version).
3. A worker outage delays blueprint application and event processing but does not block logins. Restart the worker; if blueprint tasks fail, go to `restore-authentik.md` §Blueprint.

### §OIDC (one application's login broken)
1. Discovery: `./bootstrap exec -- curl -fsS https://auth.<d>/application/o/<slug>/.well-known/openid-configuration | jq -r .issuer`. Wrong/404 → blueprint missing or failed → `restore-authentik.md` §Blueprint.
2. `invalid_client` → client secret mismatch between `authentik` and the consumer namespace (failure A8). Compare: `./bootstrap exec -- sc-bootstrap secrets compare SC-OIDC-CLIENTS/<app>` (hashes of both SOPS files). Fix with one PR that sets both files to the same value; Authentik re-applies its blueprint and the app restarts.
3. Redirect URI error → the consumer hostname changed; fix the blueprint redirect URI by PR.
4. Forgejo-specific: `./bootstrap exec -- kubectl -n forgejo exec deploy/forgejo -- forgejo admin auth list` shows source `authentik`. If missing, check that Secret `forgejo/forgejo-oidc-values` exists and the HelmRelease applied it (plan 16 §9.3).

### §Forward-auth
1. `./bootstrap exec -- curl -s -o /dev/null -w '%{http_code}\n' https://prometheus.<d>/` → expect 302 when unauthenticated. 5xx → check the embedded outpost: `./bootstrap exec -- kubectl -n authentik logs deploy/authentik-server | grep -i outpost | tail -20` and the Middleware `monitoring/authentik-forwardauth` address.
2. Meanwhile use `./bootstrap exec -- kubectl -n monitoring port-forward svc/kps-prometheus 9090` (and `svc/kps-alertmanager 9093`) for triage.

### §Upgrade
1. `./bootstrap exec -- flux -n authentik get hr authentik` and `describe` for the failure. HelmRelease upgrade remediation is off by design (plan 15 §19).
2. Migration failure → `restore-authentik.md` §Upgrade rollback. Values/rendering failure (no pod change) → revert PR on Forgejo.

## Verification
1. `./bootstrap exec -- curl -fsS https://auth.<BASE_DOMAIN>/-/health/ready/` = 200/204.
2. `sc_oidc_discovery_ok{app}` = 1 for `forgejo`, `grafana`, `hermes-dashboard` (Prometheus via port-forward).
3. A human login through Forgejo succeeds (or `./bootstrap exec -- sc-bootstrap authentik login-test` exits 0).
4. `SCAuthentikDown`, `SCAuthentikWorkerDown`, `SCOIDCLoginBroken` resolved.

## Rollback / abort
Steps here are restarts and PR reverts. Each is reversible by reverting the PR or restarting again. Abort means leaving Authentik down and using break-glass logins while escalating.

## Evidence to record
Incident issue in Forgejo with: alert timeline, pod logs (last 200 lines of server and worker, scrubbed of tokens), the chosen branch and fix (PR link), whether any break-glass credential was used (and its evidence object), and the duration of the outage.

## Follow-ups
- If capacity caused it, open a plan 51 issue with the measured working set.
- If a blueprint PR caused it, add a k3d blueprint-apply regression case (plan 15 §22).
- If break-glass was used, complete `break-glass-access.md` §After use.
