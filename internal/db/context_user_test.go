package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestLookupContextUser_CarriesAllIdentityFields pins the single canonical
// mapper every session-resolution path uses. The /app/* proxy path and the
// /api/* path both resolve the request user through this, so it MUST copy every
// identity field the proxy forwards to apps (id, username, role, email, display
// name). A field dropped here silently blanks X-Shinyhub-Email / X-Shinyhub-Name
// for every native session, which is exactly the regression this guards.
func TestLookupContextUser_CarriesAllIdentityFields(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "sso", PasswordHash: "", Role: "developer"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	u, err := store.GetUserByUsername("sso")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := store.SetDisplayNameFromIdP(u.ID, "Ana Smith"); err != nil {
		t.Fatalf("display name: %v", err)
	}
	if err := store.SetEmailFromIdP(u.ID, "ana@corp.example"); err != nil {
		t.Fatalf("email: %v", err)
	}

	cu, err := store.LookupContextUser(u.ID)
	if err != nil {
		t.Fatalf("LookupContextUser: %v", err)
	}
	if cu.ID != u.ID || cu.Username != "sso" || cu.Role != "developer" {
		t.Fatalf("core fields = %+v", cu)
	}
	if cu.DisplayName != "Ana Smith" {
		t.Errorf("DisplayName = %q, want %q (the /app proxy path forwards this as X-Shinyhub-Name)", cu.DisplayName, "Ana Smith")
	}
	if cu.Email != "ana@corp.example" {
		t.Errorf("Email = %q, want %q (forwarded as X-Shinyhub-Email)", cu.Email, "ana@corp.example")
	}
}
