-- Platform announcements: all dates are UTC Unix milliseconds on both backends.
CREATE TABLE IF NOT EXISTS announcements (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 120),
    message TEXT NOT NULL CHECK (length(message) BETWEEN 1 AND 600),
    details_url TEXT NOT NULL DEFAULT '',
    severity TEXT NOT NULL CHECK (severity IN ('information', 'warning', 'critical')),
    publication TEXT NOT NULL CHECK (publication IN ('draft', 'published', 'disabled', 'archived')),
    dismissible INTEGER NOT NULL CHECK (dismissible IN (0, 1)),
    starts_at BIGINT,
    ends_at BIGINT,
    published_at BIGINT,
    created_at BIGINT NOT NULL,
    updated_at BIGINT NOT NULL,
    created_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    updated_by BIGINT REFERENCES users(id) ON DELETE SET NULL,
    revision BIGINT NOT NULL DEFAULT 1,
    display_revision BIGINT NOT NULL DEFAULT 1,
    CHECK (publication != 'published' OR starts_at IS NOT NULL),
    CHECK (ends_at IS NULL OR starts_at IS NULL OR ends_at > starts_at)
);
CREATE INDEX IF NOT EXISTS idx_announcements_publication ON announcements(publication, starts_at, ends_at);
