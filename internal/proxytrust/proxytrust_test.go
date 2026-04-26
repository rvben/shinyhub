package proxytrust_test

import (
	"crypto/tls"
	"net"
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/proxytrust"
)

func mustCIDRs(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatalf("parse %q: %v", c, err)
		}
		out = append(out, n)
	}
	return out
}

func TestPeerIsTrusted(t *testing.T) {
	nets := mustCIDRs(t, "127.0.0.0/8", "10.0.0.0/8")

	cases := []struct {
		name       string
		remoteAddr string
		want       bool
	}{
		{"loopback inside trusted CIDR", "127.0.0.1:5000", true},
		{"private inside trusted CIDR", "10.1.2.3:5000", true},
		{"public outside any CIDR", "203.0.113.7:44444", false},
		{"empty CIDR list rejects everything", "127.0.0.1:5000", false},
		{"malformed RemoteAddr falls back without panicking", "garbage", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = tc.remoteAddr
			usedNets := nets
			if tc.name == "empty CIDR list rejects everything" {
				usedNets = nil
			}
			got := proxytrust.PeerIsTrusted(req, usedNets)
			if got != tc.want {
				t.Errorf("PeerIsTrusted(%q)=%v, want %v", tc.remoteAddr, got, tc.want)
			}
		})
	}
}

func TestHost(t *testing.T) {
	nets := mustCIDRs(t, "127.0.0.0/8")

	t.Run("trusted peer XFH wins over r.Host", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Host = "127.0.0.1:8080"
		req.Header.Set("X-Forwarded-Host", "hub.example.com")
		if got := proxytrust.Host(req, nets); got != "hub.example.com" {
			t.Errorf("trusted XFH: got %q, want hub.example.com", got)
		}
	})

	t.Run("untrusted peer XFH is ignored", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "203.0.113.7:5000"
		req.Host = "hub.example.com"
		req.Header.Set("X-Forwarded-Host", "evil.example.com")
		if got := proxytrust.Host(req, nets); got != "hub.example.com" {
			t.Errorf("untrusted XFH must be ignored: got %q, want hub.example.com", got)
		}
	})

	t.Run("trusted peer with empty XFH falls back to r.Host", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Host = "fallback.example.com"
		if got := proxytrust.Host(req, nets); got != "fallback.example.com" {
			t.Errorf("missing XFH: got %q, want fallback.example.com", got)
		}
	})

	t.Run("trusted peer with multiple XFH values takes the first", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Header.Set("X-Forwarded-Host", "hub.example.com, evil.example.com")
		if got := proxytrust.Host(req, nets); got != "hub.example.com" {
			t.Errorf("multi-XFH: got %q, want hub.example.com", got)
		}
	})
}

func TestScheme(t *testing.T) {
	nets := mustCIDRs(t, "127.0.0.0/8")

	t.Run("direct TLS is authoritative", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.TLS = &tls.ConnectionState{}
		req.Header.Set("X-Forwarded-Proto", "http")
		if got := proxytrust.Scheme(req, nets); got != "https" {
			t.Errorf("direct TLS: got %q, want https", got)
		}
	})

	t.Run("trusted peer XFP wins", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Header.Set("X-Forwarded-Proto", "https")
		if got := proxytrust.Scheme(req, nets); got != "https" {
			t.Errorf("trusted XFP: got %q, want https", got)
		}
	})

	t.Run("untrusted peer XFP is ignored", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "203.0.113.7:5000"
		req.Header.Set("X-Forwarded-Proto", "https")
		if got := proxytrust.Scheme(req, nets); got != "http" {
			t.Errorf("untrusted XFP must be ignored: got %q, want http", got)
		}
	})
}

func TestClientIP(t *testing.T) {
	nets := mustCIDRs(t, "127.0.0.0/8")

	t.Run("trusted peer first XFF entry wins", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		req.Header.Set("X-Forwarded-For", "198.51.100.1, 10.0.0.1")
		if got := proxytrust.ClientIP(req, nets); got != "198.51.100.1" {
			t.Errorf("trusted XFF: got %q, want 198.51.100.1", got)
		}
	})

	t.Run("untrusted peer XFF is ignored", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "203.0.113.7:5000"
		req.Header.Set("X-Forwarded-For", "198.51.100.1")
		if got := proxytrust.ClientIP(req, nets); got != "203.0.113.7" {
			t.Errorf("untrusted XFF: got %q, want 203.0.113.7", got)
		}
	})

	t.Run("trusted peer without XFF returns peer host", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/", nil)
		req.RemoteAddr = "127.0.0.1:5000"
		if got := proxytrust.ClientIP(req, nets); got != "127.0.0.1" {
			t.Errorf("no XFF: got %q, want 127.0.0.1", got)
		}
	})
}
