CREATE TABLE IF NOT EXISTS oidc_provider_binding (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 issuer TEXT NOT NULL,
 client_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS oidc_sessions (
 jti TEXT PRIMARY KEY,
 user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 issuer TEXT NOT NULL,
 sid TEXT NOT NULL,
 id_token TEXT NOT NULL,
 expires_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_oidc_sessions_sid ON oidc_sessions (issuer,sid);
CREATE INDEX IF NOT EXISTS idx_oidc_sessions_expiry ON oidc_sessions (expires_at);
CREATE INDEX IF NOT EXISTS idx_oidc_sessions_user ON oidc_sessions (user_id);
