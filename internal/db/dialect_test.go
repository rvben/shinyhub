package db

import "testing"

func TestRebindQuery_Postgres(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"none", `SELECT 1`, `SELECT 1`},
		{"one", `WHERE slug = ?`, `WHERE slug = $1`},
		{"several", `VALUES (?, ?, ?)`, `VALUES ($1, $2, $3)`},
		{"question in literal not rebound", `WHERE note = 'huh?' AND slug = ?`, `WHERE note = 'huh?' AND slug = $1`},
		{"escaped quote in literal", `WHERE note = 'it''s ok?' AND id = ?`, `WHERE note = 'it''s ok?' AND id = $1`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := rebindQuery(c.in); got != c.want {
				t.Fatalf("rebindQuery(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
