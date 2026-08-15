-- See the SQLite counterpart for the immutable-run rationale.
ALTER TABLE app_log_runs
    ADD COLUMN IF NOT EXISTS external_logs text NOT NULL DEFAULT '';
