package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/identity"
)

// startEchoBackend returns a backend that records the headers it received.
func startEchoBackend(t *testing.T) (*httptest.Server, func() http.Header) {
	t.Helper()
	var mu sync.Mutex
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = r.Header.Clone()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, func() http.Header {
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

type staticGroups struct{ g []string }

func (s staticGroups) GetUserGroups(int64) ([]string, error) { return s.g, nil }

func newIdentityProxy(t *testing.T, backendURL string, enabled bool) *Proxy {
	t.Helper()
	p := New()
	p.SetPoolSize("demo", 1)
	p.SetPoolAppID("demo", 42)
	p.SetPoolIdentityHeaders("demo", enabled)
	prov := identity.NewProvider("test-secret", staticGroups{[]string{"eng", "a,b"}})
	p.SetIdentityProvider(prov.PayloadFor)
	if err := p.RegisterReplica("demo", 0, backendURL, nil, 1); err != nil {
		t.Fatal(err)
	}
	return p
}

func doIdentityReq(t *testing.T, p *Proxy, user *auth.ContextUser, hdr map[string]string) {
	t.Helper()
	r := httptest.NewRequest("GET", "/app/demo/", nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	if user != nil {
		r = r.WithContext(auth.WithUser(r.Context(), user))
	}
	p.ServeHTTP(httptest.NewRecorder(), r)
}

func TestIdentityHeaders_InjectedForAuthenticatedUser(t *testing.T) {
	srv, got := startEchoBackend(t)
	p := newIdentityProxy(t, srv.URL, true)
	doIdentityReq(t, p, &auth.ContextUser{ID: 5, Username: "ana", Role: "developer"}, nil)
	h := got()
	if h.Get(identity.HeaderUser) != "ana" || h.Get(identity.HeaderUserID) != "5" ||
		h.Get(identity.HeaderRole) != "developer" {
		t.Fatalf("headers = %v", h)
	}
	if h.Get(identity.HeaderGroups) != "eng" { // "a,b" omitted from the plain header
		t.Fatalf("groups header = %q", h.Get(identity.HeaderGroups))
	}
	if h.Get(identity.HeaderToken) == "" {
		t.Fatal("identity token must be injected")
	}
}

func TestIdentityHeaders_InboundSpoofStrippedAlways(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		srv, got := startEchoBackend(t)
		p := newIdentityProxy(t, srv.URL, enabled)
		doIdentityReq(t, p, nil, map[string]string{
			"X-Shinyhub-User": "forged-admin",
			"x-shinyhub-role": "admin", // case variation; Go canonicalizes
		})
		h := got()
		if h.Get(identity.HeaderUser) != "" || h.Get(identity.HeaderRole) != "" {
			t.Fatalf("enabled=%v: spoofed headers must be stripped, got %v", enabled, h)
		}
	}

	// Non-canonical direct map write (cannot arrive from the wire; guards
	// against in-binary middleware inserting a bypass key).
	srv, got := startEchoBackend(t)
	p := newIdentityProxy(t, srv.URL, true)
	r := httptest.NewRequest("GET", "/app/demo/", nil)
	r.Header["x-shinyhub-bypass"] = []string{"internal-set"}
	p.ServeHTTP(httptest.NewRecorder(), r)
	for k := range got() {
		if strings.HasPrefix(strings.ToLower(k), "x-shinyhub-") {
			t.Fatalf("header %q reached the backend", k)
		}
	}
}

func TestIdentityHeaders_AnonymousGetsNone(t *testing.T) {
	srv, got := startEchoBackend(t)
	p := newIdentityProxy(t, srv.URL, true)
	doIdentityReq(t, p, nil, nil)
	h := got()
	if h.Get(identity.HeaderToken) != "" || h.Get(identity.HeaderUser) != "" {
		t.Fatal("anonymous request must carry no identity headers")
	}
}

func TestIdentityHeaders_OptedOutPoolGetsNoneButStillStrips(t *testing.T) {
	srv, got := startEchoBackend(t)
	p := newIdentityProxy(t, srv.URL, false)
	doIdentityReq(t, p, &auth.ContextUser{ID: 5, Username: "ana", Role: "admin"},
		map[string]string{"X-Shinyhub-Groups": "forged"})
	h := got()
	if h.Get(identity.HeaderUser) != "" || h.Get(identity.HeaderGroups) != "" {
		t.Fatalf("opted-out pool must get no identity headers, got %v", h)
	}
}

func TestSetPoolIdentityHeaders_ConcurrentWithTraffic(t *testing.T) {
	// Data-race guard: live flag flips while requests flow. Run under -race.
	srv, _ := startEchoBackend(t)
	p := newIdentityProxy(t, srv.URL, true)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			p.SetPoolIdentityHeaders("demo", i%2 == 0)
		}
		close(done)
	}()
	for i := 0; i < 200; i++ {
		doIdentityReq(t, p, &auth.ContextUser{ID: 1, Username: "u", Role: "viewer"}, nil)
	}
	<-done
}
