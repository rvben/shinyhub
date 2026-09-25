package db_test

import (
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestPostgresSessionTimeZoneIsUTC pins every connection to UTC no matter what
// the server or the DSN asks for. The store's contract is that instants are UTC
// end to end, matching SQLite's zone-less UTC text. A zone-less literal bound
// into a timestamptz column, a ::date cast and the text rendering of a
// timestamptz are all resolved in the session time zone, so on a server running
// in Europe/Amsterdam they would otherwise shift by an hour or two.
func TestPostgresSessionTimeZoneIsUTC(t *testing.T) {
	_, dsn := dbtest.NewPostgres(t)
	separator := "?"
	if strings.ContainsRune(dsn, '?') {
		separator = "&"
	}

	cases := []struct{ name, dsn string }{
		{"server default", dsn},
		{"dsn asks for Amsterdam", dsn + separator + "timezone=Europe/Amsterdam"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := db.Open(tc.dsn)
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			t.Cleanup(func() { _ = store.Close() })

			var zone string
			if err := store.DB().QueryRow(`SHOW TimeZone`).Scan(&zone); err != nil {
				t.Fatalf("show time zone: %v", err)
			}
			if zone != "UTC" {
				t.Fatalf("session TimeZone = %q, want UTC", zone)
			}

			// The behaviour the setting exists for: a zone-less literal names the
			// same instant as the explicit UTC one.
			var same bool
			if err := store.DB().QueryRow(
				`SELECT '2026-01-02 03:04:05'::timestamptz = '2026-01-02 03:04:05+00'::timestamptz`,
			).Scan(&same); err != nil {
				t.Fatalf("compare literals: %v", err)
			}
			if !same {
				t.Fatal("zone-less timestamp literal was not read as UTC")
			}
		})
	}
}
