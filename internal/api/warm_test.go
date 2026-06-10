package api

import (
	"context"
	"io"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

// recordingRuntime wraps any Runtime and records the PID of every SIGTERM
// delivered via Signal. Calls are serialized by mu so the slice is safe to
// read from the test goroutine after WarmShrink returns.
type recordingRuntime struct {
	inner process.Runtime
	mu    sync.Mutex
	pids  []int // PIDs in the order Signal was called
}

func (r *recordingRuntime) Signal(h process.RunHandle, sig syscall.Signal) error {
	if sig == syscall.SIGTERM {
		r.mu.Lock()
		r.pids = append(r.pids, h.PID)
		r.mu.Unlock()
	}
	return r.inner.Signal(h, sig)
}
func (r *recordingRuntime) Start(ctx context.Context, p process.StartParams, w io.Writer) (process.ReplicaEndpoint, error) {
	return r.inner.Start(ctx, p, w)
}
func (r *recordingRuntime) Wait(ctx context.Context, h process.RunHandle) error {
	return r.inner.Wait(ctx, h)
}
func (r *recordingRuntime) Stats(ctx context.Context, h process.RunHandle) (float64, uint64, error) {
	return r.inner.Stats(ctx, h)
}
func (r *recordingRuntime) RunOnce(ctx context.Context, p process.StartParams, w io.Writer) (process.ExitInfo, error) {
	return r.inner.RunOnce(ctx, p, w)
}
func (r *recordingRuntime) HostPreparesDeps() bool    { return r.inner.HostPreparesDeps() }
func (r *recordingRuntime) AppBindHost() string       { return r.inner.AppBindHost() }
func (r *recordingRuntime) HostProvidesAppData() bool { return r.inner.HostProvidesAppData() }

// stoppedPIDs returns the recorded PIDs, safe to call after WarmShrink returns.
func (r *recordingRuntime) stoppedPIDs() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int, len(r.pids))
	copy(out, r.pids)
	return out
}

// TestWarmShrink_DrainsToFloor proves WarmShrink drains every running replica
// above the floor (indices 1 and 2) in descending index order, deregisters
// their proxy slots, marks the rows stopped/warm, leaves the floor replica
// untouched, does NOT change app.Replicas, and records exactly one audit event
// with the correct from/to values.
func TestWarmShrink_DrainsToFloor(t *testing.T) {
	srv, app := newScaleTestServer(t, "demo", 3, &config.Config{})

	// Replace the default tier's runtime with a recording wrapper so we can
	// observe the order in which StopReplica delivers SIGTERM.
	rec := &recordingRuntime{inner: process.NewNativeRuntime()}
	srv.manager.RegisterRuntime("default", rec)

	srv.proxy.SetPoolSize("demo", 3)
	for i := 0; i < 3; i++ {
		if err := srv.proxy.RegisterReplica("demo", i, "http://127.0.0.1:"+itoa10(9000+i), nil, 0); err != nil {
			t.Fatalf("register replica %d: %v", i, err)
		}
	}

	// Start real processes at victim indices and record their PIDs so we can
	// map recorded SIGTERM PIDs back to replica indices.
	pidToIndex := map[int]int{}
	for _, idx := range []int{1, 2} {
		info, err := srv.manager.Start(process.StartParams{
			Slug:    "demo",
			Index:   idx,
			Tier:    "default",
			Dir:     t.TempDir(),
			Command: []string{"sleep", "30"},
			Port:    19400 + idx,
		})
		if err != nil {
			t.Fatalf("seed process %d: %v", idx, err)
		}
		pidToIndex[info.PID] = idx
	}

	shrunk, err := srv.WarmShrink("demo", 1, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("WarmShrink: %v", err)
	}
	if !shrunk {
		t.Fatal("WarmShrink returned false; want true (2 victims above floor)")
	}

	// Descending stop order: index 2 must be stopped before index 1.
	stopped := rec.stoppedPIDs()
	var stoppedIndices []int
	for _, pid := range stopped {
		if idx, ok := pidToIndex[pid]; ok {
			stoppedIndices = append(stoppedIndices, idx)
		}
	}
	if len(stoppedIndices) != 2 {
		t.Fatalf("expected 2 stop calls; got %d (indices %v)", len(stoppedIndices), stoppedIndices)
	}
	if stoppedIndices[0] != 2 || stoppedIndices[1] != 1 {
		t.Errorf("stop order = %v; want [2 1] (descending)", stoppedIndices)
	}

	// app.Replicas must not change — warm state is runtime, not config.
	got, err := srv.store.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Replicas != 3 {
		t.Errorf("app.Replicas = %d; want 3 (WarmShrink must not change configured capacity)", got.Replicas)
	}

	// Replica rows: victims stopped/warm, floor untouched.
	reps, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reps {
		switch r.Index {
		case 0:
			if r.Status != "running" || r.DesiredState != "running" {
				t.Errorf("floor replica 0: status=%s desired_state=%s; want running/running", r.Status, r.DesiredState)
			}
		case 1, 2:
			if r.Status != "stopped" {
				t.Errorf("victim replica %d: status=%s; want stopped", r.Index, r.Status)
			}
			if r.DesiredState != db.ReplicaDesiredWarm {
				t.Errorf("victim replica %d: desired_state=%s; want %q", r.Index, r.DesiredState, db.ReplicaDesiredWarm)
			}
		}
	}

	// Proxy slots for victims must be deregistered.
	for _, idx := range []int{1, 2} {
		if url := srv.proxy.ReplicaTargetURL("demo", idx); url != "" {
			t.Errorf("proxy slot %d still has URL %q after WarmShrink; want empty", idx, url)
		}
	}
	// Floor slot must still be live.
	if url := srv.proxy.ReplicaTargetURL("demo", 0); url == "" {
		t.Errorf("proxy slot 0 deregistered; want it still live (floor)")
	}

	// One audit event with action warm_shrink and correct from/to.
	events, err := srv.store.ListAuditEvents("warm_shrink", 10, 0)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("audit events count = %d; want 1", len(events))
	}
	ev := events[0]
	if ev.ResourceType != "app" || ev.ResourceID != "demo" {
		t.Errorf("audit event resource = %s/%s; want app/demo", ev.ResourceType, ev.ResourceID)
	}
	if !strings.Contains(ev.Detail, `"from":3`) {
		t.Errorf("audit detail %q does not contain \"from\":3", ev.Detail)
	}
	if !strings.Contains(ev.Detail, `"to":1`) {
		t.Errorf("audit detail %q does not contain \"to\":1", ev.Detail)
	}
}

// TestWarmShrink_NothingAboveFloor proves WarmShrink returns (false, nil) and
// records no audit event when every running replica is already at or below the
// floor.
func TestWarmShrink_NothingAboveFloor(t *testing.T) {
	srv, _ := newScaleTestServer(t, "demo", 1, &config.Config{})
	srv.proxy.SetPoolSize("demo", 1)

	shrunk, err := srv.WarmShrink("demo", 1, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("WarmShrink: %v", err)
	}
	if shrunk {
		t.Error("WarmShrink returned true; want false (nothing above floor 1)")
	}

	events, err := srv.store.ListAuditEvents("warm_shrink", 10, 0)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("audit events = %d; want 0 (no-op must not audit)", len(events))
	}
}

// TestWarmShrink_FloorClampsToReplicas proves that a floor larger than the
// app's configured replica count is clamped to that count, making the call a
// no-op rather than stopping everything.
func TestWarmShrink_FloorClampsToReplicas(t *testing.T) {
	srv, _ := newScaleTestServer(t, "demo", 2, &config.Config{})
	srv.proxy.SetPoolSize("demo", 2)

	// Floor 5 > replicas 2 => effective floor = 2; no replica is above index 2.
	shrunk, err := srv.WarmShrink("demo", 5, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("WarmShrink: %v", err)
	}
	if shrunk {
		t.Error("WarmShrink returned true; want false (effective floor clamps to replica count)")
	}

	got, _ := srv.store.GetAppBySlug("demo")
	if got.Replicas != 2 {
		t.Errorf("app.Replicas = %d; want 2 untouched", got.Replicas)
	}
}

// TestWarmShrink_SkipsAlreadyStoppedVictims proves that a replica already
// stopped/warm from a prior shrink cycle is not touched again: WarmShrink only
// drains and stops replicas that are currently running above the floor.
func TestWarmShrink_SkipsAlreadyStoppedVictims(t *testing.T) {
	srv, app := newScaleTestServer(t, "demo", 3, &config.Config{})

	// Override row 1 to stopped/warm to simulate a prior shrink cycle.
	if err := srv.store.UpsertReplica(db.UpsertReplicaParams{
		AppID:        app.ID,
		Index:        1,
		Status:       "stopped",
		DesiredState: db.ReplicaDesiredWarm,
	}); err != nil {
		t.Fatalf("seed stopped/warm replica 1: %v", err)
	}

	srv.proxy.SetPoolSize("demo", 3)
	// Only register live backends for the floor (0) and the running victim (2).
	// Index 1 is already stopped so it has no backend.
	for _, idx := range []int{0, 2} {
		if err := srv.proxy.RegisterReplica("demo", idx, "http://127.0.0.1:"+itoa10(9000+idx), nil, 0); err != nil {
			t.Fatalf("register replica %d: %v", idx, err)
		}
	}
	if _, err := srv.manager.Start(process.StartParams{
		Slug: "demo", Index: 2, Dir: t.TempDir(),
		Command: []string{"sleep", "30"}, Port: 19200,
	}); err != nil {
		t.Fatalf("seed process 2: %v", err)
	}

	shrunk, err := srv.WarmShrink("demo", 1, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("WarmShrink: %v", err)
	}
	if !shrunk {
		t.Fatal("WarmShrink returned false; want true (index 2 is a running victim)")
	}

	reps, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reps {
		switch r.Index {
		case 0:
			if r.Status != "running" || r.DesiredState != "running" {
				t.Errorf("floor replica 0: status=%s desired_state=%s; want running/running", r.Status, r.DesiredState)
			}
		case 1:
			// Must remain exactly as seeded (stopped/warm).
			if r.Status != "stopped" || r.DesiredState != db.ReplicaDesiredWarm {
				t.Errorf("replica 1: status=%s desired_state=%s; want stopped/%s (must not be re-touched)", r.Status, r.DesiredState, db.ReplicaDesiredWarm)
			}
		case 2:
			if r.Status != "stopped" || r.DesiredState != db.ReplicaDesiredWarm {
				t.Errorf("victim replica 2: status=%s desired_state=%s; want stopped/%s", r.Status, r.DesiredState, db.ReplicaDesiredWarm)
			}
		}
	}
}

// TestWarmShrink_HoldsDeployLock proves WarmShrink holds the per-slug deploy
// lock for the entire duration of the operation. A blocking drain stub keeps
// WarmShrink inside the lock; while blocked, TryLock must fail. After unblocking,
// TryLock must succeed.
func TestWarmShrink_HoldsDeployLock(t *testing.T) {
	srv, _ := newScaleTestServer(t, "demo", 2, &config.Config{})

	// A drain-blocking runtime: Signal succeeds but the process never exits
	// until we close the unblock channel, so waitForDrain spins until grace.
	// We use a very short grace so the test does not stall, but we observe the
	// lock state before unblocking by intercepting Signal.
	signaled := make(chan struct{}, 1)
	unblock := make(chan struct{})

	blockingRT := &blockOnWaitRuntime{
		inner:    process.NewNativeRuntime(),
		signaled: signaled,
		unblock:  unblock,
	}
	srv.manager.RegisterRuntime("spy", blockingRT)

	// Start the victim on the spy tier.
	srv.proxy.SetPoolSize("demo", 2)
	if err := srv.proxy.RegisterReplica("demo", 1, "http://127.0.0.1:19302", nil, 0); err != nil {
		t.Fatalf("register replica 1: %v", err)
	}
	_, err := srv.manager.Start(process.StartParams{
		Slug: "demo", Index: 1, Tier: "spy", Dir: t.TempDir(),
		Command: []string{"sleep", "30"}, Port: 19302,
	})
	if err != nil {
		t.Fatalf("seed victim process: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Use a short grace so WarmShrink does not block forever after we
		// unblock the spy runtime.
		srv.WarmShrink("demo", 1, 50*time.Millisecond) //nolint:errcheck
	}()

	// Wait until Signal has been called (WarmShrink is inside waitForDrain,
	// holding the deploy lock).
	select {
	case <-signaled:
	case <-time.After(5 * time.Second):
		t.Fatal("WarmShrink never called Signal; lock observation window missed")
	}

	// Deploy lock must be held by WarmShrink right now.
	mu := srv.deployLockFor("demo")
	if mu.TryLock() {
		mu.Unlock()
		t.Error("TryLock succeeded while WarmShrink should be holding the deploy lock")
	}

	// Unblock the spy runtime so WarmShrink can finish.
	close(unblock)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WarmShrink goroutine did not finish within timeout")
	}

	// After completion the lock must be released.
	if !mu.TryLock() {
		t.Error("TryLock failed after WarmShrink completed; lock was not released")
	} else {
		mu.Unlock()
	}
}

// blockOnWaitRuntime wraps a real runtime but blocks Wait until unblock is
// closed. Signal sends on signaled (non-blocking, capacity 1) so the test can
// observe when Signal was called. This pins WarmShrink inside waitForDrain,
// ensuring the deploy lock is observable as held.
type blockOnWaitRuntime struct {
	inner    process.Runtime
	signaled chan<- struct{}
	unblock  <-chan struct{}
}

func (r *blockOnWaitRuntime) Signal(h process.RunHandle, sig syscall.Signal) error {
	err := r.inner.Signal(h, sig)
	// Non-blocking send: only the first SIGTERM notification matters.
	select {
	case r.signaled <- struct{}{}:
	default:
	}
	return err
}
func (r *blockOnWaitRuntime) Wait(ctx context.Context, h process.RunHandle) error {
	select {
	case <-r.unblock:
	case <-ctx.Done():
	}
	return r.inner.Wait(ctx, h)
}
func (r *blockOnWaitRuntime) Start(ctx context.Context, p process.StartParams, w io.Writer) (process.ReplicaEndpoint, error) {
	return r.inner.Start(ctx, p, w)
}
func (r *blockOnWaitRuntime) Stats(ctx context.Context, h process.RunHandle) (float64, uint64, error) {
	return r.inner.Stats(ctx, h)
}
func (r *blockOnWaitRuntime) RunOnce(ctx context.Context, p process.StartParams, w io.Writer) (process.ExitInfo, error) {
	return r.inner.RunOnce(ctx, p, w)
}
func (r *blockOnWaitRuntime) HostPreparesDeps() bool    { return r.inner.HostPreparesDeps() }
func (r *blockOnWaitRuntime) AppBindHost() string       { return r.inner.AppBindHost() }
func (r *blockOnWaitRuntime) HostProvidesAppData() bool { return r.inner.HostProvidesAppData() }

// itoa10 converts a non-negative int to its decimal string representation.
func itoa10(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
