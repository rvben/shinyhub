package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/dbtest"
)

func TestTrustedAppSupportPreservesAdminAndGuardsAppAccess(t *testing.T) {
	store := dbtest.New(t)
	for _, p := range []db.CreateUserParams{{Username: "admin", PasswordHash: "hash", Role: "admin"}, {Username: "viewer", PasswordHash: "hash", Role: "viewer"}} {
		if err := store.CreateUser(p); err != nil {
			t.Fatal(err)
		}
	}
	admin, _ := store.GetUserByUsername("admin")
	viewer, _ := store.GetUserByUsername("viewer")
	for _, slug := range []string{"sales", "other"} {
		if _, err := store.CreateApp(db.CreateAppParams{Slug: slug, Name: slug, ProjectSlug: "default", OwnerID: admin.ID, Access: "public"}); err != nil {
			t.Fatal(err)
		}
	}
	const secret = "trusted-app-test-secret"
	const origin = "https://hub.example.com"
	cfg := &config.Config{Auth: config.AuthConfig{Secret: secret, SupportSessions: true, SupportSessionsTrustedApps: true}, Server: config.ServerConfig{BaseURL: origin}, Storage: config.StorageConfig{AppsDir: t.TempDir(), AppDataDir: t.TempDir()}}
	srv := api.New(cfg, store, nil, nil)
	appHits := 0
	backend := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		appHits++
		u := auth.UserFromContext(r.Context())
		if u == nil || u.ID != viewer.ID || u.SupportSession == nil {
			t.Errorf("app did not receive support viewer: %+v", u)
		}
		w.WriteHeader(204)
	})
	app := trustedAppSupportDispatch(access.Middleware(store, secret, store.IsTokenRevoked, store.LookupContextUser)(backend), store, secret, nil)
	mux := http.NewServeMux()
	mux.Handle("/api/", srv.Router())
	mux.Handle("/app/", app)
	mux.HandleFunc("POST /app/{slug}/.shinyhub/support-session/stop", supportSessionStopHandler(store, secret, origin+"/users", nil))
	jar, _ := cookiejar.New(nil)
	base, _ := url.Parse(origin)
	token, err := auth.IssueJWT(admin.ID, admin.Username, admin.Role, secret)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(base, []*http.Cookie{{Name: auth.SessionCookieName, Value: token, Path: "/", Secure: true, HttpOnly: true}, {Name: auth.CSRFCookieName, Value: "csrf-test", Path: "/", Secure: true}})
	request := func(method, target, body string, forwarded bool) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "application/json")
		r.Header.Set("Referer", origin+"/users")
		r.Header.Set(auth.CSRFHeaderName, "csrf-test")
		for _, c := range jar.Cookies(r.URL) {
			r.AddCookie(c)
		}
		if forwarded {
			u, _ := store.LookupContextUser(admin.ID)
			r = r.WithContext(auth.WithUser(r.Context(), u))
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		jar.SetCookies(r.URL, w.Result().Cookies())
		return w
	}
	metadata := request("GET", origin+"/api/users", "", false)
	if metadata.Code != 200 || !strings.Contains(metadata.Body.String(), `"trusted_apps":true`) {
		t.Fatalf("trusted mode metadata: %d %s", metadata.Code, metadata.Body.String())
	}
	created := request("POST", origin+"/api/support-sessions", fmt.Sprintf(`{"user_id":%d,"app_slug":"sales","reason":"checking shared-host support"}`, viewer.ID), false)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var session struct {
		ID        string `json:"id"`
		LaunchURL string `json:"launch_url"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(session.LaunchURL, origin+"/app/sales/?") {
		t.Fatalf("launch URL %q", session.LaunchURL)
	}
	launched := request("GET", session.LaunchURL, "", false)
	if launched.Code != 303 {
		t.Fatalf("launch: %d %s", launched.Code, launched.Body.String())
	}
	for _, c := range launched.Result().Cookies() {
		if c.Name == auth.SessionCookieName {
			t.Fatal("launch touched administrator cookie")
		}
	}
	if got := request("GET", session.LaunchURL, "", false); got.Code != 401 {
		t.Fatalf("replay: %d", got.Code)
	}
	for _, forwarded := range []bool{false, true} {
		if got := request("GET", origin+"/app/sales/", "", forwarded); got.Code != 204 {
			t.Fatalf("app: %d %s", got.Code, got.Body.String())
		}
		request("GET", origin+"/app/other/", "", forwarded)
	}
	if appHits != 2 {
		t.Fatalf("other app bypassed scope: hits=%d", appHits)
	}
	current := request("GET", origin+"/api/support-sessions/current", "", false)
	if current.Code != 200 || !strings.Contains(current.Body.String(), origin+"/app/sales/") {
		t.Fatalf("dashboard recovery: %d %s", current.Code, current.Body.String())
	}
	stop := request("POST", origin+"/app/sales/.shinyhub/support-session/stop", "", false)
	if stop.Code != 200 {
		t.Fatalf("stop: %d %s", stop.Code, stop.Body.String())
	}
	for _, forwarded := range []bool{false, true} {
		request("GET", origin+"/app/sales/", "", forwarded)
	}
	if appHits != 2 {
		t.Fatal("stopped support fell back to administrator")
	}
	if got := request("GET", origin+"/api/support-sessions/current", "", false); got.Code != 200 {
		t.Fatalf("admin lost: %d", got.Code)
	}
	events, err := store.ListAuditEvents("support_session.start", 10, 0)
	if err != nil || len(events) != 1 || events[0].ResourceID != session.ID {
		t.Fatalf("audit events=%v err=%v", events, err)
	}
}
