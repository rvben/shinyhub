package db

import "testing"

// TestMigration089AddsProviderColumn proves the oauth_states.provider column
// migration (089): a fresh column defaulting to the empty string is added, so
// a state row already in flight at upgrade time (created before 089 ran,
// when no provider column existed) is not silently reinterpreted as
// belonging to any provider - it fails once, since no real provider name is
// ever the empty string. A state created after the migration works
// normally: it is consumable only by the provider it was minted for. The
// migration is dialect-generic (a plain `ALTER TABLE ... ADD COLUMN ...
// TEXT NOT NULL DEFAULT` empty string), so this runs on Postgres too when
// SHINYHUB_TEST_POSTGRES_DSN is set.
func TestMigration089AddsProviderColumn(t *testing.T) {
	s := migratedThrough(t, 88)

	// Simulate a state minted by the pre-089 binary: no provider column
	// existed yet, so this is exactly what the old CreateOAuthState ran.
	mustExec(t, s, `INSERT INTO oauth_states (state) VALUES ('in-flight-state')`)

	if err := s.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var provider string
	if err := s.DB().QueryRow(`SELECT provider FROM oauth_states WHERE state = 'in-flight-state'`).Scan(&provider); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if provider != "" {
		t.Errorf("pre-migration row provider = %q, want empty default", provider)
	}

	// The in-flight state fails once: no real provider name is ever "", so it
	// can never be consumed again once providers are enforced. This is the
	// approved, intentional cost of closing the cross-provider hole for rows
	// already in flight at upgrade time.
	if err := s.ConsumeOAuthState("in-flight-state", "github"); err == nil {
		t.Error("an in-flight pre-migration state must fail to consume once providers are enforced")
	}

	// A state created after the migration works normally: matching provider
	// succeeds, mismatched provider is rejected.
	if err := s.CreateOAuthState("post-migration-state", "google"); err != nil {
		t.Fatalf("CreateOAuthState: %v", err)
	}
	if err := s.ConsumeOAuthState("post-migration-state", "github"); err == nil {
		t.Error("a state minted for google must not be consumable by github")
	}
	if err := s.ConsumeOAuthState("post-migration-state", "google"); err != nil {
		t.Errorf("a state minted for google must be consumable by google: %v", err)
	}
}
