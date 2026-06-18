package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// TestRestoreWarm_BootsThenFreezesHibernatedApps: the startup warm-restore pass
// re-boots only the hibernated apps and re-freezes each (so its next access is a
// warm resume), leaving stopped apps untouched.
func TestRestoreWarm_BootsThenFreezesHibernatedApps(t *testing.T) {
	apps := map[string]*db.App{
		"warm":    {ID: 1, Slug: "warm", Status: "hibernated", Replicas: 1},
		"stopped": {ID: 2, Slug: "stopped", Status: "stopped", Replicas: 1},
	}
	st := newFakeStore(apps, []*db.Deployment{{AppID: 1, BundleDir: "/tmp/warm"}})
	mgr := &fakeManager{suspendFreed: true}
	prx := newFakeProxy()

	var booted sync.Map
	var bootCount int32
	deployFn := func(slug, bundleDir string, idx int) (*deploy.Result, error) {
		// Mirror production's proxy.RegisterReplica contract: the pool must be
		// sized before a replica boots, otherwise registration fails with
		// "pool size not set or index out of range". Recovery only sizes
		// running apps after a restart, so RestoreWarm must size the pool for
		// the hibernated app itself before booting. Without that, this stub
		// returns the same error production did and the boot is left cold.
		prx.mu.Lock()
		size := prx.poolSizes[slug]
		prx.mu.Unlock()
		if idx >= size {
			return nil, fmt.Errorf("register: register %s#%d: pool size not set or index out of range", slug, idx)
		}
		atomic.AddInt32(&bootCount, 1)
		booted.Store(slug, bundleDir)
		return &deploy.Result{Index: idx, PID: 99, Port: 20099}, nil
	}
	w := newTestWatcher(Config{DefaultMaxSessionsPerReplica: 5}, mgr, prx, st, deployFn)

	w.RestoreWarm(context.Background())

	if dir, ok := booted.Load("warm"); !ok || dir != "/tmp/warm" {
		t.Fatalf("hibernated app 'warm' must be booted from its bundle dir, got %v ok=%v", dir, ok)
	}
	if _, ok := booted.Load("stopped"); ok {
		t.Fatalf("a stopped app must NOT be warm-restored")
	}
	if n := atomic.LoadInt32(&bootCount); n != 1 {
		t.Fatalf("boot count = %d, want 1 (only the hibernated app)", n)
	}
	if prx.poolSizes["warm"] != 1 {
		t.Fatalf("pool size for 'warm' = %d, want 1 (pool must be sized before boot)", prx.poolSizes["warm"])
	}
	if prx.poolCaps["warm"] != 5 {
		t.Fatalf("pool cap for 'warm' = %d, want 5 (DefaultMaxSessionsPerReplica fallback)", prx.poolCaps["warm"])
	}
	if mgr.suspendCalls != 1 {
		t.Fatalf("suspendCalls = %d, want 1 (re-froze the booted app)", mgr.suspendCalls)
	}
	if got, ok := lastUpsertStatus(st, 0); !ok || got != db.ReplicaStatusSuspended {
		t.Fatalf("warm app replica 0 status = %q ok=%v, want suspended", got, ok)
	}
	// The critical correctness property: the booted replicas must be REMOVED from
	// proxy routing (BeginHibernate) before being suspended, so the next access
	// triggers a wake -> warm resume instead of routing to a frozen process.
	prx.mu.Lock()
	hib := append([]string(nil), prx.hibernated...)
	prx.mu.Unlock()
	if len(hib) != 1 || hib[0] != "warm" {
		t.Fatalf("hibernated (pool removed from routing) = %v, want [warm]", hib)
	}
	// The claim must be released back to 'hibernated' so the next wake can win the
	// BeginWake CAS.
	if got := appStatusOf(st, "warm"); got != "hibernated" {
		t.Fatalf("final status of 'warm' = %q, want hibernated (claim released after park)", got)
	}
}

// appStatusOf reads the fakeStore's current status for a slug.
func appStatusOf(st *fakeStore, slug string) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.appStatus[slug]
}

// TestRestoreWarm_SkipsWhenClaimLost: if BeginWake fails (e.g. a user request
// already woke the app between the hibernated-apps snapshot and the claim),
// RestoreWarm must NOT boot or freeze it - the wake path owns it.
func TestRestoreWarm_SkipsWhenClaimLost(t *testing.T) {
	// App is 'running' (already woken), so BeginWake's hibernated->waking CAS loses.
	apps := map[string]*db.App{"warm": {ID: 1, Slug: "warm", Status: "running", Replicas: 1}}
	st := newFakeStore(apps, []*db.Deployment{{AppID: 1, BundleDir: "/tmp/warm"}})
	// Force ListHibernatedApps to still return it (simulating the snapshot-vs-claim
	// race) so we exercise the BeginWake guard, not the list filter.
	st.forceHibernatedList = []*db.App{apps["warm"]}
	mgr := &fakeManager{suspendFreed: true}
	var bootCount int32
	deployFn := func(slug, bundleDir string, idx int) (*deploy.Result, error) {
		atomic.AddInt32(&bootCount, 1)
		return &deploy.Result{Index: idx}, nil
	}
	w := newTestWatcher(Config{}, mgr, newFakeProxy(), st, deployFn)

	w.RestoreWarm(context.Background())

	if n := atomic.LoadInt32(&bootCount); n != 0 {
		t.Fatalf("boot count = %d, want 0 (claim lost, wake path owns it)", n)
	}
	if mgr.suspendCalls != 0 {
		t.Fatalf("suspendCalls = %d, want 0 (claim lost)", mgr.suspendCalls)
	}
	if got := appStatusOf(st, "warm"); got != "running" {
		t.Fatalf("status of 'warm' = %q, want running (untouched)", got)
	}
}

// TestRestoreWarm_RequestDuringRestoreLeavesRunning: if a real request lands on
// the app while it is booting (BeginHibernate returns false), RestoreWarm must
// NOT freeze it - it promotes the app to running and leaves it serving.
func TestRestoreWarm_RequestDuringRestoreLeavesRunning(t *testing.T) {
	apps := map[string]*db.App{"warm": {ID: 1, Slug: "warm", Status: "hibernated", Replicas: 1}}
	st := newFakeStore(apps, []*db.Deployment{{AppID: 1, BundleDir: "/tmp/warm"}})
	mgr := &fakeManager{suspendFreed: true}
	prx := newFakeProxy()
	prx.hibernateNever = true // simulate an in-flight request: BeginHibernate aborts
	deployFn := func(slug, bundleDir string, idx int) (*deploy.Result, error) {
		return &deploy.Result{Index: idx, PID: 99, Port: 20099}, nil
	}
	w := newTestWatcher(Config{DefaultMaxSessionsPerReplica: 5}, mgr, prx, st, deployFn)

	w.RestoreWarm(context.Background())

	if mgr.suspendCalls != 0 {
		t.Fatalf("suspendCalls = %d, want 0 (a request raced in; must not freeze)", mgr.suspendCalls)
	}
	if got := appStatusOf(st, "warm"); got != "running" {
		t.Fatalf("status of 'warm' = %q, want running (promoted, left serving)", got)
	}
}

// TestRestoreWarm_SkipsAppWithoutDeployment: a hibernated app that was never
// deployed has no bundle to boot; warm-restore skips it without booting or
// freezing.
func TestRestoreWarm_SkipsAppWithoutDeployment(t *testing.T) {
	apps := map[string]*db.App{"warm": {ID: 1, Slug: "warm", Status: "hibernated", Replicas: 1}}
	st := newFakeStore(apps, nil) // no deployments
	mgr := &fakeManager{suspendFreed: true}
	var bootCount int32
	deployFn := func(slug, bundleDir string, idx int) (*deploy.Result, error) {
		atomic.AddInt32(&bootCount, 1)
		return &deploy.Result{Index: idx}, nil
	}
	w := newTestWatcher(Config{}, mgr, newFakeProxy(), st, deployFn)

	w.RestoreWarm(context.Background())

	if n := atomic.LoadInt32(&bootCount); n != 0 {
		t.Fatalf("boot count = %d, want 0 (no deployment)", n)
	}
	if mgr.suspendCalls != 0 {
		t.Fatalf("suspendCalls = %d, want 0 (nothing to freeze)", mgr.suspendCalls)
	}
}

// TestRestoreWarm_BootFailureLeavesCold: warm restore is best-effort pre-warming,
// so a boot failure leaves the app cold (hibernated) to wake on first access - it
// must NOT mark the app crashed, because a transient/infra error is not the app's
// fault (a genuinely-broken app is surfaced by the runtime crash-loop guard).
func TestRestoreWarm_BootFailureLeavesCold(t *testing.T) {
	apps := map[string]*db.App{"warm": {ID: 1, Slug: "warm", Status: "hibernated", Replicas: 1}}
	st := newFakeStore(apps, []*db.Deployment{{AppID: 1, BundleDir: "/tmp/warm"}})
	mgr := &fakeManager{suspendFreed: true}
	prx := newFakeProxy()
	deployFn := func(slug, bundleDir string, idx int) (*deploy.Result, error) {
		return nil, errors.New("health check failed")
	}
	w := newTestWatcher(Config{}, mgr, prx, st, deployFn)

	w.RestoreWarm(context.Background())

	if mgr.suspendCalls != 0 {
		t.Fatalf("suspendCalls = %d, want 0 (boot failed, nothing to freeze)", mgr.suspendCalls)
	}
	if got := appStatusOf(st, "warm"); got != "hibernated" {
		t.Fatalf("status of 'warm' = %q, want hibernated (left cold, not crashed)", got)
	}
}

// TestRestoreWarm_PartialBootCleansUp: a multi-replica app whose first replica
// boots but second fails must NOT be left with the first replica orphaned and
// running under a 'hibernated' app (the idle watcher never reaps a hibernated
// app). RestoreWarm tears it back down (Deregister + Stop) so the app is cold.
func TestRestoreWarm_PartialBootCleansUp(t *testing.T) {
	apps := map[string]*db.App{"warm": {ID: 1, Slug: "warm", Status: "hibernated", Replicas: 2}}
	st := newFakeStore(apps, []*db.Deployment{{AppID: 1, BundleDir: "/tmp/warm"}})
	mgr := &fakeManager{suspendFreed: true}
	prx := newFakeProxy()
	deployFn := func(slug, bundleDir string, idx int) (*deploy.Result, error) {
		if idx == 1 {
			return nil, errors.New("port exhausted on replica 1")
		}
		return &deploy.Result{Index: idx, PID: 99, Port: 20099}, nil
	}
	w := newTestWatcher(Config{DefaultMaxSessionsPerReplica: 5}, mgr, prx, st, deployFn)

	w.RestoreWarm(context.Background())

	if mgr.suspendCalls != 0 {
		t.Fatalf("suspendCalls = %d, want 0 (partial boot must not freeze)", mgr.suspendCalls)
	}
	mgr.mu.Lock()
	stopped := append([]string(nil), mgr.stopped...)
	mgr.mu.Unlock()
	if len(stopped) != 1 || stopped[0] != "warm" {
		t.Fatalf("stopped = %v, want [warm] (cleanup of the booted replica)", stopped)
	}
	prx.mu.Lock()
	dereg := append([]string(nil), prx.deregistered...)
	prx.mu.Unlock()
	if len(dereg) != 1 || dereg[0] != "warm" {
		t.Fatalf("deregistered = %v, want [warm] (proxy pool reset on partial boot)", dereg)
	}
	// A partial-boot failure leaves the app cold (hibernated), not crashed.
	if got := appStatusOf(st, "warm"); got != "hibernated" {
		t.Fatalf("status of 'warm' = %q, want hibernated after partial-boot cleanup", got)
	}
}
