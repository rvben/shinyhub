-- Non-identifying totals for retained CLOSED raw sessions, not retention rollups.
-- Triggers keep these counters in the writer's transaction, including old
-- binaries, foreign-key anonymization, corrections, and retention deletion.
-- Open sessions remain live; heartbeats do not maintain counters or this index.
CREATE TABLE IF NOT EXISTS usage_closed_daily (
    app_id INTEGER NOT NULL REFERENCES apps(id) ON DELETE CASCADE,
    day TEXT NOT NULL,
    sessions INTEGER NOT NULL DEFAULT 0 CHECK (sessions >= 0),
    person_sessions INTEGER NOT NULL DEFAULT 0 CHECK (person_sessions >= 0),
    anonymous_sessions INTEGER NOT NULL DEFAULT 0 CHECK (anonymous_sessions >= 0),
    service_sessions INTEGER NOT NULL DEFAULT 0 CHECK (service_sessions >= 0),
    identified_person_sessions INTEGER NOT NULL DEFAULT 0 CHECK (identified_person_sessions >= 0),
    pseudonymous_person_sessions INTEGER NOT NULL DEFAULT 0 CHECK (pseudonymous_person_sessions >= 0),
    total_duration_seconds INTEGER NOT NULL DEFAULT 0 CHECK (total_duration_seconds >= 0),
    PRIMARY KEY (app_id, day)
);

CREATE INDEX IF NOT EXISTS idx_usage_sessions_open_app_started
    ON usage_sessions(app_id, started_at) WHERE ended_at IS NULL;

-- Adopt existing history once. IF NOT EXISTS/ON CONFLICT also permit legacy
-- migration-ledger recovery without counting existing materialization twice.
INSERT INTO usage_closed_daily (app_id, day, sessions, person_sessions, anonymous_sessions, service_sessions, identified_person_sessions, pseudonymous_person_sessions, total_duration_seconds)
SELECT app_id, substr(CAST(started_at AS TEXT), 1, 10),
    SUM(1),
    SUM((principal_kind = 'person')),
    SUM((principal_kind = 'anonymous')),
    SUM((principal_kind = 'service_account')),
    SUM((principal_kind = 'person' AND identity_mode = 'identified' AND user_id IS NOT NULL)),
    SUM((principal_kind = 'person' AND identity_mode = 'pseudonymous' AND viewer_key IS NOT NULL)),
    SUM(COALESCE(MAX(0, unixepoch(substr(CAST(ended_at AS TEXT), 1, 19)) - unixepoch(substr(CAST(started_at AS TEXT), 1, 19))), 0))
FROM usage_sessions WHERE ended_at IS NOT NULL
GROUP BY app_id, substr(CAST(started_at AS TEXT), 1, 10)
ON CONFLICT (app_id, day) DO NOTHING;

CREATE TRIGGER IF NOT EXISTS usage_closed_insert AFTER INSERT ON usage_sessions
WHEN NEW.ended_at IS NOT NULL
BEGIN
    INSERT INTO usage_closed_daily (app_id, day, sessions, person_sessions, anonymous_sessions, service_sessions, identified_person_sessions, pseudonymous_person_sessions, total_duration_seconds)
    SELECT NEW.app_id, substr(CAST(NEW.started_at AS TEXT), 1, 10),
        1,
        (NEW.principal_kind = 'person'),
        (NEW.principal_kind = 'anonymous'),
        (NEW.principal_kind = 'service_account'),
        (NEW.principal_kind = 'person' AND NEW.identity_mode = 'identified' AND NEW.user_id IS NOT NULL),
        (NEW.principal_kind = 'person' AND NEW.identity_mode = 'pseudonymous' AND NEW.viewer_key IS NOT NULL),
        COALESCE(MAX(0, unixepoch(substr(CAST(NEW.ended_at AS TEXT), 1, 19)) - unixepoch(substr(CAST(NEW.started_at AS TEXT), 1, 19))), 0)
    WHERE NEW.ended_at IS NOT NULL
    ON CONFLICT (app_id, day) DO UPDATE SET
        sessions = sessions + excluded.sessions,
        person_sessions = person_sessions + excluded.person_sessions,
        anonymous_sessions = anonymous_sessions + excluded.anonymous_sessions,
        service_sessions = service_sessions + excluded.service_sessions,
        identified_person_sessions = identified_person_sessions + excluded.identified_person_sessions,
        pseudonymous_person_sessions = pseudonymous_person_sessions + excluded.pseudonymous_person_sessions,
        total_duration_seconds = total_duration_seconds + excluded.total_duration_seconds;
END;

CREATE TRIGGER IF NOT EXISTS usage_closed_delete AFTER DELETE ON usage_sessions
WHEN OLD.ended_at IS NOT NULL
BEGIN
    UPDATE usage_closed_daily SET
        sessions = sessions - 1,
        person_sessions = person_sessions - (OLD.principal_kind = 'person'),
        anonymous_sessions = anonymous_sessions - (OLD.principal_kind = 'anonymous'),
        service_sessions = service_sessions - (OLD.principal_kind = 'service_account'),
        identified_person_sessions = identified_person_sessions - (OLD.principal_kind = 'person' AND OLD.identity_mode = 'identified' AND OLD.user_id IS NOT NULL),
        pseudonymous_person_sessions = pseudonymous_person_sessions - (OLD.principal_kind = 'person' AND OLD.identity_mode = 'pseudonymous' AND OLD.viewer_key IS NOT NULL),
        total_duration_seconds = total_duration_seconds - COALESCE(MAX(0, unixepoch(substr(CAST(OLD.ended_at AS TEXT), 1, 19)) - unixepoch(substr(CAST(OLD.started_at AS TEXT), 1, 19))), 0)
    WHERE OLD.ended_at IS NOT NULL AND app_id = OLD.app_id
      AND day = substr(CAST(OLD.started_at AS TEXT), 1, 10);
    DELETE FROM usage_closed_daily WHERE app_id = OLD.app_id
      AND day = substr(CAST(OLD.started_at AS TEXT), 1, 10) AND sessions = 0;
END;

CREATE TRIGGER IF NOT EXISTS usage_closed_update
AFTER UPDATE OF app_id, started_at, ended_at, principal_kind, identity_mode, user_id, viewer_key ON usage_sessions
WHEN (OLD.ended_at IS NOT NULL OR NEW.ended_at IS NOT NULL) AND (
    OLD.app_id IS NOT NEW.app_id OR OLD.started_at IS NOT NEW.started_at OR
    OLD.ended_at IS NOT NEW.ended_at OR OLD.principal_kind IS NOT NEW.principal_kind OR
    OLD.identity_mode IS NOT NEW.identity_mode OR OLD.user_id IS NOT NEW.user_id OR
    OLD.viewer_key IS NOT NEW.viewer_key)
BEGIN
    UPDATE usage_closed_daily SET
        sessions = sessions - 1,
        person_sessions = person_sessions - (OLD.principal_kind = 'person'),
        anonymous_sessions = anonymous_sessions - (OLD.principal_kind = 'anonymous'),
        service_sessions = service_sessions - (OLD.principal_kind = 'service_account'),
        identified_person_sessions = identified_person_sessions - (OLD.principal_kind = 'person' AND OLD.identity_mode = 'identified' AND OLD.user_id IS NOT NULL),
        pseudonymous_person_sessions = pseudonymous_person_sessions - (OLD.principal_kind = 'person' AND OLD.identity_mode = 'pseudonymous' AND OLD.viewer_key IS NOT NULL),
        total_duration_seconds = total_duration_seconds - COALESCE(MAX(0, unixepoch(substr(CAST(OLD.ended_at AS TEXT), 1, 19)) - unixepoch(substr(CAST(OLD.started_at AS TEXT), 1, 19))), 0)
    WHERE OLD.ended_at IS NOT NULL AND app_id = OLD.app_id
      AND day = substr(CAST(OLD.started_at AS TEXT), 1, 10);
    DELETE FROM usage_closed_daily WHERE app_id = OLD.app_id
      AND day = substr(CAST(OLD.started_at AS TEXT), 1, 10) AND sessions = 0;
    INSERT INTO usage_closed_daily (app_id, day, sessions, person_sessions, anonymous_sessions, service_sessions, identified_person_sessions, pseudonymous_person_sessions, total_duration_seconds)
    SELECT NEW.app_id, substr(CAST(NEW.started_at AS TEXT), 1, 10),
        1,
        (NEW.principal_kind = 'person'),
        (NEW.principal_kind = 'anonymous'),
        (NEW.principal_kind = 'service_account'),
        (NEW.principal_kind = 'person' AND NEW.identity_mode = 'identified' AND NEW.user_id IS NOT NULL),
        (NEW.principal_kind = 'person' AND NEW.identity_mode = 'pseudonymous' AND NEW.viewer_key IS NOT NULL),
        COALESCE(MAX(0, unixepoch(substr(CAST(NEW.ended_at AS TEXT), 1, 19)) - unixepoch(substr(CAST(NEW.started_at AS TEXT), 1, 19))), 0)
    WHERE NEW.ended_at IS NOT NULL
    ON CONFLICT (app_id, day) DO UPDATE SET
        sessions = sessions + excluded.sessions,
        person_sessions = person_sessions + excluded.person_sessions,
        anonymous_sessions = anonymous_sessions + excluded.anonymous_sessions,
        service_sessions = service_sessions + excluded.service_sessions,
        identified_person_sessions = identified_person_sessions + excluded.identified_person_sessions,
        pseudonymous_person_sessions = pseudonymous_person_sessions + excluded.pseudonymous_person_sessions,
        total_duration_seconds = total_duration_seconds + excluded.total_duration_seconds;
END;
