package proxy

import (
	"net/http/httptest"
	"testing"

	"github.com/rvben/shinyhub/internal/auth"
)

func TestRenderPrincipal_PublicKeysByIP(t *testing.T) {
	p := New()
	p.SetAppAccessLookup(func(slug string) string { return "public" })
	r := httptest.NewRequest("GET", "/app/demo/", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	// A public app keys on the trusted client IP, regardless of any user.
	got := p.renderPrincipal(r, "demo")
	if got != "ip:203.0.113.7" {
		t.Fatalf("public principal = %q, want ip:203.0.113.7", got)
	}
}

func TestRenderPrincipal_PublicIgnoresUser(t *testing.T) {
	p := New()
	p.SetAppAccessLookup(func(slug string) string { return "public" })
	r := httptest.NewRequest("GET", "/app/demo/", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	r = r.WithContext(auth.WithUser(r.Context(), &auth.ContextUser{ID: 42}))
	// Even with an authenticated user, a public app keys on IP so the client
	// cannot opt into a second bucket by presenting or withholding a token.
	if got := p.renderPrincipal(r, "demo"); got != "ip:203.0.113.7" {
		t.Fatalf("public+user principal = %q, want ip:203.0.113.7", got)
	}
}

func TestRenderPrincipal_PrivateKeysByUser(t *testing.T) {
	p := New()
	p.SetAppAccessLookup(func(slug string) string { return "private" })
	r := httptest.NewRequest("GET", "/app/demo/", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	r = r.WithContext(auth.WithUser(r.Context(), &auth.ContextUser{ID: 42}))
	if got := p.renderPrincipal(r, "demo"); got != "u:42" {
		t.Fatalf("private principal = %q, want u:42", got)
	}
}

func TestRenderPrincipal_UnwiredLookupUsesBothGates(t *testing.T) {
	p := New() // no SetAppAccessLookup: conservative both-gates keying
	r := httptest.NewRequest("GET", "/app/demo/", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	r = r.WithContext(auth.WithUser(r.Context(), &auth.ContextUser{ID: 42}))
	if got := p.renderPrincipal(r, "demo"); got != "ip:203.0.113.7|u:42" {
		t.Fatalf("unwired principal = %q, want ip:203.0.113.7|u:42", got)
	}
}
