package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/process"
)

func patchResizeSettings(t *testing.T, s *Server, slug string, settings map[string]any) {
	t.Helper()
	body, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.IssueJWT(1, "bob", "admin", s.cfg.Auth.Secret)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/apps/"+slug, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", rec.Code, rec.Body.String())
	}
}

func waitResize(t *testing.T, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for resize")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Exercise a real upgraded connection through the production reverse proxy.
// The backend echoes tunnel bytes so each check proves the same connection
// remains usable, rather than merely checking the stored replica count.
func resizeSession(t *testing.T, s *Server, slug string, index int) (net.Conn, func()) {
	t.Helper()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = rw.Flush()
		_, _ = io.Copy(conn, rw)
	}))
	t.Cleanup(backend.Close)
	if err := s.proxy.RegisterReplica(slug, index, backend.URL, nil, 0); err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(s.proxy)
	t.Cleanup(front.Close)
	conn, err := net.Dial("tcp", front.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = fmt.Fprintf(conn, "GET /app/%s/ws HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nCookie: shinyhub_rep_%s=%d\r\n\r\n", slug, slug, index)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil || resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade: response=%v, error=%v", resp, err)
	}
	check := func() {
		t.Helper()
		_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
		// An unmasked server-style text frame is sufficient for the transparent
		// tunnel fixture; no application protocol is implemented here.
		frame := []byte{0x81, 2, 'o', 'k'}
		if _, err := conn.Write(frame); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(frame))
		if _, err := io.ReadFull(reader, got); err != nil || !bytes.Equal(got, frame) {
			t.Fatalf("existing session lost: got=%v, error=%v", got, err)
		}
	}
	check()
	return conn, check
}

func TestPatchReplicaGrowthAndCapPreserveLiveSession(t *testing.T) {
	const slug = "live-grow"
	s, app := newScaleTestServer(t, slug, 1, &config.Config{
		Auth:    config.AuthConfig{Secret: "test-secret"},
		Runtime: config.RuntimeConfig{MaxReplicas: 8},
	})
	s.proxy.SetPoolSize(slug, 1)
	_, check := resizeSession(t, s, slug, 0)
	old, err := s.manager.Start(process.StartParams{
		Slug: slug, Index: 0, Dir: t.TempDir(), Command: []string{"sleep", "30"}, Port: 19300,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.manager.Stop(slug) })
	booted := make(chan int, 4)
	s.deployReplica = func(p deploy.Params, index int) (*deploy.Result, error) {
		stored, err := s.store.GetAppBySlug(slug)
		if err != nil || stored.Replicas != 3 {
			return nil, fmt.Errorf("requested size overwritten: app=%v err=%v", stored, err)
		}
		booted <- index
		return &deploy.Result{Index: index, PID: 4242 + index, Port: 19300 + index}, nil
	}
	patchResizeSettings(t, s, slug, map[string]any{"replicas": 3, "max_sessions_per_replica": 20})
	waitResize(t, func() bool { return !s.isRedeployInFlight(slug) })
	for _, want := range []int{1, 2} {
		select {
		case got := <-booted:
			if got != want {
				t.Fatalf("booted index %d, want %d", got, want)
			}
		default:
			t.Fatalf("missing additional replica %d", want)
		}
	}
	if err := syscall.Kill(old.PID, 0); err != nil {
		t.Fatalf("existing process stopped: %v", err)
	}
	if got := s.proxy.ReplicaSessionCounts(slug); len(got) != 3 || got[0] != 1 {
		t.Fatalf("session/pool lost: %v", got)
	}
	check()
	patchResizeSettings(t, s, slug, map[string]any{"max_sessions_per_replica": 1})
	check()
	if s.isRedeployInFlight(slug) {
		t.Fatal("cap-only edit restarted the pool")
	}
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil || len(rows) != 3 || rows[0].PID == nil || *rows[0].PID != 1000 {
		t.Fatalf("existing replica row replaced: %v %v", rows, err)
	}
}

func TestPatchReplicaShrinkDrainsOnlyRemovedSessions(t *testing.T) {
	const slug = "live-shrink"
	s, app := newScaleTestServer(t, slug, 3, &config.Config{
		Auth:   config.AuthConfig{Secret: "test-secret"},
		Server: config.ServerConfig{DrainTimeout: 2 * time.Second},
	})
	s.proxy.SetPoolSize(slug, 3)
	_, checkSurvivor := resizeSession(t, s, slug, 0)
	victim, checkVictim := resizeSession(t, s, slug, 2)
	patchResizeSettings(t, s, slug, map[string]any{"replicas": 1})
	waitResize(t, func() bool { return s.proxy.IsDraining(slug, 2) })
	waitResize(t, func() bool {
		rows, err := s.store.ListReplicas(app.ID)
		return err == nil && len(rows) == 3 && rows[2].DesiredState == "draining"
	})
	checkSurvivor()
	checkVictim()
	if got := s.proxy.ReplicaSessionCounts(slug); len(got) != 3 || got[2] != 1 {
		t.Fatalf("removed session was not allowed to drain: %v", got)
	}
	_ = victim.Close()
	waitResize(t, func() bool { return !s.isRedeployInFlight(slug) })
	checkSurvivor()
	if got := s.proxy.ReplicaSessionCounts(slug); len(got) != 1 || got[0] != 1 {
		t.Fatalf("surviving session/pool lost: %v", got)
	}
}

func TestPatchReplicaFailedGrowthKeepsCapacityAndRetries(t *testing.T) {
	const slug = "grow-retry"
	s, app := newScaleTestServer(t, slug, 1, &config.Config{
		Auth: config.AuthConfig{Secret: "test-secret"},
	})
	s.proxy.SetPoolSize(slug, 1)
	_, check := resizeSession(t, s, slug, 0)
	s.deployReplica = func(_ deploy.Params, index int) (*deploy.Result, error) {
		if index == 2 {
			return nil, fmt.Errorf("startup failed")
		}
		return &deploy.Result{Index: index, PID: 4242 + index, Port: 19400 + index}, nil
	}
	patchResizeSettings(t, s, slug, map[string]any{"replicas": 3})
	waitResize(t, func() bool { return !s.isRedeployInFlight(slug) })
	stored, _ := s.store.GetAppBySlug(slug)
	if stored.Replicas != 3 || stored.Status != "degraded" || stored.LastError == "" {
		t.Fatalf("failure not reported with requested size intact: %+v", stored)
	}
	rows, _ := s.store.ListReplicas(app.ID)
	if len(rows) != 2 || len(s.proxy.ReplicaSessionCounts(slug)) != 2 {
		t.Fatal("partial growth did not retain existing capacity")
	}
	check()
	s.deployReplica = func(_ deploy.Params, index int) (*deploy.Result, error) {
		return &deploy.Result{Index: index, PID: 4242 + index, Port: 19400 + index}, nil
	}
	patchResizeSettings(t, s, slug, map[string]any{"replicas": 3})
	waitResize(t, func() bool { return !s.isRedeployInFlight(slug) })
	stored, _ = s.store.GetAppBySlug(slug)
	if stored.Status != "running" || stored.LastError != "" || len(s.proxy.ReplicaSessionCounts(slug)) != 3 {
		t.Fatalf("retry failed to converge: %+v", stored)
	}
	check()
}

func TestResizeQueuedAfterStopDoesNotBoot(t *testing.T) {
	s, _ := newScaleTestServer(t, "stopped-resize", 1, &config.Config{})
	if err := s.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "stopped-resize", Status: "stopped"}); err != nil {
		t.Fatal(err)
	}
	s.deployReplica = func(deploy.Params, int) (*deploy.Result, error) {
		t.Error("queued resize resurrected a stopped app")
		return nil, fmt.Errorf("unexpected boot")
	}
	s.markRedeployInFlight("stopped-resize")
	s.resizeApp("stopped-resize")
	if s.isRedeployInFlight("stopped-resize") {
		t.Fatal("queued resize left in-flight marker set")
	}
}

func TestAutoscaleDefersUnconvergedManualResize(t *testing.T) {
	s, app := newScaleTestServer(t, "queued-resize", 3, &config.Config{})
	assertDeferred := func() {
		t.Helper()
		if changed, err := s.ScaleUp(app.Slug); err != nil || changed {
			t.Fatalf("autoscale grew an unconverged pool: changed=%v err=%v", changed, err)
		}
		if changed, err := s.ScaleDown(app.Slug, 0); err != nil || changed {
			t.Fatalf("autoscale shrank an unconverged pool: changed=%v err=%v", changed, err)
		}
		stored, _ := s.store.GetAppBySlug(app.Slug)
		if stored.Replicas != 3 {
			t.Fatal("autoscale overwrote the manual target")
		}
	}
	s.markRedeployInFlight(app.Slug)
	assertDeferred()
	s.clearRedeployInFlight(app.Slug)
	// A failed resize can leave fewer rows than the requested size. Do not
	// append beyond the requested size and leave a hole at the failed index.
	if err := s.store.DeleteReplica(app.ID, 2); err != nil {
		t.Fatal(err)
	}
	assertDeferred()
}

func TestResizeRestoresWarmSlotsBeforeAdding(t *testing.T) {
	s, app, rt := newWarmExpandServer(t, "warm-resize", 3, []int{1, 2}, nil)
	s.proxy.SetPoolSize(app.Slug, 3)
	if err := s.store.UpdateAppReplicas(app.ID, 4); err != nil {
		t.Fatal(err)
	}
	s.markRedeployInFlight(app.Slug)
	s.resizeApp(app.Slug)
	if booted := rt.boosted(); len(booted) != 3 {
		t.Fatalf("expected warm slots 1, 2 and new slot 3 to start, got %v", booted)
	}
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil || len(rows) != 4 {
		t.Fatalf("pool did not converge: rows=%v err=%v", rows, err)
	}
	for _, row := range rows {
		if row.Status != "running" || row.DesiredState != "running" {
			t.Fatalf("slot not restored: %+v", row)
		}
	}
}

func TestPatchCombinedReplicaAndResourceChangePrunesStoppedSlots(t *testing.T) {
	s, app := newScaleTestServer(t, "combined-resize", 3, &config.Config{
		Auth: config.AuthConfig{Secret: "test-secret"},
	})
	s.deployRun = func(deploy.Params) (*deploy.PoolResult, error) {
		rows, err := s.store.ListReplicas(app.ID)
		if err != nil || len(rows) != 1 {
			return nil, fmt.Errorf("stopped trailing slots not pruned: rows=%v err=%v", rows, err)
		}
		return &deploy.PoolResult{Replicas: []deploy.Result{{Index: 0, PID: 4242, Port: 19500}}}, nil
	}
	patchResizeSettings(t, s, app.Slug, map[string]any{"replicas": 1, "cpu_quota_percent": 150})
	waitResize(t, func() bool { return !s.isRedeployInFlight(app.Slug) })
	rows, err := s.store.ListReplicas(app.ID)
	if err != nil || len(rows) != 1 || rows[0].PID == nil || *rows[0].PID != 4242 {
		t.Fatalf("combined edit did not apply the structural restart: rows=%v err=%v", rows, err)
	}
}
