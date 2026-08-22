CREATE TABLE IF NOT EXISTS app_fleet_state (
    app_id INTEGER PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
    successful_run_id TEXT REFERENCES fleet_runs(id) ON DELETE SET NULL,
    declaration TEXT NOT NULL DEFAULT '[]',
    desired_content_digest TEXT NOT NULL DEFAULT '',
    applied_at DATETIME,
    latest_run_id TEXT REFERENCES fleet_runs(id) ON DELETE SET NULL,
    convergence_status TEXT NOT NULL DEFAULT 'in_sync'
        CHECK (convergence_status IN ('in_sync', 'incomplete')),
    convergence_error TEXT NOT NULL DEFAULT '',
    convergence_updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_app_fleet_state_latest_run ON app_fleet_state(latest_run_id);
