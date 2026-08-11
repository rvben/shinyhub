package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/db"
)

// getAppIsolation reads both isolation fields off GET /api/apps/{slug} and the
// same app's entry in GET /api/apps, so a divergence between the detail payload
// and the list payload fails the test rather than reaching one surface only.
func getAppIsolation(t *testing.T, srv *api.Server, token, slug string) (detailRaw, detailEffective, listRaw, listEffective string) {
	t.Helper()

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "GET", "/api/apps/"+slug, nil, token))
	if rec.Code != 200 {
		t.Fatalf("GET /api/apps/%s: %d: %s", slug, rec.Code, rec.Body.String())
	}
	var detail struct {
		App db.App `json:"app"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}

	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "GET", "/api/apps", nil, token))
	if rec.Code != 200 {
		t.Fatalf("GET /api/apps: %d: %s", rec.Code, rec.Body.String())
	}
	var list struct {
		Items []db.App `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	found := false
	for _, a := range list.Items {
		if a.Slug == slug {
			listRaw, listEffective, found = a.WorkerIsolation, a.EffectiveWorkerIsolation, true
		}
	}
	if !found {
		t.Fatalf("app %s missing from the list payload", slug)
	}
	return detail.App.WorkerIsolation, detail.App.EffectiveWorkerIsolation, listRaw, listEffective
}

// clearAppIsolation empties an app's worker_isolation so it inherits the fleet
// default. A created app carries an explicit "multiplex" from the column
// default, which is the opposite of the state under test.
func clearAppIsolation(t *testing.T, srv *api.Server, token, slug string) {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "PATCH", "/api/apps/"+slug,
		[]byte(`{"worker_isolation":"","worker_max_workers":2}`), token))
	if rec.Code != 200 {
		t.Fatalf("PATCH clearing isolation: %d: %s", rec.Code, rec.Body.String())
	}
}

// An app that leaves worker_isolation empty inherits the fleet default, so its
// pool is elastic while the stored column says nothing. The payload must carry
// the resolved value: the dashboard decides whether to offer Sleep from it, and
// reading the raw column would advertise an action the server rejects with 409.
func TestGetApp_EffectiveIsolationResolvesTheFleetDefault(t *testing.T) {
	srv, store := newWorkerFleetDefaultServer(t, "per_session", 0)
	slug, token := seedWorkerApp(t, store)
	clearAppIsolation(t, srv, token, slug)

	detailRaw, detailEff, listRaw, listEff := getAppIsolation(t, srv, token, slug)

	if detailRaw != "" || listRaw != "" {
		t.Fatalf("raw isolation = %q/%q, want empty (the app inherits)", detailRaw, listRaw)
	}
	if detailEff != "per_session" {
		t.Errorf("detail effective_worker_isolation = %q, want per_session", detailEff)
	}
	if listEff != "per_session" {
		t.Errorf("list effective_worker_isolation = %q, want per_session", listEff)
	}
}

// The negative control: the same empty column on a server with no configured
// default resolves to multiplex, so a test that passes for both cases cannot be
// reading a constant.
func TestGetApp_EffectiveIsolationDefaultsToMultiplex(t *testing.T) {
	srv, store := newWorkerPatchServer(t)
	slug, token := seedWorkerApp(t, store)
	clearAppIsolation(t, srv, token, slug)

	detailRaw, detailEff, listRaw, listEff := getAppIsolation(t, srv, token, slug)

	if detailRaw != "" || listRaw != "" {
		t.Fatalf("raw isolation = %q/%q, want empty (the app inherits)", detailRaw, listRaw)
	}
	if detailEff != "multiplex" {
		t.Errorf("detail effective_worker_isolation = %q, want multiplex", detailEff)
	}
	if listEff != "multiplex" {
		t.Errorf("list effective_worker_isolation = %q, want multiplex", listEff)
	}
}

// An explicit per-app mode wins over the fleet default, and the raw column is
// left untouched so the config form still renders it as an explicit choice
// rather than "inherit".
func TestGetApp_ExplicitIsolationOverridesTheFleetDefault(t *testing.T) {
	srv, store := newWorkerFleetDefaultServer(t, "per_session", 0)
	slug, token := seedWorkerApp(t, store)

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, "PATCH", "/api/apps/"+slug,
		[]byte(`{"worker_isolation":"multiplex"}`), token))
	if rec.Code != 200 {
		t.Fatalf("PATCH: %d: %s", rec.Code, rec.Body.String())
	}

	detailRaw, detailEff, _, listEff := getAppIsolation(t, srv, token, slug)

	if detailRaw != "multiplex" {
		t.Errorf("raw worker_isolation = %q, want multiplex (the explicit choice must survive)", detailRaw)
	}
	if detailEff != "multiplex" || listEff != "multiplex" {
		t.Errorf("effective_worker_isolation = %q/%q, want multiplex", detailEff, listEff)
	}
}
