package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

// An app carrying the 'deleting' tombstone accepts only reads and DELETEs. A
// stop, restart or sleep would move it off the tombstone (or bring its
// processes back), and startup reconciliation would then never finish the
// interrupted deletion.
func TestTombstonedApp_RefusesLifecycleMutations(t *testing.T) {
	srv, store, token, _, _, fake := buildManifestE2EServer(t, config.RuntimeConfig{})
	createGenerationTestApp(t, store, "demo", 1, 16)
	if rec := deployBareGeneration(t, srv, token, "demo", "print('v1')", true); rec.Code != http.StatusOK {
		t.Fatalf("deploy = %d body=%s", rec.Code, rec.Body.String())
	}
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "demo", Status: "deleting"}); err != nil {
		t.Fatal(err)
	}
	startCount := func() int {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.starts)
	}
	before := startCount()

	for _, m := range []struct {
		method, path, body string
	}{
		{http.MethodPost, "/api/apps/demo/stop", ""},
		{http.MethodPost, "/api/apps/demo/restart", ""},
		{http.MethodPost, "/api/apps/demo/sleep", ""},
		// Ownership transfer authorizes through requireViewApp, not
		// requireManageApp, and must refuse as well.
		{http.MethodPost, "/api/apps/demo/owner", `{"username":"admin"}`},
	} {
		action := m.method + " " + m.path
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, tombstoneRequest(t, m.method, m.path, []byte(m.body), token))
		if rr.Code != http.StatusConflict {
			t.Errorf("%s of a tombstoned app = %d, want 409; body=%s", action, rr.Code, rr.Body.String())
		}
		app, err := store.GetAppBySlug("demo")
		if err != nil {
			t.Fatalf("after %s: %v", action, err)
		}
		if app.Status != "deleting" {
			t.Errorf("status after %s = %q, want the tombstone kept", action, app.Status)
		}
	}
	if n := startCount() - before; n != 0 {
		t.Errorf("tombstoned app started %d process(es), want none", n)
	}

	// Teardown stays possible: removing a draft (and its preview) or a
	// sub-resource, and ending a development session's lease. None exists
	// here, so each answers 404 once the guard has let it through.
	for _, m := range []struct{ method, path string }{
		{http.MethodDelete, "/api/apps/demo/drafts/0123456789abcdef"},
		{http.MethodDelete, "/api/apps/demo/env/FOO"},
		{http.MethodPost, "/api/apps/demo/development-sessions/0123456789abcdef0123456789abcdef/end"},
	} {
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, tombstoneRequest(t, m.method, m.path, nil, token))
		if rr.Code == http.StatusConflict {
			t.Errorf("%s %s of a tombstoned app = 409, want it accepted; body=%s", m.method, m.path, rr.Body.String())
		}
	}

	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, tombstoneRequest(t, http.MethodGet, "/api/apps/demo", nil, token))
	if rr.Code != http.StatusOK {
		t.Errorf("GET of a tombstoned app = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, tombstoneRequest(t, http.MethodDelete, "/api/apps/demo", nil, token))
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE of a tombstoned app = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if _, err := store.GetAppBySlug("demo"); err == nil {
		t.Error("DELETE did not finish the tombstoned app's deletion")
	}
}

func tombstoneRequest(t *testing.T, method, path string, body []byte, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}
