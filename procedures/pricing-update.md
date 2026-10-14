# Procedure: Update model pricing, add a priced model, reconcile the ledger with provider invoices

**Owning plan:** plans/32-cost-accounting.md (related: plans/31-model-routing.md, plans/33-budget-enforcement.md, plans/53-model-gateway.md)
**SPEC references:** §79 (historical pricing retained), §80, §81, §4.7
**Severity / urgency:** routine. `SCPricingMissingForRoutedModel` and `SCModelUnpricedDenied` are degraded: requests to that model are denied until fixed.
**Expected duration:** 30–60 min per provider (mostly PR review); reconciliation 20 min per month
**Requires break-glass:** no for sections A–D (Git PR). Section E step 5 (cost adjustment) needs `SC-RUNNER-ADMIN` + cluster kubeconfig.

## When to use

- A provider announces or applies a price change, including a time-of-day (off-peak) schedule change.
- A new model is added to `policy/model-routing.yaml` (it must ship with a price card; plan 32 §9.4).
- Alerts:
  - `SCPricingEntryExpiringSoon`: an entry with `effectiveTo` set has no successor within 7 days;
  - `SCPricingMissingForRoutedModel` / `SCModelUnpricedDenied`: a routed model has no effective priced entry, so requests are denied `SC_PRICE_UNKNOWN`;
  - `SCGatewayPolicyInvalid` caused by `policy/pricing/*.yaml`.
- `make check-pricing` warns that `source.retrievedAt` is older than 90 days, or fails at 120 days.
- Monthly reconciliation (section E), on the 3rd UTC day of each month, after providers finalize the previous month.

## Preconditions and required material

- Toolbox: `./bootstrap shell`. Commands run inside it from the repository root.
- The provider's **official** pricing page URL (and the invoice or console usage export for E). Prices are never guessed (plan 32 §9.4). A rate that cannot be verified ships as `status: unpriced`.
- Rights to open and merge a PR on `strange-company` (`policy/**` is a protected path: human PRs only).

## Safety checks before acting

1. **Never edit or delete an entry that rows already reference.** Cost records are immutable and reference the entry's `pricing_digest` and `version`. History is preserved by closing the old entry (`effectiveTo`) and adding a new one. Never rewrite the old one's rates. The snapshot table keeps old entries even after they are removed from Git (32-AC-7), but removing entries before they expire breaks pricing for in-flight requests.
2. **No gaps, no overlaps.** Effective intervals are half-open `[effectiveFrom, effectiveTo)`. The old `effectiveTo` must equal the new `effectiveFrom` exactly.
   - An overlap makes the loader reject the whole file (last-known-good stays).
   - A gap makes requests in that interval fail with `SC_PRICE_UNKNOWN`.
3. **Money format:** decimal strings, ≤ 6 fractional digits, USD per **million** tokens. A price published per thousand tokens must be multiplied by 1000. Floats are rejected by the schema.
4. **Disjoint categories (plan 32 §8):**
   - `inputPerMillion` is uncached input;
   - `cachedInputPerMillion` is cache reads;
   - `cacheWritePerMillion` is cache writes at the default TTL;
   - `cacheWrite1hPerMillion` is the 1-hour TTL, where the provider distinguishes it (if a provider bills 1 h writes and the card lacks this rate, those requests are **unpriced**);
   - `reasoningPerMillion` only if the provider prices reasoning separately.
5. `timeWindows` and `longContextTiers` cannot be combined in one entry. Windows are **UTC**, `from` is inclusive, `to` is exclusive (`24:00` allowed).
6. Lowering a price never needs urgency. Raising one does: until merged, the ledger under-states cost and budgets are too generous. For a large increase, consider tightening `policy/budgets.yaml` in the same PR.

## Steps

### A. Price change for an existing model

1. Read the provider's pricing page and record the URL and today's date. Determine:
   - the effective instant of the change, in UTC;
   - for time-of-day pricing, whether the provider bills by request **start** or **completion** time. Set `windowBasis` accordingly. If the documentation is silent, keep `request_start` (default) and add a `source.note` saying so (plan 32 OQ 1).
2. Edit `policy/pricing/<provider>.yaml` on a branch:
   - set the current entry's `effectiveTo` to the change instant (e.g. `"2026-11-01T00:00:00Z"`);
   - append a new entry with the same `model` and `serviceTier`, a new unique `version` (convention `YYYY-MM-DD` of the effective date), `effectiveFrom` = that instant, `effectiveTo: null`, `status: priced`, the new `rates` (and windows or tiers), and `source.url` + `source.retrievedAt`.
3. Run locally in the toolbox:

   ```sh
   make policy-check            # schema + loader invariants (overlap, gaps vs. routing, windows)
   make check-pricing           # every routed enabled production model priced now; retrievedAt freshness
   make test-pricing-vectors    # unchanged vectors still pass (they use fixtures, not policy)
   ```

   - Expected: all exit 0.
   - `policy-check` names the file and entry on failure (e.g. `overlapping effective intervals deepseek-v4-flash/standard`). Fix it and re-run.
   - A price **increase** can fail plan 33 invariant 8 (`worst(model) > perRequest.maxCostUsd` or `> dispatch.minimumHeadroomUsd[tier]` at the model's full context window). Raise those limits in `policy/budgets.yaml` in the same PR, or route a smaller-window model id; otherwise every full-context request to that model would be denied `SC_REQUEST_COST_CAP`.
   - Providers that report non-standard service tiers (plan 32 §8 mapping) need an entry per priced `serviceTier` actually used; requests are pinned to `standard` by default (plan 53 §14.3).
4. Commit (`chore(pricing): <provider> prices effective <date>`), push, open a PR whose description links the source page. CI repeats step 3. A human reviewer compares every rate with the source page, one by one. Merge.
5. Confirm deployment (≤ 15 min after merge; speed up with `flux reconcile kustomization <policy kustomization> --with-source`):
   - `kubectl -n monitoring port-forward svc/prometheus-operated 19091:9090 &` then query `sc_gateway_policy_info`. The `pricing_rev` label is the new 12-hex prefix on every Gateway replica.
   - `sc_gateway_policy_load_errors_total` has not increased.
   - If the revision does not change, `SCGatewayPolicyInvalid` explains why. The old pricing stays in force, which is safe.
6. After the effective instant, verify that new rows use the new version:

   ```sh
   kubectl -n strange-company exec -i sc-pg-1 -c postgres -- psql -d strange_company -At -c \
    "SELECT pricing_version, count(*) FROM ledger.cost_records WHERE provider='<p>' AND model_requested='<model>'
       AND started_at > now() - interval '1 day' GROUP BY 1;"
   ```

   Expected: rows started after the instant carry the new version. Use the CNPG primary pod name (plan 12); this is a read-only query.

### B. Price card for a new model (together with its routing PR)

1. Add the entry exactly as in A2, with no predecessor (`effectiveFrom` = now or earlier, `effectiveTo: null`), in the **same PR** that adds the model to `policy/model-routing.yaml` (plan 31 §19) and to a budget-covered provider (`policy/budgets.yaml`).
2. The PR must also contain recorded usage fixtures for the model's provider and family if the family or dialect is new (plan 53 §22.2), and the provider smoke evidence must be ≤ 31 days old (`make check-provider-evidence`, 53-AC-17).
3. Steps A3–A5 apply. After merge, a dispatch that routes to the model creates rows with `pricing_status = priced`.

### C. A price cannot be verified

Ship or keep the entry as:

```yaml
- model: <apiModel>
  version: "unpriced-<date>"
  effectiveFrom: "<instant>"
  effectiveTo: null
  status: unpriced
  unpricedReason: "<why: e.g. promotional pricing without a published end date>"
  source: {url: "<page>", retrievedAt: "<date>"}
```

Routing skips the model (`unpriced`, plan 31 §14.2 filter 6), and the Gateway denies it (`SC_PRICE_UNKNOWN`). This is the intended fail-closed behavior. **Never** write `"0"` rates to make a model usable. Zero means free, not unknown (lesson L-3).

### D. Alert-driven fixes

- **`SCPricingEntryExpiringSoon`:** add the successor entry (section A) before `effectiveTo`. If the provider has not announced the next price, either extend `effectiveTo` (when the provider says the price continues) or let the model become unpriced (routing then uses the next candidate).
- **`SCPricingMissingForRoutedModel` / `SCModelUnpricedDenied`:**
  1. Find the model: `sc_routing_unpriced_candidates{tier}` > 0 and the denial rows (`SELECT model, count(*) FROM ledger.denials WHERE code='SC_PRICE_UNKNOWN' AND at > now()-interval '1 day' GROUP BY 1;`).
  2. Either price it (A or B) or disable it in routing (plan 31) by PR.
- **`SCGatewayPolicyInvalid` from pricing:** run `make policy-check` on `main`. It prints the failing invariant. Fix by PR. The last-known-good prices remain in force until then.

### E. Monthly reconciliation with provider invoices

1. For each enabled production provider, export the previous month's usage total (USD) from the provider console or invoice.
2. Ledger total for the same UTC month:

   ```sh
   kubectl -n strange-company exec -i sc-pg-1 -c postgres -- psql -d strange_company -At -c \
    "SELECT provider, sum(cost_total_micro_usd)/1e6 AS priced_usd, sum(budget_charge_micro_usd)/1e6 AS charged_usd,
            count(*) FILTER (WHERE usage_status IN ('unknown','estimated') OR pricing_status='unpriced') AS incomplete
       FROM ledger.cost_records
      WHERE started_at >= date_trunc('month', now() AT TIME ZONE 'UTC') - interval '1 month'
        AND started_at <  date_trunc('month', now() AT TIME ZONE 'UTC')
      GROUP BY 1;"
   ```

3. Compare. A difference of **> 3 % or > $5** (whichever is larger) opens a Forgejo issue labelled `ledger-discrepancy` with both numbers, the incomplete-row count and the price versions used. Typical causes:
   - a stale price card (fix with A; old rows stay as they were, and `ledger.reprice()` can recompute reports);
   - usage-parsing changes (`procedures/gateway-failure.md` §usage);
   - traffic outside the Gateway (treat as compromise: `procedures/rotate-model-credentials.md` §C);
   - a lost ledger window after a PITR (step 5).
4. Run the provider smoke for every enabled provider (`make smoke-providers`), so that 53-AC-17 evidence stays ≤ 31 days old.
5. **Budget-relevant corrections only** (e.g. spend lost to an `sc-pg` PITR): record a cost adjustment through the Runner admin API (plan 32 §20; endpoint requested from plan 26):

   ```sh
   curl -fsS -X POST "$RUNNER/v1/admin/ledger/adjustments" "${AUTH[@]}" -H 'Content-Type: application/json' \
     -H "Idempotency-Key: adj-<provider>-<YYYY-MM>-<n>" \
     -d '{"period":"<YYYY-MM or YYYY-MM-DD>","provider":"<p>","delta_usd":"<decimal, may be negative>","reason":"<why>"}'
   ```

   `RUNNER` and `AUTH` are set up as in `procedures/budget-emergency-stop.md`. Expected: 200. The adjustment row appears in reports as a separate line, `applied_at` is set, and (same transaction, `ledger.apply_cost_adjustment()`, plan 33 §20) `provider-month`/`global-month` (and the day rows for a day period) `spent_micro_usd` change by the delta (plan 33 §20). The call is idempotent through its key.

## Verification

- `make policy-check` and `make check-pricing` pass on `main` after merge.
- `sc_gateway_policy_info{pricing_rev}` shows the new revision on all replicas, and `sc_routing_unpriced_candidates` is 0 for tiers used by enabled production harnesses.
- New ledger rows carry the new `pricing_version` after the effective instant. For a sample row, `SELECT ledger.reprice(record_id, pricing_digest) = cost_total_micro_usd` returns true.
- Reconciliation: the issue is opened or not according to the 3 % / $5 rule, and the smoke evidence files are fresh.

## Rollback / abort

- Revert the PR. Because intervals are half-open and rows reference immutable snapshots, a revert only affects requests started after the revert is deployed. Rows already written keep the version they used. If the revert reintroduces a gap or overlap the loader rejects it. In that case fix forward instead.
- Do not delete `ledger.pricing_snapshots` rows, ever.

## Evidence to record

PR link with the source URLs and retrieval dates, reviewer name, `pricing_rev` after deployment. For E: the reconciliation table (provider, invoice USD, ledger priced USD, charged USD, incomplete rows, difference %), any `ledger-discrepancy` issue, adjustment ids, smoke evidence file names.

## Follow-ups

- Repeated discrepancies for one provider → review its usage dialect fixtures (plan 53 §22.2) and `windowBasis`.
- A large price increase → review `policy/budgets.yaml` and `dispatch.minimumHeadroomUsd`, and possibly the tier ordering in `policy/model-routing.yaml` (plan 31).
