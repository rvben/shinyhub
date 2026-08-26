-- Principals are explicit: interactive people and non-interactive service accounts
-- share the users primary key so existing app ownership remains intact.
ALTER TABLE users ADD COLUMN principal_type TEXT NOT NULL DEFAULT 'human';
ALTER TABLE users ADD COLUMN service_account_key TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN managed_by TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_users_service_account_key
    ON users(service_account_key) WHERE service_account_key <> '';

-- Safely adopt only the legacy synthetic account shape. A real person who
-- claimed the reserved username is deliberately left human so startup can fail
-- closed instead of silently taking over their identity.
UPDATE users
SET principal_type = 'service_account',
    service_account_key = 'deployment',
    managed_by = 'platform',
    display_name = CASE WHEN display_name = '' THEN 'Deployment automation' ELSE display_name END,
    token_epoch = token_epoch + 1
WHERE username = '__deploy__'
  AND password_hash = '!disabled'
  AND NOT EXISTS (SELECT 1 FROM oauth_accounts oa WHERE oa.user_id = users.id)
  AND NOT EXISTS (SELECT 1 FROM api_keys k WHERE k.user_id = users.id);

ALTER TABLE api_keys ADD COLUMN credential_type TEXT NOT NULL DEFAULT 'personal';
ALTER TABLE api_keys ADD COLUMN credential_role TEXT NOT NULL DEFAULT '';
ALTER TABLE api_keys ADD COLUMN app_scope TEXT NOT NULL DEFAULT '[]';
ALTER TABLE api_keys ADD COLUMN unrestricted INTEGER NOT NULL DEFAULT 0;
ALTER TABLE api_keys ADD COLUMN created_by_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE api_keys ADD COLUMN external_id TEXT NOT NULL DEFAULT '';

CREATE UNIQUE INDEX idx_api_keys_external_id
    ON api_keys(external_id) WHERE external_id <> '';

-- Credential snapshots preserve attribution after a credential is revoked.
ALTER TABLE audit_events ADD COLUMN credential_id INTEGER;
ALTER TABLE audit_events ADD COLUMN credential_type TEXT NOT NULL DEFAULT '';
ALTER TABLE audit_events ADD COLUMN credential_name TEXT NOT NULL DEFAULT '';

-- Fleet runs are bound to the exact credential that registered them. This
-- prevents a sibling team credential on the same service account from taking
-- over another team's run lifecycle.
ALTER TABLE fleet_runs ADD COLUMN credential_id INTEGER;
ALTER TABLE fleet_runs ADD COLUMN credential_type TEXT NOT NULL DEFAULT '';
ALTER TABLE fleet_runs ADD COLUMN credential_name TEXT NOT NULL DEFAULT '';

ALTER TABLE deployments ADD COLUMN origin_credential_id INTEGER;
ALTER TABLE deployments ADD COLUMN origin_credential_type TEXT NOT NULL DEFAULT '';
ALTER TABLE deployments ADD COLUMN origin_credential_name TEXT NOT NULL DEFAULT '';
