-- Pre-booted elastic workers kept available for new clients. When the runtime
-- snapshot backend is available, these workers are frozen and memory-reclaimed
-- after readiness so admission can thaw them without a cold start.
ALTER TABLE apps ADD COLUMN worker_warm_spares INTEGER NOT NULL DEFAULT 0
    CHECK (worker_warm_spares >= 0 AND worker_warm_spares <= 1000);
