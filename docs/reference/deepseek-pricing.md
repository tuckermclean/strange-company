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

Per 1M tokens, USD, **peak**:

| Model | Cache hit (input) | Cache miss (input) | Output |
|---|---|---|---|
| `deepseek-v4-flash` | 0.014 | 0.44 | 1.32 |
| `deepseek-v4-pro`   | 0.044 | 1.32 | 3.96 |

```yaml
aliases:
  implement-cheap:
    provider: deepseek-coding
    model: deepseek-v4-flash
    pricing:
      inputPerMTok:       0.44   # cache miss
      cachedInputPerMTok: 0.014  # cache hit
      outputPerMTok:      1.32
      cacheWritePerMTok:  0      # DeepSeek does not charge separately
```

### Use the peak rates

DeepSeek halves every rate outside 01:00-04:00 and 06:00-10:00 UTC, Monday to
Friday. `policy.Pricing` is a flat rate with no notion of time of day, so any
figure configured here is wrong by a factor of two in one direction or the
other. Peak over-estimates, which makes a budget stop early rather than late --
the correct direction for a guard. The same reasoning covers the cache
discount: if the harness does not surface DeepSeek's `prompt_cache_hit_tokens`
as cached input, everything bills at the miss rate and errs the same safe way.

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
