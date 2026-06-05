-- Single-row fenced lease electing the one control-plane instance that may run
-- the singleton background loops, the boot-time reconciles, and all mutating
-- operations. `role` is a constant primary key, so the table holds at most one
-- row. `epoch` is a monotonic fencing token: every successful (re)acquisition
-- bumps it, so a stale holder that resumes after its TTL can neither renew nor
-- release against a newer owner. Timestamps are SQLite TEXT in datetime('now')
-- format and compare lexicographically (== chronologically), matching the
-- workers table convention.
CREATE TABLE IF NOT EXISTS cp_owner (
    role         TEXT PRIMARY KEY,
    instance_id  TEXT,
    epoch        INTEGER NOT NULL DEFAULT 0,
    acquired_at  TEXT NOT NULL DEFAULT '',
    heartbeat_at TEXT NOT NULL DEFAULT '',
    expires_at   TEXT NOT NULL DEFAULT ''
);
