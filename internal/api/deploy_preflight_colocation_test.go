package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// The preflight judges colocated shared data exactly as the deploy does: a
// consumer placed on a remote node whose shared source is pinned to the
// control plane is reported with the deploy's own 409 message, and the
// rehearsal records nothing. Without this stage the preflight answers "valid"
// for a deploy the server is about to refuse.
func TestDeployPreflight_ReportsColocatedConflictWithDeployMessage(t *testing.T) {
	appsDir := t.TempDir()
	srv, store := newQuotaTestServer(t, appsDir, 0)

	hash, _ := testHashPassword("pass")
	_ = store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})
	u, _ := store.GetUserByUsername("admin")
	_, _ = store.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: u.ID})
	stageColocationConflict(t, srv, store, u.ID, "demo")
	token, _ := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")

	// The deploy's verdict is the reference the preflight must reproduce.
	body, ctype := buildBundleUpload(t, "app.py", "print('hi')\n")
	req := httptest.NewRequest("POST", "/api/apps/demo/deploy", body)
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("deploy: want 409 colocated conflict, got %d: %s", rec.Code, rec.Body.String())
	}
	var deployErr struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &deployErr); err != nil || deployErr.Error == "" {
		t.Fatalf("deploy error body: %v (%s)", err, rec.Body.String())
	}

	pre := httptest.NewRequest("POST", "/api/apps/demo/deploy-preflight", strings.NewReader(`{"app_type":"python"}`))
	pre.Header.Set("Content-Type", "application/json")
	pre.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, pre)
	if rec.Code != http.StatusOK {
		t.Fatalf("preflight: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var reply struct {
		Valid    bool `json:"valid"`
		Problems []struct {
			Stage   string `json:"stage"`
			Message string `json:"message"`
		} `json:"problems"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &reply); err != nil {
		t.Fatalf("preflight body: %v (%s)", err, rec.Body.String())
	}
	if reply.Valid || len(reply.Problems) != 1 {
		t.Fatalf("preflight = %+v, want exactly one problem for a deploy the server refuses with 409", reply)
	}
	if got := reply.Problems[0]; got.Stage != "deploy" || got.Message != deployErr.Error {
		t.Fatalf("preflight problem = %+v, want stage deploy with the deploy's message %q", got, deployErr.Error)
	}
	mustNoInflight(t, store, "after preflight")
	app, _ := store.GetAppBySlug("demo")
	if has, err := store.HasAnyDeployment(app.ID); err != nil || has {
		t.Errorf("HasAnyDeployment after preflight = (%v, %v), want (false, nil)", has, err)
	}
}
