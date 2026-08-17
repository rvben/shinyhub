-- Preserve the most recent unexpected exit of each reusable replica slot.
ALTER TABLE replicas ADD COLUMN exit_code BIGINT;
ALTER TABLE replicas ADD COLUMN exit_signal TEXT NOT NULL DEFAULT '';
ALTER TABLE replicas ADD COLUMN exit_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE replicas ADD COLUMN restart_count BIGINT NOT NULL DEFAULT 0;
