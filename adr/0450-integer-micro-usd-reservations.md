# ADR-0450: Money is integer micro-USD; budgets are enforced by atomic worst-case reservations

**Status:** Accepted (Stage 0) · **Date:** 2026-10-13 · **Owning plans:** 32, 33 (runtime: 53)

## Context
SPEC §79 requires exact per-request cost, and §81 requires *prevention*: per-request, execution, task, token, provider-period and global limits that hold even when many requests are in flight. Two problems have to be solved together:

- **Representation.** Provider prices go down to fractions of a cent per million tokens (e.g. `0.028` USD/M). Binary floating point cannot represent these exactly, sums drift, and a Go total and a SQL total would disagree. Lesson L-3 also requires "unpriced" to be distinct from 0.
- **Timing.** Actual cost is known only after the response ends, but the decision to spend has to be made before the request is forwarded. Checking settled spend alone (`spent < limit`) lets N concurrent requests all pass the check and overshoot the limit by N times the request cost. Harness self-reports cannot fill the gap (L-3).

## Decision
1. **Money is integer micro-USD** (1 USD = 1 000 000) in storage (`bigint`), contracts (`*_micro_usd` fields) and metrics. Configuration files write money as decimal strings with at most 6 fractional digits (`"12.50"`), parsed exactly. Floats never appear in a money field. Rates are integer micro-USD per million tokens. Each cost component is rounded with `ceil`, which is the conservative direction.
2. **Pre-authorization by reservation.** For every billable request the Gateway computes a **provable** worst-case cost (CD-28; plan 53 §14.1 step 6): the input-token upper bound of the provider family (≤ 1 token per UTF-8 byte for byte-level BPE families, plus declared overheads and documented per-image maxima, capped at the enforced context window) × the highest applicable input-side rate, plus the provider-enforced output cap (covering reasoning, or with a separately enforced reasoning cap) × the highest output rate. A request whose worst case exceeds `perRequest.maxCostUsd` is denied, so that limit is a hard ceiling (SPEC §81). It then reserves that amount in **one PostgreSQL transaction** against every applicable budget scope: it locks the scope rows in ascending `scope_key` order, checks `spent + reserved + amount <= limit` for cost, tokens and requests, inserts the reservation and increments `reserved_*`. Only after commit does it forward.
3. **Settlement** replaces the reservation by the actual charge in a second transaction (`reserved -= amount`, `spent += charge`), writing the single cost record. Unknown usage is charged at the reserved worst case and never as zero. An expired reservation is settled at worst case by the sweeper.
4. **No reservation, no forward.** A database error means the request is denied (`SC_LEDGER_UNAVAILABLE`).

## Consequences
- Concurrent requests cannot over-admit against a limit: the row locks serialize the admission decision, and every request's charge is ≤ its reservation by construction, so neither a request nor a shared scope can overshoot. The one exception is a provider breaking its own documented window, cap or tokenizer behaviour; that is recorded as a `BOUND_VIOLATION`, pages (`SCGatewayBoundViolation`) and quarantines the model until a human acknowledges it (plan 53 §14.4a). There is no "bounded overrun" in normal operation (CD-28, external review X#2).
- A shortfall caused only by open reservations is *contention*, not exhaustion: the Gateway waits for settlements (bounded) and then answers a retryable `SC_BUDGET_CONTENDED`, so transient reservation pressure never parks a task in a human-only state (review COST-3).
- The global scope rows are a serialization point. At the planned load (≤ 2 workers × ≤ 4 concurrent requests, plus Hermes with ≤ 4) a 2–5 ms transaction gives ample headroom. If load grows, scopes can be sharded (post-MVP).
- Go and SQL compute identical costs from shared test vectors (plan 32 §22).
- Budgets are pessimistic while requests are in flight, because headroom excludes open reservations sized at the provable worst case (often several times the actual cost). That is the price of a hard ceiling; plan 33 §9.1 sizes the defaults for it, and contention waits rather than fails.

## Alternatives rejected
- **Decimal or float USD:** floats are inexact, and `numeric` works in SQL but invites float conversions in Go and in JSON.
- **Post-hoc enforcement** (deny the next request once settled spend exceeds the limit): unbounded overshoot under concurrency and long streams.
- **In-memory counters per Gateway replica:** not shared between replicas, and lost on restart.
- **Provider-side spend limits as the enforcement mechanism:** not uniform across providers, not request-granular, and not attributable to task or execution. They are kept as a **required out-of-band backstop** (review BUD-B6, plan 33 §9.2a): every in-cluster control shares the same price cards and parsers, so a shared error (unannounced price rise, account surcharge, new billed dimension) could only be caught by the provider's own limit and by the daily automated reconciliation against provider-reported spend (plan 32 §20). That residual risk is accepted up to the provider limit (≤ 1.25 × the provider's `monthlyUsd`).

## Verification
GATE-C05 (plan 53, 53-AC-3; plan 32, 32-AC-3/5) and GATE-C06 (plan 33, 33-AC-3): 64 concurrent requests against a scope with headroom for exactly 10 worst-case reservations forward exactly 10; every reservation ends in exactly one cost record; Go and SQL pricing vectors agree.
