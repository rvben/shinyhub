ALTER TABLE fleet_runs ADD COLUMN run_sequence BIGINT NOT NULL DEFAULT 0;
ALTER TABLE fleet_runs ADD COLUMN status TEXT NOT NULL DEFAULT 'running'
    CHECK (status IN ('running', 'succeeded', 'partial', 'conflict', 'failed'));
ALTER TABLE fleet_runs ADD COLUMN heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP;
ALTER TABLE fleet_runs ADD COLUMN finished_at TIMESTAMPTZ;
ALTER TABLE fleet_runs ADD COLUMN exit_code INTEGER;
ALTER TABLE fleet_runs ADD COLUMN exit_reason TEXT NOT NULL DEFAULT '';

WITH ordered AS (
    SELECT id, ROW_NUMBER() OVER (ORDER BY created_at, id) AS run_sequence
    FROM fleet_runs
)
UPDATE fleet_runs
SET run_sequence = ordered.run_sequence
FROM ordered
WHERE fleet_runs.id = ordered.id;

CREATE UNIQUE INDEX idx_fleet_runs_sequence ON fleet_runs(run_sequence);
CREATE INDEX idx_fleet_runs_status_heartbeat ON fleet_runs(status, heartbeat_at);

CREATE TABLE fleet_run_sequence (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    last_sequence BIGINT NOT NULL
);

INSERT INTO fleet_run_sequence (singleton, last_sequence)
VALUES (1, (SELECT COALESCE(MAX(run_sequence), 0) FROM fleet_runs));
