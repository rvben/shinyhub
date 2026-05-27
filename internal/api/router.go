package api

import (
	"context"
	"fmt"
	"net/http"
	"sort"
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
	"github.com/rvben/shinyhub/internal/metrics"
	"github.com/rvben/shinyhub/internal/oauth"
	"github.com/rvben/shinyhub/internal/process"
	"github.com/rvben/shinyhub/internal/proxy"
	"github.com/rvben/shinyhub/internal/servertrace"
	"github.com/rvben/shinyhub/internal/tracing"
	"github.com/rvben/shinyhub/internal/worker"
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
	dataLimiter   *keyedRateLimiter // per-user: data uploads
	actionLimiter *keyedRateLimiter // per-user: restart/rollback/manual schedule run
	oauthLimiter  *keyedRateLimiter // per-IP: OAuth/OIDC login-start
	jobs          *jobs.Manager
	scheduler     *scheduler.Scheduler
	secretsKey    []byte
	traceBuffer   *tracing.Buffer
	metrics       *metrics.Registry   // nil when metrics are disabled
	tracer        *servertrace.Tracer // nil when server tracing is disabled
	router        http.Handler

	// nodeForTier resolves a tier name to the node identity backing it: a remote
	// worker's node id, or "" for any tier the control plane itself backs (all
	// such tiers share the "" identity, so they are mutually co-located). Nil
	// when worker hosting is disabled; cross-node checks are then a no-op because
	// no remote tiers exist.
	nodeForTier func(tier string) string

	// workerReg is the control plane's view of joined workers, used by the admin
	// fleet endpoints to list and revoke workers. Nil when worker hosting is
	// disabled; the endpoints then report an empty fleet and 404 on revoke.
	workerReg *worker.Registry

	// deployToken, when non-nil, registers a pre-shared bearer credential that
	// authenticates as the synthetic system user without a DB lookup. Set via
	// SetDeployToken at startup when SHINYHUB_DEPLOY_TOKEN is configured.
	deployToken *auth.DeployToken
	deployRun   func(deploy.Params) (*deploy.PoolResult, error)

	// deployLocksMu guards the deployLocks map. Each slug gets its own
	// sync.Mutex which serializes deploy/restart/rollback/stop/delete
	// operations for that app: a deploy in flight blocks a concurrent
	// restart on the same slug. Different slugs are independent. The async
	// redeployApp goroutine waits for this lock so a replica change is always
	// applied even when an HTTP-driven deploy is already running.
	deployLocksMu sync.Mutex
	deployLocks   map[string]*sync.Mutex

	// dataLocksMu guards the dataLocks map. Each slug gets its own
	// sync.Mutex held across the quota-check + write phase of handleDataPut
	// so two concurrent uploads cannot each read the pre-write usage and
	// jointly exceed the per-app quota. This lock is separate from
	// deployLocks so a slow upload does not block deploys/restarts.
	dataLocksMu sync.Mutex
	dataLocks   map[string]*sync.Mutex

	// redeployMu guards redeployInFlight, a per-slug reference count of pending
	// pool cycles. The PATCH handler increments synchronously before launching
	// each async redeployApp goroutine, so the first GET after the PATCH always
	// observes the redeploy in flight even though the app row still reads
	// "running". Every launched goroutine decrements exactly once on return,
	// whether it performed the restart or skipped because another operation held
	// the deploy lock. Reference counting keeps the marker set while the active
	// pool-cycler is still running (a coalesced skip only drops its own
	// reference) yet guarantees the marker is released even when the lock holder
	// is an unrelated operation that never cycles the pool. handleGetApp surfaces
	// a positive count as redeploy_in_flight so a --wait client polls until the
	// new pool is up.
	redeployMu       sync.Mutex
	redeployInFlight map[string]int
}

// markRedeployInFlight adds one reference for slug's pending pool cycle. Each
// mark is balanced by exactly one clearRedeployInFlight in the launched
// redeployApp goroutine.
func (s *Server) markRedeployInFlight(slug string) {
	s.redeployMu.Lock()
	defer s.redeployMu.Unlock()
	if s.redeployInFlight == nil {
		s.redeployInFlight = make(map[string]int)
	}
	s.redeployInFlight[slug]++
}

// clearRedeployInFlight drops one reference for slug, removing the entry when
// the count reaches zero. Never decrements below zero.
func (s *Server) clearRedeployInFlight(slug string) {
	s.redeployMu.Lock()
	defer s.redeployMu.Unlock()
	if s.redeployInFlight[slug] <= 1 {
		delete(s.redeployInFlight, slug)
		return
	}
	s.redeployInFlight[slug]--
}

// isRedeployInFlight reports whether slug's pool is currently being cycled.
func (s *Server) isRedeployInFlight(slug string) bool {
	s.redeployMu.Lock()
	defer s.redeployMu.Unlock()
	return s.redeployInFlight[slug] > 0
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
		dataLimiter:   newKeyedRateLimiter(120, time.Minute),
		actionLimiter: newKeyedRateLimiter(30, time.Minute),
		oauthLimiter:  newKeyedRateLimiter(20, time.Minute),
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

// withTierPlacement fills the tier-routing fields (Placement, TierOrder,
// DefaultTier) on p from the app's persisted placement and the server's
// configured tiers. Every deploy/redeploy/rollback site routes its replicas
// through this helper so a single app's placement is applied identically
// regardless of which control-plane action triggered the pool launch.
func (s *Server) withTierPlacement(p deploy.Params, app *db.App) deploy.Params {
	p.Placement = app.PlacementMap()
	p.TierOrder = s.cfg.Runtime.TierOrder()
	p.DefaultTier = s.cfg.Runtime.DefaultTierName()
	return p
}

// SetNodeForTier injects the tier-to-node resolver used to reject cross-node
// shared mounts. Wired at startup from the worker registry; left nil when
// worker hosting is disabled. Must be called before the server begins handling
// requests.
func (s *Server) SetNodeForTier(fn func(tier string) string) { s.nodeForTier = fn }

// SetWorkerRegistry injects the worker registry backing the admin fleet
// endpoints (list and revoke). Wired at startup from the worker registry; left
// nil when worker hosting is disabled. Must be called before the server begins
// handling requests.
func (s *Server) SetWorkerRegistry(reg *worker.Registry) { s.workerReg = reg }

// tiersForApp returns the tiers an app's replicas run on: the keys of its
// placement, or the default tier when no explicit placement is set.
func (s *Server) tiersForApp(app *db.App) []string {
	pm := app.PlacementMap()
	if len(pm) == 0 {
		return []string{s.cfg.Runtime.DefaultTierName()}
	}
	out := make([]string, 0, len(pm))
	for tier := range pm {
		out = append(out, tier)
	}
	return out
}

// checkColocatedShared rejects a boot whose consumer (running on consumerTiers)
// would land on a node that does not also host every app it mounts shared data
// from. A nil resolver means single-node operation (every tier is the control
// plane), so there is nothing to reject.
func (s *Server) checkColocatedShared(appID int64, consumerTiers []string) error {
	if s.nodeForTier == nil {
		return nil
	}
	sources, err := s.store.ListSharedDataSources(appID)
	if err != nil {
		return fmt.Errorf("list shared data sources: %w", err)
	}
	if len(sources) == 0 {
		return nil
	}
	sourceTiers := make(map[string][]string, len(sources))
	for _, m := range sources {
		srcApp, err := s.store.GetAppBySlug(m.SourceSlug)
		if err != nil {
			return fmt.Errorf("load shared source %q: %w", m.SourceSlug, err)
		}
		sourceTiers[m.SourceSlug] = s.tiersForApp(srcApp)
	}
	// Fail closed when a shared mount touches a multi-worker tier: the colocation
	// guard maps each tier to a single node, which cannot guarantee the source
	// and consumer land on the same worker once a tier has several. Reject until
	// same-worker pinning lands rather than passing on the unsound assumption
	// that every replica resolves to the first worker.
	if tier := s.firstMultiWorkerTier(consumerTiers, sourceTiers); tier != "" {
		return fmt.Errorf(
			"shared mounts are not supported on multi-worker tier %q: the source and consumer cannot be guaranteed to share a worker",
			tier)
	}
	return deploy.CheckColocatedShared(consumerTiers, sourceTiers, s.nodeForTier)
}

// firstMultiWorkerTier returns the first tier among the consumer's tiers and all
// source tiers that is backed by more than one up worker, or "" when none is.
// With no worker registry wired (single-node operation) it always returns "".
func (s *Server) firstMultiWorkerTier(consumerTiers []string, sourceTiers map[string][]string) string {
	if s.workerReg == nil {
		return ""
	}
	seen := make(map[string]bool)
	check := func(tiers []string) string {
		for _, t := range tiers {
			if seen[t] {
				continue
			}
			seen[t] = true
			if len(s.workerReg.WorkersForTier(t)) > 1 {
				return t
			}
		}
		return ""
	}
	if t := check(consumerTiers); t != "" {
		return t
	}
	// Iterate source slugs in order so the reported tier is deterministic.
	slugs := make([]string, 0, len(sourceTiers))
	for slug := range sourceTiers {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		if t := check(sourceTiers[slug]); t != "" {
			return t
		}
	}
	return ""
}

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

// SetDeployToken installs a pre-shared deploy credential. Must be called before
// the server begins handling requests; it is not safe to call concurrently with
// ServeHTTP.
func (s *Server) SetDeployToken(t *auth.DeployToken) { s.deployToken = t }

// SetTraceBuffer wires the proxy's ring buffer of recent slow/error spans into
// the API server so the /api/apps/{slug}/traces handler can surface them. May
// be nil when tracing is disabled — the handler then returns an empty list.
// Must be called before the server begins handling requests.
func (s *Server) SetTraceBuffer(b *tracing.Buffer) { s.traceBuffer = b }

// SetMetrics wires the Prometheus registry whose middleware records per-request
// counters and latencies for the API router. May be nil (the default) to leave
// metrics disabled. Must be called before the server begins handling requests;
// it is not safe to call concurrently with ServeHTTP.
func (s *Server) SetMetrics(m *metrics.Registry) { s.metrics = m }

// recordDeploy increments the deploy-outcome counter when metrics are enabled.
// result is "success" or "failure". A no-op when metrics are disabled.
func (s *Server) recordDeploy(result string) {
	if s.metrics != nil {
		s.metrics.RecordDeploy(result)
	}
}

// SetTracer wires the OpenTelemetry tracer whose middleware records one server
// span per API request, exported to the configured OTLP endpoint. May be nil
// (the default) to leave server tracing disabled. Must be called before the
// server begins handling requests; it is not safe to call concurrently with
// ServeHTTP.
func (s *Server) SetTracer(t *servertrace.Tracer) { s.tracer = t }

// keyLookup satisfies auth.APIKeyLookup by first checking the pre-shared
// deploy token (no DB hit) and falling back to the api_keys table. DB-backed
// keys owned by system users are refused: those accounts authenticate only
// through their bootstrap-provisioned mechanism (the env token), never through
// a persisted api_keys row.
func (s *Server) keyLookup(keyHash string) (*auth.ContextUser, error) {
	if s.deployToken != nil && s.deployToken.Matches(keyHash) {
		u := s.deployToken.User()
		if u == nil {
			return nil, fmt.Errorf("deploy token has no associated user")
		}
		return u, nil
	}
	u, err := s.store.GetUserByAPIKeyHash(keyHash)
	if err != nil {
		return nil, err
	}
	if db.IsSystemUser(u.Username) {
		return nil, fmt.Errorf("api key owned by system user is not honored")
	}
	return &auth.ContextUser{ID: u.ID, Username: u.Username, Role: u.Role}, nil
}

// userLookup satisfies auth.UserLookup by re-resolving the user against the
// live DB on every JWT-authenticated request. This is what makes role
// downgrades and account deletions take effect immediately, instead of
// remaining in force until the JWT expires.
func (s *Server) userLookup(userID int64) (*auth.ContextUser, error) {
	u, err := s.store.GetUserByID(userID)
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
	r.Use(s.accessLog)
	r.Use(middleware.Recoverer)

	// Public endpoints
	r.Post("/api/auth/login", s.handleLogin)
	r.Post("/api/auth/session", s.handleSessionLogin)
	// Server-side handoff used by the access-denied 403 page so a user signed
	// in to the wrong account can switch users in one click. Lives outside the
	// bearer+CSRF group on purpose: it's invoked by an HTML <form> POST from a
	// page that may be opened in a brand-new tab where the SPA hasn't bootstrapped
	// (so there's no CSRF token cookie yet). The handler does its own Origin/Referer
	// same-origin check; see handleSessionHandoff for the reasoning.
	r.Post("/api/auth/handoff", s.handleSessionHandoff)
	r.With(s.rateLimitByIP(s.oauthLimiter)).Get("/api/auth/github/login", s.handleGitHubLogin)
	r.Get("/api/auth/github/callback", s.handleGitHubCallback)
	r.With(s.rateLimitByIP(s.oauthLimiter)).Get("/api/auth/google/login", s.handleGoogleLogin)
	r.Get("/api/auth/google/callback", s.handleGoogleCallback)
	r.Get("/api/auth/providers", s.handleGetProviders)
	r.With(s.rateLimitByIP(s.oauthLimiter)).Get("/api/auth/oidc/login", s.handleOIDCLogin)
	r.Get("/api/auth/oidc/callback", s.handleOIDCCallback)
	r.Get("/api/server-info", s.handleServerInfo)

	// All other endpoints require either an auth header or a valid session cookie.
	bearer := auth.BearerMiddleware(s.cfg.Auth.Secret, s.keyLookup, s.userLookup, s.revocationChecker())
	csrf := auth.CSRFMiddleware(s.cfg.TrustedProxyNets)
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
		r.With(rateLimitByUser(s.actionLimiter)).Post("/api/apps/{slug}/rollback", s.handleRollbackApp)
		// Keep PUT for backwards compatibility.
		r.With(rateLimitByUser(s.actionLimiter)).Put("/api/apps/{slug}/rollback", s.handleRollbackApp)
		r.With(rateLimitByUser(s.actionLimiter)).Post("/api/apps/{slug}/restart", s.handleRestartApp)
		r.Post("/api/apps/{slug}/stop", s.handleStopApp)
		r.Get("/api/apps/{slug}/logs", s.handleLogs)
		r.Get("/api/apps/{slug}/metrics", s.handleMetrics)
		r.Get("/api/apps/{slug}/traces", s.handleTraces)
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
		r.With(rateLimitByUser(s.dataLimiter)).Put("/api/apps/{slug}/data/*", s.handleDataPut)
		r.Delete("/api/apps/{slug}/data/*", s.handleDataDelete)

		r.Get("/api/apps/{slug}/schedules", s.handleListSchedules)
		r.Post("/api/apps/{slug}/schedules", s.handleCreateSchedule)
		r.Patch("/api/apps/{slug}/schedules/{id}", s.handlePatchSchedule)
		r.Delete("/api/apps/{slug}/schedules/{id}", s.handleDeleteSchedule)
		r.With(rateLimitByUser(s.actionLimiter)).Post("/api/apps/{slug}/schedules/{id}/run", s.handleRunSchedule)
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

		r.Get("/api/workers", s.handleListWorkers)                    // admin: list joined workers
		r.Post("/api/workers/{node_id}/revoke", s.handleRevokeWorker) // admin: revoke a worker
	})

	return r
}

// Observe wraps the API handler chain (timeout handler included) with server
// tracing and Prometheus instrumentation so both record the status and latency
// the client actually observes - covering recovered panics (the inner chi
// Recoverer writes the 500 before observation reads it) and timeout responses
// (http.TimeoutHandler writes the 503 below observation). It seeds a chi route
// context so the matched route PATTERN is available for low-cardinality labels
// even though observation runs outside the chi router; the inner router
// populates that same context during routing. Both layers are no-ops when their
// dependency (tracer / metrics registry) is nil, so observation is opt-in.
//
// Must be wired before the server begins handling requests.
func (s *Server) Observe(next http.Handler) http.Handler {
	observed := s.trace(s.instrument(next))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if chi.RouteContext(r.Context()) == nil {
			rctx := chi.NewRouteContext()
			r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		}
		observed.ServeHTTP(w, r)
	})
}

// trace records one OpenTelemetry server span per request when a tracer is
// wired in. When server tracing is disabled (s.tracer == nil) it is a
// pass-through, so tracing is strictly opt-in.
func (s *Server) trace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.tracer == nil {
			next.ServeHTTP(w, r)
			return
		}
		s.tracer.Middleware(next).ServeHTTP(w, r)
	})
}

// instrument records Prometheus request metrics for the API router when a
// registry is wired in. When metrics are disabled (s.metrics == nil) it is a
// pass-through, so the instrumentation is strictly opt-in.
func (s *Server) instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.metrics == nil {
			next.ServeHTTP(w, r)
			return
		}
		s.metrics.Middleware(next).ServeHTTP(w, r)
	})
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

// rateLimitByIP applies the given limiter keyed by the client IP. Used on
// unauthenticated endpoints (OAuth/OIDC login-start) where there is no
// authenticated user yet, to bound provider-redirect and callback-state
// churn from a single source.
func (s *Server) rateLimitByIP(rl *keyedRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !rl.allow(s.ClientIP(r)) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
