-- Projects are display containers for apps: a slug plus optional display
-- metadata. apps.project_slug is a SOFT reference to projects.slug (no foreign
-- key on purpose), so an app may name a project that does not exist yet and
-- deleting a project never cascades into apps. A missing projects row means
-- "grouped under this slug, no display name yet", not "invalid".
--
-- Pre-existing rows: migration 001 gave apps.project_slug a DEFAULT of
-- 'default', so every app created before this migration carries either that
-- sentinel or an operator-typed value. This migration collapses the sentinel
-- to '' so "no project" has exactly ONE representation from here on. SQLite
-- cannot alter a column default without a full table rebuild, so the old
-- DEFAULT 'default' stays in the schema and is made unreachable by the
-- application layer instead (internal/db must always write project_slug
-- explicitly). The Postgres counterpart does drop the default, so the two
-- schemas diverge here by design.
CREATE TABLE IF NOT EXISTS projects (
    slug        TEXT PRIMARY KEY,
    name        TEXT NOT NULL DEFAULT '',
    description TEXT NOT NULL DEFAULT '',
    icon_emoji  TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Backfill: every distinct project slug already in use becomes a projects row
-- with empty display metadata. Values that cannot pass internal/slug.Valid are
-- left out rather than inserted, so a legacy free-text value like 'Bad Slug'
-- keeps grouping the app it is on but never enters the discoverable set. The
-- sentinel is excluded here and rewritten below.
INSERT OR IGNORE INTO projects (slug)
SELECT DISTINCT project_slug FROM apps
WHERE project_slug <> ''
  AND project_slug <> 'default'
  AND length(project_slug) <= 63
  AND project_slug GLOB '[a-z0-9]*'
  AND project_slug NOT GLOB '*[^a-z0-9-]*'
  AND project_slug NOT GLOB '*-';

-- Order matters: the INSERT above must run BEFORE this rewrite, otherwise the
-- sentinel exclusion has nothing to exclude and 'default' would already be ''.
UPDATE apps SET project_slug = '' WHERE project_slug = 'default';
