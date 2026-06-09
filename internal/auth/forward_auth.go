package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/textproto"
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
	EmailHeader       string
	GroupsHeader      string
	GroupRoleMappings []GroupRoleMapping
	DefaultRole       string
}

// ForwardAuthUserStore is the narrow store interface the middleware needs.
// Implemented by *db.Store via thin adapter methods.
type ForwardAuthUserStore interface {
	GetForwardAuthUser(username string) (*ContextUser, error)
	CreateForwardAuthUser(username, role string) (*ContextUser, error)
	GetUserGroups(userID int64) ([]string, error)
	ReconcileUserFromGroups(userID int64, groups []string, mappings []GroupRoleMapping, defaultRole string) error
}

// ForwardAuthMiddleware trusts a username header from a reverse proxy whose direct
// peer IP is in trustedProxies. When the header is present and trusted, it looks up
// or auto-provisions the user, optionally reconciles the user's role from the groups
// header (when present and changed), and attaches ContextUser to the request context.
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

			if cfg.GroupsHeader != "" {
				if vals, present := r.Header[textproto.CanonicalMIMEHeaderKey(cfg.GroupsHeader)]; present {
					groups := parseGroups(strings.Join(vals, ","))
					changed, err := groupsChanged(store, user.ID, groups)
					if err != nil {
						http.Error(w, "forward auth: groups error", http.StatusInternalServerError)
						return
					}
					if changed {
						if err := store.ReconcileUserFromGroups(user.ID, groups, cfg.GroupRoleMappings, cfg.DefaultRole); err != nil {
							http.Error(w, "forward auth: reconcile error", http.StatusInternalServerError)
							return
						}
						if fresh, err := store.GetForwardAuthUser(user.Username); err == nil && fresh != nil {
							user = fresh
						}
					}
				}
			}

			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// groupsChanged reports whether incoming differs from the stored group snapshot.
// Returns true (trigger reconcile) when lengths differ or any incoming group is
// absent from the stored set.
func groupsChanged(store ForwardAuthUserStore, userID int64, incoming []string) (bool, error) {
	stored, err := store.GetUserGroups(userID)
	if err != nil {
		return false, err
	}
	// The length check catches removals; the loop below catches additions.
	// Together they detect any set difference (order-insensitive).
	if len(stored) != len(incoming) {
		return true, nil
	}
	set := make(map[string]struct{}, len(stored))
	for _, g := range stored {
		set[g] = struct{}{}
	}
	for _, g := range incoming {
		if _, ok := set[g]; !ok {
			return true, nil
		}
	}
	return false, nil
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
