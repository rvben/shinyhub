-- See sqlite/029.
CREATE TABLE app_group_access (
    app_slug   TEXT NOT NULL REFERENCES apps(slug) ON DELETE CASCADE,
    group_name TEXT NOT NULL,
    role       TEXT NOT NULL DEFAULT 'viewer',
    source     TEXT NOT NULL DEFAULT 'manual',
    PRIMARY KEY (app_slug, group_name)
);
CREATE INDEX idx_app_group_access_group ON app_group_access(group_name);
