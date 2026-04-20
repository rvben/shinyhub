package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
