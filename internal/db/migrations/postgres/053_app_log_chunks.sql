-- Shared, bounded application output for multi-control-plane deployments.
-- Byte offsets remain monotonic when old chunks are pruned so live followers
-- can continue without replaying retained output.
CREATE TABLE IF NOT EXISTS app_log_chunks (
    run_id       text   NOT NULL REFERENCES app_log_runs(run_id) ON DELETE CASCADE,
    chunk_seq    bigint NOT NULL,
    start_offset bigint NOT NULL,
    end_offset   bigint NOT NULL,
    data         bytea  NOT NULL,
    created_at   bigint NOT NULL,
    PRIMARY KEY (run_id, chunk_seq)
);

CREATE INDEX IF NOT EXISTS idx_app_log_chunks_run_offset
    ON app_log_chunks(run_id, end_offset);
