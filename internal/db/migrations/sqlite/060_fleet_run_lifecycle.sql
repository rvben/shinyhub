ALTER TABLE fleet_runs ADD COLUMN run_sequence INTEGER NOT NULL DEFAULT 0;
ALTER TABLE fleet_runs ADD COLUMN status TEXT NOT NULL DEFAULT 'running'
    CHECK (status IN ('running', 'succeeded', 'partial', 'conflict', 'failed'));
-- SQLite rejects non-constant defaults on ALTER TABLE. Existing rows are
-- backfilled here; new registrations always write heartbeat_at explicitly.
ALTER TABLE fleet_runs ADD COLUMN heartbeat_at DATETIME;
ALTER TABLE fleet_runs ADD COLUMN finished_at DATETIME;
ALTER TABLE fleet_runs ADD COLUMN exit_code INTEGER;
ALTER TABLE fleet_runs ADD COLUMN exit_reason TEXT NOT NULL DEFAULT '';

UPDATE fleet_runs AS current
SET run_sequence = (
    SELECT COUNT(*)
    FROM fleet_runs AS prior
    WHERE prior.created_at < current.created_at
       OR (prior.created_at = current.created_at AND prior.id <= current.id)
);

UPDATE fleet_runs SET heartbeat_at = created_at WHERE heartbeat_at IS NULL;

CREATE UNIQUE INDEX idx_fleet_runs_sequence ON fleet_runs(run_sequence);
CREATE INDEX idx_fleet_runs_status_heartbeat ON fleet_runs(status, heartbeat_at);

CREATE TABLE fleet_run_sequence (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    last_sequence INTEGER NOT NULL
);

INSERT INTO fleet_run_sequence (singleton, last_sequence)
VALUES (1, (SELECT COALESCE(MAX(run_sequence), 0) FROM fleet_runs));
