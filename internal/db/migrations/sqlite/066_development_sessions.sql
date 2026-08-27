-- Remote development attempts remain ordinary deployment rows, while this
-- session record lets the UI group a save-heavy working session without
-- sacrificing auditability or rollback history.
CREATE TABLE development_sessions (
    id TEXT PRIMARY KEY,
    app_id INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    target_kind TEXT NOT NULL CHECK (target_kind IN ('existing', 'created', 'ephemeral')),
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'ended')),
    user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
    actor TEXT NOT NULL DEFAULT '',
    credential_id INTEGER REFERENCES api_keys(id) ON DELETE SET NULL,
    credential_type TEXT NOT NULL DEFAULT '',
    credential_name TEXT NOT NULL DEFAULT '',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at DATETIME,
    expires_at DATETIME
);

CREATE INDEX idx_development_sessions_app_created
    ON development_sessions(app_id, created_at DESC);

ALTER TABLE deployments ADD COLUMN development_session_id TEXT
    REFERENCES development_sessions(id) ON DELETE SET NULL;

CREATE INDEX idx_deployments_development_session
    ON deployments(development_session_id, id DESC);

-- Ephemeral ownership is separate from apps so normal app reads and the
-- long-lived app schema remain untouched. Deleting either the app or session
-- removes this marker automatically.
CREATE TABLE ephemeral_apps (
    app_id INTEGER PRIMARY KEY REFERENCES apps(id) ON DELETE CASCADE,
    development_session_id TEXT NOT NULL REFERENCES development_sessions(id) ON DELETE CASCADE,
    expires_at DATETIME NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_ephemeral_apps_expires_at ON ephemeral_apps(expires_at);
