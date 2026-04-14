package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// Server holds the dependencies shared by all API handlers.
type Server struct {
	cfg     *config.Config
	store   *db.Store
	manager *process.Manager
	proxy   *proxy.Proxy
	router  http.Handler
}

// New constructs a Server and wires up all routes. manager and prx may be nil
// when running in test contexts that exercise only auth/data handlers.
func New(cfg *config.Config, store *db.Store, manager *process.Manager, prx *proxy.Proxy) *Server {
	s := &Server{cfg: cfg, store: store, manager: manager, proxy: prx}
	s.router = s.buildRouter()
	return s
}

// Router returns the fully-configured http.Handler.
func (s *Server) Router() http.Handler { return s.router }

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

	// All other endpoints require a valid Bearer JWT or Token API key.
	bearer := auth.BearerMiddleware(s.cfg.Auth.Secret, s.keyLookup)
	r.Group(func(r chi.Router) {
		r.Use(bearer)

		r.Get("/api/apps", s.handleListApps)
		r.Post("/api/apps", s.handleCreateApp)
		r.Get("/api/apps/{slug}", s.handleGetApp)
		r.Patch("/api/apps/{slug}", s.handlePatchApp)
		r.Post("/api/apps/{slug}/deploy", s.handleDeployApp)
		r.Put("/api/apps/{slug}/rollback", s.handleRollbackApp)
		r.Post("/api/apps/{slug}/restart", s.handleRestartApp)
		r.Get("/api/apps/{slug}/logs", s.handleLogs)
		r.Patch("/api/apps/{slug}/access", s.handleSetAppAccess)
		r.Post("/api/apps/{slug}/members", s.handleGrantAppAccess)
		r.Delete("/api/apps/{slug}/members", s.handleRevokeAppAccess)

		r.Post("/api/tokens", s.handleCreateToken)
	})

	return r
}
