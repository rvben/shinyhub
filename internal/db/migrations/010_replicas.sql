ALTER TABLE apps ADD COLUMN replicas INTEGER NOT NULL DEFAULT 1
    CHECK (replicas >= 1 AND replicas <= 32);

CREATE TABLE IF NOT EXISTS replicas (
    app_id     INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    idx        INTEGER NOT NULL,                 -- 'index' is reserved in SQLite
    pid        INTEGER,
    port       INTEGER,
    status     TEXT    NOT NULL,                 -- 'running' | 'crashed' | 'stopped'
    updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
    PRIMARY KEY (app_id, idx)
);

CREATE INDEX IF NOT EXISTS idx_replicas_app ON replicas(app_id);

-- Carry existing per-app PID/port into replica 0 so recovery still works.
INSERT OR IGNORE INTO replicas (app_id, idx, pid, port, status, updated_at)
SELECT id, 0, current_pid, current_port,
       CASE WHEN status = 'running' THEN 'running' ELSE 'stopped' END,
       strftime('%s','now')
FROM apps;

ALTER TABLE apps DROP COLUMN current_pid;
ALTER TABLE apps DROP COLUMN current_port;
