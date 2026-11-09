package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/rvben/shinyhub/internal/access"
	"github.com/rvben/shinyhub/internal/announcementui"
	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/favicon"
	"github.com/rvben/shinyhub/internal/httpcompress"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/proxytrust"
	"github.com/rvben/shinyhub/internal/ui"
)

// httpHandlerDeps is what the server's top-level HTTP handler reads. Each
// field is built by runServe before the listener opens.
type httpHandlerDeps struct {
	cfg            *config.Config
	store          *db.Store
	srv            *api.Server
	prx            *proxy.Proxy
	readyCh        <-chan struct{}
	ownerAndReady  func() bool
	bundleTokenKey []byte
	appUserLookup  auth.UserLookup
}

// buildHTTPHandler assembles the handler the main listener serves: the
// dashboard API, the access-controlled app routes, the probes, static assets
// and runtime bundles on one mux, wrapped in the app-origin launch dispatch,
// forward auth, the app-origin boundary, compression, panic recovery and the
// security headers, outermost last.
func buildHTTPHandler(d httpHandlerDeps) (http.Handler, error) {
	cfg, store, srv, prx := d.cfg, d.store, d.srv, d.prx
	readyCh, ownerAndReady, bundleTokenKey, appUserLookup := d.readyCh, d.ownerAndReady, d.bundleTokenKey, d.appUserLookup

	mux := http.NewServeMux()
	mux.Handle("/api/", apiHandler(srv))
	// The pages these two middlewares answer with instead of the app - denied,
	// and awaiting a first deploy - are dead ends for the same reason app pages
	// are, so they carry the same switcher. Nothing but the switcher varies:
	// Support recovery stays available independently of the optional app switcher.
	navOpts := []access.Option{access.WithSupportDashboard(strings.TrimRight(cfg.Server.BaseURL, "/") + "/users")}
	navOpts = append(navOpts, access.WithAnnouncements(), access.WithEnvironment(cfg.Branding.Environment))
	if cfg.Server.AppNavEnabled() {
		navOpts = append(navOpts, access.WithAppNav(appNavHomeURL(cfg)))
	}
	proxyEmptyState := access.NeverDeployedMiddleware(store, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup, cfg.TrustedProxyNets, navOpts...)(prx)
	appHandler := access.Middleware(store, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup, navOpts...)(proxyEmptyState)
	var parsedAppOrigin *url.URL
	// With no dedicated app_origin, every deployed app runs same-origin with the
	// dashboard, so ShinyHub trusts every deployed app with dashboard users'
	// sessions. Warn loudly so that is a deliberate choice, not an oversight -
	// the same posture the empty-app_origin admin banner and docs/security.md's
	// deployment posture section both flag.
	if msg := appOriginTrustWarning(cfg.Server.AppOrigin); msg != "" {
		slog.Warn(msg)
	}
	if cfg.Server.AppOrigin != "" {
		parsedAppOrigin, _ = url.Parse(cfg.Server.AppOrigin) // validated by config.Load
		redirect := appOriginRedirectHandler(store, parsedAppOrigin, cfg.TrustedProxyNets)
		redirectEmptyState := access.NeverDeployedMiddleware(store, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup, cfg.TrustedProxyNets, navOpts...)(redirect)
		controlAppHandler := access.Middleware(store, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup, navOpts...)(redirectEmptyState)
		appHandler = appOriginDispatch(parsedAppOrigin, cfg.TrustedProxyNets, store, cfg.Auth.Secret, controlAppHandler, appHandler, cfg.Auth)
	} else if cfg.Auth.SupportSessions && cfg.Auth.SupportSessionsTrustedApps {
		slog.Warn("trusted-app support sessions enabled: malicious or compromised app JavaScript may act with the administrator's browser authority; only deploy trusted app code")
		appHandler = trustedAppSupportDispatch(appHandler, store, cfg.Auth.Secret, cfg.TrustedProxyNets)
	}
	mux.Handle("/app/", appHandler)
	// App-local renewal is always available, including on the isolated app
	// origin and when the optional app switcher is disabled. Access middleware
	// revalidates the browser identity and app permissions on every request.
	if parsedAppOrigin != nil {
		mux.HandleFunc("GET "+auth.AppLogoutPath, func(w http.ResponseWriter, r *http.Request) {
			if !sameHost(proxytrust.Host(r, cfg.TrustedProxyNets), parsedAppOrigin.Host) {
				http.NotFound(w, r)
				return
			}
			srv.HandleAppLogout(w, r)
		})
	}
	appSessionHandler := access.Middleware(store, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup)(http.HandlerFunc(srv.HandleAppSession))
	mux.Handle("GET /app/{slug}/.shinyhub/session.json", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		appSessionHandler.ServeHTTP(w, r)
	}))
	mux.HandleFunc("GET /app/{slug}/.shinyhub/announcements.json", srv.HandleActiveAnnouncements)
	mux.Handle("/app/{slug}/.shinyhub/availability", appAvailabilityHandler(cfg.Server.PublicAppAvailability, store, prx, readyCh))
	if cfg.Auth.SupportSessions {
		mux.HandleFunc("POST /app/{slug}/.shinyhub/support-session/stop",
			supportSessionStopHandler(store, cfg.Auth.Secret, strings.TrimRight(cfg.Server.BaseURL, "/")+"/users?support=ended", cfg.TrustedProxyNets))
	}
	// The per-app favicon is access-controlled exactly like the app. Register it
	// as a more-specific pattern than /app/ so it never reaches the app backend.
	platformFavicon := ui.FaviconHandler(cfg.Branding)
	appIconHandler := access.Middleware(store, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup)(
		appFaviconHandler(store, platformFavicon, cfg.Branding.Environment != nil),
	)
	mux.Handle("GET /app/{slug}"+favicon.AppSuffix, appIconHandler)
	// The switcher's own data, served from under /app/ because that is the only
	// prefix the isolated app origin admits (appOriginBoundary) - a fleet-wide
	// path would be unreachable from the very pages that need it. Registered as
	// its own pattern rather than behind the access middleware: the answer is
	// the caller's own app list, so being refused THIS app must not refuse it.
	// A more specific pattern than "/app/", so ServeMux prefers it here and
	// leaves every other app path to the proxy.
	//
	// Registered only when the switcher is on, so app_nav: false means the
	// feature is absent rather than merely unused: nothing under /app/ answers
	// differently than it did before the switcher existed.
	if cfg.Server.AppNavEnabled() {
		mux.HandleFunc("GET /app/{slug}/.shinyhub/nav.json", func(w http.ResponseWriter, r *http.Request) {
			if u := access.ResolveOptionalUser(r, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup); u != nil {
				r = r.WithContext(auth.WithUser(r.Context(), u))
			}
			srv.HandleAppNavJSON(w, r)
		})
		mux.HandleFunc("GET /app/{slug}/.shinyhub/version.json", func(w http.ResponseWriter, r *http.Request) {
			if u := access.ResolveOptionalUser(r, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup); u != nil {
				r = r.WithContext(auth.WithUser(r.Context(), u))
			}
			srv.HandleAppVersionJSON(w, r, r.PathValue("slug"))
		})
		mux.HandleFunc("POST /app/{slug}/.shinyhub/version/switch", func(w http.ResponseWriter, r *http.Request) {
			if u := access.ResolveOptionalUser(r, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup); u != nil {
				r = r.WithContext(auth.WithUser(r.Context(), u))
			}
			srv.HandleAppVersionSwitch(w, r, r.PathValue("slug"))
		})
		mux.HandleFunc("DELETE /app/{slug}/.shinyhub/version/switch", func(w http.ResponseWriter, r *http.Request) {
			if u := access.ResolveOptionalUser(r, cfg.Auth.Secret, store.IsTokenRevoked, appUserLookup); u != nil {
				r = r.WithContext(auth.WithUser(r.Context(), u))
			}
			srv.HandleAppVersionSwitch(w, r, r.PathValue("slug"))
		})
	}
	mux.Handle("/healthz", probeMethods(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})))
	mux.Handle("/readyz", probeMethods(readyzHandler(prx, readyCh, store)))
	mux.Handle("/activez", probeMethods(activezHandler(ownerAndReady)))
	mux.HandleFunc("GET "+announcementui.ClientPath, announcementui.ScriptHandler)
	mux.Handle("/static/", ui.Handler())
	mux.Handle("/invite", ui.InvitationHandler())

	// GET /internal/runtime-bundle/{digest} streams a bundle to a managed runtime
	// runner presenting a short-lived HMAC capability token. Keep the original
	// Fargate route as a compatibility alias for already-deployed runner images.
	// Mounted directly on the main mux (not under /api/) so large bundle streams
	// are not subject to apiTimeoutHandler's 30-second cap.
	fargateBundleHandler := api.NewFargateBundleHandler(store, cfg.Storage.AppsDir, bundleTokenKey)
	internalMux := chi.NewRouter()
	internalMux.Get("/runtime-bundle/{digest}", fargateBundleHandler.Handle)
	internalMux.Get("/fargate-bundle/{digest}", fargateBundleHandler.Handle)
	mux.Handle("/internal/", http.StripPrefix("/internal", internalMux))

	registerBrandingRoutes(mux, cfg, srv, store, appUserLookup)

	var rootHandler http.Handler = mux
	if parsedAppOrigin != nil {
		rootHandler = appOriginLaunchDispatch(rootHandler, parsedAppOrigin, store, cfg.Auth.Secret, cfg.TrustedProxyNets, cfg.Auth)
	}
	if cfg.Auth.ForwardAuth.Enabled {
		faCfg := auth.ForwardAuthConfig{
			DashboardURL: cfg.Server.BaseURL, SessionSecret: cfg.Auth.Secret, SessionMaxAge: cfg.Auth.BrowserSessionMaxAge(), Revoked: store.IsTokenRevoked,
			Enabled:             true,
			UserHeader:          cfg.Auth.ForwardAuth.UserHeader,
			SharedSecret:        cfg.Auth.ForwardAuth.SharedSecret,
			SecretHeader:        cfg.Auth.ForwardAuth.SecretHeader,
			EmailHeader:         cfg.Auth.ForwardAuth.EmailHeader,
			NameHeader:          cfg.Auth.ForwardAuth.NameHeader,
			GroupsHeader:        cfg.Auth.ForwardAuth.GroupsHeader,
			DefaultRole:         cfg.Auth.ForwardAuth.DefaultRole,
			GroupRoleMappings:   api.AuthMappings(cfg.Auth.GroupRoleMappings),
			RequireGroupsHeader: cfg.Auth.ForwardAuth.RequireGroupsHeader,
		}
		if parsedAppOrigin != nil {
			faCfg.AppOriginHost = parsedAppOrigin.Host
		}
		rootHandler = auth.ForwardAuthMiddleware(store, faCfg, cfg.TrustedProxyNets)(rootHandler)
	}
	rootHandler = appOriginBoundary(rootHandler, parsedAppOrigin, cfg.TrustedProxyNets)
	if cfg.Server.CompressionEnabled() {
		rootHandler = httpcompress.Handler(rootHandler)
	}

	// Branding injects inline <script>/<style> into the SPA shell only when
	// active; the strict CSP allows exactly those blocks by SHA-256 hash (both
	// slices stay empty when branding is off, so no inline is served). Computed
	// from the same resolved branding the renderer uses so they cannot drift.
	var cspScriptSrc, cspStyleSrc, cspImgSrc []string
	if cfg.Branding.IsActive() {
		brandPub := ui.PublicBranding(cfg.Branding, cfg.Branding.ResolvedAssets())
		s, st, cspErr := ui.CSPInlineSources(brandPub)
		if cspErr != nil {
			return nil, fmt.Errorf("compute branding CSP sources: %w", cspErr)
		}
		cspScriptSrc, cspStyleSrc = s, st
		// A logo/favicon hosted off-origin needs its origin in img-src, or the
		// browser blocks the operator's own image.
		cspImgSrc = ui.ImageSources(brandPub)
	} else {
		// Branding off: the raw shell is served, but its static inline
		// scripts (the pre-paint theme bootstrap) still need CSP hashes.
		s, cspErr := ui.StaticShellInlineScriptSources()
		if cspErr != nil {
			return nil, fmt.Errorf("compute shell CSP sources: %w", cspErr)
		}
		cspScriptSrc = s
	}

	// RecoverPanic wraps the outer mux so a panic in a non-/api/ handler (notably
	// the /app/* reverse proxy, which had no recovery) is logged via slog and
	// returned as a 500 instead of escaping to the stdlib server's per-connection
	// default (which bypasses structured logging). /api/* already has chi.Recoverer.
	return api.SecurityHeaders(cfg.TrustedProxyNets, cspScriptSrc, cspStyleSrc, cspImgSrc, api.RecoverPanic(slog.Default(), deadlineExemptions(rootHandler))), nil
}
