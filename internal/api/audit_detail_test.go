package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestPatchUser_AuditRecordsOldAndNewRole(t *testing.T) {
	srv, store := newTestServer(t)
	token, _ := seedUserAndJWT(t, store, "admin", "admin")
	if err := store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: "x", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	bob, _ := store.GetUserByUsername("bob")

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, adminReq(t, http.MethodPatch, fmt.Sprintf("/api/users/%d", bob.ID), map[string]string{"role": "operator"}, token))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch role: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	events, err := store.ListAuditEvents("update_user", 10, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("no update_user audit event: %v (n=%d)", err, len(events))
	}
	d := events[0].Detail
	if !strings.Contains(d, "developer") || !strings.Contains(d, "operator") {
		t.Errorf("update_user audit Detail must record old (developer) and new (operator) role; got %q", d)
	}
}

func TestSetAppAccess_AuditRecordsFromAndTo(t *testing.T) {
	srv, store := newTestServer(t)
	token, adminID := seedUserAndJWT(t, store, "admin", "admin")
	if err := store.CreateApp(db.CreateAppParams{Slug: "dash", Name: "Dash", OwnerID: adminID}); err != nil {
		t.Fatal(err)
	}
	// Establish a known starting visibility (the create-handler defaults to this
	// in production; a direct store.CreateApp leaves it empty).
	if err := store.SetAppAccess("dash", "private"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, adminReq(t, http.MethodPatch, "/api/apps/dash/access", map[string]string{"access": "public"}, token))
	if rec.Code != http.StatusOK {
		t.Fatalf("set access: want 200, got %d: %s", rec.Code, rec.Body.String())
	}

	events, err := store.ListAuditEvents("set_access", 10, 0)
	if err != nil || len(events) == 0 {
		t.Fatalf("no set_access audit event: %v (n=%d)", err, len(events))
	}
	d := events[0].Detail
	if !strings.Contains(d, "private") || !strings.Contains(d, "public") {
		t.Errorf("set_access audit Detail must record from (private) and to (public); got %q", d)
	}
}
