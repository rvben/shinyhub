-- See sqlite/052.
ALTER TABLE apps ADD COLUMN worker_warm_spares INTEGER NOT NULL DEFAULT 0
    CHECK (worker_warm_spares >= 0 AND worker_warm_spares <= 1000);
