package api

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
)

type liveResourceRuntime struct {
	process.Runtime
	calls     []process.ResourceLimits
	failFirst bool
	cancel    context.CancelFunc
}

func TestCombinedResourceFailureAndResizeRetriesWithoutLosingOutcome(t *testing.T) {
	s, app := livePlacementServer(t)
	updater := &liveResourceRuntime{Runtime: s.manager.RuntimeForTier("local"), failFirst: true}
	s.manager.RegisterRuntime("local", updater)
	old, _ := s.manager.GetReplica(app.Slug, 0)
	_, check := resizeSession(t, s, app.Slug, 0)
	patch := map[string]any{"cpu_quota_percent": 150, "replicas": 2}
	patchResizeSettings(t, s, app.Slug, patch)
	waitResize(t, func() bool { return !liveSettingsApp(t, s, app.Slug).Pending })
	failed := liveSettingsApp(t, s, app.Slug).App
	if failed.LastRedeploy == nil || failed.LastRedeploy.Outcome != db.RedeployFailed {
		t.Fatalf("failed live update reported success: %+v", failed.LastRedeploy)
	}
	check()
	patchResizeSettings(t, s, app.Slug, map[string]any{"cpu_quota_percent": 150})
	waitResize(t, func() bool { return !liveSettingsApp(t, s, app.Slug).Pending })
	current := liveSettingsApp(t, s, app.Slug).App
	if current.LastRedeploy == nil || current.LastRedeploy.Outcome != db.RedeployCompleted || current.LastError != "" {
		t.Fatalf("retry did not complete: %+v", current)
	}
	if replica, ok := s.manager.GetReplica(app.Slug, 0); !ok || replica.PID != old.PID {
		t.Fatal("retry replaced the original process")
	}
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil || len(rows) != 2 {
		t.Fatalf("retry lost requested resize: %+v %v", rows, err)
	}
	check()
}

func (r *liveResourceRuntime) UpdateResources(_ context.Context, _ process.RunHandle, limits process.ResourceLimits) error {
	r.calls = append(r.calls, limits)
	if r.cancel != nil {
		r.cancel()
	}
	if r.failFirst && len(r.calls) == 1 {
		return errors.New("temporary runtime failure")
	}
	return nil
}

func TestCanceledClientAfterLiveResourceUpdateDoesNotLaunchReplacement(t *testing.T) {
	s, app := livePlacementServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.manager.RegisterRuntime("local", &liveResourceRuntime{Runtime: s.manager.RuntimeForTier("local"), cancel: cancel})
	old, _ := s.manager.GetReplica(app.Slug, 0)
	_, check := resizeSession(t, s, app.Slug, 0)
	token, _ := auth.IssueJWT(1, "bob", "admin", s.cfg.Auth.Secret)
	req := httptest.NewRequest(http.MethodPatch, "/api/apps/"+app.Slug, bytes.NewBufferString(`{"cpu_quota_percent":150}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("live application lost on disconnect: %d %s", rec.Code, rec.Body.String())
	}
	current := liveSettingsApp(t, s, app.Slug)
	if current.Pending || current.App.LastRedeploy == nil || current.App.LastRedeploy.Outcome != db.RedeployCompleted {
		t.Fatalf("live application not recorded: %+v", current)
	}
	if replica, ok := s.manager.GetReplica(app.Slug, 0); !ok || replica.PID != old.PID {
		t.Fatal("disconnect replaced the live process")
	}
	check()
}

func TestPatchMemoryIncreaseUpdatesRuntimeWithoutReplacingSession(t *testing.T) {
	s, app := livePlacementServer(t)
	rt := s.manager.RuntimeForTier("local")
	updater := &liveResourceRuntime{Runtime: rt}
	s.manager.RegisterRuntime("local", updater)
	old, _ := s.manager.GetReplica(app.Slug, 0)
	_, check := resizeSession(t, s, app.Slug, 0)
	patchResizeSettings(t, s, app.Slug, map[string]any{"memory_limit_mb": 32})
	if current := liveSettingsApp(t, s, app.Slug); current.Pending || current.App.Status != "running" {
		t.Fatal("memory increase replaced the pool")
	}
	check()
	if current, ok := s.manager.GetReplica(app.Slug, 0); !ok || current.PID != old.PID {
		t.Fatal("memory increase replaced the process")
	}
	if len(updater.calls) != 1 || updater.calls[0].MemoryLimitMB == nil || *updater.calls[0].MemoryLimitMB != 32 || updater.calls[0].CPUQuotaPercent != nil {
		t.Fatalf("wrong live limits: %+v", updater.calls)
	}
}

func TestPatchResourceFailurePreservesSessionAndRetriesSavedTarget(t *testing.T) {
	s, app := livePlacementServer(t)
	rt := s.manager.RuntimeForTier("local")
	updater := &liveResourceRuntime{Runtime: rt, failFirst: true}
	s.manager.RegisterRuntime("local", updater)
	old, _ := s.manager.GetReplica(app.Slug, 0)
	_, check := resizeSession(t, s, app.Slug, 0)
	patch := map[string]any{"cpu_quota_percent": 150}
	patchResizeSettings(t, s, app.Slug, patch)
	failed := liveSettingsApp(t, s, app.Slug)
	if failed.Pending || failed.App.DesiredStatus != "degraded" || !strings.Contains(failed.App.LastError, "temporary runtime failure") {
		t.Fatalf("failure not visible: %+v", failed)
	}
	check()
	patchResizeSettings(t, s, app.Slug, patch)
	current := liveSettingsApp(t, s, app.Slug)
	if len(updater.calls) != 2 || current.Pending || current.App.Status != "running" || current.App.LastError != "" {
		t.Fatalf("saved target did not retry: calls=%d app=%+v", len(updater.calls), current)
	}
	if current, ok := s.manager.GetReplica(app.Slug, 0); !ok || current.PID != old.PID {
		t.Fatal("resource retry replaced the process")
	}
	check()
}

func TestResourceRetryAppliesWholeSavedTargetWhenAnotherFieldChanges(t *testing.T) {
	s, app := livePlacementServer(t)
	updater := &liveResourceRuntime{Runtime: s.manager.RuntimeForTier("local"), failFirst: true}
	s.manager.RegisterRuntime("local", updater)
	_, check := resizeSession(t, s, app.Slug, 0)
	patchResizeSettings(t, s, app.Slug, map[string]any{"memory_limit_mb": 32})
	patchResizeSettings(t, s, app.Slug, map[string]any{"cpu_quota_percent": 150})
	if len(updater.calls) != 2 || updater.calls[1].MemoryLimitMB == nil || *updater.calls[1].MemoryLimitMB != 32 || updater.calls[1].CPUQuotaPercent == nil || *updater.calls[1].CPUQuotaPercent != 150 {
		t.Fatalf("failed sibling target forgotten: %+v", updater.calls)
	}
	check()
}
