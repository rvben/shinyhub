package api_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/storage"
)

// cacheRuntime is a Runtime whose processes live until signalled, so the
// manager holds them exactly as it holds a real app process.
type cacheRuntime struct {
	mu      sync.Mutex
	nextPID int
	stops   map[int]chan struct{}
}

func (c *cacheRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextPID++
	c.stops[c.nextPID] = make(chan struct{})
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "native",
		WorkerID: fmt.Sprint(c.nextPID),
		Handle:   process.RunHandle{PID: c.nextPID},
	}, nil
}

func (c *cacheRuntime) Signal(h process.RunHandle, sig syscall.Signal) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ch, ok := c.stops[h.PID]; ok && (sig == syscall.SIGTERM || sig == syscall.SIGKILL) {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	return nil
}

func (c *cacheRuntime) Wait(_ context.Context, h process.RunHandle) error {
	c.mu.Lock()
	ch, ok := c.stops[h.PID]
	c.mu.Unlock()
	if ok {
		<-ch
	}
	return nil
}

func (c *cacheRuntime) Stats(context.Context, process.RunHandle) (*float64, uint64, error) {
	return nil, 0, nil
}

func (c *cacheRuntime) RunOnce(context.Context, process.StartParams, io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}

func (c *cacheRuntime) HostPreparesDeps() bool    { return true }
func (c *cacheRuntime) AppBindHost() string       { return "127.0.0.1" }
func (c *cacheRuntime) HostProvidesAppData() bool { return true }

type cacheTestEnv struct {
	srv   *api.Server
	store *db.Store
	mgr   *process.Manager
	root  string
}

func newCacheTestEnv(t *testing.T) cacheTestEnv {
	t.Helper()
	store := dbtest.New(t)
	base := t.TempDir()
	cfg := &config.Config{
		Auth: config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{
			AppsDir:     filepath.Join(base, "apps"),
			AppDataDir:  filepath.Join(base, "data"),
			AppCacheDir: filepath.Join(base, "cache"),
		},
	}
	mgr := process.NewManager(cfg.Storage.AppsDir, &cacheRuntime{stops: make(map[int]chan struct{})})
	if err := mgr.SetAppCache(cfg.Storage.AppCacheDir, 64); err != nil {
		t.Fatal(err)
	}
	return cacheTestEnv{
		srv:   api.New(cfg, store, mgr, proxy.New()),
		store: store,
		mgr:   mgr,
		root:  cfg.Storage.AppCacheDir,
	}
}

func (e cacheTestEnv) clear(t *testing.T, slug, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/apps/"+slug+"/cache", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	e.srv.Router().ServeHTTP(rr, req)
	return rr
}

func (e cacheTestEnv) seedNamespace(t *testing.T, slug string, deploymentID int64) string {
	t.Helper()
	p, err := storage.ProvisionCache(e.root, slug, deploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "entry.rds"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func cacheClearEvents(t *testing.T, store *db.Store) []db.AuditEvent {
	t.Helper()
	events, err := store.ListAuditEvents("", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found []db.AuditEvent
	for _, e := range events {
		if e.Action == db.AuditCacheClear {
			found = append(found, e)
		}
	}
	return found
}

func TestClearAppCache_RemovesEveryNamespaceAndAudits(t *testing.T) {
	env := newCacheTestEnv(t)
	_, token := seedOwnerAndApp(t, env.store, "owner", "demo")
	_, _ = seedOwnerAndApp(t, env.store, "neighbour", "other")
	env.seedNamespace(t, "demo", 1)
	env.seedNamespace(t, "demo", 2)
	otherNS := env.seedNamespace(t, "other", 1)

	rr := env.clear(t, "demo", token)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(env.root, "demo")); !os.IsNotExist(err) {
		t.Fatalf("the app's cache survived the clear (stat err %v)", err)
	}
	if _, err := os.Stat(otherNS); err != nil {
		t.Fatal("clearing demo removed another app's cache")
	}
	events := cacheClearEvents(t, env.store)
	if len(events) != 1 || events[0].ResourceID != "demo" {
		t.Fatalf("audit events = %+v, want one cache.clear for demo", events)
	}
	if got := auditDetail(t, events[0].Detail)["deployment_ids"]; fmt.Sprint(got) != "[1 2]" {
		t.Fatalf("audit detail deployment_ids = %v, want [1 2]", got)
	}
}

func TestClearAppCache_NothingCachedIsNotAnError(t *testing.T) {
	env := newCacheTestEnv(t)
	_, token := seedOwnerAndApp(t, env.store, "owner", "demo")
	if rr := env.clear(t, "demo", token); rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
	}
}

func TestClearAppCache_ForbiddenWithoutManageRight(t *testing.T) {
	env := newCacheTestEnv(t)
	_, _ = seedOwnerAndApp(t, env.store, "owner", "demo")
	_, visitor := seedVisitor(t, env.store, "visitor")
	ns := env.seedNamespace(t, "demo", 1)

	rr := env.clear(t, "demo", visitor)
	if rr.Code != http.StatusForbidden && rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 403 or 404: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(ns); err != nil {
		t.Fatal("a refused clear still removed the cache")
	}
	if n := len(cacheClearEvents(t, env.store)); n != 0 {
		t.Fatalf("a refused clear was audited %d times", n)
	}
}

// Every record that says a process or job may hold the cache open refuses the
// clear, and leaves the cache on disk.
func TestClearAppCache_RefusedWhileAnythingMayUseIt(t *testing.T) {
	cases := map[string]func(t *testing.T, store *db.Store, app *db.App, dep *db.Deployment){
		"running replica": func(t *testing.T, store *db.Store, app *db.App, dep *db.Deployment) {
			id := dep.ID
			if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: "running", DeploymentID: &id}); err != nil {
				t.Fatal(err)
			}
		},
		"hibernated replica": func(t *testing.T, store *db.Store, app *db.App, dep *db.Deployment) {
			id := dep.ID
			if err := store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: 0, Status: "hibernated", DeploymentID: &id}); err != nil {
				t.Fatal(err)
			}
		},
		"generation replica": func(t *testing.T, store *db.Store, app *db.App, dep *db.Deployment) {
			if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{AppID: app.ID, DeploymentID: dep.ID, Index: 0, Status: "running"}); err != nil {
				t.Fatal(err)
			}
		},
		"running schedule run": func(t *testing.T, store *db.Store, app *db.App, _ *db.Deployment) {
			scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
				AppID: app.ID, Name: "refresh", CronExpr: "0 * * * *", CommandJSON: `["true"]`,
				Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip", OnSuccess: "none",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
				ScheduleID: scheduleID, Status: "running", Trigger: "schedule", StartedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, seed := range cases {
		t.Run(name, func(t *testing.T) {
			env := newCacheTestEnv(t)
			_, token := seedOwnerAndApp(t, env.store, "owner", "demo")
			app, err := env.store.GetAppBySlug("demo")
			if err != nil {
				t.Fatal(err)
			}
			dep, err := env.store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "v1", BundleDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			ns := env.seedNamespace(t, "demo", dep.ID)
			seed(t, env.store, app, dep)

			rr := env.clear(t, "demo", token)
			if rr.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "stop the app") {
				t.Errorf("409 body does not say how to proceed: %s", rr.Body.String())
			}
			if _, err := os.Stat(ns); err != nil {
				t.Fatal("a refused clear still removed the cache")
			}
			if n := len(cacheClearEvents(t, env.store)); n != 0 {
				t.Fatalf("a refused clear was audited %d times", n)
			}
		})
	}
}

// A process the manager holds counts even when no replica row records it, and
// the namespace it runs in is the one Start provisioned for its deployment.
func TestClearAppCache_RefusedWhileTheManagerHoldsAProcess(t *testing.T) {
	env := newCacheTestEnv(t)
	_, token := seedOwnerAndApp(t, env.store, "owner", "demo")
	app, err := env.store.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.mgr.Start(process.StartParams{
		Slug: "demo", AppID: app.ID, Dir: t.TempDir(), Command: []string{"true"}, Port: 20101, DeploymentID: 7,
	}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ns := storage.CacheNamespace(env.root, "demo", 7)
	if _, err := os.Stat(ns); err != nil {
		t.Fatalf("Start did not provision the deployment's namespace: %v", err)
	}

	if rr := env.clear(t, "demo", token); rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 while the process runs: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(ns); err != nil {
		t.Fatal("a refused clear still removed the cache")
	}

	if err := env.mgr.Stop("demo"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if rr := env.clear(t, "demo", token); rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 once stopped: %s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(ns); !os.IsNotExist(err) {
		t.Fatal("the cache survived a clear after the process stopped")
	}
}

// A stopped or crashed replica row is history, not a process: it must not
// block the clear forever.
func TestClearAppCache_StoppedReplicaRowsDoNotBlock(t *testing.T) {
	env := newCacheTestEnv(t)
	_, token := seedOwnerAndApp(t, env.store, "owner", "demo")
	app, err := env.store.GetAppBySlug("demo")
	if err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"stopped", "crashed"} {
		if err := env.store.UpsertReplica(db.UpsertReplicaParams{AppID: app.ID, Index: i, Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	env.seedNamespace(t, "demo", 1)
	if rr := env.clear(t, "demo", token); rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204: %s", rr.Code, rr.Body.String())
	}
}
