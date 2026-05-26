package proxy

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rvben/shinyhub/internal/config"
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

// loadingPage is returned when a request arrives for a slug with no registered
// backend. Client-side JS reloads the page every 3 s up to loadingPageMaxRetries
// times (~60 s) and then switches to an error state with a manual retry button.
// The per-path retry count is stored in sessionStorage; a fresh navigation
// (not a reload) resets it so users can revisit after a previous give-up.
// A <noscript><meta http-equiv="refresh"> is included as a fallback for
// browsers with JS disabled — it refreshes forever but at least keeps working.
const loadingPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<noscript><meta http-equiv="refresh" content="3"></noscript>
<title>Starting app…</title>
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
  <h1 id="shinyhub-title">Starting app…</h1>
  <p id="shinyhub-msg">This page will refresh automatically.</p>
  <button id="shinyhub-retry" style="display:none">Try again</button>
</div>
<script>
(function(){
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
})();
</script>
</body>
</html>`

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

// replicaBackend wraps a single reverse proxy with connection tracking.
type replicaBackend struct {
	index       int
	targetURL   string // the URL passed to RegisterReplica; used for introspection
	rp          *httputil.ReverseProxy
	activeConns atomic.Int64
}

// backendPool holds a fixed-size slice of replicas for one slug.
// rrCounter is used for round-robin tie-breaking in least-connections selection.
//
// maxSessions is the per-replica active-connection cap. When every non-nil
// replica is at or above this value, new requests without a valid sticky
// cookie are shed with 503 (see ServeHTTP). A value of 0 disables the cap.
type backendPool struct {
	size        int
	replicas    []*replicaBackend
	rrCounter   atomic.Int64
	maxSessions int
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
	mu         sync.RWMutex
	pools      map[string]*backendPool
	onMiss     func(slug string)
	slugExists func(slug string) (bool, error)

	accessLog atomic.Pointer[accessLogFn]
	clientIP  atomic.Pointer[clientIPFn]

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

	// rejects is the in-memory rolling rejection rollup surfaced by apps show.
	rejects *rejectCounter
	// rejectRecorder is an optional sink (the Prometheus registry) for
	// admission-reject events. Nil disables metrics emission (e.g. in tests).
	rejectRecorder atomic.Pointer[RejectRecorder]
}

func New() *Proxy {
	return &Proxy{
		pools:    make(map[string]*backendPool),
		lastSeen: make(map[string]time.Time),
		wsReady:  make(map[string]struct{}),
		rejects:  newRejectCounter(),
	}
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

// SetOnMiss registers a callback invoked (in a goroutine) when a request
// arrives for a slug with no registered backend. Called by lifecycle.Watcher.
func (p *Proxy) SetOnMiss(fn func(string)) {
	p.mu.Lock()
	p.onMiss = fn
	p.mu.Unlock()
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

// RecordActivity marks slug as seen at the current time.
func (p *Proxy) RecordActivity(slug string) {
	p.seenMu.Lock()
	p.lastSeen[slug] = time.Now()
	p.seenMu.Unlock()
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

// clearWSReadyLocked drops any cached readiness for slug. Caller must hold
// p.seenMu for writing.
func (p *Proxy) clearWSReadyLocked(slug string) {
	delete(p.wsReady, slug)
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

// RegisterReplica registers a backend URL at the given index within slug's pool.
// base is the HTTP transport used for outbound requests; nil uses the default
// transport. Remote tunnel URLs may carry a path prefix (e.g. /v1/data/<token>)
// that is prepended to every forwarded app-relative path.
// Returns an error if the pool size has not been set or the index is out of range.
func (p *Proxy) RegisterReplica(slug string, index int, targetURL string, base http.RoundTripper) error {
	target, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("register %s#%d: invalid url: %w", slug, index, err)
	}
	if target.Scheme == "" || target.Host == "" {
		return fmt.Errorf("register %s#%d: url needs scheme and host", slug, index)
	}
	if base == nil {
		base = http.DefaultTransport
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
	// onto the statusRecorder so the trace span surfaces span.Error. Mirrors
	// httputil's default handler (log + 502) and adds the capture.
	rp.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		log.Printf("proxy: upstream error for %s: %v", slugCopy, err)
		if sr, ok := w.(*statusRecorder); ok {
			sr.proxyErr = err
		}
		w.WriteHeader(http.StatusBadGateway)
	}
	// ErrorHandler does not fire for failures after the response header was
	// sent: ReverseProxy reports those only on the body copy. Wrap the
	// transport so a mid-stream upstream read error is captured too, which is
	// the only signal that admits an otherwise-200, not-slow span to the buffer.
	rp.Transport = &errCapturingTransport{base: base}
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
		if req.Header.Get("X-Forwarded-Host") == "" && req.Host != "" {
			req.Header.Set("X-Forwarded-Host", req.Host)
		}
		if req.Header.Get("X-Forwarded-Proto") == "" {
			req.Header.Set("X-Forwarded-Proto", scheme)
		}
		if req.Header.Get("X-Real-IP") == "" {
			if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil && clientIP != "" {
				req.Header.Set("X-Real-IP", clientIP)
			}
		}
		if req.Header.Get("Forwarded") == "" {
			if fwd := buildForwarded(req, scheme); fwd != "" {
				req.Header.Set("Forwarded", fwd)
			}
		}

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
	pool.replicas[index] = &replicaBackend{index: index, targetURL: targetURL, rp: rp}
	// A freshly registered backend has not yet proven it accepts WS upgrades.
	// Clearing here also covers the hot-redeploy path where a caller swaps
	// replicas without an intermediate Deregister.
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

// DeregisterReplica removes the replica at index from slug's pool.
// The slot becomes nil; other replicas are unaffected.
func (p *Proxy) DeregisterReplica(slug string, index int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pool, ok := p.pools[slug]
	if !ok || index < 0 || index >= len(pool.replicas) {
		return
	}
	pool.replicas[index] = nil
	p.clearWSReady(slug)
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

// Deregister removes the entire pool for slug from the routing table.
func (p *Proxy) Deregister(slug string) {
	p.mu.Lock()
	delete(p.pools, slug)
	p.mu.Unlock()
	p.clearWSReady(slug)
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
// SetPoolSize(slug, 1) + RegisterReplica(slug, 0, targetURL, nil).
func (p *Proxy) Register(slug, targetURL string) error {
	p.SetPoolSize(slug, 1)
	return p.RegisterReplica(slug, 0, targetURL, nil)
}

// ServeHTTP handles /app/:slug/* requests. When the slug has no live replica,
// the loading page is served and onMiss is invoked in a goroutine.
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
	// Mark this slug ready the instant the reverse proxy writes 101.
	// httputil.ReverseProxy emits WriteHeader(101) before calling Hijack,
	// so the hook fires synchronously before the hijacked goroutine
	// (which lives for the duration of the WS) ever starts.
	rec.onUpgrade = func() { p.MarkWSReady(slug) }
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
	onMiss := p.onMiss
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
		if onMiss != nil {
			go onMiss(slug)
		}
		rec.Header().Set("Content-Type", "text/html; charset=utf-8")
		rec.WriteHeader(http.StatusOK)
		rec.Write([]byte(loadingPage)) //nolint:errcheck
		return
	}

	picked, isStickyHit, saturated := p.pickReplicaLocked(pool, slug, r)
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
		rec.Header().Set("Retry-After", "5")
		http.Error(rec, MsgPoolSaturated, http.StatusServiceUnavailable)
		return
	}
	replicaIndex = picked.index
	sticky = isStickyHit
	if !isStickyHit {
		http.SetCookie(rec, &http.Cookie{
			Name:     cookiePrefix + slug,
			Value:    strconv.Itoa(picked.index),
			Path:     "/app/" + slug + "/",
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}
	picked.activeConns.Add(1)
	p.mu.RUnlock()
	defer picked.activeConns.Add(-1)

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
	ready := p.IsWSReady(slug)
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
		if idx, err := strconv.Atoi(c.Value); err == nil {
			if idx >= 0 && idx < len(pool.replicas) && pool.replicas[idx] != nil {
				return pool.replicas[idx], true, false
			}
		}
	}

	var bestConns int64 = -1
	for _, rep := range pool.replicas {
		if rep == nil {
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

// poolHasAny reports whether the pool contains at least one non-nil replica.
func poolHasAny(pool *backendPool) bool {
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
