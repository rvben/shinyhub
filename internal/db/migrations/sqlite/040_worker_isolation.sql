-- Session-isolation dial. worker_isolation is the mode: 'multiplex' (default,
-- today's shared event loop), 'grouped' (up to worker_grouped_size clients per
-- worker), or 'per_session' (one client per worker). worker_max_workers is the
-- demand-driven host-capacity ceiling (0 = unset, required for non-multiplex).
-- worker_max_session_lifetime_secs is an absolute backstop that terminates a
-- worker after N seconds (0 = unlimited). warm_spares is deliberately absent:
-- it is a Phase 2 field.
ALTER TABLE apps ADD COLUMN worker_isolation TEXT NOT NULL DEFAULT 'multiplex';
ALTER TABLE apps ADD COLUMN worker_grouped_size INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN worker_max_workers INTEGER NOT NULL DEFAULT 0;
ALTER TABLE apps ADD COLUMN worker_max_session_lifetime_secs INTEGER NOT NULL DEFAULT 0;
