package access_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/appnav"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/favicon"
)

const navHome = "https://hub.example.com/"

// browserGET is a top-level navigation: the only request shape that gets an
// HTML page at all, and therefore the only one the switcher can ride on.
func browserGET(target string) *http.Request {
	req := httptest.NewRequest("GET", target, nil)
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	return req
}

// assertHasSwitcher checks the page carries the switcher, and carries it where
// it can work: after the page's own content and before the closing tag. A
// contains-check alone passes on a script spliced into the middle of the head.
func assertHasSwitcher(t *testing.T, body string) {
	t.Helper()
	tag := strings.Index(body, `id="`+appnav.ScriptID+`"`)
	if tag < 0 {
		t.Fatalf("page carries no app switcher:\n%s", body)
	}
	closing := strings.LastIndex(body, "</body>")
	if closing < 0 || tag > closing {
		t.Fatalf("switcher is not inside the document body (tag at %d, </body> at %d)", tag, closing)
	}
	if !strings.Contains(body, `data-home-url="`+navHome+`"`) {
		t.Error("switcher carries no home link, so the dashboard is unreachable from it")
	}
}

func privateAppStore(t *testing.T) *db.Store {
	t.Helper()
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "secret", Name: "Quarterly Report", OwnerID: owner.ID})
	return store
}

// The 401 page is the surface a signed-out visitor lands on, and it is where
// the dead end is worst: no session, no app, and until now no link to anything
// they could open. The switcher answers with the public apps for exactly this
// caller, so it is useful even while they are anonymous.
func TestAccessDenied_Unauthorized_CarriesTheSwitcher(t *testing.T) {
	store := privateAppStore(t)
	handler := access.Middleware(store, "test-secret", nil, nil, access.WithAppNav(navHome))(http.HandlerFunc(next))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, browserGET("/app/secret/"))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	assertHasSwitcher(t, rec.Body.String())
	if !strings.Contains(rec.Body.String(), favicon.Link(favicon.PlatformURL)) {
		t.Fatal("access-denied page does not carry the privacy-safe platform favicon")
	}
}

func TestAccessDenied_Forbidden_CarriesTheSwitcher(t *testing.T) {
	store := privateAppStore(t)
	store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: "h", Role: "developer"})
	bob, _ := store.GetUserByUsername("bob")
	bobToken, _ := auth.IssueJWT(bob.ID, "bob", "developer", "test-secret")

	handler := access.Middleware(store, "test-secret", nil, nil, access.WithAppNav(navHome))(http.HandlerFunc(next))

	req := browserGET("/app/secret/")
	req.AddCookie(&http.Cookie{Name: "shiny_session", Value: bobToken})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
	body := rec.Body.String()
	assertHasSwitcher(t, body)
	// The page's own job must survive the addition.
	if !strings.Contains(body, `<form method="POST" action="/api/auth/handoff">`) {
		t.Errorf("adding the switcher displaced the handoff form:\n%s", body)
	}
}

// The switcher must not leak the app that was just refused. Its list is fetched
// per caller and the refused app is by construction not in it, so the page it
// rides on must not name the app either - the injection adds the slug as
// addressing for the data URL, never the name.
func TestAccessDenied_SwitcherDoesNotNameTheRefusedApp(t *testing.T) {
	store := privateAppStore(t)
	handler := access.Middleware(store, "test-secret", nil, nil, access.WithAppNav(navHome))(http.HandlerFunc(next))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, browserGET("/app/secret/"))

	if body := rec.Body.String(); strings.Contains(body, "Quarterly Report") {
		t.Fatalf("the switcher leaked the refused app's name to an anonymous caller:\n%s", body)
	}
}

// Without the option every byte must be what it was. An operator who turns the
// switcher off is entitled to the pages they had before it existed.
func TestAccessDenied_WithoutTheOption_PageIsUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cookie string
		status int
	}{
		{name: "401", status: http.StatusUnauthorized},
		{name: "403", status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := privateAppStore(t)
			store.CreateUser(db.CreateUserParams{Username: "bob", PasswordHash: "h", Role: "developer"})
			bob, _ := store.GetUserByUsername("bob")
			bobToken, _ := auth.IssueJWT(bob.ID, "bob", "developer", "test-secret")

			serve := func(opts ...access.Option) *httptest.ResponseRecorder {
				handler := access.Middleware(store, "test-secret", nil, nil, opts...)(http.HandlerFunc(next))
				req := browserGET("/app/secret/")
				if tc.status == http.StatusForbidden {
					req.AddCookie(&http.Cookie{Name: "shiny_session", Value: bobToken})
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				return rec
			}

			off := serve()
			if off.Code != tc.status {
				t.Fatalf("expected %d, got %d", tc.status, off.Code)
			}
			if strings.Contains(off.Body.String(), appnav.ScriptID) {
				t.Fatalf("switcher appeared on a page that never asked for it:\n%s", off.Body.String())
			}
			// Positive control: the same page with the option on DOES change, so
			// "unchanged" above is evidence about the option and not about a
			// splice that never works here.
			if on := serve(access.WithAppNav(navHome)); on.Body.String() == off.Body.String() {
				t.Fatal("the option changed nothing, so the negative case proves nothing")
			}
		})
	}
}

// A CLI or SDK caller gets the JSON envelope, and JSON has nowhere to put a
// script. Splicing into it would hand a parser a broken body.
func TestAccessDenied_JSONCallerNeverGetsTheSwitcher(t *testing.T) {
	store := privateAppStore(t)
	handler := access.Middleware(store, "test-secret", nil, nil, access.WithAppNav(navHome))(http.HandlerFunc(next))

	req := httptest.NewRequest("GET", "/app/secret/", nil) // no navigate, no text/html
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Body.String(); got != `{"error":"unauthorized"}` {
		t.Fatalf("JSON envelope was altered: %s", got)
	}
}

// The never-deployed notice shown to a non-manager is the one ShinyHub page
// with no link on it at all. It is the strongest case for the switcher and the
// easiest to forget, because the manager variant looks fine without it.
func TestNeverDeployed_CarriesTheSwitcher(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "fresh", Name: "Fresh", OwnerID: owner.ID})
	store.SetAppAccess("fresh", "public")

	handler := access.NeverDeployedMiddleware(store, "test-secret", nil, nil, nil, access.WithAppNav(navHome))(http.HandlerFunc(next))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, browserGET("/app/fresh/"))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected the never-deployed page (200), got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "being prepared by its owner") {
		t.Fatalf("this is not the non-manager never-deployed page:\n%s", body)
	}
	assertHasSwitcher(t, body)
	if !strings.Contains(body, favicon.Link(favicon.AppURL("fresh"))) {
		t.Fatal("never-deployed page does not carry its app favicon")
	}
}

// A deployed app must reach the proxy untouched: this middleware forwards, and
// a forwarded request is not a page this package renders.
func TestNeverDeployed_ForwardedRequestIsNotInjected(t *testing.T) {
	store := makeStore(t)
	store.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"})
	owner, _ := store.GetUserByUsername("owner")
	store.CreateApp(db.CreateAppParams{Slug: "live", Name: "Live", OwnerID: owner.ID})
	app, _ := store.GetAppBySlug("live")
	if _, err := store.CreateDeployment(db.CreateDeploymentParams{
		AppID:     app.ID,
		Version:   "1",
		BundleDir: t.TempDir(),
	}); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}

	handler := access.NeverDeployedMiddleware(store, "test-secret", nil, nil, nil, access.WithAppNav(navHome))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("<html><body>upstream</body></html>"))
		}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, browserGET("/app/live/"))

	if got := rec.Body.String(); got != "<html><body>upstream</body></html>" {
		t.Fatalf("a forwarded response was rewritten by the middleware: %s", got)
	}
}
