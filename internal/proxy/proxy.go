package proxy

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/proxytrust"
	"github.com/rvben/shinyhub/internal/tracing"
)

// recorderCtxKey carries the request's *statusRecorder through the transport so
// a mid-stream upstream read error (which ReverseProxy reports only on the body
// copy, never via ErrorHandler) can be captured onto the span.
type recorderCtxKey struct{}

// errCapturingTransport wraps a RoundTripper so the upstream response body's
// read errors are recorded onto the request's statusRecorder. Pre-response
// failures still flow through ErrorHandler; this covers the post-header case.
type errCapturingTransport struct{ base http.RoundTripper }

func (t *errCapturingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	// A 101 Switching Protocols body must keep its io.ReadWriteCloser so
	// httputil.ReverseProxy can tunnel the upgraded connection (WebSockets for
	// Shiny/Streamlit). Wrapping it in an io.ReadCloser-only type would make the
	// upgrade fail, so leave protocol-switch responses untouched.
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return resp, nil
	}
	if rec, ok := req.Context().Value(recorderCtxKey{}).(*statusRecorder); ok {
		resp.Body = &errCapturingBody{ReadCloser: resp.Body, rec: rec}
	}
	return resp, nil
}

// errCapturingBody records the first non-EOF read error onto rec.proxyErr.
type errCapturingBody struct {
	io.ReadCloser
	rec *statusRecorder
}

func (b *errCapturingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF && b.rec.proxyErr == nil {
		b.rec.proxyErr = err
	}
	return n, err
}

// waitPage builds one of the auto-refreshing wait pages served when a request
// arrives for a slug with no registered backend. The shell (styles, spinner,
// hidden retry button, and a <noscript> meta-refresh fallback for browsers
// with JS disabled, which refreshes forever but at least keeps working) is
// shared; the title, message, and behaviour script differ per variant.
func waitPage(title, msg, script string) string {
	return `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<noscript><meta http-equiv="refresh" content="3"></noscript>
<title>` + title + `</title>
<style>
  html, body { background: #030510; }
  body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif;
         display: flex; align-items: center; justify-content: center;
         height: 100vh; margin: 0; color: #E8EEFF; }
  .box { text-align: center; max-width: 420px; padding: 0 1rem; }
  .spinner { width: 40px; height: 40px; border: 4px solid rgba(56,189,248,0.18);
             border-top-color: #38BDF8; border-radius: 50%;
             animation: spin 0.8s linear infinite; margin: 0 auto 1rem;
             box-shadow: 0 0 12px rgba(56,189,248,0.25); }
  @keyframes spin { to { transform: rotate(360deg); } }
  h1 { font-size: 1.25rem; color: #E8EEFF; margin: 0; font-weight: 600; }
  p  { color: #6B7AA3; font-size: 0.875rem; margin-top: 0.5rem; line-height: 1.4; }
  button { margin-top: 1rem; padding: 0.5rem 1rem; font-size: 0.875rem;
           background: linear-gradient(135deg, #38BDF8, #2DD4BF);
           color: #030510; border: 0; border-radius: 4px; cursor: pointer;
           font-weight: 600; }
  button:hover { filter: brightness(1.1); }
  .error .spinner { display: none; }
  .error h1 { color: #F87171; }
</style>
</head>
<body>
<div class="box" id="shinyhub-box">
  <div class="spinner"></div>
  <h1 id="shinyhub-title">` + title + `</h1>
  <p id="shinyhub-msg">` + msg + `</p>
  <button id="shinyhub-retry" style="display:none">Try again</button>
</div>
<script>
` + script + `
</script>
</body>
</html>`
}

// loadingScript reloads the page every 3 s up to MAX times (~60 s) and then
// switches to an error state with a manual retry button. The per-path retry
// count is stored in sessionStorage; a fresh navigation (not a reload) resets
// it so users can revisit after a previous give-up.
const loadingScript = `(function(){
  var MAX = 20;
  var INTERVAL_MS = 3000;
  var key = 'shinyhub-retry:' + window.location.pathname;
  var nav = (performance.getEntriesByType && performance.getEntriesByType('navigation')[0]) || {};
  if (nav.type !== 'reload') { sessionStorage.removeItem(key); }
  var n = parseInt(sessionStorage.getItem(key) || '0', 10);
  var box = document.getElementById('shinyhub-box');
  var title = document.getElementById('shinyhub-title');
  var msg = document.getElementById('shinyhub-msg');
  var retry = document.getElementById('shinyhub-retry');
  retry.addEventListener('click', function(){
    sessionStorage.removeItem(key);
    window.location.reload();
  });
  if (n >= MAX) {
    box.classList.add('error');
    title.textContent = 'App did not start';
    msg.textContent = 'Gave up after ' + (MAX * INTERVAL_MS / 1000) +
      ' seconds. The app may have failed to deploy or is missing its bundle.';
    retry.style.display = 'inline-block';
    return;
  }
  sessionStorage.setItem(key, String(n + 1));
  setTimeout(function(){ window.location.reload(); }, INTERVAL_MS);
})();`

// deployingScript reloads every 3 s with no give-up cap. While a deployment
// is in flight the server keeps serving the deploying page, and the pending
// deployment row resolves on every handler path (promote, fail, or startup
// reconciliation), so the refresh loop is bounded by the deploy itself, not
// by a client-side count. It also clears the loading page's give-up counter
// so the post-deploy boot phase gets a fresh ~60 s window instead of
// inheriting stale reloads.
const deployingScript = `(function(){
  var key = 'shinyhub-retry:' + window.location.pathname;
  sessionStorage.removeItem(key);
  setTimeout(function(){ window.location.reload(); }, 3000);
})();`

// loadingPage is served on a miss with no in-flight deployment: the app is
// starting (cold boot, wake, or crash-restart). Client-side JS gives up after
// ~60 s with a manual retry button.
var loadingPage = waitPage("Starting app…",
	"This page will refresh automatically.", loadingScript)

// deployingPage is served on a miss while a deployment (deploy or rollback)
// is in flight for the slug: the deploy tears the pool down before the new
// pool boots. The copy is version-neutral because rollbacks take this path
// too. See deployingScript for why it never gives up on its own.
var deployingPage = waitPage("Deploying app…",
	"A deployment is in progress. This can take a few minutes. This page will refresh automatically.",
	deployingScript)

// cookiePrefix is the prefix for the sticky-session cookie name.
// The full cookie name is cookiePrefix + slug (e.g. "shinyhub_rep_myapp").
const cookiePrefix = "shinyhub_rep_"

// readySuffix is the per-slug WS-readiness probe path. The full path is
// /app/<slug>/.shinyhub/ready. The ".shinyhub" segment is namespaced under
// a leading dot so it cannot collide with a legitimate app route.
const readySuffix = "/.shinyhub/ready"

// MsgPoolSaturated is the plain-text body returned with a 503 when every
// replica is at its session cap and the request carries no sticky cookie.
// Exported so docs/scaling.md can be guarded against drift from this string.
const MsgPoolSaturated = "Service temporarily at capacity, please retry."

// LoadingPageSentinel is a string that is always present in the loadingPage
// HTML body. Exported so tests can assert that a response is the loading page
// without comparing the full HTML; if the copy changes and the sentinel is
// removed, the build test fails rather than vacuously passing.
const LoadingPageSentinel = "Starting app"

// DeployingPageSentinel is the deployingPage counterpart of
// LoadingPageSentinel: always present in the deploying wait page and in
// neither of the other miss pages.
const DeployingPageSentinel = "Deploying app"

// replicaBackend wraps a single reverse proxy with connection tracking.
type replicaBackend struct {
	index        int
	targetURL    string // the URL passed to RegisterReplica; used for introspection
	deploymentID int64  // stamped into sticky cookies; mismatch causes re-pick on redeploy
	rp           *httputil.ReverseProxy
	activeConns  atomic.Int64
	// draining marks a slot scheduled for graceful removal. The least-
	// connections picker skips draining slots so no new cookie-less session
	// is routed to it, but the sticky-cookie path still forwards, letting
	// established sessions finish before the slot is stopped. Reset implicitly
	// because RegisterReplica installs a fresh replicaBackend.
	draining atomic.Bool

	// Phase 1 worker-isolation fields. Used only when the owning pool is elastic
	// (mode == grouped or per_session). Zero-valued and unused in multiplex pools.
	slotID          int
	status          workerStatus
	assignedClients int
}

// backendPool holds a fixed-size slice of replicas for one slug.
// rrCounter is used for round-robin tie-breaking in least-connections selection.
//
// maxSessions is the per-replica active-connection cap. When every non-nil
// replica is at or above this value, new requests without a valid sticky
// cookie are shed with 503 (see ServeHTTP). A value of 0 disables the cap.
//
// appID is the numeric database primary key for the owning app. It is set by
// SetPoolAppID at the same time the pool is created so the session reporter
// can key replica_sessions rows without a DB lookup at snapshot time. Zero
// means not yet set (reporter skips pools without an appID).
type backendPool struct {
	size        int
	replicas    []*replicaBackend
	rrCounter   atomic.Int64
	maxSessions int
	// appID is atomic because the Director closure reads it outside p.mu
	// for identity-key derivation; it is written under p.mu by SetPoolAppID.
	appID atomic.Int64
	// identityHeaders gates identity injection per pool. Atomic: the
	// Director runs inside picked.rp.ServeHTTP AFTER p.mu is released,
	// while SetPoolIdentityHeaders performs live updates under p.mu.
	identityHeaders atomic.Bool

	// Phase 1 worker-isolation additions. When mode != multiplex the pool is
	// demand-driven: workers live in the map keyed by monotonic slotID, and
	// replicas (the dense slice) is unused.
	mode        config.WorkerIsolationMode
	groupedSize int
	maxWorkers  int
	workers     map[int]*replicaBackend // slotID -> backend; nil for multiplex
	nextSlotID  int
}

// AccessLogEntry describes a single proxied request. It is passed to the
// callback registered via SetAccessLogger so callers can emit structured
// logs, metrics, or audit records without the proxy package depending on a
// particular logging library.
//
// For routed requests (a replica handled the request) ReplicaIndex is the
// zero-based pool index and Sticky reports whether the sticky-session cookie
// selected the replica. For loading-page responses (no live replica)
// ReplicaIndex is -1.
type AccessLogEntry struct {
	Slug         string
	Method       string
	Path         string
	Status       int
	Bytes        int64
	Duration     time.Duration
	ClientIP     string // trusted-proxy-aware client IP (see SetClientIPResolver)
	Peer         string // raw r.RemoteAddr (direct TCP peer)
	ReplicaIndex int    // -1 when no replica served the request
	Sticky       bool
	// Reject is the platform rejection reason for this request, set only for
	// rejections emitted on the main ServeHTTP path (pool-saturated,
	// pool-degraded, unknown-slug). Empty for routed requests and for
	// readiness-probe rejections (which bypass this access-log path).
	Reject RejectReason
}

// accessLogFn and clientIPFn are named function types so atomic.Pointer can
// hold them. They are set once at startup and read on every request, so
// atomic.Pointer avoids the lock traffic a sync.RWMutex would incur while
// still permitting a safe nil-to-value transition.
type (
	accessLogFn func(AccessLogEntry)
	clientIPFn  func(*http.Request) string
)

// Proxy routes /app/:slug/* to the registered backend pool for that slug.
type Proxy struct {
	mu          sync.RWMutex
	pools       map[string]*backendPool
	wakeTrigger func(slug string)
	// appStatusFn reports an app's lifecycle status and (for a crashed app) its
	// failure reason. When set, a no-backend miss for a "crashed" or "stopped"
	// app serves a clear status page instead of the endlessly-retrying loading
	// page. nil => always serve the loading page (the prior behaviour).
	appStatusFn func(slug string) (status, reason string)
	slugExists  func(slug string) (bool, error)

	// wakeHoldNanos is the maximum time (ns) a request for a not-yet-routable app
	// is held while its wake completes, so a warm resume is served inline instead
	// of via the loading page. 0 disables the hold (immediate loading page).
	// Atomic for a lock-free read on the request path.
	wakeHoldNanos atomic.Int64

	// stickySecret is the HMAC key that signs the per-app sticky-routing cookie.
	// When set, the cookie value carries a signature bound to the app slug and
	// replica index, so a client cannot forge or replay it to pin itself to a
	// replica (and bypass the per-replica session cap). A nil/absent pointer
	// disables signing (the cookie is a bare index), for tests and unconfigured
	// setups. Atomic so the hot path reads it lock-free.
	stickySecret atomic.Pointer[[]byte]

	// trustedProxies holds the CIDRs of upstream proxies allowed to set the
	// X-Forwarded-* / Forwarded headers the proxy forwards to app backends. A
	// request whose immediate peer is NOT in this set has those headers stripped
	// and repopulated from the proxy's own view, so a direct client cannot spoof
	// the app's notion of the client IP, scheme, or host. A nil/absent pointer
	// trusts no peer, which is correct for a directly-exposed proxy. Stored as an
	// atomic pointer so the hot-path Director reads it lock-free.
	trustedProxies atomic.Pointer[[]*net.IPNet]

	accessLog        atomic.Pointer[accessLogFn]
	clientIP         atomic.Pointer[clientIPFn]
	identityProvider atomic.Pointer[identityProviderFn]

	// tracing holds the active tracing config + ring buffer. When traceBuffer
	// is nil the proxy is a no-op for trace propagation: no traceparent header
	// is set, no spans are recorded. The config is read-only after SetTracing
	// and copied by value here so we don't need a lock on the hot path.
	traceCfg    config.TracingConfig
	traceBuffer *tracing.Buffer

	seenMu   sync.RWMutex
	lastSeen map[string]time.Time
	// wsReady tracks slugs for which at least one upstream has completed a
	// WebSocket handshake (HTTP 101) since the last deregister/hibernate.
	// Surfaced via GET /app/<slug>/.shinyhub/ready so deploy scripts can
	// distinguish "process started" from "actually accepting WS connections"
	// - the latter is what end users care about for Shiny/Streamlit apps.
	wsReady map[string]struct{}
	// firstServedAt records, per slug, the first time real user traffic was
	// proxied to a live replica since the last lifecycle reset. Paired with
	// wsReady it lets ConnectivityHealth detect an app that serves pages but whose
	// realtime WebSocket never connects (typically a reverse proxy blocking the
	// upgrade). Reset on deregister/hibernate and once a WebSocket connects.
	firstServedAt map[string]time.Time
	// wsWarned guards the one-shot "serving without WebSocket" ERROR log so a
	// misconfigured proxy logs once per app lifecycle, not once per request.
	wsWarned map[string]struct{}

	// rejects is the in-memory rolling rejection rollup surfaced by apps show.
	rejects *rejectCounter
	// rejectRecorder is an optional sink (the Prometheus registry) for
	// admission-reject events. Nil disables metrics emission (e.g. in tests).
	rejectRecorder atomic.Pointer[RejectRecorder]

	// conns tracks live hijacked (WebSocket) connections for graceful drain on
	// shutdown. instanceDraining marks this instance draining (distinct from the
	// per-replica backend draining flag): while set, /readyz reports unready.
	conns            *connTracker
	instanceDraining atomic.Bool

	// immediateFlush receives a slug when that slug's active-connection count
	// rises from 0 to >0. The session reporter drains this channel to flush
	// that slug's row immediately without waiting for the next periodic tick.
	// Nil when the reporter is not wired (single-node or clustered but not yet
	// started). Writes are non-blocking: a full channel means a flush is already
	// pending for some slug and the current signal can be dropped safely.
	immediateFlush chan string

	// syncedOnce is true once the pool syncer has completed its first
	// synchronisation from the authoritative DB. On single-node deployments it
	// is pre-seeded true at startup (no syncer runs) so /readyz is unaffected.
	// On clustered deployments the pool syncer calls MarkSynced after its first
	// successful pass; until then /readyz returns 503 with reason "syncing".
	syncedOnce atomic.Bool

	// appReadyFunc, when non-nil, overrides IsWSReady for the readiness probe.
	// The pool syncer wires this in clustered mode to answer from the DB replica
	// status so all instances answer consistently. When nil (single-node or
	// before the syncer wires it), serveReadyProbe falls back to IsWSReady.
	appReadyFunc atomic.Pointer[appReadyFuncT]

	// onMissSync, when non-nil, is called synchronously before the loading page
	// is served for a slug with no pool. In clustered mode the pool syncer wires
	// this to SyncSlug so a freshly-active app is served without waiting for the
	// next background tick. On single-node this remains nil and the miss path is
	// byte-for-byte unchanged.
	onMissSync atomic.Pointer[onMissSyncFuncT]

	// Phase 1 worker-isolation: per-client accounting.
	//
	// clients maps slug -> clientID -> *clientSlot. It lives under the SAME pool
	// lock (p.mu) so reservation, binding, and accounting updates are atomic with
	// respect to each other and to ServeHTTP's RLock path. A second lock would
	// invert against ServeHTTP (which holds p.mu.RLock through activeConns bump)
	// and deadlock.
	clients map[string]map[string]*clientSlot

	// terminate is called (via goroutine, never inline under p.mu) when an elastic
	// worker's assignedClients reaches 0 after the grace window expires. Nil
	// disables automatic termination (tests that only test accounting can leave it
	// unset). Wire it via SetTerminateFunc.
	terminate func(slug string, slotID int)

	// spawn is called (via goroutine, never inline under p.mu) when an elastic
	// decisionAllocate reserves a new slot and the request is served the loading
	// page. Tasks 12/13 provide the real implementation; wire via SetSpawnFunc.
	spawn func(slug string, slotID int)

	// memGuard is the optional host-memory admission floor for elastic pools:
	// while the host reports less available memory than the floor, NO new
	// worker is allocated (fresh sessions are shed with 503) but existing
	// bindings keep routing. Atomic so the check runs lock-free on the
	// allocate path; nil disables. Wire via SetMemoryGuard.
	memGuard atomic.Pointer[memoryGuard]
}

// memoryGuard pairs the configured floor with the probe that reads the host's
// currently available memory. probe returns ok=false when no reading is
// available, in which case admission fails open: a broken probe must never
// take the platform down.
type memoryGuard struct {
	minAvailableMB int
	probe          func() (availableMB int, ok bool)
}

// onMissSyncFuncT is the signature for the injected on-miss synchronous sync.
type onMissSyncFuncT func(slug string)

// appReadyFuncT is the signature for the injected per-slug readiness predicate.
type appReadyFuncT func(slug string) bool

func New() *Proxy {
	// wakeHoldNanos defaults to 0 (no hold): the request-holding behaviour is
	// opt-in, wired by main.go for production, so tests and embedders are not
	// implicitly slowed by it (same pattern as SetWakeTrigger).
	return &Proxy{
		pools:         make(map[string]*backendPool),
		lastSeen:      make(map[string]time.Time),
		wsReady:       make(map[string]struct{}),
		firstServedAt: make(map[string]time.Time),
		wsWarned:      make(map[string]struct{}),
		rejects:       newRejectCounter(),
		conns:         newConnTracker(),
		clients:       make(map[string]map[string]*clientSlot),
	}
}

// SetTerminateFunc registers the callback invoked (via a goroutine, never
// inline under p.mu) when an elastic worker's assignedClients count drops to
// zero after the grace window expires. Typical use: Task 12's boot-timeout
// handler and the idle-worker reaper.
func (p *Proxy) SetTerminateFunc(fn func(slug string, slotID int)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.terminate = fn
}

// SetSpawnFunc registers the callback invoked (via a goroutine) when the
// elastic routing decides to allocate a new worker slot (decisionAllocate).
// The callback is responsible for starting the worker process and subsequently
// calling RegisterElasticWorker once the backend is ready to serve. Tasks
// 12/13 wire the real implementation; leaving it unset disables demand-spawn
// (useful in tests that only exercise accounting).
func (p *Proxy) SetSpawnFunc(fn func(slug string, slotID int)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.spawn = fn
}

// SetMemoryGuard arms (or, with a non-positive floor or nil probe, disarms)
// the host-memory admission floor for elastic pools. While probe reports less
// than minAvailableMB of available host memory, requests that would allocate a
// NEW worker are shed with 503; sessions already bound to a worker are
// unaffected. Shedding one incoming session is deliberate: the alternative is
// the kernel OOM-killing a live worker together with every session on it.
func (p *Proxy) SetMemoryGuard(minAvailableMB int, probe func() (int, bool)) {
	if minAvailableMB <= 0 || probe == nil {
		p.memGuard.Store(nil)
		return
	}
	p.memGuard.Store(&memoryGuard{minAvailableMB: minAvailableMB, probe: probe})
}

const (
	// wakePollInterval is how often the hold loop re-checks the pool for a freshly
	// registered replica.
	wakePollInterval = 50 * time.Millisecond
	// wakeSyncInterval throttles the clustered on-miss sync inside the hold loop
	// (the local pool is polled far more often than the cross-instance sync).
	wakeSyncInterval = 400 * time.Millisecond
)

// SetWakeHoldTimeout sets how long a request is held during a wake before the
// loading page is served. 0 disables the hold (the loading page is served
// immediately, the pre-hold behaviour). Called once at startup.
func (p *Proxy) SetWakeHoldTimeout(d time.Duration) {
	if d < 0 {
		d = 0
	}
	p.wakeHoldNanos.Store(int64(d))
}

// holdForWake fires the wake trigger and waits up to the configured hold window
// for slug's pool to become routable, so a warm (or fast cold) resume is served
// inline instead of via the loading page. It returns true when the pool is ready
// (the caller routes the request) and false when the hold expired, the client
// disconnected, or the app is down (the caller serves the miss page). A
// crashed/stopped app is never held. ctx is the request context, so a client
// that gives up frees the hold immediately rather than polling to the deadline.
func (p *Proxy) holdForWake(ctx context.Context, slug string, trigger func(string)) bool {
	syncSuppressed := false
	if fn := p.getAppStatusLookup(); fn != nil {
		switch status, _ := fn(slug); status {
		case "crashed", "stopped":
			return false // will not come up; let the caller serve the down page now
		case "deploying":
			// A deployment is in flight: suppress the clustered on-miss sync,
			// which could re-register stale replica rows for the pool the
			// deploy just tore down (the background syncer still converges).
			// The wake trigger deliberately KEEPS firing: BeginWake is a
			// hibernated->waking CAS, so it is a no-op during a genuine deploy
			// (status is stopped/running/crashed then), and for a hibernated
			// app whose newest row is a stale pending one (a PromoteDeployment
			// failure) it is the only demand-wake path - suppressing it would
			// pin visitors on the deploying page forever. Still hold: a fast
			// redeploy that finishes within the window is served inline with
			// no interstitial at all.
			syncSuppressed = true
		}
	}
	hold := time.Duration(p.wakeHoldNanos.Load())
	if trigger != nil {
		go trigger(slug)
	}
	syncFn := p.onMissSync.Load()
	if syncSuppressed {
		syncFn = nil
	}
	if hold <= 0 {
		// Hold disabled: preserve the original one-shot clustered sync + a single
		// pool check, then fall back to the loading page immediately.
		if syncFn != nil {
			(*syncFn)(slug)
		}
		return p.poolRoutable(slug)
	}
	deadline := time.Now().Add(hold)
	nextSync := time.Now()
	for {
		now := time.Now()
		if syncFn != nil && !now.Before(nextSync) {
			(*syncFn)(slug)
			nextSync = now.Add(wakeSyncInterval)
		}
		if p.poolRoutable(slug) {
			return true
		}
		if !now.Before(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false // client gave up: stop holding the connection
		case <-time.After(wakePollInterval):
		}
	}
}

// poolRoutable reports whether slug currently has at least one routable replica.
func (p *Proxy) poolRoutable(slug string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool := p.pools[slug]
	return pool != nil && poolHasAny(pool)
}

// PoolHasAny reports whether slug currently has at least one routable backend
// (a registered replica for multiplex pools, or a running/booting elastic
// worker for elastic pools). It is the exported counterpart of poolRoutable
// used by lifecycle tests and external health checks.
func (p *Proxy) PoolHasAny(slug string) bool {
	return p.poolRoutable(slug)
}

// ElasticWorkerCount returns the number of workers (in any state) currently
// tracked in the elastic pool for slug. Returns 0 for unknown slugs or
// non-elastic pools. Intended for tests to assert that termination/deregistration
// correctly empties the workers map (PoolHasAny always returns true for elastic
// pools because they route on demand; it cannot be used for this assertion).
func (p *Proxy) ElasticWorkerCount(slug string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool, ok := p.pools[slug]
	if !ok || !poolIsElastic(pool) {
		return 0
	}
	return len(pool.workers)
}

// SetAccessLogger registers a callback invoked once per proxied request
// (including responses served from the loading page). Pass nil to disable.
// Safe to call concurrently with ServeHTTP; subsequent requests observe the
// new value atomically.
func (p *Proxy) SetAccessLogger(fn func(AccessLogEntry)) {
	if fn == nil {
		p.accessLog.Store(nil)
		return
	}
	f := accessLogFn(fn)
	p.accessLog.Store(&f)
}

// SetClientIPResolver registers a function that returns the trusted-proxy-aware
// client IP for an incoming request. When unset (or when the resolver returns
// ""), the proxy falls back to the host portion of r.RemoteAddr. This is how
// the surrounding Server injects its trusted-proxy configuration into the
// proxy without forming an import cycle. Safe to call concurrently with
// ServeHTTP.

// SetStickySecret enables HMAC signing of the sticky-routing cookie with the
// given key (derive it from the server auth secret). Wire it at startup; when
// unset the cookie carries a bare index and deployment ID without a signature.
func (p *Proxy) SetStickySecret(key []byte) {
	p.stickySecret.Store(&key)
}

func (p *Proxy) stickySecretBytes() []byte {
	if ptr := p.stickySecret.Load(); ptr != nil {
		return *ptr
	}
	return nil
}

// signStickyValue returns the signed sticky-cookie value "<index>.<deploymentID>.<hmac16>".
// The HMAC binds slug, index, and deploymentID so a value cannot be replayed for a
// different app, replica index, or deployment. Old 2-part cookies and bare integers do
// not satisfy the 3-part format and are treated as stale, causing a re-pick.
func signStickyValue(key []byte, slug string, index int, deploymentID int64) string {
	mac := hmac.New(sha256.New, key)
	fmt.Fprintf(mac, "%s\x00%d\x00%d", slug, index, deploymentID)
	return strconv.Itoa(index) + "." + strconv.FormatInt(deploymentID, 10) + "." + hex.EncodeToString(mac.Sum(nil))[:16]
}

// verifyStickyValue parses and authenticates a signed sticky-cookie value.
// Returns (index, deploymentID, true) only when the value is exactly 3 parts
// and the HMAC matches. A 2-part old cookie or bare integer returns (0, 0, false).
func verifyStickyValue(key []byte, slug, value string) (int, int64, bool) {
	// Require exactly "<idx>.<depID>.<hmac>" — 3 dot-separated parts.
	first, rest, found := strings.Cut(value, ".")
	if !found {
		return 0, 0, false
	}
	depStr, _, found2 := strings.Cut(rest, ".") // _ is the hmac segment; the full value is validated by hmac.Equal below
	if !found2 {
		// 2-part old format: stale.
		return 0, 0, false
	}
	idx, err := strconv.Atoi(first)
	if err != nil {
		return 0, 0, false
	}
	depID, err := strconv.ParseInt(depStr, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	if !hmac.Equal([]byte(value), []byte(signStickyValue(key, slug, idx, depID))) {
		return 0, 0, false
	}
	return idx, depID, true
}

// stickyIndex extracts the replica index and deployment ID from a sticky-cookie
// value, verifying its signature when signing is enabled. In unsigned mode the
// expected format is "<idx>.<deploymentID>"; a bare integer is treated as stale
// (returns false) so the request re-picks via least-connections.
func (p *Proxy) stickyIndex(slug, value string) (int, int64, bool) {
	if key := p.stickySecretBytes(); len(key) > 0 {
		return verifyStickyValue(key, slug, value)
	}
	// Unsigned mode: expect "<idx>.<deploymentID>".
	idxStr, depStr, found := strings.Cut(value, ".")
	if !found {
		// Bare integer: old unsigned cookie treated as stale.
		return 0, 0, false
	}
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		return 0, 0, false
	}
	depID, err := strconv.ParseInt(depStr, 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return idx, depID, true
}

// stickyCookieAlreadyPins reports whether the request already carries a valid
// sticky cookie pinning exactly (index, deploymentID), so it need not be
// re-signed and re-set. Used by the elastic route to avoid an HMAC + Set-Cookie
// on every steady-state request (the server-side client binding is the routing
// authority there; the cookie is only a hint).
func (p *Proxy) stickyCookieAlreadyPins(r *http.Request, slug string, index int, deploymentID int64) bool {
	c, err := r.Cookie(cookiePrefix + slug)
	if err != nil {
		return false
	}
	idx, dep, ok := p.stickyIndex(slug, c.Value)
	return ok && idx == index && dep == deploymentID
}

// stickyCookieValue returns the value to store in the sticky cookie for a
// replica index and deployment ID: signed when a key is configured,
// "<idx>.<deploymentID>" in unsigned mode.
func (p *Proxy) stickyCookieValue(slug string, index int, deploymentID int64) string {
	if key := p.stickySecretBytes(); len(key) > 0 {
		return signStickyValue(key, slug, index, deploymentID)
	}
	return strconv.Itoa(index) + "." + strconv.FormatInt(deploymentID, 10)
}

// SetTrustedProxies configures the upstream-proxy CIDRs whose forwarding
// headers are trusted (see Proxy.trustedProxies). Wire it from
// cfg.TrustedProxyNets at startup, before serving. Safe to leave unset (trust
// no peer) for a directly-exposed deployment.
func (p *Proxy) SetTrustedProxies(nets []*net.IPNet) {
	p.trustedProxies.Store(&nets)
}

// trustedProxyNets returns the configured trusted-proxy CIDRs, or nil when none
// are set (trust no peer).
func (p *Proxy) trustedProxyNets() []*net.IPNet {
	if ptr := p.trustedProxies.Load(); ptr != nil {
		return *ptr
	}
	return nil
}

func (p *Proxy) SetClientIPResolver(fn func(*http.Request) string) {
	if fn == nil {
		p.clientIP.Store(nil)
		return
	}
	f := clientIPFn(fn)
	p.clientIP.Store(&f)
}

// SetTracing wires the tracing configuration and shared ring buffer into the
// proxy. Must be called once at startup before traffic arrives. Passing a nil
// buffer disables span recording but still propagates traceparent when
// cfg.Enabled — useful for testing or for deployments that want apps to trace
// without surfacing anything in the UI.
func (p *Proxy) SetTracing(cfg config.TracingConfig, buf *tracing.Buffer) {
	p.mu.Lock()
	p.traceCfg = cfg
	p.traceBuffer = buf
	p.mu.Unlock()
}

// SetWakeTrigger registers a callback invoked (in a goroutine) when a request
// arrives for a slug with no registered backend, or when a forward error
// occurs in clustered mode. The callback issues the BeginWake CAS and, if this
// instance is the active owner, drives the wake inline. Called at startup on
// EVERY instance (not owner-gated) so a standby can arm the DB waking transition
// even though only the active executes the deploy.
func (p *Proxy) SetWakeTrigger(fn func(string)) {
	p.mu.Lock()
	p.wakeTrigger = fn
	p.mu.Unlock()
}

// getWakeTrigger returns the current wake trigger under the read lock.
func (p *Proxy) getWakeTrigger() func(string) {
	p.mu.RLock()
	fn := p.wakeTrigger
	p.mu.RUnlock()
	return fn
}

// renderAppDownPage builds a static (non-reloading) page for an app that will
// not come up on its own: "crashed" shows the failure reason, "stopped" explains
// it was stopped. Both link to the app's dashboard page, where an operator sees
// the full detail and can Restart. The reason is HTML-escaped (it carries an app
// log tail that may contain markup-like characters).
func renderAppDownPage(state, slug, reason string) string {
	esc := html.EscapeString
	heading, body := "This app is stopped", `<p>It has been stopped and is not currently running.</p>`
	if state == "crashed" {
		heading = "This app crashed"
		if strings.TrimSpace(reason) != "" {
			body = `<pre class="reason">` + esc(reason) + `</pre>`
		} else {
			body = `<p>Its replicas could not be started.</p>`
		}
	}
	return `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>` + esc(heading) + `</title>
<style>
  body { font-family: system-ui, -apple-system, sans-serif; background:#0b1020; color:#e8eeff; margin:0; min-height:100vh; display:flex; align-items:center; justify-content:center; }
  .card { max-width:680px; padding:32px; }
  h1 { color:#f87171; font-size:1.4rem; margin:0 0 12px; }
  pre.reason { background:rgba(248,113,113,0.12); border:1px solid #f87171; border-radius:8px; padding:14px; overflow:auto; max-height:320px; white-space:pre-wrap; word-break:break-word; font-size:0.82rem; line-height:1.5; }
  a.btn { display:inline-block; margin-top:18px; background:#3b82f6; color:#fff; text-decoration:none; padding:10px 18px; border-radius:8px; }
</style></head>
<body><div class="card">
<h1>` + esc(heading) + `</h1>
` + body + `
<a class="btn" href="/apps/` + esc(slug) + `">Open in dashboard</a>
</div></body></html>`
}

// SetAppStatusLookup registers a callback that reports an app's lifecycle status
// and (for a crashed app) its failure reason. It lets a no-backend miss for a
// crashed/stopped app render a clear status page instead of the loading spinner.
// Called once at startup; leaving it unset preserves the loading-page behaviour.
func (p *Proxy) SetAppStatusLookup(fn func(slug string) (status, reason string)) {
	p.mu.Lock()
	p.appStatusFn = fn
	p.mu.Unlock()
}

// getAppStatusLookup returns the current status lookup under the read lock.
func (p *Proxy) getAppStatusLookup() func(string) (string, string) {
	p.mu.RLock()
	fn := p.appStatusFn
	p.mu.RUnlock()
	return fn
}

// serveMissPage responds to a request for a slug with no live backend. A
// crashed or stopped app gets a clear, static status page so the user sees why
// it is unavailable; an app with a deployment in flight gets the deploying
// wait page (auto-refresh, no give-up, no wake); anything else fires the wake
// trigger and gets the auto-retrying loading page (the normal cold-start path).
func (p *Proxy) serveMissPage(w http.ResponseWriter, slug string, trigger func(string)) {
	if fn := p.getAppStatusLookup(); fn != nil {
		switch status, reason := fn(slug); status {
		case "crashed":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(renderAppDownPage("crashed", slug, reason))) //nolint:errcheck
			return
		case "stopped":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(renderAppDownPage("stopped", slug, ""))) //nolint:errcheck
			return
		case "deploying":
			// A deployment is in flight for this slug (the deploy tears the
			// pool down before the new pool boots). Serve the deploy-aware
			// wait page: no give-up countdown (the pending deployment row
			// resolves on every handler path and the server stops serving
			// this page the moment it does). This branch does not re-fire the
			// wake trigger itself: on the miss path holdForWake already fired
			// it, and on the upstream-error path dead-replica recovery belongs
			// to the watchdog.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(deployingPage)) //nolint:errcheck
			return
		}
	}
	if trigger != nil {
		go trigger(slug)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(loadingPage)) //nolint:errcheck
}

// SetSlugExists registers a synchronous predicate that the proxy uses to
// distinguish a known-but-not-running slug (serve loading page) from a
// completely unknown slug (return 404). The predicate returns
// (exists, lookupErr); a non-nil err signals the lookup itself failed
// (DB unavailable, context cancelled, etc.) — the proxy must not interpret
// this as "slug missing" and 404 the user. When unset, the proxy falls back
// to always serving the loading page on miss — matching the legacy behaviour
// from before the predicate was wired up.
func (p *Proxy) SetSlugExists(fn func(string) (bool, error)) {
	p.mu.Lock()
	p.slugExists = fn
	p.mu.Unlock()
}

// RecordActivity marks slug as seen at the current time. It also records the
// first time real traffic was served (firstServedAt) and, once an app has been
// serving longer than wsWarnGrace without any WebSocket connecting, logs a
// one-shot ERROR: the realtime channel is not getting through, which usually
// means a reverse proxy is not forwarding the WebSocket upgrade.
func (p *Proxy) RecordActivity(slug string) {
	p.seenMu.Lock()
	now := time.Now()
	p.lastSeen[slug] = now
	var warn bool
	if _, ready := p.wsReady[slug]; !ready {
		if first, ok := p.firstServedAt[slug]; !ok {
			p.firstServedAt[slug] = now
		} else if _, warned := p.wsWarned[slug]; !warned && now.Sub(first) > wsWarnGrace {
			p.wsWarned[slug] = struct{}{}
			warn = true
		}
	}
	p.seenMu.Unlock()
	if warn {
		slog.Error("proxy: app is serving HTTP but no WebSocket has connected; interactions will fail - the reverse proxy may be blocking WebSocket upgrades (see docs/reverse-proxy/caddy.md)",
			"slug", slug, "grace", wsWarnGrace)
	}
}

// LastSeen returns the last time a request was successfully proxied for slug.
// Returns zero time if slug has never been proxied.
func (p *Proxy) LastSeen(slug string) time.Time {
	p.seenMu.RLock()
	defer p.seenMu.RUnlock()
	return p.lastSeen[slug]
}

// MarkWSReady records that slug has completed at least one WebSocket
// handshake since the last lifecycle reset. Idempotent. Normally called
// by the statusRecorder's onUpgrade hook when the reverse proxy emits
// 101 Switching Protocols, but exported so adapters that route WS traffic
// outside the standard reverse-proxy path can still feed the probe.
func (p *Proxy) MarkWSReady(slug string) {
	p.seenMu.Lock()
	p.wsReady[slug] = struct{}{}
	// A completed handshake ends the "serving without WebSocket" window for this
	// lifecycle: drop the tracking so the warning cannot re-trip until the pool is
	// reset (deregister/hibernate).
	delete(p.firstServedAt, slug)
	delete(p.wsWarned, slug)
	p.seenMu.Unlock()
}

// IsWSReady reports whether slug has observed a 101 Switching Protocols
// response since the last deregister/hibernate.
func (p *Proxy) IsWSReady(slug string) bool {
	p.seenMu.RLock()
	defer p.seenMu.RUnlock()
	_, ok := p.wsReady[slug]
	return ok
}

// wsWarnGrace is how long an app may serve real HTTP traffic without any
// completed WebSocket handshake before the proxy treats the realtime channel as
// broken. A Shiny/Streamlit client opens its WebSocket within a second or two of
// loading, so a sustained gap means the upgrade is not getting through - most
// commonly a reverse proxy that does not forward WebSocket upgrades.
const wsWarnGrace = 20 * time.Second

// ConnectivityHealth reports the realtime-connection health for slug since its
// pool was last (re)registered:
//
//   - everConnected is true once at least one WebSocket handshake has completed.
//   - servingWithoutWS is true when the app has served real traffic for longer
//     than wsWarnGrace but no WebSocket has ever connected - the signature of a
//     reverse proxy blocking the WebSocket upgrade, which leaves the page
//     rendering while every interaction fails.
//
// The two are mutually exclusive; a never-served or still-within-grace app
// reports (false, false). The app-detail envelope combines this with the app's
// running state to surface an operator warning.
func (p *Proxy) ConnectivityHealth(slug string) (everConnected, servingWithoutWS bool) {
	p.seenMu.RLock()
	defer p.seenMu.RUnlock()
	if _, ok := p.wsReady[slug]; ok {
		return true, false
	}
	if first, ok := p.firstServedAt[slug]; ok && time.Since(first) > wsWarnGrace {
		return false, true
	}
	return false, false
}

// MarkSynced marks the proxy as having completed at least one pool
// synchronisation from the authoritative DB. On single-node deployments this
// is called at startup so /readyz is unchanged. On clustered deployments the
// pool syncer calls this after its first successful pass.
func (p *Proxy) MarkSynced() { p.syncedOnce.Store(true) }

// SyncedOnce reports whether MarkSynced has been called at least once.
func (p *Proxy) SyncedOnce() bool { return p.syncedOnce.Load() }

// SetAppReadyFunc wires an injected predicate that serveReadyProbe uses
// instead of IsWSReady when determining whether a slug is ready. When fn is
// nil (the default), serveReadyProbe reverts to IsWSReady. Called at most once
// at startup; reads on the hot path are lock-free via atomic.Pointer.
func (p *Proxy) SetAppReadyFunc(fn func(slug string) bool) {
	if fn == nil {
		p.appReadyFunc.Store(nil)
		return
	}
	f := appReadyFuncT(fn)
	p.appReadyFunc.Store(&f)
}

// SetOnMissSync registers a function called synchronously when a request
// arrives for a slug with no live pool, before the loading page is served.
// In clustered mode the pool syncer wires this to SyncSlug so a freshly-
// active app becomes routable on the very first request rather than after the
// next background tick. When fn is nil (the default, and always on single-node)
// the miss path is byte-for-byte unchanged. Called at most once at startup.
func (p *Proxy) SetOnMissSync(fn func(slug string)) {
	if fn == nil {
		p.onMissSync.Store(nil)
		return
	}
	f := onMissSyncFuncT(fn)
	p.onMissSync.Store(&f)
}

// clearWSReadyLocked drops any cached readiness for slug and resets the
// serving-without-WebSocket tracking, so a re-woken or re-registered pool gets a
// fresh detection window. Caller must hold p.seenMu for writing.
func (p *Proxy) clearWSReadyLocked(slug string) {
	delete(p.wsReady, slug)
	delete(p.firstServedAt, slug)
	delete(p.wsWarned, slug)
}

// clearWSReady drops any cached readiness for slug.
func (p *Proxy) clearWSReady(slug string) {
	p.seenMu.Lock()
	p.clearWSReadyLocked(slug)
	p.seenMu.Unlock()
}

// SetPoolSize initialises or resizes the replica pool for slug.
// It is idempotent: growing preserves existing replicas; shrinking drops
// trailing slots. Callers must invoke this before RegisterReplica.
// The per-replica session cap is preserved across resizes; set it separately
// via SetPoolCap.
func (p *Proxy) SetPoolSize(slug string, size int) {
	if size < 1 {
		size = 1
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pool, ok := p.pools[slug]
	if !ok {
		p.pools[slug] = &backendPool{size: size, replicas: make([]*replicaBackend, size)}
		return
	}
	if size < len(pool.replicas) {
		pool.replicas = pool.replicas[:size]
	}
	for len(pool.replicas) < size {
		pool.replicas = append(pool.replicas, nil)
	}
	pool.size = size
}

// SetPoolCap sets the per-replica active-session cap for slug. Once every
// non-nil replica reaches this count, new requests without a valid sticky
// cookie are shed with 503 Retry-After. A value of 0 means unlimited.
// Creates the pool (size 1) if it does not yet exist so callers can configure
// the cap before spawning replicas.
func (p *Proxy) SetPoolCap(slug string, max int) {
	if max < 0 {
		max = 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pool, ok := p.pools[slug]
	if !ok {
		pool = &backendPool{size: 1, replicas: make([]*replicaBackend, 1)}
		p.pools[slug] = pool
	}
	pool.maxSessions = max
}

// SetPoolMode sets the worker-isolation mode and elastic sizing parameters for
// slug's pool. When mode is grouped or per_session the pool switches to
// demand-driven routing via the workers map; when mode is multiplex (or the
// empty string) the pool reverts to the dense replicas slice. Mirrors
// SetPoolCap's locking pattern. Creates the pool (size 1) if absent.
func (p *Proxy) SetPoolMode(slug string, mode config.WorkerIsolationMode, groupedSize, maxWorkers int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pool, ok := p.pools[slug]
	if !ok {
		pool = &backendPool{size: 1, replicas: make([]*replicaBackend, 1)}
		p.pools[slug] = pool
	}
	pool.mode = mode
	pool.groupedSize = groupedSize
	pool.maxWorkers = maxWorkers
	if mode != config.IsolationMultiplex && mode != "" {
		// Elastic mode: initialise workers map if not already set.
		if pool.workers == nil {
			pool.workers = make(map[int]*replicaBackend)
		}
	} else {
		// Multiplex (including zero value): clear any elastic state so a stale
		// workers map never makes the pool look routable via poolHasAny.
		pool.workers = nil
	}
}

// SetPoolAppID records the numeric database primary key for the app that owns
// slug's pool. The session reporter uses this to write replica_sessions rows
// keyed by app_id without a DB lookup at snapshot time. Call this alongside
// SetPoolSize whenever the app's ID is known. Creates the pool (size 1) if it
// does not yet exist. A zero appID is ignored so callers that do not know the
// ID (e.g. single-node paths that never use the reporter) are safe to omit it.
func (p *Proxy) SetPoolAppID(slug string, appID int64) {
	if appID == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pool, ok := p.pools[slug]
	if !ok {
		pool = &backendPool{size: 1, replicas: make([]*replicaBackend, 1)}
		p.pools[slug] = pool
	}
	pool.appID.Store(appID)
}

// EnableImmediateFlush wires the channel that the session reporter reads to
// detect 0->active transitions. The channel must be buffered (capacity >= 1).
// Call once at startup before serving; never call it again. Passing a nil
// channel is a no-op (disables the feature, used by single-node paths).
func (p *Proxy) EnableImmediateFlush(ch chan string) {
	if ch == nil {
		return
	}
	p.mu.Lock()
	p.immediateFlush = ch
	p.mu.Unlock()
}

// backendResponseHeaderTimeout bounds how long the proxy waits for an app to
// send its response headers after the request is written. A hung app (stuck in
// a long computation or deadlocked) that accepts the connection but never
// responds would otherwise block the forwarding goroutine indefinitely; this
// releases it, routing through the ErrorHandler (wake + loading page) instead.
// It bounds only the header wait, so streaming response bodies and WebSocket
// upgrades (whose 101 headers arrive immediately) are unaffected.
const backendResponseHeaderTimeout = 120 * time.Second

// newBackendTransport returns the HTTP transport for local (native/docker) and
// VPC-internal (fargate) app backends. It mirrors http.DefaultTransport's
// connection settings but adds a ResponseHeaderTimeout backstop and raises
// MaxIdleConnsPerHost: every app is a single host that may carry many
// concurrent Shiny sessions, so the net/http default of 2 idle conns per host
// causes needless connection churn.
func newBackendTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = backendResponseHeaderTimeout
	t.MaxIdleConnsPerHost = 64
	return t
}

// defaultBackendTransport is the shared transport used for nil-base replica
// registrations. *http.Transport is safe for concurrent use and pools
// connections, so a single shared instance is correct and avoids the
// connection leak a per-replica transport would cause.
var defaultBackendTransport = newBackendTransport()

// RegisterReplica registers a backend URL at the given index within slug's pool.
// base is the HTTP transport used for outbound requests; nil uses the tuned
// defaultBackendTransport. Remote tunnel URLs may carry a path prefix (e.g. /v1/data/<token>)
// that is prepended to every forwarded app-relative path. deploymentID is stamped
// into the sticky cookie so a stale cookie from a previous deployment causes a
// re-pick rather than pinning the client to a potentially wrong replica.
// Returns an error if the pool size has not been set or the index is out of range.
func (p *Proxy) RegisterReplica(slug string, index int, targetURL string, base http.RoundTripper, deploymentID int64) error {
	target, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("register %s#%d: invalid url: %w", slug, index, err)
	}
	if target.Scheme == "" || target.Host == "" {
		return fmt.Errorf("register %s#%d: url needs scheme and host", slug, index)
	}
	if base == nil {
		base = defaultBackendTransport
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	pool, ok := p.pools[slug]
	if !ok || index < 0 || index >= pool.size {
		return fmt.Errorf("register %s#%d: pool size not set or index out of range", slug, index)
	}

	rp := httputil.NewSingleHostReverseProxy(target)
	slugCopy := slug
	targetPath := strings.TrimRight(target.Path, "/")
	// Capture pre-response upstream failures (connection refused, timeout)
	// onto the statusRecorder so the trace span surfaces span.Error.
	//
	// A pre-response upstream error means the backend is unreachable: the
	// replica was hibernated, stopped, or died between its pool registration
	// and this forwarded request. Trigger a wake and serve the loading page
	// (HTTP 200) so the client retries while the replica is (re)started, instead
	// of a dead-end 502. The loading page self-limits to ~60 s of retries
	// (loadingPageMaxRetries on the client), so a genuinely broken backend ends
	// in the bounded "try again" page rather than looping. This recovery is
	// unconditional: it applies to single-node and clustered deployments alike
	// (the wake trigger is wired on every instance), since the dead-replica race
	// happens in both.
	rp.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		slog.Warn("proxy_upstream_error", "slug", slugCopy, "error", err.Error())
		if sr, ok := w.(*statusRecorder); ok {
			sr.proxyErr = err
		}
		p.serveMissPage(w, slugCopy, p.getWakeTrigger())
	}
	// ErrorHandler does not fire for failures after the response header was
	// sent: ReverseProxy reports those only on the body copy. Wrap the
	// transport so a mid-stream upstream read error is captured too, which is
	// the only signal that admits an otherwise-200, not-slow span to the buffer.
	rp.Transport = &errCapturingTransport{base: base}
	// Strip any backend Set-Cookie that collides with ShinyHub's reserved
	// cookie namespaces so a deployer-controlled app cannot set the platform's
	// session/sticky/elastic-client-id cookies in a visitor's browser.
	rp.ModifyResponse = filterReservedSetCookies
	rp.Director = func(req *http.Request) {
		// Populate standard forwarding headers so backend apps (uvicorn with
		// --proxy-headers, R Shiny httpuv, Dash, custom FastAPI, etc.) can
		// reconstruct the external request. We only set when absent so an
		// edge proxy (nginx/Caddy) terminating TLS upstream keeps authority.
		// Go's ReverseProxy appends the direct peer to X-Forwarded-For
		// after the Director runs; we don't duplicate that.
		//
		// The RFC 7239 Forwarded header carries the client source port
		// alongside the IP — useful for backends that parse it. Uvicorn's
		// ProxyHeadersMiddleware does NOT read Forwarded and hardcodes
		// scope["client"] port to 0 whenever it sees X-Forwarded-For; that
		// zero-port behaviour is an uvicorn design choice, not something
		// this proxy can fix from here.
		scheme := "http"
		if req.TLS != nil {
			scheme = "https"
		}
		clientIP := ""
		if cip, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
			clientIP = cip
		}
		// Trust client-supplied forwarding headers only from a configured proxy
		// peer; for a direct/untrusted client, strip and repopulate them from our
		// own view so the backend app cannot be fed a spoofed client IP, scheme,
		// or host.
		applyForwardingHeaders(req, scheme, clientIP, proxytrust.PeerIsTrusted(req, p.trustedProxyNets()))
		// Never leak ShinyHub's own auth/session/sticky cookies to the
		// (deployer-controlled) app backend.
		stripInternalCookies(req)
		// Strip inbound platform identity headers and (when enabled for
		// this pool and a user is authenticated) inject the real ones.
		applyIdentityHeaders(req, pool, slugCopy, &p.identityProvider)

		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		prefix := "/app/" + slugCopy
		appRelative := strings.TrimPrefix(req.URL.Path, prefix)
		if appRelative == "" {
			appRelative = "/"
		}
		req.URL.Path = singleJoiningSlash(targetPath, appRelative)
		if req.URL.RawPath != "" {
			rawRelative := strings.TrimPrefix(req.URL.RawPath, prefix)
			if rawRelative == "" {
				rawRelative = "/"
			}
			req.URL.RawPath = singleJoiningSlash(targetPath, rawRelative)
		}
		req.Host = target.Host
	}
	pool.replicas[index] = &replicaBackend{index: index, targetURL: targetURL, deploymentID: deploymentID, rp: rp}
	// A freshly registered backend has not yet proven it accepts WS upgrades.
	// Clearing here also covers the hot-redeploy path where a caller swaps
	// replicas without an intermediate Deregister.
	p.clearWSReady(slug)
	return nil
}

// RegisterElasticWorker installs a ready backend into an elastic pool's worker
// map at slotID. It is called by the spawn callback (Task 12/13) once the
// native or Docker process is listening. The pool must already be elastic
// (SetPoolMode with grouped or per_session) and slotID must have been allocated
// by a prior reserveWorker call. The Director and transport setup mirrors
// RegisterReplica exactly so the forwarding behaviour is identical.
//
// If a booting placeholder already exists for slotID (inserted by reserveWorker),
// it is updated in-place to preserve assignedClients; a brand-new entry is
// created only when the slot is absent (e.g. called out of order in tests).
func (p *Proxy) RegisterElasticWorker(slug string, slotID int, targetURL string, base http.RoundTripper, deploymentID int64) error {
	target, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("register elastic %s#%d: invalid url: %w", slug, slotID, err)
	}
	if target.Scheme == "" || target.Host == "" {
		return fmt.Errorf("register elastic %s#%d: url needs scheme and host", slug, slotID)
	}
	if base == nil {
		base = defaultBackendTransport
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	pool, ok := p.pools[slug]
	if !ok || !poolIsElastic(pool) {
		return fmt.Errorf("register elastic %s#%d: pool not found or not elastic", slug, slotID)
	}

	slugCopy := slug
	targetPath := strings.TrimRight(target.Path, "/")

	rp := httputil.NewSingleHostReverseProxy(target)
	rp.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		slog.Warn("proxy_upstream_error", "slug", slugCopy, "error", err.Error())
		if sr, ok := w.(*statusRecorder); ok {
			sr.proxyErr = err
		}
		p.serveMissPage(w, slugCopy, p.getWakeTrigger())
	}
	rp.Transport = &errCapturingTransport{base: base}
	// Strip any backend Set-Cookie that collides with ShinyHub's reserved
	// cookie namespaces so a deployer-controlled app cannot set the platform's
	// session/sticky/elastic-client-id cookies in a visitor's browser.
	rp.ModifyResponse = filterReservedSetCookies
	rp.Director = func(req *http.Request) {
		scheme := "http"
		if req.TLS != nil {
			scheme = "https"
		}
		clientIP := ""
		if cip, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
			clientIP = cip
		}
		applyForwardingHeaders(req, scheme, clientIP, proxytrust.PeerIsTrusted(req, p.trustedProxyNets()))
		stripInternalCookies(req)
		applyIdentityHeaders(req, pool, slugCopy, &p.identityProvider)

		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		prefix := "/app/" + slugCopy
		appRelative := strings.TrimPrefix(req.URL.Path, prefix)
		if appRelative == "" {
			appRelative = "/"
		}
		req.URL.Path = singleJoiningSlash(targetPath, appRelative)
		if req.URL.RawPath != "" {
			rawRelative := strings.TrimPrefix(req.URL.RawPath, prefix)
			if rawRelative == "" {
				rawRelative = "/"
			}
			req.URL.RawPath = singleJoiningSlash(targetPath, rawRelative)
		}
		req.Host = target.Host
	}

	// Update the existing booting placeholder in-place to preserve the
	// assignedClients count incremented by bindClient. Create a fresh entry
	// only when the slot is absent (defensive: normal flow always creates a
	// placeholder via reserveWorker before RegisterElasticWorker is called).
	if existing := pool.workers[slotID]; existing != nil {
		existing.rp = rp
		existing.targetURL = targetURL
		existing.deploymentID = deploymentID
		existing.status = workerRunning
	} else {
		if pool.workers == nil {
			pool.workers = make(map[int]*replicaBackend)
		}
		pool.workers[slotID] = &replicaBackend{
			slotID:       slotID,
			status:       workerRunning,
			targetURL:    targetURL,
			deploymentID: deploymentID,
			rp:           rp,
		}
	}

	// Arm grace timers for any clients already bound to this slot that have
	// never opened a connection (ghost clients: they received the loading page
	// but did not reconnect before the worker became ready). Without this, such
	// a client keeps assignedClients == 1 forever because the timer is normally
	// armed only by clientConnClosed, which requires a prior clientConnOpened.
	// Arming here at worker-ready (not at reserve/bind time) ensures a slow cold
	// boot never triggers a premature reclaim: the client gets clientGraceTTL
	// (15 s) from worker-ready to actually connect. clientConnOpened cancels the
	// timer on a real connect so an active client is never interrupted.
	for clientID, cs := range p.clients[slug] {
		if cs.slotID == slotID && cs.liveConns == 0 && cs.releaseTimer == nil {
			p.armClientReleaseLocked(slug, clientID)
		}
	}

	p.clearWSReady(slug)
	return nil
}

// singleJoiningSlash joins two URL path segments with exactly one slash
// between them. When a is empty the result is b unchanged, preserving local
// replica behavior where target.Path is empty.
func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		if a == "" {
			return b
		}
		return a + "/" + b
	}
	return a + b
}

// DeregisterReplicaIfTarget removes the replica at index only while its current
// target still equals expectURL, returning whether it removed the slot. A
// worker-loss pass uses it so it cannot pull a route that a concurrent redeploy
// already re-pointed at a healthy backend: the deploy path registers the new
// route before it persists the new replica row, so a loss pass reading the stale
// row must confirm the live route still belongs to the lost replica before
// deregistering it. Unknown pools, out-of-range indices, and empty slots are
// no-ops.
func (p *Proxy) DeregisterReplicaIfTarget(slug string, index int, expectURL string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	pool, ok := p.pools[slug]
	if !ok || index < 0 || index >= len(pool.replicas) {
		return false
	}
	rb := pool.replicas[index]
	if rb == nil || rb.targetURL != expectURL {
		return false
	}
	pool.replicas[index] = nil
	p.clearWSReady(slug)
	return true
}

// DrainReplica marks the slot at index in slug's pool as draining and reports
// whether a live backend was marked. A draining slot is skipped by the least-
// connections picker (no new cookie-less sessions) while the sticky-cookie path
// still forwards, so established sessions finish before the slot is stopped.
// Returns false if the pool is absent, the index is out of range, or the slot
// holds no backend - the caller can then treat the scale-down as a no-op.
func (p *Proxy) DrainReplica(slug string, index int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool, ok := p.pools[slug]
	if !ok || index < 0 || index >= len(pool.replicas) {
		return false
	}
	rb := pool.replicas[index]
	if rb == nil {
		return false
	}
	rb.draining.Store(true)
	return true
}

// UndrainReplica clears the drain flag on the slot at index in slug's pool,
// returning it to the least-connections rotation, and reports whether a live
// backend was unmarked. It is the rollback for an aborted scale-down: when the
// stop fails, the still-running replica must resume serving new cookie-less
// sessions instead of being left permanently half-drained. Returns false for an
// absent pool, out-of-range index, or nil slot.
func (p *Proxy) UndrainReplica(slug string, index int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool, ok := p.pools[slug]
	if !ok || index < 0 || index >= len(pool.replicas) {
		return false
	}
	rb := pool.replicas[index]
	if rb == nil {
		return false
	}
	rb.draining.Store(false)
	return true
}

// IsDraining reports whether the slot at index in slug's pool is marked
// draining. Returns false for an absent pool, out-of-range index, or nil slot.
func (p *Proxy) IsDraining(slug string, index int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool, ok := p.pools[slug]
	if !ok || index < 0 || index >= len(pool.replicas) {
		return false
	}
	rb := pool.replicas[index]
	return rb != nil && rb.draining.Load()
}

// ReplicaTargetURL returns the target URL registered for slug at index, or an
// empty string if the slot is unset or the pool does not exist. Useful for
// observability and test assertions.
func (p *Proxy) ReplicaTargetURL(slug string, index int) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool, ok := p.pools[slug]
	if !ok || index < 0 || index >= len(pool.replicas) {
		return ""
	}
	if pool.replicas[index] == nil {
		return ""
	}
	return pool.replicas[index].targetURL
}

// ReplicaDeploymentID returns the deployment ID stamped into the replica at
// index for slug, or 0 if the slot is unset or the pool does not exist.
// Used by the pool syncer to diff the current state against the DB row so
// an unchanged slot is not re-registered (which would clear wsReady).
func (p *Proxy) ReplicaDeploymentID(slug string, index int) int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool, ok := p.pools[slug]
	if !ok || index < 0 || index >= len(pool.replicas) {
		return 0
	}
	if pool.replicas[index] == nil {
		return 0
	}
	return pool.replicas[index].deploymentID
}

// Deregister removes the entire pool for slug from the routing table.
// For elastic pools it dispatches the terminate callback (if set) for every
// worker in the pool and stops pending client release timers for the slug
// before dropping the map, so native or Docker processes are not orphaned on
// redeploy. Multiplex pools retain the previous behaviour (no callbacks).
func (p *Proxy) Deregister(slug string) {
	p.mu.Lock()
	pool := p.pools[slug]
	if pool != nil && poolIsElastic(pool) {
		// Stop all pending client grace timers for this slug so they do not
		// fire after the pool is gone and attempt to look up a removed worker.
		for _, cs := range p.clients[slug] {
			if cs.releaseTimer != nil {
				cs.releaseTimer.Stop()
				cs.releaseTimer = nil
			}
		}
		delete(p.clients, slug)
		// Dispatch terminate for each worker. The callback is captured before
		// the loop so it is read once under the lock; goroutines run outside it.
		if term := p.terminate; term != nil {
			for slotID := range pool.workers {
				sid := slotID
				go term(slug, sid)
			}
		}
	}
	delete(p.pools, slug)
	p.mu.Unlock()
	p.clearWSReady(slug)
}

// RegisteredSlugs returns the set of slugs that currently have a pool entry.
// Used by the pool syncer to deregister slugs that have no routable replicas
// in the DB on a given sync pass.
func (p *Proxy) RegisteredSlugs() map[string]struct{} {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]struct{}, len(p.pools))
	for slug := range p.pools {
		out[slug] = struct{}{}
	}
	return out
}

// BeginHibernate atomically removes slug from the routing table iff no
// activity has been recorded since `since` and no in-flight request is
// currently being proxied. On success it returns true and the caller is
// responsible for stopping the underlying processes. On failure (a request
// raced in or one is mid-flight) it returns false and the routing table is
// untouched, so the caller MUST NOT stop anything.
//
// The two-signal check (lastSeen and per-replica activeConns) is what makes
// this safe to call from the hibernation watchdog. lastSeen catches any
// request that has finished its routing decision and reached RecordActivity
// while activeConns catches a request that has already picked a replica but
// has not yet completed.
func (p *Proxy) BeginHibernate(slug string, since time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.seenMu.Lock()
	defer p.seenMu.Unlock()

	if last := p.lastSeen[slug]; last.After(since) {
		return false
	}
	if pool := p.pools[slug]; pool != nil {
		for _, rep := range pool.replicas {
			if rep != nil && rep.activeConns.Load() > 0 {
				return false
			}
		}
		// Elastic (grouped/per_session) pools keep their live backends in the
		// workers map, not the replicas slice, so the scan above never sees them.
		// A long-lived Shiny WebSocket holds activeConns > 0 on its worker while
		// lastSeen goes stale; without this scan the watchdog would hibernate the
		// pool and tear down the live session mid-use (ARCH-1). workers is nil for
		// multiplex pools, so this is a no-op there.
		for _, wkr := range pool.workers {
			if wkr != nil && wkr.activeConns.Load() > 0 {
				return false
			}
		}
		delete(p.pools, slug)
	}
	delete(p.lastSeen, slug)
	p.clearWSReadyLocked(slug)
	return true
}

// ReplicaSessionCounts returns a snapshot of the active connection count
// for each replica slot in slug's pool, indexed by replica index. Slots
// with a nil backend return -1. The returned slice length equals the
// current pool size; returns nil if the pool is not registered.
//
// Intended for the metrics endpoint; callers should treat the result as
// a best-effort sample, not a synchronised read (each entry is loaded
// independently).
func (p *Proxy) ReplicaSessionCounts(slug string) []int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool := p.pools[slug]
	if pool == nil {
		return nil
	}
	out := make([]int64, len(pool.replicas))
	for i, rep := range pool.replicas {
		if rep == nil {
			out[i] = -1
			continue
		}
		out[i] = rep.activeConns.Load()
	}
	return out
}

// PoolSessionStat is a per-pool session snapshot for the metrics collector:
// total active sessions across all live replicas, the per-replica admission cap
// (0 = unlimited), and the number of replicas available for NEW admission. The
// current admission ceiling is Cap*Replicas when Cap > 0.
type PoolSessionStat struct {
	Sessions int
	Cap      int
	// Replicas counts slots that admit new sessions: non-nil and not draining.
	// A draining replica (scale-down in progress) still holds its existing
	// sessions - which are counted in Sessions - but the picker routes no new
	// session to it, so it must not inflate the admission ceiling.
	Replicas int
}

// PoolSessionSnapshot returns a best-effort snapshot of session usage for every
// registered pool, keyed by slug. Taken under the read lock, but per-replica
// counts are loaded independently (not one global instant), matching
// ReplicaSessionCounts. Intended for the Prometheus session gauges.
func (p *Proxy) PoolSessionSnapshot() map[string]PoolSessionStat {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]PoolSessionStat, len(p.pools))
	for slug, pool := range p.pools {
		var sessions, admitting int
		for _, rep := range pool.replicas {
			if rep == nil {
				continue
			}
			sessions += int(rep.activeConns.Load())
			if !rep.draining.Load() {
				admitting++
			}
		}
		out[slug] = PoolSessionStat{Sessions: sessions, Cap: pool.maxSessions, Replicas: admitting}
	}
	return out
}

// appIDForSlug returns the numeric database app ID recorded for slug's pool and
// true, or 0 and false if the pool is not registered or has no ID set. Used by
// FleetSignal to resolve slug->appID without a DB round-trip at signal time.
func (p *Proxy) appIDForSlug(slug string) (int64, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if pool := p.pools[slug]; pool != nil {
		if id := pool.appID.Load(); id != 0 {
			return id, true
		}
	}
	return 0, false
}

// PoolCap returns the per-replica session cap for slug, or 0 if the pool is
// not registered or the cap is disabled.
func (p *Proxy) PoolCap(slug string) int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if pool := p.pools[slug]; pool != nil {
		return pool.maxSessions
	}
	return 0
}

// HasLiveReplica reports whether slug has at least one non-nil replica.
func (p *Proxy) HasLiveReplica(slug string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	pool := p.pools[slug]
	if pool == nil {
		return false
	}
	for _, r := range pool.replicas {
		if r != nil {
			return true
		}
	}
	return false
}

// Register is kept for single-replica callers. It is equivalent to
// SetPoolSize(slug, 1) + RegisterReplica(slug, 0, targetURL, nil, 0).
func (p *Proxy) Register(slug, targetURL string) error {
	p.SetPoolSize(slug, 1)
	return p.RegisterReplica(slug, 0, targetURL, nil, 0)
}

// ServeHTTP handles /app/:slug/* requests. When the slug has no live replica,
// the loading page is served and the wake trigger is invoked in a goroutine.
// Routing uses a sticky session cookie (shinyhub_rep_<slug>) pinned to a
// specific replica index. On a cache miss or stale cookie, least-connections
// with round-robin tie-breaking selects the replica and a new cookie is set.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	slug := extractSlug(r.URL.Path)
	if slug == "" {
		http.NotFound(w, r)
		return
	}

	// Readiness probe is special-cased before any backend lookup so it works
	// even when the pool is missing (e.g. between Deregister and the next
	// RegisterReplica). The handler never touches the upstream, never sets
	// a sticky cookie, and never records activity — it must not influence
	// hibernation or load-balancing decisions.
	if r.URL.Path == "/app/"+slug+readySuffix {
		p.serveReadyProbe(w, r, slug)
		return
	}

	// Wrap the writer so we can capture status + bytes for the access log.
	// The recorder delegates Flush/Hijack/ReadFrom so streaming responses,
	// WebSocket upgrades, and the sendfile fast path keep working.
	rec := newStatusRecorder(w)
	// Mark this slug ready the instant the reverse proxy completes a WS
	// upgrade. On this Go toolchain httputil.ReverseProxy hijacks the
	// connection and writes the 101 status line to the hijacked writer
	// WITHOUT calling WriteHeader(101), so onUpgrade fires from the recorder's
	// Hijack (see recorder.go), synchronously before the hijacked goroutine
	// (which lives for the duration of the WS) ever starts.
	rec.onUpgrade = func() { p.MarkWSReady(slug) }
	// Track the hijacked connection (if this request upgrades to WebSocket) so a
	// graceful shutdown can wait for it to close before exiting.
	rec.trackHijack = p.conns.track
	start := time.Now()
	replicaIndex := -1
	sticky := false

	// Trace context derivation: if tracing is enabled, parse the incoming
	// traceparent (or start a new trace), generate a fresh span ID for this
	// proxy hop, and overwrite the header so the upstream Shiny process sees
	// ShinyHub's span as its parent. The captured span info is recorded in the
	// ring buffer after the response completes (in the defer below).
	var (
		traceEnabled = p.traceCfg.Enabled
		traceCtx     tracing.TraceContext
		traceParent  string
		traceSampled bool
	)
	if traceEnabled {
		// tracestate is a W3C list header that MAY be split across multiple
		// header field-values; combine them so vendor entries past the first
		// split are not dropped (Header.Get would read only the first).
		incomingState := strings.Join(r.Header.Values("tracestate"), ",")
		traceCtx, traceParent, traceSampled = tracing.StartProxySpan(
			r.Header.Get("traceparent"), incomingState, p.traceCfg)
		r.Header.Set("traceparent", traceCtx.TraceparentHeader())
		// Propagate vendor tracestate only when continuing a trace; on a fresh
		// trace TraceState is empty and any stray inbound header is dropped so
		// it can't attach stale vendor context to the new trace.
		if traceCtx.TraceState != "" {
			r.Header.Set("tracestate", traceCtx.TraceState)
		} else {
			r.Header.Del("tracestate")
		}
	}

	defer func() {
		if traceEnabled && p.traceBuffer != nil {
			path := r.URL.Path
			prefix := "/app/" + slug
			if trimmed := strings.TrimPrefix(path, prefix); trimmed != path {
				if trimmed == "" {
					trimmed = "/"
				}
				path = trimmed
			}
			var spanErr string
			if rec.proxyErr != nil {
				spanErr = rec.proxyErr.Error()
			}
			p.traceBuffer.Record(tracing.Span{
				TraceID:    traceCtx.TraceIDHex(),
				SpanID:     hex.EncodeToString(traceCtx.SpanID[:]),
				ParentID:   traceParent,
				AppSlug:    slug,
				Replica:    replicaIndex,
				Method:     r.Method,
				Path:       path,
				Status:     rec.status,
				DurationMS: time.Since(start).Milliseconds(),
				StartedAt:  start,
				Sampled:    traceSampled,
				Error:      spanErr,
			})
		}
		logPtr := p.accessLog.Load()
		if logPtr == nil {
			return
		}
		var resolver func(*http.Request) string
		if rp := p.clientIP.Load(); rp != nil {
			resolver = *rp
		}
		(*logPtr)(AccessLogEntry{
			Slug:         slug,
			Method:       r.Method,
			Path:         r.URL.Path,
			Status:       rec.status,
			Bytes:        rec.bytes,
			Duration:     time.Since(start),
			ClientIP:     resolveClientIP(resolver, r),
			Peer:         r.RemoteAddr,
			ReplicaIndex: replicaIndex,
			Sticky:       sticky,
			Reject:       rec.rejectReason,
		})
	}()

	// Hold the route-table read lock from the pool fetch through the
	// activeConns bump. BeginHibernate takes the write lock and inspects
	// activeConns under it; if we released the read lock before the bump,
	// BeginHibernate could observe activeConns=0 for a replica we have
	// already chosen, hibernate the pool, and stop the very backends we
	// are about to forward to.
	p.mu.RLock()
	pool := p.pools[slug]
	trigger := p.wakeTrigger
	if pool == nil || !poolHasAny(pool) {
		p.mu.RUnlock()
		// When the slug is confidently unknown, return a real 404 instead of
		// looping the user on the loading page forever. An uncertain lookup
		// (DB blip) falls through to the loading page (the pre-predicate
		// default) rather than 404ing a possibly-valid app — see
		// slugConfidentlyUnknown for the fail-open rationale.
		if p.slugConfidentlyUnknown(slug) {
			p.writeUnknownApp(rec, r, slug)
			return
		}
		// Hold the request during the wake: trigger the wake and wait up to the
		// hold window for a replica to register, so a warm resume (and a fast cold
		// boot) is served INLINE instead of bouncing through the loading page.
		// holdForWake also drives the clustered on-miss sync each tick - in
		// clustered mode the owner may register a replica that this instance's
		// background ticker has not yet pulled in.
		if !p.holdForWake(r.Context(), slug, trigger) {
			// The hold expired (a slow cold boot) or the app is down: serve the
			// miss page - status-aware, so a crashed/stopped app gets a clear page
			// rather than the spinner. The wake is already in flight (holdForWake
			// fired it), so do not re-trigger.
			p.serveMissPage(rec, slug, nil)
			return
		}
		// A replica registered within the hold window. Re-acquire the read lock for
		// the routing path and re-check (it could have drained in the gap).
		p.mu.RLock()
		pool = p.pools[slug]
		if pool == nil || !poolHasAny(pool) {
			p.mu.RUnlock()
			p.serveMissPage(rec, slug, nil)
			return
		}
		// Pool is populated; fall through to the routing path below while still
		// holding p.mu.RLock (matching the normal routing path).
	}

	// Elastic path: demand-driven per-client routing (per_session or grouped mode).
	// The multiplex path below is byte-for-byte unchanged; only elastic pools
	// enter this branch.
	if poolIsElastic(pool) {
		// Capture the spawn callback while holding the read lock so the read is
		// race-free against SetSpawnFunc (which takes the write lock).
		spawnFn := p.spawn

		cid, isNew := p.clientID(r, slug)
		if isNew {
			p.setClientCookie(rec, r, slug, cid)
		}

		// parked bounds WS-upgrade parking to one wait per request: after a
		// successful wait the loop re-decides once, and if the worker flapped
		// away again the upgrade gets the loading page instead of re-parking.
		parked := false
	routeElastic:
		// Every iteration starts holding p.mu.RLock (from the routing path
		// above on the first pass; re-acquired before the goto on later ones).
		//
		// Resolve the routing pin from the server-side client binding, which is
		// the authoritative source. The binding is recorded by bindClient when a
		// new slot is allocated and remains valid after RegisterElasticWorker
		// stamps the real deploymentID. A cookie-based depID comparison would
		// fail here because the pin cookie is written with deploymentID=0 at
		// allocate time and the worker registers with a non-zero ID later.
		pinnedSlot := -1
		if cs := p.clients[slug][cid]; cs != nil {
			if w := pool.workers[cs.slotID]; w != nil && w.status != workerDraining {
				pinnedSlot = cs.slotID
			}
		}

		d := decide(pool.workerStates(), pool.mode, pool.groupedSize, pool.maxWorkers, pinnedSlot)

		switch d.kind {
		case decisionRoute:
			// Pre-check under the read lock (still held). A booting or draining
			// worker is not routable: serve the loading page so the client retries.
			// The pin cookie is already in the browser from the decisionAllocate
			// response, so the retry lands on the same slot. Rejecting draining here
			// (not only in the bind path's revalidation) lets the steady-state
			// branch below trust the read-lock snapshot: while the read lock is held
			// a worker cannot be removed or change status (both need the write lock).
			wkr := pool.workers[d.slotID]
			if wkr == nil || wkr.status == workerBooting || wkr.status == workerDraining {
				booting := wkr != nil && wkr.status == workerBooting
				p.mu.RUnlock()
				// A WebSocket upgrade cannot follow the loading page's reload
				// loop: a non-101 hard-fails scripted clients that connect
				// straight after their first response. Park the upgrade until
				// the pinned worker registers (bounded by wsBootParkTTL,
				// canceled when the client hangs up), then route it.
				if booting && !parked && isWSUpgrade(r) && p.waitWorkerReady(r.Context(), slug, d.slotID) {
					parked = true
					p.mu.RLock()
					pool = p.pools[slug]
					if pool != nil && poolIsElastic(pool) {
						goto routeElastic
					}
					p.mu.RUnlock()
				}
				p.serveMissPage(rec, slug, nil)
				return
			}
			var depID int64
			if cs := p.lookupClientSlot(slug, cid); cs != nil {
				// STEADY STATE: an already-bound client routing to a ready worker.
				// Do the accounting under the SHARED read lock (already held) plus
				// cs.mu, so unrelated slugs and clients route in parallel instead of
				// serialising on the global write lock. The grace-timer callback
				// takes the WRITE lock, so it cannot delete this slot while the read
				// lock is held; cs.open() cancels any pending timer and bumps
				// liveConns, closing the terminate race without the write lock the
				// pre-scaling code acquired here. assignedClients is unchanged (the
				// client is already counted), so no map mutation is needed.
				wkr.activeConns.Add(1)
				replicaIndex = wkr.slotID
				depID = wkr.deploymentID
				cs.open()
				p.mu.RUnlock()
			} else {
				// BIND: a client arrived via decisionRoute with no prior p.clients
				// entry (grouped mode: a new client packing onto an under-capacity
				// worker, or the first reconnect of a pinned client before bindClient
				// ran). Binding mutates the clients map and assignedClients, so it
				// needs the write lock. Upgrade and re-validate: pool or worker may
				// have changed in the RUnlock->Lock gap.
				p.mu.RUnlock()
				p.mu.Lock()
				pool2 := p.pools[slug]
				if pool2 == nil || !poolIsElastic(pool2) {
					p.mu.Unlock()
					p.serveMissPage(rec, slug, nil)
					return
				}
				wkr2 := pool2.workers[d.slotID]
				if wkr2 == nil || wkr2.status == workerBooting || wkr2.status == workerDraining {
					p.mu.Unlock()
					p.serveMissPage(rec, slug, nil)
					return
				}
				wkr = wkr2
				// Re-check the binding under the write lock: another request for the
				// same client may have bound it in the gap. Without the grouped-cap
				// increment, assignedClients under-counts and a worker can exceed cap.
				cs := p.lookupClientSlot(slug, cid)
				if cs == nil {
					if p.clients[slug] == nil {
						p.clients[slug] = make(map[string]*clientSlot)
					}
					cs = &clientSlot{slotID: d.slotID}
					p.clients[slug][cid] = cs
					wkr.assignedClients++
				}
				wkr.activeConns.Add(1)
				replicaIndex = wkr.slotID
				depID = wkr.deploymentID
				cs.open()
				p.mu.Unlock()
			}
			// Refresh the rep sticky cookie with the current slotID and deploymentID,
			// but only when it would change - a steady-state request whose cookie
			// already pins this slot/deployment skips the HMAC + Set-Cookie. The
			// cookie is informational only for elastic pools; the server-side
			// p.clients binding is the routing authority.
			if !p.stickyCookieAlreadyPins(r, slug, d.slotID, depID) {
				http.SetCookie(rec, &http.Cookie{
					Name:     cookiePrefix + slug,
					Value:    p.stickyCookieValue(slug, d.slotID, depID),
					Path:     "/app/" + slug + "/",
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
					Secure:   proxytrust.Scheme(r, p.trustedProxyNets()) == "https",
				})
			}
			defer wkr.activeConns.Add(-1)
			defer p.clientConnClosed(slug, cid)
			p.RecordActivity(slug)
			if traceEnabled {
				r = r.WithContext(context.WithValue(r.Context(), recorderCtxKey{}, rec))
			}
			wkr.rp.ServeHTTP(rec, r)
			return

		case decisionBind, decisionAllocate:
			p.mu.RUnlock()
			// Host-memory admission floor, probed outside any lock. It gates
			// only NEW worker allocation: binding onto an already-reserved
			// worker adds no process, so it proceeds under pressure, and
			// pinned clients take decisionRoute and keep being served. The
			// reject reason is distinct from pool-saturated so capacity
			// automation (autoscale, warm expansion) does not read memory
			// pressure as a scale-up signal.
			memOK := true
			if g := p.memGuard.Load(); g != nil {
				if availMB, ok := g.probe(); ok && availMB < g.minAvailableMB {
					memOK = false
				}
			}
			// Place under the write lock, re-deciding on CURRENT state: the
			// read-lock decide() above ran on a snapshot that a concurrent
			// cold burst makes stale (every burst client sees the same
			// under-capacity pool). placeClient packs clients onto booting
			// workers up to grouped_size before reserving new slots, so a
			// burst sheds only beyond max_workers x grouped_size.
			pl := p.placeClient(slug, cid, memOK)
			switch pl.kind {
			case placedBind:
				// Pin the client to its slot. deploymentID is 0 while the
				// worker is booting; retries during boot honor the pin and the
				// loading page reloads into a route once the worker registers.
				http.SetCookie(rec, &http.Cookie{
					Name:     cookiePrefix + slug,
					Value:    p.stickyCookieValue(slug, pl.slotID, pl.deploymentID),
					Path:     "/app/" + slug + "/",
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
					Secure:   proxytrust.Scheme(r, p.trustedProxyNets()) == "https",
				})
				if pl.spawned && spawnFn != nil {
					go spawnFn(slug, pl.slotID)
				}
				p.serveMissPage(rec, slug, nil)
			case placedMemoryPressure:
				p.recordReject(rec, slug, ReasonMemoryPressure, true)
				rec.Header().Set("Retry-After", "5")
				http.Error(rec, MsgPoolSaturated, http.StatusServiceUnavailable)
			case placedSaturated:
				p.recordReject(rec, slug, ReasonPoolSaturated, true)
				rec.Header().Set("Retry-After", "5")
				http.Error(rec, MsgPoolSaturated, http.StatusServiceUnavailable)
			default: // placedGone: pool vanished or turned non-elastic in the gap
				p.serveMissPage(rec, slug, nil)
			}
			return

		case decisionReject:
			p.mu.RUnlock()
			p.recordReject(rec, slug, ReasonPoolSaturated, true)
			rec.Header().Set("Retry-After", "5")
			http.Error(rec, MsgPoolSaturated, http.StatusServiceUnavailable)
			return
		}
	}

	picked, isStickyHit, saturated := p.pickReplicaLocked(pool, slug, r)
	if picked == nil {
		// poolHasAny passed (at least one slot is registered) but every live
		// backend is marked draining: a cookie-less request has no fresh
		// capacity to land on. Without this branch ServeHTTP would deref a
		// nil picked.index and panic the proxy goroutine, taking the whole
		// server with it during an otherwise routine scale-down window.
		// Treat as ReasonPoolDegraded (the pool exists but cannot accept
		// new sessions right now) and shed with Retry-After so a polite
		// client retries once the drain completes and a fresh slot lands.
		p.mu.RUnlock()
		p.recordReject(rec, slug, ReasonPoolDegraded, true)
		// Fire the wake trigger so a warm-shrunk pool is expanded immediately
		// rather than waiting for the next watcher tick. Duplicate triggers are
		// safe: warm expansion is idempotent and deploy-lock-guarded.
		if trigger != nil {
			go trigger(slug)
		}
		rec.Header().Set("Retry-After", "5")
		http.Error(rec, MsgPoolSaturated, http.StatusServiceUnavailable)
		return
	}
	if saturated {
		// All live replicas are at the per-replica cap and this is a new
		// session (no valid sticky cookie). Distinguish genuine capacity
		// saturation (all configured replicas live) from a degraded pool
		// (fewer replicas registered than configured) while still under the
		// read lock, then shed. Retry-After: 5 gives a finishing session a
		// realistic chance to free a slot.
		live := 0
		for _, rep := range pool.replicas {
			if rep != nil {
				live++
			}
		}
		reason := ReasonPoolSaturated
		if live < pool.size {
			reason = ReasonPoolDegraded
		}
		p.mu.RUnlock()
		p.recordReject(rec, slug, reason, true)
		// When degraded (fewer live replicas than configured), fire the wake
		// trigger to expand a warm-shrunk pool immediately. A full healthy pool
		// at cap (saturated) needs autoscaling, not warm expansion, so only the
		// degraded branch fires. Duplicate triggers are safe: expansion is
		// idempotent and deploy-lock-guarded.
		if reason == ReasonPoolDegraded && trigger != nil {
			go trigger(slug)
		}
		rec.Header().Set("Retry-After", "5")
		http.Error(rec, MsgPoolSaturated, http.StatusServiceUnavailable)
		return
	}
	replicaIndex = picked.index
	sticky = isStickyHit
	if !isStickyHit {
		http.SetCookie(rec, &http.Cookie{
			Name:     cookiePrefix + slug,
			Value:    p.stickyCookieValue(slug, picked.index, picked.deploymentID),
			Path:     "/app/" + slug + "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
			// Mirror the session cookie's scheme-aware policy: Secure over HTTPS
			// (so the routing cookie is never sent in cleartext), off over plain
			// HTTP (where the browser would otherwise drop it). X-Forwarded-Proto
			// is trusted only from configured proxies.
			Secure: proxytrust.Scheme(r, p.trustedProxyNets()) == "https",
		})
	}
	newConns := picked.activeConns.Add(1)
	p.mu.RUnlock()
	defer picked.activeConns.Add(-1)

	// When this slug's local active count rises from 0 to 1, signal the session
	// reporter for an immediate DB flush so other instances see the new session
	// without waiting for the next periodic tick. The send is non-blocking: a
	// full channel means a flush is already queued for this slug (or another)
	// and the current signal is safely dropped.
	// immediateFlush is read without holding p.mu: EnableImmediateFlush is
	// called once at startup before ListenAndServe begins, so the write
	// happens-before any ServeHTTP invocation and the field is never mutated
	// again. The unsynchronised read is therefore race-free after startup.
	if newConns == 1 {
		if ch := p.immediateFlush; ch != nil {
			select {
			case ch <- slug:
			default:
			}
		}
	}

	p.RecordActivity(slug)
	// Thread the recorder through the request context only while tracing is on
	// so the transport's body wrapper can attribute a mid-stream read error to
	// this span. When tracing is off the value is absent and the body is never
	// wrapped, keeping the hot path allocation-free.
	if traceEnabled {
		r = r.WithContext(context.WithValue(r.Context(), recorderCtxKey{}, rec))
	}
	picked.rp.ServeHTTP(rec, r)
}

// serveReadyProbe answers GET /app/<slug>/.shinyhub/ready with one of three
// states, so external monitoring can tell them apart:
//
//   - 200 {"ready":true}  - at least one replica has completed a WebSocket
//     handshake; the app is actually serving.
//   - 503 {"ready":false} - a known app that hasn't handshaken yet (cold
//     start / restart). Carries Retry-After: 1 so pollers back off politely.
//   - 404 {"error":"unknown app","slug":...} - no such app on this server.
//     Collapsing this into 503 would let a monitor that treats 503 as a
//     healthy cold-start band pass against an empty registry, masking a
//     deploy regression.
//
// For a known app the endpoint accepts GET and HEAD; other methods are rejected
// with 405 so a misconfigured client (e.g. an unintended POST) fails loudly
// rather than appearing to succeed. An unknown slug is 404 before the method
// gate: a method complaint about a resource that doesn't exist on this server
// is noise.
func (p *Proxy) serveReadyProbe(w http.ResponseWriter, r *http.Request, slug string) {
	var ready bool
	if fn := p.appReadyFunc.Load(); fn != nil {
		ready = (*fn)(slug)
	} else {
		ready = p.IsWSReady(slug)
	}
	// The unknown-vs-cold-start distinction only matters when we're about to
	// answer "not ready": a ready slug is known by definition (a replica has
	// handshaked), so the steady-state 200 path skips the existence lookup the
	// monitors would otherwise hammer on every poll. Checking existence before
	// the method gate keeps "unknown slug = 404" true for every method.
	if !ready && p.slugConfidentlyUnknown(slug) {
		p.writeUnknownApp(w, r, slug)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if !ready {
		// The probe holds no route-table lock, so read pool presence under a
		// brief RLock to set the cardinality guard: a registered app records
		// under its slug, an unregistered/fail-open slug collapses to the
		// sentinel.
		p.mu.RLock()
		registered := p.pools[slug] != nil
		p.mu.RUnlock()
		p.recordReject(w, slug, ReasonAppNotReady, registered)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"ready":false}`)) //nolint:errcheck
		}
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		w.Write([]byte(`{"ready":true}`)) //nolint:errcheck
	}
}

// slugConfidentlyUnknown reports whether the existence predicate is wired AND
// confidently reports that slug is not registered on this server. It returns
// false when no predicate is set or the lookup itself failed (DB unavailable,
// ctx cancelled), so every caller fails open: an uncertain answer must never be
// treated as "unknown", or a momentary DB blip would 404 a perfectly valid app
// while the database recovers. The predicate is invoked without holding p.mu
// because it may touch the database.
func (p *Proxy) slugConfidentlyUnknown(slug string) bool {
	p.mu.RLock()
	pred := p.slugExists
	p.mu.RUnlock()
	if pred == nil {
		return false
	}
	exists, err := pred(slug)
	return err == nil && !exists
}

// unknownAppBody is the 404 payload returned for any /app/<slug>/ request whose
// slug is not registered on this server. Field order is fixed by struct
// declaration order so the wire form (`{"error":...,"slug":...}`) is stable for
// clients that string-match it.
type unknownAppBody struct {
	Error string `json:"error"`
	Slug  string `json:"slug"`
}

// writeUnknownApp answers a /app/<slug>/ request for a slug this server does not
// know about with 404 and a machine-readable body identifying the slug. This is
// what lets external monitoring distinguish "no such app here" (a deploy
// regression) from "known app, not ready yet" (503). HEAD callers get the
// status line only.
func (p *Proxy) writeUnknownApp(w http.ResponseWriter, r *http.Request, slug string) {
	p.recordReject(w, slug, ReasonUnknownSlug, false)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	if r.Method == http.MethodHead {
		return
	}
	body, _ := json.Marshal(unknownAppBody{Error: "unknown app", Slug: slug})
	w.Write(body) //nolint:errcheck
}

// resolveClientIP returns the trusted-proxy-aware client IP when a resolver is
// registered, falling back to the host portion of r.RemoteAddr otherwise.
func resolveClientIP(resolver func(*http.Request) string, r *http.Request) string {
	if resolver != nil {
		if ip := resolver(r); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isInternalCookie reports whether a cookie belongs to ShinyHub itself (the
// session JWT, CSRF token, OAuth-state nonce, the per-app sticky-routing cookie,
// or the per-app elastic client-id cookie) and so must never be forwarded to an
// app backend, nor accepted from one via Set-Cookie.
func isInternalCookie(name string) bool {
	switch name {
	case auth.SessionCookieName, auth.CSRFCookieName, auth.OAuthStateCookieName:
		return true
	}
	return strings.HasPrefix(name, cookiePrefix) || strings.HasPrefix(name, clientCookiePrefix)
}

// filterReservedSetCookies drops any Set-Cookie header on a backend response
// whose cookie name is in one of ShinyHub's reserved namespaces (see
// isInternalCookie). Assigned as the reverse proxy's ModifyResponse so a
// deployer-controlled app backend cannot set the platform's session, sticky, or
// elastic client-id cookies in a visitor's browser - which would otherwise let
// one app pin another visitor to a worker it controls. Legitimate app cookies
// pass through unchanged. Returns nil error unconditionally (the signature
// satisfies httputil.ReverseProxy.ModifyResponse).
func filterReservedSetCookies(resp *http.Response) error {
	raws := resp.Header["Set-Cookie"]
	if len(raws) == 0 {
		return nil
	}
	kept := raws[:0:0]
	for _, raw := range raws {
		name := raw
		if i := strings.IndexByte(raw, '='); i >= 0 {
			name = raw[:i]
		}
		if isInternalCookie(strings.TrimSpace(name)) {
			continue
		}
		kept = append(kept, raw)
	}
	if len(kept) == 0 {
		resp.Header.Del("Set-Cookie")
		return nil
	}
	resp.Header["Set-Cookie"] = kept
	return nil
}

// stripInternalCookies rewrites the request's Cookie header to drop ShinyHub's
// own cookies before the request is forwarded to the (deployer-controlled) app
// backend, so a malicious app cannot harvest a visitor's session JWT. Unrelated
// app cookies are preserved; the header is removed entirely if nothing remains.
func stripInternalCookies(req *http.Request) {
	if req.Header.Get("Cookie") == "" {
		return
	}
	cookies := req.Cookies()
	kept := make([]*http.Cookie, 0, len(cookies))
	for _, c := range cookies {
		if isInternalCookie(c.Name) {
			continue
		}
		kept = append(kept, c)
	}
	if len(kept) == len(cookies) {
		return // nothing internal to strip
	}
	req.Header.Del("Cookie")
	if len(kept) == 0 {
		return
	}
	var b strings.Builder
	for i, c := range kept {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(c.Name)
		b.WriteByte('=')
		b.WriteString(c.Value)
	}
	req.Header.Set("Cookie", b.String())
}

// applyForwardingHeaders sets the X-Forwarded-* / Forwarded headers the app
// backend uses to reconstruct the external request. When the immediate peer is
// not a trusted proxy, any client-supplied values are dropped first so they
// cannot be spoofed; the headers are then populated from the proxy's own view.
// When the peer is a trusted edge proxy, its values are preserved (set only if
// absent), keeping authority with the terminating proxy.
func applyForwardingHeaders(req *http.Request, scheme, clientIP string, peerTrusted bool) {
	if !peerTrusted {
		req.Header.Del("X-Forwarded-For")
		req.Header.Del("X-Forwarded-Host")
		req.Header.Del("X-Forwarded-Proto")
		req.Header.Del("X-Real-IP")
		req.Header.Del("Forwarded")
	}
	if req.Header.Get("X-Forwarded-Host") == "" && req.Host != "" {
		req.Header.Set("X-Forwarded-Host", req.Host)
	}
	if req.Header.Get("X-Forwarded-Proto") == "" {
		req.Header.Set("X-Forwarded-Proto", scheme)
	}
	if req.Header.Get("X-Real-IP") == "" && clientIP != "" {
		req.Header.Set("X-Real-IP", clientIP)
	}
	if req.Header.Get("Forwarded") == "" {
		if fwd := buildForwarded(req, scheme); fwd != "" {
			req.Header.Set("Forwarded", fwd)
		}
	}
}

// buildForwarded constructs an RFC 7239 Forwarded header value from the
// inbound request. Returns "" when there is no useful information to convey
// (no peer and no host). The client source port is included in the for=
// element so backends that parse Forwarded (Django, some Go middleware) can
// log a non-zero port — something X-Forwarded-For cannot express.
func buildForwarded(req *http.Request, scheme string) string {
	parts := make([]string, 0, 3)
	if forTok := forwardedForToken(req.RemoteAddr); forTok != "" {
		parts = append(parts, "for="+forTok)
	}
	parts = append(parts, "proto="+scheme)
	if req.Host != "" {
		parts = append(parts, `host="`+req.Host+`"`)
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ";")
}

// forwardedForToken renders an RFC 7239 for= parameter value from a raw
// "host:port" RemoteAddr. IPv6 literals are bracketed per RFC 7239 §6.
// Returns "" when the address cannot be parsed.
func forwardedForToken(remoteAddr string) string {
	host, port, err := net.SplitHostPort(remoteAddr)
	if err != nil || host == "" {
		return ""
	}
	if strings.Contains(host, ":") {
		return `"[` + host + `]:` + port + `"`
	}
	return `"` + host + `:` + port + `"`
}

// pickReplicaLocked selects a backend for the incoming request. The caller
// must hold p.mu (read or write); this function does not lock so that
// ServeHTTP can hold the read lock continuously from the pool fetch
// through the activeConns bump (see ServeHTTP for why that matters).
//
// First, it checks for a valid sticky cookie. If the pinned replica index
// is live, that replica is returned and the cookie is left untouched
// (isStickyHit=true). Sticky hits always forward, even when the replica
// is at or above the session cap — denying an established session would
// kill a live WS and surface as a user-visible disconnect.
//
// Otherwise, least-connections is used: the replica with the fewest active
// connections wins. On a tie, a pool-scoped round-robin counter determines
// which tied candidate is preferred — this avoids always pinning
// cookie-less traffic to the lowest-index replica. If the least-loaded
// replica is at or above the cap (and the cap is non-zero), the saturated
// flag is set so ServeHTTP can shed the request with 503.
func (p *Proxy) pickReplicaLocked(pool *backendPool, slug string, r *http.Request) (best *replicaBackend, isSticky, saturated bool) {
	if c, err := r.Cookie(cookiePrefix + slug); err == nil {
		if idx, depID, ok := p.stickyIndex(slug, c.Value); ok {
			if idx >= 0 && idx < len(pool.replicas) && pool.replicas[idx] != nil {
				rep := pool.replicas[idx]
				// Honor the sticky pin only when the cookie's deployment ID matches
				// the in-memory replica's deployment ID. A mismatch means the app
				// was redeployed since the cookie was issued; fall through to
				// least-connections so the client gets re-pinned to the current
				// deployment without disruption (no 4xx, no panic).
				if rep.deploymentID == depID {
					return rep, true, false
				}
			}
		}
	}

	var bestConns int64 = -1
	for _, rep := range pool.replicas {
		if rep == nil || rep.draining.Load() {
			continue
		}
		c := rep.activeConns.Load()
		if best == nil || c < bestConns {
			best = rep
			bestConns = c
			continue
		}
		if c == bestConns {
			// Alternate on tie to distribute across equal-load replicas.
			if pool.rrCounter.Add(1)&1 == 1 {
				best = rep
			}
		}
	}
	if best != nil && pool.maxSessions > 0 && bestConns >= int64(pool.maxSessions) {
		saturated = true
	}
	return best, false, saturated
}

// poolHasAny reports whether the pool can route (or schedule) an incoming
// request. For multiplex pools (mode == "" or "multiplex") this means a
// non-nil replica slot. For elastic pools (grouped or per_session) this is
// always true: even an empty workers map is acceptable because the elastic
// branch handles decisionAllocate (first request spawns a new worker) and
// decisionReject (at capacity). Making elastic pools always "routable" lets
// ServeHTTP fall through to the elastic branch on every request, including the
// very first one, without bouncing through the loading page.
func poolHasAny(pool *backendPool) bool {
	if poolIsElastic(pool) {
		return true // elastic pools route via decide(), never need pre-populated workers
	}
	for _, r := range pool.replicas {
		if r != nil {
			return true
		}
	}
	return false
}

// extractSlug parses the slug from /app/:slug/... paths.
// Returns "" for /app or /app/ (no slug present).
func extractSlug(path string) string {
	trimmed := strings.TrimPrefix(path, "/app/")
	if trimmed == path || trimmed == "" {
		return ""
	}
	return strings.SplitN(trimmed, "/", 2)[0]
}
