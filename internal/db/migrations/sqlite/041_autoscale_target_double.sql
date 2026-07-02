-- No-op: SQLite stores every REAL column as an 8-byte IEEE double regardless of
-- the declared type, so autoscale_target already round-trips a Go float64 without
-- loss. This file exists only to keep the post-baseline migration numbering in
-- lockstep with postgres/041 (which widens REAL -> DOUBLE PRECISION). SQLite has
-- no ALTER COLUMN TYPE, and none is needed. The SELECT keeps the statement valid.
SELECT 1;
