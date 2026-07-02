-- Widen autoscale_target from REAL (float4, single precision) to DOUBLE
-- PRECISION (float8) so it matches the Go float64 field and the SQLite backend.
-- As REAL, a target of 0.8 was truncated to single precision and read back as
-- 0.800000011920929, silently perturbing the autoscale threshold on Postgres and
-- diverging from SQLite. See sqlite/041 (no-op: SQLite REAL is already 8-byte).
ALTER TABLE apps ALTER COLUMN autoscale_target TYPE DOUBLE PRECISION;
