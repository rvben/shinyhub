-- Preserve the originating browser session across the one-time app-origin
-- exchange. Legacy and forward-auth launches retain their existing behavior.
ALTER TABLE app_launch_codes ADD COLUMN auth_time INTEGER NOT NULL DEFAULT 0;
ALTER TABLE app_launch_codes ADD COLUMN session_jti TEXT NOT NULL DEFAULT '';
ALTER TABLE app_launch_codes ADD COLUMN session_epoch INTEGER NOT NULL DEFAULT 0;
