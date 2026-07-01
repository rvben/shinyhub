// Package identity derives per-app identity keys, sanitizes group lists for
// header transport, and mints the signed identity token the proxy forwards
// to app processes. See docs/identity.md for the trust model.
package identity

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/rvben/shinyhub/internal/secrets"
)

// Header names injected by the proxy. The X-Shinyhub- prefix is stripped from
// every inbound request before injection, so apps can trust these arrived
// from the proxy (and verify the token for anything security-sensitive).
const (
	HeaderUser            = "X-Shinyhub-User"
	HeaderUserID          = "X-Shinyhub-User-Id"
	HeaderRole            = "X-Shinyhub-Role"
	HeaderEmail           = "X-Shinyhub-Email"
	HeaderGroups          = "X-Shinyhub-Groups"
	HeaderGroupsTruncated = "X-Shinyhub-Groups-Truncated"
	HeaderToken           = "X-Shinyhub-Identity-Token"

	// HeaderPrefix is the reserved platform prefix: every inbound request
	// header matching it is deleted unconditionally before forwarding.
	HeaderPrefix = "X-Shinyhub-"
)

// Issuer is the iss claim of every identity token.
const Issuer = "shinyhub"

// MaxGroups caps the group list in both the plain header and the JWT claim,
// bounding per-request header size (enterprise users can carry hundreds of
// groups; unbounded forwarding blows typical 8-16 KB backend header limits).
const MaxGroups = 100

// TokenTTL bounds replay of a captured token. Apps verify exp with leeway.
const TokenTTL = 5 * time.Minute

// keyInfoPrefix versions the HKDF derivation so a future algorithm change
// can coexist. The app's numeric ID (not the slug) scopes the key: a
// deleted-and-recreated app under the same slug must NOT inherit its
// predecessor's key, and a future slug rename must not rotate it.
const keyInfoPrefix = "shinyhub-identity-v1:"

// DeriveKey derives the 32-byte per-app HMAC key from the auth secret.
// The derivation cannot fail in practice; the underlying helper panics if it ever does.
func DeriveKey(authSecret string, appID int64) []byte {
	return secrets.DeriveKeyWithInfo(authSecret, keyInfoPrefix+strconv.FormatInt(appID, 10))
}

// SanitizeGroups prepares a group list for transport: sorts (deterministic
// cap), caps at MaxGroups, and joins for the plain header with comma-bearing
// names OMITTED (a group named "team,admins" must not forge membership for
// apps that split the header). The JWT claim slice keeps comma-bearing names.
// Returns (headerValue, claimGroups, truncated).
func SanitizeGroups(groups []string) (string, []string, bool) {
	// copy to avoid mutating the caller's slice
	sorted := append([]string(nil), groups...)
	sort.Strings(sorted)
	truncated := false
	if len(sorted) > MaxGroups {
		sorted = sorted[:MaxGroups]
		truncated = true
	}
	headerParts := make([]string, 0, len(sorted))
	for _, g := range sorted {
		if strings.Contains(g, ",") {
			continue
		}
		headerParts = append(headerParts, g)
	}
	return strings.Join(headerParts, ","), sorted, truncated
}

// TokenClaims is the identity token payload. Apps verify iss, aud (their own
// slug, injected as SHINYHUB_APP_SLUG), signature, and exp with ~30 s leeway.
type TokenClaims struct {
	Role              string   `json:"role"`
	Email             string   `json:"email,omitempty"`
	Groups            []string `json:"groups"`
	GroupsTruncated   bool     `json:"groups_truncated,omitempty"`
	PreferredUsername string   `json:"preferred_username"`
	jwt.RegisteredClaims
}

// TokenParams carries everything MintToken stamps into the claims.
type TokenParams struct {
	UserID          int64
	Username        string
	Role            string
	Email           string   // empty when the upstream IdP provided none
	Groups          []string // pre-sanitized claim slice from SanitizeGroups
	GroupsTruncated bool
	Slug            string // becomes aud
}

// MintToken signs a short-lived HS256 identity token with the app's key.
func MintToken(key []byte, p TokenParams) (string, error) {
	now := time.Now()
	claims := TokenClaims{
		Role:              p.Role,
		Email:             p.Email,
		Groups:            p.Groups,
		GroupsTruncated:   p.GroupsTruncated,
		PreferredUsername: p.Username,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    Issuer,
			Subject:   strconv.FormatInt(p.UserID, 10),
			Audience:  jwt.ClaimStrings{p.Slug},
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(TokenTTL)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
}
