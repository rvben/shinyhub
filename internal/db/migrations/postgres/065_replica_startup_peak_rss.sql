ALTER TABLE replicas ADD COLUMN startup_peak_rss_bytes BIGINT NOT NULL DEFAULT 0
    CHECK (startup_peak_rss_bytes >= 0);
