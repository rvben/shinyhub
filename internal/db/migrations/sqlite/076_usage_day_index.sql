-- Stream daily report aggregates in day order instead of sorting retained rows.
-- Only immutable session fields are indexed, so heartbeats and session closure
-- do not maintain an additional index entry.
CREATE INDEX IF NOT EXISTS idx_usage_sessions_app_day_started
    ON usage_sessions(app_id, substr(CAST(started_at AS TEXT), 1, 10), started_at);
