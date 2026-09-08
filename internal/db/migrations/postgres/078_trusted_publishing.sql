CREATE TABLE IF NOT EXISTS trusted_publishing_assertions (
    assertion_id TEXT PRIMARY KEY,
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_trusted_assertions_expiry ON trusted_publishing_assertions(expires_at);
