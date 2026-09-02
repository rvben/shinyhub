-- A deployment generation is published independently from newer candidates.
-- The token is an opaque browser-facing identifier, never an authorization
-- capability; the numeric deployment ID remains internal to the control plane.
ALTER TABLE deployments ADD COLUMN activation_token TEXT NOT NULL DEFAULT '';
UPDATE deployments
SET activation_token = replace(gen_random_uuid()::text, '-', '') || replace(gen_random_uuid()::text, '-', '')
WHERE activation_token = '';
CREATE UNIQUE INDEX idx_deployments_activation_token
    ON deployments(activation_token)
    WHERE activation_token <> '';
