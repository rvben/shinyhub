-- Per-app session cap enforced by the proxy: when every replica's active
-- connection count reaches this value, new (cookie-less) requests are shed
-- with 503 Retry-After. Existing sessions (valid sticky cookie) continue
-- to forward so the cap never kills a live WS. 0 means "fall back to the
-- runtime-wide default" (Runtime.DefaultMaxSessionsPerReplica).
ALTER TABLE apps ADD COLUMN max_sessions_per_replica INTEGER NOT NULL DEFAULT 0
    CHECK (max_sessions_per_replica >= 0 AND max_sessions_per_replica <= 1000);
