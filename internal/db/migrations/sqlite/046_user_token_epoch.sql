-- Session-revocation counter. Session JWTs embed the epoch they were issued
-- at; validation rejects a token whose epoch no longer matches the user's row.
-- Bumped by admin revoke-sessions and by password changes, so a hijacked or
-- orphaned session dies at its next request instead of living out its TTL.
ALTER TABLE users ADD COLUMN token_epoch INTEGER NOT NULL DEFAULT 0;
