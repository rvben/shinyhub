-- Browser authentication mode travels with a one-use app launch.
-- Old launch rows have unspecified mode (both flags 0); consuming them
-- does not change the app-origin logout marker.
ALTER TABLE app_launch_codes ADD COLUMN forward_auth_family INTEGER NOT NULL DEFAULT 0;
ALTER TABLE app_launch_codes ADD COLUMN forward_auth_suppressed INTEGER NOT NULL DEFAULT 0;
-- Opaque logout capabilities: kind is app or complete; user_ids is a JSON
-- UID scope. parent_hash binds a completion to its app code. next_path is
-- a server-recorded local account-switch return, never a bridge destination.
CREATE TABLE browser_logout_codes (
 code_hash TEXT PRIMARY KEY,
 kind TEXT NOT NULL,
 user_ids TEXT NOT NULL,
 parent_hash TEXT NOT NULL DEFAULT '',
 next_path TEXT NOT NULL DEFAULT '',
 created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_browser_logout_codes_created_at ON browser_logout_codes(created_at);
