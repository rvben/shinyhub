-- Per-app render cost in seconds. DOUBLE PRECISION (float8), not REAL (float4):
-- REAL would truncate a Go float64 and diverge from the SQLite backend, the same
-- defect 041 corrected for autoscale_target. 0 (the default) disables pacing.
ALTER TABLE apps ADD COLUMN render_seconds DOUBLE PRECISION NOT NULL DEFAULT 0;
