package api

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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
)

// manifestFakeRuntime is a minimal Runtime for end-to-end deploy tests.
// It returns synthetic PIDs without spawning real processes, so deploy.Run
// can complete without uv/Rscript on the host.
type manifestFakeRuntime struct {
	mu      sync.Mutex
	nextPID int
	stops   map[int]chan struct{}
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

func (f *manifestFakeRuntime) Stats(_ context.Context, _ process.RunHandle) (float64, uint64, error) {
	return 0, 0, nil
}

func (f *manifestFakeRuntime) RunOnce(_ context.Context, _ process.StartParams, _ io.Writer) (process.ExitInfo, error) {
	return process.ExitInfo{}, nil
}

// HostPreparesDeps returns false so deploy.Run skips uv sync / renv::restore.
// Container-mode semantics: dependency installation is treated as a no-op on
// the host, which is exactly what we want for a test that never spawns real
// processes.
func (f *manifestFakeRuntime) HostPreparesDeps() bool    { return false }
func (f *manifestFakeRuntime) AppBindHost() string       { return "127.0.0.1" }
func (f *manifestFakeRuntime) HostProvidesAppData() bool { return true }

// buildManifestE2EServer constructs the shared scaffolding used by all
// manifest E2E server variants: temp appsDir, in-memory store, admin user +
// JWT, config, process manager with the fake runtime, Server, and a no-op
// health-check deploy runner. SetJobs is intentionally NOT called here so
// each variant can wire the scheduler it needs (nil vs real jobs.Manager).
func buildManifestE2EServer(t *testing.T, runtime config.RuntimeConfig) (srv *Server, store *db.Store, token string, mgr *process.Manager, appsDir string) {
	t.Helper()
	appsDir = t.TempDir()
	store = dbtest.New(t)

	hash, _ := auth.HashPassword("pass")
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

	mgr = process.NewManager(appsDir, newManifestFakeRuntime())
	srv = New(cfg, store, mgr, proxy.New())

	// Replace the deploy runner to inject a no-op health check so tests
	// complete instantly instead of waiting for the 120 s timeout. Sync hooks
	// are already bypassed because manifestFakeRuntime.HostPreparesDeps()
	// returns false (container-mode semantics: no host-side dep installation).
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		p.HealthCheck = func(string, time.Duration, http.RoundTripper) error { return nil }
		return deploy.Run(p)
	})
	return srv, store, token, mgr, appsDir
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
	srv, store, token, _, _ := buildManifestE2EServer(t, runtime)
	// Wire scheduler (not started — ErrNotStarted is treated as a soft warning).
	srv.SetJobs(nil, scheduler.New(nil, store, time.UTC))
	return srv, store, token
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

	if err := store.CreateApp(db.CreateAppParams{
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

	if err := store.CreateApp(db.CreateAppParams{
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

	if err := store.CreateApp(db.CreateAppParams{
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

	if err := store.CreateApp(db.CreateAppParams{
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

	if err := store.CreateApp(db.CreateAppParams{
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

	if err := store.CreateApp(db.CreateAppParams{
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
}

// TestDeploy_ResponseOmitsHooksSkippedWhenNone asserts hooks_skipped is absent
// from the response when no hooks were skipped, keeping the wire shape clean.
func TestDeploy_ResponseOmitsHooksSkippedWhenNone(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if err := store.CreateApp(db.CreateAppParams{
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
}

// TestDeploy_ResponseOmitsManifestWhenAbsent asserts that a bundle without
// a shinyhub.toml produces a deploy response with NO "manifest" key, so the
// CLI prints no spurious summary line.
func TestDeploy_ResponseOmitsManifestWhenAbsent(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if err := store.CreateApp(db.CreateAppParams{
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

	if err := store.CreateApp(db.CreateAppParams{
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

	if err := store.CreateApp(db.CreateAppParams{
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
// success) so run_on_register first-fires actually execute and record runs.
func newManifestE2EServerWithJobs(t *testing.T) (*Server, *db.Store, string) {
	t.Helper()
	srv, store, token, mgr, appsDir := buildManifestE2EServer(t, config.RuntimeConfig{})
	jm, err := jobs.NewManager(mgr, nil, process.DefaultTier, store, nil, appsDir, appsDir)
	if err != nil {
		t.Fatalf("jobs.NewManager: %v", err)
	}
	srv.SetJobs(jm, scheduler.New(jm, store, time.UTC))
	return srv, store, token
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

// waitForRegisterRunSucceeded blocks until the schedule has a succeeded run
// triggered by "register", or fails the test after a short deadline.
func waitForRegisterRunSucceeded(t *testing.T, store *db.Store, scheduleID int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := store.ListScheduleRuns(scheduleID, 50, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range runs {
			if r.Trigger == "register" && r.Status == "succeeded" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no succeeded 'register' run for schedule %d within deadline", scheduleID)
}

// TestDeploy_RunOnRegister_FiresOnceThenSelfGates verifies that a schedule with
// run_on_register = true fires exactly once on the first deploy and NOT again
// on subsequent deploys (once a succeeded run exists, the gate closes).
func TestDeploy_RunOnRegister_FiresOnceThenSelfGates(t *testing.T) {
	srv, store, token := newManifestE2EServerWithJobs(t)
	if err := store.CreateApp(db.CreateAppParams{Slug: "warmapp", Name: "warmapp", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("warmapp")

	manifest := `
[[schedule]]
name = "warm"
cron = "0 5 * * *"
cmd = "true"
run_on_register = true
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
	waitForRegisterRunSucceeded(t, store, schedID)

	runsAfterFirst, _ := store.ListScheduleRuns(schedID, 50, 0)
	registerCount := 0
	for _, r := range runsAfterFirst {
		if r.Trigger == "register" {
			registerCount++
		}
	}
	if registerCount != 1 {
		t.Fatalf("after first deploy: register runs = %d, want 1", registerCount)
	}

	body2, ct2 := buildMultiFileBundleUpload(t, map[string]string{
		"app.py":        "print('x')",
		"shinyhub.toml": manifest,
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
	registerCount = 0
	for _, r := range runsAfterSecond {
		if r.Trigger == "register" {
			registerCount++
		}
	}
	if registerCount != 1 {
		t.Errorf("after redeploy: register runs = %d, want 1 (must not re-fire)", registerCount)
	}
}

// TestDeploy_RunOnRegister_DisabledNotFired verifies that a schedule with both
// run_on_register = true and disabled = true is NOT first-fired on deploy.
func TestDeploy_RunOnRegister_DisabledNotFired(t *testing.T) {
	srv, store, token := newManifestE2EServerWithJobs(t)
	if err := store.CreateApp(db.CreateAppParams{Slug: "warmapp", Name: "warmapp", OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	app, _ := store.GetAppBySlug("warmapp")
	manifest := `
[[schedule]]
name = "warm"
cron = "0 5 * * *"
cmd = "true"
disabled = true
run_on_register = true
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
		t.Errorf("disabled schedule: runs = %d, want 0 (must not first-fire)", len(runs))
	}
}

// TestDeploy_ManifestUnknownAppFieldFails400 verifies that a shinyhub.toml
// containing an unknown [app] key (strict-mode TOML) is rejected with 400.
func TestDeploy_ManifestUnknownAppFieldFails400(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	admin, _ := store.GetUserByUsername("admin")

	if err := store.CreateApp(db.CreateAppParams{
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

	if err := store.CreateApp(db.CreateAppParams{
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
