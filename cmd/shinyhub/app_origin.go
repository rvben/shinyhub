package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/apporigin"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/originhost"
	"github.com/rvben/shinyhub/internal/proxytrust"
	"github.com/rvben/shinyhub/internal/supportui"
)

const appLaunchQueryParam = "__shinyhub_launch"

// appOriginTrustWarning returns the startup warning to log when no dedicated
// app_origin is configured, or the empty string when appOrigin is set (in
// which case apps run isolated from the dashboard and no warning applies).
func appOriginTrustWarning(appOrigin string) string {
	if appOrigin != "" {
		return ""
	}
	return "app_origin not configured: apps run same-origin with the dashboard, so every deployed app is trusted with dashboard users' sessions; set server.app_origin to isolate application traffic"
}

type appLaunchStore interface {
	CreateAppLaunchCodeWithSession(codeHash string, userID int64, appSlug string, session *auth.TokenInfo, epoch int64) error
	ConsumeAppLaunchCodeWithSession(codeHash, appSlug string) (*auth.ContextUser, *auth.TokenInfo, error)
	ActivateSupportSession(id, jti string, expiresAt time.Time) error
	AbortSupportSession(id, reason string) error
}

// appOriginBoundary makes the configured app origin a narrow virtual host. It
// deliberately exposes only app proxy traffic, liveness probes, and the public
// platform favicon; the API, dashboard, static assets, and internal bundle
// endpoint remain unreachable on an origin whose JavaScript is not trusted.
func appOriginBoundary(next http.Handler, appOrigin *url.URL, trustedNets []*net.IPNet) http.Handler {
	if appOrigin == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !sameHost(proxytrust.Host(r, trustedNets), appOrigin.Host) {
			next.ServeHTTP(w, r)
			return
		}
		if apporigin.Admits(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})
}

// appOriginDispatch serves the proxy on the app origin and turns a successful
// control-origin access check into a one-time launch redirect. The access
// middleware must wrap controlHandler so private-app authorization happens
// before a launch capability is created.
func appOriginDispatch(
	appOrigin *url.URL,
	trustedNets []*net.IPNet,
	store appLaunchStore,
	jwtSecret string,
	controlHandler, appHandler http.Handler,
	policies ...config.AuthConfig,
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sameHost(proxytrust.Host(r, trustedNets), appOrigin.Host) {
			if rawCode := r.URL.Query().Get(appLaunchQueryParam); rawCode != "" {
				consumeAppLaunchWithSharedHost(w, r, store, jwtSecret, trustedNets, rawCode, false, policies...)
				return
			}
			appHandler.ServeHTTP(w, r)
			return
		}
		controlHandler.ServeHTTP(w, r)
	})
}

func appOriginRedirectHandler(store appLaunchStore, appOrigin *url.URL, trusted []*net.IPNet) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "app traffic must use the configured app origin", http.StatusMisdirectedRequest)
			return
		}
		slug := appSlugFromPath(r.URL.Path)
		if slug == "" {
			http.NotFound(w, r)
			return
		}

		target := *appOrigin
		target.Path = r.URL.Path
		target.RawPath = r.URL.RawPath
		query := r.URL.Query()
		query.Del(appLaunchQueryParam)

		// Public apps may be launched anonymously. When a signed-in user opens a
		// public app we still exchange their identity so optional identity headers
		// keep working on the isolated origin.
		if user := auth.UserFromContext(r.Context()); user != nil {
			rawCode, codeHash, err := newAppLaunchCode()
			if err != nil || store.CreateAppLaunchCodeWithSession(codeHash, user.ID, slug, appLaunchSession(r, trusted), user.TokenEpoch) != nil {
				http.Error(w, "could not create app session", http.StatusInternalServerError)
				return
			}
			query.Set(appLaunchQueryParam, rawCode)
		}
		target.RawQuery = query.Encode()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.Redirect(w, r, target.String(), http.StatusSeeOther)
	})
}

func consumeAppLaunch(w http.ResponseWriter, r *http.Request, store appLaunchStore, jwtSecret string, trustedNets []*net.IPNet, rawCode string) {
	consumeAppLaunchWithSharedHost(w, r, store, jwtSecret, trustedNets, rawCode, false)
}

// trustedAppSupportDispatch exchanges support capabilities on the control host.
// App authorization still prefers support credentials and the fallback guard;
// the dashboard continues to authenticate using the ordinary admin cookie.
func trustedAppSupportDispatch(next http.Handler, store appLaunchStore, jwtSecret string, trustedNets []*net.IPNet) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code := r.URL.Query().Get(appLaunchQueryParam); code != "" {
			consumeAppLaunchWithSharedHost(w, r, store, jwtSecret, trustedNets, code, true)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func consumeAppLaunchWithSharedHost(w http.ResponseWriter, r *http.Request, store appLaunchStore, jwtSecret string, trustedNets []*net.IPNet, rawCode string, sharedHost bool, policies ...config.AuthConfig) {
	if r.Method != http.MethodGet {
		http.Error(w, "invalid app launch", http.StatusBadRequest)
		return
	}
	slug := appSlugFromPath(r.URL.Path)
	if slug == "" {
		http.NotFound(w, r)
		return
	}
	sum := sha256.Sum256([]byte(rawCode))
	user, original, err := store.ConsumeAppLaunchCodeWithSession(hex.EncodeToString(sum[:]), slug)
	if err != nil || user == nil {
		// Do not disclose database health or whether a code ever existed.
		http.Error(w, "app launch expired or already used", http.StatusUnauthorized)
		return
	}
	// A shared-host launch must never replace the administrator's cookie with
	// an ordinary app identity, even if a stale ordinary launch code is supplied.
	if sharedHost && user.SupportSession == nil {
		http.Error(w, "support launch required", http.StatusForbidden)
		return
	}
	if user.SupportSession == nil && (original == nil || original.JTI == "") && auth.ForwardAuthSignedOut(r, trustedNets) {
		http.Error(w, "launch requires a current browser session", http.StatusUnauthorized)
		return
	}
	var token string
	var tokenInfo *auth.TokenInfo
	if user.SupportSession != nil {
		token, tokenInfo, err = auth.IssueSessionTokenWithInfo(user, jwtSecret)
	} else {
		policy := config.AuthConfig{}
		if len(policies) > 0 {
			policy = policies[0]
		}
		var authTime time.Time
		var jti string
		if original != nil {
			authTime, jti = original.AuthTime, original.JTI
		}
		token, tokenInfo, err = auth.IssueBrowserSession(user, jwtSecret, authTime, policy.BrowserSessionTTL(), policy.BrowserSessionMaxAge(), jti)
	}
	if err != nil {
		if user.SupportSession != nil {
			_ = store.AbortSupportSession(user.SupportSession.ID, "launch_failed")
		}
		if err == auth.ErrSessionExpired {
			http.Error(w, "session expired; sign in again", http.StatusUnauthorized)
			return
		}
		http.Error(w, "could not create app session", http.StatusInternalServerError)
		return
	}
	if user.SupportSession == nil && original != nil {
		if original.ForwardAuthSuppressed {
			auth.SetForwardAuthSignedOut(w, r, trustedNets)
		}
		if original.ForwardAuthFamily {
			forwarded := auth.UserFromContext(r.Context())
			if auth.ForwardAuthFromContext(r.Context()) && forwarded != nil && forwarded.ID == user.ID {
				auth.ClearForwardAuthSignedOut(w, r, trustedNets)
			}
			if len(policies) > 0 && policies[0].ForwardAuth.Enabled {
				tracking, _, trackErr := auth.IssueForwardAuthSession(user, jwtSecret, tokenInfo.AuthTime, policies[0].BrowserSessionMaxAge(), tokenInfo.JTI)
				if trackErr != nil {
					http.Error(w, "could not create app session", http.StatusServiceUnavailable)
					return
				}
				auth.SetForwardAuthSessionCookie(w, r, tracking, trustedNets)
			}
		}
	}
	if support := user.SupportSession; support != nil {
		if support.AppSlug != slug || store.ActivateSupportSession(support.ID, tokenInfo.JTI, tokenInfo.ExpiresAt) != nil {
			_ = store.AbortSupportSession(support.ID, "activation_failed")
			http.Error(w, "could not create app session", http.StatusInternalServerError)
			return
		}
		// Remove any ordinary app-origin identity and install a root guard so
		// leaving this slug cannot fall back to the administrator via a stale
		// cookie or forward-auth context. On a shared host retain the admin
		// cookie for dashboard/API use; app access is still guarded.
		if !sharedHost {
			auth.ClearSessionCookie(w, r, trustedNets)
		}
		auth.SetSupportSessionGuardCookie(w, r, support.ID, tokenInfo.ExpiresAt, trustedNets)
		auth.SetSupportSessionCookie(w, r, token, slug, tokenInfo.ExpiresAt, trustedNets)
	} else {
		auth.SetSessionCookieUntil(w, r, token, tokenInfo.ExpiresAt, trustedNets)
	}
	query := r.URL.Query()
	query.Del(appLaunchQueryParam)
	if original != nil && original.ForwardAuthFamily && len(policies) > 0 && policies[0].ForwardAuth.Enabled {
		query.Set(auth.ForwardAuthCookieCheckParam, "1")
	}
	clean := *r.URL
	clean.RawQuery = query.Encode()
	clean.Fragment = ""
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, clean.RequestURI(), http.StatusSeeOther)
}

func supportSessionStopHandler(store *db.Store, jwtSecret, returnURL string, trustedNets []*net.IPNet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		slug := r.PathValue("slug")
		user, _, err := auth.AuthenticateSupportSessionForStop(r, jwtSecret)
		if err != nil || user == nil || user.SupportSession == nil || user.SupportSession.AppSlug != slug {
			auth.ClearSupportSessionCookie(w, r, slug, trustedNets)
			if !strings.Contains(r.Header.Get("Accept"), "application/json") {
				http.Redirect(w, r, returnURL, http.StatusSeeOther)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "support session is no longer active", "return_url": returnURL})
			return
		}
		support := user.SupportSession
		_, err = store.StopSupportSession(support.ID, "ended_by_actor", proxytrust.ClientIP(r, trustedNets))
		if err != nil {
			if !strings.Contains(r.Header.Get("Accept"), "application/json") {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				w.Header().Set("Content-Security-Policy", supportui.PageCSP)
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(supportui.BlockedPageWithError(slug, support.ActorUsername, user.Username,
					"The support session could not be ended. Try again; automatic expiry remains in force.", support.ExpiresAt)))
				return
			}
			http.Error(w, "could not end support session", http.StatusInternalServerError)
			return
		}
		auth.ClearSupportSessionCookie(w, r, slug, trustedNets)
		if !strings.Contains(r.Header.Get("Accept"), "application/json") {
			http.Redirect(w, r, returnURL, http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]string{"return_url": returnURL})
	}
}

func newAppLaunchCode() (raw, hash string, err error) {
	var buf [32]byte
	if _, err = rand.Read(buf[:]); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(buf[:])
	sum := sha256.Sum256([]byte(raw))
	return raw, hex.EncodeToString(sum[:]), nil
}

func appSlugFromPath(path string) string {
	rest := strings.TrimPrefix(path, "/app/")
	if rest == path || rest == "" {
		return ""
	}
	slug, _, _ := strings.Cut(rest, "/")
	return slug
}

func sameHost(a, b string) bool {
	canonicalA, errA := originhost.Authority(a)
	canonicalB, errB := originhost.Authority(b)
	return errA == nil && errB == nil && canonicalA == canonicalB
}

func appLaunchSession(r *http.Request, trusted []*net.IPNet) *auth.TokenInfo {
	ti := auth.TokenInfoFromContext(r.Context())
	if ti == nil {
		return nil
	}
	session := *ti
	session.ForwardAuthFamily = auth.ForwardAuthFromContext(r.Context())
	session.ForwardAuthSuppressed = auth.ForwardAuthSignedOut(r, trusted)
	return &session
}

// Intercept launches before specific app routes, so adding a launch query can
// never bypass family checks on favicons, navigation or session endpoints.
func appOriginLaunchDispatch(next http.Handler, origin *url.URL, store appLaunchStore, secret string, trusted []*net.IPNet, policy config.AuthConfig) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sameHost(proxytrust.Host(r, trusted), origin.Host) {
			if code := r.URL.Query().Get(appLaunchQueryParam); code != "" {
				consumeAppLaunchWithSharedHost(w, r, store, secret, trusted, code, false, policy)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
