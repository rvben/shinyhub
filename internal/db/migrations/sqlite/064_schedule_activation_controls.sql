ALTER TABLE app_schedules ADD COLUMN roll_fallback TEXT NOT NULL DEFAULT 'defer'
    CHECK (roll_fallback IN ('defer', 'restart'));
ALTER TABLE app_schedules ADD COLUMN max_defer_age_seconds INTEGER NOT NULL DEFAULT 0
    CHECK (max_defer_age_seconds >= 0);

ALTER TABLE schedule_runs ADD COLUMN roll_fallback TEXT NOT NULL DEFAULT 'defer'
    CHECK (roll_fallback IN ('defer', 'restart'));
ALTER TABLE schedule_runs ADD COLUMN max_defer_age_seconds INTEGER NOT NULL DEFAULT 0
    CHECK (max_defer_age_seconds >= 0);

-- SQLite cannot extend a CHECK constraint in place. Rebuild the activation
-- table once so cancellation is a real terminal state rather than a failed
-- activation carrying a magic error string. The rebuild also adds the recovery
-- policy snapshot columns introduced by this migration.
ALTER TABLE schedule_activations RENAME TO schedule_activations_v63;

CREATE TABLE schedule_activations (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id             INTEGER REFERENCES apps(id) ON DELETE SET NULL,
    app_slug           TEXT NOT NULL,
    schedule_id        INTEGER,
    schedule_name      TEXT NOT NULL,
    schedule_run_id    INTEGER UNIQUE,
    action             TEXT NOT NULL CHECK (action IN ('roll')),
    min_roll_interval_seconds INTEGER NOT NULL DEFAULT 0 CHECK (min_roll_interval_seconds >= 0),
    roll_fallback      TEXT NOT NULL DEFAULT 'defer' CHECK (roll_fallback IN ('defer', 'restart')),
    max_defer_age_seconds INTEGER NOT NULL DEFAULT 0 CHECK (max_defer_age_seconds >= 0),
    target_generation  INTEGER NOT NULL CHECK (target_generation > 0),
    status             TEXT NOT NULL CHECK (status IN (
        'pending', 'deferred_interval', 'deferred_capacity', 'repairing', 'running',
        'succeeded', 'failed', 'cancelled', 'superseded', 'not_needed',
        'blocked_unsupported', 'target_deleted'
    )),
    phase              TEXT NOT NULL DEFAULT 'pending',
    due_at             DATETIME NOT NULL,
    defer_reason       TEXT NOT NULL DEFAULT '',
    attempts           INTEGER NOT NULL DEFAULT 0,
    capacity_deferrals INTEGER NOT NULL DEFAULT 0 CHECK (capacity_deferrals >= 0),
    capacity_deferred_at DATETIME,
    surge_index        INTEGER NOT NULL DEFAULT -1,
    next_slot          INTEGER NOT NULL DEFAULT 0,
    last_error         TEXT NOT NULL DEFAULT '',
    superseded_by_id   INTEGER REFERENCES schedule_activations(id) ON DELETE SET NULL,
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    started_at         DATETIME,
    finished_at        DATETIME
);

INSERT INTO schedule_activations (
    id, app_id, app_slug, schedule_id, schedule_name, schedule_run_id, action,
    min_roll_interval_seconds, target_generation, status, phase, due_at,
    defer_reason, attempts, surge_index, next_slot, last_error,
    superseded_by_id, created_at, updated_at, started_at, finished_at
)
SELECT
    id, app_id, app_slug, schedule_id, schedule_name, schedule_run_id, action,
    min_roll_interval_seconds, target_generation, status, phase, due_at,
    defer_reason, attempts, surge_index, next_slot, last_error,
    superseded_by_id, created_at, updated_at, started_at, finished_at
FROM schedule_activations_v63;

DROP TABLE schedule_activations_v63;

CREATE INDEX idx_schedule_activations_queue
    ON schedule_activations(status, due_at, id);
CREATE INDEX idx_schedule_activations_app
    ON schedule_activations(app_id, status, id);
CREATE INDEX idx_schedule_activations_run
    ON schedule_activations(schedule_run_id);
