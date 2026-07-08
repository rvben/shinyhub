package proxy

import (
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/identity"
)

// identityProviderFn assembles the identity payload for one request. Injected
// from cmd/shinyhub (where the auth secret and store live); the proxy itself
// holds no secret and no store. Named type so atomic.Pointer can hold it,
// matching the clientIPFn / accessLogFn pattern.
type identityProviderFn func(user *auth.ContextUser, slug string, appID int64) *identity.Payload

// SetIdentityProvider wires the identity payload assembler. Call once at
// startup before serving; a nil provider disables injection (headers are
// still stripped).
func (p *Proxy) SetIdentityProvider(fn identityProviderFn) {
	if fn == nil {
		return
	}
	p.identityProvider.Store(&fn)
}

// SetPoolIdentityHeaders sets the per-pool identity-forwarding flag (the
// effective value, post global-config resolution). Creates the pool (size 1)
// if absent so callers can configure it before spawning replicas, matching
// SetPoolCap.
func (p *Proxy) SetPoolIdentityHeaders(slug string, enabled bool) {
	p.mu.Lock()
	pool, ok := p.pools[slug]
	if !ok {
		pool = &backendPool{size: 1, replicas: make([]*replicaBackend, 1)}
		p.pools[slug] = pool
	}
	p.mu.Unlock()
	pool.identityHeaders.Store(enabled)
}

// PoolIdentityHeaders reports the current identityHeaders flag for slug.
// Returns false when the pool does not exist. Used in tests.
func (p *Proxy) PoolIdentityHeaders(slug string) bool {
	p.mu.RLock()
	pool := p.pools[slug]
	p.mu.RUnlock()
	if pool == nil {
		return false
	}
	return pool.identityHeaders.Load()
}

// applyIdentityHeaders runs in the Director, after stripInternalCookies.
// First, UNCONDITIONALLY (before any flag check or early return) it deletes
// every inbound X-Shinyhub-* header: these are platform-internal and no
// client or upstream proxy may supply them, regardless of trusted_proxies
// (which governs only X-Forwarded-* trust). The strip matches raw map keys
// case-insensitively (delete by raw key, not Header.Del which canonicalizes)
// so non-canonical keys written directly to the map by in-binary middleware
// are also removed. Then, when the pool's flag is on and the request carries
// an authenticated user, it injects the identity headers and the per-app
// signed token.
func applyIdentityHeaders(req *http.Request, pool *backendPool, slug string, provider *atomic.Pointer[identityProviderFn]) {
	for k := range req.Header {
		if len(k) >= len(identity.HeaderPrefix) && strings.EqualFold(k[:len(identity.HeaderPrefix)], identity.HeaderPrefix) {
			delete(req.Header, k)
		}
	}
	if !pool.identityHeaders.Load() {
		return
	}
	user := auth.UserFromContext(req.Context())
	if user == nil {
		return
	}
	fnp := provider.Load()
	if fnp == nil {
		return
	}
	pl := (*fnp)(user, slug, pool.appID.Load())
	if pl == nil {
		return
	}
	req.Header.Set(identity.HeaderUser, pl.Username)
	req.Header.Set(identity.HeaderUserID, pl.UserID)
	req.Header.Set(identity.HeaderRole, pl.Role)
	if pl.AppRole != "" {
		req.Header.Set(identity.HeaderAppRole, pl.AppRole)
	}
	if pl.Email != "" {
		req.Header.Set(identity.HeaderEmail, pl.Email)
	}
	if pl.Name != "" {
		req.Header.Set(identity.HeaderName, pl.Name)
	}
	if pl.GroupsHeader != "" {
		req.Header.Set(identity.HeaderGroups, pl.GroupsHeader)
	}
	if pl.GroupsTruncated {
		req.Header.Set(identity.HeaderGroupsTruncated, "true")
	}
	if pl.Token != "" {
		req.Header.Set(identity.HeaderToken, pl.Token)
	}
}
