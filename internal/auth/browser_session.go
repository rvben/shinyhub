package auth

import (
	"errors"
	"time"
)

var ErrSessionExpired = errors.New("session expired")

// IssueBrowserSession preserves the login time across renewals and clamps the
// signed expiry to the absolute deadline. A zero authTime starts a new session
// (including migration of a legacy token without an auth_time claim). Renewal
// passes the authenticated JTI as sessionID to retain the revocation identity.
func IssueBrowserSession(u *ContextUser, secret string, authTime time.Time, ttl, maxAge time.Duration, sessionID ...string) (string, *TokenInfo, error) {
	if u.SupportSession != nil {
		return "", nil, ErrSupportSessionScope
	}
	now := time.Now()
	if authTime.IsZero() {
		authTime = now
	}
	expiresAt := now.Add(ttl)
	if deadline := authTime.Add(maxAge); deadline.Before(expiresAt) {
		expiresAt = deadline
	}
	// JWT NumericDate has second precision; cookies and API metadata must agree.
	expiresAt = expiresAt.Truncate(time.Second)
	if ttl <= 0 || maxAge <= 0 || !expiresAt.After(now) {
		return "", nil, ErrSessionExpired
	}
	// Preserve the session identity across renewals. Logout can then revoke all
	// versions, including a renewal response already in flight in another tab.
	jti := ""
	if len(sessionID) > 0 {
		jti = sessionID[0]
	}
	return issueUserJWTAtWithID(u, secret, authTime, expiresAt, jti)
}
