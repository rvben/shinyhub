-- Keep the last unexpected exit as a timestamped execution fact instead of
-- making callers infer its age from replicas.updated_at (which describes the
-- current slot state and changes again on a successful restart).
ALTER TABLE replicas ADD COLUMN exit_observed_at INTEGER;
ALTER TABLE replicas ADD COLUMN exit_oom_killed INTEGER NOT NULL DEFAULT 0 CHECK (exit_oom_killed IN (0, 1));
ALTER TABLE replicas ADD COLUMN exit_run_id TEXT NOT NULL DEFAULT '';

-- app_log_runs is the immutable execution history. Store the complete terminal
-- verdict beside the already-persisted OOM flag so an old run remains
-- diagnosable after its replica slot has been reused.
ALTER TABLE app_log_runs ADD COLUMN exit_code INTEGER;
ALTER TABLE app_log_runs ADD COLUMN exit_signal TEXT NOT NULL DEFAULT '';
ALTER TABLE app_log_runs ADD COLUMN exit_reason TEXT NOT NULL DEFAULT '';
