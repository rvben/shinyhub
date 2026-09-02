package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

func deployBareGeneration(t *testing.T, srv *Server, token, slug, source string, allowDowntime bool) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := buildMultiFileBundleUpload(t, map[string]string{"app.py": source})
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+slug+"/deploy", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+token)
	if allowDowntime {
		req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
	}
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

func createGenerationTestApp(t *testing.T, store *db.Store, slug string, replicas, memoryMB int) *db.App {
	t.Helper()
	if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: 1, Access: "private"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().Exec(`UPDATE apps SET replicas=?, memory_limit_mb=? WHERE slug=?`, replicas, memoryMB, slug); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func latestGenerationDeploymentStatus(t *testing.T, store *db.Store, appID int64) string {
	t.Helper()
	var status string
	if err := store.DB().QueryRow(`SELECT status FROM deployments WHERE app_id=? ORDER BY id DESC LIMIT 1`, appID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func proxyGenerationBody(srv *Server, slug, query string) (int, string) {
	req := httptest.NewRequest(http.MethodGet, "/app/"+slug+"/"+query, nil)
	rec := httptest.NewRecorder()
	srv.proxy.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestDeploy_GenerationHandoffPublishesHealthyCandidate(t *testing.T) {
	srv, store, token, mgr, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	zeroFloor := 0
	srv.cfg.Server.MinAvailableMemoryMB = &zeroFloor
	app := createGenerationTestApp(t, store, "handoff-ok", 1, 16)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	old, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false); rec.Code != http.StatusOK {
		t.Fatalf("handoff deploy: %d %s", rec.Code, rec.Body.String())
	}
	active, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil || active.DeploymentID == old.DeploymentID {
		t.Fatalf("active generation = %+v, err=%v; want new deployment", active, err)
	}
	if token, ok := srv.proxy.ActiveGenerationActivationToken(app.Slug); !ok || token != active.ActivationToken {
		t.Fatalf("proxy active token = %q, ok=%v; want %q", token, ok, active.ActivationToken)
	}
	if info, ok := mgr.GetReplica(app.Slug, 0); !ok || info.DeploymentID != active.DeploymentID {
		t.Fatalf("manager active replica = %+v, ok=%v; want deployment %d", info, ok, active.DeploymentID)
	}
}

func TestDeploy_GenerationHandoffServesOldTrafficUntilCandidateReadyAndDrainsOpenHTTP(t *testing.T) {
	srv, store, token, _, _, runtime := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	runtime.serveTraffic = true
	runtime.holdStarted = make(chan struct{}, 1)
	runtime.holdRelease = make(chan struct{})
	app := createGenerationTestApp(t, store, "handoff-traffic", 1, 16)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	old, _ := store.GetActiveDeploymentGeneration(app.ID)
	oldDeployment, _ := store.GetDeploymentByID(old.DeploymentID)

	heldResult := make(chan string, 1)
	go func() {
		status, body := proxyGenerationBody(srv, app.Slug, "?hold=1")
		heldResult <- fmt.Sprintf("%d:%s", status, body)
	}()
	select {
	case <-runtime.holdStarted:
	case <-time.After(time.Second):
		t.Fatal("old-version request did not reach its backend")
	}

	candidateChecking := make(chan struct{})
	releaseCandidate := make(chan struct{})
	var readyOnce sync.Once
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		p.HealthCheck = func(string, time.Duration, http.RoundTripper) error {
			if p.GenerationScoped {
				readyOnce.Do(func() { close(candidateChecking) })
				<-releaseCandidate
			}
			return nil
		}
		return deploy.Run(p)
	})
	deployDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { deployDone <- deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false) }()
	select {
	case <-candidateChecking:
	case <-time.After(time.Second):
		t.Fatal("candidate did not reach readiness gate")
	}
	for i := 0; i < 10; i++ {
		if status, body := proxyGenerationBody(srv, app.Slug, ""); status != http.StatusOK || body != oldDeployment.Version {
			t.Fatalf("request during candidate readiness = %d %q, want old version %q", status, body, oldDeployment.Version)
		}
	}
	close(releaseCandidate)
	if rec := <-deployDone; rec.Code != http.StatusOK {
		t.Fatalf("handoff deploy: %d %s", rec.Code, rec.Body.String())
	}
	active, _ := store.GetActiveDeploymentGeneration(app.ID)
	activeDeployment, _ := store.GetDeploymentByID(active.DeploymentID)
	if status, body := proxyGenerationBody(srv, app.Slug, ""); status != http.StatusOK || body != activeDeployment.Version {
		t.Fatalf("fresh request after cutover = %d %q, want candidate %q", status, body, activeDeployment.Version)
	}
	close(runtime.holdRelease)
	select {
	case got := <-heldResult:
		if got != fmt.Sprintf("%d:%s", http.StatusOK, oldDeployment.Version) {
			t.Fatalf("open old request crossed generations: got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("held old-version request did not complete")
	}
}

func TestDeploy_CandidateDiesBeforeCutoverKeepsHealthyRoute(t *testing.T) {
	srv, store, token, mgr, _, runtime := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	runtime.serveTraffic = true
	app := createGenerationTestApp(t, store, "handoff-dies", 1, 16)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	old, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldDeployment, _ := store.GetDeploymentByID(old.DeploymentID)
	oldInfo, ok := mgr.GetReplica(app.Slug, 0)
	if !ok {
		t.Fatal("old manager pool missing")
	}
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		p.HealthCheck = func(string, time.Duration, http.RoundTripper) error { return nil }
		result, err := deploy.Run(p)
		if err == nil && p.GenerationScoped {
			if stopErr := mgr.StopGeneration(p.Slug, p.DeploymentID); stopErr != nil {
				t.Fatalf("stop staged candidate: %v", stopErr)
			}
		}
		return result, err
	})
	rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("candidate death status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	committed, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil || committed.DeploymentID != old.DeploymentID {
		t.Fatalf("durable authority = %+v, err=%v; want old deployment %d", committed, err, old.DeploymentID)
	}
	if localToken, ok := srv.proxy.ActiveGenerationActivationToken(app.Slug); !ok || localToken != old.ActivationToken {
		t.Fatalf("healthy proxy route changed: token=%q ok=%v, want old %q", localToken, ok, old.ActivationToken)
	}
	if info, ok := mgr.GetReplica(app.Slug, 0); !ok || info.DeploymentID != oldInfo.DeploymentID {
		t.Fatalf("healthy manager pool changed: info=%+v ok=%v", info, ok)
	}
	if status, body := proxyGenerationBody(srv, app.Slug, ""); status != http.StatusOK || body != oldDeployment.Version {
		t.Fatalf("traffic after candidate failure = %d %q, want old version %q", status, body, oldDeployment.Version)
	}
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil || len(rows) != 1 || rows[0].DeploymentID == old.DeploymentID {
		t.Fatalf("candidate cleanup identity = %+v, err=%v; want only unconfirmed candidate retained", rows, err)
	}
	if status := latestGenerationDeploymentStatus(t, store, app.ID); status != db.DeploymentFailed {
		t.Fatalf("newest deployment status = %q, want failed", status)
	}
}

func TestDeploy_ProxyPublicationFailureCompensatesToOldGeneration(t *testing.T) {
	srv, store, token, mgr, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	app := createGenerationTestApp(t, store, "handoff-proxy-fails", 1, 16)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	old, _ := store.GetActiveDeploymentGeneration(app.ID)
	oldInfo, _ := mgr.GetReplica(app.Slug, 0)
	srv.SetGenerationCutoverForTest(nil, func(string, int64) (int64, error) {
		return 0, errors.New("injected proxy publication failure")
	})
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false); rec.Code != http.StatusInternalServerError {
		t.Fatalf("proxy failure deploy = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	active, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil || active.DeploymentID != old.DeploymentID {
		t.Fatalf("durable authority = %+v, err=%v; want old deployment", active, err)
	}
	if info, ok := mgr.GetReplica(app.Slug, 0); !ok || info.DeploymentID != oldInfo.DeploymentID {
		t.Fatalf("manager selection = %+v, ok=%v; want old deployment", info, ok)
	}
	if proxyToken, ok := srv.proxy.ActiveGenerationActivationToken(app.Slug); !ok || proxyToken != old.ActivationToken {
		t.Fatalf("proxy token = %q, ok=%v; want old %q", proxyToken, ok, old.ActivationToken)
	}
	if status := latestGenerationDeploymentStatus(t, store, app.ID); status != db.DeploymentFailed {
		t.Fatalf("newest deployment status = %q, want failed compensation", status)
	}
}

func TestDeploy_PromotionAcknowledgementLossConfirmsDurableCandidateBeforeCleanup(t *testing.T) {
	srv, store, token, mgr, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	app := createGenerationTestApp(t, store, "handoff-commit-ack", 1, 16)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	old, _ := store.GetActiveDeploymentGeneration(app.ID)
	srv.SetGenerationCutoverForTest(func(id int64) error {
		if err := store.PromoteDeployment(id); err != nil {
			return err
		}
		return errors.New("injected lost commit acknowledgement")
	}, nil)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false); rec.Code != http.StatusOK {
		t.Fatalf("acknowledgement loss deploy = %d, want 200 after durable confirmation: %s", rec.Code, rec.Body.String())
	}
	active, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil || active.DeploymentID == old.DeploymentID {
		t.Fatalf("durable authority = %+v, err=%v; want candidate", active, err)
	}
	if info, ok := mgr.GetReplica(app.Slug, 0); !ok || info.DeploymentID != active.DeploymentID {
		t.Fatalf("manager candidate = %+v, ok=%v; want deployment %d", info, ok, active.DeploymentID)
	}
}

func TestDeploy_DegradedPoolWithoutSlotZeroStillRequiresSafeAdmission(t *testing.T) {
	srv, store, token, mgr, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	app := createGenerationTestApp(t, store, "handoff-degraded", 2, 0)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	target0 := srv.proxy.ReplicaTargetURL(app.Slug, 0)
	if err := mgr.StopReplicaConfirmed(app.Slug, 0); err != nil {
		t.Fatal(err)
	}
	srv.proxy.DeregisterReplicaIfTarget(app.Slug, 0, target0)
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: app.Slug, Status: "degraded"}); err != nil {
		t.Fatal(err)
	}
	before, _ := store.GetActiveDeploymentGeneration(app.ID)
	target1 := srv.proxy.ReplicaTargetURL(app.Slug, 1)
	rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false)
	if rec.Code != http.StatusConflict {
		t.Fatalf("degraded redeploy = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	after, _ := store.GetActiveDeploymentGeneration(app.ID)
	if after.DeploymentID != before.DeploymentID || srv.proxy.ReplicaTargetURL(app.Slug, 1) != target1 || !mgr.HasRunning(app.Slug) {
		t.Fatalf("working degraded generation changed: before=%+v after=%+v target1=%q", before, after, srv.proxy.ReplicaTargetURL(app.Slug, 1))
	}
}

func TestDeploy_NonNativeProviderUsesExplicitStopFirstFallback(t *testing.T) {
	srv, store, token, _, _, runtime := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	runtime.provider = "docker"
	app := createGenerationTestApp(t, store, "handoff-provider", 1, 16)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	before, _ := store.GetActiveDeploymentGeneration(app.ID)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false); rec.Code != http.StatusConflict {
		t.Fatalf("non-native default = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	unchanged, _ := store.GetActiveDeploymentGeneration(app.ID)
	if unchanged.DeploymentID != before.DeploymentID {
		t.Fatal("default non-native refusal changed the active pointer")
	}
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v3')", true); rec.Code != http.StatusOK {
		t.Fatalf("explicit stop-first fallback = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	after, _ := store.GetActiveDeploymentGeneration(app.ID)
	if after.DeploymentID == before.DeploymentID {
		t.Fatal("explicit stop-first fallback did not publish the new deployment")
	}
}

func TestDeploy_GenerationCapacityRefusalPreservesWorkingVersion(t *testing.T) {
	srv, store, token, mgr, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	zeroFloor := 0
	srv.cfg.Server.MinAvailableMemoryMB = &zeroFloor
	app := createGenerationTestApp(t, store, "handoff-capacity", 1, 16)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	old, _ := store.GetActiveDeploymentGeneration(app.ID)
	oldInfo, _ := mgr.GetReplica(app.Slug, 0)
	srv.SetAvailableMemoryForTest(func() (int, error) { return 15, nil })
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false); rec.Code != http.StatusConflict {
		t.Fatalf("below-threshold capacity = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	active, _ := store.GetActiveDeploymentGeneration(app.ID)
	info, ok := mgr.GetReplica(app.Slug, 0)
	if active.DeploymentID != old.DeploymentID || !ok || info.PID != oldInfo.PID {
		t.Fatalf("capacity refusal changed working generation: active=%+v info=%+v ok=%v", active, info, ok)
	}
	srv.SetAvailableMemoryForTest(func() (int, error) { return 16, nil })
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v3')", false); rec.Code != http.StatusOK {
		t.Fatalf("at-threshold capacity = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestDeploy_UnknownGenerationCapacityRequiresExplicitDowntime(t *testing.T) {
	srv, store, token, _, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	zeroFloor := 0
	srv.cfg.Server.MinAvailableMemoryMB = &zeroFloor
	app := createGenerationTestApp(t, store, "handoff-capacity-unknown", 1, 0)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	before, _ := store.GetActiveDeploymentGeneration(app.ID)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false); rec.Code != http.StatusConflict {
		t.Fatalf("unknown capacity = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	after, _ := store.GetActiveDeploymentGeneration(app.ID)
	if after.DeploymentID != before.DeploymentID {
		t.Fatal("unknown-capacity refusal changed the active pointer")
	}
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v3')", true); rec.Code != http.StatusOK {
		t.Fatalf("explicit downtime fallback = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestDeploy_ConcurrentGenerationCapacityChecksShareLaunchReservation(t *testing.T) {
	srv, store, token, _, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	zeroFloor := 0
	srv.cfg.Server.MinAvailableMemoryMB = &zeroFloor
	for _, slug := range []string{"handoff-reserve-a", "handoff-reserve-b"} {
		app := createGenerationTestApp(t, store, slug, 1, 16)
		if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
			t.Fatalf("first deploy %s: %d %s", slug, rec.Code, rec.Body.String())
		}
	}
	var calls atomic.Int32
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	srv.SetAvailableMemoryForTest(func() (int, error) {
		if calls.Add(1) == 1 {
			close(firstEntered)
			<-releaseFirst
		}
		return 32, nil
	})
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for _, slug := range []string{"handoff-reserve-a", "handoff-reserve-b"} {
		wg.Add(1)
		go func(slug string) {
			defer wg.Done()
			results <- deployBareGeneration(t, srv, token, slug, "print('v2')", false).Code
		}(slug)
	}
	<-firstEntered
	time.Sleep(100 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("capacity samples entered concurrently: calls=%d, want 1 while first reservation is held", got)
	}
	close(releaseFirst)
	wg.Wait()
	close(results)
	for status := range results {
		if status != http.StatusOK {
			t.Fatalf("concurrent handoff status = %d, want 200", status)
		}
	}
}

func TestDeploy_RetirementFailureKeepsDurableIdentityAndBlocksNextHandoff(t *testing.T) {
	srv, store, token, mgr, _, runtime := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	srv.cfg.Server.DrainTimeout = 20 * time.Millisecond
	app := createGenerationTestApp(t, store, "handoff-cleanup", 1, 16)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	oldInfo, ok := mgr.GetReplica(app.Slug, 0)
	if !ok {
		t.Fatal("old manager pool missing")
	}
	runtime.mu.Lock()
	runtime.signalFailures[oldInfo.PID] = errors.New("injected stop failure")
	runtime.mu.Unlock()
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false); rec.Code != http.StatusOK {
		t.Fatalf("handoff deploy: %d %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		runtime.mu.Lock()
		calls := runtime.signalCalls[oldInfo.PID]
		runtime.mu.Unlock()
		if calls >= 8 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("old generation stop attempts = %d, want bounded retry limit 8", calls)
		}
		time.Sleep(20 * time.Millisecond)
	}
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil || len(rows) == 0 {
		t.Fatalf("durable cleanup ledger = %+v, err=%v; want retained old identity", rows, err)
	}
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v3')", false); rec.Code != http.StatusConflict {
		t.Fatalf("handoff with pending cleanup = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

func TestServerCloseCancelsGenerationRetirementWithoutLosingIdentity(t *testing.T) {
	srv, store, token, mgr, _, runtime := buildManifestE2EServer(t, config.RuntimeConfig{})
	srv.cfg.Server.DrainTimeout = time.Hour
	app := createGenerationTestApp(t, store, "handoff-shutdown", 1, 16)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	oldInfo, ok := mgr.GetReplica(app.Slug, 0)
	if !ok {
		t.Fatal("old manager pool missing")
	}
	runtime.mu.Lock()
	runtime.signalFailures[oldInfo.PID] = errors.New("injected stop failure")
	runtime.mu.Unlock()
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false); rec.Code != http.StatusOK {
		t.Fatalf("handoff deploy: %d %s", rec.Code, rec.Body.String())
	}

	deadline := time.Now().Add(time.Second)
	for {
		runtime.mu.Lock()
		calls := runtime.signalCalls[oldInfo.PID]
		runtime.mu.Unlock()
		if calls > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retirement worker did not attempt cleanup")
		}
		time.Sleep(10 * time.Millisecond)
	}
	started := time.Now()
	srv.Close()
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("server close waited %v for generation worker", elapsed)
	}
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil || len(rows) == 0 {
		t.Fatalf("shutdown cleanup ledger = %+v, err=%v; want retained identity", rows, err)
	}
}

func TestDeploy_TransientGenerationLedgerDeleteFailureRecoversWithoutRestart(t *testing.T) {
	srv, store, token, _, _, _ := buildManifestE2EServer(t, config.RuntimeConfig{})
	defer srv.Close()
	app := createGenerationTestApp(t, store, "handoff-ledger-retry", 1, 16)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	old, _ := store.GetActiveDeploymentGeneration(app.ID)
	if _, err := store.DB().Exec(fmt.Sprintf(`
		CREATE TRIGGER fail_candidate_ledger_delete
		BEFORE DELETE ON deployment_replicas
		WHEN OLD.deployment_id != %d
		BEGIN
			SELECT RAISE(FAIL, 'injected generation ledger delete failure');
		END`, old.DeploymentID)); err != nil {
		t.Fatal(err)
	}
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false); rec.Code != http.StatusOK {
		t.Fatalf("handoff with transient delete failure: %d %s", rec.Code, rec.Body.String())
	}
	rows, err := store.ListDeploymentReplicas(app.ID)
	if err != nil || len(rows) == 0 {
		t.Fatalf("injected failure did not retain a cleanup row: rows=%+v err=%v", rows, err)
	}
	if _, err := store.DB().Exec(`DROP TRIGGER fail_candidate_ledger_delete`); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		rows, err = store.ListDeploymentReplicas(app.ID)
		if err == nil && len(rows) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("online ledger retry did not recover: rows=%+v err=%v", rows, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v3')", false); rec.Code != http.StatusOK {
		t.Fatalf("next handoff remained blocked after cleanup recovery: %d %s", rec.Code, rec.Body.String())
	}
}
