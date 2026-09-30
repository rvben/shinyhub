package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

func TestExplicitEnvironmentApplyDrainsLiveSession(t *testing.T) {
	s, app := livePlacementServer(t)
	old, _ := s.manager.GetReplica(app.Slug, 0)
	conn, check := resizeSession(t, s, app.Slug, 0)
	token, _ := auth.IssueJWT(1, "bob", "admin", s.cfg.Auth.Secret)
	req := httptest.NewRequest(http.MethodPut, "/api/apps/"+app.Slug+"/env/LIVE_EDIT?restart=true", bytes.NewBufferString(`{"value":"new","secret":false}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { rec := httptest.NewRecorder(); s.Router().ServeHTTP(rec, req); done <- rec }()
	deadline := time.Now().Add(2 * time.Second)
	for !s.proxy.IsDraining(app.Slug, 0) {
		select {
		case rec := <-done:
			t.Fatalf("apply stopped sessions without draining: %d %s", rec.Code, rec.Body.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("apply did not start draining")
		}
		time.Sleep(5 * time.Millisecond)
	}
	check()
	if current, ok := s.manager.GetReplica(app.Slug, 0); !ok || current.PID != old.PID {
		t.Fatal("existing process replaced during drain")
	}
	_ = conn.Close()
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("apply failed: %d %s", rec.Code, rec.Body.String())
		}
		if current, ok := s.manager.GetReplica(app.Slug, 0); !ok || current.PID == old.PID {
			t.Fatal("drained process was not replaced")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("apply did not complete after the session ended")
	}
}

func TestApplyPreviouslySavedEnvironmentDrainsLiveSession(t *testing.T) {
	s, app := livePlacementServer(t)
	conn, check := resizeSession(t, s, app.Slug, 0)
	token, _ := auth.IssueJWT(1, "bob", "admin", s.cfg.Auth.Secret)
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+app.Slug+"/env/apply", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { rec := httptest.NewRecorder(); s.Router().ServeHTTP(rec, req); done <- rec }()
	deadline := time.Now().Add(2 * time.Second)
	for !s.proxy.IsDraining(app.Slug, 0) {
		select {
		case rec := <-done:
			t.Fatalf("saved apply did not drain: %d %s", rec.Code, rec.Body.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("saved apply did not start draining")
		}
		time.Sleep(5 * time.Millisecond)
	}
	check()
	_ = conn.Close()
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("apply failed: %d %s", rec.Code, rec.Body.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("saved apply did not finish")
	}
}

func TestFailedEnvironmentApplyRestoresSurvivorAndParksStoppedSlots(t *testing.T) {
	s, app := livePlacementServer(t)
	patchResizeSettings(t, s, app.Slug, map[string]any{"replicas": 2})
	waitResize(t, func() bool { return !liveSettingsApp(t, s, app.Slug).Pending })
	old, _ := s.manager.GetReplica(app.Slug, 0)
	rt := s.manager.RuntimeForTier("local").(*manifestFakeRuntime)
	rt.mu.Lock()
	rt.signalFailures[old.PID] = errors.New("stop refused")
	rt.mu.Unlock()
	t.Cleanup(func() { rt.mu.Lock(); delete(rt.signalFailures, old.PID); rt.mu.Unlock() })
	_, check := resizeSession(t, s, app.Slug, 0)
	s.cfg.Server.DrainTimeout = 50 * time.Millisecond
	token, _ := auth.IssueJWT(1, "bob", "admin", s.cfg.Auth.Secret)
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+app.Slug+"/env/apply", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("stop failure was hidden: %d %s", rec.Code, rec.Body.String())
	}
	check()
	if s.proxy.IsDraining(app.Slug, 0) {
		t.Fatal("surviving session left draining")
	}
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Index == 0 && row.DesiredState != "running" {
			t.Fatal("survivor did not regain admission")
		}
		if row.Index == 1 && (row.Status != "stopped" || row.DesiredState != "stopped" || row.EndpointURL != "") {
			t.Fatalf("confirmed stopped slot still advertised live: %+v", row)
		}
	}
	if s.proxy.ReplicaTargetURL(app.Slug, 1) != "" {
		t.Fatal("confirmed stopped slot remains routable")
	}
}

func TestFailedGroupedEnvironmentApplyRemovesStoppedWorkers(t *testing.T) {
	s, app := livePlacementServer(t)
	patchResizeSettings(t, s, app.Slug, map[string]any{"replicas": 2})
	waitResize(t, func() bool { return !liveSettingsApp(t, s, app.Slug).Pending })
	if _, err := s.store.PatchAppSettings(db.PatchAppSettingsParams{Slug: app.Slug, SetWorkerIsolation: true, WorkerIsolation: "grouped", SetWorkerGroupedSize: true, WorkerGroupedSize: 2, SetWorkerMaxWorkers: true, WorkerMaxWorkers: 3}); err != nil {
		t.Fatal(err)
	}
	s.proxy.SetPoolMode(app.Slug, config.IsolationGrouped, 2, 3)
	old, _ := s.manager.GetReplica(app.Slug, 0)
	other, _ := s.manager.GetReplica(app.Slug, 1)
	rt := s.manager.RuntimeForTier("local").(*manifestFakeRuntime)
	rt.mu.Lock()
	rt.signalFailures[old.PID] = errors.New("stop refused")
	rt.mu.Unlock()
	t.Cleanup(func() { rt.mu.Lock(); delete(rt.signalFailures, old.PID); rt.mu.Unlock() })
	_, check := resizeSession(t, s, app.Slug, 0, true)
	if err := s.proxy.RegisterElasticWorker(app.Slug, 1, other.EndpointURL, nil, other.DeploymentID); err != nil {
		t.Fatal(err)
	}
	s.cfg.Server.DrainTimeout = 50 * time.Millisecond
	token, _ := auth.IssueJWT(1, "bob", "admin", s.cfg.Auth.Secret)
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+app.Slug+"/env/apply", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("stop failure was hidden: %d", rec.Code)
	}
	check()
	pool, ok := s.proxy.ElasticWorkersSnapshot(app.Slug)
	if !ok || len(pool.Workers) != 1 || pool.Workers[0].SlotID != 0 || pool.Workers[0].Status != "running" {
		t.Fatalf("stopped worker remains routable: %+v", pool)
	}
}
