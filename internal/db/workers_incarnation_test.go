package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestWorkerIncarnation_NewWorkerStartsAtOne(t *testing.T) {
	store := dbtest.New(t)
	if err := store.UpsertWorker(db.Worker{NodeID: "n1", Tier: "default", AdvertiseAddr: "203.0.113.1:9000", Status: "joining"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	w, err := store.GetWorker("n1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if w.Incarnation != 1 {
		t.Fatalf("incarnation = %d, want 1 for a new worker", w.Incarnation)
	}
}

func TestReapWorker_BumpsIncarnationAndMarksDown(t *testing.T) {
	store := dbtest.New(t)
	_ = store.UpsertWorker(db.Worker{NodeID: "n1", Tier: "default", AdvertiseAddr: "203.0.113.1:9000", Status: "up"})
	if err := store.ReapWorker("n1"); err != nil {
		t.Fatalf("reap: %v", err)
	}
	w, _ := store.GetWorker("n1")
	if w.Status != "down" || w.Incarnation != 2 {
		t.Fatalf("after reap status=%s incarnation=%d, want down/2", w.Status, w.Incarnation)
	}
}

func TestRevokeWorker_BumpsIncarnation(t *testing.T) {
	store := dbtest.New(t)
	_ = store.UpsertWorker(db.Worker{NodeID: "n1", Tier: "default", AdvertiseAddr: "203.0.113.1:9000", Status: "up"})
	if err := store.RevokeWorker("n1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	w, _ := store.GetWorker("n1")
	if !w.Revoked() || w.Incarnation != 2 {
		t.Fatalf("after revoke revoked=%v incarnation=%d, want true/2", w.Revoked(), w.Incarnation)
	}
}
