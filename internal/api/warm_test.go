package api

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
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

// fakeBootRuntime is a minimal Runtime whose Start succeeds and records which
// indices were booted. It also supports configuring a per-index failure so
// partial-failure tests can be exercised without real OS processes.
type fakeBootRuntime struct {
	mu          sync.Mutex
	boostedPIDs []int
	nextPID     int
	failIndex   int // if >= 0, Start returns an error when p.Index == failIndex
}

func (r *fakeBootRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failIndex >= 0 && p.Index == r.failIndex {
		return process.ReplicaEndpoint{}, fmt.Errorf("simulated boot failure for index %d", p.Index)
	}
	r.nextPID++
	pid := 70000 + r.nextPID
	r.boostedPIDs = append(r.boostedPIDs, pid)
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "native",
		WorkerID: fmt.Sprintf("w%d", pid),
		Handle:   process.RunHandle{PID: pid},
	}, nil
}
func (r *fakeBootRuntime) Signal(_ process.RunHandle, _ syscall.Signal) error { return nil }
func (r *fakeBootRuntime) Wait(_ context.Context, _ process.RunHandle) error  { return nil }
func (r *fakeBootRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}
func (r *fakeBootRuntime) RunOnce(_ context.Context, _ process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}
func (r *fakeBootRuntime) HostPreparesDeps() bool    { return false }
func (r *fakeBootRuntime) AppBindHost() string       { return "127.0.0.1" }
func (r *fakeBootRuntime) HostProvidesAppData() bool { return false }

func (r *fakeBootRuntime) boosted() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]int, len(r.boostedPIDs))
	copy(out, r.boostedPIDs)
	return out
}

// newWarmExpandServer seeds a server with `total` replicas where indices in
// warmIndices are stopped/warm, indices in stoppedIndices are stopped/stopped,
// and the rest are running/running. A current deployment is always present.
func newWarmExpandServer(t *testing.T, slug string, total int, warmIndices, stoppedIndices []int) (*Server, *db.App, *fakeBootRuntime) {
	t.Helper()
	srv, app := newScaleTestServer(t, slug, total, &config.Config{})

	// Override the replica rows seeded by newScaleTestServer.
	warmSet := map[int]bool{}
	stoppedSet := map[int]bool{}
	for _, i := range warmIndices {
		warmSet[i] = true
	}
	for _, i := range stoppedIndices {
		stoppedSet[i] = true
	}

	dep, err := srv.store.ListDeployments(app.ID)
	if err != nil || len(dep) == 0 {
		t.Fatal("no deployments seeded")
	}
	depID := dep[0].ID
	for i := 0; i < total; i++ {
		pid, port := 1000+i, 9000+i
		params := db.UpsertReplicaParams{
			AppID:        app.ID,
			Index:        i,
			PID:          &pid,
			Port:         &port,
			Status:       "running",
			Provider:     "native",
			Tier:         "default",
			AppVersion:   "v1",
			DesiredState: "running",
			DeploymentID: &depID,
		}
		if warmSet[i] {
			params.Status = "stopped"
			params.DesiredState = db.ReplicaDesiredWarm
		} else if stoppedSet[i] {
			params.Status = "stopped"
			params.DesiredState = "stopped"
		}
		if err := srv.store.UpsertReplica(params); err != nil {
			t.Fatalf("seed replica %d: %v", i, err)
		}
	}

	rt := &fakeBootRuntime{failIndex: -1}
	srv.manager.RegisterRuntime("default", rt)

	// Replace deployReplica with a fake that drives the fakeBootRuntime and
	// returns a Result that mirrors what a real boot would produce.
	srv.deployReplica = func(p deploy.Params, index int) (*deploy.Result, error) {
		ep, err := rt.Start(context.Background(), process.StartParams{
			Slug:  slug,
			Index: index,
			Port:  9000 + index,
			Dir:   p.BundleDir,
		}, io.Discard)
		if err != nil {
			return nil, err
		}
		return &deploy.Result{
			Index:       index,
			PID:         ep.Handle.PID,
			Port:        9000 + index,
			Provider:    ep.Provider,
			Tier:        "default",
			EndpointURL: ep.URL,
			WorkerID:    ep.WorkerID,
		}, nil
	}

	return srv, app, rt
}

// TestWarmExpand_BootsWarmVictims proves WarmExpand boots all stopped/warm
// replicas back to running, writes running/running rows for each, and emits a
// warm_expand audit event with the correct from/to counts.
func TestWarmExpand_BootsWarmVictims(t *testing.T) {
	srv, app, rt := newWarmExpandServer(t, "demo", 3, []int{1, 2}, nil)

	expanded, err := srv.WarmExpand("demo")
	if err != nil {
		t.Fatalf("WarmExpand: %v", err)
	}
	if !expanded {
		t.Fatal("WarmExpand returned false; want true (2 warm victims exist)")
	}

	// Both warm indices must have been booted.
	if booted := rt.boosted(); len(booted) != 2 {
		t.Errorf("expected 2 boot calls; got %d", len(booted))
	}

	// Replica rows must be running/running.
	reps, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reps {
		if r.Status != "running" || r.DesiredState != "running" {
			t.Errorf("replica %d: status=%s desired_state=%s; want running/running", r.Index, r.Status, r.DesiredState)
		}
	}

	// Exactly one warm_expand audit event with from:1 and to:3.
	events, err := srv.store.ListAuditEvents("warm_expand", 10, 0)
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
	if !strings.Contains(ev.Detail, `"from":1`) {
		t.Errorf("audit detail %q does not contain \"from\":1", ev.Detail)
	}
	if !strings.Contains(ev.Detail, `"to":3`) {
		t.Errorf("audit detail %q does not contain \"to\":3", ev.Detail)
	}
}

// TestWarmExpand_IgnoresManualStops proves WarmExpand does not touch replicas
// with desired_state='stopped' (manual stops), returning (false, nil) and
// recording no audit event.
func TestWarmExpand_IgnoresManualStops(t *testing.T) {
	srv, _, rt := newWarmExpandServer(t, "demo", 2, nil, []int{1})

	expanded, err := srv.WarmExpand("demo")
	if err != nil {
		t.Fatalf("WarmExpand: %v", err)
	}
	if expanded {
		t.Error("WarmExpand returned true; want false (only manual stops, no warm victims)")
	}
	if booted := rt.boosted(); len(booted) != 0 {
		t.Errorf("expected 0 boot calls; got %d", len(booted))
	}
	events, err := srv.store.ListAuditEvents("warm_expand", 10, 0)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("audit events = %d; want 0 (no warm victims, no audit)", len(events))
	}
}

// TestWarmExpand_NoWarmRows proves WarmExpand returns (false, nil) when every
// replica is already running.
func TestWarmExpand_NoWarmRows(t *testing.T) {
	srv, _, rt := newWarmExpandServer(t, "demo", 3, nil, nil)

	expanded, err := srv.WarmExpand("demo")
	if err != nil {
		t.Fatalf("WarmExpand: %v", err)
	}
	if expanded {
		t.Error("WarmExpand returned true; want false (no warm rows)")
	}
	if booted := rt.boosted(); len(booted) != 0 {
		t.Errorf("expected 0 boot calls; got %d", len(booted))
	}
}

// TestWarmExpand_PartialBootFailure proves WarmExpand continues booting after a
// single replica failure. The failed victim's row is written as crashed/running
// (watchdog bait), the successful victim's row is running/running, and the
// function returns (true, err) - some capacity was restored and the error is
// surfaced.
func TestWarmExpand_PartialBootFailure(t *testing.T) {
	srv, app, rt := newWarmExpandServer(t, "demo", 3, []int{1, 2}, nil)
	rt.failIndex = 1 // lowest warm index fails

	expanded, err := srv.WarmExpand("demo")
	if err == nil {
		t.Fatal("WarmExpand returned nil error; want an error because replica 1 failed to boot")
	}
	if !expanded {
		t.Error("WarmExpand returned false; want true (replica 2 was restored)")
	}

	reps, err2 := srv.store.ListReplicas(app.ID)
	if err2 != nil {
		t.Fatal(err2)
	}
	for _, r := range reps {
		switch r.Index {
		case 0:
			if r.Status != "running" || r.DesiredState != "running" {
				t.Errorf("floor replica 0: status=%s desired_state=%s; want running/running", r.Status, r.DesiredState)
			}
		case 1:
			// Failed victim: must be watchdog bait (crashed/running).
			if r.Status != "crashed" || r.DesiredState != "running" {
				t.Errorf("failed replica 1: status=%s desired_state=%s; want crashed/running", r.Status, r.DesiredState)
			}
		case 2:
			// Successful victim: must be running.
			if r.Status != "running" || r.DesiredState != "running" {
				t.Errorf("successful replica 2: status=%s desired_state=%s; want running/running", r.Status, r.DesiredState)
			}
		}
	}
}

// TestWarmExpand_HoldsDeployLock proves WarmExpand holds the per-slug deploy
// lock for the entire operation. We install a deployReplica stub that blocks
// until we release it, then verify TryLock fails while WarmExpand is in flight
// and succeeds after it returns.
func TestWarmExpand_HoldsDeployLock(t *testing.T) {
	srv, _, _ := newWarmExpandServer(t, "demo", 2, []int{1}, nil)

	bootStarted := make(chan struct{}, 1)
	unblock := make(chan struct{})

	srv.deployReplica = func(p deploy.Params, index int) (*deploy.Result, error) {
		select {
		case bootStarted <- struct{}{}:
		default:
		}
		<-unblock
		return &deploy.Result{
			Index:       index,
			PID:         9999,
			Port:        9000 + index,
			Provider:    "native",
			Tier:        "default",
			EndpointURL: fmt.Sprintf("http://127.0.0.1:%d", 9000+index),
		}, nil
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.WarmExpand("demo") //nolint:errcheck
	}()

	select {
	case <-bootStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("WarmExpand never entered deployReplica; lock observation window missed")
	}

	mu := srv.deployLockFor("demo")
	if mu.TryLock() {
		mu.Unlock()
		t.Error("TryLock succeeded while WarmExpand should be holding the deploy lock")
	}

	close(unblock)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("WarmExpand goroutine did not finish within timeout")
	}

	if !mu.TryLock() {
		t.Error("TryLock failed after WarmExpand completed; lock was not released")
	} else {
		mu.Unlock()
	}
}
