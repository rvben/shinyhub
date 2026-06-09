-- Role provenance: manual_role is a break-glass override that survives SSO
-- reconciliation; role_source records what set the effective users.role.
ALTER TABLE users ADD COLUMN manual_role TEXT;
ALTER TABLE users ADD COLUMN role_source TEXT NOT NULL DEFAULT 'default';

-- Snapshot of a user's IdP groups, replaced wholesale on each SSO login.
CREATE TABLE IF NOT EXISTS user_groups (
    user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_name TEXT    NOT NULL,
    PRIMARY KEY (user_id, group_name)
);
CREATE INDEX IF NOT EXISTS idx_user_groups_user  ON user_groups(user_id);
CREATE INDEX IF NOT EXISTS idx_user_groups_group ON user_groups(group_name);

-- Seed: preserve every existing elevated user (role above viewer) as a manual
-- override so SSO reconciliation cannot auto-demote them. Operators clear the
-- override (set manual_role NULL) to opt a user into group governance.
UPDATE users SET manual_role = role, role_source = 'manual' WHERE role <> 'viewer';
