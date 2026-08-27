package db

import "testing"

func TestMigration064PreservesActivationHistoryAndAddsCancelledState(t *testing.T) {
	s := migratedThrough(t, 63)
	mustExec(t, s, `INSERT INTO users (id, username, password_hash, role) VALUES (1, 'owner', 'hash', 'developer')`)
	mustExec(t, s, `INSERT INTO apps (id, slug, name, owner_id) VALUES (1, 'reports', 'Reports', 1)`)
	mustExec(t, s, `INSERT INTO app_schedules
		(id, app_id, name, cron_expr, command_json, on_success)
		VALUES (1, 1, 'nightly', '0 2 * * *', '["refresh"]', 'roll')`)
	mustExec(t, s, `INSERT INTO schedule_runs
		(id, schedule_id, status, trigger, started_at, on_success, target_generation)
		VALUES (1, 1, 'succeeded', 'schedule', CURRENT_TIMESTAMP, 'roll', 1)`)
	mustExec(t, s, `INSERT INTO schedule_activations
		(id, app_id, app_slug, schedule_id, schedule_name, schedule_run_id, action,
		target_generation, status, phase, due_at, finished_at)
		VALUES (1, 1, 'reports', 1, 'nightly', 1, 'roll', 1, 'succeeded',
		'complete', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
	mustExec(t, s, `INSERT INTO schedule_activations
		(id, app_id, app_slug, schedule_id, schedule_name, action, target_generation,
		status, phase, due_at, superseded_by_id, finished_at)
		VALUES (2, 1, 'reports', 1, 'nightly', 'roll', 2, 'superseded',
		'complete', CURRENT_TIMESTAMP, 1, CURRENT_TIMESTAMP)`)

	if err := s.Migrate(); err != nil {
		t.Fatalf("migrate v63 to v64: %v", err)
	}

	var status, fallback string
	var supersededBy int64
	if err := s.db.QueryRow(`SELECT status, roll_fallback, superseded_by_id
		FROM schedule_activations WHERE id = 2`).Scan(&status, &fallback, &supersededBy); err != nil {
		t.Fatalf("read preserved activation: %v", err)
	}
	if status != "superseded" || fallback != "defer" || supersededBy != 1 {
		t.Fatalf("preserved activation = status %q fallback %q superseded_by %d", status, fallback, supersededBy)
	}
	if _, err := s.db.Exec(`INSERT INTO schedule_activations
		(app_id, app_slug, schedule_id, schedule_name, action, target_generation,
		status, phase, due_at, finished_at)
		VALUES (1, 'reports', 1, 'nightly', 'roll', 3, 'cancelled',
		'complete', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`); err != nil {
		t.Fatalf("insert first-class cancelled activation: %v", err)
	}
}
