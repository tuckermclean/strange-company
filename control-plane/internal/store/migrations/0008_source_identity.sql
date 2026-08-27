-- Ingestion runs on a timer, so the same GitHub issue is seen on every pass.
-- Without a unique identity for a source item, each pass creates another card
-- for the same issue and the board fills with duplicates of one piece of work.
--
-- Partial, because source_external_id is nullable: cards that did not come
-- from an external source (seeded by hand, or created by a future API) have no
-- identity to collide on and must not be forced to invent one.
CREATE UNIQUE INDEX IF NOT EXISTS cards_source_identity_idx
    ON cards (source_type, source_external_id)
    WHERE source_external_id IS NOT NULL;
