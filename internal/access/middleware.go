package access

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/appnav"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/favicon"
	"github.com/rvben/shinyhub/internal/supportui"
)

type store interface {
	GetAppBySlug(slug string) (*db.App, error)
	UserCanAccessApp(slug string, userID int64) (bool, error)
}

type supportSessionObserver interface {
	ObserveSupportSession(id string) error
}

// supportSessionLookup lets the guard page name the session a root guard
// cookie refers to. Stores without it still block; they just cannot say why.
type supportSessionLookup interface {
	GetSupportSession(id string) (*db.SupportSession, error)
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
//
// See WithAppNav for the one optional behaviour: injecting the app switcher
// into the access-denied pages this middleware writes.
func Middleware(st store, jwtSecret string, revoked auth.RevocationChecker, userLookup auth.UserLookup, opts ...Option) func(http.Handler) http.Handler {
	cfg := newOptions(opts)
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

			// Resolve the identity for every app, public included: the proxy
			// Director reads it to forward identity headers, and a public app
			// can still personalize for a signed-in user. An anonymous request
			// resolves to nil and is denied only where decide says so.
			user, token := resolveSession(r, jwtSecret, revoked, userLookup)
			if guard, err := r.Cookie(auth.SupportSessionGuardCookieName); err == nil && (user == nil || user.SupportSession == nil) {
				// The root guard without a valid app-scoped support cookie means
				// this request is outside the app the session is bound to, or the
				// bound app's own cookie is gone. resolveSession has already
				// refused every other identity; serving a public app anonymously
				// here would hide that boundary from the administrator and hand a
				// private app's sign-in page to someone who cannot use it. End the
				// request on a page that says what is going on instead.
				writeSupportPage(w, supportui.GuardOnlyPage(slug, guardedSession(st, guard.Value)))
				return
			}
			if user != nil && user.SupportSession != nil &&
				(user.SupportSession.AppSlug != slug || user.SupportSession.AppID != app.ID) {
				// Do not let a public replacement turn immutable-scope failure into
				// anonymous access. The app must not see this request at all.
				writeSupportPage(w, supportui.ScopeBlockedPage(slug, user.SupportSession.ActorUsername, user.Username, user.SupportSession.ExpiresAt))
				return
			}
			if user != nil && user.SupportSession != nil {
				if observer, ok := st.(supportSessionObserver); ok {
					if err := observer.ObserveSupportSession(user.SupportSession.ID); err != nil {
						writeSupportPage(w, supportui.InactivePage(slug, user.SupportSession.ActorUsername, user.Username, user.SupportSession.ExpiresAt))
						return
					}
				}
			}
			if user != nil {
				ctx := auth.WithUser(r.Context(), user)
				if token != nil {
					// Carried so the proxy can record this session's jti
					// alongside any connection it upgrades. The periodic
					// re-check needs it to close live sessions whose token was
					// revoked on logout.
					ctx = auth.WithTokenInfo(ctx, token)
				}
				r = r.WithContext(ctx)
			} else if auth.UserFromContext(r.Context()) != nil {
				// resolveSession can deliberately reject an upstream forward-auth
				// identity when a support guard/cookie is present. Mask that value in
				// the downstream context too; merely returning nil from resolution
				// would otherwise leave the original administrator visible to the app.
				ctx := auth.WithUser(r.Context(), nil)
				ctx = auth.WithTokenInfo(ctx, nil)
				r = r.WithContext(ctx)
			}

			status, err := decide(st, app, user)
			if err != nil {
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
			switch status {
			case http.StatusUnauthorized:
				writeAccessDenied(w, r, http.StatusUnauthorized, "Sign in to access this app", slug, cfg)
			case http.StatusForbidden:
				writeAccessDenied(w, r, http.StatusForbidden, "You don't have access to this app", slug, cfg)
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

// writeSupportPage answers 409 with a self-contained support safety page. The
// policy admits the page's own inline styles and stop form and nothing else,
// and no-store keeps a stale copy from masking a later state change.
func writeSupportPage(w http.ResponseWriter, page string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusConflict)
	_, _ = w.Write([]byte(page))
}

// guardedSession describes the support session behind a guard cookie value,
// or nil when the store cannot look it up or has no such row. A lookup error
// is treated the same as an unknown ID: the request is blocked either way, and
// the page must not claim a state it could not read.
func guardedSession(st store, id string) *supportui.GuardedSession {
	lookup, ok := st.(supportSessionLookup)
	if !ok {
		return nil
	}
	session, err := lookup.GetSupportSession(id)
	if err != nil || session == nil {
		return nil
	}
	return &supportui.GuardedSession{
		AppSlug:   session.AppSlug,
		Actor:     session.ActorUsername,
		Subject:   session.SubjectUsername,
		ExpiresAt: session.ExpiresAt,
		Active:    session.StoppedAt == nil && session.ExpiresAt.After(time.Now()),
	}
}

// decide is the per-app authorization rule, expressed as the HTTP status the
// request path would answer with: 200 admit, 401 authenticate first, 403 no
// access, 500 could not tell. user is nil for an anonymous caller.
//
// It is deliberately the ONLY place the rule is written. Recheck re-runs this
// same function against the live database for connections that were upgraded
// to WebSockets, where the middleware never gets another turn - so a live
// session can never be governed by a different rule than admission was.
func decide(st store, app *db.App, user *auth.ContextUser) (int, error) {
	if app.Access == "public" {
		return http.StatusOK, nil
	}
	// Both "private" and "shared" require authentication.
	if user == nil {
		return http.StatusUnauthorized, nil
	}
	// admin, operator, and any authenticated user for shared apps bypass the
	// membership check.
	if user.Role == "admin" || user.Role == "operator" || app.Access == "shared" {
		return http.StatusOK, nil
	}
	ok, err := st.UserCanAccessApp(app.Slug, user.ID)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if !ok {
		return http.StatusForbidden, nil
	}
	return http.StatusOK, nil
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
// claim-derived role is used as-is - that path exists only for tests
// that pre-date the live-resolve plumbing.
func extractUser(r *http.Request, secret string, revoked auth.RevocationChecker, userLookup auth.UserLookup) *auth.ContextUser {
	return ResolveOptionalUser(r, secret, revoked, userLookup)
}

// ResolveOptionalUser resolves the authenticated identity from the request's
// session cookie, returning nil when the request is anonymous or the token is
// invalid/revoked. It never writes an HTTP error response, making it suitable
// for optional-auth routes where anonymous callers must still be served.
//
// The resolved user (if any) is NOT placed into the request context by this
// function - callers are responsible for calling auth.WithUser if they want
// to propagate the identity downstream.
func ResolveOptionalUser(r *http.Request, secret string, revoked auth.RevocationChecker, userLookup auth.UserLookup) *auth.ContextUser {
	user, _ := resolveSession(r, secret, revoked, userLookup)
	return user
}

// resolveSession is ResolveOptionalUser plus the session token's metadata. The
// token is nil whenever the identity did not come from a session JWT this
// middleware validated - an upstream forward-auth identity already in the
// request context has no jti of ours to revoke.
func resolveSession(r *http.Request, secret string, revoked auth.RevocationChecker, userLookup auth.UserLookup) (*auth.ContextUser, *auth.TokenInfo) {
	if _, err := r.Cookie(auth.SupportSessionCookieName); err == nil {
		user, token, authErr := auth.AuthenticateSupportSession(r, secret, userLookup, revoked)
		if authErr != nil {
			// Presence of an invalid support cookie fails closed. Falling back to
			// forward-auth here could silently turn a broken support session into
			// the admin's unrestricted identity inside the app.
			return nil, nil
		}
		return user, token
	}
	if _, err := r.Cookie(auth.SupportSessionGuardCookieName); err == nil {
		// The root-scoped guard accompanies the path-scoped support cookie. If
		// the latter is absent, this request is outside the authorized app and
		// must not fall back to an admin session or forward-auth identity.
		return nil, nil
	}
	if u := auth.UserFromContext(r.Context()); u != nil {
		return u, auth.TokenInfoFromContext(r.Context())
	}
	user, token, err := auth.AuthenticateBrowserSession(r, secret, userLookup, revoked)
	if err != nil {
		return nil, nil
	}
	return user, token
}

// writeAccessDenied returns a styled HTML page for browser navigation requests
// (so the user sees a real "sign in" affordance instead of plain text), and a
// JSON envelope for API requests so existing CLI/SDK clients keep parsing the
// same shape they always have.
//
// The HTML page intentionally does NOT include the app's name. Anything in
// app metadata (name, project) is private — leaking it on the access-denied
// path would let an unauthenticated caller enumerate private app titles by
// guessing slugs. The app switcher cfg may add does not weaken that: it names
// only apps the caller is separately authorized to see, and the denied app is
// by definition not among them.
func writeAccessDenied(w http.ResponseWriter, r *http.Request, status int, headline, slug string, cfg options) {
	if wantsHTML(r) {
		nextURL := r.URL.RequestURI()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		page := renderAccessDeniedPage(status, headline, nextURL)
		if user := auth.UserFromContext(r.Context()); user != nil && user.SupportSession != nil && user.SupportSession.AppSlug == slug {
			// Access can be withdrawn while a support session is active. Preserve
			// the safety rail and its exit control on the resulting 403 instead of
			// leaving the administrator stranded on an unlabelled identity state.
			support := user.SupportSession
			if out, ok := appnav.SpliceIntoBody(page, supportui.Snippet(slug, support.ActorUsername, user.Username, support.ID, support.ExpiresAt)); ok {
				page = out
			} else {
				page = []byte(supportui.BlockedPage(slug, support.ActorUsername, user.Username, support.ExpiresAt))
			}
			_, _ = w.Write(page)
			return
		}
		// Keep the display name empty here: this caller was not authorized to
		// see the app, so the switcher must not disclose its private metadata.
		_, _ = w.Write(cfg.withAppNav(page, slug, ""))
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
// benefit from a styled HTML response. We rely on standard browser fetch
// metadata: Sec-Fetch-Mode: navigate is set by all current browsers on
// top-level navigations, and Accept: text/html covers older clients and the
// occasional curl-with-headers smoke test.
//
// We deliberately do NOT short-circuit on Authorization: an embedded Shiny
// app may forward its own Bearer token on top-level navigations to /app/*.
// Treating that as "this is a CLI, return JSON" would silently swap the
// styled access-denied page for a raw JSON body in the browser tab. CLI
// callers send neither Sec-Fetch-Mode: navigate nor Accept: text/html, so
// they correctly fall through to JSON without any explicit header check.
func wantsHTML(r *http.Request) bool {
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
//   - 401 (no session): a plain anchor to /login?next=<original>. The SPA
//     renders the login form; after success consumeNextParam() hard-navigates
//     to <original>.
//
//   - 403 (wrong session): an HTML <form> that POSTs to /api/auth/handoff
//     with `next=<original>` as a hidden field. The endpoint revokes the
//     current session server-side, clears the cookie, and 303-redirects to
//     /login?next=<original>. Using a form POST instead of an `<a href>` to
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
	var page []byte
	if status == http.StatusForbidden {
		page = renderHandoffPage(headline, nextURL)
	} else {
		page = renderLoginRedirectPage(headline, nextURL)
	}
	page, _ = favicon.Ensure(page, favicon.PlatformURL)
	return page
}

func renderLoginRedirectPage(headline, nextURL string) []byte {
	loginHref := "/login"
	if nextURL != "" {
		loginHref = "/login?" + url.Values{"next": {nextURL}}.Encode()
	}
	const tpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>HEADLINE · ShinyHub</title>
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
<title>HEADLINE · ShinyHub</title>
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
