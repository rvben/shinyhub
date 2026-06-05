-- Consolidated baseline: equals applying migrations/sqlite/001..024 on a fresh DB.
-- Column types follow the SQLite scan/bind contract (single-source query methods).
-- datetime columns scanned into time.Time use timestamptz; columns scanned into
-- string or int64 (epoch) use text/bigint respectively to match the Go binding layer.

CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'developer',
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE api_keys (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    key_hash   TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE apps (
    id                       BIGSERIAL PRIMARY KEY,
    slug                     TEXT NOT NULL UNIQUE,
    name                     TEXT NOT NULL,
    project_slug             TEXT NOT NULL DEFAULT 'default',
    owner_id                 BIGINT NOT NULL REFERENCES users(id),
    access                   TEXT NOT NULL DEFAULT 'private',
    status                   TEXT NOT NULL DEFAULT 'stopped',
    deploy_count             BIGINT NOT NULL DEFAULT 0,
    hibernate_timeout_minutes BIGINT,
    memory_limit_mb          BIGINT,
    cpu_quota_percent        BIGINT,
    replicas                 BIGINT NOT NULL DEFAULT 1
        CHECK (replicas >= 1 AND replicas <= 32),
    max_sessions_per_replica BIGINT NOT NULL DEFAULT 0
        CHECK (max_sessions_per_replica >= 0 AND max_sessions_per_replica <= 1000),
    managed_by               TEXT,
    replica_placement        TEXT NOT NULL DEFAULT '',
    autoscale_enabled        integer NOT NULL DEFAULT 0
        CHECK (autoscale_enabled IN (0, 1)),
    autoscale_min_replicas   BIGINT NOT NULL DEFAULT 0
        CHECK (autoscale_min_replicas >= 0 AND autoscale_min_replicas <= 1000),
    autoscale_max_replicas   BIGINT NOT NULL DEFAULT 0
        CHECK (autoscale_max_replicas >= 0 AND autoscale_max_replicas <= 1000),
    autoscale_target         REAL NOT NULL DEFAULT 0
        CHECK (autoscale_target >= 0 AND autoscale_target <= 1),
    created_at               timestamptz NOT NULL DEFAULT now(),
    updated_at               timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE deployments (
    id             BIGSERIAL PRIMARY KEY,
    app_id         BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    version        TEXT NOT NULL,
    bundle_dir     TEXT NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending',
    content_digest TEXT,
    created_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE app_members (
    app_slug TEXT NOT NULL REFERENCES apps(slug) ON DELETE CASCADE,
    user_id  BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role     TEXT NOT NULL DEFAULT 'viewer',
    PRIMARY KEY (app_slug, user_id)
);

CREATE TABLE oauth_accounts (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    provider    TEXT NOT NULL,
    provider_id TEXT NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider, provider_id)
);

CREATE TABLE oauth_states (
    state      TEXT PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_events (
    id            BIGSERIAL PRIMARY KEY,
    user_id       BIGINT REFERENCES users(id) ON DELETE SET NULL,
    action        TEXT NOT NULL,
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id   TEXT NOT NULL DEFAULT '',
    detail        TEXT NOT NULL DEFAULT '',
    ip_address    TEXT NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_created_at ON audit_events(created_at DESC);
CREATE INDEX idx_audit_user_id    ON audit_events(user_id);

CREATE TABLE revoked_tokens (
    jti        TEXT PRIMARY KEY,
    user_id    BIGINT REFERENCES users(id) ON DELETE CASCADE,
    expires_at bigint NOT NULL,
    revoked_at bigint NOT NULL DEFAULT (EXTRACT(EPOCH FROM now())::bigint)
);

CREATE INDEX idx_revoked_tokens_expires_at ON revoked_tokens(expires_at);

CREATE TABLE app_env_vars (
    id         BIGSERIAL PRIMARY KEY,
    app_id     BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    key        TEXT   NOT NULL,
    value      BYTEA  NOT NULL,
    is_secret  integer NOT NULL DEFAULT 0,
    created_at bigint NOT NULL DEFAULT (EXTRACT(EPOCH FROM now())::bigint),
    updated_at bigint NOT NULL DEFAULT (EXTRACT(EPOCH FROM now())::bigint),
    UNIQUE (app_id, key)
);

CREATE INDEX idx_app_env_vars_app ON app_env_vars(app_id);

CREATE TABLE replicas (
    app_id        BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    idx           BIGINT NOT NULL,
    pid           BIGINT,
    port          BIGINT,
    status        TEXT   NOT NULL,
    provider      TEXT   NOT NULL DEFAULT '',
    tier          TEXT   NOT NULL DEFAULT '',
    endpoint_url  TEXT   NOT NULL DEFAULT '',
    worker_id     TEXT   NOT NULL DEFAULT '',
    app_version   TEXT   NOT NULL DEFAULT '',
    desired_state TEXT   NOT NULL DEFAULT 'running',
    deployment_id BIGINT,
    updated_at    bigint NOT NULL DEFAULT (EXTRACT(EPOCH FROM now())::bigint),
    PRIMARY KEY (app_id, idx)
);

CREATE INDEX idx_replicas_app    ON replicas(app_id);
CREATE INDEX idx_replicas_status ON replicas(status);

CREATE TABLE app_schedules (
    id              BIGSERIAL PRIMARY KEY,
    app_id          BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    name            TEXT   NOT NULL,
    cron_expr       TEXT   NOT NULL,
    command_json    TEXT   NOT NULL,
    enabled         integer NOT NULL DEFAULT 1,
    timeout_seconds BIGINT  NOT NULL DEFAULT 3600,
    overlap_policy  TEXT    NOT NULL DEFAULT 'skip',
    missed_policy   TEXT    NOT NULL DEFAULT 'skip',
    timezone        TEXT,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (app_id, name)
);

CREATE INDEX idx_app_schedules_app ON app_schedules(app_id);

CREATE TABLE schedule_runs (
    id                   BIGSERIAL PRIMARY KEY,
    schedule_id          BIGINT NOT NULL REFERENCES app_schedules(id) ON DELETE CASCADE,
    status               TEXT   NOT NULL,
    trigger              TEXT   NOT NULL,
    triggered_by_user_id BIGINT REFERENCES users(id) ON DELETE SET NULL,
    started_at           timestamptz NOT NULL,
    finished_at          timestamptz,
    exit_code            BIGINT,
    log_path             TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_schedule_runs_schedule ON schedule_runs(schedule_id, started_at DESC);

CREATE TABLE app_shared_data (
    id            BIGSERIAL PRIMARY KEY,
    app_id        BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    source_app_id BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (app_id, source_app_id),
    CHECK (app_id != source_app_id)
);

CREATE INDEX idx_app_shared_data_source ON app_shared_data(source_app_id);

CREATE TABLE workers (
    node_id          TEXT PRIMARY KEY,
    name             TEXT NOT NULL DEFAULT '',
    advertise_addr   TEXT NOT NULL,
    tier             TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'up',
    cert_fingerprint TEXT NOT NULL DEFAULT '',
    version          TEXT NOT NULL DEFAULT '',
    last_heartbeat   TEXT NOT NULL DEFAULT '',
    revoked_at       TEXT NOT NULL DEFAULT '',
    created_at       TEXT NOT NULL DEFAULT (to_char(now() AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS'))
);

CREATE TABLE cp_owner (
    role         TEXT PRIMARY KEY,
    instance_id  TEXT,
    epoch        BIGINT NOT NULL DEFAULT 0,
    acquired_at  timestamptz NOT NULL DEFAULT now(),
    heartbeat_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL DEFAULT now()
);
