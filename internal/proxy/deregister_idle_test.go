package proxy_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/config"
	"github.com/rvben/shinyhub/internal/proxy"
)

// DeregisterIfIdle is the stop-first deploy's guard against disconnecting a
// session it was not permitted to disconnect: it removes the route only while
// nothing is in flight, and leaves the pool fully routable otherwise.
func TestDeregisterIfIdle(t *testing.T) {
	t.Run("unregistered slug is idle", func(t *testing.T) {
		p := proxy.New()
		if !p.PoolIdle("absent", 0) || p.DeregisterIfIdle("absent", 0) != "" {
			t.Fatal("a slug with no pool has no session to disconnect")
		}
	})

	t.Run("idle pool is removed", func(t *testing.T) {
		p := proxy.New()
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer backend.Close()
		p.SetPoolSize("quiet", 1)
		if err := p.RegisterReplica("quiet", 0, backend.URL, nil, 0); err != nil {
			t.Fatal(err)
		}
		if !p.PoolIdle("quiet", 0) {
			t.Fatal("pool with no request in flight reported busy")
		}
		if reason := p.DeregisterIfIdle("quiet", 0); reason != "" {
			t.Fatalf("idle pool was not removed: %s", reason)
		}
		if p.PoolHasAny("quiet") {
			t.Fatal("pool still registered after an idle deregister")
		}
	})

	t.Run("pool with a request in flight is kept", func(t *testing.T) {
		p := proxy.New()
		release := occupyPoolToCap(t, p, "busy", 1)
		released := false
		defer func() {
			if !released {
				release()
			}
		}()
		if p.PoolIdle("busy", 0) {
			t.Fatal("pool with a request in flight reported idle")
		}
		if reason := p.DeregisterIfIdle("busy", 0); reason != "1 open connection" {
			t.Fatalf("deregister with a request in flight = %q, want the open connection named", reason)
		}
		if !p.PoolHasAny("busy") {
			t.Fatal("refused deregister still dropped the route")
		}
		if got := p.PoolSessionSnapshot()["busy"].Sessions; got != 1 {
			t.Fatalf("sessions after refused deregister = %d, want the 1 in flight", got)
		}
		release()
		released = true
		if reason := p.DeregisterIfIdle("busy", 0); reason != "" {
			t.Fatalf("pool was not removed once its request finished: %s", reason)
		}
	})

	t.Run("a request held open across a re-registration keeps the pool", func(t *testing.T) {
		p := proxy.New()
		release := occupyPoolToCap(t, p, "readopted", 1)
		defer release()
		// Process recovery registers a replica the startup adoption already
		// registered. The visitor's connection predates the new entry.
		again := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer again.Close()
		if err := p.RegisterReplica("readopted", 0, again.URL, nil, 0); err != nil {
			t.Fatal(err)
		}
		if p.PoolIdle("readopted", 0) {
			t.Fatal("a connection opened before the replica registered again was treated as absent")
		}
		if reason := p.DeregisterIfIdle("readopted", 0); reason != "1 open connection" {
			t.Fatalf("deregister after a re-registration = %q, want the open connection named", reason)
		}
		if !p.PoolHasAny("readopted") {
			t.Fatal("refused deregister still dropped the route")
		}
	})

	t.Run("a request inside the quiet window keeps the pool", func(t *testing.T) {
		p := proxy.New()
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer backend.Close()
		p.SetPoolSize("visited", 1)
		if err := p.RegisterReplica("visited", 0, backend.URL, nil, 0); err != nil {
			t.Fatal(err)
		}
		p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/app/visited/", nil))
		if p.PoolIdle("visited", time.Hour) {
			t.Fatal("a visitor between page load and WebSocket was treated as absent")
		}
		if reason := p.DeregisterIfIdle("visited", time.Hour); reason != "a request reached it in the last 1h0m0s" {
			t.Fatalf("deregister right after a visit = %q, want the recent request named", reason)
		}
		if !p.PoolHasAny("visited") {
			t.Fatal("refused deregister still dropped the route")
		}
		if reason := p.DeregisterIfIdle("visited", time.Nanosecond); reason != "" {
			t.Fatalf("pool was not removed once the quiet window had passed: %s", reason)
		}
	})

	t.Run("a client bound to an elastic worker keeps the pool", func(t *testing.T) {
		p := proxy.New()
		p.SetPoolMode("elastic", config.IsolationPerSession, 0, 1)
		p.SetSpawnFunc(func(string, int) {})
		if !p.PoolIdle("elastic", 0) {
			t.Fatal("elastic pool with no client reported busy")
		}
		// The first request binds the client to a booting worker and returns the
		// loading page, so nothing stays in flight.
		p.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/app/elastic/", nil))
		if p.PoolIdle("elastic", 0) {
			t.Fatal("a client waiting for its worker was treated as absent")
		}
		if reason := p.DeregisterIfIdle("elastic", 0); reason != "1 visitor holds a worker" {
			t.Fatalf("deregister with a bound client = %q, want the held worker named", reason)
		}
	})
}
