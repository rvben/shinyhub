package proxy

import (
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
)

// Proxy routes /app/:slug/* to the registered backend URL for that slug.
type Proxy struct {
	mu       sync.RWMutex
	backends map[string]*httputil.ReverseProxy
	targets  map[string]string
}

func New() *Proxy {
	return &Proxy{
		backends: make(map[string]*httputil.ReverseProxy),
		targets:  make(map[string]string),
	}
}

// Register sets the backend URL for slug, atomically replacing any existing entry.
func (p *Proxy) Register(slug, targetURL string) {
	target, _ := url.Parse(targetURL)
	rp := httputil.NewSingleHostReverseProxy(target)
	// Capture slug and target in a local variable to avoid closure issues.
	slugCopy := slug
	rp.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		// Strip the /app/:slug prefix and preserve the rest.
		prefix := "/app/" + slugCopy
		req.URL.Path = strings.TrimPrefix(req.URL.Path, prefix)
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		req.Host = target.Host
	}
	p.mu.Lock()
	p.backends[slug] = rp
	p.targets[slug] = targetURL
	p.mu.Unlock()
}

// Deregister removes slug from the routing table.
func (p *Proxy) Deregister(slug string) {
	p.mu.Lock()
	delete(p.backends, slug)
	delete(p.targets, slug)
	p.mu.Unlock()
}

// ServeHTTP handles /app/:slug/* requests.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	slug := extractSlug(r.URL.Path)
	if slug == "" {
		http.NotFound(w, r)
		return
	}
	p.mu.RLock()
	rp, ok := p.backends[slug]
	p.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	rp.ServeHTTP(w, r)
}

// extractSlug parses the slug from /app/:slug/...
func extractSlug(path string) string {
	path = strings.TrimPrefix(path, "/app/")
	if path == "" {
		return ""
	}
	parts := strings.SplitN(path, "/", 2)
	return parts[0]
}
