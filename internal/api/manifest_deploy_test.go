package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/schedulespec"
)

// manifestFakeRuntime is a minimal Runtime for end-to-end deploy tests.
// It returns synthetic PIDs without spawning real processes, so deploy.Run
// can complete without uv/Rscript on the host.
type manifestFakeRuntime struct {
	mu               sync.Mutex
	nextPID          int
	stops            map[int]chan struct{}
	events           []string
	producerCommands [][]string
	producerEntered  chan struct{}
	producerBlock    chan struct{}
	producerErr      error
}

func newManifestFakeRuntime() *manifestFakeRuntime {
	return &manifestFakeRuntime{
		nextPID: 30000,
		stops:   make(map[int]chan struct{}),
	}
}

func (f *manifestFakeRuntime) Start(_ context.Context, p process.StartParams, _ io.Writer) (process.ReplicaEndpoint, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, "start:"+p.ContentDigest)
	pid := f.nextPID
	f.nextPID++
	f.stops[pid] = make(chan struct{})
	return process.ReplicaEndpoint{
		URL:      fmt.Sprintf("http://127.0.0.1:%d", p.Port),
		Provider: "native",
		WorkerID: fmt.Sprintf("%d", pid),
		Handle:   process.RunHandle{PID: pid},
	}, nil
}

func (f *manifestFakeRuntime) Signal(h process.RunHandle, sig syscall.Signal) error {
	f.mu.Lock()
	ch, ok := f.stops[h.PID]
	f.mu.Unlock()
	if ok && (sig == syscall.SIGTERM || sig == syscall.SIGKILL) {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	return nil
}

func (f *manifestFakeRuntime) Wait(_ context.Context, h process.RunHandle) error {
	f.mu.Lock()
	ch, ok := f.stops[h.PID]
	f.mu.Unlock()
	if ok {
		<-ch
	}
	return nil
}

func (f *manifestFakeRuntime) Stats(_ context.Context, _ process.RunHandle) (*float64, uint64, error) {
	return nil, 0, nil
}

func (f *manifestFakeRuntime) RunOnce(ctx context.Context, p process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	f.mu.Lock()
	f.events = append(f.events, "producer:"+p.ContentDigest)
	f.producerCommands = append(f.producerCommands, append([]string(nil), p.Command...))
	f.mu.Unlock()
	if f.producerEntered != nil {
		select {
		case f.producerEntered <- struct{}{}:
		default:
		}
	}
	if f.producerBlock != nil {
		select {
		case <-f.producerBlock:
		case <-ctx.Done():
			return process.ExitInfo{Code: -1, Signaled: true}, nil
		}
	}
	f.mu.Lock()
	err := f.producerErr
	f.mu.Unlock()
	if err != nil {
		return process.ExitInfo{}, err
	}
	return process.ExitInfo{}, nil
}

// HostPreparesDeps returns false so deploy.Run skips uv sync / renv::restore.
// Container-mode semantics: dependency installation is treated as a no-op on
// the host, which is exactly what we want for a test that never spawns real
// processes.
func (f *manifestFakeRuntime) HostPreparesDeps() bool      { return false }
func (f *manifestFakeRuntime) AppBindHost() string         { return "127.0.0.1" }
func (f *manifestFakeRuntime) HostProvidesAppData() bool   { return true }
func (f *manifestFakeRuntime) InheritsLifetimeFiles() bool { return true }

// buildManifestE2EServer constructs the shared scaffolding used by all
// manifest E2E server variants: temp appsDir, in-memory store, admin user +
// JWT, config, process manager with the fake runtime, Server, and a no-op
// health-check deploy runner. SetJobs is intentionally NOT called here so
// each variant can wire the scheduler it needs (nil vs real jobs.Manager).
func buildManifestE2EServer(t *testing.T, runtime config.RuntimeConfig) (srv *Server, store *db.Store, token string, mgr *process.Manager, appsDir string, fakeRuntime *manifestFakeRuntime) {
	t.Helper()
	appsDir = t.TempDir()
	store = dbtest.New(t)

	hash, _ := testHashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{
		Username: "admin", PasswordHash: hash, Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	admin, _ := store.GetUserByUsername("admin")
	token, _ = auth.IssueJWT(admin.ID, admin.Username, admin.Role, "test-secret")

	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir, VersionRetention: 5},
		Runtime: runtime,
	}

	fakeRuntime = newManifestFakeRuntime()
	mgr = process.NewManager(appsDir, fakeRuntime)
	srv = New(cfg, store, mgr, proxy.New())

	// Replace the deploy runner to inject a no-op health check so tests
	// complete instantly instead of waiting for the 120 s timeout. Sync hooks
	// are already bypassed because manifestFakeRuntime.HostPreparesDeps()
	// returns false (container-mode semantics: no host-side dep installation).
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		p.HealthCheck = func(string, time.Duration, http.RoundTripper) error { return nil }
		return deploy.Run(p)
	})
	return srv, store, token, mgr, appsDir, fakeRuntime
}

// newManifestE2EServer wires a Server with a fake runtime, no-op sync hooks,
// a no-op health check, and a started (wired) scheduler stub. Returns the
// server, store, and an admin JWT bearer token.
func newManifestE2EServer(t *testing.T) (*Server, *db.Store, string) {
	t.Helper()
	return newManifestE2EServerCfg(t, config.RuntimeConfig{})
}

func newManifestE2EServerCfg(t *testing.T, runtime config.RuntimeConfig) (*Server, *db.Store, string) {
	t.Helper()
	srv, store, token, _, _, _ := buildManifestE2EServer(t, runtime)
	// Wire scheduler (not started — ErrNotStarted is treated as a soft warning).
	srv.SetJobs(nil, scheduler.New(nil, store, time.UTC))
	return srv, store, token
}

func TestPrestartPlanRevalidatesSatisfiedProducerAfterInterveningWriter(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "revalidate-owner", PasswordHash: "unused", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("revalidate-owner")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateApp(db.CreateAppParams{
		Slug: "revalidate-producer", Name: "revalidate-producer", OwnerID: owner.ID, Access: "private",
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("revalidate-producer")
	if err != nil {
		t.Fatal(err)
	}
	appID := app.ID
	command := `["producer"]`
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: appID, Name: "cache", CronExpr: "0 5 * * *", CommandJSON: command,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
		DeployTrigger: schedulespec.DeployTriggerBundleChange,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, fingerprint, err := schedulespec.ProducerIdentity(command)
	if err != nil {
		t.Fatal(err)
	}
	publish := func(version, digest string, finishedAt time.Time) {
		t.Helper()
		deployment, beginErr := store.BeginDeployment(appID, version, t.TempDir())
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if setErr := store.SetDeploymentDigest(deployment.ID, digest); setErr != nil {
			t.Fatal(setErr)
		}
		deploymentID := deployment.ID
		runID, insertErr := store.InsertScheduleRun(db.InsertScheduleRunParams{
			ScheduleID: scheduleID, Status: "running", Trigger: "deploy", StartedAt: finishedAt.Add(-time.Second),
			DeploymentID: &deploymentID, AppVersion: version, ContentDigest: digest,
			ProducerFingerprint: fingerprint, ProducerCommandJSON: canonical, PublishesData: true,
		})
		if insertErr != nil {
			t.Fatal(insertErr)
		}
		zero := 0
		if _, completeErr := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
			RunID: runID, Status: "succeeded", ExitCode: &zero, FinishedAt: finishedAt,
		}); completeErr != nil {
			t.Fatal(completeErr)
		}
	}

	base := time.Now().UTC().Truncate(time.Microsecond)
	publish("target", "sha256:target", base)
	srv := &Server{store: store}
	plan, err := srv.planPrestartSchedules(app, nil, "sha256:target")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.producers) != 0 {
		t.Fatalf("initial plan producers=%d, want satisfied-on-entry", len(plan.producers))
	}

	// Model an ordinary writer admitted before the deploy acquired its producer
	// gates. It completes while gate acquisition waits and invalidates the state
	// that made initial planning look satisfied.
	publish("intervening", "sha256:intervening", base.Add(time.Second))
	if err := srv.revalidatePrestartPlan(plan, "sha256:target"); err != nil {
		t.Fatal(err)
	}
	if len(plan.producers) != 1 || plan.producers[0].ID != scheduleID {
		t.Fatalf("revalidated producers=%+v, want schedule %d", plan.producers, scheduleID)
	}
}

func TestPrestartConsumerFenceRepublishesAfterCrossProcessWriter(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "fence-owner", PasswordHash: "unused", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, err := store.GetUserByUsername("fence-owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "fenced-producer", Name: "fenced-producer", OwnerID: owner.ID, Access: "private",
	}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("fenced-producer")
	if err != nil {
		t.Fatal(err)
	}
	command := `["produce"]`
	canonical, fingerprint, err := schedulespec.ProducerIdentity(command)
	if err != nil {
		t.Fatal(err)
	}
	scheduleID, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "cache", CronExpr: "0 5 * * *", CommandJSON: command,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "concurrent", MissedPolicy: "skip",
		DeployTrigger: schedulespec.DeployTriggerBundleChange,
	})
	if err != nil {
		t.Fatal(err)
	}
	oldDeployment, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "old", BundleDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeploymentDigest(oldDeployment.ID, "sha256:old"); err != nil {
		t.Fatal(err)
	}
	oldDeployment.ContentDigest = "sha256:old"
	sharedData := t.TempDir()
	oldRuntime := newManifestFakeRuntime()
	oldRuntime.producerEntered = make(chan struct{}, 1)
	oldRuntime.producerBlock = make(chan struct{})
	oldJobs, err := jobs.NewManager(process.NewManager(t.TempDir(), oldRuntime), nil, process.DefaultTier, store, nil, sharedData, sharedData)
	if err != nil {
		t.Fatal(err)
	}
	// The old writer passes its definitive current-deployment check and begins
	// writing before the successor promotes the target deployment. It remains
	// blocked while the successor establishes target provenance and waits for
	// the exclusive publication fence to drain.
	if _, err := oldJobs.RunForDeployment(scheduleID, "manual", nil, oldDeployment); err != nil {
		t.Fatal(err)
	}
	select {
	case <-oldRuntime.producerEntered:
	case <-time.After(time.Second):
		t.Fatal("retiring writer did not acquire publication fence")
	}

	targetDeployment, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "target", BundleDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetDeploymentDigest(targetDeployment.ID, "sha256:target"); err != nil {
		t.Fatal(err)
	}
	targetDeployment.ContentDigest = "sha256:target"
	targetID := targetDeployment.ID
	seedRun, err := store.InsertScheduleRun(db.InsertScheduleRunParams{
		ScheduleID: scheduleID, Status: "running", Trigger: "deploy", StartedAt: time.Now().UTC().Add(-time.Second),
		DeploymentID: &targetID, AppVersion: targetDeployment.Version, ContentDigest: targetDeployment.ContentDigest,
		ProducerFingerprint: fingerprint, ProducerCommandJSON: canonical, PublishesData: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	zero := 0
	if _, err := store.CompleteScheduleRunAndEnqueueActivation(db.CompleteScheduleRunParams{
		RunID: seedRun, Status: "succeeded", ExitCode: &zero, FinishedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	targetRuntime := newManifestFakeRuntime()
	targetJobs, err := jobs.NewManager(process.NewManager(t.TempDir(), targetRuntime), nil, process.DefaultTier, store, nil, sharedData, sharedData)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: store, jobs: targetJobs}
	plan, err := srv.planPrestartSchedules(app, nil, targetDeployment.ContentDigest)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.producers) != 0 {
		t.Fatalf("initial target state unexpectedly stale: %+v", plan.producers)
	}
	releaseProducerGates := targetJobs.AcquireProducerGates(plan.gateIDs)
	defer releaseProducerGates()

	type fenceResult struct {
		release func()
		err     error
	}
	fenced := make(chan fenceResult, 1)
	barrierEntered := true // model the deploy's already-durable compatibility barrier
	go func() {
		release, err := srv.convergePrestartAndFenceConsumer(
			plan, targetDeployment.ContentDigest, app, targetDeployment, &barrierEntered,
		)
		fenced <- fenceResult{release: release, err: err}
	}()
	select {
	case result := <-fenced:
		if result.release != nil {
			result.release()
		}
		t.Fatalf("consumer fence crossed retiring writer early: %v", result.err)
	case <-time.After(50 * time.Millisecond):
	}
	close(oldRuntime.producerBlock)
	var result fenceResult
	select {
	case result = <-fenced:
	case <-time.After(2 * time.Second):
		t.Fatal("consumer convergence did not recover after retiring writer")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer result.release()
	state, err := store.GetScheduleProducerState(scheduleID)
	if err != nil {
		t.Fatal(err)
	}
	if state.ContentDigest != targetDeployment.ContentDigest || state.ProducerFingerprint != fingerprint {
		t.Fatalf("consumer fence returned over stale producer state: %+v", state)
	}
	targetRuntime.mu.Lock()
	targetEvents := append([]string(nil), targetRuntime.events...)
	targetRuntime.mu.Unlock()
	if len(targetEvents) != 1 || targetEvents[0] != "producer:"+targetDeployment.ContentDigest {
		t.Fatalf("target republish events=%v, want one candidate producer", targetEvents)
	}
}

// buildMultiFileBundleUpload builds a multipart body whose zip contains all
// provided files (path → content). This generalises buildBundleUpload to
// allow both app.py and shinyhub.toml in the same archive.
func buildMultiFileBundleUpload(t *testing.T, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("bundle", "bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(zipBuf.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, mw.FormDataContentType()
}

// TestDeploy_AppliesManifestAppAndSchedules_EndToEnd deploys a bundle that
// includes a shinyhub.toml with [app] settings and a [[schedule]] block,
// then verifies the DB reflects both phases. A second deploy with the same
// schedule name but a different cron verifies the upsert preserves the ID.
func TestDeploy_AppliesManifestAppAndSchedules_EndToEnd(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "myapp", Name: "My App", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	manifest := `
[app]
hibernate_timeout_minutes = 30
replicas = 2
max_sessions_per_replica = 10

[[schedule]]
name    = "nightly"
cron    = "0 0 * * *"
cmd     = "echo hello"
`

	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/myapp/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("first deploy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify [app] settings were applied.
	app, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if app.HibernateTimeoutMinutes == nil || *app.HibernateTimeoutMinutes != 30 {
		t.Errorf("hibernate_timeout_minutes = %v, want 30", app.HibernateTimeoutMinutes)
	}
	if app.Replicas != 2 {
		t.Errorf("replicas = %d, want 2", app.Replicas)
	}
	if app.MaxSessionsPerReplica != 10 {
		t.Errorf("max_sessions_per_replica = %d, want 10", app.MaxSessionsPerReplica)
	}

	// Verify the schedule was created.
	schedules, err := store.ListSchedulesByApp(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules) != 1 {
		t.Fatalf("expected 1 schedule, got %d", len(schedules))
	}
	firstSchedule := schedules[0]
	if firstSchedule.Name != "nightly" {
		t.Errorf("schedule name = %q, want nightly", firstSchedule.Name)
	}
	if firstSchedule.CronExpr != "0 0 * * *" {
		t.Errorf("cron_expr = %q, want 0 0 * * *", firstSchedule.CronExpr)
	}
	firstID := firstSchedule.ID

	// Second deploy: same schedule name, different cron. Upsert must preserve ID.
	manifest2 := `
[[schedule]]
name    = "nightly"
cron    = "0 6 * * *"
cmd     = "echo hello"
`
	body2, ctype2 := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest2,
	})
	req2 := httptest.NewRequest("POST", "/api/apps/myapp/deploy", body2)
	req2.Header.Set("Content-Type", ctype2)
	req2.Header.Set("Authorization", "Bearer "+token)

	rec2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("second deploy: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	schedules2, err := store.ListSchedulesByApp(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(schedules2) != 1 {
		t.Fatalf("expected 1 schedule after re-deploy, got %d", len(schedules2))
	}
	if schedules2[0].ID != firstID {
		t.Errorf("upsert lost id: %d → %d (want stable id)", firstID, schedules2[0].ID)
	}
	if schedules2[0].CronExpr != "0 6 * * *" {
		t.Errorf("cron not updated: %q, want 0 6 * * *", schedules2[0].CronExpr)
	}
}

// TestDeploy_AppliesManifestAutoscale_EndToEnd deploys a bundle whose
// shinyhub.toml declares an [app] autoscale block and verifies the policy is
// reconciled into the app row and echoed in the deploy response summary — the
// full production path (LoadManifest → validate → applyManifestAppSettings).
func TestDeploy_AppliesManifestAutoscale_EndToEnd(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "scaler", Name: "Scaler", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	manifest := `
[app]
replicas = 1
autoscale = { enabled = true, min_replicas = 1, max_replicas = 3, target = 0.8 }
`
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/scaler/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	app, err := store.GetAppBySlug("scaler")
	if err != nil {
		t.Fatal(err)
	}
	if !app.AutoscaleEnabled {
		t.Errorf("AutoscaleEnabled = false, want true")
	}
	if app.AutoscaleMinReplicas != 1 || app.AutoscaleMaxReplicas != 3 {
		t.Errorf("autoscale bounds = [%d,%d], want [1,3]", app.AutoscaleMinReplicas, app.AutoscaleMaxReplicas)
	}
	if app.AutoscaleTarget != 0.8 {
		t.Errorf("autoscale target = %v, want 0.8", app.AutoscaleTarget)
	}

	// The deploy response summary must echo the autoscale block so the CLI can
	// show "Applied [app] settings: ...".
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	appSummary, ok := resp["manifest"].(map[string]any)["app"].(map[string]any)
	if !ok {
		t.Fatalf("manifest.app missing in response: %s", rec.Body.String())
	}
	as, ok := appSummary["autoscale"].(map[string]any)
	if !ok {
		t.Fatalf("manifest.app.autoscale missing: %v", appSummary)
	}
	if as["enabled"] != true {
		t.Errorf("summary autoscale.enabled = %v, want true", as["enabled"])
	}
	if v, _ := as["max_replicas"].(float64); int(v) != 3 {
		t.Errorf("summary autoscale.max_replicas = %v, want 3", as["max_replicas"])
	}
}

// TestDeploy_ManifestAutoscaleExceedsMaxReplicas_Fails400 verifies the
// server-policy ceiling (runtime MaxReplicas) rejects an autoscale block whose
// max_replicas exceeds it with 400, and Phase A never runs (autoscale stays
// off on the app row).
func TestDeploy_ManifestAutoscaleExceedsMaxReplicas_Fails400(t *testing.T) {
	srv, store, token := newManifestE2EServerCfg(t, config.RuntimeConfig{MaxReplicas: 2})
	admin, _ := store.GetUserByUsername("admin")

	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "capped", Name: "Capped", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	manifest := `
[app]
autoscale = { enabled = true, min_replicas = 1, max_replicas = 5, target = 0.8 }
`
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/capped/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	app, _ := store.GetAppBySlug("capped")
	if app.AutoscaleEnabled {
		t.Errorf("autoscale enabled despite policy rejection (Phase A must not have run)")
	}
}

// TestDeploy_ManifestBadAppSettingFails400 verifies that a bundle containing
// a shinyhub.toml with an invalid [app] setting (replicas = -1) results in
// HTTP 400 and leaves the app row unchanged (no partial write).
func TestDeploy_ManifestBadAppSettingFails400(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "badapp", Name: "Bad App", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}
	// Record baseline replica count before the bad deploy.
	appBefore, _ := store.GetAppBySlug("badapp")
	replicasBefore := appBefore.Replicas

	manifest := "[app]\nreplicas = -1\n"
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/badapp/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}

	// The app row must not have been modified.
	appAfter, _ := store.GetAppBySlug("badapp")
	if appAfter.Replicas != replicasBefore {
		t.Errorf("replicas mutated: %d → %d (want no change on 400)", replicasBefore, appAfter.Replicas)
	}
}

// TestDeploy_ResponseIncludesManifestSummary asserts the deploy response
// embeds a "manifest" object describing what [app] settings and schedules
// were applied. This is the wire shape the CLI's formatManifestSummary
// parses; changing either side without updating the other regresses the
// "Applied [app] settings: ..." line.
func TestDeploy_ResponseIncludesManifestSummary(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "summary", Name: "Summary App", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	manifest := `
[app]
replicas = 2
max_sessions_per_replica = 8

[[schedule]]
name = "nightly"
cron = "0 0 * * *"
cmd  = "echo n"
`
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/summary/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v: %s", err, rec.Body.String())
	}

	// Top-level app fields must still be present (CLI deploy.go reads
	// deploy_count from the top level).
	if _, ok := resp["deploy_count"]; !ok {
		t.Errorf("top-level deploy_count missing; CLI summary would lose deployment number")
	}

	manifestSummary, ok := resp["manifest"].(map[string]any)
	if !ok {
		t.Fatalf(`response missing "manifest" object: %s`, rec.Body.String())
	}
	app, ok := manifestSummary["app"].(map[string]any)
	if !ok {
		t.Fatalf(`manifest.app missing: %v`, manifestSummary)
	}
	if v, _ := app["replicas"].(float64); int(v) != 2 {
		t.Errorf("manifest.app.replicas = %v, want 2", app["replicas"])
	}
	if v, _ := app["max_sessions_per_replica"].(float64); int(v) != 8 {
		t.Errorf("manifest.app.max_sessions_per_replica = %v, want 8", app["max_sessions_per_replica"])
	}

	schedules, ok := manifestSummary["schedules"].([]any)
	if !ok || len(schedules) != 1 {
		t.Fatalf("manifest.schedules = %v, want one entry", manifestSummary["schedules"])
	}
	first, _ := schedules[0].(map[string]any)
	if first["name"] != "nightly" || first["action"] != "created" {
		t.Errorf("schedule entry = %v, want {name:nightly action:created}", first)
	}

	// Second deploy of the same schedule must report action=updated.
	body2, ctype2 := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req2 := httptest.NewRequest("POST", "/api/apps/summary/deploy", body2)
	req2.Header.Set("Content-Type", ctype2)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second deploy: expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	var resp2 map[string]any
	_ = json.Unmarshal(rec2.Body.Bytes(), &resp2)
	schedules2 := resp2["manifest"].(map[string]any)["schedules"].([]any)
	first2, _ := schedules2[0].(map[string]any)
	if first2["action"] != "updated" {
		t.Errorf("second deploy action = %v, want updated", first2["action"])
	}
}

// TestDeploy_ResponseSurfacesHooksSkipped asserts that when the runtime
// prepares deps inside a container (HostPreparesDeps == false), declared
// post-deploy hooks are reported in the deploy response as hooks_skipped so
// the developer learns their hooks did not run. The fake runtime used here is
// container-mode, so a bundle with two [[hook]] blocks must report 2.
func TestDeploy_ResponseSurfacesHooksSkipped(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "hooked", Name: "Hooked App", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	manifest := `
[[hook]]
on = "post-deploy"
command = ["echo", "one"]

[[hook]]
on = "post-deploy"
command = ["echo", "two"]
`
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/hooked/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v: %s", err, rec.Body.String())
	}
	v, ok := resp["hooks_skipped"].(float64)
	if !ok {
		t.Fatalf("response missing hooks_skipped: %s", rec.Body.String())
	}
	if int(v) != 2 {
		t.Errorf("hooks_skipped = %v, want 2", resp["hooks_skipped"])
	}
	if resp["hooks_declared"] != float64(2) {
		t.Errorf("hooks_declared = %v, want 2", resp["hooks_declared"])
	}
	if _, ok := resp["hooks_run"]; ok {
		t.Errorf("hooks_run should be omitted when no hooks ran; got %v", resp["hooks_run"])
	}
}

// TestDeploy_ResponseOmitsHooksSkippedWhenNone asserts hooks_skipped is absent
// from the response when no hooks were skipped, keeping the wire shape clean.
func TestDeploy_ResponseOmitsHooksSkippedWhenNone(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "nohooks", Name: "No Hooks", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py": "from shiny import App\n",
	})
	req := httptest.NewRequest("POST", "/api/apps/nohooks/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if _, ok := resp["hooks_skipped"]; ok {
		t.Errorf("expected no hooks_skipped key when none skipped; got %v", resp["hooks_skipped"])
	}
	if _, ok := resp["hooks_declared"]; ok {
		t.Errorf("expected no hooks_declared key when none declared; got %v", resp["hooks_declared"])
	}
	if _, ok := resp["hooks_run"]; ok {
		t.Errorf("expected no hooks_run key when none ran; got %v", resp["hooks_run"])
	}
}

// TestDeploy_ResponseOmitsManifestWhenAbsent asserts that a bundle without
// a shinyhub.toml produces a deploy response with NO "manifest" key, so the
// CLI prints no spurious summary line.
func TestDeploy_ResponseOmitsManifestWhenAbsent(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "plain", Name: "Plain App", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py": "from shiny import App\n",
	})
	req := httptest.NewRequest("POST", "/api/apps/plain/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if _, ok := resp["manifest"]; ok {
		t.Errorf("expected no manifest key when bundle has no shinyhub.toml; got %v", resp["manifest"])
	}
}

// TestDeploy_ManifestPolicyViolation_LeavesRunningPoolIntact verifies that a
// manifest rejected by server-policy validation (replicas > MaxReplicas)
// returns 400 BEFORE the running pool is torn down. The PIDs from the prior
// deploy must still be alive in the manager after the rejection.
func TestDeploy_ManifestPolicyViolation_LeavesRunningPoolIntact(t *testing.T) {
	srv, store, token := newManifestE2EServerCfg(t, config.RuntimeConfig{MaxReplicas: 2})
	admin, _ := store.GetUserByUsername("admin")

	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "polapp", Name: "Policy App", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	// First deploy: legal manifest, pool comes up with 2 replicas.
	manifest1 := `
[app]
replicas = 2
`
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest1,
	})
	req := httptest.NewRequest("POST", "/api/apps/polapp/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("first deploy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	beforePIDs := pidsForSlug(srv, "polapp")
	if len(beforePIDs) == 0 {
		t.Fatalf("expected running replicas after first deploy, got none")
	}

	// Second deploy: replicas exceeds policy. Must return 400 and leave the
	// running pool untouched (no Stop, no Deregister).
	manifest2 := `
[app]
replicas = 5
`
	body2, ctype2 := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest2,
	})
	req2 := httptest.NewRequest("POST", "/api/apps/polapp/deploy", body2)
	req2.Header.Set("Content-Type", ctype2)
	req2.Header.Set("Authorization", "Bearer "+token)
	rec2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusBadRequest {
		t.Fatalf("policy violation: expected 400, got %d: %s", rec2.Code, rec2.Body.String())
	}

	afterPIDs := pidsForSlug(srv, "polapp")
	if !samePIDSet(beforePIDs, afterPIDs) {
		t.Errorf("pool was disturbed by rejected deploy: before=%v after=%v", beforePIDs, afterPIDs)
	}

	// App status must remain "running" — Phase A never ran, nothing to mark.
	appAfter, _ := store.GetAppBySlug("polapp")
	if appAfter.Status == "degraded" {
		t.Errorf("app marked degraded by policy rejection (want unchanged status)")
	}
	if appAfter.Replicas != 2 {
		t.Errorf("replicas mutated by rejected deploy: %d (want 2)", appAfter.Replicas)
	}
}

func pidsForSlug(srv *Server, slug string) []int {
	infos := srv.manager.AllForSlug(slug)
	pids := make([]int, 0, len(infos))
	for _, p := range infos {
		pids = append(pids, p.PID)
	}
	return pids
}

func samePIDSet(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[int]struct{}, len(a))
	for _, p := range a {
		set[p] = struct{}{}
	}
	for _, p := range b {
		if _, ok := set[p]; !ok {
			return false
		}
	}
	return true
}

// TestDeployRecordsContentDigest verifies that a successful deploy stores a
// non-empty content_digest on the promoted deployment row.
func TestDeployRecordsContentDigest(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "digest-e2e", Name: "Digest E2E", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py": "print(1)\n",
	})
	req := httptest.NewRequest("POST", "/api/apps/digest-e2e/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy status %d: %s", rec.Code, rec.Body.String())
	}

	var digest *string
	row := store.DB().QueryRow(`
		SELECT content_digest FROM deployments
		WHERE app_id = (SELECT id FROM apps WHERE slug = ?)
		  AND status = 'succeeded'
		ORDER BY id DESC LIMIT 1`, "digest-e2e")
	if err := row.Scan(&digest); err != nil {
		t.Fatalf("scan digest: %v", err)
	}
	if digest == nil || *digest == "" {
		t.Fatal("promoted deployment must carry a content_digest")
	}
}

// newManifestE2EServerWithJobs is like newManifestE2EServer but wires a real
// jobs.Manager (backed by the manifest fake runtime, whose RunOnce returns
// success) so deploy-triggered runs actually execute and record provenance.
func newManifestE2EServerWithJobs(t *testing.T) (*Server, *db.Store, string, *manifestFakeRuntime) {
	t.Helper()
	srv, store, token, mgr, appsDir, fakeRuntime := buildManifestE2EServer(t, config.RuntimeConfig{})
	jm, err := jobs.NewManager(mgr, nil, process.DefaultTier, store, nil, appsDir, appsDir)
	if err != nil {
		t.Fatalf("jobs.NewManager: %v", err)
	}
	srv.SetJobs(jm, scheduler.New(jm, store, time.UTC))
	return srv, store, token, fakeRuntime
}

// scheduleIDByName resolves a schedule's id by listing the app's schedules.
func scheduleIDByName(t *testing.T, store *db.Store, appID int64, name string) int64 {
	t.Helper()
	rows, err := store.ListSchedulesByApp(appID)
	if err != nil {
		t.Fatal(err)
	}
	for _, sc := range rows {
		if sc.Name == name {
			return sc.ID
		}
	}
	t.Fatalf("schedule %q not found", name)
	return 0
}

func waitForDeployRunCount(t *testing.T, store *db.Store, scheduleID int64, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListScheduleRuns(scheduleID, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		count := 0
		for _, r := range runs {
			if r.Trigger == "deploy" && r.Status == "succeeded" {
				count++
			}
		}
		if count >= want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("fewer than %d succeeded deploy runs for schedule %d within deadline", want, scheduleID)
}

func TestDeploy_BundleChangeTriggerTracksProducerBundle(t *testing.T) {
	srv, store, token, fakeRuntime := newManifestE2EServerWithJobs(t)
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "warmapp", Name: "warmapp", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("warmapp")

	manifest := `
[[schedule]]
name = "warm"
cron = "0 5 * * *"
cmd = "true"
deploy_trigger = "bundle_change"
`
	body, ct := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":                "print('x')",
		"helpers/fetch_data.py": "print('producer-v1')",
		"shinyhub.toml":         manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/warmapp/deploy", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("deploy status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var firstResponse struct {
		ScheduleConvergence []ScheduleConvergenceResult `json:"schedule_convergence"`
		Manifest            ManifestApplied             `json:"manifest"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &firstResponse); err != nil {
		t.Fatalf("decode first deploy response: %v", err)
	}
	if len(firstResponse.ScheduleConvergence) != 1 || !firstResponse.ScheduleConvergence[0].Prestart ||
		firstResponse.ScheduleConvergence[0].RunID == nil {
		t.Fatalf("first deploy convergence = %+v, want prestart with actual run id", firstResponse.ScheduleConvergence)
	}
	if len(firstResponse.Manifest.Schedules) != 1 || firstResponse.Manifest.Schedules[0].Action != "created" {
		t.Fatalf("first manifest schedule action = %+v, want created", firstResponse.Manifest.Schedules)
	}
	fakeRuntime.mu.Lock()
	firstEvents := append([]string(nil), fakeRuntime.events...)
	fakeRuntime.mu.Unlock()
	if len(firstEvents) < 2 || !strings.HasPrefix(firstEvents[0], "producer:sha256:") ||
		!strings.HasPrefix(firstEvents[1], "start:sha256:") ||
		strings.TrimPrefix(firstEvents[0], "producer:") != strings.TrimPrefix(firstEvents[1], "start:") {
		t.Fatalf("candidate producer must complete before matching consumer starts; events=%v", firstEvents)
	}
	schedID := scheduleIDByName(t, store, app.ID, "warm")
	waitForDeployRunCount(t, store, schedID, 1)
	replicas, err := store.ListReplicas(app.ID)
	if err != nil || len(replicas) != 1 {
		t.Fatalf("first deploy replicas=%+v err=%v", replicas, err)
	}
	if replicas[0].DataGeneration == 0 || replicas[0].ActivationID != nil ||
		replicas[0].DataProducerDeploymentID == nil || replicas[0].DataProducerContentDigest == "" ||
		replicas[0].DataProducerFingerprint == "" {
		t.Fatalf("prestart replica provenance = %+v", replicas[0])
	}

	runsAfterFirst, _ := store.ListScheduleRuns(schedID, 50, 0)
	deployCount := 0
	for _, r := range runsAfterFirst {
		if r.Trigger == "deploy" {
			deployCount++
			if r.ContentDigest == "" || r.DeploymentID == nil || r.AppVersion == "" {
				t.Fatalf("deploy run missing bundle provenance: %+v", r)
			}
		}
	}
	if deployCount != 1 {
		t.Fatalf("after first deploy: deploy runs = %d, want 1", deployCount)
	}

	// An identical deployment is already satisfied because its producer is
	// still the authoritative last writer.
	body2, ct2 := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":                "print('x')",
		"helpers/fetch_data.py": "print('producer-v1')",
		"shinyhub.toml":         manifest,
	})
	req2 := httptest.NewRequest("POST", "/api/apps/warmapp/deploy", body2)
	req2.Header.Set("Content-Type", ct2)
	req2.Header.Set("Authorization", "Bearer "+token)
	rr2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("redeploy status = %d, body=%s", rr2.Code, rr2.Body.String())
	}
	runsAfterSecond, _ := store.ListScheduleRuns(schedID, 50, 0)
	deployCount = 0
	for _, r := range runsAfterSecond {
		if r.Trigger == "deploy" {
			deployCount++
		}
	}
	if deployCount != 1 {
		t.Errorf("after identical redeploy: deploy runs = %d, want 1", deployCount)
	}
	var secondResponse struct {
		ScheduleConvergence []ScheduleConvergenceResult `json:"schedule_convergence"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &secondResponse); err != nil {
		t.Fatal(err)
	}
	if len(secondResponse.ScheduleConvergence) != 1 || !secondResponse.ScheduleConvergence[0].Prestart ||
		secondResponse.ScheduleConvergence[0].RunID == nil ||
		*secondResponse.ScheduleConvergence[0].RunID != *firstResponse.ScheduleConvergence[0].RunID {
		t.Fatalf("identical redeploy convergence=%+v, want satisfied-on-entry prestart provenance", secondResponse.ScheduleConvergence)
	}
	secondReplicas, err := store.ListReplicas(app.ID)
	if err != nil || len(secondReplicas) != 1 {
		t.Fatalf("identical redeploy replicas=%+v err=%v", secondReplicas, err)
	}
	if secondReplicas[0].DataGeneration != replicas[0].DataGeneration ||
		secondReplicas[0].DataProducerDeploymentID == nil || replicas[0].DataProducerDeploymentID == nil ||
		*secondReplicas[0].DataProducerDeploymentID != *replicas[0].DataProducerDeploymentID ||
		secondReplicas[0].DataProducerContentDigest != replicas[0].DataProducerContentDigest ||
		secondReplicas[0].DataProducerFingerprint != replicas[0].DataProducerFingerprint {
		t.Fatalf("identical redeploy lost durable publication: first=%+v second=%+v", replicas[0], secondReplicas[0])
	}

	// Changing only code behind the unchanged command pointer changes the bundle
	// digest and must dispatch a new producer run.
	body3, ct3 := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":                "print('x')",
		"helpers/fetch_data.py": "print('producer-v2')",
		"shinyhub.toml":         manifest,
	})
	req3 := httptest.NewRequest("POST", "/api/apps/warmapp/deploy", body3)
	req3.Header.Set("Content-Type", ct3)
	req3.Header.Set("Authorization", "Bearer "+token)
	rr3 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr3, req3)
	if rr3.Code != http.StatusOK {
		t.Fatalf("changed producer deploy status = %d, body=%s", rr3.Code, rr3.Body.String())
	}
	waitForDeployRunCount(t, store, schedID, 2)
	runsAfterThird, _ := store.ListScheduleRuns(schedID, 50, 0)
	digests := map[string]bool{}
	for _, r := range runsAfterThird {
		if r.Trigger == "deploy" {
			digests[r.ContentDigest] = true
		}
	}
	if len(digests) != 2 {
		t.Fatalf("deploy-run digests = %v, want two producer bundles", digests)
	}
}

func TestDeploy_ProducerSuccessConsumerFailureRetryRepublishesBeforeClearingBarrier(t *testing.T) {
	srv, store, token, fakeRuntime := newManifestE2EServerWithJobs(t)
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "durable-retry", Name: "durable-retry", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("durable-retry")
	if err != nil {
		t.Fatal(err)
	}
	failActivation := true
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		p.HealthCheck = func(string, time.Duration, http.RoundTripper) error { return nil }
		if !p.PrepareOnly && failActivation {
			failActivation = false
			return nil, errors.New("injected consumer startup failure")
		}
		return deploy.Run(p)
	})
	manifest := `
[[schedule]]
name = "warm"
cron = "0 5 * * *"
cmd = "python producer.py"
deploy_trigger = "bundle_change"
`
	deployBundle := func() *httptest.ResponseRecorder {
		t.Helper()
		body, contentType := buildMultiFileBundleUpload(t, map[string]string{
			"app.py": "print('consumer')", "producer.py": "print('producer')", "shinyhub.toml": manifest,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/apps/durable-retry/deploy", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}
	first := deployBundle()
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first deploy status=%d body=%s", first.Code, first.Body.String())
	}
	history, err := store.ListDeploymentsBySlug(app.Slug)
	if err != nil || len(history) != 1 || history[0].Status != db.DeploymentFailed {
		t.Fatalf("failed deployment history=%+v err=%v", history, err)
	}
	if quarantined, err := store.AppCompatibilityQuarantined(app.ID); err != nil || !quarantined {
		t.Fatalf("quarantine after failed consumer=%v err=%v", quarantined, err)
	}
	scheduleID := scheduleIDByName(t, store, app.ID, "warm")
	runs, err := store.ListScheduleRuns(scheduleID, 50, 0)
	if err != nil || len(runs) != 1 || runs[0].Status != "succeeded" {
		t.Fatalf("producer runs after failed consumer=%+v err=%v", runs, err)
	}

	second := deployBundle()
	if second.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", second.Code, second.Body.String())
	}
	runs, err = store.ListScheduleRuns(scheduleID, 50, 0)
	if err != nil || len(runs) != 2 {
		t.Fatalf("corrective retry did not republish exactly once: runs=%+v err=%v", runs, err)
	}
	replicas, err := store.ListReplicas(app.ID)
	if err != nil || len(replicas) != 1 {
		t.Fatalf("retry replicas=%+v err=%v", replicas, err)
	}
	if replicas[0].DataGeneration == 0 || replicas[0].DataProducerDeploymentID == nil ||
		*replicas[0].DataProducerDeploymentID == history[0].ID ||
		replicas[0].DataProducerContentDigest == "" || replicas[0].DataProducerFingerprint == "" {
		t.Fatalf("retry lost failed-attempt publication provenance: %+v", replicas[0])
	}
	if quarantined, err := store.AppCompatibilityQuarantined(app.ID); err != nil || quarantined {
		t.Fatalf("quarantine after successful corrective retry=%v err=%v", quarantined, err)
	}
	fakeRuntime.mu.Lock()
	producerCalls := len(fakeRuntime.producerCommands)
	fakeRuntime.mu.Unlock()
	if producerCalls != 2 {
		t.Fatalf("producer calls=%d, want corrective retry to republish exactly once", producerCalls)
	}
}

func TestDeploy_StoppedCorrectiveTargetWithoutProducerCannotLaunderFailedBarrier(t *testing.T) {
	srv, store, token, _ := newManifestE2EServerWithJobs(t)
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "stopped-repair", Name: "stopped-repair", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("stopped-repair")
	if err != nil {
		t.Fatal(err)
	}
	deployFiles := func(files map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		body, contentType := buildMultiFileBundleUpload(t, files)
		req := httptest.NewRequest(http.MethodPost, "/api/apps/stopped-repair/deploy", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}
	if baseline := deployFiles(map[string]string{"app.py": "print('old')"}); baseline.Code != http.StatusOK {
		t.Fatalf("baseline status=%d body=%s", baseline.Code, baseline.Body.String())
	}
	failed, err := store.BeginDeployment(app.ID, "failed-producer", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDeploymentProducerBarrierEntered(failed.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.QuarantineAndFailDeployment(failed.ID, "injected failed producer barrier"); err != nil {
		t.Fatal(err)
	}
	if err := srv.manager.Stop(app.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE apps SET status = 'stopped' WHERE id = ?`, app.ID); err != nil {
		t.Fatal(err)
	}

	corrective := deployFiles(map[string]string{"app.py": "print('new but no producer')"})
	if corrective.Code != http.StatusInternalServerError || !strings.Contains(corrective.Body.String(), "declares no enabled deploy-triggered producer") {
		t.Fatalf("producer-free corrective deploy status=%d body=%s", corrective.Code, corrective.Body.String())
	}
	if quarantined, err := store.AppCompatibilityQuarantined(app.ID); err != nil || !quarantined {
		t.Fatalf("producer-free corrective deploy laundered quarantine=%v err=%v", quarantined, err)
	}
	got, err := store.GetAppBySlug(app.Slug)
	if err != nil || got.Status != "stopped" {
		t.Fatalf("operator-stopped app=%+v err=%v, want stopped", got, err)
	}
}

func TestDeploy_FailedFirstProducerPreservesRunHistory(t *testing.T) {
	srv, store, token, fakeRuntime := newManifestE2EServerWithJobs(t)
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "failed-first-producer", Name: "failed-first-producer", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("failed-first-producer")
	if err != nil {
		t.Fatal(err)
	}
	fakeRuntime.mu.Lock()
	fakeRuntime.producerErr = errors.New("injected producer launch failure")
	fakeRuntime.mu.Unlock()

	manifest := `
[[schedule]]
name = "cache"
cron = "0 5 * * *"
cmd = "python producer.py"
deploy_trigger = "bundle_change"
`
	body, contentType := buildMultiFileBundleUpload(t, map[string]string{
		"app.py": "print('consumer')", "producer.py": "print('producer')", "shinyhub.toml": manifest,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/apps/failed-first-producer/deploy", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("deploy status=%d body=%s", rec.Code, rec.Body.String())
	}

	schedules, err := store.ListSchedulesByApp(app.ID)
	if err != nil || len(schedules) != 1 {
		t.Fatalf("planning schedule history=%+v err=%v", schedules, err)
	}
	if schedules[0].Enabled || schedules[0].DeployTrigger != schedulespec.DeployTriggerNever {
		t.Fatalf("failed placeholder became schedulable: %+v", schedules[0])
	}
	runs, err := store.ListScheduleRuns(schedules[0].ID, 10, 0)
	if err != nil || len(runs) != 1 || runs[0].Status != "failed" || runs[0].Trigger != "deploy" {
		t.Fatalf("failed producer diagnostic history=%+v err=%v", runs, err)
	}
	history, err := store.ListDeploymentsBySlug("failed-first-producer")
	if err != nil || len(history) != 1 || history[0].Status != db.DeploymentFailed {
		t.Fatalf("failed deployment history=%+v err=%v", history, err)
	}
}

func TestDeploy_FailedCorrectiveRetryInheritsQuarantineAndNeverRestoresOldConsumer(t *testing.T) {
	srv, store, token, fakeRuntime := newManifestE2EServerWithJobs(t)
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "failed-repair", Name: "failed-repair", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("failed-repair")
	if err != nil {
		t.Fatal(err)
	}
	deployFiles := func(files map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		body, contentType := buildMultiFileBundleUpload(t, files)
		req := httptest.NewRequest(http.MethodPost, "/api/apps/failed-repair/deploy", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}
	baseline := deployFiles(map[string]string{"app.py": "print('old consumer')"})
	if baseline.Code != http.StatusOK {
		t.Fatalf("baseline deploy status=%d body=%s", baseline.Code, baseline.Body.String())
	}
	if _, err := store.CreateSchedule(db.CreateScheduleParams{
		AppID: app.ID, Name: "cache", CronExpr: "0 5 * * *", CommandJSON: `["python","producer.py"]`,
		Enabled: true, TimeoutSeconds: 60, OverlapPolicy: "skip", MissedPolicy: "skip",
		DeployTrigger: schedulespec.DeployTriggerBundleChange,
	}); err != nil {
		t.Fatal(err)
	}

	activationAttempts := 0
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		p.HealthCheck = func(string, time.Duration, http.RoundTripper) error { return nil }
		if !p.PrepareOnly {
			activationAttempts++
			if activationAttempts <= 2 {
				return nil, fmt.Errorf("injected corrective consumer failure %d", activationAttempts)
			}
		}
		return deploy.Run(p)
	})
	targetFiles := map[string]string{
		"app.py": "print('new consumer')", "producer.py": "print('new producer')",
	}
	first := deployFiles(targetFiles)
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first corrective deploy status=%d body=%s", first.Code, first.Body.String())
	}
	if quarantined, err := store.AppCompatibilityQuarantined(app.ID); err != nil || !quarantined {
		t.Fatalf("quarantine after producer/consumer split=%v err=%v", quarantined, err)
	}
	fakeRuntime.mu.Lock()
	producerCallsAfterFirst := len(fakeRuntime.producerCommands)
	fakeRuntime.mu.Unlock()
	if producerCallsAfterFirst != 1 {
		t.Fatalf("first corrective deploy producer calls=%d, want 1", producerCallsAfterFirst)
	}

	second := deployFiles(targetFiles)
	if second.Code != http.StatusInternalServerError {
		t.Fatalf("failed retry status=%d body=%s", second.Code, second.Body.String())
	}
	if activationAttempts != 2 {
		t.Fatalf("non-prepare activations=%d, want exactly two failed target starts and no old-pool restore", activationAttempts)
	}
	if quarantined, err := store.AppCompatibilityQuarantined(app.ID); err != nil || !quarantined {
		t.Fatalf("failed retry dropped quarantine=%v err=%v", quarantined, err)
	}
	if _, running := srv.manager.GetReplica(app.Slug, 0); running {
		t.Fatal("failed corrective retry restored a serving consumer")
	}
	fakeRuntime.mu.Lock()
	producerCallsAfterSecond := len(fakeRuntime.producerCommands)
	fakeRuntime.mu.Unlock()
	if producerCallsAfterSecond != 2 {
		t.Fatalf("failed corrective retry producer calls=%d, want one new repair publication", producerCallsAfterSecond)
	}

	third := deployFiles(targetFiles)
	if third.Code != http.StatusOK {
		t.Fatalf("successful repair status=%d body=%s", third.Code, third.Body.String())
	}
	if quarantined, err := store.AppCompatibilityQuarantined(app.ID); err != nil || quarantined {
		t.Fatalf("successful repair left quarantine=%v err=%v", quarantined, err)
	}
}

func TestDeploy_ReplicaProvenancePersistenceFailureStopsAndRefusesPromotion(t *testing.T) {
	srv, store, token, _ := newManifestE2EServerWithJobs(t)
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "replica-provenance-fail", Name: "replica-provenance-fail", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug("replica-provenance-fail")
	if err != nil {
		t.Fatal(err)
	}
	installDBFailureTrigger(t, store, dbFailureTrigger{
		name: "reject_booted_replica", table: "replicas", event: "INSERT", condition: "TRUE",
	})
	body, contentType := buildMultiFileBundleUpload(t, map[string]string{"app.py": "print('consumer')"})
	req := httptest.NewRequest(http.MethodPost, "/api/apps/replica-provenance-fail/deploy", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("deploy status=%d body=%s", rec.Code, rec.Body.String())
	}
	history, err := store.ListDeploymentsBySlug(app.Slug)
	if err != nil || len(history) != 1 || history[0].Status != db.DeploymentFailed {
		t.Fatalf("deployment was promoted without replica provenance: history=%+v err=%v", history, err)
	}
	if _, running := srv.manager.GetReplica(app.Slug, 0); running {
		t.Fatal("consumer remained running after provenance persistence failure")
	}
	if quarantined, err := store.AppCompatibilityQuarantined(app.ID); err != nil || quarantined {
		t.Fatalf("non-producing provenance failure quarantine=%v err=%v", quarantined, err)
	}
	stopped, err := store.GetAppBySlug(app.Slug)
	if err != nil || stopped.Status != "stopped" {
		t.Fatalf("app after provenance failure=%+v err=%v", stopped, err)
	}
}

func TestDeploy_PostCommitConvergenceFailureReturnsSuccessWithWarning(t *testing.T) {
	srv, store, token, _ := newManifestE2EServerWithJobs(t)
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "postcommit-warning", Name: "postcommit-warning", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	installDBFailureTrigger(t, store, dbFailureTrigger{
		name: "reject_postcommit_obligation", table: "schedule_deploy_obligations", event: "INSERT", condition: "TRUE",
	})
	manifest := `
[[schedule]]
name = "cache"
cron = "0 5 * * *"
cmd = "python producer.py"
deploy_trigger = "bundle_change"
`
	body, contentType := buildMultiFileBundleUpload(t, map[string]string{
		"app.py": "print('consumer')", "producer.py": "print('producer')", "shinyhub.toml": manifest,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/apps/postcommit-warning/deploy", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("committed deploy status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Warning string `json:"warning"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Warning, "deployment committed") || !strings.Contains(response.Warning, "repair asynchronously") {
		t.Fatalf("post-commit warning=%q", response.Warning)
	}
	history, err := store.ListDeploymentsBySlug("postcommit-warning")
	if err != nil || len(history) != 1 || history[0].Status != db.DeploymentSucceeded {
		t.Fatalf("committed deployment history=%+v err=%v", history, err)
	}
}

func TestDeploy_DeployTriggerDisabledNotFired(t *testing.T) {
	srv, store, token, _ := newManifestE2EServerWithJobs(t)
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "warmapp", Name: "warmapp", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("warmapp")
	manifest := `
[[schedule]]
name = "warm"
cron = "0 5 * * *"
cmd = "true"
disabled = true
deploy_trigger = "bundle_change"
`
	body, ct := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "print('x')",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/warmapp/deploy", body)
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("deploy status = %d, body=%s", rr.Code, rr.Body.String())
	}
	schedID := scheduleIDByName(t, store, app.ID, "warm")
	// Settle: if a dispatch were ever going to happen it would be queued
	// synchronously inside the deploy handler before it returns; the fake
	// runtime's RunOnce has no I/O. A brief wait is therefore sufficient to
	// prove the disabled gate prevented any run.
	time.Sleep(100 * time.Millisecond)
	runs, _ := store.ListScheduleRuns(schedID, 50, 0)
	if len(runs) != 0 {
		t.Errorf("disabled schedule: runs = %d, want 0", len(runs))
	}
}

func TestRollback_BundleProducerCompletesBeforeConsumerStart(t *testing.T) {
	srv, store, token, fakeRuntime := newManifestE2EServerWithJobs(t)
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "rollback-warm", Name: "rollback-warm", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	manifest := func(command string) string {
		return fmt.Sprintf(`
[[schedule]]
name = "warm"
cron = "0 5 * * *"
cmd = "python %s"
deploy_trigger = "bundle_change"
`, command)
	}
	deployVersion := func(producer, command string) {
		t.Helper()
		body, contentType := buildMultiFileBundleUpload(t, map[string]string{
			"app.py": "print('consumer')", "producer.txt": producer,
			"shinyhub.toml": manifest(command), command: "print('producer')",
		})
		req := httptest.NewRequest(http.MethodPost, "/api/apps/rollback-warm/deploy", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("deploy %q status=%d body=%s", producer, rec.Code, rec.Body.String())
		}
	}
	deployVersion("v1", "old.py")
	deployVersion("v2", "new.py")

	rec := httptest.NewRecorder()
	rollbackReq := httptest.NewRequest(http.MethodPost, "/api/apps/rollback-warm/rollback", nil)
	rollbackReq.Header.Set("Authorization", "Bearer "+token)
	srv.Router().ServeHTTP(rec, rollbackReq)
	if rec.Code != http.StatusOK {
		t.Fatalf("rollback status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		ScheduleConvergence []ScheduleConvergenceResult `json:"schedule_convergence"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.ScheduleConvergence) != 1 || response.ScheduleConvergence[0].Status != "satisfied" {
		t.Fatalf("rollback convergence=%+v, want one satisfied producer", response.ScheduleConvergence)
	}
	if !response.ScheduleConvergence[0].Prestart || response.ScheduleConvergence[0].RunID == nil {
		t.Fatalf("rollback convergence=%+v, want actual prestart run provenance", response.ScheduleConvergence)
	}

	fakeRuntime.mu.Lock()
	events := append([]string(nil), fakeRuntime.events...)
	commands := append([][]string(nil), fakeRuntime.producerCommands...)
	fakeRuntime.mu.Unlock()
	if len(events) < 2 {
		t.Fatalf("rollback events=%v", events)
	}
	producerEvent, startEvent := events[len(events)-2], events[len(events)-1]
	if !strings.HasPrefix(producerEvent, "producer:") || !strings.HasPrefix(startEvent, "start:") ||
		strings.TrimPrefix(producerEvent, "producer:") != strings.TrimPrefix(startEvent, "start:") {
		t.Fatalf("rollback producer must complete before matching consumer starts; events=%v", events)
	}
	if len(commands) < 3 || len(commands[len(commands)-1]) != 2 || commands[len(commands)-1][1] != "old.py" {
		t.Fatalf("rollback producer commands=%v, want immutable v1 declaration ending in old.py", commands)
	}
}

// TestDeploy_ManifestUnknownAppFieldFails400 verifies that a shinyhub.toml
// containing an unknown [app] key (strict-mode TOML) is rejected with 400.
func TestDeploy_ManifestUnknownAppFieldFails400(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "strictapp", Name: "Strict App", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	// "slug" is not a recognized [app] field.
	manifest := "[app]\nslug = \"x\"\n"
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/strictapp/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown manifest field, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestDeploy_AppliesManifestAccessGroups deploys a bundle whose shinyhub.toml
// declares an [access] block and asserts that the store reflects the rules as
// source='manifest' rows, and that the deploy response includes
// manifest.access_groups with one entry per group.
func TestDeploy_AppliesManifestAccessGroups(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: "accgrp", Name: "Access Groups App", OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}

	manifest := `
[access]
viewer_groups  = ["finance"]
manager_groups = ["leads"]
`
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "from shiny import App\n",
		"shinyhub.toml": manifest,
	})
	req := httptest.NewRequest("POST", "/api/apps/accgrp/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("deploy: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// (a) store has the expected manifest rows.
	rules, err := store.ListAppGroupAccess("accgrp")
	if err != nil {
		t.Fatal(err)
	}
	byGroup := map[string]db.AppGroupRule{}
	for _, r := range rules {
		byGroup[r.Group] = r
	}
	financeRule, ok := byGroup["finance"]
	if !ok {
		t.Errorf("expected rule for group 'finance', got rules=%v", rules)
	} else {
		if financeRule.Role != "viewer" {
			t.Errorf("finance role = %q, want viewer", financeRule.Role)
		}
		if financeRule.Source != "manifest" {
			t.Errorf("finance source = %q, want manifest", financeRule.Source)
		}
	}
	leadsRule, ok := byGroup["leads"]
	if !ok {
		t.Errorf("expected rule for group 'leads', got rules=%v", rules)
	} else {
		if leadsRule.Role != "manager" {
			t.Errorf("leads role = %q, want manager", leadsRule.Role)
		}
		if leadsRule.Source != "manifest" {
			t.Errorf("leads source = %q, want manifest", leadsRule.Source)
		}
	}

	// (b) deploy response includes manifest.access_groups with 2 entries.
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v: %s", err, rec.Body.String())
	}
	manifestObj, ok := resp["manifest"].(map[string]any)
	if !ok {
		t.Fatalf(`response missing "manifest" object: %s`, rec.Body.String())
	}
	accessGroups, ok := manifestObj["access_groups"].([]any)
	if !ok {
		t.Fatalf("manifest.access_groups missing or not an array: %v", manifestObj)
	}
	if len(accessGroups) != 2 {
		t.Errorf("manifest.access_groups has %d entries, want 2: %v", len(accessGroups), accessGroups)
	}
}
