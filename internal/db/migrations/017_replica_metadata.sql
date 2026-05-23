-- Durable per-replica metadata for mixed local/remote pools and recovery.
-- endpoint_url is the route URL the proxy uses; provider/tier/worker_id identify
-- the owning runtime and the concrete worker; app_version and desired_state let
-- recovery reconcile live workers against intended state. Existing rows default
-- to empty strings (callers backfill on next deploy) and desired_state 'running'.
ALTER TABLE replicas ADD COLUMN provider      TEXT NOT NULL DEFAULT '';
ALTER TABLE replicas ADD COLUMN tier          TEXT NOT NULL DEFAULT '';
ALTER TABLE replicas ADD COLUMN endpoint_url  TEXT NOT NULL DEFAULT '';
ALTER TABLE replicas ADD COLUMN worker_id     TEXT NOT NULL DEFAULT '';
ALTER TABLE replicas ADD COLUMN app_version   TEXT NOT NULL DEFAULT '';
ALTER TABLE replicas ADD COLUMN desired_state TEXT NOT NULL DEFAULT 'running';
