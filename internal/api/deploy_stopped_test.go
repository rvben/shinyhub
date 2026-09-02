package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// seedStoppedTestApp creates an app owned by the E2E admin with deployCount
// prior successful deploys: each one is a promoted deployment row AND a counter
// increment, matching what a real deploy leaves behind. A prior successful
// deploy is what separates "an operator stopped this" from "this has never been
// deployed". Tests that need the two to disagree build that state themselves.
func seedStoppedTestApp(t *testing.T, store *db.Store, slug, status string, deployCount int) *db.App {
	t.Helper()
	admin, err := store.GetUserByUsername("admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{
		Slug: slug, Name: slug, OwnerID: admin.ID,
	}); err != nil {
		t.Fatal(err)
	}
	seeded, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < deployCount; i++ {
		dep, derr := store.BeginDeployment(seeded.ID, fmt.Sprintf("v%d", i+1), t.TempDir())
		if derr != nil {
			t.Fatal(derr)
		}
		if derr := store.PromoteDeployment(dep.ID); derr != nil {
			t.Fatal(derr)
		}
		if err := store.IncrementDeployCount(slug); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: slug, Status: status}); err != nil {
		t.Fatal(err)
	}
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	return app
}

// deployRespKeptStopped reads the deploy response's kept_stopped flag.
func deployRespKeptStopped(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	var body struct {
		KeptStopped bool `json:"kept_stopped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode deploy response: %v (%s)", err, rec.Body.String())
	}
	return body.KeptStopped
}

// postStoppedTestBundle deploys a minimal Python bundle to slug. query is
// appended to the deploy URL verbatim (e.g. "?start=true").
func postStoppedTestBundle(t *testing.T, srv *Server, token, slug, query string) *httptest.ResponseRecorder {
	t.Helper()
	body, ctype := buildMultiFileBundleUpload(t, map[string]string{
		"app.py": "from shiny import App\n",
	})
	req := httptest.NewRequest("POST", "/api/apps/"+slug+"/deploy"+query, body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-ShinyHub-Allow-Downtime", "1")
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

// EC2 parity: a stopped instance stays stopped. An operator who stopped an app
// must not find it running again after the next CI deploy.
func TestDeploy_StoppedAppStaysStopped(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	seedStoppedTestApp(t, store, "downed", "stopped", 1)

	rec := postStoppedTestBundle(t, srv, token, "downed", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy failed: %d %s", rec.Code, rec.Body.String())
	}

	app, err := store.GetAppBySlug("downed")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != "stopped" {
		t.Errorf("status = %q, want stopped", app.Status)
	}
	// Status alone is not the point: overwriting it while the pool booted would
	// leave the app serving traffic anyway, which is what stopping it was meant
	// to prevent. TestDeploy_RunningAppStillEndsRunning makes the same two
	// assertions in the positive, so a broken probe cannot pass both.
	if srv.proxy.PoolHasAny("downed") {
		t.Error("a kept-stopped deploy registered a routable backend")
	}
	replicas, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, rep := range replicas {
		if rep.Status == "running" {
			t.Errorf("replica %d is running after a kept-stopped deploy", rep.Index)
		}
	}
}

// Skipping the boot must not skip the bookkeeping: the deployment is still
// recorded and promoted, so a later `apps start` brings up the NEW bundle and
// the deployments list shows the version.
func TestDeploy_StoppedAppStillRecordsTheDeployment(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	app := seedStoppedTestApp(t, store, "downed", "stopped", 1)

	before, err := store.ListDeployments(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec := postStoppedTestBundle(t, srv, token, "downed", ""); rec.Code != http.StatusOK {
		t.Fatalf("deploy failed: %d %s", rec.Code, rec.Body.String())
	}
	after, err := store.ListDeployments(app.ID)
	if err != nil {
		t.Fatal(err)
	}

	if len(after) != len(before)+1 {
		t.Fatalf("deployments = %d, want %d", len(after), len(before)+1)
	}
	if after[0].Status != db.DeploymentSucceeded {
		t.Errorf("deployment status = %q, want succeeded", after[0].Status)
	}
	fresh, err := store.GetAppBySlug("downed")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.DeployCount != app.DeployCount+1 {
		t.Errorf("deploy_count = %d, want %d", fresh.DeployCount, app.DeployCount+1)
	}
}

// The response says so explicitly, so the CLI reports "stopped" rather than
// inferring it from a status field that a concurrent start could have changed.
func TestDeploy_StoppedAppReportsKeptStopped(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	seedStoppedTestApp(t, store, "downed", "stopped", 1)

	rec := postStoppedTestBundle(t, srv, token, "downed", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy failed: %d %s", rec.Code, rec.Body.String())
	}
	if !deployRespKeptStopped(t, rec) {
		t.Errorf("kept_stopped = false, want true: %s", rec.Body.String())
	}
}

// ?start=true is the explicit override for a pipeline that wants deploy to mean
// "make it live".
func TestDeploy_StartQueryOverridesStoppedState(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	seedStoppedTestApp(t, store, "downed", "stopped", 1)

	rec := postStoppedTestBundle(t, srv, token, "downed", "?start=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy failed: %d %s", rec.Code, rec.Body.String())
	}

	app, err := store.GetAppBySlug("downed")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != "running" {
		t.Errorf("status = %q, want running", app.Status)
	}
	if deployRespKeptStopped(t, rec) {
		t.Error("kept_stopped = true on an explicit ?start=true deploy")
	}
}

// The negative control. Without it, an implementation that never starts
// anything would pass every test above.
func TestDeploy_RunningAppStillEndsRunning(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	seedStoppedTestApp(t, store, "live", "running", 1)

	if rec := postStoppedTestBundle(t, srv, token, "live", ""); rec.Code != http.StatusOK {
		t.Fatalf("deploy failed: %d %s", rec.Code, rec.Body.String())
	}

	app, err := store.GetAppBySlug("live")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != "running" {
		t.Errorf("status = %q, want running", app.Status)
	}
	replicas, err := store.ListReplicas(app.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(replicas) == 0 {
		t.Error("no replica rows recorded; a running deploy must still boot the pool")
	}
	if !srv.proxy.PoolHasAny("live") {
		t.Error("no routable backend registered; a running deploy must still register the pool")
	}
}

// A never-deployed app has no prior state to preserve, so a first deploy starts
// it. Its status is "stopped" before the first deploy, which must not be read
// as an operator decision.
func TestDeploy_FirstDeployStartsTheApp(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	seedStoppedTestApp(t, store, "fresh", "stopped", 0)

	if rec := postStoppedTestBundle(t, srv, token, "fresh", ""); rec.Code != http.StatusOK {
		t.Fatalf("deploy failed: %d %s", rec.Code, rec.Body.String())
	}

	app, err := store.GetAppBySlug("fresh")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != "running" {
		t.Errorf("status = %q, want running on first deploy", app.Status)
	}
}

// deploy_count is soft state: the handler logs and continues when
// IncrementDeployCount fails, so an app can carry a succeeded deployment while
// the counter still reads 0. Keying the decision off the counter would then
// treat the next deploy as a first deploy and start an app the operator had
// stopped. The durable deployments row is the authority.
func TestDeploy_StoppedAppWithZeroCounterStaysStopped(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	app := seedStoppedTestApp(t, store, "downed", "stopped", 0)
	dep, err := store.BeginDeployment(app.ID, "v1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}

	if rec := postStoppedTestBundle(t, srv, token, "downed", ""); rec.Code != http.StatusOK {
		t.Fatalf("deploy failed: %d %s", rec.Code, rec.Body.String())
	}

	fresh, err := store.GetAppBySlug("downed")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.DeployCount != 1 {
		t.Fatalf("precondition: deploy_count = %d, want the counter to have been 0 before this deploy", fresh.DeployCount)
	}
	if fresh.Status != "stopped" {
		t.Errorf("status = %q, want stopped", fresh.Status)
	}
}

// The other half of the same predicate: a first deploy that FAILED leaves a
// failed deployment row behind. The retry that fixes the bundle must still
// start the app, so the test is a succeeded deployment (what ListDeployments
// returns), not the mere existence of a row.
func TestDeploy_RetryAfterFailedFirstDeployStartsTheApp(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	app := seedStoppedTestApp(t, store, "fresh", "stopped", 0)
	dep, err := store.BeginDeployment(app.ID, "v1", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FailDeployment(dep.ID); err != nil {
		t.Fatal(err)
	}

	if rec := postStoppedTestBundle(t, srv, token, "fresh", ""); rec.Code != http.StatusOK {
		t.Fatalf("deploy failed: %d %s", rec.Code, rec.Body.String())
	}

	fresh, err := store.GetAppBySlug("fresh")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "running" {
		t.Errorf("status = %q, want running: a failed first deploy is not an operator decision", fresh.Status)
	}
}

// A broken bundle must not be the thing that brings a withdrawn app back. The
// generic failure path restores the previous pool, which for a stopped app
// would boot the OLD version and mark it running - so a failing CI pipeline
// would silently undo the operator's stop.
func TestDeploy_FailedDeployLeavesAStoppedAppStopped(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	seedStoppedTestApp(t, store, "downed", "stopped", 0)

	// A real first deploy, so the app has a promoted deployment whose bundle
	// dir exists on disk: without it restorePreviousPool has nothing to boot
	// and the test would pass for the wrong reason.
	if rec := postStoppedTestBundle(t, srv, token, "downed", ""); rec.Code != http.StatusOK {
		t.Fatalf("first deploy failed: %d %s", rec.Code, rec.Body.String())
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "downed", Status: "stopped"}); err != nil {
		t.Fatal(err)
	}
	srv.proxy.Deregister("downed")

	// Only the new bundle fails; the old one still boots, so the buggy
	// behaviour is a running app on the previous version rather than a
	// second failure.
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		if p.PrepareOnly {
			return nil, errors.New("bundle is broken")
		}
		p.HealthCheck = func(string, time.Duration, http.RoundTripper) error { return nil }
		return deploy.Run(p)
	})

	rec := postStoppedTestBundle(t, srv, token, "downed", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a broken bundle must fail the deploy, got %d %s", rec.Code, rec.Body.String())
	}

	fresh, err := store.GetAppBySlug("downed")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "stopped" {
		t.Errorf("status = %q, want stopped after a failed deploy", fresh.Status)
	}
	if srv.proxy.PoolHasAny("downed") {
		t.Error("a failed deploy restored a routable backend for a stopped app")
	}
}

// The control for the test above: for an app that was running, a failed deploy
// must still restore the previous pool. Without this, dropping the restore
// entirely would pass.
func TestDeploy_FailedDeployStillRestoresARunningApp(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	seedStoppedTestApp(t, store, "live", "stopped", 0)

	if rec := postStoppedTestBundle(t, srv, token, "live", ""); rec.Code != http.StatusOK {
		t.Fatalf("first deploy failed: %d %s", rec.Code, rec.Body.String())
	}

	var failNext bool
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		if !failNext {
			failNext = true
			return nil, errors.New("bundle is broken")
		}
		p.HealthCheck = func(string, time.Duration, http.RoundTripper) error { return nil }
		return deploy.Run(p)
	})

	rec := postStoppedTestBundle(t, srv, token, "live", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a broken bundle must fail the deploy, got %d %s", rec.Code, rec.Body.String())
	}

	fresh, err := store.GetAppBySlug("live")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Status != "running" {
		t.Errorf("status = %q, want running: a failed deploy must restore a live app", fresh.Status)
	}
	if !srv.proxy.PoolHasAny("live") {
		t.Error("no routable backend after restoring a live app")
	}
}

// A hibernated app is asleep, not withdrawn: the next request wakes it, so a
// deploy legitimately brings it back up rather than leaving it down.
func TestDeploy_HibernatedAppStillEndsRunning(t *testing.T) {
	srv, store, token := newManifestE2EServer(t)
	seedStoppedTestApp(t, store, "napping", "hibernated", 1)

	if rec := postStoppedTestBundle(t, srv, token, "napping", ""); rec.Code != http.StatusOK {
		t.Fatalf("deploy failed: %d %s", rec.Code, rec.Body.String())
	}

	app, err := store.GetAppBySlug("napping")
	if err != nil {
		t.Fatal(err)
	}
	if app.Status != "running" {
		t.Errorf("status = %q, want running", app.Status)
	}
}
