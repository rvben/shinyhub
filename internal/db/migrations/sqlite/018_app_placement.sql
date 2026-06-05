-- Per-app replica placement as a JSON object {"tier": count}, e.g.
-- {"local":1,"burst":2}. Empty string means "all `replicas` on the default
-- tier", which reproduces single-tier behavior. The `replicas` column remains
-- the authoritative total (kept equal to the sum of placement counts when
-- placement is set) so existing pool-size and recovery reads are unchanged.
ALTER TABLE apps ADD COLUMN replica_placement TEXT NOT NULL DEFAULT '';
