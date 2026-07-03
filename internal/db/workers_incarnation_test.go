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
