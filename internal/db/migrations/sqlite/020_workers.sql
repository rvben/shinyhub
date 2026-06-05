-- Worker registry: one row per joined worker host (node). node_id is the stable
-- identity bound into the worker's client certificate and mirrored to
-- replicas.worker_id. cert_fingerprint is the SHA-256 of the currently trusted
-- client cert (hex); status is 'up' or 'down'.
CREATE TABLE IF NOT EXISTS workers (
    node_id          TEXT PRIMARY KEY,
    name             TEXT NOT NULL DEFAULT '',
    advertise_addr   TEXT NOT NULL,
    tier             TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'up',
    cert_fingerprint TEXT NOT NULL DEFAULT '',
    version          TEXT NOT NULL DEFAULT '',
    last_heartbeat   TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL DEFAULT (datetime('now'))
);
