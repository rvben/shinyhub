package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

const fleetStateRunID = "0123456789abcdef0123456789abcdef"

func registerFleetStateRun(t *testing.T, srv http.Handler, token string) {
	t.Helper()
	body := []byte(`{"run_id":"` + fleetStateRunID + `","fleet_id":"prod","kind":"fleet_apply","provenance":{"provider":"github"}}`)
	req := authedRequest(t, http.MethodPost, "/api/fleet/runs", body, token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register run = %d: %s", rec.Code, rec.Body.String())
	}
}

func putFleetState(t *testing.T, srv http.Handler, token, status, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := authedRequest(t, http.MethodPut, "/api/apps/fleet-detail/fleet-state", []byte(body), token)
	req.Header.Set("X-Shinyhub-Run-Id", fleetStateRunID)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func getFleetState(t *testing.T, srv http.Handler, token string) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, authedRequest(t, http.MethodGet, "/api/apps/fleet-detail", nil, token))
	if rec.Code != http.StatusOK {
		t.Fatalf("get app = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	state, ok := body["fleet_state"].(map[string]any)
	if !ok {
		t.Fatalf("fleet_state missing: %s", rec.Body.String())
	}
	return state
}

func TestFleetStateReportsTemporaryChangesAndIncompleteConvergence(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedAppWithPromotedDeploy(t, store, "fleet-detail", "sha256:live")
	marker := "fleet:prod"
	if err := store.SetAppManagedBy("fleet-detail", &marker); err != nil {
		t.Fatal(err)
	}
	router := srv.Router()
	registerFleetStateRun(t, router, token)

	success := `{"status":"in_sync","desired_content_digest":"sha256:live","declaration":[{"key":"visibility","desired":"private"},{"key":"name","desired":"\"fleet-detail\""},{"key":"replicas","desired":"1"}]}`
	if rec := putFleetState(t, router, token, "in_sync", success); rec.Code != http.StatusNoContent {
		t.Fatalf("record success = %d: %s", rec.Code, rec.Body.String())
	}
	if state := getFleetState(t, router, token); state["status"] != "in_sync" {
		t.Fatalf("initial state = %#v", state)
	}

	patch := authedRequest(t, http.MethodPatch, "/api/apps/fleet-detail", []byte(`{"replicas":2}`), token)
	patchRec := httptest.NewRecorder()
	router.ServeHTTP(patchRec, patch)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("manual patch = %d: %s", patchRec.Code, patchRec.Body.String())
	}
	state := getFleetState(t, router, token)
	if state["status"] != "temporary_changes" {
		t.Fatalf("temporary state = %#v", state)
	}
	if _, present := state["changed_by"]; present {
		t.Fatalf("temporary state must not attribute uncorrelated audit events: %#v", state)
	}
	if _, present := state["changed_at"]; present {
		t.Fatalf("temporary state must not timestamp uncorrelated audit events: %#v", state)
	}
	changes, _ := state["changes"].([]any)
	if len(changes) != 1 {
		t.Fatalf("changes = %#v", changes)
	}
	change := changes[0].(map[string]any)
	if change["key"] != "replicas" || change["current"] != "2" || change["fleet"] != "1" {
		t.Fatalf("replica change = %#v", change)
	}

	incomplete := `{"status":"incomplete","desired_content_digest":"sha256:live","declaration":[],"error":"replica update failed"}`
	if rec := putFleetState(t, router, token, "incomplete", incomplete); rec.Code != http.StatusNoContent {
		t.Fatalf("record incomplete = %d: %s", rec.Code, rec.Body.String())
	}
	state = getFleetState(t, router, token)
	if state["status"] != "incomplete" || state["error"] != "replica update failed" || state["application"] == nil {
		t.Fatalf("incomplete state = %#v", state)
	}
}

func TestFleetStateRefusesFalseSuccessfulBaseline(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedAppWithPromotedDeploy(t, store, "fleet-detail", "sha256:live")
	marker := "fleet:prod"
	if err := store.SetAppManagedBy("fleet-detail", &marker); err != nil {
		t.Fatal(err)
	}
	router := srv.Router()
	registerFleetStateRun(t, router, token)
	body := `{"status":"in_sync","desired_content_digest":"sha256:other","declaration":[{"key":"visibility","desired":"private"}]}`
	req := authedRequest(t, http.MethodPut, "/api/apps/fleet-detail/fleet-state", bytes.TrimSpace([]byte(body)), token)
	req.Header.Set("X-Shinyhub-Run-Id", fleetStateRunID)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("false baseline = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetAppFleetState(fleetStateAppID(t, store, "fleet-detail")); err != nil {
		t.Fatal(err)
	}
}

func fleetStateAppID(t *testing.T, store *db.Store, slug string) int64 {
	t.Helper()
	app, err := store.GetAppBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	return app.ID
}
