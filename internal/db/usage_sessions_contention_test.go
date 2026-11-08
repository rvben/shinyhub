package db

import (
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestPruneUsageSessionsUnderConcurrentWriter runs the usage rollup while
// another connection commits continuously, the shape of a control plane whose
// app log writer never pauses. The rollup reads its batch before it writes the
// aggregates; a transaction that opens deferred takes its read snapshot at that
// first SELECT, and once any other connection has committed, SQLite refuses the
// later write with SQLITE_BUSY_SNAPSHOT without consulting busy_timeout. The
// rollup must instead take the write lock up front so the concurrent writer
// waits for it, and that writer must still succeed afterwards.
func TestPruneUsageSessionsUnderConcurrentWriter(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "contention.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateUser(CreateUserParams{Username: "contention-owner", PasswordHash: "h", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("contention-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(CreateAppParams{Slug: "contention", Name: "Contention", OwnerID: owner.ID}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("contention")
	if err != nil {
		t.Fatal(err)
	}
	const sessions = 1000
	if _, err := store.db.Exec(`WITH RECURSIVE seq(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM seq WHERE n<?)
		INSERT INTO usage_sessions (id, app_id, principal_kind, identity_mode, instance_id, started_at, heartbeat_at, ended_at)
		SELECT CAST(n AS TEXT), ?, 'anonymous', 'unattributed', 'test',
		datetime('now', '-100 days'), datetime('now', '-100 days', '+1 hour'),
		datetime('now', '-100 days', '+1 hour') FROM seq`, sessions, app.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`CREATE TABLE contention_probe (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var writerErr atomic.Pointer[error]
	var commits atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := store.db.Exec(`INSERT INTO contention_probe DEFAULT VALUES`); err != nil {
				writerErr.Store(&err)
				return
			}
			commits.Add(1)
		}
	}()

	removed, pruneErr := store.PruneUsageSessions(90 * 24 * time.Hour)
	close(stop)
	wg.Wait()

	if pruneErr != nil {
		t.Fatalf("PruneUsageSessions under a concurrent writer (%d commits): %v", commits.Load(), pruneErr)
	}
	if removed != sessions {
		t.Fatalf("pruned %d rows, want %d", removed, sessions)
	}
	if errp := writerErr.Load(); errp != nil {
		t.Fatalf("concurrent writer failed: %v", *errp)
	}
	if commits.Load() == 0 {
		t.Fatal("the concurrent writer never committed, so the rollup was not contended")
	}
}
