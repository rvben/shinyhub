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

// newIconTestApp creates the owner and the app. CreateApp takes a real
// OwnerID: the column is a foreign key, so a hardcoded 1 fails. Access is
// omitted deliberately, matching TestAppIconRoundTrip; the column DEFAULTs to
// 'private'.
func newIconTestApp(t *testing.T) *db.Store {
	t.Helper()
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
	return store
}

// TestAppIconEmojiColumn pins that icon_emoji round-trips through the shared
// appColumns/scanApp pair AND that icon_mime still lands in IconMime. The
// second assertion is the one that catches a misaligned insertion: both are
// strings, so a swapped pair scans cleanly and only shows up as an emoji in a
// Content-Type header at runtime.
func TestAppIconEmojiColumn(t *testing.T) {
	store := newIconTestApp(t)
	app, err := store.GetAppBySlug("dash")
	if err != nil {
		t.Fatalf("get app: %v", err)
	}
	if app.IconEmoji != "" {
		t.Errorf("new app icon_emoji = %q, want empty", app.IconEmoji)
	}
	if err := store.SetAppIcon("dash", "image/png", []byte("PNG")); err != nil {
		t.Fatalf("set icon: %v", err)
	}
	app, _ = store.GetAppBySlug("dash")
	if app.IconMime != "image/png" {
		t.Errorf("icon_mime = %q, want image/png (a swapped column list scans clean but lands here)", app.IconMime)
	}
}

// TestSetAppIconEmojiNonDestructive pins that clearing the emoji keeps the
// image. An implementation that routes the clear through the exclusive setter
// passes every other test in this file while silently destroying the bytes.
func TestSetAppIconEmojiNonDestructive(t *testing.T) {
	store := newIconTestApp(t)
	if err := store.SetAppIcon("dash", "image/png", []byte("PNG")); err != nil {
		t.Fatalf("set icon: %v", err)
	}
	if err := store.SetAppIconEmoji("dash", ""); err != nil {
		t.Fatalf("clear emoji: %v", err)
	}
	mime, data, err := store.GetAppIcon("dash")
	if err != nil {
		t.Fatalf("get icon after emoji clear: %v", err)
	}
	if mime != "image/png" || string(data) != "PNG" {
		t.Errorf("image after emoji clear = %q/%q, want image/png/PNG", mime, string(data))
	}
}

// TestSetAppIconEmojiExclusiveRejectsEmpty makes the destructive setter
// structurally unreachable for a clear, rather than trusting call sites.
func TestSetAppIconEmojiExclusiveRejectsEmpty(t *testing.T) {
	store := newIconTestApp(t)
	if err := store.SetAppIcon("dash", "image/png", []byte("PNG")); err != nil {
		t.Fatalf("set icon: %v", err)
	}
	if err := store.SetAppIconEmojiExclusive("dash", ""); err == nil {
		t.Fatal("SetAppIconEmojiExclusive(\"\") = nil, want error")
	}
	if mime, _, _ := store.GetAppIcon("dash"); mime != "image/png" {
		t.Errorf("image destroyed by rejected call: mime = %q", mime)
	}
}

// TestIconMutualExclusion pins both user-initiated directions.
func TestIconMutualExclusion(t *testing.T) {
	store := newIconTestApp(t)
	if err := store.SetAppIconEmojiExclusive("dash", "\U0001F4CA"); err != nil {
		t.Fatalf("set emoji: %v", err)
	}
	if err := store.SetAppIcon("dash", "image/png", []byte("PNG")); err != nil {
		t.Fatalf("set icon: %v", err)
	}
	app, _ := store.GetAppBySlug("dash")
	if app.IconEmoji != "" {
		t.Errorf("upload left icon_emoji = %q, want cleared", app.IconEmoji)
	}
	if err := store.SetAppIconEmojiExclusive("dash", "\U0001F4CA"); err != nil {
		t.Fatalf("set emoji again: %v", err)
	}
	if _, _, err := store.GetAppIcon("dash"); err != db.ErrNotFound {
		t.Errorf("exclusive emoji left image: err = %v, want ErrNotFound", err)
	}
	// Remove-the-icon is the destructive one: it clears both.
	if err := store.SetAppIcon("dash", "image/png", []byte("PNG")); err != nil {
		t.Fatalf("re-set icon: %v", err)
	}
	if err := store.SetAppIconEmoji("dash", "\U0001F4CA"); err != nil {
		t.Fatalf("set emoji non-exclusively: %v", err)
	}
	if err := store.ClearAppIcon("dash"); err != nil {
		t.Fatalf("clear icon: %v", err)
	}
	app, _ = store.GetAppBySlug("dash")
	if app.IconEmoji != "" {
		t.Errorf("ClearAppIcon left icon_emoji = %q, want cleared", app.IconEmoji)
	}
	if _, _, err := store.GetAppIcon("dash"); err != db.ErrNotFound {
		t.Errorf("ClearAppIcon left image: err = %v, want ErrNotFound", err)
	}
}
