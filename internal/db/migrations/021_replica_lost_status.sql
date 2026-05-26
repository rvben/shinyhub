-- The replica status domain gains 'lost': a replica whose worker stopped
-- heartbeating. 'lost' is distinct from 'crashed' (which the watcher restarts):
-- a lost replica is excluded from proxy routing, surfaced as degraded, never
-- auto-restarted (so a transient worker blip does not thrash), and does not
-- block a redeploy (the next deploy re-places it). status is free-text TEXT
-- (no CHECK), so no domain alteration is needed; this index speeds the
-- routing-exclusion and degraded-state queries that filter on status.
CREATE INDEX IF NOT EXISTS idx_replicas_status ON replicas(status);
