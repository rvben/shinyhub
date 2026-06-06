-- Persisted autoscale cooldown (epoch seconds, 0 = never). See sqlite/026.
ALTER TABLE apps ADD COLUMN last_autoscale_at bigint NOT NULL DEFAULT 0;
