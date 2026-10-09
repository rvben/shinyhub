-- Unknown legacy policies are resolved only while the serving runtime is fenced.
ALTER TABLE deployments ADD COLUMN worker_isolation TEXT NOT NULL DEFAULT '';
