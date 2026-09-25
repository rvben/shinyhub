-- See sqlite/086_audit_action_index.sql: serves ListAuditEventsFiltered's
-- action-only filter (WHERE action = ? ORDER BY created_at DESC, id DESC).
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_events(action, created_at DESC, id DESC);
