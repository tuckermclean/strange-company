-- The claim query's second branch scans for InProgress cards with a dead
-- lease. Without this the reclaim path degrades to a sequential scan as the
-- board grows.
CREATE INDEX cards_expired_lease_idx
    ON cards (lease_expires_at)
    WHERE state = 'InProgress';
