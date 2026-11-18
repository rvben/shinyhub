-- Deploy demo_upstream_failed logging before applying this alert query.
-- Invocation summaries cannot distinguish intentional 503s from upstream 503s.
-- Unexpected upstream failures emit separate error logs, which remain included.
-- The container SDK can log a rollout exit even when the request succeeds.
-- Exclude that exact log, while retaining any record with an explicit 5xx.
-- Cloudflare adaptive alert datasets do not support DISTINCT aggregates.
-- This value counts error records, not failed requests or affected visitors.
-- Unknown invocation types stay in this alert so schema gaps cannot hide errors.
-- Install worker-lifecycle-alert.sql as a second alert before applying this split.
SELECT COUNT(*) AS value
FROM logs.workersLogs
WHERE accountTag = 'd39e08a123830e48534a95a0116442dd'
  AND scriptName = 'shinyhub-demo'
  AND (level IN ('error', 'fatal') OR (error IS NOT NULL AND error != '') OR httpStatus >= 500)
  AND (httpStatus IS NULL OR httpStatus != 503 OR logType IS NULL OR logType != 'cf-worker-event')
  AND (invocationType IS NULL OR invocationType != 'alarm')
  AND (error IS NULL OR error != 'Container error: Error: Runtime signalled the container to exit due to a new version rollout: 143' OR httpStatus >= 500)
