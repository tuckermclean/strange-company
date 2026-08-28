-- §21: every state transition produces a record carrying `evidence`. The
-- worker attaches evidence BEFORE it transitions a card, precisely so a card
-- can never arrive in a new state with nothing explaining why -- which means
-- evidence cannot live on the history row, because at that moment there is no
-- history row yet.
--
-- Kept separate from artifacts (§20) on purpose. An artifact is a thing a run
-- produced and is addressable on its own; evidence is the worker's account of
-- one step, and exists to make a transition legible.
CREATE TABLE card_evidence (
    id         bigserial PRIMARY KEY,
    card_id    uuid NOT NULL REFERENCES cards(id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    actor_id   text NOT NULL,
    summary    text NOT NULL,

    -- Structured context the worker chose to record. §21 forbids exposing
    -- model chain-of-thought, so this holds facts about the step -- phase,
    -- attempt, error -- never reasoning.
    detail     jsonb
);

CREATE INDEX card_evidence_card_id_created_at_idx ON card_evidence (card_id, created_at);
