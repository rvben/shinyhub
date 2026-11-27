package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
)

// A per-session app keeps a ledger row for every worker it has running. Those
// rows belong to the serving version, so a redeploy must not wait for them as
// if an earlier version still owed its cleanup.
func TestDeploy_PerSessionAppWithARunningWorkerCanBeRedeployed(t *testing.T) {
	srv, store, token, manager, _, runtime := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	runtime.serveTraffic = true
	zero := 0
	srv.cfg.Server.MinAvailableMemoryMB = &zero
	srv.SetAvailableMemoryForTest(func() (int, error) { return 1024, nil })
	app := createGenerationTestApp(t, store, "per-session-redeploy", 2, 16)
	if rec := deployIsolationManifest(t, srv, token, app.Slug, "per_session", "print('v1')"); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	serving, err := store.GetServingDeployment(app.ID)
	if err != nil {
		t.Fatal(err)
	}

	spawner := &lifecycle.ElasticSpawner{
		Store: store, Manager: manager, Proxy: srv.proxy,
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	}
	spawned := make(chan struct{}, 4)
	srv.proxy.SetSpawnFunc(func(slug string, slot int) { spawner.Spawn(slug, slot); spawned <- struct{}{} })
	srv.proxy.SetTerminateFunc(spawner.Terminate)
	srv.proxy.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/app/"+app.Slug+"/", nil))
	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	slot := -1
	for _, worker := range manager.AllGenerationsForSlug(app.Slug) {
		if worker.Status == process.StatusRunning && worker.DeploymentID == serving.ID {
			slot = worker.Index
		}
	}
	if slot < 0 {
		t.Fatalf("no running worker of the serving deployment: %+v", manager.AllGenerationsForSlug(app.Slug))
	}
	// The durable identity a native worker records before it executes.
	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: app.ID, DeploymentID: serving.ID, Index: slot, Status: "running", Provider: "native", Tier: "local",
	}); err != nil {
		t.Fatal(err)
	}

	// Consent is given so that the visitor bound a moment ago is not what
	// decides the outcome. The deadline is far below the cleanup wait.
	body, contentType := buildMultiFileBundleUpload(t, map[string]string{"app.py": "print('v2')", "shinyhub.toml": crossIsolationManifest("per_session")})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+app.Slug+"/deploy", body).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("redeploy of a per-session app with a running worker: %d %s", rec.Code, rec.Body.String())
	}
	now, err := store.GetServingDeployment(app.ID)
	if err != nil || now.ID == serving.ID {
		t.Fatalf("serving deployment after redeploy = %+v err=%v, want a new one", now, err)
	}
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.DeploymentID == serving.ID {
			t.Fatalf("worker of the replaced deployment is still in the ledger: %+v", row)
		}
	}
}

// A worker row left by an earlier server process names a process the manager
// does not run. Until that process is confirmed gone a redeploy must keep the
// row, or the ledger forgets a live worker.
func TestDeploy_PerSessionRedeployKeepsALiveWorkerItDoesNotOwn(t *testing.T) {
	srv, store, token, manager, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	// Only a worker of a local native tier can be confirmed gone by its PID.
	manager.RegisterRuntime("host", process.NewNativeRuntime())
	app := createGenerationTestApp(t, store, "per-session-orphan", 2, 16)
	if rec := deployIsolationManifest(t, srv, token, app.Slug, "per_session", "print('v1')"); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	serving, err := store.GetServingDeployment(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	orphan := exec.Command("sleep", "60")
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	t.Cleanup(func() {
		_ = orphan.Process.Kill()
		<-exited
	})
	go func() { _ = orphan.Wait(); close(exited) }()
	pid := orphan.Process.Pid
	started, err := process.NativeProcessStartIdentity(pid)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: app.ID, DeploymentID: serving.ID, Index: 7, PID: &pid, Status: "running",
		Provider: "native", Tier: "host", ProcessStartIdentity: started,
	}); err != nil {
		t.Fatal(err)
	}

	redeploy := func(source string, wait time.Duration) *httptest.ResponseRecorder {
		body, contentType := buildMultiFileBundleUpload(t, map[string]string{"app.py": source, "shinyhub.toml": crossIsolationManifest("per_session")})
		ctx, cancel := context.WithTimeout(t.Context(), wait)
		defer cancel()
		req := httptest.NewRequest(http.MethodPost, "/api/apps/"+app.Slug+"/deploy", body).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}

	refused := redeploy("print('v2')", 2*time.Second)
	if refused.Code != http.StatusConflict {
		t.Fatalf("redeploy over a live worker the manager does not own: %d %s, want it refused", refused.Code, refused.Body.String())
	}
	if got := refused.Header().Get("X-ShinyHub-Conflict-Remedy"); got != "wait" {
		t.Fatalf("X-ShinyHub-Conflict-Remedy = %q, want the refusal marked as one a later retry clears", got)
	}
	if now, err := store.GetServingDeployment(app.ID); err != nil || now.ID != serving.ID {
		t.Fatalf("serving deployment after the refusal = %+v err=%v, want it unchanged", now, err)
	}
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].PID == nil || *rows[0].PID != pid {
		t.Fatalf("ledger after the refusal = %+v, want the live worker still recorded", rows)
	}

	_ = orphan.Process.Kill()
	<-exited
	if rec := redeploy("print('v3')", 15*time.Second); rec.Code != http.StatusOK {
		t.Fatalf("redeploy once the worker is gone: %d %s", rec.Code, rec.Body.String())
	}
}

// The wait for an earlier version reads the worker ledger. A ledger that cannot
// be read is a server fault, not a version still retiring, so it must not be
// answered as a refusal that a later retry clears.
func TestDeploy_LedgerFailureDuringTheWaitIsNotReportedAsRetiring(t *testing.T) {
	srv, store, token, manager, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	app := createGenerationTestApp(t, store, "per-session-ledger", 2, 16)
	if rec := deployIsolationManifest(t, srv, token, app.Slug, "per_session", "print('v1')"); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	serving, err := store.GetServingDeployment(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A live native process the manager does not own keeps the deploy waiting.
	manager.RegisterRuntime("host", process.NewNativeRuntime())
	orphan := exec.Command("sleep", "60")
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = orphan.Process.Kill(); _ = orphan.Wait() })
	pid := orphan.Process.Pid
	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: app.ID, DeploymentID: serving.ID, Index: 7, PID: &pid, Status: "running",
		Provider: "native", Tier: "host",
	}); err != nil {
		t.Fatal(err)
	}

	body, contentType := buildMultiFileBundleUpload(t, map[string]string{"app.py": "print('v2')", "shinyhub.toml": crossIsolationManifest("per_session")})
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+app.Slug+"/deploy", body).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { srv.Router().ServeHTTP(rec, req); close(done) }()
	select {
	case <-done:
		t.Fatalf("deploy did not wait for the unowned worker: %d %s", rec.Code, rec.Body.String())
	case <-time.After(2 * time.Second):
	}
	_ = store.Close()
	<-done

	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "inspect generation cleanup") {
		t.Fatalf("deploy after the ledger failed: %d %s, want the wait's own internal error", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-ShinyHub-Conflict-Remedy"); got != "" {
		t.Fatalf("X-ShinyHub-Conflict-Remedy = %q, want none on a server fault", got)
	}
}

// A tier that cannot be asked about a worker recovers its workers from the
// runtime's own inventory, so a row there does not show a live worker. It must
// not hold a redeploy back.
func TestDeploy_PerSessionRedeployIsNotHeldByARowOnATierItCannotProbe(t *testing.T) {
	srv, store, token, _, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	app := createGenerationTestApp(t, store, "per-session-unit", 2, 16)
	if rec := deployIsolationManifest(t, srv, token, app.Slug, "per_session", "print('v1')"); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	serving, err := store.GetServingDeployment(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Fatal(err)
	}
	pid := gone.Process.Pid
	if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
		AppID: app.ID, DeploymentID: serving.ID, Index: 7, PID: &pid, Status: "running",
		Provider: "native", Tier: "local", WorkerID: "worker-unit.service",
	}); err != nil {
		t.Fatal(err)
	}
	body, contentType := buildMultiFileBundleUpload(t, map[string]string{"app.py": "print('v2')", "shinyhub.toml": crossIsolationManifest("per_session")})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+app.Slug+"/deploy", body).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("redeploy with a stale row on a tier that cannot be probed: %d %s", rec.Code, rec.Body.String())
	}
}

// unitTier is a tier that answers for a worker by its recorded identity.
type unitTier struct {
	process.Runtime
	stopped   bool
	err       error
	unbounded bool
}

func (u *unitTier) WorkerStopped(ctx context.Context, _ string) (bool, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		u.unbounded = true
	}
	return u.stopped, u.err
}

// A tier that names its workers keeps a stopped worker's row until the next
// startup, so the row alone says nothing. The tier is asked: a worker it
// confirms gone does not hold the redeploy, one it cannot confirm gone does.
func TestDeploy_PerSessionRedeployAsksTheTierAboutAWorkerItDoesNotOwn(t *testing.T) {
	srv, store, token, manager, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	tier := &unitTier{Runtime: manager.RuntimeForTier("local")}
	manager.RegisterRuntime("units", tier)
	app := createGenerationTestApp(t, store, "per-session-units", 2, 16)
	if rec := deployIsolationManifest(t, srv, token, app.Slug, "per_session", "print('v1')"); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	redeploy := func(wait time.Duration) *httptest.ResponseRecorder {
		serving, err := store.GetServingDeployment(app.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
			AppID: app.ID, DeploymentID: serving.ID, Index: 7, Status: "running",
			Provider: "native", Tier: "units", WorkerID: "worker-unit.service",
		}); err != nil {
			t.Fatal(err)
		}
		body, contentType := buildMultiFileBundleUpload(t, map[string]string{"app.py": "print('next')", "shinyhub.toml": crossIsolationManifest("per_session")})
		ctx, cancel := context.WithTimeout(t.Context(), wait)
		defer cancel()
		req := httptest.NewRequest(http.MethodPost, "/api/apps/"+app.Slug+"/deploy", body).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}

	tier.stopped = false
	if rec := redeploy(2 * time.Second); rec.Code != http.StatusConflict {
		t.Fatalf("redeploy over a unit the tier reports live: %d %s, want it refused", rec.Code, rec.Body.String())
	}
	tier.stopped, tier.err = true, errors.New("broker unavailable")
	if rec := redeploy(2 * time.Second); rec.Code != http.StatusConflict {
		t.Fatalf("redeploy when the tier cannot answer: %d %s, want it refused", rec.Code, rec.Body.String())
	}
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].WorkerID != "worker-unit.service" {
		t.Fatalf("ledger after the refusals = %+v, want the unit still recorded", rows)
	}
	tier.err = nil
	if rec := redeploy(15 * time.Second); rec.Code != http.StatusOK {
		t.Fatalf("redeploy once the tier confirms the unit gone: %d %s", rec.Code, rec.Body.String())
	}
	if tier.unbounded {
		t.Fatal("the tier was asked about a worker without a deadline, so a tier that does not answer would hold the deploy")
	}
}

// The wait for an earlier version's cleanup runs before the app operation
// lock, so a worker can be left without an owner after the wait and before the
// lock. The check under the lock must refuse it the same way: as a wait the
// caller retries later, whether or not the caller consented to downtime, and
// never by replacing the version that worker belongs to.
func TestDeploy_AWorkerLeftWithoutAnOwnerUnderTheLockHoldsTheRedeploy(t *testing.T) {
	for _, consent := range []bool{true, false} {
		name := "without consent"
		if consent {
			name = "with consent to downtime"
		}
		t.Run(name, func(t *testing.T) {
			srv, store, token, manager, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
			defer srv.Close()
			app := createGenerationTestApp(t, store, "per-session-locked", 2, 16)
			if rec := deployIsolationManifest(t, srv, token, app.Slug, "per_session", "print('v1')"); rec.Code != http.StatusOK {
				t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
			}
			serving, err := store.GetServingDeployment(app.ID)
			if err != nil {
				t.Fatal(err)
			}
			manager.RegisterRuntime("host", process.NewNativeRuntime())
			orphan := exec.Command("sleep", "60")
			if err := orphan.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = orphan.Process.Kill(); _ = orphan.Wait() })
			pid := orphan.Process.Pid
			started, err := process.NativeProcessStartIdentity(pid)
			if err != nil {
				t.Fatal(err)
			}
			reached := false
			srv.beforeDeployLock = func(string) {
				reached = true
				if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
					AppID: app.ID, DeploymentID: serving.ID, Index: 7, PID: &pid, Status: "running",
					Provider: "native", Tier: "host", ProcessStartIdentity: started,
				}); err != nil {
					t.Error(err)
				}
			}

			body, contentType := buildMultiFileBundleUpload(t, map[string]string{"app.py": "print('v2')", "shinyhub.toml": crossIsolationManifest("per_session")})
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			req := httptest.NewRequest(http.MethodPost, "/api/apps/"+app.Slug+"/deploy", body).WithContext(ctx)
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", contentType)
			if consent {
				req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
			}
			rec := httptest.NewRecorder()
			srv.Router().ServeHTTP(rec, req)

			if !reached {
				t.Fatal("the deploy never reached the app operation lock, so the window was not exercised")
			}
			if rec.Code != http.StatusConflict {
				t.Fatalf("redeploy over a worker that lost its owner before the lock: %d %s, want it refused", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("X-ShinyHub-Conflict-Remedy"); got != "wait" {
				t.Fatalf("X-ShinyHub-Conflict-Remedy = %q, want the refusal marked as one a later retry clears", got)
			}
			if strings.Contains(rec.Body.String(), "allow-downtime") {
				t.Fatalf("refusal %q advises consent to downtime, which does not clear an unstopped worker", rec.Body.String())
			}
			if now, err := store.GetServingDeployment(app.ID); err != nil || now.ID != serving.ID {
				t.Fatalf("serving deployment after the refusal = %+v err=%v, want it unchanged", now, err)
			}
			if pending, err := store.HasPendingDeployment(app.ID); err != nil || pending {
				t.Fatalf("pending deployment after the refusal = %v err=%v, want it closed", pending, err)
			}
			rows, err := store.ListDeploymentReplicas(app.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].PID == nil || *rows[0].PID != pid {
				t.Fatalf("ledger after the refusal = %+v, want the live worker still recorded", rows)
			}
		})
	}
}

// orphanedServingRows probes a worker on a native tier or on a tier that
// answers for its workers by identity, and does not fence any other tier. That
// is safe only because no other tier gets a ledger row for an elastic worker:
// an elastic spawn records one only where the runtime starts the worker
// guarded and passes it the lifetime files. A container runtime that gains
// both would record rows the fence never probes, so it must gain a probe too.
func TestOrphanFence_ContainerWorkersAreNotRecordedInTheLedger(t *testing.T) {
	var docker process.Runtime = &process.DockerRuntime{}
	recorded, ok := docker.(interface {
		process.GuardedStartCapable
		process.LifetimeFileInheritor
	})
	if ok && recorded.SupportsGuardedStart() && recorded.InheritsLifetimeFiles() {
		if _, probed := docker.(workerStopProber); !probed {
			t.Fatal("container workers are now recorded in the ledger, but the orphan fence cannot ask the container runtime whether one is stopped")
		}
	}
}

// A deploy of a stopped app only prepares the new version, but it still clears
// the worker ledger before it publishes. A worker left without an owner after
// the wait for earlier cleanup must hold that deploy back as the wait would
// have, not lose its row while it runs.
func TestDeploy_AStoppedAppKeepsTheRowOfAWorkerLeftWithoutAnOwner(t *testing.T) {
	srv, store, token, manager, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	app := createGenerationTestApp(t, store, "per-session-stopped", 2, 16)
	if rec := deployIsolationManifest(t, srv, token, app.Slug, "per_session", "print('v1')"); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	stop := httptest.NewRequest(http.MethodPost, "/api/apps/"+app.Slug+"/stop", nil)
	stop.Header.Set("Authorization", "Bearer "+token)
	stopped := httptest.NewRecorder()
	srv.Router().ServeHTTP(stopped, stop)
	if stopped.Code != http.StatusOK {
		t.Fatalf("stop: %d %s", stopped.Code, stopped.Body.String())
	}
	serving, err := store.GetServingDeployment(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	manager.RegisterRuntime("host", process.NewNativeRuntime())
	orphan := exec.Command("sleep", "60")
	if err := orphan.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = orphan.Process.Kill(); _ = orphan.Wait() })
	pid := orphan.Process.Pid
	started, err := process.NativeProcessStartIdentity(pid)
	if err != nil {
		t.Fatal(err)
	}
	reached := false
	srv.beforeDeployLock = func(string) {
		reached = true
		if err := store.UpsertDeploymentReplica(db.UpsertDeploymentReplicaParams{
			AppID: app.ID, DeploymentID: serving.ID, Index: 7, PID: &pid, Status: "running",
			Provider: "native", Tier: "host", ProcessStartIdentity: started,
		}); err != nil {
			t.Error(err)
		}
	}
	body, contentType := buildMultiFileBundleUpload(t, map[string]string{"app.py": "print('v2')", "shinyhub.toml": crossIsolationManifest("per_session")})
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+app.Slug+"/deploy", body).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if !reached {
		t.Fatal("the deploy never reached the app operation lock, so the window was not exercised")
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("deploy of a stopped app over a worker that lost its owner before the lock: %d %s, want it refused", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-ShinyHub-Conflict-Remedy"); got != "wait" {
		t.Fatalf("X-ShinyHub-Conflict-Remedy = %q, want the refusal marked as one a later retry clears", got)
	}
	if now, err := store.GetServingDeployment(app.ID); err != nil || now.ID != serving.ID {
		t.Fatalf("serving deployment after the refusal = %+v err=%v, want it unchanged", now, err)
	}
	if pending, err := store.HasPendingDeployment(app.ID); err != nil || pending {
		t.Fatalf("pending deployment after the refusal = %v err=%v, want it closed", pending, err)
	}
	if now, err := store.GetAppBySlug(app.Slug); err != nil || now.Status != "stopped" {
		t.Fatalf("app after the refusal = %+v err=%v, want it still stopped", now, err)
	}
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].PID == nil || *rows[0].PID != pid {
		t.Fatalf("ledger after the deploy = %+v, want the live worker still recorded", rows)
	}
}
