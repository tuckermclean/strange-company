-- The §10.2 specification conversation lives in the Hermes dashboard, so the
-- only thing the control plane keeps is the pointer to it. Without this, the
-- next pass over a card cannot tell that a conversation is already open and
-- would start a second one, splitting the human's context across two sessions.
--
-- Nullable, because most cards never need a conversation at all: screening
-- sends only the materially ambiguous ones here.
ALTER TABLE cards
    ADD COLUMN spec_session_id text;

-- Partial: only the cards actually in conversation are indexed, and a lookup
-- by session id (answering "which card is this dashboard session about?")
-- stays cheap as the board grows.
CREATE INDEX IF NOT EXISTS cards_spec_session_id_idx
    ON cards (spec_session_id)
    WHERE spec_session_id IS NOT NULL;
