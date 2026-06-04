package db_test

import (
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
