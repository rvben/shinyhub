package api_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
)

// Restart and rollback both bring back a bundle that already served, so they are
// activations: their hooks ran when the bundle was promoted, and re-running
// app-controlled code on a routine restart is not safe to assume idempotent.
// They differ from the unattended restore path only in the fallback for a bundle
// whose preparation state predates the record - these fail loudly rather than
// degrading, because someone is waiting on the result.

// activationHarness deploys an app successfully, then exposes a hook to drive
// restart/rollback and observe the Params each was invoked with.
type activationHarness struct {
	t        *testing.T
	srv      *api.Server
	store    *db.Store
	token    string
	mu       sync.Mutex
	params   []deploy.Params
	failWith error
}

func (h *activationHarness) lastParams() deploy.Params {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.params) == 0 {
		h.t.Fatal("deployRun was never invoked")
	}
	return h.params[len(h.params)-1]
}

func newActivationHarness(t *testing.T, slug string) *activationHarness {
	t.Helper()
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)
	h := &activationHarness{t: t, srv: srv, store: store}

	srv.SetDeployRunForTest(func(p deploy.Params) (*deploy.PoolResult, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.params = append(h.params, p)
		if h.failWith != nil {
			return nil, h.failWith
		}
		return &deploy.PoolResult{}, nil
	})

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_ = store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: u.ID})
	h.token, _ = auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")

	body, ctype := buildBundleUpload(t, "app.py", "print(1)\n")
	req := httptest.NewRequest("POST", "/api/apps/"+slug+"/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("seed deploy returned %d, want 200: %s", rec.Code, rec.Body.String())
	}
	return h
}

func (h *activationHarness) post(path string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest("POST", path, nil)
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rec, req)
	return rec
}

// TestRestart_IsAnActivation: restarting the live deployment must not re-run its
// post-deploy hooks. Before this, every restart re-executed app-controlled build
// steps.
func TestRestart_IsAnActivation(t *testing.T) {
	h := newActivationHarness(t, "rs")
	if rec := h.post("/api/apps/rs/restart"); rec.Code != http.StatusOK {
		t.Fatalf("restart returned %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := h.lastParams().Preparation; got != deploy.PrepareSkip {
		t.Errorf("restart Preparation = %v, want PrepareSkip", got)
	}
}

// TestRollback_IsAnActivation: rolling back to a prepared deployment likewise
// skips preparation. The mode must key off the historical deployment being
// restored, not the fresh pending row the rollback creates (which is never
// prepared).
func TestRollback_IsAnActivation(t *testing.T) {
	h := newActivationHarness(t, "rb")

	// A second deploy so there is a previous version to roll back to.
	body, ctype := buildBundleUpload(t, "app.py", "print(2)\n")
	req := httptest.NewRequest("POST", "/api/apps/rb/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second deploy returned %d, want 200: %s", rec.Code, rec.Body.String())
	}

	if rec := h.post("/api/apps/rb/rollback"); rec.Code != http.StatusOK {
		t.Fatalf("rollback returned %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := h.lastParams().Preparation; got != deploy.PrepareSkip {
		t.Errorf("rollback Preparation = %v, want PrepareSkip: keyed off the pending row instead of the target?", got)
	}
}

// TestRestartFailure_ReportsFailureKind: "restart failed" told the caller
// nothing. A hook failure on restart is now reachable for elastic apps, so the
// response must classify it and name the cause, like the deploy path does.
func TestRestartFailure_ReportsFailureKind(t *testing.T) {
	h := newActivationHarness(t, "rsf")
	h.mu.Lock()
	h.failWith = errors.New(`hook[0] (make assets): exit status 2`)
	h.mu.Unlock()

	rec := h.post("/api/apps/rsf/restart")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("restart returned %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"failure_kind":"hook_failed"`) {
		t.Errorf("restart failure must carry failure_kind, got: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "make assets") {
		t.Errorf("restart failure must name the cause, got: %s", rec.Body.String())
	}
}

// TestRollbackFailure_ReportsFailureKind: same contract for rollback.
func TestRollbackFailure_ReportsFailureKind(t *testing.T) {
	h := newActivationHarness(t, "rbf")

	body, ctype := buildBundleUpload(t, "app.py", "print(2)\n")
	req := httptest.NewRequest("POST", "/api/apps/rbf/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second deploy returned %d, want 200: %s", rec.Code, rec.Body.String())
	}

	h.mu.Lock()
	h.failWith = errors.New("all replicas failed health check: replica 0: health: app at http://127.0.0.1:1/ did not become healthy within 120s")
	h.mu.Unlock()

	rec = h.post("/api/apps/rbf/rollback")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("rollback returned %d, want 500: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"failure_kind":"readiness_timeout"`) {
		t.Errorf("rollback failure must carry failure_kind, got: %s", rec.Body.String())
	}
}

// TestActivationPreparation_BothBranches pins the mode for both states in one
// run. The prepared branch carries the weight: PrepareSkip is a non-zero value,
// so observing it proves the helper is actually consulted and the field wired
// through. The unprepared branch then proves the fallback is PrepareRequired
// rather than the restore path's PrepareBestEffort - swapping it would let a
// restart bring an app up after its build failed. (Asserting PrepareRequired
// alone would be vacuous, since it is the zero value; the pairing is what makes
// this test mean something.)
func TestActivationPreparation_BothBranches(t *testing.T) {
	t.Run("prepared skips preparation", func(t *testing.T) {
		h := newActivationHarness(t, "ab1")
		if rec := h.post("/api/apps/ab1/restart"); rec.Code != http.StatusOK {
			t.Fatalf("restart returned %d, want 200: %s", rec.Code, rec.Body.String())
		}
		if got := h.lastParams().Preparation; got != deploy.PrepareSkip {
			t.Fatalf("prepared restart Preparation = %v, want PrepareSkip", got)
		}
	})

	t.Run("unprepared prepares and fails loudly", func(t *testing.T) {
		h := newActivationHarness(t, "ab2")
		appID := mustAppID(t, h.store, "ab2")
		deps, err := h.store.ListDeployments(appID)
		if err != nil || len(deps) == 0 {
			t.Fatalf("list deployments: %v (n=%d)", err, len(deps))
		}
		// A row predating the prepared column: CreateDeployment records a
		// succeeded deployment without marking it prepared, and being newest it
		// becomes the one restart activates. No test-only production method needed.
		if _, err := h.store.CreateDeployment(db.CreateDeploymentParams{
			AppID: appID, Version: "legacy", BundleDir: deps[0].BundleDir,
		}); err != nil {
			t.Fatalf("create legacy deployment: %v", err)
		}

		if rec := h.post("/api/apps/ab2/restart"); rec.Code != http.StatusOK {
			t.Fatalf("restart returned %d, want 200: %s", rec.Code, rec.Body.String())
		}
		got := h.lastParams().Preparation
		if got == deploy.PrepareBestEffort {
			t.Fatal("a user-initiated restart must not degrade to best-effort preparation; that is the unattended restore path's contract")
		}
		if got != deploy.PrepareRequired {
			t.Errorf("unprepared restart Preparation = %v, want PrepareRequired", got)
		}
	})
}

func mustAppID(t *testing.T, store *db.Store, slug string) int64 {
	t.Helper()
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatalf("get app %s: %v", slug, err)
	}
	return app.ID
}

// TestWorkerDialChange_IsAnActivation: changing a scaling dial on a running app
// triggers a background redeploy of the bundle that is already live. Nothing the
// build or the hooks read has changed, so re-running app-controlled hooks to
// apply a replica count is a side effect nobody asked for - the same reasoning
// that makes restart an activation.
func TestWorkerDialChange_IsAnActivation(t *testing.T) {
	h := newActivationHarness(t, "wd")

	// redeployApp only fires for an app whose prior status is "running"
	// (apps.go workerChanged). The stubbed deploy does not leave it there, so
	// set it explicitly - otherwise this test silently skips and proves nothing.
	if err := h.store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "wd", Status: "running"}); err != nil {
		t.Fatalf("mark running: %v", err)
	}

	req := httptest.NewRequest("PATCH", "/api/apps/wd",
		strings.NewReader(`{"worker_isolation":"grouped","worker_grouped_size":6,"worker_max_workers":40}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.token)
	rec := httptest.NewRecorder()
	h.srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH returned %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// redeployApp runs in a goroutine; wait for it to reach deployRun.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		n := len(h.params)
		h.mu.Unlock()
		if n > 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	h.mu.Lock()
	n := len(h.params)
	h.mu.Unlock()
	if n < 2 {
		t.Fatal("a worker-dial change on a running app must trigger a redeploy")
	}
	if got := h.lastParams().Preparation; got != deploy.PrepareSkip {
		t.Errorf("dial-change redeploy Preparation = %v, want PrepareSkip", got)
	}
}
