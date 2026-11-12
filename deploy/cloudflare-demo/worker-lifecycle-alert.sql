-- Container lifecycle alarm failures. Keep visible independently of request errors.
-- A deployment resets the Durable Object under its running alarm; only that one
-- exception is excluded, by its exact message. Alarm invocation summaries are
-- excluded because they repeat a failure whose exception or log record carries
-- the cause, and a summary alone cannot be told apart from a deployment reset.
-- An alarm has no HTTP status, so the error test reads only level and error;
-- the monitor's query parser also rejects a deeper condition than this one.
SELECT COUNT(*) AS value
FROM logs.workersLogs
WHERE accountTag = 'd39e08a123830e48534a95a0116442dd'
  AND scriptName = 'shinyhub-demo'
  AND (level IN ('error', 'fatal') OR error != '')
  AND (logType IS NULL OR logType != 'cf-worker-event')
  AND (error IS NULL OR error != 'Durable Object reset because its code was updated.')
  AND invocationType = 'alarm'
