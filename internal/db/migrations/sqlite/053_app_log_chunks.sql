-- Shared, bounded application output for multi-control-plane deployments.
-- Byte offsets remain monotonic when old chunks are pruned so live followers
-- can continue without replaying retained output.
CREATE TABLE IF NOT EXISTS app_log_chunks (
    run_id       TEXT    NOT NULL REFERENCES app_log_runs(run_id) ON DELETE CASCADE,
    chunk_seq    INTEGER NOT NULL,
    start_offset INTEGER NOT NULL,
    end_offset   INTEGER NOT NULL,
    data         BLOB    NOT NULL,
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (run_id, chunk_seq)
);

CREATE INDEX IF NOT EXISTS idx_app_log_chunks_run_offset
    ON app_log_chunks(run_id, end_offset);
