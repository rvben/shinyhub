package db_test

import (
	"errors"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestAppLaunchCodeIsBoundAndSingleUse(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "alice", PasswordHash: "!disabled", Role: "developer"}); err != nil {
		t.Fatal(err)
	}
	user, err := store.GetUserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "sales", Name: "Sales", OwnerID: user.ID}); err != nil {
		t.Fatal(err)
	}

	if err := store.CreateAppLaunchCode("hash-one", user.ID, "sales"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeAppLaunchCode("hash-one", "another-app"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("wrong-slug consume error = %v, want ErrNotFound", err)
	}
	got, err := store.ConsumeAppLaunchCode("hash-one", "sales")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != user.ID || got.Username != user.Username {
		t.Fatalf("consumed user = %#v, want %#v", got, user)
	}
	if _, err := store.ConsumeAppLaunchCode("hash-one", "sales"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("replay error = %v, want ErrNotFound", err)
	}
}

func TestAppLaunchPreservesBrowserSessionAndRejectsRevocation(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "browser", PasswordHash: "!disabled", Role: "viewer"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("browser")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "session-app", Name: "Session", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}
	original := &auth.TokenInfo{JTI: "browser-family", AuthTime: time.Now().Add(-time.Hour).Truncate(time.Second)}
	create := func(code string) {
		t.Helper()
		if err := store.CreateAppLaunchCodeWithSession(code, u.ID, "session-app", original, u.TokenEpoch); err != nil {
			t.Fatal(err)
		}
	}
	create("preserved")
	got, ti, err := store.ConsumeAppLaunchCodeWithSession("preserved", "session-app")
	if err != nil || got.ID != u.ID || ti.JTI != original.JTI || !ti.AuthTime.Equal(original.AuthTime) {
		t.Fatalf("launch changed browser identity: user=%+v session=%+v err=%v", got, ti, err)
	}
	create("logout")
	if err := store.RevokeToken(original.JTI, u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ConsumeAppLaunchCodeWithSession("logout", "session-app"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("pending launch survived logout: %v", err)
	}
	original.JTI = "different-family"
	create("epoch")
	if err := store.BumpTokenEpoch(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.ConsumeAppLaunchCodeWithSession("epoch", "session-app"); !errors.Is(err, db.ErrNotFound) {
		t.Fatalf("pending launch survived session revocation: %v", err)
	}
}
