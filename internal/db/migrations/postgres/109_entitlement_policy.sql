ALTER TABLE app_entitlements ADD COLUMN managed_by TEXT NOT NULL DEFAULT 'manual';
ALTER TABLE app_group_entitlements ADD COLUMN managed_by TEXT NOT NULL DEFAULT 'manual';
