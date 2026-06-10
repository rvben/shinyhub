package identity

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

var errFake = errors.New("boom")

type fakeGroups struct {
	calls  atomic.Int64
	groups []string
	err    error
}

func (f *fakeGroups) GetUserGroups(userID int64) ([]string, error) {
	f.calls.Add(1)
	return f.groups, f.err
}

func TestProvider_PayloadCarriesIdentity(t *testing.T) {
	src := &fakeGroups{groups: []string{"eng", "ops"}}
	p := NewProvider("secret", src)
	pl := p.PayloadFor(&auth.ContextUser{ID: 5, Username: "ana", Role: "developer"}, "demo", 42)
	if pl.Username != "ana" || pl.UserID != "5" || pl.Role != "developer" {
		t.Fatalf("payload = %+v", pl)
	}
	if pl.GroupsHeader != "eng,ops" {
		t.Fatalf("groups header = %q", pl.GroupsHeader)
	}
	if pl.Token == "" {
		t.Fatal("token must be minted")
	}
}

func TestProvider_GroupsCachedWithinTTL(t *testing.T) {
	src := &fakeGroups{groups: []string{"eng"}}
	p := NewProvider("secret", src)
	u := &auth.ContextUser{ID: 9, Username: "u", Role: "viewer"}
	p.PayloadFor(u, "demo", 1)
	p.PayloadFor(u, "demo", 1)
	if got := src.calls.Load(); got != 1 {
		t.Fatalf("DB calls = %d, want 1 (cached)", got)
	}
}

func TestProvider_CacheExpires(t *testing.T) {
	src := &fakeGroups{groups: []string{"eng"}}
	p := NewProvider("secret", src)
	p.cacheTTL = 10 * time.Millisecond
	u := &auth.ContextUser{ID: 9, Username: "u", Role: "viewer"}
	p.PayloadFor(u, "demo", 1)
	time.Sleep(20 * time.Millisecond)
	p.PayloadFor(u, "demo", 1)
	if got := src.calls.Load(); got != 2 {
		t.Fatalf("DB calls = %d, want 2 (expired)", got)
	}
}

func TestProvider_LookupErrorYieldsEmptyGroupsNotFailure(t *testing.T) {
	// The advisory identity payload must never fail the request.
	src := &fakeGroups{err: errFake}
	p := NewProvider("secret", src)
	pl := p.PayloadFor(&auth.ContextUser{ID: 1, Username: "u", Role: "viewer"}, "demo", 1)
	if pl == nil || pl.GroupsHeader != "" || pl.Token == "" {
		t.Fatalf("payload = %+v; want token minted with no groups", pl)
	}
}

func TestProvider_CacheBounded(t *testing.T) {
	src := &fakeGroups{groups: []string{"g"}}
	p := NewProvider("secret", src)
	p.cacheMax = 10
	for i := int64(0); i < 100; i++ {
		p.PayloadFor(&auth.ContextUser{ID: i, Username: "u", Role: "viewer"}, "demo", 1)
	}
	p.mu.Lock()
	n := len(p.cache)
	p.mu.Unlock()
	if n > 10 {
		t.Fatalf("cache size = %d, want <= 10", n)
	}
}
