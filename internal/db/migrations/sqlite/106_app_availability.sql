-- Exposure belongs to an app identity. Existing apps remain private to probes
-- unless their slug is centrally allowlisted. Never infer historical stop intent.
ALTER TABLE apps ADD COLUMN availability TEXT NOT NULL DEFAULT 'off' CHECK (availability IN ('off', 'public'));
-- Lifecycle writers maintain intent atomically. Avoid a cross-column CHECK:
-- a rolling-upgrade predecessor does not know to clear this new column.
ALTER TABLE apps ADD COLUMN operator_stopped INTEGER NOT NULL DEFAULT 0 CHECK (operator_stopped IN (0, 1));
CREATE INDEX idx_apps_availability ON apps (availability, slug);
