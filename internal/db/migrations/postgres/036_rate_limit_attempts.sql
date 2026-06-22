-- Shared rate-limit counters for the database-backed login limiter, so a
-- load-balanced deployment enforces one global limit per client IP instead of a
-- per-instance limit. One row per (bucket, key, fixed-window) holds the attempt
-- count for that window. The check-and-increment is a single atomic UPSERT
-- (ON CONFLICT DO UPDATE), whose row lock serializes concurrent attempts on the
-- same key so a parallel burst cannot exceed the cap. window_start_ms is a
-- unix-millis window boundary, an integer so the math is portable across
-- SQLite and Postgres with no dialect timestamp handling.
CREATE TABLE IF NOT EXISTS rate_limit_counters (
    bucket          TEXT    NOT NULL,
    rl_key          TEXT    NOT NULL,
    window_start_ms BIGINT  NOT NULL,
    count           INTEGER NOT NULL,
    PRIMARY KEY (bucket, rl_key, window_start_ms)
);
