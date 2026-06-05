CREATE TABLE IF NOT EXISTS app_env_vars (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id     INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    key        TEXT    NOT NULL,
    value      BLOB    NOT NULL,
    is_secret  INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    UNIQUE (app_id, key)
);

CREATE INDEX IF NOT EXISTS idx_app_env_vars_app ON app_env_vars(app_id);
