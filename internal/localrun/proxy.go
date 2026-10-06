package localrun

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/rvben/shinyhub/internal/appnav"
	shinyproxy "github.com/rvben/shinyhub/internal/proxy"
)

type localProxy struct {
	slug           string
	listener       net.Listener
	server         *http.Server
	proxy          *shinyproxy.Proxy
	publicPort     int
	generation     int64
	revision       atomic.Int64
	session        string
	browserRefresh bool
	unavailable    atomic.Bool
}

func newLocalProxy(port int, slug string) (*localProxy, error) {
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("port must be between 0 and 65535, got %d", port)
	}
	addr := "127.0.0.1:0"
	if port != 0 {
		addr = fmt.Sprintf("127.0.0.1:%d", port)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("reserve local port %d: %w", port, err)
	}
	actualPort := ln.Addr().(*net.TCPAddr).Port
	p := shinyproxy.New()
	p.SetPoolSize(slug, 1)
	// Local runs should exercise the same app-owned chrome integration as a
	// deployed app. The one-app payload keeps the switch action truthful while
	// still exposing opt-in capabilities such as bookmarking.
	p.SetAppNav(true, "/")
	lp := &localProxy{slug: slug, listener: ln, proxy: p, publicPort: actualPort, session: fmt.Sprintf("%x", randomSession())}
	lp.server = &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if lp.browserRefresh {
				w.Header().Set("Cache-Control", "no-store")
				if r.URL.Path == lp.reloadURL() {
					if r.Method != http.MethodGet {
						w.Header().Set("Allow", "GET")
						w.WriteHeader(http.StatusMethodNotAllowed)
						return
					}
					if lp.revision.Load() == 0 {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					_, _ = fmt.Fprint(w, lp.currentRevision())
					return
				}
			}
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/app/"+slug+"/", http.StatusTemporaryRedirect)
				return
			}
			if lp.unavailable.Load() {
				http.Error(w, "App is not running. Resume it in ShinyHub dev.", http.StatusServiceUnavailable)
				return
			}
			if r.URL.Path == appnav.DataURL(slug) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "private, no-store")
				_ = json.NewEncoder(w).Encode(appnav.Payload{Apps: []appnav.App{{
					Slug: slug, Name: slug, Openable: true,
				}}})
				return
			}
			p.ServeHTTP(w, r)
		}),
	}
	return lp, nil
}

func (p *localProxy) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/app/%s/", p.publicPort, p.slug)
}

func (p *localProxy) routeTo(port int) error {
	p.generation++
	return p.proxy.RegisterReplica(
		p.slug,
		0,
		fmt.Sprintf("http://127.0.0.1:%d", port),
		nil,
		p.generation,
	)
}

func (p *localProxy) serve(errCh chan<- error) {
	go func() {
		if err := p.server.Serve(p.listener); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("local proxy: %w", err)
		}
	}()
}

func (p *localProxy) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.server.Shutdown(ctx)
}

// A session token also refreshes tabs when dev is restarted on the same port.
func randomSession() []byte {
	token := make([]byte, 16)
	_, _ = rand.Read(token) // crypto/rand.Read cannot fail on supported Go versions.
	return token
}

func (p *localProxy) reloadURL() string { return "/app/" + p.slug + "/__shinyhub_dev_revision" }
func (p *localProxy) currentRevision() string {
	return fmt.Sprintf("%s:%d", p.session, p.revision.Load())
}

// Called before serving: the one-shot/check path never injects a watcher.
func (p *localProxy) enableBrowserRefresh() {
	p.browserRefresh = true
	p.proxy.SetDevReload(p.reloadURL(), p.currentRevision)
}

// Routing a candidate is provisional until its public readiness check passes.
// Rollbacks therefore never change the revision observed by open browsers.
func (p *localProxy) activateBrowserRevision() { p.revision.Add(1) }
