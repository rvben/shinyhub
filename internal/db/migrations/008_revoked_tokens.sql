CREATE TABLE IF NOT EXISTS revoked_tokens (
    jti         TEXT PRIMARY KEY,
    user_id     INTEGER REFERENCES users(id) ON DELETE CASCADE,
    expires_at  INTEGER NOT NULL,
    revoked_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);

CREATE INDEX IF NOT EXISTS idx_revoked_tokens_expires_at ON revoked_tokens(expires_at);
