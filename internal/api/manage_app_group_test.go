package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

func TestManageApp_ViaGroupManager(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "gm", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	gm, _ := store.GetUserByUsername("gm")
	store.CreateApp(db.CreateAppParams{Slug: "gapp", Name: "G App", OwnerID: owner.ID})
	store.ReplaceUserGroups(gm.ID, []string{"leads"})
	store.GrantAppGroupAccess("gapp", "leads", "manager", "manual")

	token, _ := auth.IssueJWT(gm.ID, "gm", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{})
	req := authedRequest(t, "PATCH", "/api/apps/gapp", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("group-manager: expected 200 on PATCH, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestManageApp_ViaGroupViewerForbidden(t *testing.T) {
	srv, store := newTestServer(t)
	hash, _ := auth.HashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "gv", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	gv, _ := store.GetUserByUsername("gv")
	store.CreateApp(db.CreateAppParams{Slug: "gapp2", Name: "G App 2", OwnerID: owner.ID})
	store.ReplaceUserGroups(gv.ID, []string{"viewers"})
	store.GrantAppGroupAccess("gapp2", "viewers", "viewer", "manual")

	token, _ := auth.IssueJWT(gv.ID, "gv", "developer", "test-secret")
	body, _ := json.Marshal(map[string]any{})
	req := authedRequest(t, "PATCH", "/api/apps/gapp2", body, token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("group-viewer: expected 403 on PATCH, got %d: %s", rec.Code, rec.Body.String())
	}
}
