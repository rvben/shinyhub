-- See the sqlite counterpart for the full rationale. Differences: timestamptz
-- with now() instead of TIMESTAMP with CURRENT_TIMESTAMP, an anchored regexp
-- instead of the GLOB chain, and an actual DROP of the legacy column default
-- (Postgres can alter it in place, SQLite cannot).
CREATE TABLE IF NOT EXISTS projects (
    slug        text PRIMARY KEY,
    name        text NOT NULL DEFAULT '',
    description text NOT NULL DEFAULT '',
    icon_emoji  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

INSERT INTO projects (slug)
SELECT DISTINCT project_slug FROM apps
WHERE project_slug <> ''
  AND project_slug <> 'default'
  AND project_slug ~ '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'
ON CONFLICT (slug) DO NOTHING;

-- Order matters: the INSERT above must run BEFORE this rewrite.
UPDATE apps SET project_slug = '' WHERE project_slug = 'default';

ALTER TABLE apps ALTER COLUMN project_slug SET DEFAULT '';
