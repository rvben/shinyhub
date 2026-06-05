package db

import "testing"

func TestDialectExpressions(t *testing.T) {
	sq := sqliteDialect{}
	if got := sq.rebind(`WHERE a = ?`); got != `WHERE a = ?` {
		t.Fatalf("sqlite rebind should be identity, got %q", got)
	}
	if sq.now() != "datetime('now')" {
		t.Fatalf("sqlite now() = %q", sq.now())
	}
	if got := sq.nowPlusSeconds(30); got != "datetime('now', '+30 seconds')" {
		t.Fatalf("sqlite nowPlusSeconds(30) = %q", got)
	}

	pg := pgDialect{}
	if got := pg.rebind(`WHERE a = ?`); got != `WHERE a = $1` {
		t.Fatalf("pg rebind = %q", got)
	}
	if pg.now() != "now()" {
		t.Fatalf("pg now() = %q", pg.now())
	}
	if got := pg.nowPlusSeconds(30); got != "now() + make_interval(secs => 30)" {
		t.Fatalf("pg nowPlusSeconds(30) = %q", got)
	}
}

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
