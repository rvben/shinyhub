CREATE TABLE IF NOT EXISTS app_schedules (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id          INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    name            TEXT    NOT NULL,
    cron_expr       TEXT    NOT NULL,
    command_json    TEXT    NOT NULL,
    enabled         INTEGER NOT NULL DEFAULT 1,
    timeout_seconds INTEGER NOT NULL DEFAULT 3600,
    overlap_policy  TEXT    NOT NULL DEFAULT 'skip',
    missed_policy   TEXT    NOT NULL DEFAULT 'skip',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (app_id, name)
);
CREATE INDEX IF NOT EXISTS idx_app_schedules_app ON app_schedules(app_id);

CREATE TABLE IF NOT EXISTS schedule_runs (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    schedule_id           INTEGER NOT NULL REFERENCES app_schedules(id) ON DELETE CASCADE,
    status                TEXT    NOT NULL,
    trigger               TEXT    NOT NULL,
    triggered_by_user_id  INTEGER REFERENCES users(id) ON DELETE SET NULL,
    started_at            DATETIME NOT NULL,
    finished_at           DATETIME,
    exit_code             INTEGER,
    log_path              TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_schedule_runs_schedule ON schedule_runs(schedule_id, started_at DESC);

CREATE TABLE IF NOT EXISTS app_shared_data (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id          INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    source_app_id   INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (app_id, source_app_id),
    CHECK (app_id != source_app_id)
);
CREATE INDEX IF NOT EXISTS idx_app_shared_data_source ON app_shared_data(source_app_id);
