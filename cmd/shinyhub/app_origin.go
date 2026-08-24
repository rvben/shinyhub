package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/favicon"
	"github.com/rvben/shinyhub/internal/proxytrust"
)

const appLaunchQueryParam = "__shinyhub_launch"

type appLaunchStore interface {
	CreateAppLaunchCode(codeHash string, userID int64, appSlug string) error
	ConsumeAppLaunchCode(codeHash, appSlug string) (*auth.ContextUser, error)
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
	token, err := auth.IssueSessionToken(user, jwtSecret)
	if err != nil {
		http.Error(w, "could not create app session", http.StatusInternalServerError)
		return
	}
	auth.SetSessionCookie(w, r, token, trustedNets)
	query := r.URL.Query()
	query.Del(appLaunchQueryParam)
	clean := *r.URL
	clean.RawQuery = query.Encode()
	clean.Fragment = ""
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, clean.RequestURI(), http.StatusSeeOther)
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
	return strings.EqualFold(normalizeHTTPSHost(a), normalizeHTTPSHost(b))
}

func normalizeHTTPSHost(host string) string {
	u, err := url.Parse("//" + strings.TrimSpace(host))
	if err != nil || u.Hostname() == "" {
		return strings.TrimSuffix(strings.TrimSpace(host), ".")
	}
	name := strings.TrimSuffix(u.Hostname(), ".")
	if port := u.Port(); port != "" && port != "443" {
		return net.JoinHostPort(name, port)
	}
	return name
}
