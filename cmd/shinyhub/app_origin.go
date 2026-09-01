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

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/favicon"
	"github.com/rvben/shinyhub/internal/originhost"
	"github.com/rvben/shinyhub/internal/proxytrust"
	"github.com/rvben/shinyhub/internal/supportui"
)

const appLaunchQueryParam = "__shinyhub_launch"

type appLaunchStore interface {
	CreateAppLaunchCode(codeHash string, userID int64, appSlug string) error
	ConsumeAppLaunchCode(codeHash, appSlug string) (*auth.ContextUser, error)
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
		if strings.HasPrefix(r.URL.Path, "/app/") || r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == favicon.RootURL {
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
) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sameHost(proxytrust.Host(r, trustedNets), appOrigin.Host) {
			if rawCode := r.URL.Query().Get(appLaunchQueryParam); rawCode != "" {
				consumeAppLaunch(w, r, store, jwtSecret, trustedNets, rawCode)
				return
			}
			appHandler.ServeHTTP(w, r)
			return
		}
		controlHandler.ServeHTTP(w, r)
	})
}

func appOriginRedirectHandler(store appLaunchStore, appOrigin *url.URL) http.Handler {
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
			if err != nil || store.CreateAppLaunchCode(codeHash, user.ID, slug) != nil {
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
	user, err := store.ConsumeAppLaunchCode(hex.EncodeToString(sum[:]), slug)
	if err != nil || user == nil {
		// Do not disclose database health or whether a code ever existed.
		http.Error(w, "app launch expired or already used", http.StatusUnauthorized)
		return
	}
	token, tokenInfo, err := auth.IssueSessionTokenWithInfo(user, jwtSecret)
	if err != nil {
		if user.SupportSession != nil {
			_ = store.AbortSupportSession(user.SupportSession.ID, "launch_failed")
		}
		http.Error(w, "could not create app session", http.StatusInternalServerError)
		return
	}
	if support := user.SupportSession; support != nil {
		if support.AppSlug != slug || store.ActivateSupportSession(support.ID, tokenInfo.JTI, tokenInfo.ExpiresAt) != nil {
			_ = store.AbortSupportSession(support.ID, "activation_failed")
			http.Error(w, "could not create app session", http.StatusInternalServerError)
			return
		}
		// Remove any ordinary app-origin identity and install a root guard so
		// leaving this slug cannot fall back to the administrator via a stale
		// cookie or forward-auth context.
		auth.ClearSessionCookie(w, r, trustedNets)
		auth.SetSupportSessionGuardCookie(w, r, support.ID, tokenInfo.ExpiresAt, trustedNets)
		auth.SetSupportSessionCookie(w, r, token, slug, tokenInfo.ExpiresAt, trustedNets)
	} else {
		auth.SetSessionCookie(w, r, token, trustedNets)
	}
	query := r.URL.Query()
	query.Del(appLaunchQueryParam)
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
				w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'")
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
