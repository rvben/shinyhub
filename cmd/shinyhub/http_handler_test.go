package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/rvben/shinyhub/internal/api"
	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/dbtest"
	"github.com/rvben/shinyhub/internal/proxy"
)

// testHTTPHandler builds the server's real top-level handler over a fresh
// database, the way runServe does, so routing and wrapping are exercised
// through the production function rather than a hand-built copy of it.
type testHTTPHandler struct {
	handler http.Handler
	readyCh chan struct{}
	owner   *atomic.Bool
	prx     *proxy.Proxy
}

func newTestHTTPHandler(t *testing.T, configure func(*config.Config)) *testHTTPHandler {
	t.Helper()
	store := dbtest.New(t)
	prx := proxy.New()
	cfg := &config.Config{
		Server:  config.ServerConfig{BaseURL: "http://127.0.0.1"},
		Auth:    config.AuthConfig{Secret: "http-handler-test-secret-http-handler-test"},
		Storage: config.StorageConfig{AppsDir: t.TempDir()},
	}
	if configure != nil {
		configure(cfg)
	}
	srv := api.New(cfg, store, nil, prx)
	h := &testHTTPHandler{readyCh: make(chan struct{}), owner: &atomic.Bool{}, prx: prx}
	handler, err := buildHTTPHandler(httpHandlerDeps{
		cfg:            cfg,
		store:          store,
		srv:            srv,
		prx:            prx,
		readyCh:        h.readyCh,
		ownerAndReady:  h.owner.Load,
		bundleTokenKey: deriveBundleTokenKey(cfg.Auth.Secret),
		appUserLookup:  store.LookupContextUser,
	})
	if err != nil {
		t.Fatalf("buildHTTPHandler: %v", err)
	}
	h.handler = handler
	return h
}

// testResponse is what a client would receive: the status, the headers as
// they were sent, and the body bytes. A recorder's live Header map is not that;
// a wrapper may set headers while writing them and then restore the handler's
// own map.
type testResponse struct {
	Code   int
	Header http.Header
	Body   []byte
}

func (h *testHTTPHandler) get(t *testing.T, path string, header map[string]string) testResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return testResponse{Code: rec.Code, Header: rec.Result().Header, Body: rec.Body.Bytes()}
}

// Every response, including the probes registered directly on the mux, passes
// through the security-header layer: it is the outermost wrapper, not one
// applied per route.
func TestHTTPHandlerSetsSecurityHeadersOnEveryRoute(t *testing.T) {
	h := newTestHTTPHandler(t, nil)
	for _, path := range []string{"/healthz", "/api/server-info", "/static/app.js"} {
		rec := h.get(t, path, nil)
		if rec.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("GET %s: no Content-Security-Policy header (status %d)", path, rec.Code)
		}
		if rec.Header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s: X-Content-Type-Options = %q, want nosniff", path, rec.Header.Get("X-Content-Type-Options"))
		}
	}
}

// /readyz answers starting until the listener is serving and the pool has
// synced once; /activez follows the ownership predicate it was given.
func TestHTTPHandlerProbesFollowReadinessAndOwnership(t *testing.T) {
	h := newTestHTTPHandler(t, nil)

	if rec := h.get(t, "/healthz", nil); rec.Code != http.StatusOK || string(rec.Body) != "ok" {
		t.Fatalf("/healthz = %d %q, want 200 ok", rec.Code, string(rec.Body))
	}
	if rec := h.get(t, "/readyz", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz before the listener is ready = %d, want 503", rec.Code)
	}
	close(h.readyCh)
	h.prx.MarkSynced()
	if rec := h.get(t, "/readyz", nil); rec.Code != http.StatusOK {
		t.Fatalf("/readyz once ready and synced = %d %s, want 200", rec.Code, string(rec.Body))
	}

	if rec := h.get(t, "/activez", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/activez while not the owner = %d, want 503", rec.Code)
	}
	h.owner.Store(true)
	if rec := h.get(t, "/activez", nil); rec.Code != http.StatusOK {
		t.Fatalf("/activez as the ready owner = %d, want 200", rec.Code)
	}
}

// The app switcher's data route exists only while the switcher is enabled, so
// app_nav: false leaves nothing under /app/ answering differently than before
// the switcher existed.
func TestHTTPHandlerRegistersAppNavOnlyWhenEnabled(t *testing.T) {
	const navPath = "/app/some-app/.shinyhub/nav.json"

	enabled := newTestHTTPHandler(t, nil)
	rec := enabled.get(t, navPath, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("nav.json with the switcher on = %d %s, want 200", rec.Code, string(rec.Body))
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body, &payload); err != nil {
		t.Fatalf("nav.json body is not JSON: %v: %s", err, string(rec.Body))
	}
	if _, ok := payload["apps"]; !ok {
		t.Fatalf("nav.json payload has no apps key: %s", string(rec.Body))
	}

	// Off, the path is just another app path: whatever /app/ answers, it is
	// not the switcher's payload.
	off := false
	disabled := newTestHTTPHandler(t, func(c *config.Config) { c.Server.AppNav = &off })
	rec = disabled.get(t, navPath, nil)
	if err := json.Unmarshal(rec.Body, &payload); err == nil {
		if _, ok := payload["apps"]; ok {
			t.Fatalf("nav.json with the switcher off still serves the switcher payload: %s", string(rec.Body))
		}
	}
}

// Response compression is the operator's choice and wraps the whole tree.
// The probe is a page rendered per request under /app/; the dashboard's
// embedded assets carry their own precomputed gzip variant and would read as
// compressed either way.
func TestHTTPHandlerCompressesOnlyWhenEnabled(t *testing.T) {
	const page = "/app/some-app/"
	gzip := map[string]string{"Accept-Encoding": "gzip"}

	off := false
	plain := newTestHTTPHandler(t, func(c *config.Config) { c.Server.Compression = &off })
	rec := plain.get(t, page, gzip)
	if got := rec.Header.Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding with compression off = %q, want none", got)
	}
	// The page must be large enough to compress, or the check below proves
	// nothing about the setting.
	if len(rec.Body) < 1024 {
		t.Fatalf("probe page is %d bytes, under the compression threshold", len(rec.Body))
	}

	on := newTestHTTPHandler(t, nil)
	if got := on.get(t, page, gzip).Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding with compression on = %q, want gzip", got)
	}
}
