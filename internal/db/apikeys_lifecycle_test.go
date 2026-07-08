package db_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func mkKeyUser(t *testing.T, store *db.Store) int64 {
	t.Helper()
	if err := store.CreateUser(db.CreateUserParams{Username: "keyer", PasswordHash: "x", Role: "developer"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, err := store.GetUserByUsername("keyer")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	return u.ID
}

// TestAuthenticateAPIKey_ExpiryFiltered pins the expiry contract: an expired
// key authenticates nobody (same miss as a wrong key), a future-dated key and
// a never-expiring key both work.
func TestAuthenticateAPIKey_ExpiryFiltered(t *testing.T) {
	store := dbtest.New(t)
	uid := mkKeyUser(t, store)

	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	cases := []struct {
		name      string
		expiresAt *time.Time
		wantOK    bool
	}{
		{"expired", &past, false},
		{"future", &future, true},
		{"never", nil, true},
	}
	for _, tc := range cases {
		hash := "hash-" + tc.name
		if _, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{
			UserID: uid, KeyHash: hash, Name: tc.name, ExpiresAt: tc.expiresAt,
		}); err != nil {
			t.Fatalf("%s: create: %v", tc.name, err)
		}
		u, _, _, err := store.AuthenticateAPIKey(hash)
		if tc.wantOK {
			if err != nil {
				t.Errorf("%s: expected success, got %v", tc.name, err)
			} else if u.ID != uid {
				t.Errorf("%s: wrong user %d", tc.name, u.ID)
			}
		} else if !errors.Is(err, db.ErrNotFound) {
			t.Errorf("%s: expected ErrNotFound for expired key, got %v", tc.name, err)
		}
	}
}

// TestTouchAPIKey_RecordsLastUsed pins the last-used plumbing: fresh keys have
// no last_used_at, TouchAPIKey stamps it, and both Authenticate and ListAPIKeys
// read it back.
func TestTouchAPIKey_RecordsLastUsed(t *testing.T) {
	store := dbtest.New(t)
	uid := mkKeyUser(t, store)
	keyID, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{UserID: uid, KeyHash: "h1", Name: "ci"})
	if err != nil {
		t.Fatal(err)
	}

	_, gotID, lastUsed, err := store.AuthenticateAPIKey("h1")
	if err != nil {
		t.Fatal(err)
	}
	if gotID != keyID {
		t.Errorf("key id = %d, want %d", gotID, keyID)
	}
	if lastUsed != nil {
		t.Errorf("fresh key lastUsed = %v, want nil", lastUsed)
	}

	stamp := time.Now().UTC().Truncate(time.Second)
	if err := store.TouchAPIKey(keyID, stamp); err != nil {
		t.Fatal(err)
	}
	_, _, lastUsed, err = store.AuthenticateAPIKey("h1")
	if err != nil {
		t.Fatal(err)
	}
	if lastUsed == nil || lastUsed.Unix() != stamp.Unix() {
		t.Errorf("lastUsed = %v, want %v", lastUsed, stamp)
	}

	keys, err := store.ListAPIKeys(uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].LastUsedAt == nil {
		t.Errorf("ListAPIKeys should carry last_used_at, got %+v", keys)
	}
}

// TestListAllAPIKeys_AdminInventory pins the cross-user inventory: every key
// with its owning username, so admins can revoke without audit archaeology.
func TestListAllAPIKeys_AdminInventory(t *testing.T) {
	store := dbtest.New(t)
	uid := mkKeyUser(t, store)
	if err := store.CreateUser(db.CreateUserParams{Username: "other", PasswordHash: "x", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	other, err := store.GetUserByUsername("other")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{UserID: uid, KeyHash: "ha", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateAPIKey(db.CreateAPIKeyParams{UserID: other.ID, KeyHash: "hb", Name: "b"}); err != nil {
		t.Fatal(err)
	}

	keys, err := store.ListAllAPIKeys()
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Fatalf("inventory size = %d, want 2", len(keys))
	}
	byName := map[string]string{}
	for _, k := range keys {
		byName[k.Name] = k.Username
	}
	if byName["a"] != "keyer" || byName["b"] != "other" {
		t.Errorf("inventory usernames = %v", byName)
	}
}
