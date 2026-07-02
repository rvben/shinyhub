-- deployments has no index on app_id, yet deploymentSummarySQL runs four
-- correlated subqueries keyed on (app_id [, status], created_at DESC, id DESC)
-- embedded in all seven core App SELECTs. Those are read on the hot path by the
-- watchdog (twice per 15s tick), the autoscaler (every 30s), and the dashboard
-- metrics poll. Without this index each subquery is a full scan of deployments,
-- worsening as deploy history accumulates. This composite index covers the
-- MAX(created_at), latest-version, latest-status, and latest-succeeded-digest
-- lookups.
CREATE INDEX IF NOT EXISTS idx_deployments_app_created
    ON deployments (app_id, created_at DESC, id DESC);
