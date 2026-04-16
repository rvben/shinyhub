package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/oauth"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// Server holds the dependencies shared by all API handlers.
type Server struct {
	cfg          *config.Config
	store        *db.Store
	manager      *process.Manager
	proxy        *proxy.Proxy
	github       *oauth.GitHub        // nil when GitHub OAuth is not configured
	googleOAuth  *oauth.Google        // nil when Google OAuth is not configured
	oidcProvider *oauth.OIDCProvider  // nil when OIDC SSO is not configured
	sampler      process.Sampler
	loginLimiter *loginRateLimiter
	router       http.Handler
}

// New constructs a Server and wires up all routes. manager and prx may be nil
// when running in test contexts that exercise only auth/data handlers.
func New(cfg *config.Config, store *db.Store, manager *process.Manager, prx *proxy.Proxy) *Server {
	s := &Server{
		cfg:          cfg,
		store:        store,
		manager:      manager,
		proxy:        prx,
		sampler:      &process.GopsutilSampler{},
		loginLimiter: newLoginRateLimiter(10, time.Minute),
	}
	if cfg.OAuth.GitHub.ClientID != "" {
		s.github = oauth.NewGitHub(
			cfg.OAuth.GitHub.ClientID,
			cfg.OAuth.GitHub.ClientSecret,
			cfg.OAuth.GitHub.CallbackURL,
		)
	}
	if cfg.OAuth.Google.ClientID != "" {
		s.googleOAuth = oauth.NewGoogle(
			cfg.OAuth.Google.ClientID,
			cfg.OAuth.Google.ClientSecret,
			cfg.OAuth.Google.CallbackURL,
		)
	}
	s.router = s.buildRouter()
	return s
}

// Router returns the fully-configured http.Handler.
func (s *Server) Router() http.Handler { return s.router }

// SetSampler replaces the metrics sampler. Must be called before the server
// begins handling requests; it is not safe to call concurrently with ServeHTTP.
func (s *Server) SetSampler(sampler process.Sampler) { s.sampler = sampler }

// SetOIDCProvider sets the OIDC provider after the server is constructed.
// Must be called before the server begins handling requests.
func (s *Server) SetOIDCProvider(p *oauth.OIDCProvider) { s.oidcProvider = p }

// keyLookup satisfies auth.APIKeyLookup by delegating to the DB.
func (s *Server) keyLookup(keyHash string) (*auth.ContextUser, error) {
	u, err := s.store.GetUserByAPIKeyHash(keyHash)
	if err != nil {
		return nil, err
	}
	return &auth.ContextUser{ID: u.ID, Username: u.Username, Role: u.Role}, nil
}

func (s *Server) buildRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Public endpoints
	r.Post("/api/auth/login", s.handleLogin)
	r.Post("/api/auth/session", s.handleSessionLogin)
	r.Post("/api/auth/logout", s.handleLogout)
	r.Get("/api/auth/github/login", s.handleGitHubLogin)
	r.Get("/api/auth/github/callback", s.handleGitHubCallback)
	r.Get("/api/auth/google/login", s.handleGoogleLogin)
	r.Get("/api/auth/google/callback", s.handleGoogleCallback)
	r.Get("/api/auth/providers", s.handleGetProviders)
	r.Get("/api/auth/oidc/login", s.handleOIDCLogin)
	r.Get("/api/auth/oidc/callback", s.handleOIDCCallback)

	// All other endpoints require either an auth header or a valid session cookie.
	bearer := auth.BearerMiddleware(s.cfg.Auth.Secret, s.keyLookup)
	csrf := auth.CSRFMiddleware()
	r.Group(func(r chi.Router) {
		r.Use(bearer)
		r.Use(csrf)

		r.Get("/api/auth/me", s.handleMe)
		r.Get("/api/apps", s.handleListApps)
		r.Post("/api/apps", s.handleCreateApp)
		r.Get("/api/apps/{slug}", s.handleGetApp)
		r.Patch("/api/apps/{slug}", s.handlePatchApp)
		r.Delete("/api/apps/{slug}", s.handleDeleteApp)
		r.Post("/api/apps/{slug}/deploy", s.handleDeployApp)
		r.Post("/api/apps/{slug}/rollback", s.handleRollbackApp)
		// Keep PUT for backwards compatibility.
		r.Put("/api/apps/{slug}/rollback", s.handleRollbackApp)
		r.Post("/api/apps/{slug}/restart", s.handleRestartApp)
		r.Post("/api/apps/{slug}/stop", s.handleStopApp)
		r.Get("/api/apps/{slug}/logs", s.handleLogs)
		r.Get("/api/apps/{slug}/metrics", s.handleMetrics)
		r.Get("/api/apps/{slug}/members", s.handleGetMembers)
		r.Patch("/api/apps/{slug}/access", s.handleSetAppAccess)
		r.Post("/api/apps/{slug}/members", s.handleGrantAppAccess)
		r.Delete("/api/apps/{slug}/members", s.handleRevokeAppAccess)
		r.Delete("/api/apps/{slug}/members/{user_id}", s.handleRevokeAppAccess)
		r.Get("/api/apps/{slug}/deployments", s.handleListDeployments)

		r.Post("/api/tokens", s.handleCreateToken)
		r.Get("/api/tokens", s.handleListTokens)
		r.Delete("/api/tokens/{id}", s.handleDeleteToken)
		r.Get("/api/users", s.handleListUsers)           // admin: list all users
		r.Post("/api/users", s.handleCreateUser)          // admin: create user
		r.Get("/api/users/{username}", s.handleGetUser)   // any auth: lookup by username
		r.Patch("/api/users/{id}", s.handlePatchUser)     // admin: update role
		r.Delete("/api/users/{id}", s.handleDeleteUser)   // admin: delete user

		r.Get("/api/audit", s.handleListAuditEvents) // admin: audit log
	})

	return r
}
