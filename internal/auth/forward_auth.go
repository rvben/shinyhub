package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
)

// ErrUserNotFound is returned by ForwardAuthUserStore.GetForwardAuthUser when no
// user with that username exists. Distinct from sql.ErrNoRows so the auth package
// has no DB-driver dependency.
var ErrUserNotFound = errors.New("forward auth: user not found")

// ForwardAuthConfig mirrors config.ForwardAuthConfig. Duplicated here so the auth
// package has no import cycle on config.
type ForwardAuthConfig struct {
	Enabled    bool
	UserHeader string
	// EmailHeader is accepted but not yet consumed by the middleware (reserved).
	EmailHeader  string
	GroupsHeader string
	AdminGroups  []string
	DefaultRole  string
}

// ForwardAuthUserStore is the narrow store interface the middleware needs.
// Implemented by *db.Store via thin adapter methods.
type ForwardAuthUserStore interface {
	GetForwardAuthUser(username string) (*ContextUser, error)
	CreateForwardAuthUser(username, role string) (*ContextUser, error)
	PromoteToAdmin(userID int64) error
}

// ForwardAuthMiddleware trusts a username header from a reverse proxy whose direct
// peer IP is in trustedProxies. When the header is present and trusted, it looks up
// or auto-provisions the user, optionally promotes to admin based on the groups
// header, and attaches ContextUser to the request context.
//
// When disabled, header missing, or peer untrusted, the middleware passes the
// request through unchanged so subsequent middleware (BearerMiddleware) can run. It
// never writes a 401 - it either authenticates or falls through.
func ForwardAuthMiddleware(store ForwardAuthUserStore, cfg ForwardAuthConfig, trustedProxies []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}
			if !peerInTrustedProxies(r.RemoteAddr, trustedProxies) {
				next.ServeHTTP(w, r)
				return
			}
			username := strings.TrimSpace(r.Header.Get(cfg.UserHeader))
			if username == "" {
				next.ServeHTTP(w, r)
				return
			}

			user, err := store.GetForwardAuthUser(username)
			if errors.Is(err, ErrUserNotFound) {
				user, err = store.CreateForwardAuthUser(username, cfg.DefaultRole)
			}
			if err != nil || user == nil {
				http.Error(w, "forward auth: store error", http.StatusInternalServerError)
				return
			}

			if cfg.GroupsHeader != "" && len(cfg.AdminGroups) > 0 {
				groups := parseGroups(r.Header.Get(cfg.GroupsHeader))
				if anyGroupMatches(groups, cfg.AdminGroups) && user.Role != "admin" {
					if err := store.PromoteToAdmin(user.ID); err != nil {
						http.Error(w, "forward auth: promote error", http.StatusInternalServerError)
						return
					}
					user.Role = "admin"
				}
			}

			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// peerInTrustedProxies reports whether r.RemoteAddr's host portion is inside any
// trusted CIDR. Operates on the immediate TCP peer, NOT the XFF chain.
func peerInTrustedProxies(remoteAddr string, trusted []*net.IPNet) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range trusted {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// parseGroups splits a comma-separated header value into trimmed, non-empty group
// names. Order is preserved.
func parseGroups(header string) []string {
	parts := strings.Split(header, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// anyGroupMatches reports whether any element of have appears in the admin set.
func anyGroupMatches(have, admin []string) bool {
	set := make(map[string]struct{}, len(admin))
	for _, g := range admin {
		set[g] = struct{}{}
	}
	for _, g := range have {
		if _, ok := set[g]; ok {
			return true
		}
	}
	return false
}
