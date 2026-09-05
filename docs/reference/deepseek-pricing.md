# Turning the cost ledger on for a DeepSeek ladder

`max_cost_usd` is enforced from 0.22.0, but only for a card whose every run
has a price. This is how to give a DeepSeek-based pipeline one.

## There is no cost endpoint. Verified, not assumed.

DeepSeek publishes exactly one billing endpoint:

```
GET https://api.deepseek.com/user/balance
-> {"is_available": true,
    "balance_infos": [{"currency": "USD", "total_balance": "...",
                       "granted_balance": "...", "topped_up_balance": "..."}]}
```

That is an account balance. There is no per-request charge, no historical
usage, and no per-model breakdown; the usage view in the web dashboard is not
exposed over the API. Every tool that displays DeepSeek spend computes it
locally from published rates and the token counts already in the completion
response, which is exactly what `runner.Price` and `policy.Pricing` do.

So the ledger is turned on with a rate card, not an integration.

## The rates

Per 1M tokens, USD, read from the raw pricing page rather than a summary of it:

| | `deepseek-v4-flash` | `deepseek-v4-pro` |
|---|---|---|
| Input, cache hit — off-peak / peak | 0.007 / 0.014 | 0.022 / 0.044 |
| Input, cache miss — off-peak / peak | 0.22 / 0.44 | 0.66 / 1.32 |
| Output — off-peak / peak | 0.66 / 1.32 | 1.98 / 3.96 |

> (1) Off-peak rates are half of the peak rates. Peak hours are 01:00 - 04:00
> and 06:00 - 10:00 UTC, Monday through Friday (all other hours are off-peak).

### Configure both tiers, never one flat rate

Peak is 35 hours of 168. A flat card carrying the published (peak) rates
over-charges roughly four runs in five by a factor of two, so a budget set
against it fires at about half the spend that was authorised. That is not a
conservative approximation; it is the wrong number, and this repo already
holds that a budget enforced against a wrong number is worse than one
enforced against an admittedly missing one.

```yaml
aliases:
  implement-cheap:
    provider: deepseek-coding
    model: deepseek-v4-flash
    pricing:
      inputPerMTok:       0.44   # cache miss, peak
      cachedInputPerMTok: 0.014  # cache hit, peak
      outputPerMTok:      1.32
      cacheWritePerMTok:  0      # DeepSeek does not charge separately
      offPeak:
        inputPerMTok:       0.22
        cachedInputPerMTok: 0.007
        outputPerMTok:      0.66
        cacheWritePerMTok:  0
      peakHoursUTC:
        - {days: [Mon, Tue, Wed, Thu, Fri], from: "01:00", to: "04:00"}
        - {days: [Mon, Tue, Wed, Thu, Fri], from: "06:00", to: "10:00"}
```

A malformed schedule is refused at load: a misspelled day or an unreadable
time would otherwise bill every run off-peak and halve the ledger silently,
which is the failure this whole mechanism exists to avoid.

### Why the cache split is safe to price

`Pricing.CostUSD` charges `cachedInput` IN ADDITION TO `input`, which is only
correct if the two are disjoint -- and DeepSeek's `prompt_tokens` includes its
cache hits, so passing both through unchanged would double-charge every cached
token. opencode does not pass them through unchanged:

```ts
input: safe(usage?.nonCachedInputTokens),
cache: { read, write }
```

`input` is non-cached input. The fields are disjoint and the mapping is exact:
`input` at the cache-miss rate, `cache.read` at the cache-hit rate.

### The one approximation, stated

A run that touches peak at any point is priced entirely at peak. Runs here last
minutes and the windows last hours, so spanning a boundary is rare; when one
does, the error is bounded by the off-peak remainder of a single short run and
falls in the over-charging direction. Pricing it exactly would mean bucketing
tokens per event by the tier in force when each arrived, which is not worth it
for that.

## Price every alias the phases use, not just the coding ones

One unpriced run is enough to switch the budget off for the whole card, by
design -- a total missing some of its runs is a floor, and a limit enforced
against a floor stops cards for spending an amount nobody measured.

That includes the foreman. The Hermes gateway reports no cost at all, so a
foreman alias without a rate card leaves every card unpriced however carefully
the coding ladder is priced. `GET /cards/{id}/cost` reports
`unpriced_attempts`, and the card's own page says when its budget is not in
force. Both are the fastest way to find the alias that was missed.

## Zero does not mean free

`runner.Price` treats a computed cost of zero as "no rate card", because that
is overwhelmingly what it is: a nil `*policy.Pricing` satisfies the `Pricer`
interface as a non-nil interface value whose method dutifully returns 0.

So an alias that is genuinely free -- a model reached through a flat-fee
subscription rather than metered per token -- cannot be expressed as
`pricing: {inputPerMTok: 0, ...}`. It will read as unpriced and hold the
budget off.

This has not bitten anything yet: subscription-backed aliases here serve the
§10.2 specification conversation, which is a Hermes session rather than a
coding run and never reaches `runner.Price`. It would matter the moment such
an alias is put on a phase that records attempts, and is written down here
rather than rediscovered then.
