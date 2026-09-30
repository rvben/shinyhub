-- Durable record of the asynchronous pool cycle a settings PATCH launches.
-- redeploy_seq_launched advances in the same transaction as a pool-shape change
-- on a running app, so "a redeploy is owed" commits or rolls back with the
-- settings it applies. last_redeploy_* holds the outcome reported for the
-- latest served seq; a seq is owed while last_redeploy_seq < redeploy_seq_launched.
-- redeploy_claim_seq/_epoch name the control-plane owner (lease epoch) serving
-- a seq, so only that claimant can report its outcome.
-- redeploy_full_seq is the latest seq armed by a change that needs the whole
-- pool cycled (placement, resource limits, worker dials); a replica-count-only
-- seq is served by resizing the live pool. A claimant cycles the pool whenever
-- redeploy_full_seq > last_redeploy_seq, so a structural seq superseded by a
-- newer replica-only seq still gets its full cycle. A failed or skipped outcome
-- moves an owed full cycle on to the next seq, so it stays owed until a
-- completed or partial redeploy or boot applies the settings.
-- last_redeploy_at is Unix-epoch seconds, 0 when nothing has been reported.
ALTER TABLE apps ADD COLUMN redeploy_seq_launched INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN last_redeploy_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN last_redeploy_outcome TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN last_redeploy_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE apps ADD COLUMN last_redeploy_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN redeploy_claim_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN redeploy_claim_epoch INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN redeploy_full_seq INTEGER NOT NULL DEFAULT 0;
