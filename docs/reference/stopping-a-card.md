# Stopping a card

An operator watching a card misbehave needs a lever that is not "ship a new
image". There is one, it is not a new endpoint, and this page exists because it
was not discoverable — a real operator went looking for `cancel`, `park` and
`abort`, found none, and concluded a redeploy was the only option
([#77](https://github.com/tuckermclean/strange-company/issues/77)).

## Stop it

Move the card to **Blocked**. Either way works:

```
POST /cards/{id}/transition   {"to": "Blocked", "reason": "why"}
```

or drag it to the **Blocked** column on the Vikunja board — §4.3 makes a human
move on the board a real input to the state machine, so the two are the same
action.

A blocked card is not claimable and no supervisor promotes it, so it stops. If
a worker is holding it, the claim is taken away immediately; the model call
already in flight finishes, which is one call rather than an unbounded loop.
The worker's own step then cannot undo the block — `Blocked → Review` is not a
legal transition for an agent, so an operator's stop cannot be overwritten
half a second later by the step it interrupted.

Verified by `internal/store/kill_switch_test.go`, not by assertion.

## Start it again

```
POST /cards/{id}/transition   {"to": "Ready", "reason": "cause fixed"}
```

`Blocked → Ready` is **human-only**. An agent cannot release its own block,
which is what keeps Blocked meaningful: an agent that could park and unpark
cards would be deciding a human is needed without ever saying so.

## Why there is no `/cards/{id}/cancel`

§30 lists the API surface and does not include one. A second endpoint that
performed a transition the transition endpoint already performs would be
surface for its own sake, and a second way to reach a state is a second way for
the two to disagree. What was missing was not the capability. It was this page.

## What stops a card on its own

Two bounds exist so an operator does not have to be watching:

- **The escalation ladder** (§12.3). Attempts that fail on the merits burn
  rungs; the last rung ends at `NeedsHuman`.
- **Consecutive infrastructure failures.** Five runs in a row that could not
  be *run* — provider timeouts, evicted pods — send the card to `NeedsHuman`
  with a reason saying the step could not run and that this says nothing about
  whether the work is good. Any run that reaches the model clears the count,
  and so does moving the card out of `NeedsHuman`, so the escalation is one a
  human can reverse.

Neither existed when #77 was filed, which is why the card in that report ran
until someone noticed.

## What is still missing

A **spend** bound. Both bounds above count runs, not dollars, so a card making
expensive-but-progressing calls is not capped by anything. `max_cost_usd`
exists and is enforced — but against a figure that only moves once the alias
carries a `pricing` block, and none ship by default. Until rates are configured
for a provider, that card's spend is unbounded and
`GET /cards/{id}/cost` will say so with `cost_complete: false`.
