# Procedure: Rotate model credentials (provider API keys, Hermes gateway token)

**Owning plan:** plans/53-model-gateway.md (related: plans/31-model-routing.md, plans/33-budget-enforcement.md, plans/26-agent-runner.md, plans/35-secrets.md)
**SPEC references:** §77, §86, §99, §113 ("rotate model credentials")
**Severity / urgency:** routine (scheduled, `maxAge` P90D per `contracts/secret-classes.yaml`); outage-level when a key is compromised (section C)
**Expected duration:** A: 30–60 min (dominated by PR review and Flux sync); B: 10 min; C: 15 min to contain, then A
**Requires break-glass:** no for A (Git PR plus a SOPS-capable age identity); yes for B and C (`SC-RUNNER-ADMIN` + cluster kubeconfig for port-forward)

## When to use

- Scheduled rotation of `SC-MODEL-API-KEYS` (every ≤ 90 days, or when the provider forces it).
- Adding the key for a newly enabled provider (section A, with "old key" steps skipped).
- Alerts:
  - `SCModelCredentialRejected`: the provider returns 401/403 to the Gateway;
  - `SCModelProviderUnavailable`: the key is missing, empty, or has a forbidden prefix such as an Anthropic OAuth `sk-ant-oat…` token (SPEC §77).
- Suspected exposure of a provider key or of the Hermes orchestrator gateway token (section C).
- Forced rotation of the Hermes orchestrator token (section B): after a Hermes incident, before the Runner signing-key (`SC-RUNNER-SIGNING`) removes an old `kid` (section D), or on schedule if automatic rotation failed (`SCHermesCredentialRotationFailed`).

Worker gateway tokens (`SC-GATEWAY-TOKEN`, per execution) are never rotated. They expire with the execution and are revoked at its end. To kill one early, cancel its task or execution (`agent_cancel` or `POST /v1/tasks/<id>/cancel`).

## Preconditions and required material

- Toolbox shell: `./bootstrap shell`, then `sc-env` (kubeconfig + tunnel, plan 50). All commands below run inside it.
- Section A: the provider console account (operator-owned, API organization, **not** a consumer subscription [SPEC §77]), and an age identity able to decrypt SOPS files (the recovery identity, or the cluster key from the recovery bundle, per `procedures/rotate-secrets.md`).
- Sections B–D: your per-operator `SC-RUNNER-ADMIN` credential `scadm_<kid>.<secret>` (issued by `sc-bootstrap access join`; the Runner derives the actor `human:<login>` from the key id, CD-30), or, if you have none, the break-glass credential from the recovery bundle (`procedures/break-glass-access.md`; actor `breakglass:runner-admin`): `read -rs SC_RUNNER_ADMIN; export SC_RUNNER_ADMIN`, and the audit reason `export SC_REASON="<rotation id>: <why>"` (sent as `X-SC-Reason`; identity comes from the credential, CD-30).
- Helpers (same as `procedures/budget-emergency-stop.md`):

  ```sh
  kubectl -n strange-company port-forward deploy/agent-runner 18080:8080 >/tmp/pf-runner.log 2>&1 &
  RUNNER=http://127.0.0.1:18080; AUTH=(-H "Authorization: Bearer ${SC_RUNNER_ADMIN}" -H "X-SC-Reason: ${SC_REASON}")   # no X-SC-Actor: it is rejected (CD-30)
  gw_status() { for p in $(kubectl -n strange-company get pods -l app.kubernetes.io/name=model-gateway -o name); do
      kubectl -n strange-company port-forward "$p" 19090:9090 >/dev/null 2>&1 & pf=$!; sleep 2
      echo "== $p"; curl -fsS http://127.0.0.1:19090/sc/v1/status | jq '.providers'; kill $pf; done; }
  ```

- Paths: the SOPS file `gitops/clusters/<cluster>/secrets/strange-company/model-api-keys.sops.yaml` (plan 35 register) becomes the Secret `strange-company/model-api-keys`. Its keys are the `credential.secretKey` values of `policy/model-routing.yaml` (`anthropic`, `openai`, `deepseek`, `zai`, `fake`, `fake-b`).

## Safety checks before acting

1. **Never** paste a key into a PR description, issue, chat, Hermes, or a shell history that persists. In the toolbox, use `read -rs NEW_KEY`.
2. The new key must be an **API key** of an API/organization account. Reject keys whose prefix appears in that provider's `forbiddenKeyPrefixes` (for Anthropic, OAuth access tokens start with `sk-ant-oat`): the Gateway would refuse to load them and mark the provider unavailable.
3. Do **not** patch the Kubernetes Secret directly with `kubectl`. Flux restores the Git version at the next reconcile, which would silently reinstate the old key. Git is the only path.
4. Keep the old key valid until step A6 passes. That gives a zero-downtime rotation (the Gateway hot-reloads keys every 30 s).
5. Check `curl -fsS "$RUNNER/v1/admin/emergency-stop" "${AUTH[@]}"` (or `gw_status`) so that a pre-existing stop is not mistaken for a key failure.

## Steps

### A. Rotate (or add) a provider API key

1. **Create the new key** in the provider console, with the same project/organization as the old one and a spend limit at the provider as defense in depth (optional, not relied upon, plan 33 §24). Expected: the console shows two active keys. `read -rs NEW_KEY` in the toolbox.
2. **Record the expected fingerprint:** `printf %s "$NEW_KEY" | sha256sum | cut -c1-12`. Write this 12-hex value into the rotation record. It is not secret (plan 53 §10).
3. **Update the SOPS file on a branch:**

   ```sh
   git switch -c rotate/model-key-<provider>-$(date -u +%Y%m%d)
   sops gitops/clusters/<cluster>/secrets/strange-company/model-api-keys.sops.yaml   # opens $EDITOR decrypted
   # set stringData.<secretKey> to the new key, save, quit
   sops -d gitops/clusters/<cluster>/secrets/strange-company/model-api-keys.sops.yaml | yq '.stringData | keys'
   git add -A && git commit -m "chore(secrets): rotate <provider> model API key" && git push -u origin HEAD
   ```

   - Expected: the decrypted key list still contains every provider. The diff shows only ciphertext and `sops.lastmodified`/`mac` changes.
   - `sops` cannot decrypt → wrong or missing age identity (`procedures/rotate-secrets.md` §identities). Stop here.
4. **Open the PR, let CI pass, and merge (human).** CI runs the plan 35 SOPS lint: both recipients are present and no plaintext is committed. Then speed up Flux: `flux reconcile source git flux-system && flux reconcile kustomization <secrets kustomization> --with-source` (names: `flux get kustomizations`). Expected: the Kustomization is Ready at the merge SHA.
5. **Confirm the Gateway loaded it** (≤ 2 min: kubelet Secret-volume sync plus the 30 s key reload): `gw_status`.
   - Expected on **every** replica: `.providers.<provider>.key_fingerprint` equals step 2, and `.enabled == true`. `sc_gateway_provider_available{provider="<p>"}` = 1.
   - Still the old fingerprint after 5 min: check that the Secret changed (`kubectl -n strange-company get secret model-api-keys -o jsonpath='{.metadata.resourceVersion}'` before and after) and the Flux events. A rollout restart (`kubectl -n strange-company rollout restart deploy/model-gateway`) is safe: in-flight streams finish within the grace period.
   - `available == false` → the key is empty or has a forbidden prefix. Fix the key and go back to step 3.
6. **Prove the new key works upstream.** With organic traffic, `sc_gateway_upstream_responses_total{provider="<p>",code=~"401|403"}` must not increase over 15 min while 2xx responses occur. Without traffic, run the opt-in smoke for that provider: `make smoke-providers PROVIDERS=<p>` (plan 53 §22.5; ≤ 2 requests of 16 output tokens). It writes `evidence/provider-smoke/<date>-<p>.json` with `normalization_invariants_ok: true`.
   - 401/403 → the key is wrong or lacks API access. Keep the old key active, fix, and repeat from step 3.
7. **Revoke the old key** in the provider console. Expected: the console shows one active key. Watch `SCModelCredentialRejected` for 15 min: it must stay inactive.

### B. Rotate the Hermes orchestrator gateway token (Runner-managed, ADR-0401)

1. Current state:

   ```sh
   kubectl -n strange-company exec -i sc-pg-1 -c postgres -- psql -d strange_company -At -c \
    "SELECT jti, issued_at, expires_at, revoked_at FROM ledger.gateway_tokens WHERE token_kind='orchestrator' ORDER BY issued_at DESC LIMIT 3;"
   ```

   This is a read-only query. If `sc-pg-1` is not the primary, use the pod labelled `role=primary` (verify the CNPG naming at implementation).
2. Force rotation through the Runner admin API. This is an endpoint requested from plan 26 (`POST /v1/admin/hermes-credentials:rotate`):

   ```sh
   curl -fsS -X POST "$RUNNER/v1/admin/hermes-credentials:rotate" "${AUTH[@]}" -H 'Content-Type: application/json' \
     -H "Idempotency-Key: hermes-rotate-$(date -u +%Y%m%dT%H%M%SZ)" \
     -d '{"credentials":["gateway_token"],"revoke_previous":"after_overlap","reason":"<why>"}' | jq .
   ```

   - Use `"revoke_previous":"immediately"` for compromise (section C).
   - Expected: 200 with the new jti. The Runner writes the new token and the model pin into Hermes' managed Secret and restarts Hermes (plan 24/26).
   - If the endpoint is absent (pre-implementation of the request), the daily rotation check is the only path. Revoking the old token without a replacement stops Hermes (`SC_TOKEN_REVOKED`). Accept that only under compromise.
3. Verify (≤ 5 min): `kubectl -n hermes rollout status deploy/<hermes deployment>` completes (name per plan 24). Ask Hermes one question in its dashboard. A new `ledger.cost_records` row with `token_kind='orchestrator'` and `token_jti` equal to the new jti appears.
   - The query of step 1 shows the old jti with `revoked_at` set (immediately), or `expires_at`/`revoked_at` within `hermes.overlapHours` (after overlap).
   - Hermes errors with `SC_TOKEN_*` → `procedures/hermes-failure.md`.

### C. Compromise of a provider key or the Hermes token

1. **Contain first.** Engage an emergency stop: `procedures/budget-emergency-stop.md` A1, or A3 if the Runner is unavailable.
   - Level `all` if the compromised key is the provider Hermes uses or if you are unsure.
   - Level `workers` if the compromised key serves only worker tiers.
2. **Provider key:** revoke it **immediately** in the provider console. Accept that calls to that provider fail until step 3. Routing skips the provider once the Gateway marks it unavailable, or calls fail with 401. Then perform section A steps 1–6 with a fresh key. Skip step 7, because the old key is already revoked.
3. **Hermes token:** section B with `"revoke_previous":"immediately"`.
4. **Measure exposure:** compare the provider console usage for the suspected window with the ledger:

   ```sh
   kubectl -n strange-company exec -i sc-pg-1 -c postgres -- psql -d strange_company -At -c \
    "SELECT date_trunc('hour', started_at), sum(cost_total_micro_usd)/1e6, count(*) FROM ledger.cost_records
      WHERE provider='<p>' AND started_at BETWEEN '<from>' AND '<to>' GROUP BY 1 ORDER BY 1;"
   ```

   Provider usage clearly above the ledger means the key was used outside the Gateway. Treat it as a secret leak (Git, Secret, Gateway pod or operator machine) and follow `procedures/credential-compromise.md#model-provider-keys`.
5. Release the stop (`procedures/budget-emergency-stop.md` G) once the new credential is verified.

### D. Runner signing key rotation: ordering constraint for gateway tokens

The rotation of `SC-RUNNER-SIGNING` itself is `procedures/rotate-secrets.md` (plan 35). Gateway tokens add these ordering rules:

1. Publish the new public key **alongside** the old one in `sc-runner-jwks`. Wait until `gw_status`/`/readyz` shows both `kid`s loaded on every Gateway replica.
2. Only then switch the Runner to sign with the new key.
3. Before removing the old `kid`, run section B. The Hermes token lives up to 31 days and would otherwise become invalid (the Runner also re-mints it automatically when the signing `kid` changes, ADR-0451). Also wait ≥ 6 h, the maximum execution-token lifetime.
4. Remove the old `kid`. Expected: no `SC_TOKEN_INVALID` spike (`sc_gateway_denials_total{code="SC_TOKEN_INVALID"}` flat).

## Verification

- A: every Gateway replica reports the new fingerprint, `sc_gateway_provider_available{provider}` = 1, there are no 401/403 upstream responses for 15 min after old-key revocation, and the smoke evidence is fresh (≤ 31 days, as 53-AC-17 requires).
- B: exactly one non-revoked orchestrator token after the overlap. Hermes answers, and the answer's ledger row carries the new jti.
- C: provider console shows the old key revoked, the ledger-vs-console comparison is recorded, and the emergency stop is released.

## Rollback / abort

- A before step 7: revert the PR (a new PR restoring the previous SOPS file). The old key is still valid, so service continues.
- A after step 7 with a non-working new key: create another key and repeat A. Meanwhile engage the stop for that provider's workloads, or let routing skip the unavailable provider (plan 31 §14.2).
- B: the previous Hermes token remains valid during the overlap if `after_overlap` was used. Otherwise rotate again.

## Evidence to record

In the rotation record (Forgejo issue, label `rotation/model-credentials`), or the incident issue for C:
- provider, date, actor, PR link;
- old and new 12-hex fingerprints;
- smoke evidence file name;
- time of old-key revocation;
- for B, the old and new jti prefixes (first 8 characters);
- for C, the exposure comparison.

**Never record a key.**

## Follow-ups

- Next rotation due date (90 days), per `contracts/secret-classes.yaml`.
- If `SCModelCredentialRejected` was the trigger, check whether the provider changed its auth requirements, and update `policy/model-routing.yaml` (`authScheme`, `forbiddenKeyPrefixes`) by PR.
- Section C → a post-incident review of how the key leaked.
