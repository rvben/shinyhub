package jobs

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
)

// Terminal rows can precede deferred slot/queue cleanup. Recovery waits for the
// execution slot instead of recording a skipped_overlap row in this window.
func TestRefreshAdmissionResidualSlotsAreCancellable(t *testing.T) {
	slot := newSchedLock()
	if !slot.tryLock() {
		t.Fatal("acquire residual slot")
	}
	queue := make(chan struct{}, 1)
	queue <- struct{}{}
	manager := &Manager{locks: map[int64]*schedLock{1: slot}, queues: map[int64]chan struct{}{1: queue}}
	gate := &sync.RWMutex{}
	gate.RLock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	id, err := manager.runRefresh(ctx, &db.Schedule{ID: 1}, &db.App{}, &db.Deployment{}, nil, gate, &refreshAdmission{})
	if id != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("id=%d error=%v", id, err)
	}
	slot.unlock()
	if !gate.TryLock() {
		t.Fatal("cancelled recovery retained producer gate")
	}
	gate.Unlock()
}
