package api

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/process"
)

type sharedRevokeFixture struct {
	srv                          *Server
	app, source, keep            *db.App
	dep                          *db.Deployment
	mgr                          *process.Manager
	jm                           *jobs.Manager
	token, root, bundle, dataDir string
}

func newSharedRevokeFixture(t *testing.T, rt process.Runtime) *sharedRevokeFixture {
	t.Helper()
	root := t.TempDir()
	appsDir := filepath.Join(root, "apps")
	dataDir := filepath.Join(root, "data")
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "test-admin", PasswordHash: "unused", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	token, err := auth.IssueJWT(1, "test-admin", "admin", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	var apps []*db.App
	for _, slug := range []string{"consumer", "source", "keep"} {
		if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: 1}); err != nil {
			t.Fatal(err)
		}
		app, err := store.GetAppBySlug(slug)
		if err != nil {
			t.Fatal(err)
		}
		apps = append(apps, app)
		if err := os.MkdirAll(filepath.Join(dataDir, slug), 0700); err != nil {
			t.Fatal(err)
		}
	}
	app, source, keep := apps[0], apps[1], apps[2]
	for _, src := range []*db.App{source, keep} {
		if err := store.GrantSharedData(app.ID, src.ID); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dataDir, src.Slug, "marker"), []byte(src.Slug+" bytes"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	bundle := filepath.Join(appsDir, "consumer", "versions", "1")
	if err := os.MkdirAll(bundle, 0700); err != nil {
		t.Fatal(err)
	}
	dep, err := store.BeginDeployment(app.ID, "1", bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}
	mgr := process.NewManager(appsDir, rt)
	mgr.SetStopGrace(100 * time.Millisecond)
	if err := mgr.SetAppDataRoot(dataDir); err != nil {
		t.Fatal(err)
	}
	mgr.SetSharedMountResolver(func(slug string) ([]process.SharedMount, error) {
		owner, err := store.GetAppBySlug(slug)
		if err != nil {
			return nil, err
		}
		rows, err := store.ListSharedDataSources(owner.ID)
		if err != nil {
			return nil, err
		}
		var mounts []process.SharedMount
		for _, row := range rows {
			mounts = append(mounts, process.SharedMount{SourceSlug: row.SourceSlug, HostPath: filepath.Join(dataDir, row.SourceSlug)})
		}
		return mounts, nil
	})
	cfg := &config.Config{Auth: config.AuthConfig{Secret: "test-secret"}, Storage: config.StorageConfig{AppsDir: appsDir, AppDataDir: dataDir}}
	cfg.Runtime.Mode = "native"
	srv := New(cfg, store, mgr, nil)
	jm, err := jobs.NewManager(mgr, nil, process.DefaultTier, store, nil, appsDir, dataDir)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetJobs(jm, nil)
	mgr.SetConsumerLifetimeResolver(jm.AcquireConsumerLifetime)
	f := &sharedRevokeFixture{srv: srv, app: app, source: source, keep: keep, dep: dep, mgr: mgr, jm: jm, token: token, root: root, bundle: bundle, dataDir: dataDir}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		jm.Stop(ctx)
		_ = mgr.StopConfirmed(app.Slug)
	})
	// Simulate an existing native mount, also used to prove API cleanup while
	// dormant and before a replacement process is allowed to launch.
	base := filepath.Join(dataDir, app.Slug, "shared")
	if err := os.MkdirAll(base, 0700); err != nil {
		t.Fatal(err)
	}
	for _, src := range []*db.App{source, keep} {
		if err := os.Symlink(filepath.Join(dataDir, src.Slug), filepath.Join(base, src.Slug)); err != nil {
			t.Fatal(err)
		}
	}
	return f
}
func (f *sharedRevokeFixture) request(method, path string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+f.token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(w, req)
	return w
}
func (f *sharedRevokeFixture) revoke(restart bool) *httptest.ResponseRecorder {
	query := ""
	if restart {
		query = "?restart=true"
	}
	return f.request(http.MethodDelete, "/api/apps/consumer/shared-data/source"+query, "")
}
func (f *sharedRevokeFixture) start(t *testing.T, index int, command string) *process.ProcessInfo {
	t.Helper()
	info, err := f.mgr.Start(process.StartParams{Slug: f.app.Slug, AppID: f.app.ID, Index: index, Dir: f.bundle, Command: []string{"sh", "-c", command}, DeploymentID: f.dep.ID, AppVersion: f.dep.Version})
	if err != nil {
		t.Fatal(err)
	}
	pid, port, depID := info.PID, info.Port, f.dep.ID
	if err := f.srv.store.UpsertReplica(db.UpsertReplicaParams{AppID: f.app.ID, Index: index, PID: &pid, Port: &port, Status: "running", DesiredState: "running", WorkerID: info.WorkerID, Provider: info.Provider, DeploymentID: &depID}); err != nil {
		t.Fatal(err)
	}
	return info
}
func waitSharedProof(t *testing.T, path, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			if string(data) != want {
				t.Fatalf("proof %q: %q want %q", path, data, want)
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("missing child-process proof %s", path)
}
func (f *sharedRevokeFixture) assertGrant(t *testing.T, want bool) {
	t.Helper()
	rows, err := f.srv.store.ListSharedDataSources(f.app.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.SourceSlug == f.source.Slug {
			found = true
		}
	}
	if found != want {
		t.Fatalf("source grant present=%t want=%t", found, want)
	}
}

func TestSharedDataUnmountNativeStopsEveryOldProcessBeforeRestart(t *testing.T) {
	f := newSharedRevokeFixture(t, process.NewNativeRuntime())
	if err := f.srv.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: f.app.Slug, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	var old []*process.ProcessInfo
	for i := range 2 {
		old = append(old, f.start(t, i, fmt.Sprintf("cat data/shared/source/marker > initial-%d; exec sleep 30", i)))
		waitSharedProof(t, filepath.Join(f.bundle, fmt.Sprintf("initial-%d", i)), "source bytes")
	}
	f.srv.deployRun = func(p deploy.Params) (*deploy.PoolResult, error) {
		for _, info := range old {
			if err := syscall.Kill(info.PID, 0); !errors.Is(err, syscall.ESRCH) {
				return nil, fmt.Errorf("old PID still alive before restart: %d %v", info.PID, err)
			}
		}
		f.assertGrant(t, false)
		result := &deploy.PoolResult{}
		for i := range 2 {
			info := f.start(t, i, fmt.Sprintf("if test -e data/shared/source/marker; then printf leaked > proof-%d; else cat data/shared/keep/marker > proof-%d; fi; exec sleep 30", i, i))
			waitSharedProof(t, filepath.Join(f.bundle, fmt.Sprintf("proof-%d", i)), "keep bytes")
			result.Replicas = append(result.Replicas, deploy.Result{Index: i, PID: info.PID, Port: info.Port, WorkerID: info.WorkerID, Provider: info.Provider})
		}
		return result, nil
	}
	w := f.revoke(true)
	if w.Code != http.StatusOK {
		t.Fatalf("unmount: %d %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["unmounted"] != true || body["restarted"] != true {
		t.Fatalf("response: %s", w.Body.String())
	}
	if _, err := os.Lstat(filepath.Join(f.dataDir, "consumer", "shared", "source")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("revoked link remains: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(f.dataDir, "source", "marker")); err != nil || string(data) != "source bytes" {
		t.Fatalf("source modified: %q %v", data, err)
	}
}

func TestSharedDataUnmountDormantAndRepeatedRemovalStayDormant(t *testing.T) {
	for _, status := range []string{"stopped", "hibernated"} {
		t.Run(status, func(t *testing.T) {
			f := newSharedRevokeFixture(t, process.NewNativeRuntime())
			var oldPID int
			if status == "hibernated" {
				old := f.start(t, 0, "exec sleep 30")
				oldPID = old.PID
				// Model a warm frozen process without requiring Linux cgroups.
				if err := syscall.Kill(-oldPID, syscall.SIGSTOP); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.srv.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: f.app.Slug, Status: status}); err != nil {
				t.Fatal(err)
			}
			f.srv.deployRun = func(deploy.Params) (*deploy.PoolResult, error) { t.Fatal("dormant app was restarted"); return nil, nil }
			for range 2 {
				w := f.revoke(true)
				if w.Code != http.StatusNoContent {
					t.Fatalf("unmount: %d %s", w.Code, w.Body.String())
				}
			}
			if oldPID > 0 && !errors.Is(syscall.Kill(oldPID, 0), syscall.ESRCH) {
				t.Fatal("warm suspended process survived unmount")
			}
			f.assertGrant(t, false)
			app, err := f.srv.store.GetAppByID(f.app.ID)
			if err != nil || app.Status != status {
				t.Fatalf("intent changed: %+v %v", app, err)
			}
			if _, err := os.Lstat(filepath.Join(f.dataDir, "consumer", "shared", "source")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("stale link retained: %v", err)
			}
		})
	}
}

func TestSharedDataUnmountRequiresConsentAndReportsRestartFailureAsApplied(t *testing.T) {
	f := newSharedRevokeFixture(t, process.NewNativeRuntime())
	f.start(t, 0, "exec sleep 30")
	if err := f.srv.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: f.app.Slug, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	w := f.revoke(false)
	if w.Code != http.StatusConflict {
		t.Fatalf("missing restart consent: %d %s", w.Code, w.Body.String())
	}
	f.assertGrant(t, true)
	f.srv.deployRun = func(deploy.Params) (*deploy.PoolResult, error) { return nil, errors.New("replacement could not start") }
	w = f.revoke(true)
	if w.Code != http.StatusOK {
		t.Fatalf("unmount: %d %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["unmounted"] != true || body["restarted"] != false || body["app_status"] != "stopped" || !strings.Contains(fmt.Sprint(body["restart_error"]), "replacement could not start") {
		t.Fatalf("partial success: %s", w.Body.String())
	}
	f.assertGrant(t, false)
	if f.mgr.HoldsLiveProcess(f.app.Slug) {
		t.Fatal("old process survived failed replacement")
	}
	app, err := f.srv.store.GetAppByID(f.app.ID)
	if err != nil || app.Status != "stopped" {
		t.Fatalf("failed replacement left running status: %+v %v", app, err)
	}
}

type sharedRevokeRuntime struct {
	*activationDockerRuntime
	muMount  sync.Mutex
	mounts   [][]process.SharedMount
	failStop bool
	provider string
}

func (r *sharedRevokeRuntime) Start(ctx context.Context, p process.StartParams, out io.Writer) (process.ReplicaEndpoint, error) {
	r.muMount.Lock()
	r.mounts = append(r.mounts, append([]process.SharedMount(nil), p.SharedMounts...))
	r.muMount.Unlock()
	ep, err := r.activationDockerRuntime.Start(ctx, p, out)
	ep.Provider = r.provider
	return ep, err
}
func (r *sharedRevokeRuntime) Signal(h process.RunHandle, sig syscall.Signal) error {
	r.muMount.Lock()
	fail := r.failStop
	r.muMount.Unlock()
	if fail {
		return errors.New("worker cannot confirm shutdown")
	}
	return r.activationDockerRuntime.Signal(h, sig)
}

func TestSharedDataUnmountRecreatesContainerAndRemotePoolsWithoutOldBind(t *testing.T) {
	for _, provider := range []string{"docker", "remote_docker"} {
		t.Run(provider, func(t *testing.T) {
			rt := &sharedRevokeRuntime{activationDockerRuntime: newActivationDockerRuntime(), provider: provider}
			f := newSharedRevokeFixture(t, rt)
			if err := f.srv.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: f.app.Slug, Status: "running"}); err != nil {
				t.Fatal(err)
			}
			old := f.start(t, 0, "ignored by runtime")
			rt.mu.Lock()
			oldDone := rt.containers[old.WorkerID].done
			rt.mu.Unlock()
			f.srv.deployRun = func(p deploy.Params) (*deploy.PoolResult, error) {
				select {
				case <-oldDone:
				default:
					t.Fatal("new container launched before old exited")
				}
				info := f.start(t, 0, "ignored by runtime")
				return &deploy.PoolResult{Replicas: []deploy.Result{{Index: 0, PID: info.PID, Port: info.Port, Provider: provider, WorkerID: info.WorkerID}}}, nil
			}
			w := f.revoke(true)
			if w.Code != http.StatusOK {
				t.Fatalf("unmount: %d %s", w.Code, w.Body.String())
			}
			rt.muMount.Lock()
			defer rt.muMount.Unlock()
			if len(rt.mounts) != 2 || len(rt.mounts[0]) != 2 || len(rt.mounts[1]) != 1 || rt.mounts[1][0].SourceSlug != "keep" {
				t.Fatalf("container mount sets: %+v", rt.mounts)
			}
		})
	}
}

func TestSharedDataUnmountStopFailurePreservesGrantLinkAndTrackedWorker(t *testing.T) {
	rt := &sharedRevokeRuntime{activationDockerRuntime: newActivationDockerRuntime(), provider: "remote_docker", failStop: true}
	f := newSharedRevokeFixture(t, rt)
	t.Cleanup(func() { rt.muMount.Lock(); rt.failStop = false; rt.muMount.Unlock() })
	f.start(t, 0, "ignored by runtime")
	if err := f.srv.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: f.app.Slug, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	w := f.revoke(true)
	if w.Code != http.StatusConflict {
		t.Fatalf("unconfirmed stop: %d %s", w.Code, w.Body.String())
	}
	f.assertGrant(t, true)
	if !f.mgr.HoldsLiveProcess(f.app.Slug) {
		t.Fatal("unconfirmed worker lost from tracking")
	}
	if data, err := os.ReadFile(filepath.Join(f.dataDir, "consumer", "shared", "source", "marker")); err != nil || string(data) != "source bytes" {
		t.Fatalf("mount changed despite failed stop: %q %v", data, err)
	}
}

func TestSharedDataUnmountAndGrantRejectActiveScheduledJob(t *testing.T) {
	f := newSharedRevokeFixture(t, process.NewNativeRuntime())
	id, err := f.srv.store.CreateSchedule(db.CreateScheduleParams{AppID: f.app.ID, Name: "holding-source", CronExpr: "0 * * * *", CommandJSON: `["sh","-c","cat data/shared/source/marker > job-proof; exec sleep 30"]`, Enabled: true, OverlapPolicy: "skip", TimeoutSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	run, err := f.jm.Run(id, "manual", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitSharedProof(t, filepath.Join(f.bundle, "job-proof"), "source bytes")
	w := f.revoke(true)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "scheduled jobs") {
		t.Fatalf("running job: %d %s", w.Code, w.Body.String())
	}
	f.assertGrant(t, true)
	w = f.request(http.MethodPost, "/api/apps/consumer/shared-data", `{"source_slug":"keep"}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("grant raced running job: %d %s", w.Code, w.Body.String())
	}
	if err := f.jm.Cancel(run); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		row, err := f.srv.store.GetScheduleRun(run)
		if err == nil && row.Status != "running" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The physical fence can outlive the terminal row briefly; retry verifies
	// both durable and in-memory state have drained before mutating mounts.
	for time.Now().Before(deadline) {
		w = f.revoke(true)
		if w.Code == http.StatusNoContent {
			f.assertGrant(t, false)
			return
		}
		if w.Code != http.StatusConflict {
			t.Fatalf("retry: %d %s", w.Code, w.Body.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job fence did not reopen: %d %s", w.Code, w.Body.String())
}

func TestSharedDataUnmountDoesNotForgetUntrackedRemoteOrDrainingIdentity(t *testing.T) {
	for _, generation := range []bool{false, true} {
		t.Run(fmt.Sprintf("generation=%t", generation), func(t *testing.T) {
			f := newSharedRevokeFixture(t, process.NewNativeRuntime())
			if generation {
				if err := f.srv.store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{AppID: f.app.ID, DeploymentID: f.dep.ID, Index: 7, Status: "running", Provider: "remote_docker", WorkerID: "untracked-worker", EndpointURL: "http://worker.invalid/replica"}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := f.srv.store.UpsertReplica(db.UpsertReplicaParams{AppID: f.app.ID, Index: 0, Status: "running", Provider: "remote_docker", WorkerID: "untracked-worker", EndpointURL: "http://worker.invalid/replica"}); err != nil {
					t.Fatal(err)
				}
			}
			w := f.revoke(true)
			if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "verified runtime owner") {
				t.Fatalf("untracked identity: %d %s", w.Code, w.Body.String())
			}
			f.assertGrant(t, true)
			if generation {
				rows, err := f.srv.store.ListDeploymentReplicas(f.app.ID)
				if err != nil || len(rows) != 1 {
					t.Fatalf("lost draining identity: %+v %v", rows, err)
				}
			} else {
				rows, err := f.srv.store.ListReplicas(f.app.ID)
				if err != nil || len(rows) != 1 || rows[0].WorkerID != "untracked-worker" {
					t.Fatalf("lost remote identity: %+v %v", rows, err)
				}
			}
		})
	}
}

func TestSharedDataUnmountInheritedRunningJobAndDatabaseFailureRemainRetryable(t *testing.T) {
	t.Run("inherited running job", func(t *testing.T) {
		f := newSharedRevokeFixture(t, process.NewNativeRuntime())
		id, err := f.srv.store.CreateSchedule(db.CreateScheduleParams{AppID: f.app.ID, Name: "inherited", CronExpr: "0 * * * *", CommandJSON: `["true"]`, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.srv.store.InsertScheduleRun(db.InsertScheduleRunParams{ScheduleID: id, Status: "running", Trigger: "manual"}); err != nil {
			t.Fatal(err)
		}
		w := f.revoke(true)
		if w.Code != http.StatusConflict {
			t.Fatalf("inherited job: %d %s", w.Code, w.Body.String())
		}
		f.assertGrant(t, true)
	})
	t.Run("database delete failure", func(t *testing.T) {
		f := newSharedRevokeFixture(t, process.NewNativeRuntime())
		if f.srv.store.IsPostgres() {
			t.Skip("SQLite fault-injection trigger")
		}
		if _, err := f.srv.store.DB().Exec(`CREATE TRIGGER fail_shared_revoke BEFORE DELETE ON app_shared_data BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`); err != nil {
			t.Fatal(err)
		}
		w := f.revoke(true)
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("delete failure: %d %s", w.Code, w.Body.String())
		}
		f.assertGrant(t, true)
		if _, err := f.srv.store.DB().Exec(`DROP TRIGGER fail_shared_revoke`); err != nil {
			t.Fatal(err)
		}
		w = f.revoke(true)
		if w.Code != http.StatusNoContent {
			t.Fatalf("retry: %d %s", w.Code, w.Body.String())
		}
		f.assertGrant(t, false)
	})
}
