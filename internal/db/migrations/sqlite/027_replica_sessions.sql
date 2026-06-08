-- Per-instance session counts for the HA data plane. Each row records the
-- active session count a specific instance (instance_id) reports for one
-- replica slot (app_id, idx). updated_at bounds how long a crashed instance's
-- rows can inflate the fleet view before they are excluded as stale.
CREATE TABLE IF NOT EXISTS replica_sessions (
    instance_id   TEXT    NOT NULL,
    app_id        INTEGER NOT NULL,
    idx           INTEGER NOT NULL,
    active        INTEGER NOT NULL DEFAULT 0,
    last_activity INTEGER NOT NULL DEFAULT 0,
    updated_at    INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (instance_id, app_id, idx)
);
CREATE INDEX IF NOT EXISTS idx_replica_sessions_app_updated
    ON replica_sessions(app_id, updated_at);
