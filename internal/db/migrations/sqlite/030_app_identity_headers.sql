-- Per-app identity-header forwarding override, reconciled from the bundle
-- manifest ([app] identity_headers) on every deploy. NULL = inherit the
-- global auth.identity_headers config; 0/1 = explicit per-app value.
-- Effective = global AND (column != 0). The global false is a hard kill
-- switch a manifest cannot override.
ALTER TABLE apps ADD COLUMN identity_headers INTEGER;
