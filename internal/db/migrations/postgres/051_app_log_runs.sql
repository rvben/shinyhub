-- See the SQLite counterpart for the execution-identity rationale.
CREATE TABLE IF NOT EXISTS app_log_runs (
    run_id        text PRIMARY KEY,
    app_id        bigint NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    replica_index bigint NOT NULL,
    deployment_id bigint REFERENCES deployments(id) ON DELETE SET NULL,
    app_version   text NOT NULL DEFAULT '',
    tier          text NOT NULL DEFAULT '',
    provider      text NOT NULL DEFAULT '',
    status        text NOT NULL,
    started_at    bigint NOT NULL,
    finished_at   bigint,
    oom_killed    integer NOT NULL DEFAULT 0 CHECK (oom_killed IN (0, 1))
);

CREATE INDEX IF NOT EXISTS idx_app_log_runs_app_started
    ON app_log_runs(app_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_app_log_runs_replica_started
    ON app_log_runs(app_id, replica_index, started_at DESC);
