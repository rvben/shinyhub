package db_test

import (
	"database/sql"
	"os"
	"testing"
)

func TestSupportSessionRoleMigrationPreservesSessions(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	if _, err = conn.Exec("CREATE TABLE users (id INTEGER PRIMARY KEY); CREATE TABLE apps (id INTEGER PRIMARY KEY, slug TEXT UNIQUE);"); err != nil {
		t.Fatal(err)
	}
	old, err := os.ReadFile("migrations/sqlite/072_support_sessions.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(string(old)); err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(`INSERT INTO support_sessions (id,actor_username,actor_token_epoch,subject_username,subject_role,subject_token_epoch,app_slug_snapshot,reason,launch_code_hash,token_jti,expires_at) VALUES ('existing','actor',0,'person','viewer',0,'sales','Existing support session','hash','existing-jti','2030-01-01')`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/sqlite/083_support_session_human_roles.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var role, jti string
	if err = conn.QueryRow("SELECT subject_role,token_jti FROM support_sessions WHERE id='existing'").Scan(&role, &jti); err != nil {
		t.Fatal(err)
	}
	if role != "viewer" || jti != "existing-jti" {
		t.Fatalf("session changed: %s %s", role, jti)
	}
	for _, role = range []string{"operator", "admin"} {
		if _, err = conn.Exec("UPDATE support_sessions SET subject_role=? WHERE id='existing'", role); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = conn.Exec("UPDATE support_sessions SET subject_role='unknown' WHERE id='existing'"); err == nil {
		t.Fatal("invalid role accepted")
	}
	var indexes int
	if err = conn.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='index' AND name LIKE 'idx_support_sessions_%'").Scan(&indexes); err != nil || indexes != 4 {
		t.Fatalf("indexes=%d err=%v", indexes, err)
	}
}
