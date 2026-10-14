# ADR-0450: Money is integer micro-USD; budgets are enforced by atomic worst-case reservations

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plans:** 32, 33 (runtime: 53)

## Context
SPEC §79 requires exact per-request cost, and §81 requires *prevention*: per-request, execution, task, token, provider-period and global limits that hold even when many requests are in flight. Two problems have to be solved together:

- **Representation.** Provider prices go down to fractions of a cent per million tokens (e.g. `0.028` USD/M). Binary floating point cannot represent these exactly, sums drift, and a Go total and a SQL total would disagree. Lesson L-3 also requires "unpriced" to be distinct from 0.
- **Timing.** Actual cost is known only after the response ends, but the decision to spend has to be made before the request is forwarded. Checking settled spend alone (`spent < limit`) lets N concurrent requests all pass the check and overshoot the limit by N times the request cost. Harness self-reports cannot fill the gap (L-3).

## Decision
1. **Money is integer micro-USD** (1 USD = 1 000 000) in storage (`bigint`), contracts (`*_micro_usd` fields) and metrics. Configuration files write money as decimal strings with at most 6 fractional digits (`"12.50"`), parsed exactly. Floats never appear in a money field. Rates are integer micro-USD per million tokens. Each cost component is rounded with `ceil`, which is the conservative direction.
2. **Pre-authorization by reservation.** For every billable request the Gateway computes a worst-case cost (estimated input from body bytes × the highest applicable input rate, plus the enforced output cap × the highest output rate; plan 53 §14.1 step 6). It then reserves that amount in **one PostgreSQL transaction** against every applicable budget scope: it locks the scope rows in ascending `scope_key` order, checks `spent + reserved + amount <= limit` for cost, tokens and requests, inserts the reservation and increments `reserved_*`. Only after commit does it forward.
3. **Settlement** replaces the reservation by the actual charge in a second transaction (`reserved -= amount`, `spent += charge`), writing the single cost record. Unknown usage is charged at the reserved worst case and never as zero. An expired reservation is settled at worst case by the sweeper.
4. **No reservation, no forward.** A database error means the request is denied (`SC_LEDGER_UNAVAILABLE`).

## Consequences
- Concurrent requests cannot over-admit against a limit: the row locks serialize the admission decision. The only possible overshoot is the *overrun* of a request whose actual input exceeded the byte-based estimate. Channels that bill input the body does not contain (provider-side state, file ids, URL sources, window-enlarging betas) are denied by the Gateway's request allowlist, and the estimate is capped at the model's context window. The residual per-request overrun is therefore below `contextWindowTokens × max input rate`; it is **not** bounded by `perRequest.maxCostUsd`. It is measured (`sc_gateway_reservation_overrun_micro_usd_total`), alerted, and its scope-level bound is stated in plan 33 §14.3 (review COST-4).
- A shortfall caused only by open reservations is *contention*, not exhaustion: the Gateway waits for settlements (bounded) and then answers a retryable `SC_BUDGET_CONTENDED`, so transient reservation pressure never parks a task in a human-only state (review COST-3).
- The global scope rows are a serialization point. At the planned load (≤ 2 workers × ≤ 8 concurrent requests, plus Hermes) a 2–5 ms transaction gives ample headroom. If load grows, scopes can be sharded (post-MVP).
- Go and SQL compute identical costs from shared test vectors (plan 32 §22).
- Budgets are slightly pessimistic while requests are in flight, because headroom excludes open reservations. That is intended.

## Alternatives rejected
- **Decimal or float USD:** floats are inexact, and `numeric` works in SQL but invites float conversions in Go and in JSON.
- **Post-hoc enforcement** (deny the next request once settled spend exceeds the limit): unbounded overshoot under concurrency and long streams.
- **In-memory counters per Gateway replica:** not shared between replicas, and lost on restart.
- **Provider-side spend limits:** not uniform across providers, not request-granular, and not attributable to task or execution.

## Verification
GATE-C05 (plan 53, 53-AC-3; plan 32, 32-AC-3/5) and GATE-C06 (plan 33, 33-AC-3): 64 concurrent requests against a scope with headroom for exactly 10 worst-case reservations forward exactly 10; every reservation ends in exactly one cost record; Go and SQL pricing vectors agree.
