package localrun

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	shinyproxy "github.com/rvben/shinyhub/internal/proxy"
)

type localProxy struct {
	slug       string
	listener   net.Listener
	server     *http.Server
	proxy      *shinyproxy.Proxy
	publicPort int
	generation int64
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
	lp := &localProxy{slug: slug, listener: ln, proxy: p, publicPort: actualPort}
	lp.server = &http.Server{
		ReadHeaderTimeout: 10 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/" {
				http.Redirect(w, r, "/app/"+slug+"/", http.StatusTemporaryRedirect)
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
