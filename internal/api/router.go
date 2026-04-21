package api

import (
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/db"
	"github.com/rvben/shinyhub/internal/deploy"
	"github.com/rvben/shinyhub/internal/jobs"
	"github.com/rvben/shinyhub/internal/lifecycle/scheduler"
	"github.com/rvben/shinyhub/internal/oauth"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
)

// Server holds the dependencies shared by all API handlers.
type Server struct {
	cfg           *config.Config
	store         *db.Store
	manager       *process.Manager
	proxy         *proxy.Proxy
	github        *oauth.GitHub       // nil when GitHub OAuth is not configured
	googleOAuth   *oauth.Google       // nil when Google OAuth is not configured
	oidcProvider  *oauth.OIDCProvider // nil when OIDC SSO is not configured
	sampler       process.Sampler
	loginLimiter  *loginRateLimiter
	deployLimiter *keyedRateLimiter
	userLimiter   *keyedRateLimiter
	tokenLimiter  *keyedRateLimiter
	jobs          *jobs.Manager
	scheduler     *scheduler.Scheduler
	secretsKey    []byte
	router        http.Handler
	deployRun     func(deploy.Params) (*deploy.PoolResult, error)

	// deployLocksMu guards the deployLocks map. Each slug gets its own
	// sync.Mutex which serializes deploy/restart/rollback/stop/delete
	// operations for that app: a deploy in flight blocks a concurrent
	// restart on the same slug. Different slugs are independent. The
	// async redeployApp goroutine uses TryLock so it coalesces (skips)
	// when an HTTP-driven deploy is already running.
	deployLocksMu sync.Mutex
	deployLocks   map[string]*sync.Mutex
}

// New constructs a Server and wires up all routes. manager and prx may be nil
// when running in test contexts that exercise only auth/data handlers.
func New(cfg *config.Config, store *db.Store, manager *process.Manager, prx *proxy.Proxy) *Server {
	s := &Server{
		cfg:           cfg,
		store:         store,
		manager:       manager,
		proxy:         prx,
		sampler:       &process.GopsutilSampler{},
		loginLimiter:  newLoginRateLimiter(10, time.Minute),
		deployLimiter: newKeyedRateLimiter(10, time.Minute),
		userLimiter:   newKeyedRateLimiter(5, time.Minute),
		tokenLimiter:  newKeyedRateLimiter(20, time.Minute),
		deployRun:     deploy.Run,
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

// Config returns the server's configuration. Exposed for tests that need to
// locate temp directories (e.g. AppsDir, AppDataDir) created by the test helper.
func (s *Server) Config() *config.Config { return s.cfg }

// SetSampler replaces the metrics sampler. Must be called before the server
// begins handling requests; it is not safe to call concurrently with ServeHTTP.
func (s *Server) SetSampler(sampler process.Sampler) { s.sampler = sampler }

// SetOIDCProvider sets the OIDC provider after the server is constructed.
// Must be called before the server begins handling requests.
func (s *Server) SetOIDCProvider(p *oauth.OIDCProvider) { s.oidcProvider = p }

// SetSecretsKey sets the AES-256 key used to decrypt per-app secret env vars.
// Must be called before the server begins handling requests.
func (s *Server) SetSecretsKey(k []byte) { s.secretsKey = k }

// SetJobs wires the schedule-runner and the cron scheduler into the API server.
// Must be called before the server begins handling requests.
func (s *Server) SetJobs(j *jobs.Manager, sc *scheduler.Scheduler) {
	s.jobs = j
	s.scheduler = sc
}

// SetDeployRunForTest replaces the deploy.Run hook used by maybeRestartForChange.
// Must be called before the server begins handling requests; intended for tests.
func (s *Server) SetDeployRunForTest(fn func(deploy.Params) (*deploy.PoolResult, error)) {
	s.deployRun = fn
}

// keyLookup satisfies auth.APIKeyLookup by delegating to the DB.
func (s *Server) keyLookup(keyHash string) (*auth.ContextUser, error) {
	u, err := s.store.GetUserByAPIKeyHash(keyHash)
	if err != nil {
		return nil, err
	}
	return &auth.ContextUser{ID: u.ID, Username: u.Username, Role: u.Role}, nil
}

// revocationChecker returns an auth.RevocationChecker bound to the server's
// store. Returning nil for the checker (when store is unset) disables the
// revocation path, which matches the behavior expected by tests that construct
// a Server without a database.
func (s *Server) revocationChecker() auth.RevocationChecker {
	if s.store == nil {
		return nil
	}
	return s.store.IsTokenRevoked
}

func (s *Server) buildRouter() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	// Public endpoints
	r.Post("/api/auth/login", s.handleLogin)
	r.Post("/api/auth/session", s.handleSessionLogin)
	r.Get("/api/auth/github/login", s.handleGitHubLogin)
	r.Get("/api/auth/github/callback", s.handleGitHubCallback)
	r.Get("/api/auth/google/login", s.handleGoogleLogin)
	r.Get("/api/auth/google/callback", s.handleGoogleCallback)
	r.Get("/api/auth/providers", s.handleGetProviders)
	r.Get("/api/auth/oidc/login", s.handleOIDCLogin)
	r.Get("/api/auth/oidc/callback", s.handleOIDCCallback)

	// All other endpoints require either an auth header or a valid session cookie.
	bearer := auth.BearerMiddleware(s.cfg.Auth.Secret, s.keyLookup, s.revocationChecker())
	csrf := auth.CSRFMiddleware()
	r.Group(func(r chi.Router) {
		r.Use(bearer)
		r.Use(csrf)

		// Logout is authenticated so we can revoke the caller's JWT by jti.
		// An unauthenticated logout has nothing to revoke — the client can
		// just discard its own session cookie.
		r.Post("/api/auth/logout", s.handleLogout)
		r.Get("/api/auth/me", s.handleMe)
		r.Get("/api/apps", s.handleListApps)
		r.Post("/api/apps", s.handleCreateApp)
		r.Get("/api/apps/{slug}", s.handleGetApp)
		r.Patch("/api/apps/{slug}", s.handlePatchApp)
		r.Delete("/api/apps/{slug}", s.handleDeleteApp)
		r.With(rateLimitByUser(s.deployLimiter)).Post("/api/apps/{slug}/deploy", s.handleDeployApp)
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
		r.Get("/api/apps/{slug}/env", s.handleListAppEnv)
		r.Put("/api/apps/{slug}/env/{key}", s.handleUpsertAppEnv)
		r.Delete("/api/apps/{slug}/env/{key}", s.handleDeleteAppEnv)
		r.Get("/api/apps/{slug}/data", s.handleDataList)
		r.Put("/api/apps/{slug}/data/*", s.handleDataPut)
		r.Delete("/api/apps/{slug}/data/*", s.handleDataDelete)

		r.Get("/api/apps/{slug}/schedules", s.handleListSchedules)
		r.Post("/api/apps/{slug}/schedules", s.handleCreateSchedule)
		r.Patch("/api/apps/{slug}/schedules/{id}", s.handlePatchSchedule)
		r.Delete("/api/apps/{slug}/schedules/{id}", s.handleDeleteSchedule)
		r.Post("/api/apps/{slug}/schedules/{id}/run", s.handleRunSchedule)
		r.Get("/api/apps/{slug}/schedules/{id}/runs", s.handleListScheduleRuns)
		r.Get("/api/apps/{slug}/schedules/{id}/runs/{run_id}", s.handleGetScheduleRun)
		r.Get("/api/apps/{slug}/schedules/{id}/runs/{run_id}/logs", s.handleScheduleRunLogs)
		r.Post("/api/apps/{slug}/schedules/{id}/runs/{run_id}/cancel", s.handleCancelScheduleRun)

		r.Get("/api/apps/{slug}/shared-data", s.handleListSharedData)
		r.Post("/api/apps/{slug}/shared-data", s.handleGrantSharedData)
		r.Delete("/api/apps/{slug}/shared-data/{source_slug}", s.handleRevokeSharedData)

		r.With(rateLimitByUser(s.tokenLimiter)).Post("/api/tokens", s.handleCreateToken)
		r.Get("/api/tokens", s.handleListTokens)
		r.Delete("/api/tokens/{id}", s.handleDeleteToken)
		r.Get("/api/users", s.handleListUsers)                                        // admin: list all users
		r.With(rateLimitByUser(s.userLimiter)).Post("/api/users", s.handleCreateUser) // admin: create user
		r.Get("/api/users/{username}", s.handleGetUser)                               // any auth: lookup by username
		r.Patch("/api/users/{id}", s.handlePatchUser)                                 // admin: update role
		r.Patch("/api/users/{id}/password", s.handlePatchUserPassword)                // admin: reset password
		r.Delete("/api/users/{id}", s.handleDeleteUser)                               // admin: delete user

		r.Get("/api/audit", s.handleListAuditEvents) // admin: audit log
	})

	return r
}

// rateLimitByUser applies the given limiter, keyed by the authenticated user
// ID. Must be placed after the bearer middleware so UserFromContext resolves.
func rateLimitByUser(rl *keyedRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := auth.UserFromContext(r.Context())
			if u == nil {
				next.ServeHTTP(w, r)
				return
			}
			if !rl.allow(strconv.FormatInt(u.ID, 10)) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
