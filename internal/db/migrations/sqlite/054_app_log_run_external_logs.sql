-- Provider-owned log metadata belongs to the immutable execution, not the
-- reusable replica slot. The JSON envelope keeps the handoff extensible across
-- external runtimes while an empty string preserves old-run compatibility.
ALTER TABLE app_log_runs
    ADD COLUMN external_logs TEXT NOT NULL DEFAULT '';
