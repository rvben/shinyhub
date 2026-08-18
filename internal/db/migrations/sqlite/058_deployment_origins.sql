ALTER TABLE deployments ADD COLUMN origin_kind TEXT NOT NULL DEFAULT 'legacy'
    CHECK (origin_kind IN ('fleet', 'direct', 'rollback', 'legacy'));
ALTER TABLE deployments ADD COLUMN origin_channel TEXT NOT NULL DEFAULT ''
    CHECK (origin_channel IN ('', 'fleet', 'dashboard', 'cli', 'api'));
ALTER TABLE deployments ADD COLUMN origin_user_id INTEGER REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE deployments ADD COLUMN origin_actor TEXT NOT NULL DEFAULT '';

-- Migration 057 already captured fleet run IDs. Preserve their authenticated
-- actor as a snapshot so later user renames/deletions cannot erase attribution.
UPDATE deployments
SET origin_kind = 'fleet',
    origin_channel = 'fleet',
    origin_user_id = (SELECT fr.user_id FROM fleet_runs fr WHERE fr.id = deployments.run_id),
    origin_actor = COALESCE((
        SELECT u.username
        FROM fleet_runs fr LEFT JOIN users u ON u.id = fr.user_id
        WHERE fr.id = deployments.run_id
    ), '')
WHERE run_id IS NOT NULL;

CREATE INDEX idx_deployments_origin_user_id ON deployments(origin_user_id);
