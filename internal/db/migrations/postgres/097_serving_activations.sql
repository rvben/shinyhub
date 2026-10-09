ALTER TABLE schedule_activations ADD COLUMN activation_deployment_id BIGINT REFERENCES deployments(id) ON DELETE SET NULL;
ALTER TABLE deployments ADD COLUMN serving_activation_id BIGINT;
ALTER TABLE deployment_replicas ADD COLUMN data_generation BIGINT NOT NULL DEFAULT 0;
ALTER TABLE deployment_replicas ADD COLUMN startup_peak_rss_bytes BIGINT NOT NULL DEFAULT 0;
ALTER TABLE deployment_replicas ADD COLUMN process_start_identity BIGINT NOT NULL DEFAULT 0;
CREATE INDEX idx_deployments_serving_activation ON deployments(serving_activation_id);
CREATE INDEX idx_schedule_activation_deployment ON schedule_activations(activation_deployment_id);
