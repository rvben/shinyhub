package access_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

type rootRedirectStore struct {
	app *db.App
	err error
}

func TestAccessAppRootRedirectRestoresScopedSupportCookie(t *testing.T) {
	const secret = "test-secret"
	actor := &auth.ContextUser{ID: 1, Username: "admin", Role: "admin"}
	subject := &auth.ContextUser{ID: 2, Username: "subject", Role: "viewer"}
	expires := time.Now().Add(15 * time.Minute)
	issued := *subject
	issued.SupportSession = &auth.SupportSessionContext{
		ID: "support-id", ActorID: actor.ID, ActorUsername: actor.Username,
		AppID: 42, AppSlug: "demo", ExpiresAt: expires,
	}
	token, _, err := auth.IssueSessionTokenWithInfo(&issued, secret)
	if err != nil {
		t.Fatal(err)
	}
	lookup := func(id int64) (*auth.ContextUser, error) {
		if id == actor.ID {
			return actor, nil
		}
		return subject, nil
	}
	st := rootRedirectStore{app: &db.App{ID: 42, Slug: "demo", Access: "public"}}
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r.Context())
		if u == nil || u.ID != subject.ID || u.SupportSession == nil {
			t.Error("canonical root did not preserve the support identity")
		}
		io.WriteString(w, "app content")
	})
	accessHandler := access.Middleware(st, secret, nil, lookup)(app)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/demo" {
			if _, err := r.Cookie(auth.SupportSessionCookieName); err == nil {
				t.Error("cookie jar sent the scoped cookie to a bare root")
			}
		}
		accessHandler.ServeHTTP(w, r)
	}))
	defer front.Close()
	origin, err := url.Parse(front.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	cookies := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, front.URL+"/app/demo/", nil)
	auth.SetSupportSessionCookie(cookies, req, token, "demo", expires, nil)
	auth.SetSupportSessionGuardCookie(cookies, req, "support-id", expires, nil)
	jar.SetCookies(origin, cookies.Result().Cookies())
	client := front.Client()
	client.Jar = jar
	resp, err := client.Get(front.URL + "/app/demo")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Request.URL.Path != "/app/demo/" || string(body) != "app content" {
		t.Fatalf("support landing: status=%d url=%s body=%q", resp.StatusCode, resp.Request.URL, body)
	}
	// Canonicalization must not bypass the support guard when the app cookie
	// is actually missing: the follow-up slash request must still be blocked.
	jar.SetCookies(origin, []*http.Cookie{{Name: auth.SupportSessionCookieName, Path: "/app/demo/", MaxAge: -1}})
	blocked, err := client.Get(front.URL + "/app/demo")
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Body.Close()
	if blocked.StatusCode != http.StatusConflict || blocked.Request.URL.Path != "/app/demo/" {
		t.Fatalf("missing support cookie: status=%d url=%s", blocked.StatusCode, blocked.Request.URL)
	}
}

func (s rootRedirectStore) GetAppBySlug(slug string) (*db.App, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.app != nil && s.app.Slug == slug {
		return s.app, nil
	}
	return nil, db.ErrNotFound
}

func (rootRedirectStore) UserCanAccessApp(string, int64) (bool, error) { return false, nil }

func TestAccessAppRootRedirectBeforeSessionResolution(t *testing.T) {
	for _, visibility := range []string{"public", "private", "shared"} {
		t.Run(visibility, func(t *testing.T) {
			st := rootRedirectStore{app: &db.App{ID: 1, Slug: "demo", Access: visibility}}
			lookup := func(int64) (*auth.ContextUser, error) {
				t.Error("bare root resolved a session before canonicalization")
				return nil, nil
			}
			handler := access.Middleware(st, "test-secret", nil, lookup)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("bare root reached downstream handler")
			}))
			for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost} {
				for _, target := range []string{"/app/demo", "/app/demo?", "/app/demo?_inputs_&flt_app=%5B%22vscode%22%5D", "/app/%64emo?x=%2f&x=2&q=a+b"} {
					req := httptest.NewRequest(method, target, strings.NewReader("payload"))
					// Browsers omit the app-scoped support cookie at a bare root.
					req.AddCookie(&http.Cookie{Name: auth.SupportSessionGuardCookieName, Value: "support-id"})
					req.Header.Set("X-Forwarded-Host", "public.example")
					req.Header.Set("X-Forwarded-Proto", "https")
					rec := httptest.NewRecorder()
					handler.ServeHTTP(rec, req)
					want := req.URL.EscapedPath() + "/"
					if req.URL.RawQuery != "" || req.URL.ForceQuery {
						want += "?" + req.URL.RawQuery
					}
					if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != want {
						t.Errorf("%s %s: status=%d Location=%q, want 308 %q", method, target, rec.Code, rec.Header().Get("Location"), want)
					}
					if len(rec.Result().Cookies()) != 0 || method == http.MethodHead && rec.Body.Len() != 0 {
						t.Error("redirect set cookies or wrote a HEAD body")
					}
				}
			}
		})
	}
}

func TestAccessAppRootRedirectDoesNotHideErrorsOrBypassAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name   string
		st     rootRedirectStore
		path   string
		status int
		called bool
	}{
		{"unknown root", rootRedirectStore{}, "/app/missing", http.StatusNotFound, true},
		{"lookup failure", rootRedirectStore{err: errors.New("database unavailable")}, "/app/demo", http.StatusInternalServerError, false},
		{"private canonical root", rootRedirectStore{app: &db.App{Slug: "demo", Access: "private"}}, "/app/demo/", http.StatusUnauthorized, false},
		{"private asset", rootRedirectStore{app: &db.App{Slug: "demo", Access: "private"}}, "/app/demo/lib/shiny.js", http.StatusUnauthorized, false},
		{"public asset", rootRedirectStore{app: &db.App{Slug: "demo", Access: "public"}}, "/app/demo/lib/shiny.js", http.StatusNotFound, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := access.Middleware(tc.st, "test-secret", nil, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				http.NotFound(w, r)
			}))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != tc.status || called != tc.called || rec.Header().Get("Location") != "" {
				t.Fatalf("status=%d called=%v Location=%q, want status=%d called=%v", rec.Code, called, rec.Header().Get("Location"), tc.status, tc.called)
			}
		})
	}
}

func TestAccessAppRootRedirectBeforeNeverDeployedPage(t *testing.T) {
	st := makeStore(t)
	if err := st.CreateUser(db.CreateUserParams{Username: "owner", PasswordHash: "h", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	owner, err := st.GetUserByUsername("owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateApp(db.CreateAppParams{Slug: "demo", Name: "Demo", OwnerID: owner.ID, Access: "public"}); err != nil {
		t.Fatal(err)
	}
	emptyState := access.NeverDeployedMiddleware(st, "test-secret", nil, nil, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("never-deployed app reached backend")
	}))
	handler := access.Middleware(st, "test-secret", nil, nil)(emptyState)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app/demo?tab=one", nil))
	if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != "/app/demo/?tab=one" {
		t.Fatalf("bare root: status=%d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	landing := httptest.NewRecorder()
	handler.ServeHTTP(landing, httptest.NewRequest(http.MethodGet, rec.Header().Get("Location"), nil))
	if landing.Code != http.StatusOK || !strings.Contains(landing.Body.String(), "is being prepared") {
		t.Fatalf("canonical empty state: status=%d body=%q", landing.Code, landing.Body.String())
	}
}
