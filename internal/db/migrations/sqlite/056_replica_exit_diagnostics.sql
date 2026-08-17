-- Preserve the most recent unexpected exit of each reusable replica slot.
-- Nullable exit_code and signal cover mutually exclusive normal/signal exits;
-- reason remains useful when a provider cannot expose either.
ALTER TABLE replicas ADD COLUMN exit_code INTEGER;
ALTER TABLE replicas ADD COLUMN exit_signal TEXT NOT NULL DEFAULT '';
ALTER TABLE replicas ADD COLUMN exit_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE replicas ADD COLUMN restart_count INTEGER NOT NULL DEFAULT 0;
