package api_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rvben/shinyhub/internal/auth"
)

// publicAPIRoutes is every /api route an anonymous caller may reach. Each one
// authenticates the caller some other way (credentials in the body, an OAuth
// state, a one-time token) or serves only public information. Adding a route
// here is a security decision; every other route sits behind the bearer
// middleware.
var publicAPIRoutes = map[string]bool{
	"POST /api/auth/login":                true,
	"POST /api/auth/trusted-publishing":   true,
	"POST /api/auth/session":              true,
	"POST /api/auth/handoff":              true,
	"POST " + auth.ForwardAuthResumePath:  true,
	"GET " + auth.ForwardAuthLogoutPath:   true,
	"GET /api/auth/github/login":          true,
	"GET /api/auth/github/callback":       true,
	"GET /api/auth/google/login":          true,
	"GET /api/auth/google/callback":       true,
	"GET /api/auth/providers":             true,
	"POST /api/auth/invitations/preview":  true,
	"POST /api/auth/invitations/accept":   true,
	"GET /api/auth/cli-connect/status":    true,
	"POST /api/auth/cli-connect/register": true,
	"GET /api/auth/oidc/login":            true,
	"GET /api/auth/oidc/callback":         true,
	"GET /api/server-info":                true,
	"GET /api/announcements/active":       true,
}

// concretePath fills a chi route pattern's parameters and wildcard with
// placeholder values so the route can be requested.
func concretePath(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '{':
			end := strings.IndexByte(pattern[i:], '}')
			b.WriteString("1")
			i += end
		case '*':
			b.WriteString("x")
		default:
			b.WriteByte(pattern[i])
		}
	}
	return b.String()
}

func apiRoutes(t *testing.T, h http.Handler) []string {
	t.Helper()
	routes, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("Router() is %T, want a chi.Routes to enumerate", h)
	}
	var out []string
	if err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		out = append(out, method+" "+route)
		return nil
	}); err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	sort.Strings(out)
	return out
}

// wrongSchemeJWT is a JWT-shaped credential sent under the Token scheme. The
// bearer middleware refuses it with a realm="shinyhub" challenge naming the
// right scheme; no handler emits that challenge, so it proves the request
// was stopped by the middleware rather than by a handler's own nil-user
// check, which would mask a route registered outside the authenticated group.
const wrongSchemeJWT = "Token eyJhbGciOiJIUzI1NiJ9.e30.c2ln"

func refusedByBearerMiddleware(rec *httptest.ResponseRecorder) bool {
	return rec.Code == http.StatusUnauthorized &&
		strings.Contains(rec.Header().Get("WWW-Authenticate"), `realm="shinyhub"`)
}

// Every /api route outside publicAPIRoutes is behind the bearer middleware, so
// a route registered outside the authenticated group fails here instead of
// shipping open.
func TestAPIRoutesRequireAuthenticationUnlessPublic(t *testing.T) {
	srv, _ := newTestServer(t)
	router := srv.Router()
	routes := apiRoutes(t, router)

	seen := make(map[string]bool, len(routes))
	protected := 0
	for i, route := range routes {
		seen[route] = true
		method, pattern, _ := strings.Cut(route, " ")
		req := httptest.NewRequest(method, concretePath(pattern), nil)
		req.Header.Set("Authorization", wrongSchemeJWT)
		// One client per probe keeps the auth-failure limiter from answering
		// 429 in place of the middleware.
		req.RemoteAddr = fmt.Sprintf("[2001:db8::%x]:40000", i+1)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if publicAPIRoutes[route] {
			// A public route that started demanding a session would lock
			// out the callers it exists for.
			if refusedByBearerMiddleware(rec) {
				t.Errorf("%s is listed public but demands authentication", route)
			}
			continue
		}
		protected++
		if !refusedByBearerMiddleware(rec) {
			t.Errorf("%s answered %d without the bearer middleware's challenge; it is reachable without authentication (add it to publicAPIRoutes only if it is meant to be public)", route, rec.Code)
		}
	}
	for route := range publicAPIRoutes {
		if !seen[route] {
			t.Errorf("publicAPIRoutes lists %s, which is not registered", route)
		}
	}
	if protected < 100 {
		t.Fatalf("only %d protected routes enumerated; the walk is not seeing the authenticated group", protected)
	}
}
