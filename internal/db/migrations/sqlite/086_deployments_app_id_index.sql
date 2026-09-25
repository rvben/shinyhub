-- ListRecentDeployments (and the watcher/recovery/rollback hot paths that only
-- need the newest row or two) filter by app_id and ORDER BY id DESC LIMIT n.
-- idx_deployments_app_created (migration 042) is ordered by
-- (app_id, created_at DESC, id DESC) and does not cover a pure id-ordered
-- scan, so that query built the full per-app result set into a temp B-tree
-- before applying LIMIT. This index covers it directly.
CREATE INDEX IF NOT EXISTS idx_deployments_app_id_desc
    ON deployments (app_id, id DESC);
