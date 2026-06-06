-- Persisted autoscale cooldown: Unix-epoch seconds of the last scale action the
-- controller took on this app, 0 = never scaled. Replaces the controller's
-- in-memory cooldown map so the cooldown survives process restart and failover
-- to a standby control-plane instance.
ALTER TABLE apps ADD COLUMN last_autoscale_at INTEGER NOT NULL DEFAULT 0;
