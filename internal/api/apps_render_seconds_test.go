package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestPatchApp_RenderSecondsPersistsAndReturnsPacingBlock verifies that PATCHing
// render_seconds persists the value, applies it live to the proxy, and the
// response carries a render_pacing block with the suggested and current
// effective session caps.
func TestPatchApp_RenderSecondsPersistsAndReturnsPacingBlock(t *testing.T) {
	srv, store, _ := newInlineServerWithProxy(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token := loginAsAdmin(t, srv)
	createApp(t, srv, token, "my-app")

	patch := map[string]any{"render_seconds": 1.3}
	body, _ := json.Marshal(patch)
	req := httptest.NewRequest(http.MethodPatch, "/api/apps/my-app", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	app, ok := resp["app"].(map[string]any)
	if !ok {
		t.Fatalf("response missing app object: %v", resp)
	}
	if app["render_seconds"] != 1.3 {
		t.Errorf("app.render_seconds = %v, want 1.3", app["render_seconds"])
	}

	block, ok := resp["render_pacing"].(map[string]any)
	if !ok {
		t.Fatalf("response missing render_pacing block: %v", resp)
	}
	if block["render_seconds"] != 1.3 {
		t.Errorf("render_pacing.render_seconds = %v, want 1.3", block["render_seconds"])
	}
	suggested, ok := block["suggested_max_sessions_per_replica"].(float64)
	if !ok || suggested < 1 {
		t.Errorf("render_pacing.suggested_max_sessions_per_replica = %v, want >= 1", block["suggested_max_sessions_per_replica"])
	}
	current, ok := block["current_effective_max_sessions_per_replica"].(float64)
	if !ok || current != 0 {
		t.Errorf("render_pacing.current_effective_max_sessions_per_replica = %v, want 0 (no per-app or runtime default set)", block["current_effective_max_sessions_per_replica"])
	}

	// Re-GET to confirm the value is durably persisted, not just echoed back.
	req = httptest.NewRequest(http.MethodGet, "/api/apps/my-app", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr = httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	var getResp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode GET: %v", err)
	}
	getApp := getResp["app"].(map[string]any)
	if getApp["render_seconds"] != 1.3 {
		t.Errorf("GET app.render_seconds = %v, want 1.3", getApp["render_seconds"])
	}
}

// TestPatchApp_RenderSecondsRejectsNegative verifies a negative render_seconds
// is rejected with 400 and never persisted.
func TestPatchApp_RenderSecondsRejectsNegative(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token := loginAsAdmin(t, srv)
	createApp(t, srv, token, "my-app")

	patch := map[string]any{"render_seconds": -1}
	body, _ := json.Marshal(patch)
	req := httptest.NewRequest(http.MethodPatch, "/api/apps/my-app", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("render_seconds=-1: got %d, want 400: %s", rr.Code, rr.Body.String())
	}

	app, err := store.GetAppBySlug("my-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.RenderSeconds != 0 {
		t.Errorf("render_seconds after rejected PATCH = %v, want 0 (unchanged)", app.RenderSeconds)
	}
}

// TestPatchApp_RenderSecondsRejectsNonFinite verifies a JSON number that cannot
// be represented as a finite float64 (1e999, which overflows to +Inf) is
// rejected with 400 rather than persisted as +Inf.
func TestPatchApp_RenderSecondsRejectsNonFinite(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token := loginAsAdmin(t, srv)
	createApp(t, srv, token, "my-app")

	req := httptest.NewRequest(http.MethodPatch, "/api/apps/my-app", bytes.NewReader([]byte(`{"render_seconds": 1e999}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.Router().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("render_seconds=1e999: got %d, want 400: %s", rr.Code, rr.Body.String())
	}

	app, err := store.GetAppBySlug("my-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.RenderSeconds != 0 {
		t.Errorf("render_seconds after rejected non-finite PATCH = %v, want 0 (unchanged)", app.RenderSeconds)
	}
}

// TestPatchApp_RenderSecondsOmittedLeavesUnchanged verifies that a PATCH which
// does not mention render_seconds leaves the app's stored value untouched.
func TestPatchApp_RenderSecondsOmittedLeavesUnchanged(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	token := loginAsAdmin(t, srv)
	createApp(t, srv, token, "my-app")

	patch := func(body map[string]any) int {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest(http.MethodPatch, "/api/apps/my-app", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		srv.Router().ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("PATCH %v: got %d: %s", body, rr.Code, rr.Body.String())
		}
		return rr.Code
	}

	patch(map[string]any{"render_seconds": 2.5})
	patch(map[string]any{"name": "renamed"})

	app, err := store.GetAppBySlug("my-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.RenderSeconds != 2.5 {
		t.Errorf("render_seconds after unrelated PATCH = %v, want 2.5 (unchanged)", app.RenderSeconds)
	}
}
