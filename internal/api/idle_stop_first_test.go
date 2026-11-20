package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/lifecycle"
	"github.com/rvben/shinyhub/internal/process"
)

// idleStopFirstManifest declares a producer that reruns on every bundle change,
// so a code change cannot use parallel handoff and needs a stop-first deploy.
const idleStopFirstManifest = `[app]
replicas = 1
memory_limit_mb = 16
[app.worker]
isolation = "multiplex"
[[schedule]]
name = "refresh"
cron = "0 5 * * *"
cmd = "true"
deploy_trigger = "bundle_change"
on_success = "roll"
`

func deployIdleStopFirstBundle(t *testing.T, srv *Server, token, slug, source string) *httptest.ResponseRecorder {
	t.Helper()
	body, contentType := buildMultiFileBundleUpload(t, map[string]string{
		"app.py": source, "shinyhub.toml": idleStopFirstManifest,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/apps/"+slug+"/deploy", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

// armAppSessionHold makes the fake runtime serve traffic and park requests
// carrying hold=1. It runs before the first deploy, because a backend reads the
// hold channels without synchronisation once it has started.
func armAppSessionHold(runtime *manifestFakeRuntime) {
	runtime.serveTraffic = true
	runtime.holdStarted = make(chan struct{}, 1)
	runtime.holdRelease = make(chan struct{})
}

// holdAppSession parks one request inside the app's backend and returns a
// function that releases it. While it is parked the proxy counts one session,
// which is what a visitor with the app open looks like to a deploy.
func holdAppSession(t *testing.T, srv *Server, runtime *manifestFakeRuntime, slug string, cookies ...*http.Cookie) (release func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/app/"+slug+"/?hold=1", nil)
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		srv.proxy.ServeHTTP(httptest.NewRecorder(), req)
	}()
	select {
	case <-runtime.holdStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("held request did not reach its backend")
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			close(runtime.holdRelease)
			<-done
		})
	}
}

// holdElasticAppSession does the same for an app whose workers start on
// demand: the first request starts a worker and the held one follows it there.
func holdElasticAppSession(t *testing.T, srv *Server, store *db.Store, mgr *process.Manager, runtime *manifestFakeRuntime, slug string) (release func()) {
	t.Helper()
	spawner := &lifecycle.ElasticSpawner{
		Store: store, Manager: mgr, Proxy: srv.proxy,
		HealthCheck: func(string, time.Duration, http.RoundTripper) error { return nil },
	}
	spawned := make(chan struct{}, 4)
	srv.proxy.SetSpawnFunc(func(slug string, slot int) { spawner.Spawn(slug, slot); spawned <- struct{}{} })
	srv.proxy.SetTerminateFunc(spawner.Terminate)
	first := httptest.NewRecorder()
	srv.proxy.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/app/"+slug+"/", nil))
	select {
	case <-spawned:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	return holdAppSession(t, srv, runtime, slug, first.Result().Cookies()...)
}

func TestDeploy_StopFirstWithoutConsentWhenNoSessionIsConnected(t *testing.T) {
	srv, store, token, runtime := newManifestE2EServerWithJobs(t)
	defer srv.Close()
	runtime.serveTraffic = true
	app := createGenerationTestApp(t, store, "idle-stop-first", 1, 16)
	if rec := deployIdleStopFirstBundle(t, srv, token, app.Slug, "print('app') # v1"); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	previous, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	live, err := store.GetAppBySlug(app.Slug)
	if err != nil || live.Status != "running" {
		t.Fatalf("app before redeploy = %+v err=%v, want running", live, err)
	}

	rec := deployIdleStopFirstBundle(t, srv, token, app.Slug, "print('app') # v2")
	if rec.Code != http.StatusOK {
		t.Fatalf("redeploy of an app with no connected session: %d %s", rec.Code, rec.Body.String())
	}
	active, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if active.DeploymentID == previous.DeploymentID {
		t.Fatal("new version was not promoted")
	}
	promoted, err := store.GetDeploymentByID(active.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if status, body := proxyGenerationBody(srv, app.Slug, ""); status != http.StatusOK || body != promoted.Version {
		t.Fatalf("route after redeploy = %d %q, want new version %q", status, body, promoted.Version)
	}
	var response struct {
		StopFirstReason string `json:"stop_first_reason"`
		StopFirstIdle   bool   `json:"stop_first_idle"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.StopFirstIdle || !strings.Contains(response.StopFirstReason, "changes shared producer state") {
		t.Fatalf("response does not explain the unattended stop-first deploy: %s", rec.Body.String())
	}
	// The deploy was accepted without consent, so its reason must not say that
	// consent was needed.
	for _, consent := range []string{"explicit", "--allow-downtime"} {
		if strings.Contains(response.StopFirstReason, consent) {
			t.Fatalf("reason of a deploy accepted without consent mentions %q: %s", consent, response.StopFirstReason)
		}
	}
}

func TestDeploy_StopFirstStillNeedsConsentWhileASessionIsConnected(t *testing.T) {
	srv, store, token, runtime := newManifestE2EServerWithJobs(t)
	defer srv.Close()
	armAppSessionHold(runtime)
	app := createGenerationTestApp(t, store, "busy-stop-first", 1, 16)
	if rec := deployIdleStopFirstBundle(t, srv, token, app.Slug, "print('app') # v1"); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	previous, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	previousDep, err := store.GetDeploymentByID(previous.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	release := holdAppSession(t, srv, runtime, app.Slug)
	defer release()

	rec := deployIdleStopFirstBundle(t, srv, token, app.Slug, "print('app') # v2")
	if rec.Code != http.StatusConflict || rec.Header().Get("X-ShinyHub-Conflict") != "generation-handoff-deferred" {
		t.Fatalf("redeploy with a connected session: %d %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		"changes shared producer state",
		"the app is in use (1 open connection)",
		"retry when it is not in use, or retry the CLI command with --allow-downtime",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("refusal does not say %q: %s", want, rec.Body.String())
		}
	}
	active, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil || active.DeploymentID != previous.DeploymentID {
		t.Fatalf("refused deploy changed authority: %+v err=%v", active, err)
	}
	if latestGenerationDeploymentStatus(t, store, app.ID) != db.DeploymentFailed {
		t.Fatal("refused attempt was not recorded as failed")
	}
	release()
	if status, body := proxyGenerationBody(srv, app.Slug, ""); status != http.StatusOK || body != previousDep.Version {
		t.Fatalf("working version after refusal = %d %q, want %q", status, body, previousDep.Version)
	}
}

// A visitor who has just loaded the page and has not opened a WebSocket yet
// holds no connection, and is still someone the deploy would interrupt.
func TestDeploy_StopFirstStillNeedsConsentRightAfterAVisit(t *testing.T) {
	srv, store, token, runtime := newManifestE2EServerWithJobs(t)
	defer srv.Close()
	runtime.serveTraffic = true
	app := createGenerationTestApp(t, store, "visited-stop-first", 1, 16)
	if rec := deployIdleStopFirstBundle(t, srv, token, app.Slug, "print('app') # v1"); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	previous, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := proxyGenerationBody(srv, app.Slug, ""); status != http.StatusOK {
		t.Fatalf("visit = %d", status)
	}
	rec := deployIdleStopFirstBundle(t, srv, token, app.Slug, "print('app') # v2")
	if rec.Code != http.StatusConflict || rec.Header().Get("X-ShinyHub-Conflict") != "generation-handoff-deferred" {
		t.Fatalf("redeploy right after a visit: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "the app is in use (a request reached it in the last 5s)") {
		t.Fatalf("refusal does not name the recent visit: %s", rec.Body.String())
	}
	active, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil || active.DeploymentID != previous.DeploymentID {
		t.Fatalf("refused deploy changed authority: %+v err=%v", active, err)
	}
}

// Another cluster node may hold sessions this node cannot count, so a
// clustered server never infers that an app is unused.
func TestDeploy_StopFirstStillNeedsConsentOnAClusteredServer(t *testing.T) {
	srv, store, token, runtime := newManifestE2EServerWithJobs(t)
	defer srv.Close()
	runtime.serveTraffic = true
	app := createGenerationTestApp(t, store, "clustered-stop-first", 1, 16)
	if rec := deployBareGeneration(t, srv, token, app.Slug, "print('v1')", false); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	previous, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetCluster("test-instance")
	rec := deployBareGeneration(t, srv, token, app.Slug, "print('v2')", false)
	if rec.Code != http.StatusConflict || rec.Header().Get("X-ShinyHub-Conflict") != "generation-handoff-deferred" {
		t.Fatalf("clustered redeploy with no local session: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "in use") {
		t.Fatalf("clustered refusal offers idleness as a remedy: %s", rec.Body.String())
	}
	active, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil || active.DeploymentID != previous.DeploymentID {
		t.Fatalf("refused deploy changed authority: %+v err=%v", active, err)
	}
}

// The idle check that admits the deploy is a sample. A visitor who connects
// after it, and before the old version leaves the routing table, keeps the
// working version, and the deploy becomes the usual refusal.
func TestDeploy_StopFirstIsRefusedWhenASessionConnectsDuringAdmission(t *testing.T) {
	srv, store, token, runtime := newManifestE2EServerWithJobs(t)
	defer srv.Close()
	armAppSessionHold(runtime)
	app := createGenerationTestApp(t, store, "raced-stop-first", 1, 16)
	if rec := deployIdleStopFirstBundle(t, srv, token, app.Slug, "print('app') # v1"); rec.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", rec.Code, rec.Body.String())
	}
	previous, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	previousDep, err := store.GetDeploymentByID(previous.DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	admitted := 0
	release := func() {}
	srv.SetBeforeIdleDeregisterForTest(func(slug string) {
		admitted++
		release = holdAppSession(t, srv, runtime, slug)
	})
	defer func() { release() }()

	rec := deployIdleStopFirstBundle(t, srv, token, app.Slug, "print('app') # v2")
	if admitted != 1 {
		t.Fatalf("deploy was admitted as idle %d times, want 1: %d %s", admitted, rec.Code, rec.Body.String())
	}
	if rec.Code != http.StatusConflict || rec.Header().Get("X-ShinyHub-Conflict") != "generation-handoff-deferred" {
		t.Fatalf("redeploy raced by a new session: %d %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{
		"changes shared producer state",
		"the app is in use (1 open connection, seen while the deployment was being admitted)",
		"retry when it is not in use, or retry the CLI command with --allow-downtime",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("refusal does not say %q: %s", want, rec.Body.String())
		}
	}
	active, err := store.GetActiveDeploymentGeneration(app.ID)
	if err != nil || active.DeploymentID != previous.DeploymentID {
		t.Fatalf("refused deploy changed authority: %+v err=%v", active, err)
	}
	if latestGenerationDeploymentStatus(t, store, app.ID) != db.DeploymentFailed {
		t.Fatal("refused attempt was not recorded as failed")
	}
	live, err := store.GetAppBySlug(app.Slug)
	if err != nil || live.Status != "running" {
		t.Fatalf("app after the refusal = %+v err=%v, want still running", live, err)
	}
	release()
	if status, body := proxyGenerationBody(srv, app.Slug, ""); status != http.StatusOK || body != previousDep.Version {
		t.Fatalf("working version after refusal = %d %q, want %q", status, body, previousDep.Version)
	}
}
