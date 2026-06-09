-- Per-app group access rules: an IdP group is granted a role (viewer/manager)
-- on a specific app. Effective access is computed live by joining user_groups
-- (migration 028) against this table at authz time.
CREATE TABLE IF NOT EXISTS app_group_access (
    app_slug   TEXT NOT NULL REFERENCES apps(slug) ON DELETE CASCADE,
    group_name TEXT NOT NULL,
    role       TEXT NOT NULL DEFAULT 'viewer',  -- viewer | manager
    source     TEXT NOT NULL DEFAULT 'manual',  -- manual | manifest (manifest reconcile is P4)
    PRIMARY KEY (app_slug, group_name)
);
CREATE INDEX IF NOT EXISTS idx_app_group_access_group ON app_group_access(group_name);
