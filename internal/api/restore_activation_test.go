package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// restorePreviousPool is the unattended safety net that brings back the previous
// good bundle after a failed deploy. It had no coverage, which is how a change to
// the deploy path silently changed its failure mode: preparation started running
// on it, so a transient build error or a non-idempotent hook could leave the app
// degraded instead of recovered. These tests pin the contract that restoring is
// an activation, not a promotion.

// deployThenFail drives one successful deploy followed by a failing one, and
// returns the Params the restore attempt was invoked with. The second deploy's
// failure is what triggers restorePreviousPool.
func deployThenFail(t *testing.T) (restoreParams deploy.Params, restored bool) {
	t.Helper()
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)

	var (
		mu    sync.Mutex
		calls int
	)
	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		switch calls {
		case 1: // initial promotion succeeds
			return &deploy.PoolResult{}, nil
		case 2: // the new bundle fails, triggering the restore
			return nil, errors.New("all replicas failed health check: replica 0: boom")
		default: // the restore of the previous bundle
			restoreParams, restored = p, true
			return &deploy.PoolResult{}, nil
		}
	})

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "ra", Name: "RA", OwnerID: u.ID})
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")

	postDeploy := func() *httptest.ResponseRecorder {
		body, ctype := buildBundleUpload(t, "app.py", "print(1)\n")
		req := httptest.NewRequest("POST", "/api/apps/ra/deploy", body)
		req.Header.Set("Content-Type", ctype)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.Router().ServeHTTP(rec, req)
		return rec
	}

	if rec := postDeploy(); rec.Code != http.StatusOK {
		t.Fatalf("first deploy returned %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec := postDeploy(); rec.Code != http.StatusInternalServerError {
		t.Fatalf("second deploy returned %d, want 500: %s", rec.Code, rec.Body.String())
	}
	return restoreParams, restored
}

// TestRestorePreviousPool_IsAnActivation: the restore must not be a promotion.
// A successful deploy records the bundle as prepared, so bringing it back skips
// preparation entirely rather than re-running app-controlled hooks.
func TestRestorePreviousPool_IsAnActivation(t *testing.T) {
	p, restored := deployThenFail(t)
	if !restored {
		t.Fatal("a failed deploy must trigger a restore of the previous bundle")
	}
	if p.Preparation != deploy.PrepareSkip {
		t.Errorf("restore Preparation = %v, want PrepareSkip: the previous deploy recorded the bundle as prepared, so its hooks must not run again", p.Preparation)
	}
}

// TestFailedDeploy_DoesNotRecordPrepared is the dangerous direction. Marking a
// bundle prepared when its preparation did not actually complete would make a
// later restore skip the build for an environment that was never built, and the
// app would come up against nothing. A failed deploy must leave the row
// unprepared so a restore falls back to building it.
func TestFailedDeploy_DoesNotRecordPrepared(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)
	srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) {
		return nil, errors.New(`hook[0] (make assets): exit status 2`)
	})

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "fp", Name: "FP", OwnerID: u.ID})

	body, ctype := buildBundleUpload(t, "app.py", "print(1)\n")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/fp/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("deploy returned %d, want 500: %s", rec.Code, rec.Body.String())
	}

	// A failed deploy's row is 'failed', which ListDeployments excludes, so
	// assert over every row for this app rather than only the live ones.
	for _, d := range allDeploymentsForApp(t, store, "fp") {
		if d.Prepared {
			t.Errorf("deployment %d recorded prepared despite the deploy failing", d.ID)
		}
	}
}

// TestDeploySuccess_RecordsPrepared: PrepareSkip above is only correct because a
// successful deploy durably records that the bundle was prepared. If that record
// stops being written, restores silently downgrade to rebuilding.
func TestDeploySuccess_RecordsPrepared(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)
	srv.SetDeployRunForTest(func(deploy.Params) (*deploy.PoolResult, error) {
		return &deploy.PoolResult{}, nil
	})

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "pr", Name: "PR", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("pr")

	body, ctype := buildBundleUpload(t, "app.py", "print(1)\n")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	req := httptest.NewRequest("POST", "/api/apps/pr/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("deploy returned %d, want 200: %s", rec.Code, rec.Body.String())
	}

	deps, err := store.ListDeployments(app.ID)
	if err != nil || len(deps) == 0 {
		t.Fatalf("list deployments: %v (n=%d)", err, len(deps))
	}
	if !deps[0].Prepared {
		t.Error("a succeeded deploy must record the deployment as prepared")
	}
}

// TestDeployment_PreparedDefaultsFalse: rows written before the column existed
// report false, which the restore path must read as "unknown" and handle with a
// best-effort build rather than assuming either way.
func TestDeployment_PreparedDefaultsFalse(t *testing.T) {
	appsDir := t.TempDir()
	_, store := newQuotaTestServer(t, appsDir, 0)
	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: "df", Name: "DF", OwnerID: u.ID})
	app, _ := store.GetAppBySlug("df")

	dep, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID: app.ID, Version: "1", BundleDir: t.TempDir(),
	})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	deps, _ := store.ListDeployments(app.ID)
	if len(deps) == 0 || deps[0].Prepared {
		t.Error("a deployment must default to not-prepared until it is recorded")
	}

	if err := store.MarkDeploymentPrepared(dep.ID); err != nil {
		t.Fatalf("mark prepared: %v", err)
	}
	deps, _ = store.ListDeployments(app.ID)
	if len(deps) == 0 || !deps[0].Prepared {
		t.Error("MarkDeploymentPrepared must persist and round-trip")
	}
}

// allDeploymentsForApp returns every deployment row for an app, including the
// failed ones ListDeployments filters out. Goes through the by-slug summary
// listing (which does include failed rows) and re-reads each by id to get the
// full record.
func allDeploymentsForApp(t *testing.T, store *db.Store, slug string) []*db.Deployment {
	t.Helper()
	summaries, err := store.ListDeploymentsBySlug(slug)
	if err != nil {
		t.Fatalf("list deployments for %s: %v", slug, err)
	}
	if len(summaries) == 0 {
		t.Fatalf("no deployment rows recorded for %s; the test would assert nothing", slug)
	}
	out := make([]*db.Deployment, 0, len(summaries))
	for _, sum := range summaries {
		d, err := store.GetDeploymentBySlugAndID(slug, sum.ID)
		if err != nil {
			t.Fatalf("get deployment %d: %v", sum.ID, err)
		}
		out = append(out, d)
	}
	return out
}
