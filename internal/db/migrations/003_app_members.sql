CREATE TABLE IF NOT EXISTS app_members (
    app_slug TEXT NOT NULL REFERENCES apps(slug) ON DELETE CASCADE,
    user_id  INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    PRIMARY KEY (app_slug, user_id)
);
