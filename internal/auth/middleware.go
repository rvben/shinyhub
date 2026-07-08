package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type contextKey string

const (
	userContextKey  contextKey = "user"
	tokenContextKey contextKey = "token"
)

const SessionCookieName = "shiny_session"

type ContextUser struct {
	ID       int64
	Username string
	Role     string
	// DisplayName is the user's friendly name. Populated for forward-auth
	// requests (from the resolved DB user) so the middleware can skip a write
	// when the IdP name header is unchanged; empty on JWT/API-key paths, which
	// do not need it.
	DisplayName string
	// Email is the user's email address. On forward-auth requests it comes from
	// the upstream IdP email header; on native session (JWT) and API-key requests
	// it comes from the persisted DB user, set from the provider on SSO login. It
	// is forwarded to apps as X-Shinyhub-Email and the identity token's email
	// claim. Empty for local username/password accounts and when no upstream
	// email is available.
	Email string
	// AppScope, when non-empty, restricts this identity to the listed app
	// slugs across every app surface, regardless of role. Set on the deploy
	// token identity from auth.deploy_token_apps; nil for every normal user.
	AppScope []string
	// TokenEpoch is the user's live session-revocation counter (users.token_epoch).
	// JWT validation rejects tokens whose embedded epoch differs.
	TokenEpoch int64
}

// AppInScope reports whether this identity may touch the app named by slug.
// An identity with no AppScope is unrestricted; a scoped identity is limited
// to its allowlist even when its role would otherwise grant broader access.
func (u *ContextUser) AppInScope(slug string) bool {
	if u == nil || len(u.AppScope) == 0 {
		return true
	}
	for _, s := range u.AppScope {
		if s == slug {
			return true
		}
	}
	return false
}

// TokenInfo describes the JWT the current request was authenticated with.
// It is only set when auth succeeded via a JWT (Bearer or session cookie);
// API-key authenticated requests leave it unset. Handlers use this to revoke
// the caller's own token on logout.
type TokenInfo struct {
	JTI       string
	ExpiresAt time.Time
	// AuthTime is the original login time (auth_time claim), zero for a legacy
	// token. It bounds the absolute session lifetime across sliding renewals.
	AuthTime time.Time
}

// APIKeyLookup looks up a user by API key hash. Injected to avoid import cycles.
type APIKeyLookup func(keyHash string) (*ContextUser, error)

// UserLookup looks up a user by ID so JWT-authenticated requests can be
// revalidated against the live database on every request. Returning a
// non-nil error (e.g. db.ErrNotFound for a deleted user) rejects the
// request as unauthorized. Production wiring MUST supply this — without it
// a stale JWT keeps its original role until it expires, which means a role
// downgrade or user deletion does not take effect immediately.
type UserLookup func(userID int64) (*ContextUser, error)

// authResult carries the authenticated identity plus (for JWT paths) the token
// metadata needed by the logout handler to revoke the current session.
type authResult struct {
	User  *ContextUser
	Token *TokenInfo
}

func userFromClaims(c *Claims) *ContextUser {
	return &ContextUser{ID: c.UserID, Username: c.Subject, Role: c.Role}
}

func tokenFromClaims(c *Claims) *TokenInfo {
	info := &TokenInfo{JTI: c.ID}
	if c.ExpiresAt != nil {
		info.ExpiresAt = c.ExpiresAt.Time
	}
	if c.AuthTime != nil {
		info.AuthTime = c.AuthTime.Time
	}
	return info
}

// resolveJWTUser turns validated claims into a ContextUser. When userLookup
// is supplied, the live DB record wins over what the token was issued with;
// this is what makes role demotions and user deletions take effect without
// waiting for the JWT to expire. It also enforces session revocation: a token
// whose embedded epoch no longer matches the user's live token_epoch (bumped
// by admin revoke-sessions or a password change) is rejected. With no lookup
// we fall back to the claim values (used by tests that want to skip DB
// plumbing).
func resolveJWTUser(claims *Claims, userLookup UserLookup) (*ContextUser, error) {
	if userLookup == nil {
		return userFromClaims(claims), nil
	}
	u, err := userLookup(claims.UserID)
	if err != nil {
		return nil, err
	}
	if u != nil && u.TokenEpoch != claims.SessionEpoch {
		return nil, ErrTokenRevoked
	}
	return u, nil
}

func authenticateHeader(header, secret string, keyLookup APIKeyLookup, userLookup UserLookup, revoked RevocationChecker) (*authResult, error) {
	if header == "" {
		return nil, nil
	}

	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid authorization header")
	}
	scheme, token := parts[0], parts[1]

	switch strings.ToLower(scheme) {
	case "bearer":
		claims, err := ValidateJWT(token, secret, revoked)
		if err != nil {
			return nil, err
		}
		user, err := resolveJWTUser(claims, userLookup)
		if err != nil {
			return nil, err
		}
		return &authResult{User: user, Token: tokenFromClaims(claims)}, nil
	case "token":
		if keyLookup == nil {
			return nil, fmt.Errorf("api key lookup unavailable")
		}
		user, err := keyLookup(HashAPIKey(token))
		if err != nil {
			return nil, err
		}
		return &authResult{User: user}, nil
	default:
		return nil, fmt.Errorf("unsupported authorization scheme")
	}
}

func authenticateSessionCookie(r *http.Request, secret string, userLookup UserLookup, revoked RevocationChecker) (*authResult, error) {
	c, err := r.Cookie(SessionCookieName)
	if err != nil {
		return nil, err
	}
	claims, err := ValidateJWT(c.Value, secret, revoked)
	if err != nil {
		return nil, err
	}
	user, err := resolveJWTUser(claims, userLookup)
	if err != nil {
		return nil, err
	}
	return &authResult{User: user, Token: tokenFromClaims(claims)}, nil
}

// AuthenticateBrowserSession authenticates a request strictly from the
// session cookie, ignoring any Authorization header. Used by middleware
// that fronts user-facing /app/* routes: Shiny apps regularly forward
// their own Authorization headers to their own backend through that
// path, and routing those headers into ShinyHub's JWT validator would
// reject perfectly valid browser sessions with a spurious 401.
func AuthenticateBrowserSession(r *http.Request, secret string, userLookup UserLookup, revoked RevocationChecker) (*ContextUser, *TokenInfo, error) {
	res, err := authenticateSessionCookie(r, secret, userLookup, revoked)
	if err != nil || res == nil {
		return nil, nil, err
	}
	return res.User, res.Token, nil
}

// AuthenticateRequest authenticates a request from either an Authorization
// header or the browser session cookie. If an Authorization header is present,
// it takes precedence over the cookie and must be valid. JWT paths
// revalidate the user against userLookup when supplied so role changes and
// account deletions take effect without waiting for the token to expire.
func AuthenticateRequest(r *http.Request, secret string, keyLookup APIKeyLookup, userLookup UserLookup, revoked RevocationChecker) (*ContextUser, *TokenInfo, error) {
	var (
		res *authResult
		err error
	)
	if header := r.Header.Get("Authorization"); header != "" {
		res, err = authenticateHeader(header, secret, keyLookup, userLookup, revoked)
	} else {
		res, err = authenticateSessionCookie(r, secret, userLookup, revoked)
	}
	if err != nil || res == nil {
		return nil, nil, err
	}
	return res.User, res.Token, nil
}

// BearerMiddleware authenticates the request from an Authorization header or
// the session cookie. The optional RevocationChecker is consulted for JWT
// paths so revoked tokens are rejected. When a JWT is used, userLookup (if
// non-nil) re-resolves the user against the database on every request — this
// is the path that makes role downgrades and account deletions effective
// without waiting for the token to expire. The token's jti and expiry are
// attached to the context via WithTokenInfo so handlers can reference them
// (e.g. to revoke on logout).
func BearerMiddleware(secret string, keyLookup APIKeyLookup, userLookup UserLookup, revoked RevocationChecker) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// An upstream middleware (e.g. forward-auth) may have already authenticated
			// this request and attached the user. Honor it and pass through.
			if UserFromContext(r.Context()) != nil {
				next.ServeHTTP(w, r)
				return
			}

			user, token, err := AuthenticateRequest(r, secret, keyLookup, userLookup, revoked)
			if err != nil || user == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			ctx := WithUser(r.Context(), user)
			if token != nil {
				ctx = context.WithValue(ctx, tokenContextKey, token)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// TokenInfoFromContext returns the JWT metadata attached by BearerMiddleware,
// or nil if the request was authenticated by an API key or from an unauthed
// route.
func TokenInfoFromContext(ctx context.Context) *TokenInfo {
	t, _ := ctx.Value(tokenContextKey).(*TokenInfo)
	return t
}

// WithTokenInfo returns a context annotated with token metadata. Exposed for
// tests that pre-populate the token context without running the middleware.
func WithTokenInfo(ctx context.Context, t *TokenInfo) context.Context {
	return context.WithValue(ctx, tokenContextKey, t)
}

func UserFromContext(ctx context.Context) *ContextUser {
	u, _ := ctx.Value(userContextKey).(*ContextUser)
	return u
}

// Role is a typed role identifier. Exported constants are the only valid values.
type Role string

const (
	RoleViewer    Role = "viewer"
	RoleDeveloper Role = "developer"
	RoleOperator  Role = "operator"
	RoleAdmin     Role = "admin"
)

// roleOrder ranks roles for comparison. Unknown role strings on a user rank 0
// (below every required level) and are rejected.
var roleOrder = map[string]int{
	string(RoleViewer):    1,
	string(RoleDeveloper): 2,
	string(RoleOperator):  3,
	string(RoleAdmin):     4,
}

// IsValidGlobalRole reports whether s names one of the four global roles that
// can be assigned to a user account (viewer, developer, operator, admin).
// Keep the single source of truth in this package so handlers and migrations
// cannot drift from the ranking used by group-to-role mapping. Authorization
// itself is enforced per-handler (requireAdmin and the app gates in
// internal/api/authorization.go), not by router middleware.
func IsValidGlobalRole(s string) bool {
	_, ok := roleOrder[s]
	return ok
}

// WithUser returns a copy of ctx with the given ContextUser attached.
// Used in tests and handlers that pre-populate context.
func WithUser(ctx context.Context, u *ContextUser) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}
