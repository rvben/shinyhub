package db_test

import (
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestAppDescriptionRoundTrip verifies the optional description column defaults
// to empty, persists through SetAppDescription, reads back via GetAppBySlug, and
// clears on an empty string.
func TestAppDescriptionRoundTrip(t *testing.T) {
	store := dbtest.New(t)
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "x", Role: "developer"}); err != nil {
		t.Fatalf("create user: %v", err)
	}
	u, err := store.GetUserByUsername("owner")
	if err != nil {
		t.Fatalf("get user: %v", err)
	}
	if err := store.CreateApp(db.CreateAppParams{Slug: "dash", Name: "Dash", OwnerID: u.ID}); err != nil {
		t.Fatalf("create app: %v", err)
	}

	app, err := store.GetAppBySlug("dash")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.Description != "" {
		t.Errorf("new app description = %q, want empty", app.Description)
	}

	if err := store.SetAppDescription("dash", "Q3 revenue by region"); err != nil {
		t.Fatalf("set description: %v", err)
	}
	app, _ = store.GetAppBySlug("dash")
	if app.Description != "Q3 revenue by region" {
		t.Errorf("description = %q, want %q", app.Description, "Q3 revenue by region")
	}

	if err := store.SetAppDescription("dash", ""); err != nil {
		t.Fatalf("clear description: %v", err)
	}
	app, _ = store.GetAppBySlug("dash")
	if app.Description != "" {
		t.Errorf("cleared description = %q, want empty", app.Description)
	}

	if err := store.SetAppDescription("nope", "x"); err != db.ErrNotFound {
		t.Errorf("SetAppDescription on missing app = %v, want ErrNotFound", err)
	}
}
