package auth

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func faStripConfig() ForwardAuthConfig {
	return ForwardAuthConfig{
		Enabled:      true,
		UserHeader:   "Remote-User",
		GroupsHeader: "Remote-Groups",
		EmailHeader:  "Remote-Email",
		NameHeader:   "Remote-Name",
		DefaultRole:  "developer",
	}
}

// TestForwardAuth_StripsHeadersFromUntrustedPeer proves the forward-auth headers
// are removed from the request before any downstream handler even when the peer
// is untrusted - so a caller reaching the port directly cannot inject a forged
// Remote-User/-Groups/-Email/-Name into a backend app (SEC-M4).
func TestForwardAuth_StripsHeadersFromUntrustedPeer(t *testing.T) {
	var seen http.Header
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	})
	h := ForwardAuthMiddleware(newFakeStore(), faStripConfig(), nil)(next) // nil trusted => untrusted

	req := httptest.NewRequest("GET", "/app/demo/", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	req.Header.Set("Remote-User", "attacker")
	req.Header.Set("Remote-Groups", "admins")
	req.Header.Set("Remote-Email", "a@evil.com")
	req.Header.Set("Remote-Name", "Attacker")
	h.ServeHTTP(httptest.NewRecorder(), req)

	for _, hn := range []string{"Remote-User", "Remote-Groups", "Remote-Email", "Remote-Name"} {
		if seen.Get(hn) != "" {
			t.Errorf("forward-auth header %q reached the backend from an untrusted peer (injection)", hn)
		}
	}
}

// TestForwardAuth_StripsHeadersOnTrustedAuthedPath proves that on the legitimate
// trusted path the middleware still authenticates the user AND strips the raw
// forward-auth headers so the backend app never receives them (identity reaches
// apps only via the X-Shinyhub-* channel).
func TestForwardAuth_StripsHeadersOnTrustedAuthedPath(t *testing.T) {
	_, trusted, _ := net.ParseCIDR("127.0.0.0/8")
	var seen http.Header
	var gotUser *ContextUser
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		gotUser = UserFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})
	h := ForwardAuthMiddleware(newFakeStore(), faStripConfig(), []*net.IPNet{trusted})(next)

	req := httptest.NewRequest("GET", "/app/demo/", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("Remote-User", "alice")
	req.Header.Set("Remote-Groups", "admins")
	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotUser == nil || gotUser.Username != "alice" {
		t.Fatalf("forward auth did not authenticate alice on the trusted path: %+v", gotUser)
	}
	if seen.Get("Remote-User") != "" || seen.Get("Remote-Groups") != "" {
		t.Error("forward-auth headers were not stripped on the trusted authenticated path")
	}
}
