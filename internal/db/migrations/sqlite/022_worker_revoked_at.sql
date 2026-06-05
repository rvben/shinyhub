-- Explicit worker revocation. revoked_at is the UTC datetime a worker was
-- administratively revoked (empty when never revoked). A revoked worker's
-- client certificate is rejected by the worker API and excluded from
-- control->worker dials immediately, independent of its short cert TTL. The
-- marker is permanent and orthogonal to up/down liveness so a revoked node can
-- never be promoted back to up by a heartbeat.
ALTER TABLE workers ADD COLUMN revoked_at TEXT NOT NULL DEFAULT '';
