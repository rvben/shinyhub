package db_test

import (
	"bytes"
	"testing"

	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

// TestAppIconRoundTrip verifies that an app has no icon by default, that
// SetAppIcon persists the bytes + MIME (and surfaces icon_mime on the app row
// while keeping the bytes off it), that GetAppIcon reads them back, and that
// ClearAppIcon reverts to the iconless state. Missing apps yield ErrNotFound.
func TestAppIconRoundTrip(t *testing.T) {
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

	// Default: no icon. The app row carries an empty MIME, and GetAppIcon is a
	// not-found (the serve handler 404s, the UI shows the monogram).
	app, err := store.GetAppBySlug("dash")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.IconMime != "" {
		t.Errorf("new app icon_mime = %q, want empty", app.IconMime)
	}
	if _, _, err := store.GetAppIcon("dash"); err != db.ErrNotFound {
		t.Errorf("GetAppIcon on iconless app = %v, want ErrNotFound", err)
	}

	// A tiny PNG header is enough to prove byte fidelity.
	pngBytes := []byte("\x89PNG\r\n\x1a\n\x00\x01\x02\x03binary\xff\xfe")
	if err := store.SetAppIcon("dash", "image/png", pngBytes); err != nil {
		t.Fatalf("set icon: %v", err)
	}
	app, _ = store.GetAppBySlug("dash")
	if app.IconMime != "image/png" {
		t.Errorf("icon_mime = %q, want image/png", app.IconMime)
	}
	mime, data, err := store.GetAppIcon("dash")
	if err != nil {
		t.Fatalf("get icon: %v", err)
	}
	if mime != "image/png" {
		t.Errorf("served mime = %q, want image/png", mime)
	}
	if !bytes.Equal(data, pngBytes) {
		t.Errorf("served bytes = %v, want %v (icon bytes must round-trip intact)", data, pngBytes)
	}

	// Replacing the icon overwrites both bytes and MIME.
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)
	if err := store.SetAppIcon("dash", "image/svg+xml", svg); err != nil {
		t.Fatalf("replace icon: %v", err)
	}
	mime, data, _ = store.GetAppIcon("dash")
	if mime != "image/svg+xml" || !bytes.Equal(data, svg) {
		t.Errorf("after replace: mime=%q bytes=%v, want svg", mime, data)
	}

	// Clearing reverts to the iconless state.
	if err := store.ClearAppIcon("dash"); err != nil {
		t.Fatalf("clear icon: %v", err)
	}
	app, _ = store.GetAppBySlug("dash")
	if app.IconMime != "" {
		t.Errorf("after clear, icon_mime = %q, want empty", app.IconMime)
	}
	if _, _, err := store.GetAppIcon("dash"); err != db.ErrNotFound {
		t.Errorf("GetAppIcon after clear = %v, want ErrNotFound", err)
	}

	// Missing apps are ErrNotFound on every entry point.
	if err := store.SetAppIcon("nope", "image/png", pngBytes); err != db.ErrNotFound {
		t.Errorf("SetAppIcon missing = %v, want ErrNotFound", err)
	}
	if err := store.ClearAppIcon("nope"); err != db.ErrNotFound {
		t.Errorf("ClearAppIcon missing = %v, want ErrNotFound", err)
	}
	if _, _, err := store.GetAppIcon("nope"); err != db.ErrNotFound {
		t.Errorf("GetAppIcon missing = %v, want ErrNotFound", err)
	}
}
