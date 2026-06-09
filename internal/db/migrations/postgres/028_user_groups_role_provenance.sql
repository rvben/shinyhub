-- See sqlite/028. Role provenance + group snapshot, with the same viewer-baseline seed.
ALTER TABLE users ADD COLUMN manual_role TEXT;
ALTER TABLE users ADD COLUMN role_source TEXT NOT NULL DEFAULT 'default';

CREATE TABLE user_groups (
    user_id    bigint NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_name TEXT   NOT NULL,
    PRIMARY KEY (user_id, group_name)
);
CREATE INDEX idx_user_groups_user  ON user_groups(user_id);
CREATE INDEX idx_user_groups_group ON user_groups(group_name);

UPDATE users SET manual_role = role, role_source = 'manual' WHERE role <> 'viewer';
