package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestSetEmailFromIdP pins email as an IdP-authoritative field, mirroring the
// display-name contract: an SSO account (no local bcrypt password) has its email
// set and refreshed from the IdP on each login, a blank email is a no-op so a
// login that omits the email claim never blanks a stored one, and a local
// account's row is never touched by an SSO login. The persisted email is
// readable through every user reader (that is what feeds X-Shinyhub-Email for
// native session users, not just forward-auth requests).
func TestSetEmailFromIdP(t *testing.T) {
	store := mustOpenDB(t)

	// SSO account: empty password hash.
	if err := store.CreateUser(db.CreateUserParams{
		Username: "sso", PasswordHash: "", Role: "viewer",
	}); err != nil {
		t.Fatalf("create sso: %v", err)
	}
	sso, _ := store.GetUserByUsername("sso")
	if sso.Email != "" {
		t.Errorf("new user email = %q, want empty", sso.Email)
	}

	// First login sets it; a blank claim is a no-op; a later login with a
	// changed upstream address REFRESHES it (authoritative).
	if err := store.SetEmailFromIdP(sso.ID, "sam@discworld.example"); err != nil {
		t.Fatalf("set from idp: %v", err)
	}
	if got, _ := store.GetUserByID(sso.ID); got.Email != "sam@discworld.example" {
		t.Errorf("idp set = %q, want %q", got.Email, "sam@discworld.example")
	}
	if err := store.SetEmailFromIdP(sso.ID, "   "); err != nil {
		t.Fatalf("set blank: %v", err)
	}
	if got, _ := store.GetUserByID(sso.ID); got.Email != "sam@discworld.example" {
		t.Errorf("blank claim changed email to %q, want unchanged", got.Email)
	}
	if err := store.SetEmailFromIdP(sso.ID, "samuel@watch.example"); err != nil {
		t.Fatalf("refresh from idp: %v", err)
	}
	if got, _ := store.GetUserByID(sso.ID); got.Email != "samuel@watch.example" {
		t.Errorf("idp refresh = %q, want %q", got.Email, "samuel@watch.example")
	}

	// The refreshed email is visible through the username reader and the list.
	if got, _ := store.GetUserByUsername("sso"); got.Email != "samuel@watch.example" {
		t.Errorf("GetUserByUsername email = %q, want %q", got.Email, "samuel@watch.example")
	}
	users, _ := store.ListUsers()
	for _, u := range users {
		if u.Username == "sso" && u.Email != "samuel@watch.example" {
			t.Errorf("ListUsers email = %q, want %q", u.Email, "samuel@watch.example")
		}
	}

	// Local account: has a bcrypt password. An SSO login (e.g. a linked provider)
	// must NOT drive identity fields on a locally-managed account.
	if err := store.CreateUser(db.CreateUserParams{
		Username: "local", PasswordHash: "$2y$10$hash", Role: "developer",
	}); err != nil {
		t.Fatalf("create local: %v", err)
	}
	local, _ := store.GetUserByUsername("local")
	if err := store.SetEmailFromIdP(local.ID, "hr@corp.example"); err != nil {
		t.Fatalf("idp on local: %v", err)
	}
	if got, _ := store.GetUserByID(local.ID); got.Email != "" {
		t.Errorf("idp set email on a local account: got %q, want empty", got.Email)
	}
}
