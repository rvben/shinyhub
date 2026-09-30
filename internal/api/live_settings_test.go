package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

func liveSettingsApp(t *testing.T, s *Server, slug string) struct {
	App     db.App `json:"app"`
	Pending bool   `json:"redeploy_in_flight"`
} {
	t.Helper()
	token, err := auth.IssueJWT(1, "bob", "admin", s.cfg.Auth.Secret)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/apps/"+slug, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	var result struct {
		App     db.App `json:"app"`
		Pending bool   `json:"redeploy_in_flight"`
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPatchWorkerLimitPreservesMultiplexSession(t *testing.T) {
	const slug = "hot-worker-limit"
	s, _ := newScaleTestServer(t, slug, 1, &config.Config{Auth: config.AuthConfig{Secret: "test-secret"}})
	s.proxy.SetPoolSize(slug, 1)
	_, check := resizeSession(t, s, slug, 0)
	old, err := s.manager.Start(process.StartParams{Slug: slug, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.manager.Stop(slug) })
	patchResizeSettings(t, s, slug, map[string]any{"worker_max_workers": 4})
	current := liveSettingsApp(t, s, slug)
	if current.Pending {
		t.Fatal("inactive worker limit restarted multiplex sessions")
	}
	if current.App.WorkerMaxWorkers != 4 {
		t.Fatal("worker setting was not saved")
	}
	check()
	if err := syscall.Kill(old.PID, 0); err != nil {
		t.Fatalf("existing process stopped: %v", err)
	}
}

func TestPatchGroupedAdmissionPreservesAssignedSession(t *testing.T) {
	const slug = "hot-group-size"
	s, _ := newScaleTestServer(t, slug, 1, &config.Config{Auth: config.AuthConfig{Secret: "test-secret"}})
	if _, err := s.store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug: slug, SetWorkerIsolation: true, WorkerIsolation: "grouped",
		SetWorkerGroupedSize: true, WorkerGroupedSize: 2,
		SetWorkerMaxWorkers: true, WorkerMaxWorkers: 3,
	}); err != nil {
		t.Fatal(err)
	}
	s.proxy.SetPoolMode(slug, config.IsolationGrouped, 2, 3)
	old, err := s.manager.Start(process.StartParams{Slug: slug, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.manager.Stop(slug) })
	_, check := resizeSession(t, s, slug, 0, true)
	for _, settings := range []map[string]any{
		{"worker_grouped_size": 3},
		{"worker_grouped_size": 1, "worker_max_workers": 1},
	} {
		patchResizeSettings(t, s, slug, settings)
		if current := liveSettingsApp(t, s, slug); current.Pending || current.App.Status != "running" {
			t.Fatal("admission edit restarted assigned grouped sessions")
		}
		check()
		if err := syscall.Kill(old.PID, 0); err != nil {
			t.Fatalf("assigned worker stopped: %v", err)
		}
	}
	pool, ok := s.proxy.ElasticWorkersSnapshot(slug)
	if !ok || pool.SessionsPerWorker != 1 || pool.MaxWorkers != 1 {
		t.Fatalf("admission settings did not apply live: %+v", pool)
	}
}

func TestPatchInheritedIsolationPreservesEffectiveMode(t *testing.T) {
	const slug = "hot-inherited-mode"
	s, _ := newScaleTestServer(t, slug, 1, &config.Config{Auth: config.AuthConfig{Secret: "test-secret"}})
	if _, err := s.store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug: slug, SetWorkerIsolation: true, WorkerIsolation: "",
	}); err != nil {
		t.Fatal(err)
	}
	s.proxy.SetPoolSize(slug, 1)
	_, check := resizeSession(t, s, slug, 0)
	old, err := s.manager.Start(process.StartParams{Slug: slug, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.manager.Stop(slug) })
	patchResizeSettings(t, s, slug, map[string]any{"worker_isolation": "multiplex"})
	current := liveSettingsApp(t, s, slug)
	if current.Pending || current.App.Status != "running" {
		t.Fatal("unchanged effective mode restarted the pool")
	}
	if current.App.WorkerIsolation != "multiplex" {
		t.Fatal("explicit override was not saved")
	}
	check()
	if err := syscall.Kill(old.PID, 0); err != nil {
		t.Fatalf("existing process stopped: %v", err)
	}
}

func TestPatchLifetimePreservesGroupedSession(t *testing.T) {
	const slug = "hot-worker-lifetime"
	s, _ := newScaleTestServer(t, slug, 1, &config.Config{Auth: config.AuthConfig{Secret: "test-secret"}})
	if _, err := s.store.PatchAppSettings(db.PatchAppSettingsParams{
		Slug: slug, SetWorkerIsolation: true, WorkerIsolation: "grouped",
		SetWorkerGroupedSize: true, WorkerGroupedSize: 2,
		SetWorkerMaxWorkers: true, WorkerMaxWorkers: 3,
	}); err != nil {
		t.Fatal(err)
	}
	s.proxy.SetPoolMode(slug, config.IsolationGrouped, 2, 3)
	old, err := s.manager.Start(process.StartParams{Slug: slug, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.manager.Stop(slug) })
	_, check := resizeSession(t, s, slug, 0, true)
	patchResizeSettings(t, s, slug, map[string]any{"worker_max_session_lifetime_secs": 120})
	current := liveSettingsApp(t, s, slug)
	if current.Pending || current.App.Status != "running" {
		t.Fatal("lifetime setting restarted assigned workers")
	}
	if current.App.WorkerMaxSessionLifetimeSecs != 120 {
		t.Fatal("lifetime was not saved")
	}
	check()
	if err := syscall.Kill(old.PID, 0); err != nil {
		t.Fatalf("assigned worker stopped: %v", err)
	}
}

func TestPatchEquivalentInheritedResourcesPreservesSession(t *testing.T) {
	const slug = "hot-inherited-resources"
	cfg := &config.Config{Auth: config.AuthConfig{Secret: "test-secret"}, Runtime: config.RuntimeConfig{Docker: config.DockerRuntimeConfig{DefaultMemoryMB: 256, DefaultCPUPercent: 100}}}
	s, _ := newScaleTestServer(t, slug, 1, cfg)
	s.proxy.SetPoolSize(slug, 1)
	_, check := resizeSession(t, s, slug, 0)
	old, err := s.manager.Start(process.StartParams{Slug: slug, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.manager.Stop(slug) })
	patchResizeSettings(t, s, slug, map[string]any{"memory_limit_mb": 256, "cpu_quota_percent": 100})
	current := liveSettingsApp(t, s, slug)
	if current.Pending || current.App.Status != "running" {
		t.Fatal("equivalent inherited resources restarted the pool")
	}
	check()
	if err := syscall.Kill(old.PID, 0); err != nil {
		t.Fatalf("existing process stopped: %v", err)
	}
}

func TestPatchCPUChangePreservesNativeSession(t *testing.T) {
	const slug = "hot-native-cpu"
	s, _ := newScaleTestServer(t, slug, 1, &config.Config{Auth: config.AuthConfig{Secret: "test-secret"}})
	s.proxy.SetPoolSize(slug, 1)
	_, check := resizeSession(t, s, slug, 0)
	old, err := s.manager.Start(process.StartParams{Slug: slug, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.manager.Stop(slug) })
	patchResizeSettings(t, s, slug, map[string]any{"cpu_quota_percent": 150})
	if current := liveSettingsApp(t, s, slug); current.Pending || current.App.Status != "running" {
		t.Fatal("CPU change restarted the native pool")
	}
	check()
	if err := syscall.Kill(old.PID, 0); err != nil {
		t.Fatalf("existing process stopped: %v", err)
	}
}

func TestPatchIsolationChangeDrainsGroupedSessionBeforeReplacement(t *testing.T) {
	s, app := livePlacementServer(t)
	if _, err := s.store.PatchAppSettings(db.PatchAppSettingsParams{Slug: app.Slug, SetWorkerIsolation: true, WorkerIsolation: "grouped", SetWorkerGroupedSize: true, WorkerGroupedSize: 2, SetWorkerMaxWorkers: true, WorkerMaxWorkers: 3}); err != nil {
		t.Fatal(err)
	}
	s.proxy.SetPoolMode(app.Slug, config.IsolationGrouped, 2, 3)
	old, _ := s.manager.GetReplica(app.Slug, 0)
	conn, check := resizeSession(t, s, app.Slug, 0, true)
	patchResizeSettings(t, s, app.Slug, map[string]any{"worker_isolation": "multiplex"})
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, ok := s.proxy.ElasticWorkersSnapshot(app.Slug)
		if ok && len(snapshot.Workers) == 1 && snapshot.Workers[0].Status == "draining" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("isolation change discarded assigned workers before draining")
		}
		time.Sleep(5 * time.Millisecond)
	}
	check()
	if current, ok := s.manager.GetReplica(app.Slug, 0); !ok || current.PID != old.PID {
		t.Fatal("isolation changed the process before the session drained")
	}
	_ = conn.Close()
	waitResize(t, func() bool { return !liveSettingsApp(t, s, app.Slug).Pending })
	current := liveSettingsApp(t, s, app.Slug)
	if current.App.EffectiveWorkerIsolation != "multiplex" || current.App.DesiredStatus != "running" {
		t.Fatalf("isolation did not converge: %+v", current)
	}
}
