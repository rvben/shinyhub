package leader

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/dbtest"
)

// fakeStore is an in-memory OwnerStore. A single holder at a time; epoch bumps
// on each acquire. renewErr/acquireErr inject transient failures.
type fakeStore struct {
	mu         sync.Mutex
	holder     string
	epoch      int64
	renewErr   error
	acquireErr error
}

func (f *fakeStore) AcquireOwner(id string, _ time.Duration) (bool, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.acquireErr != nil {
		return false, 0, f.acquireErr
	}
	// A live lease is held by anyone - match the real DB WHERE clause
	// (instance_id IS NULL OR expires_at <= now). Expiry/re-acquire of one's
	// own lease is covered by internal/db/owner_test.go.
	if f.holder != "" {
		return false, 0, nil
	}
	f.holder = id
	f.epoch++
	return true, f.epoch, nil
}

func (f *fakeStore) RenewOwner(id string, epoch int64, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.renewErr != nil {
		return false, f.renewErr
	}
	return f.holder == id && f.epoch == epoch, nil
}

func (f *fakeStore) ReleaseOwner(id string, epoch int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.holder == id && f.epoch == epoch {
		f.holder = ""
	}
	return nil
}

func (f *fakeStore) takeover(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holder = id
	f.epoch++
}

func TestElector_AcquiresAndFiresOnAcquire(t *testing.T) {
	fs := &fakeStore{}
	var gotEpoch atomic.Int64
	e := New(fs, Config{
		InstanceID: "a", TTL: time.Second, RenewEvery: time.Millisecond,
		OnAcquire: func(epoch int64) { gotEpoch.Store(epoch) },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)

	// Wait until the callback has fired (gotEpoch written) rather than just
	// until IsOwner() flips - the set and the callback are two distinct steps.
	waitFor(t, func() bool { return gotEpoch.Load() != 0 })
	if v := gotEpoch.Load(); v != 1 {
		t.Fatalf("OnAcquire epoch: got %d want 1", v)
	}
}

func TestElector_LosingOwnershipFiresOnLose(t *testing.T) {
	fs := &fakeStore{}
	var lost atomic.Int32
	e := New(fs, Config{
		InstanceID: "a", TTL: time.Second, RenewEvery: time.Millisecond,
		OnLose: func() { lost.Store(1) },
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	waitFor(t, func() bool { return e.IsOwner() })

	fs.takeover("b") // another instance steals it; next renew returns !ok
	// Wait until the OnLose callback has fired, not just until IsOwner() flips -
	// the state update and the callback run in the same step but are two distinct
	// operations that a scheduler could interleave with the test goroutine.
	waitFor(t, func() bool { return lost.Load() == 1 })
	if lost.Load() != 1 {
		t.Fatal("expected OnLose to fire when ownership is lost")
	}
}

func TestElector_RenewErrorKeepsOwnership(t *testing.T) {
	fs := &fakeStore{}
	e := New(fs, Config{InstanceID: "a", TTL: time.Second, RenewEvery: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	waitFor(t, func() bool { return e.IsOwner() })

	fs.mu.Lock()
	fs.renewErr = errors.New("db blip")
	fs.mu.Unlock()
	time.Sleep(20 * time.Millisecond) // several renew ticks fail transiently
	if !e.IsOwner() {
		t.Fatal("a transient renew error must not drop ownership")
	}
}

func TestElector_ReleasesOnContextCancel(t *testing.T) {
	fs := &fakeStore{}
	var lost atomic.Int32
	e := New(fs, Config{InstanceID: "a", TTL: time.Second, RenewEvery: time.Millisecond,
		OnLose: func() { lost.Store(1) }})
	ctx, cancel := context.WithCancel(context.Background())
	go e.Run(ctx)
	waitFor(t, func() bool { return e.IsOwner() })

	cancel()
	waitFor(t, func() bool {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		return fs.holder == ""
	})
	waitFor(t, func() bool { return lost.Load() == 1 })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}

func TestClampTTL(t *testing.T) {
	cases := []struct {
		name             string
		ttl, renew, want time.Duration
	}{
		{"already-2x", 30 * time.Second, 10 * time.Second, 30 * time.Second},
		{"exactly-2x", 20 * time.Second, 10 * time.Second, 20 * time.Second},
		{"below-2x-raised", 15 * time.Second, 10 * time.Second, 20 * time.Second},
		{"zero-raised", 0, 5 * time.Second, 10 * time.Second},
		{"negative-ttl-raised", -5 * time.Second, 10 * time.Second, 20 * time.Second},
		{"nonpositive-renew-unchanged", 5 * time.Second, 0, 5 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clampTTL(c.ttl, c.renew); got != c.want {
				t.Fatalf("clampTTL(%v, %v) = %v, want %v", c.ttl, c.renew, got, c.want)
			}
		})
	}
}

func TestElector_EpochReflectsOwnership(t *testing.T) {
	fs := &fakeStore{}
	e := New(fs, Config{InstanceID: "a", TTL: time.Second, RenewEvery: time.Millisecond})
	if e.Epoch() != 0 {
		t.Fatalf("non-owner Epoch = %d, want 0", e.Epoch())
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	waitFor(t, func() bool { return e.Epoch() != 0 })
	if e.Epoch() != 1 {
		t.Fatalf("owner Epoch = %d, want 1", e.Epoch())
	}
}

func TestElector_RealStoreSingleOwnerHandoff(t *testing.T) {
	store := dbtest.New(t)

	// Compile-time proof the adapter satisfies the interface.
	var _ OwnerStore = store

	a := New(store, Config{InstanceID: "a", TTL: time.Second, RenewEvery: 5 * time.Millisecond})
	b := New(store, Config{InstanceID: "b", TTL: time.Second, RenewEvery: 5 * time.Millisecond})
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	go a.Run(ctxA)
	go b.Run(ctxB)

	// Exactly one owner settles.
	waitFor(t, func() bool { return a.IsOwner() != b.IsOwner() })

	// Cancel whichever owns; the other must take over.
	if a.IsOwner() {
		cancelA()
		waitFor(t, func() bool { return b.IsOwner() })
	} else {
		cancelB()
		waitFor(t, func() bool { return a.IsOwner() })
	}
}
