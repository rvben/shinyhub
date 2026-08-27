-- Keep lease renewal separate from updated_at: the latter is user-facing
-- deployment activity ("Last save"), while heartbeats occur when no save does.
-- SQLite ALTER TABLE accepts only constant defaults, so writers set the
-- database-clock heartbeat explicitly and the epoch fallback fails safely
-- stale if an older writer ever omits it.
ALTER TABLE development_sessions
    ADD COLUMN heartbeat_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00';

UPDATE development_sessions SET heartbeat_at = updated_at;
