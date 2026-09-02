package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/appnav"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/proxy"
)

// navRequest asks the nav endpoint for u's list. The slug in the path is one an
// anonymous caller could guess: the endpoint must answer from the identity, not
// from the app being addressed.
func navRequest(t *testing.T, srv *Server, u *auth.ContextUser) appnav.Payload {
	t.Helper()
	rr := httptest.NewRecorder()
	srv.HandleAppNavJSON(rr, reqWithOptionalUser("GET", appnav.DataURL("any-app"), u))
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var payload appnav.Payload
	if err := json.NewDecoder(rr.Body).Decode(&payload); err != nil {
		t.Fatalf("decode nav payload: %v", err)
	}
	return payload
}

func navSlugs(p appnav.Payload) map[string]bool {
	out := make(map[string]bool, len(p.Apps))
	for _, a := range p.Apps {
		out[a.Slug] = true
	}
	return out
}

// The switcher is drawn on pages an anonymous visitor can reach, so its list
// must be scoped exactly as the dashboard's is. A rail that named private apps
// would turn every public app page into an inventory of the private fleet.
func TestAppNavJSON_Visibility(t *testing.T) {
	srv, store := newBrandingTestServer(t, config.BrandingConfig{})

	hash, _ := testHashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: hash, Role: "viewer"})
	store.CreateUser(db.CreateUserParams{Username: "op", PasswordHash: hash, Role: "operator"})
	owner, _ := store.GetUserByUsername("owner")
	viewer, _ := store.GetUserByUsername("viewer")
	op, _ := store.GetUserByUsername("op")

	store.CreateApp(db.CreateAppParams{Slug: "pub-app", Name: "Public App", OwnerID: owner.ID, Access: "public"})
	store.CreateApp(db.CreateAppParams{Slug: "shared-app", Name: "Shared App", OwnerID: owner.ID, Access: "shared"})
	store.CreateApp(db.CreateAppParams{Slug: "member-app", Name: "Member App", OwnerID: owner.ID, Access: "private"})
	store.CreateApp(db.CreateAppParams{Slug: "private-other", Name: "Private Other", OwnerID: owner.ID, Access: "private"})
	store.GrantAppAccess("member-app", viewer.ID)

	cases := []struct {
		name    string
		user    *auth.ContextUser
		want    []string
		notWant []string
	}{
		{
			name:    "anonymous sees only public",
			user:    nil,
			want:    []string{"pub-app"},
			notWant: []string{"shared-app", "member-app", "private-other"},
		},
		{
			name:    "viewer sees public, shared, and their own membership",
			user:    &auth.ContextUser{ID: viewer.ID, Username: "viewer", Role: "viewer"},
			want:    []string{"pub-app", "shared-app", "member-app"},
			notWant: []string{"private-other"},
		},
		{
			name: "operator sees the fleet",
			user: &auth.ContextUser{ID: op.ID, Username: "op", Role: "operator"},
			want: []string{"pub-app", "shared-app", "member-app", "private-other"},
		},
		{
			name: "a scoped identity sees only its allowlist",
			user: &auth.ContextUser{
				ID:       op.ID,
				Username: "op",
				Role:     "operator", // privileged, and still bound by the scope
				AppScope: []string{"pub-app"},
			},
			want:    []string{"pub-app"},
			notWant: []string{"shared-app", "member-app", "private-other"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := navSlugs(navRequest(t, srv, tc.user))
			for _, slug := range tc.want {
				if !got[slug] {
					t.Errorf("expected %q in the switcher, got %v", slug, got)
				}
			}
			for _, slug := range tc.notWant {
				if got[slug] {
					t.Errorf("%q must not appear in this caller's switcher, got %v", slug, got)
				}
			}
			if len(got) != len(tc.want) {
				t.Errorf("expected exactly %v, got %v", tc.want, got)
			}
		})
	}
}

// The response is a function of the caller alone. If it ever varied by the slug
// in the path, the endpoint would become a way to probe which slugs exist.
func TestAppNavJSON_AnswerDoesNotVaryBySlug(t *testing.T) {
	srv, store := newBrandingTestServer(t, config.BrandingConfig{})
	hash, _ := testHashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "pub-app", Name: "Public App", OwnerID: owner.ID, Access: "public"})
	store.CreateApp(db.CreateAppParams{Slug: "private-other", Name: "Private Other", OwnerID: owner.ID, Access: "private"})

	body := func(slug string) string {
		rr := httptest.NewRecorder()
		srv.HandleAppNavJSON(rr, reqWithOptionalUser("GET", appnav.DataURL(slug), nil))
		return rr.Body.String()
	}
	// A slug that exists but is private, one that exists and is public, and one
	// that does not exist at all must be indistinguishable.
	real, hidden, absent := body("pub-app"), body("private-other"), body("no-such-app")
	if real != hidden || real != absent {
		t.Fatalf("the answer varies by slug, which discloses which apps exist:\npublic:  %s\nprivate: %s\nabsent:  %s", real, hidden, absent)
	}
}

// The username is what lets a visitor holding two accounts tell which one the
// rail belongs to, and it must never appear for a caller who has none.
func TestAppNavJSON_UsernameTracksTheCaller(t *testing.T) {
	srv, store := newBrandingTestServer(t, config.BrandingConfig{})
	hash, _ := testHashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "viewer", PasswordHash: hash, Role: "viewer"})
	viewer, _ := store.GetUserByUsername("viewer")

	if got := navRequest(t, srv, &auth.ContextUser{ID: viewer.ID, Username: "viewer", Role: "viewer"}).Username; got != "viewer" {
		t.Errorf("username = %q, want %q", got, "viewer")
	}
	if got := navRequest(t, srv, nil).Username; got != "" {
		t.Errorf("anonymous caller was given a username %q", got)
	}
}

// A clipped list must say it is clipped. Silently returning the first MaxApps
// reads as a complete fleet, and the visitor concludes their app is gone.
func TestAppNavJSON_TruncationIsDeclared(t *testing.T) {
	srv, store := newBrandingTestServer(t, config.BrandingConfig{})
	hash, _ := testHashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")

	for i := 0; i < appnav.MaxApps+5; i++ {
		slug := fmt.Sprintf("app-%03d", i)
		if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: owner.ID, Access: "public"}); err != nil {
			t.Fatalf("seed %s: %v", slug, err)
		}
	}

	payload := navRequest(t, srv, nil)
	if len(payload.Apps) != appnav.MaxApps {
		t.Fatalf("expected the list capped at %d, got %d", appnav.MaxApps, len(payload.Apps))
	}
	if !payload.Truncated {
		t.Error("a clipped list did not declare itself truncated")
	}
}

func TestAppNavJSON_ExactlyAtTheCapIsNotTruncated(t *testing.T) {
	srv, store := newBrandingTestServer(t, config.BrandingConfig{})
	hash, _ := testHashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")

	for i := 0; i < appnav.MaxApps; i++ {
		slug := fmt.Sprintf("app-%03d", i)
		if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, OwnerID: owner.ID, Access: "public"}); err != nil {
			t.Fatalf("seed %s: %v", slug, err)
		}
	}

	payload := navRequest(t, srv, nil)
	if len(payload.Apps) != appnav.MaxApps {
		t.Fatalf("expected %d apps, got %d", appnav.MaxApps, len(payload.Apps))
	}
	if payload.Truncated {
		t.Error("a fleet that ends exactly at the cap was reported as truncated")
	}
}

// Project name and icon are not columns on the app row; they are joined per
// request. Without that join every app groups as ungrouped, which compiles,
// passes a shape test, and silently flattens the switcher.
func TestAppNavJSON_CarriesProjectDisplay(t *testing.T) {
	srv, store := newBrandingTestServer(t, config.BrandingConfig{})
	hash, _ := testHashPassword("pw")
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")

	// CreateApp creates the project row lazily, with no display metadata; the
	// name and icon are set on the project itself, which is exactly why the
	// handler has to join them back in.
	store.CreateApp(db.CreateAppParams{
		Slug: "pub-app", Name: "Public App", OwnerID: owner.ID, Access: "public",
		ProjectSlug: "finance",
	})
	if err := store.UpdateProject(db.UpdateProjectParams{
		Slug: "finance", SetName: true, Name: "Finance", SetIconEmoji: true, IconEmoji: "💰",
	}); err != nil {
		t.Fatalf("set project display: %v", err)
	}
	if err := store.SetAppIconEmoji("pub-app", "📊"); err != nil {
		t.Fatalf("set app icon: %v", err)
	}

	payload := navRequest(t, srv, nil)
	if len(payload.Apps) != 1 {
		t.Fatalf("expected 1 app, got %d", len(payload.Apps))
	}
	got := payload.Apps[0]
	if got.ProjectSlug != "finance" {
		t.Errorf("project_slug = %q, want %q", got.ProjectSlug, "finance")
	}
	if got.ProjectName != "Finance" {
		t.Errorf("project_name = %q, want %q - without it the switcher labels the group by its slug", got.ProjectName, "Finance")
	}
	if got.ProjectIconEmoji != "💰" {
		t.Errorf("project_icon_emoji = %q, want %q", got.ProjectIconEmoji, "💰")
	}
	if got.IconEmoji != "📊" {
		t.Errorf("icon_emoji = %q, want %q", got.IconEmoji, "📊")
	}
}

// The rail is read on every app page load and the fleet changes underneath it.
// A cached copy would pin a visitor to whatever was true when they first opened
// an app, including apps they have since lost access to.
func TestAppNavJSON_IsNotCacheable(t *testing.T) {
	srv, _ := newBrandingTestServer(t, config.BrandingConfig{})
	rr := httptest.NewRecorder()
	srv.HandleAppNavJSON(rr, reqWithOptionalUser("GET", appnav.DataURL("any-app"), nil))
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}

func TestAppVersionJSON_ReturnsOnlyThePublishedOpaqueGeneration(t *testing.T) {
	srv, store := newBrandingTestServer(t, config.BrandingConfig{})
	hash, _ := testHashPassword("pw")
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "dashboard", Name: "Dashboard", OwnerID: owner.ID, Access: "public"}); err != nil {
		t.Fatalf("create app: %v", err)
	}
	app, _ := store.GetAppBySlug("dashboard")
	deployment, err := store.CreateDeployment(db.CreateDeploymentParams{AppID: app.ID, Version: "v1", BundleDir: "/bundle/v1"})
	if err != nil {
		t.Fatalf("create deployment: %v", err)
	}
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(backend.Close)
	srv.proxy = proxy.New()
	srv.proxy.SetPoolSize("dashboard", 1)
	srv.proxy.SetGenerationActivationToken("dashboard", deployment.ID, deployment.ActivationToken)
	if err := srv.proxy.RegisterReplica("dashboard", 0, backend.URL, nil, deployment.ID); err != nil {
		t.Fatalf("register local active generation: %v", err)
	}

	rr := httptest.NewRecorder()
	srv.HandleAppVersionJSON(rr, reqWithOptionalUser("GET", appnav.VersionURL("dashboard"), nil), "dashboard")
	if rr.Code != http.StatusOK {
		t.Fatalf("version status = %d: %s", rr.Code, rr.Body.String())
	}
	var shape map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&shape); err != nil {
		t.Fatalf("decode version payload: %v", err)
	}
	active, _ := shape["active_generation"].(string)
	if active != deployment.ActivationToken || active == "" {
		t.Fatalf("active generation = %q, want opaque token %q", active, deployment.ActivationToken)
	}
	if len(shape) != 1 || shape["active_generation"] == nil {
		t.Fatalf("version response disclosed extra metadata: %#v", shape)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store, private" {
		t.Fatalf("Cache-Control = %q", got)
	}
}

func TestAppVersionSwitch_RequiresExplicitHeaderAndClearsOnlyThisAppCookie(t *testing.T) {
	srv, store := newBrandingTestServer(t, config.BrandingConfig{})
	srv.proxy = proxy.New()
	hash, _ := testHashPassword("pw")
	if err := store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"}); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	owner, _ := store.GetUserByUsername("owner")
	if _, err := store.CreateApp(db.CreateAppParams{Slug: "dashboard", Name: "Dashboard", OwnerID: owner.ID, Access: "public"}); err != nil {
		t.Fatalf("create app: %v", err)
	}

	withoutHeader := httptest.NewRecorder()
	srv.HandleAppVersionSwitch(withoutHeader, reqWithOptionalUser("POST", appnav.SwitchURL("dashboard"), nil), "dashboard")
	if withoutHeader.Code != http.StatusForbidden {
		t.Fatalf("switch without confirmation = %d, want 403", withoutHeader.Code)
	}

	req := reqWithOptionalUser("POST", appnav.SwitchURL("dashboard"), nil)
	req.Header.Set("X-ShinyHub-Version-Switch", "1")
	rr := httptest.NewRecorder()
	srv.HandleAppVersionSwitch(rr, req, "dashboard")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("switch status = %d: %s", rr.Code, rr.Body.String())
	}
	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want one app-scoped affinity deletion", len(cookies))
	}
	if cookies[0].Name != "shinyhub_rep_dashboard" || cookies[0].Path != "/app/dashboard/" || cookies[0].MaxAge >= 0 {
		t.Fatalf("affinity deletion = %#v", cookies[0])
	}
}

func TestAppVersionJSON_DoesNotRevealPrivateAppsToAnonymousCallers(t *testing.T) {
	srv, store := newBrandingTestServer(t, config.BrandingConfig{})
	hash, _ := testHashPassword("pw")
	_ = store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: hash, Role: "developer"})
	owner, _ := store.GetUserByUsername("owner")
	_, _ = store.CreateApp(db.CreateAppParams{Slug: "private-dashboard", Name: "Private", OwnerID: owner.ID, Access: "private"})

	rr := httptest.NewRecorder()
	srv.HandleAppVersionJSON(rr, reqWithOptionalUser("GET", appnav.VersionURL("private-dashboard"), nil), "private-dashboard")
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous private version status = %d, want 401", rr.Code)
	}
}
