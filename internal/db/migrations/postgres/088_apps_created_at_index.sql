-- See sqlite/088_apps_created_at_index.sql: serves ListApps and
-- ListAppsVisibleToUser's `ORDER BY created_at DESC LIMIT ? OFFSET ?` (backing
-- GET /api/apps), avoiding a full-table sort per page request.
CREATE INDEX IF NOT EXISTS idx_apps_created_at ON apps (created_at DESC);
