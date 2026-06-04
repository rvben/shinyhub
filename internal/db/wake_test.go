package db_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestBeginWake_CASWinsOnceThenRevert(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "hibernated"}); err != nil {
		t.Fatal(err)
	}

	won, err := store.BeginWake("demo")
	if err != nil {
		t.Fatalf("BeginWake: %v", err)
	}
	if !won {
		t.Fatal("first BeginWake on a hibernated app must win")
	}
	if got, _ := store.GetAppBySlug("demo"); got.Status != "waking" {
		t.Fatalf("status after BeginWake = %q, want waking", got.Status)
	}

	// A second caller (concurrent request / other process) must lose.
	won2, err := store.BeginWake("demo")
	if err != nil {
		t.Fatalf("BeginWake #2: %v", err)
	}
	if won2 {
		t.Fatal("second BeginWake must lose while status is waking")
	}

	// Revert restores hibernated so a retry can win again.
	if err := store.AbortWake("demo"); err != nil {
		t.Fatalf("AbortWake: %v", err)
	}
	if got, _ := store.GetAppBySlug("demo"); got.Status != "hibernated" {
		t.Fatalf("status after AbortWake = %q, want hibernated", got.Status)
	}

	won3, err := store.BeginWake("demo")
	if err != nil {
		t.Fatalf("BeginWake #3: %v", err)
	}
	if !won3 {
		t.Fatal("BeginWake after AbortWake must win again")
	}
}

func TestAbortWake_NoopWhenNotWaking(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := store.AbortWake("demo"); err != nil {
		t.Fatalf("AbortWake: %v", err)
	}
	if got, _ := store.GetAppBySlug("demo"); got.Status != "running" {
		t.Fatalf("AbortWake must not touch a running app; got %q", got.Status)
	}
}

func TestBeginWake_LosesWhenNotHibernated(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "running"}); err != nil {
		t.Fatal(err)
	}
	won, err := store.BeginWake("demo")
	if err != nil {
		t.Fatalf("BeginWake: %v", err)
	}
	if won {
		t.Fatal("BeginWake on a running app must lose")
	}
}

func TestFinishWake_OnlyFromWaking(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "hibernated"}); err != nil {
		t.Fatal(err)
	}
	// Not waking yet: FinishWake must not win.
	if won, _ := store.FinishWake("demo"); won {
		t.Fatal("FinishWake on a hibernated app must not win")
	}
	// waking -> running.
	if _, err := store.BeginWake("demo"); err != nil {
		t.Fatal(err)
	}
	won, err := store.FinishWake("demo")
	if err != nil {
		t.Fatalf("FinishWake: %v", err)
	}
	if !won {
		t.Fatal("FinishWake on a waking app must win")
	}
	if got, _ := store.GetAppBySlug("demo"); got.Status != "running" {
		t.Fatalf("status after FinishWake = %q, want running", got.Status)
	}
}

func TestFinishWake_DoesNotClobberConcurrentStop(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "hibernated"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginWake("demo"); err != nil {
		t.Fatal(err)
	}
	// A concurrent stop moves the app off "waking" while the wake is mid-flight.
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "stopped"}); err != nil {
		t.Fatal(err)
	}
	won, err := store.FinishWake("demo")
	if err != nil {
		t.Fatalf("FinishWake: %v", err)
	}
	if won {
		t.Fatal("FinishWake must not win once the app left waking")
	}
	if got, _ := store.GetAppBySlug("demo"); got.Status != "stopped" {
		t.Fatalf("FinishWake clobbered a stopped app to %q", got.Status)
	}
}

func TestBeginWake_ConcurrentSingleWinner(t *testing.T) {
	store := mustOpenDB(t)
	u := mustCreateUser(t, store, "owner", "developer")
	mustCreateApp(t, store, "demo", u.ID)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "hibernated"}); err != nil {
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
			if won, _ := store.BeginWake("demo"); won {
				wins.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("concurrent BeginWake winners = %d, want exactly 1", wins.Load())
	}
}
