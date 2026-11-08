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
	"github.com/rvben/shinyhub/internal/db"
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

// concretePath fills a chi route pattern's parameters with param and its
// wildcard with a placeholder so the route can be requested.
func concretePath(pattern, param string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '{':
			end := strings.IndexByte(pattern[i:], '}')
			b.WriteString(param)
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
		req := httptest.NewRequest(method, concretePath(pattern, "1"), nil)
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

// roleGatedAPIRoutes maps every /api route that refuses some signed-in roles
// outright (403) to the least role it admits. Every authenticated route not
// listed admits all roles at this level and leaves finer decisions, such as
// access to one app, to its handler.
var roleGatedAPIRoutes = map[string]string{
	"DELETE /api/service-accounts/{key}/credentials/{id}": "admin",
	"DELETE /api/support-sessions/current":                "admin",
	"DELETE /api/user-invitations/{id}":                   "admin",
	"DELETE /api/users/{id}":                              "admin",
	"GET /api/announcements":                              "admin",
	"GET /api/announcements/{id}":                         "admin",
	"GET /api/audit":                                      "admin",
	"GET /api/fleet/health":                               "admin",
	"GET /api/service-accounts":                           "admin",
	"GET /api/service-accounts/{key}":                     "admin",
	"GET /api/service-accounts/{key}/credentials":         "admin",
	"GET /api/support-sessions/current":                   "admin",
	"GET /api/user-invitations":                           "admin",
	"GET /api/users":                                      "admin",
	"GET /api/users/{id}/automatic-role":                  "admin",
	"GET /api/users/{id}/support-apps":                    "admin",
	"GET /api/workers":                                    "admin",
	"PATCH /api/announcements/{id}":                       "admin",
	"PATCH /api/users/{id}":                               "admin",
	"PATCH /api/users/{id}/password":                      "admin",
	"POST /api/announcements":                             "admin",
	"POST /api/service-accounts/{key}/credentials":        "admin",
	"POST /api/support-sessions":                          "admin",
	"POST /api/user-invitations":                          "admin",
	"POST /api/user-invitations/{id}/replace":             "admin",
	"POST /api/users":                                     "admin",
	"POST /api/users/{id}/revoke-sessions":                "admin",
	"POST /api/workers/{node_id}/revoke":                  "admin",

	"DELETE /api/projects/{slug}":     "operator",
	"GET /api/fleet/schedules/status": "operator",
	"PATCH /api/projects/{slug}":      "operator",
	"POST /api/projects":              "operator",

	"GET /api/fleet/runs/{id}":   "developer",
	"GET /api/users/{username}":  "developer",
	"PATCH /api/fleet/runs/{id}": "developer",
	"POST /api/apps":             "developer",
	"POST /api/fleet/runs":       "developer",
}

// roleProbeBodies holds a valid body for routes that validate their input
// before checking the caller's role, so the probe reaches the role check.
var roleProbeBodies = map[string]string{
	"POST /api/apps": `{"slug":"role-probe","name":"Role probe"}`,
}

// signedInRoles lists the user roles from least to most privileged.
var signedInRoles = []string{"viewer", "developer", "operator", "admin"}

func roleRank(t *testing.T, role string) int {
	t.Helper()
	for i, r := range signedInRoles {
		if r == role {
			return i
		}
	}
	t.Fatalf("unknown role %q", role)
	return -1
}

// probeToken creates a user with the given role and returns a session token
// for it.
func probeToken(t *testing.T, store *db.Store, name, role string) string {
	t.Helper()
	if err := store.CreateUser(db.CreateUserParams{Username: name, PasswordHash: "x", Role: role}); err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	u, err := store.GetUserByUsername(name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	token, err := auth.IssueJWT(u.ID, u.Username, role, "test-secret")
	if err != nil {
		t.Fatalf("issue token for %s: %v", name, err)
	}
	return token
}

// Every authenticated /api route refuses exactly the roles below the least
// role roleGatedAPIRoutes lists for it, so dropping a role check from a
// handler, or adding one nobody recorded, fails here.
func TestAPIRoutesAdmitOnlyTheirRoles(t *testing.T) {
	srv, store := newTestServer(t)
	router := srv.Router()
	routes := apiRoutes(t, router)

	seen := make(map[string]bool, len(routes))
	for i, route := range routes {
		seen[route] = true
		if publicAPIRoutes[route] {
			continue
		}
		least, gated := roleGatedAPIRoutes[route]
		if !gated {
			least = signedInRoles[0]
		}
		body, ok := roleProbeBodies[route]
		if !ok {
			body = "{}"
		}
		method, pattern, _ := strings.Cut(route, " ")
		for _, role := range signedInRoles {
			// Fresh users per route: revoke-sessions and logout invalidate
			// their caller's tokens, which would otherwise turn every later
			// probe into a 401.
			token := probeToken(t, store, fmt.Sprintf("probe%d-%s", i, role), role)
			// A parameter that names no row keeps an admin probe of a
			// destructive route from deleting the probe's own users.
			req := httptest.NewRequest(method, concretePath(pattern, "999999"), strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = fmt.Sprintf("[2001:db8::%x]:40000", i+1)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			if rec.Code == http.StatusUnauthorized {
				t.Fatalf("%s as %s answered 401; the probe's session was not accepted, so the matrix proves nothing", route, role)
			}
			wantRefused := roleRank(t, role) < roleRank(t, least)
			refused := rec.Code == http.StatusForbidden
			switch {
			case wantRefused && !refused:
				t.Errorf("%s admitted role %s (answered %d); it requires at least %s", route, role, rec.Code, least)
			case !wantRefused && refused:
				t.Errorf("%s refused role %s with 403 (%s); record its least role in roleGatedAPIRoutes if that is intended", route, role, strings.TrimSpace(rec.Body.String()))
			}
		}
	}
	for route := range roleGatedAPIRoutes {
		if !seen[route] {
			t.Errorf("roleGatedAPIRoutes lists %s, which is not registered", route)
		}
	}
	for route := range roleProbeBodies {
		if !seen[route] {
			t.Errorf("roleProbeBodies lists %s, which is not registered", route)
		}
	}
}
