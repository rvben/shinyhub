-- Publish one durable generation pointer per app. Kept separate from the token
-- migration so legacy-ledger adoption can never mark a partially applied
-- multi-ALTER migration complete.
ALTER TABLE apps ADD COLUMN active_deployment_id INTEGER
    REFERENCES deployments(id) ON DELETE SET NULL;
UPDATE apps
SET active_deployment_id = (
    SELECT id
    FROM deployments
    WHERE deployments.app_id = apps.id
      AND deployments.status = 'succeeded'
    ORDER BY deployments.id DESC
    LIMIT 1
);
