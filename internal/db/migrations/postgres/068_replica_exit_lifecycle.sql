-- See the SQLite counterpart for lifecycle and execution-history rationale.
ALTER TABLE replicas ADD COLUMN exit_observed_at BIGINT;
ALTER TABLE replicas ADD COLUMN exit_oom_killed INTEGER NOT NULL DEFAULT 0 CHECK (exit_oom_killed IN (0, 1));
ALTER TABLE replicas ADD COLUMN exit_run_id TEXT NOT NULL DEFAULT '';

ALTER TABLE app_log_runs ADD COLUMN exit_code BIGINT;
ALTER TABLE app_log_runs ADD COLUMN exit_signal TEXT NOT NULL DEFAULT '';
ALTER TABLE app_log_runs ADD COLUMN exit_reason TEXT NOT NULL DEFAULT '';
