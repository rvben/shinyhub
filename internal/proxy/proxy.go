package proxy

import (
	"fmt"
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
}

func New() *Proxy {
	return &Proxy{
		backends: make(map[string]*httputil.ReverseProxy),
	}
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
	// Capture slug and target in local variables to avoid closure issues.
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

// extractSlug parses the slug from /app/:slug/... Requires a trailing slash
// after the slug, so /app/foo returns "" but /app/foo/ returns "foo".
func extractSlug(path string) string {
	trimmed := strings.TrimPrefix(path, "/app/")
	if trimmed == path || trimmed == "" {
		return ""
	}
	return strings.SplitN(trimmed, "/", 2)[0]
}
