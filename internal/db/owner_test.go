package db_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

func TestOwner_AcquireWhenFree(t *testing.T) {
	store := mustOpenDB(t)
	acquired, epoch, err := store.AcquireOwner("inst-a", 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireOwner: %v", err)
	}
	if !acquired || epoch != 1 {
		t.Fatalf("expected acquired epoch=1, got acquired=%v epoch=%d", acquired, epoch)
	}
}

func TestOwner_SecondAcquireBlockedWhileHeld(t *testing.T) {
	store := mustOpenDB(t)
	if a, _, err := store.AcquireOwner("inst-a", 30*time.Second); err != nil || !a {
		t.Fatalf("first acquire: acquired=%v err=%v", a, err)
	}
	acquired, _, err := store.AcquireOwner("inst-b", 30*time.Second)
	if err != nil {
		t.Fatalf("AcquireOwner(b): %v", err)
	}
	if acquired {
		t.Fatal("inst-b must not acquire a live lease held by inst-a")
	}
}

func TestOwner_RenewHolderSucceeds_NonHolderFails(t *testing.T) {
	store := mustOpenDB(t)
	_, epoch, err := store.AcquireOwner("inst-a", 30*time.Second)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	ok, err := store.RenewOwner("inst-a", epoch, 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("holder renew: ok=%v err=%v", ok, err)
	}
	if ok, _ := store.RenewOwner("inst-b", epoch, 30*time.Second); ok {
		t.Fatal("non-holder renew must fail")
	}
	if ok, _ := store.RenewOwner("inst-a", epoch-1, 30*time.Second); ok {
		t.Fatal("stale-epoch renew must fail")
	}
}

func TestOwner_ReleaseThenReacquireBumpsEpochAndFences(t *testing.T) {
	store := mustOpenDB(t)
	_, epoch1, err := store.AcquireOwner("inst-a", 30*time.Second)
	if err != nil {
		t.Fatalf("acquire a: %v", err)
	}
	if err := store.ReleaseOwner("inst-a", epoch1); err != nil {
		t.Fatalf("release a: %v", err)
	}
	acquired, epoch2, err := store.AcquireOwner("inst-b", 30*time.Second)
	if err != nil || !acquired {
		t.Fatalf("acquire b: acquired=%v err=%v", acquired, err)
	}
	if epoch2 <= epoch1 {
		t.Fatalf("epoch must increase on reacquire: epoch1=%d epoch2=%d", epoch1, epoch2)
	}
	if ok, _ := store.RenewOwner("inst-a", epoch1, 30*time.Second); ok {
		t.Fatal("stale holder renew must fail after takeover")
	}
	if err := store.ReleaseOwner("inst-a", epoch1); err != nil {
		t.Fatalf("stale release returned error (should be a no-op): %v", err)
	}
	owner, err := store.GetOwner()
	if err != nil {
		t.Fatalf("GetOwner: %v", err)
	}
	if owner.InstanceID != "inst-b" || owner.Epoch != epoch2 {
		t.Fatalf("stale release clobbered owner: %+v", owner)
	}
}

func TestOwner_GetOwnerNotFound(t *testing.T) {
	store := mustOpenDB(t)
	if _, err := store.GetOwner(); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("expected db.ErrNotFound on empty table, got %v", err)
	}
}

func TestOwner_ExpiredLeaseIsAcquirable(t *testing.T) {
	store := mustOpenDB(t)
	if a, _, err := store.AcquireOwner("inst-a", 1*time.Second); err != nil || !a {
		t.Fatalf("acquire a: acquired=%v err=%v", a, err)
	}
	time.Sleep(1200 * time.Millisecond)
	acquired, _, err := store.AcquireOwner("inst-b", 30*time.Second)
	if err != nil {
		t.Fatalf("acquire b: %v", err)
	}
	if !acquired {
		t.Fatal("inst-b must acquire an expired lease")
	}
}
