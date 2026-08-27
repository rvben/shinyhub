-- Keep lease renewal separate from updated_at: the latter is user-facing
-- deployment activity ("Last save"), while heartbeats occur when no save does.
ALTER TABLE development_sessions
    ADD COLUMN heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP;

UPDATE development_sessions SET heartbeat_at = updated_at;
