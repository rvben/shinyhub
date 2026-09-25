package db_test

import (
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestMigrate_ConcurrentInstancesDoNotCorruptLedger reproduces OB-6: two
// processes opening the same fresh SQLite file and calling Migrate() at the
// same time (an operator accidentally starting a second `shinyhub serve`, or a
// systemd restart overlapping the old process). Postgres serializes this with
// a session advisory lock (see migrateAdvisoryLockKey in db.go); SQLite had no
// equivalent, so the two migrators raced the schema_migrations ledger: each
// read the applied set before the other committed, so both tried to create the
// same tables / insert the same ledger rows, and the loser failed with a
// low-level SQLite error instead of either waiting and finding its work
// already done, or getting an actionable error.
//
// The two Migrate() calls are released from the same closed channel to
// maximize overlap, and each goroutine records its own [start,end) interval so
// the test can prove the two calls actually overlapped in wall-clock time
// rather than happening to run back-to-back (a non-overlapping "race" test
// proves nothing).
func TestMigrate_ConcurrentInstancesDoNotCorruptLedger(t *testing.T) {
	dbtest.SkipIfPostgres(t) // OB-6 is the SQLite single-node path; Postgres already serializes via pg_advisory_lock
	path := t.TempDir() + "/race.db"

	storeA, err := db.Open(path)
	if err != nil {
		t.Fatalf("open A: %v", err)
	}
	defer storeA.Close()
	storeB, err := db.Open(path)
	if err != nil {
		t.Fatalf("open B: %v", err)
	}
	defer storeB.Close()

	type result struct {
		err        error
		start, end time.Time
	}
	results := make([]result, 2)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	run := func(i int, s *db.Store) {
		defer wg.Done()
		<-start
		results[i].start = time.Now()
		results[i].err = s.Migrate()
		results[i].end = time.Now()
	}
	go run(0, storeA)
	go run(1, storeB)
	close(start)
	wg.Wait()

	// Prove the trigger actually overlapped: goroutine 1 must have started
	// before goroutine 0 finished, or vice versa. Without this, a scheduler
	// that happened to run them back-to-back would make the assertions below
	// pass vacuously on both correct and broken code.
	overlapped := results[0].start.Before(results[1].end) && results[1].start.Before(results[0].end)
	if !overlapped {
		t.Fatalf("Migrate() calls did not overlap: A=[%s,%s] B=[%s,%s]; test does not exercise the race",
			results[0].start, results[0].end, results[1].start, results[1].end)
	}

	if results[0].err != nil || results[1].err != nil {
		t.Fatalf("concurrent Migrate() against the same fresh database: A=%v B=%v", results[0].err, results[1].err)
	}

	// Ledger integrity: every embedded migration recorded exactly once, and
	// PendingMigrations (which the loser would still see as pending if it
	// silently gave up) reports nothing outstanding.
	pending, err := storeA.PendingMigrations()
	if err != nil {
		t.Fatalf("pending after race: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending migrations after concurrent Migrate: %v", pending)
	}
	var n int
	if err := storeA.DB().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	var distinct int
	if err := storeA.DB().QueryRow(`SELECT COUNT(DISTINCT version) FROM schema_migrations`).Scan(&distinct); err != nil {
		t.Fatalf("count distinct ledger versions: %v", err)
	}
	if n != distinct {
		t.Fatalf("schema_migrations has duplicate version rows: %d rows, %d distinct versions", n, distinct)
	}
}
