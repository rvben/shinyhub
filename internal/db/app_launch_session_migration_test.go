package db

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

func TestMigration091PreservesLaunchCodesAndBrowserSessions(t *testing.T) {
	s := migratedThrough(t, 90)
	mustExec(t, s, `INSERT INTO users (id, username, password_hash, role) VALUES (1, 'owner', 'hash', 'developer')`)
	mustExec(t, s, `INSERT INTO apps (id, slug, name, owner_id) VALUES (1, 'reports', 'Reports', 1)`)
	mustExec(t, s, `INSERT INTO app_launch_codes (code_hash, user_id, app_slug) VALUES ('pending', 1, 'reports')`)

	if err := s.Migrate(); err != nil {
		t.Fatalf("upgrade schema 90: %v", err)
	}
	// A second startup must leave both the schema and outstanding codes intact.
	if err := s.Migrate(); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var authTime, epoch int64
	var jti string
	if err := s.DB().QueryRow(`SELECT auth_time, session_jti, session_epoch FROM app_launch_codes WHERE code_hash = 'pending'`).Scan(&authTime, &jti, &epoch); err != nil {
		t.Fatal(err)
	}
	if authTime != 0 || jti != "" || epoch != 0 {
		t.Fatalf("legacy defaults = (%d, %q, %d)", authTime, jti, epoch)
	}
	u, session, err := s.ConsumeAppLaunchCodeWithSession("pending", "reports")
	if err != nil || u == nil || u.ID != 1 || session != nil {
		t.Fatalf("legacy launch = user %v, session %v, error %v", u, session, err)
	}
	if _, _, err := s.ConsumeAppLaunchCodeWithSession("pending", "reports"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy replay = %v, want ErrNotFound", err)
	}
	if err := s.BumpTokenEpoch(1); err != nil {
		t.Fatal(err)
	}
	original := &auth.TokenInfo{JTI: "original-family", AuthTime: time.Now().Add(-time.Hour).Truncate(time.Second)}
	if err := s.CreateAppLaunchCodeWithSession("renewable", 1, "reports", original, 1); err != nil {
		t.Fatal(err)
	}
	u, session, err = s.ConsumeAppLaunchCodeWithSession("renewable", "reports")
	if err != nil || u == nil || u.ID != 1 || session == nil || session.JTI != original.JTI || !session.AuthTime.Equal(original.AuthTime) {
		t.Fatalf("upgraded browser launch = user %v, session %v, error %v", u, session, err)
	}
}
