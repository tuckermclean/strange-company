package runner

// Pricer turns a run's token usage into a dollar figure.
//
// Declared here rather than taken from internal/policy so this package stays
// free of the configuration layer: an adapter produces a CodingRunResult and
// should not need to know where a rate card comes from. *policy.Pricing
// satisfies it.
type Pricer interface {
	CostUSD(input, output, cachedInput, cacheWrite int) float64
}

// Price fills in a run's cost when the harness did not report a usable one.
//
// Both cases below are real and both leave §22's ledger at zero:
//
//   - opencode reports cost 0 for any provider models.dev has no pricing for,
//     which is every custom OpenAI-compatible provider including the DeepSeek
//     one this runs on. A reported zero is a real number and wrong.
//   - the Hermes gateway reports no cost at all, so the review phase counts
//     thousands of tokens against nothing.
//
// A harness that reports a real, non-zero price is believed and left alone: it
// knows the provider's actual rate, and second-guessing it with a configured
// table would make the ledger disagree with the invoice.
//
// A harness zero is treated as "unknown", not as "free", and is cleared when
// it cannot be priced. opencode emits that zero for every provider it has no
// rates for, so believing it would have /cards/{id}/cost report a fully-priced
// card costing nothing -- which is the exact lie that endpoint was built to
// stop telling. Better to say the number is missing.
//
// Nothing here invents a figure: with no tokens, or no rate card, CostUSD ends
// up nil and the card keeps reporting as unpriced.
func Price(result *CodingRunResult, p Pricer) {
	if result == nil {
		return
	}

	// A real, non-zero price from the harness is the provider's actual rate.
	if result.CostUSD != nil && *result.CostUSD > 0 {
		return
	}

	// Anything else is unknown until proven otherwise, including a reported
	// zero.
	result.CostUSD = nil

	if p == nil {
		return
	}
	cost := p.CostUSD(
		result.Usage.InputTokens,
		result.Usage.OutputTokens,
		result.Usage.CachedInputTokens,
		result.Usage.CacheCreationTokens,
	)

	// Zero arrives two ways and both mean "no rate card": none was
	// configured, or -- the trap -- a nil *policy.Pricing was passed as this
	// interface, which is NOT nil as an interface value and whose method
	// dutifully returns 0.
	if cost == 0 {
		return
	}
	result.CostUSD = &cost
}
