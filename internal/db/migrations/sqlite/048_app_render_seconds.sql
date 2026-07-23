-- Per-app render cost in seconds, the CPU a first render spends. 0 (the
-- default) disables render-aware pacing for the app, so this column is a strict
-- no-op until an operator sets it and the charge point is wired. SQLite stores
-- every REAL as an 8-byte IEEE double, so a Go float64 round-trips without loss.
ALTER TABLE apps ADD COLUMN render_seconds REAL NOT NULL DEFAULT 0;
