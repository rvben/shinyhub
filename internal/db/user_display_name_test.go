package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

// TestUserDisplayName_SelfEditPersists pins the local self-service edit: an
// explicit edit persists across all three user readers, and updating a missing
// user is reported as not-found.
func TestUserDisplayName_SelfEditPersists(t *testing.T) {
	store := mustOpenDB(t)

	if err := store.CreateUser(db.CreateUserParams{
		Username: "alice", PasswordHash: "$2y$10$hash", Role: "admin",
	}); err != nil {
		t.Fatalf("create alice: %v", err)
	}
	alice, _ := store.GetUserByUsername("alice")
	if alice.DisplayName != "" {
		t.Errorf("new user display name = %q, want empty", alice.DisplayName)
	}

	if err := store.UpdateUserDisplayName(alice.ID, "Alice Liddell"); err != nil {
		t.Fatalf("update display name: %v", err)
	}
	if got, _ := store.GetUserByUsername("alice"); got.DisplayName != "Alice Liddell" {
		t.Errorf("GetUserByUsername display name = %q, want %q", got.DisplayName, "Alice Liddell")
	}
	if got, _ := store.GetUserByID(alice.ID); got.DisplayName != "Alice Liddell" {
		t.Errorf("GetUserByID display name = %q, want %q", got.DisplayName, "Alice Liddell")
	}
	users, _ := store.ListUsers()
	for _, u := range users {
		if u.Username == "alice" && u.DisplayName != "Alice Liddell" {
			t.Errorf("ListUsers display name = %q, want %q", u.DisplayName, "Alice Liddell")
		}
	}

	if err := store.UpdateUserDisplayName(999999, "Ghost"); err == nil {
		t.Error("UpdateUserDisplayName on missing user: want error, got nil")
	}
}

// TestSetDisplayNameFromIdP pins the IdP-authoritative refresh: SSO accounts
// (no local bcrypt password) are set AND refreshed from the IdP, a blank name is
// a no-op so a missing claim never blanks a good name, and a local account's
// self-set name is never overwritten by an SSO login.
func TestSetDisplayNameFromIdP(t *testing.T) {
	store := mustOpenDB(t)

	// SSO account: empty password hash.
	if err := store.CreateUser(db.CreateUserParams{
		Username: "sso", PasswordHash: "", Role: "viewer",
	}); err != nil {
		t.Fatalf("create sso: %v", err)
	}
	sso, _ := store.GetUserByUsername("sso")

	// First login sets the name; a blank claim is a no-op; a later login with a
	// changed upstream name REFRESHES it (authoritative).
	if err := store.SetDisplayNameFromIdP(sso.ID, "Sam Vimes"); err != nil {
		t.Fatalf("set from idp: %v", err)
	}
	if got, _ := store.GetUserByID(sso.ID); got.DisplayName != "Sam Vimes" {
		t.Errorf("idp set = %q, want %q", got.DisplayName, "Sam Vimes")
	}
	if err := store.SetDisplayNameFromIdP(sso.ID, "   "); err != nil {
		t.Fatalf("set blank: %v", err)
	}
	if got, _ := store.GetUserByID(sso.ID); got.DisplayName != "Sam Vimes" {
		t.Errorf("blank claim changed name to %q, want %q (unchanged)", got.DisplayName, "Sam Vimes")
	}
	if err := store.SetDisplayNameFromIdP(sso.ID, "Samuel Vimes"); err != nil {
		t.Fatalf("refresh from idp: %v", err)
	}
	if got, _ := store.GetUserByID(sso.ID); got.DisplayName != "Samuel Vimes" {
		t.Errorf("idp refresh = %q, want %q", got.DisplayName, "Samuel Vimes")
	}

	// Local account: has a bcrypt password and a self-set name. An SSO login
	// (e.g. a linked provider) must NOT overwrite it.
	if err := store.CreateUser(db.CreateUserParams{
		Username: "local", PasswordHash: "$2y$10$hash", Role: "developer",
	}); err != nil {
		t.Fatalf("create local: %v", err)
	}
	local, _ := store.GetUserByUsername("local")
	if err := store.UpdateUserDisplayName(local.ID, "Preferred Name"); err != nil {
		t.Fatalf("self edit: %v", err)
	}
	if err := store.SetDisplayNameFromIdP(local.ID, "HR Legal Name"); err != nil {
		t.Fatalf("idp on local: %v", err)
	}
	if got, _ := store.GetUserByID(local.ID); got.DisplayName != "Preferred Name" {
		t.Errorf("idp overwrote a local account's self-set name: got %q", got.DisplayName)
	}
}

func TestHasLocalPassword(t *testing.T) {
	cases := map[string]bool{
		"$2a$10$abcdefghijklmnopqrstuv": true,
		"$2y$10$abc":                    true,
		"$2b$12$xyz":                    true,
		"":                              false, // OAuth/OIDC account
		"!disabled":                     false, // forward-auth / system account
		"plaintext":                     false,
	}
	for hash, want := range cases {
		if got := db.HasLocalPassword(hash); got != want {
			t.Errorf("HasLocalPassword(%q) = %v, want %v", hash, got, want)
		}
	}
}
