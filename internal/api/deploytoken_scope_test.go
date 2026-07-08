package api_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// doToken performs a request authenticated with the `token` scheme (the
// deploy-token / API-key path; `Bearer` routes to the JWT validator).
func doToken(t *testing.T, srv interface{ Router() http.Handler }, method, path, raw string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("Authorization", "token "+raw)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	return rec
}

// scopedDeployToken registers a pre-shared deploy token on srv with the given
// role and app allowlist, returning the raw credential.
func scopedDeployToken(t *testing.T, srv interface {
	SetDeployToken(*auth.DeployToken)
}, store *db.Store, role string, scope []string) string {
	t.Helper()
	sysUser, err := store.UpsertSystemUser(db.SystemUsernameDeploy, role)
	if err != nil {
		t.Fatalf("upsert system user: %v", err)
	}
	raw := "shk_" + strings.Repeat("c", 64)
	srv.SetDeployToken(auth.NewDeployToken(raw, &auth.ContextUser{
		ID: sysUser.ID, Username: sysUser.Username, Role: sysUser.Role, AppScope: scope,
	}))
	return raw
}

// TestDeployTokenScope_RestrictsAppSurface pins the allowlist semantics: a
// deploy token with auth.deploy_token_apps set can only see and touch the
// listed slugs, even when its role is admin and even when the other app is
// public. Out-of-scope apps 404 (the anti-enumeration shape used everywhere).
func TestDeployTokenScope_RestrictsAppSurface(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, _ := mkUser(t, store, "owner", "developer")
	for _, slug := range []string{"inscope", "outscope"} {
		if err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: ownerID}); err != nil {
			t.Fatal(err)
		}
	}
	// Make the out-of-scope app public: scope must beat both role and visibility.
	if err := store.SetAppAccess("outscope", "public"); err != nil {
		t.Fatal(err)
	}
	tok := scopedDeployToken(t, srv, store, "admin", []string{"inscope", "newapp"})

	get := func(path string) *int {
		rec := doToken(t, srv, "GET", path, tok, nil)
		return &rec.Code
	}
	if code := get("/api/apps/inscope"); *code != http.StatusOK {
		t.Errorf("GET inscope = %d, want 200", *code)
	}
	if code := get("/api/apps/outscope"); *code != http.StatusNotFound {
		t.Errorf("GET outscope = %d, want 404 (scope beats admin role and public visibility)", *code)
	}
	rec := doToken(t, srv, "PATCH", "/api/apps/outscope/access", tok, []byte(`{"access":"private"}`))
	if rec.Code != http.StatusNotFound {
		t.Errorf("PATCH outscope/access = %d, want 404", rec.Code)
	}
	rec = doToken(t, srv, "GET", "/api/apps/outscope/data", tok, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET outscope/data = %d, want 404", rec.Code)
	}

	rec = doToken(t, srv, "GET", "/api/apps", tok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/apps = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "inscope") {
		t.Errorf("app list should include the in-scope app, got %s", body)
	}
	if strings.Contains(body, "outscope") {
		t.Errorf("app list must not include out-of-scope apps, got %s", body)
	}

	rec = doToken(t, srv, "POST", "/api/apps", tok, []byte(`{"slug":"other","name":"Other"}`))
	if rec.Code != http.StatusForbidden {
		t.Errorf("create out-of-scope app = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	rec = doToken(t, srv, "POST", "/api/apps", tok, []byte(`{"slug":"newapp","name":"New App"}`))
	if rec.Code != http.StatusCreated {
		t.Errorf("create in-scope app = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
}

// TestDeployTokenScope_EmptyScopeUnrestricted pins backward compatibility: a
// deploy token with no allowlist keeps full role-based access.
func TestDeployTokenScope_EmptyScopeUnrestricted(t *testing.T) {
	srv, store := newTestServer(t)
	ownerID, _ := mkUser(t, store, "owner", "developer")
	if err := store.CreateApp(db.CreateAppParams{Slug: "anyapp", Name: "Any", OwnerID: ownerID}); err != nil {
		t.Fatal(err)
	}
	tok := scopedDeployToken(t, srv, store, "admin", nil)
	rec := doToken(t, srv, "GET", "/api/apps/anyapp", tok, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("unscoped deploy token GET = %d, want 200", rec.Code)
	}
}
