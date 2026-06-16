package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

// fakeSnapshotter wraps NativeRuntime (for the Runtime method set) and makes
// Suspend/Resume controllable, so the warm-pool freeze/thaw path can be tested
// without a real cgroup. suspendFreed dictates whether a freeze "succeeds".
type fakeSnapshotter struct {
	*process.NativeRuntime
	suspendFreed bool
}

func (f *fakeSnapshotter) Suspend(_ context.Context, _ process.RunHandle) (bool, error) {
	return f.suspendFreed, nil
}

func (f *fakeSnapshotter) Resume(_ context.Context, h process.RunHandle) (process.ReplicaEndpoint, error) {
	return process.ReplicaEndpoint{Provider: "native", Handle: h}, nil
}

// TestWarmShrink_SuspendsWhenSnapshotter proves WarmShrink freezes (rather than
// stops) a drained victim when the tier runtime can snapshot, recording the row
// as suspended/warm so a later expansion thaws it.
func TestWarmShrink_SuspendsWhenSnapshotter(t *testing.T) {
	srv, app := newScaleTestServer(t, "demo", 2, &config.Config{})
	fs := &fakeSnapshotter{NativeRuntime: process.NewNativeRuntime(), suspendFreed: true}
	srv.manager.RegisterRuntime("default", fs)

	srv.proxy.SetPoolSize("demo", 2)
	for i := 0; i < 2; i++ {
		if err := srv.proxy.RegisterReplica("demo", i, "http://127.0.0.1:"+itoa10(9000+i), nil, 0); err != nil {
			t.Fatalf("register replica %d: %v", i, err)
		}
	}
	// A real process at the victim index 1 so the Manager has an entry to suspend.
	if _, err := srv.manager.Start(process.StartParams{
		Slug: "demo", Index: 1, Tier: "default", Dir: t.TempDir(),
		Command: []string{"sleep", "30"}, Port: 19401,
	}); err != nil {
		t.Fatalf("seed process: %v", err)
	}
	t.Cleanup(func() { _ = srv.manager.StopReplica("demo", 1) }) // reap after the fake "suspend"

	shrunk, err := srv.WarmShrink("demo", 1, 200*time.Millisecond)
	if err != nil || !shrunk {
		t.Fatalf("WarmShrink = (%v, %v), want (true, nil)", shrunk, err)
	}

	reps, err := srv.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	var victim *db.Replica
	for _, r := range reps {
		if r.Index == 1 {
			victim = r
		}
	}
	if victim == nil || victim.Status != "suspended" || victim.DesiredState != db.ReplicaDesiredWarm {
		t.Fatalf("victim row = %+v, want status=suspended desired_state=%s", victim, db.ReplicaDesiredWarm)
	}
}

// TestWarmExpand_ThawsSuspendedAndColdBootsStopped proves a mixed warm pool is
// restored correctly: the suspended victim is thawed via resumeReplica (not
// re-booted) and the stopped victim is cold-booted via deployReplica.
func TestWarmExpand_ThawsSuspendedAndColdBootsStopped(t *testing.T) {
	srv, app, rt := newWarmExpandServer(t, "demo", 3, []int{1, 2}, nil)

	deps, err := srv.store.ListDeployments(app.ID)
	if err != nil || len(deps) == 0 {
		t.Fatal("no deployments")
	}
	depID := deps[0].ID
	// Re-mark index 1 as suspended/warm (frozen); index 2 stays stopped/warm (cold).
	pid, port := 1001, 9001
	if err := srv.store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 1, PID: &pid, Port: &port, Status: "suspended",
		Provider: "native", Tier: "default", AppVersion: "v1",
		DesiredState: db.ReplicaDesiredWarm, DeploymentID: &depID,
	}); err != nil {
		t.Fatalf("seed suspended victim: %v", err)
	}

	var thawed []int
	srv.resumeReplica = func(p deploy.Params, index int) (*deploy.Result, error) {
		thawed = append(thawed, index)
		url := "http://127.0.0.1:" + itoa10(9000+index)
		if regErr := srv.proxy.RegisterReplica("demo", index, url, nil, p.DeploymentID); regErr != nil {
			return nil, fmt.Errorf("fake resume: register: %w", regErr)
		}
		return &deploy.Result{Index: index, PID: 1000 + index, Port: 9000 + index, Provider: "native", Tier: "default", EndpointURL: url}, nil
	}

	expanded, err := srv.WarmExpand("demo")
	if err != nil || !expanded {
		t.Fatalf("WarmExpand = (%v, %v), want (true, nil)", expanded, err)
	}

	if len(thawed) != 1 || thawed[0] != 1 {
		t.Fatalf("thawed via resume = %v, want [1] (the suspended victim)", thawed)
	}
	// Exactly one cold boot (the stopped victim, index 2); the suspended one went
	// via resume above, not deployReplica. (boosted() records synthetic PIDs.)
	if booted := rt.boosted(); len(booted) != 1 {
		t.Fatalf("cold boots = %d, want 1 (the stopped victim only)", len(booted))
	}
	reps, _ := srv.store.ListReplicas(app.ID)
	for _, r := range reps {
		if (r.Index == 1 || r.Index == 2) && (r.Status != "running" || r.DesiredState != "running") {
			t.Errorf("replica %d = %s/%s, want running/running", r.Index, r.Status, r.DesiredState)
		}
	}
}

// TestWarmExpand_ThawFailureFallsBackToColdBoot proves a failed thaw degrades to
// a cold boot - never worse than today.
func TestWarmExpand_ThawFailureFallsBackToColdBoot(t *testing.T) {
	srv, app, rt := newWarmExpandServer(t, "demo", 2, []int{1}, nil)
	deps, _ := srv.store.ListDeployments(app.ID)
	depID := deps[0].ID
	pid, port := 1001, 9001
	if err := srv.store.UpsertReplica(db.UpsertReplicaParams{
		AppID: app.ID, Index: 1, PID: &pid, Port: &port, Status: "suspended",
		Provider: "native", Tier: "default", AppVersion: "v1",
		DesiredState: db.ReplicaDesiredWarm, DeploymentID: &depID,
	}); err != nil {
		t.Fatalf("seed suspended victim: %v", err)
	}
	srv.resumeReplica = func(deploy.Params, int) (*deploy.Result, error) {
		return nil, fmt.Errorf("resume boom")
	}

	expanded, err := srv.WarmExpand("demo")
	if err != nil || !expanded {
		t.Fatalf("WarmExpand = (%v, %v), want (true, nil) after cold-boot fallback", expanded, err)
	}
	if booted := rt.boosted(); len(booted) != 1 {
		t.Fatalf("cold-boot fallback count = %d, want 1", len(booted))
	}
	reps, _ := srv.store.ListReplicas(app.ID)
	for _, r := range reps {
		if r.Index == 1 && (r.Status != "running" || r.DesiredState != "running") {
			t.Errorf("replica 1 = %s/%s, want running/running", r.Status, r.DesiredState)
		}
	}
}
