package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
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

// Proxy routes /app/:slug/* to the registered backend URL for that slug.
type Proxy struct {
	mu       sync.RWMutex
	backends map[string]*httputil.ReverseProxy
	onMiss   func(slug string)

	seenMu   sync.RWMutex
	lastSeen map[string]time.Time
}

func New() *Proxy {
	return &Proxy{
		backends: make(map[string]*httputil.ReverseProxy),
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

// Register sets the backend URL for slug, atomically replacing any existing entry.
func (p *Proxy) Register(slug, targetURL string) error {
	target, err := url.Parse(targetURL)
	if err != nil {
		return fmt.Errorf("register %s: invalid url: %w", slug, err)
	}
	if target.Scheme == "" || target.Host == "" {
		return fmt.Errorf("register %s: url must have scheme and host", slug)
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
	p.mu.Lock()
	p.backends[slug] = rp
	p.mu.Unlock()
	return nil
}

// Deregister removes slug from the routing table.
func (p *Proxy) Deregister(slug string) {
	p.mu.Lock()
	delete(p.backends, slug)
	p.mu.Unlock()
}

// ServeHTTP handles /app/:slug/* requests. When the slug has no registered
// backend, the loading page is served and onMiss is invoked in a goroutine.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	slug := extractSlug(r.URL.Path)
	if slug == "" {
		http.NotFound(w, r)
		return
	}
	p.mu.RLock()
	rp, ok := p.backends[slug]
	onMiss := p.onMiss
	p.mu.RUnlock()

	if !ok {
		if onMiss != nil {
			go onMiss(slug)
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(loadingPage)) //nolint:errcheck
		return
	}
	p.RecordActivity(slug)
	rp.ServeHTTP(w, r)
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
