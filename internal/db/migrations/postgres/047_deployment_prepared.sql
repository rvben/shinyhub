-- See sqlite/047. Stored as INTEGER (not BOOLEAN) so the shared deployment
-- scan works across both backends.
ALTER TABLE deployments ADD COLUMN prepared INTEGER NOT NULL DEFAULT 0;
