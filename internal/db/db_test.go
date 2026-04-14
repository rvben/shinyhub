package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
)

func TestOpenAndMigrate(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
}

func TestCreateAndGetUser(t *testing.T) {
	store := mustOpenDB(t)
	err := store.CreateUser(db.CreateUserParams{
		Username:     "alice",
		PasswordHash: "hash",
		Role:         "admin",
	})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, err := store.GetUserByUsername("alice")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if u.Username != "alice" || u.Role != "admin" {
		t.Errorf("unexpected user: %+v", u)
	}
}

func TestCreateAndGetApp(t *testing.T) {
	store := mustOpenDB(t)
	// Create the owning user first (FK requires it)
	if err := store.CreateUser(db.CreateUserParams{
		Username:     "owner",
		PasswordHash: "hash",
		Role:         "developer",
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	owner, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get owner: %v", err)
	}
	if err := store.CreateApp(db.CreateAppParams{
		Slug:        "my-app",
		Name:        "My App",
		ProjectSlug: "default",
		OwnerID:     owner.ID,
	}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, err := store.GetAppBySlug("my-app")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Slug != "my-app" {
		t.Errorf("expected slug my-app, got %s", app.Slug)
	}
}

func mustOpenDB(t *testing.T) *db.Store {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestMigrate_HibernateTimeoutColumn(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if err := store.CreateUser(db.CreateUserParams{Username: "u", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUserByUsername("u")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "myapp", Name: "My App", OwnerID: u.ID}); err != nil {
		t.Fatal(err)
	}

	mins := 45
	if err := store.UpdateHibernateTimeout("myapp", &mins); err != nil {
		t.Fatalf("UpdateHibernateTimeout: %v", err)
	}
	app, err := store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if app.HibernateTimeoutMinutes == nil || *app.HibernateTimeoutMinutes != 45 {
		t.Errorf("expected HibernateTimeoutMinutes=45, got %v", app.HibernateTimeoutMinutes)
	}

	// Reset to NULL (global default).
	if err := store.UpdateHibernateTimeout("myapp", nil); err != nil {
		t.Fatalf("UpdateHibernateTimeout nil: %v", err)
	}
	app, err = store.GetAppBySlug("myapp")
	if err != nil {
		t.Fatal(err)
	}
	if app.HibernateTimeoutMinutes != nil {
		t.Errorf("expected nil after reset, got %v", app.HibernateTimeoutMinutes)
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.Migrate(); err != nil {
		t.Fatalf("first Migrate: %v", err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatalf("second Migrate must be idempotent: %v", err)
	}
}
