-- Durable usage is separate from the mutation audit trail. Identity policy is
-- enforced in stored columns, not merely filtered when a report is read.
ALTER TABLE apps ADD COLUMN usage_identity_mode TEXT
    CHECK (usage_identity_mode IS NULL OR usage_identity_mode IN
        ('disabled', 'unattributed', 'pseudonymous', 'identified'));

CREATE TABLE usage_policy (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    identity_mode TEXT NOT NULL CHECK (identity_mode IN
        ('unattributed', 'pseudonymous', 'identified')),
    generation BIGINT NOT NULL DEFAULT 1 CHECK (generation > 0),
    pseudonym_key_enc BYTEA,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE usage_sessions (
    id TEXT PRIMARY KEY,
    app_id BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    deployment_id BIGINT REFERENCES deployments(id) ON DELETE SET NULL,
    user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    viewer_key TEXT,
    principal_kind TEXT NOT NULL CHECK (principal_kind IN ('anonymous', 'person', 'service_account')),
    identity_mode TEXT NOT NULL CHECK (identity_mode IN
        ('unattributed', 'pseudonymous', 'identified')),
    policy_generation BIGINT NOT NULL DEFAULT 1,
    instance_id TEXT NOT NULL,
    started_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP,
    ended_at TIMESTAMPTZ,
    CHECK (identity_mode = 'identified' OR user_id IS NULL),
    CHECK (identity_mode = 'pseudonymous' OR viewer_key IS NULL),
    CHECK (principal_kind = 'person' OR (user_id IS NULL AND viewer_key IS NULL))
);

CREATE TABLE usage_daily (
    app_id BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    day DATE NOT NULL,
    sessions BIGINT NOT NULL DEFAULT 0 CHECK (sessions >= 0),
    person_sessions BIGINT NOT NULL DEFAULT 0 CHECK (person_sessions >= 0),
    anonymous_sessions BIGINT NOT NULL DEFAULT 0 CHECK (anonymous_sessions >= 0),
    service_sessions BIGINT NOT NULL DEFAULT 0 CHECK (service_sessions >= 0),
    peak_concurrent_sessions BIGINT NOT NULL DEFAULT 0 CHECK (peak_concurrent_sessions >= 0),
    peak_finalized BOOLEAN NOT NULL DEFAULT FALSE,
    total_duration_seconds BIGINT NOT NULL DEFAULT 0 CHECK (total_duration_seconds >= 0),
    last_opened_at TIMESTAMPTZ,
    PRIMARY KEY (app_id, day)
);

CREATE INDEX idx_usage_sessions_app_started ON usage_sessions(app_id, started_at DESC, id DESC);
CREATE INDEX idx_usage_sessions_app_user_started ON usage_sessions(app_id, user_id, started_at DESC);
CREATE INDEX idx_usage_sessions_app_viewer_started ON usage_sessions(app_id, viewer_key, started_at DESC);
CREATE INDEX idx_usage_sessions_open_heartbeat ON usage_sessions(heartbeat_at) WHERE ended_at IS NULL;
