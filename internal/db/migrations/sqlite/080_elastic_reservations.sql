CREATE TABLE IF NOT EXISTS elastic_reservations (
    id TEXT PRIMARY KEY,
    app_id BIGINT NOT NULL REFERENCES apps(id),
    deployment_id BIGINT NOT NULL REFERENCES deployments(id),
    client_key TEXT NOT NULL,
    slot BIGINT NOT NULL CHECK (slot >= 0),
    owner_instance TEXT NOT NULL,
    owner_epoch BIGINT NOT NULL CHECK (owner_epoch > 0),
    state TEXT NOT NULL CHECK (state IN ('reserved', 'launching', 'ready', 'stopping', 'stopped')),
    UNIQUE(app_id, slot)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_elastic_reservations_client
    ON elastic_reservations(app_id, client_key) WHERE state <> 'stopped';
