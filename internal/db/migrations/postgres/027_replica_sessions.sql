-- Per-instance session counts for the HA data plane. See sqlite/027.
CREATE TABLE replica_sessions (
    instance_id   TEXT   NOT NULL,
    app_id        bigint NOT NULL,
    idx           bigint NOT NULL,
    active        bigint NOT NULL DEFAULT 0,
    last_activity bigint NOT NULL DEFAULT 0,
    updated_at    bigint NOT NULL DEFAULT 0,
    PRIMARY KEY (instance_id, app_id, idx)
);
CREATE INDEX idx_replica_sessions_app_updated
    ON replica_sessions(app_id, updated_at);
