package backup

import (
	"os"
	"strings"
	"testing"
)

// TestPgConnEnv verifies the DSN password is moved into PGPASSWORD (never left
// in argv) and that password-less or ambient-auth DSNs are untouched.
func TestPgConnEnv(t *testing.T) {
	clean, env := pgConnEnv("postgres://alice:s3cret@db.example:5432/shiny?sslmode=disable")
	if strings.Contains(clean, "s3cret") {
		t.Errorf("clean DSN still contains password: %q", clean)
	}
	if want := "postgres://alice@db.example:5432/shiny?sslmode=disable"; clean != want {
		t.Errorf("clean DSN = %q, want %q", clean, want)
	}
	found := false
	for _, e := range env {
		if e == "PGPASSWORD=s3cret" {
			found = true
		}
	}
	if !found {
		t.Errorf("PGPASSWORD=s3cret not present in env")
	}

	// A password-less URI relies on ambient auth (.pgpass / peer / trust):
	// pass it through unchanged and add nothing to the inherited environment.
	clean2, env2 := pgConnEnv("postgres://alice@db.example/shiny")
	if clean2 != "postgres://alice@db.example/shiny" {
		t.Errorf("password-less DSN changed: %q", clean2)
	}
	if len(env2) != len(os.Environ()) {
		t.Errorf("password-less DSN altered the environment: %d entries vs %d", len(env2), len(os.Environ()))
	}
}
