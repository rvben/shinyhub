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
}

// TokenInfo describes the JWT the current request was authenticated with.
// It is only set when auth succeeded via a JWT (Bearer or session cookie);
// API-key authenticated requests leave it unset. Handlers use this to revoke
// the caller's own token on logout.
type TokenInfo struct {
	JTI       string
	ExpiresAt time.Time
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
	return info
}

// resolveJWTUser turns validated claims into a ContextUser. When userLookup
// is supplied, the live DB record wins over what the token was issued with;
// this is what makes role demotions and user deletions take effect without
// waiting for the JWT to expire. With no lookup we fall back to the claim
// values (used by tests that want to skip DB plumbing).
func resolveJWTUser(claims *Claims, userLookup UserLookup) (*ContextUser, error) {
	if userLookup == nil {
		return userFromClaims(claims), nil
	}
	return userLookup(claims.UserID)
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
			user, token, err := AuthenticateRequest(r, secret, keyLookup, userLookup, revoked)
			if err != nil || user == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), userContextKey, user)
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
// cannot drift from the hierarchy used by RequireRole.
func IsValidGlobalRole(s string) bool {
	_, ok := roleOrder[s]
	return ok
}

// RequireRole enforces a minimum role level. Roles are ordered:
// viewer < developer < operator < admin.
func RequireRole(role Role) func(http.Handler) http.Handler {
	required := roleOrder[string(role)]
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := UserFromContext(r.Context())
			if u == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			actual := roleOrder[u.Role]
			if actual == 0 || actual < required {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// WithUser returns a copy of ctx with the given ContextUser attached.
// Used in tests and handlers that pre-populate context.
func WithUser(ctx context.Context, u *ContextUser) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}
