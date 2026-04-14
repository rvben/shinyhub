package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type contextKey string

const userContextKey contextKey = "user"

type ContextUser struct {
	ID       int64
	Username string
	Role     string
}

// APIKeyLookup looks up a user by API key hash. Injected to avoid import cycles.
type APIKeyLookup func(keyHash string) (*ContextUser, error)

func BearerMiddleware(secret string, keyLookup APIKeyLookup) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if header == "" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			parts := strings.SplitN(header, " ", 2)
			if len(parts) != 2 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			scheme, token := parts[0], parts[1]

			var user *ContextUser
			switch strings.ToLower(scheme) {
			case "bearer":
				claims, err := ValidateJWT(token, secret)
				if err != nil {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				user = &ContextUser{ID: claims.UserID, Username: claims.Username, Role: claims.Role}
			case "token":
				if keyLookup == nil {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				u, err := keyLookup(HashAPIKey(token))
				if err != nil {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
				user = u
			default:
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), userContextKey, user)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func UserFromContext(ctx context.Context) *ContextUser {
	u, _ := ctx.Value(userContextKey).(*ContextUser)
	return u
}

// RequireRole enforces a minimum role level. Roles are ordered:
// viewer < developer < operator < admin.
// Panics at startup if an unknown role name is passed.
func RequireRole(role string) func(http.Handler) http.Handler {
	order := map[string]int{"viewer": 1, "developer": 2, "operator": 3, "admin": 4}
	required, ok := order[role]
	if !ok {
		panic(fmt.Sprintf("auth.RequireRole: unknown role %q", role))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u := UserFromContext(r.Context())
			if u == nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			actual, ok := order[u.Role]
			if !ok || actual < required {
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
