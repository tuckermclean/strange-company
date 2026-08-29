# opencode reports no cost, and why the ledger says so out loud

Every coding run on the live board records `cost_usd = 0` and zero tokens. The
work was not free. The ledger is blind.

## What is actually happening

`opencode run --format json` reports usage and cost in its `step_finish` event.
opencode drops that event — along with its `text` events — when it runs in a
container (anomalyco/opencode issues 26855 and 31435). The runner already
tolerates the missing `step_finish` rather than failing the run, and the same
dropped events are why summaries read `opencode exited 0 with no narrative
output`.

So `runner.CodingRunResult.Usage` stays zero and `CostUSD` stays nil, and
`RecordAttempt` faithfully writes what it was given.

## What was fixed

`GET /cards/{id}/cost` now reports `unpriced_attempts` and `cost_complete`. A
total that reads zero because nothing could be priced is reported as a floor,
not as a figure. This matters beyond tidiness: `max_cost_usd` is enforced
against a number that, today, cannot move — a budget checked against a blind
ledger is not being enforced at all, and until now nothing said so.

## What is not fixed, and what it needs

Reading the usage from opencode's own storage rather than from its event
stream. The storage lives under `$XDG_DATA_HOME/opencode`, but its layout —
which file holds a session's token counts, and in what shape — cannot be
established from outside a real containerised run. Probing it locally is how an
earlier fix in this area went wrong: opencode was observed loading a
working-directory config, that observation was generalised, and the Job's own
log later showed it reading somewhere else entirely.

So the runner now lists that directory after every opencode run, bounded to 40
files under 1 MB each, into the run log the control plane already preserves.
The next live coding run is the evidence, and the fix can be written against
what it shows rather than against a guess.

Until then: treat `cost_complete: false` as "this card's spend is unknown",
never as "this card was cheap".
