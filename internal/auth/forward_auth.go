package auth

import (
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"sync"
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
	// EmailHeader is the proxy header carrying the user's email. When set, the
	// middleware captures it request-scoped onto the ContextUser (forwarded to
	// apps as X-Shinyhub-Email and the token email claim); not persisted.
	EmailHeader string
	// NameHeader is the proxy header carrying the user's friendly name (e.g.
	// Authelia's Remote-Name). When set and present, the middleware captures it
	// as the forward-auth user's display name. Empty disables name capture.
	NameHeader        string
	GroupsHeader      string
	GroupRoleMappings []GroupRoleMapping
	DefaultRole       string
	// RequireGroupsHeader, when true, causes the middleware to refuse (403) any
	// request that is missing the configured groups header instead of treating it
	// as no groups. Default false preserves the fail-secure revoke behavior.
	RequireGroupsHeader bool
}

// ForwardAuthUserStore is the narrow store interface the middleware needs.
// Implemented by *db.Store via thin adapter methods.
type ForwardAuthUserStore interface {
	GetForwardAuthUser(username string) (*ContextUser, error)
	CreateForwardAuthUser(username, role string) (*ContextUser, error)
	GetUserGroups(userID int64) ([]string, error)
	ReconcileUserFromGroups(userID int64, groups []string, mappings []GroupRoleMapping, defaultRole string) error
	// SetDisplayNameFromIdP refreshes the user's display name from the proxy's
	// name header. The store skips local-password accounts, so forward-auth
	// (IdP-governed) users are updated and a missing name is a no-op.
	SetDisplayNameFromIdP(userID int64, name string) error
}

// ForwardAuthMiddleware trusts a username header from a reverse proxy whose direct
// peer IP is in trustedProxies. When the header is present and trusted, it looks up
// or auto-provisions the user, reconciles the user's role from the groups header
// whenever the group snapshot has changed (treating an absent header as no groups),
// and attaches ContextUser to the request context.
//
// When disabled, header missing, or peer untrusted, the middleware passes the
// request through unchanged so subsequent middleware (BearerMiddleware) can run. It
// never writes a 401 - it either authenticates or falls through.
//
// When cfg.RequireGroupsHeader is true and a groups header is configured, any
// request that omits the groups header is refused with 403 so that a proxy
// misconfiguration fails loudly instead of silently demoting users.
func ForwardAuthMiddleware(store ForwardAuthUserStore, cfg ForwardAuthConfig, trustedProxies []*net.IPNet) func(http.Handler) http.Handler {
	// warnedPeers tracks untrusted peer IPs that have already triggered the
	// misconfiguration warning so we log at most once per distinct IP per
	// server lifetime.
	var warnedPeers sync.Map

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !cfg.Enabled {
				next.ServeHTTP(w, r)
				return
			}
			// Capture the forward-auth header values for our own auth decision, then
			// STRIP every configured forward-auth header from the request before any
			// downstream handler (API or the /app proxy) can see it. These headers
			// are ShinyHub's ingress-auth channel from the trusted proxy; a backend
			// app receives identity only via the X-Shinyhub-* channel. Stripping
			// unconditionally (even from an untrusted direct-port peer) prevents a
			// caller from injecting a forged Remote-User/-Groups/-Email/-Name into a
			// tenant app.
			userHdr := strings.TrimSpace(r.Header.Get(cfg.UserHeader))
			nameHdr := strings.TrimSpace(r.Header.Get(cfg.NameHeader))
			emailHdr := strings.TrimSpace(r.Header.Get(cfg.EmailHeader))
			var groupsVals []string
			groupsPresent := false
			if cfg.GroupsHeader != "" {
				groupsVals, groupsPresent = r.Header[textproto.CanonicalMIMEHeaderKey(cfg.GroupsHeader)]
			}
			delForwardAuthHeader(r, cfg.UserHeader)
			delForwardAuthHeader(r, cfg.GroupsHeader)
			delForwardAuthHeader(r, cfg.NameHeader)
			delForwardAuthHeader(r, cfg.EmailHeader)

			if !peerInTrustedProxies(r.RemoteAddr, trustedProxies) {
				// If the request carries the user header that would authenticate a
				// user, the operator most likely forgot to add this peer to
				// server.trusted_proxies. Log a WARN once per distinct peer IP so
				// the misconfiguration surfaces without flooding the log.
				if userHdr != "" {
					host, _, err := net.SplitHostPort(r.RemoteAddr)
					if err != nil {
						host = r.RemoteAddr
					}
					if _, alreadyWarned := warnedPeers.LoadOrStore(host, struct{}{}); !alreadyWarned {
						slog.Warn("forward_auth: user header present from untrusted peer; add peer to server.trusted_proxies to enable forward auth",
							"peer", host,
							"user_header", cfg.UserHeader,
						)
					}
				}
				next.ServeHTTP(w, r)
				return
			}
			username := userHdr
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
				vals, present := groupsVals, groupsPresent
				if !present && cfg.RequireGroupsHeader {
					// Strict mode: the operator requires the proxy to assert group
					// membership on every request. A missing header is a proxy
					// misconfiguration, so refuse rather than silently revoke.
					http.Error(w, "forward auth: groups header required but missing", http.StatusForbidden)
					return
				}
				// Default mode: an absent header means no groups, so we reconcile and
				// revoke group-derived roles. A present (even empty) header is the
				// authoritative current membership.
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

			// Capture the friendly name from the proxy. The proxy is authoritative
			// (the user is governed by the upstream IdP), so refresh whenever it
			// differs from the stored value; the equality guard keeps this off the
			// write path for the common unchanged case, since this middleware runs
			// on every request.
			if cfg.NameHeader != "" {
				if name := nameHdr; name != "" && name != user.DisplayName {
					if err := store.SetDisplayNameFromIdP(user.ID, name); err != nil {
						slog.Warn("forward_auth: failed to update display name", "user", user.Username, "err", err)
					} else {
						user.DisplayName = name
					}
				}
			}

			// Capture the email the proxy asserts, if configured. It is
			// request-scoped (the users table has no email column) and forwarded
			// to apps via X-Shinyhub-Email and the identity token's email claim.
			if cfg.EmailHeader != "" {
				user.Email = emailHdr
			}

			ctx := WithUser(r.Context(), user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// delForwardAuthHeader removes a forward-auth header from the request by both
// its canonical and raw case-insensitive key, so a value written directly to the
// header map (non-canonical key) is also removed. A no-op for an empty name.
func delForwardAuthHeader(r *http.Request, name string) {
	if name == "" {
		return
	}
	canonical := textproto.CanonicalMIMEHeaderKey(name)
	for k := range r.Header {
		if strings.EqualFold(k, canonical) || strings.EqualFold(k, name) {
			delete(r.Header, k)
		}
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
