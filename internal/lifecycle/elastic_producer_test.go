package lifecycle_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// A producer schedule no longer pins an app to multiplex. Native elastic
// workers record their identity before they execute and inherit the server's
// lifetime locks, so a grouped or per-session pool can serve a producer app.
// Workers on a runtime without those guarantees cannot, and the marker such a
// worker leaves behind is what producer enablement checks for afterwards.

func withProducerSchedule(t *testing.T, store *db.Store, appID int64) {
	t.Helper()
	if _, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "refresh", CronExpr: "0 5 * * *", CommandJSON: `["producer"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
		DeployTrigger: "bundle_change", OnSuccess: "none", RollFallback: "defer",
	}); err != nil {
		t.Fatal(err)
	}
}

func TestElasticNativeSpawnServesProducerApp(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "native-producer")
	withProducerSchedule(t, store, app.ID)
	bundle := t.TempDir()
	manifest := "[app]\ncommand = [\"/bin/sh\", \"-c\", \"exec sleep 60\"]\n"
	if err := os.WriteFile(filepath.Join(bundle, "shinyhub.toml"), []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	mustCreateDeploymentInDir(t, store, app.ID, bundle)
	mgr := process.NewManager(t.TempDir(), process.NewNativeRuntime())
	t.Cleanup(func() { _ = mgr.StopAll() })
	prx := proxy.New()
	prx.SetPoolAppID(app.Slug, app.ID)
	prx.SetPoolMode(app.Slug, config.IsolationPerSession, 1, 1)
	s := &lifecycle.ElasticSpawner{Store: store, Manager: mgr, Proxy: prx, HealthCheck: noopHealthCheck}

	s.Spawn(app.Slug, 0)

	if prx.ElasticWorkerCount(app.Slug) != 1 || !mgr.HasRunning(app.Slug) {
		t.Fatal("native worker was refused for a producer app")
	}
	if risk, err := store.AppElasticOrphanRisk(app.ID); err != nil || risk {
		t.Fatalf("recorded native worker left an orphan-risk marker: risk=%v err=%v", risk, err)
	}
	s.Terminate(app.Slug, 0)
}

func TestElasticUnguardedSpawnIsRefusedForProducerApp(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "unguarded-producer")
	withProducerSchedule(t, store, app.ID)
	mustCreateDeploymentInDir(t, store, app.ID, mustMinimalBundle(t))
	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)
	prx := proxy.New()
	prx.SetPoolAppID(app.Slug, app.ID)
	prx.SetPoolMode(app.Slug, config.IsolationPerSession, 1, 1)
	s := &lifecycle.ElasticSpawner{Store: store, Manager: mgr, Proxy: prx, HealthCheck: noopHealthCheck}

	s.Spawn(app.Slug, 0)

	rt.mu.Lock()
	started := len(rt.started)
	rt.mu.Unlock()
	if started != 0 || prx.ElasticWorkerCount(app.Slug) != 0 {
		t.Fatalf("worker without durable identity started for a producer app: starts=%d workers=%d", started, prx.ElasticWorkerCount(app.Slug))
	}
	if risk, err := store.AppElasticOrphanRisk(app.ID); err != nil || risk {
		t.Fatalf("refused spawn marked orphan risk: risk=%v err=%v", risk, err)
	}
}

// lockProber stands in for the jobs manager: it reports the apps whose
// consumer-lifetime lock is still held and records which apps were probed.
type lockProber struct {
	held   map[int64]bool
	probed []int64
}

func (p *lockProber) ConsumerLifetimeFree(appID int64) (bool, error) {
	p.probed = append(p.probed, appID)
	return !p.held[appID], nil
}

// Startup discharge clears the marker only where the lock proves the app has
// no surviving worker, keeps it where a worker still holds the lock, and
// leaves unmarked apps alone.
func TestDischargeElasticOrphanRiskClearsOnlyProvablyFreeApps(t *testing.T) {
	store := mustOpenStore(t)
	gone := mustCreateElasticApp(t, store, "survivor-gone")
	alive := mustCreateElasticApp(t, store, "survivor-alive")
	clean := mustCreateElasticApp(t, store, "never-marked")
	for _, app := range []*db.App{gone, alive} {
		if err := store.MarkElasticOrphanRisk(app.ID); err != nil {
			t.Fatal(err)
		}
	}
	prober := &lockProber{held: map[int64]bool{alive.ID: true}}

	lifecycle.DischargeElasticOrphanRisk(store, prober)

	for _, tc := range []struct {
		app  *db.App
		want bool
	}{{gone, false}, {alive, true}, {clean, false}} {
		risk, err := store.AppElasticOrphanRisk(tc.app.ID)
		if err != nil || risk != tc.want {
			t.Fatalf("%s: marker=%v err=%v, want %v", tc.app.Slug, risk, err, tc.want)
		}
	}
	for _, id := range prober.probed {
		if id == clean.ID {
			t.Fatal("an unmarked app was probed")
		}
	}
	if len(prober.probed) != 2 {
		t.Fatalf("probed %v, want exactly the two marked apps", prober.probed)
	}
}

func TestElasticUnguardedSpawnMarksOrphanRisk(t *testing.T) {
	store := mustOpenStore(t)
	app := mustCreateElasticApp(t, store, "unguarded-plain")
	mustCreateDeploymentInDir(t, store, app.ID, mustMinimalBundle(t))
	rt := &recordingRuntime{}
	mgr := process.NewManager(t.TempDir(), rt)
	prx := proxy.New()
	prx.SetPoolAppID(app.Slug, app.ID)
	prx.SetPoolMode(app.Slug, config.IsolationPerSession, 1, 1)
	s := &lifecycle.ElasticSpawner{Store: store, Manager: mgr, Proxy: prx, HealthCheck: noopHealthCheck}

	s.Spawn(app.Slug, 0)

	if prx.ElasticWorkerCount(app.Slug) != 1 {
		t.Fatalf("workers = %d, want the plain app served", prx.ElasticWorkerCount(app.Slug))
	}
	if risk, err := store.AppElasticOrphanRisk(app.ID); err != nil || !risk {
		t.Fatalf("worker without durable identity left no orphan-risk marker: risk=%v err=%v", risk, err)
	}
}
