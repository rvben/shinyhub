-- ListApps and ListAppsVisibleToUser (backing GET /api/apps, the dashboard's
-- paginated app list) run `ORDER BY created_at DESC LIMIT ? OFFSET ?`. With no
-- index on apps.created_at, both queries build the whole table into a temp
-- B-tree to satisfy the ORDER BY before applying LIMIT/OFFSET, so a page
-- request's cost scales with the total app count rather than the page size.
-- This index lets the planner walk apps in created_at order directly.
CREATE INDEX IF NOT EXISTS idx_apps_created_at ON apps (created_at DESC);
