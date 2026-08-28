ALTER TABLE app_schedules ADD COLUMN deploy_trigger TEXT NOT NULL DEFAULT 'never'
    CHECK (deploy_trigger IN ('never', 'first_deploy', 'bundle_change'));

ALTER TABLE schedule_runs ADD COLUMN deployment_id BIGINT;
ALTER TABLE schedule_runs ADD COLUMN app_version TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_runs ADD COLUMN content_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_runs ADD COLUMN producer_fingerprint TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_runs ADD COLUMN producer_command_json TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_runs ADD COLUMN deploy_obligation_id BIGINT;
ALTER TABLE schedule_runs ADD COLUMN publishes_data INTEGER NOT NULL DEFAULT 0
    CHECK (publishes_data IN (0, 1));
ALTER TABLE schedule_runs ADD COLUMN data_write_sequence BIGINT NOT NULL DEFAULT 0;
ALTER TABLE schedule_runs ADD COLUMN provenance_admission INTEGER NOT NULL DEFAULT 0
    CHECK (provenance_admission IN (0, 1));

CREATE TABLE legacy_unfenced_schedule_runs (
    run_id BIGINT PRIMARY KEY REFERENCES schedule_runs(id),
    detected_at timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP
);
INSERT INTO legacy_unfenced_schedule_runs (run_id)
SELECT id FROM schedule_runs WHERE status = 'running';

-- Atomically reject admissions from a pre-069 process that remains alive
-- during a tableflip/HA rolling upgrade. New code always binds every execution
-- to an immutable deployment and canonical producer identity.
CREATE FUNCTION schedule_runs_require_execution_provenance_fn()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
    IF NEW.status = 'running' AND NEW.provenance_admission <> 1 THEN
        RAISE EXCEPTION 'schedule run requires deployment provenance; stop the legacy server before upgrading';
    END IF;
    RETURN NEW;
END;
$$;

CREATE TRIGGER schedule_runs_require_execution_provenance
BEFORE INSERT ON schedule_runs
FOR EACH ROW EXECUTE FUNCTION schedule_runs_require_execution_provenance_fn();

ALTER TABLE schedule_activations ADD COLUMN source_deployment_id BIGINT;
ALTER TABLE schedule_activations ADD COLUMN source_app_version TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_activations ADD COLUMN source_content_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_activations ADD COLUMN source_producer_fingerprint TEXT NOT NULL DEFAULT '';

CREATE TABLE schedule_producer_state (
    schedule_id          BIGINT PRIMARY KEY REFERENCES app_schedules(id) ON DELETE CASCADE,
    content_digest       TEXT NOT NULL,
    producer_fingerprint TEXT NOT NULL,
    producer_command_json TEXT NOT NULL,
    deployment_id        BIGINT,
    app_version          TEXT NOT NULL DEFAULT '',
    schedule_run_id      BIGINT,
    publication_generation BIGINT NOT NULL DEFAULT 1,
    data_write_sequence  BIGINT NOT NULL DEFAULT 0,
    published_at         timestamptz NOT NULL
);

CREATE TABLE schedule_deploy_obligations (
    id                   BIGSERIAL PRIMARY KEY,
    schedule_id          BIGINT NOT NULL REFERENCES app_schedules(id) ON DELETE CASCADE,
    deployment_id        BIGINT NOT NULL,
    app_version          TEXT NOT NULL,
    content_digest       TEXT NOT NULL,
    producer_fingerprint TEXT NOT NULL,
    producer_command_json TEXT NOT NULL,
    timeout_seconds      INTEGER NOT NULL,
    on_success           TEXT NOT NULL,
    min_roll_interval_seconds INTEGER NOT NULL,
    roll_fallback        TEXT NOT NULL,
    max_defer_age_seconds INTEGER NOT NULL,
    status               TEXT NOT NULL CHECK (status IN ('pending', 'dispatching', 'running', 'satisfied', 'failed', 'superseded')),
    schedule_run_id      BIGINT,
    attempts             INTEGER NOT NULL DEFAULT 0,
    last_error           TEXT NOT NULL DEFAULT '',
    next_attempt_at      timestamptz,
    created_at           timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           timestamptz NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at          timestamptz,
    UNIQUE (schedule_id, deployment_id, producer_fingerprint)
);

CREATE INDEX idx_schedule_runs_digest
    ON schedule_runs(schedule_id, content_digest, status);

CREATE INDEX idx_schedule_deploy_obligations_pending
    ON schedule_deploy_obligations(status, next_attempt_at, schedule_id, deployment_id);
