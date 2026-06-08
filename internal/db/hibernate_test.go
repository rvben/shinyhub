package db_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestHibernateApp_CASRunningToHibernated verifies that HibernateApp transitions
// a running app to hibernated exactly once (the second call returns false because
// the app is already hibernated, not running).
//
// Runs on SQLite always and on Postgres when SHINYHUB_TEST_POSTGRES_DSN is set,
// via the dual-backend dbtest.New helper.
func TestHibernateApp_CASRunningToHibernated(t *testing.T) {
	store := dbtest.New(t)
	u := mustCreateUser(t, store, "hib-owner", "developer")
	mustCreateApp(t, store, "hib-demo", u.ID)

	// Default app status after creation is "stopped"; advance to running.
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "hib-demo", Status: "running"}); err != nil {
		t.Fatal(err)
	}

	// First call: running -> hibernated, must win.
	won, err := store.HibernateApp("hib-demo")
	if err != nil {
		t.Fatalf("HibernateApp #1: %v", err)
	}
	if !won {
		t.Fatal("first HibernateApp on a running app must win")
	}
	got, err := store.GetAppBySlug("hib-demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "hibernated" {
		t.Fatalf("status after HibernateApp = %q, want hibernated", got.Status)
	}

	// Second call: status is already hibernated, not running; must lose (idempotent).
	won2, err := store.HibernateApp("hib-demo")
	if err != nil {
		t.Fatalf("HibernateApp #2: %v", err)
	}
	if won2 {
		t.Fatal("second HibernateApp on an already-hibernated app must lose")
	}
	// Status must remain hibernated - no regression.
	got2, err := store.GetAppBySlug("hib-demo")
	if err != nil {
		t.Fatal(err)
	}
	if got2.Status != "hibernated" {
		t.Fatalf("status after second HibernateApp = %q, want hibernated", got2.Status)
	}
}

// TestHibernateApp_LosesWhenNotRunning verifies that the WHERE status='running'
// guard in HibernateApp rejects every non-running state: the CAS must lose and
// the status must remain unchanged for each case.
//
// Runs on SQLite always and on Postgres when SHINYHUB_TEST_POSTGRES_DSN is set.
func TestHibernateApp_LosesWhenNotRunning(t *testing.T) {
	cases := []string{"stopped", "degraded", "waking", "hibernated"}
	for _, status := range cases {
		status := status
		t.Run(status, func(t *testing.T) {
			store := dbtest.New(t)
			u := mustCreateUser(t, store, "hib-nr-owner-"+status, "developer")
			mustCreateApp(t, store, "hib-nr-"+status, u.ID)
			if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "hib-nr-" + status, Status: status}); err != nil {
				t.Fatalf("set status %q: %v", status, err)
			}

			won, err := store.HibernateApp("hib-nr-" + status)
			if err != nil {
				t.Fatalf("HibernateApp on %q app: %v", status, err)
			}
			if won {
				t.Errorf("HibernateApp on a %q app must lose, got won=true", status)
			}

			// Status must be left unchanged.
			got, err := store.GetAppBySlug("hib-nr-" + status)
			if err != nil {
				t.Fatalf("GetAppBySlug after HibernateApp on %q: %v", status, err)
			}
			if got.Status != status {
				t.Errorf("status changed: got %q, want %q", got.Status, status)
			}
		})
	}
}

// TestHibernateApp_ConcurrentSingleWinner verifies that under concurrent callers
// exactly one wins the running->hibernated CAS.
func TestHibernateApp_ConcurrentSingleWinner(t *testing.T) {
	store := dbtest.New(t)
	u := mustCreateUser(t, store, "hib-conc-owner", "developer")
	mustCreateApp(t, store, "hib-conc-demo", u.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "hib-conc-demo", Status: "running"}); err != nil {
		t.Fatal(err)
	}

	const n = 16
	var wins atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if w, _ := store.HibernateApp("hib-conc-demo"); w {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if wins.Load() != 1 {
		t.Fatalf("concurrent HibernateApp winners = %d, want exactly 1", wins.Load())
	}
}
