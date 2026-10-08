-- Deploy demo_upstream_failed logging before applying this alert query.
-- Invocation summaries cannot distinguish intentional 503s from upstream 503s.
-- Unexpected upstream failures emit separate error logs, which remain included.
SELECT COUNT(*) AS value
FROM logs.workersLogs
WHERE accountTag = 'd39e08a123830e48534a95a0116442dd'
  AND scriptName = 'shinyhub-demo'
  AND (level IN ('error', 'fatal') OR (error IS NOT NULL AND error != '') OR httpStatus >= 500)
  AND (httpStatus IS NULL OR httpStatus != 503 OR logType IS NULL OR logType != 'cf-worker-event')
