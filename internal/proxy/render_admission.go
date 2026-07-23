package proxy

import (
	"net/http"
	"strconv"
	"sync"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/proxytrust"
)

// renderPrincipal returns the fairness key for this request under slug's access
// mode. The key is chosen by the app's access mode, not by what the request
// presents, so a client cannot pick which bucket it lands in: a public app keys
// on the trusted client IP even when a user is present, and a private or shared
// app keys on the authenticated user id (access middleware has already rejected
// anonymous requests for those modes). When the access lookup is unwired or
// returns an unknown mode, both keys are combined so neither gate is skipped,
// which is conservative in both directions.
func (p *Proxy) renderPrincipal(r *http.Request, slug string) string {
	mode := ""
	if fp := p.appAccessLookup.Load(); fp != nil {
		mode = (*fp)(slug)
	}
	ip := proxytrust.ClientIP(r, p.trustedProxyNets())
	uid := int64(0)
	if u := auth.UserFromContext(r.Context()); u != nil {
		uid = u.ID
	}
	switch mode {
	case "public":
		return "ip:" + ip
	case "private", "shared":
		return "u:" + strconv.FormatInt(uid, 10)
	default:
		return "ip:" + ip + "|u:" + strconv.FormatInt(uid, 10)
	}
}

// parkBudget bounds how many render-paced WebSocket upgrades may be parked at
// once, per app and host-wide. A parked upgrade holds a goroutine and a socket,
// so an unbounded park queue under a stampede is itself a resource exhaustion;
// the ceilings cap it. A non-positive ceiling means that dimension is unlimited.
type parkBudget struct {
	mu     sync.Mutex
	perApp int
	total  int
	byApp  map[string]int
	inUse  int
}

func newParkBudget(perApp, total int) *parkBudget {
	return &parkBudget{perApp: perApp, total: total, byApp: make(map[string]int)}
}

// acquire takes a park slot for slug if both the per-app and global ceilings
// allow it, returning whether it did. A refused acquire takes nothing.
func (b *parkBudget) acquire(slug string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.total > 0 && b.inUse >= b.total {
		return false
	}
	if b.perApp > 0 && b.byApp[slug] >= b.perApp {
		return false
	}
	b.inUse++
	b.byApp[slug]++
	return true
}

// release returns a park slot for slug. It is a no-op guard against underflow.
func (b *parkBudget) release(slug string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inUse > 0 {
		b.inUse--
	}
	if b.byApp[slug] > 0 {
		b.byApp[slug]--
		if b.byApp[slug] == 0 {
			delete(b.byApp, slug)
		}
	}
}
