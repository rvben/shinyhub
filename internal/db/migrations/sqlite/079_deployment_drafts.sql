CREATE TABLE IF NOT EXISTS deployment_drafts (
    id TEXT PRIMARY KEY,
    app_id BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    preview_app_id BIGINT UNIQUE REFERENCES apps(id) ON DELETE SET NULL,
    content_digest TEXT NOT NULL,
    base_revision TEXT NOT NULL,
    created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    expires_at BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    promoted_at BIGINT
);
CREATE INDEX IF NOT EXISTS idx_deployment_drafts_app ON deployment_drafts(app_id, created_at);
