package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

const jwtExpiry = 1 * time.Hour

// ErrTokenRevoked is returned by ValidateJWT when the caller's RevocationChecker
// reports that the token's jti has been revoked.
var ErrTokenRevoked = errors.New("token revoked")

// RevocationChecker reports whether a JWT's jti has been revoked. Returning an
// error causes validation to fail closed — we'd rather reject a valid token on
// DB hiccups than accept a revoked one.
// A nil checker disables revocation (useful in tests and in layers that don't
// have store access).
type RevocationChecker func(jti string) (bool, error)

type Claims struct {
	UserID int64  `json:"uid"`
	Role   string `json:"role"`
	// AuthTime is the original login time. Unlike IssuedAt/ExpiresAt it is NOT
	// reset by a sliding renewal, so it bounds the absolute session lifetime.
	AuthTime *jwt.NumericDate `json:"auth_time,omitempty"`
	// SessionEpoch is the user's token_epoch at issuance. Validation rejects
	// the token when the live epoch differs (admin revoke-sessions or a
	// password change bumped it). Legacy tokens carry 0, matching the column
	// default, so an upgrade logs nobody out.
	SessionEpoch int64 `json:"sess_epoch,omitempty"`
	jwt.RegisteredClaims
}

// AbsoluteSessionMaxAge caps how long a session may be kept alive by sliding
// renewals from its original login. Past this the session is not renewed and
// expires within jwtExpiry, forcing a fresh login - which re-runs SSO group
// reconciliation, so a user removed from an authorizing group loses access
// within this window instead of indefinitely.
const AbsoluteSessionMaxAge = 12 * time.Hour

// CanSlideSession reports whether a session that originally logged in at
// authTime may still be renewed. A zero authTime (a legacy token issued before
// the auth_time claim existed) is renewable so an upgrade does not log everyone
// out; its clock starts at the next renewal.
func CanSlideSession(authTime time.Time) bool {
	return authTime.IsZero() || time.Since(authTime) < AbsoluteSessionMaxAge
}

// bcryptCost is the password-hashing work factor. 12 is the OWASP-recommended
// minimum for current hardware; existing lower-cost hashes still verify since
// bcrypt self-describes its cost in the hash.
const bcryptCost = 12

func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func VerifyPassword(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

// newJTI returns a random 128-bit hex string for the jti claim.
func newJTI() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("jti: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// IssueSessionToken issues a fresh session token for a new login, embedding
// the user's current token epoch so a later revoke-sessions/password-change
// bump invalidates it. Production login paths MUST use this (or
// SlideSessionToken) rather than IssueJWT: a token issued without the user's
// live epoch is rejected on the first request after any bump.
func IssueSessionToken(u *ContextUser, secret string) (string, error) {
	return issueJWT(u.ID, u.Username, u.Role, u.TokenEpoch, secret, time.Now())
}

// SlideSessionToken re-issues a session token during a sliding renewal,
// preserving the original auth_time (so renewals cannot extend the absolute
// session lifetime) while embedding the user's current token epoch.
func SlideSessionToken(u *ContextUser, secret string, authTime time.Time) (string, error) {
	return issueJWT(u.ID, u.Username, u.Role, u.TokenEpoch, secret, authTime)
}

// IssueJWT issues a session token at epoch 0, stamping auth_time to now. Test
// helper: valid for users whose epoch was never bumped. Production sessions go
// through IssueSessionToken/SlideSessionToken, which carry the live epoch.
func IssueJWT(userID int64, username, role, secret string) (string, error) {
	return issueJWT(userID, username, role, 0, secret, time.Now())
}

// IssueJWTAt issues an epoch-0 token whose auth_time is authTime. Test helper;
// see IssueJWT.
func IssueJWTAt(userID int64, username, role, secret string, authTime time.Time) (string, error) {
	return issueJWT(userID, username, role, 0, secret, authTime)
}

// issueJWT mints the session token. IssuedAt/ExpiresAt/NotBefore always slide
// to now; auth_time and the session epoch are the caller's responsibility.
func issueJWT(userID int64, username, role string, epoch int64, secret string, authTime time.Time) (string, error) {
	jti, err := newJTI()
	if err != nil {
		return "", err
	}
	now := time.Now()
	claims := Claims{
		UserID:       userID,
		Role:         role,
		AuthTime:     jwt.NewNumericDate(authTime),
		SessionEpoch: epoch,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   username,
			ExpiresAt: jwt.NewNumericDate(now.Add(jwtExpiry)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// ValidateJWT parses the signed token and, if revoked is non-nil, also checks
// the jti against the revocation list. Returns ErrTokenRevoked when the token
// is on the list so callers can distinguish signature failures from logout.
func ValidateJWT(tokenStr, secret string, revoked RevocationChecker) (*Claims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token")
	}
	if revoked != nil {
		isRevoked, err := revoked(claims.ID)
		if err != nil {
			return nil, fmt.Errorf("revocation check: %w", err)
		}
		if isRevoked {
			return nil, ErrTokenRevoked
		}
	}
	return claims, nil
}

// ParseJWT validates a token and returns the embedded user as a ContextUser.
// Passing a nil RevocationChecker disables the revocation check.
func ParseJWT(tokenStr, secret string, revoked RevocationChecker) (*ContextUser, error) {
	claims, err := ValidateJWT(tokenStr, secret, revoked)
	if err != nil {
		return nil, err
	}
	return &ContextUser{
		ID:       claims.UserID,
		Username: claims.Subject,
		Role:     claims.Role,
	}, nil
}

func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", sum)
}
