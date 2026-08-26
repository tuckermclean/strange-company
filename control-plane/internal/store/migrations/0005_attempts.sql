-- Spec §12.1: an implementation attempt is the coding agent receiving valid
-- context, doing work, the runner regaining control, verification running,
-- and verification failing. A provider outage, a Kubernetes scheduling
-- failure, an image pull failure, or an auth outage is not an attempt --
-- those increment infrastructure_failures instead (cards.infrastructure_failures,
-- migration 0001) and must never burn a Haiku/Sonnet/Opus rung on the
-- escalation ladder (§12.3).
--
-- This table is the audit trail behind that distinction: one row per coding
-- run, whether or not it counted as an attempt, so the evidence (tokens,
-- cost, duration, summary) survives even for runs the ladder ignores.
--
-- attempt_number is NULL for any run that did not count as an implementation
-- attempt (completed, infra error, timeout, or policy violation) -- it is
-- only ever set for a StatusFailed run, and only then does it correspond to
-- the card's implementation_attempt counter at the moment this row was
-- written.
--
-- cost_usd is nullable on purpose: Claude Code reports total_cost_usd
-- directly on its terminal result event, but Codex reports tokens only and
-- its cost has to be computed downstream from a price table (spec §22).
-- Storing 0 for Codex would make every cost question quietly wrong -- NULL
-- means "this harness does not report cost", not "this run was free".
CREATE TABLE card_attempts (
    id                 bigserial PRIMARY KEY,
    card_id            uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    run_id             text NOT NULL,
    phase              text NOT NULL,
    attempt_number     integer,               -- NULL when the run did not count as an attempt
    model_alias        text NOT NULL,
    provider           text NOT NULL,
    harness            text NOT NULL,
    model              text NOT NULL,
    status             text NOT NULL,
    counted_as_attempt boolean NOT NULL,
    summary            text,
    input_tokens       integer NOT NULL DEFAULT 0,
    output_tokens      integer NOT NULL DEFAULT 0,
    cached_tokens      integer NOT NULL DEFAULT 0,
    cost_usd           numeric(12,6),         -- NULL when the harness does not report cost
    duration_ms        bigint NOT NULL DEFAULT 0,
    started_at         timestamptz NOT NULL DEFAULT now(),
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX card_attempts_card_idx ON card_attempts (card_id, created_at);
