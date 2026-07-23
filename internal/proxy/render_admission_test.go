package proxy

import (
	"net/http/httptest"
	"sync"
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

func TestParkBudget_PerAppAndGlobalCeilings(t *testing.T) {
	b := newParkBudget(2, 3) // 2 per app, 3 global
	// Two parks for app-a succeed, the third fails the per-app ceiling.
	if !b.acquire("app-a") || !b.acquire("app-a") {
		t.Fatal("first two app-a parks should succeed")
	}
	if b.acquire("app-a") {
		t.Fatal("third app-a park should fail the per-app ceiling of 2")
	}
	// app-b can still park (its own per-app count is 0), up to the global 3.
	if !b.acquire("app-b") {
		t.Fatal("app-b first park should succeed under the global ceiling")
	}
	if b.acquire("app-b") {
		t.Fatal("app-b second park should fail: global ceiling of 3 reached")
	}
	// Releasing an app-a slot frees room under both ceilings.
	b.release("app-a")
	if !b.acquire("app-b") {
		t.Fatal("after releasing app-a, app-b should park under the freed global slot")
	}
}

func TestParkBudget_UnlimitedWhenNonPositive(t *testing.T) {
	b := newParkBudget(0, 0) // both unlimited
	for i := 0; i < 1000; i++ {
		if !b.acquire("x") {
			t.Fatalf("unlimited budget refused acquire %d", i)
		}
	}
}

func TestParkBudget_ConcurrentNeverExceedsGlobal(t *testing.T) {
	b := newParkBudget(0, 50) // per-app unlimited, global 50
	var granted int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if b.acquire("app") {
				mu.Lock()
				granted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if granted != 50 {
		t.Fatalf("concurrent acquires granted %d, want exactly 50 (global ceiling)", granted)
	}
}
