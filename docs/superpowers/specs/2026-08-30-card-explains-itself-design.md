# The card explains itself

**Status:** design, approved 2026-08-30
**Scope:** one sub-project of five. See "Where this sits" below.

## Why this first

Between 2026-08-29 and 2026-08-30 the deployment agent filed eight issues
against a running control plane (#51, #52, #57, #70, #73, #76, #77, #80). Every
one of them required a human to read pod logs, query Postgres directly, and
reconstruct a sequence of events by hand. Five releases came out of that night,
and the diagnosis was archaeology every single time.

The system cannot currently explain itself. That is not a missing convenience;
it is where the operator's whole budget goes.

It also explains a pattern that recurs across the codebase: values are written
and never read. `card_evidence` was written from M2 and read by nothing until
0.10.0. The attempt ledger recorded one phase out of four until 0.11.0.
`infrastructure_failures` was incremented and consulted by no code until 0.11.1.
`card_history` has been written since M0 and still has no reader. **The
characteristic defect of this system is a number nobody reads**, and the read
side is what this sub-project builds.

Governance (the fifth sub-project) is a read surface by definition and cannot
start before this. Turning the human out of the loop (the third) is unsafe
before it: an autonomous loop you cannot inspect is one whose mistakes you learn
about from the invoice.

## Where this sits

Five sub-projects were identified. This is the first.

1. **The card explains itself** — this document.
2. The autonomy dial: human-in-the-loop as a setting.
3. The interactive spec/issue generator.
4. A reviewer that scales to any diff size.
5. Governance: portfolio, cost, what is in flight, what is stuck.

## Explicitly not in scope

- **No ingress, no browser-reachable control plane, no auth.** Artifacts reach
  a human as Vikunja attachments. Serving them over HTTP is deferred to a later
  sub-project along with the authentik forward-auth decision that goes with it.
- **No rendered HTML card page.** The Vikunja task detail view is the view.
- **No Hermes querying.** §33's "why did card 143 escalate" belongs with
  governance, where we will know what questions are actually asked.
- **No new UI of any kind.** That decision (docs/reference/hermes-integration-notes.md)
  stands unchanged.

## The two surfaces

§21 requires the stakeholder view to answer "what happened to card X?" *without
exposing model chain-of-thought*. The operator needs exactly that chain-of-thought
when something breaks. Both are true, and they are two audiences rather than one
compromise.

| | Surface | Contents | Rule |
|---|---|---|---|
| Stakeholder | Vikunja task **description** | §33's ordered list | §21-clean. No model reasoning. |
| Operator | Vikunja task **attachments** | Specs, plans, diffs, raw run logs | Raw. Labelled as unverified model output. |

One dataset, two renderings. §21 is amended to say which view it governs rather
than being contradicted: the description is the stakeholder view and the rule
holds there absolutely.

The attachment carrying a run log is what satisfies "read any Meeseeks or
subagent discourse with a click". The click is Vikunja's own download button.

## The card, per §33

§33 fixes the contents and the order. Each line below names where the data
comes from and what is missing.

| # | §33 item | Source | State |
|---|---|---|---|
| 1 | title | `cards.title` | done |
| 2 | state / current phase | `cards.state`, `cards.phase` | done |
| 3 | why it exists | `cards.source_url`, spec artifact | done |
| 4 | acceptance criteria | `card_specs.content` | **needs extraction** |
| 5 | current worker | `claimed_by`, `phase`, `implementation_attempt`, policy ladder | **needs the ladder length** |
| 6 | latest result | newest `card_evidence.summary` | done |
| 7 | cost | `cards.cost_usd`, `max_cost_usd`, unpriced count | data exists; reads `$0` until rates are configured |
| 8 | artifacts | `card_artifacts` + attachments | **needs the attachment pusher** |
| 9 | history with timestamps | `card_history` | **no reader exists** |

### 4. Acceptance criteria

No new parsing. `internal/spec` already has `Parse(cardID, doc) (*Document,
[]Problem)`, and `Document` carries structured `Criterion` values — the
specification gate depends on them, so they are validated rather than
scraped. Render those.

If a card has no spec, or the spec has no criteria, render nothing rather than
guessing. A card claiming criteria it does not have is worse than one that
admits it has none.

### 5. Current worker

§33's example is `Meeseeks #8f2c — Implementation — Haiku attempt 2/3`. The
"of 3" is the current rung's `max_attempts`, which lives in the policy. The
reconciler does not have the policy today.

`main` already holds a `*policy.Policy`; pass it to the reconciler. Rendering
`attempt 2` without the denominator would lose the thing the operator actually
wants to know, which is how much rope is left.

### 9. History

`card_history` rows carry `at`, `from_state`, `to_state`, `actor_type`,
`actor_id`, `reason`. The reconciler reads them through a new store method.

Add `GET /cards/{id}/history` as well. Nothing in this sub-project consumes it —
the reconciler goes straight to the store — but §30 lists a read surface per
card and governance will want it. It is a dozen lines against a table that
already exists, and skipping it is how `card_history` stayed unread for four
milestones.

## The attachment mechanism

Verified against Vikunja v0.24.6 source, not recalled:

- `PUT /api/v1/tasks/{task}/attachments`, `multipart/form-data`, field name
  **`files`** (plural; the handler iterates `form.File["files"]`).
- `GET /api/v1/tasks/{task}/attachments` lists them; each carries
  `file.name`, `file.mime`, `file.size`.
- The routes are registered only when `ServiceEnableTaskAttachments` is on, so
  on an install without it they are simply absent — the same shape as task
  comments. The operator's instance reports `task_attachments_enabled: true`
  and `max_file_size: 20MB`.

### The failure mode to encode

`UploadTaskAttachment` returns **HTTP 200 with a per-file `errors` array**:

```json
{"errors": [...], "success": [ ... ]}
```

A failed upload does not fail the request. Success must be read from the body.
This is structurally the same trap as the Hermes gateway returning a backend
outage as a 200 completion, which cost a round trip to find. It is encoded here
before it is rediscovered.

### Idempotence

Artifacts are immutable, so a name that is already attached is already done.

- Names are deterministic and carry their identity: `spec.md`, `plan.md`,
  `diff.patch`, `tests-attempt-1.log`, `implementation-attempt-2.log`,
  `review.md`.
- Each pass lists the task's attachments and uploads only the missing names.

Without this the reconciler re-uploads every artifact on every tick. That is the
same class of defect as the description-rewrite loop fixed in 0.10.0, and the
consequence here is worse: unbounded storage growth on the operator's Vikunja
rather than merely a noisy timestamp.

### Size

Cap each attachment well below the instance's 20 MB. A runaway harness can emit
far more than that, and an upload rejected for size is a silent gap in the
audit trail. Truncate with an explicit marker in the file so a reader knows they
are looking at a prefix, and record the true size in the description.

## Data gap: the run logs mostly do not exist

This is worse than it first appeared, and it is the load-bearing gap in the
whole sub-project.

- `internal/teststep` stores `result.Raw` **only when the run did not
  complete** — so a card whose tests were written cleanly keeps no log of it.
- `internal/implstep` **never stores `result.Raw` at all.** Not on failure,
  not on success.

The implementation phase is the one that writes the code. Its raw output — the
actual Meeseeks discourse a human would most want to read — has never been
stored anywhere. It exists in the pod's logs for as long as the Job survives,
and the Job is deleted as soon as the control plane has read it.

So "read any Meeseeks discourse with a click" currently has almost nothing to
click. Attaching artifacts is not enough on its own; the artifact has to start
existing first.

Store the run log on **every** run in both steps, under a distinct artifact
type (`ArtifactRunLog`), capped as above. This is the artifact that satisfies
the operator surface: it is the raw discourse, and everything else in this
document is a way of getting it in front of a person.

## Data flow

No new process. The existing Vikunja reconciler tick gains work:

```
for each card:
    render description from card + spec + evidence + cost + history + policy
    if description differs from what Vikunja holds (text comparison):
        update the task
    list the task's attachments
    for each artifact with no attachment of that name:
        upload it
```

The description comparison already exists and already normalises for Vikunja's
sanitisation. The attachment step is new and follows the same shape: read what
is there, write only what is missing.

## Error handling

The reconciler's existing rule holds throughout: **a failure to describe a card
must never fail the pass.** A board that has stopped tracking reality is a worse
outcome than a card missing its attachments.

- Upload failure: log, continue to the next artifact.
- Attachments disabled (route 404): log once at debug, continue. Same treatment
  as comments.
- Missing spec, missing history, missing cost: render the sections that exist.
- Oversized artifact: truncate, mark, continue.

## Testing

Against a fake Vikunja, as the existing reconciler tests do:

- The description carries all nine §33 items in §33's order.
- A card with no acceptance criteria renders none rather than inventing them.
- Attempt rendering shows the denominator from the policy ladder.
- An artifact already attached is not uploaded again — asserted across three
  consecutive passes, because one pass proves nothing about a loop.
- A 200 response carrying a per-file error is treated as a failure, not a
  success.
- An oversized artifact is truncated with a marker rather than dropped.
- Attachments disabled: the pass still completes and the description is still
  written.
- Run logs are stored for a successful run, not only a failed one.

## Risks

**The description will be long.** Nine sections including timestamped history is
a lot of text in a Vikunja task. The board shows titles and the detail view
shows everything, so it works — but this is the thing most likely to feel wrong
in practice, and it is the argument for the deferred rendered page. Shipping the
cheap version first is deliberate: it puts a real card in front of a real reader
before anyone designs around a guess about what they need.

**Cost will read `$0`.** The plumbing is complete as of 0.11.0; the rates are
the operator's to configure and none ship by default. The card should say
`unpriced` rather than `$0.00` wherever `cost_complete` is false, so the number
is never mistaken for a cheap card.

## Deferred, with the decision already made

- Artifacts served over HTTP from a browser-reachable control plane, behind
  **authentik forward-auth at the ingress** — chosen, then deferred when the
  artifact mechanism changed to attachments. The reasoning stands for whenever
  that sub-project starts: it mirrors the Hermes dashboard's existing
  protection, adds no auth code or new secret to the control plane, and the
  service continues to trust its network exactly as it does today.
