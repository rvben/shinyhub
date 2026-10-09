ALTER TABLE app_schedules ADD COLUMN inputs_json TEXT NOT NULL DEFAULT '';
ALTER TABLE deployment_schedule_snapshots ADD COLUMN inputs_json TEXT NOT NULL DEFAULT '';
ALTER TABLE deployment_prior_schedule_snapshots ADD COLUMN inputs_json TEXT NOT NULL DEFAULT '';
ALTER TABLE schedule_deploy_obligations ADD COLUMN producer_inputs_json TEXT NOT NULL DEFAULT '';
