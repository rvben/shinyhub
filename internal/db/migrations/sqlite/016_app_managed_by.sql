-- Fleet ownership marker. NULL means the app is not fleet-managed and is
-- never pruned or drift-patched by `shinyhub fleet apply`. When set, the
-- value is "fleet:<fleet_id>" identifying the owning manifest scope.
ALTER TABLE apps ADD COLUMN managed_by TEXT;
