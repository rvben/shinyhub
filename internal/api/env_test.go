package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/secrets"
)

func TestListAppEnv_MasksSecrets(t *testing.T) {
	srv, store := newTestServer(t)

	key := secrets.DeriveKey("test-secret")
	srv.SetSecretsKey(key)

	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	ownerToken, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")

	store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo App", OwnerID: owner.ID})
	app, _ := store.GetApp("demo")

	// Plain env var
	store.UpsertAppEnvVar(app.ID, "AWS_REGION", []byte("eu-west-1"), false)

	// Secret env var — stored as ciphertext
	ciphertext, err := secrets.Encrypt(key, []byte("supersecret"))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	store.UpsertAppEnvVar(app.ID, "AWS_SECRET_KEY", ciphertext, true)

	req := authedRequest(t, "GET", "/api/apps/demo/env", nil, ownerToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Env []struct {
			Key    string `json:"key"`
			Value  string `json:"value"`
			Secret bool   `json:"secret"`
			Set    bool   `json:"set"`
		} `json:"env"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Env) != 2 {
		t.Fatalf("expected 2 env vars, got %d", len(resp.Env))
	}

	byKey := make(map[string]struct {
		Value  string
		Secret bool
		Set    bool
	})
	for _, item := range resp.Env {
		byKey[item.Key] = struct {
			Value  string
			Secret bool
			Set    bool
		}{item.Value, item.Secret, item.Set}
	}

	region := byKey["AWS_REGION"]
	if region.Value != "eu-west-1" {
		t.Errorf("AWS_REGION value: want eu-west-1, got %q", region.Value)
	}
	if region.Secret {
		t.Error("AWS_REGION should not be secret")
	}
	if !region.Set {
		t.Error("AWS_REGION.set should be true")
	}

	secretKey := byKey["AWS_SECRET_KEY"]
	if secretKey.Value != "" {
		t.Errorf("AWS_SECRET_KEY value should be masked, got %q", secretKey.Value)
	}
	if !secretKey.Secret {
		t.Error("AWS_SECRET_KEY should be secret")
	}
	if !secretKey.Set {
		t.Error("AWS_SECRET_KEY.set should be true")
	}
}

func TestListAppEnv_ViewerCanList(t *testing.T) {
	srv, store := newTestServer(t)

	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "appowner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("appowner")
	viewer, _ := store.GetUserByUsername("viewer")
	viewerToken, _ := auth.IssueJWT(viewer.ID, "viewer", "developer", "test-secret")

	store.CreateApp(db.CreateAppParams{Slug: "shared-app", Name: "Shared App", OwnerID: owner.ID})
	store.SetAppAccess("shared-app", "shared")
	app, _ := store.GetApp("shared-app")
	store.UpsertAppEnvVar(app.ID, "MODE", []byte("production"), false)

	req := authedRequest(t, "GET", "/api/apps/shared-app/env", nil, viewerToken)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("viewer should be able to list env on shared app: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestListAppEnv_UnauthenticatedDenied(t *testing.T) {
	srv, store := newTestServer(t)

	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner2", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner2")
	store.CreateApp(db.CreateAppParams{Slug: "private-app", Name: "Private App", OwnerID: owner.ID})

	req := httptest.NewRequest("GET", "/api/apps/private-app/env", nil) // no auth
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

// --- PUT /api/apps/{slug}/env/{key} tests ---

// setupEnvApp creates a developer user + app for upsert tests.
// Returns the server (with secrets key set), store, app, and a bearer token for the owner.
func setupEnvApp(t *testing.T) (*testEnvFixture, error) {
	t.Helper()
	srv, store := newTestServer(t)
	key := secrets.DeriveKey("test-secret")
	srv.SetSecretsKey(key)

	hash, _ := auth.HashPassword("pass")
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"}); err != nil {
		return nil, err
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		return nil, err
	}
	ownerToken, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")

	if err := store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo App", OwnerID: owner.ID}); err != nil {
		return nil, err
	}
	app, err := store.GetApp("demo")
	if err != nil {
		return nil, err
	}

	return &testEnvFixture{
		srv:        srv,
		store:      store,
		app:        app,
		ownerToken: ownerToken,
		secretsKey: key,
	}, nil
}

type testEnvFixture struct {
	srv        *api.Server
	store      *db.Store
	app        *db.App
	ownerToken string
	secretsKey []byte
}

func putEnv(t *testing.T, f *testEnvFixture, key, value string, secret bool) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"value": value, "secret": secret})
	req := authedRequest(t, "PUT", "/api/apps/demo/env/"+key, body, f.ownerToken)
	rec := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(rec, req)
	return rec
}

func TestUpsertAppEnv_CreatesNonSecret(t *testing.T) {
	f, err := setupEnvApp(t)
	if err != nil {
		t.Fatal(err)
	}

	rec := putEnv(t, f, "AWS_REGION", "eu-west-1", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["key"] != "AWS_REGION" {
		t.Errorf("want key=AWS_REGION, got %v", resp["key"])
	}
	if resp["set"] != true {
		t.Errorf("want set=true, got %v", resp["set"])
	}

	// Confirm via GET
	getReq := authedRequest(t, "GET", "/api/apps/demo/env", nil, f.ownerToken)
	getRec := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET want 200, got %d", getRec.Code)
	}
	var listResp struct {
		Env []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"env"`
	}
	json.NewDecoder(getRec.Body).Decode(&listResp)
	found := false
	for _, v := range listResp.Env {
		if v.Key == "AWS_REGION" && v.Value == "eu-west-1" {
			found = true
		}
	}
	if !found {
		t.Errorf("GET did not return AWS_REGION=eu-west-1 after PUT")
	}
}

func TestUpsertAppEnv_CreatesSecret_EncryptsAtRest(t *testing.T) {
	f, err := setupEnvApp(t)
	if err != nil {
		t.Fatal(err)
	}

	rec := putEnv(t, f, "DB_PASSWORD", "topsecret", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Read the raw row — value must NOT equal the plaintext
	v, err := f.store.GetAppEnvVar(f.app.ID, "DB_PASSWORD")
	if err != nil {
		t.Fatalf("GetAppEnvVar: %v", err)
	}
	if string(v.Value) == "topsecret" {
		t.Fatal("secret value stored as plaintext — encryption not applied")
	}
	if !v.IsSecret {
		t.Fatal("is_secret flag not set")
	}

	// Decrypt and verify round-trip
	pt, err := secrets.Decrypt(f.secretsKey, v.Value)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if string(pt) != "topsecret" {
		t.Errorf("round-trip failed: got %q, want topsecret", pt)
	}
}

func TestUpsertAppEnv_UpdatesExisting(t *testing.T) {
	f, err := setupEnvApp(t)
	if err != nil {
		t.Fatal(err)
	}

	rec1 := putEnv(t, f, "LOG_LEVEL", "debug", false)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first PUT want 200, got %d: %s", rec1.Code, rec1.Body.String())
	}

	rec2 := putEnv(t, f, "LOG_LEVEL", "info", false)
	if rec2.Code != http.StatusOK {
		t.Fatalf("second PUT want 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	v, err := f.store.GetAppEnvVar(f.app.ID, "LOG_LEVEL")
	if err != nil {
		t.Fatalf("GetAppEnvVar: %v", err)
	}
	if string(v.Value) != "info" {
		t.Errorf("want value=info after update, got %q", v.Value)
	}
}

func TestUpsertAppEnv_RejectsReservedPrefix(t *testing.T) {
	f, err := setupEnvApp(t)
	if err != nil {
		t.Fatal(err)
	}

	rec := putEnv(t, f, "SHINYHUB_FOO", "bar", false)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("want 422, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpsertAppEnv_RejectsInvalidKey(t *testing.T) {
	f, err := setupEnvApp(t)
	if err != nil {
		t.Fatal(err)
	}

	// Lowercase key
	rec := putEnv(t, f, "foo", "bar", false)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("lowercase key: want 422, got %d: %s", rec.Code, rec.Body.String())
	}

	// Leading digit
	rec2 := putEnv(t, f, "1FOO", "bar", false)
	if rec2.Code != http.StatusUnprocessableEntity {
		t.Errorf("leading digit key: want 422, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestUpsertAppEnv_EnforcesValueSize(t *testing.T) {
	f, err := setupEnvApp(t)
	if err != nil {
		t.Fatal(err)
	}

	bigValue := strings.Repeat("x", 65*1024) // 65 KB
	rec := putEnv(t, f, "BIG_VAR", bigValue, false)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("want 413, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpsertAppEnv_EnforcesKeyCount(t *testing.T) {
	f, err := setupEnvApp(t)
	if err != nil {
		t.Fatal(err)
	}

	// Insert 100 vars directly via store
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("VAR_%03d", i)
		if err := f.store.UpsertAppEnvVar(f.app.ID, key, []byte("val"), false); err != nil {
			t.Fatalf("seed var %d: %v", i, err)
		}
	}

	// 101st key via PUT
	rec := putEnv(t, f, "NEW_VAR", "value", false)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("want 422 at key cap, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpsertAppEnv_ViewerDenied(t *testing.T) {
	f, err := setupEnvApp(t)
	if err != nil {
		t.Fatal(err)
	}

	hash, _ := auth.HashPassword("pass")
	f.store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: hash, Role: "developer"})
	viewer, _ := f.store.GetUserByUsername("viewer")
	viewerToken, _ := auth.IssueJWT(viewer.ID, "viewer", "developer", "test-secret")

	// Set the app to shared so the viewer can at least see it, but has no manage rights
	f.store.SetAppAccess("demo", "shared")

	body, _ := json.Marshal(map[string]any{"value": "val", "secret": false})
	req := authedRequest(t, "PUT", "/api/apps/demo/env/FOO", body, viewerToken)
	rec := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("want 403 for viewer, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpsertAppEnv_WritesAuditEvent(t *testing.T) {
	f, err := setupEnvApp(t)
	if err != nil {
		t.Fatal(err)
	}

	rec := putEnv(t, f, "AWS_REGION", "eu-west-1", false)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	events, err := f.store.ListAuditEvents(10, 0)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least one audit event")
	}

	var found bool
	for _, e := range events {
		if e.Action == "env.set" && e.ResourceType == "app" && e.ResourceID == "demo" {
			found = true
			// Detail must contain key and secret flag
			if !strings.Contains(e.Detail, `"key":"AWS_REGION"`) {
				t.Errorf("audit detail missing key: %s", e.Detail)
			}
			if !strings.Contains(e.Detail, `"secret":false`) {
				t.Errorf("audit detail missing secret flag: %s", e.Detail)
			}
			// Detail must NOT contain the value
			if strings.Contains(e.Detail, "eu-west-1") {
				t.Errorf("audit detail must not contain the value, got: %s", e.Detail)
			}
		}
	}
	if !found {
		t.Errorf("no env.set audit event found; events: %+v", events)
	}
}

// TestUpsertAppEnv_RestartTrue_RestartsRunningApp is skipped because the
// process.Manager does not expose a Restart method and the test server is
// constructed without a manager. Restarting requires Stop + deploy.Run with
// the current deployment's bundle dir, which cannot be exercised without a
// real runtime. This is tracked for a future task.
func TestUpsertAppEnv_RestartTrue_RestartsRunningApp(t *testing.T) {
	t.Skip("manager.Restart not yet implemented; restart via ?restart=true returns restarted:false when no manager is present")
}

// --- DELETE /api/apps/{slug}/env/{key} tests ---

func TestDeleteAppEnv_Success(t *testing.T) {
	f, err := setupEnvApp(t)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.store.UpsertAppEnvVar(f.app.ID, "AWS_REGION", []byte("eu-west-1"), false); err != nil {
		t.Fatalf("seed var: %v", err)
	}

	req := authedRequest(t, "DELETE", "/api/apps/demo/env/AWS_REGION", nil, f.ownerToken)
	rec := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}

	// Confirm the key is gone via GET
	getReq := authedRequest(t, "GET", "/api/apps/demo/env", nil, f.ownerToken)
	getRec := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET want 200, got %d", getRec.Code)
	}
	var listResp struct {
		Env []struct {
			Key string `json:"key"`
		} `json:"env"`
	}
	json.NewDecoder(getRec.Body).Decode(&listResp)
	for _, v := range listResp.Env {
		if v.Key == "AWS_REGION" {
			t.Error("AWS_REGION still present after DELETE")
		}
	}
}

func TestDeleteAppEnv_NotFound(t *testing.T) {
	f, err := setupEnvApp(t)
	if err != nil {
		t.Fatal(err)
	}

	req := authedRequest(t, "DELETE", "/api/apps/demo/env/NO_SUCH_KEY", nil, f.ownerToken)
	rec := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("want 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteAppEnv_RequiresManager(t *testing.T) {
	f, err := setupEnvApp(t)
	if err != nil {
		t.Fatal(err)
	}

	hash, _ := auth.HashPassword("pass")
	f.store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: hash, Role: "developer"})
	viewer, _ := f.store.GetUserByUsername("viewer")
	viewerToken, _ := auth.IssueJWT(viewer.ID, "viewer", "developer", "test-secret")

	// Set the app to shared so the viewer can at least see it, but has no manage rights
	f.store.SetAppAccess("demo", "shared")

	// Seed a var so the attempt is meaningful
	f.store.UpsertAppEnvVar(f.app.ID, "FOO", []byte("bar"), false)

	req := authedRequest(t, "DELETE", "/api/apps/demo/env/FOO", nil, viewerToken)
	rec := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("want 403 for viewer, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestDeleteAppEnv_AuditLogged(t *testing.T) {
	f, err := setupEnvApp(t)
	if err != nil {
		t.Fatal(err)
	}

	if err := f.store.UpsertAppEnvVar(f.app.ID, "AWS_REGION", []byte("eu-west-1"), false); err != nil {
		t.Fatalf("seed var: %v", err)
	}

	req := authedRequest(t, "DELETE", "/api/apps/demo/env/AWS_REGION", nil, f.ownerToken)
	rec := httptest.NewRecorder()
	f.srv.Router().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d: %s", rec.Code, rec.Body.String())
	}

	events, err := f.store.ListAuditEvents(10, 0)
	if err != nil {
		t.Fatalf("ListAuditEvents: %v", err)
	}

	var found bool
	for _, e := range events {
		if e.Action == "env.delete" && e.ResourceType == "app" && e.ResourceID == "demo" {
			found = true
			if !strings.Contains(e.Detail, `"key":"AWS_REGION"`) {
				t.Errorf("audit detail missing key: %s", e.Detail)
			}
			// Detail must NOT contain the value
			if strings.Contains(e.Detail, "eu-west-1") {
				t.Errorf("audit detail must not contain the value, got: %s", e.Detail)
			}
		}
	}
	if !found {
		t.Errorf("no env.delete audit event found; events: %+v", events)
	}
}

// TestDeleteAppEnv_RestartTrue_RestartsRunningApp is skipped because the
// process.Manager does not expose a Restart method and the test server is
// constructed without a manager. Restarting requires Stop + deploy.Run with
// the current deployment's bundle dir, which cannot be exercised without a
// real runtime. This is tracked for a future task.
func TestDeleteAppEnv_RestartTrue_RestartsRunningApp(t *testing.T) {
	t.Skip("manager.Restart not yet implemented; restart via ?restart=true returns restarted:false when no manager is present")
}
