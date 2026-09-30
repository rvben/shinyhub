package proxy

import (
	"net/http"
	"strings"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
	"github.com/rvben/shinyhub/internal/sessionui"
)

type browserSessionSettings struct {
	maxAge  time.Duration
	homeURL string
}

// SetBrowserSessions enables the shared renewal client and hard browser login
// deadlines. It is independent of all optional page enhancements.
func (p *Proxy) SetBrowserSessions(maxAge time.Duration, homeURL string) {
	if maxAge <= 0 {
		p.browserSessions.Store(nil)
		return
	}
	p.browserSessions.Store(&browserSessionSettings{maxAge: maxAge, homeURL: strings.TrimRight(homeURL, "/")})
}

func (p *Proxy) browserSessionDeadline(r *http.Request) time.Time {
	settings := p.browserSessions.Load()
	if settings == nil || r == nil {
		return time.Time{}
	}
	u := auth.UserFromContext(r.Context())
	ti := auth.TokenInfoFromContext(r.Context())
	if u == nil || u.SupportSession != nil || ti == nil {
		return time.Time{}
	}
	if ti.AuthTime.IsZero() {
		// A legacy socket cannot have its login time reconstructed. Its current
		// signed expiry bounds it; a fresh connection carries the migrated time.
		return ti.ExpiresAt
	}
	return ti.AuthTime.Add(settings.maxAge).Truncate(time.Second)
}

func (p *Proxy) browserPageScript(r *http.Request, slug string) *pageScript {
	settings := p.browserSessions.Load()
	if settings == nil || r == nil {
		return nil
	}
	u := auth.UserFromContext(r.Context())
	if u == nil || u.SupportSession != nil {
		return nil
	}
	return &pageScript{snippet: sessionui.Snippet(slug, settings.homeURL+"/app/"+slug+"/", u.ID), cspHash: sessionui.CSPHash}
}
