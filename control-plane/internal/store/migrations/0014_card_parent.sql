-- Parentage was inferred from card_dependencies, and that table means two
-- different things.
--
-- Decomposition writes both kinds of edge into it: (parent -> each child),
-- recording what a card was split into, and (child N -> child N-1), recording
-- that the pieces are built in order. Both are dependencies, and neither says
-- which it is.
--
-- So a query that read "depends_on is a decomposed card" as "depends_on is a
-- child of card_id" matched the sibling edges too, and rendered piece A as
-- being part of piece B. A board built to show a person which cards belong
-- together showed them belonging to each other, arbitrarily, depending on
-- which row came back last.
--
-- Parentage is a fact about a card, so it is stored on the card. What stays in
-- card_dependencies is what the gate actually consumes: ordering.

ALTER TABLE cards
    ADD COLUMN parent_id uuid REFERENCES cards(id) ON DELETE SET NULL;

CREATE INDEX cards_parent_idx ON cards (parent_id) WHERE parent_id IS NOT NULL;

-- Backfill what can be recovered unambiguously: a decomposed card whose
-- dependent is NOT itself decomposed can only be hanging off its parent,
-- because sibling edges join two decomposed cards. A nested split -- a child
-- that was itself decomposed -- cannot be told from a sibling edge by shape
-- alone and is left null rather than guessed at. It reads as "no parent",
-- which is the honest answer and what the board showed before parentage
-- existed at all.
UPDATE cards ch
   SET parent_id = d.card_id
  FROM card_dependencies d
  JOIN cards p ON p.id = d.card_id
 WHERE d.depends_on = ch.id
   AND ch.source_type = 'decomposed'
   AND p.source_type <> 'decomposed';
