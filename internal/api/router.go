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
	"github.com/rvben/shinyhub/internal/httproute"
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
	router        chi.Router

	// version is the binary version string advertised by GET /api/server-info,
	// set by the parent binary via SetVersion. Empty until SetVersion is called
	// (e.g. in test contexts that do not wire it).
	version string

	// isOwner reports whether this instance currently holds the control-plane
	// ownership lease. Set via SetOwnership; nil means "behave as owner" (tests
	// and any caller that never wires single-writer gating).
	isOwner func() bool

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

	// secretsCleaner removes an app's external secret-backend resources (Fargate
	// Secrets Manager entries + per-app task-def revisions) on delete. Nil when
	// no Fargate secrets backend is configured. Set via SetSecretsCleaner.
	secretsCleaner appSecretsCleaner

	// clustered is true when the server is running against a shared Postgres
	// backend (multiple control-plane instances). When false, all cluster-only
	// code paths (desired_state writes, fleet drain wait) are skipped and the
	// single-node behavior is byte-for-byte identical to the pre-cluster path.
	clustered bool

	// instanceID is the unique identifier of this control-plane instance, used
	// to exclude this instance's own replica_sessions rows from the fleet wait
	// (its exact local count is already handled by the local drain wait).
	instanceID string

	// deployToken, when non-nil, registers a pre-shared bearer credential that
	// authenticates as the synthetic system user without a DB lookup. Set via
	// SetDeployToken at startup when SHINYHUB_DEPLOY_TOKEN is configured.
	deployToken *auth.DeployToken
	deployRun   func(deploy.Params) (*deploy.PoolResult, error)
	// deployReplica boots a single replica at one index, used by the autoscale
	// scale-up primitive to grow a pool by one without cycling the whole pool.
	// Defaults to deploy.RunReplica; overridable in tests.
	deployReplica func(deploy.Params, int) (*deploy.Result, error)

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
		deployReplica: deploy.RunReplica,
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
	p.AppID = app.ID
	p.Placement = app.PlacementMap()
	p.TierOrder = s.cfg.Runtime.TierOrder()
	p.DefaultTier = s.cfg.Runtime.DefaultTierName()
	// Pin a shared-mount consumer to the worker(s) hosting its source data so
	// each replica lands beside the data it mounts. resolveColocation returns no
	// pin (and no error) for the common case of no shared mounts or a
	// deterministic single-worker/native placement. An infeasible colocation is
	// surfaced as a 409 by the checkColocatedShared precheck on user-facing deploy
	// paths; the internal recovery paths (restore/redeploy) that reach here
	// without that precheck fall back to unconstrained placement rather than
	// failing the recovery, and an unsatisfiable mount then fails its own health
	// check.
	if pins, err := s.resolveColocation(app.ID, s.tiersForApp(app)); err == nil {
		p.ColocateWorkers = pins
	}
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
// cannot be placed beside every app it mounts shared data from. It is the
// user-facing precheck: it surfaces the same infeasibility resolveColocation
// reports, as an error the deploy handlers turn into a 409.
func (s *Server) checkColocatedShared(appID int64, consumerTiers []string) error {
	_, err := s.resolveColocation(appID, consumerTiers)
	return err
}

// ColocationPins returns the worker node ids a shared-mount consumer must be
// pinned to so each replica co-locates with its source data, or nil when there
// is no colocation constraint or it cannot currently be satisfied. It is the
// best-effort form of resolveColocation (it swallows the infeasibility error)
// exposed so the lifecycle watchdog's single-replica restart pins a recovered
// replica to the same worker set the full deploy uses; an unsatisfiable pin
// falls back to unconstrained placement rather than wedging recovery.
func (s *Server) ColocationPins(app *db.App) []string {
	pins, err := s.resolveColocation(app.ID, s.tiersForApp(app))
	if err != nil {
		return nil
	}
	return pins
}

// resolveColocation determines how a shared-mount consumer must be placed so each
// replica lands on a node that also hosts every source's provisioned data
// (shared mounts resolve to the source's local app-data on whatever node the
// replica runs on, so the source must have a replica there).
//
// It returns:
//   - (nil, nil) when there is no constraint: no tier resolver (single-node
//     operation), no shared mounts, or a deterministic single-worker/native
//     placement that the node-equality check below already governs.
//   - (pins, nil) a sorted, de-duplicated set of worker node ids when the
//     consumer touches a multi-worker tier and a common worker hosts the data;
//     the caller pins the pool to these.
//   - (nil, err) when colocation is infeasible.
func (s *Server) resolveColocation(appID int64, consumerTiers []string) ([]string, error) {
	if s.nodeForTier == nil {
		return nil, nil
	}
	sources, err := s.store.ListSharedDataSources(appID)
	if err != nil {
		return nil, fmt.Errorf("list shared data sources: %w", err)
	}
	if len(sources) == 0 {
		return nil, nil
	}
	sourceTiers := make(map[string][]string, len(sources))
	for _, m := range sources {
		srcApp, err := s.store.GetAppBySlug(m.SourceSlug)
		if err != nil {
			return nil, fmt.Errorf("load shared source %q: %w", m.SourceSlug, err)
		}
		sourceTiers[m.SourceSlug] = s.tiersForApp(srcApp)
	}
	// A multi-worker tier breaks the deterministic tier->node assumption the
	// node-equality check relies on (it maps each tier to one node, but placement
	// may pick any of a tier's workers). When that ambiguity can affect this
	// consumer, resolve the pin from where the source's data actually lives
	// instead.
	if s.workerReg != nil && s.needsColocationPins(consumerTiers, sourceTiers) {
		return s.colocationPins(consumerTiers, sources)
	}
	// Deterministic single-worker/native placement: the node-equality check is
	// sound. No pin is needed (placement has no spread to constrain).
	if err := deploy.CheckColocatedShared(consumerTiers, sourceTiers, s.nodeForTier); err != nil {
		return nil, err
	}
	return nil, nil
}

// needsColocationPins reports whether colocation must be resolved by pinning the
// consumer to specific source-hosting workers rather than by the deterministic
// tier->node equality check.
//
// Pinning is required when:
//   - the consumer is itself placed on a multi-worker tier: its replicas spread
//     across that tier's workers, so we must constrain which worker each lands
//     on; or
//   - the consumer is placed on a worker-backed tier AND a source spans a
//     multi-worker tier: the source's representative tier->node mapping is
//     ambiguous, so node-equality cannot reliably decide whether the consumer's
//     worker hosts the source's data.
//
// A consumer placed entirely on control-plane tiers (no workers) has a fixed
// node, so node-equality soundly matches it against a source's control-plane
// replica regardless of the source's extra multi-worker spread; such a consumer
// must NOT be dragged into the pin path, where it has no worker and would be
// rejected.
func (s *Server) needsColocationPins(consumerTiers []string, sourceTiers map[string][]string) bool {
	if s.anyMultiWorkerTier(consumerTiers) {
		return true
	}
	if !s.anyRemoteTier(consumerTiers) {
		return false
	}
	for _, tiers := range sourceTiers {
		if s.anyMultiWorkerTier(tiers) {
			return true
		}
	}
	return false
}

// anyMultiWorkerTier reports whether any of tiers is backed by more than one up
// worker.
func (s *Server) anyMultiWorkerTier(tiers []string) bool {
	for _, t := range tiers {
		if len(s.workerReg.WorkersForTier(t)) > 1 {
			return true
		}
	}
	return false
}

// anyRemoteTier reports whether any of tiers is backed by at least one up worker
// (i.e. is not a control-plane/native-only tier).
func (s *Server) anyRemoteTier(tiers []string) bool {
	for _, t := range tiers {
		if len(s.workerReg.WorkersForTier(t)) > 0 {
			return true
		}
	}
	return false
}

// colocationPins computes the worker node ids a consumer on consumerTiers must be
// pinned to so each replica co-locates with every source's running data. It
// intersects the workers that host a running replica of every source with the
// workers backing the consumer's tiers. An empty intersection, or a consumer
// that also runs on a native tier (whose replicas cannot reach worker-hosted
// data), is infeasible.
func (s *Server) colocationPins(consumerTiers []string, sources []*db.SharedDataMount) ([]string, error) {
	consumerWorkers := make(map[string]bool)
	nativeConsumerTier := false
	var remoteTiers []string
	for _, t := range consumerTiers {
		ws := s.workerReg.WorkersForTier(t)
		if len(ws) == 0 {
			nativeConsumerTier = true
			continue
		}
		remoteTiers = append(remoteTiers, t)
		for _, w := range ws {
			consumerWorkers[w.NodeID] = true
		}
	}

	// ColocateWorkers is a flat worker set applied round-robin to every tier's
	// replicas, so it can confine at most one worker tier: a worker belonging to
	// one tier would otherwise be stamped onto another tier's replica and
	// rejected as a wrong-tier target. A consumer spanning multiple worker tiers
	// must therefore be rejected rather than deployed onto an unexecutable plan.
	if len(remoteTiers) > 1 {
		sort.Strings(remoteTiers)
		return nil, fmt.Errorf(
			"shared mount cannot be co-located: the consumer is placed on multiple worker tiers %v; place it on a single worker tier so its replicas can pin to the source's worker",
			remoteTiers)
	}

	// common = workers hosting a running replica of every source.
	var common map[string]bool
	for _, m := range sources {
		hosts, err := s.store.RunningReplicaWorkersForSlug(m.SourceSlug)
		if err != nil {
			return nil, fmt.Errorf("running replica workers for %q: %w", m.SourceSlug, err)
		}
		hset := make(map[string]bool, len(hosts))
		for _, h := range hosts {
			hset[h] = true
		}
		if common == nil {
			common = hset
			continue
		}
		for n := range common {
			if !hset[n] {
				delete(common, n)
			}
		}
	}

	pins := make([]string, 0, len(common))
	for n := range common {
		if consumerWorkers[n] {
			pins = append(pins, n)
		}
	}
	sort.Strings(pins)
	if len(pins) == 0 {
		return nil, fmt.Errorf(
			"shared mount cannot be co-located: no worker backing the consumer's tier hosts a running replica of every mounted source; deploy the sources onto a shared worker first")
	}
	if nativeConsumerTier {
		return nil, fmt.Errorf(
			"shared mount cannot be co-located: the consumer also runs on a control-plane tier whose replicas cannot reach worker-hosted source data")
	}
	return pins, nil
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

// appSecretsCleaner removes an app's external secret-backend resources on
// delete. The Fargate runtime implements it; nil disables the step.
type appSecretsCleaner interface {
	CleanupApp(ctx context.Context, appID int64) error
}

// SetSecretsCleaner wires the external secret-backend cleanup invoked on app
// delete. Called at startup when a Fargate secrets backend is configured; left
// nil otherwise. Must be called before the server handles requests.
func (s *Server) SetSecretsCleaner(c appSecretsCleaner) { s.secretsCleaner = c }

// cleanupAppSecrets runs the external secret-backend cleanup for a deleted app,
// or is a no-op when no cleaner is wired.
func (s *Server) cleanupAppSecrets(ctx context.Context, appID int64) error {
	if s.secretsCleaner == nil {
		return nil
	}
	return s.secretsCleaner.CleanupApp(ctx, appID)
}

// SetJobs wires the schedule-runner and the cron scheduler into the API server.
// Must be called before the server begins handling requests.
func (s *Server) SetJobs(j *jobs.Manager, sc *scheduler.Scheduler) {
	s.jobs = j
	s.scheduler = sc
}

// SetOwnership wires the predicate reporting whether this instance holds the
// control-plane ownership lease. Mutating API requests are rejected with 503 on
// a non-owner so that during a zero-downtime handoff only the lease owner
// mutates cluster state. Call this once during startup before the server begins
// handling requests; it is not safe to call concurrently with live traffic.
func (s *Server) SetOwnership(isOwner func() bool) {
	s.isOwner = isOwner
}

// SetCluster marks this instance as part of a multi-instance cluster and
// records its unique identity. In clustered mode, ScaleDown also writes
// desired_state to the DB before the drain wait and polls the fleet-wide
// session count (excluding this instance) alongside the local count. Must be
// called before the server begins handling requests; not safe to call
// concurrently with live traffic.
func (s *Server) SetCluster(instanceID string) {
	s.clustered = true
	s.instanceID = instanceID
}

// ownerGuard rejects cluster-state mutations on a non-owner instance with 503.
// Reads (GET/HEAD/OPTIONS) and the auth endpoints always pass so the read-only
// dashboard stays usable and users can still log in/out during a handoff.
func (s *Server) ownerGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		// The authenticated dashboard must stay usable during a handoff so a user
		// can still end their session. Logout is the only mutating auth route in
		// this group; /api/auth/me is a GET already passed above. Keep this an
		// explicit allowlist so any future /api/auth mutation is gated unless
		// deliberately added here.
		switch r.URL.Path {
		case "/api/auth/logout", "/api/auth/me":
			next.ServeHTTP(w, r)
			return
		}
		if s.isOwner == nil || s.isOwner() {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Retry-After", "2")
		writeError(w, http.StatusServiceUnavailable, "control-plane handoff in progress, retry")
	})
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

func (s *Server) buildRouter() chi.Router {
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
	r.With(s.rateLimitByIP(s.oauthLimiter)).Get("/api/auth/github/callback", s.handleGitHubCallback)
	r.With(s.rateLimitByIP(s.oauthLimiter)).Get("/api/auth/google/login", s.handleGoogleLogin)
	r.With(s.rateLimitByIP(s.oauthLimiter)).Get("/api/auth/google/callback", s.handleGoogleCallback)
	r.Get("/api/auth/providers", s.handleGetProviders)
	r.With(s.rateLimitByIP(s.oauthLimiter)).Get("/api/auth/oidc/login", s.handleOIDCLogin)
	r.With(s.rateLimitByIP(s.oauthLimiter)).Get("/api/auth/oidc/callback", s.handleOIDCCallback)
	r.Get("/api/server-info", s.handleServerInfo)

	// All other endpoints require either an auth header or a valid session cookie.
	bearer := auth.BearerMiddleware(s.cfg.Auth.Secret, s.keyLookup, s.userLookup, s.revocationChecker())
	csrf := auth.CSRFMiddleware(s.cfg.TrustedProxyNets)
	r.Group(func(r chi.Router) {
		r.Use(bearer)
		r.Use(csrf)
		r.Use(s.ownerGuard)

		// Logout is authenticated so we can revoke the caller's JWT by jti.
		// An unauthenticated logout has nothing to revoke — the client can
		// just discard its own session cookie.
		r.Post("/api/auth/logout", s.handleLogout)
		r.Get("/api/auth/me", s.handleMe)
		r.Patch("/api/auth/me", s.handlePatchMe) // self-service profile: display name + own password
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
		r.Patch("/api/apps/{slug}/members/{user_id}", s.handleSetMemberRole)
		r.Get("/api/apps/{slug}/group-access", s.handleGetAppGroupAccess)
		r.Post("/api/apps/{slug}/group-access", s.handleGrantAppGroupAccess)
		r.Delete("/api/apps/{slug}/group-access", s.handleRevokeAppGroupAccess)
		r.Delete("/api/apps/{slug}/group-access/{group}", s.handleRevokeAppGroupAccess)
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
		r.Get("/api/fleet/health", s.handleFleetHealth)               // admin: aggregate fleet health
	})

	return r
}

// Observe wraps the API handler chain (timeout handler included) with server
// tracing and Prometheus instrumentation so both record the status and latency
// the client actually observes - covering recovered panics (the inner chi
// Recoverer writes the 500 before observation reads it) and timeout responses
// (http.TimeoutHandler writes the 503 below observation). Both layers are
// no-ops when their dependency (tracer / metrics registry) is nil, so
// observation is opt-in.
//
// The matched route pattern is resolved once, before the inner chain runs, by
// calling Match on a private route context. The resulting pattern string is
// stashed in the request context via httproute.WithPattern so metrics and
// tracing can read it as an immutable value after the inner handler returns.
// This avoids sharing a mutable chi.RouteContext across an http.TimeoutHandler
// boundary: under a timeout, the TimeoutHandler returns (writing the 503)
// while the inner chi mux goroutine is still mutating the same RouteContext's
// RoutePatterns slice, causing a data race on the outer read.
//
// Must be wired before the server begins handling requests.
func (s *Server) Observe(next http.Handler) http.Handler {
	observed := s.trace(s.instrument(next))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pattern := ""
		rc := chi.NewRouteContext()
		if s.router.Match(rc, r.Method, r.URL.Path) {
			pattern = rc.RoutePattern()
		}
		r = r.WithContext(httproute.WithPattern(r.Context(), pattern))
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

// AuthMappings converts config group-role mappings into the auth-package type.
func AuthMappings(ms []config.GroupRoleMapping) []auth.GroupRoleMapping {
	out := make([]auth.GroupRoleMapping, 0, len(ms))
	for _, m := range ms {
		out = append(out, auth.GroupRoleMapping{Group: m.Group, Role: m.Role})
	}
	return out
}
