-- See sqlite/035.
CREATE INDEX IF NOT EXISTS idx_audit_resource_lookup
    ON audit_events(resource_type, resource_id, action, created_at DESC);
