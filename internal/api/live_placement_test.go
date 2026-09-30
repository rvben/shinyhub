package api

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"io"
)

func livePlacementServer(t *testing.T) (*Server, *db.App) {
	t.Helper()
	s, store, token, mgr, _, rt := buildManifestE2EServer(t, config.RuntimeConfig{
		MaxReplicas: 8,
		Tiers:       []config.TierConfig{{Name: "local", Runtime: "native"}, {Name: "burst", Runtime: "native"}},
	})
	rt.serveTraffic = true
	mgr.RegisterRuntime("local", rt)
	mgr.RegisterRuntime("burst", rt)
	mgr.SetDefaultTier("local")
	s.cfg.Server.DrainTimeout = time.Second
	t.Cleanup(func() { s.Close(); _ = mgr.Stop("placement-live") })
	app := createGenerationTestApp(t, store, "placement-live", 1, 16)
	if rec := deployBareGeneration(t, s, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("initial deploy: %d %s", rec.Code, rec.Body.String())
	}
	return s, app
}

func TestPatchPlacementGrowthPreservesExistingReplica(t *testing.T) {
	s, app := livePlacementServer(t)
	old, ok := s.manager.GetReplica(app.Slug, 0)
	if !ok {
		t.Fatal("initial replica missing")
	}
	_, check := resizeSession(t, s, app.Slug, 0)
	patchResizeSettings(t, s, app.Slug, map[string]any{"placement": map[string]int{"local": 1, "burst": 1}})
	waitResize(t, func() bool { return !liveSettingsApp(t, s, app.Slug).Pending })
	current := liveSettingsApp(t, s, app.Slug)
	if current.App.Status != "running" || current.App.Replicas != 2 {
		t.Fatalf("placement did not converge: %+v", current.App)
	}
	got, ok := s.manager.GetReplica(app.Slug, 0)
	if !ok || got.PID != old.PID {
		t.Fatalf("unchanged replica restarted: before=%+v after=%+v", old, got)
	}
	added, ok := s.manager.GetReplica(app.Slug, 1)
	if !ok || added.Tier != "burst" {
		t.Fatalf("new replica has wrong placement: %+v", added)
	}
	check()
	events, err := s.store.ListAuditEvents("update_app", 10, 0)
	if err != nil || len(events) != 1 {
		t.Fatalf("placement change must audit once: %+v %v", events, err)
	}
}

type placementBootRuntime struct {
	process.Runtime
	fail bool
}

func (r *placementBootRuntime) Start(ctx context.Context, p process.StartParams, logs io.Writer) (process.ReplicaEndpoint, error) {
	if r.fail {
		return process.ReplicaEndpoint{}, errors.New("placement boot failed")
	}
	return r.Runtime.Start(ctx, p, logs)
}

func TestPatchPlacementMovesOnlyAffectedSlotAndRetriesFailedMove(t *testing.T) {
	s, app := livePlacementServer(t)
	patchResizeSettings(t, s, app.Slug, map[string]any{"placement": map[string]int{"local": 2, "burst": 1}})
	waitResize(t, func() bool { return !liveSettingsApp(t, s, app.Slug).Pending })
	old0, _ := s.manager.GetReplica(app.Slug, 0)
	old2, _ := s.manager.GetReplica(app.Slug, 2)
	_, check0 := resizeSession(t, s, app.Slug, 0)
	_, check2 := resizeSession(t, s, app.Slug, 2)
	failing := &placementBootRuntime{Runtime: s.manager.RuntimeForTier("burst"), fail: true}
	s.manager.RegisterRuntime("burst", failing)
	patch := map[string]any{"placement": map[string]int{"local": 1, "burst": 2}}
	patchResizeSettings(t, s, app.Slug, patch)
	waitResize(t, func() bool { return !liveSettingsApp(t, s, app.Slug).Pending })
	if state := liveSettingsApp(t, s, app.Slug); state.App.DesiredStatus != "degraded" {
		t.Fatalf("failed move not reported: %+v", state)
	}
	check0()
	check2()
	if changed, err := s.ScaleUp(app.Slug); err != nil || changed {
		t.Fatalf("autoscale interfered with failed placement: %v %v", changed, err)
	}
	failing.fail = false
	patchResizeSettings(t, s, app.Slug, patch)
	waitResize(t, func() bool { return !liveSettingsApp(t, s, app.Slug).Pending })
	for index, old := range map[int]*process.ProcessInfo{0: old0, 2: old2} {
		if current, ok := s.manager.GetReplica(app.Slug, index); !ok || current.PID != old.PID {
			t.Fatalf("unaffected slot %d restarted", index)
		}
	}
	if moved, ok := s.manager.GetReplica(app.Slug, 1); !ok || moved.Tier != "burst" {
		t.Fatalf("failed move did not recover: %+v", moved)
	}
	check0()
	check2()
	if state := liveSettingsApp(t, s, app.Slug); state.App.DesiredStatus != "running" || state.App.LastError != "" {
		t.Fatalf("placement retry did not clear failure: %+v", state)
	}
}
