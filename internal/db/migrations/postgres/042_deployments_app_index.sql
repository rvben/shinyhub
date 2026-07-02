-- See sqlite/042. Indexes the correlated deploymentSummarySQL subqueries so the
-- watchdog/autoscaler/dashboard hot paths stop full-scanning deployments.
CREATE INDEX IF NOT EXISTS idx_deployments_app_created
    ON deployments (app_id, created_at DESC, id DESC);
