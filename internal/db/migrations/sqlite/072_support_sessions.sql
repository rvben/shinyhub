-- Short-lived, app-scoped administrator support sessions. The raw launch
-- capability is never persisted; only its SHA-256 hash crosses the database.
CREATE TABLE IF NOT EXISTS support_sessions (
    id                    TEXT PRIMARY KEY,
    actor_user_id         INTEGER REFERENCES users(id) ON DELETE SET NULL,
    actor_username        TEXT NOT NULL,
    actor_token_epoch     INTEGER NOT NULL,
    subject_user_id       INTEGER REFERENCES users(id) ON DELETE SET NULL,
    subject_username      TEXT NOT NULL,
    subject_role          TEXT NOT NULL CHECK (subject_role IN ('viewer', 'developer')),
    subject_token_epoch   INTEGER NOT NULL,
    app_id                INTEGER REFERENCES apps(id) ON DELETE SET NULL,
    app_slug              TEXT REFERENCES apps(slug) ON DELETE SET NULL,
    app_slug_snapshot     TEXT NOT NULL,
    reason                TEXT NOT NULL CHECK (length(reason) BETWEEN 8 AND 500),
    launch_code_hash      TEXT NOT NULL UNIQUE,
    launch_consumed_at    DATETIME,
    token_jti             TEXT,
    token_expires_at      DATETIME,
    first_used_at         DATETIME,
    stopped_at            DATETIME,
    stop_reason           TEXT NOT NULL DEFAULT '',
    created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    expires_at            DATETIME NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_support_sessions_actor_active
    ON support_sessions(actor_user_id, expires_at)
    WHERE stopped_at IS NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_support_sessions_one_active_actor
    ON support_sessions(actor_user_id)
    WHERE stopped_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_support_sessions_subject_active
    ON support_sessions(subject_user_id, expires_at)
    WHERE stopped_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_support_sessions_expires_at
    ON support_sessions(expires_at);
