package db_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// BenchmarkAppUsageReportHighChurn guards the report's scale behavior. The
// fixture deliberately has many retained sessions but a peak concurrency of
// one: the implementation should stream all rows while retaining only one end
// time in its heap. Run with:
//
//	go test ./internal/db -run '^$' -bench AppUsageReportHighChurn -benchmem -benchtime=3x
func BenchmarkAppUsageReportHighChurn(b *testing.B) {
	dbtest.SkipIfPostgres(b) // fixture seeding below uses SQLite's recursive CTE.
	for _, sessions := range []int{10_000, 100_000} {
		b.Run(fmt.Sprintf("sessions-%d", sessions), func(b *testing.B) {
			store := dbtest.New(b)
			if err := store.CreateUser(db.CreateUserParams{Username: "bench-owner", PasswordHash: "h", Role: "developer"}); err != nil {
				b.Fatal(err)
			}
			owner, err := store.GetUserByUsername("bench-owner")
			if err != nil {
				b.Fatal(err)
			}
			if _, err := store.CreateApp(db.CreateAppParams{Slug: "usage-bench", Name: "Usage", OwnerID: owner.ID}); err != nil {
				b.Fatal(err)
			}
			app, err := store.GetAppBySlug("usage-bench")
			if err != nil {
				b.Fatal(err)
			}
			_, err = store.DB().Exec(`WITH RECURSIVE seq(n) AS (
				SELECT 1 UNION ALL SELECT n + 1 FROM seq WHERE n < ?
			) INSERT INTO usage_sessions
				(id, app_id, principal_kind, identity_mode, policy_generation, instance_id,
				 started_at, heartbeat_at, ended_at)
			SELECT printf('bench-%d', n), ?, 'anonymous', 'unattributed', 1, 'bench',
			       datetime('now', printf('-%d seconds', (? - n) * 5 + 2)),
			       datetime('now', printf('-%d seconds', (? - n) * 5 + 1)),
			       datetime('now', printf('-%d seconds', (? - n) * 5 + 1))
			FROM seq`, sessions, app.ID, sessions, sessions, sessions)
			if err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				report, err := store.AppUsageReport(context.Background(), app.ID, 30*24*time.Hour, "unattributed", false)
				if err != nil {
					b.Fatal(err)
				}
				if report.Summary.Sessions != int64(sessions) || report.Summary.PeakConcurrentSessions != 1 {
					b.Fatalf("summary = %+v", report.Summary)
				}
			}
		})
	}
}
