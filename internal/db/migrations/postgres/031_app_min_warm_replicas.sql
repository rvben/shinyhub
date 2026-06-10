-- See sqlite/031.
ALTER TABLE apps ADD COLUMN min_warm_replicas INTEGER NOT NULL DEFAULT 0;
