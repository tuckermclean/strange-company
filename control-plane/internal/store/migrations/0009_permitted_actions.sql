-- Spec §5: every card carries a permitted_actions block, and §10's gate
-- refuses to promote a card without one. Nothing supplied it, so no card
-- could ever reach Ready -- the board would fill with work nobody could claim.
--
-- Stored per card rather than consulted globally at gate time: a global
-- default would make the gate's check unfailable and turn a real rule into a
-- rubber stamp. A card created by a path that forgets to stamp one still
-- fails the gate, which is the check doing its job.
--
-- Nullable for exactly that reason. Backfilling every existing card would
-- grant an allowlist to cards nobody reviewed.
ALTER TABLE cards
    ADD COLUMN permitted_actions jsonb;
