ALTER TABLE deployments ADD COLUMN producer_barrier_entered INTEGER NOT NULL DEFAULT 0
    CHECK (producer_barrier_entered IN (0, 1));
ALTER TABLE deployments ADD COLUMN schedule_snapshot_recorded INTEGER NOT NULL DEFAULT 0
    CHECK (schedule_snapshot_recorded IN (0, 1));
ALTER TABLE deployments ADD COLUMN prior_schedule_snapshot_recorded INTEGER NOT NULL DEFAULT 0
    CHECK (prior_schedule_snapshot_recorded IN (0, 1));

ALTER TABLE replicas ADD COLUMN data_producer_deployment_id INTEGER;
ALTER TABLE replicas ADD COLUMN data_producer_app_version TEXT NOT NULL DEFAULT '';
ALTER TABLE replicas ADD COLUMN data_producer_content_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE replicas ADD COLUMN data_producer_fingerprint TEXT NOT NULL DEFAULT '';

-- Transactionally persists the physical completion order of every data writer.
-- The app publication fence serializes those writers before this is allocated.
ALTER TABLE apps ADD COLUMN data_write_sequence INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN elastic_orphan_risk INTEGER NOT NULL DEFAULT 0
    CHECK (elastic_orphan_risk IN (0, 1));

CREATE TABLE app_data_publication (
    app_id INTEGER PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL,
    schedule_run_id INTEGER NOT NULL,
    producer_deployment_id INTEGER,
    producer_app_version TEXT NOT NULL DEFAULT '',
    producer_content_digest TEXT NOT NULL DEFAULT '',
    producer_fingerprint TEXT NOT NULL DEFAULT '',
    data_write_sequence INTEGER NOT NULL DEFAULT 0,
    published_at DATETIME NOT NULL
);

-- Durable safety fact for a physical writer that may have partially mutated
-- shared data and has not since succeeded. It deliberately does not depend on
-- retained run history, so pruning cannot erase compatibility quarantine.
CREATE TABLE schedule_data_uncertainty (
    schedule_id INTEGER PRIMARY KEY REFERENCES app_schedules(id) ON DELETE CASCADE,
    data_write_sequence INTEGER NOT NULL,
    schedule_run_id INTEGER NOT NULL,
    status TEXT NOT NULL,
    recorded_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE deployment_schedule_snapshots (
    deployment_id INTEGER NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    cron_expr TEXT NOT NULL,
    command_json TEXT NOT NULL,
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    timeout_seconds INTEGER NOT NULL,
    overlap_policy TEXT NOT NULL,
    missed_policy TEXT NOT NULL,
    deploy_trigger TEXT NOT NULL,
    timezone TEXT,
    on_success TEXT NOT NULL,
    min_roll_interval_seconds INTEGER NOT NULL,
    roll_fallback TEXT NOT NULL,
    max_defer_age_seconds INTEGER NOT NULL,
    PRIMARY KEY (deployment_id, name)
);

CREATE TABLE deployment_prior_schedule_snapshots (
    deployment_id INTEGER NOT NULL REFERENCES deployments(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    cron_expr TEXT NOT NULL,
    command_json TEXT NOT NULL,
    enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    timeout_seconds INTEGER NOT NULL,
    overlap_policy TEXT NOT NULL,
    missed_policy TEXT NOT NULL,
    deploy_trigger TEXT NOT NULL,
    timezone TEXT,
    on_success TEXT NOT NULL,
    min_roll_interval_seconds INTEGER NOT NULL,
    roll_fallback TEXT NOT NULL,
    max_defer_age_seconds INTEGER NOT NULL,
    PRIMARY KEY (deployment_id, name)
);
