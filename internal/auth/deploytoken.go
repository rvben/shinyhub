package auth

import (
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
)

// DeployToken is a pre-shared, env-sourced bearer credential that authenticates
// as a fixed synthetic user. The raw token never leaves this struct; callers
// match against its SHA-256 hash (the same hash the DB-backed api_keys path
// uses) so the existing Token auth scheme continues to work unchanged.
type DeployToken struct {
	hash string // hex-encoded sha256 of the raw token
	user *ContextUser
}

// NewDeployToken wraps raw in a DeployToken bound to user. The raw value is
// hashed immediately and discarded; only the hash is retained.
func NewDeployToken(raw string, user *ContextUser) *DeployToken {
	return &DeployToken{hash: HashAPIKey(raw), user: user}
}

// Matches reports whether candidateHash equals the configured token's hash, in
// constant time. candidateHash is the hex SHA-256 of the request's bearer
// value, as produced by HashAPIKey.
func (d *DeployToken) Matches(candidateHash string) bool {
	if d == nil || d.hash == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(d.hash), []byte(candidateHash)) == 1
}

// User returns a fresh copy of the synthetic user this token authenticates as.
// Callers must not mutate the returned struct.
func (d *DeployToken) User() *ContextUser {
	if d == nil || d.user == nil {
		return nil
	}
	u := *d.user
	return &u
}

const (
	deployTokenPrefix    = "shk_"
	deployTokenMinHexLen = 32 // 16 bytes minimum entropy
)

// ValidateDeployTokenFormat enforces a minimum entropy and the shk_ prefix on
// SHINYHUB_DEPLOY_TOKEN at startup. Operators who supply a weak value get a
// clear refusal-to-boot instead of a silently insecure deployment.
func ValidateDeployTokenFormat(raw string) error {
	if raw == "" {
		return fmt.Errorf("deploy token is empty")
	}
	if !strings.HasPrefix(raw, deployTokenPrefix) {
		return fmt.Errorf("deploy token must start with %q", deployTokenPrefix)
	}
	body := strings.TrimPrefix(raw, deployTokenPrefix)
	if len(body) < deployTokenMinHexLen {
		return fmt.Errorf("deploy token body must be at least %d hex chars (got %d); generate one with: openssl rand -hex 32",
			deployTokenMinHexLen, len(body))
	}
	if _, err := hex.DecodeString(body); err != nil {
		return fmt.Errorf("deploy token body must be hex-encoded: %w", err)
	}
	return nil
}
