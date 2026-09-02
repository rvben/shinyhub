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
	registerFleetStateRunWithID(t, srv, token, fleetStateRunID)
}

func registerFleetStateRunWithID(t *testing.T, srv http.Handler, token, runID string) {
	registerFleetStateRunWithFleet(t, srv, token, runID, "prod")
}

func registerFleetStateRunWithFleet(t *testing.T, srv http.Handler, token, runID, fleetID string) {
	t.Helper()
	body := []byte(`{"run_id":"` + runID + `","fleet_id":"` + fleetID + `","kind":"fleet_apply","provenance":{"provider":"github"}}`)
	req := authedRequest(t, http.MethodPost, "/api/fleet/runs", body, token)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register run = %d: %s", rec.Code, rec.Body.String())
	}
}

func putFleetState(t *testing.T, srv http.Handler, token, status, body string) *httptest.ResponseRecorder {
	return putFleetStateWithRun(t, srv, token, fleetStateRunID, body)
}

func putFleetStateWithRun(t *testing.T, srv http.Handler, token, runID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := authedRequest(t, http.MethodPut, "/api/apps/fleet-detail/fleet-state", []byte(body), token)
	req.Header.Set("X-Shinyhub-Run-Id", runID)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestFleetStateOldClientSuccessRetainsLegacyChangedSemantics(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedAppWithPromotedDeploy(t, store, "fleet-detail", "sha256:live")
	marker := "fleet:prod"
	if err := store.SetAppManagedBy("fleet-detail", &marker); err != nil {
		t.Fatal(err)
	}
	router := srv.Router()
	registerFleetStateRun(t, router, token)
	body := `{"status":"in_sync","desired_content_digest":"sha256:live","declaration":[{"key":"visibility","desired":"private"}]}`
	if rec := putFleetState(t, router, token, "in_sync", body); rec.Code != http.StatusNoContent {
		t.Fatalf("first success = %d: %s", rec.Code, rec.Body.String())
	}

	secondID := "fedcba9876543210fedcba9876543210"
	registerFleetStateRunWithID(t, router, token, secondID)
	if rec := putFleetStateWithRun(t, router, token, secondID, body); rec.Code != http.StatusNoContent {
		t.Fatalf("legacy-shaped second success = %d: %s", rec.Code, rec.Body.String())
	}
	state := getFleetState(t, router, token)
	application, _ := state["application"].(map[string]any)
	if application["run_id"] != secondID {
		t.Fatalf("legacy omitted state_changed did not advance application: %#v", state)
	}
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
	} else if state["checked_at"] == nil {
		t.Fatalf("initial state has no checked_at: %#v", state)
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

func TestFleetStateAdoptionRecoversAfterLostAdoptRecording(t *testing.T) {
	srv, store := newTestServer(t)
	token := seedAppWithPromotedDeploy(t, store, "fleet-detail", "sha256:live")
	marker := "fleet:prod"
	if err := store.SetAppManagedBy("fleet-detail", &marker); err != nil {
		t.Fatal(err)
	}
	router := srv.Router()
	registerFleetStateRun(t, router, token)
	body := `{"status":"in_sync","desired_content_digest":"sha256:live","declaration":[{"key":"visibility","desired":"private"}]}`
	if rec := putFleetState(t, router, token, "in_sync", body); rec.Code != http.StatusNoContent {
		t.Fatalf("prod success = %d: %s", rec.Code, rec.Body.String())
	}

	// Fleet beta adopts the app; source and declared config already match, so
	// the adopt mutates only ownership. The adopt run's fleet-state request is
	// lost (never arrives), leaving prod's run as the recorded application.
	adopted := "fleet:beta"
	if err := store.SetAppManagedBy("fleet-detail", &adopted); err != nil {
		t.Fatal(err)
	}

	// Beta's next apply is a no-op check. It must repair the provenance: the
	// view discards a cross-fleet application, so without the repair the app
	// reads as never applied by its owning fleet, forever.
	betaRunID := "b17ab17ab17ab17ab17ab17ab17ab17a"
	registerFleetStateRunWithFleet(t, router, token, betaRunID, "beta")
	noop := `{"status":"in_sync","desired_content_digest":"sha256:live","declaration":[{"key":"visibility","desired":"private"}],"state_changed":false}`
	if rec := putFleetStateWithRun(t, router, token, betaRunID, noop); rec.Code != http.StatusNoContent {
		t.Fatalf("beta no-op = %d: %s", rec.Code, rec.Body.String())
	}
	state := getFleetState(t, router, token)
	if state["status"] != "in_sync" {
		t.Fatalf("adopted state = %#v, want in_sync with a recorded application", state)
	}
	application, _ := state["application"].(map[string]any)
	if application["run_id"] != betaRunID {
		t.Fatalf("application = %#v, want the adopting fleet's no-op run", application)
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
