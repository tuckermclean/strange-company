-- §10.1 screening costs a model call. Without a record of what was already
-- screened, a reconciler that walks the backlog on a timer re-screens every
-- card on every pass -- an unbounded, recurring bill for an answer that has
-- not changed.
--
-- The hash is of the exact document that was screened, for the same reason
-- approved_sha256 is: an edit after screening must be detectable, so a
-- specification that changed gets a fresh answer while one that did not costs
-- nothing.
ALTER TABLE card_specs
    ADD COLUMN screened_sha256 text,
    ADD COLUMN screened_score  integer,
    ADD COLUMN screened_at     timestamptz;
