ALTER TABLE app_schedules ADD COLUMN deploy_trigger TEXT NOT NULL DEFAULT 'never'
    CHECK (deploy_trigger IN ('never', 'first_deploy', 'bundle_change'));

ALTER TABLE schedule_runs ADD COLUMN deployment_id INTEGER;
ALTER TABLE schedule_runs ADD COLUMN app_version TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_runs ADD COLUMN content_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_runs ADD COLUMN producer_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_runs ADD COLUMN producer_command_json TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_runs ADD COLUMN deploy_obligation_id INTEGER;
ALTER TABLE schedule_runs ADD COLUMN publishes_data INTEGER NOT NULL DEFAULT 0
    CHECK (publishes_data IN (0, 1));
ALTER TABLE schedule_runs ADD COLUMN data_write_sequence INTEGER NOT NULL DEFAULT 0;
ALTER TABLE schedule_runs ADD COLUMN provenance_admission INTEGER NOT NULL DEFAULT 0
    CHECK (provenance_admission IN (0, 1));

-- An unclean in-place upgrade can inherit a 0.12.x one-shot process that has
-- no durable PID and does not hold the new publication lifetime flock. Preserve
-- that ambiguity explicitly so startup can require a host-level fence instead
-- of recovering consumers over a potentially live writer.
CREATE TABLE legacy_unfenced_schedule_runs (
    run_id INTEGER PRIMARY KEY REFERENCES schedule_runs(id),
    detected_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO legacy_unfenced_schedule_runs (run_id)
SELECT id FROM schedule_runs WHERE status = 'running';

-- A tableflip successor migrates while the old server is still alive. Close
-- that rolling-upgrade window in the database itself: a pre-069 binary cannot
-- supply these columns, so it must not admit another process after the snapshot
-- above. Keep the trigger permanently; it also prevents future callers from
-- creating execution rows whose bundle cannot be fenced or attributed.
CREATE TRIGGER schedule_runs_require_execution_provenance
BEFORE INSERT ON schedule_runs
WHEN NEW.status = 'running' AND NEW.provenance_admission <> 1
BEGIN
    SELECT RAISE(ABORT, 'schedule run requires deployment provenance; stop the legacy server before upgrading');
END;

ALTER TABLE schedule_activations ADD COLUMN source_deployment_id INTEGER;
ALTER TABLE schedule_activations ADD COLUMN source_app_version TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_activations ADD COLUMN source_content_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_activations ADD COLUMN source_producer_fingerprint TEXT NOT NULL DEFAULT '';

-- The cache is mutable, so provenance is a single last-writer pointer. Keeping
-- an ever-succeeded set would incorrectly bless a rollback after another
-- producer had overwritten the shared data.
CREATE TABLE schedule_producer_state (
    schedule_id          INTEGER PRIMARY KEY REFERENCES app_schedules(id) ON DELETE CASCADE,
    content_digest       TEXT NOT NULL,
    producer_fingerprint TEXT NOT NULL,
    producer_command_json TEXT NOT NULL,
    deployment_id        INTEGER,
    app_version          TEXT NOT NULL DEFAULT '',
    schedule_run_id      INTEGER,
    publication_generation INTEGER NOT NULL DEFAULT 1,
    data_write_sequence  INTEGER NOT NULL DEFAULT 0,
    published_at         DATETIME NOT NULL
);

-- One durable desired-state record is created for each deployment/producer
-- identity. Old running work may finish, but it cannot satisfy a newer row.
CREATE TABLE schedule_deploy_obligations (
    id                   INTEGER PRIMARY KEY,
    schedule_id          INTEGER NOT NULL REFERENCES app_schedules(id) ON DELETE CASCADE,
    deployment_id        INTEGER NOT NULL,
    app_version          TEXT NOT NULL,
    content_digest       TEXT NOT NULL,
    producer_fingerprint TEXT NOT NULL,
    producer_command_json TEXT NOT NULL,
    timeout_seconds      INTEGER NOT NULL,
    on_success           TEXT NOT NULL,
    min_roll_interval_seconds INTEGER NOT NULL,
    roll_fallback        TEXT NOT NULL,
    max_defer_age_seconds INTEGER NOT NULL,
    status               TEXT NOT NULL CHECK (status IN ('pending', 'dispatching', 'running', 'satisfied', 'failed', 'superseded')),
    schedule_run_id      INTEGER,
    attempts             INTEGER NOT NULL DEFAULT 0,
    last_error           TEXT NOT NULL DEFAULT '',
    next_attempt_at      DATETIME,
    created_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at          DATETIME,
    UNIQUE (schedule_id, deployment_id, producer_fingerprint)
);

CREATE INDEX idx_schedule_runs_digest
    ON schedule_runs(schedule_id, content_digest, status);

CREATE INDEX idx_schedule_deploy_obligations_pending
    ON schedule_deploy_obligations(status, next_attempt_at, schedule_id, deployment_id);
