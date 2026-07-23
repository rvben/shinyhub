package proxy

import (
	"net/http"
	"strconv"

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
