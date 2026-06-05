-- Opt-in, per-app replica autoscaling driven by the proxy's session-saturation
-- signal. All columns default to the "disabled / fall back to runtime defaults"
-- state so existing apps keep their fixed replica count untouched.
--
-- autoscale_enabled      0 = off (default); 1 = the controller may scale this app.
-- autoscale_min_replicas lower bound the controller will never scale below; 0
--                        when disabled. Enforced >= 1 by the API when enabling.
-- autoscale_max_replicas upper bound the controller will never scale above; 0
--                        when disabled. Capped at runtime.max_replicas by the API.
-- autoscale_target       target average active sessions per replica expressed as
--                        a fraction (0,1] of the per-replica session cap; 0 means
--                        fall back to the runtime-wide default target.
ALTER TABLE apps ADD COLUMN autoscale_enabled INTEGER NOT NULL DEFAULT 0
    CHECK (autoscale_enabled IN (0, 1));
ALTER TABLE apps ADD COLUMN autoscale_min_replicas INTEGER NOT NULL DEFAULT 0
    CHECK (autoscale_min_replicas >= 0 AND autoscale_min_replicas <= 1000);
ALTER TABLE apps ADD COLUMN autoscale_max_replicas INTEGER NOT NULL DEFAULT 0
    CHECK (autoscale_max_replicas >= 0 AND autoscale_max_replicas <= 1000);
ALTER TABLE apps ADD COLUMN autoscale_target REAL NOT NULL DEFAULT 0
    CHECK (autoscale_target >= 0 AND autoscale_target <= 1);
