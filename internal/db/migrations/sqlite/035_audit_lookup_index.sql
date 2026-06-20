-- Composite index for per-resource audit lookups (e.g. LatestAutoscaleEvent,
-- which filters resource_type + resource_id + action and orders by created_at
-- DESC). As audit_events grows large the single-column created_at index forces
-- a backward scan plus row filter; this index serves the lookup directly.
CREATE INDEX IF NOT EXISTS idx_audit_resource_lookup
    ON audit_events(resource_type, resource_id, action, created_at DESC);
