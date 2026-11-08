package db

import "testing"

// Migration 105 gives every existing run the retained size of the chunks it
// still holds, counting a chunk whose head was already trimmed in place by
// its current offsets, and leaves a run without output at zero.
func TestMigration105BackfillsRetainedLogBytes(t *testing.T) {
	s := migratedThrough(t, 104)
	mustExec(t, s, `INSERT INTO users (username, password_hash, role) VALUES ('owner','x','admin')`)
	mustExec(t, s, `INSERT INTO apps (slug, name, owner_id, access) VALUES ('logs','Logs',1,'private')`)
	for _, run := range []string{"run-with-output", "run-without-output"} {
		mustExec(t, s, `INSERT INTO app_log_runs (run_id, app_id, replica_index, status, started_at)
			VALUES (?, 1, 0, 'exited', 1)`, run)
	}
	mustExec(t, s, `INSERT INTO app_log_chunks (run_id, chunk_seq, start_offset, end_offset, data, created_at)
		VALUES ('run-with-output', 0, 2, 4, ?, 1)`, []byte("cd"))
	mustExec(t, s, `INSERT INTO app_log_chunks (run_id, chunk_seq, start_offset, end_offset, data, created_at)
		VALUES ('run-with-output', 1, 4, 8, ?, 2)`, []byte("efgh"))

	if err := s.Migrate(); err != nil {
		t.Fatal(err)
	}
	for run, want := range map[string]int64{"run-with-output": 6, "run-without-output": 0} {
		var got int64
		if err := s.DB().QueryRow(`SELECT bytes_retained FROM app_log_runs WHERE run_id = ?`, run).Scan(&got); err != nil {
			t.Fatalf("%s: read bytes_retained: %v", run, err)
		}
		if got != want {
			t.Fatalf("%s: bytes_retained = %d, want %d", run, got, want)
		}
	}
}
