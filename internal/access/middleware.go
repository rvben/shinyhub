package access

import (
	"errors"
	"net/http"
	"strings"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
)

type store interface {
	GetAppBySlug(slug string) (*db.App, error)
	UserCanAccessApp(slug string, userID int64) (bool, error)
}

// Middleware returns an HTTP middleware that enforces per-app access control.
// Public apps pass through unconditionally. Private and shared apps require
// a valid JWT from the Authorization header or session cookie, and the
// authenticated user must be the owner or an explicit member. The optional
// RevocationChecker is consulted so tokens revoked on logout can no longer
// reach private apps either.
func Middleware(st store, jwtSecret string, revoked auth.RevocationChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			slug := extractSlug(r.URL.Path)
			if slug == "" {
				next.ServeHTTP(w, r)
				return
			}

			app, err := st.GetAppBySlug(slug)
			if err != nil {
				if errors.Is(err, db.ErrNotFound) {
					next.ServeHTTP(w, r)
					return
				}
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}

			if app.Access == "public" {
				next.ServeHTTP(w, r)
				return
			}

			// Both "private" and "shared" require authentication. Admins,
			// operators, and any authenticated user on shared apps bypass the
			// membership check; other roles on private apps must pass the
			// UserCanAccessApp check.
			user := extractUser(r, jwtSecret, revoked)
			if user == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			// admin, operator, and any authenticated user for shared apps bypass membership check.
			if user.Role == "admin" || user.Role == "operator" || app.Access == "shared" {
				next.ServeHTTP(w, r)
				return
			}

			ok, err := st.UserCanAccessApp(slug, user.ID)
			if err != nil {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			if !ok {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractUser tries to parse a valid JWT from the Authorization header or the
// session cookie. Returns nil if no valid token is found.
func extractUser(r *http.Request, secret string, revoked auth.RevocationChecker) *auth.ContextUser {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token := strings.TrimPrefix(h, "Bearer ")
		if u, err := auth.ParseJWT(token, secret, revoked); err == nil {
			return u
		}
	}

	if c, err := r.Cookie(auth.SessionCookieName); err == nil {
		if u, err := auth.ParseJWT(c.Value, secret, revoked); err == nil {
			return u
		}
	}

	return nil
}

// extractSlug parses the slug from /app/:slug/... paths.
func extractSlug(path string) string {
	trimmed := strings.TrimPrefix(path, "/app/")
	if trimmed == path || trimmed == "" {
		return ""
	}
	return strings.SplitN(trimmed, "/", 2)[0]
}
