-- A run's retained shared-output size lives on the run row and every append
-- keeps it in step, so trimming to the retention cap no longer sums every
-- retained chunk. Existing runs take the size of the chunks they still hold.
ALTER TABLE app_log_runs ADD COLUMN bytes_retained INTEGER NOT NULL DEFAULT 0;

UPDATE app_log_runs SET bytes_retained = (
    SELECT COALESCE(SUM(end_offset - start_offset), 0)
    FROM app_log_chunks
    WHERE app_log_chunks.run_id = app_log_runs.run_id
);
