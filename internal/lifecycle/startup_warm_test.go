package lifecycle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

func TestStartupWarmFloorRestoresWithoutVisitors(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		floor, autoMin, want int
		auto                 bool
	}{
		{"warm", 2, 0, 2, false}, {"autoscale floor", 1, 3, 3, true}, {"clamped", 20, 0, 8, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &db.App{ID: 1, Slug: "demo", Status: "hibernated", Replicas: 8, MinWarmReplicas: tc.floor, AutoscaleEnabled: tc.auto, AutoscaleMinReplicas: tc.autoMin}
			prior := *app
			prior.Status = "running"
			st := newFakeStore(map[string]*db.App{"demo": app}, []*db.Deployment{{ID: 42, AppID: 1, BundleDir: "/bundle"}})
			st.replicas = map[int64][]*db.Replica{1: {}}
			for i := 0; i < 8; i++ {
				st.replicas[1] = append(st.replicas[1], &db.Replica{AppID: 1, Index: i, Status: "stopped", DesiredState: "stopped"})
			}
			mgr := &fakeManager{}
			var active, peak, boots atomic.Int32
			fn := func(context.Context, string, string, int) (*deploy.Result, error) {
				n := active.Add(1)
				for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
				}
				defer active.Add(-1)
				boots.Add(1)
				time.Sleep(time.Millisecond)
				return &deploy.Result{PID: 123, Port: 20123}, nil
			}
			w := newTestWatcher(Config{}, mgr, newFakeProxy(), st, fn)
			w.RestoreWarmFloors(context.Background(), []*db.App{&prior})
			if boots.Load() != int32(tc.want) || appStatusOf(st, "demo") != "running" {
				t.Fatalf("boots=%d, status=%s", boots.Load(), appStatusOf(st, "demo"))
			}
			if peak.Load() > 4 || mgr.suspendCalls != 0 {
				t.Fatalf("peak=%d, freezes=%d", peak.Load(), mgr.suspendCalls)
			}
			for _, r := range st.replicas[1] {
				if r.Index >= tc.want && r.DesiredState != db.ReplicaDesiredWarm {
					t.Fatalf("surplus slot not warm-parked: %+v", r)
				}
			}
		})
	}
}

func TestStartupWarmFloorPreservesDecidedStates(t *testing.T) {
	for _, tc := range []struct {
		name, status, isolation string
		floor                   int
	}{
		{"stopped", "stopped", "", 2}, {"failed", "failed", "", 2}, {"survivor", "running", "", 2}, {"no warm floor", "hibernated", "", 0}, {"elastic", "hibernated", "per_session", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := &db.App{ID: 1, Slug: "demo", Status: tc.status, Replicas: 2, MinWarmReplicas: tc.floor, WorkerIsolation: tc.isolation}
			prior := *app
			prior.Status = "running"
			st := newFakeStore(map[string]*db.App{"demo": app}, nil)
			w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, func(context.Context, string, string, int) (*deploy.Result, error) {
				t.Fatal("decided app booted")
				return nil, nil
			})
			w.RestoreWarmFloors(context.Background(), []*db.App{&prior})
			if appStatusOf(st, "demo") != tc.status {
				t.Fatal("decided state changed")
			}
		})
	}
}

func TestStartupWarmFloorFailedBootIsVisible(t *testing.T) {
	app := &db.App{ID: 1, Slug: "demo", Status: "hibernated", Replicas: 1, MinWarmReplicas: 1}
	st := newFakeStore(map[string]*db.App{"demo": app}, []*db.Deployment{{AppID: 1, BundleDir: "/bundle"}})
	w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, func(context.Context, string, string, int) (*deploy.Result, error) {
		return nil, errors.New("boot failed")
	})
	w.RestoreWarmFloors(context.Background(), []*db.App{{ID: 1, Slug: "demo", Status: "running", Replicas: 1, MinWarmReplicas: 1}})
	if appStatusOf(st, "demo") != "crashed" {
		t.Fatalf("failed startup status = %s", appStatusOf(st, "demo"))
	}
}

func TestRecoveryRemovesRejectedStaleRoute(t *testing.T) {
	store, app := seedWarmApp(t)
	p := proxy.New()
	const stale = "http://127.0.0.1:29999"
	if err := p.Register(app.Slug, stale); err != nil {
		t.Fatal(err)
	}
	r := &db.Replica{AppID: app.ID, Index: 0, EndpointURL: stale, Status: "running"}
	if alive, _ := recoverNativeReplica(store, nil, p, app, r, t.TempDir(), "", nil); alive {
		t.Fatal("missing process adopted")
	}
	if p.ReplicaTargetURL(app.Slug, 0) != "" {
		t.Fatal("rejected stale route retained")
	}
	if err := p.Register(app.Slug, "http://127.0.0.1:30000"); err != nil {
		t.Fatal(err)
	}
	discardRecoveredRoute(p, app, r)
	if p.ReplicaTargetURL(app.Slug, 0) == "" {
		t.Fatal("concurrent replacement removed")
	}
}

func TestStartupWarmFloorCancellationStopsQueuedBoots(t *testing.T) {
	app := &db.App{ID: 1, Slug: "demo", Status: "hibernated", Replicas: 16, MinWarmReplicas: 16}
	st := newFakeStore(map[string]*db.App{"demo": app}, []*db.Deployment{{AppID: 1, BundleDir: "/bundle"}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var boots atomic.Int32
	w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, func(ctx context.Context, _, _ string, _ int) (*deploy.Result, error) {
		boots.Add(1)
		cancel()
		<-ctx.Done()
		return nil, ctx.Err()
	})
	w.RestoreWarmFloors(ctx, []*db.App{{ID: 1, Slug: "demo", Status: "running", Replicas: 16, MinWarmReplicas: 16}})
	w.wakeWG.Wait()
	if boots.Load() > 4 {
		t.Fatalf("queued boots ran after cancellation: %d", boots.Load())
	}
	if got := appStatusOf(st, "demo"); got != "hibernated" {
		t.Fatalf("cancelled startup = %s", got)
	}
}

func TestFrozenRestoreDoesNotFreezeServingFloor(t *testing.T) {
	app := &db.App{ID: 1, Slug: "demo", Status: "hibernated", Replicas: 1, MinWarmReplicas: 1}
	st := newFakeStore(map[string]*db.App{"demo": app}, []*db.Deployment{{AppID: 1, BundleDir: "/bundle"}})
	w := newTestWatcher(Config{}, &fakeManager{}, newFakeProxy(), st, func(context.Context, string, string, int) (*deploy.Result, error) {
		t.Fatal("serving floor frozen by snapshot restore")
		return nil, nil
	})
	w.RestoreWarm(context.Background())
}

func TestRecoveryThenStartupWarmFloorWithRealStore(t *testing.T) {
	store, app := seedWarmApp(t)
	bundle := t.TempDir()
	dep, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "v1", BundleDir: bundle, Status: db.DeploymentSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		store.UpdateAppReplicas(app.ID, 3),
		store.UpdateAppMinWarmReplicas(app.ID, 2),
		store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "running"}),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	// The surplus slot has no persisted row; startup must still make it warm
	// capacity that the ordinary expansion path can discover later.
	for i := 0; i < 2; i++ {
		if err := store.UpsertReplica(db.UpsertReplicaParams{
			AppID: app.ID, Index: i, Status: "running", Provider: "native", Tier: "default",
			DesiredState: "running", DeploymentID: &dep.ID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	prior, err := store.ListRunningApps()
	if err != nil {
		t.Fatal(err)
	}
	p := proxy.New()
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	inputs, err := PrepareRecovery(store)
	if err != nil {
		t.Fatal(err)
	}
	RecoverProcesses(store, mgr, p, 0, false, "", nil, inputs)
	recovered, err := store.GetAppBySlug(app.Slug)
	if err != nil || recovered.Status != "hibernated" {
		t.Fatalf("recovered app = %+v, err=%v", recovered, err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("warm app")) }))
	defer backend.Close()
	var boots atomic.Int32
	w := New(Config{}, mgr, p, store, func(ctx context.Context, slug, dir string, idx int) (*deploy.Result, error) {
		if err := deploy.ProbeReadiness(ctx, backend.URL, dir, nil); err != nil {
			return nil, err
		}
		if err := p.RegisterReplica(slug, idx, backend.URL, nil, dep.ID, app.ID); err != nil {
			return nil, err
		}
		boots.Add(1)
		return &deploy.Result{PID: 123, Port: 20123}, nil
	})
	w.RestoreWarmFloors(context.Background(), prior)
	if boots.Load() != 2 || replicaAt(t, store, app.ID, 2).DesiredState != db.ReplicaDesiredWarm {
		t.Fatalf("boots=%d; surplus=%+v", boots.Load(), replicaAt(t, store, app.ID, 2))
	}
	current, err := store.GetAppBySlug(app.Slug)
	if err != nil || current.Status != "running" {
		t.Fatalf("startup app = %+v, err=%v", current, err)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/app/demo/", nil))
	if rec.Body.String() != "warm app" || len(p.RejectsByReason(app.Slug, time.Minute)) != 0 {
		t.Fatalf("first visitor = %d %s", rec.Code, rec.Body.String())
	}
}
