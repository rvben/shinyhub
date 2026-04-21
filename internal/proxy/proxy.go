package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

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
  body { font-family: sans-serif; display: flex; align-items: center;
         justify-content: center; height: 100vh; margin: 0; background: #f8f9fa; }
  .box { text-align: center; max-width: 420px; padding: 0 1rem; }
  .spinner { width: 40px; height: 40px; border: 4px solid #dee2e6;
             border-top-color: #0d6efd; border-radius: 50%;
             animation: spin 0.8s linear infinite; margin: 0 auto 1rem; }
  @keyframes spin { to { transform: rotate(360deg); } }
  h1 { font-size: 1.25rem; color: #495057; margin: 0; }
  p  { color: #868e96; font-size: 0.875rem; margin-top: 0.5rem; line-height: 1.4; }
  button { margin-top: 1rem; padding: 0.5rem 1rem; font-size: 0.875rem;
           background: #0d6efd; color: #fff; border: 0; border-radius: 4px;
           cursor: pointer; }
  button:hover { background: #0b5ed7; }
  .error .spinner { display: none; }
  .error h1 { color: #dc3545; }
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

// replicaBackend wraps a single reverse proxy with connection tracking.
type replicaBackend struct {
	index       int
	rp          *httputil.ReverseProxy
	activeConns atomic.Int64
}

// backendPool holds a fixed-size slice of replicas for one slug.
// rrCounter is used for round-robin tie-breaking in least-connections selection.
type backendPool struct {
	size      int
	replicas  []*replicaBackend
	rrCounter atomic.Int64
}

// Proxy routes /app/:slug/* to the registered backend pool for that slug.
type Proxy struct {
	mu     sync.RWMutex
	pools  map[string]*backendPool
	onMiss func(slug string)

	seenMu   sync.RWMutex
	lastSeen map[string]time.Time
}

func New() *Proxy {
	return &Proxy{
		pools:    make(map[string]*backendPool),
		lastSeen: make(map[string]time.Time),
	}
}

// SetOnMiss registers a callback invoked (in a goroutine) when a request
// arrives for a slug with no registered backend. Called by lifecycle.Watcher.
func (p *Proxy) SetOnMiss(fn func(string)) {
	p.mu.Lock()
	p.onMiss = fn
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

// SetPoolSize initialises or resizes the replica pool for slug.
// It is idempotent: growing preserves existing replicas; shrinking drops
// trailing slots. Callers must invoke this before RegisterReplica.
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

// RegisterReplica registers a backend URL at the given index within slug's pool.
// Returns an error if the pool size has not been set or the index is out of range.
func (p *Proxy) RegisterReplica(slug string, index int, targetURL string) error {
	target, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("register %s#%d: invalid url: %w", slug, index, err)
	}
	if target.Scheme == "" || target.Host == "" {
		return fmt.Errorf("register %s#%d: url needs scheme and host", slug, index)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	pool, ok := p.pools[slug]
	if !ok || index < 0 || index >= pool.size {
		return fmt.Errorf("register %s#%d: pool size not set or index out of range", slug, index)
	}

	rp := httputil.NewSingleHostReverseProxy(target)
	slugCopy := slug
	rp.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		prefix := "/app/" + slugCopy
		req.URL.Path = strings.TrimPrefix(req.URL.Path, prefix)
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		if req.URL.RawPath != "" {
			req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, prefix)
			if req.URL.RawPath == "" {
				req.URL.RawPath = "/"
			}
		}
		req.Host = target.Host
	}
	pool.replicas[index] = &replicaBackend{index: index, rp: rp}
	return nil
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
}

// Deregister removes the entire pool for slug from the routing table.
func (p *Proxy) Deregister(slug string) {
	p.mu.Lock()
	delete(p.pools, slug)
	p.mu.Unlock()
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
// SetPoolSize(slug, 1) + RegisterReplica(slug, 0, targetURL).
func (p *Proxy) Register(slug, targetURL string) error {
	p.SetPoolSize(slug, 1)
	return p.RegisterReplica(slug, 0, targetURL)
}

// ServeHTTP handles /app/:slug/* requests. When the slug has no live replica,
// the loading page is served and onMiss is invoked in a goroutine.
// For multi-replica pools, the first live replica is used; Task 7 will add
// sticky cookie + least-connections routing.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	slug := extractSlug(r.URL.Path)
	if slug == "" {
		http.NotFound(w, r)
		return
	}

	p.mu.RLock()
	pool := p.pools[slug]
	onMiss := p.onMiss
	p.mu.RUnlock()

	if pool == nil || !poolHasAny(pool) {
		if onMiss != nil {
			go onMiss(slug)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(loadingPage)) //nolint:errcheck
		return
	}

	// Pick the first live replica. Task 7 replaces this with sticky cookie +
	// least-connections selection.
	var picked *replicaBackend
	p.mu.RLock()
	for _, rep := range pool.replicas {
		if rep != nil {
			picked = rep
			break
		}
	}
	p.mu.RUnlock()

	p.RecordActivity(slug)
	picked.rp.ServeHTTP(w, r)
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
