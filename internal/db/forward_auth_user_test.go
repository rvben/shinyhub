package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestGetForwardAuthUser_CarriesDisplayNameAndEmail pins that the forward-auth
// user resolver routes through the canonical User.ContextUser mapper, so a
// persisted display name and email are carried as the base identity. The
// forward-auth middleware layers the proxy's per-request name/email headers on
// top (the IdP is authoritative), but when a header is absent the persisted DB
// value is the correct fallback - and both fields must reach the /app proxy as
// X-Shinyhub-Name / X-Shinyhub-Email. A resolver that dropped a field would
// blank it for forward-auth sessions the same way appUserLookup did for native
// ones.
func TestGetForwardAuthUser_CarriesDisplayNameAndEmail(t *testing.T) {
	store := dbtest.New(t)
	cu, err := store.CreateForwardAuthUser("fa", "developer")
	if err != nil {
		t.Fatalf("create forward-auth user: %v", err)
	}
	// A forward-auth account can carry an IdP-persisted name/email (e.g. from a
	// prior SSO login under the same username). Both apply to non-local accounts.
	if err := store.SetDisplayNameFromIdP(cu.ID, "Fa User"); err != nil {
		t.Fatalf("set display name: %v", err)
	}
	if err := store.SetEmailFromIdP(cu.ID, "fa@corp.example"); err != nil {
		t.Fatalf("set email: %v", err)
	}

	got, err := store.GetForwardAuthUser("fa")
	if err != nil {
		t.Fatalf("GetForwardAuthUser: %v", err)
	}
	if got.DisplayName != "Fa User" {
		t.Errorf("DisplayName = %q, want %q", got.DisplayName, "Fa User")
	}
	if got.Email != "fa@corp.example" {
		t.Errorf("Email = %q, want %q (forward-auth resolver must carry the persisted email fallback)", got.Email, "fa@corp.example")
	}
}

// TestCreateForwardAuthUser_ReturnsCanonicalContextUser pins that a freshly
// created forward-auth user resolves through the canonical mapper (all identity
// fields present, empty until an IdP sets them).
func TestCreateForwardAuthUser_ReturnsCanonicalContextUser(t *testing.T) {
	store := dbtest.New(t)
	cu, err := store.CreateForwardAuthUser("fresh", "viewer")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if cu.Username != "fresh" || cu.Role != "viewer" {
		t.Fatalf("core fields = %+v", cu)
	}
	if cu.DisplayName != "" || cu.Email != "" {
		t.Errorf("fresh forward-auth user should have empty name/email, got name=%q email=%q", cu.DisplayName, cu.Email)
	}
}
