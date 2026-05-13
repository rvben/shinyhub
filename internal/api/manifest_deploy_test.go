package api

import (
	"archive/zip"
	"bytes"
	"context"
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
	"github.com/rvben/shinyhub/internal/deploy"
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

func (f *manifestFakeRuntime) Start(_ context.Context, _ process.StartParams, _ io.Writer) (process.RunHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pid := f.nextPID
	f.nextPID++
	f.stops[pid] = make(chan struct{})
	return process.RunHandle{PID: pid}, nil
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
func (f *manifestFakeRuntime) HostPreparesDeps() bool { return false }
func (f *manifestFakeRuntime) AppBindHost() string    { return "127.0.0.1" }

// newManifestE2EServer wires a Server with a fake runtime, no-op sync hooks,
// a no-op health check, and a started (wired) scheduler stub. Returns the
// server, store, and an admin JWT bearer token.
func newManifestE2EServer(t *testing.T) (*Server, *db.Store, string) {
	t.Helper()
	appsDir := t.TempDir()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	hash, _ := auth.HashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{
		Username: "admin", PasswordHash: hash, Role: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	admin, _ := store.GetUserByUsername("admin")
	token, _ := auth.IssueJWT(admin.ID, admin.Username, admin.Role, "test-secret")

	cfg := &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Storage: config.StorageConfig{AppsDir: appsDir, VersionRetention: 5},
	}

	rt := newManifestFakeRuntime()
	mgr := process.NewManager(appsDir, rt)
	prx := proxy.New()
	srv := New(cfg, store, mgr, prx)

	// Wire scheduler (not started — ErrNotStarted is treated as a soft warning).
	sc := scheduler.New(nil, store)
	srv.SetJobs(nil, sc)

	// Replace the deploy runner to inject a no-op health check so tests
	// complete instantly instead of waiting for the 120 s timeout. Sync hooks
	// are already bypassed because manifestFakeRuntime.HostPreparesDeps()
	// returns false (container-mode semantics: no host-side dep installation).
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		p.HealthCheck = func(int, time.Duration) error { return nil }
		return deploy.Run(p)
	})

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
		"app.py":          "from shiny import App\n",
		"shinyhub.toml":  manifest,
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
		"app.py":         "from shiny import App\n",
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
