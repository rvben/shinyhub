package lifecycle

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/fargate"
)

func openMemStore(t *testing.T) *db.Store {
	t.Helper()
	s, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open mem store: %v", err)
	}
	if err := s.Migrate(); err != nil {
		t.Fatalf("migrate mem store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestWorkerDeclaredGone_FargateWorkerIDReturnsFalse(t *testing.T) {
	// fargate.WorkerID is never in the workers table; workerDeclaredGone must
	// return false (not gone) so Fargate replicas are never marked lost due to
	// a missing DB row.
	store := openMemStore(t)
	got := workerDeclaredGone(store, fargate.WorkerID)
	if got {
		t.Errorf("workerDeclaredGone(%q) = true, want false: Fargate replicas must not be marked gone due to a missing worker row", fargate.WorkerID)
	}
}

func TestWorkerDeclaredGone_EmptyWorkerIDReturnsTrue(t *testing.T) {
	store := openMemStore(t)
	got := workerDeclaredGone(store, "")
	if !got {
		t.Errorf("workerDeclaredGone(\"\") = false, want true: no owner means gone")
	}
}

func TestWorkerDeclaredGone_MissingWorkerRowReturnsTrue(t *testing.T) {
	// A regular (non-fargate) workerID not in DB is treated as gone.
	store := openMemStore(t)
	got := workerDeclaredGone(store, "worker-node-not-in-db")
	if !got {
		t.Errorf("workerDeclaredGone(missing) = false, want true")
	}
}

func TestWorkerDeclaredGone_LiveWorkerReturnsFalse(t *testing.T) {
	// A worker that is "up" in the DB is not gone.
	store := openMemStore(t)
	if err := store.UpsertWorker(db.Worker{
		NodeID: "real-worker",
		Tier:   "local",
		Status: "up",
	}); err != nil {
		t.Fatalf("upsert worker: %v", err)
	}
	got := workerDeclaredGone(store, "real-worker")
	if got {
		t.Errorf("workerDeclaredGone(up worker) = true, want false")
	}
}
