CREATE TABLE fleet_runs (
    id TEXT PRIMARY KEY,
    fleet_id TEXT NOT NULL,
    kind TEXT NOT NULL DEFAULT 'fleet_apply',
    provenance TEXT NOT NULL DEFAULT '{}',
    user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE deployments ADD COLUMN run_id TEXT REFERENCES fleet_runs(id) ON DELETE SET NULL;
ALTER TABLE deployments ADD COLUMN restored_from_id INTEGER REFERENCES deployments(id) ON DELETE SET NULL;
ALTER TABLE audit_events ADD COLUMN run_id TEXT REFERENCES fleet_runs(id) ON DELETE SET NULL;

CREATE INDEX idx_fleet_runs_fleet_created ON fleet_runs(fleet_id, created_at DESC);
CREATE INDEX idx_deployments_run_id ON deployments(run_id);
CREATE INDEX idx_audit_events_run_id ON audit_events(run_id);
