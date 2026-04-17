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
	jwt.RegisteredClaims
}

func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
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

func IssueJWT(userID int64, username, role, secret string) (string, error) {
	jti, err := newJTI()
	if err != nil {
		return "", err
	}
	claims := Claims{
		UserID: userID,
		Role:   role,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        jti,
			Subject:   username,
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(jwtExpiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
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
