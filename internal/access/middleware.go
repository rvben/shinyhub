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
//
// userLookup, when supplied, re-resolves the JWT-claimed user against the
// live database on every request — this is what makes role demotions and
// account deletions take effect immediately. Without it, an admin with a
// valid JWT keeps the admin-bypass path through this middleware until the
// token expires (potentially hours), even after being demoted to "user" or
// deleted entirely. Production wiring MUST supply this; tests may pass nil
// when they want to assert behaviour purely from the JWT claims.
func Middleware(st store, jwtSecret string, revoked auth.RevocationChecker, userLookup auth.UserLookup) func(http.Handler) http.Handler {
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
			user := extractUser(r, jwtSecret, revoked, userLookup)
			if user == nil {
				writeAccessDenied(w, r, http.StatusUnauthorized, "Sign in to access this app")
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
				writeAccessDenied(w, r, http.StatusForbidden, "You don't have access to this app")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractUser authenticates the request strictly from the session cookie.
// Authorization headers are intentionally ignored: /app/* is the path a
// Shiny app's own frontend uses to talk back to its own backend, and
// those calls regularly carry an `Authorization: Bearer ...` (or `Basic`)
// header meant for the embedded app. Routing that header into ShinyHub's
// JWT validator would reject perfectly valid browser sessions with a
// spurious 401. CLI/SDK callers use /api/* instead.
//
// When userLookup is supplied, the JWT-claimed identity is re-resolved
// against the live database on every request; this defeats stale-claim
// attacks where a demoted admin's still-valid JWT would otherwise keep
// granting bypass access until token expiry. With nil userLookup the
// claim-derived role is used as-is — that path exists only for tests
// that pre-date the live-resolve plumbing.
func extractUser(r *http.Request, secret string, revoked auth.RevocationChecker, userLookup auth.UserLookup) *auth.ContextUser {
	user, _, err := auth.AuthenticateBrowserSession(r, secret, userLookup, revoked)
	if err != nil {
		return nil
	}
	return user
}

// writeAccessDenied returns a styled HTML page for browser navigation requests
// (so the user sees a real "sign in" affordance instead of plain text), and a
// JSON envelope for API requests so existing CLI/SDK clients keep parsing the
// same shape they always have.
//
// The HTML page intentionally does NOT include the app's name. Anything in
// app metadata (name, project) is private — leaking it on the access-denied
// path would let an unauthenticated caller enumerate private app titles by
// guessing slugs.
func writeAccessDenied(w http.ResponseWriter, r *http.Request, status int, headline string) {
	if wantsHTML(r) {
		nextURL := r.URL.RequestURI()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		_, _ = w.Write(renderAccessDeniedPage(status, headline, nextURL))
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

// renderAccessDeniedPage builds the styled HTML shown to a browser that hit a
// private app without (401) or with the wrong (403) credentials. The body
// never names the app — see writeAccessDenied for the rationale.
//
// The CTA differs by status so the user reaches the login form by the right
// path:
//
//   - 401 (no session): a plain anchor to /?next=<original>. The SPA renders
//     the login form; after success consumeNextParam() hard-navigates to
//     <original>.
//
//   - 403 (wrong session): an HTML <form> that POSTs to /api/auth/handoff
//     with `next=<original>` as a hidden field. The endpoint revokes the
//     current session server-side, clears the cookie, and 303-redirects to
//     /?next=<original>. Using a form POST instead of an `<a href>` to
//     /?logout=1 means the handoff works even when the access-denied page
//     was opened in a brand-new tab (Cmd+Click / Ctrl+Click on a link in
//     the address bar): the previous design depended on a sessionStorage
//     marker planted by an onclick handler in the same tab, and the new
//     tab had no such marker — so the user bounced straight back to the
//     same 403. The form POST has no per-tab dependency.
//
// The button label tracks the same distinction: "Log in" for 401,
// "Sign in as a different user" for 403.
func renderAccessDeniedPage(status int, headline, nextURL string) []byte {
	if status == http.StatusForbidden {
		return renderHandoffPage(headline, nextURL)
	}
	return renderLoginRedirectPage(headline, nextURL)
}

func renderLoginRedirectPage(headline, nextURL string) []byte {
	loginHref := "/"
	if nextURL != "" {
		loginHref = "/?" + url.Values{"next": {nextURL}}.Encode()
	}
	const tpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>HEADLINE</title>
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
    <p>This app is private. Sign in to continue.</p>
    <a class="btn" href="LOGIN">Log in</a>
  </div>
</body>
</html>`
	out := strings.NewReplacer(
		"HEADLINE", htmlEscape(headline),
		"LOGIN", htmlEscape(loginHref),
	).Replace(tpl)
	return []byte(out)
}

func renderHandoffPage(headline, nextURL string) []byte {
	const tpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>HEADLINE</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
         display: flex; align-items: center; justify-content: center;
         height: 100vh; margin: 0; background: #f8f9fa; color: #212529; }
  .box { text-align: center; max-width: 420px; padding: 0 1rem; }
  h1   { font-size: 1.25rem; margin: 0 0 0.5rem; color: #495057; }
  p    { color: #868e96; font-size: 0.875rem; line-height: 1.4; margin: 0 0 1.25rem; }
  form { margin: 0; }
  button.btn { display: inline-block; padding: 0.55rem 1.1rem; font-size: 0.875rem;
               background: #0d6efd; color: #fff; border: 0; border-radius: 4px;
               cursor: pointer; font-family: inherit; }
  button.btn:hover { background: #0b5ed7; }
</style>
</head>
<body>
  <div class="box">
    <h1>HEADLINE</h1>
    <p>Your account doesn't have access to this app. Sign in with a different account.</p>
    <form method="POST" action="/api/auth/handoff">
      <input type="hidden" name="next" value="NEXT">
      <button type="submit" class="btn">Sign in as a different user</button>
    </form>
  </div>
</body>
</html>`
	out := strings.NewReplacer(
		"HEADLINE", htmlEscape(headline),
		"NEXT", htmlEscape(nextURL),
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
