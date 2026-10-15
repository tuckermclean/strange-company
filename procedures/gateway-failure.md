# Procedure: Model Gateway failure, degraded accounting and routing failures

**Owning plan:** plans/53-model-gateway.md (related: plans/32-cost-accounting.md, plans/31-model-routing.md, plans/33-budget-enforcement.md, plans/26-agent-runner.md, plans/12-cnpg.md)
**SPEC references:** §78–§81, §88, §96, §113 ("Agent Runner failure" companion; Gateway failure added by Stage 0)
**Severity / urgency:** outage for autonomous work (`SCModelGatewayDown`, `SCGatewayNotReady`). Core platform services are unaffected, and the failure is fail-safe: no model spend is possible while the Gateway is down. Degraded for the accounting and routing sections.
**Expected duration:** 10–60 min depending on section
**Requires break-glass:** cluster kubeconfig (`SC-K3S-ADMIN-KUBECONFIG`) for diagnosis. `SC-RUNNER-ADMIN` only where a section says so. Fixes to policy, keys or chart values go through Git PRs.

## When to use

| Alert (catalogue name, CD-01) | Section |
|---|---|
| `SCModelGatewayDown` | §down |
| `SCGatewayNotReady` | §ready |
| `SCLedgerWriteFailing`, `SCLedgerRollupStale`, `SCBudgetMetricsStale` | §ledger |
| `SCGatewayPolicyInvalid` | §ready (policy) |
| `SCModelProviderUnavailable`, `SCModelCredentialRejected` | §provider |
| `SCModelProviderErrorsHigh` | §upstream |
| `SCModelUsageUnknown`, `SCUsageNormalizationInconsistent` | §usage |
| `SCGatewayBoundViolation` (critical); requests failing `SC_MODEL_QUARANTINED` | §bound |
| `SCModelMismatch` | §mismatch |
| spike of `SC_TOKEN_INVALID` denials; readiness reason `jwks` | §tokens |
| `SCEmergencyStopSourceUnavailable` | §estop |
| `SCGatewayLatencyOverheadHigh` | §latency |
| `SCRoutingTierEmpty`, `SCRoutingUnresolvable`, dispatch refused with `DEPENDENCY_UNAVAILABLE` (Gateway) | §routing |
| executions failing with `SC_REQUEST_FEATURE_DENIED` (typically after a harness or Hermes upgrade) | §allowlist |

Symptoms without an alert: workers fail with `model_unavailable`; Hermes answers with `finish_reason: "error"` (L-1); `agent_start` returns `ROUTING_UNRESOLVABLE`.

## Preconditions and required material

- Toolbox: `./bootstrap shell`, then `sc-env` (plan 50). All commands run inside it.
- Helpers:

  ```sh
  NS=strange-company; SEL=app.kubernetes.io/name=model-gateway          # label per plan 54 (verify)
  kubectl -n monitoring port-forward svc/prometheus-operated 19091:9090 >/tmp/pf-prom.log 2>&1 &
  PROM=http://127.0.0.1:19091
  promq() { curl -fsS --get "$PROM/api/v1/query" --data-urlencode "query=$1" | jq -r '.data.result[] | "\(.metric) \(.value[1])"'; }
  gw() { for p in $(kubectl -n $NS get pods -l $SEL -o name); do
      kubectl -n $NS port-forward "$p" 19090:9090 >/dev/null 2>&1 & pf=$!; sleep 2
      echo "== $p"; curl -sS "http://127.0.0.1:19090$1" | jq .; kill $pf; done; }
  psqlro() { kubectl -n $NS exec -i "$(kubectl -n $NS get pods -l cnpg.io/cluster=sc-pg,role=primary -o name | head -1)" \
      -c postgres -- psql -d strange_company -At -c "$1"; }   # read-only queries only; CNPG labels per plan 12 (verify)
  ```

## Safety checks before acting

1. **Gateway down is fail-safe.** Do not "restore model access" by giving workers or Hermes provider keys, by opening their egress to provider APIs, or by pointing harnesses at providers directly. Each of these bypasses accounting and budgets (ADR-0004), and plan 36 forbids them.
2. Do not disable readiness checks or set `fakeUpstream`/test-only providers in production to make the Gateway "ready".
3. Do not modify `ledger.*` rows by SQL. Use read-only queries. Corrections go through cost adjustments (`procedures/pricing-update.md` §E).
4. If unexpected spend is suspected while you diagnose, engage a stop first (`procedures/budget-emergency-stop.md`).

## Steps

### §down — no ready Gateway pod

1. `kubectl -n $NS get deploy model-gateway; kubectl -n $NS get pods -l $SEL -o wide`. Expected: you see why pods are missing.
2. By pod state:
   - **`ImagePullBackOff`/`ErrImagePull`:** the registry or digest is the problem. Check the image reference in the chart values and the Forgejo registry (plan 23). Upstream images must be pullable by digest.
   - **`CrashLoopBackOff`:** `kubectl -n $NS logs <pod> --previous --tail=100`. The startup errors are:
     - `schema version` → migrations not applied. The Runner applies every `sc-pg` migration in-process at its startup (CD-04): check that the Runner is Ready at the expected version (`kubectl -n $NS get deploy agent-runner`, logs `migration`) and follow `procedures/agent-runner-failure.md`. The Gateway never migrates; it waits until `runner.schema_migrations` reaches its required version.
     - `policy` → see §ready (policy).
     - `jwks` → see §tokens.
   - **`OOMKilled`** (`kubectl -n $NS describe pod <pod>`): raise `modelGateway.resources.limits.memory` by PR (plan 54 values). As a temporary measure lower `modelGateway.limits.maxInflightPerReplica`.
   - **`Pending`:** scheduling or capacity (plan 51). The Gateway's PriorityClass is platform tier, so check for node pressure (`kubectl describe node`).
3. After the fix, `kubectl -n $NS rollout status deploy/model-gateway --timeout=5m` must succeed, and `gw /readyz` must return `{"ready":true}` on every pod.
4. Running worker executions that failed meanwhile are classified `infrastructure` by the Runner and retried per plan 29. Nothing else to do.

### §ready — pods running but not ready

1. `gw /readyz`. The `reasons` array names the cause: `db`, `policy`, `jwks`, `schema`, `providers`.
2. **`db`:** `kubectl -n $NS get cluster sc-pg -o jsonpath='{.status.phase}{"\n"}{.status.currentPrimary}{"\n"}'`.
   - Phase not healthy → `procedures/cnpg-primary-loss.md` / `procedures/cnpg-replica-loss.md`.
   - Healthy → check that the Gateway credential Secret exists: `kubectl -n $NS get secret sc-pg-gateway` (plan 35). Then check that the NetworkPolicy allows Gateway → `sc-pg-rw:5432` (plan 36): run a throw-away pod **with the Gateway's pod labels**, so the same policies apply:
     `kubectl -n $NS run gw-netcheck --rm -i --restart=Never --labels="$SEL" --image=<toolbox image digest from toolbox/image.lock> -- sh -c 'nc -zvw3 sc-pg-rw 5432'`.
     Expected: `open`. A timeout points to the NetworkPolicy (plan 36). The `restricted` PSA level of the namespace applies to this pod, and the toolbox image runs non-root (plan 02; verify that it satisfies `restricted`). The Service name `sc-pg-rw` follows CNPG convention (plan 12).
3. **`policy`:** `gw /sc/v1/status | jq .policy.last_load_error`. On a checkout of the deployed commit, run `make policy-check`. Fix by PR. Readiness is lost only when no valid policy was ever loaded; otherwise last-known-good stays and only `SCGatewayPolicyInvalid` fires.
4. **`schema`:** the migrations are behind the Gateway version. Same as §down (`schema version`).
5. **`providers`:** no provider has a usable key. See §provider. In a fresh install the `fake` key must exist (plan 35 seeds it).

### §ledger — writes, rollups or budget gauges failing

1. `promq 'increase(sc_gateway_settlement_failures_total[15m])'` and `promq 'sc_gateway_reservations_open'`. Then the Gateway logs: `kubectl -n $NS logs -l $SEL --since=30m | grep -E 'settle|ledger' | tail -50`.
2. Typical causes:
   - DB disk full → `SCCnpgDatabaseLarge`, plan 12 procedures;
   - permission or trigger errors after a migration → roll back the Gateway version by PR, see `procedures/agent-runner-failure.md` §rollback for migrations;
   - lock timeouts → see §latency.
3. Settlements are retried in memory for 10 min, then the sweeper settles expired reservations at **worst case**. Budgets over-count and never under-count, so no manual action is needed for correctness. Confirm with `psqlro "SELECT state, count(*) FROM ledger.reservations WHERE created_at > now()-interval '1 day' GROUP BY 1;"`: `open` must decrease to ~0 within `timeouts.total + expiryGrace` after recovery.
4. `SCLedgerRollupStale` / `SCBudgetMetricsStale`: check that a leader exists: exactly one replica logs `leader acquired`. A leader lost over and over means DB connectivity flaps (step 2). Dashboards may be stale, but enforcement is unaffected.

### §provider — key missing, rejected or forbidden

1. `gw /sc/v1/status | jq '.providers'` shows `enabled`, `key_fingerprint` and `key_loaded_at` per provider.
2. `available=false` with an empty fingerprint → the key is missing or empty in `model-api-keys`. A key with a forbidden prefix (Anthropic OAuth `sk-ant-oat…`) is refused by design [SPEC §77]. Both are fixed with `procedures/rotate-model-credentials.md` §A.
3. `SCModelCredentialRejected` (the provider answers 401/403) → `procedures/rotate-model-credentials.md` §A, or §C if compromise is suspected.
4. Meanwhile routing skips the provider (plan 31 §14.2 filter 8) and uses the next candidate if one exists. `SCRoutingTierEmpty` tells you if none exists.

### §upstream — provider error rate high

1. `promq 'sum by (provider, status_class, code) (increase(sc_gateway_upstream_responses_total[15m]))'`. Check the provider's public status page.
2. `429` dominating: provider rate limits for your account. Reduce concurrency with a PR that lowers `perExecution.maxConcurrentRequests` (`policy/budgets.yaml`) and/or `ModelRouting.orchestrator.maxConcurrentRequests`, or raise the limits with the provider.
3. `5xx`/`529`: provider outage. The Gateway never retries (plan 53 §16). Harness SDKs retry, and executions failing this way are provider-attributable infrastructure failures, which triggers same-tier fallback where a second provider is eligible (plan 31 §14.4). If the outage is prolonged (> 1 h) and no fallback exists, consider disabling the provider by PR (`enabled: false` in `policy/model-routing.yaml`) to stop wasting attempts. Re-enable by PR after recovery.
4. Connection or TLS errors to one provider only: DNS or egress problem (plan 36 egress rule, node firewall plan 08) or the provider changed its endpoint. Compare with `upstream.baseUrl`.

### §usage — usage unknown or inconsistent

1. `promq 'sum by (provider, api_family, usage_status) (increase(sc_gateway_usage_status_total[1h]))'` and `promq 'increase(sc_gateway_usage_inconsistent_total[1h])'`.
2. Correlate with recent changes: a harness CLI bump (plan 28 `policy/harnesses.yaml` history) or a provider API change (provider changelog).
3. Effect while unresolved: affected requests are charged at the reserved worst case and marked incomplete (`cost_complete=false`). Budgets fill faster than real spend, which is safe.
4. Capture fresh fixtures (opt-in, real keys, a few cents): `make record-provider-fixtures PROVIDERS=<p>`. Open a task or PR that updates the dialect parser and the fixtures (plan 32 §8, plan 53 §22.2). That is ordinary test-first work.
5. If worst-case charging blocks work (`SCBudgetExhausted` driven by unknown rows), consider disabling the affected provider or family by PR until the parser is fixed. Do **not** raise budgets to compensate without recording why.

### §bound — provider usage above the provable bound (`SCGatewayBoundViolation`)

Under CD-28 a request's charge can exceed its reservation only if a provider breaks its documented tokenizer, context-window, output-cap or price behaviour. The Gateway then quarantines the (provider, model) (plan 53 §14.4a): requests for it fail `SC_MODEL_QUARANTINED` and routing skips it.

1. `psqlro "SELECT record_id, provider, model_requested, usage, computed_bound, reserved_micro_usd, budget_charge_micro_usd, started_at FROM ledger.cost_records WHERE bound_violation ORDER BY started_at DESC LIMIT 20;"` identifies the dimension (input, output or cost) and the size of the excess.
2. Diagnose: input above the bound → the family's `inputBound` (per-byte factor or overheads) is wrong for some content, or the provider billed beyond the window; output above → the output/reasoning cap is not enforced as documented (VS-8); cost above with usage inside the bound → the price card or service-tier mapping is wrong (`procedures/pricing-update.md`).
3. Fix by PR: raise `inputBound` values (and add the triggering content shape to `testdata/tokenizer-bound/`), set `outputCapCoversReasoning: false` with the enforced reasoning cap, or disable the model. Re-run the corpus smoke for that provider (53-AC-36).
4. Only after the fix is deployed, acknowledge: `curl -fsS -X POST "$RUNNER/v1/admin/model-quarantine/<provider>/<model>:acknowledge" "${AUTH[@]}"` (`RUNNER`/`AUTH` as in `procedures/budget-emergency-stop.md`; the `X-SC-Reason` names the fix PR). Expected: 200; `sc_gateway_model_quarantined{provider,model}` returns to 0 and requests succeed.
5. If the excess was material, check the affected scopes (`procedures/budget-emergency-stop.md` §E) and record it in the incident.

### §mismatch — provider answered with another model

1. `psqlro "SELECT model_requested, model_reported, count(*) FROM ledger.cost_records WHERE model_mismatch AND started_at > now()-interval '1 day' GROUP BY 1,2;"`
2. If `model_reported` is a dated snapshot or a new alias of the same model, add it to `responseModelMatch.exact` or `prefixes` for that model in `policy/model-routing.yaml` by PR. This is a benign mismatch.
3. If it is a **different** model, the routing intent is being violated (L-4).
   - Rows were priced at the reported model's price if it is known, otherwise charged at worst case.
   - Disable the requested model by PR until the provider behavior is understood. If the reported model is more expensive and the volume is significant, engage a stop (`procedures/budget-emergency-stop.md`).
4. Record the provider evidence (response ids, times) in the issue.

### §tokens — gateway tokens rejected

1. `promq 'sum by (code, token_kind) (increase(sc_gateway_denials_total{code=~"SC_TOKEN_.*"}[15m]))'`.
2. **`SC_TOKEN_INVALID` for all tokens:** the JWKS does not contain the Runner's current signing `kid`. Compare
   `kubectl -n $NS get cm sc-runner-jwks -o jsonpath='{.data.jwks\.json}' | jq -r '.keys[].kid'` with
   `kubectl -n $NS get secret agent-runner-signing -o jsonpath='{.data.kid}' | base64 -d` (the `kid` is not secret).
   Fix the JWKS publication (plan 35, `procedures/rotate-secrets.md` signing-key section). Rotation must publish before signing (`procedures/rotate-model-credentials.md` §D).
3. **`SC_TOKEN_REVOKED` for Hermes only:** its orchestrator token was revoked or expired without replacement. Follow `procedures/rotate-model-credentials.md` §B.
4. **`SC_TOKEN_REVOKED` for a running worker:** expected after cancellation or an emergency stop. Otherwise check the Runner's token registry writes (`procedures/agent-runner-failure.md`).

### §estop — Kubernetes e-stop source unreadable

The Gateway raises the effective level to `workers` after 60 s without a successful read (plan 33 §14.6). Worker spend is stopped by design.

1. `gw /sc/v1/status | jq .estop` shows `sources.k8s.last_read_ok_at` and `degraded`.
2. RBAC: `kubectl auth can-i get configmaps/sc-emergency-stop -n $NS --as=system:serviceaccount:$NS:model-gateway`. Expected: `yes`. A `no` means the chart RBAC regressed (plan 54). Fix it by PR.
3. Network: the Gateway's egress to the kube-apiserver (plan 36). A recent NetworkPolicy change is the usual cause.
4. API server health: `kubectl get --raw /readyz`. If the API server itself is degraded, follow plan 07 procedures. Worker dispatch cannot work anyway.
5. Once reads succeed the degraded level clears within 10 s. Verify that `gw /sc/v1/status | jq .estop.effective_level` is back to the level of the real sources.

### §latency — Gateway overhead high

1. `promq 'histogram_quantile(0.99, sum by (le, stage) (rate(sc_gateway_stage_seconds_bucket[15m])))'`.
2. `reserve` or `settle` dominating: DB contention.
   - `psqlro "SELECT wait_event_type, wait_event, count(*) FROM pg_stat_activity WHERE datname='strange_company' GROUP BY 1,2 ORDER BY 3 DESC;"`.
   - Lock waits on `budget_scopes` mean hot global rows (ADR-0450 consequence). Check for request storms (§upstream, 429 retry loops). Reduce `maxConcurrentRequests` by PR.
   - Check `sc-pg` CPU and IO (plan 12 dashboards).
3. `authn` or `inspect` dominating: CPU throttling (`container_cpu_cfs_throttled_periods_total` for the Gateway). Raise the CPU limit by PR (plan 54).
4. Verify that p99 `sc_gateway_overhead_seconds{stage="pre_upstream"}` is ≤ 50 ms over 15 min.

### §routing — no model resolvable

1. `promq 'sc_routing_tier_eligible_candidates == 0'` names the (harness, role, tier) pairs that have no candidate. `promq 'sum by (tier, model, reason) (increase(sc_routing_candidate_rejections_total[1h]))'` gives the reasons.
2. By reason:
   - `unpriced` → `procedures/pricing-update.md`;
   - `provider_unavailable` → §provider;
   - `provider_status_stale` (or `DEPENDENCY_UNAVAILABLE` on dispatch) → the Gateway leader is not writing `ledger.provider_status`: `psqlro "SELECT provider, available, reason, updated_at FROM ledger.provider_status;"`. If timestamps are old, see §ledger step 4 (leader);
   - `provider_budget_headroom` → `procedures/budget-emergency-stop.md` §E;
   - `disabled`, `family_incompatible`, `harness_provider_constraint`, `repository_denied` → a policy or override decision. Fix by PR if unintended.
3. Verify that `sc_routing_tier_eligible_candidates` > 0 for every pair and that a test `agent_start` (or the next Hermes dispatch) resolves.

### §allowlist — requests refused by the request-feature allowlist

The Gateway forwards only allowlisted body members, tool types and beta headers, and always refuses URL/file content sources and provider-side conversation state (plan 53 §7.3). A new harness version that sends a new feature fails until the allowlist is reviewed.

1. `psqlro "SELECT code, detail->>'member', count(*) FROM ledger.denials WHERE code = 'SC_REQUEST_FEATURE_DENIED' AND at > now()-interval '1 day' GROUP BY 1,2 ORDER BY 3 DESC;"` names the refused feature (member name only; no content is stored).
2. Determine what the feature does from the provider's API reference. Add it to `requestPolicy` in `policy/model-routing.yaml` by PR **only** if it is executed client-side and billed purely by tokens. Never allowlist provider-hosted tools, MCP connectors, URL or file sources, conversation-state members, or window-enlarging betas (plan 31 §9.3 invariant 7 rejects them).
3. Otherwise pin the harness back to the previous version (plan 28 registry PR) or configure the harness not to send the feature.
4. Verify: re-run the harness capture check (53-AC-29) for the affected harness; it reports zero refused features.

## Verification

- Every Gateway pod is Ready and `/readyz` returns `{"ready":true}`. `up{job="model-gateway"}` = 1 per replica.
- The triggering alert has resolved and stayed resolved for 15 min.
- For §ledger: `sc_gateway_reservations_open` returned to baseline. For §usage/§bound: the respective counters are flat for 1 h after the fix.
- One end-to-end request succeeds: the next worker execution or Hermes turn produces a `ledger.cost_records` row with `usage_status='reported'`, or, in a fake-only installation, `./bootstrap verify --only <gateway check id>` passes (plan 50).

## Rollback / abort

- Gateway version regressions: revert the chart values or image PR. With `maxUnavailable: 0` the rollback is non-disruptive. Schema migrations are expand/contract, so the previous Gateway version tolerates the newer schema (plan 53 §19).
- Policy changes: revert the PR. The last-known-good policy remains until a valid revision loads.
- Provider disablement: re-enable by PR after the provider recovers.

## Evidence to record

Alert name and time, section followed, `/readyz` and `/sc/v1/status` output (no secrets are in it), the relevant `promq`/`psqlro` outputs, root cause, PR links, and the time of recovery. For §mismatch and §usage, keep provider response ids, never content.

## Follow-ups

- A recurring §upstream issue on one provider → reconsider its tier position or enable a second provider for fallback (plan 31).
- A recurring §usage issue → add the fixture scenario to the contract suite permanently (plan 53 §22.2).
- Any manual intervention → check that the corresponding acceptance test (plan 53 §23) still covers the failure mode, and add one if not.
