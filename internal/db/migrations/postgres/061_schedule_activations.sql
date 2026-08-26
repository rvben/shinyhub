ALTER TABLE apps ADD COLUMN data_generation BIGINT NOT NULL DEFAULT 0;

ALTER TABLE app_schedules ADD COLUMN on_success TEXT NOT NULL DEFAULT 'none'
    CHECK (on_success IN ('none', 'roll'));
ALTER TABLE app_schedules ADD COLUMN min_roll_interval_seconds BIGINT NOT NULL DEFAULT 0
    CHECK (min_roll_interval_seconds >= 0);

-- Snapshot activation policy when a run is admitted. Editing a schedule while
-- its command is running must not retroactively change that run's outcome.
ALTER TABLE schedule_runs ADD COLUMN on_success TEXT NOT NULL DEFAULT 'none'
    CHECK (on_success IN ('none', 'roll'));
ALTER TABLE schedule_runs ADD COLUMN min_roll_interval_seconds BIGINT NOT NULL DEFAULT 0
    CHECK (min_roll_interval_seconds >= 0);
ALTER TABLE schedule_runs ADD COLUMN target_generation BIGINT;

ALTER TABLE replicas ADD COLUMN data_generation BIGINT NOT NULL DEFAULT 0;
ALTER TABLE replicas ADD COLUMN activation_id BIGINT;

CREATE TABLE schedule_activations (
    id                 BIGSERIAL PRIMARY KEY,
    app_id             BIGINT REFERENCES apps(id) ON DELETE SET NULL,
    app_slug           TEXT NOT NULL,
    -- Snapshot identifiers deliberately are not foreign keys. Terminal
    -- activation attribution must survive deletion of its source schedule/run.
    schedule_id        BIGINT,
    schedule_name      TEXT NOT NULL,
    schedule_run_id    BIGINT UNIQUE,
    action             TEXT NOT NULL CHECK (action IN ('roll')),
    min_roll_interval_seconds BIGINT NOT NULL DEFAULT 0 CHECK (min_roll_interval_seconds >= 0),
    target_generation  BIGINT NOT NULL CHECK (target_generation > 0),
    status             TEXT NOT NULL CHECK (status IN (
        'pending', 'deferred_interval', 'deferred_capacity', 'repairing', 'running',
        'succeeded', 'failed', 'superseded', 'not_needed',
        'blocked_unsupported', 'target_deleted'
    )),
    phase              TEXT NOT NULL DEFAULT 'pending',
    due_at             TIMESTAMPTZ NOT NULL,
    defer_reason       TEXT NOT NULL DEFAULT '',
    attempts           BIGINT NOT NULL DEFAULT 0,
    surge_index        BIGINT NOT NULL DEFAULT -1,
    next_slot          BIGINT NOT NULL DEFAULT 0,
    last_error         TEXT NOT NULL DEFAULT '',
    superseded_by_id   BIGINT REFERENCES schedule_activations(id) ON DELETE SET NULL,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at         TIMESTAMPTZ,
    finished_at        TIMESTAMPTZ
);

CREATE INDEX idx_schedule_activations_queue
    ON schedule_activations(status, due_at, id);
CREATE INDEX idx_schedule_activations_app
    ON schedule_activations(app_id, status, id);
CREATE INDEX idx_schedule_activations_run
    ON schedule_activations(schedule_run_id);
