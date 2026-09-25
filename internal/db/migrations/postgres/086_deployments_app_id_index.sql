-- See sqlite/085. Covers the app_id + ORDER BY id DESC LIMIT n shape used by
-- ListRecentDeployments and the watcher/recovery/rollback hot paths.
CREATE INDEX IF NOT EXISTS idx_deployments_app_id_desc
    ON deployments (app_id, id DESC);
