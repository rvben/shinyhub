package db_test

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// Compare durable counters with an independent full-history aggregation after
// mutations, including SQL/FK writers that bypass Store's recorder methods.
func assertUsageClosedTotals(t *testing.T, store *db.Store) {
	t.Helper()
	read := func(query string) []string {
		t.Helper()
		rows, err := store.DB().Query(query)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var result []string
		for rows.Next() {
			var app, sessions, people, anonymous, service, identified, pseudo, duration int64
			var day string
			if err := rows.Scan(&app, &day, &sessions, &people, &anonymous, &service, &identified, &pseudo, &duration); err != nil {
				t.Fatal(err)
			}
			result = append(result, fmt.Sprint(app, day, sessions, people, anonymous, service, identified, pseudo, duration))
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return result
	}
	got := read(`SELECT app_id, day, sessions, person_sessions, anonymous_sessions,
		service_sessions, identified_person_sessions, pseudonymous_person_sessions, total_duration_seconds
		FROM usage_closed_daily ORDER BY app_id, day`)
	want := read(`SELECT app_id, substr(CAST(started_at AS TEXT), 1, 10), COUNT(*),
		SUM(principal_kind = 'person'), SUM(principal_kind = 'anonymous'),
		SUM(principal_kind = 'service_account'),
		SUM(principal_kind = 'person' AND identity_mode = 'identified' AND user_id IS NOT NULL),
		SUM(principal_kind = 'person' AND identity_mode = 'pseudonymous' AND viewer_key IS NOT NULL),
		COALESCE(SUM(MAX(0, unixepoch(substr(CAST(ended_at AS TEXT), 1, 19)) -
			unixepoch(substr(CAST(started_at AS TEXT), 1, 19)))), 0)
		FROM usage_sessions WHERE ended_at IS NOT NULL GROUP BY 1, 2 ORDER BY 1, 2`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("closed totals = %v; raw history = %v", got, want)
	}
}

func TestUsageClosedTotalsTrackEveryWriter(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "closed-owner", "developer")
	viewer := mustCreateUser(t, store, "closed-viewer", "viewer")
	app := mustCreateApp(t, store, "closed-counts", owner.ID)
	other := mustCreateApp(t, store, "closed-other", owner.ID)
	day := time.Now().UTC().Truncate(24 * time.Hour).Add(-72 * time.Hour)
	for i := 0; i < 30; i++ {
		start := day.Add(time.Duration(i) * 5 * time.Hour)
		id := fmt.Sprintf("closed-%d", i)
		if err := store.BeginUsageSession(db.UsageSessionStart{ID: id, Slug: app.Slug, InstanceID: "test", UserID: viewer.ID, StartedAt: start}); err != nil {
			t.Fatal(err)
		}
		assertUsageClosedTotals(t, store)
		// Includes late closures across UTC days and sessions still open now.
		if i%3 != 0 {
			if err := store.EndUsageSession(id, start.Add(25*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if err := store.EndUsageSession(id, start.Add(26*time.Hour)); err != nil {
				t.Fatal(err)
			}
			assertUsageClosedTotals(t, store)
		}
	}
	for _, mutation := range []struct {
		query string
		args  []any
	}{
		{`UPDATE usage_sessions SET app_id = ?, started_at = ? WHERE id = 'closed-1'`, []any{other.ID, day.Add(-time.Hour)}},
		{`UPDATE usage_sessions SET user_id = NULL, identity_mode = 'pseudonymous', viewer_key = 'synthetic-key' WHERE id = 'closed-2'`, nil},
		{`UPDATE usage_sessions SET user_id = NULL, viewer_key = NULL, identity_mode = 'unattributed' WHERE id = 'closed-4'`, nil},
		{`UPDATE usage_sessions SET ended_at = NULL WHERE id = 'closed-5'`, nil},
		{`UPDATE usage_sessions SET heartbeat_at = ? WHERE ended_at IS NULL`, []any{time.Now().UTC()}},
		{`UPDATE usage_sessions SET ended_at = heartbeat_at WHERE ended_at IS NULL`, nil},
		{`DELETE FROM usage_sessions WHERE id = 'closed-1'`, nil},
	} {
		if _, err := store.DB().Exec(mutation.query, mutation.args...); err != nil {
			t.Fatal(err)
		}
		assertUsageClosedTotals(t, store)
	}
	if err := store.DeleteUser(viewer.ID); err != nil {
		t.Fatal(err)
	}
	assertUsageClosedTotals(t, store)
	// dbtest's in-memory SQLite database is pinned to one connection.
	for _, query := range []string{"BEGIN", "DELETE FROM usage_sessions", "ROLLBACK"} {
		if _, err := store.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	assertUsageClosedTotals(t, store)
	if _, err := store.DB().Exec(`DELETE FROM apps WHERE id = ?`, app.ID); err != nil {
		t.Fatal(err)
	}
	assertUsageClosedTotals(t, store)
}

func TestUsageClosedTotalsBackfillAndRetention(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	store := mustOpenDB(t)
	owner := mustCreateUser(t, store, "backfill-owner", "developer")
	app := mustCreateApp(t, store, "backfill-counts", owner.ID)
	// Recreate the preceding schema, then seed history before upgrading.
	for _, query := range []string{
		`DROP TRIGGER usage_closed_insert`, `DROP TRIGGER usage_closed_update`, `DROP TRIGGER usage_closed_delete`,
		`DROP TABLE usage_closed_daily`, `DROP INDEX idx_usage_sessions_open_app_started`,
		`DELETE FROM schema_migrations WHERE version = 77`,
	} {
		if _, err := store.DB().Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now().UTC().Add(-100 * 24 * time.Hour)
	if err := store.BeginUsageSession(db.UsageSessionStart{ID: "backfill", Slug: app.Slug, InstanceID: "test", StartedAt: start}); err != nil {
		t.Fatal(err)
	}
	if err := store.EndUsageSession("backfill", start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	assertUsageClosedTotals(t, store)
	// Recovering a lost ledger entry must not backfill the same rows twice.
	if _, err := store.DB().Exec(`DELETE FROM schema_migrations WHERE version = 77`); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	assertUsageClosedTotals(t, store)
	before, err := store.AppUsageReport(context.Background(), app.ID, 120*24*time.Hour, "unattributed", false)
	if err != nil {
		t.Fatal(err)
	}
	if before.Summary.Sessions != 1 || before.Summary.TotalDurationSeconds != 3600 {
		t.Fatalf("backfilled report = %+v", before.Summary)
	}
	if _, err := store.PruneUsageSessions(90 * 24 * time.Hour); err != nil {
		t.Fatal(err)
	}
	assertUsageClosedTotals(t, store)
	after, err := store.AppUsageReport(context.Background(), app.ID, 120*24*time.Hour, "unattributed", false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("retention changed report:\nbefore: %+v\nafter: %+v", before, after)
	}
}

// A closure moves a row between the two query inputs. Readers must see it in
// exactly one input even when another database connection commits that move.
func TestUsageClosedTotalsConcurrentClosures(t *testing.T) {
	dbtest.SkipIfPostgres(t)
	path := filepath.Join(t.TempDir(), "usage.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	writer, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	owner := mustCreateUser(t, store, "concurrent-owner", "developer")
	app := mustCreateApp(t, store, "concurrent-counts", owner.ID)
	start := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	const count = 200
	for i := 0; i < count; i++ {
		if err := store.BeginUsageSession(db.UsageSessionStart{ID: fmt.Sprint(i), Slug: app.Slug, InstanceID: "test", StartedAt: start}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.DB().Exec(`UPDATE usage_sessions SET heartbeat_at = ?`, start.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < count; i++ {
			if err := writer.EndUsageSession(fmt.Sprint(i), start.Add(time.Second)); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for {
		report, err := store.AppUsageReport(context.Background(), app.ID, 48*time.Hour, "unattributed", false)
		if err != nil {
			t.Fatal(err)
		}
		if report.Summary.Sessions != count || report.Summary.TotalDurationSeconds != count {
			t.Fatalf("closure lost or duplicated a contribution: %+v", report.Summary)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			assertUsageClosedTotals(t, store)
			return
		default:
		}
	}
}
