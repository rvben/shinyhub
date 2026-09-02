-- Runtime identities for staged, active, and draining generations. The
-- composite ownership key prevents associating a deployment with another app.
CREATE UNIQUE INDEX IF NOT EXISTS idx_deployments_id_app ON deployments(id, app_id);

CREATE TABLE IF NOT EXISTS deployment_replicas (
    app_id          INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    deployment_id   INTEGER NOT NULL,
    idx             INTEGER NOT NULL,
    pid             INTEGER,
    port            INTEGER,
    status          TEXT NOT NULL,
    provider        TEXT NOT NULL DEFAULT '',
    tier            TEXT NOT NULL DEFAULT '',
    endpoint_url    TEXT NOT NULL DEFAULT '',
    worker_id       TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (deployment_id, idx),
    FOREIGN KEY (deployment_id, app_id) REFERENCES deployments(id, app_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_deployment_replicas_app
    ON deployment_replicas(app_id, deployment_id, idx);
