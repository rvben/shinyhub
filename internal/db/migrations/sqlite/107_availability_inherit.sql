-- Preserve every historical value: omitted and explicit off are indistinguishable.
-- Replace only this column, retaining apps IDs, foreign keys, triggers and the
-- legacy off default for predecessor binaries during rolling upgrades.
DROP INDEX idx_apps_availability;
ALTER TABLE apps RENAME COLUMN availability TO availability_legacy;
ALTER TABLE apps ADD COLUMN availability TEXT NOT NULL DEFAULT 'off' CHECK (availability IN ('off', 'public', 'inherit'));
UPDATE apps SET availability = availability_legacy;
ALTER TABLE apps DROP COLUMN availability_legacy;
CREATE INDEX idx_apps_availability ON apps (availability, slug);
