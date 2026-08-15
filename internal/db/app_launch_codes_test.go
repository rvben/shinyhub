package db_test

import (
	"errors"
	"testing"

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
