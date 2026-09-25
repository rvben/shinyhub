-- The audit log page's action-only filter (ListAuditEventsFiltered with just
-- Action set) runs `WHERE ae.action = ? ORDER BY ae.created_at DESC, ae.id
-- DESC`. None of the existing audit indexes lead with action (idx_audit_created_at
-- is created_at-only, idx_audit_resource_lookup leads with resource_type), so
-- this filter falls back to a full table scan plus a temp B-tree sort as the
-- table grows. This index serves both the filter and the ordering directly.
CREATE INDEX IF NOT EXISTS idx_audit_action ON audit_events(action, created_at DESC, id DESC);
