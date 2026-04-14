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
// a valid JWT from the Authorization header or shiny_session cookie, and the
// authenticated user must be the owner or an explicit member.
func Middleware(st store, jwtSecret string) func(http.Handler) http.Handler {
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

			// Both "private" and "shared" require authentication and an explicit
			// member or owner grant. The distinction is surfaced to the UI/CLI
			// to convey intent, but the enforcement logic is identical: present
			// a valid JWT and pass the UserCanAccessApp check.
			user := extractUser(r, jwtSecret)
			if user == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			if user.Role == "admin" {
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
// shiny_session cookie. Returns nil if no valid token is found.
func extractUser(r *http.Request, secret string) *auth.ContextUser {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		token := strings.TrimPrefix(h, "Bearer ")
		if u, err := auth.ParseJWT(token, secret); err == nil {
			return u
		}
	}

	if c, err := r.Cookie("shiny_session"); err == nil {
		if u, err := auth.ParseJWT(c.Value, secret); err == nil {
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
