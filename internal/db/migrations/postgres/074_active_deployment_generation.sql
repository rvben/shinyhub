-- Publish one durable generation pointer per app. Kept separate from the token
-- migration so legacy-ledger adoption can never mark a partially applied
-- multi-ALTER migration complete.
ALTER TABLE apps ADD COLUMN active_deployment_id BIGINT
    REFERENCES deployments(id) ON DELETE SET NULL;
UPDATE apps
SET active_deployment_id = (
    SELECT deployments.id
    FROM deployments
    WHERE deployments.app_id = apps.id
      AND deployments.status = 'succeeded'
    ORDER BY deployments.id DESC
    LIMIT 1
)
WHERE EXISTS (
    SELECT 1
    FROM deployments
    WHERE deployments.app_id = apps.id
      AND deployments.status = 'succeeded'
);
