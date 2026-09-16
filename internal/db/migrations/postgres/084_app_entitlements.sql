-- See sqlite/084.
CREATE TABLE IF NOT EXISTS app_entitlements (
    app_id BIGINT NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (app_id, name)
);
CREATE TABLE IF NOT EXISTS app_user_entitlements (
    app_id BIGINT NOT NULL,
    entitlement TEXT NOT NULL,
    user_id BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (app_id, entitlement, user_id),
    FOREIGN KEY (app_id, entitlement) REFERENCES app_entitlements(app_id, name) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_app_user_entitlements_user ON app_user_entitlements(user_id, app_id);
CREATE TABLE IF NOT EXISTS app_group_entitlements (
    app_id BIGINT NOT NULL,
    entitlement TEXT NOT NULL,
    group_name TEXT NOT NULL,
    PRIMARY KEY (app_id, entitlement, group_name),
    FOREIGN KEY (app_id, entitlement) REFERENCES app_entitlements(app_id, name) ON DELETE CASCADE
);
