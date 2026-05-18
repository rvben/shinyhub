package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
)

// newBrandingTestServer builds a test Server with an optional branding config.
// Mirrors the pattern in authorization_test.go (package api, direct server construction).
func newBrandingTestServer(t *testing.T, branding config.BrandingConfig) (*Server, *db.Store) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Auth:     config.AuthConfig{Secret: "test-secret"},
		Storage:  config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()},
		Branding: branding,
	}
	srv := New(cfg, store, nil, nil)
	t.Cleanup(func() { store.Close() })
	return srv, store
}

// reqWithOptionalUser builds an httptest.Request and optionally injects a
// ContextUser into its context. When u is nil the request is unauthenticated.
func reqWithOptionalUser(method, path string, u *auth.ContextUser) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if u != nil {
		r = r.WithContext(auth.WithUser(r.Context(), u))
	}
	return r
}

// TestBrandingJSONPublic asserts that handleBrandingJSON returns 200 with the
// configured site_title even when the caller is unauthenticated.
func TestBrandingJSONPublic(t *testing.T) {
	branding := config.BrandingConfig{SiteTitle: "Acme"}
	srv, _ := newBrandingTestServer(t, branding)

	r := reqWithOptionalUser("GET", "/.shinyhub/branding.json", nil)
	rr := httptest.NewRecorder()
	srv.HandleBrandingJSON(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var body struct {
		SiteTitle string `json:"site_title"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.SiteTitle != "Acme" {
		t.Errorf("site_title = %q, want %q", body.SiteTitle, "Acme")
	}
}

// TestBrandingJSONInactive asserts that handleBrandingJSON returns an empty
// JSON object when no branding is configured.
func TestBrandingJSONInactive(t *testing.T) {
	srv, _ := newBrandingTestServer(t, config.BrandingConfig{})

	r := reqWithOptionalUser("GET", "/.shinyhub/branding.json", nil)
	rr := httptest.NewRecorder()
	srv.HandleBrandingJSON(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body) != 0 {
		t.Errorf("expected empty object when branding inactive, got %v", body)
	}
}

// TestAppsJSONAnonymousPublicOnly asserts that an unauthenticated caller
// receives only public apps and that the DTO contains exactly {slug, name,
// visibility} - no owner_id, status, replicas, or other internal fields.
func TestAppsJSONAnonymousPublicOnly(t *testing.T) {
	srv, store := newBrandingTestServer(t, config.BrandingConfig{})

	// Seed one user to be the owner.
	hash, _ := auth.HashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")

	// Create one app of each visibility.
	store.CreateApp(db.CreateAppParams{Slug: "pub-app", Name: "Public App", OwnerID: owner.ID, Access: "public"})
	store.CreateApp(db.CreateAppParams{Slug: "shared-app", Name: "Shared App", OwnerID: owner.ID, Access: "shared"})
	store.CreateApp(db.CreateAppParams{Slug: "private-app", Name: "Private App", OwnerID: owner.ID, Access: "private"})

	r := reqWithOptionalUser("GET", "/.shinyhub/apps.json", nil) // no auth
	rr := httptest.NewRecorder()
	srv.HandleAppsJSON(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Decode as a slice of raw maps to inspect the exact key set.
	var items []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&items); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if len(items) != 1 {
		t.Fatalf("anonymous caller should see exactly 1 app, got %d: %v", len(items), items)
	}

	entry := items[0]

	// Verify it's the public app.
	if slug, _ := entry["slug"].(string); slug != "pub-app" {
		t.Errorf("expected slug %q, got %q", "pub-app", slug)
	}

	// Shared app must be absent.
	// (already asserted via len==1, but be explicit)
	for _, item := range items {
		if s, _ := item["slug"].(string); s == "shared-app" {
			t.Errorf("shared app must not appear in anonymous response")
		}
	}

	// DTO must contain exactly {slug, name, visibility}.
	allowed := map[string]bool{"slug": true, "name": true, "visibility": true}
	for key := range entry {
		if !allowed[key] {
			t.Errorf("unexpected key %q in anonymous apps.json DTO", key)
		}
	}
	for key := range allowed {
		if _, present := entry[key]; !present {
			t.Errorf("missing expected key %q in anonymous apps.json DTO", key)
		}
	}

	// Visibility should be "public" for this app.
	if vis, _ := entry["visibility"].(string); vis != "public" {
		t.Errorf("visibility = %q, want %q", vis, "public")
	}
}

// TestAppsJSONAuthedVisibility asserts visibility rules for authenticated and
// admin callers:
//   - non-admin sees public + shared + member apps but not private-not-owned
//   - admin sees all apps
func TestAppsJSONAuthedVisibility(t *testing.T) {
	srv, store := newBrandingTestServer(t, config.BrandingConfig{})

	// Create users.
	hash, _ := auth.HashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: hash, Role: "viewer"})
	store.CreateUser(db.CreateUserParams{Username: "admin", PasswordHash: hash, Role: "admin"})

	owner, _ := store.GetUserByUsername("owner")
	viewer, _ := store.GetUserByUsername("viewer")
	adminUser, _ := store.GetUserByUsername("admin")

	// Create apps with different access levels.
	store.CreateApp(db.CreateAppParams{Slug: "pub-app", Name: "Public App", OwnerID: owner.ID, Access: "public"})
	store.CreateApp(db.CreateAppParams{Slug: "shared-app", Name: "Shared App", OwnerID: owner.ID, Access: "shared"})
	store.CreateApp(db.CreateAppParams{Slug: "private-other", Name: "Private Other", OwnerID: owner.ID, Access: "private"})
	store.CreateApp(db.CreateAppParams{Slug: "member-app", Name: "Member App", OwnerID: owner.ID, Access: "private"})

	// Grant viewer explicit access to member-app.
	store.GrantAppAccess("member-app", viewer.ID)

	// --- Non-admin viewer ---
	viewerCtxUser := &auth.ContextUser{ID: viewer.ID, Username: "viewer", Role: "viewer"}
	r := reqWithOptionalUser("GET", "/.shinyhub/apps.json", viewerCtxUser)
	rr := httptest.NewRecorder()
	srv.HandleAppsJSON(rr, r)

	if rr.Code != http.StatusOK {
		t.Fatalf("viewer: expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var viewerItems []map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&viewerItems); err != nil {
		t.Fatalf("viewer: decode response: %v", err)
	}
	if len(viewerItems) != 3 {
		t.Fatalf("viewer: expected exactly 3 apps, got %d: %v", len(viewerItems), viewerItems)
	}

	// Should see public, shared, member-app; NOT private-other.
	viewerSlugs := slugSet(viewerItems)
	for _, want := range []string{"pub-app", "shared-app", "member-app"} {
		if !viewerSlugs[want] {
			t.Errorf("viewer: expected to see app %q, but did not (got %v)", want, viewerSlugs)
		}
	}
	if viewerSlugs["private-other"] {
		t.Errorf("viewer: must not see private-other app")
	}

	// DTO key set must be EXACTLY {slug, name, visibility} - bidirectional check.
	allowedKeys := map[string]bool{"slug": true, "name": true, "visibility": true}
	for _, item := range viewerItems {
		for key := range item {
			if !allowedKeys[key] {
				t.Errorf("viewer: unexpected key %q in apps.json DTO", key)
			}
		}
		for key := range allowedKeys {
			if _, present := item[key]; !present {
				t.Errorf("viewer: missing expected key %q in apps.json DTO", key)
			}
		}
	}

	// --- Admin sees all ---
	adminCtxUser := &auth.ContextUser{ID: adminUser.ID, Username: "admin", Role: "admin"}
	r2 := reqWithOptionalUser("GET", "/.shinyhub/apps.json", adminCtxUser)
	rr2 := httptest.NewRecorder()
	srv.HandleAppsJSON(rr2, r2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("admin: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}

	var adminItems []map[string]any
	if err := json.NewDecoder(rr2.Body).Decode(&adminItems); err != nil {
		t.Fatalf("admin: decode response: %v", err)
	}
	if len(adminItems) != 4 {
		t.Fatalf("admin: expected exactly 4 apps, got %d: %v", len(adminItems), adminItems)
	}

	adminSlugs := slugSet(adminItems)
	for _, want := range []string{"pub-app", "shared-app", "private-other", "member-app"} {
		if !adminSlugs[want] {
			t.Errorf("admin: expected to see app %q, but did not (got %v)", want, adminSlugs)
		}
	}

	// DTO key set must be EXACTLY {slug, name, visibility} for admin entries too.
	for _, item := range adminItems {
		for key := range item {
			if !allowedKeys[key] {
				t.Errorf("admin: unexpected key %q in apps.json DTO", key)
			}
		}
		for key := range allowedKeys {
			if _, present := item[key]; !present {
				t.Errorf("admin: missing expected key %q in apps.json DTO", key)
			}
		}
	}
}

// slugSet extracts slug values from a slice of raw JSON objects for easy membership tests.
func slugSet(items []map[string]any) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		if s, _ := item["slug"].(string); s != "" {
			out[s] = true
		}
	}
	return out
}
