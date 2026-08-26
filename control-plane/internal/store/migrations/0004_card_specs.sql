-- Spec §10.2: "Human approves the completed spec. Only then may the control
-- plane promote the card to Ready." This table stores the specification
-- document itself plus the record of whether a human ever approved it.
--
-- approved_sha256 is the point of this table's design: approval is of a
-- SPECIFIC document, not of the card. Storing the hash of the exact content
-- that was approved means an edit made after approval (even one that ends up
-- byte-identical to what was approved before, if written through PutSpec)
-- is detectable and, per store.PutSpec, always revokes the approval outright.
CREATE TABLE card_specs (
    card_id         uuid PRIMARY KEY REFERENCES cards(id) ON DELETE CASCADE,
    content         text NOT NULL,
    updated_at      timestamptz NOT NULL DEFAULT now(),
    updated_by      text NOT NULL,
    approved_by     text,
    approved_at     timestamptz,
    approved_sha256 text
);
