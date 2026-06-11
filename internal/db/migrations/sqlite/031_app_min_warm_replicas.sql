-- Pre-warming floor: at idle timeout, keep this many replicas running
-- instead of hibernating all of them. 0 = full hibernation (default).
-- Scale-down (manual or autoscale) also never goes below this floor.
ALTER TABLE apps ADD COLUMN min_warm_replicas INTEGER NOT NULL DEFAULT 0;
