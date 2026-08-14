-- Immutable application execution history. A replica index identifies a pool
-- slot; run_id identifies one concrete start of that slot. Keeping the two
-- separate prevents restarts and redeploys from blending into one log stream.
CREATE TABLE IF NOT EXISTS app_log_runs (
    run_id        TEXT PRIMARY KEY,
    app_id        INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    replica_index INTEGER NOT NULL,
    deployment_id INTEGER REFERENCES deployments(id) ON DELETE SET NULL,
    app_version   TEXT NOT NULL DEFAULT '',
    tier          TEXT NOT NULL DEFAULT '',
    provider      TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL,
    started_at    INTEGER NOT NULL,
    finished_at   INTEGER,
    oom_killed    INTEGER NOT NULL DEFAULT 0 CHECK (oom_killed IN (0, 1))
);

CREATE INDEX IF NOT EXISTS idx_app_log_runs_app_started
    ON app_log_runs(app_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_app_log_runs_replica_started
    ON app_log_runs(app_id, replica_index, started_at DESC);
