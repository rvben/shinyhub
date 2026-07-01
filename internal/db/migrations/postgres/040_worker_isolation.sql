-- See sqlite/040.
ALTER TABLE apps ADD COLUMN worker_isolation TEXT NOT NULL DEFAULT 'multiplex';
ALTER TABLE apps ADD COLUMN worker_grouped_size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN worker_max_workers INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN worker_max_session_lifetime_secs INTEGER NOT NULL DEFAULT 0;
