-- Durable deploy state machine. A deploy now records a 'pending' deployment
-- row before any pool swap, promotes it to 'succeeded' only once the new pool
-- is serving, and marks it 'failed' if the attempt aborts. The authoritative
-- "live bundle" pointer is the newest non-pending, non-failed row.
--
-- Pre-013 'pending' rows were never confirmed: the original runner only ever
-- wrote 'succeeded' against an already-live pool, so any 'pending' left in an
-- existing database is a stale artefact. Normalize it to 'failed' so startup
-- recovery never mistakes it for the authoritative deployment.
UPDATE deployments SET status = 'failed' WHERE status = 'pending';
