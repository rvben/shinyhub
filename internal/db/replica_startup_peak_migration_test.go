package db

import "testing"

func TestMigration065AddsReplicaStartupPeakWithoutLosingRows(t *testing.T) {
	s := migratedThrough(t, 64)
	mustExec(t, s, `INSERT INTO users (id, username, password_hash, role) VALUES (1, 'owner', 'hash', 'developer')`)
	mustExec(t, s, `INSERT INTO apps (id, slug, name, owner_id) VALUES (1, 'reports', 'Reports', 1)`)
	mustExec(t, s, `INSERT INTO replicas (app_id, idx, status) VALUES (1, 0, 'running')`)

	if err := s.Migrate(); err != nil {
		t.Fatalf("migrate v64 to v65: %v", err)
	}

	var peak int64
	if err := s.db.QueryRow(`SELECT startup_peak_rss_bytes FROM replicas WHERE app_id = 1 AND idx = 0`).Scan(&peak); err != nil {
		t.Fatalf("read migrated replica: %v", err)
	}
	if peak != 0 {
		t.Fatalf("migrated startup peak = %d, want 0", peak)
	}
	if _, err := s.db.Exec(`UPDATE replicas SET startup_peak_rss_bytes = -1 WHERE app_id = 1 AND idx = 0`); err == nil {
		t.Fatal("negative startup peak unexpectedly accepted")
	}
}
