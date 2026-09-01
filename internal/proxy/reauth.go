package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/rvben/shinyhub/internal/auth"
)

// ConnPrincipal is the identity an upgraded (WebSocket) connection was admitted
// under, captured at the hijack - the last instant the request context still
// exists. Everything in it is comparable, so it doubles as a map key when a
// sweep decides one verdict per principal rather than one per connection.
//
// The proxy holds no store and no secret, so it cannot interpret these fields;
// it only carries them back to the injected Reauthorizer.
type ConnPrincipal struct {
	Slug              string
	UserID            int64 // 0 when the connection was admitted anonymously
	Role              string
	SessionEpoch      int64
	JTI               string
	ActorID           int64
	ActorRole         string
	ActorSessionEpoch int64
	SupportAppID      int64
	RoutedAppID       int64
	SupportExpiresAt  time.Time
}

// Reauthorizer re-decides whether a live upgraded connection may stay open.
//
// It must report revoked=true with a short reason ONLY when access has
// definitively lapsed. A non-nil error means the decision could not be made and
// the connection is kept. Failing open on error is deliberate: a database blip
// must not drop every live session on the instance.
type Reauthorizer func(ConnPrincipal) (revoked bool, reason string, err error)

// connPrincipal reads the identity the access middleware attached to this
// request. An anonymous request on a public app yields the zero principal,
// which is exactly right: there is no identity to revoke, only the app's
// openness to re-check.
func connPrincipal(r *http.Request, slug string, routedAppID int64) ConnPrincipal {
	p := ConnPrincipal{Slug: slug, RoutedAppID: routedAppID}
	if u := auth.UserFromContext(r.Context()); u != nil {
		p.UserID = u.ID
		p.Role = u.Role
		p.SessionEpoch = u.TokenEpoch
		if support := u.SupportSession; support != nil {
			p.ActorID = support.ActorID
			p.ActorRole = string(auth.RoleAdmin)
			p.ActorSessionEpoch = support.ActorTokenEpoch
			p.SupportAppID = support.AppID
			p.SupportExpiresAt = support.ExpiresAt
		}
	}
	if t := auth.TokenInfoFromContext(r.Context()); t != nil {
		p.JTI = t.JTI
	}
	return p
}

// RecheckSessions re-runs fn over every live upgraded connection and force-closes
// the ones whose access has lapsed. It returns how many it closed.
//
// Connections sharing a principal are decided once per pass: a user with eight
// tabs open on one app costs one decision, not eight.
//
// A lapsed connection is closed at the socket, without a WebSocket close frame.
// The proxy's copy loops own the write side of a live tunnel, so injecting a
// frame here would risk interleaving with a partially written one and corrupting
// the stream. The client sees a dropped connection, reconnects, and meets the
// access middleware - which now denies it, with the proper 401/403 page. This is
// the same close the shutdown drain performs.
func (p *Proxy) RecheckSessions(fn Reauthorizer) int {
	if fn == nil {
		return 0
	}
	live := p.conns.snapshot()
	if len(live) == 0 {
		return 0
	}
	// "" means keep; anything else is the reason to close.
	verdicts := make(map[ConnPrincipal]string, len(live))
	closed := 0
	for _, c := range live {
		reason, decided := verdicts[c.principal]
		if !decided {
			reason = p.recheckOne(fn, c.principal)
			verdicts[c.principal] = reason
		}
		if reason == "" {
			continue
		}
		c.Close()
		closed++
	}
	return closed
}

// recheckOne returns the reason to close every connection held by this
// principal, or "" to keep them. It logs the decision once per principal so a
// user with many tabs produces one line, not one per tab.
func (p *Proxy) recheckOne(fn Reauthorizer, principal ConnPrincipal) string {
	revoked, reason, err := fn(principal)
	if err != nil {
		slog.Warn("session recheck failed, keeping connections",
			"slug", principal.Slug, "user_id", principal.UserID, "err", err)
		return ""
	}
	if !revoked {
		return ""
	}
	if reason == "" {
		reason = "access revoked"
	}
	slog.Info("closing upgraded connections: access revoked",
		"slug", principal.Slug, "user_id", principal.UserID, "reason", reason)
	return reason
}

// StartSessionRecheck re-runs fn over this instance's live upgraded connections
// every interval, until ctx is done. A non-positive interval or a nil fn
// disables it and returns immediately.
//
// It is deliberately NOT gated on control-plane ownership. Every instance holds
// its own hijacked connections and no other instance can reach them, so a
// standby must sweep its own exactly as the owner does. That is also why
// revocation is handled by a sweep at all rather than by closing connections at
// each revocation site: the instance processing a revocation generally does not
// hold the connections it revokes.
func (p *Proxy) StartSessionRecheck(ctx context.Context, interval time.Duration, fn Reauthorizer) {
	if interval <= 0 || fn == nil {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := p.RecheckSessions(fn); n > 0 {
				slog.Info("session recheck closed upgraded connections", "count", n)
			}
		}
	}
}
