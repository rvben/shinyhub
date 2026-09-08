package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// seedAuditActor creates an admin and returns a JWT for it.
func seedAuditActor(t *testing.T, store *db.Store) (int64, string) {
	t.Helper()
	hash, err := testHashPassword("pass")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if err := store.CreateUser(db.CreateUserParams{Username: "auditor", PasswordHash: hash, Role: "admin"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, err := store.GetUserByUsername("auditor")
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	token, err := auth.IssueJWT(u.ID, u.Username, u.Role, "test-secret")
	if err != nil {
		t.Fatalf("issue jwt: %v", err)
	}
	return u.ID, token
}

func serveAudited(t *testing.T, srv *api.Server, method, path string, body []byte, token string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, authedRequest(t, method, path, body, token))
	return rec
}

// TestCreateApp_AuditDetailDescribesTheApp pins that the create event records
// the shape the app was created with. Without it the trail says only that a
// slug appeared, and the access level it was born with - the fact that decides
// whether the app was reachable by anyone - is unrecoverable once it is later
// changed.
func TestCreateApp_AuditDetailDescribesTheApp(t *testing.T) {
	srv, store, _ := newLimitEnforcementServer(t)
	ownerID, token := seedAuditActor(t, store)

	rec := serveAudited(t, srv, "POST", "/api/apps", []byte(`{"slug":"audited","name":"Audited App"}`), token)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create app: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	detail := latestAuditDetail(t, store, "create_app")
	if got, _ := detail["name"].(string); got != "Audited App" {
		t.Errorf("detail.name = %q, want %q", got, "Audited App")
	}
	if got, _ := detail["access"].(string); got == "" {
		t.Error("detail.access must record the access level the app was created with")
	}
	if got, _ := detail["owner_id"].(float64); int64(got) != ownerID {
		t.Errorf("detail.owner_id = %v, want %d", detail["owner_id"], ownerID)
	}
}

// TestStopApp_AuditDetailRecordsThePreviousStatus pins the fact that only
// exists before the stop runs. The app's status afterwards is "stopped" for
// every stop ever recorded, so an event that captured the post-state would
// carry no information; what an incident reviewer needs to know is whether this
// stop took a running app down or was a no-op against one already stopped.
func TestStopApp_AuditDetailRecordsThePreviousStatus(t *testing.T) {
	srv, store, _ := newLimitEnforcementServer(t)
	_, token := seedAuditActor(t, store)
	u, _ := store.GetUserByUsername("auditor")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "stoppable", Name: "Stoppable", OwnerID: u.ID}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	// The app must be running before the stop, or the test cannot tell a
	// pre-state capture from a post-state one: a freshly created app is already
	// "stopped", so both readings agree and the assertion proves nothing.
	if err := store.UpdateAppStatus(db.UpdateAppStatusParams{Slug: "stoppable", Status: "running"}); err != nil {
		t.Fatalf("mark app running: %v", err)
	}
	before, err := store.GetAppBySlug("stoppable")
	if err != nil {
		t.Fatalf("load app: %v", err)
	}
	if before.Status != "running" {
		t.Fatalf("setup failed: app status is %q, so a pre-state capture is indistinguishable from a post-state one", before.Status)
	}

	rec := serveAudited(t, srv, "POST", "/api/apps/stoppable/stop", nil, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop app: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	detail := latestAuditDetail(t, store, "stop")
	got, _ := detail["previous_status"].(string)
	if got != before.Status {
		t.Errorf("detail.previous_status = %q, want the pre-stop status %q", got, before.Status)
	}
	// The trap this guards: reading the status after the handler has already
	// written "stopped" would satisfy a laxer assertion while recording nothing.
	if before.Status != "stopped" && got == "stopped" {
		t.Errorf("detail.previous_status recorded the post-stop status instead of the pre-stop one (%q)", before.Status)
	}
}
