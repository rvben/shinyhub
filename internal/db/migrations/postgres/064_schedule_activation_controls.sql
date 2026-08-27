ALTER TABLE app_schedules ADD COLUMN roll_fallback TEXT NOT NULL DEFAULT 'defer'
    CHECK (roll_fallback IN ('defer', 'restart'));
ALTER TABLE app_schedules ADD COLUMN max_defer_age_seconds BIGINT NOT NULL DEFAULT 0
    CHECK (max_defer_age_seconds >= 0);

ALTER TABLE schedule_runs ADD COLUMN roll_fallback TEXT NOT NULL DEFAULT 'defer'
    CHECK (roll_fallback IN ('defer', 'restart'));
ALTER TABLE schedule_runs ADD COLUMN max_defer_age_seconds BIGINT NOT NULL DEFAULT 0
    CHECK (max_defer_age_seconds >= 0);

ALTER TABLE schedule_activations ADD COLUMN roll_fallback TEXT NOT NULL DEFAULT 'defer'
    CHECK (roll_fallback IN ('defer', 'restart'));
ALTER TABLE schedule_activations ADD COLUMN max_defer_age_seconds BIGINT NOT NULL DEFAULT 0
    CHECK (max_defer_age_seconds >= 0);
ALTER TABLE schedule_activations ADD COLUMN capacity_deferrals BIGINT NOT NULL DEFAULT 0
    CHECK (capacity_deferrals >= 0);
ALTER TABLE schedule_activations ADD COLUMN capacity_deferred_at TIMESTAMPTZ;

ALTER TABLE schedule_activations DROP CONSTRAINT schedule_activations_status_check;
ALTER TABLE schedule_activations ADD CONSTRAINT schedule_activations_status_check CHECK (status IN (
    'pending', 'deferred_interval', 'deferred_capacity', 'repairing', 'running',
    'succeeded', 'failed', 'cancelled', 'superseded', 'not_needed',
    'blocked_unsupported', 'target_deleted'
));
