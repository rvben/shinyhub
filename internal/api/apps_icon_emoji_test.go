package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

// doPatch sends a PATCH /api/apps/{slug} request as the given bearer token,
// asserts the response status, and decodes the body into a generic map for
// field-level assertions (e.g. body["app"].(map[string]any)["icon_emoji"]).
func doPatch(t *testing.T, srv *api.Server, path, body, token string, wantStatus int) map[string]any {
	t.Helper()
	req := authedRequest(t, "PATCH", path, []byte(body), token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("PATCH %s: expected %d, got %d: %s", path, wantStatus, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
			t.Fatalf("decode response: %v (body=%q)", err, rec.Body.String())
		}
	}
	return out
}

// Setting an emoji through PATCH discards the image (explicit user intent),
// while clearing it preserves the image so the display falls back to it.
func TestPatchAppIconEmoji(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "dash", Name: "Dash", OwnerID: owner.ID, Access: "public"})
	if err := store.SetAppIcon("dash", "image/png", []byte("PNG")); err != nil {
		t.Fatalf("seed icon: %v", err)
	}
	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")

	// Non-empty: exclusive. Image is discarded, emoji lands in the envelope.
	body := doPatch(t, srv, "/api/apps/dash", `{"icon_emoji":"📊"}`, token, http.StatusOK)
	if got := body["app"].(map[string]any)["icon_emoji"]; got != "\U0001F4CA" {
		t.Errorf("body.app.icon_emoji = %v, want the emoji", got)
	}
	if _, _, err := store.GetAppIcon("dash"); err != db.ErrNotFound {
		t.Errorf("image survived an exclusive emoji set: %v", err)
	}

	// Clear: non-destructive. Re-upload, then clear, and the image must remain.
	if err := store.SetAppIcon("dash", "image/png", []byte("PNG")); err != nil {
		t.Fatalf("re-set icon: %v", err)
	}
	if err := store.SetAppIconEmoji("dash", "\U0001F4CA"); err != nil {
		t.Fatalf("seed emoji: %v", err)
	}
	doPatch(t, srv, "/api/apps/dash", `{"icon_emoji":""}`, token, http.StatusOK)
	if mime, _, err := store.GetAppIcon("dash"); err != nil || mime != "image/png" {
		t.Errorf("clearing the emoji destroyed the image: mime=%q err=%v", mime, err)
	}

	// Invalid input is 400 and writes nothing.
	doPatch(t, srv, "/api/apps/dash", `{"icon_emoji":"AB"}`, token, http.StatusBadRequest)
	app, _ := store.GetAppBySlug("dash")
	if app.IconEmoji != "" {
		t.Errorf("rejected value was written: %q", app.IconEmoji)
	}
}

// A viewer may not set the icon.
func TestPatchAppIconEmojiForbiddenForViewer(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: hash, Role: "developer"})
	viewer, _ := store.GetUserByUsername("viewer")
	store.CreateApp(db.CreateAppParams{Slug: "dash2", Name: "Dash2", OwnerID: owner.ID, Access: "public"})
	store.GrantAppAccess("dash2", viewer.ID)
	// viewer has the default member role="viewer" - no explicit SetMemberRole needed
	// (mirrors TestViewerMember_CannotManage).

	token, _ := auth.IssueJWT(viewer.ID, "viewer", "developer", "test-secret")
	req := authedRequest(t, "PATCH", "/api/apps/dash2", []byte(`{"icon_emoji":"📊"}`), token)
	rec := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer PATCH icon_emoji: expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

// Clearing the emoji over HTTP leaves the image served. Write this test
// IMMEDIATELY ABOVE the one below: they are a deliberate contrast pair, and
// the distinction is exactly what a later edit tends to collapse.
func TestPatchAppIconEmojiClearKeepsServedImage(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "dash3", Name: "Dash3", OwnerID: owner.ID, Access: "public"})
	if err := store.SetAppIcon("dash3", "image/png", []byte("PNG")); err != nil {
		t.Fatalf("seed icon: %v", err)
	}
	if err := store.SetAppIconEmoji("dash3", "\U0001F4CA"); err != nil {
		t.Fatalf("seed emoji: %v", err)
	}
	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")

	doPatch(t, srv, "/api/apps/dash3", `{"icon_emoji":""}`, token, http.StatusOK)

	if rec := serveIcon(srv, iconReq("GET", "/api/apps/dash3/icon", nil, "", token)); rec.Code != http.StatusOK {
		t.Errorf("GET icon after clearing emoji: expected 200, got %d: %s", rec.Code, rec.Body.String())
	} else if rec.Body.String() != "PNG" {
		t.Errorf("GET icon after clearing emoji: body = %q, want the retained image bytes", rec.Body.String())
	}
}

// Remove-the-icon destroys both, which is the contrast to the test above.
func TestDeleteAppIconClearsEmojiToo(t *testing.T) {
	srv, store, _ := newManagerTestServer(t)
	hash, _ := testHashPassword("pass")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "dash4", Name: "Dash4", OwnerID: owner.ID, Access: "public"})
	if err := store.SetAppIcon("dash4", "image/png", []byte("PNG")); err != nil {
		t.Fatalf("seed icon: %v", err)
	}
	if err := store.SetAppIconEmoji("dash4", "\U0001F4CA"); err != nil {
		t.Fatalf("seed emoji: %v", err)
	}
	token, _ := auth.IssueJWT(owner.ID, "owner", "developer", "test-secret")

	if rec := serveIcon(srv, iconReq("DELETE", "/api/apps/dash4/icon", nil, "", token)); rec.Code != http.StatusOK {
		t.Fatalf("DELETE icon: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := serveIcon(srv, iconReq("GET", "/api/apps/dash4/icon", nil, "", token)); rec.Code != http.StatusNotFound {
		t.Errorf("GET icon after delete: expected 404, got %d", rec.Code)
	}

	app, _ := store.GetAppBySlug("dash4")
	if app.IconEmoji != "" {
		t.Errorf("DELETE icon left icon_emoji = %q, want cleared", app.IconEmoji)
	}
}
