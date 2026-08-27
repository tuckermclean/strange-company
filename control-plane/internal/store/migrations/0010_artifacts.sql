-- Spec §20. Artifacts are the evidence a stakeholder view is built from: what
-- a run produced, not what a model was thinking. §21 is explicit that the view
-- must answer "what happened to card X?" WITHOUT exposing chain-of-thought, so
-- this table holds outputs -- plans, diffs, test output, reviews -- and never
-- reasoning.
--
-- Artifacts accumulate; they are never updated. Attempt 4's diff does not
-- replace attempt 3's, or the cost ledger and the escalation record would
-- describe a history that no longer exists.
CREATE TABLE artifacts (
    id           uuid PRIMARY KEY,
    card_id      uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,

    -- The attempt that produced this, when one did. Null for artifacts that
    -- belong to the card rather than to a run: the specification itself, an
    -- ambiguity report, a human decision.
    attempt_id   bigint REFERENCES card_attempts(id) ON DELETE SET NULL,

    type         text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    actor        text NOT NULL,
    model        text,
    commit_sha   text,
    content_type text NOT NULL,

    -- storage_uri is empty while the content lives here. §20: small text
    -- artifacts may live in PostgreSQL, and S3/MinIO arrives only when size
    -- requires it -- at which point this column carries the pointer and
    -- content goes empty.
    storage_uri  text NOT NULL DEFAULT '',
    content      text NOT NULL DEFAULT '',

    -- sha256 of the COMPLETE content, before any truncation. A hash of the
    -- stored copy would quietly certify a truncated artifact as the whole
    -- thing.
    sha256       text NOT NULL,

    -- Size of the complete content, and whether what is stored is all of it.
    -- §20 caps large logs; a cap that is invisible turns a truncated log into
    -- a misleading one.
    size_bytes   bigint NOT NULL,
    truncated    boolean NOT NULL DEFAULT false
);

CREATE INDEX artifacts_card_id_created_at_idx ON artifacts (card_id, created_at);
CREATE INDEX artifacts_card_id_type_idx ON artifacts (card_id, type);
