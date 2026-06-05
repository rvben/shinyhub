package db

import "testing"

func TestIsPostgresDSN(t *testing.T) {
	pg := []string{
		"postgres://u:p@localhost:5432/db",
		"postgresql://u:p@localhost/db?sslmode=disable",
	}
	notPG := []string{
		":memory:",
		"file:test.db?mode=memory",
		"./data/shinyhub.db",
		"data/shinyhub.db",
	}
	for _, d := range pg {
		if !isPostgresDSN(d) {
			t.Errorf("isPostgresDSN(%q) = false, want true", d)
		}
	}
	for _, d := range notPG {
		if isPostgresDSN(d) {
			t.Errorf("isPostgresDSN(%q) = true, want false", d)
		}
	}
}
