-- One-time, short-lived exchanges used to establish an app-origin session
-- without sharing the control-plane session cookie with proxied apps.
CREATE TABLE IF NOT EXISTS app_launch_codes (
    code_hash  TEXT PRIMARY KEY,
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    app_slug   TEXT NOT NULL REFERENCES apps(slug) ON DELETE CASCADE,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_app_launch_codes_created_at ON app_launch_codes(created_at);
