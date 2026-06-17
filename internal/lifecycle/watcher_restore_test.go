package lifecycle

import (
	"context"
	"errors"
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
		atomic.AddInt32(&bootCount, 1)
		booted.Store(slug, bundleDir)
		return &deploy.Result{Index: idx, PID: 99, Port: 20099}, nil
	}
	w := newTestWatcher(Config{}, mgr, prx, st, deployFn)

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
	if mgr.suspendCalls != 1 {
		t.Fatalf("suspendCalls = %d, want 1 (re-froze the booted app)", mgr.suspendCalls)
	}
	if got, ok := lastUpsertStatus(st, 0); !ok || got != db.ReplicaStatusSuspended {
		t.Fatalf("warm app replica 0 status = %q ok=%v, want suspended", got, ok)
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

// TestRestoreWarm_BootFailureLeavesAppCold: when a boot fails, the app is left
// cold (woken on first access) and not frozen.
func TestRestoreWarm_BootFailureLeavesAppCold(t *testing.T) {
	apps := map[string]*db.App{"warm": {ID: 1, Slug: "warm", Status: "hibernated", Replicas: 1}}
	st := newFakeStore(apps, []*db.Deployment{{AppID: 1, BundleDir: "/tmp/warm"}})
	mgr := &fakeManager{suspendFreed: true}
	deployFn := func(slug, bundleDir string, idx int) (*deploy.Result, error) {
		return nil, errors.New("crashed on startup")
	}
	w := newTestWatcher(Config{}, mgr, newFakeProxy(), st, deployFn)

	w.RestoreWarm(context.Background())

	if mgr.suspendCalls != 0 {
		t.Fatalf("suspendCalls = %d, want 0 (boot failed, nothing to freeze)", mgr.suspendCalls)
	}
}
