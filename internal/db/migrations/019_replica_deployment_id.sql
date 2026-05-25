-- deployment_id ties a live replica to the exact deployment that produced it,
-- so reconcile can distinguish a current replica from a superseded one even
-- when two deployments share a version string. NULL for rows written before
-- this column existed; backfilled on next deploy.
ALTER TABLE replicas ADD COLUMN deployment_id INTEGER;
