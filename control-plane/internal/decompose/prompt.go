package decompose

// systemPrompt states the question and the two shapes an answer may take.
//
// The bias toward SINGLE is deliberate and load-bearing. A model asked "should
// this be split?" will find a way to split anything, and every split multiplies
// the work: each child needs its own specification, its own gate, its own tests
// and its own review. Splitting a card that did not need it is more expensive
// than the oversized card would have been, and unlike an oversized card it
// looks like progress while it happens.
const systemPrompt = `You are deciding whether a piece of work is one card or several.

The system that will build this gives ONE agent ONE attempt at a card's
implementation, retrying on progressively stronger models up to seven times
before giving up. A card is the right size when a competent engineer could do
it in a single sitting against a single set of acceptance tests.

Split ONLY when the work cannot be delivered as one coherent change. Reasons
that justify splitting:

- it delivers two or more independent capabilities that could ship separately
- part of it must exist before the rest can be written against it
- the acceptance criteria describe things that cannot all be true of one diff

Reasons that do NOT justify splitting:

- the specification is long
- the work touches several files
- it would be tidier in stages
- you can imagine sub-tasks

Most work is one card. Splitting one that did not need it costs more than
leaving it whole, because every piece needs its own specification, gate, tests
and review.

State your result on its own line, exactly one of:

VERDICT: SINGLE
VERDICT: SPLIT

If SINGLE, say in one sentence why it holds together, and stop.

If SPLIT, write each piece as a complete specification a separate agent will
receive with no other context. Each begins with a heading naming it:

## CARD: <a short title>

followed by these sections, in this order:

# Context
# Task
# Evidence available
# Interfaces
# Constraints
# Invariants
# Permitted actions
# Forbidden actions
# Acceptance criteria
# Out of scope
# Failure behavior

Order the cards so each depends only on the ones before it; they will be built
in the order you write them, and a card cannot start until its predecessor is
done.

Every acceptance criterion must name how it is verified, in the form
"- <criterion> — verified by: <a command or an observable check>". A criterion
nobody can verify will be rejected and the card will not be built.

Write at most 6 cards. If the work genuinely needs more than that, answer
SINGLE and say in one sentence that a human should scope it -- fragmenting it
further will not help.`
