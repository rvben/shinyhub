package access

import (
	"errors"
	"net/http"
	"net/url"
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
				writeAccessDenied(w, r, http.StatusUnauthorized, "Sign in to access this app", app.Name)
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
				writeAccessDenied(w, r, http.StatusForbidden, "You don't have access to this app", app.Name)
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

// writeAccessDenied returns a styled HTML page for browser navigation requests
// (so the user sees a real "sign in" affordance instead of plain text), and a
// JSON envelope for API requests so existing CLI/SDK clients keep parsing the
// same shape they always have.
func writeAccessDenied(w http.ResponseWriter, r *http.Request, status int, headline, appName string) {
	if wantsHTML(r) {
		nextURL := r.URL.RequestURI()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(renderAccessDeniedPage(headline, appName, nextURL))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"error":"` + httpStatusErrorString(status) + `"}`))
}

func httpStatusErrorString(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "unauthorized"
	case http.StatusForbidden:
		return "forbidden"
	default:
		return http.StatusText(status)
	}
}

// wantsHTML reports whether the request is a browser navigation that would
// benefit from a styled HTML response. We treat presence of an Authorization
// header (CLI/SDK) as definitive: those callers always want JSON. Otherwise
// the standard browser fetch metadata (Sec-Fetch-Mode: navigate) and Accept
// header heuristics are applied.
func wantsHTML(r *http.Request) bool {
	if r.Header.Get("Authorization") != "" {
		return false
	}
	if r.Header.Get("Sec-Fetch-Mode") == "navigate" {
		return true
	}
	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "text/html") {
		return true
	}
	return false
}

func renderAccessDeniedPage(headline, appName, nextURL string) []byte {
	loginHref := "/"
	if nextURL != "" {
		loginHref = "/?next=" + url.QueryEscape(nextURL)
	}
	const tpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>HEADLINE — APP</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
         display: flex; align-items: center; justify-content: center;
         height: 100vh; margin: 0; background: #f8f9fa; color: #212529; }
  .box { text-align: center; max-width: 420px; padding: 0 1rem; }
  h1   { font-size: 1.25rem; margin: 0 0 0.5rem; color: #495057; }
  p    { color: #868e96; font-size: 0.875rem; line-height: 1.4; margin: 0 0 1.25rem; }
  a.btn { display: inline-block; padding: 0.55rem 1.1rem; font-size: 0.875rem;
          background: #0d6efd; color: #fff; border-radius: 4px;
          text-decoration: none; }
  a.btn:hover { background: #0b5ed7; }
</style>
</head>
<body>
  <div class="box">
    <h1>HEADLINE</h1>
    <p>The app <strong>APP</strong> is private. Log in with an account that has access.</p>
    <a class="btn" href="LOGIN">Log in</a>
  </div>
</body>
</html>`
	out := strings.NewReplacer(
		"HEADLINE", htmlEscape(headline),
		"APP", htmlEscape(appName),
		"LOGIN", htmlEscape(loginHref),
	).Replace(tpl)
	return []byte(out)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&#39;")
	return r.Replace(s)
}

// extractSlug parses the slug from /app/:slug/... paths.
func extractSlug(path string) string {
	trimmed := strings.TrimPrefix(path, "/app/")
	if trimmed == path || trimmed == "" {
		return ""
	}
	return strings.SplitN(trimmed, "/", 2)[0]
}
